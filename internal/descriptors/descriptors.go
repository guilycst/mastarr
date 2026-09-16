// Package descriptors retains original torrent and NZB descriptor bytes in a
// private, root-confined store. Metadata and content have separate APIs:
// ordinary reads return metadata only, while Content is an explicit byte
// retrieval operation.
package descriptors

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/guilycst/mastarr/internal/domain"
)

const (
	// DefaultMaxBytes bounds one descriptor and protects both memory and disk
	// use during capture and explicit content reads.
	DefaultMaxBytes    int64 = 4 << 20
	objectDirectory          = "objects"
	objectSuffix             = ".descriptor"
	privateStagePrefix       = ".stage-"
	maxIdentifierBytes       = 256
	maxReasonBytes           = 128
	deleteIntentAction       = "descriptor.delete.intent"
)

var (
	ErrInvalidConfig           = errors.New("descriptor service configuration is invalid")
	ErrInvalidRequest          = errors.New("descriptor request is invalid")
	ErrSourceUnverified        = errors.New("descriptor source is not verified")
	ErrSourceUnavailable       = errors.New("descriptor source is unavailable")
	ErrDescriptorNotFound      = errors.New("descriptor was not found")
	ErrDescriptorConflict      = errors.New("descriptor capture conflicts with retained bytes")
	ErrDescriptorDeleted       = errors.New("descriptor has been deleted")
	ErrDescriptorUnavailable   = errors.New("descriptor content is unavailable")
	ErrDescriptorChanged       = errors.New("descriptor content changed")
	ErrDescriptorTooLarge      = errors.New("descriptor exceeds configured size limit")
	ErrPathEscape              = errors.New("descriptor path escapes configured root")
	ErrSymlink                 = errors.New("descriptor symlink is not allowed")
	ErrSpecialFile             = errors.New("descriptor special file is not allowed")
	ErrDeleteAcknowledgement   = errors.New("descriptor deletion requires explicit acknowledgement")
	ErrDeleteUncertain         = errors.New("descriptor deletion result is uncertain")
	ErrDescriptorDeletePending = errors.New("descriptor deletion is pending")
	ErrStorage                 = errors.New("descriptor metadata storage failed")
)

// Retention describes the lifecycle of retained bytes. A deleted record stays
// in metadata so provenance and audit history remain available.
type Retention string

const (
	RetentionRetain   Retention = "retain"
	RetentionEligible Retention = "eligible"
	RetentionDeleted  Retention = "deleted"
)

// Options configures one descriptor service. StorageRoot is application-owned
// and receives private descriptor objects below its objects directory.
// MountedRoot is an explicitly configured, read-only source root used by
// CaptureMounted. Both paths are canonicalized once at construction.
type Options struct {
	StorageRoot string
	MountedRoot string
	MaxBytes    int64
	Clock       func() time.Time
}

// CaptureRequest identifies one descriptor associated with one local download.
// DownloadID is required so repeated captures have one durable identity and
// cannot create duplicate descriptors for the same download/type pair.
type CaptureRequest struct {
	DownloadID     string
	DescriptorType string
}

// VerifiedExport contains bytes obtained from a supported upstream export.
// The caller must provide the digest calculated by its verified upstream
// identity check and a non-secret verification reference. The service
// recalculates the digest before storing anything.
type VerifiedExport struct {
	Source         string
	SourceIdentity string
	Verification   string
	ExpectedDigest string
	ExpectedSize   int64
	Bytes          []byte
	Reader         io.Reader
}

// Record is the ordinary descriptor response. It intentionally has no
// storage pathname or content bytes. Content is available only through the
// explicit Service.Content method.
type Record struct {
	ID                string     `json:"id"`
	DownloadID        string     `json:"downloadId"`
	DescriptorType    string     `json:"descriptorType"`
	Available         bool       `json:"available"`
	Size              int64      `json:"size"`
	Digest            string     `json:"digest,omitempty"`
	CaptureSource     string     `json:"captureSource"`
	CapturedAt        time.Time  `json:"capturedAt"`
	Retention         Retention  `json:"retention"`
	UnavailableReason string     `json:"unavailableReason,omitempty"`
	DeletedAt         *time.Time `json:"deletedAt,omitempty"`
}

// DeleteRequest is deliberately singular and exact-scope. The durable action
// layer supplies the acknowledgement after review; this package does not
// accept wildcards or bulk deletion.
type DeleteRequest struct {
	DescriptorID             string
	IrreversibleAcknowledged bool
}

// Service owns descriptor bytes and their SQLite metadata. The database is
// supplied by the already opened storage owner; this package never opens a
// second database or changes migrations.
type Service struct {
	db          *sql.DB
	storageRoot string
	objectsRoot string
	mountedRoot string
	maxBytes    int64
	clock       func() time.Time
	locks       sync.Map
}

type storedRecord struct {
	ID                string
	DownloadID        sql.NullString
	DescriptorType    string
	StoragePath       string
	OriginalDigest    sql.NullString
	CaptureSource     string
	CapturedAt        string
	Retention         string
	UnavailableReason sql.NullString
	DeletedAt         sql.NullString
}

// New constructs a service without reading descriptor content. It validates
// and prepares only the application-owned storage root; a mounted source is
// checked when CaptureMounted is called.
func New(db *sql.DB, options Options) (*Service, error) {
	if db == nil {
		return nil, fmt.Errorf("%w: database is nil", ErrInvalidConfig)
	}
	if options.MaxBytes <= 0 {
		options.MaxBytes = DefaultMaxBytes
	}
	if options.MaxBytes > DefaultMaxBytes {
		return nil, fmt.Errorf("%w: size limit exceeds safety ceiling", ErrInvalidConfig)
	}
	storageRoot, err := prepareDirectory(options.StorageRoot, true, true)
	if err != nil {
		return nil, fmt.Errorf("%w: storage root", err)
	}
	objectsRoot := filepath.Join(storageRoot, objectDirectory)
	if _, err := prepareDirectory(objectsRoot, true, true); err != nil {
		return nil, fmt.Errorf("%w: object root", err)
	}
	mountedRoot := ""
	if strings.TrimSpace(options.MountedRoot) != "" {
		mountedRoot, err = prepareDirectory(options.MountedRoot, false, false)
		if err != nil {
			return nil, fmt.Errorf("%w: mounted source root", err)
		}
	}
	clock := options.Clock
	if clock == nil {
		clock = func() time.Time { return time.Now().UTC() }
	}
	return &Service{db: db, storageRoot: storageRoot, objectsRoot: objectsRoot, mountedRoot: mountedRoot, maxBytes: options.MaxBytes, clock: clock}, nil
}

// CaptureExport stores exact bytes from a verified upstream export. A digest,
// source identity and verification reference are mandatory so an arbitrary
// caller byte slice cannot be mistaken for an upstream original.
func (service *Service) CaptureExport(ctx context.Context, request CaptureRequest, export VerifiedExport) (Record, error) {
	if err := service.validate(); err != nil {
		return Record{}, err
	}
	if err := contextCheckpoint(ctx); err != nil {
		return Record{}, err
	}
	if err := validateCaptureRequest(request); err != nil {
		return Record{}, err
	}
	if err := validateSourceMetadata(export.Source, export.SourceIdentity, export.Verification, export.ExpectedDigest, export.ExpectedSize); err != nil {
		return Record{}, err
	}
	data, err := readExport(ctx, export, service.maxBytes)
	if err != nil {
		return Record{}, err
	}
	return service.capture(ctx, request, data, export.Source)
}

