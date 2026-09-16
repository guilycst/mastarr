// Package trash owns the durable coordination boundary for recoverable media
// deletion. It records an exact manifest before calling a filesystem or
// download-client port, then advances each item only from returned evidence.
//
// Filesystem and upstream implementations remain outside this package. The
// service deliberately does not call an Arr adapter, re-add a download, or
// resume a stopped client. It only composes the already reviewed ports and
// the existing SQLite trash/janitor journal.
package trash

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/guilycst/mastarr/internal/domain"
	"github.com/guilycst/mastarr/internal/ports"
	"github.com/guilycst/mastarr/internal/storage"
	"github.com/guilycst/mastarr/internal/storage/sqlc"
)

const (
	// DefaultRetention is the recoverable-trash policy used when a caller does
	// not provide a policy-specific duration.
	DefaultRetention = 30 * 24 * time.Hour
	// DefaultLeaseDuration bounds one external call. A crashed process leaves
	// a reconcilable lease instead of an indefinitely claimed entry.
	DefaultLeaseDuration    = 2 * time.Minute
	DefaultRetryAfter       = 30 * time.Second
	DefaultMaxItems         = 10_000
	DefaultMaxManifestBytes = 4 << 20
)

var (
	ErrInvalidConfig       = errors.New("trash service configuration is invalid")
	ErrInvalidRequest      = errors.New("trash request is invalid")
	ErrNotFound            = errors.New("trash entry was not found")
	ErrConflict            = errors.New("trash entry conflicts with the requested operation")
	ErrClaimed             = errors.New("trash entry is claimed by another operation")
	ErrHeld                = errors.New("trash entry is held for review")
	ErrNotDue              = errors.New("trash entry retention has not expired")
	ErrClock               = errors.New("trash service clock cannot establish retention order")
	ErrUncertain           = errors.New("trash operation outcome is uncertain")
	ErrIncomplete          = errors.New("trash operation returned incomplete item evidence")
	ErrDependency          = errors.New("trash operation dependency is unavailable")
	ErrDownloadMissing     = errors.New("download record is missing")
	ErrUnsupported         = errors.New("trash operation is unsupported")
	ErrStorage             = errors.New("trash journal operation failed")
	ErrClientActive        = errors.New("download client is not stopped")
	ErrIdentityChanged     = errors.New("trash item identity changed")
	ErrDestinationConflict = errors.New("trash restore destination conflicts")
	ErrCorrupt             = errors.New("trash journal contains invalid manifest evidence")
)

// Options wires the service to already opened storage and normalized ports.
// FilesystemRead is required even though action ports perform their own
// checks: this package must observe exact identity before every mutation.
type Options struct {
	Store *storage.Store
	// Storage is an alias retained for callers that name the dependency after
	// its responsibility. New accepts either field, never both distinct stores.
	Storage *storage.Store

	Read             ports.FilesystemReadPort
	FilesystemRead   ports.FilesystemReadPort
	Action           ports.FilesystemActionPort
	FilesystemAction ports.FilesystemActionPort
	Download         ports.DownloadControlPort
	DownloadControl  ports.DownloadControlPort

	Clock            func() time.Time
	WorkerID         string
	LeaseDuration    time.Duration
	RetryAfter       time.Duration
	MaxItems         int
	MaxManifestBytes int

	// IsDownloadMissing is optional because upstream adapters use typed error
	// vocabularies. The safe default treats every observation error as unknown.
	IsDownloadMissing func(error) bool
}

// Service coordinates exact trash entries and janitor records.
type Service struct {
	store             *storage.Store
	read              ports.FilesystemReadPort
	action            ports.FilesystemActionPort
	download          ports.DownloadControlPort
	clock             func() time.Time
	workerID          string
	leaseDuration     time.Duration
	retryAfter        time.Duration
	maxItems          int
	maxManifestBytes  int
	isDownloadMissing func(error) bool
	locks             sync.Map
}

// TrashRequest describes one exact root-relative payload scope. EntryID is
// optional; when omitted the service creates a runtime UUID. ID is an alias
// useful to HTTP callers that already use a generic resource ID field. When an
// idempotency key is present, retries return this entry's durable state.
type TrashRequest struct {
	EntryID        string
	ID             string
	RootID         domain.ConfigID
	OriginalPrefix string
	TrashPrefix    string
	Manifest       []domain.FileManifestEntry
	Retention      time.Duration
	Client         *ports.DownloadRef
	IdempotencyKey string
}

// RestoreRequest identifies one previously trashed entry. Restore is always
// exact-scope and never re-adds or resumes an upstream download.
type RestoreRequest struct {
	EntryID        string
	ID             string
	IdempotencyKey string
}

// PurgeRequest identifies one trash entry for payload deletion. HardDelete
// is an explicit API decision that permits a purge before expiry; it still
// cannot select anything outside the stored trash manifest.
type PurgeRequest struct {
	EntryID        string
	ID             string
	HardDelete     bool
	Force          bool
	IdempotencyKey string
}

// RetryRequest reopens a held operation only after a fresh read-only
// reconciliation proves its stored item scope is still safe to claim.
type RetryRequest struct {
	EntryID   string
	ID        string
	Operation Operation
}

// Operation names the two mutually exclusive janitor operations.
type Operation string

const (
	OperationPurge   Operation = "purge"
	OperationRestore Operation = "restore"
)

// Item is the public, non-storage representation of one persisted manifest
// item. It contains exact root-relative paths and identity evidence, never a
// host path or a wildcard.
type Item struct {
	ID                   string
	EntryID              string
	RootID               domain.ConfigID
	OriginalRelativePath string
	TrashRelativePath    string
	Type                 domain.ManifestEntryType
	Size                 int64
	Digest               string
	FileIdentity         string
	State                string
	TrashedAt            *time.Time
	RestoredAt           *time.Time
	PurgedAt             *time.Time
	Client               *ports.DownloadRef
}

// Entry is the durable trash projection returned to API/BFF callers.
type Entry struct {
	ID              string
	RootID          domain.ConfigID
	State           string
	OriginalPrefix  string
	TrashPrefix     string
	Manifest        []domain.FileManifestEntry
	Retention       time.Duration
	TrashedAt       *time.Time
	ExpiresAt       time.Time
	HoldReason      string
	Client          *ports.DownloadRef
	Items           []Item
	Version         int64
	ActiveOperation Operation
	ClaimedBy       string
	LeaseUntil      *time.Time
}

// ItemEffect preserves per-item certainty and evidence without exposing
// storage or upstream implementation types.
type ItemEffect struct {
	ItemID     string
	Path       string
	Operation  Operation
	State      string
	Outcome    domain.EffectOutcome
	ObservedAt time.Time
	Evidence   []string
}

// Result combines durable state with the effect evidence observed in this
// call. A non-nil error may still accompany a useful held/partial result.
type Result struct {
	Entry     Entry
	Outcome   domain.EffectOutcome
	Effects   []ItemEffect
	Evidence  []string
	Retryable bool
}

// TickResult reports bounded janitor work. Individual operation errors remain
// attached to result entries; one bad entry does not let another entry bypass
// its expiry or identity checks.
type TickResult struct {
	Processed int
	Skipped   int
	Held      int
	Results   []Result
}

// New validates wiring but does not touch external services. Store must be an
// already migrated storage owner; this package never opens a second database.
func New(store *storage.Store, options Options) (*Service, error) {
	if store == nil {
		store = options.Store
	}
	if store == nil {
		store = options.Storage
	}
	if store == nil {
		return nil, fmt.Errorf("%w: storage is nil", ErrInvalidConfig)
	}
	read := options.Read
	if read == nil {
		read = options.FilesystemRead
	}
	action := options.Action
	if action == nil {
		action = options.FilesystemAction
	}
	if read == nil || action == nil {
		return nil, fmt.Errorf("%w: filesystem read/action ports are required", ErrInvalidConfig)
	}
	download := options.Download
	if download == nil {
		download = options.DownloadControl
	}
	clock := options.Clock
	if clock == nil {
		clock = func() time.Time { return time.Now().UTC() }
	}
	workerID := strings.TrimSpace(options.WorkerID)
	if workerID == "" {
		id, err := domain.NewRuntimeID()
		if err != nil {
			return nil, fmt.Errorf("%w: generate worker id: %v", ErrInvalidConfig, err)
		}
		workerID = id.String()
	}
	if len(workerID) > 128 || strings.ContainsRune(workerID, 0) {
		return nil, fmt.Errorf("%w: worker id is invalid", ErrInvalidConfig)
	}
	lease := options.LeaseDuration
	if lease <= 0 {
		lease = DefaultLeaseDuration
	}
	if lease > 24*time.Hour {
		return nil, fmt.Errorf("%w: lease duration exceeds safety bound", ErrInvalidConfig)
	}
	retry := options.RetryAfter
	if retry <= 0 {
		retry = DefaultRetryAfter
	}
	maxItems := options.MaxItems
	if maxItems <= 0 {
		maxItems = DefaultMaxItems
	}
	if maxItems > DefaultMaxItems {
		return nil, fmt.Errorf("%w: item bound exceeds safety ceiling", ErrInvalidConfig)
	}
	maxManifestBytes := options.MaxManifestBytes
	if maxManifestBytes <= 0 {
		maxManifestBytes = DefaultMaxManifestBytes
	}
	if maxManifestBytes > DefaultMaxManifestBytes {
		return nil, fmt.Errorf("%w: manifest bound exceeds safety ceiling", ErrInvalidConfig)
	}
	return &Service{
		store: store, read: read, action: action, download: download, clock: clock,
		workerID: workerID, leaseDuration: lease, retryAfter: retry,
		maxItems: maxItems, maxManifestBytes: maxManifestBytes,
		isDownloadMissing: options.IsDownloadMissing,
	}, nil
}

// NewService is an options-only constructor for composition roots that keep
// the storage handle inside Options.
func NewService(options Options) (*Service, error) { return New(options.Store, options) }

