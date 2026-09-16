package trash

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/guilycst/mastarr/internal/domain"
	"github.com/guilycst/mastarr/internal/ports"
	"github.com/guilycst/mastarr/internal/storage"
	"github.com/guilycst/mastarr/internal/storage/sqlc"
)

func trashFixtureRequest(entry domain.FileManifestEntry, id string, retention time.Duration) TrashRequest {
	return TrashRequest{
		EntryID:        id,
		RootID:         entry.RootID,
		OriginalPrefix: "downloads",
		TrashPrefix:    ".mastarr-trash/" + id,
		Manifest:       []domain.FileManifestEntry{entry},
		Retention:      retention,
	}
}

func prepareTrashedEntry(t *testing.T, retention time.Duration) (*Service, *fakeClock, *fakeRead, *fakeAction, *storage.Store, Entry, domain.FileManifestEntry, time.Time) {
	t.Helper()
	now := time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)
	service, clock, read, action, _, store := newTrashFixture(t, now)
	entry := syntheticManifest(now)
	read.set(domain.FileTarget{RootID: entry.RootID, RelativePath: entry.RelativePath}, syntheticObservation(entry))
	action.trashEffect = ports.FilesystemEffect{Outcome: domain.OutcomeApplied, Affected: []domain.FileManifestEntry{entry}, ObservedAt: now, Evidence: []string{"synthetic_trash"}}
	result, err := service.Trash(context.Background(), trashFixtureRequest(entry, "entry-correction", retention))
	if err != nil {
		t.Fatalf("prepare trash: %v", err)
	}
	if result.Entry.State != "trashed" {
		t.Fatalf("prepared entry state = %q, want trashed", result.Entry.State)
	}
	return service, clock, read, action, store, result.Entry, entry, now
}

func setTrashObject(read *fakeRead, entry Entry, source domain.FileManifestEntry, observedAt time.Time) domain.FileManifestEntry {
	trash := source
	trash.RelativePath = entry.Items[0].TrashRelativePath
	trash.ObservedAt = observedAt
	read.set(domain.FileTarget{RootID: trash.RootID, RelativePath: trash.RelativePath}, syntheticObservation(trash))
	return trash
}

func removeReadObservation(read *fakeRead, target domain.FileTarget) {
	read.mu.Lock()
	delete(read.stats, targetKey(target))
	delete(read.errs, targetKey(target))
	read.mu.Unlock()
}

func TestTrashReplayKeepsHeldAndFailedStatesVisible(t *testing.T) {
	now := time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)
	service, _, read, action, _, _ := newTrashFixture(t, now)
	entry := syntheticManifest(now)
	request := trashFixtureRequest(entry, "entry-replay", 24*time.Hour)
	// The first operation is held by an unexpected extra response rather than
	// by a direct database edit.
	read.set(domain.FileTarget{RootID: entry.RootID, RelativePath: entry.RelativePath}, syntheticObservation(entry))
	action.trashEffect = ports.FilesystemEffect{
		Outcome:    domain.OutcomeApplied,
		Affected:   []domain.FileManifestEntry{entry, {RootID: entry.RootID, RelativePath: "downloads/foreign.mkv", Type: domain.ManifestFile, Size: 1, FileIdentity: "inode:foreign:1", ObservedAt: entry.ObservedAt}},
		ObservedAt: entry.ObservedAt,
	}
	request.EntryID = "entry-replay"
	first, err := service.Trash(context.Background(), request)
	if !errors.Is(err, ErrHeld) || first.Entry.State != "held" {
		t.Fatalf("held trash result=%+v err=%v", first, err)
	}
	action.mu.Lock()
	trashCalls := action.trashCalls
	action.mu.Unlock()
	replayed, err := service.Trash(context.Background(), request)
	if !errors.Is(err, ErrHeld) || replayed.Outcome == domain.OutcomeAlreadySatisfied || replayed.Entry.State != "held" {
		t.Fatalf("held replay result=%+v err=%v", replayed, err)
	}
	if action.trashCalls != trashCalls {
		t.Fatalf("held replay dispatched again: calls=%d before=%d", action.trashCalls, trashCalls)
	}

	if _, err := service.store.DB().Exec(`UPDATE trash_entries SET state = 'failed', hold_reason = 'synthetic failure', version = version + 1 WHERE id = ?`, request.EntryID); err != nil {
		t.Fatal(err)
	}
	failed, err := service.Trash(context.Background(), request)
	if !errors.Is(err, ErrConflict) || failed.Outcome == domain.OutcomeAlreadySatisfied || failed.Entry.State != "failed" {
		t.Fatalf("failed replay result=%+v err=%v", failed, err)
	}
	if action.trashCalls != trashCalls {
		t.Fatalf("failed replay dispatched again: calls=%d before=%d", action.trashCalls, trashCalls)
	}
}

