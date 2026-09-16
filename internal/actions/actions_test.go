package actions

import (
	"context"
	"errors"
	"io/fs"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/guilycst/mastarr/internal/descriptors"
	"github.com/guilycst/mastarr/internal/domain"
	"github.com/guilycst/mastarr/internal/execution"
	"github.com/guilycst/mastarr/internal/filesystem/organize"
	"github.com/guilycst/mastarr/internal/filesystem/placement"
	"github.com/guilycst/mastarr/internal/ports"
)

var actionTestNow = time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)

func testOptions() HandlerOptions {
	return HandlerOptions{Now: func() time.Time { return actionTestNow }, MaxItems: 50, MaxFiles: 50}
}

func testAction(t *testing.T, kind domain.ActionKind, desired any) execution.Action {
	t.Helper()
	encoded, err := EncodeIntent(desired)
	if err != nil {
		t.Fatal(err)
	}
	return execution.Action{ID: "action-1", PlanID: "plan-1", PlanRevision: 1, PlanDigest: "digest-1", Kind: kind, State: domain.ActionQueued, DesiredState: encoded, Version: 1, Outcome: []byte(`{}`)}
}

func testConnection() domain.ConfigID { return domain.ConfigID("arr-main") }

type fakeCapabilityPort struct {
	values []domain.Capability
	err    error
}

func (fake fakeCapabilityPort) Capabilities(context.Context, domain.ConfigID) ([]domain.Capability, error) {
	return append([]domain.Capability(nil), fake.values...), fake.err
}

type fakeArrRead struct {
	list         []ports.MediaRecord
	preview      ports.ImportPreview
	imported     ports.ImportObservation
	listCalls    int
	previewCalls int
	importCalls  int
	err          error
}

func (fake *fakeArrRead) List(context.Context, domain.ConfigID, string, int) (ports.Page[ports.MediaRecord], error) {
	fake.listCalls++
	if fake.err != nil {
		return ports.Page[ports.MediaRecord]{}, fake.err
	}
	return ports.Page[ports.MediaRecord]{Items: append([]ports.MediaRecord(nil), fake.list...), Coverage: domain.Coverage{Completeness: domain.CompletenessComplete, ObservedAt: actionTestNow}}, nil
}

func (fake *fakeArrRead) Lookup(context.Context, domain.ConfigID, string, domain.MediaKind) ([]ports.MediaRecord, error) {
	return nil, errors.New("lookup not used")
}

func (fake *fakeArrRead) Options(context.Context, domain.ConfigID) (ports.ManagerOptions, error) {
	return ports.ManagerOptions{}, errors.New("options not used")
}

func (fake *fakeArrRead) PreviewImport(context.Context, domain.ConfigID, ports.ImportPreviewRequest) (ports.ImportPreview, error) {
	fake.previewCalls++
	if fake.err != nil {
		return ports.ImportPreview{}, fake.err
	}
	return fake.preview, nil
}

func (fake *fakeArrRead) ObserveImport(context.Context, domain.ConfigID, string) (ports.ImportObservation, error) {
	fake.importCalls++
	if fake.err != nil {
		return ports.ImportObservation{}, fake.err
	}
	return fake.imported, nil
}

type fakeArrWrite struct {
	registerCalls int
	importCalls   int
}

func (fake *fakeArrWrite) Register(context.Context, domain.ConfigID, ports.RegistrationRequest) (ports.RegistrationResult, error) {
	fake.registerCalls++
	return ports.RegistrationResult{ExternalID: "201", Effect: ports.ClientEffect{Outcome: domain.OutcomeApplied}}, nil
}

func (fake *fakeArrWrite) Import(context.Context, domain.ConfigID, ports.ImportRequest) (ports.ImportObservation, error) {
	fake.importCalls++
	return ports.ImportObservation{ExternalID: "201", Effect: func() *ports.ClientEffect { value := ports.ClientEffect{Outcome: domain.OutcomeApplied}; return &value }()}, nil
}

func testCapability(name string, state domain.CapabilityState) domain.Capability {
	return domain.Capability{Name: name, State: state, Version: "fixture-1", Evidence: []string{"synthetic"}, ObservedAt: actionTestNow}
}

func TestRegistrationObserveBeforeG01BlockedDispatch(t *testing.T) {
	read := &fakeArrRead{list: []ports.MediaRecord{{ExternalID: "201", ProviderID: "123", Kind: domain.MediaMovie, Monitored: false}}}
	write := &fakeArrWrite{}
	handler, err := NewRegistrationHandler(RegistrationConfig{Read: read, Write: write, Options: testOptions()})
	if err != nil {
		t.Fatal(err)
	}
	monitored := true
	action := testAction(t, domain.ActionArrRegistration, RegistrationIntent{ConnectionID: testConnection(), ProviderID: "123", Kind: domain.MediaMovie, Fields: ports.RegistrationFields{Monitored: &monitored}})
	_, err = handler.Dispatch(context.Background(), action, execution.Attempt{ID: "attempt-1"})
	if !errors.Is(err, ErrCapabilityUnknown) {
		t.Fatalf("expected capability block, got %v", err)
	}
	if read.listCalls == 0 || write.registerCalls != 0 {
		t.Fatalf("observe-before-write violated: reads=%d writes=%d", read.listCalls, write.registerCalls)
	}
}

