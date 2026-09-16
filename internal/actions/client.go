package actions

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/guilycst/mastarr/internal/domain"
	"github.com/guilycst/mastarr/internal/execution"
	"github.com/guilycst/mastarr/internal/ports"
)

const (
	clientStopOperation   = "client.stop"
	clientRemoveOperation = "client.remove"
)

// ClientConfig wires qBittorrent/NZBGet control and capability ports. The
// action package consumes only the normalized DownloadControlPort, so native
// DTOs and transport errors remain in their adapters/client modules.
type ClientConfig struct {
	Control      ports.DownloadControlPort
	Capabilities ports.CapabilityPort
	Options      HandlerOptions
}

// ClientHandlers groups independent stop and metadata-removal handlers.
type ClientHandlers struct {
	Stop   *ClientHandler
	Remove *ClientHandler
}

// NewClientHandlers constructs the two download-client action handlers.
func NewClientHandlers(config ClientConfig) (ClientHandlers, error) {
	if config.Control == nil || config.Capabilities == nil {
		return ClientHandlers{}, fmt.Errorf("%w: client control/capability ports", ErrDependencyRequired)
	}
	options := config.Options.normalized()
	return ClientHandlers{
		Stop:   &ClientHandler{baseHandler: baseHandler{kind: domain.ActionClientStop, options: options}, control: config.Control, capabilities: config.Capabilities, operation: clientStopOperation},
		Remove: &ClientHandler{baseHandler: baseHandler{kind: domain.ActionClientRemove, options: options}, control: config.Control, capabilities: config.Capabilities, operation: clientRemoveOperation},
	}, nil
}

// NewClientStopHandler constructs one stop handler.
func NewClientStopHandler(config ClientConfig) (*ClientHandler, error) {
	handlers, err := NewClientHandlers(config)
	return handlers.Stop, err
}

// NewClientRemoveHandler constructs one metadata-removal handler.
func NewClientRemoveHandler(config ClientConfig) (*ClientHandler, error) {
	handlers, err := NewClientHandlers(config)
	return handlers.Remove, err
}

// ClientHandler implements exactly one download-client action kind.
type ClientHandler struct {
	baseHandler
	control      ports.DownloadControlPort
	capabilities ports.CapabilityPort
	operation    string
}

var _ execution.Handler = (*ClientHandler)(nil)

// Reservations binds the exact client record to this action.
func (handler *ClientHandler) Reservations(action execution.Action) []string {
	intent, err := clientIntentFromJSON(action.DesiredState)
	if err != nil {
		return nil
	}
	return []string{"client:" + refID(intent.Ref)}
}

// Observe reads the exact client record. Stop requires a known paused/stopped
// state; remove requires a known stopped state and treats an authoritative
// not-found callback as already satisfied.
func (handler *ClientHandler) Observe(ctx context.Context, action execution.Action) (execution.Observation, error) {
	if err := handler.validateAction(action); err != nil {
		return execution.Observation{}, err
	}
	if err := handler.guardObserve(ctx); err != nil {
		return execution.Observation{}, err
	}
	intent, err := clientIntentFromJSON(action.DesiredState)
	if err != nil {
		return execution.Observation{}, err
	}
	if err := validateClientIntent(intent); err != nil {
		return execution.Observation{}, err
	}
	observed, err := handler.control.Observe(ctx, intent.Ref)
	if err != nil {
		if handler.kind == domain.ActionClientRemove && classifyNotFound(err, handler.options) {
			return execution.Observation{State: execution.ObserveSatisfied, Evidence: []string{"client_record_absent"}, Effects: []execution.Effect{effect("client", refID(intent.Ref), handler.operation, execution.EffectAlreadySatisfied, handler.now(), "record_absent")}}, nil
		}
		return execution.Observation{}, handler.failure(err, false)
	}
	if observed.Ref != intent.Ref {
		return execution.Observation{}, handler.failure(fmt.Errorf("%w: client read-back belongs to another reference", ErrStateUnknown), false)
	}
	if observed.ObservedAt.IsZero() {
		observed.ObservedAt = handler.now()
	}
	if strings.TrimSpace(observed.State) == "" {
		return execution.Observation{State: execution.ObserveUnknown, Evidence: []string{"client_state_missing"}, Effects: []execution.Effect{effect("client", refID(intent.Ref), handler.operation, execution.EffectUnknown, observed.ObservedAt, "client_state_missing")}}, nil
	}
	if handler.kind == domain.ActionClientStop {
		state := execution.EffectPending
		evidence := "client_active"
		observationState := execution.ObserveNeedsAction
		if isStoppedDownload(observed) {
			state = execution.EffectAlreadySatisfied
			evidence = "client_stopped"
			observationState = execution.ObserveSatisfied
		}
		return execution.Observation{State: observationState, Evidence: []string{"client_state_read_back", evidence}, Effects: []execution.Effect{effect("client", refID(intent.Ref), handler.operation, state, observed.ObservedAt, evidence)}}, nil
	}
	if !isStoppedDownload(observed) {
		return execution.Observation{State: execution.ObserveUnknown, Evidence: []string{"client_remove_requires_stopped_state", "client_active"}, Effects: []execution.Effect{effect("client", refID(intent.Ref), handler.operation, execution.EffectUnknown, observed.ObservedAt, "remove_blocked_active")}}, nil
	}
	return execution.Observation{State: execution.ObserveNeedsAction, Evidence: []string{"client_stopped", "metadata_removal_required"}, Effects: []execution.Effect{effect("client", refID(intent.Ref), handler.operation, execution.EffectPending, observed.ObservedAt, "metadata_removal_required")}}, nil
}