// Trash creates an exact durable entry, stops an associated client when
// necessary, then delegates payload movement to the reviewed filesystem port.
// A partial or uncertain effect is held with item-level evidence.
func (service *Service) Trash(ctx context.Context, request TrashRequest) (Result, error) {
	if err := service.validateContext(ctx); err != nil {
		return Result{}, err
	}
	normalized, entries, leaves, manifestJSON, digest, err := service.normalizeTrashRequest(request)
	if err != nil {
		return Result{}, err
	}
	if normalized.IdempotencyKey != "" {
		if result, found, lookupErr := service.lookupIdempotency(ctx, "trash", normalized.IdempotencyKey, digest); lookupErr != nil {
			return Result{}, lookupErr
		} else if found {
			return result, nil
		}
	}
	entryID := normalized.EntryID
	if entryID == "" {
		id, idErr := domain.NewRuntimeID()
		if idErr != nil {
			return Result{}, idErr
		}
		entryID = id.String()
	}
	if err := validateID(entryID); err != nil {
		return Result{}, err
	}
	if existing, getErr := service.loadEntry(ctx, entryID); getErr == nil {
		if err := service.ensureEntryRequestMatches(existing, normalized, digest); err != nil {
			return Result{}, err
		}
		if existing.State != "planned" {
			return alreadySatisfiedResult(existing, "existing_trash_entry"), nil
		}
		// A planned entry has a durable purge record and can be resumed after a
		// caller crash. Do not create another manifest or idempotency row.
	} else if !errors.Is(getErr, ErrNotFound) {
		return Result{}, getErr
	}
	if err := service.preflightSource(ctx, normalized.Manifest); err != nil {
		return Result{}, err
	}
	now := service.now()
	_, _, err = service.ensurePlannedEntry(ctx, entryID, normalized, entries, leaves, manifestJSON, digest, now)
	if err != nil {
		return Result{}, err
	}
	if normalized.Client != nil {
		clientEffect, observation, stopErr := service.ensureStopped(ctx, *normalized.Client)
		if stopErr != nil {
			if errors.Is(stopErr, ErrDownloadMissing) {
				clientEffect = ports.ClientEffect{Outcome: domain.OutcomeAlreadySatisfied, ObservedAt: now, Evidence: []string{"client_record_absent"}}
				observation = ports.DownloadObservation{Ref: *normalized.Client, State: "missing", ObservedAt: now}
			} else {
				result, holdErr := service.holdPlanned(ctx, entryID, fmt.Sprintf("client_stop: %v", safeError(stopErr)), clientEffect, now)
				return result, errors.Join(stopErr, holdErr)
			}
		}
		if err := service.persistClientObservation(ctx, entryID, observation, clientEffect); err != nil {
			return Result{}, err
		}
	}
	if err := service.validateContext(ctx); err != nil {
		return Result{}, err
	}
	effect, actionErr := service.action.Trash(ctx, ports.FilesystemTrashRequest{Files: normalized.Manifest, Retention: normalized.Retention})
	result, finalizeErr := service.finalizeTrash(ctx, entryID, effect, actionErr)
	if actionErr != nil {
		return result, errors.Join(actionErr, finalizeErr)
	}
	return result, finalizeErr
}

// Restore claims one entry, validates every remaining trash object and exact
// original destination, then restores only the selected objects. It never
// calls a download-client add/resume operation.
func (service *Service) Restore(ctx context.Context, request RestoreRequest) (Result, error) {
	if err := service.validateContext(ctx); err != nil {
		return Result{}, err
	}
	entryID := request.entryID()
	if err := validateID(entryID); err != nil {
		return Result{}, err
	}
	request.EntryID = entryID
	request.ID = ""
	digest := digestRestoreRequest(request)
	if request.IdempotencyKey != "" {
		if result, found, err := service.lookupIdempotency(ctx, "restore", request.IdempotencyKey, digest); err != nil {
			return Result{}, err
		} else if found {
			return result, nil
		}
	}
	entry, err := service.loadEntry(ctx, entryID)
	if err != nil {
		return Result{}, err
	}
	if entry.State == "restored" {
		return alreadySatisfiedResult(entry, "already_restored"), nil
	}
	if entry.State == "purged" {
		return Result{}, fmt.Errorf("%w: entry is purged", ErrConflict)
	}
	if err := service.validateEntryClock(entry); err != nil {
		return service.holdEntry(ctx, entryID, err.Error(), OperationRestore, nil)
	}
	if err := service.ensureJanitorRecord(ctx, entryID, OperationRestore); err != nil {
		return Result{}, err
	}
	claim, err := service.claim(ctx, entryID, OperationRestore, false)
	if err != nil {
		if errors.Is(err, ErrClaimed) {
			latest, loadErr := service.loadEntry(ctx, entryID)
			if loadErr == nil {
				return resultForEntry(latest, "claim_conflict"), err
			}
		}
		return Result{}, err
	}
	result, runErr := service.runRestore(ctx, claim)
	if request.IdempotencyKey != "" {
		runErr = errors.Join(runErr, service.saveIdempotency(ctx, "restore", request.IdempotencyKey, digest, entryID))
	}
	return result, runErr
}

// Purge removes only payload represented by a stored trash manifest. Ordinary
// janitor purges require expiry. HardDelete explicitly bypasses retention but
// remains exact-scope and still performs stop/read-back before payload delete.
func (service *Service) Purge(ctx context.Context, request PurgeRequest) (Result, error) {
	if err := service.validateContext(ctx); err != nil {
		return Result{}, err
	}
	entryID := request.entryID()
	if err := validateID(entryID); err != nil {
		return Result{}, err
	}
	request.EntryID = entryID
	request.ID = ""
	digest := digestPurgeRequest(request)
	if request.IdempotencyKey != "" {
		if result, found, err := service.lookupIdempotency(ctx, "purge", request.IdempotencyKey, digest); err != nil {
			return Result{}, err
		} else if found {
			return result, nil
		}
	}
	entry, err := service.loadEntry(ctx, entryID)
	if err != nil {
		return Result{}, err
	}
	if entry.State == "purged" {
		return alreadySatisfiedResult(entry, "already_purged"), nil
	}
	if entry.State == "restored" {
		return Result{}, fmt.Errorf("%w: entry is restored", ErrConflict)
	}
	if err := service.validateEntryClock(entry); err != nil {
		return service.holdEntry(ctx, entryID, err.Error(), OperationPurge, nil)
	}
	now := service.now()
	hard := request.HardDelete || request.Force
	if !hard && now.Before(entry.ExpiresAt) {
		return resultForEntry(entry, "retention_not_expired"), ErrNotDue
	}
	if err := service.ensureJanitorRecord(ctx, entryID, OperationPurge); err != nil {
		return Result{}, err
	}
	claim, err := service.claim(ctx, entryID, OperationPurge, hard)
	if err != nil {
		return Result{}, err
	}
	result, runErr := service.runPurge(ctx, claim)
	if request.IdempotencyKey != "" {
		runErr = errors.Join(runErr, service.saveIdempotency(ctx, "purge", request.IdempotencyKey, digest, entryID))
	}
	return result, runErr
}

// Tick performs bounded due-janitor work. It never claims a future entry and
// skips held/uncertain records until an explicit Reconcile/Retry call proves
// their exact stored scope safe again.
func (service *Service) Tick(ctx context.Context, limit int) (TickResult, error) {
	if err := service.validateContext(ctx); err != nil {
		return TickResult{}, err
	}
	if limit <= 0 {
		limit = 100
	}
	if limit > service.maxItems {
		limit = service.maxItems
	}
	now := service.now()
	// A periodic tick also performs lease recovery. Recovery changes only
	// durable state and leaves effects for the normal read-before-write path.
	if _, err := service.store.Queries().RecoverExpiredJanitorRecords(ctx, formatTime(now)); err != nil {
		return TickResult{}, fmt.Errorf("%w: recover janitor leases: %v", ErrStorage, err)
	}
	due, err := service.store.Queries().ListDueTrashEntries(ctx, &sqlc.ListDueTrashEntriesParams{Now: formatTime(now), Limit: int64(limit)})
	if err != nil {
		return TickResult{}, fmt.Errorf("%w: list due entries: %v", ErrStorage, err)
	}
	for _, row := range due {
		if err := service.ensureJanitorRecord(ctx, row.ID, OperationPurge); err != nil && !errors.Is(err, ErrConflict) {
			// Keep processing independent entries. Result retains a skipped count;
			// durable absence is never treated as successful purge.
			continue
		}
	}
	records, err := service.store.Queries().ListDueJanitorRecords(ctx, &sqlc.ListDueJanitorRecordsParams{Now: sql.NullString{String: formatTime(now), Valid: true}, Limit: int64(limit)})
	if err != nil {
		return TickResult{}, fmt.Errorf("%w: list due janitor records: %v", ErrStorage, err)
	}
	tick := TickResult{Results: make([]Result, 0, len(records))}
	for _, record := range records {
		if len(tick.Results) >= limit {
			break
		}
		entry, loadErr := service.loadEntry(ctx, record.TrashEntryID)
		if loadErr != nil {
			tick.Skipped++
			continue
		}
		if record.Operation == string(OperationPurge) {
			activeRecovery := entry.State == "purging" && entry.ActiveOperation == OperationPurge
			if (!activeRecovery && entry.State != "trashed") || (!activeRecovery && now.Before(entry.ExpiresAt)) {
				tick.Skipped++
				continue
			}
		} else if record.Operation != string(OperationRestore) {
			tick.Skipped++
			continue
		}
		claim, claimErr := service.claim(ctx, record.TrashEntryID, Operation(record.Operation), false)
		if claimErr != nil {
			tick.Skipped++
			continue
		}
		var result Result
		var opErr error
		if claim.Operation == string(OperationPurge) {
			result, opErr = service.runPurge(ctx, claim)
		} else {
			result, opErr = service.runRestore(ctx, claim)
		}
		tick.Processed++
		if result.Entry.State == "held" || errors.Is(opErr, ErrHeld) {
			tick.Held++
		}
		tick.Results = append(tick.Results, result)
	}
	return tick, nil
}

// Janitor is a descriptive alias used by worker composition roots.
func (service *Service) Janitor(ctx context.Context, limit int) (TickResult, error) {
	return service.Tick(ctx, limit)
}

// Recover transitions abandoned running records into read-only reconciliation.
// It does not dispatch any filesystem/client mutation.
func (service *Service) Recover(ctx context.Context) (int, error) {
	if err := service.validateContext(ctx); err != nil {
		return 0, err
	}
	recovered, err := service.store.Queries().RecoverRunningJanitorRecords(ctx, formatTime(service.now()))
	if err != nil {
		return 0, fmt.Errorf("%w: recover running janitor records: %v", ErrStorage, err)
	}
	return len(recovered), nil
}

