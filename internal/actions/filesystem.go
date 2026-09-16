package actions

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"strings"
	"time"

	"github.com/guilycst/mastarr/internal/domain"
	"github.com/guilycst/mastarr/internal/execution"
	"github.com/guilycst/mastarr/internal/filesystem/organize"
	"github.com/guilycst/mastarr/internal/filesystem/placement"
	"github.com/guilycst/mastarr/internal/planning"
	"github.com/guilycst/mastarr/internal/ports"
)

const (
	executorMastarr      = "mastarr"
	executorNativeClient = "native_client"

	copyOperation     = "fs.copy"
	hardlinkOperation = "fs.hardlink"
	moveOperation     = "fs.move"
	renameOperation   = "fs.rename"
	trashOperation    = "fs.trash"
	restoreOperation  = "fs.restore"
	deleteOperation   = "fs.delete"
	defaultRetention  = 30 * 24 * time.Hour
)

// FilesystemConfig wires the read and action ports used by each independent
// filesystem handler. The read port is required even when the action port
// performs its own checks: direct calls must observe before dispatching.
type FilesystemConfig struct {
	Read    ports.FilesystemReadPort
	Action  ports.FilesystemActionPort
	Options HandlerOptions
}

// FilesystemHandlers groups independent handlers for convenient startup
// registration. Each field still implements execution.Handler separately.
type FilesystemHandlers struct {
	Copy     *FilesystemHandler
	Hardlink *FilesystemHandler
	Move     *FilesystemHandler
	Rename   *FilesystemHandler
	Trash    *FilesystemHandler
	Restore  *FilesystemHandler
	Delete   *FilesystemHandler
}

// NewFilesystemHandlers constructs all seven filesystem action handlers.
func NewFilesystemHandlers(config FilesystemConfig) (FilesystemHandlers, error) {
	if config.Read == nil || config.Action == nil {
		return FilesystemHandlers{}, fmt.Errorf("%w: filesystem read/action ports", ErrDependencyRequired)
	}
	options := config.Options.normalized()
	newHandler := func(kind domain.ActionKind, operation string) *FilesystemHandler {
		return &FilesystemHandler{baseHandler: baseHandler{kind: kind, options: options}, read: config.Read, action: config.Action, operation: operation}
	}
	return FilesystemHandlers{
		Copy: newHandler(domain.ActionFSCopy, copyOperation), Hardlink: newHandler(domain.ActionFSHardlink, hardlinkOperation),
		Move: newHandler(domain.ActionFSMove, moveOperation), Rename: newHandler(domain.ActionFSRename, renameOperation),
		Trash: newHandler(domain.ActionFSTrash, trashOperation), Restore: newHandler(domain.ActionFSRestore, restoreOperation),
		Delete: newHandler(domain.ActionFSDelete, deleteOperation),
	}, nil
}

// NewCopyHandler constructs one copy handler.
func NewCopyHandler(config FilesystemConfig) (*FilesystemHandler, error) {
	handlers, err := NewFilesystemHandlers(config)
	return handlers.Copy, err
}

// NewHardlinkHandler constructs one hardlink handler.
func NewHardlinkHandler(config FilesystemConfig) (*FilesystemHandler, error) {
	handlers, err := NewFilesystemHandlers(config)
	return handlers.Hardlink, err
}

// NewMoveHandler constructs one same-filesystem move handler.
func NewMoveHandler(config FilesystemConfig) (*FilesystemHandler, error) {
	handlers, err := NewFilesystemHandlers(config)
	return handlers.Move, err
}

// NewRenameHandler constructs one same-filesystem rename handler.
func NewRenameHandler(config FilesystemConfig) (*FilesystemHandler, error) {
	handlers, err := NewFilesystemHandlers(config)
	return handlers.Rename, err
}

// NewTrashHandler constructs one exact-manifest trash handler.
func NewTrashHandler(config FilesystemConfig) (*FilesystemHandler, error) {
	handlers, err := NewFilesystemHandlers(config)
	return handlers.Trash, err
}

// NewRestoreHandler constructs one exact-map restore handler.
func NewRestoreHandler(config FilesystemConfig) (*FilesystemHandler, error) {
	handlers, err := NewFilesystemHandlers(config)
	return handlers.Restore, err
}

// NewDeleteHandler constructs one exact-manifest permanent-delete handler.
func NewDeleteHandler(config FilesystemConfig) (*FilesystemHandler, error) {
	handlers, err := NewFilesystemHandlers(config)
	return handlers.Delete, err
}

// FilesystemHandler implements exactly one filesystem action kind.
type FilesystemHandler struct {
	baseHandler
	read      ports.FilesystemReadPort
	action    ports.FilesystemActionPort
	operation string
}

var _ execution.Handler = (*FilesystemHandler)(nil)

// Reservations binds source and destination paths to this action. Directory
// descendants are represented by their exact requested parent; the executor's
// reservation convention treats a parent key as overlapping its children.
func (handler *FilesystemHandler) Reservations(action execution.Action) []string {
	intent, err := fileIntentFromJSON(action.DesiredState)
	if err != nil {
		return nil
	}
	keys := make([]string, 0, len(intent.Files)*2+len(intent.Manifest)+1)
	if intent.LinkedDownload != nil {
		if err := validateRef(*intent.LinkedDownload); err == nil {
			// Client stop/remove actions reserve the same identity. Keeping this
			// key on linked filesystem actions serializes the prerequisite and
			// prevents a concurrent client mutation from invalidating the plan.
			keys = append(keys, "client:"+refID(*intent.LinkedDownload))
		}
	}
	for _, mapping := range intent.Files {
		keys = append(keys, "path:"+targetID(sourceTarget(mapping)), "path:"+targetID(mapping.Destination))
	}
	for _, entry := range intent.Manifest {
		keys = append(keys, "path:"+targetID(domain.FileTarget{RootID: entry.RootID, RelativePath: entry.RelativePath}))
	}
	return keys
}