// Dispatch re-observes and checks the independent operation capability before
// invoking Stop or Remove. Remove is metadata-only in the port contract.
func (handler *ClientHandler) Dispatch(ctx context.Context, action execution.Action, attempt execution.Attempt) (execution.DispatchResult, error) {
	if err := handler.validateAction(action); err != nil {
		return execution.DispatchResult{}, err
	}
	if err := handler.guardDispatch(ctx, action); err != nil {
		return execution.DispatchResult{}, err
	}
	intent, err := clientIntentFromJSON(action.DesiredState)
	if err != nil {
		return execution.DispatchResult{}, err
	}
	if err := validateClientIntent(intent); err != nil {
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
	if err := handler.requireCapability(ctx, intent.Ref.ConnectionID, handler.operation); err != nil {
		return execution.DispatchResult{}, err
	}
	if err := handler.guardDispatch(ctx, action); err != nil {
		return execution.DispatchResult{}, err
	}
	var result ports.ClientEffect
	switch handler.kind {
	case domain.ActionClientStop:
		result, err = handler.control.Stop(ctx, intent.Ref)
	case domain.ActionClientRemove:
		result, err = handler.control.Remove(ctx, intent.Ref)
	default:
		return execution.DispatchResult{}, fmt.Errorf("%w: client action kind is unsupported", ErrInvalidIntent)
	}
	if err != nil {
		return execution.DispatchResult{}, handler.failure(err, !isKnownPreDispatchClientError(err))
	}
	outcome := result.Outcome
	if !outcome.Valid() {
		return execution.DispatchResult{}, handler.failure(ErrStateUnknown, true)
	}
	return execution.DispatchResult{Accepted: true, Outcome: outcome, Evidence: append([]string{"client_write_returned"}, result.Evidence...), Effects: terminalEffects(observation.Effects, outcome, handler.now(), "client_read_back")}, nil
}

// Reconcile keeps client effects unresolved until a fresh read proves the
// selected state. Removal can be retried only after an explicit not-found
// observation or an exact stopped record.
func (handler *ClientHandler) Reconcile(ctx context.Context, action execution.Action, attempt execution.Attempt) (execution.ReconcileResult, error) {
	observation, err := handler.Observe(ctx, action)
	if err != nil {
		return execution.ReconcileResult{}, handler.failure(err, false)
	}
	switch observation.State {
	case execution.ObserveSatisfied:
		return execution.ReconcileResult{Outcome: domain.OutcomeApplied, Evidence: append(observation.Evidence, "client_reconciled"), Effects: terminalEffects(observation.Effects, domain.OutcomeApplied, handler.now(), "client_reconciled")}, nil
	case execution.ObserveNeedsAction:
		for index := range observation.Effects {
			observation.Effects[index].State = execution.EffectPending
		}
		return execution.ReconcileResult{SafeToRetry: true, Evidence: append(observation.Evidence, "safe_to_retry_client"), Effects: observation.Effects}, nil
	default:
		return execution.ReconcileResult{Evidence: append(observation.Evidence, "client_state_unknown"), Effects: unknownEffects(observation.Effects, handler.now())}, nil
	}
}

func (handler *ClientHandler) requireCapability(ctx context.Context, connectionID domain.ConfigID, operation string) error {
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

func clientIntentFromJSON(raw []byte) (ClientIntent, error) {
	var intent ClientIntent
	if err := decodeIntent(raw, &intent); err != nil {
		return intent, err
	}
	return intent, nil
}

func validateClientIntent(intent ClientIntent) error {
	if err := validateRef(intent.Ref); err != nil {
		return err
	}
	if intent.Destination != nil || intent.Source != nil || strings.TrimSpace(intent.NewName) != "" {
		return fmt.Errorf("%w: client stop/remove does not accept filesystem fields", ErrInvalidIntent)
	}
	return nil
}

func isKnownPreDispatchClientError(err error) bool {
	var upstream domain.UpstreamError
	if !errors.As(err, &upstream) {
		return false
	}
	switch upstream.Code {
	case domain.OutcomeUnsupported, domain.OutcomeConflict, domain.OutcomeInvalidInput, domain.OutcomeUnauthorized:
		return true
	default:
		return false
	}
}
