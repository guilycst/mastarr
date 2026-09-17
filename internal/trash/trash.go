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
	Approval       *EarlyPurgeApproval
	IdempotencyKey string
}

// EarlyPurgeApproval is the immutable review binding required to purge a
// trashed entry before its recorded retention expiry. Every identity is
// checked by the storage ClaimApprovedEarlyPurge CAS; a non-empty boolean
// alone never authorizes an early purge.
type EarlyPurgeApproval struct {
	PlanID               string
	PlanRevision         int64
	PlanDigest           string
	DecisionID           string
	ActionRunID          string
	ApprovedEntryVersion int64
	ActionRunVersion     int64
}

// RetryRequest reopens a held operation only after a fresh read-only
// reconciliation proves its stored item scope is still safe to claim.
type RetryRequest struct {
	EntryID   string
	ID        string
	Operation Operation
}

// Operation names the janitor operations and the read-only trash-intent
// reconciliation. Trash reconciliation is not a janitor mutation operation;
// the existing purge journal row stores its durable evidence because the
// initial schema intentionally has no third janitor operation.
type Operation string

const (
	OperationPurge   Operation = "purge"
	OperationRestore Operation = "restore"
	OperationTrash   Operation = "trash"
	operationTrash   Operation = OperationTrash
)

// TrashReconciliationState is the exact per-item observation derived from the
// original and mapped trash paths. It is persisted as evidence in the purge
// journal row; it never authorizes a filesystem mutation by itself.
type TrashReconciliationState string

const (
	TrashSourceOnly        TrashReconciliationState = "source_only"
	TrashOnly              TrashReconciliationState = "trash_only"
	TrashBothPresent       TrashReconciliationState = "both_present"
	TrashNeitherObservable TrashReconciliationState = "neither_observable"
	TrashChangedIdentity   TrashReconciliationState = "changed_identity"
	TrashPartial           TrashReconciliationState = "partial"
	TrashDirectoryLeaf     TrashReconciliationState = "directory_leaf"
	TrashPartialDirectory  TrashReconciliationState = "partial_directory_leaves"
	TrashRetryPending      TrashReconciliationState = "retry_pending"
	// TrashTerminalStale means a previously terminal item no longer has the
	// exact trash-only read-back that justified its terminal state. It is kept
	// distinct from pending source-only scope so a retry cannot treat a stale
	// terminal row as ordinary remaining work.
	TrashTerminalStale TrashReconciliationState = "terminal_item_stale"
)

type trashReconciliationItem struct {
	ItemID       string                    `json:"itemId"`
	OriginalPath string                    `json:"originalPath"`
	TrashPath    string                    `json:"trashPath"`
	State        TrashReconciliationState  `json:"state"`
	ObservedAt   time.Time                 `json:"observedAt,omitempty"`
	Original     *domain.FileManifestEntry `json:"original,omitempty"`
	Trash        *domain.FileManifestEntry `json:"trash,omitempty"`
	Evidence     []string                  `json:"evidence,omitempty"`
}

type trashReconciliation struct {
	Operation   Operation                 `json:"operation"`
	State       string                    `json:"state"`
	Disposition TrashReconciliationState  `json:"disposition"`
	ObservedAt  time.Time                 `json:"observedAt"`
	Items       []trashReconciliationItem `json:"items"`
	Evidence    []string                  `json:"evidence,omitempty"`
}