func TestImportReportsPerFilePartialState(t *testing.T) {
	root := domain.ConfigID("download")
	requested := []ports.ImportFile{
		{Source: domain.FileTarget{RootID: root, RelativePath: "movie.mkv"}, MovieOrEpisodeID: "101"},
		{Source: domain.FileTarget{RootID: root, RelativePath: "movie.en.srt"}, MovieOrEpisodeID: "101", Subtitle: true},
	}
	read := &fakeArrRead{preview: ports.ImportPreview{Revision: "preview-1", Files: requested, ObservedAt: actionTestNow}, imported: ports.ImportObservation{ExternalID: "201", Files: []ports.MediaFile{{Path: requested[0].Source, ExternalID: "801", MovieID: "101"}}, ObservedAt: actionTestNow}}
	write := &fakeArrWrite{}
	handler, err := NewImportHandler(ImportConfig{Read: read, Write: write, Capabilities: fakeCapabilityPort{values: []domain.Capability{testCapability(importOperation, domain.CapabilityUnknown)}}, Options: testOptions()})
	if err != nil {
		t.Fatal(err)
	}
	action := testAction(t, domain.ActionArrImport, ImportIntent{ConnectionID: testConnection(), RegisteredExternalID: "201", PreviewRevision: "preview-1", Transfer: "copy", Files: requested})
	observation, err := handler.Observe(context.Background(), action)
	if err != nil {
		t.Fatal(err)
	}
	if observation.State != execution.ObserveNeedsAction || len(observation.Effects) != 2 {
		t.Fatalf("expected partial import observation, got %#v", observation)
	}
	if observation.Effects[0].State != execution.EffectAlreadySatisfied || observation.Effects[1].State != execution.EffectPending {
		t.Fatalf("expected per-file states, got %#v", observation.Effects)
	}
	if write.importCalls != 0 {
		t.Fatal("observe must not import")
	}
}

func TestImportRequiresExactPreviewBeforeAnyWrite(t *testing.T) {
	requested := []ports.ImportFile{{Source: domain.FileTarget{RootID: "download", RelativePath: "movie.mkv"}, MovieOrEpisodeID: "101"}}
	read := &fakeArrRead{
		preview:  ports.ImportPreview{Revision: "different-preview", Files: requested, ObservedAt: actionTestNow},
		imported: ports.ImportObservation{ExternalID: "201", ObservedAt: actionTestNow},
	}
	write := &fakeArrWrite{}
	handler, err := NewImportHandler(ImportConfig{Read: read, Write: write, Capabilities: fakeCapabilityPort{values: []domain.Capability{testCapability(importOperation, domain.CapabilitySupported)}}, Options: testOptions()})
	if err != nil {
		t.Fatal(err)
	}
	action := testAction(t, domain.ActionArrImport, ImportIntent{ConnectionID: testConnection(), RegisteredExternalID: "201", PreviewRevision: "approved-preview", Transfer: "copy", Files: requested})
	result, err := handler.Dispatch(context.Background(), action, execution.Attempt{ID: "preview-mismatch"})
	if !errors.Is(err, ErrStateUnknown) || result.Accepted || read.previewCalls != 1 || read.importCalls != 0 || write.importCalls != 0 {
		t.Fatalf("preview mismatch must fail before read-back/write: result=%#v err=%v preview=%d observe=%d writes=%d", result, err, read.previewCalls, read.importCalls, write.importCalls)
	}
}

func TestImportDoesNotTreatFileIDAsMovieAssociation(t *testing.T) {
	requested := []ports.ImportFile{{Source: domain.FileTarget{RootID: "download", RelativePath: "movie.mkv"}, MovieOrEpisodeID: "101"}}
	read := &fakeArrRead{
		preview: ports.ImportPreview{Revision: "preview-1", Files: requested, ObservedAt: actionTestNow},
		imported: ports.ImportObservation{
			ExternalID: "201", ObservedAt: actionTestNow,
			Files: []ports.MediaFile{{ExternalID: "101", Path: requested[0].Source}},
		},
	}
	handler, err := NewImportHandler(ImportConfig{Read: read, Write: &fakeArrWrite{}, Options: testOptions()})
	if err != nil {
		t.Fatal(err)
	}
	action := testAction(t, domain.ActionArrImport, ImportIntent{ConnectionID: testConnection(), RegisteredExternalID: "201", PreviewRevision: "preview-1", Transfer: "copy", Files: requested})
	observation, err := handler.Observe(context.Background(), action)
	if err != nil || observation.State != execution.ObserveNeedsAction || len(observation.Effects) != 1 || observation.Effects[0].State != execution.EffectPending {
		t.Fatalf("file ID alone must not satisfy movie association: observation=%#v err=%v", observation, err)
	}
}

type fakeFilesystemRead struct {
	mu          sync.Mutex
	entries     map[string]ports.FilesystemObservation
	missing     map[string]bool
	statCalls   int
	hashCalls   int
	defaultHash string
}

func (fake *fakeFilesystemRead) Enumerate(context.Context, domain.ConfigID, string, int) (ports.Page[domain.FileManifestEntry], error) {
	return ports.Page[domain.FileManifestEntry]{}, errors.New("enumerate not used")
}

func (fake *fakeFilesystemRead) EnumeratePage(context.Context, domain.ConfigID, string, string, int) (ports.Page[domain.FileManifestEntry], error) {
	return ports.Page[domain.FileManifestEntry]{}, errors.New("enumerate page not used")
}

func (fake *fakeFilesystemRead) Stat(_ context.Context, target domain.FileTarget) (ports.FilesystemObservation, error) {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	fake.statCalls++
	key := targetID(target)
	if fake.missing[key] {
		return ports.FilesystemObservation{}, fs.ErrNotExist
	}
	value, ok := fake.entries[key]
	if !ok {
		return ports.FilesystemObservation{}, fs.ErrNotExist
	}
	return value, nil
}

func (fake *fakeFilesystemRead) Hash(context.Context, domain.FileTarget) (string, error) {
	fake.mu.Lock()
	fake.hashCalls++
	fake.mu.Unlock()
	return fake.defaultHash, nil
}