// Reconcile performs a read-only check of one operation's remaining targets.
// It never guesses that a missing or replaced object was successfully
// deleted/restored; callers can use Retry only after an unambiguous result.
func (service *Service) Reconcile(ctx context.Context, entryID string, operation Operation) (Result, error) {
	if err := service.validateContext(ctx); err != nil {
		return Result{}, err
	}
	if err := validateID(entryID); err != nil {
		return Result{}, err
	}
	if operation != OperationPurge && operation != OperationRestore {
		return Result{}, fmt.Errorf("%w: operation", ErrInvalidRequest)
	}
	entry, err := service.loadEntry(ctx, entryID)
	if err != nil {
		return Result{}, err
	}
	if err := service.validateEntryClock(entry); err != nil {
		return resultForEntry(entry, "clock_unknown"), err
	}
	effects := make([]ItemEffect, 0, len(entry.Items))
	allSafe := true
	if operation == OperationPurge && entry.Client != nil {
		if service.download == nil {
			allSafe = false
			effects = append(effects, ItemEffect{Operation: operation, State: "unknown", ObservedAt: service.now(), Evidence: []string{"client_control_unavailable"}})
		} else {
			observation, observeErr := service.download.Observe(ctx, *entry.Client)
			switch {
			case observeErr != nil && service.isDownloadMissing != nil && service.isDownloadMissing(observeErr):
				// A removed client record is already satisfied for metadata cleanup.
			case observeErr != nil:
				allSafe = false
				effects = append(effects, ItemEffect{Operation: operation, State: "unknown", ObservedAt: service.now(), Evidence: []string{"client_state_unobservable"}})
			case observation.Ref != *entry.Client:
				allSafe = false
				effects = append(effects, ItemEffect{Operation: operation, State: "held", ObservedAt: observation.ObservedAt, Evidence: []string{"client_reference_changed"}})
			case !isStopped(observation):
				allSafe = false
				effects = append(effects, ItemEffect{Operation: operation, State: "held", ObservedAt: observation.ObservedAt, Evidence: []string{"client_not_stopped"}})
			}
		}
	}
	for _, item := range entry.Items {
		if item.State == "purged" || item.State == "restored" {
			continue
		}
		if item.Type == domain.ManifestDirectory {
			continue
		}
		if operation == OperationPurge {
			observation, statErr := service.read.Stat(ctx, domain.FileTarget{RootID: item.RootID, RelativePath: item.TrashRelativePath})
			if statErr != nil {
				allSafe = false
				effects = append(effects, ItemEffect{ItemID: item.ID, Path: item.TrashRelativePath, Operation: operation, State: "unknown", Outcome: "", ObservedAt: service.now(), Evidence: []string{"trash_source_unobservable"}})
				continue
			}
			if identityError := identityMatches(observation.Entry, identityForItemAt(item, item.TrashRelativePath)); identityError != nil {
				allSafe = false
				effects = append(effects, ItemEffect{ItemID: item.ID, Path: item.TrashRelativePath, Operation: operation, State: "held", ObservedAt: observation.ObservedAt, Evidence: []string{identityError.Error()}})
				continue
			}
			effects = append(effects, ItemEffect{ItemID: item.ID, Path: item.TrashRelativePath, Operation: operation, State: "present", ObservedAt: observation.ObservedAt, Evidence: []string{"trash_source_verified"}})
		} else {
			trashObs, trashErr := service.read.Stat(ctx, domain.FileTarget{RootID: item.RootID, RelativePath: item.TrashRelativePath})
			destinationObs, destinationErr := service.read.Stat(ctx, domain.FileTarget{RootID: item.RootID, RelativePath: item.OriginalRelativePath})
			if trashErr != nil && destinationErr != nil {
				allSafe = false
				effects = append(effects, ItemEffect{ItemID: item.ID, Path: item.OriginalRelativePath, Operation: operation, State: "unknown", ObservedAt: service.now(), Evidence: []string{"restore_source_and_destination_unobservable"}})
				continue
			}
			if trashErr == nil && destinationErr == nil {
				allSafe = false
				effects = append(effects, ItemEffect{ItemID: item.ID, Path: item.OriginalRelativePath, Operation: operation, State: "held", ObservedAt: service.now(), Evidence: []string{"restore_destination_collision"}})
				continue
			}
			if trashErr != nil && !isMissing(trashErr) || destinationErr != nil && !isMissing(destinationErr) {
				allSafe = false
				effects = append(effects, ItemEffect{ItemID: item.ID, Path: item.OriginalRelativePath, Operation: operation, State: "unknown", ObservedAt: service.now(), Evidence: []string{"restore_scope_unobservable"}})
				continue
			}
			if trashErr == nil {
				if identityError := identityMatches(trashObs.Entry, identityForItemAt(item, item.TrashRelativePath)); identityError != nil {
					allSafe = false
					effects = append(effects, ItemEffect{ItemID: item.ID, Path: item.TrashRelativePath, Operation: operation, State: "held", ObservedAt: trashObs.ObservedAt, Evidence: []string{identityError.Error()}})
					continue
				}
				effects = append(effects, ItemEffect{ItemID: item.ID, Path: item.TrashRelativePath, Operation: operation, State: "present", ObservedAt: trashObs.ObservedAt, Evidence: []string{"restore_source_verified", "restore_destination_absent"}})
			} else if destinationErr == nil {
				if identityError := identityMatches(destinationObs.Entry, identityForItem(item)); identityError != nil {
					allSafe = false
					effects = append(effects, ItemEffect{ItemID: item.ID, Path: item.OriginalRelativePath, Operation: operation, State: "held", ObservedAt: destinationObs.ObservedAt, Evidence: []string{identityError.Error()}})
					continue
				}
				effects = append(effects, ItemEffect{ItemID: item.ID, Path: item.OriginalRelativePath, Operation: operation, State: "already_satisfied", Outcome: domain.OutcomeAlreadySatisfied, ObservedAt: destinationObs.ObservedAt, Evidence: []string{"restore_destination_verified", "trash_source_absent"}})
			}
		}
	}
	if !allSafe {
		return Result{Entry: entry, Evidence: []string{"reconciliation_requires_review"}, Effects: effects}, ErrHeld
	}
	return Result{Entry: entry, Outcome: domain.OutcomeAlreadySatisfied, Evidence: []string{"reconciliation_scope_verified"}, Effects: effects, Retryable: true}, nil
}

// Retry requeues a held operation only after Reconcile validates every
// remaining object. It is explicit so janitor never blindly repeats a delete.
func (service *Service) Retry(ctx context.Context, request RetryRequest) (Result, error) {
	entryID := request.EntryID
	if entryID == "" {
		entryID = request.ID
	}
	reconciled, err := service.Reconcile(ctx, entryID, request.Operation)
	if err != nil || !reconciled.Retryable {
		return reconciled, err
	}
	if err := service.requeueHeld(ctx, entryID, request.Operation); err != nil {
		return reconciled, err
	}
	entry, err := service.loadEntry(ctx, entryID)
	if err != nil {
		return Result{}, err
	}
	return resultForEntry(entry, "requeued_after_reconciliation"), nil
}

func (request RestoreRequest) entryID() string {
	if strings.TrimSpace(request.EntryID) != "" {
		return strings.TrimSpace(request.EntryID)
	}
	return strings.TrimSpace(request.ID)
}

func (request PurgeRequest) entryID() string {
	if strings.TrimSpace(request.EntryID) != "" {
		return strings.TrimSpace(request.EntryID)
	}
	return strings.TrimSpace(request.ID)
}

func (service *Service) validateContext(ctx context.Context) error {
	if service == nil || service.store == nil || service.read == nil || service.action == nil {
		return fmt.Errorf("%w: service is not fully configured", ErrInvalidConfig)
	}
	if ctx == nil {
		return fmt.Errorf("%w: context is nil", ErrInvalidRequest)
	}
	return ctx.Err()
}

func (service *Service) now() time.Time {
	value := service.clock()
	if value.IsZero() {
		return time.Time{}
	}
	return value.UTC().Round(0)
}

func (service *Service) normalizeTrashRequest(request TrashRequest) (TrashRequest, []domain.FileManifestEntry, []domain.FileManifestEntry, string, string, error) {
	request.EntryID = strings.TrimSpace(request.EntryID)
	if request.EntryID == "" {
		request.EntryID = strings.TrimSpace(request.ID)
	}
	if !request.RootID.Valid() {
		return TrashRequest{}, nil, nil, "", "", fmt.Errorf("%w: root id", ErrInvalidRequest)
	}
	request.OriginalPrefix = path.Clean(strings.TrimSpace(request.OriginalPrefix))
	request.TrashPrefix = path.Clean(strings.TrimSpace(request.TrashPrefix))
	if err := validateRelativePrefix(request.OriginalPrefix); err != nil {
		return TrashRequest{}, nil, nil, "", "", fmt.Errorf("%w: original prefix: %v", ErrInvalidRequest, err)
	}
	if err := validateRelativePrefix(request.TrashPrefix); err != nil {
		return TrashRequest{}, nil, nil, "", "", fmt.Errorf("%w: trash prefix: %v", ErrInvalidRequest, err)
	}
	retention := request.Retention
	if retention == 0 {
		retention = DefaultRetention
	}
	if retention <= 0 || retention > 10*365*24*time.Hour || retention%time.Second != 0 {
		return TrashRequest{}, nil, nil, "", "", fmt.Errorf("%w: retention", ErrInvalidRequest)
	}
	request.Retention = retention
	request.IdempotencyKey = strings.TrimSpace(request.IdempotencyKey)
	if len(request.IdempotencyKey) > 256 || strings.ContainsRune(request.IdempotencyKey, 0) {
		return TrashRequest{}, nil, nil, "", "", fmt.Errorf("%w: idempotency key", ErrInvalidRequest)
	}
	if request.Client != nil {
		if err := validateDownloadRef(*request.Client); err != nil {
			return TrashRequest{}, nil, nil, "", "", fmt.Errorf("%w: client: %v", ErrInvalidRequest, err)
		}
	}
	if len(request.Manifest) == 0 || len(request.Manifest) > service.maxItems {
		return TrashRequest{}, nil, nil, "", "", fmt.Errorf("%w: manifest item count", ErrInvalidRequest)
	}
	manifest := cloneManifest(request.Manifest)
	if err := validateManifestBytes(manifest, service.maxManifestBytes); err != nil {
		return TrashRequest{}, nil, nil, "", "", fmt.Errorf("%w: manifest: %v", ErrInvalidRequest, err)
	}
	flat := make([]domain.FileManifestEntry, 0, len(manifest))
	for _, entry := range manifest {
		if err := validateManifestRootAndPrefix(entry, request.RootID, request.OriginalPrefix); err != nil {
			return TrashRequest{}, nil, nil, "", "", fmt.Errorf("%w: manifest: %v", ErrInvalidRequest, err)
		}
		if err := flattenManifest(entry, &flat); err != nil {
			return TrashRequest{}, nil, nil, "", "", fmt.Errorf("%w: manifest: %v", ErrInvalidRequest, err)
		}
	}
	if len(flat) > service.maxItems {
		return TrashRequest{}, nil, nil, "", "", fmt.Errorf("%w: flattened manifest item count", ErrInvalidRequest)
	}
	seen := make(map[string]struct{}, len(flat))
	for _, entry := range flat {
		key := string(entry.RootID) + "\x00" + entry.RelativePath
		if _, exists := seen[key]; exists {
			return TrashRequest{}, nil, nil, "", "", fmt.Errorf("%w: duplicate manifest path %q", ErrInvalidRequest, entry.RelativePath)
		}
		seen[key] = struct{}{}
		trashPath := mapTrashPath(request.OriginalPrefix, request.TrashPrefix, entry.RelativePath)
		if err := validateRelativePrefix(trashPath); err != nil {
			return TrashRequest{}, nil, nil, "", "", fmt.Errorf("%w: trash path %q: %v", ErrInvalidRequest, trashPath, err)
		}
		if trashPath == entry.RelativePath || pathWithin(trashPath, entry.RelativePath) || pathWithin(entry.RelativePath, trashPath) {
			return TrashRequest{}, nil, nil, "", "", fmt.Errorf("%w: trash destination overlaps source %q", ErrInvalidRequest, entry.RelativePath)
		}
	}
	manifestJSON, err := json.Marshal(manifest)
	if err != nil {
		return TrashRequest{}, nil, nil, "", "", fmt.Errorf("%w: encode manifest: %v", ErrInvalidRequest, err)
	}
	return request, manifest, flat, string(manifestJSON), digestTrashRequest(request), nil
}

func (service *Service) preflightSource(ctx context.Context, manifest []domain.FileManifestEntry) error {
	flat := make([]domain.FileManifestEntry, 0, len(manifest))
	for _, entry := range manifest {
		if entry.Type == domain.ManifestDirectory {
			if observation, err := service.read.Stat(ctx, domain.FileTarget{RootID: entry.RootID, RelativePath: entry.RelativePath}); err != nil {
				return fmt.Errorf("%w: source %q: %v", ErrConflict, entry.RelativePath, err)
			} else if identityError := identityMatches(observation.Entry, identityForEntry(entry)); identityError != nil {
				return fmt.Errorf("%w: source %q: %v", ErrIdentityChanged, entry.RelativePath, identityError)
			}
		}
		if err := flattenManifest(entry, &flat); err != nil {
			return err
		}
	}
	for _, entry := range flat {
		if entry.Type == domain.ManifestDirectory {
			continue
		}
		observation, err := service.read.Stat(ctx, domain.FileTarget{RootID: entry.RootID, RelativePath: entry.RelativePath})
		if err != nil {
			return fmt.Errorf("%w: source %q: %v", ErrConflict, entry.RelativePath, err)
		}
		if identityError := identityMatches(observation.Entry, identityForEntry(entry)); identityError != nil {
			return fmt.Errorf("%w: source %q: %v", ErrIdentityChanged, entry.RelativePath, identityError)
		}
	}
	return nil
}

