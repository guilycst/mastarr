package trash

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/guilycst/mastarr/internal/domain"
	"github.com/guilycst/mastarr/internal/ports"
)

func TestRetryTrashPersistsExactPartialEffectsAndRetriesOnlyRemainingItems(t *testing.T) {
	now := time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)
	service, clock, read, action, _, store := newTrashFixture(t, now)
	first, second := partialTrashLeaves(now)
	request, _ := seedPartialTrash(t, service, []domain.FileManifestEntry{first, second}, "entry-round4-partial")
	setTrashSources(read, first, second)

	// The filesystem port reports a valid effect for exactly one selected
	// object. The omitted object remains selected and must be the only retry
	// candidate after the held state is reconciled.
	partialErr := errors.New("synthetic partial trash")
	action.trashEffect = ports.FilesystemEffect{Outcome: domain.OutcomeApplied, Affected: []domain.FileManifestEntry{first}, ObservedAt: now, Evidence: []string{"partial_first"}}
	action.trashErr = partialErr
	result, err := service.RetryTrash(context.Background(), TrashRetryRequest{EntryID: request.EntryID})
	if !errors.Is(err, ErrHeld) || !errors.Is(err, partialErr) || result.Entry.State != "held" {
		t.Fatalf("partial retry result=%+v err=%v", result, err)
	}
	if action.trashCalls != 1 {
		t.Fatalf("partial retry calls=%d, want 1", action.trashCalls)
	}
	if len(action.trashRequests) != 1 || len(action.trashRequests[0].Files) != 2 {
		t.Fatalf("initial retry request=%+v, want both pending leaves", action.trashRequests)
	}
	firstItem := itemByPath(result.Entry, first.RelativePath)
	secondItem := itemByPath(result.Entry, second.RelativePath)
	if firstItem.State != "trashed" || secondItem.State != "selected" {
		t.Fatalf("partial item states first=%q second=%q, want trashed/selected", firstItem.State, secondItem.State)
	}
	if effect := effectByID(result.Effects, firstItem.ID); effect.State != "trashed" || effect.Outcome != domain.OutcomeApplied {
		t.Fatalf("matched partial effect=%+v, want item-bound applied trash", effect)
	}
	if effect := effectByID(result.Effects, secondItem.ID); effect.State != "pending" || effect.Outcome != "" {
		t.Fatalf("remaining partial effect=%+v, want pending without false outcome", effect)
	}
	janitor, err := store.Queries().GetJanitorRecord(context.Background(), janitorParams(request.EntryID, OperationPurge))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(janitor.OutcomeJson, `"operation":"trash"`) || !strings.Contains(janitor.OutcomeJson, firstItem.ID) || !strings.Contains(janitor.OutcomeJson, secondItem.ID) {
		t.Fatalf("partial durable outcome=%s, want both item identities", janitor.OutcomeJson)
	}

	// Make the physical read-back agree with the reported partial effect. The
	// first item is now trash-only while the second is still source-only.
	removeReadObservation(read, domain.FileTarget{RootID: first.RootID, RelativePath: first.RelativePath})
	firstTrash := first
	firstTrash.RelativePath = firstItem.TrashRelativePath
	read.set(domain.FileTarget{RootID: firstTrash.RootID, RelativePath: firstTrash.RelativePath}, syntheticObservation(firstTrash))
	reconciled, err := service.ReconcileTrash(context.Background(), request.EntryID)
	if !errors.Is(err, ErrUncertain) || !reconciled.Retryable || reconciled.Entry.State != "held" {
		t.Fatalf("held read-only reconciliation result=%+v err=%v", reconciled, err)
	}
	if action.trashCalls != 1 {
		t.Fatalf("read-only reconciliation dispatched %d calls", action.trashCalls)
	}

	// A fresh process can continue the held operation only after that
	// read-only observation, and its exact request contains the remaining
	// item rather than the already-materialized first item.
	freshRead := &fakeRead{stats: make(map[string]ports.FilesystemObservation), errs: make(map[string]error)}
	freshAction := &fakeAction{trashEffect: ports.FilesystemEffect{Outcome: domain.OutcomeApplied, Affected: []domain.FileManifestEntry{second}, ObservedAt: clock.Now(), Evidence: []string{"partial_second"}}}
	fresh, err := New(store, Options{Read: freshRead, Action: freshAction, Clock: clock.Now, WorkerID: "w05-round4-fresh"})
	if err != nil {
		t.Fatal(err)
	}
	freshRead.set(domain.FileTarget{RootID: firstTrash.RootID, RelativePath: firstTrash.RelativePath}, syntheticObservation(firstTrash))
	freshRead.set(domain.FileTarget{RootID: second.RootID, RelativePath: second.RelativePath}, syntheticObservation(second))
	if observed, observeErr := fresh.ReconcileTrash(context.Background(), request.EntryID); !errors.Is(observeErr, ErrUncertain) || !observed.Retryable {
		t.Fatalf("fresh read-only reconciliation result=%+v err=%v", observed, observeErr)
	}
	result, err = fresh.RetryTrash(context.Background(), TrashRetryRequest{EntryID: request.EntryID})
	if err != nil || result.Entry.State != "trashed" || freshAction.trashCalls != 1 {
		t.Fatalf("fresh remaining retry result=%+v err=%v calls=%d", result, err, freshAction.trashCalls)
	}
	if len(freshAction.trashRequests) != 1 || len(freshAction.trashRequests[0].Files) != 1 || freshAction.trashRequests[0].Files[0].RelativePath != second.RelativePath {
		t.Fatalf("fresh retry request=%+v, want only %q", freshAction.trashRequests, second.RelativePath)
	}
	if itemByPath(result.Entry, first.RelativePath).State != "trashed" || itemByPath(result.Entry, second.RelativePath).State != "trashed" {
		t.Fatalf("fresh final item states=%+v, want both trashed", result.Entry.Items)
	}
}