func (fake *fakeFilesystemRead) Capabilities(context.Context, domain.ConfigID) ([]domain.Capability, error) {
	return nil, nil
}

type fakeFilesystemAction struct {
	copyCalls     int
	hardlinkCalls int
	moveCalls     int
	deleteCalls   int
	trashCalls    int
	trashRequest  ports.FilesystemTrashRequest
	copyEffect    ports.FilesystemEffect
	err           error
}

func (fake *fakeFilesystemAction) Copy(context.Context, ports.FilesystemCopyRequest) (ports.FilesystemEffect, error) {
	fake.copyCalls++
	effect := fake.copyEffect
	if effect.Outcome == "" {
		effect = ports.FilesystemEffect{Outcome: domain.OutcomeApplied, ObservedAt: actionTestNow}
	}
	return effect, fake.err
}

func (fake *fakeFilesystemAction) Hardlink(context.Context, ports.FilesystemHardlinkRequest) (ports.FilesystemEffect, error) {
	fake.hardlinkCalls++
	return ports.FilesystemEffect{Outcome: domain.OutcomeApplied, ObservedAt: actionTestNow}, fake.err
}

func (fake *fakeFilesystemAction) Move(context.Context, ports.FilesystemMoveRequest) (ports.FilesystemEffect, error) {
	fake.moveCalls++
	return ports.FilesystemEffect{}, fake.err
}

func (fake *fakeFilesystemAction) Rename(context.Context, ports.FilesystemRenameRequest) (ports.FilesystemEffect, error) {
	return ports.FilesystemEffect{}, fake.err
}

func (fake *fakeFilesystemAction) Trash(_ context.Context, request ports.FilesystemTrashRequest) (ports.FilesystemEffect, error) {
	fake.trashCalls++
	fake.trashRequest = request
	return ports.FilesystemEffect{Outcome: domain.OutcomeApplied, ObservedAt: actionTestNow}, fake.err
}

func (fake *fakeFilesystemAction) Restore(context.Context, ports.FilesystemRestoreRequest) (ports.FilesystemEffect, error) {
	return ports.FilesystemEffect{}, fake.err
}

func (fake *fakeFilesystemAction) Delete(context.Context, ports.FilesystemDeleteRequest) (ports.FilesystemEffect, error) {
	fake.deleteCalls++
	return ports.FilesystemEffect{}, fake.err
}

func manifest(root domain.ConfigID, relative, digest, identity string) domain.FileManifestEntry {
	return domain.FileManifestEntry{RootID: root, RelativePath: relative, Type: domain.ManifestFile, Size: 4, Digest: digest, FileIdentity: identity, ObservedAt: actionTestNow}
}

func TestCopyDirectHandlerObservesAndUsesExplicitPort(t *testing.T) {
	source := manifest("download", "movie.mkv", strings.Repeat("a", 64), "inode-1")
	mapping := ports.FileMap{Source: source, Destination: domain.FileTarget{RootID: "library", RelativePath: "movie.mkv"}}
	read := &fakeFilesystemRead{entries: map[string]ports.FilesystemObservation{
		targetID(sourceTarget(mapping)): {Entry: source, ObservedAt: actionTestNow},
	}, missing: map[string]bool{targetID(mapping.Destination): true}}
	filesystem := &fakeFilesystemAction{}
	handler, err := NewCopyHandler(FilesystemConfig{Read: read, Action: filesystem, Options: testOptions()})
	if err != nil {
		t.Fatal(err)
	}
	action := testAction(t, domain.ActionFSCopy, FileIntent{Files: []ports.FileMap{mapping}})
	observation, err := handler.Observe(context.Background(), action)
	if err != nil || observation.State != execution.ObserveNeedsAction {
		t.Fatalf("expected copy needed, state=%#v err=%v", observation, err)
	}
	result, err := handler.Dispatch(context.Background(), action, execution.Attempt{ID: "attempt-copy"})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Accepted || result.Outcome != domain.OutcomeApplied || filesystem.copyCalls != 1 {
		t.Fatalf("unexpected copy result %#v calls=%d", result, filesystem.copyCalls)
	}
}

func TestCopyMissingSourceAndDestinationBlocksWithoutWrite(t *testing.T) {
	source := manifest("download", "movie.mkv", strings.Repeat("a", 64), "inode-1")
	mapping := ports.FileMap{Source: source, Destination: domain.FileTarget{RootID: "library", RelativePath: "movie.mkv"}}
	read := &fakeFilesystemRead{entries: map[string]ports.FilesystemObservation{}, missing: map[string]bool{
		targetID(sourceTarget(mapping)): true,
		targetID(mapping.Destination):   true,
	}}
	actionPort := &fakeFilesystemAction{}
	handler, err := NewCopyHandler(FilesystemConfig{Read: read, Action: actionPort, Options: testOptions()})
	if err != nil {
		t.Fatal(err)
	}
	action := testAction(t, domain.ActionFSCopy, FileIntent{Files: []ports.FileMap{mapping}})
	result, err := handler.Dispatch(context.Background(), action, execution.Attempt{ID: "missing-source"})
	if !errors.Is(err, ErrStateUnknown) || result.Accepted || actionPort.copyCalls != 0 {
		t.Fatalf("missing source and destination must block copy: result=%#v err=%v writes=%d", result, err, actionPort.copyCalls)
	}
}