func (service *Service) ensurePlannedEntry(ctx context.Context, entryID string, request TrashRequest, manifest, flat []domain.FileManifestEntry, manifestJSON, digest string, now time.Time) (Entry, bool, error) {
	if now.IsZero() {
		return Entry{}, false, ErrClock
	}
	clientState := map[string]any{}
	if request.Client != nil {
		clientState["connectionId"] = request.Client.ConnectionID
		clientState["externalId"] = request.Client.ExternalID
	}
	clientStateBytes, err := json.Marshal(clientState)
	if err != nil {
		return Entry{}, false, err
	}
	lock := service.lockFor("entry:" + entryID)
	defer lock()
	if existing, getErr := service.loadEntry(ctx, entryID); getErr == nil {
		return existing, false, nil
	} else if !errors.Is(getErr, ErrNotFound) {
		return Entry{}, false, getErr
	}
	createdAt := formatTime(now)
	expiresAt := formatTime(now.Add(request.Retention))
	if err := service.withTx(ctx, func(tx *sql.Tx, queries *sqlc.Queries) error {
		if _, err := queries.CreateTrashEntry(ctx, &sqlc.CreateTrashEntryParams{
			ID: entryID, RootID: request.RootID.String(), State: "planned",
			OriginalPrefix: request.OriginalPrefix, TrashPrefix: request.TrashPrefix,
			ManifestJson: manifestJSON, RetentionSeconds: int64(request.Retention / time.Second),
			ExpiresAt: expiresAt, ClientStateJson: string(clientStateBytes),
			CreatedAt: createdAt, UpdatedAt: createdAt,
		}); err != nil {
			return err
		}
		for _, item := range flat {
			itemID, idErr := domain.NewRuntimeID()
			if idErr != nil {
				return idErr
			}
			clientConnectionID := sql.NullString{}
			clientExternalID := sql.NullString{}
			if request.Client != nil {
				clientConnectionID = sql.NullString{String: request.Client.ConnectionID.String(), Valid: true}
				clientExternalID = sql.NullString{String: request.Client.ExternalID, Valid: true}
			}
			if _, err := queries.CreateTrashItem(ctx, &sqlc.CreateTrashItemParams{
				ID: itemID.String(), EntryID: entryID, RootID: item.RootID.String(),
				OriginalRelativePath: item.RelativePath,
				TrashRelativePath:    mapTrashPath(request.OriginalPrefix, request.TrashPrefix, item.RelativePath),
				EntryType:            string(item.Type), SizeBytes: item.Size,
				Digest: nullable(item.Digest), FileIdentity: nullable(item.FileIdentity),
				ClientConnectionID: clientConnectionID, ClientExternalID: clientExternalID,
				State: "selected",
			}); err != nil {
				return err
			}
		}
		janitorID, idErr := domain.NewRuntimeID()
		if idErr != nil {
			return idErr
		}
		if _, err := queries.CreateJanitorRecord(ctx, &sqlc.CreateJanitorRecordParams{
			ID: janitorID.String(), TrashEntryID: entryID, Operation: string(OperationPurge),
			State: "queued", OutcomeJson: `{"operation":"purge","state":"queued","requestDigest":"` + digest + `"}`,
			CreatedAt: createdAt, UpdatedAt: createdAt,
		}); err != nil {
			return err
		}
		if request.IdempotencyKey != "" {
			response, marshalErr := json.Marshal(map[string]string{"entryId": entryID})
			if marshalErr != nil {
				return marshalErr
			}
			if _, err := queries.CreateIdempotencyRecord(ctx, &sqlc.CreateIdempotencyRecordParams{
				Scope: "trash", IdempotencyKey: request.IdempotencyKey, RequestDigest: digest,
				StatusCode: 202, ResourceKind: "trash_entry", ResourceID: entryID,
				ResponseJson: string(response), CreatedAt: createdAt,
			}); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		if existing, getErr := service.loadEntry(ctx, entryID); getErr == nil {
			return existing, false, nil
		}
		return Entry{}, false, fmt.Errorf("%w: create planned entry: %v", ErrStorage, err)
	}
	return Entry{ID: entryID}, true, nil
}

func (service *Service) finalizeTrash(ctx context.Context, entryID string, effect ports.FilesystemEffect, actionErr error) (Result, error) {
	// An action may return an effect together with context cancellation. Read
	// the durable plan with the persistence context so that evidence can still
	// be journalled even though no later external call is allowed.
	entry, err := service.loadEntry(persistenceContext(ctx), entryID)
	if err != nil {
		return Result{}, err
	}
	affected := affectedPaths(effect.Affected)
	matched := make(map[string]bool)
	for _, item := range entry.Items {
		if item.State != "selected" {
			continue
		}
		observed, ok := exactAffectedItem(item, affected)
		if ok && identityMatches(observed, identityForItemAt(item, observed.RelativePath)) == nil {
			matched[item.ID] = true
		}
	}
	all := true
	for _, item := range entry.Items {
		if item.Type == domain.ManifestDirectory {
			continue
		}
		if item.State == "selected" && !matched[item.ID] {
			all = false
		}
	}
	if actionErr != nil || !effect.Outcome.Valid() || len(affected) == 0 || !all {
		reason := "filesystem_trash_incomplete"
		if actionErr != nil {
			reason = "filesystem_trash: " + safeError(actionErr)
		} else if !effect.Outcome.Valid() {
			reason = "filesystem_trash_invalid_effect"
		} else if len(affected) == 0 {
			reason = "filesystem_trash_missing_affected_items"
		}
		result, holdErr := service.finishTrashState(ctx, entry, matched, "held", reason, effect)
		return result, errors.Join(ErrHeld, holdErr)
	}
	result, finishErr := service.finishTrashState(ctx, entry, matched, "trashed", "", effect)
	return result, finishErr
}

func (service *Service) finishTrashState(ctx context.Context, entry Entry, matched map[string]bool, state, reason string, effect ports.FilesystemEffect) (Result, error) {
	now := service.now()
	if now.IsZero() {
		return Result{}, ErrClock
	}
	resultErr := service.withTx(ctx, func(tx *sql.Tx, queries *sqlc.Queries) error {
		for _, item := range entry.Items {
			if matched[item.ID] {
				if _, err := tx.ExecContext(ctx, `UPDATE trash_items SET state = 'trashed', trashed_at = ? WHERE id = ? AND entry_id = ? AND state = 'selected'`, formatTime(now), item.ID, entry.ID); err != nil {
					return err
				}
			}
		}
		clientState := effectState(entry, effect)
		if state == "trashed" {
			updated, err := tx.ExecContext(ctx, `UPDATE trash_entries SET expires_at = ?, version = version + 1, updated_at = ? WHERE id = ? AND version = ?`, formatTime(now.Add(entry.Retention)), formatTime(now), entry.ID, entry.Version)
			if err != nil {
				return err
			}
			if affected, err := updated.RowsAffected(); err != nil {
				return err
			} else if affected != 1 {
				return ErrClaimed
			}
			entry.Version++
		}
		updated, err := queries.UpdateTrashEntryState(ctx, &sqlc.UpdateTrashEntryStateParams{
			State: state, TrashedAt: nullableTime(state == "trashed", now), HoldReason: nullable(reason),
			ClientStateJson: clientState, PurgeClaimedAt: nullableTime(false, time.Time{}), RestoreRequestedAt: nullableTime(false, time.Time{}),
			UpdatedAt: formatTime(now), ID: entry.ID, Version: entry.Version,
		})
		if err != nil {
			return err
		}
		if updated == nil {
			return ErrClaimed
		}
		if state == "held" {
			janitor, janitorErr := queries.GetJanitorRecord(ctx, &sqlc.GetJanitorRecordParams{TrashEntryID: entry.ID, Operation: string(OperationPurge)})
			if janitorErr == nil && janitor.State == "queued" {
				if _, err := queries.UpdateJanitorRecord(ctx, &sqlc.UpdateJanitorRecordParams{State: "held", OutcomeJson: outcomeJSON(OperationPurge, "held", reason, effect.Evidence, nil), UpdatedAt: formatTime(now), ID: janitor.ID, Version: janitor.Version}); err != nil {
					return err
				}
			}
		}
		return nil
	})
	latest, loadErr := service.loadEntry(persistenceContext(ctx), entry.ID)
	if loadErr != nil {
		return Result{}, errors.Join(resultErr, loadErr)
	}
	result := resultForEffect(latest, OperationPurge, effect, matched)
	return result, resultErr
}

func (service *Service) holdPlanned(ctx context.Context, entryID, reason string, effect ports.ClientEffect, now time.Time) (Result, error) {
	entry, err := service.loadEntry(persistenceContext(ctx), entryID)
	if err != nil {
		return Result{}, err
	}
	resultErr := service.withTx(ctx, func(tx *sql.Tx, queries *sqlc.Queries) error {
		stateJSON := map[string]any{}
		if raw := entryClientState(entry); raw != "{}" {
			_ = json.Unmarshal([]byte(raw), &stateJSON)
		}
		stateJSON["clientStop"] = map[string]any{"outcome": effect.Outcome, "operationId": effect.OperationID, "evidence": effect.Evidence}
		encoded, _ := json.Marshal(stateJSON)
		updated, err := queries.UpdateTrashEntryState(ctx, &sqlc.UpdateTrashEntryStateParams{State: "held", HoldReason: nullable(reason), ClientStateJson: string(encoded), UpdatedAt: formatTime(now), ID: entry.ID, Version: entry.Version})
		if err != nil {
			return err
		}
		if updated == nil {
			return ErrClaimed
		}
		janitor, janitorErr := queries.GetJanitorRecord(ctx, &sqlc.GetJanitorRecordParams{TrashEntryID: entry.ID, Operation: string(OperationPurge)})
		if janitorErr == nil && janitor.State == "queued" {
			_, err = queries.UpdateJanitorRecord(ctx, &sqlc.UpdateJanitorRecordParams{State: "held", OutcomeJson: outcomeJSON(OperationPurge, "held", reason, effect.Evidence, nil), UpdatedAt: formatTime(now), ID: janitor.ID, Version: janitor.Version})
		}
		return err
	})
	latest, loadErr := service.loadEntry(persistenceContext(ctx), entryID)
	if loadErr != nil {
		return Result{}, errors.Join(resultErr, loadErr)
	}
	return Result{Entry: latest, Evidence: []string{"client_stop_unresolved"}}, resultErr
}

func (service *Service) persistClientObservation(ctx context.Context, entryID string, observation ports.DownloadObservation, effect ports.ClientEffect) error {
	entry, err := service.loadEntry(persistenceContext(ctx), entryID)
	if err != nil {
		return err
	}
	state := map[string]any{"state": observation.State, "seeding": observation.Seeding, "observedAt": formatTime(observation.ObservedAt), "stop": map[string]any{"outcome": effect.Outcome, "operationId": effect.OperationID, "evidence": effect.Evidence}}
	if entry.Client != nil {
		state["connectionId"] = entry.Client.ConnectionID.String()
		state["externalId"] = entry.Client.ExternalID
	}
	encoded, _ := json.Marshal(state)
	return service.withTx(ctx, func(tx *sql.Tx, queries *sqlc.Queries) error {
		_, err := queries.UpdateTrashEntryState(ctx, &sqlc.UpdateTrashEntryStateParams{State: entry.State, TrashedAt: nullableTime(entry.TrashedAt != nil, derefTime(entry.TrashedAt)), HoldReason: nullable(entry.HoldReason), ClientStateJson: string(encoded), PurgeClaimedAt: nullableTime(false, time.Time{}), RestoreRequestedAt: nullableTime(false, time.Time{}), ActiveOperation: nullable(string(entry.ActiveOperation)), OperationClaimedBy: nullable(entry.ClaimedBy), OperationLeaseUntil: nullableTime(entry.LeaseUntil != nil, derefTime(entry.LeaseUntil)), UpdatedAt: formatTime(service.now()), ID: entry.ID, Version: entry.Version})
		return err
	})
}

func (service *Service) ensureStopped(ctx context.Context, ref ports.DownloadRef) (ports.ClientEffect, ports.DownloadObservation, error) {
	if service.download == nil {
		return ports.ClientEffect{}, ports.DownloadObservation{}, fmt.Errorf("%w: download control port", ErrDependency)
	}
	initial, err := service.download.Observe(ctx, ref)
	if err != nil {
		if service.isDownloadMissing != nil && service.isDownloadMissing(err) {
			return ports.ClientEffect{}, ports.DownloadObservation{}, fmt.Errorf("%w: %s", ErrDownloadMissing, safeError(err))
		}
		return ports.ClientEffect{}, ports.DownloadObservation{}, fmt.Errorf("%w: observe download: %v", ErrDependency, safeError(err))
	}
	if initial.Ref != ref {
		return ports.ClientEffect{}, initial, fmt.Errorf("%w: download observation reference differs", ErrConflict)
	}
	if isStopped(initial) {
		return ports.ClientEffect{Outcome: domain.OutcomeAlreadySatisfied, ObservedAt: initial.ObservedAt, Evidence: []string{"client_already_stopped"}}, initial, nil
	}
	effect, stopErr := service.download.Stop(ctx, ref)
	if stopErr != nil {
		return effect, initial, fmt.Errorf("%w: stop download: %v", ErrUncertain, safeError(stopErr))
	}
	if !effect.Outcome.Valid() {
		return effect, initial, fmt.Errorf("%w: stop returned invalid effect", ErrUncertain)
	}
	final, observeErr := service.download.Observe(ctx, ref)
	if observeErr != nil {
		return effect, initial, fmt.Errorf("%w: stop read-back: %v", ErrUncertain, safeError(observeErr))
	}
	if final.Ref != ref || !isStopped(final) {
		return effect, final, ErrClientActive
	}
	return effect, final, nil
}

func (service *Service) claim(ctx context.Context, entryID string, operation Operation, hard bool) (*sqlc.JanitorRecord, error) {
	if operation != OperationPurge && operation != OperationRestore {
		return nil, fmt.Errorf("%w: operation", ErrInvalidRequest)
	}
	now := service.now()
	leaseUntil := now.Add(service.leaseDuration)
	lock := service.lockFor("claim:" + entryID)
	defer lock()
	if hard && operation == OperationPurge {
		var claimed *sqlc.JanitorRecord
		err := service.withTx(ctx, func(tx *sql.Tx, queries *sqlc.Queries) error {
			janitor, err := queries.GetJanitorRecord(ctx, &sqlc.GetJanitorRecordParams{TrashEntryID: entryID, Operation: string(operation)})
			if err != nil {
				return err
			}
			if janitor.ApprovalPlanID.Valid || (janitor.State != "queued" && janitor.State != "reconciling") {
				return ErrClaimed
			}
			entry, err := queries.GetTrashEntry(ctx, entryID)
			if err != nil {
				return err
			}
			if entry.ActiveOperation.Valid || (entry.State != "trashed" && entry.State != "held") {
				return ErrClaimed
			}
			result, err := tx.ExecContext(ctx, `UPDATE trash_entries SET state = 'purging', active_operation = 'purge', operation_claimed_by = ?, operation_lease_until = ?, version = version + 1, updated_at = ? WHERE id = ? AND version = ? AND active_operation IS NULL AND state IN ('trashed','held')`, service.workerID, formatTime(leaseUntil), formatTime(now), entryID, entry.Version)
			if err != nil {
				return err
			}
			if affected, err := result.RowsAffected(); err != nil {
				return err
			} else if affected != 1 {
				return ErrClaimed
			}
			result, err = tx.ExecContext(ctx, `UPDATE janitor_records SET state = 'running', claimed_by = ?, lease_until = ?, version = version + 1, updated_at = ? WHERE id = ? AND version = ? AND state = 'queued' AND claimed_by IS NULL`, service.workerID, formatTime(leaseUntil), formatTime(now), janitor.ID, janitor.Version)
			if err != nil {
				return err
			}
			if affected, err := result.RowsAffected(); err != nil {
				return err
			} else if affected != 1 {
				return ErrClaimed
			}
			claimed, err = queries.GetJanitorRecord(ctx, &sqlc.GetJanitorRecordParams{TrashEntryID: entryID, Operation: string(operation)})
			return err
		})
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return nil, ErrClaimed
			}
			return nil, fmt.Errorf("%w: hard claim: %v", ErrStorage, err)
		}
		return claimed, nil
	}
	var claimed *sqlc.JanitorRecord
	err := service.withTx(ctx, func(tx *sql.Tx, queries *sqlc.Queries) error {
		janitor, err := queries.GetJanitorRecord(ctx, &sqlc.GetJanitorRecordParams{TrashEntryID: entryID, Operation: string(operation)})
		if err != nil {
			return err
		}
		claimed, err = queries.ClaimJanitorRecord(ctx, &sqlc.ClaimJanitorRecordParams{WorkerID: sql.NullString{String: service.workerID, Valid: true}, LeaseUntil: sql.NullString{String: formatTime(leaseUntil), Valid: true}, Now: formatTime(now), ID: janitor.ID, Version: janitor.Version})
		return err
	})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) || strings.Contains(err.Error(), "already claimed") || strings.Contains(err.Error(), "not eligible") {
			return nil, ErrClaimed
		}
		return nil, fmt.Errorf("%w: claim: %v", ErrStorage, err)
	}
	return claimed, nil
}