// Observe performs exact source/destination read-back. It never treats a
// missing source as proof that a trash or restore effect happened.
func (handler *FilesystemHandler) Observe(ctx context.Context, action execution.Action) (execution.Observation, error) {
	if err := handler.validateAction(action); err != nil {
		return execution.Observation{}, err
	}
	if err := handler.guardObserve(ctx); err != nil {
		return execution.Observation{}, err
	}
	intent, err := fileIntentFromJSON(action.DesiredState)
	if err != nil {
		return execution.Observation{}, err
	}
	if err := validateFileIntent(handler.kind, intent, handler.options.MaxFiles); err != nil {
		return execution.Observation{}, err
	}
	if err := validateNativeClientScope(handler.kind, intent); err != nil {
		// The native client can rename only within the source root and its
		// containing directory. Keep this structural rejection ahead of the
		// linked-client read so an invalid scope cannot trigger any native call.
		return execution.Observation{}, err
	}
	if err := handler.checkLinkedClient(ctx, intent.LinkedDownload); err != nil {
		if errors.Is(err, ErrStateUnknown) {
			return execution.Observation{State: execution.ObserveUnknown, Evidence: []string{"linked_client_not_stopped"}}, nil
		}
		return execution.Observation{}, handler.failure(err, false)
	}
	if handler.kind == domain.ActionFSTrash {
		return handler.observeTrash(ctx, intent)
	}
	if handler.kind == domain.ActionFSDelete {
		return handler.observeDelete(ctx, intent)
	}
	mappings, err := expandedMappings(intent.Files)
	if err != nil {
		return execution.Observation{}, err
	}
	if len(mappings) > handler.options.MaxFiles {
		return execution.Observation{}, fmt.Errorf("%w: expanded filesystem file bound is exceeded", ErrInvalidIntent)
	}
	return handler.observeMappings(ctx, mappings)
}

// Dispatch re-observes the exact desired state, rechecks linked-client
// prerequisites and then invokes one explicit frozen action method. The
// organize package returns ErrUnsupported on Linux for risky mutations; this
// handler propagates that capability result without attempting a fallback.
func (handler *FilesystemHandler) Dispatch(ctx context.Context, action execution.Action, attempt execution.Attempt) (execution.DispatchResult, error) {
	if err := handler.validateAction(action); err != nil {
		return execution.DispatchResult{}, err
	}
	if err := handler.guardDispatch(ctx, action); err != nil {
		return execution.DispatchResult{}, err
	}
	intent, err := fileIntentFromJSON(action.DesiredState)
	if err != nil {
		return execution.DispatchResult{}, err
	}
	if err := validateFileIntent(handler.kind, intent, handler.options.MaxFiles); err != nil {
		return execution.DispatchResult{}, err
	}
	if err := validateNativeClientScope(handler.kind, intent); err != nil {
		// Reject an unrepresentable native rename before even observing the
		// linked client or filesystem. A qBittorrent basename operation can only
		// address one path inside the source root and containing directory.
		return execution.DispatchResult{}, err
	}
	observation, err := handler.Observe(ctx, action)
	if err != nil {
		return execution.DispatchResult{}, err
	}
	if observation.State == execution.ObserveSatisfied {
		return execution.DispatchResult{Accepted: true, Outcome: domain.OutcomeAlreadySatisfied, Evidence: observation.Evidence, Effects: observation.Effects}, nil
	}
	if observation.State != execution.ObserveNeedsAction {
		return execution.DispatchResult{}, handler.failure(ErrStateUnknown, false)
	}
	if err := handler.checkLinkedClient(ctx, intent.LinkedDownload); err != nil {
		return execution.DispatchResult{}, handler.failure(err, false)
	}
	if err := handler.guardDispatch(ctx, action); err != nil {
		return execution.DispatchResult{}, err
	}
	operationID := strings.TrimSpace(attempt.ID)
	if operationID == "" {
		operationID = action.ID
	}
	effect, err := handler.callAction(ctx, operationID, intent)
	if err != nil {
		// Filesystem ports may return an affected subset together with an error
		// (for example, a later source-stability check can fail after earlier
		// files were published). Preserve that subset as per-target evidence and
		// classify the failure as dispatched/uncertain whenever the returned
		// effect or error leaves any possibility of a mutation.
		partial := filesystemDispatchResult(handler.kind, observation, intent, effect, handler.now(), false)
		if len(effect.Affected) > 0 && len(partial.Effects) == 0 {
			return partial, handler.failure(fmt.Errorf("%w: filesystem action returned unapproved affected entries", ErrStateUnknown), true)
		}
		return partial, handler.failure(err, filesystemEffectMayHaveDispatched(effect, err))
	}
	outcome := effect.Outcome
	if !outcome.Valid() {
		partial := filesystemDispatchResult(handler.kind, observation, intent, effect, handler.now(), false)
		if len(effect.Affected) > 0 && len(partial.Effects) == 0 {
			return partial, handler.failure(fmt.Errorf("%w: filesystem action returned unapproved affected entries", ErrStateUnknown), true)
		}
		return partial, handler.failure(fmt.Errorf("%w: filesystem action returned an invalid effect outcome", ErrStateUnknown), true)
	}
	result := filesystemDispatchResult(handler.kind, observation, intent, effect, handler.now(), true)
	if len(effect.Affected) > 0 && len(result.Effects) == 0 {
		result.Accepted = false
		result.Outcome = ""
		return result, handler.failure(fmt.Errorf("%w: filesystem action returned unapproved affected entries", ErrStateUnknown), true)
	}
	return result, nil
}

