package actions

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/guilycst/mastarr/internal/domain"
	"github.com/guilycst/mastarr/internal/execution"
	"github.com/guilycst/mastarr/internal/ports"
)

const (
	refreshOperation        = "jellyfin.refresh"
	refreshLibraryOperation = "jellyfin.refresh.library"
	refreshItemOperation    = "jellyfin.refresh.item"
)

// RefreshConfig wires the read-only capability probe and the explicit
// Jellyfin refresh port.
type RefreshConfig struct {
	Refresh      ports.MediaServerRefreshPort
	Capabilities ports.CapabilityPort
	Options      HandlerOptions
}

// RefreshHandler implements jellyfin.refresh. Acceptance of a native refresh
// request is retained as its own evidence and is never presented as library
// availability.
type RefreshHandler struct {
	baseHandler
	refresh      ports.MediaServerRefreshPort
	capabilities ports.CapabilityPort
}

var _ execution.Handler = (*RefreshHandler)(nil)

var refreshAcceptance = struct {
	sync.Mutex
	values map[string][]string
}{values: make(map[string][]string)}

// NewRefreshHandler validates the refresh/capability ports.
func NewRefreshHandler(config RefreshConfig) (*RefreshHandler, error) {
	if config.Refresh == nil || config.Capabilities == nil {
		return nil, fmt.Errorf("%w: refresh/capability ports", ErrDependencyRequired)
	}
	return &RefreshHandler{baseHandler: baseHandler{kind: domain.ActionJellyfinRefresh, options: config.Options.normalized()}, refresh: config.Refresh, capabilities: config.Capabilities}, nil
}

// NewJellyfinRefreshHandler is an explicit constructor alias.
func NewJellyfinRefreshHandler(config RefreshConfig) (*RefreshHandler, error) {
	return NewRefreshHandler(config)
}

// Reservations binds one Jellyfin connection and closed refresh scope.
func (handler *RefreshHandler) Reservations(action execution.Action) []string {
	intent, err := refreshIntentFromJSON(action.DesiredState)
	if err != nil {
		return nil
	}
	return []string{"jellyfin:" + intent.ConnectionID.String() + ":" + string(intent.Scope)}
}

// Observe gates a library refresh on a current supported capability. Item
// scope stays fail-closed because acceptance does not prove availability.
func (handler *RefreshHandler) Observe(ctx context.Context, action execution.Action) (execution.Observation, error) {
	if err := handler.validateAction(action); err != nil {
		return execution.Observation{}, err
	}
	if err := handler.guardObserve(ctx); err != nil {
		return execution.Observation{}, err
	}
	intent, err := refreshIntentFromJSON(action.DesiredState)
	if err != nil {
		return execution.Observation{}, err
	}
	if err := validateRefreshIntent(intent); err != nil {
		return execution.Observation{}, err
	}
	if accepted, evidence := knownRefreshAcceptance(action.ID); accepted || persistedRefreshAcceptance(action) {
		if len(evidence) == 0 {
			evidence = []string{"refresh_acceptance_persisted", "availability_requires_later_read"}
		}
		return execution.Observation{State: execution.ObserveSatisfied, Evidence: evidence, Effects: []execution.Effect{effect("jellyfin", intent.ConnectionID.String()+":"+string(intent.Scope), refreshOperation, execution.EffectAlreadySatisfied, handler.now(), "refresh_acceptance_recorded")}}, nil
	}
	if intent.Scope == ports.RefreshItem {
		return execution.Observation{State: execution.ObserveUnknown, Evidence: []string{"item_refresh_scope_unsupported", "availability_requires_later_read"}, Effects: []execution.Effect{effect("jellyfin", intent.ConnectionID.String()+":item", refreshItemOperation, execution.EffectUnknown, handler.now(), "item_scope_unsupported")}}, nil
	}
	capability, err := capabilityFor(ctx, handler.capabilities, intent.ConnectionID, refreshLibraryOperation)
	if err != nil {
		return execution.Observation{}, handler.failure(err, false)
	}
	if capability.State != domain.CapabilitySupported {
		return execution.Observation{State: execution.ObserveUnknown, Evidence: []string{"refresh_capability_not_supported", capabilityReason(capability)}, Effects: []execution.Effect{effect("jellyfin", intent.ConnectionID.String()+":library", refreshLibraryOperation, execution.EffectUnknown, handler.now(), "refresh_blocked")}}, nil
	}
	return execution.Observation{State: execution.ObserveNeedsAction, Evidence: []string{"refresh_capability_supported", "availability_requires_later_read"}, Effects: []execution.Effect{effect("jellyfin", intent.ConnectionID.String()+":library", refreshLibraryOperation, execution.EffectPending, handler.now(), "refresh_request_required")}}, nil
}