func TestRetryTrashPartialForeignOrDuplicateKeepsMatchedItemAndBlocksReconciliation(t *testing.T) {
	now := time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)
	for _, testCase := range []struct {
		name     string
		affected func(domain.FileManifestEntry) []domain.FileManifestEntry
	}{
		{
			name: "foreign",
			affected: func(first domain.FileManifestEntry) []domain.FileManifestEntry {
				foreign := first
				foreign.RelativePath = "downloads/foreign.mkv"
				foreign.FileIdentity = "inode:foreign:1"
				return []domain.FileManifestEntry{first, foreign}
			},
		},
		{
			name: "duplicate",
			affected: func(first domain.FileManifestEntry) []domain.FileManifestEntry {
				return []domain.FileManifestEntry{first, first}
			},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			service, _, read, action, _, store := newTrashFixture(t, now)
			first, second := partialTrashLeaves(now)
			request, _ := seedPartialTrash(t, service, []domain.FileManifestEntry{first, second}, "entry-round4-"+testCase.name)
			setTrashSources(read, first, second)
			action.trashEffect = ports.FilesystemEffect{Outcome: domain.OutcomeApplied, Affected: testCase.affected(first), ObservedAt: now, Evidence: []string{"scope_partial"}}
			result, err := service.RetryTrash(context.Background(), TrashRetryRequest{EntryID: request.EntryID})
			if !errors.Is(err, ErrHeld) || result.Entry.State != "held" || action.trashCalls != 1 {
				t.Fatalf("partial scope result=%+v err=%v calls=%d", result, err, action.trashCalls)
			}
			firstItem := itemByPath(result.Entry, first.RelativePath)
			secondItem := itemByPath(result.Entry, second.RelativePath)
			if firstItem.State != "trashed" || secondItem.State != "selected" {
				t.Fatalf("partial scope states first=%q second=%q", firstItem.State, secondItem.State)
			}
			if effect := effectByID(result.Effects, firstItem.ID); effect.State != "trashed" {
				t.Fatalf("matched scope effect=%+v, want trashed", effect)
			}
			if !hasEffectState(result.Effects, "unknown") {
				t.Fatalf("partial scope effects=%+v, want unresolved aggregate", result.Effects)
			}
			janitor, err := store.Queries().GetJanitorRecord(context.Background(), janitorParams(request.EntryID, OperationPurge))
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(janitor.OutcomeJson, "scope_unresolved") {
				t.Fatalf("partial scope outcome=%s, want scope_unresolved", janitor.OutcomeJson)
			}

			// The exact first trash object and the unresolved second source are
			// observable, but the foreign/duplicate evidence still prevents any
			// read-only reconciliation from authorizing a second mutation.
			removeReadObservation(read, domain.FileTarget{RootID: first.RootID, RelativePath: first.RelativePath})
			firstTrash := first
			firstTrash.RelativePath = firstItem.TrashRelativePath
			read.set(domain.FileTarget{RootID: firstTrash.RootID, RelativePath: firstTrash.RelativePath}, syntheticObservation(firstTrash))
			reconciled, reconcileErr := service.ReconcileTrash(context.Background(), request.EntryID)
			if !errors.Is(reconcileErr, ErrHeld) || reconciled.Retryable || action.trashCalls != 1 {
				t.Fatalf("scope reconciliation result=%+v err=%v calls=%d", reconciled, reconcileErr, action.trashCalls)
			}
		})
	}
}

