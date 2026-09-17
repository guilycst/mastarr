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
	"github.com/guilycst/mastarr/internal/storage/sqlc"
)

func TestPlannedTrashReconciliationSourceOnlyRequiresExplicitRetry(t *testing.T) {
	now := time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)
	service, _, read, action, _, store := newTrashFixture(t, now)
	source := syntheticManifest(now)
	request := trashFixtureRequest(source, "entry-round3-source", time.Hour)
	normalized, entries, leaves, manifestJSON, digest, err := service.normalizeTrashRequest(request)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := service.ensurePlannedEntry(context.Background(), request.EntryID, normalized, entries, leaves, manifestJSON, digest, now); err != nil {
		t.Fatalf("seed planned entry: %v", err)
	}

	read.set(domain.FileTarget{RootID: source.RootID, RelativePath: source.RelativePath}, syntheticObservation(source))
	result, err := service.Trash(context.Background(), request)
	if !errors.Is(err, ErrUncertain) || !result.Retryable || result.Entry.State != "planned" {
		t.Fatalf("source-only replay result=%+v err=%v", result, err)
	}
	if action.trashCalls != 0 {
		t.Fatalf("source-only replay dispatched %d trash calls", action.trashCalls)
	}
	if read.calls < 2 {
		t.Fatalf("source-only replay reads=%d, want both exact paths", read.calls)
	}
	assertTrashOutcome(t, store, request.EntryID, TrashSourceOnly, "source_only", "source_only")
	reads := read.calls
	if recovered, err := service.Recover(context.Background()); err != nil || recovered != 0 {
		t.Fatalf("planned source-only recover count=%d err=%v", recovered, err)
	}
	if read.calls <= reads {
		t.Fatalf("planned source-only recover performed %d reads, want a fresh observation", read.calls-reads)
	}
	tick, err := service.Tick(context.Background(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if tick.Processed != 1 || action.trashCalls != 0 {
		t.Fatalf("planned source-only tick=%+v calls=%d", tick, action.trashCalls)
	}

	// A retry is a separate explicit authorization. Reconciliation is repeated
	// immediately before the one permitted filesystem dispatch.
	action.trashEffect = ports.FilesystemEffect{Outcome: domain.OutcomeApplied, Affected: []domain.FileManifestEntry{source}, ObservedAt: now}
	result, err = service.RetryTrash(context.Background(), TrashRetryRequest{EntryID: request.EntryID})
	if err != nil || result.Entry.State != "trashed" || action.trashCalls != 1 {
		t.Fatalf("explicit retry result=%+v err=%v calls=%d", result, err, action.trashCalls)
	}
}

func TestPlannedTrashReconciliationFinalizesTrashOnlyWithoutAction(t *testing.T) {
	now := time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)
	service, _, read, action, _, store := newTrashFixture(t, now)
	source := syntheticManifest(now)
	request := trashFixtureRequest(source, "entry-round3-materialized", time.Hour)
	normalized, entries, leaves, manifestJSON, digest, err := service.normalizeTrashRequest(request)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := service.ensurePlannedEntry(context.Background(), request.EntryID, normalized, entries, leaves, manifestJSON, digest, now); err != nil {
		t.Fatalf("seed planned entry: %v", err)
	}
	trash := source
	trash.RelativePath = ".mastarr-trash/entry-round3-materialized/example.mkv"
	read.set(domain.FileTarget{RootID: trash.RootID, RelativePath: trash.RelativePath}, syntheticObservation(trash))

	result, err := service.ReconcileTrash(context.Background(), request.EntryID)
	if err != nil || result.Outcome != domain.OutcomeAlreadySatisfied || result.Entry.State != "trashed" {
		t.Fatalf("trash-only reconciliation result=%+v err=%v", result, err)
	}
	if action.trashCalls != 0 {
		t.Fatalf("trash-only reconciliation dispatched %d calls", action.trashCalls)
	}
	if len(result.Entry.Items) != 1 || result.Entry.Items[0].State != "trashed" {
		t.Fatalf("trash-only item state=%+v", result.Entry.Items)
	}
	assertTrashOutcome(t, store, request.EntryID, TrashOnly, "trash_only", "trash_only")
}

func TestPlannedTrashReconciliationHoldsCollisionIdentityAndNeither(t *testing.T) {
	now := time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name        string
		configure   func(*fakeRead, domain.FileManifestEntry)
		disposition TrashReconciliationState
		itemState   string
		evidence    string
	}{
		{
			name: "both_present",
			configure: func(read *fakeRead, source domain.FileManifestEntry) {
				read.set(domain.FileTarget{RootID: source.RootID, RelativePath: source.RelativePath}, syntheticObservation(source))
				trash := source
				trash.RelativePath = ".mastarr-trash/entry-round3-both_present/example.mkv"
				read.set(domain.FileTarget{RootID: trash.RootID, RelativePath: trash.RelativePath}, syntheticObservation(trash))
			},
			disposition: TrashBothPresent, itemState: "held", evidence: "both_present",
		},
		{
			name: "changed_identity",
			configure: func(read *fakeRead, source domain.FileManifestEntry) {
				changed := source
				changed.FileIdentity = "inode:replacement:2"
				read.set(domain.FileTarget{RootID: source.RootID, RelativePath: source.RelativePath}, syntheticObservation(changed))
			},
			disposition: TrashChangedIdentity, itemState: "held", evidence: "changed_identity",
		},
		{
			name:        "neither_observable",
			configure:   func(*fakeRead, domain.FileManifestEntry) {},
			disposition: TrashNeitherObservable, itemState: "unknown", evidence: "neither_observable",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			service, _, read, action, _, store := newTrashFixture(t, now)
			source := syntheticManifest(now)
			request := trashFixtureRequest(source, "entry-round3-"+tc.name, time.Hour)
			normalized, entries, leaves, manifestJSON, digest, err := service.normalizeTrashRequest(request)
			if err != nil {
				t.Fatal(err)
			}
			if _, _, err := service.ensurePlannedEntry(context.Background(), request.EntryID, normalized, entries, leaves, manifestJSON, digest, now); err != nil {
				t.Fatalf("seed planned entry: %v", err)
			}
			tc.configure(read, source)
			result, err := service.ReconcileTrash(context.Background(), request.EntryID)
			if !errors.Is(err, ErrHeld) || result.Entry.State != "held" || action.trashCalls != 0 {
				t.Fatalf("held reconciliation result=%+v err=%v calls=%d", result, err, action.trashCalls)
			}
			if len(result.Effects) != 1 || result.Effects[0].State != tc.itemState {
				t.Fatalf("effect=%+v, want state %q", result.Effects, tc.itemState)
			}
			assertTrashOutcome(t, store, request.EntryID, tc.disposition, string(tc.disposition), tc.evidence)
		})
	}
}