// Reconcile reads the exact target set. A retry is permitted only when the
// read proves the desired effects remain absent and no ambiguity exists.
func (handler *FilesystemHandler) Reconcile(ctx context.Context, action execution.Action, attempt execution.Attempt) (execution.ReconcileResult, error) {
	observation, err := handler.Observe(ctx, action)
	if err != nil {
		return execution.ReconcileResult{}, handler.failure(err, false)
	}
	switch observation.State {
	case execution.ObserveSatisfied:
		return execution.ReconcileResult{Outcome: domain.OutcomeApplied, Evidence: append(observation.Evidence, "filesystem_reconciled"), Effects: terminalEffects(observation.Effects, domain.OutcomeApplied, handler.now(), "filesystem_reconciled")}, nil
	case execution.ObserveNeedsAction:
		return reconcileNeedsAction(observation.Effects, observation.Evidence, "safe_to_retry_filesystem"), nil
	default:
		return execution.ReconcileResult{Evidence: append(observation.Evidence, "filesystem_state_unknown"), Effects: cloneEffects(observation.Effects)}, nil
	}
}

type linkedClientBinding struct {
	control ports.DownloadControlPort
	ref     ports.DownloadRef
}

func (handler *FilesystemHandler) resolveLinkedClient(reference *ports.DownloadRef) (linkedClientBinding, error) {
	if reference == nil {
		return linkedClientBinding{}, nil
	}
	if err := validateRef(*reference); err != nil {
		return linkedClientBinding{}, err
	}
	linked := handler.options.LinkedClient
	if linked == nil {
		return linkedClientBinding{}, fmt.Errorf("%w: linked client is not configured", ErrDependencyRequired)
	}
	var (
		control ports.DownloadControlPort
		err     error
	)
	switch {
	case linked.Resolve != nil:
		control, err = linked.Resolve(*reference)
	case linked.Controls != nil:
		control = linked.Controls[refID(*reference)]
		if control == nil {
			err = fmt.Errorf("%w: linked client reference is not configured", ErrDependencyRequired)
		}
	case linked.Control != nil && (linked.Ref == (ports.DownloadRef{}) || linked.Ref == *reference):
		// A normalized multi-instance control port may safely serve the exact
		// reference supplied by the action. The legacy Ref field still fences
		// single-client wiring when it is populated.
		control = linked.Control
	case linked.Control != nil:
		err = fmt.Errorf("%w: linked client reference differs from approved action", ErrStateUnknown)
	default:
		err = fmt.Errorf("%w: linked client control is not configured", ErrDependencyRequired)
	}
	if err != nil {
		return linkedClientBinding{}, err
	}
	if control == nil {
		return linkedClientBinding{}, fmt.Errorf("%w: linked client resolver returned no control port", ErrDependencyRequired)
	}
	return linkedClientBinding{control: control, ref: *reference}, nil
}

func (handler *FilesystemHandler) checkLinkedClient(ctx context.Context, reference *ports.DownloadRef) error {
	if reference == nil {
		return nil
	}
	linked, err := handler.resolveLinkedClient(reference)
	if err != nil {
		return err
	}
	observation, err := linked.control.Observe(ctx, linked.ref)
	if err != nil {
		return err
	}
	if observation.Ref != linked.ref {
		return fmt.Errorf("%w: linked client returned a different reference", ErrStateUnknown)
	}
	// v0.0.1 filesystem actions always require a stopped linked client. The
	// field remains part of the wiring type for a future reviewed prerequisite
	// variant, but no value can weaken this safety boundary today.
	if !isStoppedDownload(observation) {
		return fmt.Errorf("%w: linked download client is active", ErrStateUnknown)
	}
	return nil
}

func (handler *FilesystemHandler) observeMappings(ctx context.Context, mappings []ports.FileMap) (execution.Observation, error) {
	effects := make([]execution.Effect, len(mappings))
	allSatisfied := true
	observedAt := handler.now()
	for index, mapping := range mappings {
		if err := contextError(ctx); err != nil {
			return execution.Observation{}, err
		}
		source, sourceErr := handler.read.Stat(ctx, sourceTarget(mapping))
		sourcePresent := sourceErr == nil
		if sourceErr != nil && !isMissing(sourceErr, handler.options) {
			return execution.Observation{}, handler.failure(sourceErr, false)
		}
		if sourcePresent {
			if err := exactManifestObservation(source.Entry, mapping.Source); err != nil {
				return execution.Observation{}, handler.failure(err, false)
			}
			if err := handler.verifySourceDigest(ctx, mapping, source.Entry); err != nil {
				return execution.Observation{}, handler.failure(err, false)
			}
		}
		if !source.ObservedAt.IsZero() && source.ObservedAt.After(observedAt) {
			observedAt = source.ObservedAt
		}
		destination, destinationErr := handler.read.Stat(ctx, mapping.Destination)
		state := execution.EffectPending
		evidence := "destination_missing"
		if destinationErr == nil {
			matched, matchErr := handler.destinationMatches(ctx, mapping, destination)
			if matchErr != nil {
				return execution.Observation{}, handler.failure(matchErr, false)
			}
			copyLike := handler.kind == domain.ActionFSCopy || handler.kind == domain.ActionFSHardlink
			if matched && ((sourcePresent && copyLike) || !sourcePresent) {
				state = execution.EffectAlreadySatisfied
				evidence = "destination_read_back"
			} else if matched && sourcePresent {
				return execution.Observation{}, handler.failure(fmt.Errorf("%w: both source and destination are present", ErrStateUnknown), false)
			} else if handler.kind == domain.ActionFSMove || handler.kind == domain.ActionFSRename {
				return execution.Observation{}, handler.failure(fmt.Errorf("%w: destination conflicts with source identity", ErrStateUnknown), false)
			} else {
				return execution.Observation{}, handler.failure(fmt.Errorf("%w: destination content conflicts", ErrStateUnknown), false)
			}
		} else if !isMissing(destinationErr, handler.options) {
			return execution.Observation{}, handler.failure(destinationErr, false)
		}
		if !sourcePresent && destinationErr != nil && isMissing(destinationErr, handler.options) && (handler.kind == domain.ActionFSMove || handler.kind == domain.ActionFSRename) {
			return execution.Observation{}, handler.failure(fmt.Errorf("%w: move destination and source are both absent", ErrStateUnknown), false)
		}
		if !sourcePresent && destinationErr != nil && isMissing(destinationErr, handler.options) && (handler.kind == domain.ActionFSCopy || handler.kind == domain.ActionFSHardlink) {
			return execution.Observation{}, handler.failure(fmt.Errorf("%w: copy source and destination are both absent", ErrStateUnknown), false)
		}
		if !sourcePresent && destinationErr != nil && isMissing(destinationErr, handler.options) && handler.kind == domain.ActionFSRestore {
			return execution.Observation{}, handler.failure(fmt.Errorf("%w: restore source and destination are both absent", ErrStateUnknown), false)
		}
		if state != execution.EffectAlreadySatisfied {
			allSatisfied = false
		}
		effects[index] = effect("filesystem", targetID(mapping.Destination), handler.operation, state, observedAt, evidence)
	}
	if allSatisfied {
		return execution.Observation{State: execution.ObserveSatisfied, Evidence: []string{"exact_destination_read_back"}, Effects: effects}, nil
	}
	return execution.Observation{State: execution.ObserveNeedsAction, Evidence: []string{"exact_source_and_destination_observed"}, Effects: effects}, nil
}

