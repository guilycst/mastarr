package actions

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/guilycst/mastarr/internal/domain"
	"github.com/guilycst/mastarr/internal/execution"
	"github.com/guilycst/mastarr/internal/filesystem/placement"
	"github.com/guilycst/mastarr/internal/ports"
	"github.com/guilycst/mastarr/internal/storage"
	"github.com/guilycst/mastarr/internal/storage/sqlc"
)

func TestExecutorPersistsManifestAndMappingPartialFilesystemEffects(t *testing.T) {
	first := manifest("download", "one.mkv", strings.Repeat("a", 64), "inode-one")
	second := manifest("download", "two.mkv", strings.Repeat("b", 64), "inode-two")
	firstMap := ports.FileMap{Source: first, Destination: domain.FileTarget{RootID: "library", RelativePath: "one.mkv"}}
	secondMap := ports.FileMap{Source: second, Destination: domain.FileTarget{RootID: "library", RelativePath: "two.mkv"}}
	read := &fakeFilesystemRead{
		entries: map[string]ports.FilesystemObservation{
			targetID(sourceTarget(firstMap)):  {Entry: first, ObservedAt: actionTestNow},
			targetID(sourceTarget(secondMap)): {Entry: second, ObservedAt: actionTestNow},
		},
		missing: map[string]bool{targetID(firstMap.Destination): true, targetID(secondMap.Destination): true},
	}
	actionPort := &fakeFilesystemAction{copyEffect: ports.FilesystemEffect{
		Outcome:    domain.OutcomeApplied,
		Affected:   []domain.FileManifestEntry{first},
		ObservedAt: actionTestNow,
		Evidence:   []string{"first_file_published"},
	}, err: placement.ErrSourceChanged}
	handler, err := NewCopyHandler(FilesystemConfig{Read: read, Action: actionPort, Options: testOptions()})
	if err != nil {
		t.Fatal(err)
	}
	desired, err := EncodeIntent(FileIntent{Files: []ports.FileMap{firstMap, secondMap}})
	if err != nil {
		t.Fatal(err)
	}
	store, journal, action := newActionsSQLFixture(t, "filesystem-partial-effects", desired)
	defer store.Close()
	executor, err := execution.New(journal, execution.Options{WorkerID: "actions-integration", Now: testOptions().Now})
	if err != nil {
		t.Fatal(err)
	}
	if err := executor.RegisterHandler(handler); err != nil {
		t.Fatal(err)
	}
	batch, err := executor.RunOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(batch.Results) != 1 || batch.Results[0].State != domain.ActionReconciling || !batch.Results[0].Dispatched {
		t.Fatalf("partial filesystem dispatch must enter reconciliation: batch=%#v", batch)
	}
	current, err := journal.GetAction(context.Background(), action.ID)
	if err != nil {
		t.Fatal(err)
	}
	if current.State != domain.ActionReconciling || current.UnresolvedCount != 1 {
		t.Fatalf("partial filesystem dispatch state=%#v, want reconciling with one unresolved effect", current)
	}
	effects, err := journal.ListEffects(context.Background(), action.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(effects) != 2 {
		t.Fatalf("persisted effects=%#v, want two exact planned effects", effects)
	}
	if effects[0].State != execution.EffectApplied || !strings.Contains(string(effects[0].Evidence), "filesystem_affected") || !strings.Contains(string(effects[0].Evidence), "first_file_published") {
		t.Fatalf("affected effect evidence/state was lost: %#v", effects[0])
	}
	if effects[1].State != execution.EffectUnknown || !strings.Contains(string(effects[1].Evidence), "dispatch_result_unreported") {
		t.Fatalf("omitted effect must remain explicitly unknown: %#v", effects[1])
	}
}

func newActionsSQLFixture(t *testing.T, actionID string, desired []byte) (*storage.Store, *execution.SQLJournal, execution.Action) {
	t.Helper()
	path := filepath.Join(t.TempDir(), actionID+".sqlite")
	store, err := storage.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	created := actionTestNow.Format("2006-01-02T15:04:05Z07:00")
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
	if _, err := store.Queries().CreateActionRun(ctx, &sqlc.CreateActionRunParams{ID: actionID, PlanID: planID, PlanRevision: 1, PlanDigest: digest, State: string(domain.ActionQueued), DesiredStateJson: string(desired), Version: 1, OutcomeJson: `{}`, CreatedAt: created, UpdatedAt: created}); err != nil {
		store.Close()
		t.Fatal(err)
	}
	journal, err := execution.NewSQLJournal(store)
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
