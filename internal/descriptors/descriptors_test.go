package descriptors

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/guilycst/mastarr/internal/storage"
)

const (
	testDownloadID = "11111111-1111-4111-8111-111111111111"
	testNow        = "2026-09-16T12:00:00Z"
)

type descriptorFixture struct {
	service    *Service
	store      *storage.Store
	dbPath     string
	root       string
	mounted    string
	downloadID string
}

func newDescriptorFixture(t *testing.T) *descriptorFixture {
	t.Helper()
	base := t.TempDir()
	root := filepath.Join(base, "retained")
	mounted := filepath.Join(base, "mounted")
	if err := os.MkdirAll(mounted, 0o755); err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(base, "state.sqlite")
	store, err := storage.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	db := store.DB()
	if _, err := db.Exec(`INSERT INTO config_snapshots
		(id, source, document_id, revision, startup_at, effective_json, created_at)
		VALUES ('descriptor-test-snapshot', 'api', 'descriptor-test', '1', ?, '{}', ?)`, testNow, testNow); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO connections
		(id, kind, label, endpoint, source, source_snapshot_id, revision, created_at, updated_at)
		VALUES ('descriptor-test-qbt', 'qbittorrent', 'Synthetic qBittorrent', 'https://example.invalid/qbt', 'api', 'descriptor-test-snapshot', '1', ?, ?)`, testNow, testNow); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO downloads
		(id, connection_id, external_id, protocol, state, payload_json, history_json, first_seen_at, last_seen_at)
		VALUES (?, 'descriptor-test-qbt', 'synthetic-torrent', 'qbittorrent', 'complete', '{}', '[]', ?, ?)`, testDownloadID, testNow, testNow); err != nil {
		t.Fatal(err)
	}
	service, err := New(db, Options{
		StorageRoot: root,
		MountedRoot: mounted,
		Clock: func() time.Time {
			return time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return &descriptorFixture{service: service, store: store, dbPath: dbPath, root: root, mounted: mounted, downloadID: testDownloadID}
}

func syntheticExport(data []byte) VerifiedExport {
	return VerifiedExport{
		Source:         "qbittorrent.export",
		SourceIdentity: "synthetic-torrent",
		Verification:   "inventory-readback-1",
		ExpectedDigest: digestBytes(data),
		ExpectedSize:   int64(len(data)),
		Bytes:          data,
	}
}

func captureRequest(fixture *descriptorFixture, descriptorType string) CaptureRequest {
	return CaptureRequest{DownloadID: fixture.downloadID, DescriptorType: descriptorType}
}

func TestCaptureExportRetainsExactBytesAndMetadataOnlyRecord(t *testing.T) {
	fixture := newDescriptorFixture(t)
	ctx := context.Background()
	data := []byte("d4:infod4:name13:Example Filmee")
	request := captureRequest(fixture, "torrent")

	record, err := fixture.service.CaptureExport(ctx, request, syntheticExport(data))
	if err != nil {
		t.Fatal(err)
	}
	if record.ID == "" || !record.Available || record.Size != int64(len(data)) || record.Digest != digestBytes(data) || record.CaptureSource != "qbittorrent.export" {
		t.Fatalf("capture record = %#v", record)
	}
	encoded, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(encoded, data) || bytes.Contains(encoded, []byte(filepath.Join(fixture.root, objectDirectory))) {
		t.Fatalf("ordinary record contains descriptor bytes or storage path: %s", encoded)
	}

	got, err := fixture.service.Get(ctx, record.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != record.ID || got.Size != int64(len(data)) || !got.Available {
		t.Fatalf("metadata read-back = %#v", got)
	}
	content, err := fixture.service.Content(ctx, record.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(content, data) {
		t.Fatalf("content = %q, want %q", content, data)
	}
	if _, err := fixture.service.ContentBytes(ctx, record.ID); err != nil {
		t.Fatal(err)
	}

	same, err := fixture.service.CaptureExport(ctx, request, syntheticExport(append([]byte(nil), data...)))
	if err != nil {
		t.Fatal(err)
	}
	if same.ID != record.ID || same.Size != int64(len(data)) {
		t.Fatalf("idempotent capture = %#v, original %#v", same, record)
	}
	var count int
	if err := fixture.store.DB().QueryRow(`SELECT count(*) FROM descriptors WHERE download_id = ? AND descriptor_type = ?`, fixture.downloadID, request.DescriptorType).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("descriptor rows = %d, want 1", count)
	}

	changed := []byte("d4:infod4:name14:Changed Filmee")
	_, err = fixture.service.CaptureExport(ctx, request, syntheticExport(changed))
	if !errors.Is(err, ErrDescriptorConflict) {
		t.Fatalf("changed capture error = %v, want conflict", err)
	}
	if bytes.Contains([]byte(err.Error()), changed) {
		t.Fatalf("capture error contains source bytes: %v", err)
	}
}

func TestCaptureExportRequiresVerifiedBoundedSource(t *testing.T) {
	fixture := newDescriptorFixture(t)
	request := captureRequest(fixture, "torrent")
	data := []byte("synthetic export")
	valid := syntheticExport(data)

	cases := []struct {
		name   string
		export VerifiedExport
		want   error
	}{
		{name: "missing source", export: func() VerifiedExport { value := valid; value.Source = "qbt"; return value }(), want: ErrSourceUnverified},
		{name: "missing identity", export: func() VerifiedExport { value := valid; value.SourceIdentity = ""; return value }(), want: ErrSourceUnverified},
		{name: "missing verification", export: func() VerifiedExport { value := valid; value.Verification = ""; return value }(), want: ErrSourceUnverified},
		{name: "wrong digest", export: func() VerifiedExport {
			value := valid
			value.ExpectedDigest = digestBytes([]byte("other"))
			return value
		}(), want: ErrSourceUnverified},
		{name: "wrong size", export: func() VerifiedExport { value := valid; value.ExpectedSize++; return value }(), want: ErrSourceUnverified},
		{name: "two readers", export: func() VerifiedExport { value := valid; value.Reader = bytes.NewReader(data); return value }(), want: ErrInvalidRequest},
		{name: "empty", export: func() VerifiedExport {
			value := valid
			value.Bytes = nil
			value.ExpectedDigest = digestBytes(nil)
			value.ExpectedSize = 0
			return value
		}(), want: ErrSourceUnavailable},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			_, err := fixture.service.CaptureExport(context.Background(), request, testCase.export)
			if !errors.Is(err, testCase.want) {
				t.Fatalf("error = %v, want %v", err, testCase.want)
			}
		})
	}
	var count int
	if err := fixture.store.DB().QueryRow("SELECT count(*) FROM descriptors").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("invalid capture created %d metadata rows", count)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := fixture.service.CaptureExport(cancelled, request, valid); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled capture error = %v", err)
	}
}

