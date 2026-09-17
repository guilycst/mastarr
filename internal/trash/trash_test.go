package trash

import (
	"context"
	"errors"
	"io/fs"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/guilycst/mastarr/internal/domain"
	"github.com/guilycst/mastarr/internal/ports"
	"github.com/guilycst/mastarr/internal/storage"
	"github.com/guilycst/mastarr/internal/storage/sqlc"
)

var errSyntheticMissing = fs.ErrNotExist

type fakeClock struct {
	mu  sync.RWMutex
	now time.Time
}

func (clock *fakeClock) Now() time.Time {
	clock.mu.RLock()
	defer clock.mu.RUnlock()
	return clock.now
}

func (clock *fakeClock) Set(value time.Time) {
	clock.mu.Lock()
	clock.now = value
	clock.mu.Unlock()
}

type fakeRead struct {
	mu    sync.Mutex
	stats map[string]ports.FilesystemObservation
	errs  map[string]error
	calls int
}

func (read *fakeRead) Stat(_ context.Context, target domain.FileTarget) (ports.FilesystemObservation, error) {
	read.mu.Lock()
	defer read.mu.Unlock()
	read.calls++
	key := targetKey(target)
	if err := read.errs[key]; err != nil {
		return ports.FilesystemObservation{}, err
	}
	if observation, ok := read.stats[key]; ok {
		return observation, nil
	}
	return ports.FilesystemObservation{}, errSyntheticMissing
}

func (read *fakeRead) Enumerate(context.Context, domain.ConfigID, string, int) (ports.Page[domain.FileManifestEntry], error) {
	return ports.Page[domain.FileManifestEntry]{}, nil
}

func (read *fakeRead) EnumeratePage(context.Context, domain.ConfigID, string, string, int) (ports.Page[domain.FileManifestEntry], error) {
	return ports.Page[domain.FileManifestEntry]{}, nil
}

func (read *fakeRead) Hash(context.Context, domain.FileTarget) (string, error) { return "", nil }

func (read *fakeRead) Capabilities(context.Context, domain.ConfigID) ([]domain.Capability, error) {
	return nil, nil
}

func (read *fakeRead) set(target domain.FileTarget, observation ports.FilesystemObservation) {
	read.mu.Lock()
	if read.stats == nil {
		read.stats = make(map[string]ports.FilesystemObservation)
	}
	if read.errs == nil {
		read.errs = make(map[string]error)
	}
	read.stats[targetKey(target)] = observation
	delete(read.errs, targetKey(target))
	read.mu.Unlock()
}

func (read *fakeRead) setErr(target domain.FileTarget, err error) {
	read.mu.Lock()
	if read.errs == nil {
		read.errs = make(map[string]error)
	}
	read.errs[targetKey(target)] = err
	read.mu.Unlock()
}

type fakeAction struct {
	mu sync.Mutex

	trashCalls    int
	deleteCalls   int
	deleteStarts  int
	restoreCalls  int
	trashErr      error
	deleteErr     error
	restoreErr    error
	trashEffect   ports.FilesystemEffect
	deleteEffect  ports.FilesystemEffect
	restoreEffect ports.FilesystemEffect
	trashRequests []ports.FilesystemTrashRequest
	callOrder     []string
	deleteBlock   <-chan struct{}
}

func (action *fakeAction) Copy(context.Context, ports.FilesystemCopyRequest) (ports.FilesystemEffect, error) {
	return ports.FilesystemEffect{}, ErrUnsupported
}

func (action *fakeAction) Hardlink(context.Context, ports.FilesystemHardlinkRequest) (ports.FilesystemEffect, error) {
	return ports.FilesystemEffect{}, ErrUnsupported
}

func (action *fakeAction) Move(context.Context, ports.FilesystemMoveRequest) (ports.FilesystemEffect, error) {
	return ports.FilesystemEffect{}, ErrUnsupported
}

func (action *fakeAction) Rename(context.Context, ports.FilesystemRenameRequest) (ports.FilesystemEffect, error) {
	return ports.FilesystemEffect{}, ErrUnsupported
}