func (service *Service) runPurge(ctx context.Context, claim *sqlc.JanitorRecord) (Result, error) {
	entry, err := service.loadEntry(ctx, claim.TrashEntryID)
	if err != nil {
		return Result{}, err
	}
	if entry.State != "purging" || entry.ActiveOperation != OperationPurge || entry.ClaimedBy != service.workerID {
		return Result{}, fmt.Errorf("%w: purge lease is no longer owned", ErrClaimed)
	}
	clientMissing := false
	if entry.Client != nil {
		effect, _, stopErr := service.ensureStopped(ctx, *entry.Client)
		if stopErr != nil {
			if errors.Is(stopErr, ErrDownloadMissing) {
				clientMissing = true
			} else {
				return service.finishClaim(ctx, claim, entry, OperationPurge, "held", "client_stop: "+safeError(stopErr), nil, effect)
			}
		}
	}
	effects := make([]ItemEffect, 0, len(entry.Items))
	matched := map[string]bool{}
	for _, item := range entry.Items {
		if item.State == "purged" || item.Type == domain.ManifestDirectory {
			continue
		}
		if err := service.validateContext(ctx); err != nil {
			return service.finishClaim(ctx, claim, entry, OperationPurge, "reconciling", "purge cancelled before dispatch", effects, ports.FilesystemEffect{})
		}
		if entry.Client != nil && !clientMissing {
			stopEffect, _, stopErr := service.ensureStopped(ctx, *entry.Client)
			if stopErr != nil {
				if errors.Is(stopErr, ErrDownloadMissing) {
					clientMissing = true
				} else {
					return service.finishClaim(ctx, claim, entry, OperationPurge, "held", "client_stop: "+safeError(stopErr), effects, stopEffect)
				}
			}
		}
		observation, statErr := service.read.Stat(ctx, domain.FileTarget{RootID: item.RootID, RelativePath: item.TrashRelativePath})
		if statErr != nil {
			return service.finishClaim(ctx, claim, entry, OperationPurge, "held", "trash identity unavailable: "+safeError(statErr), effects, ports.FilesystemEffect{})
		}
		if identityErr := identityMatches(observation.Entry, identityForItemAt(item, item.TrashRelativePath)); identityErr != nil {
			return service.finishClaim(ctx, claim, entry, OperationPurge, "held", identityErr.Error(), effects, ports.FilesystemEffect{})
		}
		expected := manifestFromItem(item, observation.ObservedAt)
		effect, actionErr := service.action.Delete(ctx, ports.FilesystemDeleteRequest{Files: []domain.FileManifestEntry{expected}})
		matchedThis := false
		if reported, ok := exactAffectedItem(item, affectedPaths(effect.Affected)); ok {
			matchedThis = identityMatches(reported, identityForItemAt(item, reported.RelativePath)) == nil
		}
		if matchedThis {
			matched[item.ID] = true
			effects = append(effects, ItemEffect{ItemID: item.ID, Path: item.TrashRelativePath, Operation: OperationPurge, State: "purged", Outcome: effect.Outcome, ObservedAt: effect.ObservedAt, Evidence: append([]string{"payload_delete"}, effect.Evidence...)})
		} else if len(effect.Affected) > 0 {
			effects = append(effects, ItemEffect{ItemID: item.ID, Path: item.TrashRelativePath, Operation: OperationPurge, State: "unknown", Outcome: effect.Outcome, ObservedAt: effect.ObservedAt, Evidence: append([]string{"payload_delete_unmatched"}, effect.Evidence...)})
		}
		if actionErr != nil || !matchedThis || !effect.Outcome.Valid() {
			reason := "payload_delete_incomplete"
			if actionErr != nil {
				reason = "payload_delete: " + safeError(actionErr)
			}
			return service.finishClaim(ctx, claim, entry, OperationPurge, "held", reason, effects, effect)
		}
	}
	if entry.Client != nil && !clientMissing {
		// Remove is metadata-only by ports.DownloadControlPort contract. It is
		// intentionally after every selected payload deletion and never uses a
		// deleteFiles flag or re-add/resume fallback.
		effect, removeErr := service.download.Remove(ctx, *entry.Client)
		if removeErr != nil || !effect.Outcome.Valid() {
			reason := "client_remove_unresolved"
			if removeErr != nil {
				reason += ": " + safeError(removeErr)
			}
			effects = append(effects, ItemEffect{Path: entry.Client.ExternalID, Operation: OperationPurge, State: "metadata_unknown", Outcome: effect.Outcome, ObservedAt: effect.ObservedAt, Evidence: append([]string{"client_metadata_uncertain"}, effect.Evidence...)})
			return service.finishClaim(ctx, claim, entry, OperationPurge, "held", reason, effects, effect)
		}
		effects = append(effects, ItemEffect{Path: entry.Client.ExternalID, Operation: OperationPurge, State: "metadata_removed", Outcome: effect.Outcome, ObservedAt: effect.ObservedAt, Evidence: append([]string{"client_metadata_only"}, effect.Evidence...)})
	}
	return service.finishClaim(ctx, claim, entry, OperationPurge, "succeeded", "purge_completed", effects, ports.FilesystemEffect{Outcome: domain.OutcomeApplied})
}

