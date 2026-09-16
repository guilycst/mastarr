package actions

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/guilycst/mastarr/internal/domain"
	"github.com/guilycst/mastarr/internal/execution"
	"github.com/guilycst/mastarr/internal/planning"
	"github.com/guilycst/mastarr/internal/ports"
)

const (
	registrationOperation = "arr.registration"
	importOperation       = "arr.import"
)

// RegistrationConfig wires one independent Arr registration handler.
type RegistrationConfig struct {
	Read         ports.MediaManagerReadPort
	Write        ports.MediaManagerWritePort
	Capabilities ports.CapabilityPort
	Options      HandlerOptions
}

// RegistrationHandler implements arr.registration. It observes the complete
// title catalog before deciding whether a write is necessary.
type RegistrationHandler struct {
	baseHandler
	read         ports.MediaManagerReadPort
	write        ports.MediaManagerWritePort
	capabilities ports.CapabilityPort
}

var _ execution.Handler = (*RegistrationHandler)(nil)

// NewRegistrationHandler validates the required read/write ports.
func NewRegistrationHandler(config RegistrationConfig) (*RegistrationHandler, error) {
	if config.Read == nil || config.Write == nil {
		return nil, fmt.Errorf("%w: registration read/write ports", ErrDependencyRequired)
	}
	options := config.Options.normalized()
	return &RegistrationHandler{
		baseHandler:  baseHandler{kind: domain.ActionArrRegistration, options: options},
		read:         config.Read,
		write:        config.Write,
		capabilities: config.Capabilities,
	}, nil
}

// Reservations returns one connection/provider identity reservation.
func (handler *RegistrationHandler) Reservations(action execution.Action) []string {
	intent, err := registrationIntentFromJSON(action.DesiredState)
	if err != nil {
		return nil
	}
	return []string{"arr:" + intent.ConnectionID.String() + ":registration:" + strings.TrimSpace(intent.ProviderID)}
}

// Observe reads the complete Arr title set and reports an exact semantic
// registration predicate. Non-complete pagination never proves absence.
func (handler *RegistrationHandler) Observe(ctx context.Context, action execution.Action) (execution.Observation, error) {
	if err := handler.validateAction(action); err != nil {
		return execution.Observation{}, err
	}
	if err := handler.guardObserve(ctx); err != nil {
		return execution.Observation{}, err
	}
	intent, err := registrationIntentFromJSON(action.DesiredState)
	if err != nil {
		return execution.Observation{}, err
	}
	if err := handler.validateIntent(intent); err != nil {
		return execution.Observation{}, err
	}
	titles, observedAt, err := listMediaRecords(ctx, handler.read, intent.ConnectionID, handler.options)
	if err != nil {
		return execution.Observation{}, handler.failure(err, false)
	}
	matching, conflict := matchingRegistrationRecords(titles, intent)
	if conflict != nil {
		return execution.Observation{}, handler.failure(conflict, false)
	}
	target := "provider:" + intent.ProviderID
	if matching == nil {
		if intent.ExternalID != "" {
			return execution.Observation{}, handler.failure(fmt.Errorf("%w: requested external id is not present in the complete catalog", ErrStateUnknown), false)
		}
		return execution.Observation{
			State:    execution.ObserveNeedsAction,
			Evidence: []string{"catalog_complete", "registration_absent"},
			Effects:  []execution.Effect{effect("arr.registration", target, registrationOperation, execution.EffectPending, observedAt, "registration_required")},
		}, nil
	}
	if intent.ExternalID != "" && matching.ExternalID != intent.ExternalID {
		return execution.Observation{}, handler.failure(fmt.Errorf("%w: existing external id differs", ErrStateUnknown), false)
	}
	if registrationFieldsMatch(*matching, intent.Fields) {
		return execution.Observation{
			State:    execution.ObserveSatisfied,
			Evidence: []string{"catalog_complete", "registration_fields_match"},
			Effects:  []execution.Effect{effect("arr.registration", target, registrationOperation, execution.EffectAlreadySatisfied, observedAt, "already_satisfied")},
		}, nil
	}
	return execution.Observation{
		State:    execution.ObserveNeedsAction,
		Evidence: []string{"catalog_complete", "registration_fields_differ"},
		Effects:  []execution.Effect{effect("arr.registration", target, registrationOperation, execution.EffectPending, observedAt, "registration_update_required")},
	}, nil
}

