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

func TestTrashReconciliationCompactsPriorOutcomeAcrossManyObservations(t *testing.T) {
	now := time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)
	service, _, read, action, _, store := newTrashFixture(t, now)
	first, second := partialTrashLeaves(now)
	request, _ := seedPartialTrash(t, service, []domain.FileManifestEntry{first, second}, "entry-round6-compaction")
	setTrashSources(read, first, second)

	foreign := first
	foreign.RelativePath = "downloads/foreign-round6.mkv"
	foreign.FileIdentity = "inode:foreign-round6:1"
	action.trashEffect = ports.FilesystemEffect{
		Outcome:    domain.OutcomeApplied,
		Affected:   []domain.FileManifestEntry{first, foreign},
		ObservedAt: now,
		Evidence:   []string{"round6_action_evidence"},
	}
	action.trashErr = errors.New("round6 partial action")
	result, err := service.RetryTrash(context.Background(), TrashRetryRequest{EntryID: request.EntryID})
	if !errors.Is(err, ErrHeld) || result.Entry.State != "held" {
		t.Fatalf("partial retry result=%+v err=%v", result, err)
	}
	firstItem := itemByPath(result.Entry, first.RelativePath)
	if firstItem.State != "trashed" || itemByPath(result.Entry, second.RelativePath).State != "selected" {
		t.Fatalf("partial retry items=%+v, want trashed/selected", result.Entry.Items)
	}

	// Make the first matched object exactly trash-only and leave the second
	// source-only. Every later observation is unchanged, so any growth beyond
	// the one retained action envelope would be recursive journal duplication.
	removeReadObservation(read, domain.FileTarget{RootID: first.RootID, RelativePath: first.RelativePath})
	firstTrash := first
	firstTrash.RelativePath = firstItem.TrashRelativePath
	read.set(domain.FileTarget{RootID: firstTrash.RootID, RelativePath: firstTrash.RelativePath}, syntheticObservation(firstTrash))
	if observed, observeErr := service.ReconcileTrash(context.Background(), request.EntryID); !errors.Is(observeErr, ErrHeld) || observed.Retryable {
		t.Fatalf("initial read-back result=%+v err=%v", observed, observeErr)
	}
	initial, err := store.Queries().GetJanitorRecord(context.Background(), janitorParams(request.EntryID, OperationPurge))
	if err != nil {
		t.Fatal(err)
	}
	initialBytes := len(initial.OutcomeJson)

	for observation := 0; observation < 127; observation++ {
		observed, observeErr := service.ReconcileTrash(context.Background(), request.EntryID)
		if !errors.Is(observeErr, ErrHeld) || observed.Retryable || observed.Entry.State != "held" {
			t.Fatalf("unchanged observation %d result=%+v err=%v", observation, observed, observeErr)
		}
	}
	final, err := store.Queries().GetJanitorRecord(context.Background(), janitorParams(request.EntryID, OperationPurge))
	if err != nil {
		t.Fatal(err)
	}
	if len(final.OutcomeJson) > initialBytes*2 {
		t.Fatalf("compacted outcome grew from %d to %d bytes", initialBytes, len(final.OutcomeJson))
	}
	var decoded any
	if err := json.Unmarshal([]byte(final.OutcomeJson), &decoded); err != nil {
		t.Fatalf("decode compacted outcome: %v", err)
	}
	priorCount, priorDepth := priorOutcomeStats(decoded)
	if priorCount > 1 || priorDepth > 1 {
		t.Fatalf("compacted outcome prior count=%d depth=%d, want at most one", priorCount, priorDepth)
	}
	for _, evidence := range []string{
		"round6_action_evidence",
		"round6 partial action",
		"affected_foreign:downloads/foreign-round6.mkv",
		"scope_unresolved",
		firstItem.ID,
		"trash_state:trash_only",
		"trash_state:source_only",
	} {
		if !strings.Contains(final.OutcomeJson, evidence) {
			t.Fatalf("compacted outcome=%s, missing evidence %q", final.OutcomeJson, evidence)
		}
	}
}

func priorOutcomeStats(value any) (count, maxDepth int) {
	envelope, ok := value.(map[string]any)
	if !ok {
		return 0, 0
	}
	prior, ok := envelope["priorOutcome"]
	if !ok {
		return 0, 0
	}
	nestedCount, nestedDepth := priorOutcomeStats(prior)
	return 1 + nestedCount, 1 + nestedDepth
}