// CaptureMounted stores exact bytes from one file below the explicitly
// configured mounted root. The path is never persisted or returned as
// ordinary descriptor metadata.
func (service *Service) CaptureMounted(ctx context.Context, request CaptureRequest, relativePath string) (Record, error) {
	if err := service.validate(); err != nil {
		return Record{}, err
	}
	if err := contextCheckpoint(ctx); err != nil {
		return Record{}, err
	}
	if err := validateCaptureRequest(request); err != nil {
		return Record{}, err
	}
	if service.mountedRoot == "" {
		return Record{}, fmt.Errorf("%w: mounted source root is not configured", ErrSourceUnavailable)
	}
	if err := validateRelativePath(relativePath); err != nil {
		return Record{}, err
	}
	file, info, err := openConstrainedFile(service.mountedRoot, relativePath)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return Record{}, fmt.Errorf("%w: mounted source is absent", ErrSourceUnavailable)
		}
		return Record{}, err
	}
	defer file.Close()
	if !info.Mode().IsRegular() {
		return Record{}, ErrSpecialFile
	}
	data, digest, before, err := readStableFile(ctx, file, info, service.maxBytes)
	if err != nil {
		return Record{}, err
	}
	afterSourceRead(file)
	if err := verifyStableFile(ctx, file, before, digest, int64(len(data)), service.maxBytes); err != nil {
		return Record{}, err
	}
	// The opened descriptor proves the bytes that were read, but it does not
	// prove that the configured pathname still selects that same object. A
	// source can be atomically replaced while the opened descriptor remains
	// valid. Re-open the constrained pathname and bind it to the verified
	// object before any bytes are persisted.
	if err := verifyMountedPath(ctx, service.mountedRoot, relativePath, info, digest, int64(len(data)), service.maxBytes); err != nil {
		return Record{}, err
	}
	// Only the bounded bytes whose configured pathname still names the
	// verified object proceed to the private descriptor store.
	return service.capture(ctx, request, data, "mounted_file")
}

// RecordUnavailable persists an honest absence when a supported source cannot
// provide original bytes. Existing available bytes are never downgraded by a
// later unavailable observation.
func (service *Service) RecordUnavailable(ctx context.Context, request CaptureRequest, source, reason string) (Record, error) {
	if err := service.validate(); err != nil {
		return Record{}, err
	}
	if err := contextCheckpoint(ctx); err != nil {
		return Record{}, err
	}
	if err := validateCaptureRequest(request); err != nil {
		return Record{}, err
	}
	if err := validateToken(source, maxIdentifierBytes); err != nil {
		return Record{}, fmt.Errorf("%w: unavailable source", ErrInvalidRequest)
	}
	if err := validateToken(reason, maxReasonBytes); err != nil {
		return Record{}, fmt.Errorf("%w: unavailable reason", ErrInvalidRequest)
	}
	unlock := service.lockFor(descriptorLockKey(request.DownloadID, request.DescriptorType))
	defer unlock()
	existing, found, err := service.findByKey(ctx, request)
	if err != nil {
		return Record{}, err
	}
	if found {
		record, err := service.recordForRow(ctx, existing)
		if err != nil {
			return Record{}, err
		}
		if record.DeletedAt != nil {
			return record, ErrDescriptorDeleted
		}
		if _, pending, err := service.pendingDeleteIntent(ctx, existing); err != nil {
			return Record{}, err
		} else if pending {
			return Record{}, ErrDescriptorDeletePending
		}
		if record.Available {
			return record, nil
		}
		if record.UnavailableReason == reason {
			return record, nil
		}
		if err := service.updateUnavailable(ctx, existing.ID, existing.StoragePath, source, reason); err != nil {
			return Record{}, err
		}
		return service.get(ctx, existing.ID)
	}
	id, err := newID()
	if err != nil {
		return Record{}, err
	}
	path := objectPath(id)
	row := storedRecord{ID: id, DownloadID: sql.NullString{String: request.DownloadID, Valid: true}, DescriptorType: request.DescriptorType, StoragePath: path, CaptureSource: source, CapturedAt: service.now().Format(time.RFC3339Nano), Retention: string(RetentionRetain), UnavailableReason: sql.NullString{String: reason, Valid: true}}
	if err := service.insert(ctx, row); err != nil {
		if existing, found, lookupErr := service.findByKey(ctx, request); lookupErr == nil && found {
			return service.recordForRow(ctx, existing)
		}
		return Record{}, err
	}
	return service.recordForRow(ctx, row)
}

// Get returns metadata only. It may verify the private object to report a
// trustworthy size and availability, but it never includes descriptor bytes.
func (service *Service) Get(ctx context.Context, id string) (Record, error) {
	if err := service.validate(); err != nil {
		return Record{}, err
	}
	if err := contextCheckpoint(ctx); err != nil {
		return Record{}, err
	}
	if err := validateID(id); err != nil {
		return Record{}, err
	}
	return service.get(ctx, id)
}

// Content explicitly retrieves original bytes after verifying retained
// metadata, object confinement, size and digest. Callers must treat the
// returned bytes as sensitive; ordinary Record responses never contain them.
func (service *Service) Content(ctx context.Context, id string) ([]byte, error) {
	if err := service.validate(); err != nil {
		return nil, err
	}
	if err := contextCheckpoint(ctx); err != nil {
		return nil, err
	}
	if err := validateID(id); err != nil {
		return nil, err
	}
	row, err := service.getStored(ctx, id)
	if err != nil {
		return nil, err
	}
	if row.DeletedAt.Valid {
		return nil, ErrDescriptorDeleted
	}
	if !row.OriginalDigest.Valid {
		return nil, ErrDescriptorUnavailable
	}
	if err := validateStoragePath(row.StoragePath, id); err != nil {
		return nil, err
	}
	file, info, err := openConstrainedFile(service.storageRoot, row.StoragePath)
	if err != nil {
		return nil, ErrDescriptorChanged
	}
	defer file.Close()
	data, digest, before, err := readStableFile(ctx, file, info, service.maxBytes)
	if err != nil {
		return nil, err
	}
	if err := verifyStableFile(ctx, file, before, digest, int64(len(data)), service.maxBytes); err != nil {
		return nil, err
	}
	if digest != row.OriginalDigest.String || int64(len(data)) != info.Size() {
		return nil, ErrDescriptorChanged
	}
	return data, nil
}