// Dispatch re-observes before invoking the Arr write port. A capability must
// be independently supported; the public Arr adapter intentionally reports
// G-01 as unknown, so native writes remain unreachable in v0.0.1.
func (handler *RegistrationHandler) Dispatch(ctx context.Context, action execution.Action, attempt execution.Attempt) (execution.DispatchResult, error) {
	if err := handler.validateAction(action); err != nil {
		return execution.DispatchResult{}, err
	}
	if err := handler.guardDispatch(ctx, action); err != nil {
		return execution.DispatchResult{}, err
	}
	intent, err := registrationIntentFromJSON(action.DesiredState)
	if err != nil {
		return execution.DispatchResult{}, err
	}
	if err := handler.validateIntent(intent); err != nil {
		return execution.DispatchResult{}, err
	}
	observation, err := handler.Observe(ctx, action)
	if err != nil {
		return execution.DispatchResult{}, err
	}
	if observation.State == execution.ObserveSatisfied {
		return execution.DispatchResult{Accepted: true, Outcome: domain.OutcomeAlreadySatisfied, Effects: observation.Effects, Evidence: observation.Evidence}, nil
	}
	if observation.State != execution.ObserveNeedsAction {
		return execution.DispatchResult{}, handler.failure(ErrStateUnknown, false)
	}
	if err := handler.requireCapability(ctx, intent.ConnectionID, registrationOperation); err != nil {
		return execution.DispatchResult{}, err
	}
	if err := handler.guardDispatch(ctx, action); err != nil {
		return execution.DispatchResult{}, err
	}
	result, err := handler.write.Register(ctx, intent.ConnectionID, ports.RegistrationRequest{
		ProviderID: intent.ProviderID,
		Kind:       intent.Kind,
		Fields:     cloneRegistrationFields(intent.Fields),
	})
	if err != nil {
		return execution.DispatchResult{}, handler.failure(err, true)
	}
	if result.ExternalID == "" {
		return execution.DispatchResult{}, handler.failure(ErrStateUnknown, true)
	}
	if intent.ExternalID != "" && result.ExternalID != intent.ExternalID {
		return execution.DispatchResult{}, handler.failure(fmt.Errorf("%w: registration returned a different external id", ErrStateUnknown), true)
	}
	outcome := result.Effect.Outcome
	if !outcome.Valid() {
		return execution.DispatchResult{}, handler.failure(fmt.Errorf("%w: registration returned an invalid effect outcome", ErrStateUnknown), true)
	}
	return execution.DispatchResult{
		Accepted:   true,
		Outcome:    outcome,
		ExternalID: result.ExternalID,
		Evidence:   append([]string{"registration_write_returned"}, result.Effect.Evidence...),
		Effects:    terminalEffects(observation.Effects, outcome, handler.now(), "registration_read_back"),
	}, nil
}

// Reconcile performs only the complete catalog read and permits retry only
// when the desired registration is still absent or differs without conflict.
func (handler *RegistrationHandler) Reconcile(ctx context.Context, action execution.Action, attempt execution.Attempt) (execution.ReconcileResult, error) {
	observation, err := handler.Observe(ctx, action)
	if err != nil {
		return execution.ReconcileResult{}, handler.failure(err, false)
	}
	switch observation.State {
	case execution.ObserveSatisfied:
		return execution.ReconcileResult{Outcome: domain.OutcomeApplied, Evidence: append(observation.Evidence, "reconciled"), Effects: terminalEffects(observation.Effects, domain.OutcomeApplied, handler.now(), "reconciled")}, nil
	case execution.ObserveNeedsAction:
		for index := range observation.Effects {
			observation.Effects[index].State = execution.EffectPending
		}
		return execution.ReconcileResult{SafeToRetry: true, Evidence: append(observation.Evidence, "safe_to_retry_before_registration"), Effects: observation.Effects}, nil
	default:
		return execution.ReconcileResult{Evidence: append(observation.Evidence, "registration_state_unknown"), Effects: unknownEffects(observation.Effects, handler.now())}, nil
	}
}