// TrashRetryRequest is an explicit authorization to retry a planned trash
// intent after read-only reconciliation proves that every selected source is
// still present and every mapped destination is absent.
type TrashRetryRequest struct {
	EntryID string
	ID      string
}

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
	clientStateJSON string
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
			if result.Entry.State == "planned" {
				return service.ReconcileTrash(ctx, result.Entry.ID)
			}
			return service.replayTrashResult(result)
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
		if existing.State == "planned" {
			// A durable planned entry already records an intent whose dispatch may
			// have happened before the process stopped. Inspect both exact paths
			// before reporting pending or materialized; never infer safety from the
			// state alone or dispatch the same intent from a replay.
			return service.ReconcileTrash(ctx, existing.ID)
		}
		return service.replayTrashResult(Result{Entry: existing})
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
	claim, err := service.claim(ctx, entryID, OperationRestore, false, nil)
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
	if hard && request.Approval == nil {
		return resultForEntry(entry, "early_purge_approval_required"), fmt.Errorf("%w: early purge requires an exact approval binding", ErrConflict)
	}
	if !hard && now.Before(entry.ExpiresAt) {
		return resultForEntry(entry, "retention_not_expired"), ErrNotDue
	}
	if err := service.ensureJanitorRecord(ctx, entryID, OperationPurge); err != nil {
		return Result{}, err
	}
	claim, err := service.claim(ctx, entryID, OperationPurge, hard, request.Approval)
	if err != nil {
		return Result{}, err
	}
	if hard {
		// Both the first approved claim and an approved recovery claim must
		// perform a fresh read-only reconciliation before deleting payload.
		// This is also the explicit retry path after process loss.
		if reconciled, reconcileErr := service.reconcileBeforePurgeDispatch(ctx, claim); reconcileErr != nil || !reconciled.Retryable {
			if reconcileErr == nil {
				reconcileErr = ErrHeld
			}
			return reconciled, reconcileErr
		}
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
	if err := service.recoverExpiredPlannedTrashClaim(ctx, now); err != nil {
		return TickResult{}, err
	}
	planned, err := service.listPlannedTrashEntries(ctx, limit)
	if err != nil {
		return TickResult{}, err
	}
	tick := TickResult{Results: make([]Result, 0, len(planned)+limit)}
	for _, entryID := range planned {
		result, reconcileErr := service.ReconcileTrash(ctx, entryID)
		tick.Processed++
		if errors.Is(reconcileErr, ErrHeld) {
			tick.Held++
		}
		tick.Results = append(tick.Results, result)
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
		var claim *sqlc.JanitorRecord
		var claimErr error
		var opErr error
		if record.Operation == string(OperationPurge) && approvalBindingPresent(record) {
			claim, claimErr = service.claimApprovedEarlyPurgeReconciliation(ctx, record, nil)
		} else {
			claim, claimErr = service.claim(ctx, record.TrashEntryID, Operation(record.Operation), false, nil)
		}
		if claimErr != nil {
			tick.Skipped++
			continue
		}
		if record.Operation == string(OperationPurge) && approvalBindingPresent(record) {
			var reconciliation Result
			reconciliation, opErr = service.reconcileBeforePurgeDispatch(ctx, claim)
			if opErr != nil || !reconciliation.Retryable {
				tick.Processed++
				if reconciliation.Entry.State == "held" || errors.Is(opErr, ErrHeld) {
					tick.Held++
				}
				tick.Results = append(tick.Results, reconciliation)
				continue
			}
		}
		var result Result
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
	now := service.now()
	if now.IsZero() {
		return 0, ErrClock
	}
	if err := service.recoverExpiredPlannedTrashClaim(ctx, now); err != nil {
		return 0, err
	}
	recovered, err := service.store.Queries().RecoverRunningJanitorRecords(ctx, formatTime(now))
	if err != nil {
		return 0, fmt.Errorf("%w: recover running janitor records: %v", ErrStorage, err)
	}
	// Planned trash intents have no janitor operation of their own in the
	// frozen schema. Discover them by entry state and run the read-only
	// reconciliation so a process restart cannot strand an intent. The result
	// is durably journalled even when the observation remains unresolved.
	planned, listErr := service.listPlannedTrashEntries(ctx, service.maxItems)
	if listErr != nil {
		return 0, listErr
	}
	for _, entryID := range planned {
		_, _ = service.ReconcileTrash(ctx, entryID)
	}
	return len(recovered), nil
}

// ReconcileTrash performs the explicit read-only recovery for an interrupted
// trash intent. It observes every exact original and mapped destination and
// persists per-item evidence in the durable purge journal row. It never calls
// a filesystem action or download-client mutation.
func (service *Service) ReconcileTrash(ctx context.Context, entryID string) (Result, error) {
	if err := service.validateContext(ctx); err != nil {
		return Result{}, err
	}
	if err := validateID(entryID); err != nil {
		return Result{}, err
	}
	entry, err := service.loadEntry(ctx, entryID)
	if err != nil {
		return Result{}, err
	}
	if entry.State == "trashed" {
		return alreadySatisfiedResult(entry, "trash_already_materialized"), nil
	}
	heldRetry, heldRetryErr := service.isHeldTrashRetry(ctx, entry)
	if heldRetryErr != nil {
		return Result{Entry: entry}, heldRetryErr
	}
	if entry.State != "planned" && !heldRetry {
		return resultForEntry(entry, "trash_reconciliation_not_planned"), fmt.Errorf("%w: trash entry is %s", ErrConflict, entry.State)
	}
	if entry.ActiveOperation == operationTrash || entry.ActiveOperation == OperationPurge {
		if entry.LeaseUntil != nil && service.now().Before(*entry.LeaseUntil) && entry.ClaimedBy != service.workerID {
			return resultForEntry(entry, "trash_reconciliation_claimed"), ErrClaimed
		}
		if err := service.recoverExpiredPlannedTrashClaim(ctx, service.now()); err != nil {
			return Result{Entry: entry}, err
		}
		entry, err = service.loadEntry(ctx, entryID)
		if err != nil {
			return Result{}, err
		}
		if entry.ActiveOperation == OperationPurge {
			return resultForEntry(entry, "trash_reconciliation_claimed"), ErrClaimed
		}
	}
	now := service.now()
	if now.IsZero() {
		return Result{}, ErrClock
	}
	reconciliation, effects := service.observePlannedTrash(ctx, entry)
	if err := service.persistTrashReconciliation(ctx, entry, reconciliation, effects, now); err != nil {
		return Result{Entry: entry, Effects: effects, Evidence: reconciliation.Evidence}, err
	}
	latest, loadErr := service.loadEntry(persistenceContext(ctx), entry.ID)
	if loadErr == nil {
		entry = latest
	}
	result := Result{Entry: entry, Effects: effects, Evidence: append([]string(nil), reconciliation.Evidence...)}
	switch reconciliation.Disposition {
	case TrashOnly:
		if heldRetry && outcomeHasUnresolvedScopeEvidenceForEntry(service, ctx, entry.ID) {
			result.Evidence = append(result.Evidence, "trash_scope_requires_manual_review")
			return result, ErrHeld
		}
		result.Outcome = domain.OutcomeAlreadySatisfied
		result.Evidence = append(result.Evidence, "trash_materialized_by_readback")
		return result, nil
	case TrashSourceOnly:
		if heldRetry && outcomeHasUnresolvedScopeEvidenceForEntry(service, ctx, entry.ID) {
			result.Evidence = append(result.Evidence, "trash_scope_requires_manual_review")
			return result, ErrHeld
		}
		if len(pendingTrashItems(entry)) == 0 {
			result.Evidence = append(result.Evidence, "trash_retry_has_no_pending_items")
			return result, ErrHeld
		}
		result.Retryable = true
		result.Evidence = append(result.Evidence, "trash_retry_requires_explicit_authorization")
		return result, ErrUncertain
	default:
		result.Evidence = append(result.Evidence, "trash_reconciliation_requires_review")
		return result, ErrHeld
	}
}

// isHeldTrashRetry identifies the only held state that the trash
// reconciliation endpoint may inspect: a previous explicit retry whose
// durable outcome is the trash operation. Other held states belong to the
// normal trash/purge lifecycle and must remain behind their own review path.
func (service *Service) isHeldTrashRetry(ctx context.Context, entry Entry) (bool, error) {
	if entry.State != "held" || !strings.HasPrefix(entry.HoldReason, "trash_retry") {
		return false, nil
	}
	record, err := service.store.Queries().GetJanitorRecord(ctx, &sqlc.GetJanitorRecordParams{TrashEntryID: entry.ID, Operation: string(OperationPurge)})
	if errors.Is(err, sql.ErrNoRows) {
		return false, fmt.Errorf("%w: held trash retry journal is missing", ErrCorrupt)
	}
	if err != nil {
		return false, fmt.Errorf("%w: get held trash retry journal: %v", ErrStorage, err)
	}
	if outcomeOperation(record.OutcomeJson) != OperationTrash {
		return false, nil
	}
	return true, nil
}

func outcomeOperation(raw string) Operation {
	var envelope struct {
		Operation Operation `json:"operation"`
	}
	if json.Unmarshal([]byte(raw), &envelope) != nil {
		return ""
	}
	return envelope.Operation
}

func outcomeHasUnresolvedScopeEvidenceForEntry(service *Service, ctx context.Context, entryID string) bool {
	record, err := service.store.Queries().GetJanitorRecord(ctx, &sqlc.GetJanitorRecordParams{TrashEntryID: entryID, Operation: string(OperationPurge)})
	return err == nil && outcomeHasUnresolvedScopeEvidence(record.OutcomeJson)
}

// RetryTrash is the separately authorized mutation path for a planned intent.
// It requires a fresh source-only reconciliation, takes an object-scoped
// durable lease, and then dispatches exactly the stored manifest. A caller
// cannot turn a collision, partial result or unknown observation into a
// retry by state alone.
func (service *Service) RetryTrash(ctx context.Context, request TrashRetryRequest) (Result, error) {
	if err := service.validateContext(ctx); err != nil {
		return Result{}, err
	}
	entryID := strings.TrimSpace(request.EntryID)
	if entryID == "" {
		entryID = strings.TrimSpace(request.ID)
	}
	if err := validateID(entryID); err != nil {
		return Result{}, err
	}
	reconciled, reconcileErr := service.ReconcileTrash(ctx, entryID)
	if (!errors.Is(reconcileErr, ErrUncertain) && reconcileErr != nil) || !reconciled.Retryable {
		return reconciled, reconcileErr
	}
	claim, err := service.claimPlannedTrash(ctx, entryID)
	if err != nil {
		return reconciled, err
	}
	return service.dispatchPlannedTrash(ctx, claim)
}

func (service *Service) observePlannedTrash(ctx context.Context, entry Entry) (trashReconciliation, []ItemEffect) {
	now := service.now()
	reconciliation := trashReconciliation{Operation: OperationTrash, State: "observed", ObservedAt: now, Items: make([]trashReconciliationItem, 0, len(entry.Items))}
	effects := make([]ItemEffect, 0, len(entry.Items))
	if len(entry.Items) == 0 {
		reconciliation.Disposition = TrashNeitherObservable
		reconciliation.Evidence = []string{"trash_manifest_has_no_actionable_items"}
		return reconciliation, effects
	}
	states := make([]TrashReconciliationState, 0, len(entry.Items))
	observedStates := make([]TrashReconciliationState, 0, len(entry.Items))
	allMaterialized := true
	terminalStale := false
	for _, item := range entry.Items {
		originalTarget := domain.FileTarget{RootID: item.RootID, RelativePath: item.OriginalRelativePath}
		trashTarget := domain.FileTarget{RootID: item.RootID, RelativePath: item.TrashRelativePath}
		original, originalErr := service.read.Stat(ctx, originalTarget)
		trash, trashErr := service.read.Stat(ctx, trashTarget)
		observation := trashReconciliationItem{ItemID: item.ID, OriginalPath: item.OriginalRelativePath, TrashPath: item.TrashRelativePath, ObservedAt: now}
		itemEffect := ItemEffect{ItemID: item.ID, Path: item.OriginalRelativePath, Operation: OperationTrash, State: "unknown", ObservedAt: now}
		if original.ObservedAt.After(observation.ObservedAt) {
			observation.ObservedAt = original.ObservedAt
		}
		if trash.ObservedAt.After(observation.ObservedAt) {
			observation.ObservedAt = trash.ObservedAt
		}
		if originalErr == nil {
			copy := original.Entry
			observation.Original = &copy
		}
		if trashErr == nil {
			copy := trash.Entry
			observation.Trash = &copy
		}
		originalMissing := originalErr != nil && isMissing(originalErr)
		trashMissing := trashErr != nil && isMissing(trashErr)
		switch {
		case originalErr != nil && !originalMissing || trashErr != nil && !trashMissing:
			observation.State = TrashNeitherObservable
			observation.Evidence = []string{"trash_path_unobservable"}
			itemEffect.State = "unknown"
			itemEffect.Evidence = append([]string(nil), observation.Evidence...)
		case originalErr == nil && trashErr == nil:
			originalIdentityErr := identityMatches(original.Entry, identityForItem(item))
			trashIdentityErr := identityMatches(trash.Entry, identityForItemAt(item, item.TrashRelativePath))
			if originalIdentityErr != nil || trashIdentityErr != nil {
				observation.State = TrashChangedIdentity
				observation.Evidence = []string{"trash_identity_changed"}
				itemEffect.State = "held"
				itemEffect.Evidence = append([]string(nil), observation.Evidence...)
			} else {
				observation.State = TrashBothPresent
				observation.Evidence = []string{"trash_source_and_destination_present"}
				itemEffect.State = "held"
				itemEffect.Evidence = append([]string(nil), observation.Evidence...)
			}
		case originalErr == nil && trashMissing:
			if identityErr := identityMatches(original.Entry, identityForItem(item)); identityErr != nil {
				observation.State = TrashChangedIdentity
				observation.Evidence = []string{"trash_identity_changed"}
				itemEffect.State = "held"
				itemEffect.Evidence = append([]string(nil), observation.Evidence...)
			} else {
				observation.State = TrashSourceOnly
				observation.Evidence = []string{"trash_source_present_destination_absent"}
				itemEffect.State = "source_only"
				itemEffect.Evidence = append([]string(nil), observation.Evidence...)
			}
		case originalMissing && trashErr == nil:
			if identityErr := identityMatches(trash.Entry, identityForItemAt(item, item.TrashRelativePath)); identityErr != nil {
				observation.State = TrashChangedIdentity
				observation.Evidence = []string{"trash_identity_changed"}
				itemEffect.State = "held"
				itemEffect.Evidence = append([]string(nil), observation.Evidence...)
			} else {
				observation.State = TrashOnly
				observation.Evidence = []string{"trash_source_absent_destination_present"}
				itemEffect.Path = item.TrashRelativePath
				itemEffect.State = "trash_only"
				itemEffect.Outcome = domain.OutcomeAlreadySatisfied
				itemEffect.Evidence = append([]string(nil), observation.Evidence...)
			}
		default:
			observation.State = TrashNeitherObservable
			observation.Evidence = []string{"trash_source_and_destination_absent"}
			itemEffect.State = "unknown"
			itemEffect.Evidence = append([]string(nil), observation.Evidence...)
		}
		if isDirectoryLeaf(entry.Manifest, item.OriginalRelativePath) {
			observation.Evidence = append(observation.Evidence, "directory_leaf")
			itemEffect.Evidence = append(itemEffect.Evidence, "directory_leaf")
		}
		if !isPendingTrashItem(item) && observation.State != TrashOnly {
			// A terminal item is materialized only by an exact trash-only
			// observation. Seeing its source, both paths, neither path, or a
			// changed identity means the durable terminal state is stale. Keep
			// this separate from pending states so RetryTrash cannot authorize a
			// narrow retry and falsely complete the aggregate entry.
			terminalStale = true
			observation.Evidence = appendUniqueString(observation.Evidence, "trash_terminal_item_stale")
			itemEffect.Evidence = appendUniqueString(itemEffect.Evidence, "trash_terminal_item_stale")
		}
		observedStates = append(observedStates, observation.State)
		// A previously matched retry item is already terminal only when the
		// exact trash object remains present and the source is absent. Keep that
		// observation in the evidence, but do not make it a pending retry
		// target. Any other observation for a terminal row must still block
		// finalization: it proves that the durable item state is stale.
		if isPendingTrashItem(item) || observation.State != TrashOnly {
			states = append(states, observation.State)
		}
		if isPendingTrashItem(item) {
			allMaterialized = false
		}
		reconciliation.Items = append(reconciliation.Items, observation)
		effects = append(effects, itemEffect)
	}
	if allMaterialized && len(states) == 0 {
		reconciliation.Disposition = TrashOnly
	} else {
		reconciliation.Disposition = classifyTrashReconciliation(states)
	}
	if terminalStale {
		reconciliation.Disposition = TrashTerminalStale
	}
	reconciliation.Evidence = []string{"trash_reconciliation_observed"}
	if terminalStale {
		reconciliation.Evidence = append(reconciliation.Evidence, "trash_terminal_item_stale")
	}
	if reconciliation.Disposition == TrashPartial && containsDirectoryEvidence(reconciliation.Items) {
		reconciliation.Disposition = TrashPartialDirectory
	}
	for _, state := range observedStates {
		reconciliation.Evidence = append(reconciliation.Evidence, "trash_state:"+string(state))
	}
	sort.Strings(reconciliation.Evidence)
	return reconciliation, effects
}

func isPendingTrashItem(item Item) bool {
	if item.Type == domain.ManifestDirectory {
		return false
	}
	switch item.State {
	case "trashed", "purged", "restored":
		return false
	default:
		return true
	}
}

func pendingTrashItems(entry Entry) []Item {
	items := make([]Item, 0, len(entry.Items))
	for _, item := range entry.Items {
		if isPendingTrashItem(item) {
			items = append(items, item)
		}
	}
	return items
}

// manifestForTrashItems reconstructs an exact leaf manifest from the
// immutable stored manifest and the durable per-item selection. A retry must
// submit only unresolved items; reusing the original root or full manifest
// could repeat an already-materialized item or widen the mutation scope.
func manifestForTrashItems(entry Entry, items []Item) ([]domain.FileManifestEntry, error) {
	flat := make([]domain.FileManifestEntry, 0, len(entry.Manifest))
	for _, manifestEntry := range entry.Manifest {
		if err := flattenManifest(manifestEntry, &flat); err != nil {
			return nil, fmt.Errorf("%w: flatten retry manifest: %v", ErrCorrupt, err)
		}
	}
	byPath := make(map[string]domain.FileManifestEntry, len(flat))
	for _, manifestEntry := range flat {
		if manifestEntry.Type == domain.ManifestDirectory {
			continue
		}
		key := string(manifestEntry.RootID) + "\x00" + manifestEntry.RelativePath
		if _, exists := byPath[key]; exists {
			return nil, fmt.Errorf("%w: duplicate retry manifest path %q", ErrCorrupt, manifestEntry.RelativePath)
		}
		byPath[key] = manifestEntry
	}
	result := make([]domain.FileManifestEntry, 0, len(items))
	for _, item := range items {
		key := string(item.RootID) + "\x00" + item.OriginalRelativePath
		manifestEntry, ok := byPath[key]
		if !ok || manifestEntry.Type != item.Type || manifestEntry.Size != item.Size || manifestEntry.Digest != item.Digest || manifestEntry.FileIdentity != item.FileIdentity {
			return nil, fmt.Errorf("%w: retry item %s is not bound to the stored manifest", ErrCorrupt, item.ID)
		}
		result = append(result, manifestEntry)
	}
	if len(result) == 0 {
		return nil, fmt.Errorf("%w: retry manifest has no pending items", ErrConflict)
	}
	return result, nil
}

func classifyTrashReconciliation(states []TrashReconciliationState) TrashReconciliationState {
	if len(states) == 0 {
		return TrashNeitherObservable
	}
	for _, state := range states {
		switch state {
		case TrashChangedIdentity:
			return TrashChangedIdentity
		case TrashBothPresent:
			return TrashBothPresent
		case TrashNeitherObservable:
			return TrashNeitherObservable
		}
	}
	allSource, allTrash := true, true
	for _, state := range states {
		if state != TrashSourceOnly && state != TrashDirectoryLeaf {
			allSource = false
		}
		if state != TrashOnly && state != TrashDirectoryLeaf {
			allTrash = false
		}
	}
	if allSource {
		return TrashSourceOnly
	}
	if allTrash {
		return TrashOnly
	}
	return TrashPartial
}

func containsDirectoryEvidence(items []trashReconciliationItem) bool {
	for _, item := range items {
		for _, evidence := range item.Evidence {
			if evidence == "directory_leaf" {
				return true
			}
		}
	}
	return false
}

func isDirectoryLeaf(manifest []domain.FileManifestEntry, relativePath string) bool {
	for _, item := range manifest {
		if item.Type == domain.ManifestDirectory && pathWithin(relativePath, item.RelativePath) && relativePath != item.RelativePath {
			return true
		}
	}
	return false
}

func (service *Service) persistTrashReconciliation(ctx context.Context, entry Entry, reconciliation trashReconciliation, effects []ItemEffect, now time.Time) error {
	return service.withTx(persistenceContext(ctx), func(tx *sql.Tx, queries *sqlc.Queries) error {
		current, getErr := queries.GetTrashEntry(ctx, entry.ID)
		if getErr != nil {
			return getErr
		}
		if current.Version != entry.Version || current.ActiveOperation.Valid || current.State != "planned" && current.State != "held" {
			return ErrClaimed
		}
		janitor, getErr := queries.GetJanitorRecord(ctx, &sqlc.GetJanitorRecordParams{TrashEntryID: entry.ID, Operation: string(OperationPurge)})
		if getErr != nil {
			return getErr
		}
		priorScope := outcomeHasUnresolvedScopeEvidence(janitor.OutcomeJson)
		if priorScope {
			reconciliation.Evidence = appendUniqueString(reconciliation.Evidence, "scope_unresolved")
		}
		state := "pending"
		if reconciliation.Disposition == TrashOnly && !priorScope {
			state = "materialized"
		} else if reconciliation.Disposition != TrashSourceOnly {
			state = "held"
		}
		heldReason := "trash_reconciliation:" + string(reconciliation.Disposition)
		if current.State == "held" && strings.HasPrefix(entry.HoldReason, "trash_retry") {
			heldReason = entry.HoldReason
		}
		if reconciliation.Disposition == TrashOnly {
			for _, itemEffect := range effects {
				if itemEffect.State != "trash_only" {
					continue
				}
				if _, updateErr := tx.ExecContext(ctx, `UPDATE trash_items SET state = 'trashed', trashed_at = ? WHERE id = ? AND entry_id = ? AND state = 'selected'`, formatTime(now), itemEffect.ItemID, entry.ID); updateErr != nil {
					return updateErr
				}
			}
			if state == "materialized" {
				updated, updateErr := tx.ExecContext(ctx, `UPDATE trash_entries SET state = 'trashed', trashed_at = ?, expires_at = ?, hold_reason = NULL, version = version + 1, updated_at = ? WHERE id = ? AND version = ? AND state IN ('planned', 'held') AND active_operation IS NULL`, formatTime(now), formatTime(now.Add(entry.Retention)), formatTime(now), entry.ID, entry.Version)
				if updateErr != nil {
					return updateErr
				}
				if affected, affectedErr := updated.RowsAffected(); affectedErr != nil {
					return affectedErr
				} else if affected != 1 {
					return ErrClaimed
				}
			} else {
				updated, updateErr := tx.ExecContext(ctx, `UPDATE trash_entries SET state = 'held', hold_reason = ?, version = version + 1, updated_at = ? WHERE id = ? AND version = ? AND state IN ('planned', 'held') AND active_operation IS NULL`, heldReason, formatTime(now), entry.ID, entry.Version)
				if updateErr != nil {
					return updateErr
				}
				if affected, affectedErr := updated.RowsAffected(); affectedErr != nil {
					return affectedErr
				} else if affected != 1 {
					return ErrClaimed
				}
			}
		} else if reconciliation.Disposition != TrashSourceOnly {
			updated, updateErr := tx.ExecContext(ctx, `UPDATE trash_entries SET state = 'held', hold_reason = ?, version = version + 1, updated_at = ? WHERE id = ? AND version = ? AND state IN ('planned', 'held') AND active_operation IS NULL`, heldReason, formatTime(now), entry.ID, entry.Version)
			if updateErr != nil {
				return updateErr
			}
			if affected, affectedErr := updated.RowsAffected(); affectedErr != nil {
				return affectedErr
			} else if affected != 1 {
				return ErrClaimed
			}
		}
		priorOutcome := ""
		if outcomeOperation(janitor.OutcomeJson) == OperationTrash {
			// Keep the complete preceding action/reconciliation envelope. A
			// read-back is additive evidence and must not erase affected item
			// identities, action errors, timestamps, or scope diagnostics.
			priorOutcome = janitor.OutcomeJson
		}
		outcome, err := encodeTrashReconciliation(reconciliation, state, effects, priorOutcome)
		if err != nil {
			return fmt.Errorf("%w: encode trash reconciliation: %v", ErrStorage, err)
		}
		_, updateErr := queries.UpdateJanitorRecord(ctx, &sqlc.UpdateJanitorRecordParams{
			State:         stateForTrashReconciliation(state, janitor.State),
			NextAttemptAt: sql.NullString{},
			ClaimedBy:     sql.NullString{},
			LeaseUntil:    sql.NullString{},
			OutcomeJson:   outcome,
			UpdatedAt:     formatTime(now),
			ID:            janitor.ID,
			Version:       janitor.Version,
		})
		return updateErr
	})
}

func stateForTrashReconciliation(reconciliationState, current string) string {
	if reconciliationState == "pending" || reconciliationState == "materialized" {
		return current
	}
	return reconciliationState
}

func encodeTrashReconciliation(reconciliation trashReconciliation, state string, effects []ItemEffect, priorOutcome string) (string, error) {
	value := map[string]any{
		"operation":   OperationTrash,
		"state":       state,
		"disposition": reconciliation.Disposition,
		"observedAt":  reconciliation.ObservedAt,
		"items":       reconciliation.Items,
	}
	if len(reconciliation.Evidence) > 0 {
		value["evidence"] = reconciliation.Evidence
	}
	if len(effects) > 0 {
		value["effects"] = effects
	}
	if strings.TrimSpace(priorOutcome) != "" {
		if json.Valid([]byte(priorOutcome)) {
			// RawMessage preserves the prior package-owned outcome as a JSON
			// envelope. It remains queryable evidence without flattening or
			// silently dropping fields from the action result.
			value["priorOutcome"] = json.RawMessage(priorOutcome)
		} else {
			// This should not occur for package-generated outcomes, but retain
			// an opaque historical value rather than allowing evidence loss.
			value["priorOutcome"] = priorOutcome
		}
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}

func appendUniqueString(values []string, value string) []string {
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}

func (service *Service) listPlannedTrashEntries(ctx context.Context, limit int) ([]string, error) {
	if limit <= 0 {
		limit = service.maxItems
	}
	rows, err := service.store.DB().QueryContext(ctx, `SELECT id FROM trash_entries WHERE state = 'planned' ORDER BY updated_at, id LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("%w: list planned trash entries: %v", ErrStorage, err)
	}
	defer rows.Close()
	entries := make([]string, 0, limit)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("%w: scan planned trash entry: %v", ErrStorage, err)
		}
		entries = append(entries, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("%w: list planned trash entries: %v", ErrStorage, err)
	}
	return entries, nil
}

func (service *Service) recoverExpiredPlannedTrashClaim(ctx context.Context, now time.Time) error {
	if now.IsZero() {
		return ErrClock
	}
	// The frozen storage schema permits only purge/restore in active_operation.
	// A planned or held trash retry uses the purge slot as an entry-scoped
	// lease while its janitor row stores the trash operation. The ordinary
	// purge path never claims these states, so an expired retry lease can be
	// cleared safely for a fresh read-only reconciliation.
	_, err := service.store.DB().ExecContext(ctx, `UPDATE trash_entries SET active_operation = NULL, operation_claimed_by = NULL, operation_lease_until = NULL, version = version + 1, updated_at = ? WHERE state IN ('planned', 'held') AND active_operation = 'purge' AND (operation_lease_until IS NULL OR operation_lease_until <= ?)`, formatTime(now), formatTime(now))
	if err != nil {
		return fmt.Errorf("%w: recover planned trash lease: %v", ErrStorage, err)
	}
	return nil
}

func (service *Service) claimPlannedTrash(ctx context.Context, entryID string) (Entry, error) {
	now := service.now()
	if now.IsZero() {
		return Entry{}, ErrClock
	}
	leaseUntil := now.Add(service.leaseDuration)
	lock := service.lockFor("trash-claim:" + entryID)
	defer lock()
	if err := service.withTx(ctx, func(tx *sql.Tx, queries *sqlc.Queries) error {
		row, err := queries.GetTrashEntry(ctx, entryID)
		if err != nil {
			return err
		}
		if row.State != "planned" && row.State != "held" || row.ActiveOperation.Valid {
			return ErrClaimed
		}
		janitor, err := queries.GetJanitorRecord(ctx, &sqlc.GetJanitorRecordParams{TrashEntryID: entryID, Operation: string(OperationPurge)})
		if err != nil {
			return err
		}
		if approvalBindingPresent(janitor) {
			return ErrClaimed
		}
		if row.State == "planned" && janitor.State != "queued" {
			return ErrClaimed
		}
		if row.State == "held" && (janitor.State != "held" || outcomeOperation(janitor.OutcomeJson) != OperationTrash || outcomeHasUnresolvedScopeEvidence(janitor.OutcomeJson)) {
			return ErrClaimed
		}
		// Store the trash retry lease in the existing purge operation slot; the
		// database CHECK constraint intentionally has no third operation value.
		updated, err := tx.ExecContext(ctx, `UPDATE trash_entries SET active_operation = 'purge', operation_claimed_by = ?, operation_lease_until = ?, version = version + 1, updated_at = ? WHERE id = ? AND version = ? AND state IN ('planned', 'held') AND active_operation IS NULL`, service.workerID, formatTime(leaseUntil), formatTime(now), entryID, row.Version)
		if err != nil {
			return err
		}
		if affected, affectedErr := updated.RowsAffected(); affectedErr != nil {
			return affectedErr
		} else if affected != 1 {
			return ErrClaimed
		}
		outcome := appendReconciliationEvidence(janitor.OutcomeJson, []string{"trash_retry_claimed"})
		_, err = queries.UpdateJanitorRecord(ctx, &sqlc.UpdateJanitorRecordParams{
			State:       "queued",
			OutcomeJson: outcome,
			UpdatedAt:   formatTime(now),
			ID:          janitor.ID,
			Version:     janitor.Version,
		})
		return err
	}); err != nil {
		if errors.Is(err, ErrClaimed) {
			return Entry{}, err
		}
		if errors.Is(err, sql.ErrNoRows) {
			return Entry{}, fmt.Errorf("%w: planned trash entry", ErrNotFound)
		}
		return Entry{}, fmt.Errorf("%w: claim planned trash: %v", ErrStorage, err)
	}
	entry, err := service.loadEntry(persistenceContext(ctx), entryID)
	if err != nil {
		return Entry{}, err
	}
	return entry, nil
}

func (service *Service) dispatchPlannedTrash(ctx context.Context, claim Entry) (Result, error) {
	if err := service.validateContext(ctx); err != nil {
		return Result{Entry: claim}, err
	}
	if (claim.State != "planned" && claim.State != "held") || claim.ActiveOperation != OperationPurge || claim.ClaimedBy != service.workerID {
		return Result{Entry: claim}, ErrClaimed
	}
	pending := pendingTrashItems(claim)
	manifest, manifestErr := manifestForTrashItems(claim, pending)
	if manifestErr != nil {
		return service.finishClaimedTrash(ctx, claim, ports.FilesystemEffect{}, manifestErr)
	}
	if err := service.preflightSource(ctx, manifest); err != nil {
		return service.finishClaimedTrash(ctx, claim, ports.FilesystemEffect{}, err)
	}
	if claim.Client != nil {
		effect, observation, stopErr := service.ensureStopped(ctx, *claim.Client)
		if stopErr != nil {
			return service.finishClaimedTrash(ctx, claim, ports.FilesystemEffect{}, stopErr)
		}
		if err := service.persistClientObservation(ctx, claim.ID, observation, effect); err != nil {
			return Result{Entry: claim}, err
		}
		latest, loadErr := service.loadEntry(persistenceContext(ctx), claim.ID)
		if loadErr != nil {
			return Result{Entry: claim}, loadErr
		}
		claim = latest
	}
	if err := service.validateContext(ctx); err != nil {
		return service.finishClaimedTrash(ctx, claim, ports.FilesystemEffect{}, err)
	}
	effect, actionErr := service.action.Trash(ctx, ports.FilesystemTrashRequest{Files: manifest, Retention: claim.Retention})
	result, finishErr := service.finishClaimedTrash(ctx, claim, effect, actionErr)
	return result, errors.Join(actionErr, finishErr)
}

func (service *Service) finishClaimedTrash(ctx context.Context, claim Entry, effect ports.FilesystemEffect, actionErr error) (Result, error) {
	entry, loadErr := service.loadEntry(persistenceContext(ctx), claim.ID)
	if loadErr != nil {
		return Result{}, loadErr
	}
	if (entry.State != "planned" && entry.State != "held") || entry.ActiveOperation != OperationPurge || entry.ClaimedBy != service.workerID {
		return Result{Entry: entry}, ErrClaimed
	}
	pending := pendingTrashItems(entry)
	matched, scopeIssues := validateAffectedSet(pending, effect.Affected, operationTrash)
	terminalMatched := make(map[string]bool, len(matched))
	if effect.Outcome.Valid() {
		for itemID := range matched {
			terminalMatched[itemID] = true
		}
	}
	complete := actionErr == nil && effect.Outcome.Valid() && len(effect.Affected) > 0 && len(scopeIssues) == 0 && len(matched) == len(pending)
	now := service.now()
	if now.IsZero() {
		return Result{Entry: entry}, ErrClock
	}
	state := "held"
	reason := "trash_retry_incomplete"
	if complete {
		state = "trashed"
		reason = "trash_retry_completed"
	}
	hardScope := hasHardTrashScopeIssue(scopeIssues)
	if len(scopeIssues) > 0 {
		effect.Evidence = append(effect.Evidence, scopeIssues...)
	}
	if hardScope {
		effect.Evidence = appendUniqueString(effect.Evidence, "scope_unresolved")
	}
	if actionErr != nil {
		reason = "trash_retry: " + safeError(actionErr)
	} else if len(scopeIssues) > 0 {
		reason = "trash_retry_scope: " + strings.Join(scopeIssues, ",")
	} else if !effect.Outcome.Valid() {
		reason = "trash_retry_invalid_effect"
	}
	effects := make([]ItemEffect, 0, len(pending)+1)
	observedAt := effect.ObservedAt
	if observedAt.IsZero() {
		observedAt = now
		effect.ObservedAt = observedAt
	}
	for _, item := range pending {
		itemEffect := ItemEffect{ItemID: item.ID, Path: item.OriginalRelativePath, Operation: OperationTrash, State: "pending", Outcome: "", ObservedAt: observedAt, Evidence: []string{"trash_retry_pending"}}
		if terminalMatched[item.ID] {
			itemEffect.Path = item.TrashRelativePath
			itemEffect.State = "trashed"
			itemEffect.Outcome = effect.Outcome
			itemEffect.Evidence = append([]string{"trash_retry"}, effect.Evidence...)
		} else {
			itemEffect.Evidence = append(itemEffect.Evidence, trashScopeEvidenceForItem(item.ID, item.OriginalRelativePath, item.TrashRelativePath, scopeIssues)...)
		}
		effects = append(effects, itemEffect)
	}
	if hardScope {
		// Keep the exact per-item matches above while retaining a separate
		// aggregate marker for foreign, duplicate, or contradictory reports.
		effects = append(effects, ItemEffect{Operation: OperationTrash, State: "unknown", Outcome: effect.Outcome, ObservedAt: observedAt, Evidence: append([]string{"trash_retry_scope_unresolved"}, effect.Evidence...)})
	}
	outcome := effectOutcomeJSON(OperationTrash, state, reason, effects, effect)
	resultErr := service.withTx(persistenceContext(ctx), func(tx *sql.Tx, queries *sqlc.Queries) error {
		current, err := queries.GetTrashEntry(ctx, entry.ID)
		if err != nil {
			return err
		}
		if current.Version != entry.Version || current.State != "planned" && current.State != "held" || !current.ActiveOperation.Valid || current.ActiveOperation.String != string(OperationPurge) || !current.OperationClaimedBy.Valid || current.OperationClaimedBy.String != service.workerID {
			return ErrClaimed
		}
		for itemID := range terminalMatched {
			if _, err := tx.ExecContext(ctx, `UPDATE trash_items SET state = 'trashed', trashed_at = ? WHERE id = ? AND entry_id = ? AND state = 'selected'`, formatTime(now), itemID, entry.ID); err != nil {
				return err
			}
		}
		if complete {
			updated, err := tx.ExecContext(ctx, `UPDATE trash_entries SET state = 'trashed', trashed_at = ?, expires_at = ?, hold_reason = NULL, active_operation = NULL, operation_claimed_by = NULL, operation_lease_until = NULL, version = version + 1, updated_at = ? WHERE id = ? AND version = ? AND state IN ('planned', 'held') AND active_operation = 'purge' AND operation_claimed_by = ?`, formatTime(now), formatTime(now.Add(entry.Retention)), formatTime(now), entry.ID, entry.Version, service.workerID)
			if err != nil {
				return err
			}
			if affected, affectedErr := updated.RowsAffected(); affectedErr != nil {
				return affectedErr
			} else if affected != 1 {
				return ErrClaimed
			}
		} else {
			updated, err := tx.ExecContext(ctx, `UPDATE trash_entries SET state = 'held', hold_reason = ?, active_operation = NULL, operation_claimed_by = NULL, operation_lease_until = NULL, version = version + 1, updated_at = ? WHERE id = ? AND version = ? AND state IN ('planned', 'held') AND active_operation = 'purge' AND operation_claimed_by = ?`, reason, formatTime(now), entry.ID, entry.Version, service.workerID)
			if err != nil {
				return err
			}
			if affected, affectedErr := updated.RowsAffected(); affectedErr != nil {
				return affectedErr
			} else if affected != 1 {
				return ErrClaimed
			}
		}
		janitor, err := queries.GetJanitorRecord(ctx, &sqlc.GetJanitorRecordParams{TrashEntryID: entry.ID, Operation: string(OperationPurge)})
		if err != nil {
			return err
		}
		_, err = queries.UpdateJanitorRecord(ctx, &sqlc.UpdateJanitorRecordParams{State: stateForTrashRetry(state), OutcomeJson: outcome, UpdatedAt: formatTime(now), ID: janitor.ID, Version: janitor.Version})
		return err
	})
	latest, latestErr := service.loadEntry(persistenceContext(ctx), entry.ID)
	if latestErr != nil {
		return Result{}, errors.Join(resultErr, latestErr)
	}
	result := Result{Entry: latest, Effects: effects, Evidence: []string{reason}}
	if complete {
		result.Outcome = effect.Outcome
		return result, resultErr
	}
	result.Evidence = append(result.Evidence, "trash_retry_held")
	return result, errors.Join(ErrHeld, resultErr)
}

func hasHardTrashScopeIssue(issues []string) bool {
	for _, issue := range issues {
		for _, prefix := range []string{"affected_foreign:", "affected_ambiguous:", "affected_duplicate:", "affected_identity_changed:"} {
			if strings.HasPrefix(issue, prefix) {
				return true
			}
		}
	}
	return false
}

func trashScopeEvidenceForItem(itemID, originalPath, trashPath string, issues []string) []string {
	result := make([]string, 0, 2)
	for _, issue := range issues {
		if issue == "affected_missing:"+itemID || strings.HasSuffix(issue, ":"+originalPath) || strings.HasSuffix(issue, ":"+trashPath) {
			result = append(result, issue)
		}
	}
	return result
}

func stateForTrashRetry(state string) string {
	if state == "trashed" {
		return "queued"
	}
	return "held"
}

// reconcileBeforePurgeDispatch is the read-before-write boundary for an
// approval-bound purge. The claim established a lease only; it does not prove
// that the stored payload is still the approved object. A failed or ambiguous
// read releases that lease back to durable reconciliation, retaining the
// original attempt evidence so a later worker cannot blindly delete.
func (service *Service) reconcileBeforePurgeDispatch(ctx context.Context, claim *sqlc.JanitorRecord) (Result, error) {
	if claim == nil || claim.Operation != string(OperationPurge) {
		return Result{}, fmt.Errorf("%w: purge reconciliation claim is invalid", ErrConflict)
	}
	reconciled, reconcileErr := service.Reconcile(ctx, claim.TrashEntryID, OperationPurge)
	if reconcileErr == nil && reconciled.Retryable {
		return reconciled, nil
	}
	if reconcileErr == nil {
		reconcileErr = ErrHeld
	}
	releaseErr := service.releaseClaimForReconciliation(ctx, claim, reconciliationEvidence(reconciled))
	latest, loadErr := service.loadEntry(persistenceContext(ctx), claim.TrashEntryID)
	if loadErr == nil {
		reconciled.Entry = latest
	}
	if loadErr != nil {
		releaseErr = errors.Join(releaseErr, loadErr)
	}
	return reconciled, errors.Join(reconcileErr, releaseErr)
}

func reconciliationEvidence(result Result) []string {
	evidence := append([]string(nil), result.Evidence...)
	for _, effect := range result.Effects {
		evidence = append(evidence, effect.Evidence...)
	}
	return evidence
}

// releaseClaimForReconciliation performs an owner/version-checked transition
// from a claimed approved purge to reconciling. The migration trigger updates
// the coupled action run and trash-entry lease in the same SQLite transaction.
// Keeping this transition durable is what makes a lost response safe across a
// fresh process.
func (service *Service) releaseClaimForReconciliation(ctx context.Context, claim *sqlc.JanitorRecord, evidence []string) error {
	if claim == nil || claim.Operation != string(OperationPurge) || !claim.ClaimedBy.Valid || claim.ClaimedBy.String != service.workerID {
		return fmt.Errorf("%w: purge reconciliation lease is not owned", ErrClaimed)
	}
	now := service.now()
	if now.IsZero() {
		return ErrClock
	}
	outcome := appendReconciliationEvidence(claim.OutcomeJson, evidence)
	err := service.withTx(ctx, func(tx *sql.Tx, queries *sqlc.Queries) error {
		current, getErr := queries.GetJanitorRecord(ctx, &sqlc.GetJanitorRecordParams{TrashEntryID: claim.TrashEntryID, Operation: string(OperationPurge)})
		if getErr != nil {
			return getErr
		}
		if current.ID != claim.ID || current.Version != claim.Version || current.State != "running" || !current.ClaimedBy.Valid || current.ClaimedBy.String != service.workerID {
			return ErrClaimed
		}
		if approvalBindingPresent(claim) {
			approval, approvalErr := approvalFromJanitor(claim)
			if approvalErr != nil {
				return approvalErr
			}
			if matchErr := approvalMatchesJanitor(approval, current); matchErr != nil {
				return matchErr
			}
		}
		updated, updateErr := queries.UpdateJanitorRecord(ctx, &sqlc.UpdateJanitorRecordParams{
			State:         "reconciling",
			NextAttemptAt: nullableTime(true, now.Add(service.retryAfter)),
			ClaimedBy:     sql.NullString{},
			LeaseUntil:    sql.NullString{},
			OutcomeJson:   outcome,
			UpdatedAt:     formatTime(now),
			ID:            claim.ID,
			Version:       claim.Version,
		})
		if updateErr != nil {
			return updateErr
		}
		if updated == nil {
			return ErrClaimed
		}
		return nil
	})
	if errors.Is(err, ErrClaimed) || errors.Is(err, ErrConflict) {
		return err
	}
	if err != nil {
		return fmt.Errorf("%w: release purge reconciliation lease: %v", ErrStorage, safeError(err))
	}
	return nil
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
	if operation != OperationPurge && operation != OperationRestore && operation != OperationTrash {
		return Result{}, fmt.Errorf("%w: operation", ErrInvalidRequest)
	}
	entry, err := service.loadEntry(ctx, entryID)
	if err != nil {
		return Result{}, err
	}
	if operation == OperationTrash {
		return service.ReconcileTrash(ctx, entryID)
	}
	if err := service.validateEntryClock(entry); err != nil {
		return resultForEntry(entry, "clock_unknown"), err
	}
	if record, recordErr := service.store.Queries().GetJanitorRecord(ctx, &sqlc.GetJanitorRecordParams{TrashEntryID: entryID, Operation: string(operation)}); recordErr == nil && outcomeHasUnresolvedScopeEvidence(record.OutcomeJson) {
		// A scope-invalid effect is durable unresolved evidence. Even if the
		// approved item is no longer present, a read of the remaining payload
		// cannot prove what the extra/contradictory effect touched.
		return Result{Entry: entry, Evidence: []string{"scope_evidence_requires_manual_review"}}, ErrHeld
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
	if request.Operation == OperationTrash {
		return service.RetryTrash(ctx, TrashRetryRequest{EntryID: entryID})
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
	leafCount := 0
	seen := make(map[string]struct{}, len(flat))
	for _, entry := range flat {
		if entry.Type != domain.ManifestDirectory {
			leafCount++
		}
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
	if leafCount == 0 {
		return TrashRequest{}, nil, nil, "", "", fmt.Errorf("%w: manifest has no file or subtitle leaves", ErrInvalidRequest)
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
			// Directory rows are represented by their immutable expanded child
			// scope. Persisting the directory root as an actionable item would
			// leave it selected when a safe filesystem port reports each child.
			if item.Type == domain.ManifestDirectory {
				continue
			}
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
	matched, scopeIssues := validateAffectedSet(entry.Items, effect.Affected, operationTrash)
	all := len(scopeIssues) == 0 && len(matched) > 0
	if actionErr != nil || !effect.Outcome.Valid() || len(effect.Affected) == 0 || !all {
		reason := "filesystem_trash_incomplete"
		if actionErr != nil {
			reason = "filesystem_trash: " + safeError(actionErr)
		} else if len(scopeIssues) > 0 {
			reason = "filesystem_trash_scope: " + strings.Join(scopeIssues, ",")
		} else if !effect.Outcome.Valid() {
			reason = "filesystem_trash_invalid_effect"
		} else if len(effect.Affected) == 0 {
			reason = "filesystem_trash_missing_affected_items"
		}
		if len(scopeIssues) > 0 {
			effect.Evidence = append(effect.Evidence, "scope_unresolved")
			effect.Evidence = append(effect.Evidence, scopeIssues...)
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
	state := make(map[string]any)
	if raw := entryClientState(entry); raw != "{}" {
		if json.Unmarshal([]byte(raw), &state) != nil || state == nil {
			state = make(map[string]any)
		}
	}
	state["state"] = observation.State
	state["seeding"] = observation.Seeding
	state["observedAt"] = formatTime(observation.ObservedAt)
	state["stop"] = map[string]any{"outcome": effect.Outcome, "operationId": effect.OperationID, "evidence": append([]string(nil), effect.Evidence...)}
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

func (service *Service) claim(ctx context.Context, entryID string, operation Operation, hard bool, approval *EarlyPurgeApproval) (*sqlc.JanitorRecord, error) {
	if operation != OperationPurge && operation != OperationRestore {
		return nil, fmt.Errorf("%w: operation", ErrInvalidRequest)
	}
	now := service.now()
	if now.IsZero() {
		return nil, ErrClock
	}
	leaseUntil := now.Add(service.leaseDuration)
	lock := service.lockFor("claim:" + entryID)
	defer lock()
	if hard && operation == OperationPurge {
		return service.claimApprovedEarlyPurge(ctx, entryID, approval, now, leaseUntil)
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

func validateEarlyPurgeApproval(approval *EarlyPurgeApproval) error {
	if approval == nil {
		return fmt.Errorf("%w: early purge approval is required", ErrConflict)
	}
	if strings.TrimSpace(approval.PlanID) == "" || approval.PlanRevision <= 0 || strings.TrimSpace(approval.PlanDigest) == "" || strings.TrimSpace(approval.DecisionID) == "" || strings.TrimSpace(approval.ActionRunID) == "" || approval.ApprovedEntryVersion <= 0 || approval.ActionRunVersion <= 0 {
		return fmt.Errorf("%w: early purge approval binding is incomplete", ErrConflict)
	}
	for _, value := range []string{approval.PlanID, approval.PlanDigest, approval.DecisionID, approval.ActionRunID} {
		if len(value) > 256 || strings.ContainsRune(value, 0) || strings.TrimSpace(value) != value {
			return fmt.Errorf("%w: early purge approval binding is invalid", ErrConflict)
		}
	}
	return nil
}

func (service *Service) claimApprovedEarlyPurge(ctx context.Context, entryID string, approval *EarlyPurgeApproval, now, leaseUntil time.Time) (*sqlc.JanitorRecord, error) {
	if err := validateEarlyPurgeApproval(approval); err != nil {
		return nil, err
	}
	entry, err := service.loadEntry(ctx, entryID)
	if err != nil {
		return nil, err
	}
	janitor, err := service.store.Queries().GetJanitorRecord(ctx, &sqlc.GetJanitorRecordParams{TrashEntryID: entryID, Operation: string(OperationPurge)})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("%w: purge record is missing", ErrConflict)
		}
		return nil, fmt.Errorf("%w: read purge record: %v", ErrStorage, err)
	}
	if approvalBindingPresent(janitor) {
		if err := approvalMatchesJanitor(approval, janitor); err != nil {
			return nil, err
		}
		switch janitor.State {
		case "reconciling":
			return service.claimApprovedEarlyPurgeReconciliation(ctx, janitor, approval)
		case "running":
			return nil, ErrClaimed
		default:
			return nil, fmt.Errorf("%w: approved purge record is %s", ErrConflict, janitor.State)
		}
	}
	// A fresh process may be retrying the same immutable approval after the
	// running claim was recovered. The shared entry generation has advanced as
	// part of that recovery; the approval-time version is checked by the
	// reconciliation CAS below, not against the current projection here.
	if approval.ApprovedEntryVersion != entry.Version {
		return nil, fmt.Errorf("%w: approved trash entry version is stale", ErrConflict)
	}
	var claimed *sqlc.JanitorRecord
	err = service.withTx(ctx, func(tx *sql.Tx, queries *sqlc.Queries) error {
		current, getErr := queries.GetJanitorRecord(ctx, &sqlc.GetJanitorRecordParams{TrashEntryID: entryID, Operation: string(OperationPurge)})
		if getErr != nil {
			return getErr
		}
		if approvalBindingPresent(current) || current.State != "queued" {
			return fmt.Errorf("%w: purge record is already bound or claimed", ErrConflict)
		}
		claimed, err = queries.ClaimApprovedEarlyPurge(ctx, &sqlc.ClaimApprovedEarlyPurgeParams{
			WorkerID:                 sql.NullString{String: service.workerID, Valid: true},
			LeaseUntil:               sql.NullString{String: formatTime(leaseUntil), Valid: true},
			ApprovalPlanID:           sql.NullString{String: approval.PlanID, Valid: true},
			ApprovalPlanRevision:     sql.NullInt64{Int64: approval.PlanRevision, Valid: true},
			ApprovalPlanDigest:       sql.NullString{String: approval.PlanDigest, Valid: true},
			ApprovalDecisionID:       sql.NullString{String: approval.DecisionID, Valid: true},
			ApprovalActionRunID:      sql.NullString{String: approval.ActionRunID, Valid: true},
			ApprovedEntryVersion:     sql.NullInt64{Int64: approval.ApprovedEntryVersion, Valid: true},
			ApprovalActionRunVersion: sql.NullInt64{Int64: approval.ActionRunVersion, Valid: true},
			Now:                      formatTime(now),
			ID:                       janitor.ID,
			Version:                  janitor.Version,
			TrashEntryID:             entryID,
		})
		return err
	})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("%w: exact early purge approval claim rejected", ErrConflict)
		}
		if errors.Is(err, ErrConflict) {
			return nil, err
		}
		// SQLite exposes trigger failures as ordinary constraint errors. The
		// approval path deliberately treats a failed immutable binding as a
		// caller conflict, while reserving ErrStorage for an unavailable
		// journal. Do not let an invalid action/decision identity look like a
		// transient storage failure that could be retried blindly.
		message := strings.ToLower(err.Error())
		if strings.Contains(message, "early purge") || strings.Contains(message, "approval") {
			return nil, fmt.Errorf("%w: exact early purge approval claim rejected", ErrConflict)
		}
		return nil, fmt.Errorf("%w: approved early purge claim: %v", ErrStorage, safeError(err))
	}
	return claimed, nil
}

func approvalBindingPresent(record *sqlc.JanitorRecord) bool {
	if record == nil {
		return false
	}
	return record.ApprovalPlanID.Valid || record.ApprovalPlanRevision.Valid || record.ApprovalPlanDigest.Valid || record.ApprovalDecisionID.Valid || record.ApprovalActionRunID.Valid || record.ApprovedEntryVersion.Valid || record.ApprovalActionRunVersion.Valid
}

func approvalFromJanitor(record *sqlc.JanitorRecord) (*EarlyPurgeApproval, error) {
	if record == nil || !approvalBindingPresent(record) || !record.ApprovalPlanID.Valid || !record.ApprovalPlanRevision.Valid || !record.ApprovalPlanDigest.Valid || !record.ApprovalDecisionID.Valid || !record.ApprovalActionRunID.Valid || !record.ApprovedEntryVersion.Valid || !record.ApprovalActionRunVersion.Valid {
		return nil, fmt.Errorf("%w: stored early purge approval binding is incomplete", ErrConflict)
	}
	approval := &EarlyPurgeApproval{
		PlanID:               record.ApprovalPlanID.String,
		PlanRevision:         record.ApprovalPlanRevision.Int64,
		PlanDigest:           record.ApprovalPlanDigest.String,
		DecisionID:           record.ApprovalDecisionID.String,
		ActionRunID:          record.ApprovalActionRunID.String,
		ApprovedEntryVersion: record.ApprovedEntryVersion.Int64,
		ActionRunVersion:     record.ApprovalActionRunVersion.Int64,
	}
	if err := validateEarlyPurgeApproval(approval); err != nil {
		return nil, err
	}
	return approval, nil
}

func approvalMatchesJanitor(approval *EarlyPurgeApproval, record *sqlc.JanitorRecord) error {
	if err := validateEarlyPurgeApproval(approval); err != nil {
		return err
	}
	stored, err := approvalFromJanitor(record)
	if err != nil {
		return err
	}
	if *stored != *approval {
		return fmt.Errorf("%w: approved purge binding differs from durable record", ErrConflict)
	}
	return nil
}

// claimApprovedEarlyPurgeReconciliation reacquires the exact immutable
// approval binding after startup/lease recovery. This CAS only establishes a
// bounded read-only reconciliation lease; callers must perform a fresh
// Reconcile before dispatching any payload mutation.
func (service *Service) claimApprovedEarlyPurgeReconciliation(ctx context.Context, record *sqlc.JanitorRecord, requested *EarlyPurgeApproval) (*sqlc.JanitorRecord, error) {
	if record == nil || record.State != "reconciling" || record.Operation != string(OperationPurge) {
		return nil, fmt.Errorf("%w: approved purge is not reconciling", ErrConflict)
	}
	approval, err := approvalFromJanitor(record)
	if err != nil {
		return nil, err
	}
	if requested != nil {
		if err := approvalMatchesJanitor(requested, record); err != nil {
			return nil, err
		}
	}
	now := service.now()
	if now.IsZero() {
		return nil, ErrClock
	}
	leaseUntil := now.Add(service.leaseDuration)
	claimed, err := service.store.Queries().ClaimApprovedEarlyPurgeReconciliation(ctx, &sqlc.ClaimApprovedEarlyPurgeReconciliationParams{
		WorkerID:                 sql.NullString{String: service.workerID, Valid: true},
		LeaseUntil:               sql.NullString{String: formatTime(leaseUntil), Valid: true},
		Now:                      formatTime(now),
		ID:                       record.ID,
		Version:                  record.Version,
		TrashEntryID:             record.TrashEntryID,
		ApprovalPlanID:           sql.NullString{String: approval.PlanID, Valid: true},
		ApprovalPlanRevision:     sql.NullInt64{Int64: approval.PlanRevision, Valid: true},
		ApprovalPlanDigest:       sql.NullString{String: approval.PlanDigest, Valid: true},
		ApprovalDecisionID:       sql.NullString{String: approval.DecisionID, Valid: true},
		ApprovalActionRunID:      sql.NullString{String: approval.ActionRunID, Valid: true},
		ApprovedEntryVersion:     sql.NullInt64{Int64: approval.ApprovedEntryVersion, Valid: true},
		ApprovalActionRunVersion: sql.NullInt64{Int64: approval.ActionRunVersion, Valid: true},
	})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("%w: exact approved purge reconciliation claim rejected", ErrConflict)
		}
		message := strings.ToLower(err.Error())
		if strings.Contains(message, "approval") || strings.Contains(message, "reconcil") || strings.Contains(message, "generation") || strings.Contains(message, "fence") {
			return nil, fmt.Errorf("%w: exact approved purge reconciliation claim rejected", ErrConflict)
		}
		return nil, fmt.Errorf("%w: approved purge reconciliation claim: %v", ErrStorage, safeError(err))
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
		matchedItems, scopeIssues := validateAffectedSet([]Item{item}, effect.Affected, OperationPurge)
		if len(scopeIssues) > 0 {
			effect.Evidence = append(effect.Evidence, "scope_unresolved")
			effect.Evidence = append(effect.Evidence, scopeIssues...)
		}
		// A returned error, invalid outcome, or any scope issue means the
		// affected set is unresolved. Even an exact item in an
		// exact-plus-foreign response must remain non-terminal until a
		// later read-only reconciliation proves the whole effect safe.
		matchedThis := actionErr == nil && effect.Outcome.Valid() && len(scopeIssues) == 0 && matchedItems[item.ID]
		if matchedThis {
			matched[item.ID] = true
			effects = append(effects, ItemEffect{ItemID: item.ID, Path: item.TrashRelativePath, Operation: OperationPurge, State: "purged", Outcome: effect.Outcome, ObservedAt: effect.ObservedAt, Evidence: append([]string{"payload_delete"}, effect.Evidence...)})
		} else if len(effect.Affected) > 0 || len(scopeIssues) > 0 {
			effects = append(effects, ItemEffect{ItemID: item.ID, Path: item.TrashRelativePath, Operation: OperationPurge, State: "unknown", Outcome: effect.Outcome, ObservedAt: effect.ObservedAt, Evidence: append([]string{"payload_delete_unmatched"}, effect.Evidence...)})
		}
		if actionErr != nil || !matchedThis || !effect.Outcome.Valid() || len(scopeIssues) > 0 {
			reason := "payload_delete_incomplete"
			if actionErr != nil {
				reason = "payload_delete: " + safeError(actionErr)
			} else if len(scopeIssues) > 0 {
				reason = "payload_delete_scope: " + strings.Join(scopeIssues, ",")
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
		matchedItems, scopeIssues := validateAffectedSet([]Item{item}, effect.Affected, OperationRestore)
		matched := matchedItems[item.ID]
		if len(scopeIssues) > 0 {
			effect.Evidence = append(effect.Evidence, "scope_unresolved")
			effect.Evidence = append(effect.Evidence, scopeIssues...)
		}
		if actionErr != nil || !matched || !effect.Outcome.Valid() || len(scopeIssues) > 0 {
			reason := "restore_incomplete"
			if actionErr != nil {
				reason = "restore: " + safeError(actionErr)
			} else if len(scopeIssues) > 0 {
				reason = "restore_scope: " + strings.Join(scopeIssues, ",")
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
	entry := Entry{ID: row.ID, RootID: domain.ConfigID(row.RootID), State: row.State, OriginalPrefix: row.OriginalPrefix, TrashPrefix: row.TrashPrefix, Manifest: cloneManifest(manifest), Retention: time.Duration(row.RetentionSeconds) * time.Second, Version: row.Version, Items: make([]Item, 0, len(items)), clientStateJSON: row.ClientStateJson}
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

// validateAffectedSet maps an action response back to the immutable item
// identities. It requires semantic set equality: every eligible item appears
// exactly once, every reported identity agrees with its manifest, and no
// foreign or extra object is silently discarded. Trash accepts either the
// original or mapped trash spelling because upstream filesystem ports may
// report the source or published path; purge and restore each have one exact
// path namespace.
func validateAffectedSet(items []Item, affected []domain.FileManifestEntry, operation Operation) (map[string]bool, []string) {
	paths := make(map[string][]Item)
	for _, item := range items {
		if item.Type == domain.ManifestDirectory || item.State == "purged" || item.State == "restored" {
			continue
		}
		candidates := []string{item.TrashRelativePath}
		if operation == operationTrash {
			candidates = append(candidates, item.OriginalRelativePath)
		} else if operation == OperationRestore {
			candidates = []string{item.OriginalRelativePath}
		}
		for _, relativePath := range candidates {
			key := string(item.RootID) + "\x00" + relativePath
			paths[key] = append(paths[key], item)
		}
	}
	matched := make(map[string]bool)
	issues := make([]string, 0)
	for _, observed := range affected {
		key := string(observed.RootID) + "\x00" + observed.RelativePath
		candidates := paths[key]
		if len(candidates) == 0 {
			issues = append(issues, "affected_foreign:"+observed.RelativePath)
			continue
		}
		if len(candidates) != 1 {
			issues = append(issues, "affected_ambiguous:"+observed.RelativePath)
			continue
		}
		item := candidates[0]
		if matched[item.ID] {
			issues = append(issues, "affected_duplicate:"+observed.RelativePath)
			continue
		}
		if identityErr := identityMatches(observed, identityForItemAt(item, observed.RelativePath)); identityErr != nil {
			issues = append(issues, "affected_identity_changed:"+observed.RelativePath)
			continue
		}
		matched[item.ID] = true
	}
	for _, item := range items {
		if item.Type == domain.ManifestDirectory || item.State == "purged" || item.State == "restored" {
			continue
		}
		if !matched[item.ID] {
			issues = append(issues, "affected_missing:"+item.ID)
		}
	}
	sort.Strings(issues)
	return matched, issues
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

// appendReconciliationEvidence augments a durable outcome without replacing
// the original handler evidence. Outcomes are JSON envelopes owned by this
// package; invalid historical JSON is retained as an opaque prior value and
// cannot be mistaken for a clean result.
func appendReconciliationEvidence(raw string, evidence []string) string {
	var value map[string]any
	if err := json.Unmarshal([]byte(raw), &value); err != nil || value == nil {
		value = map[string]any{
			"operation":    OperationPurge,
			"state":        "reconciling",
			"priorOutcome": raw,
		}
	}
	if state, ok := value["state"]; ok && state != "reconciling" {
		value["priorState"] = state
	}
	value["state"] = "reconciling"
	if len(evidence) == 0 {
		encoded, err := json.Marshal(value)
		if err != nil {
			return outcomeJSON(OperationPurge, "reconciling", "reconciliation evidence unavailable", nil, raw)
		}
		return string(encoded)
	}
	seen := make(map[string]struct{}, len(evidence))
	merged := make([]string, 0, len(evidence))
	if previous, ok := value["reconciliationEvidence"].([]any); ok {
		for _, item := range previous {
			text, ok := item.(string)
			if !ok || text == "" {
				continue
			}
			if _, exists := seen[text]; !exists {
				seen[text] = struct{}{}
				merged = append(merged, text)
			}
		}
	}
	for _, item := range evidence {
		if item == "" {
			continue
		}
		if _, exists := seen[item]; !exists {
			seen[item] = struct{}{}
			merged = append(merged, item)
		}
	}
	value["reconciliationEvidence"] = merged
	encoded, err := json.Marshal(value)
	if err != nil {
		return outcomeJSON(OperationPurge, "reconciling", "reconciliation evidence unavailable", evidence, raw)
	}
	return string(encoded)
}

func outcomeHasUnresolvedScopeEvidence(raw string) bool {
	if strings.TrimSpace(raw) == "" {
		return false
	}
	var value any
	if json.Unmarshal([]byte(raw), &value) != nil {
		return false
	}
	return containsOutcomeString(value, "scope_unresolved")
}

func containsOutcomeString(value any, target string) bool {
	switch typed := value.(type) {
	case string:
		return typed == target
	case []any:
		for _, item := range typed {
			if containsOutcomeString(item, target) {
				return true
			}
		}
	case map[string]any:
		for _, item := range typed {
			if containsOutcomeString(item, target) {
				return true
			}
		}
	}
	return false
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
	if strings.TrimSpace(entry.clientStateJSON) != "" {
		var value any
		if json.Unmarshal([]byte(entry.clientStateJSON), &value) == nil && value != nil {
			return entry.clientStateJSON
		}
	}
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

func (service *Service) replayTrashResult(result Result) (Result, error) {
	switch result.Entry.State {
	case "trashed":
		result.Outcome = domain.OutcomeAlreadySatisfied
		result.Evidence = append(result.Evidence, "trash_materialized")
		return result, nil
	case "planned":
		result.Evidence = append(result.Evidence, "trash_materialization_pending")
		return result, ErrClaimed
	case "held":
		result.Evidence = append(result.Evidence, "trash_replay_held")
		return result, ErrHeld
	case "failed":
		result.Evidence = append(result.Evidence, "trash_replay_failed")
		return result, fmt.Errorf("%w: previous trash operation failed", ErrConflict)
	case "purging", "restoring":
		result.Evidence = append(result.Evidence, "trash_operation_in_progress")
		return result, ErrClaimed
	case "restored", "purged":
		result.Evidence = append(result.Evidence, "trash_materialization_not_present")
		return result, fmt.Errorf("%w: trash entry is %s", ErrConflict, result.Entry.State)
	default:
		return result, fmt.Errorf("%w: unknown trash entry state", ErrCorrupt)
	}
}

func resultForEffect(entry Entry, operation Operation, effect ports.FilesystemEffect, matched map[string]bool) Result {
	effects := make([]ItemEffect, 0, len(matched))
	for itemID := range matched {
		effects = append(effects, ItemEffect{ItemID: itemID, Operation: operation, Outcome: effect.Outcome, ObservedAt: effect.ObservedAt, Evidence: append([]string(nil), effect.Evidence...)})
	}
	sort.Slice(effects, func(i, j int) bool { return effects[i].ItemID < effects[j].ItemID })
	outcome := effect.Outcome
	if entry.State != "trashed" {
		outcome = ""
	}
	return Result{Entry: entry, Outcome: outcome, Effects: effects, Evidence: append([]string(nil), effect.Evidence...)}
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