func TestFilesystemRejectsInvalidReturnedOutcome(t *testing.T) {
	source := manifest("download", "movie.mkv", strings.Repeat("a", 64), "inode-1")
	mapping := ports.FileMap{Source: source, Destination: domain.FileTarget{RootID: "library", RelativePath: "movie.mkv"}}
	read := &fakeFilesystemRead{entries: map[string]ports.FilesystemObservation{
		targetID(sourceTarget(mapping)): {Entry: source, ObservedAt: actionTestNow},
	}, missing: map[string]bool{targetID(mapping.Destination): true}}
	actionPort := &fakeFilesystemAction{copyEffect: ports.FilesystemEffect{Outcome: "bogus", ObservedAt: actionTestNow}}
	handler, err := NewCopyHandler(FilesystemConfig{Read: read, Action: actionPort, Options: testOptions()})
	if err != nil {
		t.Fatal(err)
	}
	action := testAction(t, domain.ActionFSCopy, FileIntent{Files: []ports.FileMap{mapping}})
	result, err := handler.Dispatch(context.Background(), action, execution.Attempt{ID: "invalid-copy-outcome"})
	if !errors.Is(err, ErrStateUnknown) || result.Accepted || actionPort.copyCalls != 1 {
		t.Fatalf("invalid filesystem outcome must be uncertain: result=%#v err=%v calls=%d", result, err, actionPort.copyCalls)
	}
}

func TestFilesystemLinkedClientBlocksActiveDownload(t *testing.T) {
	source := manifest("download", "movie.mkv", strings.Repeat("a", 64), "inode-1")
	mapping := ports.FileMap{Source: source, Destination: domain.FileTarget{RootID: "library", RelativePath: "movie.mkv"}}
	read := &fakeFilesystemRead{entries: map[string]ports.FilesystemObservation{targetID(sourceTarget(mapping)): {Entry: source, ObservedAt: actionTestNow}}, missing: map[string]bool{targetID(mapping.Destination): true}}
	client := &fakeDownloadControl{observation: ports.DownloadObservation{Ref: ports.DownloadRef{ConnectionID: "qbit", ExternalID: "hash-1"}, State: "downloading"}}
	filesystem := &fakeFilesystemAction{}
	handler, err := NewCopyHandler(FilesystemConfig{Read: read, Action: filesystem, Options: HandlerOptions{Now: testOptions().Now, LinkedClient: &LinkedClient{Control: client, Ref: client.observation.Ref}}})
	if err != nil {
		t.Fatal(err)
	}
	action := testAction(t, domain.ActionFSCopy, FileIntent{Files: []ports.FileMap{mapping}, LinkedDownload: &client.observation.Ref})
	result, err := handler.Dispatch(context.Background(), action, execution.Attempt{ID: "attempt-linked"})
	if !errors.Is(err, ErrStateUnknown) || result.Accepted || filesystem.copyCalls != 0 {
		t.Fatalf("active linked client must block copy: result=%#v err=%v calls=%d", result, err, filesystem.copyCalls)
	}
}

func TestLinuxOrganizeMutationsPropagateCapabilityBlock(t *testing.T) {
	source := manifest("download", "movie.mkv", strings.Repeat("a", 64), "inode-1")
	mapping := ports.FileMap{Source: source, Destination: domain.FileTarget{RootID: "library", RelativePath: "movie.mkv"}}
	read := &fakeFilesystemRead{entries: map[string]ports.FilesystemObservation{targetID(sourceTarget(mapping)): {Entry: source, ObservedAt: actionTestNow}}, missing: map[string]bool{targetID(mapping.Destination): true}}
	filesystem := &fakeFilesystemAction{err: organize.ErrUnsupported}
	handler, err := NewMoveHandler(FilesystemConfig{Read: read, Action: filesystem, Options: testOptions()})
	if err != nil {
		t.Fatal(err)
	}
	action := testAction(t, domain.ActionFSMove, FileIntent{Files: []ports.FileMap{mapping}})
	result, err := handler.Dispatch(context.Background(), action, execution.Attempt{ID: "attempt-move"})
	if !errors.Is(err, organize.ErrUnsupported) || result.Accepted || filesystem.moveCalls != 1 {
		t.Fatalf("expected explicit organizer capability block: result=%#v err=%v", result, err)
	}
	var failure *execution.Failure
	if !errors.As(err, &failure) || failure.Dispatched {
		t.Fatalf("unsupported organize write must be pre-dispatch failure: %#v", failure)
	}
}

func TestTrashPreservesExplicitCustomRetention(t *testing.T) {
	entry := manifest("download", "movie.mkv", "", "inode-1")
	read := &fakeFilesystemRead{entries: map[string]ports.FilesystemObservation{
		targetID(domain.FileTarget{RootID: entry.RootID, RelativePath: entry.RelativePath}): {Entry: entry, ObservedAt: actionTestNow},
	}}
	actionPort := &fakeFilesystemAction{}
	handler, err := NewTrashHandler(FilesystemConfig{Read: read, Action: actionPort, Options: testOptions()})
	if err != nil {
		t.Fatal(err)
	}
	retention := 7 * 24 * time.Hour
	action := testAction(t, domain.ActionFSTrash, FileIntent{Manifest: []domain.FileManifestEntry{entry}, Retention: retention})
	result, err := handler.Dispatch(context.Background(), action, execution.Attempt{ID: "custom-retention"})
	if err != nil || !result.Accepted || actionPort.trashCalls != 1 || actionPort.trashRequest.Retention != retention {
		t.Fatalf("custom retention must reach action port unchanged: result=%#v err=%v calls=%d retention=%s", result, err, actionPort.trashCalls, actionPort.trashRequest.Retention)
	}
}

type fakeDownloadControl struct {
	mu                 sync.Mutex
	observation        ports.DownloadObservation
	observeCalls       int
	stopCalls          int
	removeCalls        int
	relocateCalls      int
	renameFileCalls    int
	renameFolderCalls  int
	relocateEffect     ports.ClientEffect
	renameFileEffect   ports.ClientEffect
	renameFolderEffect ports.ClientEffect
}