func (handler *RegistrationHandler) validateIntent(intent RegistrationIntent) error {
	if !intent.ConnectionID.Valid() || strings.TrimSpace(intent.ProviderID) == "" || !validMediaKind(intent.Kind) {
		return fmt.Errorf("%w: registration identity is invalid", ErrInvalidIntent)
	}
	if intent.ExternalID != "" && len(intent.ExternalID) > 256 {
		return fmt.Errorf("%w: registration external id is too long", ErrInvalidIntent)
	}
	if intent.Fields.Monitored != nil && intent.Fields.SeasonFolder != nil {
		// Both are valid together; this branch documents that pointers are
		// deliberate and keeps the copy helper exercised by validation.
		_ = *intent.Fields.Monitored
		_ = *intent.Fields.SeasonFolder
	}
	return nil
}

func (handler *RegistrationHandler) requireCapability(ctx context.Context, connectionID domain.ConfigID, operation string) error {
	capability, err := capabilityFor(ctx, handler.capabilities, connectionID, operation)
	if err != nil {
		return handler.failure(err, false)
	}
	switch capability.State {
	case domain.CapabilitySupported:
		return nil
	case domain.CapabilityUnsupported:
		return handler.failure(fmt.Errorf("%w: %s", ErrCapabilityUnsupported, operation), false)
	default:
		return handler.failure(fmt.Errorf("%w: %s", ErrCapabilityUnknown, operation), false)
	}
}

// ImportConfig wires one independent Arr import handler.
type ImportConfig struct {
	Read         ports.MediaManagerReadPort
	Write        ports.MediaManagerWritePort
	Capabilities ports.CapabilityPort
	Options      HandlerOptions
}

// ImportHandler implements arr.import with exact preview-bound file
// associations and read-before-write idempotency.
type ImportHandler struct {
	baseHandler
	read         ports.MediaManagerReadPort
	write        ports.MediaManagerWritePort
	capabilities ports.CapabilityPort
}

var _ execution.Handler = (*ImportHandler)(nil)

// NewImportHandler validates the required read/write ports.
func NewImportHandler(config ImportConfig) (*ImportHandler, error) {
	if config.Read == nil || config.Write == nil {
		return nil, fmt.Errorf("%w: import read/write ports", ErrDependencyRequired)
	}
	options := config.Options.normalized()
	return &ImportHandler{baseHandler: baseHandler{kind: domain.ActionArrImport, options: options}, read: config.Read, write: config.Write, capabilities: config.Capabilities}, nil
}

// Reservations returns the connection/title and exact source reservations.
func (handler *ImportHandler) Reservations(action execution.Action) []string {
	intent, err := importIntentFromJSON(action.DesiredState)
	if err != nil {
		return nil
	}
	keys := []string{"arr:" + intent.ConnectionID.String() + ":import:" + intent.RegisteredExternalID}
	for _, file := range intent.Files {
		keys = append(keys, "path:"+targetID(file.Source))
	}
	return keys
}

// Observe reads exact imported associations from the selected Arr title.
func (handler *ImportHandler) Observe(ctx context.Context, action execution.Action) (execution.Observation, error) {
	if err := handler.validateAction(action); err != nil {
		return execution.Observation{}, err
	}
	if err := handler.guardObserve(ctx); err != nil {
		return execution.Observation{}, err
	}
	intent, err := importIntentFromJSON(action.DesiredState)
	if err != nil {
		return execution.Observation{}, err
	}
	if err := validateImportIntent(intent, handler.options.MaxFiles); err != nil {
		return execution.Observation{}, err
	}
	preview, err := handler.read.PreviewImport(ctx, intent.ConnectionID, ports.ImportPreviewRequest{
		RegisteredExternalID: intent.RegisteredExternalID,
		Files:                cloneImportFiles(intent.Files),
		Transfer:             intent.Transfer,
	})
	if err != nil {
		return execution.Observation{}, handler.failure(err, false)
	}
	if err := validateImportPreviewBinding(intent, preview); err != nil {
		return execution.Observation{}, handler.failure(err, false)
	}
	observed, err := handler.read.ObserveImport(ctx, intent.ConnectionID, intent.RegisteredExternalID)
	if err != nil {
		return execution.Observation{}, handler.failure(err, false)
	}
	observedAt := observed.ObservedAt
	if observedAt.IsZero() {
		observedAt = handler.now()
	}
	if observed.ExternalID != intent.RegisteredExternalID {
		return execution.Observation{}, handler.failure(fmt.Errorf("%w: import read-back belongs to another title", ErrStateUnknown), false)
	}
	matched, err := matchImportFiles(observed.Files, intent.Files)
	if err != nil {
		return execution.Observation{}, handler.failure(err, false)
	}
	effects := make([]execution.Effect, len(intent.Files))
	all := true
	for index, file := range intent.Files {
		state := execution.EffectPending
		evidence := "import_association_missing"
		if matched[index] {
			state = execution.EffectAlreadySatisfied
			evidence = "import_association_present"
		} else {
			all = false
		}
		effects[index] = effect("arr.import", targetID(file.Source), importOperation, state, observedAt, evidence)
	}
	if all {
		return execution.Observation{State: execution.ObserveSatisfied, Evidence: []string{"import_read_back", "all_associations_present"}, Effects: effects}, nil
	}
	return execution.Observation{State: execution.ObserveNeedsAction, Evidence: []string{"import_read_back", "some_associations_missing"}, Effects: effects}, nil
}