// Dispatch performs a capability-gated library refresh. A successful native
// response is recorded as acceptance only; a later inventory read is needed
// to establish Jellyfin availability.
func (handler *RefreshHandler) Dispatch(ctx context.Context, action execution.Action, attempt execution.Attempt) (execution.DispatchResult, error) {
	if err := handler.validateAction(action); err != nil {
		return execution.DispatchResult{}, err
	}
	if err := handler.guardDispatch(ctx, action); err != nil {
		return execution.DispatchResult{}, err
	}
	intent, err := refreshIntentFromJSON(action.DesiredState)
	if err != nil {
		return execution.DispatchResult{}, err
	}
	if err := validateRefreshIntent(intent); err != nil {
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
	if err := handler.guardDispatch(ctx, action); err != nil {
		return execution.DispatchResult{}, err
	}
	result, err := handler.refresh.Refresh(ctx, intent.ConnectionID, ports.RefreshRequest{Scope: intent.Scope, ExternalID: intent.ExternalID})
	if err != nil {
		return execution.DispatchResult{}, handler.failure(err, !isKnownPreDispatchRefreshError(err))
	}
	if !result.Accepted {
		return execution.DispatchResult{}, handler.failure(ErrStateUnknown, true)
	}
	evidence := append([]string{"refresh_request_accepted", "availability_requires_later_read"}, result.Evidence...)
	rememberRefreshAcceptance(action.ID, evidence)
	return execution.DispatchResult{Accepted: true, Outcome: domain.OutcomeApplied, Evidence: evidence, Effects: terminalEffects(observation.Effects, domain.OutcomeApplied, handler.now(), "refresh_request_accepted")}, nil
}

// Reconcile can complete only when this process retained explicit acceptance
// evidence for the exact action. Otherwise it returns unknown and does not
// retry a request that may already have been accepted remotely.
func (handler *RefreshHandler) Reconcile(ctx context.Context, action execution.Action, attempt execution.Attempt) (execution.ReconcileResult, error) {
	observation, err := handler.Observe(ctx, action)
	if err != nil {
		return execution.ReconcileResult{}, handler.failure(err, false)
	}
	if observation.State == execution.ObserveSatisfied {
		return execution.ReconcileResult{Outcome: domain.OutcomeApplied, Evidence: append(observation.Evidence, "refresh_acceptance_reconciled"), Effects: terminalEffects(observation.Effects, domain.OutcomeApplied, handler.now(), "refresh_acceptance_reconciled")}, nil
	}
	return execution.ReconcileResult{Evidence: append(observation.Evidence, "refresh_acceptance_or_availability_unknown"), Effects: unknownEffects(observation.Effects, handler.now())}, nil
}

func refreshIntentFromJSON(raw []byte) (RefreshIntent, error) {
	var intent RefreshIntent
	if err := decodeIntent(raw, &intent); err != nil {
		return intent, err
	}
	return intent, nil
}

func validateRefreshIntent(intent RefreshIntent) error {
	if !intent.ConnectionID.Valid() {
		return fmt.Errorf("%w: refresh connection is invalid", ErrInvalidIntent)
	}
	if intent.Scope != ports.RefreshLibrary && intent.Scope != ports.RefreshItem {
		return fmt.Errorf("%w: refresh scope is invalid", ErrInvalidIntent)
	}
	if intent.Scope == ports.RefreshLibrary && intent.ExternalID != "" {
		return fmt.Errorf("%w: library refresh cannot include an item ID", ErrInvalidIntent)
	}
	if intent.Scope == ports.RefreshItem && strings.TrimSpace(intent.ExternalID) == "" {
		return fmt.Errorf("%w: item refresh requires an item ID", ErrInvalidIntent)
	}
	return nil
}

func rememberRefreshAcceptance(actionID string, evidence []string) {
	refreshAcceptance.Lock()
	refreshAcceptance.values[actionID] = append([]string(nil), evidence...)
	refreshAcceptance.Unlock()
}

func knownRefreshAcceptance(actionID string) (bool, []string) {
	refreshAcceptance.Lock()
	defer refreshAcceptance.Unlock()
	evidence, ok := refreshAcceptance.values[actionID]
	return ok, append([]string(nil), evidence...)
}

func persistedRefreshAcceptance(action execution.Action) bool {
	if action.State != domain.ActionSucceeded || len(action.Outcome) == 0 || string(action.Outcome) == "null" {
		return false
	}
	var outcome struct {
		Value domain.EffectOutcome `json:"outcome"`
	}
	if err := json.Unmarshal(action.Outcome, &outcome); err != nil {
		return false
	}
	return outcome.Value == domain.OutcomeApplied || outcome.Value == domain.OutcomeAlreadySatisfied
}

func capabilityReason(capability domain.Capability) string {
	if capability.Reason != "" {
		return capability.Reason
	}
	return string(capability.State)
}

func isKnownPreDispatchRefreshError(err error) bool {
	var upstream domain.UpstreamError
	if !errors.As(err, &upstream) {
		return false
	}
	return upstream.Code == domain.OutcomeUnsupported || upstream.Code == domain.OutcomeConflict || upstream.Code == domain.OutcomeInvalidInput || upstream.Code == domain.OutcomeUnauthorized
}
