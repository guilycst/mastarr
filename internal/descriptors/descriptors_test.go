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
	store, err := storage.Open(filepath.Join(base, "state.sqlite"))
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
	return &descriptorFixture{service: service, store: store, root: root, mounted: mounted, downloadID: testDownloadID}
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
