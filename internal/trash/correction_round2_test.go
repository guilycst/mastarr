package trash

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/guilycst/mastarr/internal/domain"
	"github.com/guilycst/mastarr/internal/ports"
	"github.com/guilycst/mastarr/internal/storage/sqlc"
)

func TestApprovedEarlyPurgeRecoversWithExactBindingAndReadBack(t *testing.T) {
	ctx := context.Background()
	service, clock, read, action, store, entry, source, now := prepareTrashedEntry(t, 24*time.Hour)
	trash := setTrashObject(read, entry, source, now)
	approval := seedEarlyPurgeApproval(t, store, entry, now)

	// Claiming an approved early purge establishes a durable intent, but the
	// action has not yet been dispatched. Recover must leave it available only
	// through the approval-bound reconciliation CAS.
	claim, err := service.claim(ctx, entry.ID, OperationPurge, true, &approval)
	if err != nil {
		t.Fatalf("approved claim: %v", err)
	}
	if claim.State != "running" || action.deleteCalls != 0 {
		t.Fatalf("initial claim=%+v deleteCalls=%d", claim, action.deleteCalls)
	}
	if recovered, err := service.Recover(ctx); err != nil || recovered != 1 {
		t.Fatalf("recover count=%d err=%v", recovered, err)
	}

	// A foreign approval cannot reacquire the recovered generation and cannot
	// reach a filesystem call, even though the approval-time entry version is
	// now older than the projected entry.
	freshAction := &fakeAction{}
	freshRead := &fakeRead{stats: make(map[string]ports.FilesystemObservation), errs: make(map[string]error)}
	fresh, err := New(store, Options{Read: freshRead, Action: freshAction, Clock: clock.Now, WorkerID: "w05-recovered"})
	if err != nil {
		t.Fatal(err)
	}
	wrong := approval
	wrong.DecisionID = "foreign-recovered-decision"
	if _, err := fresh.Purge(ctx, PurgeRequest{EntryID: entry.ID, HardDelete: true, Approval: &wrong}); !errors.Is(err, ErrConflict) {
		t.Fatalf("foreign recovered approval error=%v, want ErrConflict", err)
	}
	if freshAction.deleteCalls != 0 {
		t.Fatalf("foreign recovered approval dispatched %d deletes", freshAction.deleteCalls)
	}

	// A lost read is held and released to durable reconciling state. No delete
	// is attempted while the exact object cannot be proven.
	if tick, err := fresh.Tick(ctx, 10); err != nil {
		t.Fatalf("lost-read tick: %v", err)
	} else if tick.Processed != 1 || tick.Held != 1 || freshAction.deleteCalls != 0 {
		t.Fatalf("lost-read tick=%+v deleteCalls=%d", tick, freshAction.deleteCalls)
	}
	janitor, err := store.Queries().GetJanitorRecord(ctx, janitorParams(entry.ID, OperationPurge))
	if err != nil {
		t.Fatal(err)
	}
	if janitor.State != "reconciling" || !containsScopeEvidence(janitor.OutcomeJson, "trash_source_unobservable") {
		t.Fatalf("lost-read janitor=%+v", janitor)
	}

	// Once the retry deadline has elapsed, exact approval plus an exact fresh
	// read-back is sufficient to reacquire and dispatch once. The persisted
	// approval binding remains unchanged across the restart boundary.
	clock.Set(now.Add(DefaultRetryAfter + time.Second))
	freshAction.deleteEffect = ports.FilesystemEffect{Outcome: domain.OutcomeApplied, Affected: []domain.FileManifestEntry{trash}, ObservedAt: clock.Now(), Evidence: []string{"recovered_delete"}}
	freshRead.set(domain.FileTarget{RootID: trash.RootID, RelativePath: trash.RelativePath}, syntheticObservation(trash))
	result, err := fresh.Purge(ctx, PurgeRequest{EntryID: entry.ID, HardDelete: true, Approval: &approval})
	if err != nil {
		t.Fatalf("exact recovered purge: %v", err)
	}
	if result.Entry.State != "purged" || freshAction.deleteCalls != 1 {
		t.Fatalf("recovered result=%+v deleteCalls=%d", result, freshAction.deleteCalls)
	}
}