func (service *Service) runRestore(ctx context.Context, claim *sqlc.JanitorRecord) (Result, error) {
	entry, err := service.loadEntry(ctx, claim.TrashEntryID)
	if err != nil {
		return Result{}, err
	}
	if entry.State != "restoring" || entry.ActiveOperation != OperationRestore || entry.ClaimedBy != service.workerID {
		return Result{}, fmt.Errorf("%w: restore lease is no longer owned", ErrClaimed)
	}
	effects := make([]ItemEffect, 0, len(entry.Items))
	for _, item := range entry.Items {
		if item.State == "restored" || item.State == "purged" || item.Type == domain.ManifestDirectory {
			continue
		}
		if err := service.validateContext(ctx); err != nil {
			return service.finishClaim(ctx, claim, entry, OperationRestore, "reconciling", "restore cancelled before dispatch", effects, ports.FilesystemEffect{})
		}
		source, sourceErr := service.read.Stat(ctx, domain.FileTarget{RootID: item.RootID, RelativePath: item.TrashRelativePath})
		destination, destinationErr := service.read.Stat(ctx, domain.FileTarget{RootID: item.RootID, RelativePath: item.OriginalRelativePath})
		if sourceErr != nil && !isMissing(sourceErr) || destinationErr != nil && !isMissing(destinationErr) {
			return service.finishClaim(ctx, claim, entry, OperationRestore, "held", "restore scope unavailable", effects, ports.FilesystemEffect{})
		}
		if sourceErr != nil {
			if destinationErr == nil && identityMatches(destination.Entry, identityForItem(item)) == nil {
				effects = append(effects, ItemEffect{ItemID: item.ID, Path: item.OriginalRelativePath, Operation: OperationRestore, State: "restored", Outcome: domain.OutcomeAlreadySatisfied, ObservedAt: destination.ObservedAt, Evidence: []string{"destination_read_back", "trash_source_absent"}})
				if err := service.markItem(ctx, item, "restored", destination.ObservedAt); err != nil {
					return Result{}, err
				}
				continue
			}
			return service.finishClaim(ctx, claim, entry, OperationRestore, "held", "restore source unavailable: "+safeError(sourceErr), effects, ports.FilesystemEffect{})
		}
		if identityErr := identityMatches(source.Entry, identityForItemAt(item, item.TrashRelativePath)); identityErr != nil {
			return service.finishClaim(ctx, claim, entry, OperationRestore, "held", identityErr.Error(), effects, ports.FilesystemEffect{})
		}
		if destinationErr == nil {
			return service.finishClaim(ctx, claim, entry, OperationRestore, "held", ErrDestinationConflict.Error(), effects, ports.FilesystemEffect{})
		}
		mapping := ports.FileMap{Source: manifestFromItem(item, source.ObservedAt), Destination: domain.FileTarget{RootID: item.RootID, RelativePath: item.OriginalRelativePath}}
		effect, actionErr := service.action.Restore(ctx, ports.FilesystemRestoreRequest{Files: []ports.FileMap{mapping}})
		matched := mappingEffectMatches(item, effect.Affected)
		if actionErr != nil || !matched || !effect.Outcome.Valid() {
			reason := "restore_incomplete"
			if actionErr != nil {
				reason = "restore: " + safeError(actionErr)
			}
			if len(effect.Affected) > 0 {
				effects = append(effects, ItemEffect{ItemID: item.ID, Path: item.OriginalRelativePath, Operation: OperationRestore, State: "unknown", Outcome: effect.Outcome, ObservedAt: effect.ObservedAt, Evidence: append([]string{"restore_effect_uncertain"}, effect.Evidence...)})
			}
			return service.finishClaim(ctx, claim, entry, OperationRestore, "held", reason, effects, effect)
		}
		effects = append(effects, ItemEffect{ItemID: item.ID, Path: item.OriginalRelativePath, Operation: OperationRestore, State: "restored", Outcome: effect.Outcome, ObservedAt: effect.ObservedAt, Evidence: append([]string{"restore_read_back"}, effect.Evidence...)})
		if err := service.markItem(ctx, item, "restored", effect.ObservedAt); err != nil {
			return Result{}, err
		}
	}
	return service.finishClaim(ctx, claim, entry, OperationRestore, "succeeded", "restore_completed", effects, ports.FilesystemEffect{Outcome: domain.OutcomeApplied})
}

func (service *Service) finishClaim(ctx context.Context, claim *sqlc.JanitorRecord, entry Entry, operation Operation, state, reason string, effects []ItemEffect, effect any) (Result, error) {
	now := service.now()
	if now.IsZero() {
		return Result{}, ErrClock
	}
	resultErr := service.withTx(ctx, func(tx *sql.Tx, queries *sqlc.Queries) error {
		for _, itemEffect := range effects {
			if itemEffect.ItemID == "" || (itemEffect.State != "purged" && itemEffect.State != "restored") {
				continue
			}
			column := "purged_at"
			if itemEffect.State == "restored" {
				column = "restored_at"
			}
			_, err := tx.ExecContext(ctx, `UPDATE trash_items SET state = ?, `+column+` = ? WHERE id = ? AND entry_id = ? AND state NOT IN ('purged','restored')`, itemEffect.State, formatTime(now), itemEffect.ItemID, entry.ID)
			if err != nil {
				return err
			}
		}
		outcome := effectOutcomeJSON(operation, state, reason, effects, effect)
		updated, err := queries.UpdateJanitorRecord(ctx, &sqlc.UpdateJanitorRecordParams{State: state, NextAttemptAt: nullableTime(state == "reconciling", now.Add(service.retryAfter)), ClaimedBy: sql.NullString{}, LeaseUntil: sql.NullString{}, OutcomeJson: outcome, UpdatedAt: formatTime(now), ID: claim.ID, Version: claim.Version})
		if err != nil {
			return err
		}
		if updated == nil {
			return ErrClaimed
		}
		return nil
	})
	latest, loadErr := service.loadEntry(persistenceContext(ctx), entry.ID)
	if loadErr != nil {
		return Result{}, errors.Join(resultErr, loadErr)
	}
	result := Result{Entry: latest, Effects: append([]ItemEffect(nil), effects...), Evidence: []string{reason}}
	if state == "succeeded" {
		result.Outcome = domain.OutcomeApplied
	} else if state == "reconciling" {
		result.Retryable = false
	} else if state == "held" {
		result.Evidence = append(result.Evidence, "operation_held")
	}
	if resultErr != nil {
		return result, errors.Join(ErrUncertain, resultErr)
	}
	if state == "held" {
		return result, ErrHeld
	}
	return result, nil
}

func (service *Service) markItem(ctx context.Context, item Item, state string, at time.Time) error {
	if state != "restored" && state != "purged" {
		return ErrInvalidRequest
	}
	column := "purged_at"
	if state == "restored" {
		column = "restored_at"
	}
	_, err := service.store.DB().ExecContext(ctx, `UPDATE trash_items SET state = ?, `+column+` = ? WHERE id = ? AND entry_id = ? AND state NOT IN ('purged','restored')`, state, formatTime(at), item.ID, item.EntryID)
	return err
}

func (service *Service) ensureJanitorRecord(ctx context.Context, entryID string, operation Operation) error {
	if operation != OperationPurge && operation != OperationRestore {
		return fmt.Errorf("%w: operation", ErrInvalidRequest)
	}
	if _, err := service.loadEntry(ctx, entryID); err != nil {
		return err
	}
	if operation == OperationPurge {
		if record, getErr := service.store.Queries().GetJanitorRecord(ctx, &sqlc.GetJanitorRecordParams{TrashEntryID: entryID, Operation: string(operation)}); getErr == nil {
			_ = record
			return nil
		} else if !errors.Is(getErr, sql.ErrNoRows) {
			return fmt.Errorf("%w: read janitor record: %v", ErrStorage, getErr)
		}
	}
	now := service.now()
	janitorID, err := domain.NewRuntimeID()
	if err != nil {
		return err
	}
	return service.withTx(ctx, func(tx *sql.Tx, queries *sqlc.Queries) error {
		if _, err := queries.GetJanitorRecord(ctx, &sqlc.GetJanitorRecordParams{TrashEntryID: entryID, Operation: string(operation)}); err == nil {
			return nil
		} else if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		_, err := queries.CreateJanitorRecord(ctx, &sqlc.CreateJanitorRecordParams{ID: janitorID.String(), TrashEntryID: entryID, Operation: string(operation), State: "queued", OutcomeJson: outcomeJSON(operation, "queued", "", nil, nil), CreatedAt: formatTime(now), UpdatedAt: formatTime(now)})
		return err
	})
}

func (service *Service) requeueHeld(ctx context.Context, entryID string, operation Operation) error {
	now := service.now()
	return service.withTx(ctx, func(tx *sql.Tx, queries *sqlc.Queries) error {
		entry, err := queries.GetTrashEntry(ctx, entryID)
		if err != nil {
			return err
		}
		if entry.ActiveOperation.Valid {
			return ErrClaimed
		}
		if operation == OperationPurge && entry.State != "held" {
			return ErrConflict
		}
		if operation == OperationRestore && entry.State != "held" {
			return ErrConflict
		}
		if _, err := tx.ExecContext(ctx, `UPDATE trash_entries SET state = CASE WHEN ? = 'purge' THEN 'trashed' ELSE 'held' END, hold_reason = NULL, updated_at = ?, version = version + 1 WHERE id = ? AND version = ? AND active_operation IS NULL`, string(operation), formatTime(now), entryID, entry.Version); err != nil {
			return err
		}
		janitor, err := queries.GetJanitorRecord(ctx, &sqlc.GetJanitorRecordParams{TrashEntryID: entryID, Operation: string(operation)})
		if err != nil {
			return err
		}
		if janitor.State != "held" {
			return ErrConflict
		}
		_, err = tx.ExecContext(ctx, `UPDATE janitor_records SET state = 'queued', next_attempt_at = NULL, claimed_by = NULL, lease_until = NULL, version = version + 1, updated_at = ? WHERE id = ? AND version = ? AND state = 'held'`, formatTime(now), janitor.ID, janitor.Version)
		return err
	})
}

func (service *Service) holdEntry(ctx context.Context, entryID, reason string, operation Operation, effect any) (Result, error) {
	entry, err := service.loadEntry(persistenceContext(ctx), entryID)
	if err != nil {
		return Result{}, err
	}
	now := service.now()
	resultErr := service.withTx(ctx, func(tx *sql.Tx, queries *sqlc.Queries) error {
		janitor, getErr := queries.GetJanitorRecord(ctx, &sqlc.GetJanitorRecordParams{TrashEntryID: entryID, Operation: string(operation)})
		if getErr == nil && (janitor.State == "running" || janitor.State == "reconciling") {
			_, err := queries.UpdateJanitorRecord(ctx, &sqlc.UpdateJanitorRecordParams{State: "held", OutcomeJson: outcomeJSON(operation, "held", reason, nil, effect), UpdatedAt: formatTime(now), ID: janitor.ID, Version: janitor.Version})
			return err
		}
		_, err := queries.UpdateTrashEntryState(ctx, &sqlc.UpdateTrashEntryStateParams{State: "held", HoldReason: nullable(reason), ClientStateJson: entryClientState(entry), UpdatedAt: formatTime(now), ID: entry.ID, Version: entry.Version})
		return err
	})
	latest, loadErr := service.loadEntry(persistenceContext(ctx), entryID)
	if loadErr != nil {
		return Result{}, errors.Join(resultErr, loadErr)
	}
	return Result{Entry: latest, Evidence: []string{reason}}, errors.Join(ErrHeld, resultErr)
}