// Delete removes exactly one retained object after explicit acknowledgement.
// A redacted pending intent is committed before unlink, and the terminal
// metadata/audit transition is committed only after the filesystem effect.
// This leaves a restart-reconcilable record if the database fails after
// unlink. Repeating a completed deletion returns retained metadata without
// another filesystem effect or duplicate terminal audit event.
func (service *Service) Delete(ctx context.Context, request DeleteRequest) (Record, error) {
	if err := service.validate(); err != nil {
		return Record{}, err
	}
	if err := contextCheckpoint(ctx); err != nil {
		return Record{}, err
	}
	if !request.IrreversibleAcknowledged {
		return Record{}, ErrDeleteAcknowledgement
	}
	if err := validateID(request.DescriptorID); err != nil {
		return Record{}, err
	}
	row, err := service.getStored(ctx, request.DescriptorID)
	if err != nil {
		return Record{}, err
	}
	unlock := service.lockFor(descriptorLockKey(row.DownloadID.String, row.DescriptorType))
	defer unlock()
	// Reload after acquiring the shared descriptor identity lock. The durable
	// CAS below remains authoritative for separate Service values, but this
	// keeps ordinary same-process capture/delete calls ordered as well.
	row, err = service.getStored(ctx, request.DescriptorID)
	if err != nil {
		return Record{}, err
	}
	if row.DeletedAt.Valid {
		return service.recordForRow(ctx, row)
	}
	pendingMetadata, pending, err := service.pendingDeleteIntent(ctx, row)
	if err != nil {
		return Record{}, err
	}
	beforeDeleteIntent(request.DescriptorID)
	if row.OriginalDigest.Valid {
		if err := validateStoragePath(row.StoragePath, row.ID); err != nil {
			return Record{}, err
		}
		file, info, openErr := openConstrainedFile(service.storageRoot, row.StoragePath)
		if openErr != nil {
			if pending && errors.Is(openErr, fs.ErrNotExist) {
				// A committed intent proves this exact private object was the
				// target of a deletion. Its absence means the filesystem phase
				// completed before a durable terminal transition; reconcile the
				// metadata without attempting a pathname operation.
			} else {
				return Record{}, fmt.Errorf("%w: retained object cannot be verified", ErrDescriptorChanged)
			}
		} else {
			fileIdentity, identityOK := descriptorObjectIdentity(info)
			if !identityOK {
				_ = file.Close()
				return Record{}, fmt.Errorf("%w: retained object identity unavailable", ErrDeleteUncertain)
			}
			if pending && pendingMetadata.FileIdentity != fileIdentity {
				_ = file.Close()
				return Record{}, ErrDescriptorChanged
			}
			dataDigest, verifyErr := digestOpenedFile(ctx, file, info, service.maxBytes)
			if verifyErr != nil {
				_ = file.Close()
				return Record{}, verifyErr
			}
			if dataDigest != row.OriginalDigest.String {
				_ = file.Close()
				return Record{}, ErrDescriptorChanged
			}
			if err := service.ensureDeleteIntent(ctx, row, fileIdentity); err != nil {
				_ = file.Close()
				return Record{}, err
			}
			afterDeleteIntent(request.DescriptorID)
			beforeDeleteDescriptor(request.DescriptorID)
			// Re-read after the review seam and immediately before unlink. This
			// catches an in-place rewrite that preserves the selected inode; the
			// descriptor-relative removal below separately rejects pathname
			// replacement with a different inode.
			latestDigest, verifyErr := digestOpenedFile(ctx, file, info, service.maxBytes)
			if verifyErr != nil {
				_ = file.Close()
				return Record{}, verifyErr
			}
			if latestDigest != row.OriginalDigest.String {
				_ = file.Close()
				return Record{}, ErrDescriptorChanged
			}
			if err := removeConstrainedFile(service.storageRoot, row.StoragePath, info); err != nil {
				_ = file.Close()
				return Record{}, err
			}
			if err := file.Close(); err != nil {
				return Record{}, fmt.Errorf("%w: close deleted object", ErrDeleteUncertain)
			}
			if err := syncDirectoryPath(service.objectsRoot); err != nil {
				return Record{}, fmt.Errorf("%w: sync deleted object directory", ErrDeleteUncertain)
			}
			afterDeleteUnlink(request.DescriptorID)
		}
	} else if err := service.ensureDeleteIntent(ctx, row, ""); err != nil {
		// Unavailable descriptors have no filesystem phase, but they still
		// receive the same durable intent/terminal audit sequence so a retry
		// cannot lose the exact reviewed deletion scope.
		return Record{}, err
	} else {
		afterDeleteIntent(request.DescriptorID)
	}
	deletedAt := service.now()
	if err := service.markDeleted(ctx, row, deletedAt); err != nil {
		return Record{}, fmt.Errorf("%w: journal deleted descriptor: %w", ErrDeleteUncertain, err)
	}
	return service.get(ctx, row.ID)
}

// ContentBytes is an explicit-name alias for Content used by HTTP adapters.
func (service *Service) ContentBytes(ctx context.Context, id string) ([]byte, error) {
	return service.Content(ctx, id)
}

func (service *Service) capture(ctx context.Context, request CaptureRequest, data []byte, source string) (Record, error) {
	if err := contextCheckpoint(ctx); err != nil {
		return Record{}, err
	}
	if len(data) == 0 {
		return Record{}, fmt.Errorf("%w: empty descriptor", ErrSourceUnavailable)
	}
	if int64(len(data)) > service.maxBytes {
		return Record{}, ErrDescriptorTooLarge
	}
	digest := digestBytes(data)
	unlock := service.lockFor(descriptorLockKey(request.DownloadID, request.DescriptorType))
	defer unlock()
	existing, found, err := service.findByKey(ctx, request)
	if err != nil {
		return Record{}, err
	}
	if found {
		existingRecord, recordErr := service.recordForRow(ctx, existing)
		if recordErr != nil {
			return Record{}, recordErr
		}
		if existingRecord.DeletedAt != nil {
			return existingRecord, ErrDescriptorDeleted
		}
		if _, pending, err := service.pendingDeleteIntent(ctx, existing); err != nil {
			return Record{}, err
		} else if pending {
			return Record{}, ErrDescriptorDeletePending
		}
		if existing.OriginalDigest.Valid && existing.OriginalDigest.String != digest {
			return Record{}, ErrDescriptorConflict
		}
		if existing.OriginalDigest.Valid {
			if _, contentErr := service.contentForRow(ctx, existing); contentErr == nil {
				if _, pending, err := service.pendingDeleteIntent(ctx, existing); err != nil {
					return Record{}, err
				} else if pending {
					return Record{}, ErrDescriptorDeletePending
				}
				return service.recordForRow(ctx, existing)
			}
		}
		beforeCaptureMaterialize(existing.ID)
		if _, pending, err := service.pendingDeleteIntent(ctx, existing); err != nil {
			return Record{}, err
		} else if pending {
			return Record{}, ErrDescriptorDeletePending
		}
		object, err := service.writeObject(ctx, existing.ID, data)
		if err != nil {
			return Record{}, err
		}
		if err := service.updateCaptured(ctx, existing.ID, object, digest, source, service.now()); err != nil {
			// The object was absent before writeObject succeeded, so it is
			// operation-owned. Remove it only after the same-file check; a
			// concurrent replacement is preserved and reported on the next read.
			_ = service.removeGeneratedObject(object, existing.ID)
			return Record{}, err
		}
		return service.get(ctx, existing.ID)
	}
	id, err := newID()
	if err != nil {
		return Record{}, err
	}
	object, err := service.writeObject(ctx, id, data)
	if err != nil {
		return Record{}, err
	}
	row := storedRecord{ID: id, DownloadID: sql.NullString{String: request.DownloadID, Valid: true}, DescriptorType: request.DescriptorType, StoragePath: object, OriginalDigest: sql.NullString{String: digest, Valid: true}, CaptureSource: source, CapturedAt: service.now().Format(time.RFC3339Nano), Retention: string(RetentionRetain)}
	if err := service.insert(ctx, row); err != nil {
		// The object name is operation-generated. Remove only after identity
		// validation; a concurrent winner's object is never selected here.
		_ = service.removeGeneratedObject(row.StoragePath, row.ID)
		if existing, found, lookupErr := service.findByKey(ctx, request); lookupErr == nil && found {
			if existing.OriginalDigest.Valid && existing.OriginalDigest.String == digest {
				return service.recordForRow(ctx, existing)
			}
			return Record{}, ErrDescriptorConflict
		}
		return Record{}, err
	}
	return service.recordForRow(ctx, row)
}

