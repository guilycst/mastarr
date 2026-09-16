package workflows

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/guilycst/mastarr/internal/domain"
	"github.com/guilycst/mastarr/internal/planning"
	"github.com/guilycst/mastarr/internal/ports"
	"github.com/guilycst/mastarr/internal/reviews"
	"github.com/guilycst/mastarr/internal/storage"
	"github.com/guilycst/mastarr/internal/storage/sqlc"
)

var workflowTestNow = time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)

func TestOrderedRegistrationThenImportNeedsSeparateApprovals(t *testing.T) {
	service, reviewService, store := newWorkflowService(t)
	registration := workflowRegistrationPlan(t, "workflow-registration", planning.ApprovalRegistration)
	importPlan := workflowImportPlan(t, "workflow-import")
	for _, plan := range []planning.Plan{registration, importPlan} {
		if _, err := reviewService.StorePlan(context.Background(), reviews.PlanSaveRequest{Plan: plan}); err != nil {
			t.Fatal(err)
		}
	}

	workflow, err := service.Create(context.Background(), CreateRequest{
		Name: "ordered media", Steps: []StepSpec{{ID: "register", PlanID: registration.ID}, {ID: "import", PlanID: importPlan.ID}}, At: workflowTestNow, IdempotencyKey: "create-ordered",
	})
	if err != nil {
		t.Fatal(err)
	}
	if workflow.State != domain.WorkflowAwaitingApproval || workflow.CurrentStep != 0 || len(workflow.Steps) != 2 || workflow.Steps[0].State != domain.StepQueued || workflow.Steps[1].State != domain.StepBlocked {
		t.Fatalf("created workflow = %+v, want ordered approval gate", workflow)
	}

	first, err := service.ApproveStep(context.Background(), ApprovalRequest{WorkflowID: workflow.ID, StepID: "register", Decision: reviews.DecisionApprove, IdempotencyKey: "approve-register", At: workflowTestNow})
	if err != nil {
		t.Fatal(err)
	}
	if first.ActionRunID == "" || first.WorkflowStepID != "register" {
		t.Fatalf("registration approval = %+v, want linked action", first)
	}
	if _, err := service.ApproveStep(context.Background(), ApprovalRequest{WorkflowID: workflow.ID, StepID: "import", Decision: reviews.DecisionApprove, IdempotencyKey: "approve-import-too-early", At: workflowTestNow}); !errors.Is(err, ErrStepBlocked) {
		t.Fatalf("early import approval error = %v, want ErrStepBlocked", err)
	}

	if _, err := store.DB().Exec("UPDATE action_runs SET state = 'succeeded', outcome_json = ?, unresolved_count = 0 WHERE id = ?", `{"outcome":"applied"}`, first.ActionRunID); err != nil {
		t.Fatal(err)
	}
	workflow, err = service.Sync(context.Background(), workflow.ID)
	if err != nil {
		t.Fatal(err)
	}
	if workflow.CurrentStep != 1 || workflow.Steps[0].State != domain.StepSucceeded || workflow.Steps[1].State != domain.StepQueued {
		t.Fatalf("after registration sync = %+v, want import queued", workflow)
	}
	second, err := service.ApproveStep(context.Background(), ApprovalRequest{WorkflowID: workflow.ID, StepID: "import", Decision: reviews.DecisionApprove, IdempotencyKey: "approve-import", At: workflowTestNow})
	if err != nil {
		t.Fatal(err)
	}
	if second.ActionRunID == "" || second.WorkflowStepID != "import" {
		t.Fatalf("import approval = %+v, want second exact action", second)
	}
}