func (fake *fakeDownloadControl) Observe(_ context.Context, ref ports.DownloadRef) (ports.DownloadObservation, error) {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	fake.observeCalls++
	if fake.observation.Ref != ref {
		return ports.DownloadObservation{}, errors.New("unexpected download reference")
	}
	return fake.observation, nil
}

func (fake *fakeDownloadControl) Stop(context.Context, ports.DownloadRef) (ports.ClientEffect, error) {
	fake.mu.Lock()
	fake.stopCalls++
	fake.observation.State = "stoppedUP"
	fake.mu.Unlock()
	return ports.ClientEffect{Outcome: domain.OutcomeApplied}, nil
}

func (fake *fakeDownloadControl) Relocate(context.Context, ports.DownloadRef, domain.FileTarget) (ports.ClientEffect, error) {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	fake.relocateCalls++
	if fake.relocateEffect.Outcome == "" {
		fake.relocateEffect.Outcome = domain.OutcomeApplied
	}
	return fake.relocateEffect, nil
}

func (fake *fakeDownloadControl) RenameFile(context.Context, ports.DownloadRef, domain.FileTarget, string) (ports.ClientEffect, error) {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	fake.renameFileCalls++
	if fake.renameFileEffect.Outcome == "" {
		fake.renameFileEffect.Outcome = domain.OutcomeApplied
	}
	return fake.renameFileEffect, nil
}

func (fake *fakeDownloadControl) RenameFolder(context.Context, ports.DownloadRef, domain.FileTarget, string) (ports.ClientEffect, error) {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	fake.renameFolderCalls++
	if fake.renameFolderEffect.Outcome == "" {
		fake.renameFolderEffect.Outcome = domain.OutcomeApplied
	}
	return fake.renameFolderEffect, nil
}

func (fake *fakeDownloadControl) Remove(context.Context, ports.DownloadRef) (ports.ClientEffect, error) {
	fake.removeCalls++
	return ports.ClientEffect{Outcome: domain.OutcomeApplied}, nil
}

func TestClientStopUsesIndependentCapability(t *testing.T) {
	control := &fakeDownloadControl{observation: ports.DownloadObservation{Ref: ports.DownloadRef{ConnectionID: "qbit", ExternalID: "hash-1"}, State: "downloading", ObservedAt: actionTestNow}}
	handlers, err := NewClientHandlers(ClientConfig{Control: control, Capabilities: fakeCapabilityPort{values: []domain.Capability{testCapability(clientStopOperation, domain.CapabilitySupported)}}, Options: testOptions()})
	if err != nil {
		t.Fatal(err)
	}
	action := testAction(t, domain.ActionClientStop, ClientIntent{Ref: control.observation.Ref})
	result, err := handlers.Stop.Dispatch(context.Background(), action, execution.Attempt{ID: "attempt-stop"})
	if err != nil || !result.Accepted || control.stopCalls != 1 {
		t.Fatalf("unexpected stop result=%#v err=%v calls=%d", result, err, control.stopCalls)
	}
}

func TestNativeMoveUsesLinkedClientAndNeverFilesystemMove(t *testing.T) {
	source := manifest("download", "incoming/movie.mkv", strings.Repeat("a", 64), "inode-1")
	mapping := ports.FileMap{Source: source, Destination: domain.FileTarget{RootID: "library", RelativePath: "Movies/movie.mkv"}}
	read := &fakeFilesystemRead{
		entries: map[string]ports.FilesystemObservation{targetID(sourceTarget(mapping)): {Entry: source, ObservedAt: actionTestNow}},
		missing: map[string]bool{targetID(mapping.Destination): true},
	}
	filesystem := &fakeFilesystemAction{}
	ref := ports.DownloadRef{ConnectionID: "qbit", ExternalID: "hash-1"}
	client := &fakeDownloadControl{observation: ports.DownloadObservation{Ref: ref, State: "stoppedUP", ObservedAt: actionTestNow}}
	handler, err := NewMoveHandler(FilesystemConfig{Read: read, Action: filesystem, Options: HandlerOptions{
		Now: testOptions().Now, LinkedClient: &LinkedClient{Control: client, Ref: ref},
	}})
	if err != nil {
		t.Fatal(err)
	}
	action := testAction(t, domain.ActionFSMove, FileIntent{Executor: executorNativeClient, Files: []ports.FileMap{mapping}, LinkedDownload: &ref})
	result, err := handler.Dispatch(context.Background(), action, execution.Attempt{ID: "native-move"})
	if err != nil || !result.Accepted || result.Outcome != domain.OutcomeApplied {
		t.Fatalf("unexpected native move result=%#v err=%v", result, err)
	}
	if client.relocateCalls != 1 || filesystem.moveCalls != 0 {
		t.Fatalf("native move dispatch counts: relocate=%d filesystem=%d", client.relocateCalls, filesystem.moveCalls)
	}
}

func TestNativeRenameSelectsExactFileOperation(t *testing.T) {
	source := manifest("download", "incoming/movie.mkv", strings.Repeat("a", 64), "inode-1")
	mapping := ports.FileMap{Source: source, Destination: domain.FileTarget{RootID: "download", RelativePath: "incoming/renamed.mkv"}}
	read := &fakeFilesystemRead{
		entries: map[string]ports.FilesystemObservation{targetID(sourceTarget(mapping)): {Entry: source, ObservedAt: actionTestNow}},
		missing: map[string]bool{targetID(mapping.Destination): true},
	}
	filesystem := &fakeFilesystemAction{}
	ref := ports.DownloadRef{ConnectionID: "qbit", ExternalID: "hash-1"}
	client := &fakeDownloadControl{observation: ports.DownloadObservation{Ref: ref, State: "stoppedUP", ObservedAt: actionTestNow}}
	handler, err := NewRenameHandler(FilesystemConfig{Read: read, Action: filesystem, Options: HandlerOptions{
		Now: testOptions().Now, LinkedClient: &LinkedClient{Control: client, Ref: ref},
	}})
	if err != nil {
		t.Fatal(err)
	}
	action := testAction(t, domain.ActionFSRename, FileIntent{Executor: executorNativeClient, Files: []ports.FileMap{mapping}, LinkedDownload: &ref})
	result, err := handler.Dispatch(context.Background(), action, execution.Attempt{ID: "native-rename"})
	if err != nil || !result.Accepted || result.Outcome != domain.OutcomeApplied {
		t.Fatalf("unexpected native rename result=%#v err=%v", result, err)
	}
	if client.renameFileCalls != 1 || client.renameFolderCalls != 0 || filesystem.moveCalls != 0 {
		t.Fatalf("native rename dispatch counts: file=%d folder=%d filesystem-move=%d", client.renameFileCalls, client.renameFolderCalls, filesystem.moveCalls)
	}
}