func partialTrashLeaves(now time.Time) (domain.FileManifestEntry, domain.FileManifestEntry) {
	first := syntheticManifest(now)
	first.RelativePath = "downloads/episode-01.mkv"
	first.FileIdentity = "inode:episode-01:1"
	second := first
	second.RelativePath = "downloads/episode-02.mkv"
	second.FileIdentity = "inode:episode-02:1"
	return first, second
}

func seedPartialTrash(t *testing.T, service *Service, manifest []domain.FileManifestEntry, entryID string) (TrashRequest, Entry) {
	t.Helper()
	request := TrashRequest{EntryID: entryID, RootID: manifest[0].RootID, OriginalPrefix: "downloads", TrashPrefix: ".mastarr-trash/" + entryID, Manifest: manifest, Retention: time.Hour}
	normalized, entries, leaves, manifestJSON, digest, err := service.normalizeTrashRequest(request)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := service.ensurePlannedEntry(context.Background(), request.EntryID, normalized, entries, leaves, manifestJSON, digest, normalizedNow(manifest)); err != nil {
		t.Fatalf("seed planned trash: %v", err)
	}
	entry, err := service.loadEntry(context.Background(), entryID)
	if err != nil {
		t.Fatal(err)
	}
	return request, entry
}

func normalizedNow(manifest []domain.FileManifestEntry) time.Time {
	if len(manifest) > 0 && !manifest[0].ObservedAt.IsZero() {
		return manifest[0].ObservedAt
	}
	return time.Unix(1, 0).UTC()
}

func setTrashSources(read *fakeRead, entries ...domain.FileManifestEntry) {
	for _, entry := range entries {
		read.set(domain.FileTarget{RootID: entry.RootID, RelativePath: entry.RelativePath}, syntheticObservation(entry))
	}
}

func itemByPath(entry Entry, path string) Item {
	for _, item := range entry.Items {
		if item.OriginalRelativePath == path {
			return item
		}
	}
	return Item{}
}

func effectByID(effects []ItemEffect, itemID string) ItemEffect {
	for _, effect := range effects {
		if effect.ItemID == itemID {
			return effect
		}
	}
	return ItemEffect{}
}

func hasEffectState(effects []ItemEffect, state string) bool {
	for _, effect := range effects {
		if effect.State == state {
			return true
		}
	}
	return false
}