// Dispatch re-observes exact associations, validates the independent Arr
// capability and invokes only the explicit preview-bound import operation.
func (handler *ImportHandler) Dispatch(ctx context.Context, action execution.Action, attempt execution.Attempt) (execution.DispatchResult, error) {
	if err := handler.validateAction(action); err != nil {
		return execution.DispatchResult{}, err
	}
	if err := handler.guardDispatch(ctx, action); err != nil {
		return execution.DispatchResult{}, err
	}
	intent, err := importIntentFromJSON(action.DesiredState)
	if err != nil {
		return execution.DispatchResult{}, err
	}
	if err := validateImportIntent(intent, handler.options.MaxFiles); err != nil {
		return execution.DispatchResult{}, err
	}
	observed, err := handler.Observe(ctx, action)
	if err != nil {
		return execution.DispatchResult{}, err
	}
	if observed.State == execution.ObserveSatisfied {
		return execution.DispatchResult{Accepted: true, Outcome: domain.OutcomeAlreadySatisfied, Evidence: observed.Evidence, Effects: observed.Effects}, nil
	}
	if observed.State != execution.ObserveNeedsAction {
		return execution.DispatchResult{}, handler.failure(ErrStateUnknown, false)
	}
	if err := handler.requireCapability(ctx, intent.ConnectionID, importOperation); err != nil {
		return execution.DispatchResult{}, err
	}
	if err := handler.guardDispatch(ctx, action); err != nil {
		return execution.DispatchResult{}, err
	}
	result, err := handler.write.Import(ctx, intent.ConnectionID, ports.ImportRequest{
		RegisteredExternalID: intent.RegisteredExternalID,
		PreviewRevision:      intent.PreviewRevision,
		Files:                cloneImportFiles(intent.Files),
		Transfer:             intent.Transfer,
	})
	if err != nil {
		return execution.DispatchResult{}, handler.failure(err, true)
	}
	if result.ExternalID == "" || result.ExternalID != intent.RegisteredExternalID {
		return execution.DispatchResult{}, handler.failure(ErrStateUnknown, true)
	}
	outcome := domain.OutcomeApplied
	if result.Effect != nil {
		outcome = result.Effect.Outcome
		if !outcome.Valid() {
			return execution.DispatchResult{}, handler.failure(ErrStateUnknown, true)
		}
	}
	evidence := []string{"import_write_returned"}
	if result.Effect != nil {
		evidence = append(evidence, result.Effect.Evidence...)
	}
	return execution.DispatchResult{Accepted: true, Outcome: outcome, Evidence: evidence, Effects: terminalEffects(observed.Effects, outcome, handler.now(), "import_read_back")}, nil
}