func TestCancelClosesWorkflowAndPreventsLaterDispatch(t *testing.T) {
	service, reviewService, store := newWorkflowService(t)
	plan := workflowRegistrationPlan(t, "workflow-cancel", planning.ApprovalRegistration)
	if _, err := reviewService.StorePlan(context.Background(), reviews.PlanSaveRequest{Plan: plan}); err != nil {
		t.Fatal(err)
	}
	workflow, err := service.Create(context.Background(), CreateRequest{Name: "cancel me", Steps: []StepSpec{{ID: "register", PlanID: plan.ID}}, At: workflowTestNow, IdempotencyKey: "create-cancel"})
	if err != nil {
		t.Fatal(err)
	}
	approval, err := service.ApproveStep(context.Background(), ApprovalRequest{WorkflowID: workflow.ID, StepID: "register", Decision: reviews.DecisionApprove, IdempotencyKey: "approve-cancel", At: workflowTestNow})
	if err != nil {
		t.Fatal(err)
	}
	cancelled, err := service.Cancel(context.Background(), CancelRequest{WorkflowID: workflow.ID, Reason: "operator stopped it", IdempotencyKey: "cancel-1", At: workflowTestNow.Add(time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	if cancelled.State != domain.WorkflowCancelled || cancelled.Steps[0].State != domain.StepCancelled {
		t.Fatalf("cancelled workflow = %+v, want cancelled step", cancelled)
	}
	var cancellation sql.NullString
	if err := store.DB().QueryRow("SELECT cancellation_requested_at FROM action_runs WHERE id = ?", approval.ActionRunID).Scan(&cancellation); err != nil {
		t.Fatal(err)
	}
	if !cancellation.Valid {
		t.Fatal("linked action did not receive cancellation request")
	}
	if _, err := service.ApproveStep(context.Background(), ApprovalRequest{WorkflowID: workflow.ID, StepID: "register", Decision: reviews.DecisionApprove, IdempotencyKey: "approve-after-cancel", At: workflowTestNow.Add(2 * time.Minute)}); !errors.Is(err, ErrWorkflowClosed) {
		t.Fatalf("approval after cancellation error = %v, want ErrWorkflowClosed", err)
	}
	if _, err := service.AddStep(context.Background(), AddStepRequest{WorkflowID: workflow.ID, Step: StepSpec{ID: "later", PlanID: plan.ID}, IdempotencyKey: "append-after-cancel", At: workflowTestNow.Add(2 * time.Minute)}); !errors.Is(err, ErrWorkflowClosed) {
		t.Fatalf("append after cancellation error = %v, want ErrWorkflowClosed", err)
	}
}

func TestApprovedWorkflowActionDeadlineFencesSQLiteDueAndClaim(t *testing.T) {
	service, reviewService, store := newWorkflowService(t)
	plan := workflowRegistrationPlan(t, "workflow-action-deadline", planning.ApprovalRegistration)
	if _, err := reviewService.StorePlan(context.Background(), reviews.PlanSaveRequest{Plan: plan}); err != nil {
		t.Fatal(err)
	}
	deadline := workflowTestNow.Add(time.Minute)
	workflow, err := service.Create(context.Background(), CreateRequest{
		Name: "deadline-bound action", Steps: []StepSpec{{ID: "register", PlanID: plan.ID}},
		DeadlineAt: deadline, At: workflowTestNow, IdempotencyKey: "create-action-deadline",
	})
	if err != nil {
		t.Fatal(err)
	}
	approval, err := service.ApproveStep(context.Background(), ApprovalRequest{
		WorkflowID: workflow.ID, StepID: "register", Decision: reviews.DecisionApprove,
		IdempotencyKey: "approve-action-deadline", At: workflowTestNow,
	})
	if err != nil {
		t.Fatal(err)
	}
	var actionDeadline sql.NullString
	if err := store.DB().QueryRow("SELECT deadline_at FROM action_runs WHERE id = ?", approval.ActionRunID).Scan(&actionDeadline); err != nil {
		t.Fatal(err)
	}
	if !actionDeadline.Valid || actionDeadline.String != deadline.Format(time.RFC3339Nano) {
		t.Fatalf("action deadline = %#v, want workflow deadline %s", actionDeadline, deadline.Format(time.RFC3339Nano))
	}

	expiredAt := deadline.Add(time.Second).Format(time.RFC3339Nano)
	due, err := store.Queries().ListDueActionRuns(context.Background(), &sqlc.ListDueActionRunsParams{
		Now: sql.NullString{String: expiredAt, Valid: true}, Limit: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(due) != 0 {
		t.Fatalf("due action runs after workflow deadline = %d, want zero", len(due))
	}
	_, err = store.Queries().ClaimActionRun(context.Background(), &sqlc.ClaimActionRunParams{
		WorkerID:   sql.NullString{String: "worker-after-deadline", Valid: true},
		LeaseUntil: sql.NullString{String: deadline.Add(time.Minute).Format(time.RFC3339Nano), Valid: true},
		Now:        expiredAt, ID: approval.ActionRunID, Version: 1,
	})
	if !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("claim after workflow deadline error = %v, want sql.ErrNoRows", err)
	}
}

func TestRejectedReviewAtomicallyProjectsBlockedStepAcrossRestart(t *testing.T) {
	service, reviewService, store := newWorkflowService(t)
	plan := workflowRegistrationPlan(t, "workflow-rejection", planning.ApprovalRegistration)
	if _, err := reviewService.StorePlan(context.Background(), reviews.PlanSaveRequest{Plan: plan}); err != nil {
		t.Fatal(err)
	}
	workflow, err := service.Create(context.Background(), CreateRequest{
		Name: "rejected registration", Steps: []StepSpec{{ID: "register", PlanID: plan.ID}},
		At: workflowTestNow, IdempotencyKey: "create-rejection",
	})
	if err != nil {
		t.Fatal(err)
	}
	request := reviews.ApprovalRequest{
		PlanID: plan.ID, Revision: plan.Revision, Digest: plan.Digest,
		Decision: reviews.DecisionReject, Reason: "needs a narrower scope", Actor: "operator", CallerLabel: "review-console",
		IdempotencyKey: "reject-registration", At: workflowTestNow,
		WorkflowID: workflow.ID, WorkflowStepID: "register",
	}
	decision, err := reviewService.Approve(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	var state, outcome string
	if err := store.DB().QueryRow("SELECT state, outcome_json FROM workflow_steps WHERE id = ?", "register").Scan(&state, &outcome); err != nil {
		t.Fatal(err)
	}
	if state != string(domain.StepBlocked) {
		t.Fatalf("durable rejected step state = %q, want %q", state, domain.StepBlocked)
	}
	var metadata map[string]json.RawMessage
	if err := json.Unmarshal([]byte(outcome), &metadata); err != nil {
		t.Fatal(err)
	}
	var recordedDecision, recordedReason string
	if err := json.Unmarshal(metadata["decision"], &recordedDecision); err != nil || recordedDecision != string(reviews.DecisionReject) {
		t.Fatalf("durable rejection decision = %q, want %q", recordedDecision, reviews.DecisionReject)
	}
	if err := json.Unmarshal(metadata["decisionReason"], &recordedReason); err != nil || recordedReason != "needs a narrower scope" {
		t.Fatalf("durable rejection reason = %q, want persisted reason", recordedReason)
	}
	var recordedActor, recordedCaller string
	if err := json.Unmarshal(metadata["decisionActor"], &recordedActor); err != nil || recordedActor != request.Actor {
		t.Fatalf("durable rejection actor = %q, want persisted actor", recordedActor)
	}
	if err := json.Unmarshal(metadata["decisionCallerLabel"], &recordedCaller); err != nil || recordedCaller != request.CallerLabel {
		t.Fatalf("durable rejection caller = %q, want persisted caller", recordedCaller)
	}
	persistedDecision, err := store.Queries().GetReviewDecision(context.Background(), decision.DecisionID)
	if err != nil {
		t.Fatal(err)
	}
	if persistedDecision.Actor != request.Actor || persistedDecision.CallerLabel.String != request.CallerLabel || !persistedDecision.CallerLabel.Valid || persistedDecision.Reason.String != request.Reason || !persistedDecision.Reason.Valid {
		t.Fatalf("durable review attribution = %+v, want actor/caller/reason from first decision", persistedDecision)
	}
	var actionCount int
	if err := store.DB().QueryRow("SELECT count(*) FROM action_runs WHERE id = ?", decision.ActionRunID).Scan(&actionCount); err != nil {
		t.Fatal(err)
	}
	if actionCount != 0 || decision.ActionRunID != "" {
		t.Fatalf("rejected decision action = %q/count %d, want no action", decision.ActionRunID, actionCount)
	}
	var storedWorkflowState string
	if err := store.DB().QueryRow("SELECT state FROM workflow_runs WHERE id = ?", workflow.ID).Scan(&storedWorkflowState); err != nil {
		t.Fatal(err)
	}
	if storedWorkflowState != string(domain.WorkflowRunning) {
		t.Fatalf("durable rejection workflow state = %q, want valid running bridge before projection", storedWorkflowState)
	}
	initialOutcome := outcome

	// A fresh idempotency key is a semantic replay, so matching immutable
	// attribution must preserve the already-persisted workflow evidence.
	replayRequest := request
	replayRequest.IdempotencyKey = "reject-registration-replay"
	replayed, err := reviewService.Approve(context.Background(), replayRequest)
	if err != nil {
		t.Fatalf("matching fresh-key rejection replay error = %v", err)
	}
	if !replayed.Replayed || replayed.DecisionID != decision.DecisionID || replayed.ActionRunID != "" {
		t.Fatalf("matching fresh-key rejection replay = %+v, want replayed decision without action", replayed)
	}
	var replayOutcome string
	if err := store.DB().QueryRow("SELECT outcome_json FROM workflow_steps WHERE id = ?", request.WorkflowStepID).Scan(&replayOutcome); err != nil {
		t.Fatal(err)
	}
	if replayOutcome != initialOutcome {
		t.Fatalf("matching fresh-key replay rewrote workflow evidence = %s, want %s", replayOutcome, initialOutcome)
	}
	for _, testCase := range []struct {
		name   string
		mutate func(*reviews.ApprovalRequest)
	}{
		{name: "actor", mutate: func(candidate *reviews.ApprovalRequest) { candidate.Actor = "another-operator" }},
		{name: "caller", mutate: func(candidate *reviews.ApprovalRequest) { candidate.CallerLabel = "other-console" }},
		{name: "reason", mutate: func(candidate *reviews.ApprovalRequest) { candidate.Reason = "rewritten" }},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			candidate := request
			candidate.IdempotencyKey = "reject-registration-conflict-" + testCase.name
			testCase.mutate(&candidate)
			if _, err := reviewService.Approve(context.Background(), candidate); !errors.Is(err, reviews.ErrDecisionConflict) {
				t.Fatalf("conflicting fresh-key %s replay error = %v, want reviews.ErrDecisionConflict", testCase.name, err)
			}
		})
	}

	// A fresh service instance must observe the committed rejection without
	// relying on a client retry or a second post-decision transaction.
	restartedReviews, err := reviews.NewWithOptions(reviews.Options{Store: store, Now: func() time.Time { return workflowTestNow }})
	if err != nil {
		t.Fatal(err)
	}
	restarted, err := NewWithOptions(Options{Store: store, Reviews: restartedReviews, Now: func() time.Time { return workflowTestNow }})
	if err != nil {
		t.Fatal(err)
	}
	projected, err := restarted.Sync(context.Background(), workflow.ID)
	if err != nil {
		t.Fatal(err)
	}
	if projected.Steps[0].State != domain.StepBlocked || projected.State != domain.WorkflowNeedsReview {
		t.Fatalf("restarted rejection projection = state %q/step %q, want needs_review/blocked", projected.State, projected.Steps[0].State)
	}
	if _, err := restarted.ApproveStep(context.Background(), ApprovalRequest{
		WorkflowID: workflow.ID, StepID: "register", Decision: reviews.DecisionApprove,
		IdempotencyKey: "approve-after-rejection", At: workflowTestNow,
	}); !errors.Is(err, reviews.ErrDecisionConflict) {
		t.Fatalf("approval after durable rejection error = %v, want reviews.ErrDecisionConflict", err)
	}
}

func TestAddStepUsesTrustedClockForDeadlineAndCallerTimestamp(t *testing.T) {
	service, reviewService, _ := newWorkflowService(t)
	first := workflowRegistrationPlan(t, "workflow-clock-first", planning.ApprovalRegistration)
	second := workflowRegistrationPlan(t, "workflow-clock-second", planning.ApprovalRegistration)
	expiring := workflowRegistrationPlanAt(t, "workflow-clock-expiring", planning.ApprovalRegistration, workflowTestNow, workflowTestNow.Add(time.Minute))
	for _, plan := range []planning.Plan{first, second, expiring} {
		if _, err := reviewService.StorePlan(context.Background(), reviews.PlanSaveRequest{Plan: plan}); err != nil {
			t.Fatal(err)
		}
	}
	deadline := workflowTestNow.Add(time.Minute)
	workflow, err := service.Create(context.Background(), CreateRequest{
		Name: "trusted lifecycle clock", Steps: []StepSpec{{ID: "first-expired", PlanID: first.ID}},
		DeadlineAt: deadline, At: workflowTestNow, IdempotencyKey: "create-trusted-clock",
	})
	if err != nil {
		t.Fatal(err)
	}
	service.now = func() time.Time { return deadline.Add(time.Minute) }
	if _, err := service.AddStep(context.Background(), AddStepRequest{
		WorkflowID: workflow.ID, Step: StepSpec{ID: "backdated", PlanID: second.ID},
		IdempotencyKey: "append-backdated", At: workflowTestNow,
	}); !errors.Is(err, ErrWorkflowClosed) {
		t.Fatalf("backdated append after trusted deadline error = %v, want ErrWorkflowClosed", err)
	}

	service.now = func() time.Time { return workflowTestNow }
	openWorkflow, err := service.Create(context.Background(), CreateRequest{
		Name: "future timestamp", Steps: []StepSpec{{ID: "first-open", PlanID: first.ID}},
		At: workflowTestNow, IdempotencyKey: "create-future-timestamp",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.AddStep(context.Background(), AddStepRequest{
		WorkflowID: openWorkflow.ID, Step: StepSpec{ID: "future", PlanID: second.ID},
		IdempotencyKey: "append-future", At: workflowTestNow.Add(time.Second),
	}); !errors.Is(err, ErrInvalidRecipe) {
		t.Fatalf("future append timestamp error = %v, want ErrInvalidRecipe", err)
	}
	service.now = func() time.Time { return workflowTestNow.Add(2 * time.Minute) }
	if _, err := service.AddStep(context.Background(), AddStepRequest{
		WorkflowID: openWorkflow.ID, Step: StepSpec{ID: "expired-plan", PlanID: expiring.ID},
		IdempotencyKey: "append-expired-plan", At: workflowTestNow,
	}); !errors.Is(err, ErrPrerequisite) {
		t.Fatalf("append with expired plan and backdated timestamp error = %v, want ErrPrerequisite", err)
	}
}

func TestAddStepUpdatesRecipeAndReplaysIdempotently(t *testing.T) {
	service, reviewService, _ := newWorkflowService(t)
	first := workflowRegistrationPlan(t, "workflow-add-first", planning.ApprovalRegistration)
	second := workflowRegistrationPlan(t, "workflow-add-second", planning.ApprovalRegistration)
	for _, plan := range []planning.Plan{first, second} {
		if _, err := reviewService.StorePlan(context.Background(), reviews.PlanSaveRequest{Plan: plan}); err != nil {
			t.Fatal(err)
		}
	}
	workflow, err := service.Create(context.Background(), CreateRequest{Name: "append step", Steps: []StepSpec{{ID: "first", PlanID: first.ID}}, At: workflowTestNow, IdempotencyKey: "create-append"})
	if err != nil {
		t.Fatal(err)
	}
	request := AddStepRequest{WorkflowID: workflow.ID, Step: StepSpec{ID: "second", PlanID: second.ID}, IdempotencyKey: "append-second", At: workflowTestNow}
	appended, err := service.AddStep(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if len(appended.Steps) != 2 || appended.Steps[1].ID != "second" {
		t.Fatalf("appended workflow = %+v, want persisted second step", appended)
	}
	replayed, err := service.AddStep(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if !replayed.Replayed || len(replayed.Steps) != 2 {
		t.Fatalf("replayed append = %+v, want same durable workflow", replayed)
	}
	request.Step.PlanID = first.ID
	if _, err := service.AddStep(context.Background(), request); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("changed append error = %v, want ErrIdempotencyConflict", err)
	}
}

func TestSyncDeadlineCancelsUnapprovedSteps(t *testing.T) {
	service, reviewService, _ := newWorkflowService(t)
	plan := workflowRegistrationPlan(t, "workflow-deadline", planning.ApprovalRegistration)
	if _, err := reviewService.StorePlan(context.Background(), reviews.PlanSaveRequest{Plan: plan}); err != nil {
		t.Fatal(err)
	}
	workflow, err := service.Create(context.Background(), CreateRequest{Name: "deadline", Steps: []StepSpec{{ID: "register", PlanID: plan.ID}}, DeadlineAt: workflowTestNow.Add(time.Minute), At: workflowTestNow, IdempotencyKey: "create-deadline"})
	if err != nil {
		t.Fatal(err)
	}
	service.now = func() time.Time { return workflowTestNow.Add(2 * time.Minute) }
	expired, err := service.Sync(context.Background(), workflow.ID)
	if err != nil {
		t.Fatal(err)
	}
	if expired.State != domain.WorkflowDeadlineExceeded || expired.Steps[0].State != domain.StepCancelled {
		t.Fatalf("expired workflow = %+v, want deadline and cancelled step", expired)
	}
}

func TestDecodeRejectsTrailingJSON(t *testing.T) {
	if _, err := decodeMetadata(`{} {}`); err == nil {
		t.Fatal("trailing metadata JSON was accepted")
	}
}

func newWorkflowService(t *testing.T) (*Service, *reviews.Service, *storage.Store) {
	t.Helper()
	store, err := storage.Open(filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.DB().Exec(`INSERT INTO config_snapshots (id, source, document_id, revision, startup_at, effective_json, created_at) VALUES ('snapshot-test', 'api', 'workflow-test', '1', ?, '{}', ?), ('snapshot-test-2', 'api', 'workflow-test-2', '1', ?, '{}', ?)`, workflowTestNow.Format(time.RFC3339Nano), workflowTestNow.Format(time.RFC3339Nano), workflowTestNow.Format(time.RFC3339Nano), workflowTestNow.Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB().Exec(`INSERT INTO storage_roots (id, label, purpose, path, source, source_snapshot_id, revision, capabilities_json, created_at, updated_at) VALUES ('downloads', 'Downloads', 'download', '/downloads', 'api', 'snapshot-test', '1', '[]', ?, ?), ('library', 'Library', 'library', '/library', 'api', 'snapshot-test-2', '1', '[]', ?, ?)`, workflowTestNow.Format(time.RFC3339Nano), workflowTestNow.Format(time.RFC3339Nano), workflowTestNow.Format(time.RFC3339Nano), workflowTestNow.Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	reviewService, err := reviews.NewWithOptions(reviews.Options{Store: store, Now: func() time.Time { return workflowTestNow }})
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewWithOptions(Options{Store: store, Reviews: reviewService, Now: func() time.Time { return workflowTestNow }})
	if err != nil {
		t.Fatal(err)
	}
	return service, reviewService, store
}

func workflowRegistrationPlan(t *testing.T, id string, gate planning.ApprovalKind) planning.Plan {
	return workflowRegistrationPlanAt(t, id, gate, workflowTestNow, workflowTestNow.Add(time.Hour))
}

func workflowRegistrationPlanAt(t *testing.T, id string, gate planning.ApprovalKind, createdAt, expiresAt time.Time) planning.Plan {
	t.Helper()
	connection := domain.ConfigID("sonarr-main")
	monitored := false
	desired, err := planning.NewDesiredState(planning.NewRegistrationPredicate(connection, "tvdb:123", domain.MediaEpisode, "", ports.RegistrationFields{RootFolder: "/library", QualityProfileID: "1", Monitored: &monitored, SeriesType: "standard"}))
	if err != nil {
		t.Fatal(err)
	}
	plan, err := planning.Build(planning.Request{ID: id, Action: domain.ActionArrRegistration, Desired: desired, Binding: planning.Binding{SourceID: "discovery-1", SourceRevision: "coverage-1", ConnectionRevisions: map[domain.ConfigID]string{connection: "config-1"}}, Preconditions: []planning.Precondition{{Kind: "source_identity", Target: "discovery-1", Expected: "coverage-1", Required: true}}, RequiredApproval: gate, CreatedAt: createdAt, ExpiresAt: expiresAt})
	if err != nil {
		t.Fatal(err)
	}
	return plan
}

func workflowImportPlan(t *testing.T, id string) planning.Plan {
	t.Helper()
	connection := domain.ConfigID("sonarr-main")
	root := domain.ConfigID("downloads")
	source := domain.FileTarget{RootID: root, RelativePath: "incoming/episode.mkv"}
	manifest := []domain.FileManifestEntry{{RootID: root, RelativePath: source.RelativePath, Type: domain.ManifestFile, Size: 10, Digest: "sha256:" + strings.Repeat("a", 64), FileIdentity: "inode-1", Role: domain.RoleVideo, ObservedAt: workflowTestNow}}
	desired, err := planning.NewDesiredState(planning.NewImportPredicate(connection, "series-1", []planning.ImportSelection{{Source: source, EpisodeIDs: []string{"episode-1"}, Confidence: planning.MappingExact}}, "copy"))
	if err != nil {
		t.Fatal(err)
	}
	plan, err := planning.Build(planning.Request{ID: id, Action: domain.ActionArrImport, Desired: desired, Manifest: manifest, Binding: planning.Binding{SourceID: "discovery-1", SourceRevision: "coverage-1", ConnectionRevisions: map[domain.ConfigID]string{connection: "config-1"}, MappingRevisions: map[domain.ConfigID]string{"mapping-main": "mapping-1"}, MappingScopes: []planning.MappingScope{{MappingID: "mapping-main", ConnectionID: connection, RootID: root}}}, Preconditions: []planning.Precondition{{Kind: "source_identity", Target: "discovery-1", Expected: "coverage-1", Required: true}}, RequiredApproval: planning.ApprovalImport, CreatedAt: workflowTestNow, ExpiresAt: workflowTestNow.Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	return plan
}
