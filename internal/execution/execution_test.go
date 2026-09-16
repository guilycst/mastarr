package execution

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/guilycst/mastarr/internal/domain"
	"github.com/guilycst/mastarr/internal/storage"
	"github.com/guilycst/mastarr/internal/storage/sqlc"
)

const executionFixtureTime = "2026-09-14T12:00:00Z"

func TestRunOnceAlreadySatisfiedSkipsDispatch(t *testing.T) {
	journal, action := newMemoryAction("already-satisfied", domain.ActionFSCopy, domain.ActionQueued)
	handler := &scriptedHandler{
		kind: domain.ActionFSCopy,
		observeFn: func(_ context.Context, _ Action, _ int) (Observation, error) {
			return Observation{State: ObserveSatisfied, Evidence: []string{"destination_digest_matches"}}, nil
		},
	}
	clockValue := executionTime()
	clock := func() time.Time { return clockValue }
	executor := newTestExecutor(t, journal, handler, clock)

	batch, err := executor.RunOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if batch.Considered != 1 || len(batch.Results) != 1 {
		t.Fatalf("batch = %+v, want one considered result", batch)
	}
	result := batch.Results[0]
	if result.State != domain.ActionSucceeded || result.Outcome != domain.OutcomeAlreadySatisfied {
		t.Fatalf("result = %+v, want already-satisfied success", result)
	}
	if handler.dispatchCalls() != 0 {
		t.Fatalf("dispatch calls = %d, want zero", handler.dispatchCalls())
	}
	current := mustAction(t, journal, action.ID)
	if current.State != domain.ActionSucceeded || current.Version != 3 {
		t.Fatalf("persisted action = %+v, want succeeded at version 3", current)
	}
	attempts := mustAttempts(t, journal, action.ID)
	if len(attempts) != 1 || attempts[0].Phase != AttemptObserve || attempts[0].State != domain.AttemptSucceeded {
		t.Fatalf("attempts = %+v, want one successful observe", attempts)
	}
	if effects := mustEffects(t, journal, action.ID); len(effects) != 0 {
		t.Fatalf("effects = %+v, want no effects for an empty already-satisfied observation", effects)
	}
}

func TestDispatchRecordsOrderedAttemptsAndReadBack(t *testing.T) {
	journal, action := newMemoryAction("dispatch-success", domain.ActionFSCopy, domain.ActionQueued)
	effect := executionEffect("copy", "payload.bin")
	handler := &scriptedHandler{
		kind: domain.ActionFSCopy,
		observeFn: func(_ context.Context, _ Action, call int) (Observation, error) {
			if call == 1 {
				return Observation{State: ObserveNeedsAction, Evidence: []string{"destination_missing"}, Effects: []Effect{effect}}, nil
			}
			return Observation{State: ObserveSatisfied, Evidence: []string{"destination_digest_matches"}, Effects: []Effect{effect}}, nil
		},
		dispatchFn: func(_ context.Context, _ Action, _ Attempt) (DispatchResult, error) {
			return DispatchResult{Accepted: true, Outcome: domain.OutcomeApplied, ExternalID: "copy-effect"}, nil
		},
	}
	executor := newTestExecutor(t, journal, handler, executionClock())

	batch, err := executor.RunOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(batch.Results) != 1 {
		t.Fatalf("results = %+v, want one result", batch.Results)
	}
	result := batch.Results[0]
	if result.State != domain.ActionSucceeded || result.Outcome != domain.OutcomeApplied || !result.Dispatched {
		t.Fatalf("result = %+v, want applied dispatched success", result)
	}
	if got := handler.eventsSnapshot(); !equalStrings(got, []string{"observe", "dispatch", "observe"}) {
		t.Fatalf("handler order = %v, want observe, dispatch, observe", got)
	}
	attempts := mustAttempts(t, journal, action.ID)
	if len(attempts) != 2 {
		t.Fatalf("attempt count = %d, want observe and dispatch", len(attempts))
	}
	if attempts[0].Phase != AttemptObserve || attempts[0].State != domain.AttemptSucceeded || attempts[1].Phase != AttemptDispatch || attempts[1].State != domain.AttemptSucceeded {
		t.Fatalf("ordered attempts = %+v", attempts)
	}
	effects := mustEffects(t, journal, action.ID)
	if len(effects) != 1 || effects[0].State != EffectApplied || effects[0].AttemptID != attempts[1].ID {
		t.Fatalf("effects = %+v, want one applied effect bound to dispatch", effects)
	}
	if journal.transactionCalls() < 2 {
		t.Fatalf("transaction calls = %d, want prepare and effect transactions", journal.transactionCalls())
	}
}

func TestLostDispatchResponseReconcilesBeforeRetry(t *testing.T) {
	journal, action := newMemoryAction("uncertain-dispatch", domain.ActionFSCopy, domain.ActionQueued)
	effect := executionEffect("copy", "payload.bin")
	handler := &scriptedHandler{
		kind: domain.ActionFSCopy,
		observeFn: func(_ context.Context, _ Action, _ int) (Observation, error) {
			return Observation{State: ObserveNeedsAction, Effects: []Effect{effect}}, nil
		},
		dispatchFn: func(_ context.Context, _ Action, _ Attempt) (DispatchResult, error) {
			return DispatchResult{}, NewDispatchedFailure(FailureUncertain, errors.New("connection lost after write"))
		},
		reconcileFn: func(_ context.Context, _ Action, _ Attempt) (ReconcileResult, error) {
			return ReconcileResult{Outcome: domain.OutcomeApplied, Evidence: []string{"read_back_applied"}, Effects: []Effect{effect}}, nil
		},
	}
	executor := newTestExecutor(t, journal, handler, executionClock())

	first := mustRunOnce(t, executor)
	if len(first.Results) != 1 || first.Results[0].State != domain.ActionReconciling {
		t.Fatalf("first result = %+v, want reconciling", first.Results)
	}
	if handler.dispatchCalls() != 1 || handler.reconcileCalls() != 0 {
		t.Fatalf("calls after uncertain dispatch = dispatch %d reconcile %d", handler.dispatchCalls(), handler.reconcileCalls())
	}
	current := mustAction(t, journal, action.ID)
	if current.State != domain.ActionReconciling || current.UnresolvedCount != 1 {
		t.Fatalf("uncertain action = %+v, want unresolved reconciling", current)
	}
	clockValue := executionTime().Add(10 * time.Second)
	clock := func() time.Time { return clockValue }
	executor = newTestExecutor(t, journal, handler, clock)

	second := mustRunOnce(t, executor)
	if len(second.Results) != 1 || second.Results[0].State != domain.ActionSucceeded || second.Results[0].Outcome != domain.OutcomeApplied {
		t.Fatalf("reconciliation result = %+v, want applied success", second.Results)
	}
	if handler.dispatchCalls() != 1 || handler.reconcileCalls() != 1 {
		t.Fatalf("calls after reconciliation = dispatch %d reconcile %d, want 1/1", handler.dispatchCalls(), handler.reconcileCalls())
	}
	if got := handler.eventsSnapshot(); !equalStrings(got, []string{"observe", "dispatch", "reconcile"}) {
		t.Fatalf("handler order = %v, want observe, dispatch, reconcile", got)
	}
	if effects := mustEffects(t, journal, action.ID); len(effects) != 1 || effects[0].State != EffectApplied {
		t.Fatalf("reconciled effects = %+v, want applied evidence", effects)
	}
	if current := mustAction(t, journal, action.ID); current.UnresolvedCount != 0 {
		t.Fatalf("reconciled action unresolved count = %d, want zero", current.UnresolvedCount)
	}
}

func TestReconciliationMustProveNoEffectBeforeMutationRetry(t *testing.T) {
	journal, _ := newMemoryAction("safe-retry", domain.ActionFSCopy, domain.ActionQueued)
	effect := executionEffect("copy", "payload.bin")
	handler := &scriptedHandler{
		kind: domain.ActionFSCopy,
		observeFn: func(_ context.Context, _ Action, _ int) (Observation, error) {
			return Observation{State: ObserveNeedsAction, Effects: []Effect{effect}}, nil
		},
		dispatchFn: func(_ context.Context, _ Action, _ Attempt) (DispatchResult, error) {
			return DispatchResult{}, NewDispatchedFailure(FailureUncertain, errors.New("write response lost"))
		},
		reconcileFn: func(_ context.Context, _ Action, _ Attempt) (ReconcileResult, error) {
			return ReconcileResult{SafeToRetry: true, Evidence: []string{"target_absent"}, Effects: []Effect{effect}}, nil
		},
	}
	executor := newTestExecutor(t, journal, handler, executionClock())

	first := mustRunOnce(t, executor)
	if len(first.Results) != 1 || first.Results[0].State != domain.ActionReconciling {
		t.Fatalf("uncertain result = %+v", first.Results)
	}
	clockValue := executionTime().Add(10 * time.Second)
	clock := func() time.Time { return clockValue }
	// Rebuild the executor with the advanced clock only after the first
	// uncertain result has been persisted; reconciliation is backoff-gated.
	executor = newTestExecutor(t, journal, handler, clock)
	second := mustRunOnce(t, executor)
	if len(second.Results) != 1 || second.Results[0].State != domain.ActionQueued {
		t.Fatalf("safe retry result = %+v, want queued", second.Results)
	}
	if handler.dispatchCalls() != 1 {
		t.Fatalf("dispatch calls before due retry = %d, want one", handler.dispatchCalls())
	}
	if got := handler.eventsSnapshot(); !equalStrings(got, []string{"observe", "dispatch", "reconcile"}) {
		t.Fatalf("handler order before retry = %v", got)
	}

	third := mustRunOnce(t, executor)
	if len(third.Results) != 1 || third.Results[0].State != domain.ActionReconciling {
		t.Fatalf("second dispatch result = %+v, want uncertain reconciling", third.Results)
	}
	if handler.dispatchCalls() != 2 {
		t.Fatalf("dispatch calls after proven retry = %d, want two", handler.dispatchCalls())
	}
	if got := handler.eventsSnapshot(); !equalStrings(got, []string{"observe", "dispatch", "reconcile", "observe", "dispatch"}) {
		t.Fatalf("handler order after retry = %v", got)
	}
}