func TestPurgeForeignEffectStaysHeldAndCannotBeRetriedBlindly(t *testing.T) {
	service, clock, read, action, store, entry, source, now := prepareTrashedEntry(t, time.Hour)
	clock.Set(now.Add(time.Hour))
	trash := setTrashObject(read, entry, source, clock.Now())
	foreign := trash
	foreign.RelativePath = ".mastarr-trash/foreign-entry/other.mkv"
	foreign.FileIdentity = "inode:foreign:1"
	action.deleteEffect = ports.FilesystemEffect{
		Outcome:    domain.OutcomeApplied,
		Affected:   []domain.FileManifestEntry{trash, foreign},
		ObservedAt: clock.Now(),
		Evidence:   []string{"exact_plus_foreign"},
	}

	result, err := service.Purge(context.Background(), PurgeRequest{EntryID: entry.ID})
	if !errors.Is(err, ErrHeld) || result.Entry.State != "held" {
		t.Fatalf("foreign purge result=%+v err=%v", result, err)
	}
	if result.Outcome == domain.OutcomeApplied || len(result.Entry.Items) != 1 || result.Entry.Items[0].State == "purged" {
		t.Fatalf("foreign purge terminalized approved item: %+v", result.Entry)
	}
	if action.deleteCalls != 1 {
		t.Fatalf("initial delete calls=%d, want 1", action.deleteCalls)
	}

	janitor, err := store.Queries().GetJanitorRecord(context.Background(), janitorParams(entry.ID, OperationPurge))
	if err != nil {
		t.Fatal(err)
	}
	if janitor.State != "held" || !containsScopeEvidence(janitor.OutcomeJson, "scope_unresolved") {
		t.Fatalf("foreign purge janitor=%+v", janitor)
	}
	if _, err := service.Reconcile(context.Background(), entry.ID, OperationPurge); !errors.Is(err, ErrHeld) {
		t.Fatalf("foreign reconcile error=%v, want ErrHeld", err)
	}
	if _, err := service.Retry(context.Background(), RetryRequest{EntryID: entry.ID, Operation: OperationPurge}); !errors.Is(err, ErrHeld) {
		t.Fatalf("foreign retry error=%v, want ErrHeld", err)
	}
	if action.deleteCalls != 1 {
		t.Fatalf("foreign retry dispatched another delete: calls=%d", action.deleteCalls)
	}
}

func TestPlannedTrashReplayWithoutIdempotencyKeyDoesNotDispatch(t *testing.T) {
	now := time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)
	service, _, read, action, _, _ := newTrashFixture(t, now)
	source := syntheticManifest(now)
	read.set(domain.FileTarget{RootID: source.RootID, RelativePath: source.RelativePath}, syntheticObservation(source))
	request := trashFixtureRequest(source, "entry-planned-replay", time.Hour)
	normalized, entries, leaves, manifestJSON, digest, err := service.normalizeTrashRequest(request)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := service.ensurePlannedEntry(context.Background(), request.EntryID, normalized, entries, leaves, manifestJSON, digest, now); err != nil {
		t.Fatalf("seed planned entry: %v", err)
	}

	result, err := service.Trash(context.Background(), request)
	if !errors.Is(err, ErrUncertain) || result.Entry.State != "planned" || !result.Retryable {
		t.Fatalf("planned replay result=%+v err=%v", result, err)
	}
	if action.trashCalls != 0 {
		t.Fatalf("planned replay dispatched %d trash calls", action.trashCalls)
	}
	if read.calls < 2 {
		t.Fatalf("planned replay performed %d reads, want original and trash observations", read.calls)
	}
}