func (service *Service) contentForRow(ctx context.Context, row storedRecord) ([]byte, error) {
	if !row.OriginalDigest.Valid || row.DeletedAt.Valid {
		return nil, ErrDescriptorUnavailable
	}
	if err := validateStoragePath(row.StoragePath, row.ID); err != nil {
		return nil, err
	}
	file, info, err := openConstrainedFile(service.storageRoot, row.StoragePath)
	if err != nil {
		return nil, ErrDescriptorChanged
	}
	defer file.Close()
	data, digest, before, err := readStableFile(ctx, file, info, service.maxBytes)
	if err != nil {
		return nil, err
	}
	if err := verifyStableFile(ctx, file, before, digest, int64(len(data)), service.maxBytes); err != nil {
		return nil, err
	}
	if digest != row.OriginalDigest.String {
		return nil, ErrDescriptorChanged
	}
	return data, nil
}

func (service *Service) writeObject(ctx context.Context, id string, data []byte) (string, error) {
	if err := validateID(id); err != nil {
		return "", err
	}
	if len(data) == 0 || int64(len(data)) > service.maxBytes {
		return "", ErrDescriptorTooLarge
	}
	object := objectPath(id)
	destination := filepath.Join(service.storageRoot, object)
	objectsInfo, err := os.Stat(service.objectsRoot)
	if err != nil || !objectsInfo.IsDir() || objectsInfo.Mode().Perm()&0o077 != 0 {
		return "", fmt.Errorf("%w: object root unavailable", ErrPathEscape)
	}
	temporary, temporaryName, temporaryInfo, err := createPrivateStage(service.objectsRoot)
	if err != nil {
		return "", fmt.Errorf("%w: create private descriptor stage", ErrStorage)
	}
	removeStage := true
	publishedLink := false
	completed := false
	defer func() {
		_ = temporary.Close()
		if !completed && publishedLink {
			// The destination link is operation-owned only while it still
			// names the stage inode. A replacement is deliberately preserved.
			_ = removeFileIfSame(destination, temporaryInfo)
		}
		if !completed && removeStage {
			_ = removeFileIfSame(temporaryName, temporaryInfo)
		}
	}()
	hasher := sha256.New()
	buffer := make([]byte, 64*1024)
	reader := bytes.NewReader(data)
	for {
		if err := contextCheckpoint(ctx); err != nil {
			return "", err
		}
		read, readErr := reader.Read(buffer)
		if read > 0 {
			if _, err := temporary.Write(buffer[:read]); err != nil {
				return "", fmt.Errorf("%w: write descriptor stage", ErrStorage)
			}
			if _, err := hasher.Write(buffer[:read]); err != nil {
				return "", fmt.Errorf("%w: hash descriptor stage", ErrStorage)
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return "", fmt.Errorf("%w: read descriptor source", ErrStorage)
		}
	}
	if err := contextCheckpoint(ctx); err != nil {
		return "", err
	}
	if err := temporary.Sync(); err != nil {
		return "", fmt.Errorf("%w: sync descriptor stage", ErrStorage)
	}
	if err := temporary.Close(); err != nil {
		return "", fmt.Errorf("%w: close descriptor stage", ErrStorage)
	}
	latestRoot, err := os.Stat(service.objectsRoot)
	if err != nil || !os.SameFile(objectsInfo, latestRoot) {
		return "", fmt.Errorf("%w: object root changed", ErrPathEscape)
	}
	if _, err := os.Lstat(destination); err == nil {
		return "", ErrDescriptorConflict
	} else if !errors.Is(err, fs.ErrNotExist) {
		return "", fmt.Errorf("%w: inspect descriptor destination", ErrStorage)
	}
	if err := os.Link(temporaryName, destination); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return "", ErrDescriptorConflict
		}
		return "", fmt.Errorf("%w: publish descriptor object", ErrStorage)
	}
	publishedLink = true
	destinationInfo, err := os.Stat(destination)
	stageDigest := "sha256:" + hex.EncodeToString(hasher.Sum(nil))
	if err != nil || !os.SameFile(temporaryInfo, destinationInfo) || destinationInfo.Size() != int64(len(data)) || destinationInfo.Mode().Perm() != 0o600 || stageDigest != digestBytes(data) {
		return "", fmt.Errorf("%w: verify descriptor object", ErrStorage)
	}
	publishedFile, publishedInfo, err := openConstrainedFile(service.storageRoot, object)
	if err != nil {
		return "", fmt.Errorf("%w: open published descriptor", ErrStorage)
	}
	publishedData, publishedDigest, publishedBefore, readErr := readStableFile(ctx, publishedFile, publishedInfo, service.maxBytes)
	if readErr == nil {
		readErr = verifyStableFile(ctx, publishedFile, publishedBefore, publishedDigest, int64(len(publishedData)), service.maxBytes)
	}
	closeErr := publishedFile.Close()
	if readErr != nil || closeErr != nil || !os.SameFile(temporaryInfo, publishedInfo) || publishedDigest != digestBytes(data) || int64(len(publishedData)) != int64(len(data)) {
		return "", fmt.Errorf("%w: verify published descriptor bytes", ErrStorage)
	}
	if err := removeFileIfSame(temporaryName, temporaryInfo); err != nil {
		return "", fmt.Errorf("%w: clean descriptor stage", ErrStorage)
	}
	removeStage = false
	if err := syncDirectoryPath(service.objectsRoot); err != nil {
		return "", fmt.Errorf("%w: sync descriptor object directory", ErrStorage)
	}
	completed = true
	return object, nil
}

func (service *Service) removeGeneratedObject(storagePath, id string) error {
	if err := validateStoragePath(storagePath, id); err != nil {
		return err
	}
	file, info, err := openConstrainedFile(service.storageRoot, storagePath)
	if err != nil {
		return nil
	}
	_ = file.Close()
	return removeConstrainedFile(service.storageRoot, storagePath, info)
}

type deleteIntentMetadata struct {
	Scope          string `json:"scope"`
	DescriptorType string `json:"descriptor_type"`
	Digest         string `json:"digest"`
	FileIdentity   string `json:"file_identity,omitempty"`
}

func nullableString(value sql.NullString) any {
	if value.Valid {
		return value.String
	}
	return nil
}