func TestRestartRecoveryNeverBlindlyDispatchesRunningAttempt(t *testing.T) {
	journal, action := newMemoryAction("crash-recovery", domain.ActionFSCopy, domain.ActionRunning)
	action.ClaimedBy = "old-worker"
	action.LeaseUntil = executionFixtureTime
	action.Version = 2
	journal.putAction(action)
	journal.putAttempt(Attempt{
		ID: "crash-recovery-dispatch", ActionRunID: action.ID, AttemptNumber: 1,
		Phase: AttemptDispatch, State: domain.AttemptRunning, StartedAt: executionFixtureTime,
		OutcomeCertainty: CertaintyNotDispatched, Evidence: json.RawMessage(`{}`),
	})
	recoveredEffect := executionEffect("copy", "payload.bin")
	recoveredEffect.AttemptID = "crash-recovery-dispatch"
	recoveredEffect.State = EffectPending
	journal.putEffect(recoveredEffect)
	handler := &scriptedHandler{
		kind: domain.ActionFSCopy,
		reconcileFn: func(_ context.Context, _ Action, _ Attempt) (ReconcileResult, error) {
			return ReconcileResult{Outcome: domain.OutcomeApplied, Effects: []Effect{recoveredEffect}, Evidence: []string{"startup_read_back"}}, nil
		},
	}
	executor := newTestExecutor(t, journal, handler, executionClock())

	report, err := executor.Recover(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if report.Actions != 1 || report.Attempts != 1 {
		t.Fatalf("recovery report = %+v, want one action and attempt", report)
	}
	recovered := mustAction(t, journal, action.ID)
	if recovered.State != domain.ActionReconciling || recovered.ClaimedBy != "" || recovered.LeaseUntil != "" {
		t.Fatalf("recovered action = %+v, want unleased reconciliation", recovered)
	}
	attempts := mustAttempts(t, journal, action.ID)
	if len(attempts) != 1 || attempts[0].State != domain.AttemptReconciling || attempts[0].OutcomeCertainty != CertaintyUncertain {
		t.Fatalf("recovered attempt = %+v, want uncertain reconciliation", attempts)
	}
	result := mustRunOnce(t, executor)
	if len(result.Results) != 1 || result.Results[0].State != domain.ActionSucceeded {
		t.Fatalf("post-recovery result = %+v", result.Results)
	}
	if handler.dispatchCalls() != 0 || handler.reconcileCalls() != 1 {
		t.Fatalf("post-recovery calls = dispatch %d reconcile %d", handler.dispatchCalls(), handler.reconcileCalls())
	}
}

func TestDependencyBackoffPersistsAcrossPollingAndRestart(t *testing.T) {
	journal, action := newMemoryAction("dependency-backoff", domain.ActionFSCopy, domain.ActionQueued)
	clock := executionClock()
	var observations int
	handler := &scriptedHandler{
		kind: domain.ActionFSCopy,
		observeFn: func(_ context.Context, _ Action, _ int) (Observation, error) {
			observations++
			if observations == 1 {
				return Observation{}, NewFailure(FailureDependency, errors.New("upstream unavailable"))
			}
			return Observation{State: ObserveSatisfied}, nil
		},
	}
	executor := newTestExecutor(t, journal, handler, clock)
	first := mustRunOnce(t, executor)
	if len(first.Results) != 1 || first.Results[0].State != domain.ActionWaitingDependency {
		t.Fatalf("dependency result = %+v, want waiting", first.Results)
	}
	waiting := mustAction(t, journal, action.ID)
	if waiting.State != domain.ActionWaitingDependency {
		t.Fatalf("waiting action = %+v", waiting)
	}
	next, err := parseTime(waiting.NextAttemptAt)
	if err != nil || !next.Equal(clock().Add(5*time.Second)) {
		t.Fatalf("next attempt = %q (%v), want five second persisted backoff", waiting.NextAttemptAt, err)
	}
	beforeDue := mustRunOnce(t, executor)
	if beforeDue.Considered != 0 || len(beforeDue.Results) != 0 || observations != 1 {
		t.Fatalf("before due batch = %+v observations=%d", beforeDue, observations)
	}
	clockNow := clock().Add(5 * time.Second)
	clock = func() time.Time { return clockNow }
	// Construct a second executor over the same journal to prove the delay is
	// read from the durable action row, rather than held in worker memory.
	restarted := newTestExecutor(t, journal, handler, clock)
	second := mustRunOnce(t, restarted)
	if len(second.Results) != 1 || second.Results[0].State != domain.ActionSucceeded {
		t.Fatalf("post-backoff result = %+v", second.Results)
	}
	if observations != 2 {
		t.Fatalf("observations = %d, want one failed and one retry", observations)
	}
}

func TestCancellationBeforeDispatchHasNoMutationCall(t *testing.T) {
	journal, action := newMemoryAction("cancel-before-dispatch", domain.ActionFSCopy, domain.ActionQueued)
	handler := &scriptedHandler{kind: domain.ActionFSCopy}
	handler.observeFn = func(_ context.Context, _ Action, call int) (Observation, error) {
		if call == 1 {
			if _, err := journal.RequestCancellation(context.Background(), action.ID, executionFixtureTime); err != nil {
				return Observation{}, err
			}
			return Observation{State: ObserveNeedsAction, Effects: []Effect{executionEffect("copy", "payload.bin")}}, nil
		}
		return Observation{State: ObserveSatisfied}, nil
	}
	handler.dispatchFn = func(_ context.Context, _ Action, _ Attempt) (DispatchResult, error) {
		t.Fatal("dispatch was called after durable cancellation")
		return DispatchResult{}, nil
	}
	executor := newTestExecutor(t, journal, handler, executionClock())
	batch := mustRunOnce(t, executor)
	if len(batch.Results) != 1 || batch.Results[0].State != domain.ActionCancelled {
		t.Fatalf("cancelled result = %+v, want cancelled", batch.Results)
	}
	current := mustAction(t, journal, action.ID)
	if current.State != domain.ActionCancelled {
		t.Fatalf("persisted action = %+v, want cancelled", current)
	}
	if handler.dispatchCalls() != 0 {
		t.Fatalf("dispatch calls = %d, want zero", handler.dispatchCalls())
	}
	attempts := mustAttempts(t, journal, action.ID)
	if len(attempts) != 2 || attempts[1].Phase != AttemptDispatch || attempts[1].State != domain.AttemptCancelled || attempts[1].OutcomeCertainty != CertaintyNotDispatched {
		t.Fatalf("cancelled attempts = %+v", attempts)
	}
}

func TestCancellationDuringDispatchReportsLateEffect(t *testing.T) {
	journal, action := newMemoryAction("cancel-during-dispatch", domain.ActionFSCopy, domain.ActionQueued)
	handler := &scriptedHandler{kind: domain.ActionFSCopy}
	handler.observeFn = func(_ context.Context, _ Action, call int) (Observation, error) {
		if call == 1 {
			return Observation{State: ObserveNeedsAction, Effects: []Effect{executionEffect("copy", "payload.bin")}}, nil
		}
		return Observation{State: ObserveSatisfied, Effects: []Effect{executionEffect("copy", "payload.bin")}}, nil
	}
	handler.dispatchFn = func(_ context.Context, _ Action, _ Attempt) (DispatchResult, error) {
		if _, err := journal.RequestCancellation(context.Background(), action.ID, executionFixtureTime); err != nil {
			return DispatchResult{}, err
		}
		return DispatchResult{Accepted: true, Outcome: domain.OutcomeApplied}, nil
	}
	executor := newTestExecutor(t, journal, handler, executionClock())
	batch := mustRunOnce(t, executor)
	if len(batch.Results) != 1 || batch.Results[0].State != domain.ActionCancelled {
		t.Fatalf("late cancellation result = %+v, want cancelled", batch.Results)
	}
	current := mustAction(t, journal, action.ID)
	if current.State != domain.ActionCancelled {
		t.Fatalf("persisted action = %+v, want cancelled", current)
	}
	if handler.dispatchCalls() != 1 {
		t.Fatalf("dispatch calls = %d, want one accepted call", handler.dispatchCalls())
	}
	effects := mustEffects(t, journal, action.ID)
	if len(effects) != 1 || effects[0].State != EffectApplied {
		t.Fatalf("late effect evidence = %+v, want applied evidence retained", effects)
	}
}

func TestReservationAndClaimCASPreventOverlap(t *testing.T) {
	journal, first := newMemoryAction("reservation-first", domain.ActionFSCopy, domain.ActionQueued)
	_, second := newMemoryActionOnJournal(journal, "reservation-second", domain.ActionFSCopy, domain.ActionQueued)
	started := make(chan struct{})
	release := make(chan struct{})
	var startOnce sync.Once
	handler := &scriptedHandler{
		kind:        domain.ActionFSCopy,
		reservation: []string{"root:library"},
		observeFn: func(_ context.Context, _ Action, call int) (Observation, error) {
			if call > 1 {
				return Observation{State: ObserveSatisfied, Effects: []Effect{executionEffect("copy", "payload.bin")}}, nil
			}
			return Observation{State: ObserveNeedsAction, Effects: []Effect{executionEffect("copy", "payload.bin")}}, nil
		},
		dispatchFn: func(ctx context.Context, _ Action, _ Attempt) (DispatchResult, error) {
			startOnce.Do(func() { close(started) })
			select {
			case <-release:
				return DispatchResult{Accepted: true, Outcome: domain.OutcomeApplied}, nil
			case <-ctx.Done():
				return DispatchResult{}, ctx.Err()
			}
		},
	}
	// The same handler is registered on both executor instances. Reservations
	// protect work inside a process, while Claim's version CAS protects the
	// durable row if another executor races on the same action.
	executor := newTestExecutor(t, journal, handler, executionClock())
	otherExecutor := newTestExecutor(t, journal, handler, executionClock())
	firstDone := make(chan Result, 1)
	go func() { firstDone <- executor.RunAction(context.Background(), first.ID) }()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("first dispatch did not start")
	}
	secondDone := make(chan Result, 1)
	go func() { secondDone <- executor.RunAction(context.Background(), second.ID) }()
	select {
	case result := <-secondDone:
		if result.State != domain.ActionWaitingDependency {
			t.Fatalf("overlapping result = %+v, want waiting dependency", result)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("overlapping action did not stop at reservation boundary")
	}
	// A second worker trying to claim the first row observes the active lease
	// and must not dispatch a second copy.
	casResult := otherExecutor.RunAction(context.Background(), first.ID)
	if !errors.Is(casResult.Err, ErrLeaseLost) {
		t.Fatalf("second worker result = %+v, want lease-loss CAS rejection", casResult)
	}
	close(release)
	firstResult := <-firstDone
	if firstResult.State != domain.ActionSucceeded {
		t.Fatalf("first result = %+v, want success", firstResult)
	}
	if handler.dispatchCalls() != 1 {
		t.Fatalf("dispatch calls = %d, want one", handler.dispatchCalls())
	}
	if current := mustAction(t, journal, second.ID); current.State != domain.ActionWaitingDependency {
		t.Fatalf("second persisted action = %+v, want waiting", current)
	}
}

func TestSQLJournalRecoveryPersistsAcrossReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "execution.sqlite")
	store, err := storage.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	created := "2026-09-14T11:00:00Z"
	planID, actionID, digest := "sql-plan", "sql-action", "sql-digest"
	if _, err := store.Queries().CreateActionPlan(ctx, &sqlc.CreateActionPlanParams{
		ID: planID, Kind: string(domain.ActionFSCopy), State: "ready", CurrentRevision: 1, CurrentDigest: digest,
		CreatedAt: created, UpdatedAt: created,
	}); err != nil {
		_ = store.Close()
		t.Fatal(err)
	}
	if _, err := store.Queries().CreateActionPlanRevision(ctx, &sqlc.CreateActionPlanRevisionParams{
		PlanID: planID, Revision: 1, Digest: digest, State: "ready", InputJson: `{}`, PreconditionsJson: `{}`, CapabilitiesJson: `[]`, ManifestJson: `[]`, CreatedAt: created, ExpiresAt: "2026-09-20T00:00:00Z",
	}); err != nil {
		_ = store.Close()
		t.Fatal(err)
	}
	if _, err := store.Queries().CreateActionRun(ctx, &sqlc.CreateActionRunParams{
		ID: actionID, PlanID: planID, PlanRevision: 1, PlanDigest: digest, State: string(domain.ActionRunning), DesiredStateJson: `{}`, ClaimedBy: sql.NullString{String: "old-worker", Valid: true}, LeaseUntil: sql.NullString{String: "2026-09-14T11:59:00Z", Valid: true}, Version: 2, OutcomeJson: `{}`, CreatedAt: created, UpdatedAt: created,
	}); err != nil {
		_ = store.Close()
		t.Fatal(err)
	}
	if _, err := store.Queries().CreateActionAttempt(ctx, &sqlc.CreateActionAttemptParams{
		ID: "sql-dispatch-attempt", ActionRunID: actionID, AttemptNumber: 1, Phase: string(AttemptDispatch), State: string(domain.AttemptRunning), StartedAt: created, OutcomeCertainty: string(CertaintyNotDispatched), EvidenceJson: `{}`,
	}); err != nil {
		_ = store.Close()
		t.Fatal(err)
	}
	if _, err := store.Queries().CreateActionEffect(ctx, &sqlc.CreateActionEffectParams{
		ID: "sql-dispatch-effect", ActionRunID: actionID, AttemptID: sql.NullString{String: "sql-dispatch-attempt", Valid: true}, Ordinal: 0,
		TargetKind: "file", TargetID: "payload.bin", EffectKind: "copy", State: string(EffectPending), EvidenceJson: `{}`, ObservedAt: created,
	}); err != nil {
		_ = store.Close()
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := storage.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	journal, err := NewSQLJournal(reopened)
	if err != nil {
		t.Fatal(err)
	}
	handler := &scriptedHandler{
		kind: domain.ActionFSCopy,
		reconcileFn: func(_ context.Context, _ Action, _ Attempt) (ReconcileResult, error) {
			return ReconcileResult{Outcome: domain.OutcomeApplied, Effects: []Effect{executionEffect("copy", "payload.bin")}, Evidence: []string{"reopen_read_back"}}, nil
		},
	}
	executor, err := New(journal, Options{WorkerID: "restarted-worker", Now: executionClock()})
	if err != nil {
		t.Fatal(err)
	}
	if err := executor.RegisterHandler(handler); err != nil {
		t.Fatal(err)
	}
	report, err := executor.Recover(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if report.Actions != 1 || report.Attempts != 1 {
		t.Fatalf("SQL recovery report = %+v", report)
	}
	batch, err := executor.RunOnce(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(batch.Results) != 1 || batch.Results[0].State != domain.ActionSucceeded {
		t.Fatalf("SQL post-recovery batch = %+v", batch)
	}
	row, err := journal.GetAction(ctx, actionID)
	if err != nil {
		t.Fatal(err)
	}
	if row.State != domain.ActionSucceeded || row.ClaimedBy != "" || row.LeaseUntil != "" {
		t.Fatalf("SQL final action = %+v", row)
	}
	if handler.dispatchCalls() != 0 || handler.reconcileCalls() != 1 {
		t.Fatalf("SQL calls = dispatch %d reconcile %d", handler.dispatchCalls(), handler.reconcileCalls())
	}
}

func TestMemoryRecoverExpiredOnlyReclaimsExpiredLeases(t *testing.T) {
	journal, expiredAction := newMemoryAction("expired-lease", domain.ActionFSCopy, domain.ActionRunning)
	expiredAction.ClaimedBy = "old-worker"
	expiredAction.LeaseUntil = "2026-09-14T11:59:00Z"
	expiredAction.Version = 2
	journal.putAction(expiredAction)
	_, activeAction := newMemoryActionOnJournal(journal, "active-lease", domain.ActionFSCopy, domain.ActionRunning)
	activeAction.ClaimedBy = "active-worker"
	activeAction.LeaseUntil = "2026-09-14T12:01:00Z"
	activeAction.Version = 2
	journal.putAction(activeAction)

	recovered, err := journal.RecoverExpired(context.Background(), executionFixtureTime)
	if err != nil {
		t.Fatal(err)
	}
	if len(recovered) != 1 || recovered[0].ID != expiredAction.ID {
		t.Fatalf("recovered = %+v, want only expired lease", recovered)
	}
	if current := mustAction(t, journal, expiredAction.ID); current.State != domain.ActionReconciling || current.ClaimedBy != "" {
		t.Fatalf("expired action = %+v, want unleased reconciliation", current)
	}
	if current := mustAction(t, journal, activeAction.ID); current.State != domain.ActionRunning || current.ClaimedBy != activeAction.ClaimedBy {
		t.Fatalf("active action = %+v, want active lease preserved", current)
	}
}

func TestNewRejectsNonTransactionalJournal(t *testing.T) {
	journal, _ := newMemoryAction("non-transactional", domain.ActionFSCopy, domain.ActionQueued)
	_, err := New(journalWithoutTransactions{Journal: journal}, Options{})
	if !errors.Is(err, ErrInvalidJournal) {
		t.Fatalf("New error = %v, want invalid journal", err)
	}
}

func TestDispatchIntentTransactionRollsBackBeforeHandler(t *testing.T) {
	journal, action := newMemoryAction("dispatch-rollback", domain.ActionFSCopy, domain.ActionQueued)
	journal.failCreateEffect = true
	handler := &scriptedHandler{
		kind: domain.ActionFSCopy,
		observeFn: func(_ context.Context, _ Action, _ int) (Observation, error) {
			return Observation{State: ObserveNeedsAction, Effects: []Effect{executionEffect("copy", "payload.bin")}}, nil
		},
		dispatchFn: func(_ context.Context, _ Action, _ Attempt) (DispatchResult, error) {
			t.Fatal("dispatch called after intent transaction failure")
			return DispatchResult{}, nil
		},
	}
	result := newTestExecutor(t, journal, handler, executionClock()).RunAction(context.Background(), action.ID)
	if result.Err == nil {
		t.Fatal("dispatch rollback result = nil error, want transaction failure")
	}
	if handler.dispatchCalls() != 0 {
		t.Fatalf("dispatch calls = %d, want zero", handler.dispatchCalls())
	}
	if attempts := mustAttempts(t, journal, action.ID); len(attempts) != 1 || attempts[0].Phase != AttemptObserve || attempts[0].State != domain.AttemptRunning {
		t.Fatalf("attempts after rollback = %+v, want only the running observe attempt", attempts)
	}
	if effects := mustEffects(t, journal, action.ID); len(effects) != 0 {
		t.Fatalf("effects after rollback = %+v, want no persisted effects", effects)
	}
}

func TestDurableCancelCancelsInFlightHandler(t *testing.T) {
	journal, action := newMemoryAction("cancel-in-flight", domain.ActionFSCopy, domain.ActionQueued)
	started := make(chan struct{})
	observedCancel := make(chan struct{})
	var startOnce, cancelOnce sync.Once
	effect := executionEffect("copy", "payload.bin")
	handler := &scriptedHandler{
		kind: domain.ActionFSCopy,
		observeFn: func(_ context.Context, _ Action, _ int) (Observation, error) {
			return Observation{State: ObserveNeedsAction, Effects: []Effect{effect}}, nil
		},
		dispatchFn: func(ctx context.Context, _ Action, _ Attempt) (DispatchResult, error) {
			startOnce.Do(func() { close(started) })
			<-ctx.Done()
			cancelOnce.Do(func() { close(observedCancel) })
			return DispatchResult{}, ctx.Err()
		},
	}
	executor := newTestExecutor(t, journal, handler, executionClock())
	done := make(chan Result, 1)
	go func() { done <- executor.RunAction(context.Background(), action.ID) }()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("dispatch did not start")
	}
	if _, err := executor.Cancel(context.Background(), action.ID); err != nil {
		t.Fatal(err)
	}
	select {
	case <-observedCancel:
	case <-time.After(2 * time.Second):
		t.Fatal("durable cancellation did not reach handler context")
	}
	select {
	case result := <-done:
		if result.State != domain.ActionCancelled {
			t.Fatalf("cancelled result = %+v, want cancelled", result)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("in-flight action did not finish after cancellation")
	}
	if current := mustAction(t, journal, action.ID); current.State != domain.ActionCancelled {
		t.Fatalf("persisted action = %+v, want cancelled", current)
	}
}

func TestDurableCancelReachesHandlerAcrossExecutors(t *testing.T) {
	journal, action := newMemoryAction("cancel-across-executors", domain.ActionFSCopy, domain.ActionQueued)
	started := make(chan struct{})
	cancelled := make(chan struct{})
	var startOnce, cancelOnce sync.Once
	effect := executionEffect("copy", "payload.bin")
	handler := &scriptedHandler{
		kind: domain.ActionFSCopy,
		observeFn: func(_ context.Context, _ Action, _ int) (Observation, error) {
			return Observation{State: ObserveNeedsAction, Effects: []Effect{effect}}, nil
		},
		dispatchFn: func(ctx context.Context, _ Action, _ Attempt) (DispatchResult, error) {
			startOnce.Do(func() { close(started) })
			<-ctx.Done()
			cancelOnce.Do(func() { close(cancelled) })
			return DispatchResult{}, ctx.Err()
		},
	}
	owner, err := New(journal, Options{WorkerID: "cancel-owner", Now: executionClock(), LeaseDuration: time.Minute, CancellationPollInterval: 10 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	if err := owner.RegisterHandler(handler); err != nil {
		t.Fatal(err)
	}
	canceller, err := New(journal, Options{WorkerID: "cancel-requester", Now: executionClock(), CancellationPollInterval: 10 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan Result, 1)
	go func() { done <- owner.RunAction(context.Background(), action.ID) }()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("dispatch did not start")
	}
	if _, err := canceller.Cancel(context.Background(), action.ID); err != nil {
		t.Fatal(err)
	}
	select {
	case <-cancelled:
	case <-time.After(2 * time.Second):
		t.Fatal("durable cancellation did not reach handler owned by another executor")
	}
	select {
	case result := <-done:
		if result.State != domain.ActionCancelled {
			t.Fatalf("cross-executor cancellation result = %+v, want cancelled", result)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("cross-executor cancellation did not finish")
	}
	if current := mustAction(t, journal, action.ID); current.State != domain.ActionCancelled {
		t.Fatalf("cross-executor cancellation action = %+v, want cancelled", current)
	}
}

func TestDurableCancelReachesInitialObserveAcrossExecutors(t *testing.T) {
	journal, action := newMemoryAction("cancel-observe-across-executors", domain.ActionFSCopy, domain.ActionQueued)
	started := make(chan struct{})
	cancelled := make(chan struct{})
	var startOnce, cancelOnce sync.Once
	handler := &scriptedHandler{
		kind: domain.ActionFSCopy,
		observeFn: func(ctx context.Context, _ Action, _ int) (Observation, error) {
			startOnce.Do(func() { close(started) })
			<-ctx.Done()
			cancelOnce.Do(func() { close(cancelled) })
			return Observation{}, ctx.Err()
		},
	}
	owner, err := New(journal, Options{WorkerID: "observe-cancel-owner", Now: executionClock(), LeaseDuration: time.Minute, CancellationPollInterval: time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	if err := owner.RegisterHandler(handler); err != nil {
		t.Fatal(err)
	}
	canceller, err := New(journal, Options{WorkerID: "observe-cancel-requester", Now: executionClock(), CancellationPollInterval: time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan Result, 1)
	go func() { done <- owner.RunAction(context.Background(), action.ID) }()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("initial observe did not start")
	}
	if _, err := canceller.Cancel(context.Background(), action.ID); err != nil {
		t.Fatal(err)
	}
	select {
	case <-cancelled:
	case <-time.After(2 * time.Second):
		t.Fatal("durable cancellation did not reach initial observe owned by another executor")
	}
	select {
	case result := <-done:
		if result.State != domain.ActionCancelled {
			t.Fatalf("initial observe cancellation result = %+v, want cancelled", result)
		}
		if result.Dispatched {
			t.Fatal("initial observe cancellation dispatched a mutation")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("initial observe cancellation did not finish")
	}
	if current := mustAction(t, journal, action.ID); current.State != domain.ActionCancelled {
		t.Fatalf("initial observe cancellation action = %+v, want cancelled", current)
	}
	if attempts := mustAttempts(t, journal, action.ID); len(attempts) != 1 || attempts[0].Phase != AttemptObserve || attempts[0].State != domain.AttemptCancelled {
		t.Fatalf("initial observe cancellation attempts = %+v, want one cancelled observe", attempts)
	}
	if effects := mustEffects(t, journal, action.ID); len(effects) != 0 {
		t.Fatalf("initial observe cancellation effects = %+v, want none", effects)
	}
}

func TestNoDeadlineDispatchLeaseRenewsUntilHandlerReturns(t *testing.T) {
	journal, action := newMemoryAction("lease-renewal-no-deadline", domain.ActionFSCopy, domain.ActionQueued)
	started := make(chan struct{})
	var startOnce sync.Once
	effect := executionEffect("copy", "payload.bin")
	handler := &scriptedHandler{
		kind: domain.ActionFSCopy,
		observeFn: func(_ context.Context, _ Action, call int) (Observation, error) {
			if call == 1 {
				return Observation{State: ObserveNeedsAction, Effects: []Effect{effect}}, nil
			}
			return Observation{State: ObserveSatisfied, Effects: []Effect{effect}}, nil
		},
		dispatchFn: func(ctx context.Context, _ Action, _ Attempt) (DispatchResult, error) {
			startOnce.Do(func() { close(started) })
			timer := time.NewTimer(140 * time.Millisecond)
			defer timer.Stop()
			select {
			case <-timer.C:
				return DispatchResult{Accepted: true, Outcome: domain.OutcomeApplied}, nil
			case <-ctx.Done():
				return DispatchResult{}, ctx.Err()
			}
		},
	}
	executor, err := New(journal, Options{
		WorkerID:                 "lease-renewal-worker",
		LeaseDuration:            35 * time.Millisecond,
		LeaseRenewalInterval:     10 * time.Millisecond,
		JournalTimeout:           time.Second,
		CancellationPollInterval: 2 * time.Millisecond,
		Now:                      time.Now,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := executor.RegisterHandler(handler); err != nil {
		t.Fatal(err)
	}
	done := make(chan Result, 1)
	go func() { done <- executor.RunAction(context.Background(), action.ID) }()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("dispatch did not start")
	}
	select {
	case result := <-done:
		if result.Err != nil || result.State != domain.ActionSucceeded || result.Outcome != domain.OutcomeApplied {
			t.Fatalf("no-deadline lease renewal result = %+v, want applied success", result)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no-deadline lease renewal handler did not finish")
	}
	if renewals := journal.leaseRenewals(); renewals < 2 {
		t.Fatalf("lease renewals = %d, want at least two extensions during dispatch", renewals)
	}
}

func TestUncertainDispatchPersistsExactUnresolvedCount(t *testing.T) {
	journal, action := newMemoryAction("uncertain-exact-unresolved-count", domain.ActionFSCopy, domain.ActionQueued)
	effects := []Effect{
		executionEffect("copy", "one.bin"),
		executionEffect("copy", "two.bin"),
		executionEffect("copy", "three.bin"),
	}
	for index := range effects {
		effects[index].Ordinal = int64(index)
	}
	handler := &scriptedHandler{
		kind: domain.ActionFSCopy,
		observeFn: func(_ context.Context, _ Action, _ int) (Observation, error) {
			return Observation{State: ObserveNeedsAction, Effects: effects}, nil
		},
		dispatchFn: func(_ context.Context, _ Action, _ Attempt) (DispatchResult, error) {
			return DispatchResult{}, NewDispatchedFailure(FailureUncertain, errors.New("response lost"))
		},
	}
	result := mustRunOnce(t, newTestExecutor(t, journal, handler, executionClock()))
	if len(result.Results) != 1 || result.Results[0].State != domain.ActionReconciling {
		t.Fatalf("exact unresolved result = %+v, want reconciling", result.Results)
	}
	current := mustAction(t, journal, action.ID)
	if current.UnresolvedCount != int64(len(effects)) {
		t.Fatalf("persisted unresolved count = %d, want %d", current.UnresolvedCount, len(effects))
	}
	var outcome map[string]json.RawMessage
	if err := json.Unmarshal(current.Outcome, &outcome); err != nil {
		t.Fatal(err)
	}
	var unresolved int64
	if err := json.Unmarshal(outcome["unresolvedEffects"], &unresolved); err != nil {
		t.Fatal(err)
	}
	if unresolved != int64(len(effects)) {
		t.Fatalf("outcome unresolved count = %d, want %d", unresolved, len(effects))
	}
	for _, effect := range mustEffects(t, journal, action.ID) {
		if effect.State != EffectUnknown {
			t.Fatalf("uncertain effect = %+v, want unknown", effect)
		}
	}
}

func TestExpiredLiveDispatchBlocksRecoveredWorkerUntilReturn(t *testing.T) {
	store, journal, action := newSQLExecutionFixture(t, "sql-live-dispatch-barrier")
	defer store.Close()
	started := make(chan struct{})
	release := make(chan struct{})
	var startOnce sync.Once
	effects := []Effect{executionEffect("copy", "payload.bin"), executionEffect("copy", "subtitle.srt")}
	effects[1].Ordinal = 1
	oldHandler := &scriptedHandler{
		kind: domain.ActionFSCopy,
		observeFn: func(_ context.Context, _ Action, call int) (Observation, error) {
			if call == 1 {
				return Observation{State: ObserveNeedsAction, Effects: effects}, nil
			}
			return Observation{State: ObserveSatisfied, Effects: effects}, nil
		},
		dispatchFn: func(_ context.Context, _ Action, _ Attempt) (DispatchResult, error) {
			startOnce.Do(func() { close(started) })
			<-release
			return DispatchResult{Accepted: true, Outcome: domain.OutcomeApplied}, nil
		},
	}
	old, err := New(journal, Options{WorkerID: "live-old-worker", Now: executionClock(), LeaseDuration: time.Second, CancellationPollInterval: 10 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	if err := old.RegisterHandler(oldHandler); err != nil {
		t.Fatal(err)
	}
	oldDone := make(chan Result, 1)
	go func() { oldDone <- old.RunAction(context.Background(), action.ID) }()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("old dispatch did not start")
	}
	if recovered, err := journal.RecoverExpired(context.Background(), "2026-09-14T12:00:02Z"); err != nil {
		t.Fatal(err)
	} else if len(recovered) != 1 || recovered[0].State != domain.ActionReconciling {
		t.Fatalf("recovered live action = %+v, want one reconciling action", recovered)
	}

	var freshNow = executionTime().Add(2 * time.Second)
	freshHandler := &scriptedHandler{
		kind: domain.ActionFSCopy,
		reconcileFn: func(_ context.Context, _ Action, _ Attempt) (ReconcileResult, error) {
			return ReconcileResult{SafeToRetry: true, Effects: effects}, nil
		},
	}
	fresh, err := New(journal, Options{WorkerID: "live-fresh-worker", Now: func() time.Time { return freshNow }, LeaseDuration: time.Minute, CancellationPollInterval: 10 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	if err := fresh.RegisterHandler(freshHandler); err != nil {
		t.Fatal(err)
	}
	blocked := fresh.RunAction(context.Background(), action.ID)
	if blocked.State != domain.ActionReconciling || blocked.Action.UnresolvedCount != 2 {
		t.Fatalf("fresh worker result while old dispatch live = %+v, want reconciling", blocked)
	}
	if freshHandler.dispatchCalls() != 0 || freshHandler.reconcileCalls() != 0 {
		t.Fatalf("fresh worker calls while old dispatch live = dispatch %d reconcile %d, want zero/zero", freshHandler.dispatchCalls(), freshHandler.reconcileCalls())
	}

	close(release)
	select {
	case result := <-oldDone:
		if !errors.Is(result.Err, ErrLeaseLost) {
			t.Fatalf("old worker result after recovery = %+v, want lease loss", result)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("old worker did not return after release")
	}
	barrierEffects := mustEffects(t, journal, action.ID)
	if len(barrierEffects) != 2 || barrierEffects[0].State != EffectUnknown || barrierEffects[1].State != EffectUnknown {
		t.Fatalf("effects after old dispatch return = %+v, want two unknown effects", barrierEffects)
	}
	freshNow = executionTime().Add(20 * time.Second)
	resolved := fresh.RunAction(context.Background(), action.ID)
	if resolved.State != domain.ActionQueued {
		t.Fatalf("fresh worker result after old return = %+v, want queued safe retry", resolved)
	}
	if freshHandler.reconcileCalls() != 1 || freshHandler.dispatchCalls() != 0 {
		t.Fatalf("fresh worker calls after old return = dispatch %d reconcile %d, want zero/one", freshHandler.dispatchCalls(), freshHandler.reconcileCalls())
	}
	if resolved.Action.UnresolvedCount != 0 {
		t.Fatalf("fresh worker action after safe retry = %+v, want zero unresolved effects", resolved.Action)
	}
}

func TestBarrierReleaseRetriesAfterTransientJournalFailure(t *testing.T) {
	journal, action := newMemoryAction("barrier-release-retry", domain.ActionFSCopy, domain.ActionQueued)
	started := make(chan struct{})
	release := make(chan struct{})
	effect := executionEffect("copy", "payload.bin")
	oldHandler := &scriptedHandler{
		kind: domain.ActionFSCopy,
		observeFn: func(_ context.Context, _ Action, call int) (Observation, error) {
			if call == 1 {
				return Observation{State: ObserveNeedsAction, Effects: []Effect{effect}}, nil
			}
			return Observation{State: ObserveSatisfied, Effects: []Effect{effect}}, nil
		},
		dispatchFn: func(_ context.Context, _ Action, _ Attempt) (DispatchResult, error) {
			close(started)
			<-release
			return DispatchResult{Accepted: true, Outcome: domain.OutcomeApplied}, nil
		},
	}
	old, err := New(journal, Options{WorkerID: "barrier-old-worker", Now: executionClock(), LeaseDuration: time.Second, CancellationPollInterval: time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	if err := old.RegisterHandler(oldHandler); err != nil {
		t.Fatal(err)
	}
	oldDone := make(chan Result, 1)
	go func() { oldDone <- old.RunAction(context.Background(), action.ID) }()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("old dispatch did not start")
	}
	if _, err := journal.RecoverExpired(context.Background(), "2026-09-14T12:00:02Z"); err != nil {
		t.Fatal(err)
	}
	journal.failNextBarrierUpdateAttempt(1)
	close(release)
	select {
	case result := <-oldDone:
		if !errors.Is(result.Err, ErrLeaseLost) {
			t.Fatalf("old worker result after barrier failure = %+v, want lease loss", result)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("old worker did not return")
	}
	// The old worker's defer schedules the transient failure recovery in a
	// separate goroutine. Wait for that exact dispatch attempt to leave the
	// barrier before a fresh worker claims the action; otherwise this fixture
	// would intentionally race two safe recovery transactions.
	barrierDeadline := time.NewTimer(3 * time.Second)
	defer barrierDeadline.Stop()
	for {
		attempts := mustAttempts(t, journal, action.ID)
		if len(attempts) >= 2 && attempts[1].State == domain.AttemptReconciling {
			break
		}
		select {
		case <-barrierDeadline.C:
			t.Fatalf("attempts after transient barrier failure = %+v, want dispatch barrier reconciled", attempts)
		case <-time.After(time.Millisecond):
		}
	}

	freshHandler := &scriptedHandler{
		kind: domain.ActionFSCopy,
		reconcileFn: func(_ context.Context, _ Action, _ Attempt) (ReconcileResult, error) {
			return ReconcileResult{SafeToRetry: true, Effects: []Effect{effect}}, nil
		},
	}
	fresh, err := New(journal, Options{WorkerID: "barrier-fresh-worker", Now: func() time.Time { return executionTime().Add(20 * time.Second) }, LeaseDuration: time.Minute, CancellationPollInterval: time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	if err := fresh.RegisterHandler(freshHandler); err != nil {
		t.Fatal(err)
	}
	batch, err := fresh.RunOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(batch.Results) != 1 {
		t.Fatalf("fresh scheduler batch after transient barrier recovery = %+v, want one result", batch)
	}
	result := batch.Results[0]
	if result.State != domain.ActionQueued || result.Err != nil {
		t.Fatalf("fresh worker after barrier retry = %+v, want queued safe retry", result)
	}
	if freshHandler.reconcileCalls() != 1 || freshHandler.dispatchCalls() != 0 {
		t.Fatalf("fresh worker calls after barrier retry = reconcile %d dispatch %d, want one/zero", freshHandler.reconcileCalls(), freshHandler.dispatchCalls())
	}
	attempts := mustAttempts(t, journal, action.ID)
	if len(attempts) < 3 || attempts[1].State != domain.AttemptReconciling {
		t.Fatalf("attempts after barrier retry = %+v, want dispatch reconciled before new reconcile", attempts)
	}
	if resolved := mustAction(t, journal, action.ID); resolved.UnresolvedCount != 0 {
		t.Fatalf("action after barrier retry = %+v, want zero unresolved effects", resolved)
	}
}

func TestDispatchBarrierIntentSurvivesMarkerWriteOutage(t *testing.T) {
	journal, action := newMemoryAction("barrier-marker-outage", domain.ActionFSCopy, domain.ActionQueued)
	started := make(chan struct{})
	release := make(chan struct{})
	effect := executionEffect("copy", "payload.bin")
	oldHandler := &scriptedHandler{
		kind: domain.ActionFSCopy,
		observeFn: func(_ context.Context, _ Action, call int) (Observation, error) {
			if call == 1 {
				return Observation{State: ObserveNeedsAction, Effects: []Effect{effect}}, nil
			}
			return Observation{State: ObserveSatisfied, Effects: []Effect{effect}}, nil
		},
		dispatchFn: func(_ context.Context, _ Action, _ Attempt) (DispatchResult, error) {
			close(started)
			<-release
			return DispatchResult{Accepted: true, Outcome: domain.OutcomeApplied}, nil
		},
	}
	old, err := New(journal, Options{WorkerID: "marker-outage-old-worker", Now: executionClock(), LeaseDuration: time.Second, CancellationPollInterval: time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	if err := old.RegisterHandler(oldHandler); err != nil {
		t.Fatal(err)
	}
	oldDone := make(chan Result, 1)
	go func() { oldDone <- old.RunAction(context.Background(), action.ID) }()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("old dispatch did not start")
	}
	if _, err := journal.RecoverExpired(context.Background(), "2026-09-14T12:00:02Z"); err != nil {
		t.Fatal(err)
	}
	journal.blockDispatchBarrierMarkerWrites(true)
	close(release)
	select {
	case result := <-oldDone:
		if !errors.Is(result.Err, ErrLeaseLost) {
			t.Fatalf("old worker result during marker outage = %+v, want lease loss", result)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("old worker did not return during marker outage")
	}
	// The producer and all 32 bounded in-memory retries must encounter the
	// outage before this test restores the marker write capability.
	deadline := time.NewTimer(3 * time.Second)
	defer deadline.Stop()
	for journal.dispatchBarrierMarkerWriteAttempts() < 33 {
		select {
		case <-deadline.C:
			t.Fatalf("marker write attempts = %d, want the initial write plus all bounded retries", journal.dispatchBarrierMarkerWriteAttempts())
		case <-time.After(time.Millisecond):
		}
	}
	current := mustAction(t, journal, action.ID)
	if current.State != domain.ActionReconciling {
		t.Fatalf("action during marker outage = %+v, want reconciling", current)
	}
	if returnedAttemptID, returned := dispatchBarrierReturnedAttempt(current.Outcome); returned {
		t.Fatalf("action marker during outage = %q, want no action-level marker", returnedAttemptID)
	}
	attempts := mustAttempts(t, journal, action.ID)
	if len(attempts) < 2 || attempts[1].State != domain.AttemptRunning {
		t.Fatalf("attempts during marker outage = %+v, want a running dispatch barrier", attempts)
	}
	intentAttemptID, hasIntent := dispatchBarrierRetryIntentAttempt(attempts[1].Evidence)
	if !hasIntent || intentAttemptID != attempts[1].ID {
		t.Fatalf("dispatch intent = %q/%t in attempt %+v, want exact durable attempt identity", intentAttemptID, hasIntent, attempts[1])
	}

	journal.blockDispatchBarrierMarkerWrites(false)
	freshHandler := &scriptedHandler{
		kind: domain.ActionFSCopy,
		reconcileFn: func(_ context.Context, _ Action, _ Attempt) (ReconcileResult, error) {
			return ReconcileResult{SafeToRetry: true, Effects: []Effect{effect}}, nil
		},
	}
	fresh, err := New(journal, Options{WorkerID: "marker-outage-fresh-worker", Now: func() time.Time { return executionTime().Add(20 * time.Second) }, LeaseDuration: time.Minute, CancellationPollInterval: time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	if err := fresh.RegisterHandler(freshHandler); err != nil {
		t.Fatal(err)
	}
	batch, err := fresh.RunOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(batch.Results) != 1 {
		t.Fatalf("fresh scheduler batch after marker recovery = %+v, want one result", batch)
	}
	result := batch.Results[0]
	if result.Err != nil || result.State != domain.ActionQueued {
		t.Fatalf("fresh worker after marker recovery = %+v, want queued safe retry", result)
	}
	if freshHandler.reconcileCalls() != 1 || freshHandler.dispatchCalls() != 0 {
		t.Fatalf("fresh worker calls after marker recovery = reconcile %d dispatch %d, want one/zero", freshHandler.reconcileCalls(), freshHandler.dispatchCalls())
	}
	if markerAttemptID, marked := dispatchBarrierReturnedAttempt(mustAction(t, journal, action.ID).Outcome); marked {
		t.Fatalf("action marker after reconciliation = %q, want cleared terminal/retry outcome", markerAttemptID)
	}
	finalAttempts := mustAttempts(t, journal, action.ID)
	if len(finalAttempts) < 3 || finalAttempts[1].State != domain.AttemptReconciling {
		t.Fatalf("attempts after marker recovery = %+v, want barrier reconciled before fresh read-only attempt", finalAttempts)
	}
	if final := mustAction(t, journal, action.ID); final.UnresolvedCount != 0 {
		t.Fatalf("action after marker recovery = %+v, want zero unresolved effects", final)
	}
}

func TestDispatchBarrierActionMarkerSurvivesIntentWriteOutage(t *testing.T) {
	journal, action := newMemoryAction("barrier-intent-outage", domain.ActionFSCopy, domain.ActionQueued)
	started := make(chan struct{})
	release := make(chan struct{})
	effect := executionEffect("copy", "payload.bin")
	oldHandler := &scriptedHandler{
		kind: domain.ActionFSCopy,
		observeFn: func(_ context.Context, _ Action, call int) (Observation, error) {
			if call == 1 {
				return Observation{State: ObserveNeedsAction, Effects: []Effect{effect}}, nil
			}
			return Observation{State: ObserveSatisfied, Effects: []Effect{effect}}, nil
		},
		dispatchFn: func(_ context.Context, _ Action, _ Attempt) (DispatchResult, error) {
			close(started)
			<-release
			return DispatchResult{Accepted: true, Outcome: domain.OutcomeApplied}, nil
		},
	}
	old, err := New(journal, Options{WorkerID: "intent-outage-old-worker", Now: executionClock(), LeaseDuration: time.Second, CancellationPollInterval: time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	if err := old.RegisterHandler(oldHandler); err != nil {
		t.Fatal(err)
	}
	oldDone := make(chan Result, 1)
	go func() { oldDone <- old.RunAction(context.Background(), action.ID) }()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("old dispatch did not start")
	}
	if _, err := journal.RecoverExpired(context.Background(), "2026-09-14T12:00:02Z"); err != nil {
		t.Fatal(err)
	}
	journal.blockDispatchBarrierIntentWrites(true)
	close(release)
	select {
	case result := <-oldDone:
		if !errors.Is(result.Err, ErrLeaseLost) {
			t.Fatalf("old worker result during intent outage = %+v, want lease loss", result)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("old worker did not return during intent outage")
	}
	// The action marker is the durable fallback while every attempt-level
	// intent write fails. Wait for the initial call and all 32 process-local
	// retries to exercise that fault before restoring the prerequisite write.
	deadline := time.NewTimer(3 * time.Second)
	defer deadline.Stop()
	for journal.dispatchBarrierIntentWriteAttempts() < 33 {
		select {
		case <-deadline.C:
			t.Fatalf("intent write attempts = %d, want the initial write plus all bounded retries", journal.dispatchBarrierIntentWriteAttempts())
		case <-time.After(time.Millisecond):
		}
	}
	// Ensure the bounded producer retry has exited before the fresh scheduler
	// is allowed to claim the action. The exact action marker and running
	// attempt remain the only durable state during the outage.
	attemptsBeforeRetryWait := mustAttempts(t, journal, action.ID)
	if len(attemptsBeforeRetryWait) < 2 {
		t.Fatalf("attempts before retry wait = %+v, want dispatch attempt", attemptsBeforeRetryWait)
	}
	retryKey := action.ID + "\x00" + attemptsBeforeRetryWait[1].ID
	retryDeadline := time.NewTimer(3 * time.Second)
	defer retryDeadline.Stop()
	for {
		old.barrierMu.Lock()
		_, retryActive := old.barrierRetry[retryKey]
		old.barrierMu.Unlock()
		if !retryActive {
			break
		}
		select {
		case <-retryDeadline.C:
			t.Fatal("bounded barrier retry remained active after all intent failures")
		case <-time.After(time.Millisecond):
		}
	}
	current := mustAction(t, journal, action.ID)
	if current.State != domain.ActionReconciling {
		t.Fatalf("action during intent outage = %+v, want reconciling", current)
	}
	returnedAttemptID, returned := dispatchBarrierReturnedAttempt(current.Outcome)
	if !returned || returnedAttemptID != "barrier-intent-outage:attempt:2:dispatch" {
		t.Fatalf("action marker during intent outage = %q/%t, want exact durable dispatch marker", returnedAttemptID, returned)
	}
	attempts := mustAttempts(t, journal, action.ID)
	if len(attempts) < 2 || attempts[1].State != domain.AttemptRunning {
		t.Fatalf("attempts during intent outage = %+v, want running dispatch barrier", attempts)
	}
	if intentAttemptID, hasIntent := dispatchBarrierRetryIntentAttempt(attempts[1].Evidence); hasIntent {
		t.Fatalf("attempt intent during injected outage = %q, want no attempt-level marker", intentAttemptID)
	}
	if got := mustEffects(t, journal, action.ID); len(got) != 1 || got[0].State != EffectPending {
		t.Fatalf("effects during intent outage = %+v, want pending effect behind live barrier", got)
	}

	journal.blockDispatchBarrierIntentWrites(false)
	freshHandler := &scriptedHandler{
		kind: domain.ActionFSCopy,
		reconcileFn: func(_ context.Context, _ Action, _ Attempt) (ReconcileResult, error) {
			return ReconcileResult{SafeToRetry: true, Effects: []Effect{effect}}, nil
		},
	}
	fresh, err := New(journal, Options{WorkerID: "intent-outage-fresh-worker", Now: func() time.Time { return executionTime().Add(20 * time.Second) }, LeaseDuration: time.Minute, CancellationPollInterval: time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	if err := fresh.RegisterHandler(freshHandler); err != nil {
		t.Fatal(err)
	}
	batch, err := fresh.RunOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(batch.Results) != 1 {
		t.Fatalf("fresh scheduler batch after intent recovery = %+v, want one result", batch)
	}
	result := batch.Results[0]
	if result.Err != nil || result.State != domain.ActionQueued {
		t.Fatalf("fresh worker after intent recovery = %+v, want queued safe retry", result)
	}
	if freshHandler.reconcileCalls() != 1 || freshHandler.dispatchCalls() != 0 {
		t.Fatalf("fresh worker calls after intent recovery = reconcile %d dispatch %d, want one/zero", freshHandler.reconcileCalls(), freshHandler.dispatchCalls())
	}
	finalAttempts := mustAttempts(t, journal, action.ID)
	if len(finalAttempts) < 3 || finalAttempts[1].State != domain.AttemptReconciling {
		t.Fatalf("attempts after intent recovery = %+v, want barrier reconciled before fresh read-only attempt", finalAttempts)
	}
	if final := mustAction(t, journal, action.ID); final.UnresolvedCount != 0 {
		t.Fatalf("action after intent recovery = %+v, want zero unresolved effects", final)
	}
}

func TestDispatchBarrierFallbackSurvivesDualMarkerWriteOutage(t *testing.T) {
	journal, action := newMemoryAction(t.Name(), domain.ActionFSCopy, domain.ActionQueued)
	started := make(chan struct{})
	release := make(chan struct{})
	effect := executionEffect("copy", "payload.bin")
	oldHandler := &scriptedHandler{
		kind: domain.ActionFSCopy,
		observeFn: func(_ context.Context, _ Action, call int) (Observation, error) {
			if call == 1 {
				return Observation{State: ObserveNeedsAction, Effects: []Effect{effect}}, nil
			}
			return Observation{State: ObserveSatisfied, Effects: []Effect{effect}}, nil
		},
		dispatchFn: func(_ context.Context, _ Action, _ Attempt) (DispatchResult, error) {
			close(started)
			<-release
			return DispatchResult{Accepted: true, Outcome: domain.OutcomeApplied}, nil
		},
	}
	old, err := New(journal, Options{WorkerID: "dual-marker-outage-old-worker", Now: executionClock(), LeaseDuration: time.Second, CancellationPollInterval: time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	if err := old.RegisterHandler(oldHandler); err != nil {
		t.Fatal(err)
	}
	oldDone := make(chan Result, 1)
	go func() { oldDone <- old.RunAction(context.Background(), action.ID) }()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("old dispatch did not start")
	}
	if _, err := journal.RecoverExpired(context.Background(), "2026-09-14T12:00:02Z"); err != nil {
		t.Fatal(err)
	}
	journal.blockDispatchBarrierIntentWrites(true)
	journal.blockDispatchBarrierMarkerWrites(true)
	close(release)
	select {
	case result := <-oldDone:
		if !errors.Is(result.Err, ErrLeaseLost) {
			t.Fatalf("old worker result during dual-marker outage = %+v, want lease loss", result)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("old worker did not return during dual-marker outage")
	}
	// Both exact marker writes must fail through the initial call and all 32
	// bounded retries before the test restores journal writes. The fallback is
	// retained after that process-local retry producer exits.
	deadline := time.NewTimer(3 * time.Second)
	defer deadline.Stop()
	for journal.dispatchBarrierIntentWriteAttempts() < 33 || journal.dispatchBarrierMarkerWriteAttempts() < 33 {
		select {
		case <-deadline.C:
			t.Fatalf("dual-marker write attempts = intent %d marker %d, want at least 33 each", journal.dispatchBarrierIntentWriteAttempts(), journal.dispatchBarrierMarkerWriteAttempts())
		case <-time.After(time.Millisecond):
		}
	}
	attemptsBeforeRetryWait := mustAttempts(t, journal, action.ID)
	if len(attemptsBeforeRetryWait) < 2 {
		t.Fatalf("attempts before retry wait = %+v, want dispatch attempt", attemptsBeforeRetryWait)
	}
	retryKey := action.ID + "\x00" + attemptsBeforeRetryWait[1].ID
	retryDeadline := time.NewTimer(3 * time.Second)
	defer retryDeadline.Stop()
	for {
		old.barrierMu.Lock()
		_, retryActive := old.barrierRetry[retryKey]
		old.barrierMu.Unlock()
		if !retryActive {
			break
		}
		select {
		case <-retryDeadline.C:
			t.Fatal("bounded barrier retry remained active after dual-marker failure")
		case <-time.After(time.Millisecond):
		}
	}
	current := mustAction(t, journal, action.ID)
	if current.State != domain.ActionReconciling {
		t.Fatalf("action during dual-marker outage = %+v, want reconciling", current)
	}
	if _, returned := dispatchBarrierReturnedAttempt(current.Outcome); returned {
		t.Fatal("action marker during dual-marker outage = present, want no durable marker")
	}
	attempts := mustAttempts(t, journal, action.ID)
	if len(attempts) < 2 || attempts[1].State != domain.AttemptRunning {
		t.Fatalf("attempts during dual-marker outage = %+v, want running dispatch barrier", attempts)
	}
	if _, hasIntent := dispatchBarrierRetryIntentAttempt(attempts[1].Evidence); hasIntent {
		t.Fatal("attempt marker during dual-marker outage = present, want no durable marker")
	}
	if got := mustEffects(t, journal, action.ID); len(got) != 1 || got[0].State != EffectPending {
		t.Fatalf("effects during dual-marker outage = %+v, want pending effect behind live barrier", got)
	}
	pending := pendingDispatchBarrierFallbackAttempts(action.ID)
	if _, retained := pending[attempts[1].ID]; !retained {
		t.Fatalf("process-local fallback = %v, want exact dispatch attempt %q", pending, attempts[1].ID)
	}

	journal.blockDispatchBarrierIntentWrites(false)
	journal.blockDispatchBarrierMarkerWrites(false)
	freshHandler := &scriptedHandler{
		kind: domain.ActionFSCopy,
		reconcileFn: func(_ context.Context, _ Action, _ Attempt) (ReconcileResult, error) {
			return ReconcileResult{SafeToRetry: true, Effects: []Effect{effect}}, nil
		},
	}
	fresh, err := New(journal, Options{WorkerID: "dual-marker-outage-fresh-worker", Now: func() time.Time { return executionTime().Add(20 * time.Second) }, LeaseDuration: time.Minute, CancellationPollInterval: time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	if err := fresh.RegisterHandler(freshHandler); err != nil {
		t.Fatal(err)
	}
	batch, err := fresh.RunOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(batch.Results) != 1 {
		t.Fatalf("fresh scheduler batch after dual-marker recovery = %+v, want one result", batch)
	}
	result := batch.Results[0]
	if result.Err != nil || result.State != domain.ActionQueued {
		t.Fatalf("fresh worker after dual-marker recovery = %+v, want queued safe retry", result)
	}
	if freshHandler.reconcileCalls() != 1 || freshHandler.dispatchCalls() != 0 {
		t.Fatalf("fresh worker calls after dual-marker recovery = reconcile %d dispatch %d, want one/zero", freshHandler.reconcileCalls(), freshHandler.dispatchCalls())
	}
	finalAttempts := mustAttempts(t, journal, action.ID)
	if len(finalAttempts) < 3 || finalAttempts[1].State != domain.AttemptReconciling {
		t.Fatalf("attempts after dual-marker recovery = %+v, want barrier reconciled before fresh read-only attempt", finalAttempts)
	}
	if final := mustAction(t, journal, action.ID); final.UnresolvedCount != 0 {
		t.Fatalf("action after dual-marker recovery = %+v, want zero unresolved effects", final)
	}
	if pending := pendingDispatchBarrierFallbackAttempts(action.ID); len(pending) != 0 {
		t.Fatalf("process-local fallback after recovery = %v, want empty", pending)
	}
}

func TestUnresolvedReservationPersistsAcrossExecutorRestart(t *testing.T) {
	journal, first := newMemoryAction("reservation-persistent-first", domain.ActionFSCopy, domain.ActionQueued)
	_, second := newMemoryActionOnJournal(journal, "reservation-persistent-second", domain.ActionFSCopy, domain.ActionQueued)
	effect := executionEffect("copy", "payload.bin")
	firstHandler := &scriptedHandler{
		kind:        domain.ActionFSCopy,
		reservation: []string{"root:library/show"},
		observeFn: func(_ context.Context, _ Action, _ int) (Observation, error) {
			return Observation{State: ObserveNeedsAction, Effects: []Effect{effect}}, nil
		},
		dispatchFn: func(_ context.Context, _ Action, _ Attempt) (DispatchResult, error) {
			return DispatchResult{}, NewDispatchedFailure(FailureUncertain, errors.New("response lost"))
		},
	}
	firstExecutor := newTestExecutor(t, journal, firstHandler, executionClock())
	firstResult := firstExecutor.RunAction(context.Background(), first.ID)
	if firstResult.State != domain.ActionReconciling {
		t.Fatalf("first result = %+v, want unresolved reconciliation", firstResult)
	}
	current := mustAction(t, journal, first.ID)
	keys, err := reservationKeysFromOutcome(current.Outcome)
	if err != nil || !equalStrings(keys, []string{"root:library/show"}) {
		t.Fatalf("persisted reservation keys = %v (%v), want first action key", keys, err)
	}

	secondHandler := &scriptedHandler{
		kind:        domain.ActionFSCopy,
		reservation: []string{"root:library/show/file.mkv"},
		observeFn: func(_ context.Context, _ Action, _ int) (Observation, error) {
			return Observation{State: ObserveNeedsAction, Effects: []Effect{executionEffect("copy", "other.bin")}}, nil
		},
		dispatchFn: func(_ context.Context, _ Action, _ Attempt) (DispatchResult, error) {
			return DispatchResult{Accepted: true, Outcome: domain.OutcomeApplied}, nil
		},
	}
	// A fresh executor has no process-local reservation map. The durable
	// action outcome still prevents a descendant reservation from dispatching.
	secondExecutor := newTestExecutor(t, journal, secondHandler, executionClock())
	secondResult := secondExecutor.RunAction(context.Background(), second.ID)
	if secondResult.State != domain.ActionWaitingDependency {
		t.Fatalf("overlapping restart result = %+v, want waiting dependency", secondResult)
	}
	if secondHandler.dispatchCalls() != 0 {
		t.Fatalf("overlapping dispatch calls = %d, want zero", secondHandler.dispatchCalls())
	}
}

func TestReadBackRejectsPartialAndChangedEffectSets(t *testing.T) {
	tests := []struct {
		name     string
		readBack []Effect
	}{
		{
			name:     "empty target set",
			readBack: nil,
		},
		{
			name:     "omitted target",
			readBack: []Effect{executionEffect("copy", "one.bin")},
		},
		{
			name: "changed target",
			readBack: []Effect{{Ordinal: 0, TargetKind: "file", TargetID: "different.bin", EffectKind: "copy", State: EffectApplied, Evidence: json.RawMessage(`{}`), ObservedAt: executionFixtureTime},
				{Ordinal: 1, TargetKind: "file", TargetID: "two.bin", EffectKind: "copy", State: EffectApplied, Evidence: json.RawMessage(`{}`), ObservedAt: executionFixtureTime}},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			journal, action := newMemoryAction("effect-set-"+test.name, domain.ActionFSCopy, domain.ActionQueued)
			first := executionEffect("copy", "one.bin")
			second := executionEffect("copy", "two.bin")
			second.Ordinal = 1
			handler := &scriptedHandler{
				kind: domain.ActionFSCopy,
				observeFn: func(_ context.Context, _ Action, call int) (Observation, error) {
					if call == 1 {
						return Observation{State: ObserveNeedsAction, Effects: []Effect{first, second}}, nil
					}
					return Observation{State: ObserveSatisfied, Effects: test.readBack}, nil
				},
				dispatchFn: func(_ context.Context, _ Action, _ Attempt) (DispatchResult, error) {
					return DispatchResult{Accepted: true, Outcome: domain.OutcomeApplied}, nil
				},
			}
			result := mustRunOnce(t, newTestExecutor(t, journal, handler, executionClock()))
			if len(result.Results) != 1 || result.Results[0].State != domain.ActionReconciling {
				t.Fatalf("result = %+v, want unresolved reconciliation", result.Results)
			}
			if current := mustAction(t, journal, action.ID); current.State != domain.ActionReconciling {
				t.Fatalf("persisted action = %+v, want reconciling", current)
			}
		})
	}
}

func TestNeedsActionRequiresEffectEvidence(t *testing.T) {
	if err := (Observation{State: ObserveNeedsAction}).validate("needs-effect"); !errors.Is(err, ErrInvalidJournal) {
		t.Fatalf("empty needs-action validation error = %v, want invalid journal", err)
	}
}

func TestTerminalReadBackRejectsFailedEffectEvidence(t *testing.T) {
	journal, action := newMemoryAction("failed-read-back", domain.ActionFSCopy, domain.ActionQueued)
	effect := executionEffect("copy", "payload.bin")
	failed := effect
	failed.State = EffectFailed
	handler := &scriptedHandler{
		kind: domain.ActionFSCopy,
		observeFn: func(_ context.Context, _ Action, call int) (Observation, error) {
			if call == 1 {
				return Observation{State: ObserveNeedsAction, Effects: []Effect{effect}}, nil
			}
			return Observation{State: ObserveSatisfied, Effects: []Effect{failed}}, nil
		},
		dispatchFn: func(_ context.Context, _ Action, _ Attempt) (DispatchResult, error) {
			return DispatchResult{Accepted: true, Outcome: domain.OutcomeApplied}, nil
		},
	}
	result := mustRunOnce(t, newTestExecutor(t, journal, handler, executionClock()))
	if len(result.Results) != 1 || result.Results[0].State != domain.ActionReconciling {
		t.Fatalf("failed read-back result = %+v, want reconciling", result.Results)
	}
	if current := mustAction(t, journal, action.ID); current.State != domain.ActionReconciling {
		t.Fatalf("failed read-back action = %+v, want reconciling", current)
	}
	effects := mustEffects(t, journal, action.ID)
	if len(effects) != 1 || effects[0].State != EffectUnknown {
		t.Fatalf("failed read-back persisted effects = %+v, want unknown", effects)
	}
}

func TestUncertainDispatchMarksEveryPlannedEffectUnknown(t *testing.T) {
	journal, action := newMemoryAction("uncertain-all-effects", domain.ActionFSCopy, domain.ActionQueued)
	first := executionEffect("copy", "one.bin")
	second := executionEffect("copy", "two.bin")
	second.Ordinal = 1
	handler := &scriptedHandler{
		kind: domain.ActionFSCopy,
		observeFn: func(_ context.Context, _ Action, _ int) (Observation, error) {
			return Observation{State: ObserveNeedsAction, Effects: []Effect{first, second}}, nil
		},
		dispatchFn: func(_ context.Context, _ Action, _ Attempt) (DispatchResult, error) {
			return DispatchResult{}, NewDispatchedFailure(FailureUncertain, errors.New("lost response"))
		},
	}
	result := mustRunOnce(t, newTestExecutor(t, journal, handler, executionClock()))
	if len(result.Results) != 1 || result.Results[0].State != domain.ActionReconciling {
		t.Fatalf("uncertain result = %+v, want reconciling", result.Results)
	}
	effects := mustEffects(t, journal, action.ID)
	if len(effects) != 2 {
		t.Fatalf("uncertain effects = %+v, want two effects", effects)
	}
	for _, effect := range effects {
		if effect.State != EffectUnknown {
			t.Fatalf("uncertain effect = %+v, want unknown", effect)
		}
	}
	if current := mustAction(t, journal, action.ID); current.UnresolvedCount != 2 {
		t.Fatalf("uncertain action unresolved count = %d, want two", current.UnresolvedCount)
	}
}

func TestErroredDispatchPersistsReturnedPartialEffectsAndReconciles(t *testing.T) {
	journal, action := newMemoryAction("errored-partial-dispatch", domain.ActionFSCopy, domain.ActionQueued)
	first := executionEffect("copy", "one.bin")
	second := executionEffect("copy", "two.bin")
	second.Ordinal = 1
	second.Evidence = json.RawMessage(`{"approved_scope":"two.bin","digest":"sha256:synthetic","_mastarr_execution_markers":["source_marker"]}`)
	returned := first
	returned.State = EffectApplied
	returned.Evidence = json.RawMessage(`[` + `"handler_applied"` + `]`)
	returned.ObservedAt = executionFixtureTime
	reconciledFirst := returned
	reconciledSecond := second
	reconciledSecond.State = EffectApplied
	reconciledSecond.Evidence = json.RawMessage(`[` + `"read_back_applied"` + `]`)
	handler := &scriptedHandler{
		kind: domain.ActionFSCopy,
		observeFn: func(_ context.Context, _ Action, _ int) (Observation, error) {
			return Observation{State: ObserveNeedsAction, Effects: []Effect{first, second}}, nil
		},
		dispatchFn: func(_ context.Context, _ Action, _ Attempt) (DispatchResult, error) {
			return DispatchResult{
				ExternalID: "command-partial",
				Evidence:   []string{"one_file_published", "response_lost"},
				Effects:    []Effect{returned},
			}, NewDispatchedFailure(FailureUncertain, errors.New("response lost after partial dispatch"))
		},
		reconcileFn: func(_ context.Context, _ Action, _ Attempt) (ReconcileResult, error) {
			return ReconcileResult{Outcome: domain.OutcomeApplied, Evidence: []string{"read_back_complete"}, Effects: []Effect{reconciledFirst, reconciledSecond}}, nil
		},
	}

	firstRun := mustRunOnce(t, newTestExecutor(t, journal, handler, executionClock()))
	if len(firstRun.Results) != 1 || firstRun.Results[0].State != domain.ActionReconciling || !firstRun.Results[0].Dispatched {
		t.Fatalf("errored partial dispatch result = %+v, want dispatched reconciliation", firstRun.Results)
	}
	current := mustAction(t, journal, action.ID)
	if current.State != domain.ActionReconciling || current.UnresolvedCount != 1 {
		t.Fatalf("errored partial action = %+v, want one unresolved effect", current)
	}
	effects := mustEffects(t, journal, action.ID)
	if len(effects) != 2 || effects[0].State != EffectApplied || effects[1].State != EffectUnknown {
		t.Fatalf("errored partial effects = %+v, want applied plus unknown", effects)
	}
	var omittedEvidence map[string]json.RawMessage
	if err := json.Unmarshal(effects[1].Evidence, &omittedEvidence); err != nil {
		t.Fatalf("omitted opaque evidence is invalid: %v", err)
	}
	var approvedScope string
	if err := json.Unmarshal(omittedEvidence["approved_scope"], &approvedScope); err != nil || approvedScope != "two.bin" {
		t.Fatalf("omitted opaque evidence lost approved scope: %s (%v)", omittedEvidence["approved_scope"], err)
	}
	var markers []string
	if err := json.Unmarshal(omittedEvidence["_mastarr_execution_markers"], &markers); err != nil || !equalStrings(markers, []string{"source_marker", "dispatch_result_unreported"}) || strings.Count(string(effects[1].Evidence), `"_mastarr_execution_markers"`) != 1 {
		t.Fatalf("omitted opaque evidence markers = %v (%v), want unreported marker", markers, err)
	}
	if !strings.Contains(string(effects[0].Evidence), "handler_applied") {
		t.Fatalf("handler effect evidence was not persisted: %+v", effects)
	}
	attempts := mustAttempts(t, journal, action.ID)
	if len(attempts) != 2 || attempts[1].State != domain.AttemptReconciling || attempts[1].OutcomeCertainty != CertaintyUncertain || attempts[1].ExternalID != "command-partial" {
		t.Fatalf("errored partial attempts = %+v, want uncertain dispatch attempt", attempts)
	}
	if !strings.Contains(string(attempts[1].Evidence), "one_file_published") || !strings.Contains(string(attempts[1].Evidence), "dispatch_effect_count=1") {
		t.Fatalf("dispatch result evidence was not persisted: %s", attempts[1].Evidence)
	}

	secondRun := mustRunOnce(t, newTestExecutor(t, journal, handler, func() time.Time { return executionTime().Add(10 * time.Second) }))
	if len(secondRun.Results) != 1 || secondRun.Results[0].State != domain.ActionSucceeded || secondRun.Results[0].Outcome != domain.OutcomeApplied {
		t.Fatalf("reconciled errored partial result = %+v, want success", secondRun.Results)
	}
	if handler.dispatchCalls() != 1 || handler.reconcileCalls() != 1 {
		t.Fatalf("errored partial calls = dispatch %d reconcile %d, want one each", handler.dispatchCalls(), handler.reconcileCalls())
	}
	effects = mustEffects(t, journal, action.ID)
	if len(effects) != 2 || effects[0].State != EffectApplied || effects[1].State != EffectApplied {
		t.Fatalf("reconciled partial effects = %+v, want applied effects", effects)
	}
}

func TestExternalIDOnlyErroredDependencyForcesReconciliation(t *testing.T) {
	journal, action := newMemoryAction("external-id-only-dispatch", domain.ActionFSCopy, domain.ActionQueued)
	effect := executionEffect("copy", "payload.bin")
	var reconcileExternalID string
	handler := &scriptedHandler{
		kind: domain.ActionFSCopy,
		observeFn: func(_ context.Context, _ Action, _ int) (Observation, error) {
			return Observation{State: ObserveNeedsAction, Effects: []Effect{effect}}, nil
		},
		dispatchFn: func(_ context.Context, _ Action, _ Attempt) (DispatchResult, error) {
			return DispatchResult{ExternalID: "command-123"}, NewFailure(FailureDependency, errors.New("command status unavailable"))
		},
		reconcileFn: func(_ context.Context, _ Action, attempt Attempt) (ReconcileResult, error) {
			reconcileExternalID = attempt.ExternalID
			applied := effect
			applied.State = EffectApplied
			applied.Evidence = json.RawMessage(`{"read_back":"command-123"}`)
			return ReconcileResult{Outcome: domain.OutcomeApplied, Effects: []Effect{applied}, Evidence: []string{"command_read_back"}}, nil
		},
	}

	firstRun := mustRunOnce(t, newTestExecutor(t, journal, handler, executionClock()))
	if len(firstRun.Results) != 1 || firstRun.Results[0].State != domain.ActionReconciling || !firstRun.Results[0].Dispatched {
		t.Fatalf("external-ID-only dispatch result = %+v, want dispatched reconciliation", firstRun.Results)
	}
	current := mustAction(t, journal, action.ID)
	if current.State != domain.ActionReconciling {
		t.Fatalf("external-ID-only action = %+v, want reconciling", current)
	}
	attempts := mustAttempts(t, journal, action.ID)
	if len(attempts) != 2 || attempts[1].ExternalID != "command-123" || attempts[1].OutcomeCertainty != CertaintyUncertain {
		t.Fatalf("external-ID-only attempts = %+v, want durable uncertain command ID", attempts)
	}

	secondRun := mustRunOnce(t, newTestExecutor(t, journal, handler, func() time.Time { return executionTime().Add(10 * time.Second) }))
	if len(secondRun.Results) != 1 || secondRun.Results[0].State != domain.ActionSucceeded || secondRun.Results[0].Outcome != domain.OutcomeApplied {
		t.Fatalf("external-ID-only reconciliation result = %+v, want success", secondRun.Results)
	}
	if reconcileExternalID != "command-123" {
		t.Fatalf("reconciliation external ID = %q, want command-123", reconcileExternalID)
	}
	if handler.dispatchCalls() != 1 || handler.reconcileCalls() != 1 {
		t.Fatalf("external-ID-only calls = dispatch %d reconcile %d, want one dispatch and one reconciliation", handler.dispatchCalls(), handler.reconcileCalls())
	}
}

func TestReadBackFailureRetainsDispatchExternalIDForReconciliation(t *testing.T) {
	journal, action := newMemoryAction("external-id-read-back-failure", domain.ActionFSCopy, domain.ActionQueued)
	effect := executionEffect("copy", "payload.bin")
	var reconcileExternalID string
	handler := &scriptedHandler{
		kind: domain.ActionFSCopy,
		observeFn: func(_ context.Context, _ Action, call int) (Observation, error) {
			if call == 1 {
				return Observation{State: ObserveNeedsAction, Effects: []Effect{effect}}, nil
			}
			return Observation{}, NewFailure(FailureDependency, errors.New("read-back unavailable"))
		},
		dispatchFn: func(_ context.Context, _ Action, _ Attempt) (DispatchResult, error) {
			return DispatchResult{Accepted: true, Outcome: domain.OutcomeApplied, ExternalID: "read-back-command-123"}, nil
		},
		reconcileFn: func(_ context.Context, _ Action, attempt Attempt) (ReconcileResult, error) {
			reconcileExternalID = attempt.ExternalID
			applied := effect
			applied.State = EffectApplied
			return ReconcileResult{Outcome: domain.OutcomeApplied, Effects: []Effect{applied}}, nil
		},
	}

	first := mustRunOnce(t, newTestExecutor(t, journal, handler, executionClock()))
	if len(first.Results) != 1 || first.Results[0].State != domain.ActionReconciling || !first.Results[0].Dispatched {
		t.Fatalf("read-back failure result = %+v, want dispatched reconciliation", first.Results)
	}
	attempts := mustAttempts(t, journal, action.ID)
	if len(attempts) != 2 || attempts[1].ExternalID != "read-back-command-123" {
		t.Fatalf("read-back failure attempts = %+v, want retained command identity", attempts)
	}

	second := mustRunOnce(t, newTestExecutor(t, journal, handler, func() time.Time { return executionTime().Add(10 * time.Second) }))
	if len(second.Results) != 1 || second.Results[0].State != domain.ActionSucceeded || second.Results[0].Outcome != domain.OutcomeApplied {
		t.Fatalf("read-back failure reconciliation = %+v, want applied success", second.Results)
	}
	if reconcileExternalID != "read-back-command-123" {
		t.Fatalf("read-back failure reconciliation identity = %q, want read-back-command-123", reconcileExternalID)
	}
	if handler.dispatchCalls() != 1 || handler.reconcileCalls() != 1 {
		t.Fatalf("read-back failure calls = dispatch %d reconcile %d, want one dispatch and one reconciliation", handler.dispatchCalls(), handler.reconcileCalls())
	}
}

func TestLatestDispatchExternalIDUsesCurrentDispatchAttempt(t *testing.T) {
	if got := latestDispatchExternalID([]Attempt{
		{AttemptNumber: 2, Phase: AttemptDispatch, ExternalID: "old-command"},
		{AttemptNumber: 3, Phase: AttemptDispatch},
	}); got != "" {
		t.Fatalf("latest dispatch external ID after an empty current attempt = %q, want empty", got)
	}
	if got := latestDispatchExternalID([]Attempt{
		{AttemptNumber: 2, Phase: AttemptDispatch, ExternalID: "old-command"},
		{AttemptNumber: 3, Phase: AttemptDispatch, ExternalID: "current-command"},
	}); got != "current-command" {
		t.Fatalf("latest dispatch external ID = %q, want current-command", got)
	}
}

func TestAppendEvidencePreservesOpaqueJSONKeys(t *testing.T) {
	rawInvalidUTF8 := append([]byte(`{"_mastarr_execution_markers":["`), 0xff)
	rawInvalidUTF8 = append(rawInvalidUTF8, []byte(`"],"approved_scope":"two.bin"}`)...)
	tests := []struct {
		name       string
		input      json.RawMessage
		merge      bool
		wantMarker []string
	}{
		{
			name:       "compatible marker array",
			input:      json.RawMessage(`{"_mastarr_execution_markers":["source_marker"],"approved_scope":"two.bin"}`),
			merge:      true,
			wantMarker: []string{"source_marker", "dispatch_result_unreported"},
		},
		{
			name:  "null marker",
			input: json.RawMessage(`{"_mastarr_execution_markers":null,"approved_scope":"two.bin"}`),
		},
		{
			name:  "duplicate marker keys",
			input: json.RawMessage(`{"_mastarr_execution_markers":["first"],"_mastarr_execution_markers":["second"],"approved_scope":"two.bin"}`),
		},
		{
			name:  "duplicate ordinary keys",
			input: json.RawMessage(`{"approved_scope":"first","approved_scope":"second"}`),
		},
		{
			name:  "non-array marker",
			input: json.RawMessage(`{"_mastarr_execution_markers":{"source":"marker"},"approved_scope":"two.bin"}`),
		},
		{
			name:  "non-string marker member",
			input: json.RawMessage(`{"_mastarr_execution_markers":[1],"approved_scope":"two.bin"}`),
		},
		{
			name:  "escaped unpaired surrogate marker member",
			input: json.RawMessage(`{"_mastarr_execution_markers":["\ud800"],"approved_scope":"two.bin"}`),
		},
		{
			name:  "raw invalid UTF-8 marker member",
			input: rawInvalidUTF8,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := appendEvidence(test.input, "dispatch_result_unreported")
			if !json.Valid(got) {
				t.Fatalf("annotated evidence is invalid JSON: %s", got)
			}
			if !jsonObjectHasUniqueKeys(got) {
				t.Fatalf("annotated evidence has duplicate top-level keys: %s", got)
			}
			var object map[string]json.RawMessage
			if err := json.Unmarshal(got, &object); err != nil {
				t.Fatalf("annotated evidence object decode: %v", err)
			}
			expectedMarkers := []string{"dispatch_result_unreported"}
			if test.merge {
				expectedMarkers = test.wantMarker
			}
			markers, ok := decodeExecutionMarkers(object["_mastarr_execution_markers"])
			if !ok || !equalStrings(markers, expectedMarkers) {
				t.Fatalf("execution markers = %v, want %v", markers, expectedMarkers)
			}
			prior, hasPrior := object["_mastarr_prior"]
			if test.merge {
				if hasPrior {
					t.Fatalf("compatible marker array unexpectedly wrapped prior evidence: %s", got)
				}
				return
			}
			if !hasPrior {
				t.Fatalf("incompatible opaque evidence lost raw prior: %s", got)
			}
			if !bytes.Equal(bytes.TrimSpace(prior), bytes.TrimSpace(test.input)) {
				t.Fatalf("raw prior changed: got %s, want %s", prior, test.input)
			}
		})
	}
}

func TestPartialDispatchPreservesIllFormedMarkerEvidence(t *testing.T) {
	rawInvalidUTF8 := append([]byte(`{"_mastarr_execution_markers":["`), 0xff)
	rawInvalidUTF8 = append(rawInvalidUTF8, []byte(`"],"approved_scope":"two.bin"}`)...)
	tests := []struct {
		name     string
		evidence json.RawMessage
	}{
		{
			name:     "escaped unpaired surrogate",
			evidence: json.RawMessage(`{"_mastarr_execution_markers":["\ud800"],"approved_scope":"two.bin"}`),
		},
		{
			name:     "raw invalid UTF-8",
			evidence: rawInvalidUTF8,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			actionID := "partial-ill-formed-" + strings.ReplaceAll(test.name, " ", "-")
			journal, action := newMemoryAction(actionID, domain.ActionFSCopy, domain.ActionQueued)
			first := executionEffect("copy", "one.bin")
			second := executionEffect("copy", "two.bin")
			second.Ordinal = 1
			second.Evidence = test.evidence
			returned := first
			returned.State = EffectApplied
			returned.Evidence = json.RawMessage(`[` + `"handler_applied"` + `]`)
			reconciledFirst := returned
			reconciledSecond := second
			reconciledSecond.State = EffectApplied
			reconciledSecond.Evidence = json.RawMessage(`[` + `"read_back_applied"` + `]`)
			handler := &scriptedHandler{
				kind: domain.ActionFSCopy,
				observeFn: func(_ context.Context, _ Action, _ int) (Observation, error) {
					return Observation{State: ObserveNeedsAction, Effects: []Effect{first, second}}, nil
				},
				dispatchFn: func(_ context.Context, _ Action, _ Attempt) (DispatchResult, error) {
					return DispatchResult{ExternalID: "ill-formed-command", Effects: []Effect{returned}}, NewDispatchedFailure(FailureUncertain, errors.New("response lost after partial dispatch"))
				},
				reconcileFn: func(_ context.Context, _ Action, _ Attempt) (ReconcileResult, error) {
					return ReconcileResult{Outcome: domain.OutcomeApplied, Effects: []Effect{reconciledFirst, reconciledSecond}}, nil
				},
			}

			firstRun := mustRunOnce(t, newTestExecutor(t, journal, handler, executionClock()))
			if len(firstRun.Results) != 1 || firstRun.Results[0].State != domain.ActionReconciling || !firstRun.Results[0].Dispatched {
				t.Fatalf("ill-formed partial result = %+v, want dispatched reconciliation", firstRun.Results)
			}
			effects := mustEffects(t, journal, action.ID)
			if len(effects) != 2 || effects[1].State != EffectUnknown {
				t.Fatalf("ill-formed partial effects = %+v, want unknown omitted effect", effects)
			}
			var envelope map[string]json.RawMessage
			if err := json.Unmarshal(effects[1].Evidence, &envelope); err != nil {
				t.Fatalf("ill-formed partial evidence is invalid: %v", err)
			}
			prior, ok := envelope["_mastarr_prior"]
			if !ok || !bytes.Equal(bytes.TrimSpace(prior), bytes.TrimSpace(test.evidence)) {
				t.Fatalf("ill-formed partial raw prior = %s, want %s", prior, test.evidence)
			}
			markers, ok := decodeExecutionMarkers(envelope["_mastarr_execution_markers"])
			if !ok || !equalStrings(markers, []string{"dispatch_result_unreported"}) {
				t.Fatalf("ill-formed partial markers = %v, want unreported marker", markers)
			}

			secondRun := mustRunOnce(t, newTestExecutor(t, journal, handler, func() time.Time { return executionTime().Add(10 * time.Second) }))
			if len(secondRun.Results) != 1 || secondRun.Results[0].State != domain.ActionSucceeded || secondRun.Results[0].Outcome != domain.OutcomeApplied {
				t.Fatalf("ill-formed partial reconciliation = %+v, want applied success", secondRun.Results)
			}
			if handler.dispatchCalls() != 1 || handler.reconcileCalls() != 1 {
				t.Fatalf("ill-formed partial calls = dispatch %d reconcile %d, want one dispatch and one reconciliation", handler.dispatchCalls(), handler.reconcileCalls())
			}
		})
	}
}

func TestReconciliationRejectsFailedTerminalEffect(t *testing.T) {
	journal, action := newMemoryAction("reconcile-failed-effect", domain.ActionFSCopy, domain.ActionQueued)
	effect := executionEffect("copy", "payload.bin")
	failed := effect
	failed.State = EffectFailed
	handler := &scriptedHandler{
		kind: domain.ActionFSCopy,
		observeFn: func(_ context.Context, _ Action, _ int) (Observation, error) {
			return Observation{State: ObserveNeedsAction, Effects: []Effect{effect}}, nil
		},
		dispatchFn: func(_ context.Context, _ Action, _ Attempt) (DispatchResult, error) {
			return DispatchResult{}, NewDispatchedFailure(FailureUncertain, errors.New("lost response"))
		},
		reconcileFn: func(_ context.Context, _ Action, _ Attempt) (ReconcileResult, error) {
			return ReconcileResult{Outcome: domain.OutcomeApplied, Effects: []Effect{failed}}, nil
		},
	}
	executor := newTestExecutor(t, journal, handler, executionClock())
	if first := mustRunOnce(t, executor); first.Results[0].State != domain.ActionReconciling {
		t.Fatalf("uncertain result = %+v", first.Results)
	}
	clockNow := executionTime().Add(10 * time.Second)
	restarted := newTestExecutor(t, journal, handler, func() time.Time { return clockNow })
	second := mustRunOnce(t, restarted)
	if len(second.Results) != 1 || second.Results[0].State != domain.ActionNeedsReview {
		t.Fatalf("failed reconciliation result = %+v, want needs review", second.Results)
	}
	if current := mustAction(t, journal, action.ID); current.State != domain.ActionNeedsReview {
		t.Fatalf("failed reconciliation action = %+v, want needs review", current)
	}
}

func TestIdleCancellationReleasesLocalReservation(t *testing.T) {
	journal, first := newMemoryAction("idle-cancel-first", domain.ActionFSCopy, domain.ActionQueued)
	_, second := newMemoryActionOnJournal(journal, "idle-cancel-second", domain.ActionFSCopy, domain.ActionQueued)
	effect := executionEffect("copy", "payload.bin")
	handler := &scriptedHandler{
		kind:        domain.ActionFSCopy,
		reservation: []string{"root:library/show"},
		observeFn: func(_ context.Context, action Action, _ int) (Observation, error) {
			if action.ID == first.ID {
				return Observation{State: ObserveNeedsAction, Effects: []Effect{effect}}, nil
			}
			return Observation{State: ObserveSatisfied}, nil
		},
		dispatchFn: func(_ context.Context, _ Action, _ Attempt) (DispatchResult, error) {
			return DispatchResult{}, NewDispatchedFailure(FailureUncertain, errors.New("lost response"))
		},
		reconcileFn: func(_ context.Context, _ Action, _ Attempt) (ReconcileResult, error) {
			return ReconcileResult{SafeToRetry: true, Effects: []Effect{effect}}, nil
		},
	}
	executor := newTestExecutor(t, journal, handler, executionClock())
	if result := executor.RunAction(context.Background(), first.ID); result.State != domain.ActionReconciling {
		t.Fatalf("uncertain first action = %+v, want reconciling", result)
	}
	if _, err := executor.Cancel(context.Background(), first.ID); err != nil {
		t.Fatal(err)
	}
	if result := executor.RunAction(context.Background(), first.ID); result.State != domain.ActionCancelled {
		t.Fatalf("idle cancelled first action = %+v, want cancelled", result)
	}
	result := executor.RunAction(context.Background(), second.ID)
	if result.State != domain.ActionSucceeded || result.Outcome != domain.OutcomeAlreadySatisfied {
		t.Fatalf("overlapping action after idle cancellation = %+v, want already-satisfied success", result)
	}
}

func TestReconciliationRejectsChangedEffectIdentity(t *testing.T) {
	journal, action := newMemoryAction("reconcile-effect-drift", domain.ActionFSCopy, domain.ActionQueued)
	expected := executionEffect("copy", "payload.bin")
	handler := &scriptedHandler{
		kind: domain.ActionFSCopy,
		observeFn: func(_ context.Context, _ Action, _ int) (Observation, error) {
			return Observation{State: ObserveNeedsAction, Effects: []Effect{expected}}, nil
		},
		dispatchFn: func(_ context.Context, _ Action, _ Attempt) (DispatchResult, error) {
			return DispatchResult{}, NewDispatchedFailure(FailureUncertain, errors.New("response lost"))
		},
		reconcileFn: func(_ context.Context, _ Action, _ Attempt) (ReconcileResult, error) {
			return ReconcileResult{Outcome: domain.OutcomeApplied, Effects: []Effect{{Ordinal: 0, TargetKind: "file", TargetID: "other.bin", EffectKind: "copy", State: EffectApplied, Evidence: json.RawMessage(`{}`), ObservedAt: executionFixtureTime}}, Evidence: []string{"drift"}}, nil
		},
	}
	executor := newTestExecutor(t, journal, handler, executionClock())
	first := mustRunOnce(t, executor)
	if first.Results[0].State != domain.ActionReconciling {
		t.Fatalf("first result = %+v, want reconciliation", first.Results)
	}
	clockNow := executionTime().Add(10 * time.Second)
	restarted := newTestExecutor(t, journal, handler, func() time.Time { return clockNow })
	second := mustRunOnce(t, restarted)
	if len(second.Results) != 1 || second.Results[0].State != domain.ActionNeedsReview {
		t.Fatalf("reconciliation drift result = %+v, want needs review", second.Results)
	}
	if current := mustAction(t, journal, action.ID); current.State != domain.ActionNeedsReview {
		t.Fatalf("persisted action = %+v, want needs review", current)
	}
}

func TestValidateEffectsRejectsDuplicateTargetIdentity(t *testing.T) {
	first := executionEffect("copy", "payload.bin")
	second := executionEffect("copy", "payload.bin")
	second.Ordinal = 1
	if err := (Observation{State: ObserveNeedsAction, Effects: []Effect{first, second}}).validate("duplicate-effects"); !errors.Is(err, ErrInvalidJournal) {
		t.Fatalf("duplicate validation error = %v, want invalid journal", err)
	}
}

func TestSQLStaleWorkerCannotFinalizeRecoveredLease(t *testing.T) {
	path := filepath.Join(t.TempDir(), "stale-worker.sqlite")
	store, err := storage.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	created := "2026-09-14T11:00:00Z"
	planID, actionID, digest := "stale-plan", "stale-action", "stale-digest"
	if _, err := store.Queries().CreateActionPlan(ctx, &sqlc.CreateActionPlanParams{ID: planID, Kind: string(domain.ActionFSCopy), State: "ready", CurrentRevision: 1, CurrentDigest: digest, CreatedAt: created, UpdatedAt: created}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Queries().CreateActionPlanRevision(ctx, &sqlc.CreateActionPlanRevisionParams{PlanID: planID, Revision: 1, Digest: digest, State: "ready", InputJson: `{}`, PreconditionsJson: `{}`, CapabilitiesJson: `[]`, ManifestJson: `[]`, CreatedAt: created, ExpiresAt: "2026-09-20T00:00:00Z"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Queries().CreateActionRun(ctx, &sqlc.CreateActionRunParams{ID: actionID, PlanID: planID, PlanRevision: 1, PlanDigest: digest, State: string(domain.ActionQueued), DesiredStateJson: `{}`, Version: 1, OutcomeJson: `{}`, CreatedAt: created, UpdatedAt: created}); err != nil {
		t.Fatal(err)
	}
	journal, err := NewSQLJournal(store)
	if err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	handler := &scriptedHandler{
		kind: domain.ActionFSCopy,
		observeFn: func(_ context.Context, _ Action, _ int) (Observation, error) {
			return Observation{State: ObserveNeedsAction, Effects: []Effect{executionEffect("copy", "payload.bin")}}, nil
		},
		dispatchFn: func(_ context.Context, _ Action, _ Attempt) (DispatchResult, error) {
			once.Do(func() { close(started) })
			<-release
			return DispatchResult{Accepted: true, Outcome: domain.OutcomeApplied}, nil
		},
	}
	clock := executionClock()
	worker, err := New(journal, Options{WorkerID: "stale-worker", Now: clock, LeaseDuration: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if err := worker.RegisterHandler(handler); err != nil {
		t.Fatal(err)
	}
	resultDone := make(chan Result, 1)
	go func() { resultDone <- worker.RunAction(ctx, actionID) }()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("stale worker dispatch did not start")
	}
	recovered, err := journal.RecoverExpired(ctx, "2026-09-14T12:00:02Z")
	if err != nil {
		t.Fatal(err)
	}
	if len(recovered) != 1 || recovered[0].State != domain.ActionReconciling {
		t.Fatalf("recovered = %+v, want one reconciling action", recovered)
	}
	close(release)
	select {
	case result := <-resultDone:
		if !errors.Is(result.Err, ErrLeaseLost) {
			t.Fatalf("stale worker result = %+v, want lease loss", result)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("stale worker did not finish")
	}
	current, err := journal.GetAction(ctx, actionID)
	if err != nil {
		t.Fatal(err)
	}
	if current.State != domain.ActionReconciling || current.ClaimedBy != "" {
		t.Fatalf("recovered action after stale completion = %+v, want untouched reconciliation", current)
	}
}

func TestSQLReadBackRejectsPartialEffectSet(t *testing.T) {
	store, journal, action := newSQLExecutionFixture(t, "sql-effect-set")
	defer store.Close()
	first := executionEffect("copy", "one.bin")
	second := executionEffect("copy", "two.bin")
	second.Ordinal = 1
	handler := &scriptedHandler{
		kind: domain.ActionFSCopy,
		observeFn: func(_ context.Context, _ Action, call int) (Observation, error) {
			if call == 1 {
				return Observation{State: ObserveNeedsAction, Effects: []Effect{first, second}}, nil
			}
			return Observation{State: ObserveSatisfied, Effects: []Effect{first}}, nil
		},
		dispatchFn: func(_ context.Context, _ Action, _ Attempt) (DispatchResult, error) {
			return DispatchResult{Accepted: true, Outcome: domain.OutcomeApplied}, nil
		},
	}
	executor, err := New(journal, Options{WorkerID: "sql-effect-worker", Now: executionClock(), LeaseDuration: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	if err := executor.RegisterHandler(handler); err != nil {
		t.Fatal(err)
	}
	result := executor.RunAction(context.Background(), action.ID)
	if result.State != domain.ActionReconciling {
		t.Fatalf("SQL partial read-back result = %+v, want reconciling", result)
	}
	current, err := journal.GetAction(context.Background(), action.ID)
	if err != nil {
		t.Fatal(err)
	}
	if current.State != domain.ActionReconciling || current.UnresolvedCount != 2 {
		t.Fatalf("SQL partial read-back action = %+v, want unresolved reconciliation", current)
	}
}

func TestSQLExternalIDSurvivesRestartIntoReconciliation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sql-external-id-restart.sqlite")
	store, journal, action := newSQLExecutionFixtureAtPath(t, path, "sql-external-id-restart")
	effect := executionEffect("copy", "payload.bin")
	var reconcileExternalID string
	handler := &scriptedHandler{
		kind: domain.ActionFSCopy,
		observeFn: func(_ context.Context, _ Action, _ int) (Observation, error) {
			return Observation{State: ObserveNeedsAction, Effects: []Effect{effect}}, nil
		},
		dispatchFn: func(_ context.Context, _ Action, _ Attempt) (DispatchResult, error) {
			return DispatchResult{ExternalID: "sql-command-123"}, NewFailure(FailureDependency, errors.New("command status unavailable"))
		},
		reconcileFn: func(_ context.Context, _ Action, attempt Attempt) (ReconcileResult, error) {
			reconcileExternalID = attempt.ExternalID
			applied := effect
			applied.State = EffectApplied
			applied.Evidence = json.RawMessage(`{"read_back":"sql-command-123"}`)
			return ReconcileResult{Outcome: domain.OutcomeApplied, Effects: []Effect{applied}}, nil
		},
	}
	firstExecutor, err := New(journal, Options{WorkerID: "sql-external-id-worker", Now: executionClock(), LeaseDuration: time.Minute})
	if err != nil {
		store.Close()
		t.Fatal(err)
	}
	if err := firstExecutor.RegisterHandler(handler); err != nil {
		store.Close()
		t.Fatal(err)
	}
	first := firstExecutor.RunAction(context.Background(), action.ID)
	if first.State != domain.ActionReconciling {
		store.Close()
		t.Fatalf("SQL external-ID first result = %+v, want reconciling", first)
	}
	attempts, err := journal.ListAttempts(context.Background(), action.ID)
	if err != nil {
		store.Close()
		t.Fatal(err)
	}
	if len(attempts) != 2 || attempts[1].ExternalID != "sql-command-123" {
		store.Close()
		t.Fatalf("SQL external-ID dispatch attempts = %+v, want durable command ID", attempts)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := storage.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	reopenedJournal, err := NewSQLJournal(reopened)
	if err != nil {
		t.Fatal(err)
	}
	restarted, err := New(reopenedJournal, Options{WorkerID: "sql-external-id-restart", Now: func() time.Time { return executionTime().Add(10 * time.Second) }, LeaseDuration: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	if err := restarted.RegisterHandler(handler); err != nil {
		t.Fatal(err)
	}
	second, err := restarted.RunOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if second.Considered != 1 || len(second.Results) != 1 || second.Results[0].State != domain.ActionSucceeded || second.Results[0].Outcome != domain.OutcomeApplied {
		t.Fatalf("SQL external-ID restart result = %+v, want reconciled success", second)
	}
	if reconcileExternalID != "sql-command-123" {
		t.Fatalf("SQL external-ID reconciliation identity = %q, want sql-command-123", reconcileExternalID)
	}
	if handler.dispatchCalls() != 1 || handler.reconcileCalls() != 1 {
		t.Fatalf("SQL external-ID calls = dispatch %d reconcile %d, want one dispatch and one reconciliation", handler.dispatchCalls(), handler.reconcileCalls())
	}
	attempts, err = reopenedJournal.ListAttempts(context.Background(), action.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(attempts) != 3 || attempts[2].Phase != AttemptReconcile || attempts[2].ExternalID != "sql-command-123" {
		t.Fatalf("SQL external-ID restart attempts = %+v, want reconciler command ID", attempts)
	}
}

func TestSQLReconciliationRejectsChangedEffectIdentity(t *testing.T) {
	store, journal, action := newSQLExecutionFixture(t, "sql-reconcile-drift")
	defer store.Close()
	expected := executionEffect("copy", "payload.bin")
	handler := &scriptedHandler{
		kind: domain.ActionFSCopy,
		observeFn: func(_ context.Context, _ Action, _ int) (Observation, error) {
			return Observation{State: ObserveNeedsAction, Effects: []Effect{expected}}, nil
		},
		dispatchFn: func(_ context.Context, _ Action, _ Attempt) (DispatchResult, error) {
			return DispatchResult{}, NewDispatchedFailure(FailureUncertain, errors.New("response lost"))
		},
		reconcileFn: func(_ context.Context, _ Action, _ Attempt) (ReconcileResult, error) {
			return ReconcileResult{Outcome: domain.OutcomeApplied, Effects: []Effect{{Ordinal: 0, TargetKind: "file", TargetID: "other.bin", EffectKind: "copy", State: EffectApplied, Evidence: json.RawMessage(`{}`), ObservedAt: executionFixtureTime}}}, nil
		},
	}
	executor, err := New(journal, Options{WorkerID: "sql-reconcile-worker", Now: executionClock(), LeaseDuration: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	if err := executor.RegisterHandler(handler); err != nil {
		t.Fatal(err)
	}
	first := executor.RunAction(context.Background(), action.ID)
	if first.State != domain.ActionReconciling {
		t.Fatalf("SQL uncertain result = %+v, want reconciliation", first)
	}
	clockNow := executionTime().Add(10 * time.Second)
	restarted, err := New(journal, Options{WorkerID: "sql-reconcile-restart", Now: func() time.Time { return clockNow }, LeaseDuration: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	if err := restarted.RegisterHandler(handler); err != nil {
		t.Fatal(err)
	}
	second := restarted.RunAction(context.Background(), action.ID)
	if second.State != domain.ActionNeedsReview {
		t.Fatalf("SQL reconciliation drift result = %+v, want needs review", second)
	}
	current, err := journal.GetAction(context.Background(), action.ID)
	if err != nil {
		t.Fatal(err)
	}
	if current.State != domain.ActionNeedsReview {
		t.Fatalf("SQL reconciliation drift action = %+v, want needs review", current)
	}
}

func executionClock() func() time.Time {
	value := executionTime()
	return func() time.Time { return value }
}

func executionTime() time.Time {
	value, _ := time.Parse(time.RFC3339, executionFixtureTime)
	return value
}

func newSQLExecutionFixture(t *testing.T, actionID string) (*storage.Store, *SQLJournal, Action) {
	t.Helper()
	path := filepath.Join(t.TempDir(), actionID+".sqlite")
	return newSQLExecutionFixtureAtPath(t, path, actionID)
}

func newSQLExecutionFixtureAtPath(t *testing.T, path, actionID string) (*storage.Store, *SQLJournal, Action) {
	t.Helper()
	store, err := storage.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	created := executionFixtureTime
	planID := actionID + "-plan"
	digest := actionID + "-digest"
	ctx := context.Background()
	if _, err := store.Queries().CreateActionPlan(ctx, &sqlc.CreateActionPlanParams{ID: planID, Kind: string(domain.ActionFSCopy), State: "ready", CurrentRevision: 1, CurrentDigest: digest, CreatedAt: created, UpdatedAt: created}); err != nil {
		store.Close()
		t.Fatal(err)
	}
	if _, err := store.Queries().CreateActionPlanRevision(ctx, &sqlc.CreateActionPlanRevisionParams{PlanID: planID, Revision: 1, Digest: digest, State: "ready", InputJson: `{}`, PreconditionsJson: `{}`, CapabilitiesJson: `[]`, ManifestJson: `[]`, CreatedAt: created, ExpiresAt: "2026-09-20T00:00:00Z"}); err != nil {
		store.Close()
		t.Fatal(err)
	}
	if _, err := store.Queries().CreateActionRun(ctx, &sqlc.CreateActionRunParams{ID: actionID, PlanID: planID, PlanRevision: 1, PlanDigest: digest, State: string(domain.ActionQueued), DesiredStateJson: `{}`, Version: 1, OutcomeJson: `{}`, CreatedAt: created, UpdatedAt: created}); err != nil {
		store.Close()
		t.Fatal(err)
	}
	journal, err := NewSQLJournal(store)
	if err != nil {
		store.Close()
		t.Fatal(err)
	}
	action, err := journal.GetAction(ctx, actionID)
	if err != nil {
		store.Close()
		t.Fatal(err)
	}
	return store, journal, action
}

func newTestExecutor(t *testing.T, journal *memoryJournal, handler *scriptedHandler, now func() time.Time) *Executor {
	t.Helper()
	executor, err := New(journal, Options{WorkerID: "test-worker", Now: now, LeaseDuration: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	if err := executor.RegisterHandler(handler); err != nil {
		t.Fatal(err)
	}
	return executor
}

func mustRunOnce(t *testing.T, executor *Executor) BatchResult {
	t.Helper()
	batch, err := executor.RunOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return batch
}

func newMemoryAction(id string, kind domain.ActionKind, state domain.ActionState) (*memoryJournal, Action) {
	journal := &memoryJournal{actions: make(map[string]Action), plans: make(map[string]Plan), attempts: make(map[string][]Attempt), effects: make(map[string][]Effect), failureMu: &sync.Mutex{}, markerWrite: &markerWriteFault{}, barrierIntentWrite: &barrierIntentWriteFault{}}
	return newMemoryActionOnJournal(journal, id, kind, state)
}

func newMemoryActionOnJournal(journal *memoryJournal, id string, kind domain.ActionKind, state domain.ActionState) (*memoryJournal, Action) {
	action := Action{
		ID: id, PlanID: id + "-plan", PlanRevision: 1, PlanDigest: id + "-digest", Kind: kind, State: state,
		DesiredState: json.RawMessage(`{"target":"payload.bin"}`), Outcome: json.RawMessage(`{}`), Version: 1,
		CreatedAt: executionFixtureTime, UpdatedAt: executionFixtureTime,
	}
	journal.putPlan(Plan{ID: action.PlanID, Kind: kind, State: "ready", CurrentRevision: 1, CurrentDigest: action.PlanDigest})
	journal.putAction(action)
	return journal, action
}

func executionEffect(kind, target string) Effect {
	return Effect{Ordinal: 0, TargetKind: "file", TargetID: target, EffectKind: kind, State: EffectPending, Evidence: json.RawMessage(`{}`), ObservedAt: executionFixtureTime}
}

func mustAction(t *testing.T, journal Journal, id string) Action {
	t.Helper()
	action, err := journal.GetAction(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return action
}

func mustAttempts(t *testing.T, journal Journal, id string) []Attempt {
	t.Helper()
	attempts, err := journal.ListAttempts(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return attempts
}

func mustEffects(t *testing.T, journal Journal, id string) []Effect {
	t.Helper()
	effects, err := journal.ListEffects(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return effects
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

type scriptedHandler struct {
	mu             sync.Mutex
	kind           domain.ActionKind
	reservation    []string
	observeFn      func(context.Context, Action, int) (Observation, error)
	dispatchFn     func(context.Context, Action, Attempt) (DispatchResult, error)
	reconcileFn    func(context.Context, Action, Attempt) (ReconcileResult, error)
	events         []string
	observeCount   int
	dispatchCount  int
	reconcileCount int
}

type journalWithoutTransactions struct {
	Journal
}

func (handler *scriptedHandler) Kind() domain.ActionKind { return handler.kind }

func (handler *scriptedHandler) Reservations(Action) []string {
	handler.mu.Lock()
	defer handler.mu.Unlock()
	return append([]string(nil), handler.reservation...)
}

func (handler *scriptedHandler) Observe(ctx context.Context, action Action) (Observation, error) {
	handler.mu.Lock()
	handler.observeCount++
	call := handler.observeCount
	handler.events = append(handler.events, "observe")
	fn := handler.observeFn
	handler.mu.Unlock()
	if fn == nil {
		return Observation{State: ObserveSatisfied}, nil
	}
	return fn(ctx, action, call)
}

func (handler *scriptedHandler) Dispatch(ctx context.Context, action Action, attempt Attempt) (DispatchResult, error) {
	handler.mu.Lock()
	handler.dispatchCount++
	handler.events = append(handler.events, "dispatch")
	fn := handler.dispatchFn
	handler.mu.Unlock()
	if fn == nil {
		return DispatchResult{Accepted: true, Outcome: domain.OutcomeApplied}, nil
	}
	return fn(ctx, action, attempt)
}

func (handler *scriptedHandler) Reconcile(ctx context.Context, action Action, attempt Attempt) (ReconcileResult, error) {
	handler.mu.Lock()
	handler.reconcileCount++
	handler.events = append(handler.events, "reconcile")
	fn := handler.reconcileFn
	handler.mu.Unlock()
	if fn == nil {
		return ReconcileResult{SafeToRetry: true}, nil
	}
	return fn(ctx, action, attempt)
}

func (handler *scriptedHandler) dispatchCalls() int {
	handler.mu.Lock()
	defer handler.mu.Unlock()
	return handler.dispatchCount
}

func (handler *scriptedHandler) reconcileCalls() int {
	handler.mu.Lock()
	defer handler.mu.Unlock()
	return handler.reconcileCount
}

func (handler *scriptedHandler) eventsSnapshot() []string {
	handler.mu.Lock()
	defer handler.mu.Unlock()
	return append([]string(nil), handler.events...)
}

type memoryJournal struct {
	mu                 sync.Mutex
	actions            map[string]Action
	plans              map[string]Plan
	attempts           map[string][]Attempt
	effects            map[string][]Effect
	transactionCount   int
	revision           uint64
	baseRevision       uint64
	failCreateEffect   bool
	failUpdateAttempt  *int
	failBarrierUpdate  *int
	renewalCount       int
	failureMu          *sync.Mutex
	markerWrite        *markerWriteFault
	barrierIntentWrite *barrierIntentWriteFault
}

type markerWriteFault struct {
	mu       sync.Mutex
	blocked  bool
	attempts int
}

type barrierIntentWriteFault struct {
	mu       sync.Mutex
	blocked  bool
	attempts int
}

func (journal *memoryJournal) putPlan(plan Plan) {
	journal.mu.Lock()
	defer journal.mu.Unlock()
	journal.plans[plan.ID] = plan
	journal.revision++
}

func (journal *memoryJournal) putAction(action Action) {
	journal.mu.Lock()
	defer journal.mu.Unlock()
	journal.actions[action.ID] = cloneAction(action)
	journal.revision++
}

func (journal *memoryJournal) putAttempt(attempt Attempt) {
	journal.mu.Lock()
	defer journal.mu.Unlock()
	journal.attempts[attempt.ActionRunID] = append(journal.attempts[attempt.ActionRunID], cloneAttempt(attempt))
	journal.revision++
}

func (journal *memoryJournal) putEffect(effect Effect) {
	journal.mu.Lock()
	defer journal.mu.Unlock()
	journal.effects[effect.ActionRunID] = append(journal.effects[effect.ActionRunID], cloneEffect(effect))
	journal.revision++
}

func (journal *memoryJournal) transactionCalls() int {
	journal.mu.Lock()
	defer journal.mu.Unlock()
	return journal.transactionCount
}

func (journal *memoryJournal) leaseRenewals() int {
	journal.mu.Lock()
	defer journal.mu.Unlock()
	return journal.renewalCount
}

func (journal *memoryJournal) failNextUpdateAttempt(count int) {
	if journal.failureMu == nil {
		journal.failureMu = &sync.Mutex{}
	}
	journal.failureMu.Lock()
	journal.failUpdateAttempt = &count
	journal.failureMu.Unlock()
}

func (journal *memoryJournal) failNextBarrierUpdateAttempt(count int) {
	if journal.failureMu == nil {
		journal.failureMu = &sync.Mutex{}
	}
	journal.failureMu.Lock()
	journal.failBarrierUpdate = &count
	journal.failureMu.Unlock()
}

func (journal *memoryJournal) blockDispatchBarrierMarkerWrites(block bool) {
	if journal.markerWrite == nil {
		journal.markerWrite = &markerWriteFault{}
	}
	journal.markerWrite.mu.Lock()
	journal.markerWrite.blocked = block
	journal.markerWrite.mu.Unlock()
}

func (journal *memoryJournal) dispatchBarrierMarkerWriteAttempts() int {
	if journal.markerWrite == nil {
		return 0
	}
	journal.markerWrite.mu.Lock()
	defer journal.markerWrite.mu.Unlock()
	return journal.markerWrite.attempts
}

func (journal *memoryJournal) blockDispatchBarrierIntentWrites(block bool) {
	if journal.barrierIntentWrite == nil {
		journal.barrierIntentWrite = &barrierIntentWriteFault{}
	}
	journal.barrierIntentWrite.mu.Lock()
	journal.barrierIntentWrite.blocked = block
	journal.barrierIntentWrite.mu.Unlock()
}

func (journal *memoryJournal) dispatchBarrierIntentWriteAttempts() int {
	if journal.barrierIntentWrite == nil {
		return 0
	}
	journal.barrierIntentWrite.mu.Lock()
	defer journal.barrierIntentWrite.mu.Unlock()
	return journal.barrierIntentWrite.attempts
}

func (journal *memoryJournal) GetAction(_ context.Context, id string) (Action, error) {
	journal.mu.Lock()
	defer journal.mu.Unlock()
	action, ok := journal.actions[id]
	if !ok {
		return Action{}, ErrNotFound
	}
	return cloneAction(action), nil
}

func (journal *memoryJournal) GetPlan(_ context.Context, id string) (Plan, error) {
	journal.mu.Lock()
	defer journal.mu.Unlock()
	plan, ok := journal.plans[id]
	if !ok {
		return Plan{}, ErrNotFound
	}
	return plan, nil
}

func (journal *memoryJournal) ListDue(_ context.Context, now string, limit int) ([]Action, error) {
	journal.mu.Lock()
	defer journal.mu.Unlock()
	current, err := parseTime(now)
	if err != nil {
		return nil, err
	}
	var actions []Action
	for _, action := range journal.actions {
		if action.State != domain.ActionQueued && action.State != domain.ActionWaitingDependency && action.State != domain.ActionReconciling {
			continue
		}
		if action.CancellationRequestedAt != "" || expired(action.DeadlineAt, current) {
			continue
		}
		if !memoryDue(action.NextAttemptAt, current) || !memoryLeaseAvailable(action, current) {
			continue
		}
		actions = append(actions, cloneAction(action))
	}
	sort.Slice(actions, func(i, j int) bool { return actions[i].ID < actions[j].ID })
	if limit > 0 && len(actions) > limit {
		actions = actions[:limit]
	}
	return actions, nil
}

func (journal *memoryJournal) ListReconciling(_ context.Context, now string, limit int) ([]Action, error) {
	journal.mu.Lock()
	defer journal.mu.Unlock()
	current, err := parseTime(now)
	if err != nil {
		return nil, err
	}
	var actions []Action
	for _, action := range journal.actions {
		if action.State != domain.ActionReconciling || !memoryLeaseAvailable(action, current) {
			continue
		}
		if action.CancellationRequestedAt == "" && !expired(action.DeadlineAt, current) && !memoryDue(action.NextAttemptAt, current) {
			continue
		}
		actions = append(actions, cloneAction(action))
	}
	sort.Slice(actions, func(i, j int) bool { return actions[i].ID < actions[j].ID })
	if limit > 0 && len(actions) > limit {
		actions = actions[:limit]
	}
	return actions, nil
}

func (journal *memoryJournal) Claim(_ context.Context, id string, version int64, workerID, leaseUntil, now string) (Action, error) {
	journal.mu.Lock()
	defer journal.mu.Unlock()
	action, ok := journal.actions[id]
	if !ok || action.Version != version || (action.State != domain.ActionQueued && action.State != domain.ActionWaitingDependency && action.State != domain.ActionReconciling) {
		return Action{}, sql.ErrNoRows
	}
	current, err := parseTime(now)
	if err != nil {
		return Action{}, err
	}
	if action.CancellationRequestedAt != "" || expired(action.DeadlineAt, current) || !memoryDue(action.NextAttemptAt, current) || !memoryLeaseAvailable(action, current) {
		return Action{}, sql.ErrNoRows
	}
	action.State = domain.ActionRunning
	action.ClaimedBy = workerID
	action.LeaseUntil = leaseUntil
	action.Version++
	action.UpdatedAt = now
	journal.actions[id] = cloneAction(action)
	journal.revision++
	return cloneAction(action), nil
}

func (journal *memoryJournal) RenewLease(_ context.Context, fence ClaimFence, leaseUntil, now string) error {
	journal.mu.Lock()
	defer journal.mu.Unlock()
	action, ok := journal.actions[fence.ActionID]
	if !ok || action.State != domain.ActionRunning || action.Version != fence.Version || action.ClaimedBy != fence.WorkerID || action.LeaseUntil != fence.LeaseUntil || action.CancellationRequestedAt != "" {
		return sql.ErrNoRows
	}
	currentNow, err := parseTime(now)
	if err != nil {
		return err
	}
	if expired(action.LeaseUntil, currentNow) {
		return sql.ErrNoRows
	}
	if parsedLease, err := parseTime(leaseUntil); err != nil || parsedLease.IsZero() || !parsedLease.After(currentNow) {
		if err != nil {
			return err
		}
		return sql.ErrNoRows
	}
	action.LeaseUntil = leaseUntil
	action.UpdatedAt = now
	journal.actions[fence.ActionID] = cloneAction(action)
	journal.renewalCount++
	journal.revision++
	return nil
}

func (journal *memoryJournal) RecoverRunning(_ context.Context, now string) ([]Action, error) {
	journal.mu.Lock()
	defer journal.mu.Unlock()
	var recovered []Action
	for id, action := range journal.actions {
		if action.State != domain.ActionRunning && !(action.State == domain.ActionReconciling && action.ClaimedBy != "") {
			continue
		}
		action.State = domain.ActionReconciling
		action.NextAttemptAt = now
		action.ClaimedBy = ""
		action.LeaseUntil = ""
		action.Version++
		action.UpdatedAt = now
		journal.actions[id] = cloneAction(action)
		journal.revision++
		recovered = append(recovered, cloneAction(action))
	}
	return recovered, nil
}

func (journal *memoryJournal) RecoverExpired(ctx context.Context, now string) ([]Action, error) {
	journal.mu.Lock()
	defer journal.mu.Unlock()
	current, err := parseTime(now)
	if err != nil {
		return nil, err
	}
	var recovered []Action
	for id, action := range journal.actions {
		if (action.State != domain.ActionRunning && !(action.State == domain.ActionReconciling && action.ClaimedBy != "")) || action.ClaimedBy == "" || action.LeaseUntil == "" || !expired(action.LeaseUntil, current) {
			continue
		}
		action.State = domain.ActionReconciling
		action.NextAttemptAt = now
		action.ClaimedBy = ""
		action.LeaseUntil = ""
		action.Version++
		action.UpdatedAt = now
		journal.actions[id] = cloneAction(action)
		journal.revision++
		recovered = append(recovered, cloneAction(action))
	}
	return recovered, nil
}

func (journal *memoryJournal) RecoverRunningAttempts(_ context.Context) ([]Attempt, error) {
	journal.mu.Lock()
	defer journal.mu.Unlock()
	var recovered []Attempt
	for id, attempts := range journal.attempts {
		for index := range attempts {
			if attempts[index].State != domain.AttemptRunning {
				continue
			}
			attempts[index].State = domain.AttemptReconciling
			if attempts[index].Phase == AttemptDispatch || attempts[index].OutcomeCertainty == CertaintyUncertain {
				attempts[index].OutcomeCertainty = CertaintyUncertain
			}
			recovered = append(recovered, cloneAttempt(attempts[index]))
		}
		journal.attempts[id] = attempts
		journal.revision++
	}
	return recovered, nil
}

func (journal *memoryJournal) FinalizeCancelled(_ context.Context, now string) ([]Action, error) {
	journal.mu.Lock()
	defer journal.mu.Unlock()
	var finalized []Action
	for id, action := range journal.actions {
		if (action.State != domain.ActionQueued && action.State != domain.ActionWaitingDependency) || action.CancellationRequestedAt == "" {
			continue
		}
		action.State = domain.ActionCancelled
		action.ClaimedBy = ""
		action.LeaseUntil = ""
		action.Version++
		action.UpdatedAt = now
		journal.actions[id] = cloneAction(action)
		journal.revision++
		finalized = append(finalized, cloneAction(action))
	}
	return finalized, nil
}

func (journal *memoryJournal) FinalizeDeadline(_ context.Context, now string) ([]Action, error) {
	journal.mu.Lock()
	defer journal.mu.Unlock()
	current, err := parseTime(now)
	if err != nil {
		return nil, err
	}
	var finalized []Action
	for id, action := range journal.actions {
		if (action.State != domain.ActionQueued && action.State != domain.ActionWaitingDependency) || action.CancellationRequestedAt != "" || !expired(action.DeadlineAt, current) {
			continue
		}
		action.State = domain.ActionDeadlineExceeded
		action.ClaimedBy = ""
		action.LeaseUntil = ""
		action.Version++
		action.UpdatedAt = now
		journal.actions[id] = cloneAction(action)
		journal.revision++
		finalized = append(finalized, cloneAction(action))
	}
	return finalized, nil
}

func (journal *memoryJournal) RequestCancellation(_ context.Context, id, requestedAt string) (Action, error) {
	journal.mu.Lock()
	defer journal.mu.Unlock()
	action, ok := journal.actions[id]
	if !ok {
		return Action{}, ErrNotFound
	}
	if action.State.Terminal() {
		return Action{}, sql.ErrNoRows
	}
	if action.CancellationRequestedAt == "" {
		action.CancellationRequestedAt = requestedAt
		action.Version++
		action.UpdatedAt = requestedAt
		journal.actions[id] = cloneAction(action)
		journal.revision++
	}
	return cloneAction(action), nil
}

func (journal *memoryJournal) ListAttempts(_ context.Context, id string) ([]Attempt, error) {
	journal.mu.Lock()
	defer journal.mu.Unlock()
	attempts := cloneAttempts(journal.attempts[id])
	sort.Slice(attempts, func(i, j int) bool { return attempts[i].AttemptNumber < attempts[j].AttemptNumber })
	return attempts, nil
}

func (journal *memoryJournal) CreateAttempt(_ context.Context, attempt Attempt) (Attempt, error) {
	if err := attempt.validate(); err != nil {
		return Attempt{}, err
	}
	journal.mu.Lock()
	defer journal.mu.Unlock()
	for _, existing := range journal.attempts[attempt.ActionRunID] {
		if existing.ID == attempt.ID || existing.AttemptNumber == attempt.AttemptNumber {
			return Attempt{}, fmt.Errorf("duplicate attempt %s", attempt.ID)
		}
	}
	journal.attempts[attempt.ActionRunID] = append(journal.attempts[attempt.ActionRunID], cloneAttempt(attempt))
	journal.revision++
	return cloneAttempt(attempt), nil
}

func (journal *memoryJournal) UpdateAttempt(_ context.Context, attempt Attempt) (Attempt, error) {
	if err := attempt.validate(); err != nil {
		return Attempt{}, err
	}
	if intentWrite := journal.barrierIntentWrite; intentWrite != nil {
		if _, isBarrierIntent := dispatchBarrierRetryIntentAttempt(attempt.Evidence); isBarrierIntent {
			intentWrite.mu.Lock()
			intentWrite.attempts++
			blocked := intentWrite.blocked
			intentWrite.mu.Unlock()
			if blocked {
				return Attempt{}, errors.New("injected dispatch barrier intent journal failure")
			}
		}
	}
	if journal.failureMu != nil {
		journal.failureMu.Lock()
		if journal.failUpdateAttempt != nil && *journal.failUpdateAttempt > 0 {
			*journal.failUpdateAttempt = *journal.failUpdateAttempt - 1
			journal.failureMu.Unlock()
			return Attempt{}, errors.New("injected attempt journal failure")
		}
		if journal.failBarrierUpdate != nil && *journal.failBarrierUpdate > 0 && attempt.ErrorDetail == "dispatch handler returned after ownership boundary" {
			*journal.failBarrierUpdate = *journal.failBarrierUpdate - 1
			journal.failureMu.Unlock()
			return Attempt{}, errors.New("injected barrier attempt journal failure")
		}
		journal.failureMu.Unlock()
	}
	journal.mu.Lock()
	defer journal.mu.Unlock()
	attempts := journal.attempts[attempt.ActionRunID]
	for index := range attempts {
		if attempts[index].ID != attempt.ID {
			continue
		}
		attempts[index] = cloneAttempt(attempt)
		journal.attempts[attempt.ActionRunID] = attempts
		journal.revision++
		return cloneAttempt(attempt), nil
	}
	return Attempt{}, ErrNotFound
}

func (journal *memoryJournal) ListEffects(_ context.Context, id string) ([]Effect, error) {
	journal.mu.Lock()
	defer journal.mu.Unlock()
	effects := cloneEffects(journal.effects[id])
	sort.Slice(effects, func(i, j int) bool { return effects[i].Ordinal < effects[j].Ordinal })
	return effects, nil
}

func (journal *memoryJournal) CreateEffect(_ context.Context, effect Effect) (Effect, error) {
	if err := effect.validate(); err != nil {
		return Effect{}, err
	}
	journal.mu.Lock()
	defer journal.mu.Unlock()
	if journal.failCreateEffect {
		return Effect{}, errors.New("injected effect journal failure")
	}
	for _, existing := range journal.effects[effect.ActionRunID] {
		if existing.ID == effect.ID || existing.Ordinal == effect.Ordinal {
			return Effect{}, fmt.Errorf("duplicate effect %s", effect.ID)
		}
	}
	if effect.AttemptID != "" {
		found := false
		for _, attempt := range journal.attempts[effect.ActionRunID] {
			if attempt.ID == effect.AttemptID {
				found = true
				break
			}
		}
		if !found {
			return Effect{}, fmt.Errorf("effect attempt %s is not in action", effect.AttemptID)
		}
	}
	journal.effects[effect.ActionRunID] = append(journal.effects[effect.ActionRunID], cloneEffect(effect))
	journal.revision++
	return cloneEffect(effect), nil
}

func (journal *memoryJournal) UpdateEffect(_ context.Context, effect Effect) (Effect, error) {
	if err := effect.validate(); err != nil {
		return Effect{}, err
	}
	journal.mu.Lock()
	defer journal.mu.Unlock()
	effects := journal.effects[effect.ActionRunID]
	for index := range effects {
		if effects[index].ID != effect.ID {
			continue
		}
		effects[index] = cloneEffect(effect)
		journal.effects[effect.ActionRunID] = effects
		journal.revision++
		return cloneEffect(effect), nil
	}
	return Effect{}, ErrNotFound
}

func (journal *memoryJournal) UpdateOutcome(_ context.Context, update OutcomeUpdate) (Action, error) {
	if update.ID == "" || update.Version <= 0 || !update.State.Valid() || !json.Valid(update.Outcome) {
		return Action{}, ErrInvalidJournal
	}
	if markerWrite := journal.markerWrite; markerWrite != nil {
		if _, isBarrierMarker := dispatchBarrierReturnedAttempt(update.Outcome); isBarrierMarker {
			markerWrite.mu.Lock()
			markerWrite.attempts++
			blocked := markerWrite.blocked
			markerWrite.mu.Unlock()
			if blocked {
				return Action{}, errors.New("injected dispatch barrier marker journal failure")
			}
		}
	}
	journal.mu.Lock()
	defer journal.mu.Unlock()
	action, ok := journal.actions[update.ID]
	if !ok || action.Version != update.Version {
		return Action{}, sql.ErrNoRows
	}
	preserved, err := outcomePreservingReservations(action.Outcome, update.Outcome, update.State)
	if err != nil {
		return Action{}, err
	}
	action.State = update.State
	action.NextAttemptAt = update.NextAttemptAt
	action.ClaimedBy = update.ClaimedBy
	action.LeaseUntil = update.LeaseUntil
	action.Outcome = cloneRaw(preserved)
	action.UnresolvedCount = update.UnresolvedCount
	action.Version++
	action.UpdatedAt = update.UpdatedAt
	journal.actions[update.ID] = cloneAction(action)
	journal.revision++
	return cloneAction(action), nil
}

func (journal *memoryJournal) InTx(_ context.Context, fn func(Journal) error) error {
	if fn == nil {
		return errors.New("transaction callback is required")
	}
	journal.mu.Lock()
	transaction := journal.cloneLocked()
	journal.mu.Unlock()
	if err := fn(transaction); err != nil {
		return err
	}
	journal.mu.Lock()
	defer journal.mu.Unlock()
	if journal.revision != transaction.baseRevision {
		return ErrLeaseLost
	}
	journal.actions = transaction.actions
	journal.plans = transaction.plans
	journal.attempts = transaction.attempts
	journal.effects = transaction.effects
	journal.revision++
	journal.transactionCount++
	return nil
}

func (journal *memoryJournal) InTxClaimed(_ context.Context, fence ClaimFence, fn func(Journal) error) error {
	if fn == nil {
		return errors.New("claimed transaction callback is required")
	}
	journal.mu.Lock()
	transaction := journal.cloneLocked()
	journal.mu.Unlock()
	if err := fn(transaction); err != nil {
		return err
	}
	journal.mu.Lock()
	defer journal.mu.Unlock()
	if journal.revision != transaction.baseRevision {
		return ErrLeaseLost
	}
	current, ok := journal.actions[fence.ActionID]
	if !ok || !claimFenceMatches(current, fence) {
		return ErrLeaseLost
	}
	journal.actions = transaction.actions
	journal.plans = transaction.plans
	journal.attempts = transaction.attempts
	journal.effects = transaction.effects
	journal.revision++
	journal.transactionCount++
	return nil
}

func (journal *memoryJournal) Reserve(_ context.Context, actionID string, keys []string) error {
	keys = canonicalReservationKeys(keys)
	var own Action
	ownFound := false
	for id, action := range journal.actions {
		if action.State != domain.ActionRunning && action.State != domain.ActionReconciling && action.State != domain.ActionNeedsReview {
			continue
		}
		candidate, err := reservationKeysFromOutcome(action.Outcome)
		if err != nil {
			return err
		}
		if id == actionID {
			own = action
			ownFound = true
			continue
		}
		for _, requested := range keys {
			for _, held := range candidate {
				if reservationConflicts(requested, held) {
					return fmt.Errorf("%w: %s", ErrReservationConflict, requested)
				}
			}
		}
	}
	if !ownFound {
		return ErrNotFound
	}
	existing, err := reservationKeysFromOutcome(own.Outcome)
	if err != nil {
		return err
	}
	merged, err := outcomeWithReservations(own.Outcome, append(existing, keys...))
	if err != nil {
		return err
	}
	own.Outcome = merged
	journal.actions[actionID] = cloneAction(own)
	journal.revision++
	return nil
}

func (journal *memoryJournal) cloneLocked() *memoryJournal {
	clone := &memoryJournal{actions: make(map[string]Action, len(journal.actions)), plans: make(map[string]Plan, len(journal.plans)), attempts: make(map[string][]Attempt, len(journal.attempts)), effects: make(map[string][]Effect, len(journal.effects)), revision: journal.revision, baseRevision: journal.revision, failCreateEffect: journal.failCreateEffect, failUpdateAttempt: journal.failUpdateAttempt, failBarrierUpdate: journal.failBarrierUpdate, renewalCount: journal.renewalCount, failureMu: journal.failureMu, markerWrite: journal.markerWrite, barrierIntentWrite: journal.barrierIntentWrite}
	for id, action := range journal.actions {
		clone.actions[id] = cloneAction(action)
	}
	for id, plan := range journal.plans {
		clone.plans[id] = plan
	}
	for id, attempts := range journal.attempts {
		clone.attempts[id] = cloneAttempts(attempts)
	}
	for id, effects := range journal.effects {
		clone.effects[id] = cloneEffects(effects)
	}
	return clone
}

func memoryDue(value string, now time.Time) bool {
	if value == "" {
		return true
	}
	parsed, err := parseTime(value)
	return err == nil && !parsed.After(now)
}

func memoryLeaseAvailable(action Action, now time.Time) bool {
	if action.ClaimedBy == "" || action.LeaseUntil == "" {
		return true
	}
	parsed, err := parseTime(action.LeaseUntil)
	return err == nil && !parsed.After(now)
}

func cloneRaw(value json.RawMessage) json.RawMessage {
	return append(json.RawMessage(nil), value...)
}

func cloneAction(action Action) Action {
	action.DesiredState = cloneRaw(action.DesiredState)
	action.Outcome = cloneRaw(action.Outcome)
	return action
}

func cloneAttempt(attempt Attempt) Attempt {
	attempt.Evidence = cloneRaw(attempt.Evidence)
	return attempt
}

func cloneAttempts(attempts []Attempt) []Attempt {
	cloned := make([]Attempt, len(attempts))
	for index, attempt := range attempts {
		cloned[index] = cloneAttempt(attempt)
	}
	return cloned
}

func cloneEffect(effect Effect) Effect {
	effect.Evidence = cloneRaw(effect.Evidence)
	return effect
}

func cloneEffects(effects []Effect) []Effect {
	cloned := make([]Effect, len(effects))
	for index, effect := range effects {
		cloned[index] = cloneEffect(effect)
	}
	return cloned
}

var _ Journal = (*memoryJournal)(nil)
var _ TransactionalJournal = (*memoryJournal)(nil)
var _ FencedTransactionalJournal = (*memoryJournal)(nil)
var _ ReservationJournal = (*memoryJournal)(nil)
var _ Handler = (*scriptedHandler)(nil)