func (handler *FilesystemHandler) destinationMatches(ctx context.Context, mapping ports.FileMap, destination ports.FilesystemObservation) (bool, error) {
	if err := exactTargetObservation(destination.Entry, mapping.Destination); err != nil {
		return false, err
	}
	if destination.Entry.Type != mapping.Source.Type {
		return false, nil
	}
	if handler.kind == domain.ActionFSCopy {
		if mapping.Source.Type == domain.ManifestDirectory {
			return false, fmt.Errorf("%w: directory copy must be expanded", ErrInvalidIntent)
		}
		digest := destination.Entry.Digest
		if digest == "" {
			var err error
			digest, err = handler.read.Hash(ctx, mapping.Destination)
			if err != nil {
				return false, err
			}
		}
		return strings.EqualFold(strings.TrimPrefix(digest, "sha256:"), strings.TrimPrefix(mapping.Source.Digest, "sha256:")) && destination.Entry.Size == mapping.Source.Size, nil
	}
	if handler.kind == domain.ActionFSHardlink {
		return mapping.Source.FileIdentity != "" && mapping.Source.FileIdentity == destination.Entry.FileIdentity, nil
	}
	if mapping.Source.FileIdentity == "" || destination.Entry.FileIdentity != mapping.Source.FileIdentity || destination.Entry.Size != mapping.Source.Size {
		return false, nil
	}
	return true, nil
}

func (handler *FilesystemHandler) verifySourceDigest(ctx context.Context, mapping ports.FileMap, observed domain.FileManifestEntry) error {
	if strings.TrimSpace(mapping.Source.Digest) == "" || mapping.Source.Type == domain.ManifestDirectory {
		return nil
	}
	digest := observed.Digest
	if digest == "" {
		var err error
		digest, err = handler.read.Hash(ctx, sourceTarget(mapping))
		if err != nil {
			return err
		}
	}
	if !strings.EqualFold(strings.TrimPrefix(strings.TrimSpace(digest), "sha256:"), strings.TrimPrefix(strings.TrimSpace(mapping.Source.Digest), "sha256:")) {
		return fmt.Errorf("%w: source digest changed after approval", ErrStateUnknown)
	}
	return nil
}

func (handler *FilesystemHandler) observeDelete(ctx context.Context, intent FileIntent) (execution.Observation, error) {
	effects := make([]execution.Effect, len(intent.Manifest))
	allAbsent := true
	observedAt := handler.now()
	for index, entry := range intent.Manifest {
		if err := contextError(ctx); err != nil {
			return execution.Observation{}, err
		}
		observation, err := handler.read.Stat(ctx, domain.FileTarget{RootID: entry.RootID, RelativePath: entry.RelativePath})
		if err != nil {
			if isMissing(err, handler.options) {
				effects[index] = effect("filesystem", targetID(domain.FileTarget{RootID: entry.RootID, RelativePath: entry.RelativePath}), deleteOperation, execution.EffectAlreadySatisfied, observedAt, "source_absent")
				continue
			}
			return execution.Observation{}, handler.failure(err, false)
		}
		if err := exactManifestObservation(observation.Entry, entry); err != nil {
			return execution.Observation{}, handler.failure(err, false)
		}
		allAbsent = false
		effects[index] = effect("filesystem", targetID(domain.FileTarget{RootID: entry.RootID, RelativePath: entry.RelativePath}), deleteOperation, execution.EffectPending, observedAt, "source_present")
	}
	if allAbsent {
		return execution.Observation{State: execution.ObserveSatisfied, Evidence: []string{"source_absent"}, Effects: effects}, nil
	}
	return execution.Observation{State: execution.ObserveNeedsAction, Evidence: []string{"exact_source_manifest_observed"}, Effects: effects}, nil
}

