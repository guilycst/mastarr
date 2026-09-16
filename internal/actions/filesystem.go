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
	keys := make([]string, 0, len(intent.Files)*2+len(intent.Manifest))
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
		return execution.DispatchResult{}, handler.failure(err, !isKnownPreDispatchFilesystemError(err))
	}
	outcome := effect.Outcome
	if !outcome.Valid() {
		return execution.DispatchResult{}, handler.failure(fmt.Errorf("%w: filesystem action returned an invalid effect outcome", ErrStateUnknown), true)
	}
	return execution.DispatchResult{
		Accepted: true, Outcome: outcome, Evidence: append([]string{handler.operation + "_returned"}, effect.Evidence...),
		Effects: terminalEffects(observation.Effects, outcome, handler.now(), "filesystem_read_back"),
	}, nil
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
		for index := range observation.Effects {
			observation.Effects[index].State = execution.EffectPending
		}
		return execution.ReconcileResult{SafeToRetry: true, Evidence: append(observation.Evidence, "safe_to_retry_filesystem"), Effects: observation.Effects}, nil
	default:
		return execution.ReconcileResult{Evidence: append(observation.Evidence, "filesystem_state_unknown"), Effects: unknownEffects(observation.Effects, handler.now())}, nil
	}
}

func (handler *FilesystemHandler) checkLinkedClient(ctx context.Context, reference *ports.DownloadRef) error {
	if reference == nil {
		return nil
	}
	if handler.options.LinkedClient == nil || handler.options.LinkedClient.Control == nil {
		return fmt.Errorf("%w: linked client is not configured", ErrDependencyRequired)
	}
	if err := validateRef(*reference); err != nil {
		return err
	}
	linked := handler.options.LinkedClient
	if linked.Ref != *reference {
		return fmt.Errorf("%w: linked client reference differs from approved action", ErrStateUnknown)
	}
	observation, err := linked.Control.Observe(ctx, linked.Ref)
	if err != nil {
		return err
	}
	if observation.Ref != linked.Ref {
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

// callNativeClient is the deliberately narrow bridge for the native-client
// executor selected by fs.move/fs.rename. The client port owns its own
// whole-torrent scope checks, native API read-back and error normalization;
// this package only translates the normalized ClientEffect into the
// FilesystemEffect shape expected by the durable executor. No generated
// upstream DTO or native command payload crosses this boundary.
func (handler *FilesystemHandler) callNativeClient(ctx context.Context, operationID string, intent FileIntent) (ports.FilesystemEffect, error) {
	if handler.options.LinkedClient == nil || handler.options.LinkedClient.Control == nil {
		return ports.FilesystemEffect{}, fmt.Errorf("%w: native client control is not configured", ErrDependencyRequired)
	}
	if len(intent.Files) != 1 {
		return ports.FilesystemEffect{}, fmt.Errorf("%w: native client executor requires one exact file map", ErrInvalidIntent)
	}
	mapping := intent.Files[0]
	client := handler.options.LinkedClient
	var effect ports.ClientEffect
	var err error
	switch handler.kind {
	case domain.ActionFSMove:
		effect, err = client.Control.Relocate(ctx, client.Ref, mapping.Destination)
	case domain.ActionFSRename:
		if path.Dir(mapping.Source.RelativePath) != path.Dir(mapping.Destination.RelativePath) {
			return ports.FilesystemEffect{}, fmt.Errorf("%w: native rename cannot change the containing directory", ErrInvalidIntent)
		}
		newName := path.Base(mapping.Destination.RelativePath)
		if newName == "." || newName == ".." || newName == "" {
			return ports.FilesystemEffect{}, fmt.Errorf("%w: native rename destination name is invalid", ErrInvalidIntent)
		}
		if mapping.Source.Type == domain.ManifestDirectory {
			effect, err = client.Control.RenameFolder(ctx, client.Ref, domain.FileTarget{RootID: mapping.Source.RootID, RelativePath: mapping.Source.RelativePath}, newName)
		} else {
			effect, err = client.Control.RenameFile(ctx, client.Ref, domain.FileTarget{RootID: mapping.Source.RootID, RelativePath: mapping.Source.RelativePath}, newName)
		}
	default:
		return ports.FilesystemEffect{}, fmt.Errorf("%w: native client executor is unsupported for %s", ErrInvalidIntent, handler.kind)
	}
	if err != nil {
		return ports.FilesystemEffect{}, err
	}
	if !effect.Outcome.Valid() {
		return ports.FilesystemEffect{}, fmt.Errorf("%w: native client returned an invalid outcome", ErrStateUnknown)
	}
	evidence := append([]string{"executor=native_client"}, effect.Evidence...)
	if effect.OperationID != "" {
		evidence = append(evidence, "native_operation_id="+effect.OperationID)
	}
	if operationID != "" {
		evidence = append(evidence, "action_operation_id="+operationID)
	}
	return ports.FilesystemEffect{Outcome: effect.Outcome, ObservedAt: effect.ObservedAt, Evidence: evidence}, nil
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