func (action *fakeAction) Trash(_ context.Context, request ports.FilesystemTrashRequest) (ports.FilesystemEffect, error) {
	action.mu.Lock()
	action.trashCalls++
	action.callOrder = append(action.callOrder, "trash")
	action.trashRequests = append(action.trashRequests, ports.FilesystemTrashRequest{Files: cloneManifest(request.Files), Retention: request.Retention})
	effect, err := action.trashEffect, action.trashErr
	action.mu.Unlock()
	return effect, err
}

func (action *fakeAction) Restore(context.Context, ports.FilesystemRestoreRequest) (ports.FilesystemEffect, error) {
	action.mu.Lock()
	action.restoreCalls++
	action.callOrder = append(action.callOrder, "restore")
	effect, err := action.restoreEffect, action.restoreErr
	action.mu.Unlock()
	return effect, err
}

func (action *fakeAction) Delete(ctx context.Context, _ ports.FilesystemDeleteRequest) (ports.FilesystemEffect, error) {
	action.mu.Lock()
	action.deleteStarts++
	action.mu.Unlock()
	if action.deleteBlock != nil {
		select {
		case <-action.deleteBlock:
		case <-ctx.Done():
			return ports.FilesystemEffect{}, ctx.Err()
		}
	}
	action.mu.Lock()
	action.deleteCalls++
	action.callOrder = append(action.callOrder, "delete")
	effect, err := action.deleteEffect, action.deleteErr
	action.mu.Unlock()
	return effect, err
}

type fakeDownload struct {
	mu          sync.Mutex
	current     ports.DownloadObservation
	observeErr  error
	stopErr     error
	removeErr   error
	stopCalls   int
	removeCalls int
	callOrder   []string
}

func (download *fakeDownload) Observe(context.Context, ports.DownloadRef) (ports.DownloadObservation, error) {
	download.mu.Lock()
	defer download.mu.Unlock()
	download.callOrder = append(download.callOrder, "observe")
	if download.observeErr != nil {
		return ports.DownloadObservation{}, download.observeErr
	}
	return download.current, nil
}

func (download *fakeDownload) Stop(_ context.Context, _ ports.DownloadRef) (ports.ClientEffect, error) {
	download.mu.Lock()
	defer download.mu.Unlock()
	download.stopCalls++
	download.callOrder = append(download.callOrder, "stop")
	if download.stopErr != nil {
		return ports.ClientEffect{}, download.stopErr
	}
	download.current.State = "paused"
	download.current.Seeding = false
	return ports.ClientEffect{OperationID: "stop-1", Outcome: domain.OutcomeApplied, ObservedAt: download.current.ObservedAt, Evidence: []string{"synthetic_stop"}}, nil
}

func (download *fakeDownload) Relocate(context.Context, ports.DownloadRef, domain.FileTarget) (ports.ClientEffect, error) {
	return ports.ClientEffect{}, ErrUnsupported
}

func (download *fakeDownload) RenameFile(context.Context, ports.DownloadRef, domain.FileTarget, string) (ports.ClientEffect, error) {
	return ports.ClientEffect{}, ErrUnsupported
}

func (download *fakeDownload) RenameFolder(context.Context, ports.DownloadRef, domain.FileTarget, string) (ports.ClientEffect, error) {
	return ports.ClientEffect{}, ErrUnsupported
}

func (download *fakeDownload) Remove(_ context.Context, _ ports.DownloadRef) (ports.ClientEffect, error) {
	download.mu.Lock()
	defer download.mu.Unlock()
	download.removeCalls++
	download.callOrder = append(download.callOrder, "remove")
	if download.removeErr != nil {
		return ports.ClientEffect{}, download.removeErr
	}
	return ports.ClientEffect{OperationID: "remove-1", Outcome: domain.OutcomeApplied, ObservedAt: download.current.ObservedAt, Evidence: []string{"synthetic_remove"}}, nil
}

