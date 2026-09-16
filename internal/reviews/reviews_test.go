package reviews

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/guilycst/mastarr/internal/domain"
	"github.com/guilycst/mastarr/internal/planning"
	"github.com/guilycst/mastarr/internal/ports"
	"github.com/guilycst/mastarr/internal/storage"
)

var reviewFixtureNow = time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)

func TestApprovePersistsDecisionAndActionAtomically(t *testing.T) {
	service, store := newReviewService(t, reviewFixtureNow)
	plan := registrationPlan(t, "register-atomic", reviewFixtureNow, reviewFixtureNow.Add(15*time.Minute), planning.ApprovalRegistration)
	if _, err := service.StorePlan(context.Background(), PlanSaveRequest{Plan: plan}); err != nil {
		t.Fatal(err)
	}

	result, err := service.Approve(context.Background(), ApprovalRequest{
		PlanID: plan.ID, Revision: plan.Revision, Digest: plan.Digest, Decision: DecisionApprove,
		IdempotencyScope: "review-test", IdempotencyKey: "atomic-1", At: reviewFixtureNow,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Decision != DecisionApprove || result.ActionRunID == "" || result.Replayed {
		t.Fatalf("result = %+v, want new approved action", result)
	}
	decision, err := store.Queries().GetReviewDecision(context.Background(), result.DecisionID)
	if err != nil {
		t.Fatal(err)
	}
	if decision.PlanID != plan.ID || decision.PlanRevision != plan.Revision || decision.PlanDigest != plan.Digest || decision.Actor != "unauthenticated" {
		t.Fatalf("decision = %+v, want exact binding and default actor", decision)
	}
	action, err := store.Queries().GetActionRun(context.Background(), result.ActionRunID)
	if err != nil {
		t.Fatal(err)
	}
	if action.State != string(domain.ActionQueued) || action.PlanID != plan.ID || action.PlanDigest != plan.Digest {
		t.Fatalf("action = %+v, want queued exact plan", action)
	}
	var decisions, actions int
	if err := store.DB().QueryRow("SELECT count(*) FROM review_decisions WHERE plan_id = ?", plan.ID).Scan(&decisions); err != nil {
		t.Fatal(err)
	}
	if err := store.DB().QueryRow("SELECT count(*) FROM action_runs WHERE plan_id = ?", plan.ID).Scan(&actions); err != nil {
		t.Fatal(err)
	}
	if decisions != 1 || actions != 1 {
		t.Fatalf("durable counts = decisions:%d actions:%d, want 1/1", decisions, actions)
	}
}

func TestApproveSameKeyReplaysAndChangedPayloadConflicts(t *testing.T) {
	service, store := newReviewService(t, reviewFixtureNow)
	plan := registrationPlan(t, "register-idempotent", reviewFixtureNow, reviewFixtureNow.Add(time.Hour), planning.ApprovalRegistration)
	if _, err := service.StorePlan(context.Background(), PlanSaveRequest{Plan: plan}); err != nil {
		t.Fatal(err)
	}
	request := ApprovalRequest{
		PlanID: plan.ID, Revision: plan.Revision, Digest: plan.Digest, Decision: DecisionApprove,
		IdempotencyScope: "review-test", IdempotencyKey: "same-key", At: reviewFixtureNow,
	}
	first, err := service.Approve(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.Approve(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if !second.Replayed || second.DecisionID != first.DecisionID || second.ActionRunID != first.ActionRunID {
		t.Fatalf("replay = %+v, first = %+v", second, first)
	}
	changed := request
	changed.Actor = "different-label"
	if _, err := service.Approve(context.Background(), changed); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("changed idempotency payload error = %v, want ErrIdempotencyConflict", err)
	}
	var count int
	if err := store.DB().QueryRow("SELECT count(*) FROM review_decisions WHERE plan_id = ?", plan.ID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("decision count = %d, want one after replay/conflict", count)
	}
}

func TestApproveConcurrentSameIntentCreatesOneDecision(t *testing.T) {
	service, store := newReviewService(t, reviewFixtureNow)
	plan := registrationPlan(t, "register-race", reviewFixtureNow, reviewFixtureNow.Add(time.Hour), planning.ApprovalRegistration)
	if _, err := service.StorePlan(context.Background(), PlanSaveRequest{Plan: plan}); err != nil {
		t.Fatal(err)
	}
	request := ApprovalRequest{
		PlanID: plan.ID, Revision: plan.Revision, Digest: plan.Digest, Decision: DecisionApprove,
		IdempotencyScope: "review-race", IdempotencyKey: "race-key", At: reviewFixtureNow,
	}
	results := make([]ApprovalResult, 2)
	errs := make([]error, 2)
	var group sync.WaitGroup
	for index := range results {
		group.Add(1)
		go func(index int) {
			defer group.Done()
			results[index], errs[index] = service.Approve(context.Background(), request)
		}(index)
	}
	group.Wait()
	for index, err := range errs {
		if err != nil {
			t.Fatalf("approval %d error = %v", index, err)
		}
	}
	if results[0].DecisionID != results[1].DecisionID || results[0].ActionRunID != results[1].ActionRunID {
		t.Fatalf("concurrent results differ: %#v %#v", results[0], results[1])
	}
	var decisions, actions int
	if err := store.DB().QueryRow("SELECT count(*) FROM review_decisions WHERE plan_id = ?", plan.ID).Scan(&decisions); err != nil {
		t.Fatal(err)
	}
	if err := store.DB().QueryRow("SELECT count(*) FROM action_runs WHERE plan_id = ?", plan.ID).Scan(&actions); err != nil {
		t.Fatal(err)
	}
	if decisions != 1 || actions != 1 {
		t.Fatalf("concurrent durable counts = decisions:%d actions:%d, want 1/1", decisions, actions)
	}
}

func TestApproveRejectsExpiredForgedAndBlanketRequestsBeforeAction(t *testing.T) {
	service, store := newReviewService(t, reviewFixtureNow.Add(2*time.Hour))
	expired := registrationPlan(t, "register-expired", reviewFixtureNow, reviewFixtureNow.Add(time.Hour), planning.ApprovalRegistration)
	if _, err := service.StorePlan(context.Background(), PlanSaveRequest{Plan: expired}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Approve(context.Background(), ApprovalRequest{
		PlanID: expired.ID, Revision: expired.Revision, Digest: expired.Digest, Decision: DecisionApprove,
		IdempotencyScope: "review-test", IdempotencyKey: "expired", At: reviewFixtureNow,
	}); !errors.Is(err, ErrPlanExpired) {
		t.Fatalf("expired approval error = %v, want ErrPlanExpired", err)
	}
	if _, err := service.Approve(context.Background(), ApprovalRequest{
		PlanID: expired.ID, Revision: expired.Revision, Digest: "sha256:" + strings.Repeat("0", 64), Decision: DecisionApprove,
		IdempotencyScope: "review-test", IdempotencyKey: "forged", At: reviewFixtureNow,
	}); !errors.Is(err, ErrPlanDigest) {
		t.Fatalf("forged digest error = %v, want ErrPlanDigest", err)
	}
	blanket := registrationPlan(t, "register-blanket", reviewFixtureNow, reviewFixtureNow.Add(time.Hour), planning.ApprovalNone)
	if _, err := service.StorePlan(context.Background(), PlanSaveRequest{Plan: blanket}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Approve(context.Background(), ApprovalRequest{
		PlanID: blanket.ID, Revision: blanket.Revision, Digest: blanket.Digest, Decision: DecisionApprove,
		IdempotencyScope: "review-test", IdempotencyKey: "blanket", At: reviewFixtureNow,
	}); !errors.Is(err, ErrBlanketApproval) {
		t.Fatalf("blanket approval error = %v, want ErrBlanketApproval", err)
	}
	var actions int
	if err := store.DB().QueryRow("SELECT count(*) FROM action_runs").Scan(&actions); err != nil {
		t.Fatal(err)
	}
	if actions != 0 {
		t.Fatalf("blocked approvals created %d action runs", actions)
	}
}

func TestApproveRequiresAtLeastOneRequiredPrecondition(t *testing.T) {
	service, store := newReviewService(t, reviewFixtureNow)
	plan := registrationPlan(t, "registration-no-precondition", reviewFixtureNow, reviewFixtureNow.Add(time.Hour), planning.ApprovalRegistration)
	plan.Preconditions = nil
	// Rebuild the digest after changing the authority-bearing preconditions so
	// the persisted plan remains a valid planning.Plan while the review gate
	// can reject the missing execution fence explicitly.
	rebuilt, err := planning.Build(planning.Request{
		ID: plan.ID, Action: plan.Action, Desired: plan.Desired, Binding: plan.Binding,
		RequiredApproval: plan.RequiredApproval, CreatedAt: plan.CreatedAt, ExpiresAt: plan.ExpiresAt,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.StorePlan(context.Background(), PlanSaveRequest{Plan: rebuilt}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Approve(context.Background(), ApprovalRequest{
		PlanID: rebuilt.ID, Revision: rebuilt.Revision, Digest: rebuilt.Digest, Decision: DecisionApprove,
		IdempotencyScope: "precondition-test", IdempotencyKey: "missing", At: reviewFixtureNow,
	}); !errors.Is(err, ErrPrerequisite) {
		t.Fatalf("missing precondition error = %v, want ErrPrerequisite", err)
	}
	var actions int
	if err := store.DB().QueryRow("SELECT count(*) FROM action_runs").Scan(&actions); err != nil {
		t.Fatal(err)
	}
	if actions != 0 {
		t.Fatalf("missing precondition created %d action runs", actions)
	}
}

func TestStorePlanRejectsMissingPreconditionAndOversizedIntentBeforeWrite(t *testing.T) {
	service, store := newReviewService(t, reviewFixtureNow)
	invalid := registrationPlan(t, "register-invalid-precondition", reviewFixtureNow, reviewFixtureNow.Add(time.Hour), planning.ApprovalRegistration)
	invalid.Preconditions = []planning.Precondition{{Kind: "source_identity", Target: "incoming/file.mkv"}}
	if _, err := service.StorePlan(context.Background(), PlanSaveRequest{Plan: invalid}); err == nil {
		t.Fatal("missing precondition was accepted")
	}
	var plans int
	if err := store.DB().QueryRow("SELECT count(*) FROM action_plans").Scan(&plans); err != nil {
		t.Fatal(err)
	}
	if plans != 0 {
		t.Fatalf("invalid plan created %d durable plans", plans)
	}

	valid := registrationPlan(t, "register-oversized", reviewFixtureNow, reviewFixtureNow.Add(time.Hour), planning.ApprovalRegistration)
	small, err := NewWithOptions(Options{Store: store, Now: func() time.Time { return reviewFixtureNow }, MaxInputBytes: 256})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := small.StorePlan(context.Background(), PlanSaveRequest{Plan: valid, DesiredState: []byte(`{"payload":"` + strings.Repeat("x", 300) + `"}`)}); !errors.Is(err, ErrInvalidDecision) {
		t.Fatalf("oversized intent error = %v, want ErrInvalidDecision", err)
	}
	if err := store.DB().QueryRow("SELECT count(*) FROM action_plans").Scan(&plans); err != nil {
		t.Fatal(err)
	}
	if plans != 0 {
		t.Fatalf("oversized intent created %d durable plans", plans)
	}
}

func TestStorePlanAppendsImmutableRevisionAndRejectsStaleApproval(t *testing.T) {
	service, store := newReviewService(t, reviewFixtureNow)
	first := registrationPlan(t, "registration-revisions", reviewFixtureNow, reviewFixtureNow.Add(time.Hour), planning.ApprovalRegistration)
	if _, err := service.StorePlan(context.Background(), PlanSaveRequest{Plan: first}); err != nil {
		t.Fatal(err)
	}
	second, err := planning.NewRevision(first, planning.Request{
		Action: first.Action, Desired: first.Desired, Binding: planning.Binding{
			SourceID: "discovery-1", SourceRevision: "coverage-2", ConnectionRevisions: map[domain.ConfigID]string{"sonarr-main": "config-2"},
		}, Preconditions: first.Preconditions, RequiredApproval: first.RequiredApproval, CreatedAt: first.CreatedAt, ExpiresAt: first.ExpiresAt,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.StorePlan(context.Background(), PlanSaveRequest{Plan: second}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.GetPlan(context.Background(), first.ID, first.Revision); err != nil {
		t.Fatalf("load immutable first revision: %v", err)
	}
	if _, err := service.Approve(context.Background(), ApprovalRequest{
		PlanID: first.ID, Revision: first.Revision, Digest: first.Digest, Decision: DecisionApprove,
		IdempotencyScope: "revision-test", IdempotencyKey: "stale", At: reviewFixtureNow,
	}); !errors.Is(err, ErrPlanRevision) {
		t.Fatalf("stale approval error = %v, want ErrPlanRevision", err)
	}
	if _, err := service.Approve(context.Background(), ApprovalRequest{
		PlanID: second.ID, Revision: second.Revision, Digest: second.Digest, Decision: DecisionApprove,
		IdempotencyScope: "revision-test", IdempotencyKey: "current", At: reviewFixtureNow,
	}); err != nil {
		t.Fatal(err)
	}
	var revisions int
	if err := store.DB().QueryRow("SELECT count(*) FROM action_plan_revisions WHERE plan_id = ?", first.ID).Scan(&revisions); err != nil {
		t.Fatal(err)
	}
	if revisions != 2 {
		t.Fatalf("revision count = %d, want two immutable rows", revisions)
	}
}

func TestApprovalGateNeverReusesRegistrationForImport(t *testing.T) {
	registration := registrationPlan(t, "registration-gate", reviewFixtureNow, reviewFixtureNow.Add(time.Hour), planning.ApprovalRegistration)
	if err := validateApprovalGate(registration); err != nil {
		t.Fatalf("registration gate = %v", err)
	}
	importPlan := registration
	importPlan.Action = domain.ActionArrImport
	importPlan.RequiredApproval = planning.ApprovalRegistration
	if !errors.Is(validateApprovalGate(importPlan), ErrBlanketApproval) {
		t.Fatalf("import reused registration gate: %v", validateApprovalGate(importPlan))
	}
}

func newReviewService(t *testing.T, now time.Time) (*Service, *storage.Store) {
	t.Helper()
	store, err := storage.Open(filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	service, err := NewWithOptions(Options{Store: store, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	return service, store
}

func registrationPlan(t *testing.T, id string, createdAt, expiresAt time.Time, gate planning.ApprovalKind) planning.Plan {
	t.Helper()
	connection := domain.ConfigID("sonarr-main")
	monitored := false
	desired, err := planning.NewDesiredState(planning.NewRegistrationPredicate(
		connection, "tvdb:123", domain.MediaEpisode, "",
		ports.RegistrationFields{RootFolder: "/library", QualityProfileID: "1", Monitored: &monitored, SeriesType: "standard"},
	))
	if err != nil {
		t.Fatal(err)
	}
	plan, err := planning.Build(planning.Request{
		ID: id, Action: domain.ActionArrRegistration, Desired: desired,
		Binding:          planning.Binding{SourceID: "discovery-1", SourceRevision: "coverage-1", ConnectionRevisions: map[domain.ConfigID]string{connection: "config-1"}},
		Preconditions:    []planning.Precondition{{Kind: "source_identity", Target: "discovery-1", Expected: "coverage-1", Required: true}},
		RequiredApproval: gate, CreatedAt: createdAt, ExpiresAt: expiresAt,
	})
	if err != nil {
		t.Fatal(err)
	}
	return plan
}