func TestNativeRenameRejectsDifferentRootsBeforeNativeCall(t *testing.T) {
	source := manifest("download", "incoming/movie.mkv", strings.Repeat("a", 64), "inode-1")
	mapping := ports.FileMap{Source: source, Destination: domain.FileTarget{RootID: "library", RelativePath: "incoming/renamed.mkv"}}
	read := &fakeFilesystemRead{
		entries: map[string]ports.FilesystemObservation{targetID(sourceTarget(mapping)): {Entry: source, ObservedAt: actionTestNow}},
		missing: map[string]bool{targetID(mapping.Destination): true},
	}
	filesystem := &fakeFilesystemAction{}
	ref := ports.DownloadRef{ConnectionID: "qbit", ExternalID: "hash-1"}
	client := &fakeDownloadControl{observation: ports.DownloadObservation{Ref: ref, State: "stoppedUP", ObservedAt: actionTestNow}}
	handler, err := NewRenameHandler(FilesystemConfig{Read: read, Action: filesystem, Options: HandlerOptions{
		Now: testOptions().Now, LinkedClient: &LinkedClient{Control: client, Ref: ref},
	}})
	if err != nil {
		t.Fatal(err)
	}
	action := testAction(t, domain.ActionFSRename, FileIntent{Executor: executorNativeClient, Files: []ports.FileMap{mapping}, LinkedDownload: &ref})
	for repetition := 0; repetition < 10; repetition++ {
		result, dispatchErr := handler.Dispatch(context.Background(), action, execution.Attempt{ID: "native-cross-root"})
		if !errors.Is(dispatchErr, ErrInvalidIntent) || result.Accepted || client.observeCalls != 0 || client.renameFileCalls != 0 || client.renameFolderCalls != 0 {
			t.Fatalf("cross-root native rename must reject before any client call (run %d): result=%#v err=%v observe=%d file=%d folder=%d", repetition, result, dispatchErr, client.observeCalls, client.renameFileCalls, client.renameFolderCalls)
		}
	}
}

func TestImportReconcilePreservesPartialEffectStatesAndDoesNotRetry(t *testing.T) {
	requested := []ports.ImportFile{
		{Source: domain.FileTarget{RootID: "download", RelativePath: "movie.mkv"}, MovieOrEpisodeID: "101"},
		{Source: domain.FileTarget{RootID: "download", RelativePath: "movie.en.srt"}, MovieOrEpisodeID: "101", Subtitle: true},
	}
	read := &fakeArrRead{
		preview:  ports.ImportPreview{Revision: "preview-1", Files: requested, ObservedAt: actionTestNow},
		imported: ports.ImportObservation{ExternalID: "201", Files: []ports.MediaFile{{Path: requested[0].Source, ExternalID: "801", MovieID: "101"}}, ObservedAt: actionTestNow},
	}
	handler, err := NewImportHandler(ImportConfig{Read: read, Write: &fakeArrWrite{}, Options: testOptions()})
	if err != nil {
		t.Fatal(err)
	}
	action := testAction(t, domain.ActionArrImport, ImportIntent{ConnectionID: testConnection(), RegisteredExternalID: "201", PreviewRevision: "preview-1", Transfer: "copy", Files: requested})
	for repetition := 0; repetition < 10; repetition++ {
		result, reconcileErr := handler.Reconcile(context.Background(), action, execution.Attempt{ID: "import-reconcile"})
		if reconcileErr != nil || result.SafeToRetry || len(result.Effects) != 2 {
			t.Fatalf("partial import must remain unresolved and non-retryable (run %d): result=%#v err=%v", repetition, result, reconcileErr)
		}
		if result.Effects[0].State != execution.EffectAlreadySatisfied || result.Effects[1].State != execution.EffectPending {
			t.Fatalf("partial import effect states changed (run %d): %#v", repetition, result.Effects)
		}
	}
}

func TestFilesystemReconcilePreservesPartialEffectStatesAndDoesNotRetry(t *testing.T) {
	first := manifest("download", "one.mkv", strings.Repeat("a", 64), "inode-1")
	second := manifest("download", "two.mkv", strings.Repeat("b", 64), "inode-2")
	mappings := []ports.FileMap{
		{Source: first, Destination: domain.FileTarget{RootID: "library", RelativePath: "one.mkv"}},
		{Source: second, Destination: domain.FileTarget{RootID: "library", RelativePath: "two.mkv"}},
	}
	read := &fakeFilesystemRead{
		entries: map[string]ports.FilesystemObservation{
			targetID(sourceTarget(mappings[0])): {Entry: first, ObservedAt: actionTestNow},
			targetID(mappings[0].Destination):   {Entry: destinationManifest(mappings[0].Destination, first), ObservedAt: actionTestNow},
			targetID(sourceTarget(mappings[1])): {Entry: second, ObservedAt: actionTestNow},
		},
		missing: map[string]bool{targetID(mappings[1].Destination): true},
	}
	handler, err := NewCopyHandler(FilesystemConfig{Read: read, Action: &fakeFilesystemAction{}, Options: testOptions()})
	if err != nil {
		t.Fatal(err)
	}
	action := testAction(t, domain.ActionFSCopy, FileIntent{Files: mappings})
	for repetition := 0; repetition < 10; repetition++ {
		result, reconcileErr := handler.Reconcile(context.Background(), action, execution.Attempt{ID: "filesystem-reconcile"})
		if reconcileErr != nil || result.SafeToRetry || len(result.Effects) != 2 {
			t.Fatalf("partial filesystem operation must remain unresolved and non-retryable (run %d): result=%#v err=%v", repetition, result, reconcileErr)
		}
		if result.Effects[0].State != execution.EffectAlreadySatisfied || result.Effects[1].State != execution.EffectPending {
			t.Fatalf("partial filesystem effect states changed (run %d): %#v", repetition, result.Effects)
		}
	}
}