func (handler *FilesystemHandler) observeTrash(ctx context.Context, intent FileIntent) (execution.Observation, error) {
	effects := make([]execution.Effect, len(intent.Manifest))
	observedAt := handler.now()
	for index, entry := range intent.Manifest {
		effects[index] = effect("filesystem", targetID(domain.FileTarget{RootID: entry.RootID, RelativePath: entry.RelativePath}), trashOperation, execution.EffectUnknown, observedAt, "trash_state_unknown")
	}
	for index, entry := range intent.Manifest {
		if err := contextError(ctx); err != nil {
			return execution.Observation{}, err
		}
		observation, err := handler.read.Stat(ctx, domain.FileTarget{RootID: entry.RootID, RelativePath: entry.RelativePath})
		if err != nil {
			if isMissing(err, handler.options) {
				return execution.Observation{State: execution.ObserveUnknown, Evidence: []string{"trash_destination_not_exposed", "source_absent"}, Effects: unknownEffects(effects, observedAt)}, nil
			}
			return execution.Observation{}, handler.failure(err, false)
		}
		if err := exactManifestObservation(observation.Entry, entry); err != nil {
			return execution.Observation{}, handler.failure(err, false)
		}
		effects[index] = effect("filesystem", targetID(domain.FileTarget{RootID: entry.RootID, RelativePath: entry.RelativePath}), trashOperation, execution.EffectPending, observedAt, "source_present")
	}
	return execution.Observation{State: execution.ObserveNeedsAction, Evidence: []string{"exact_source_manifest_observed", "trash_visibility_requires_later_read"}, Effects: effects}, nil
}

func (handler *FilesystemHandler) callAction(ctx context.Context, operationID string, intent FileIntent) (ports.FilesystemEffect, error) {
	if strings.TrimSpace(intent.Executor) == executorNativeClient {
		return handler.callNativeClient(ctx, operationID, intent)
	}
	switch handler.kind {
	case domain.ActionFSCopy:
		request := ports.FilesystemCopyRequest{Files: intent.Files}
		if caller, ok := handler.action.(interface {
			CopyWithOperation(context.Context, string, ports.FilesystemCopyRequest) (ports.FilesystemEffect, error)
		}); ok {
			return caller.CopyWithOperation(ctx, operationID, request)
		}
		return handler.action.Copy(ctx, request)
	case domain.ActionFSHardlink:
		request := ports.FilesystemHardlinkRequest{Files: intent.Files}
		if caller, ok := handler.action.(interface {
			HardlinkWithOperation(context.Context, string, ports.FilesystemHardlinkRequest) (ports.FilesystemEffect, error)
		}); ok {
			return caller.HardlinkWithOperation(ctx, operationID, request)
		}
		return handler.action.Hardlink(ctx, request)
	case domain.ActionFSMove:
		request := ports.FilesystemMoveRequest{Files: intent.Files}
		if caller, ok := handler.action.(interface {
			MoveWithOperation(context.Context, string, ports.FilesystemMoveRequest) (ports.FilesystemEffect, error)
		}); ok {
			return caller.MoveWithOperation(ctx, operationID, request)
		}
		return handler.action.Move(ctx, request)
	case domain.ActionFSRename:
		request := ports.FilesystemRenameRequest{Files: intent.Files}
		if caller, ok := handler.action.(interface {
			RenameWithOperation(context.Context, string, ports.FilesystemRenameRequest) (ports.FilesystemEffect, error)
		}); ok {
			return caller.RenameWithOperation(ctx, operationID, request)
		}
		return handler.action.Rename(ctx, request)
	case domain.ActionFSTrash:
		retention := intent.Retention
		if retention <= 0 {
			retention = defaultRetention
		}
		return handler.action.Trash(ctx, ports.FilesystemTrashRequest{Files: intent.Manifest, Retention: retention})
	case domain.ActionFSRestore:
		return handler.action.Restore(ctx, ports.FilesystemRestoreRequest{Files: intent.Files})
	case domain.ActionFSDelete:
		request := ports.FilesystemDeleteRequest{Files: intent.Manifest}
		if caller, ok := handler.action.(interface {
			DeleteWithOperation(context.Context, string, ports.FilesystemDeleteRequest) (ports.FilesystemEffect, error)
		}); ok {
			return caller.DeleteWithOperation(ctx, operationID, request)
		}
		return handler.action.Delete(ctx, request)
	default:
		return ports.FilesystemEffect{}, fmt.Errorf("%w: unsupported filesystem handler", ErrInvalidIntent)
	}
}

// filesystemDispatchResult translates the action port's aggregate effect into
// the exact approved target set. A port can finish some files before returning
// an error; the affected entries are therefore marked individually while
// On an errored dispatch, only the exact affected subset is returned so the
// durable executor can preserve those per-target states and mark omitted
// targets unknown. Manifest actions map directory children back to their
// top-level effect; mapping actions use the expanded file list. Any affected
// entry outside that approved identity set is rejected by the caller before a
// result can be accepted.
func filesystemDispatchResult(kind domain.ActionKind, observation execution.Observation, intent FileIntent, effect ports.FilesystemEffect, observedAt time.Time, accepted bool) execution.DispatchResult {
	result := execution.DispatchResult{
		Accepted: accepted,
		Outcome:  effect.Outcome,
		Evidence: append([]string{"filesystem_effect_returned"}, effect.Evidence...),
	}
	if !effect.Outcome.Valid() {
		result.Outcome = ""
	}
	if accepted {
		if len(effect.Affected) > 0 {
			if _, ok := mapFilesystemAffected(kind, observation, intent, effect, observedAt); !ok {
				return result
			}
		}
		result.Effects = terminalEffects(observation.Effects, effect.Outcome, observedAt, "filesystem_read_back")
		return result
	}
	if len(effect.Affected) == 0 {
		return result
	}
	if effect.ObservedAt.IsZero() {
		effect.ObservedAt = observedAt
	}
	result.Effects, _ = mapFilesystemAffected(kind, observation, intent, effect, observedAt)
	return result
}