func TestCaptureMountedConfinementAndMutationVerification(t *testing.T) {
	fixture := newDescriptorFixture(t)
	data := []byte("synthetic mounted descriptor")
	pathValue := filepath.Join(fixture.mounted, "nested", "source.torrent")
	if err := os.MkdirAll(filepath.Dir(pathValue), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(pathValue, data, 0o600); err != nil {
		t.Fatal(err)
	}
	record, err := fixture.service.CaptureMounted(context.Background(), captureRequest(fixture, "mounted"), "nested/source.torrent")
	if err != nil {
		t.Fatal(err)
	}
	if !record.Available || record.Size != int64(len(data)) || record.CaptureSource != "mounted_file" {
		t.Fatalf("mounted record = %#v", record)
	}

	outside := filepath.Join(t.TempDir(), "outside.torrent")
	if err := os.WriteFile(outside, []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.service.CaptureMounted(context.Background(), captureRequest(fixture, "traversal"), "../outside.torrent"); !errors.Is(err, ErrPathEscape) {
		t.Fatalf("traversal error = %v", err)
	}
	if _, err := fixture.service.CaptureMounted(context.Background(), captureRequest(fixture, "absolute"), outside); !errors.Is(err, ErrPathEscape) {
		t.Fatalf("absolute path error = %v", err)
	}
	if err := os.Mkdir(filepath.Join(fixture.mounted, "directory"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.service.CaptureMounted(context.Background(), captureRequest(fixture, "special"), "directory"); !errors.Is(err, ErrSpecialFile) {
		t.Fatalf("directory error = %v", err)
	}
	if _, err := fixture.service.CaptureMounted(context.Background(), captureRequest(fixture, "missing"), "missing.torrent"); !errors.Is(err, ErrSourceUnavailable) {
		t.Fatalf("missing error = %v", err)
	}

	symlinkPath := filepath.Join(fixture.mounted, "link.torrent")
	if err := os.Symlink(outside, symlinkPath); err == nil {
		_, err := fixture.service.CaptureMounted(context.Background(), captureRequest(fixture, "symlink"), "link.torrent")
		if !errors.Is(err, ErrSymlink) {
			t.Fatalf("symlink error = %v", err)
		}
	}

	originalHook := afterSourceRead
	afterSourceRead = func(*os.File) {
		if writeErr := os.WriteFile(pathValue, []byte("changed mounted descriptor"), 0o600); writeErr != nil {
			t.Fatalf("mutate mounted source: %v", writeErr)
		}
	}
	t.Cleanup(func() { afterSourceRead = originalHook })
	if _, err := fixture.service.CaptureMounted(context.Background(), captureRequest(fixture, "changed"), "nested/source.torrent"); !errors.Is(err, ErrDescriptorChanged) {
		t.Fatalf("changed mounted source error = %v", err)
	}
}

func TestCaptureMountedRejectsAtomicPathReplacement(t *testing.T) {
	fixture := newDescriptorFixture(t)
	pathValue := filepath.Join(fixture.mounted, "replacement.torrent")
	if err := os.WriteFile(pathValue, []byte("original mounted descriptor"), 0o600); err != nil {
		t.Fatal(err)
	}
	replacement := []byte("replacement must not be captured")
	replacementPath := pathValue + ".next"
	originalHook := afterSourceRead
	afterSourceRead = func(*os.File) {
		if err := os.WriteFile(replacementPath, replacement, 0o600); err != nil {
			t.Errorf("write replacement pathname: %v", err)
			return
		}
		if err := os.Rename(replacementPath, pathValue); err != nil {
			t.Errorf("atomically replace mounted pathname: %v", err)
		}
	}
	t.Cleanup(func() { afterSourceRead = originalHook })

	for range 4 {
		if _, err := fixture.service.CaptureMounted(context.Background(), captureRequest(fixture, "atomic-replacement"), "replacement.torrent"); !errors.Is(err, ErrDescriptorChanged) {
			t.Fatalf("atomic replacement error = %v, want descriptor changed", err)
		}
	}
	var count int
	if err := fixture.store.DB().QueryRow(`SELECT count(*) FROM descriptors WHERE descriptor_type = 'atomic-replacement'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("atomic replacement created %d descriptor rows", count)
	}
	got, err := os.ReadFile(pathValue)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, replacement) {
		t.Fatalf("replacement bytes = %q, want %q", got, replacement)
	}
}

func TestRecordUnavailablePreservesHonestStateAndCanBeReplaced(t *testing.T) {
	fixture := newDescriptorFixture(t)
	request := captureRequest(fixture, "nzb")
	ctx := context.Background()

	record, err := fixture.service.RecordUnavailable(ctx, request, "nzbget.history", "descriptor_not_retained")
	if err != nil {
		t.Fatal(err)
	}
	if record.Available || record.UnavailableReason != "descriptor_not_retained" || record.Digest != "" || record.Size != 0 {
		t.Fatalf("unavailable record = %#v", record)
	}
	if _, err := fixture.service.Content(ctx, record.ID); !errors.Is(err, ErrDescriptorUnavailable) {
		t.Fatalf("unavailable content error = %v", err)
	}
	same, err := fixture.service.RecordUnavailable(ctx, request, "nzbget.history", "descriptor_not_retained")
	if err != nil || same.ID != record.ID {
		t.Fatalf("idempotent unavailable = %#v, err=%v", same, err)
	}
	updated, err := fixture.service.RecordUnavailable(ctx, request, "nzbget.history", "history_purged")
	if err != nil || updated.ID != record.ID || updated.UnavailableReason != "history_purged" {
		t.Fatalf("updated unavailable = %#v, err=%v", updated, err)
	}

	data := []byte("verified NZB export")
	available, err := fixture.service.CaptureExport(ctx, request, syntheticExport(data))
	if err != nil {
		t.Fatal(err)
	}
	if available.ID != record.ID || !available.Available || available.Size != int64(len(data)) {
		t.Fatalf("replacement capture = %#v", available)
	}
	kept, err := fixture.service.RecordUnavailable(ctx, request, "nzbget.history", "now_missing")
	if err != nil || !kept.Available || kept.ID != record.ID {
		t.Fatalf("available record was downgraded: %#v, err=%v", kept, err)
	}
}

func TestCaptureIntentReconcilesMetadataAndCleanupFailureAfterRestart(t *testing.T) {
	fixture := newDescriptorFixture(t)
	ctx := context.Background()
	request := captureRequest(fixture, "capture-recovery")
	unavailable, err := fixture.service.RecordUnavailable(ctx, request, "nzbget.history", "not_exported")
	if err != nil {
		t.Fatal(err)
	}
	data := []byte("descriptor bytes survive a failed metadata and cleanup transition")

	originalBeforeMetadata := beforeCaptureMetadata
	originalRemoveObject := removeCapturedObject
	beforeCaptureMetadata = func(id string) error {
		if id != unavailable.ID {
			t.Errorf("metadata failure id = %q, want %q", id, unavailable.ID)
		}
		return ErrStorage
	}
	removeCapturedObject = func(_ *Service, _, id, _ string) error {
		if id != unavailable.ID {
			t.Errorf("cleanup failure id = %q, want %q", id, unavailable.ID)
		}
		return errors.New("synthetic cleanup unavailable")
	}
	t.Cleanup(func() {
		beforeCaptureMetadata = originalBeforeMetadata
		removeCapturedObject = originalRemoveObject
	})

	if _, err := fixture.service.CaptureExport(ctx, request, syntheticExport(data)); !errors.Is(err, ErrCaptureUncertain) {
		t.Fatalf("failed capture error = %v, want uncertain", err)
	}
	objectPathValue := filepath.Join(fixture.root, objectPath(unavailable.ID))
	retained, err := os.ReadFile(objectPathValue)
	if err != nil {
		t.Fatalf("orphaned operation object read = %v", err)
	}
	if !bytes.Equal(retained, data) {
		t.Fatalf("retained operation object = %q, want %q", retained, data)
	}
	var pending int
	if err := fixture.store.DB().QueryRow(`SELECT count(*) FROM audit_events WHERE action = ? AND resource_id = ? AND outcome = 'pending'`, captureIntentAction, unavailable.ID).Scan(&pending); err != nil {
		t.Fatal(err)
	}
	if pending != 1 {
		t.Fatalf("pending capture intents = %d, want 1", pending)
	}

	// A fresh service must not let acknowledged deletion discard the bound
	// object. It must first surface the unresolved capture identity.
	beforeCaptureMetadata = originalBeforeMetadata
	removeCapturedObject = originalRemoveObject
	if _, err := fixture.service.Delete(ctx, DeleteRequest{DescriptorID: unavailable.ID, IrreversibleAcknowledged: true}); !errors.Is(err, ErrCaptureUncertain) {
		t.Fatalf("delete during pending capture = %v, want uncertain", err)
	}

	if err := fixture.store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := storage.Open(fixture.dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	fresh, err := New(reopened.DB(), Options{
		StorageRoot: fixture.root,
		MountedRoot: fixture.mounted,
		Clock:       fixture.service.clock,
	})
	if err != nil {
		t.Fatal(err)
	}
	recovered, err := fresh.CaptureExport(ctx, request, syntheticExport(data))
	if err != nil {
		t.Fatalf("fresh capture reconciliation = %v", err)
	}
	if recovered.ID != unavailable.ID || !recovered.Available || recovered.Size != int64(len(data)) {
		t.Fatalf("recovered capture = %#v", recovered)
	}
	content, err := fresh.Content(ctx, recovered.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(content, data) {
		t.Fatalf("recovered content = %q, want %q", content, data)
	}
	var committed, stillPending int
	if err := reopened.DB().QueryRow(`SELECT count(*) FROM audit_events WHERE action = ? AND resource_id = ? AND outcome = 'committed'`, captureTerminalAction, unavailable.ID).Scan(&committed); err != nil {
		t.Fatal(err)
	}
	if err := reopened.DB().QueryRow(`SELECT count(*) FROM audit_events WHERE action = ? AND resource_id = ? AND outcome = 'pending'`, captureIntentAction, unavailable.ID).Scan(&stillPending); err != nil {
		t.Fatal(err)
	}
	if committed != 1 || stillPending != 1 {
		t.Fatalf("capture audit recovery = committed %d pending %d, want committed 1 and durable intent 1", committed, stillPending)
	}
	deleted, err := fresh.Delete(ctx, DeleteRequest{DescriptorID: recovered.ID, IrreversibleAcknowledged: true})
	if err != nil {
		t.Fatalf("delete after capture recovery = %v", err)
	}
	if deleted.Retention != RetentionDeleted || deleted.Available {
		t.Fatalf("deleted recovered descriptor = %#v", deleted)
	}
	if _, err := os.Stat(objectPathValue); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("recovered object after delete = %v, want absent", err)
	}
}

func TestCaptureIntentRecoversNewDescriptorAfterMetadataFailure(t *testing.T) {
	fixture := newDescriptorFixture(t)
	ctx := context.Background()
	request := captureRequest(fixture, "capture-new-recovery")
	data := []byte("new descriptor bytes survive metadata publication failure")

	originalBeforeMetadata := beforeCaptureMetadata
	originalRemoveObject := removeCapturedObject
	beforeCaptureMetadata = func(string) error { return ErrStorage }
	removeCapturedObject = func(_ *Service, _, _ string, _ string) error {
		return errors.New("synthetic cleanup unavailable")
	}
	t.Cleanup(func() {
		beforeCaptureMetadata = originalBeforeMetadata
		removeCapturedObject = originalRemoveObject
	})

	if _, err := fixture.service.CaptureExport(ctx, request, syntheticExport(data)); !errors.Is(err, ErrCaptureUncertain) {
		t.Fatalf("failed new capture error = %v, want uncertain", err)
	}
	var id, metadata string
	if err := fixture.store.DB().QueryRow(`SELECT resource_id, metadata_json FROM audit_events WHERE action = ? AND outcome = 'pending' ORDER BY id DESC LIMIT 1`, captureIntentAction).Scan(&id, &metadata); err != nil {
		t.Fatal(err)
	}
	if id == "" || bytes.Contains([]byte(metadata), data) || bytes.Contains([]byte(metadata), []byte(fixture.root)) {
		t.Fatalf("capture intent leaked identity or source bytes: id=%q metadata=%q", id, metadata)
	}

	beforeCaptureMetadata = originalBeforeMetadata
	removeCapturedObject = originalRemoveObject
	if err := fixture.store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := storage.Open(fixture.dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	fresh, err := New(reopened.DB(), Options{
		StorageRoot: fixture.root,
		MountedRoot: fixture.mounted,
		Clock:       fixture.service.clock,
	})
	if err != nil {
		t.Fatal(err)
	}
	recovered, err := fresh.CaptureExport(ctx, request, syntheticExport(data))
	if err != nil {
		t.Fatalf("fresh new capture reconciliation = %v", err)
	}
	if recovered.ID != id || !recovered.Available || recovered.Size != int64(len(data)) {
		t.Fatalf("recovered new capture = %#v, want id %q", recovered, id)
	}
	content, err := fresh.Content(ctx, recovered.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(content, data) {
		t.Fatalf("recovered new content = %q, want %q", content, data)
	}
}

func TestCaptureIntentRecoversStageBeforeFinalPublication(t *testing.T) {
	fixture := newDescriptorFixture(t)
	ctx := context.Background()
	request := captureRequest(fixture, "capture-stage-recovery")
	data := []byte("durably staged descriptor bytes")
	id, err := newID()
	if err != nil {
		t.Fatal(err)
	}
	stageFile, stagePath, stageInfo, err := createPrivateStage(fixture.service.objectsRoot)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := stageFile.Write(data); err != nil {
		_ = stageFile.Close()
		t.Fatal(err)
	}
	if err := stageFile.Sync(); err != nil {
		_ = stageFile.Close()
		t.Fatal(err)
	}
	if err := stageFile.Close(); err != nil {
		t.Fatal(err)
	}
	intent, err := fixture.service.ensureCaptureIntent(ctx, id, nil, request, digestBytes(data), int64(len(data)), "qbittorrent.export", filepath.Base(stagePath), stageInfo)
	if err != nil {
		t.Fatal(err)
	}
	if intent.EventID == "" {
		t.Fatal("stage capture intent has no event identity")
	}

	fresh, err := New(fixture.store.DB(), Options{
		StorageRoot: fixture.root,
		MountedRoot: fixture.mounted,
		Clock:       fixture.service.clock,
	})
	if err != nil {
		t.Fatal(err)
	}
	recovered, err := fresh.CaptureExport(ctx, request, syntheticExport(data))
	if err != nil {
		t.Fatalf("stage recovery capture = %v", err)
	}
	if recovered.ID != id || !recovered.Available || recovered.Size != int64(len(data)) {
		t.Fatalf("stage recovery record = %#v", recovered)
	}
	if _, err := os.Stat(stagePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stage after recovery = %v, want absent", err)
	}
}

func TestDeleteRequiresAcknowledgementRetainsAuditMetadataAndIsIdempotent(t *testing.T) {
	fixture := newDescriptorFixture(t)
	ctx := context.Background()
	data := []byte("descriptor selected for deletion")
	record, err := fixture.service.CaptureExport(ctx, captureRequest(fixture, "delete"), syntheticExport(data))
	if err != nil {
		t.Fatal(err)
	}
	request := DeleteRequest{DescriptorID: record.ID}
	if _, err := fixture.service.Delete(ctx, request); !errors.Is(err, ErrDeleteAcknowledgement) {
		t.Fatalf("unacknowledged delete error = %v", err)
	}
	if _, err := fixture.service.Content(ctx, record.ID); err != nil {
		t.Fatalf("content changed after rejected delete: %v", err)
	}

	deleted, err := fixture.service.Delete(ctx, DeleteRequest{DescriptorID: record.ID, IrreversibleAcknowledged: true})
	if err != nil {
		t.Fatal(err)
	}
	if deleted.Retention != RetentionDeleted || deleted.DeletedAt == nil || deleted.Available || deleted.Digest != record.Digest || deleted.CaptureSource != record.CaptureSource || deleted.DownloadID != record.DownloadID {
		t.Fatalf("deleted metadata = %#v", deleted)
	}
	if _, err := os.Stat(filepath.Join(fixture.root, objectPath(record.ID))); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("deleted object stat error = %v", err)
	}
	if _, err := fixture.service.Content(ctx, record.ID); !errors.Is(err, ErrDescriptorDeleted) {
		t.Fatalf("deleted content error = %v", err)
	}
	second, err := fixture.service.Delete(ctx, DeleteRequest{DescriptorID: record.ID, IrreversibleAcknowledged: true})
	if err != nil || second.ID != record.ID || second.DeletedAt == nil {
		t.Fatalf("repeat delete = %#v, err=%v", second, err)
	}
	var auditCount int
	if err := fixture.store.DB().QueryRow(`SELECT count(*) FROM audit_events WHERE action = 'descriptor.delete' AND resource_id = ?`, record.ID).Scan(&auditCount); err != nil {
		t.Fatal(err)
	}
	if auditCount != 1 {
		t.Fatalf("descriptor audit events = %d, want 1", auditCount)
	}
	var redacted, metadata string
	if err := fixture.store.DB().QueryRow(`SELECT redacted, metadata_json FROM audit_events WHERE resource_id = ?`, record.ID).Scan(&redacted, &metadata); err != nil {
		t.Fatal(err)
	}
	if redacted != "1" || bytes.Contains([]byte(metadata), data) {
		t.Fatalf("audit metadata is not redacted: redacted=%q metadata=%q", redacted, metadata)
	}
}

func TestDeleteIntentReconcilesAfterDatabaseFailure(t *testing.T) {
	fixture := newDescriptorFixture(t)
	ctx := context.Background()
	data := []byte("descriptor survives the journal boundary")
	record, err := fixture.service.CaptureExport(ctx, captureRequest(fixture, "restart-delete"), syntheticExport(data))
	if err != nil {
		t.Fatal(err)
	}
	originalHook := afterDeleteUnlink
	afterDeleteUnlink = func(id string) {
		if id != record.ID {
			t.Fatalf("delete hook id = %q, want %q", id, record.ID)
		}
		if err := fixture.store.Close(); err != nil {
			t.Fatalf("close database after unlink: %v", err)
		}
	}
	t.Cleanup(func() { afterDeleteUnlink = originalHook })
	_, err = fixture.service.Delete(ctx, DeleteRequest{DescriptorID: record.ID, IrreversibleAcknowledged: true})
	if !errors.Is(err, ErrDeleteUncertain) {
		t.Fatalf("post-unlink database failure = %v, want uncertain", err)
	}
	if _, err := os.Stat(filepath.Join(fixture.root, objectPath(record.ID))); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("object after post-unlink failure = %v, want absent", err)
	}
	// The hook is only for the first process. A fresh service must reconcile
	// the exact pending intent without dispatching another filesystem delete.
	afterDeleteUnlink = originalHook
	reopened, err := storage.Open(fixture.dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	var retention string
	if err := reopened.DB().QueryRow(`SELECT retention FROM descriptors WHERE id = ?`, record.ID).Scan(&retention); err != nil {
		t.Fatal(err)
	}
	if retention != string(RetentionRetain) {
		t.Fatalf("retention before restart reconciliation = %q", retention)
	}
	var pendingCount int
	if err := reopened.DB().QueryRow(`SELECT count(*) FROM audit_events WHERE action = ? AND resource_id = ? AND outcome = 'pending'`, deleteIntentAction, record.ID).Scan(&pendingCount); err != nil {
		t.Fatal(err)
	}
	if pendingCount != 1 {
		t.Fatalf("pending delete intents = %d, want 1", pendingCount)
	}
	fresh, err := New(reopened.DB(), Options{
		StorageRoot: fixture.root,
		MountedRoot: fixture.mounted,
		Clock: func() time.Time {
			return time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	// A replacement with identical bytes must not satisfy the persisted intent:
	// digest equality alone cannot prove that it is the original object.
	objectPathValue := filepath.Join(fixture.root, objectPath(record.ID))
	if err := os.WriteFile(objectPathValue, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := fresh.Delete(ctx, DeleteRequest{DescriptorID: record.ID, IrreversibleAcknowledged: true}); !errors.Is(err, ErrDescriptorChanged) {
		t.Fatalf("same-content replacement delete = %v, want descriptor changed", err)
	}
	replacementBytes, err := os.ReadFile(objectPathValue)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(replacementBytes, data) {
		t.Fatalf("same-content replacement changed after rejected retry: %q", replacementBytes)
	}
	if err := os.Remove(objectPathValue); err != nil {
		t.Fatal(err)
	}
	deleted, err := fresh.Delete(ctx, DeleteRequest{DescriptorID: record.ID, IrreversibleAcknowledged: true})
	if err != nil {
		t.Fatal(err)
	}
	if deleted.Retention != RetentionDeleted || deleted.DeletedAt == nil || deleted.Digest != record.Digest {
		t.Fatalf("reconciled delete = %#v", deleted)
	}
	var finalCount int
	if err := reopened.DB().QueryRow(`SELECT count(*) FROM audit_events WHERE action = 'descriptor.delete' AND resource_id = ? AND outcome = 'deleted'`, record.ID).Scan(&finalCount); err != nil {
		t.Fatal(err)
	}
	if finalCount != 1 {
		t.Fatalf("terminal delete audits = %d, want 1", finalCount)
	}
	var stillPending int
	if err := reopened.DB().QueryRow(`SELECT count(*) FROM audit_events WHERE action = ? AND resource_id = ? AND outcome = 'pending'`, deleteIntentAction, record.ID).Scan(&stillPending); err != nil {
		t.Fatal(err)
	}
	if stillPending != 1 {
		t.Fatalf("durable delete intents after reconciliation = %d, want 1", stillPending)
	}
}

func TestDeleteFailsClosedWhenSelectedObjectIsReplaced(t *testing.T) {
	fixture := newDescriptorFixture(t)
	ctx := context.Background()
	data := []byte("descriptor before replacement")
	record, err := fixture.service.CaptureExport(ctx, captureRequest(fixture, "replace"), syntheticExport(data))
	if err != nil {
		t.Fatal(err)
	}
	pathValue := filepath.Join(fixture.root, objectPath(record.ID))
	replacement := []byte("replacement object must survive")
	originalHook := beforeDeleteDescriptor
	beforeDeleteDescriptor = func(id string) {
		if id != record.ID {
			t.Fatalf("delete hook id = %q, want %q", id, record.ID)
		}
		_ = os.Remove(pathValue)
		if writeErr := os.WriteFile(pathValue, replacement, 0o600); writeErr != nil {
			t.Fatalf("write replacement: %v", writeErr)
		}
	}
	t.Cleanup(func() { beforeDeleteDescriptor = originalHook })
	if _, err := fixture.service.Delete(ctx, DeleteRequest{DescriptorID: record.ID, IrreversibleAcknowledged: true}); !errors.Is(err, ErrDescriptorChanged) {
		t.Fatalf("replacement delete error = %v, want changed", err)
	}
	got, err := os.ReadFile(pathValue)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, replacement) {
		t.Fatalf("replacement object = %q, want %q", got, replacement)
	}
	var retention string
	if err := fixture.store.DB().QueryRow("SELECT retention FROM descriptors WHERE id = ?", record.ID).Scan(&retention); err != nil {
		t.Fatal(err)
	}
	if retention != string(RetentionRetain) {
		t.Fatalf("retention after failed delete = %q", retention)
	}
}

func TestDeleteUnavailableRetainsRecordAndAudit(t *testing.T) {
	fixture := newDescriptorFixture(t)
	record, err := fixture.service.RecordUnavailable(context.Background(), captureRequest(fixture, "missing"), "nzbget.history", "not_exported")
	if err != nil {
		t.Fatal(err)
	}
	deleted, err := fixture.service.Delete(context.Background(), DeleteRequest{DescriptorID: record.ID, IrreversibleAcknowledged: true})
	if err != nil {
		t.Fatal(err)
	}
	if deleted.Retention != RetentionDeleted || deleted.DeletedAt == nil || deleted.Digest != "" || deleted.UnavailableReason != "not_exported" {
		t.Fatalf("deleted unavailable record = %#v", deleted)
	}
	var auditCount int
	if err := fixture.store.DB().QueryRow(`SELECT count(*) FROM audit_events WHERE action = 'descriptor.delete' AND resource_id = ?`, record.ID).Scan(&auditCount); err != nil {
		t.Fatal(err)
	}
	if auditCount != 1 {
		t.Fatalf("audit count = %d, want 1", auditCount)
	}
}

func TestDeleteCaptureAcrossServiceValuesUsesDurableDescriptorFence(t *testing.T) {
	t.Run("delete intent fences capture", func(t *testing.T) {
		fixture := newDescriptorFixture(t)
		ctx := context.Background()
		request := captureRequest(fixture, "intent-first")
		record, err := fixture.service.RecordUnavailable(ctx, request, "nzbget.history", "not_exported")
		if err != nil {
			t.Fatal(err)
		}
		captureService, err := New(fixture.store.DB(), Options{
			StorageRoot: fixture.root,
			MountedRoot: fixture.mounted,
			Clock:       fixture.service.clock,
		})
		if err != nil {
			t.Fatal(err)
		}

		intentCommitted := make(chan struct{})
		releaseDelete := make(chan struct{})
		var releaseOnce sync.Once
		release := func() { releaseOnce.Do(func() { close(releaseDelete) }) }
		originalAfterIntent := afterDeleteIntent
		afterDeleteIntent = func(id string) {
			if id != record.ID {
				t.Errorf("delete intent hook id = %q, want %q", id, record.ID)
			}
			close(intentCommitted)
			<-releaseDelete
		}
		t.Cleanup(func() {
			afterDeleteIntent = originalAfterIntent
			release()
		})

		deleteResult := make(chan error, 1)
		go func() {
			_, deleteErr := fixture.service.Delete(ctx, DeleteRequest{DescriptorID: record.ID, IrreversibleAcknowledged: true})
			deleteResult <- deleteErr
		}()
		select {
		case <-intentCommitted:
		case <-time.After(2 * time.Second):
			t.Fatal("delete did not commit its durable intent")
		}

		captureResult := make(chan error, 1)
		go func() {
			_, captureErr := captureService.CaptureExport(ctx, request, syntheticExport([]byte("must not materialize")))
			captureResult <- captureErr
		}()
		select {
		case captureErr := <-captureResult:
			if !errors.Is(captureErr, ErrDescriptorDeletePending) {
				t.Fatalf("capture during pending delete error = %v, want pending", captureErr)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("capture did not observe the pending delete fence")
		}
		release()
		if deleteErr := <-deleteResult; deleteErr != nil {
			t.Fatalf("delete error = %v", deleteErr)
		}

		deleted, err := fixture.service.Get(ctx, record.ID)
		if err != nil {
			t.Fatal(err)
		}
		if deleted.Retention != RetentionDeleted || deleted.Available {
			t.Fatalf("deleted record = %#v", deleted)
		}
		if _, err := os.Stat(filepath.Join(fixture.root, objectPath(record.ID))); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("capture created an orphaned object: stat error = %v", err)
		}
	})

	t.Run("stale delete cannot terminalize capture", func(t *testing.T) {
		fixture := newDescriptorFixture(t)
		ctx := context.Background()
		request := captureRequest(fixture, "capture-first")
		record, err := fixture.service.RecordUnavailable(ctx, request, "nzbget.history", "not_exported")
		if err != nil {
			t.Fatal(err)
		}
		captureService, err := New(fixture.store.DB(), Options{
			StorageRoot: fixture.root,
			MountedRoot: fixture.mounted,
			Clock:       fixture.service.clock,
		})
		if err != nil {
			t.Fatal(err)
		}

		deleteRead := make(chan struct{})
		releaseDelete := make(chan struct{})
		var releaseOnce sync.Once
		release := func() { releaseOnce.Do(func() { close(releaseDelete) }) }
		originalBeforeIntent := beforeDeleteIntent
		beforeDeleteIntent = func(id string) {
			if id != record.ID {
				t.Errorf("delete read hook id = %q, want %q", id, record.ID)
			}
			close(deleteRead)
			<-releaseDelete
		}
		t.Cleanup(func() {
			beforeDeleteIntent = originalBeforeIntent
			release()
		})

		deleteResult := make(chan error, 1)
		go func() {
			_, deleteErr := fixture.service.Delete(ctx, DeleteRequest{DescriptorID: record.ID, IrreversibleAcknowledged: true})
			deleteResult <- deleteErr
		}()
		select {
		case <-deleteRead:
		case <-time.After(2 * time.Second):
			t.Fatal("delete did not reach its pre-intent boundary")
		}

		captured, err := captureService.CaptureExport(ctx, request, syntheticExport([]byte("capture wins")))
		if err != nil {
			t.Fatalf("capture before delete intent = %v", err)
		}
		if !captured.Available || captured.ID != record.ID {
			t.Fatalf("capture record = %#v", captured)
		}
		release()
		deleteErr := <-deleteResult
		if !errors.Is(deleteErr, ErrDescriptorConflict) {
			t.Fatalf("stale delete error = %v, want conflict", deleteErr)
		}

		kept, err := fixture.service.Get(ctx, record.ID)
		if err != nil {
			t.Fatal(err)
		}
		if !kept.Available || kept.Retention != RetentionRetain || kept.Digest != captured.Digest {
			t.Fatalf("captured record after stale delete = %#v", kept)
		}
		content, err := fixture.service.Content(ctx, record.ID)
		if err != nil {
			t.Fatal(err)
		}
		if string(content) != "capture wins" {
			t.Fatalf("retained content = %q", content)
		}
		var terminalCount, pendingCount int
		if err := fixture.store.DB().QueryRow(`SELECT count(*) FROM audit_events WHERE action = 'descriptor.delete' AND resource_id = ?`, record.ID).Scan(&terminalCount); err != nil {
			t.Fatal(err)
		}
		if err := fixture.store.DB().QueryRow(`SELECT count(*) FROM audit_events WHERE action = ? AND resource_id = ? AND outcome = 'pending'`, deleteIntentAction, record.ID).Scan(&pendingCount); err != nil {
			t.Fatal(err)
		}
		if terminalCount != 0 || pendingCount != 0 {
			t.Fatalf("stale delete audit events = terminal %d pending %d, want both zero", terminalCount, pendingCount)
		}
	})
}

func TestConcurrentSameCaptureIsIdempotent(t *testing.T) {
	fixture := newDescriptorFixture(t)
	data := []byte("concurrent synthetic descriptor")
	request := captureRequest(fixture, "concurrent")
	const workers = 16
	start := make(chan struct{})
	results := make(chan Record, workers)
	errorsCh := make(chan error, workers)
	var wait sync.WaitGroup
	wait.Add(workers)
	for range workers {
		go func() {
			defer wait.Done()
			<-start
			record, err := fixture.service.CaptureExport(context.Background(), request, syntheticExport(data))
			if err != nil {
				errorsCh <- err
				return
			}
			results <- record
		}()
	}
	close(start)
	wait.Wait()
	close(results)
	close(errorsCh)
	for err := range errorsCh {
		t.Fatal(err)
	}
	var first Record
	for record := range results {
		if first.ID == "" {
			first = record
			continue
		}
		if record.ID != first.ID || record.Digest != first.Digest || record.Size != first.Size {
			t.Fatalf("non-idempotent concurrent records: first=%#v current=%#v", first, record)
		}
	}
	if first.ID == "" {
		t.Fatal("no concurrent capture result")
	}
	var count int
	if err := fixture.store.DB().QueryRow("SELECT count(*) FROM descriptors WHERE download_id = ? AND descriptor_type = ?", fixture.downloadID, request.DescriptorType).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("concurrent descriptor rows = %d, want 1", count)
	}
}

func TestNewRejectsUnsafeConfiguration(t *testing.T) {
	if _, err := New(nil, Options{}); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("nil db error = %v", err)
	}
	fixture := newDescriptorFixture(t)
	if _, err := New(fixture.store.DB(), Options{StorageRoot: filepath.Join(t.TempDir(), "root"), MaxBytes: DefaultMaxBytes + 1}); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("oversized config error = %v", err)
	}
	if _, err := New(fixture.store.DB(), Options{StorageRoot: "relative"}); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("relative root error = %v", err)
	}
}

func TestBoundedReaderAndContextCancellation(t *testing.T) {
	fixture := newDescriptorFixture(t)
	request := captureRequest(fixture, "reader")
	data := []byte("reader source")
	export := syntheticExport(data)
	export.Bytes = nil
	export.Reader = bytes.NewReader(data)
	record, err := fixture.service.CaptureExport(context.Background(), request, export)
	if err != nil || !record.Available {
		t.Fatalf("reader capture = %#v, err=%v", record, err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := fixture.service.Content(ctx, record.ID); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled content error = %v", err)
	}

	large := bytes.Repeat([]byte{'x'}, 32)
	smallService, err := New(fixture.store.DB(), Options{StorageRoot: filepath.Join(t.TempDir(), "limited"), MaxBytes: 16})
	if err != nil {
		t.Fatal(err)
	}
	tooLarge := syntheticExport(large)
	if _, err := smallService.CaptureExport(context.Background(), captureRequest(fixture, "limited"), tooLarge); !errors.Is(err, ErrDescriptorTooLarge) {
		t.Fatalf("bounded capture error = %v", err)
	}
}