// Reconcile reads exact associations and never retries after an ambiguous
// response. A safe retry is possible only when every selected association is
// still absent and the read itself was authoritative.
func (handler *ImportHandler) Reconcile(ctx context.Context, action execution.Action, attempt execution.Attempt) (execution.ReconcileResult, error) {
	observation, err := handler.Observe(ctx, action)
	if err != nil {
		return execution.ReconcileResult{}, handler.failure(err, false)
	}
	switch observation.State {
	case execution.ObserveSatisfied:
		return execution.ReconcileResult{Outcome: domain.OutcomeApplied, Evidence: append(observation.Evidence, "import_reconciled"), Effects: terminalEffects(observation.Effects, domain.OutcomeApplied, handler.now(), "import_reconciled")}, nil
	case execution.ObserveNeedsAction:
		for index := range observation.Effects {
			observation.Effects[index].State = execution.EffectPending
		}
		return execution.ReconcileResult{SafeToRetry: true, Evidence: append(observation.Evidence, "safe_to_retry_import"), Effects: observation.Effects}, nil
	default:
		return execution.ReconcileResult{Evidence: append(observation.Evidence, "import_state_unknown"), Effects: unknownEffects(observation.Effects, handler.now())}, nil
	}
}

func (handler *ImportHandler) requireCapability(ctx context.Context, connectionID domain.ConfigID, operation string) error {
	capability, err := capabilityFor(ctx, handler.capabilities, connectionID, operation)
	if err != nil {
		return handler.failure(err, false)
	}
	switch capability.State {
	case domain.CapabilitySupported:
		return nil
	case domain.CapabilityUnsupported:
		return handler.failure(fmt.Errorf("%w: %s", ErrCapabilityUnsupported, operation), false)
	default:
		return handler.failure(fmt.Errorf("%w: %s", ErrCapabilityUnknown, operation), false)
	}
}

func registrationIntentFromJSON(raw []byte) (RegistrationIntent, error) {
	var intent RegistrationIntent
	if desired, isPlanning, err := decodePlanningDesired(raw); err != nil {
		return intent, err
	} else if isPlanning {
		var found *planning.RegistrationPredicate
		for index := range desired.Predicates {
			if desired.Predicates[index].Kind != planning.PredicateRegistration || desired.Predicates[index].Registration == nil {
				continue
			}
			if found != nil {
				return intent, fmt.Errorf("%w: registration desired state has multiple predicates", ErrInvalidIntent)
			}
			value := *desired.Predicates[index].Registration
			found = &value
		}
		if found == nil {
			return intent, fmt.Errorf("%w: registration predicate is missing", ErrInvalidIntent)
		}
		return RegistrationIntent{ConnectionID: found.ConnectionID, ProviderID: found.ProviderID, Kind: found.Kind, ExternalID: found.ExternalID, Fields: cloneRegistrationFields(found.Fields)}, nil
	}
	if err := decodeIntent(raw, &intent); err != nil {
		return intent, err
	}
	return intent, nil
}

func importIntentFromJSON(raw []byte) (ImportIntent, error) {
	var intent ImportIntent
	if desired, isPlanning, err := decodePlanningDesired(raw); err != nil {
		return intent, err
	} else if isPlanning {
		var found *planning.ImportPredicate
		for index := range desired.Predicates {
			if desired.Predicates[index].Kind != planning.PredicateImport || desired.Predicates[index].Import == nil {
				continue
			}
			if found != nil {
				return intent, fmt.Errorf("%w: import desired state has multiple predicates", ErrInvalidIntent)
			}
			value := *desired.Predicates[index].Import
			found = &value
		}
		if found == nil {
			return intent, fmt.Errorf("%w: import predicate is missing", ErrInvalidIntent)
		}
		files := make([]ports.ImportFile, 0, len(found.Files))
		for _, selection := range found.Files {
			if len(selection.EpisodeIDs) > 1 {
				return intent, fmt.Errorf("%w: planning import episode set needs explicit handler envelope", ErrInvalidIntent)
			}
			files = append(files, ports.ImportFile{Source: selection.Source, MovieOrEpisodeID: selection.MovieOrEpisodeID, Subtitle: selection.Subtitle, Language: selection.Language, Forced: selection.Forced, HearingImpaired: selection.HearingImpaired})
		}
		return ImportIntent{ConnectionID: found.ConnectionID, RegisteredExternalID: found.RegisteredExternalID, Files: files, Transfer: found.Transfer}, nil
	}
	if err := decodeIntent(raw, &intent); err != nil {
		return intent, err
	}
	return intent, nil
}

func validMediaKind(kind domain.MediaKind) bool {
	return kind == domain.MediaMovie || kind == domain.MediaEpisode || kind == domain.MediaSeason || kind == domain.MediaAnime
}