func destinationManifest(destination domain.FileTarget, source domain.FileManifestEntry) domain.FileManifestEntry {
	source.RootID = destination.RootID
	source.RelativePath = destination.RelativePath
	return source
}

func TestFilesystemDispatchPreservesAffectedEffectOnErrorAsDispatched(t *testing.T) {
	source := manifest("download", "movie.mkv", strings.Repeat("a", 64), "inode-1")
	mapping := ports.FileMap{Source: source, Destination: domain.FileTarget{RootID: "library", RelativePath: "movie.mkv"}}
	read := &fakeFilesystemRead{
		entries: map[string]ports.FilesystemObservation{targetID(sourceTarget(mapping)): {Entry: source, ObservedAt: actionTestNow}},
		missing: map[string]bool{targetID(mapping.Destination): true},
	}
	actionPort := &fakeFilesystemAction{
		copyEffect: ports.FilesystemEffect{Outcome: domain.OutcomeApplied, Affected: []domain.FileManifestEntry{source}, Evidence: []string{"first_file_published"}, ObservedAt: actionTestNow},
		err:        placement.ErrSourceChanged,
	}
	handler, err := NewCopyHandler(FilesystemConfig{Read: read, Action: actionPort, Options: testOptions()})
	if err != nil {
		t.Fatal(err)
	}
	action := testAction(t, domain.ActionFSCopy, FileIntent{Files: []ports.FileMap{mapping}})
	for repetition := 0; repetition < 10; repetition++ {
		result, dispatchErr := handler.Dispatch(context.Background(), action, execution.Attempt{ID: "partial-copy"})
		var failure *execution.Failure
		if !errors.As(dispatchErr, &failure) || !failure.Dispatched || len(result.Effects) != 1 || result.Effects[0].State != execution.EffectApplied {
			t.Fatalf("affected filesystem result must remain dispatched/uncertain (run %d): result=%#v err=%v failure=%#v", repetition, result, dispatchErr, failure)
		}
		if !strings.Contains(string(result.Effects[0].Evidence), "filesystem_affected") || !strings.Contains(string(result.Effects[0].Evidence), "first_file_published") {
			t.Fatalf("affected evidence was discarded (run %d): %s", repetition, result.Effects[0].Evidence)
		}
	}
}