type approvedFilesystemSource struct {
	entry       domain.FileManifestEntry
	effectIndex int
}

func approvedFilesystemSources(kind domain.ActionKind, intent FileIntent) ([]approvedFilesystemSource, error) {
	if kind == domain.ActionFSTrash || kind == domain.ActionFSDelete {
		result := make([]approvedFilesystemSource, 0, len(intent.Manifest))
		for index, entry := range intent.Manifest {
			result = appendManifestSources(result, entry, index)
		}
		if len(result) == 0 {
			return nil, fmt.Errorf("%w: filesystem manifest has no approved sources", ErrInvalidIntent)
		}
		return result, nil
	}
	mappings, err := expandedMappings(intent.Files)
	if err != nil {
		return nil, err
	}
	result := make([]approvedFilesystemSource, 0, len(mappings))
	for index, mapping := range mappings {
		result = append(result, approvedFilesystemSource{entry: mapping.Source, effectIndex: index})
	}
	return result, nil
}

func appendManifestSources(result []approvedFilesystemSource, entry domain.FileManifestEntry, effectIndex int) []approvedFilesystemSource {
	result = append(result, approvedFilesystemSource{entry: entry, effectIndex: effectIndex})
	for _, child := range entry.Children {
		result = appendManifestSources(result, child, effectIndex)
	}
	return result
}

func mapFilesystemAffected(kind domain.ActionKind, observation execution.Observation, intent FileIntent, effect ports.FilesystemEffect, observedAt time.Time) ([]execution.Effect, bool) {
	approved, err := approvedFilesystemSources(kind, intent)
	if err != nil {
		return nil, false
	}
	expectedEffects := len(intent.Manifest)
	if kind != domain.ActionFSTrash && kind != domain.ActionFSDelete {
		expectedEffects = len(approved)
	}
	if len(observation.Effects) != expectedEffects {
		return nil, false
	}
	if len(effect.Affected) == 0 {
		return nil, true
	}
	matched := make(map[int]struct{}, len(effect.Affected))
	result := make(map[int]execution.Effect, len(effect.Affected))
	for _, affected := range effect.Affected {
		match := -1
		matchIndex := -1
		for index, candidate := range approved {
			if !sameFilesystemSource(affected, candidate.entry) {
				continue
			}
			if matchIndex >= 0 {
				return nil, false
			}
			match = candidate.effectIndex
			matchIndex = index
		}
		if matchIndex < 0 {
			return nil, false
		}
		if _, duplicate := matched[matchIndex]; duplicate {
			return nil, false
		}
		matched[matchIndex] = struct{}{}
		if _, alreadyMapped := result[match]; alreadyMapped {
			continue
		}
		value := observation.Effects[match]
		if value.Ordinal < 0 {
			value.Ordinal = int64(match)
		}
		state := execution.EffectUnknown
		switch effect.Outcome {
		case domain.OutcomeApplied:
			state = execution.EffectApplied
		case domain.OutcomeAlreadySatisfied:
			state = execution.EffectAlreadySatisfied
		}
		value.State = state
		observed := effect.ObservedAt
		if observed.IsZero() {
			observed = observedAt
		}
		value.ObservedAt = observed.UTC().Format(time.RFC3339Nano)
		value.Evidence = appendEffectEvidence(value.Evidence, append([]string{"filesystem_affected"}, effect.Evidence...)...)
		result[match] = value
	}
	ordered := make([]execution.Effect, 0, len(result))
	for index := range observation.Effects {
		if value, ok := result[index]; ok {
			ordered = append(ordered, value)
		}
	}
	return ordered, true
}

func sameFilesystemSource(left, right domain.FileManifestEntry) bool {
	if left.RootID != right.RootID || left.RelativePath != right.RelativePath || left.Type != right.Type || left.Size != right.Size || left.FileIdentity == "" || left.FileIdentity != right.FileIdentity {
		return false
	}
	if left.Digest != "" && right.Digest != "" && !strings.EqualFold(strings.TrimPrefix(left.Digest, "sha256:"), strings.TrimPrefix(right.Digest, "sha256:")) {
		return false
	}
	return true
}

func filesystemEffectMayHaveDispatched(effect ports.FilesystemEffect, err error) bool {
	if len(effect.Affected) > 0 || effect.Outcome.Valid() || len(effect.Evidence) > 0 {
		return true
	}
	return !isKnownPreDispatchFilesystemError(err)
}

// callNativeClient is the deliberately narrow bridge for the native-client
// executor selected by fs.move/fs.rename. The client port owns its own
// whole-torrent scope checks, native API read-back and error normalization;
// this package only translates the normalized ClientEffect into the
// FilesystemEffect shape expected by the durable executor. No generated
// upstream DTO or native command payload crosses this boundary.
func (handler *FilesystemHandler) callNativeClient(ctx context.Context, operationID string, intent FileIntent) (ports.FilesystemEffect, error) {
	if len(intent.Files) != 1 {
		return ports.FilesystemEffect{}, fmt.Errorf("%w: native client executor requires one exact file map", ErrInvalidIntent)
	}
	if err := validateNativeClientScope(handler.kind, intent); err != nil {
		return ports.FilesystemEffect{}, err
	}
	mapping := intent.Files[0]
	client, err := handler.resolveLinkedClient(intent.LinkedDownload)
	if err != nil {
		return ports.FilesystemEffect{}, err
	}
	var effect ports.ClientEffect
	switch handler.kind {
	case domain.ActionFSMove:
		effect, err = client.control.Relocate(ctx, client.ref, mapping.Destination)
	case domain.ActionFSRename:
		newName := path.Base(mapping.Destination.RelativePath)
		if mapping.Source.Type == domain.ManifestDirectory {
			effect, err = client.control.RenameFolder(ctx, client.ref, domain.FileTarget{RootID: mapping.Source.RootID, RelativePath: mapping.Source.RelativePath}, newName)
		} else {
			effect, err = client.control.RenameFile(ctx, client.ref, domain.FileTarget{RootID: mapping.Source.RootID, RelativePath: mapping.Source.RelativePath}, newName)
		}
	default:
		return ports.FilesystemEffect{}, fmt.Errorf("%w: native client executor is unsupported for %s", ErrInvalidIntent, handler.kind)
	}
	if err != nil {
		return nativeClientFilesystemEffect(effect, operationID), err
	}
	converted := nativeClientFilesystemEffect(effect, operationID)
	if !effect.Outcome.Valid() {
		return converted, fmt.Errorf("%w: native client returned an invalid outcome", ErrStateUnknown)
	}
	return converted, nil
}