func listMediaRecords(ctx context.Context, read ports.MediaManagerReadPort, connectionID domain.ConfigID, options HandlerOptions) ([]ports.MediaRecord, time.Time, error) {
	if read == nil {
		return nil, time.Time{}, ErrDependencyRequired
	}
	if !connectionID.Valid() {
		return nil, time.Time{}, fmt.Errorf("%w: invalid Arr connection", ErrInvalidIntent)
	}
	cursor := ""
	items := make([]ports.MediaRecord, 0)
	seenIDs := make(map[string]struct{})
	var observedAt time.Time
	for pageNumber := 0; pageNumber < options.MaxItems; pageNumber++ {
		page, err := read.List(ctx, connectionID, cursor, min(options.MaxItems, 500))
		if err != nil {
			return nil, time.Time{}, err
		}
		if err := page.Coverage.Validate(); err != nil {
			return nil, time.Time{}, fmt.Errorf("%w: Arr catalog coverage is invalid: %v", ErrStateUnknown, err)
		}
		if page.Coverage.ConnectionID != "" && page.Coverage.ConnectionID != connectionID {
			return nil, time.Time{}, fmt.Errorf("%w: Arr catalog coverage belongs to another connection", ErrStateUnknown)
		}
		if !page.Coverage.ObservedAt.IsZero() && page.Coverage.ObservedAt.After(observedAt) {
			observedAt = page.Coverage.ObservedAt
		}
		if len(items)+len(page.Items) > options.MaxItems {
			return nil, time.Time{}, fmt.Errorf("%w: Arr catalog exceeds bound", ErrInvalidIntent)
		}
		for index, record := range page.Items {
			if strings.TrimSpace(record.ExternalID) == "" || strings.TrimSpace(record.ProviderID) == "" || !validMediaKind(record.Kind) {
				return nil, time.Time{}, fmt.Errorf("%w: Arr catalog record %d has incomplete identity", ErrStateUnknown, index)
			}
			if _, exists := seenIDs[record.ExternalID]; exists {
				return nil, time.Time{}, fmt.Errorf("%w: Arr catalog has duplicate external identity %q", ErrStateUnknown, record.ExternalID)
			}
			seenIDs[record.ExternalID] = struct{}{}
		}
		items = append(items, page.Items...)
		if page.NextCursor == "" {
			if page.Coverage.Completeness != domain.CompletenessComplete {
				return nil, time.Time{}, fmt.Errorf("%w: Arr catalog coverage is not complete", ErrStateUnknown)
			}
			if observedAt.IsZero() {
				observedAt = options.Now()
			}
			return items, observedAt.UTC(), nil
		}
		if page.NextCursor == cursor || len(page.NextCursor) > 256 {
			return nil, time.Time{}, fmt.Errorf("%w: Arr catalog cursor did not advance", ErrStateUnknown)
		}
		if page.Coverage.Completeness != domain.CompletenessPartial && page.Coverage.Completeness != domain.CompletenessComplete {
			return nil, time.Time{}, fmt.Errorf("%w: Arr catalog continuation coverage is unknown", ErrStateUnknown)
		}
		cursor = page.NextCursor
	}
	return nil, time.Time{}, fmt.Errorf("%w: Arr catalog page bound exceeded", ErrStateUnknown)
}

func matchingRegistrationRecords(records []ports.MediaRecord, intent RegistrationIntent) (*ports.MediaRecord, error) {
	var matched *ports.MediaRecord
	for index := range records {
		record := records[index]
		if record.Kind != intent.Kind || !providerMatches(record.ProviderID, intent.ProviderID) {
			continue
		}
		if matched != nil {
			return nil, fmt.Errorf("%w: provider maps to multiple Arr records", ErrStateUnknown)
		}
		copyValue := record
		copyValue.Files = append([]ports.MediaFile(nil), record.Files...)
		matched = &copyValue
	}
	return matched, nil
}

func providerMatches(left, right string) bool {
	return strings.EqualFold(strings.TrimSpace(left), strings.TrimSpace(right)) && strings.TrimSpace(right) != ""
}