func TestFilesystemLinkedClientResolvesActionReferenceAndSharesReservation(t *testing.T) {
	source := manifest("download", "movie.mkv", strings.Repeat("a", 64), "inode-1")
	mapping := ports.FileMap{Source: source, Destination: domain.FileTarget{RootID: "library", RelativePath: "movie.mkv"}}
	refA := ports.DownloadRef{ConnectionID: "qbit", ExternalID: "hash-a"}
	refB := ports.DownloadRef{ConnectionID: "qbit", ExternalID: "hash-b"}
	clientA := &fakeDownloadControl{observation: ports.DownloadObservation{Ref: refA, State: "stoppedUP", ObservedAt: actionTestNow}}
	clientB := &fakeDownloadControl{observation: ports.DownloadObservation{Ref: refB, State: "stoppedUP", ObservedAt: actionTestNow}}
	resolverCalls := make([]ports.DownloadRef, 0, 2)
	resolver := func(ref ports.DownloadRef) (ports.DownloadControlPort, error) {
		resolverCalls = append(resolverCalls, ref)
		switch ref {
		case refA:
			return clientA, nil
		case refB:
			return clientB, nil
		default:
			return nil, errors.New("unexpected reference")
		}
	}
	read := &fakeFilesystemRead{
		entries: map[string]ports.FilesystemObservation{targetID(sourceTarget(mapping)): {Entry: source, ObservedAt: actionTestNow}},
		missing: map[string]bool{targetID(mapping.Destination): true},
	}
	handler, err := NewCopyHandler(FilesystemConfig{Read: read, Action: &fakeFilesystemAction{}, Options: HandlerOptions{
		Now: testOptions().Now, LinkedClient: &LinkedClient{Resolve: resolver},
	}})
	if err != nil {
		t.Fatal(err)
	}
	action := testAction(t, domain.ActionFSCopy, FileIntent{Files: []ports.FileMap{mapping}, LinkedDownload: &refB})
	if _, err := handler.Dispatch(context.Background(), action, execution.Attempt{ID: "linked-reference"}); err != nil {
		t.Fatal(err)
	}
	if clientA.observeCalls != 0 || clientB.observeCalls == 0 || len(resolverCalls) == 0 {
		t.Fatalf("linked action was not routed by immutable reference: resolver=%#v clientA=%d clientB=%d", resolverCalls, clientA.observeCalls, clientB.observeCalls)
	}
	reservations := handler.Reservations(action)
	wantClientKey := "client:" + refID(refB)
	if !containsString(reservations, wantClientKey) {
		t.Fatalf("linked filesystem reservation missing shared client key %q: %#v", wantClientKey, reservations)
	}
	clientHandlers, err := NewClientHandlers(ClientConfig{Control: clientB, Capabilities: fakeCapabilityPort{values: []domain.Capability{testCapability(clientStopOperation, domain.CapabilitySupported)}}, Options: testOptions()})
	if err != nil {
		t.Fatal(err)
	}
	clientAction := testAction(t, domain.ActionClientStop, ClientIntent{Ref: refB})
	if !containsString(clientHandlers.Stop.Reservations(clientAction), wantClientKey) {
		t.Fatalf("client action reservation does not share linked key %q: %#v", wantClientKey, clientHandlers.Stop.Reservations(clientAction))
	}
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func TestNativeClientRejectsMultiMapBeforeAnyNativeOrFilesystemCall(t *testing.T) {
	first := manifest("download", "one.mkv", strings.Repeat("a", 64), "inode-1")
	second := manifest("download", "two.mkv", strings.Repeat("b", 64), "inode-2")
	ref := ports.DownloadRef{ConnectionID: "qbit", ExternalID: "hash-1"}
	client := &fakeDownloadControl{observation: ports.DownloadObservation{Ref: ref, State: "stoppedUP", ObservedAt: actionTestNow}}
	filesystem := &fakeFilesystemAction{}
	read := &fakeFilesystemRead{entries: map[string]ports.FilesystemObservation{}, missing: map[string]bool{}}
	handler, err := NewMoveHandler(FilesystemConfig{Read: read, Action: filesystem, Options: HandlerOptions{
		Now: testOptions().Now, LinkedClient: &LinkedClient{Control: client, Ref: ref},
	}})
	if err != nil {
		t.Fatal(err)
	}
	action := testAction(t, domain.ActionFSMove, FileIntent{Executor: executorNativeClient, Files: []ports.FileMap{
		{Source: first, Destination: domain.FileTarget{RootID: "library", RelativePath: "one.mkv"}},
		{Source: second, Destination: domain.FileTarget{RootID: "library", RelativePath: "two.mkv"}},
	}, LinkedDownload: &ref})
	result, err := handler.Dispatch(context.Background(), action, execution.Attempt{ID: "native-multi"})
	if !errors.Is(err, ErrInvalidIntent) || result.Accepted || client.relocateCalls != 0 || filesystem.moveCalls != 0 {
		t.Fatalf("multi-map native move must reject before calls: result=%#v err=%v relocate=%d filesystem=%d", result, err, client.relocateCalls, filesystem.moveCalls)
	}
}

type fakeDescriptorPort struct {
	record      descriptors.Record
	getCalls    int
	deleteCalls int
}

func (fake *fakeDescriptorPort) Get(context.Context, string) (descriptors.Record, error) {
	fake.getCalls++
	return fake.record, nil
}

func (fake *fakeDescriptorPort) Delete(context.Context, descriptors.DeleteRequest) (descriptors.Record, error) {
	fake.deleteCalls++
	now := actionTestNow
	fake.record.DeletedAt = &now
	fake.record.Retention = descriptors.RetentionDeleted
	return fake.record, nil
}

func TestDescriptorDeleteRequiresAcknowledgementAndIsIdempotent(t *testing.T) {
	descriptor := &fakeDescriptorPort{record: descriptors.Record{ID: "descriptor-1", Retention: descriptors.RetentionRetain}}
	handler, err := NewDescriptorHandler(DescriptorConfig{Port: descriptor, Options: testOptions()})
	if err != nil {
		t.Fatal(err)
	}
	action := testAction(t, domain.ActionDescriptorDelete, DescriptorIntent{DescriptorID: "descriptor-1", IrreversibleAcknowledged: true})
	result, err := handler.Dispatch(context.Background(), action, execution.Attempt{ID: "attempt-descriptor"})
	if err != nil || !result.Accepted || descriptor.deleteCalls != 1 {
		t.Fatalf("unexpected descriptor result=%#v err=%v calls=%d", result, err, descriptor.deleteCalls)
	}
	result, err = handler.Dispatch(context.Background(), action, execution.Attempt{ID: "attempt-descriptor-repeat"})
	if err != nil || result.Outcome != domain.OutcomeAlreadySatisfied || descriptor.deleteCalls != 1 {
		t.Fatalf("repeat descriptor delete must be idempotent: result=%#v err=%v calls=%d", result, err, descriptor.deleteCalls)
	}
}

type fakeRefresh struct{ calls int }

func (fake *fakeRefresh) Refresh(context.Context, domain.ConfigID, ports.RefreshRequest) (ports.RefreshResult, error) {
	fake.calls++
	return ports.RefreshResult{Accepted: true, ObservedAt: actionTestNow}, nil
}

func TestRefreshUnknownCapabilityDoesNotDispatch(t *testing.T) {
	refresh := &fakeRefresh{}
	handler, err := NewRefreshHandler(RefreshConfig{Refresh: refresh, Capabilities: fakeCapabilityPort{values: []domain.Capability{testCapability(refreshLibraryOperation, domain.CapabilityUnknown)}}, Options: testOptions()})
	if err != nil {
		t.Fatal(err)
	}
	action := testAction(t, domain.ActionJellyfinRefresh, RefreshIntent{ConnectionID: testConnection(), Scope: ports.RefreshLibrary})
	result, err := handler.Dispatch(context.Background(), action, execution.Attempt{ID: "attempt-refresh"})
	if !errors.Is(err, ErrStateUnknown) || result.Accepted || refresh.calls != 0 {
		t.Fatalf("unknown refresh capability must block dispatch: result=%#v err=%v calls=%d", result, err, refresh.calls)
	}
}