func validateNativeClientScope(kind domain.ActionKind, intent FileIntent) error {
	if strings.TrimSpace(intent.Executor) != executorNativeClient || kind != domain.ActionFSRename {
		return nil
	}
	if len(intent.Files) != 1 {
		return fmt.Errorf("%w: native client executor requires one exact file map", ErrInvalidIntent)
	}
	mapping := intent.Files[0]
	if mapping.Source.RootID != mapping.Destination.RootID {
		return fmt.Errorf("%w: native rename cannot change the configured root", ErrInvalidIntent)
	}
	if path.Dir(mapping.Source.RelativePath) != path.Dir(mapping.Destination.RelativePath) {
		return fmt.Errorf("%w: native rename cannot change the containing directory", ErrInvalidIntent)
	}
	newName := path.Base(mapping.Destination.RelativePath)
	if newName == "." || newName == ".." || newName == "" {
		return fmt.Errorf("%w: native rename destination name is invalid", ErrInvalidIntent)
	}
	return nil
}

func nativeClientFilesystemEffect(effect ports.ClientEffect, operationID string) ports.FilesystemEffect {
	evidence := append([]string{"executor=native_client"}, effect.Evidence...)
	if effect.OperationID != "" {
		evidence = append(evidence, "native_operation_id="+effect.OperationID)
	}
	if operationID != "" {
		evidence = append(evidence, "action_operation_id="+operationID)
	}
	return ports.FilesystemEffect{Outcome: effect.Outcome, ObservedAt: effect.ObservedAt, Evidence: evidence}
}

func isKnownPreDispatchFilesystemError(err error) bool {
	return errors.Is(err, placement.ErrUnsupported) || errors.Is(err, organize.ErrUnsupported) || errors.Is(err, placement.ErrInvalidPlan) || errors.Is(err, organize.ErrInvalidPlan) || errors.Is(err, placement.ErrDestinationConflict) || errors.Is(err, organize.ErrDestinationConflict) || errors.Is(err, placement.ErrDestinationExists) || errors.Is(err, organize.ErrDestinationExists) || errors.Is(err, placement.ErrSourceChanged) || errors.Is(err, organize.ErrSourceChanged) || errors.Is(err, placement.ErrPathEscape) || errors.Is(err, organize.ErrPathEscape) || errors.Is(err, placement.ErrSymlink) || errors.Is(err, organize.ErrSymlink) || errors.Is(err, placement.ErrSpecialFile) || errors.Is(err, organize.ErrSpecialFile) || errors.Is(err, placement.ErrReadOnly) || errors.Is(err, organize.ErrReadOnly)
}

func fileIntentFromJSON(raw []byte) (FileIntent, error) {
	var intent FileIntent
	if desired, isPlanning, err := decodePlanningDesired(raw); err != nil {
		return intent, err
	} else if isPlanning {
		for _, predicate := range desired.Predicates {
			switch predicate.Kind {
			case planning.PredicateFileContent, planning.PredicateHardlink, planning.PredicateFileIdentity:
				if predicate.File == nil {
					continue
				}
				if predicate.Kind == planning.PredicateHardlink && !isZeroTarget(predicate.File.Source) {
					intent.Files = append(intent.Files, ports.FileMap{Source: manifestFromPlanning(predicate.File.Source, predicate.File.Size, predicate.File.Digest, predicate.File.FileIdentity), Destination: predicate.File.Target})
				} else {
					intent.Files = append(intent.Files, ports.FileMap{Source: manifestFromPlanning(predicate.File.Target, predicate.File.Size, predicate.File.Digest, predicate.File.FileIdentity), Destination: predicate.File.Target})
				}
			}
		}
		return intent, nil
	}
	if err := decodeIntent(raw, &intent); err != nil {
		return intent, err
	}
	return intent, nil
}