func registrationFieldsMatch(record ports.MediaRecord, fields ports.RegistrationFields) bool {
	if fields.Monitored != nil && record.Monitored != *fields.Monitored {
		return false
	}
	// MediaRecord intentionally does not expose root/profile/series fields.
	// A non-empty requested field therefore cannot be proven at this read
	// boundary and must remain actionable rather than falsely satisfied.
	return fields.RootFolder == "" && fields.QualityProfileID == "" && fields.SeriesType == "" && fields.SeasonFolder == nil && len(fields.Seasons) == 0
}

func validateImportIntent(intent ImportIntent, maxFiles int) error {
	if !intent.ConnectionID.Valid() || strings.TrimSpace(intent.RegisteredExternalID) == "" || strings.TrimSpace(intent.PreviewRevision) == "" {
		return fmt.Errorf("%w: import identity and preview revision are required", ErrInvalidIntent)
	}
	if intent.Transfer != "copy" && intent.Transfer != "move" {
		return fmt.Errorf("%w: import transfer mode is invalid", ErrInvalidIntent)
	}
	if len(intent.Files) == 0 || len(intent.Files) > maxFiles {
		return fmt.Errorf("%w: import file bound is invalid", ErrInvalidIntent)
	}
	seen := make(map[string]struct{}, len(intent.Files))
	for index, file := range intent.Files {
		if err := file.Source.Validate(); err != nil {
			return fmt.Errorf("%w: import file %d source: %v", ErrInvalidIntent, index, err)
		}
		if strings.TrimSpace(file.MovieOrEpisodeID) == "" {
			return fmt.Errorf("%w: import file %d media identity is required", ErrInvalidIntent, index)
		}
		key := targetID(file.Source)
		if _, exists := seen[key]; exists {
			return fmt.Errorf("%w: import source is duplicated", ErrInvalidIntent)
		}
		seen[key] = struct{}{}
	}
	return nil
}

func matchImportFiles(records []ports.MediaFile, requested []ports.ImportFile) ([]bool, error) {
	byPath := make(map[string]ports.MediaFile, len(records))
	for _, record := range records {
		key := targetID(record.Path)
		if _, exists := byPath[key]; exists {
			return nil, fmt.Errorf("%w: import read-back has duplicate file path", ErrStateUnknown)
		}
		if err := record.Path.Validate(); err != nil {
			return nil, fmt.Errorf("%w: import read-back path is invalid", ErrStateUnknown)
		}
		byPath[key] = record
	}
	matched := make([]bool, len(requested))
	for index, request := range requested {
		record, exists := byPath[targetID(request.Source)]
		if !exists {
			continue
		}
		if importAssociationMatches(record, request.MovieOrEpisodeID) {
			matched[index] = true
		}
	}
	return matched, nil
}

func importAssociationMatches(record ports.MediaFile, wanted string) bool {
	if strings.TrimSpace(wanted) == "" {
		return false
	}
	if record.MovieID == wanted {
		return true
	}
	for _, episodeID := range record.EpisodeIDs {
		if episodeID == wanted {
			return true
		}
	}
	return false
}

// validateImportPreviewBinding proves that the immutable preview selected by
// the action is the same exact file set the upstream read port currently
// previews. A nonempty revision alone is not evidence: a changed selection,
// an omitted file, or any rejection must stop before a write can be called.
func validateImportPreviewBinding(intent ImportIntent, preview ports.ImportPreview) error {
	if preview.Revision == "" || preview.Revision != intent.PreviewRevision {
		return fmt.Errorf("%w: import preview revision does not match the approved request", ErrStateUnknown)
	}
	if preview.ObservedAt.IsZero() {
		return fmt.Errorf("%w: import preview observation time is missing", ErrStateUnknown)
	}
	if len(preview.Files) != len(intent.Files) {
		return fmt.Errorf("%w: import preview file selection differs from the approved request", ErrStateUnknown)
	}
	wanted := make(map[string]struct{}, len(intent.Files))
	for _, file := range intent.Files {
		key, err := importFileKey(file)
		if err != nil {
			return err
		}
		if _, exists := wanted[key]; exists {
			return fmt.Errorf("%w: import preview request has duplicate file identity", ErrStateUnknown)
		}
		wanted[key] = struct{}{}
	}
	for _, file := range preview.Files {
		key, err := importFileKey(file)
		if err != nil {
			return fmt.Errorf("%w: import preview contains malformed file evidence", ErrStateUnknown)
		}
		if _, exists := wanted[key]; !exists {
			return fmt.Errorf("%w: import preview contains an unselected file", ErrStateUnknown)
		}
		delete(wanted, key)
	}
	if len(wanted) != 0 {
		return fmt.Errorf("%w: import preview omits a selected file", ErrStateUnknown)
	}
	for _, rejection := range preview.Rejections {
		if err := rejection.Source.Validate(); err != nil || strings.TrimSpace(rejection.Code) == "" || strings.TrimSpace(rejection.Reason) == "" {
			return fmt.Errorf("%w: import preview contains malformed rejection evidence", ErrStateUnknown)
		}
		return fmt.Errorf("%w: import preview rejected a selected file", ErrStateUnknown)
	}
	return nil
}