func (service *Service) validateEntryClock(entry Entry) error {
	now := service.now()
	if now.IsZero() || entry.ExpiresAt.IsZero() {
		return ErrClock
	}
	if entry.TrashedAt == nil || entry.TrashedAt.IsZero() || entry.ExpiresAt.Before(*entry.TrashedAt) || entry.ExpiresAt.Equal(*entry.TrashedAt) {
		return ErrClock
	}
	if now.Before(*entry.TrashedAt) {
		return ErrClock
	}
	return nil
}

func (service *Service) loadEntry(ctx context.Context, id string) (Entry, error) {
	if err := validateID(id); err != nil {
		return Entry{}, err
	}
	row, err := service.store.Queries().GetTrashEntry(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return Entry{}, fmt.Errorf("%w: %s", ErrNotFound, id)
	}
	if err != nil {
		return Entry{}, fmt.Errorf("%w: get entry: %v", ErrStorage, err)
	}
	items, err := service.store.Queries().ListTrashItems(ctx, id)
	if err != nil {
		return Entry{}, fmt.Errorf("%w: list items: %v", ErrStorage, err)
	}
	var manifest []domain.FileManifestEntry
	if len(row.ManifestJson) == 0 || len(row.ManifestJson) > service.maxManifestBytes || json.Unmarshal([]byte(row.ManifestJson), &manifest) != nil || len(manifest) == 0 {
		return Entry{}, fmt.Errorf("%w: manifest", ErrCorrupt)
	}
	for _, entry := range manifest {
		if err := entry.Validate(); err != nil {
			return Entry{}, fmt.Errorf("%w: manifest: %v", ErrCorrupt, err)
		}
	}
	entry, err := decodeEntry(row, manifest, items)
	if err != nil {
		return Entry{}, err
	}
	return entry, nil
}

func decodeEntry(row *sqlc.TrashEntry, manifest []domain.FileManifestEntry, items []*sqlc.TrashItem) (Entry, error) {
	if row == nil || !domain.ConfigID(row.RootID).Valid() || row.RetentionSeconds <= 0 {
		return Entry{}, fmt.Errorf("%w: entry identity or retention", ErrCorrupt)
	}
	switch row.State {
	case "planned", "trashed", "restoring", "restored", "purging", "purged", "held", "failed":
	default:
		return Entry{}, fmt.Errorf("%w: state", ErrCorrupt)
	}
	entry := Entry{ID: row.ID, RootID: domain.ConfigID(row.RootID), State: row.State, OriginalPrefix: row.OriginalPrefix, TrashPrefix: row.TrashPrefix, Manifest: cloneManifest(manifest), Retention: time.Duration(row.RetentionSeconds) * time.Second, Version: row.Version, Items: make([]Item, 0, len(items))}
	var err error
	entry.ExpiresAt, err = parseTime(row.ExpiresAt)
	if err != nil {
		return Entry{}, fmt.Errorf("%w: expires_at: %v", ErrCorrupt, err)
	}
	if row.TrashedAt.Valid {
		value, parseErr := parseTime(row.TrashedAt.String)
		if parseErr != nil {
			return Entry{}, fmt.Errorf("%w: trashed_at: %v", ErrCorrupt, parseErr)
		}
		entry.TrashedAt = &value
	}
	if row.HoldReason.Valid {
		entry.HoldReason = row.HoldReason.String
	}
	if row.ActiveOperation.Valid {
		entry.ActiveOperation = Operation(row.ActiveOperation.String)
	}
	if row.OperationClaimedBy.Valid {
		entry.ClaimedBy = row.OperationClaimedBy.String
	}
	if row.OperationLeaseUntil.Valid {
		value, parseErr := parseTime(row.OperationLeaseUntil.String)
		if parseErr != nil {
			return Entry{}, fmt.Errorf("%w: operation_lease_until: %v", ErrCorrupt, parseErr)
		}
		entry.LeaseUntil = &value
	}
	if err := decodeClientRef(row.ClientStateJson, &entry.Client); err != nil {
		return Entry{}, err
	}
	for _, row := range items {
		item := Item{ID: row.ID, EntryID: row.EntryID, RootID: domain.ConfigID(row.RootID), OriginalRelativePath: row.OriginalRelativePath, TrashRelativePath: row.TrashRelativePath, Type: domain.ManifestEntryType(row.EntryType), Size: row.SizeBytes, State: row.State}
		if row.Digest.Valid {
			item.Digest = row.Digest.String
		}
		if row.FileIdentity.Valid {
			item.FileIdentity = row.FileIdentity.String
		}
		if row.ClientConnectionID.Valid && row.ClientExternalID.Valid {
			item.Client = &ports.DownloadRef{ConnectionID: domain.ConfigID(row.ClientConnectionID.String), ExternalID: row.ClientExternalID.String}
		}
		if row.TrashedAt.Valid {
			value, parseErr := parseTime(row.TrashedAt.String)
			if parseErr != nil {
				return Entry{}, fmt.Errorf("%w: item trashed_at: %v", ErrCorrupt, parseErr)
			}
			item.TrashedAt = &value
		}
		if row.RestoredAt.Valid {
			value, parseErr := parseTime(row.RestoredAt.String)
			if parseErr != nil {
				return Entry{}, fmt.Errorf("%w: item restored_at: %v", ErrCorrupt, parseErr)
			}
			item.RestoredAt = &value
		}
		if row.PurgedAt.Valid {
			value, parseErr := parseTime(row.PurgedAt.String)
			if parseErr != nil {
				return Entry{}, fmt.Errorf("%w: item purged_at: %v", ErrCorrupt, parseErr)
			}
			item.PurgedAt = &value
		}
		entry.Items = append(entry.Items, item)
	}
	if entry.Client == nil {
		for index := range entry.Items {
			if entry.Items[index].Client != nil {
				ref := *entry.Items[index].Client
				entry.Client = &ref
				break
			}
		}
	}
	sort.Slice(entry.Items, func(i, j int) bool { return entry.Items[i].OriginalRelativePath < entry.Items[j].OriginalRelativePath })
	return entry, nil
}

func decodeClientRef(raw string, target **ports.DownloadRef) error {
	if strings.TrimSpace(raw) == "" || raw == "{}" {
		return nil
	}
	var value struct {
		ConnectionID string `json:"connectionId"`
		ExternalID   string `json:"externalId"`
	}
	if err := json.Unmarshal([]byte(raw), &value); err != nil {
		return fmt.Errorf("%w: client state: %v", ErrCorrupt, err)
	}
	if value.ConnectionID == "" && value.ExternalID == "" {
		return nil
	}
	if value.ConnectionID == "" || value.ExternalID == "" {
		return fmt.Errorf("%w: client state is incomplete", ErrCorrupt)
	}
	ref := ports.DownloadRef{ConnectionID: domain.ConfigID(value.ConnectionID), ExternalID: value.ExternalID}
	if err := validateDownloadRef(ref); err != nil {
		// Client state can contain only evidence; malformed association must not
		// be silently used for a metadata removal.
		return fmt.Errorf("%w: client state: %v", ErrCorrupt, err)
	}
	*target = &ref
	return nil
}

func (service *Service) ensureEntryRequestMatches(entry Entry, request TrashRequest, digest string) error {
	encoded, err := json.Marshal(entry.Manifest)
	if err != nil {
		return err
	}
	if entry.RootID != request.RootID || entry.OriginalPrefix != request.OriginalPrefix || entry.TrashPrefix != request.TrashPrefix || entry.Retention != request.Retention || sha256Hex(encoded) != sha256HexString(request.Manifest) || !sameClient(entry.Client, request.Client) {
		return fmt.Errorf("%w: entry %s", ErrConflict, entry.ID)
	}
	_ = digest
	return nil
}

func sameClient(left, right *ports.DownloadRef) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func (service *Service) lookupIdempotency(ctx context.Context, scope, key, digest string) (Result, bool, error) {
	record, err := service.store.Queries().GetIdempotencyRecord(ctx, &sqlc.GetIdempotencyRecordParams{Scope: scope, IdempotencyKey: key})
	if errors.Is(err, sql.ErrNoRows) {
		return Result{}, false, nil
	}
	if err != nil {
		return Result{}, false, fmt.Errorf("%w: idempotency lookup: %v", ErrStorage, err)
	}
	if record.RequestDigest != digest {
		return Result{}, false, fmt.Errorf("%w: idempotency key payload changed", ErrConflict)
	}
	var response struct {
		EntryID string `json:"entryId"`
	}
	if err := json.Unmarshal([]byte(record.ResponseJson), &response); err != nil || response.EntryID == "" {
		return Result{}, false, fmt.Errorf("%w: idempotency response", ErrCorrupt)
	}
	entry, err := service.loadEntry(ctx, response.EntryID)
	if err != nil {
		return Result{}, false, err
	}
	return resultForEntry(entry, "idempotent_replay"), true, nil
}

func (service *Service) saveIdempotency(ctx context.Context, scope, key, digest, entryID string) error {
	if strings.TrimSpace(key) == "" {
		return nil
	}
	now := service.now()
	if now.IsZero() {
		return ErrClock
	}
	response, err := json.Marshal(map[string]string{"entryId": entryID})
	if err != nil {
		return err
	}
	return service.withTx(ctx, func(tx *sql.Tx, queries *sqlc.Queries) error {
		existing, getErr := queries.GetIdempotencyRecord(ctx, &sqlc.GetIdempotencyRecordParams{Scope: scope, IdempotencyKey: key})
		if getErr == nil {
			if existing.RequestDigest != digest {
				return fmt.Errorf("%w: idempotency key payload changed", ErrConflict)
			}
			return nil
		}
		if !errors.Is(getErr, sql.ErrNoRows) {
			return getErr
		}
		_, err := queries.CreateIdempotencyRecord(ctx, &sqlc.CreateIdempotencyRecordParams{
			Scope: scope, IdempotencyKey: key, RequestDigest: digest, StatusCode: 202,
			ResourceKind: "trash_entry", ResourceID: entryID, ResponseJson: string(response), CreatedAt: formatTime(now),
		})
		return err
	})
}

func (service *Service) withTx(ctx context.Context, fn func(*sql.Tx, *sqlc.Queries) error) error {
	ctx = persistenceContext(ctx)
	tx, err := service.store.DB().BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("%w: begin: %v", ErrStorage, err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := fn(tx, sqlc.New(tx)); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("%w: commit: %v", ErrStorage, err)
	}
	return nil
}

// persistenceContext lets cancellation stop future external work while still
// allowing an already-observed effect to reach the durable journal. The
// transaction itself remains bounded by SQLite's busy timeout and process
// ownership; it does not revive a cancelled network or filesystem call.
func persistenceContext(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	if ctx.Err() != nil {
		return context.WithoutCancel(ctx)
	}
	return ctx
}

