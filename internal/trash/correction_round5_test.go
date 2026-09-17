package trash

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/guilycst/mastarr/internal/domain"
	"github.com/guilycst/mastarr/internal/ports"
)

func TestTrashReconciliationHoldsStaleTerminalItemAcrossRestart(t *testing.T) {
	now := time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)
	service, clock, read, action, _, store := newTrashFixture(t, now)
	first, second := partialTrashLeaves(now)
	request, _ := seedPartialTrash(t, service, []domain.FileManifestEntry{first, second}, "entry-round5-stale-terminal")
	setTrashSources(read, first, second)

	// The first retry reports only the first item as applied. Its durable row
	// is terminal, while the second row remains selected for later review.
	action.trashEffect = ports.FilesystemEffect{
		Outcome:    domain.OutcomeApplied,
		Affected:   []domain.FileManifestEntry{first},
		ObservedAt: now,
		Evidence:   []string{"round5_partial_first"},
	}
	action.trashErr = errors.New("round5 partial action")
	result, err := service.RetryTrash(context.Background(), TrashRetryRequest{EntryID: request.EntryID})
	if !errors.Is(err, ErrHeld) || result.Entry.State != "held" {
		t.Fatalf("partial retry result=%+v err=%v", result, err)
	}
	if itemByPath(result.Entry, first.RelativePath).State != "trashed" || itemByPath(result.Entry, second.RelativePath).State != "selected" {
		t.Fatalf("partial retry items=%+v, want trashed/selected", result.Entry.Items)
	}

	// Model a stale terminal row: the source object is still present and the
	// mapped destination is absent. A fresh service must hold the entry rather
	// than treating that terminal row as ordinary source-only retry scope.
	freshRead := &fakeRead{stats: make(map[string]ports.FilesystemObservation), errs: make(map[string]error)}
	freshAction := &fakeAction{trashEffect: ports.FilesystemEffect{Outcome: domain.OutcomeApplied, Affected: []domain.FileManifestEntry{second}, ObservedAt: clock.Now()}}
	fresh, err := New(store, Options{Read: freshRead, Action: freshAction, Clock: clock.Now, WorkerID: "w05-round5-fresh"})
	if err != nil {
		t.Fatal(err)
	}
	setTrashSources(freshRead, first, second)

	observed, err := fresh.ReconcileTrash(context.Background(), request.EntryID)
	if !errors.Is(err, ErrHeld) || observed.Retryable || observed.Entry.State != "held" {
		t.Fatalf("stale-terminal reconciliation result=%+v err=%v", observed, err)
	}
	if freshAction.trashCalls != 0 {
		t.Fatalf("stale-terminal reconciliation dispatched %d actions", freshAction.trashCalls)
	}
	janitor, err := store.Queries().GetJanitorRecord(context.Background(), janitorParams(request.EntryID, OperationPurge))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(janitor.OutcomeJson, `"disposition":"terminal_item_stale"`) || !strings.Contains(janitor.OutcomeJson, "trash_terminal_item_stale") {
		t.Fatalf("stale-terminal outcome=%s, want durable stale evidence", janitor.OutcomeJson)
	}

	// Retry repeats the read-only fence and cannot dispatch the remaining item
	// until the stale terminal observation is resolved by an explicit review.
	retried, err := fresh.RetryTrash(context.Background(), TrashRetryRequest{EntryID: request.EntryID})
	if !errors.Is(err, ErrHeld) || retried.Retryable || freshAction.trashCalls != 0 {
		t.Fatalf("stale-terminal retry result=%+v err=%v calls=%d", retried, err, freshAction.trashCalls)
	}
}

func TestTrashReconciliationRetainsActionAndScopeEvidence(t *testing.T) {
	now := time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)
	service, _, read, action, _, store := newTrashFixture(t, now)
	first, second := partialTrashLeaves(now)
	request, _ := seedPartialTrash(t, service, []domain.FileManifestEntry{first, second}, "entry-round5-evidence")
	setTrashSources(read, first, second)

	foreign := first
	foreign.RelativePath = "downloads/foreign-round5.mkv"
	foreign.FileIdentity = "inode:foreign-round5:1"
	action.trashEffect = ports.FilesystemEffect{
		Outcome:    domain.OutcomeApplied,
		Affected:   []domain.FileManifestEntry{first, foreign},
		ObservedAt: now,
		Evidence:   []string{"round5_action_evidence"},
	}
	result, err := service.RetryTrash(context.Background(), TrashRetryRequest{EntryID: request.EntryID})
	if !errors.Is(err, ErrHeld) || result.Entry.State != "held" {
		t.Fatalf("scope retry result=%+v err=%v", result, err)
	}
	firstItem := itemByPath(result.Entry, first.RelativePath)
	secondItem := itemByPath(result.Entry, second.RelativePath)
	if firstItem.State != "trashed" || secondItem.State != "selected" {
		t.Fatalf("scope retry items=%+v, want trashed/selected", result.Entry.Items)
	}

	// Read-back observes the exact first destination and the still-pending
	// second source. It must append this observation while retaining the prior
	// action's affected foreign path, evidence and item-bound outcome.
	removeReadObservation(read, domain.FileTarget{RootID: first.RootID, RelativePath: first.RelativePath})
	firstTrash := first
	firstTrash.RelativePath = firstItem.TrashRelativePath
	read.set(domain.FileTarget{RootID: firstTrash.RootID, RelativePath: firstTrash.RelativePath}, syntheticObservation(firstTrash))
	if _, err := service.ReconcileTrash(context.Background(), request.EntryID); !errors.Is(err, ErrHeld) {
		t.Fatalf("scope read-back error=%v, want ErrHeld", err)
	}

	janitor, err := store.Queries().GetJanitorRecord(context.Background(), janitorParams(request.EntryID, OperationPurge))
	if err != nil {
		t.Fatal(err)
	}
	for _, evidence := range []string{"round5_action_evidence", "affected_foreign:downloads/foreign-round5.mkv", "scope_unresolved", firstItem.ID, secondItem.ID, "trash_state:trash_only", "trash_state:source_only"} {
		if !strings.Contains(janitor.OutcomeJson, evidence) {
			t.Fatalf("reconciled outcome=%s, missing preserved evidence %q", janitor.OutcomeJson, evidence)
		}
	}
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal([]byte(janitor.OutcomeJson), &envelope); err != nil {
		t.Fatalf("decode reconciled outcome: %v", err)
	}
	if len(envelope["priorOutcome"]) == 0 {
		t.Fatalf("reconciled outcome=%s, want prior action envelope", janitor.OutcomeJson)
	}
	var prior map[string]json.RawMessage
	if err := json.Unmarshal(envelope["priorOutcome"], &prior); err != nil {
		t.Fatalf("decode prior action envelope: %v", err)
	}
	if len(prior["effect"]) == 0 || len(prior["evidence"]) == 0 {
		t.Fatalf("prior action outcome=%s, want effect and item effects", string(envelope["priorOutcome"]))
	}
}