// pendingDeleteIntent returns whether an append-only, redacted deletion
// intent exists for the exact descriptor identity. Audit rows cannot be
// updated by design, so the later descriptor.delete event is the terminal
// state and the pending row remains as the durable pre-unlink evidence.
func (service *Service) pendingDeleteIntent(ctx context.Context, row storedRecord) (deleteIntentMetadata, bool, error) {
	var rawMetadata string
	err := service.db.QueryRowContext(ctx, `SELECT metadata_json FROM audit_events WHERE action = ? AND resource_kind = 'descriptor' AND resource_id = ? AND outcome = 'pending' ORDER BY id LIMIT 1`, deleteIntentAction, row.ID).Scan(&rawMetadata)
	if errors.Is(err, sql.ErrNoRows) {
		return deleteIntentMetadata{}, false, nil
	}
	if err != nil {
		if contextErr := contextCheckpoint(ctx); contextErr != nil {
			return deleteIntentMetadata{}, false, contextErr
		}
		return deleteIntentMetadata{}, false, fmt.Errorf("%w: read deletion intent", ErrStorage)
	}
	metadata, err := decodeDeleteIntentMetadata(rawMetadata)
	if err != nil {
		return deleteIntentMetadata{}, false, err
	}
	if err := validateDeleteIntentMetadata(metadata, row, ""); err != nil {
		return deleteIntentMetadata{}, false, err
	}
	return metadata, true, nil
}

// ensureDeleteIntent commits the exact deletion identity before any
// irreversible filesystem effect. It is idempotent for the descriptor's
// pending intent and contains no pathname or descriptor bytes.
func (service *Service) ensureDeleteIntent(ctx context.Context, row storedRecord, fileIdentity string) error {
	metadata, err := json.Marshal(deleteIntentMetadata{
		Scope:          "descriptor",
		DescriptorType: row.DescriptorType,
		Digest:         row.OriginalDigest.String,
		FileIdentity:   fileIdentity,
	})
	if err != nil {
		return fmt.Errorf("%w: encode deletion intent", ErrStorage)
	}
	tx, err := service.db.BeginTx(ctx, nil)
	if err != nil {
		if contextErr := contextCheckpoint(ctx); contextErr != nil {
			return contextErr
		}
		return fmt.Errorf("%w: begin deletion intent", ErrStorage)
	}
	defer func() { _ = tx.Rollback() }()
	var existing string
	queryErr := tx.QueryRowContext(ctx, `SELECT metadata_json FROM audit_events WHERE action = ? AND resource_kind = 'descriptor' AND resource_id = ? AND outcome = 'pending' ORDER BY id LIMIT 1`, deleteIntentAction, row.ID).Scan(&existing)
	switch {
	case queryErr == nil:
		existingMetadata, decodeErr := decodeDeleteIntentMetadata(existing)
		if decodeErr != nil {
			return decodeErr
		}
		if err := validateDeleteIntentMetadata(existingMetadata, row, fileIdentity); err != nil {
			return err
		}
	case errors.Is(queryErr, sql.ErrNoRows):
		eventID, idErr := newID()
		if idErr != nil {
			return idErr
		}
		result, insertErr := tx.ExecContext(ctx, `
			INSERT INTO audit_events (event_id, occurred_at, actor, action, resource_kind, resource_id, outcome, metadata_json, redacted)
			SELECT ?, ?, 'unauthenticated', ?, 'descriptor', ?, 'pending', ?, 1
			FROM descriptors
			WHERE id = ?
			  AND download_id = ?
			  AND descriptor_type = ?
			  AND storage_path = ?
			  AND original_digest IS ?
			  AND capture_source = ?
			  AND captured_at = ?
			  AND retention = ?
			  AND unavailable_reason IS ?
			  AND deleted_at IS NULL
			  AND NOT EXISTS (
				  SELECT 1
				  FROM audit_events
				  WHERE action = ?
				    AND resource_kind = 'descriptor'
				    AND resource_id = ?
				    AND outcome = 'pending'
			  )`,
			eventID,
			service.now().Format(time.RFC3339Nano),
			deleteIntentAction,
			row.ID,
			string(metadata),
			row.ID,
			row.DownloadID.String,
			row.DescriptorType,
			row.StoragePath,
			nullableString(row.OriginalDigest),
			row.CaptureSource,
			row.CapturedAt,
			row.Retention,
			nullableString(row.UnavailableReason),
			deleteIntentAction,
			row.ID,
		)
		if insertErr != nil {
			if contextErr := contextCheckpoint(ctx); contextErr != nil {
				return contextErr
			}
			return fmt.Errorf("%w: persist deletion intent", ErrStorage)
		}
		count, rowsErr := result.RowsAffected()
		if rowsErr != nil {
			return fmt.Errorf("%w: inspect deletion intent transition", ErrStorage)
		}
		if count != 1 {
			return fmt.Errorf("%w: descriptor changed before deletion intent", ErrDescriptorConflict)
		}
	default:
		if contextErr := contextCheckpoint(ctx); contextErr != nil {
			return contextErr
		}
		return fmt.Errorf("%w: read deletion intent", ErrStorage)
	}
	if err := tx.Commit(); err != nil {
		if contextErr := contextCheckpoint(ctx); contextErr != nil {
			return contextErr
		}
		return fmt.Errorf("%w: commit deletion intent", ErrStorage)
	}
	return nil
}

func decodeDeleteIntentMetadata(raw string) (deleteIntentMetadata, error) {
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.DisallowUnknownFields()
	var metadata deleteIntentMetadata
	if err := decoder.Decode(&metadata); err != nil {
		return deleteIntentMetadata{}, fmt.Errorf("%w: malformed deletion intent", ErrStorage)
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return deleteIntentMetadata{}, fmt.Errorf("%w: malformed deletion intent", ErrStorage)
	}
	return metadata, nil
}

func validateDeleteIntentMetadata(metadata deleteIntentMetadata, row storedRecord, expectedIdentity string) error {
	if metadata.Scope != "descriptor" || metadata.DescriptorType != row.DescriptorType || metadata.Digest != row.OriginalDigest.String {
		return fmt.Errorf("%w: deletion intent identity changed", ErrDescriptorChanged)
	}
	if row.OriginalDigest.Valid {
		if metadata.FileIdentity == "" {
			return fmt.Errorf("%w: deletion intent lacks object identity", ErrStorage)
		}
		if expectedIdentity != "" && metadata.FileIdentity != expectedIdentity {
			return ErrDescriptorChanged
		}
	} else if metadata.FileIdentity != "" {
		return fmt.Errorf("%w: unavailable deletion intent has object identity", ErrStorage)
	}
	return nil
}

func ensureJSONEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return errors.New("unexpected trailing JSON value")
		}
		return err
	}
	return nil
}