func (service *Service) lockFor(key string) func() {
	value, _ := service.locks.LoadOrStore(key, &sync.Mutex{})
	mutex := value.(*sync.Mutex)
	mutex.Lock()
	return mutex.Unlock
}

func validateID(value string) error {
	if value == "" || len(value) > 128 || strings.ContainsRune(value, 0) || strings.TrimSpace(value) != value || strings.ContainsAny(value, "/\\") {
		return fmt.Errorf("%w: id", ErrInvalidRequest)
	}
	return nil
}

func validateDownloadRef(ref ports.DownloadRef) error {
	if !ref.ConnectionID.Valid() || strings.TrimSpace(ref.ExternalID) == "" || len(ref.ExternalID) > 256 || strings.ContainsRune(ref.ExternalID, 0) {
		return ErrInvalidRequest
	}
	return nil
}

func validateRelativePrefix(value string) error {
	if value == "" || value == "." || strings.HasPrefix(value, "/") || strings.ContainsRune(value, 0) || path.Clean(value) != value || strings.Contains(value, `\\`) {
		return errors.New("relative path is invalid")
	}
	return domain.ValidateRelativePath(value)
}

func validateManifestRootAndPrefix(entry domain.FileManifestEntry, root domain.ConfigID, prefix string) error {
	if err := entry.ValidateAction(false); err != nil {
		return err
	}
	if entry.RootID != root || !pathWithin(entry.RelativePath, prefix) {
		return fmt.Errorf("entry %q is outside original prefix", entry.RelativePath)
	}
	for _, child := range entry.Children {
		if err := validateManifestRootAndPrefix(child, root, prefix); err != nil {
			return err
		}
	}
	return nil
}

func flattenManifest(entry domain.FileManifestEntry, target *[]domain.FileManifestEntry) error {
	*target = append(*target, entry)
	for _, child := range entry.Children {
		if err := flattenManifest(child, target); err != nil {
			return err
		}
	}
	return nil
}

func cloneManifest(entries []domain.FileManifestEntry) []domain.FileManifestEntry {
	encoded, _ := json.Marshal(entries)
	var clone []domain.FileManifestEntry
	_ = json.Unmarshal(encoded, &clone)
	return clone
}

func validateManifestBytes(manifest []domain.FileManifestEntry, max int) error {
	encoded, err := json.Marshal(manifest)
	if err != nil {
		return err
	}
	if len(encoded) > max {
		return errors.New("manifest exceeds configured byte bound")
	}
	return nil
}

func mapTrashPath(originalPrefix, trashPrefix, relative string) string {
	suffix := strings.TrimPrefix(relative, originalPrefix)
	suffix = strings.TrimPrefix(suffix, "/")
	if suffix == "" {
		suffix = path.Base(relative)
	}
	return path.Join(trashPrefix, suffix)
}

func pathWithin(value, prefix string) bool {
	return value == prefix || strings.HasPrefix(value, prefix+"/")
}

type identityValues struct {
	rootID       domain.ConfigID
	relativePath string
	entryType    domain.ManifestEntryType
	size         int64
	digest       string
	fileIdentity string
}

func identityForEntry(entry domain.FileManifestEntry) identityValues {
	return identityValues{
		rootID: entry.RootID, relativePath: entry.RelativePath, entryType: entry.Type,
		size: entry.Size, digest: entry.Digest, fileIdentity: entry.FileIdentity,
	}
}

func identityForItem(item Item) identityValues {
	return identityForItemAt(item, item.OriginalRelativePath)
}

func identityForItemAt(item Item, relativePath string) identityValues {
	return identityValues{
		rootID: item.RootID, relativePath: relativePath, entryType: item.Type,
		size: item.Size, digest: item.Digest, fileIdentity: item.FileIdentity,
	}
}

func identityMatches(observed domain.FileManifestEntry, expected identityValues) error {
	if observed.RootID != expected.rootID || observed.RelativePath != expected.relativePath || observed.Type != expected.entryType || observed.Size != expected.size {
		return ErrIdentityChanged
	}
	if strings.TrimSpace(expected.fileIdentity) == "" || observed.FileIdentity != expected.fileIdentity {
		return ErrIdentityChanged
	}
	if expected.digest != "" && observed.Digest != "" && !strings.EqualFold(strings.TrimPrefix(expected.digest, "sha256:"), strings.TrimPrefix(observed.Digest, "sha256:")) {
		return ErrIdentityChanged
	}
	return nil
}

func manifestFromItem(item Item, observedAt time.Time) domain.FileManifestEntry {
	return domain.FileManifestEntry{RootID: item.RootID, RelativePath: item.TrashRelativePath, Type: item.Type, Size: item.Size, Digest: item.Digest, FileIdentity: item.FileIdentity, ObservedAt: observedAt}
}

func affectedPaths(entries []domain.FileManifestEntry) map[string]domain.FileManifestEntry {
	result := make(map[string]domain.FileManifestEntry, len(entries))
	for _, entry := range entries {
		result[string(entry.RootID)+"\x00"+entry.RelativePath] = entry
	}
	return result
}

func itemAffected(item Item, affected map[string]domain.FileManifestEntry) bool {
	for _, entry := range affected {
		if entry.RootID != item.RootID {
			continue
		}
		if entry.RelativePath == item.OriginalRelativePath || entry.RelativePath == item.TrashRelativePath || (entry.Type == domain.ManifestDirectory && (pathWithin(item.OriginalRelativePath, entry.RelativePath) || pathWithin(item.TrashRelativePath, entry.RelativePath))) {
			return true
		}
	}
	return false
}

// exactAffectedItem returns only a directly reported target. Directory
// effects are deliberately not expanded here: an action must report each
// selected payload identity before this coordinator can terminalize its item.
// This keeps a directory-level or foreign effect from becoming false per-file
// success evidence.
func exactAffectedItem(item Item, affected map[string]domain.FileManifestEntry) (domain.FileManifestEntry, bool) {
	for _, entry := range affected {
		if entry.RootID != item.RootID || (entry.RelativePath != item.OriginalRelativePath && entry.RelativePath != item.TrashRelativePath) {
			continue
		}
		return entry, true
	}
	return domain.FileManifestEntry{}, false
}

func mappingAffected(item Item, affected []domain.FileManifestEntry) bool {
	for _, entry := range affected {
		if entry.RootID == item.RootID && (entry.RelativePath == item.TrashRelativePath || entry.RelativePath == item.OriginalRelativePath) {
			return true
		}
	}
	return false
}

func mappingEffectMatches(item Item, affected []domain.FileManifestEntry) bool {
	for _, entry := range affected {
		if entry.RootID != item.RootID || entry.RelativePath != item.OriginalRelativePath {
			continue
		}
		return identityMatches(entry, identityForItemAt(item, item.OriginalRelativePath)) == nil
	}
	return false
}

func isStopped(observation ports.DownloadObservation) bool {
	if observation.Seeding || strings.TrimSpace(observation.State) == "" {
		return false
	}
	switch observation.State {
	case "paused", "pausedDL", "pausedUP", "stopped", "stoppedDL", "stoppedUP":
		return true
	default:
		return false
	}
}

func nullable(value string) sql.NullString {
	if strings.TrimSpace(value) == "" {
		return sql.NullString{}
	}
	return sql.NullString{String: value, Valid: true}
}

func nullableTime(valid bool, value time.Time) sql.NullString {
	if !valid || value.IsZero() {
		return sql.NullString{}
	}
	return sql.NullString{String: formatTime(value), Valid: true}
}

func derefTime(value *time.Time) time.Time {
	if value == nil {
		return time.Time{}
	}
	return *value
}

func parseTime(value string) (time.Time, error) {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}, err
	}
	return parsed.UTC().Round(0), nil
}

func formatTime(value time.Time) string { return value.UTC().Round(0).Format(time.RFC3339Nano) }

func digestRequest(kind string, value any) string {
	encoded, _ := json.Marshal(struct {
		Kind  string `json:"kind"`
		Value any    `json:"value"`
	}{Kind: kind, Value: value})
	return sha256Hex(encoded)
}

func digestTrashRequest(request TrashRequest) string {
	request.IdempotencyKey = ""
	return digestRequest("trash", request)
}

func digestRestoreRequest(request RestoreRequest) string {
	request.IdempotencyKey = ""
	return digestRequest("restore", request)
}

func digestPurgeRequest(request PurgeRequest) string {
	request.IdempotencyKey = ""
	return digestRequest("purge", request)
}

func sha256Hex(value []byte) string {
	digest := sha256.Sum256(value)
	return hex.EncodeToString(digest[:])
}

func sha256HexString(manifest []domain.FileManifestEntry) string {
	encoded, _ := json.Marshal(manifest)
	return sha256Hex(encoded)
}

func outcomeJSON(operation Operation, state, reason string, evidence any, effect any) string {
	value := map[string]any{"operation": operation, "state": state}
	if reason != "" {
		value["reason"] = reason
	}
	if evidence != nil {
		value["evidence"] = evidence
	}
	if effect != nil {
		value["effect"] = effect
	}
	encoded, _ := json.Marshal(value)
	return string(encoded)
}

func effectOutcomeJSON(operation Operation, state, reason string, effects []ItemEffect, effect any) string {
	return outcomeJSON(operation, state, reason, effects, effect)
}

func effectState(entry Entry, effect ports.FilesystemEffect) string {
	state := entryClientState(entry)
	var value map[string]any
	if json.Unmarshal([]byte(state), &value) != nil {
		value = map[string]any{}
	}
	value["filesystem"] = map[string]any{"outcome": effect.Outcome, "observedAt": formatTime(effect.ObservedAt), "evidence": effect.Evidence}
	encoded, _ := json.Marshal(value)
	return string(encoded)
}

func entryClientState(entry Entry) string {
	if entry.Client == nil {
		return "{}"
	}
	encoded, _ := json.Marshal(map[string]string{"connectionId": entry.Client.ConnectionID.String(), "externalId": entry.Client.ExternalID})
	return string(encoded)
}

func resultForEntry(entry Entry, evidence string) Result {
	return Result{Entry: entry, Evidence: []string{evidence}}
}

func alreadySatisfiedResult(entry Entry, evidence string) Result {
	return Result{Entry: entry, Outcome: domain.OutcomeAlreadySatisfied, Evidence: []string{evidence}}
}

func resultForEffect(entry Entry, operation Operation, effect ports.FilesystemEffect, matched map[string]bool) Result {
	effects := make([]ItemEffect, 0, len(matched))
	for itemID := range matched {
		effects = append(effects, ItemEffect{ItemID: itemID, Operation: operation, Outcome: effect.Outcome, ObservedAt: effect.ObservedAt, Evidence: append([]string(nil), effect.Evidence...)})
	}
	sort.Slice(effects, func(i, j int) bool { return effects[i].ItemID < effects[j].ItemID })
	return Result{Entry: entry, Outcome: effect.Outcome, Effects: effects, Evidence: append([]string(nil), effect.Evidence...)}
}

func safeError(err error) string {
	if err == nil {
		return ""
	}
	// Port implementations are expected to sanitize errors. Keep journal
	// reasons bounded and avoid copying arbitrary upstream bodies into state.
	value := strings.TrimSpace(err.Error())
	if len(value) > 256 {
		value = value[:256]
	}
	return value
}

func (service *Service) _missingDownload(err error) bool {
	return service.isDownloadMissing != nil && service.isDownloadMissing(err)
}

func isMissing(err error) bool { return errors.Is(err, fs.ErrNotExist) }