func validateFileIntent(kind domain.ActionKind, intent FileIntent, maxFiles int) error {
	if intent.LinkedDownload != nil {
		if err := validateRef(*intent.LinkedDownload); err != nil {
			return err
		}
	}
	executor := strings.TrimSpace(intent.Executor)
	if intent.Executor != "" && executor == "" {
		return fmt.Errorf("%w: filesystem executor is empty", ErrInvalidIntent)
	}
	if kind != domain.ActionFSMove && kind != domain.ActionFSRename && executor != "" {
		return fmt.Errorf("%w: executor is only valid for move and rename", ErrInvalidIntent)
	}
	if executor != "" && executor != executorMastarr && executor != executorNativeClient {
		return fmt.Errorf("%w: filesystem executor %q is unsupported", ErrInvalidIntent, executor)
	}
	if executor == executorNativeClient {
		if intent.LinkedDownload == nil {
			return fmt.Errorf("%w: native client executor requires a linked download", ErrDependencyRequired)
		}
		// The qBittorrent control boundary is a whole-item operation. One
		// approved map is required so a native call cannot widen a selected
		// multi-file plan into an unreviewed client scope. A directory map is
		// still allowed for rename-folder; the client adapter validates its
		// exact child scope before dispatch.
		if len(intent.Files) != 1 {
			return fmt.Errorf("%w: native client executor requires one exact file map", ErrInvalidIntent)
		}
	}
	switch kind {
	case domain.ActionFSCopy:
		if len(intent.Files) == 0 || len(intent.Files) > maxFiles {
			return fmt.Errorf("%w: copy files are out of bounds", ErrInvalidIntent)
		}
		return (ports.FilesystemCopyRequest{Files: intent.Files}).Validate()
	case domain.ActionFSHardlink:
		if len(intent.Files) == 0 || len(intent.Files) > maxFiles {
			return fmt.Errorf("%w: hardlink files are out of bounds", ErrInvalidIntent)
		}
		return (ports.FilesystemHardlinkRequest{Files: intent.Files}).Validate()
	case domain.ActionFSMove:
		return boundedMaps(intent.Files, maxFiles, ports.FilesystemMoveRequest{Files: intent.Files}.Validate)
	case domain.ActionFSRename:
		return boundedMaps(intent.Files, maxFiles, ports.FilesystemRenameRequest{Files: intent.Files}.Validate)
	case domain.ActionFSRestore:
		return boundedMaps(intent.Files, maxFiles, ports.FilesystemRestoreRequest{Files: intent.Files}.Validate)
	case domain.ActionFSTrash:
		if len(intent.Manifest) == 0 || len(intent.Manifest) > maxFiles {
			return fmt.Errorf("%w: trash manifest is out of bounds", ErrInvalidIntent)
		}
		if intent.Retention < 0 {
			return fmt.Errorf("%w: trash retention is negative", ErrInvalidIntent)
		}
		retention := intent.Retention
		if retention <= 0 {
			retention = defaultRetention
		}
		return (ports.FilesystemTrashRequest{Files: intent.Manifest, Retention: retention}).Validate()
	case domain.ActionFSDelete:
		if len(intent.Manifest) == 0 || len(intent.Manifest) > maxFiles {
			return fmt.Errorf("%w: delete manifest is out of bounds", ErrInvalidIntent)
		}
		return (ports.FilesystemDeleteRequest{Files: intent.Manifest}).Validate()
	default:
		return fmt.Errorf("%w: unsupported filesystem kind", ErrInvalidIntent)
	}
}

func boundedMaps(maps []ports.FileMap, maxFiles int, validate func() error) error {
	if len(maps) == 0 || len(maps) > maxFiles {
		return fmt.Errorf("%w: filesystem maps are out of bounds", ErrInvalidIntent)
	}
	return validate()
}

func expandedMappings(maps []ports.FileMap) ([]ports.FileMap, error) {
	result := make([]ports.FileMap, 0, len(maps))
	for _, mapping := range maps {
		if err := appendExpandedMappings(&result, mapping); err != nil {
			return nil, err
		}
	}
	if len(result) == 0 {
		return nil, fmt.Errorf("%w: filesystem map has no concrete children", ErrInvalidIntent)
	}
	return result, nil
}

func appendExpandedMappings(result *[]ports.FileMap, mapping ports.FileMap) error {
	if mapping.Source.Type != domain.ManifestDirectory {
		*result = append(*result, mapping)
		return nil
	}
	for _, child := range mapping.Source.Children {
		suffix, ok := pathForChild(mapping.Source.RelativePath, child.RelativePath)
		if !ok {
			return fmt.Errorf("%w: directory child is outside approved source", ErrInvalidIntent)
		}
		destination, err := joinTarget(mapping.Destination, suffix)
		if err != nil {
			return err
		}
		if err := appendExpandedMappings(result, ports.FileMap{Source: child, Destination: destination}); err != nil {
			return err
		}
	}
	return nil
}

func exactManifestObservation(actual domain.FileManifestEntry, expected domain.FileManifestEntry) error {
	if actual.RootID != expected.RootID || actual.RelativePath != expected.RelativePath || actual.Type != expected.Type || actual.Size != expected.Size || actual.FileIdentity == "" || actual.FileIdentity != expected.FileIdentity {
		return fmt.Errorf("%w: source identity or size changed", ErrStateUnknown)
	}
	return nil
}

func exactTargetObservation(actual domain.FileManifestEntry, expected domain.FileTarget) error {
	if actual.RootID != expected.RootID || actual.RelativePath != expected.RelativePath || actual.FileIdentity == "" {
		return fmt.Errorf("%w: destination identity is incomplete", ErrStateUnknown)
	}
	return nil
}

func isMissing(err error, options HandlerOptions) bool {
	return errors.Is(err, fs.ErrNotExist) || classifyNotFound(err, options)
}

func contextError(ctx context.Context) error {
	if ctx == nil {
		return context.Canceled
	}
	return ctx.Err()
}

func sourceTarget(mapping ports.FileMap) domain.FileTarget {
	return domain.FileTarget{RootID: mapping.Source.RootID, RelativePath: mapping.Source.RelativePath}
}

func isZeroTarget(target domain.FileTarget) bool {
	return target.RootID == "" && target.RelativePath == ""
}

func manifestFromPlanning(target domain.FileTarget, size int64, digest, identity string) domain.FileManifestEntry {
	return domain.FileManifestEntry{RootID: target.RootID, RelativePath: target.RelativePath, Type: domain.ManifestFile, Size: size, Digest: digest, FileIdentity: identity, ObservedAt: time.Now().UTC()}
}

func max(left, right time.Duration) time.Duration {
	if left > right {
		return left
	}
	return right
}