func TestDirectoryManifestUsesExpandedLeafScope(t *testing.T) {
	now := time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name       string
		affected   func(domain.FileManifestEntry, domain.FileManifestEntry) []domain.FileManifestEntry
		wantError  bool
		wantState  string
		wantItems  []string
		wantAction string
	}{
		{
			name: "partial_trash",
			affected: func(first, _ domain.FileManifestEntry) []domain.FileManifestEntry {
				return []domain.FileManifestEntry{first}
			},
			wantError: true, wantState: "held", wantItems: []string{"trashed", "selected"}, wantAction: "trash",
		},
		{
			name: "root_effect_rejected",
			affected: func(first, _ domain.FileManifestEntry) []domain.FileManifestEntry {
				return []domain.FileManifestEntry{{RootID: first.RootID, RelativePath: "downloads/season", Type: domain.ManifestDirectory, FileIdentity: "inode:season:1", ObservedAt: first.ObservedAt}}
			},
			wantError: true, wantState: "held", wantItems: []string{"selected", "selected"}, wantAction: "trash",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			service, _, read, action, _, _ := newTrashFixture(t, now)
			first := syntheticManifest(now)
			first.RelativePath = "downloads/season/episode-01.mkv"
			first.FileIdentity = "inode:episode-01:1"
			second := first
			second.RelativePath = "downloads/season/episode-02.mkv"
			second.FileIdentity = "inode:episode-02:1"
			directory := domain.FileManifestEntry{RootID: first.RootID, RelativePath: "downloads/season", Type: domain.ManifestDirectory, FileIdentity: "inode:season:1", ObservedAt: now, Children: []domain.FileManifestEntry{first, second}}
			read.set(domain.FileTarget{RootID: directory.RootID, RelativePath: directory.RelativePath}, syntheticObservation(directory))
			read.set(domain.FileTarget{RootID: first.RootID, RelativePath: first.RelativePath}, syntheticObservation(first))
			read.set(domain.FileTarget{RootID: second.RootID, RelativePath: second.RelativePath}, syntheticObservation(second))
			action.trashEffect = ports.FilesystemEffect{Outcome: domain.OutcomeApplied, Affected: tc.affected(first, second), ObservedAt: now}
			result, err := service.Trash(context.Background(), TrashRequest{EntryID: "entry-directory-" + tc.name, RootID: directory.RootID, OriginalPrefix: "downloads", TrashPrefix: ".mastarr-trash/entry-directory-" + tc.name, Manifest: []domain.FileManifestEntry{directory}, Retention: time.Hour})
			if tc.wantError && !errors.Is(err, ErrHeld) {
				t.Fatalf("trash error=%v, want ErrHeld", err)
			}
			if result.Entry.State != tc.wantState || len(result.Entry.Items) != len(tc.wantItems) {
				t.Fatalf("directory result entry=%+v", result.Entry)
			}
			for index, want := range tc.wantItems {
				if result.Entry.Items[index].State != want || result.Entry.Items[index].Type == domain.ManifestDirectory {
					t.Fatalf("item[%d]=%+v, want leaf state %q", index, result.Entry.Items[index], want)
				}
			}
			if action.trashCalls != 1 {
				t.Fatalf("trash calls=%d, want 1", action.trashCalls)
			}
		})
	}

	t.Run("complete_purge_and_restore_have_no_directory_row", func(t *testing.T) {
		service, clock, read, action, _, _ := newTrashFixture(t, now)
		first := syntheticManifest(now)
		first.RelativePath = "downloads/season/episode-01.mkv"
		first.FileIdentity = "inode:episode-01:1"
		directory := domain.FileManifestEntry{RootID: first.RootID, RelativePath: "downloads/season", Type: domain.ManifestDirectory, FileIdentity: "inode:season:1", ObservedAt: now, Children: []domain.FileManifestEntry{first}}
		read.set(domain.FileTarget{RootID: directory.RootID, RelativePath: directory.RelativePath}, syntheticObservation(directory))
		read.set(domain.FileTarget{RootID: first.RootID, RelativePath: first.RelativePath}, syntheticObservation(first))
		action.trashEffect = ports.FilesystemEffect{Outcome: domain.OutcomeApplied, Affected: []domain.FileManifestEntry{first}, ObservedAt: now}
		request := TrashRequest{EntryID: "entry-directory-lifecycle", RootID: directory.RootID, OriginalPrefix: "downloads", TrashPrefix: ".mastarr-trash/entry-directory-lifecycle", Manifest: []domain.FileManifestEntry{directory}, Retention: time.Hour}
		result, err := service.Trash(context.Background(), request)
		if err != nil {
			t.Fatal(err)
		}
		if len(result.Entry.Items) != 1 {
			t.Fatalf("expanded item count=%d, want 1", len(result.Entry.Items))
		}
		for _, item := range result.Entry.Items {
			trashEntry := first
			trashEntry.RelativePath = item.TrashRelativePath
			read.set(domain.FileTarget{RootID: item.RootID, RelativePath: item.TrashRelativePath}, syntheticObservation(trashEntry))
		}
		clock.Set(now.Add(time.Hour))
		action.deleteEffect = ports.FilesystemEffect{Outcome: domain.OutcomeApplied, Affected: []domain.FileManifestEntry{
			{RootID: first.RootID, RelativePath: ".mastarr-trash/entry-directory-lifecycle/season/episode-01.mkv", Type: first.Type, Size: first.Size, Digest: first.Digest, FileIdentity: first.FileIdentity, ObservedAt: clock.Now()},
		}, ObservedAt: clock.Now()}
		purged, err := service.Purge(context.Background(), PurgeRequest{EntryID: result.Entry.ID})
		if err != nil || purged.Entry.State != "purged" {
			t.Fatalf("purge result=%+v err=%v", purged, err)
		}
		for _, item := range purged.Entry.Items {
			if item.Type == domain.ManifestDirectory || item.State != "purged" {
				t.Fatalf("purged item=%+v", item)
			}
		}

		// A separate nested entry exercises restore bookkeeping without a
		// directory wildcard or a stale directory row.
		restoreService, _, restoreRead, restoreAction, _, _ := newTrashFixture(t, now)
		restoreRead.set(domain.FileTarget{RootID: first.RootID, RelativePath: first.RelativePath}, syntheticObservation(first))
		restoreAction.trashEffect = ports.FilesystemEffect{Outcome: domain.OutcomeApplied, Affected: []domain.FileManifestEntry{first}, ObservedAt: now}
		restoreRequest := request
		restoreRequest.EntryID = "entry-directory-restore"
		restoreRequest.TrashPrefix = ".mastarr-trash/entry-directory-restore"
		restoreRead.set(domain.FileTarget{RootID: directory.RootID, RelativePath: directory.RelativePath}, syntheticObservation(directory))
		restored, err := restoreService.Trash(context.Background(), restoreRequest)
		if err != nil {
			t.Fatal(err)
		}
		for _, item := range restored.Entry.Items {
			trashEntry := first
			trashEntry.RelativePath = item.TrashRelativePath
			restoreRead.set(domain.FileTarget{RootID: item.RootID, RelativePath: item.TrashRelativePath}, syntheticObservation(trashEntry))
			removeReadObservation(restoreRead, domain.FileTarget{RootID: item.RootID, RelativePath: item.OriginalRelativePath})
		}
		restoreAction.restoreEffect = ports.FilesystemEffect{Outcome: domain.OutcomeApplied, Affected: []domain.FileManifestEntry{first}, ObservedAt: now}
		restored, err = restoreService.Restore(context.Background(), RestoreRequest{EntryID: restored.Entry.ID})
		if err != nil || restored.Entry.State != "restored" {
			t.Fatalf("restore result=%+v err=%v", restored, err)
		}
		for _, item := range restored.Entry.Items {
			if item.Type == domain.ManifestDirectory || item.State != "restored" {
				t.Fatalf("restored item=%+v", item)
			}
		}
	})
}

func janitorParams(entryID string, operation Operation) *sqlc.GetJanitorRecordParams {
	return &sqlc.GetJanitorRecordParams{TrashEntryID: entryID, Operation: string(operation)}
}

func containsScopeEvidence(raw, expected string) bool {
	return strings.Contains(raw, expected)
}