func TestPlannedTrashReconciliationPreservesPartialDirectoryEvidenceAcrossRestart(t *testing.T) {
	now := time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)
	service, clock, read, _, _, store := newTrashFixture(t, now)
	first := syntheticManifest(now)
	first.RelativePath = "downloads/season/episode-01.mkv"
	first.FileIdentity = "inode:episode-01:1"
	second := first
	second.RelativePath = "downloads/season/episode-02.mkv"
	second.FileIdentity = "inode:episode-02:1"
	directory := domain.FileManifestEntry{RootID: first.RootID, RelativePath: "downloads/season", Type: domain.ManifestDirectory, FileIdentity: "inode:season:1", ObservedAt: now, Children: []domain.FileManifestEntry{first, second}}
	request := TrashRequest{EntryID: "entry-round3-directory", RootID: directory.RootID, OriginalPrefix: "downloads", TrashPrefix: ".mastarr-trash/entry-round3-directory", Manifest: []domain.FileManifestEntry{directory}, Retention: time.Hour}
	normalized, entries, leaves, manifestJSON, digest, err := service.normalizeTrashRequest(request)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := service.ensurePlannedEntry(context.Background(), request.EntryID, normalized, entries, leaves, manifestJSON, digest, now); err != nil {
		t.Fatalf("seed planned entry: %v", err)
	}
	read.set(domain.FileTarget{RootID: first.RootID, RelativePath: first.RelativePath}, syntheticObservation(first))
	trashSecond := second
	trashSecond.RelativePath = ".mastarr-trash/entry-round3-directory/season/episode-02.mkv"
	read.set(domain.FileTarget{RootID: trashSecond.RootID, RelativePath: trashSecond.RelativePath}, syntheticObservation(trashSecond))

	result, err := service.ReconcileTrash(context.Background(), request.EntryID)
	if !errors.Is(err, ErrHeld) || result.Entry.State != "held" {
		t.Fatalf("partial directory result=%+v err=%v", result, err)
	}
	if len(result.Effects) != 2 || result.Effects[0].State != "source_only" || result.Effects[1].State != "trash_only" {
		t.Fatalf("partial directory effects=%+v", result.Effects)
	}
	assertTrashOutcome(t, store, request.EntryID, TrashPartialDirectory, "source_only", "partial_directory_leaves")

	// A fresh service discovers only durable planned intents. This entry is
	// held after the explicit observation, so recovery must not redispatch it.
	freshRead := &fakeRead{stats: make(map[string]ports.FilesystemObservation), errs: make(map[string]error)}
	freshAction := &fakeAction{}
	fresh, err := New(store, Options{Read: freshRead, Action: freshAction, Clock: clock.Now, WorkerID: "w05-round3-restart"})
	if err != nil {
		t.Fatal(err)
	}
	if recovered, err := fresh.Recover(context.Background()); err != nil || recovered != 0 {
		t.Fatalf("recover count=%d err=%v", recovered, err)
	}
	if tick, err := fresh.Tick(context.Background(), 10); err != nil {
		t.Fatal(err)
	} else if tick.Processed != 0 || freshAction.trashCalls != 0 {
		t.Fatalf("held restart tick=%+v calls=%d", tick, freshAction.trashCalls)
	}
}

func assertTrashOutcome(t *testing.T, store interface {
	Queries() *sqlc.Queries
}, entryID string, disposition TrashReconciliationState, itemState, evidence string) {
	t.Helper()
	janitor, err := store.Queries().GetJanitorRecord(context.Background(), janitorParams(entryID, OperationPurge))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(janitor.OutcomeJson, `"operation":"trash"`) || !strings.Contains(janitor.OutcomeJson, `"disposition":"`+string(disposition)+`"`) || !strings.Contains(janitor.OutcomeJson, evidence) {
		t.Fatalf("janitor outcome=%s, want operation/disposition %q", janitor.OutcomeJson, disposition)
	}
	var envelope struct {
		Items []struct {
			State TrashReconciliationState `json:"state"`
		} `json:"items"`
	}
	if err := json.Unmarshal([]byte(janitor.OutcomeJson), &envelope); err != nil {
		t.Fatalf("decode janitor outcome: %v", err)
	}
	if len(envelope.Items) != 0 && itemState != "" && string(envelope.Items[0].State) != itemState {
		t.Fatalf("first item state=%q, want %q", envelope.Items[0].State, itemState)
	}
}