func TestTrashRejectsForeignDuplicateAndChangedAffectedEvidence(t *testing.T) {
	now := time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)
	exact := syntheticManifest(now)
	cases := []struct {
		name     string
		affected func(domain.FileManifestEntry) []domain.FileManifestEntry
	}{
		{
			name: "foreign",
			affected: func(entry domain.FileManifestEntry) []domain.FileManifestEntry {
				return []domain.FileManifestEntry{entry, {RootID: entry.RootID, RelativePath: "downloads/foreign.mkv", Type: domain.ManifestFile, Size: 1, FileIdentity: "inode:foreign:1", ObservedAt: entry.ObservedAt}}
			},
		},
		{
			name: "duplicate",
			affected: func(entry domain.FileManifestEntry) []domain.FileManifestEntry {
				return []domain.FileManifestEntry{entry, entry}
			},
		},
		{
			name: "changed_identity",
			affected: func(entry domain.FileManifestEntry) []domain.FileManifestEntry {
				entry.FileIdentity = "inode:replacement:2"
				return []domain.FileManifestEntry{entry}
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			service, _, read, action, _, _ := newTrashFixture(t, now)
			read.set(domain.FileTarget{RootID: exact.RootID, RelativePath: exact.RelativePath}, syntheticObservation(exact))
			action.trashEffect = ports.FilesystemEffect{Outcome: domain.OutcomeApplied, Affected: tc.affected(exact), ObservedAt: now, Evidence: []string{"synthetic_scope"}}
			result, err := service.Trash(context.Background(), trashFixtureRequest(exact, "entry-scope-"+tc.name, 24*time.Hour))
			if !errors.Is(err, ErrHeld) {
				t.Fatalf("trash error = %v, want ErrHeld", err)
			}
			if result.Entry.State != "held" || result.Outcome == domain.OutcomeApplied {
				t.Fatalf("unsafe scope result = %+v", result)
			}
			if result.Entry.HoldReason == "" {
				t.Fatal("held result lost scope reason")
			}
		})
	}
}

func TestPurgeAndRestoreRejectForeignAffectedEvidence(t *testing.T) {
	t.Run("purge", func(t *testing.T) {
		service, clock, read, action, _, entry, source, now := prepareTrashedEntry(t, time.Hour)
		clock.Set(now.Add(time.Hour))
		trash := setTrashObject(read, entry, source, clock.Now())
		action.deleteEffect = ports.FilesystemEffect{
			Outcome:    domain.OutcomeApplied,
			Affected:   []domain.FileManifestEntry{{RootID: trash.RootID, RelativePath: "trash/foreign.mkv", Type: domain.ManifestFile, Size: 1, FileIdentity: "inode:foreign:1", ObservedAt: clock.Now()}},
			ObservedAt: clock.Now(),
		}
		result, err := service.Purge(context.Background(), PurgeRequest{EntryID: entry.ID})
		if !errors.Is(err, ErrHeld) || result.Entry.State != "held" || result.Outcome == domain.OutcomeApplied {
			t.Fatalf("foreign purge result=%+v err=%v", result, err)
		}
		if action.deleteCalls != 1 {
			t.Fatalf("purge delete calls=%d, want one observed dispatch", action.deleteCalls)
		}
	})

	t.Run("restore", func(t *testing.T) {
		service, _, read, action, _, entry, source, now := prepareTrashedEntry(t, time.Hour)
		trash := setTrashObject(read, entry, source, now)
		removeReadObservation(read, domain.FileTarget{RootID: source.RootID, RelativePath: source.RelativePath})
		action.restoreEffect = ports.FilesystemEffect{
			Outcome:    domain.OutcomeApplied,
			Affected:   []domain.FileManifestEntry{{RootID: trash.RootID, RelativePath: "downloads/foreign.mkv", Type: domain.ManifestFile, Size: 1, FileIdentity: "inode:foreign:1", ObservedAt: now}},
			ObservedAt: now,
		}
		result, err := service.Restore(context.Background(), RestoreRequest{EntryID: entry.ID})
		if !errors.Is(err, ErrHeld) || result.Entry.State != "held" || result.Outcome == domain.OutcomeApplied {
			t.Fatalf("foreign restore result=%+v err=%v", result, err)
		}
		if action.restoreCalls != 1 {
			t.Fatalf("restore calls=%d, want one observed dispatch", action.restoreCalls)
		}
	})
}