func (service *Service) markDeleted(ctx context.Context, row storedRecord, deletedAt time.Time) error {
	eventID, err := newID()
	if err != nil {
		return err
	}
	metadata, err := json.Marshal(map[string]string{"scope": "descriptor", "descriptor_type": row.DescriptorType, "digest": row.OriginalDigest.String})
	if err != nil {
		return err
	}
	tx, err := service.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("%w: begin deletion", ErrStorage)
	}
	defer func() { _ = tx.Rollback() }()
	result, err := tx.ExecContext(ctx, `
		UPDATE descriptors
		SET retention = 'deleted', deleted_at = ?
		WHERE id = ?
		  AND download_id = ?
		  AND descriptor_type = ?
		  AND storage_path = ?
		  AND original_digest IS ?
		  AND capture_source = ?
		  AND captured_at = ?
		  AND retention = ?
		  AND unavailable_reason IS ?
		  AND deleted_at IS NULL
		  AND EXISTS (
			  SELECT 1
			  FROM audit_events
			  WHERE action = ?
			    AND resource_kind = 'descriptor'
			    AND resource_id = ?
			    AND outcome = 'pending'
		  )`,
		deletedAt.UTC().Format(time.RFC3339Nano),
		row.ID,
		row.DownloadID.String,
		row.DescriptorType,
		row.StoragePath,
		nullableString(row.OriginalDigest),
		row.CaptureSource,
		row.CapturedAt,
		row.Retention,
		nullableString(row.UnavailableReason),
		deleteIntentAction,
		row.ID,
	)
	if err != nil {
		return fmt.Errorf("%w: mark deletion", ErrStorage)
	}
	count, err := result.RowsAffected()
	if err != nil || count == 0 {
		return fmt.Errorf("%w: descriptor deletion lost race", ErrDescriptorConflict)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO audit_events (event_id, occurred_at, actor, action, resource_kind, resource_id, outcome, metadata_json, redacted) VALUES (?, ?, 'unauthenticated', 'descriptor.delete', 'descriptor', ?, 'deleted', ?, 1)`, eventID, deletedAt.UTC().Format(time.RFC3339Nano), row.ID, string(metadata)); err != nil {
		return fmt.Errorf("%w: record deletion audit", ErrStorage)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("%w: commit deletion", ErrStorage)
	}
	return nil
}

func (service *Service) insert(ctx context.Context, row storedRecord) error {
	_, err := service.db.ExecContext(ctx, `INSERT INTO descriptors (id, download_id, descriptor_type, storage_path, original_digest, capture_source, captured_at, retention, unavailable_reason, deleted_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, row.ID, row.DownloadID, row.DescriptorType, row.StoragePath, row.OriginalDigest, row.CaptureSource, row.CapturedAt, row.Retention, row.UnavailableReason, row.DeletedAt)
	if err != nil {
		if contextErr := contextCheckpoint(ctx); contextErr != nil {
			return contextErr
		}
		return fmt.Errorf("%w: insert descriptor metadata", ErrStorage)
	}
	return nil
}

func (service *Service) updateCaptured(ctx context.Context, id, storagePath, digest, source string, capturedAt time.Time) error {
	result, err := service.db.ExecContext(ctx, `
		UPDATE descriptors
		SET storage_path = ?, original_digest = ?, capture_source = ?, captured_at = ?, retention = 'retain', unavailable_reason = NULL, deleted_at = NULL
		WHERE id = ?
		  AND deleted_at IS NULL
		  AND NOT EXISTS (
			  SELECT 1
			  FROM audit_events
			  WHERE action = ?
			    AND resource_kind = 'descriptor'
			    AND resource_id = descriptors.id
			    AND outcome = 'pending'
		  )`, storagePath, digest, source, capturedAt.UTC().Format(time.RFC3339Nano), id, deleteIntentAction)
	if err != nil {
		if contextErr := contextCheckpoint(ctx); contextErr != nil {
			return contextErr
		}
		return fmt.Errorf("%w: update descriptor metadata", ErrStorage)
	}
	count, err := result.RowsAffected()
	if err != nil || count != 1 {
		return fmt.Errorf("%w: descriptor metadata changed", ErrDescriptorConflict)
	}
	return nil
}

func (service *Service) updateUnavailable(ctx context.Context, id, storagePath, source, reason string) error {
	result, err := service.db.ExecContext(ctx, `
		UPDATE descriptors
		SET storage_path = ?, original_digest = NULL, capture_source = ?, unavailable_reason = ?, retention = 'retain', deleted_at = NULL
		WHERE id = ?
		  AND deleted_at IS NULL
		  AND original_digest IS NULL
		  AND NOT EXISTS (
			  SELECT 1
			  FROM audit_events
			  WHERE action = ?
			    AND resource_kind = 'descriptor'
			    AND resource_id = descriptors.id
			    AND outcome = 'pending'
		  )`, storagePath, source, reason, id, deleteIntentAction)
	if err != nil {
		if contextErr := contextCheckpoint(ctx); contextErr != nil {
			return contextErr
		}
		return fmt.Errorf("%w: update unavailable descriptor", ErrStorage)
	}
	count, err := result.RowsAffected()
	if err != nil || count != 1 {
		return fmt.Errorf("%w: unavailable descriptor changed", ErrDescriptorConflict)
	}
	return nil
}

func (service *Service) get(ctx context.Context, id string) (Record, error) {
	row, err := service.getStored(ctx, id)
	if err != nil {
		return Record{}, err
	}
	return service.recordForRow(ctx, row)
}

// recordForRow enriches the durable metadata with a verified byte count. The
// count is deliberately derived only after the object has been opened and
// checked against its persisted digest; a missing or changed object is
// reported as unavailable while its non-secret provenance remains visible.
func (service *Service) recordForRow(ctx context.Context, row storedRecord) (Record, error) {
	record, err := row.record()
	if err != nil {
		return Record{}, err
	}
	if !record.Available {
		return record, nil
	}
	data, err := service.contentForRow(ctx, row)
	if err != nil {
		record.Available = false
		record.Size = 0
		if record.UnavailableReason == "" {
			record.UnavailableReason = "content_unavailable"
		}
		return record, nil
	}
	record.Size = int64(len(data))
	return record, nil
}

func (service *Service) getStored(ctx context.Context, id string) (storedRecord, error) {
	var row storedRecord
	err := service.db.QueryRowContext(ctx, `SELECT id, download_id, descriptor_type, storage_path, original_digest, capture_source, captured_at, retention, unavailable_reason, deleted_at FROM descriptors WHERE id = ?`, id).Scan(&row.ID, &row.DownloadID, &row.DescriptorType, &row.StoragePath, &row.OriginalDigest, &row.CaptureSource, &row.CapturedAt, &row.Retention, &row.UnavailableReason, &row.DeletedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return storedRecord{}, ErrDescriptorNotFound
	}
	if err != nil {
		if contextErr := contextCheckpoint(ctx); contextErr != nil {
			return storedRecord{}, contextErr
		}
		return storedRecord{}, fmt.Errorf("%w: read descriptor metadata", ErrStorage)
	}
	if err := row.validate(); err != nil {
		return storedRecord{}, err
	}
	return row, nil
}

func (service *Service) findByKey(ctx context.Context, request CaptureRequest) (storedRecord, bool, error) {
	var row storedRecord
	err := service.db.QueryRowContext(ctx, `SELECT id, download_id, descriptor_type, storage_path, original_digest, capture_source, captured_at, retention, unavailable_reason, deleted_at FROM descriptors WHERE download_id = ? AND descriptor_type = ? LIMIT 1`, request.DownloadID, request.DescriptorType).Scan(&row.ID, &row.DownloadID, &row.DescriptorType, &row.StoragePath, &row.OriginalDigest, &row.CaptureSource, &row.CapturedAt, &row.Retention, &row.UnavailableReason, &row.DeletedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return storedRecord{}, false, nil
	}
	if err != nil {
		if contextErr := contextCheckpoint(ctx); contextErr != nil {
			return storedRecord{}, false, contextErr
		}
		return storedRecord{}, false, fmt.Errorf("%w: find descriptor metadata", ErrStorage)
	}
	if err := row.validate(); err != nil {
		return storedRecord{}, false, err
	}
	return row, true, nil
}

func (service *Service) lockFor(key string) func() {
	value, _ := service.locks.LoadOrStore(key, &sync.Mutex{})
	mutex := value.(*sync.Mutex)
	mutex.Lock()
	return mutex.Unlock
}