func newTrashFixture(t *testing.T, now time.Time) (*Service, *fakeClock, *fakeRead, *fakeAction, *fakeDownload, *storage.Store) {
	t.Helper()
	store, err := storage.Open(filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	ctx := context.Background()
	if _, err := store.Queries().CreateConfigSnapshot(ctx, &sqlc.CreateConfigSnapshotParams{
		ID: "w05-snapshot", Source: "api", DocumentID: "w05-runtime", Revision: "1",
		StartupAt: formatTime(now), EffectiveJson: "{}", CreatedAt: formatTime(now),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Queries().CreateStorageRoot(ctx, &sqlc.CreateStorageRootParams{
		ID: "downloads", Label: "Synthetic downloads", Purpose: "download", Path: "/synthetic/downloads",
		Source: "api", SourceSnapshotID: "w05-snapshot", Revision: "1", CapabilitiesJson: "[]",
		CreatedAt: formatTime(now), UpdatedAt: formatTime(now),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Queries().CreateConnection(ctx, &sqlc.CreateConnectionParams{
		ID: "qbt", Kind: "qbittorrent", Label: "Synthetic qBittorrent", Endpoint: "http://qbt.invalid:8080",
		Source: "api", SourceSnapshotID: "w05-snapshot", Revision: "1", CreatedAt: formatTime(now), UpdatedAt: formatTime(now),
	}); err != nil {
		t.Fatal(err)
	}
	clock := &fakeClock{now: now}
	read := &fakeRead{stats: make(map[string]ports.FilesystemObservation), errs: make(map[string]error)}
	action := &fakeAction{}
	download := &fakeDownload{}
	service, err := New(store, Options{Read: read, Action: action, Download: download, Clock: clock.Now, WorkerID: "w05-worker"})
	if err != nil {
		t.Fatal(err)
	}
	return service, clock, read, action, download, store
}

func syntheticManifest(now time.Time) domain.FileManifestEntry {
	return domain.FileManifestEntry{
		RootID: "downloads", RelativePath: "downloads/example.mkv", Type: domain.ManifestFile,
		Size: 7, Digest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		FileIdentity: "inode:example:1", Role: domain.RoleVideo, ObservedAt: now,
	}
}

func syntheticObservation(entry domain.FileManifestEntry) ports.FilesystemObservation {
	return ports.FilesystemObservation{Entry: entry, Mode: "regular", Readable: true, Writable: true, ObservedAt: entry.ObservedAt}
}

func targetKey(target domain.FileTarget) string {
	return target.RootID.String() + "\x00" + target.RelativePath
}

func clientRef() ports.DownloadRef {
	return ports.DownloadRef{ConnectionID: "qbt", ExternalID: "torrent-1"}
}

func TestTrashUsesDefaultRetentionAndJanitorDoesNotPurgeEarly(t *testing.T) {
	now := time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)
	service, clock, read, action, _, _ := newTrashFixture(t, now)
	entry := syntheticManifest(now)
	read.set(domain.FileTarget{RootID: entry.RootID, RelativePath: entry.RelativePath}, syntheticObservation(entry))
	action.trashEffect = ports.FilesystemEffect{Outcome: domain.OutcomeApplied, Affected: []domain.FileManifestEntry{entry}, ObservedAt: now, Evidence: []string{"moved_to_trash"}}
	result, err := service.Trash(context.Background(), TrashRequest{EntryID: "entry-default", RootID: entry.RootID, OriginalPrefix: "downloads", TrashPrefix: ".mastarr-trash/entry-default", Manifest: []domain.FileManifestEntry{entry}})
	if err != nil {
		t.Fatal(err)
	}
	if result.Entry.State != "trashed" || result.Entry.Retention != DefaultRetention {
		t.Fatalf("trash result = %#v", result.Entry)
	}
	if _, err := service.Purge(context.Background(), PurgeRequest{EntryID: result.Entry.ID}); !errors.Is(err, ErrNotDue) {
		t.Fatalf("early purge error = %v, want ErrNotDue", err)
	}
	clock.Set(now.Add(DefaultRetention - time.Second))
	if tick, err := service.Tick(context.Background(), 10); err != nil {
		t.Fatal(err)
	} else if tick.Processed != 0 {
		t.Fatalf("early janitor processed %d entries", tick.Processed)
	}
	clock.Set(now.Add(DefaultRetention))
	read.set(domain.FileTarget{RootID: entry.RootID, RelativePath: ".mastarr-trash/entry-default/example.mkv"}, syntheticObservation(domain.FileManifestEntry{
		RootID: entry.RootID, RelativePath: ".mastarr-trash/entry-default/example.mkv", Type: entry.Type, Size: entry.Size, Digest: entry.Digest, FileIdentity: entry.FileIdentity, ObservedAt: clock.Now(),
	}))
	// The action result identifies the exact trash object read above.
	action.deleteEffect = ports.FilesystemEffect{Outcome: domain.OutcomeApplied, Affected: []domain.FileManifestEntry{{RootID: entry.RootID, RelativePath: ".mastarr-trash/entry-default/example.mkv", Type: entry.Type, Size: entry.Size, Digest: entry.Digest, FileIdentity: entry.FileIdentity, ObservedAt: clock.Now()}}, ObservedAt: clock.Now(), Evidence: []string{"deleted"}}
	tick, err := service.Tick(context.Background(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if tick.Processed != 1 || len(tick.Results) != 1 || tick.Results[0].Entry.State != "purged" {
		t.Fatalf("expired janitor result = %#v", tick)
	}
	if action.deleteCalls != 1 {
		t.Fatalf("delete calls = %d, want 1", action.deleteCalls)
	}
}

func TestTrashStopsWholeTorrentBeforePayloadAndPurgeRemovesMetadataOnly(t *testing.T) {
	now := time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)
	service, clock, read, action, download, _ := newTrashFixture(t, now)
	entry := syntheticManifest(now)
	read.set(domain.FileTarget{RootID: entry.RootID, RelativePath: entry.RelativePath}, syntheticObservation(entry))
	ref := clientRef()
	download.current = ports.DownloadObservation{Ref: ref, State: "downloading", Seeding: true, ObservedAt: now}
	action.trashEffect = ports.FilesystemEffect{Outcome: domain.OutcomeApplied, Affected: []domain.FileManifestEntry{entry}, ObservedAt: now}
	result, err := service.Trash(context.Background(), TrashRequest{EntryID: "entry-client", RootID: entry.RootID, OriginalPrefix: "downloads", TrashPrefix: ".mastarr-trash/entry-client", Manifest: []domain.FileManifestEntry{entry}, Client: &ref})
	if err != nil {
		t.Fatal(err)
	}
	if result.Entry.State != "trashed" || download.stopCalls != 1 {
		t.Fatalf("trash state=%q stop calls=%d", result.Entry.State, download.stopCalls)
	}
	clock.Set(now.Add(DefaultRetention))
	trashTarget := domain.FileTarget{RootID: entry.RootID, RelativePath: ".mastarr-trash/entry-client/example.mkv"}
	trashEntry := entry
	trashEntry.RelativePath = trashTarget.RelativePath
	read.set(trashTarget, syntheticObservation(trashEntry))
	action.deleteEffect = ports.FilesystemEffect{Outcome: domain.OutcomeApplied, Affected: []domain.FileManifestEntry{trashEntry}, ObservedAt: clock.Now()}
	result, err = service.Purge(context.Background(), PurgeRequest{EntryID: "entry-client"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Entry.State != "purged" || download.removeCalls != 1 {
		t.Fatalf("purge state=%q remove calls=%d", result.Entry.State, download.removeCalls)
	}
	if len(download.callOrder) < 4 || download.callOrder[len(download.callOrder)-1] != "remove" {
		t.Fatalf("download call order = %v", download.callOrder)
	}
}

func TestTrashMissingTorrentSkipsStopAndMetadataRemoval(t *testing.T) {
	now := time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)
	service, clock, read, action, download, _ := newTrashFixture(t, now)
	entry := syntheticManifest(now)
	read.set(domain.FileTarget{RootID: entry.RootID, RelativePath: entry.RelativePath}, syntheticObservation(entry))
	ref := clientRef()
	download.observeErr = errSyntheticMissing
	action.trashEffect = ports.FilesystemEffect{Outcome: domain.OutcomeApplied, Affected: []domain.FileManifestEntry{entry}, ObservedAt: now}
	service.isDownloadMissing = func(err error) bool { return errors.Is(err, fs.ErrNotExist) }
	result, err := service.Trash(context.Background(), TrashRequest{EntryID: "entry-missing", RootID: entry.RootID, OriginalPrefix: "downloads", TrashPrefix: ".mastarr-trash/entry-missing", Manifest: []domain.FileManifestEntry{entry}, Client: &ref})
	if err != nil {
		t.Fatal(err)
	}
	clock.Set(now.Add(DefaultRetention))
	trashTarget := domain.FileTarget{RootID: entry.RootID, RelativePath: ".mastarr-trash/entry-missing/example.mkv"}
	trashEntry := entry
	trashEntry.RelativePath = trashTarget.RelativePath
	read.set(trashTarget, syntheticObservation(trashEntry))
	action.deleteEffect = ports.FilesystemEffect{Outcome: domain.OutcomeApplied, Affected: []domain.FileManifestEntry{trashEntry}, ObservedAt: clock.Now()}
	result, err = service.Purge(context.Background(), PurgeRequest{EntryID: result.Entry.ID})
	if err != nil {
		t.Fatal(err)
	}
	if result.Entry.State != "purged" || download.stopCalls != 0 || download.removeCalls != 0 {
		t.Fatalf("missing torrent purge state=%q stop=%d remove=%d", result.Entry.State, download.stopCalls, download.removeCalls)
	}
}

func TestConcurrentPurgeClaimsOneOperation(t *testing.T) {
	now := time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)
	service, clock, read, action, _, _ := newTrashFixture(t, now)
	entry := syntheticManifest(now)
	read.set(domain.FileTarget{RootID: entry.RootID, RelativePath: entry.RelativePath}, syntheticObservation(entry))
	action.trashEffect = ports.FilesystemEffect{Outcome: domain.OutcomeApplied, Affected: []domain.FileManifestEntry{entry}, ObservedAt: now}
	if _, err := service.Trash(context.Background(), TrashRequest{EntryID: "entry-race", RootID: entry.RootID, OriginalPrefix: "downloads", TrashPrefix: ".mastarr-trash/entry-race", Manifest: []domain.FileManifestEntry{entry}}); err != nil {
		t.Fatal(err)
	}
	clock.Set(now.Add(DefaultRetention))
	trashTarget := domain.FileTarget{RootID: entry.RootID, RelativePath: ".mastarr-trash/entry-race/example.mkv"}
	trashEntry := entry
	trashEntry.RelativePath = trashTarget.RelativePath
	read.set(trashTarget, syntheticObservation(trashEntry))
	action.deleteEffect = ports.FilesystemEffect{Outcome: domain.OutcomeApplied, Affected: []domain.FileManifestEntry{trashEntry}, ObservedAt: clock.Now()}
	barrier := make(chan struct{})
	action.deleteBlock = barrier
	firstDone := make(chan error, 1)
	go func() {
		_, err := service.Purge(context.Background(), PurgeRequest{EntryID: "entry-race"})
		firstDone <- err
	}()
	deadline := time.After(2 * time.Second)
	for {
		action.mu.Lock()
		called := action.deleteStarts
		action.mu.Unlock()
		if called > 0 {
			break
		}
		select {
		case <-deadline:
			t.Fatal("first purge did not reach delete")
		default:
			time.Sleep(time.Millisecond)
		}
	}
	if _, err := service.Purge(context.Background(), PurgeRequest{EntryID: "entry-race"}); !errors.Is(err, ErrClaimed) {
		t.Fatalf("concurrent purge error = %v, want ErrClaimed", err)
	}
	close(barrier)
	if err := <-firstDone; err != nil {
		t.Fatal(err)
	}
	if action.deleteCalls != 1 {
		t.Fatalf("delete calls = %d, want one", action.deleteCalls)
	}
}