func TestTrashPreservesClientAndFilesystemEvidenceInOneStateEnvelope(t *testing.T) {
	now := time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)
	service, _, read, action, download, _ := newTrashFixture(t, now)
	entry := syntheticManifest(now)
	read.set(domain.FileTarget{RootID: entry.RootID, RelativePath: entry.RelativePath}, syntheticObservation(entry))
	ref := clientRef()
	download.current = ports.DownloadObservation{Ref: ref, State: "downloading", Seeding: true, ObservedAt: now}
	action.trashEffect = ports.FilesystemEffect{Outcome: domain.OutcomeApplied, Affected: []domain.FileManifestEntry{entry}, ObservedAt: now, Evidence: []string{"filesystem_move_observed"}}
	request := trashFixtureRequest(entry, "entry-evidence", 24*time.Hour)
	request.Client = &ref
	result, err := service.Trash(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	var state map[string]any
	if err := json.Unmarshal([]byte(result.Entry.clientStateJSON), &state); err != nil {
		t.Fatalf("client state json: %v", err)
	}
	if state["connectionId"] != ref.ConnectionID.String() || state["externalId"] != ref.ExternalID || state["state"] != "paused" {
		t.Fatalf("client state identity/status lost: %v", state)
	}
	stop, ok := state["stop"].(map[string]any)
	if !ok || stop["outcome"] != string(domain.OutcomeApplied) {
		t.Fatalf("stop evidence lost: %v", state["stop"])
	}
	filesystem, ok := state["filesystem"].(map[string]any)
	if !ok || filesystem["outcome"] != string(domain.OutcomeApplied) {
		t.Fatalf("filesystem evidence lost: %v", state["filesystem"])
	}
	if evidence, ok := filesystem["evidence"].([]any); !ok || len(evidence) != 1 || evidence[0] != "filesystem_move_observed" {
		t.Fatalf("filesystem evidence = %v", filesystem["evidence"])
	}
}

func TestHardPurgeRequiresExactApprovalAndHasZeroDispatchOnBindingFailures(t *testing.T) {
	t.Run("absent", func(t *testing.T) {
		service, _, _, action, _, entry, _, _ := prepareTrashedEntry(t, 24*time.Hour)
		result, err := service.Purge(context.Background(), PurgeRequest{EntryID: entry.ID, HardDelete: true})
		if !errors.Is(err, ErrConflict) || result.Evidence[0] != "early_purge_approval_required" || action.deleteCalls != 0 {
			t.Fatalf("absent approval result=%+v err=%v deleteCalls=%d", result, err, action.deleteCalls)
		}
	})

	for _, tc := range []struct {
		name   string
		mutate func(*EarlyPurgeApproval)
	}{
		{name: "stale_entry_version", mutate: func(approval *EarlyPurgeApproval) { approval.ApprovedEntryVersion-- }},
		{name: "foreign_plan", mutate: func(approval *EarlyPurgeApproval) { approval.PlanID = "foreign-plan" }},
		{name: "foreign_decision", mutate: func(approval *EarlyPurgeApproval) { approval.DecisionID = "foreign-decision" }},
		{name: "foreign_action", mutate: func(approval *EarlyPurgeApproval) { approval.ActionRunID = "foreign-action" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			service, _, read, action, store, entry, source, now := prepareTrashedEntry(t, 24*time.Hour)
			approval := seedEarlyPurgeApproval(t, store, entry, now)
			trash := setTrashObject(read, entry, source, now)
			action.deleteEffect = ports.FilesystemEffect{Outcome: domain.OutcomeApplied, Affected: []domain.FileManifestEntry{trash}, ObservedAt: now}
			tc.mutate(&approval)
			result, err := service.Purge(context.Background(), PurgeRequest{EntryID: entry.ID, HardDelete: true, Approval: &approval})
			if !errors.Is(err, ErrConflict) || action.deleteCalls != 0 {
				t.Fatalf("binding failure result=%+v err=%v deleteCalls=%d", result, err, action.deleteCalls)
			}
		})
	}
}

func TestHardPurgeUsesBoundApprovalAndExactPayload(t *testing.T) {
	service, _, read, action, store, entry, source, now := prepareTrashedEntry(t, 24*time.Hour)
	approval := seedEarlyPurgeApproval(t, store, entry, now)
	trash := setTrashObject(read, entry, source, now)
	action.deleteEffect = ports.FilesystemEffect{Outcome: domain.OutcomeApplied, Affected: []domain.FileManifestEntry{trash}, ObservedAt: now, Evidence: []string{"approved_delete"}}
	result, err := service.Purge(context.Background(), PurgeRequest{EntryID: entry.ID, HardDelete: true, Approval: &approval})
	if err != nil {
		t.Fatalf("approved purge: %v", err)
	}
	if result.Entry.State != "purged" || result.Outcome != domain.OutcomeApplied || action.deleteCalls != 1 {
		t.Fatalf("approved purge result=%+v deleteCalls=%d", result, action.deleteCalls)
	}
	janitor, err := store.Queries().GetJanitorRecord(context.Background(), &sqlc.GetJanitorRecordParams{TrashEntryID: entry.ID, Operation: string(OperationPurge)})
	if err != nil {
		t.Fatal(err)
	}
	if !janitor.ApprovalPlanID.Valid || janitor.ApprovalPlanID.String != approval.PlanID || janitor.ApprovalDecisionID.String != approval.DecisionID {
		t.Fatalf("janitor lost approval binding: %+v", janitor)
	}
}