// descriptorLockKey is shared by every local transition for one durable
// descriptor identity. A descriptor ID alone is insufficient for capture
// because capture initially addresses the download/type pair; using the
// same composite key keeps those transitions ordered when they share a
// Service. The SQL compare-and-swap guards remain authoritative across
// separate Service values or processes.
func descriptorLockKey(downloadID, descriptorType string) string {
	return "descriptor\x00" + downloadID + "\x00" + descriptorType
}

func (service *Service) now() time.Time {
	return service.clock().UTC()
}

func (service *Service) validate() error {
	if service == nil || service.db == nil || service.storageRoot == "" || service.objectsRoot == "" {
		return ErrInvalidConfig
	}
	return nil
}

func (row storedRecord) validate() error {
	if err := validateID(row.ID); err != nil {
		return err
	}
	if !row.DownloadID.Valid {
		return fmt.Errorf("%w: descriptor download identity", ErrStorage)
	}
	if err := validateIdentifier(row.DownloadID.String); err != nil {
		return fmt.Errorf("%w: descriptor download identity", ErrStorage)
	}
	if err := validateToken(row.DescriptorType, maxIdentifierBytes); err != nil {
		return fmt.Errorf("%w: descriptor type", ErrStorage)
	}
	if err := validateStoragePath(row.StoragePath, row.ID); err != nil {
		return err
	}
	if err := validateToken(row.CaptureSource, maxIdentifierBytes); err != nil {
		return fmt.Errorf("%w: capture source", ErrStorage)
	}
	if _, err := time.Parse(time.RFC3339Nano, row.CapturedAt); err != nil {
		return fmt.Errorf("%w: capture timestamp", ErrStorage)
	}
	switch Retention(row.Retention) {
	case RetentionRetain, RetentionEligible, RetentionDeleted:
	default:
		return fmt.Errorf("%w: retention", ErrStorage)
	}
	if row.OriginalDigest.Valid {
		if _, err := normalizeDigest(row.OriginalDigest.String); err != nil {
			return fmt.Errorf("%w: descriptor digest", ErrStorage)
		}
	}
	if !row.OriginalDigest.Valid && !row.UnavailableReason.Valid {
		return fmt.Errorf("%w: descriptor has neither bytes nor unavailable reason", ErrStorage)
	}
	if row.UnavailableReason.Valid {
		if err := validateToken(row.UnavailableReason.String, maxReasonBytes); err != nil {
			return fmt.Errorf("%w: unavailable reason", ErrStorage)
		}
	}
	if row.DeletedAt.Valid {
		if _, err := time.Parse(time.RFC3339Nano, row.DeletedAt.String); err != nil {
			return fmt.Errorf("%w: deletion timestamp", ErrStorage)
		}
	}
	return nil
}

func (row storedRecord) record() (Record, error) {
	if err := row.validate(); err != nil {
		return Record{}, err
	}
	captured, _ := time.Parse(time.RFC3339Nano, row.CapturedAt)
	record := Record{ID: row.ID, DownloadID: row.DownloadID.String, DescriptorType: row.DescriptorType, Digest: row.OriginalDigest.String, CaptureSource: row.CaptureSource, CapturedAt: captured.UTC(), Retention: Retention(row.Retention), UnavailableReason: row.UnavailableReason.String}
	if row.OriginalDigest.Valid {
		// Size is intentionally read from the object only for explicit content;
		// metadata responses do not claim a size from unverified filesystem state.
		record.Available = !row.DeletedAt.Valid
	}
	if row.DeletedAt.Valid {
		deleted, _ := time.Parse(time.RFC3339Nano, row.DeletedAt.String)
		record.DeletedAt = &deleted
		record.Available = false
	}
	return record, nil
}

func validateCaptureRequest(request CaptureRequest) error {
	if err := validateIdentifier(request.DownloadID); err != nil {
		return fmt.Errorf("%w: download id", ErrInvalidRequest)
	}
	if err := validateToken(request.DescriptorType, maxIdentifierBytes); err != nil {
		return fmt.Errorf("%w: descriptor type", ErrInvalidRequest)
	}
	return nil
}

func validateSourceMetadata(source, identity, verification, expectedDigest string, expectedSize int64) error {
	if err := validateToken(source, maxIdentifierBytes); err != nil {
		return fmt.Errorf("%w: source", ErrSourceUnverified)
	}
	if !strings.HasSuffix(source, ".export") {
		return fmt.Errorf("%w: source is not an export", ErrSourceUnverified)
	}
	if err := validateIdentifier(identity); err != nil {
		return fmt.Errorf("%w: source identity", ErrSourceUnverified)
	}
	if err := validateToken(verification, maxIdentifierBytes); err != nil {
		return fmt.Errorf("%w: verification reference", ErrSourceUnverified)
	}
	if _, err := normalizeDigest(expectedDigest); err != nil {
		return fmt.Errorf("%w: expected digest", ErrSourceUnverified)
	}
	if expectedSize < 0 || expectedSize > DefaultMaxBytes {
		return ErrDescriptorTooLarge
	}
	return nil
}

func readExport(ctx context.Context, export VerifiedExport, maximum int64) ([]byte, error) {
	if export.Reader != nil && len(export.Bytes) != 0 {
		return nil, fmt.Errorf("%w: export has multiple byte sources", ErrInvalidRequest)
	}
	var data []byte
	var err error
	if export.Reader != nil {
		data, err = readBounded(ctx, export.Reader, maximum)
	} else {
		if int64(len(export.Bytes)) > maximum {
			return nil, ErrDescriptorTooLarge
		}
		data = append([]byte(nil), export.Bytes...)
		if err := contextCheckpoint(ctx); err != nil {
			return nil, err
		}
	}
	if err != nil {
		return nil, err
	}
	if len(data) == 0 {
		return nil, ErrSourceUnavailable
	}
	if export.ExpectedSize > 0 && int64(len(data)) != export.ExpectedSize {
		return nil, ErrSourceUnverified
	}
	if digestBytes(data) != mustNormalizeDigest(export.ExpectedDigest) {
		return nil, ErrSourceUnverified
	}
	return data, nil
}

func readBounded(ctx context.Context, source io.Reader, maximum int64) ([]byte, error) {
	if maximum <= 0 {
		return nil, ErrDescriptorTooLarge
	}
	data := make([]byte, 0, minInt64(maximum, 64*1024))
	buffer := make([]byte, 64*1024)
	zeroReads := 0
	for {
		if err := contextCheckpoint(ctx); err != nil {
			return nil, err
		}
		read, err := source.Read(buffer)
		if read < 0 || read > len(buffer) {
			return nil, fmt.Errorf("%w: invalid export reader result", ErrSourceUnavailable)
		}
		if read > 0 {
			zeroReads = 0
			if int64(len(data))+int64(read) > maximum {
				return nil, ErrDescriptorTooLarge
			}
			data = append(data, buffer[:read]...)
		} else if err == nil {
			zeroReads++
			if zeroReads >= 8 {
				return nil, fmt.Errorf("%w: export reader made no progress", ErrSourceUnavailable)
			}
		}
		if err == io.EOF {
			return data, nil
		}
		if err != nil {
			if contextErr := contextCheckpoint(ctx); contextErr != nil {
				return nil, contextErr
			}
			return nil, fmt.Errorf("%w: read export", ErrSourceUnavailable)
		}
	}
}