func importFileKey(file ports.ImportFile) (string, error) {
	if err := file.Source.Validate(); err != nil {
		return "", fmt.Errorf("%w: import preview source is invalid", ErrStateUnknown)
	}
	if strings.TrimSpace(file.MovieOrEpisodeID) == "" {
		return "", fmt.Errorf("%w: import preview media identity is missing", ErrStateUnknown)
	}
	return strings.Join([]string{
		targetID(file.Source), file.MovieOrEpisodeID,
		fmt.Sprintf("%t", file.Subtitle), file.Language,
		fmt.Sprintf("%t", file.Forced), fmt.Sprintf("%t", file.HearingImpaired),
	}, "\x00"), nil
}

func capabilityFor(ctx context.Context, port ports.CapabilityPort, connectionID domain.ConfigID, name string) (domain.Capability, error) {
	if port == nil {
		return domain.Capability{}, fmt.Errorf("%w: capability port is not configured", ErrCapabilityUnknown)
	}
	if ctx == nil {
		return domain.Capability{}, context.Canceled
	}
	if !connectionID.Valid() {
		return domain.Capability{}, fmt.Errorf("%w: capability connection is invalid", ErrInvalidIntent)
	}
	capabilities, err := port.Capabilities(ctx, connectionID)
	if err != nil {
		return domain.Capability{}, err
	}
	var found *domain.Capability
	for index := range capabilities {
		if capabilities[index].Name != name {
			continue
		}
		candidate := capabilities[index]
		if err := candidate.Validate(); err != nil {
			return domain.Capability{}, fmt.Errorf("%w: capability %q evidence is invalid: %v", ErrStateUnknown, name, err)
		}
		if found != nil {
			return domain.Capability{}, fmt.Errorf("%w: conflicting capability evidence", ErrStateUnknown)
		}
		copyValue := candidate
		copyValue.Evidence = append([]string(nil), candidate.Evidence...)
		found = &copyValue
	}
	if found == nil {
		return domain.Capability{}, fmt.Errorf("%w: capability %q is absent", ErrCapabilityUnknown, name)
	}
	return *found, nil
}

func terminalEffects(effects []execution.Effect, outcome domain.EffectOutcome, observedAt time.Time, evidence string) []execution.Effect {
	result := make([]execution.Effect, len(effects))
	copy(result, effects)
	for index := range result {
		// A partial action can contain targets already materialized by an
		// earlier attempt. Preserve that read-proven state while terminalizing
		// only pending targets reached by this dispatch.
		if result[index].State != execution.EffectAlreadySatisfied || outcome == domain.OutcomeAlreadySatisfied {
			result[index].State = effectState(outcome)
		}
		result[index].ObservedAt = observedAt.UTC().Format(time.RFC3339Nano)
		result[index].Evidence = evidenceJSON([]string{evidence})
	}
	return result
}

func unknownEffects(effects []execution.Effect, observedAt time.Time) []execution.Effect {
	result := make([]execution.Effect, len(effects))
	copy(result, effects)
	for index := range result {
		result[index].State = execution.EffectUnknown
		result[index].ObservedAt = observedAt.UTC().Format(time.RFC3339Nano)
		result[index].Evidence = evidenceJSON([]string{"read_back_unknown"})
	}
	return result
}

func min(left, right int) int {
	if left < right {
		return left
	}
	return right
}

// Keep errors imported by future adapters from changing this package's
// classification semantics accidentally.
var _ = errors.Is