func seedEarlyPurgeApproval(t *testing.T, store *storage.Store, entry Entry, now time.Time) EarlyPurgeApproval {
	t.Helper()
	ctx := context.Background()
	planID, digest := "correction-early-plan", "correction-early-digest"
	manifestJSON, err := json.Marshal(entry.Manifest)
	if err != nil {
		t.Fatal(err)
	}
	queries := store.Queries()
	if _, err := queries.CreateActionPlan(ctx, &sqlc.CreateActionPlanParams{ID: planID, Kind: "fs.delete", State: "ready", CurrentRevision: 1, CurrentDigest: digest, CreatedAt: formatTime(now), UpdatedAt: formatTime(now)}); err != nil {
		t.Fatal(err)
	}
	if _, err := queries.CreateActionPlanRevision(ctx, &sqlc.CreateActionPlanRevisionParams{
		PlanID: planID, Revision: 1, Digest: digest, State: "ready", InputJson: `{}`, PreconditionsJson: `{}`, CapabilitiesJson: `[]`, ManifestJson: string(manifestJSON), CreatedAt: formatTime(now), ExpiresAt: formatTime(now.Add(time.Hour)), ReadyAt: sql.NullString{String: formatTime(now), Valid: true},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := queries.CreateEarlyPurgePlanTarget(ctx, &sqlc.CreateEarlyPurgePlanTargetParams{PlanID: planID, Revision: 1, PlanDigest: digest, IntentKind: "fs.delete", TrashEntryID: entry.ID, TrashEntryVersion: entry.Version, ManifestJson: string(manifestJSON), CreatedAt: formatTime(now)}); err != nil {
		t.Fatal(err)
	}
	if _, err := queries.CreateReviewDecision(ctx, &sqlc.CreateReviewDecisionParams{ID: "correction-early-decision", PlanID: planID, PlanRevision: 1, PlanDigest: digest, Decision: "approve", Actor: "unauthenticated", IdempotencyScope: "correction-early-review", IdempotencyKey: "approve", CreatedAt: formatTime(now)}); err != nil {
		t.Fatal(err)
	}
	if _, err := queries.CreateActionRun(ctx, &sqlc.CreateActionRunParams{ID: "correction-early-action", PlanID: planID, PlanRevision: 1, PlanDigest: digest, State: "queued", DesiredStateJson: `{}`, DeadlineAt: sql.NullString{String: formatTime(now.Add(time.Hour)), Valid: true}, Version: 1, OutcomeJson: `{}`, CreatedAt: formatTime(now), UpdatedAt: formatTime(now)}); err != nil {
		t.Fatal(err)
	}
	return EarlyPurgeApproval{PlanID: planID, PlanRevision: 1, PlanDigest: digest, DecisionID: "correction-early-decision", ActionRunID: "correction-early-action", ApprovedEntryVersion: entry.Version, ActionRunVersion: 1}
}

func TestValidateAffectedSetRequiresExactSemanticIdentity(t *testing.T) {
	now := time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)
	entry := syntheticManifest(now)
	item := Item{ID: "item-1", RootID: entry.RootID, OriginalRelativePath: entry.RelativePath, TrashRelativePath: ".mastarr-trash/entry/example.mkv", Type: entry.Type, Size: entry.Size, Digest: entry.Digest, FileIdentity: entry.FileIdentity, State: "selected"}
	exact := entry
	exact.RelativePath = item.TrashRelativePath
	for _, tc := range []struct {
		name     string
		affected []domain.FileManifestEntry
	}{
		{name: "foreign", affected: []domain.FileManifestEntry{{RootID: entry.RootID, RelativePath: "downloads/other.mkv", Type: entry.Type, Size: entry.Size, FileIdentity: entry.FileIdentity, ObservedAt: now}}},
		{name: "duplicate", affected: []domain.FileManifestEntry{exact, exact}},
		{name: "changed", affected: []domain.FileManifestEntry{{RootID: exact.RootID, RelativePath: exact.RelativePath, Type: exact.Type, Size: exact.Size, FileIdentity: "inode:changed:2", ObservedAt: now}}},
		{name: "missing", affected: nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			matched, issues := validateAffectedSet([]Item{item}, tc.affected, operationTrash)
			if len(issues) == 0 || matched[item.ID] && tc.name != "duplicate" {
				t.Fatalf("affected=%+v matched=%v issues=%v", tc.affected, matched, issues)
			}
		})
	}
}