func readStableFile(ctx context.Context, file *os.File, before fs.FileInfo, maximum int64) ([]byte, string, fs.FileInfo, error) {
	if before.Size() < 0 || before.Size() > maximum {
		return nil, "", before, ErrDescriptorTooLarge
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return nil, "", before, ErrDescriptorChanged
	}
	data, err := readBounded(ctx, file, maximum)
	if err != nil {
		return nil, "", before, err
	}
	digest := digestBytes(data)
	return data, digest, before, nil
}

func verifyStableFile(ctx context.Context, file *os.File, before fs.FileInfo, digest string, size int64, maximum int64) error {
	if err := contextCheckpoint(ctx); err != nil {
		return err
	}
	after, err := file.Stat()
	if err != nil || !os.SameFile(before, after) || after.Size() != size || after.Size() > maximum {
		return ErrDescriptorChanged
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return ErrDescriptorChanged
	}
	second, err := readBounded(ctx, file, maximum)
	if err != nil || digestBytes(second) != digest || int64(len(second)) != size {
		if contextErr := contextCheckpoint(ctx); contextErr != nil {
			return contextErr
		}
		return ErrDescriptorChanged
	}
	latest, err := file.Stat()
	if err != nil || !os.SameFile(before, latest) || latest.Size() != size {
		return ErrDescriptorChanged
	}
	if err := contextCheckpoint(ctx); err != nil {
		return err
	}
	return nil
}

// verifyMountedPath re-opens the configured source pathname after the
// opened-file stability check. Holding an fd keeps an unlinked inode readable,
// so the fd checks alone cannot detect an atomic pathname replacement. The
// pathname must still resolve to the same object and the second opened view
// must still carry the bytes that were approved for capture.
func verifyMountedPath(ctx context.Context, root, relative string, expected fs.FileInfo, digest string, size, maximum int64) error {
	if err := contextCheckpoint(ctx); err != nil {
		return err
	}
	file, info, err := openConstrainedFile(root, relative)
	if err != nil {
		// The initial path was already validated. Any later path failure means
		// the selected source no longer has the identity that was reviewed.
		return fmt.Errorf("%w: mounted pathname changed: %v", ErrDescriptorChanged, err)
	}
	defer file.Close()
	if expected == nil || !os.SameFile(expected, info) {
		return ErrDescriptorChanged
	}
	data, reopenedDigest, before, err := readStableFile(ctx, file, info, maximum)
	if err != nil {
		return err
	}
	if err := verifyStableFile(ctx, file, before, reopenedDigest, int64(len(data)), maximum); err != nil {
		return err
	}
	if reopenedDigest != digest || int64(len(data)) != size {
		return ErrDescriptorChanged
	}
	return nil
}

func digestOpenedFile(ctx context.Context, file *os.File, info fs.FileInfo, maximum int64) (string, error) {
	data, digest, before, err := readStableFile(ctx, file, info, maximum)
	if err != nil {
		return "", err
	}
	if err := verifyStableFile(ctx, file, before, digest, int64(len(data)), maximum); err != nil {
		return "", err
	}
	return digest, nil
}

func digestBytes(data []byte) string {
	digest := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(digest[:])
}

func normalizeDigest(value string) (string, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	if len(value) != len("sha256:")+64 || !strings.HasPrefix(value, "sha256:") {
		return "", ErrSourceUnverified
	}
	if _, err := hex.DecodeString(value[len("sha256:"):]); err != nil {
		return "", ErrSourceUnverified
	}
	return value, nil
}

func mustNormalizeDigest(value string) string {
	normalized, _ := normalizeDigest(value)
	return normalized
}

func validateID(value string) error {
	if _, err := domain.ParseRuntimeID(value); err != nil {
		return fmt.Errorf("%w: descriptor id", ErrInvalidRequest)
	}
	return nil
}

func newID() (string, error) {
	id, err := domain.NewRuntimeID()
	if err != nil {
		return "", fmt.Errorf("%w: descriptor identity", ErrStorage)
	}
	return id.String(), nil
}

func validateIdentifier(value string) error {
	trimmed := strings.TrimSpace(value)
	if value != trimmed || trimmed == "" || len(trimmed) > maxIdentifierBytes || strings.IndexByte(trimmed, 0) >= 0 || strings.ContainsAny(trimmed, `/\\`) {
		return ErrInvalidRequest
	}
	return nil
}

func validateToken(value string, maximum int) error {
	if value == "" || len(value) > maximum || strings.IndexByte(value, 0) >= 0 {
		return ErrInvalidRequest
	}
	for _, char := range value {
		if char > 0x7f || (char < 0x21) || char == '/' || char == '\\' || char == '"' {
			return ErrInvalidRequest
		}
	}
	return nil
}

func validateRelativePath(value string) error {
	if value == "" || strings.IndexByte(value, 0) >= 0 || filepath.IsAbs(value) {
		return ErrPathEscape
	}
	clean := filepath.Clean(filepath.FromSlash(value))
	if clean == "." || clean != filepath.FromSlash(value) || strings.ContainsAny(value, `\\`) {
		return ErrPathEscape
	}
	for _, component := range strings.Split(filepath.ToSlash(clean), "/") {
		if component == "" || component == "." || component == ".." {
			return ErrPathEscape
		}
	}
	return nil
}

func objectPath(id string) string {
	return filepath.ToSlash(filepath.Join(objectDirectory, id+objectSuffix))
}

func validateStoragePath(value, id string) error {
	if value != objectPath(id) {
		return fmt.Errorf("%w: descriptor storage identity", ErrPathEscape)
	}
	return nil
}

func prepareDirectory(value string, create, private bool) (string, error) {
	if value == "" || strings.IndexByte(value, 0) >= 0 || !filepath.IsAbs(value) {
		return "", ErrInvalidConfig
	}
	clean := filepath.Clean(value)
	if clean != value {
		return "", ErrInvalidConfig
	}
	if create {
		if err := os.MkdirAll(clean, 0o700); err != nil {
			return "", err
		}
	}
	info, err := os.Lstat(clean)
	if err != nil {
		return "", err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return "", ErrSymlink
	}
	if !info.IsDir() {
		return "", ErrSpecialFile
	}
	if private && info.Mode().Perm()&0o077 != 0 {
		return "", ErrInvalidConfig
	}
	resolved, err := filepath.EvalSymlinks(clean)
	if err != nil {
		return "", err
	}
	return resolved, nil
}

func contextCheckpoint(ctx context.Context) error {
	if ctx == nil {
		return context.Canceled
	}
	return ctx.Err()
}

func minInt64(left, right int64) int {
	if left < right {
		return int(left)
	}
	return int(right)
}

func removeFileIfSame(name string, expected fs.FileInfo) error {
	current, err := os.Lstat(name)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	}
	if expected == nil || !os.SameFile(expected, current) {
		return ErrDescriptorChanged
	}
	return os.Remove(name)
}

// These seams are package-local and therefore unavailable to production
// callers. They let synthetic tests inject a mutation at each safety boundary.
var afterSourceRead = func(*os.File) {}
var beforeCaptureMaterialize = func(string) {}
var beforeDeleteIntent = func(string) {}
var afterDeleteIntent = func(string) {}
var beforeDeleteDescriptor = func(string) {}
var afterDeleteUnlink = func(string) {}
