package actions

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/guilycst/mastarr/internal/descriptors"
	"github.com/guilycst/mastarr/internal/domain"
	"github.com/guilycst/mastarr/internal/execution"
)

const descriptorDeleteOperation = "descriptor.delete"

// DescriptorConfig wires the metadata-only descriptor service.
type DescriptorConfig struct {
	Port    DescriptorPort
	Options HandlerOptions
}

// DescriptorHandler implements exact acknowledged descriptor deletion.
type DescriptorHandler struct {
	baseHandler
	port DescriptorPort
}

var _ execution.Handler = (*DescriptorHandler)(nil)

// NewDescriptorHandler validates the descriptor metadata port.
func NewDescriptorHandler(config DescriptorConfig) (*DescriptorHandler, error) {
	if config.Port == nil {
		return nil, fmt.Errorf("%w: descriptor port", ErrDependencyRequired)
	}
	return &DescriptorHandler{baseHandler: baseHandler{kind: domain.ActionDescriptorDelete, options: config.Options.normalized()}, port: config.Port}, nil
}

// Reservations binds the exact descriptor ID to this action.
func (handler *DescriptorHandler) Reservations(action execution.Action) []string {
	intent, err := descriptorIntentFromJSON(action.DesiredState)
	if err != nil {
		return nil
	}
	return []string{"descriptor:" + intent.DescriptorID}
}

// Observe reads descriptor metadata only. A deleted or absent object is
// already satisfied, while an available object requires an explicit delete.
func (handler *DescriptorHandler) Observe(ctx context.Context, action execution.Action) (execution.Observation, error) {
	if err := handler.validateAction(action); err != nil {
		return execution.Observation{}, err
	}
	if err := handler.guardObserve(ctx); err != nil {
		return execution.Observation{}, err
	}
	intent, err := descriptorIntentFromJSON(action.DesiredState)
	if err != nil {
		return execution.Observation{}, err
	}
	if err := validateDescriptorIntent(intent); err != nil {
		return execution.Observation{}, err
	}
	record, err := handler.port.Get(ctx, intent.DescriptorID)
	if err != nil {
		if errors.Is(err, descriptors.ErrDescriptorNotFound) {
			return execution.Observation{State: execution.ObserveSatisfied, Evidence: []string{"descriptor_absent"}, Effects: []execution.Effect{effect("descriptor", intent.DescriptorID, descriptorDeleteOperation, execution.EffectAlreadySatisfied, handler.now(), "descriptor_absent")}}, nil
		}
		return execution.Observation{}, handler.failure(err, false)
	}
	if strings.TrimSpace(record.ID) == "" || record.ID != intent.DescriptorID {
		return execution.Observation{}, handler.failure(fmt.Errorf("%w: descriptor read-back belongs to another ID", ErrStateUnknown), false)
	}
	if record.DeletedAt != nil || record.Retention == descriptors.RetentionDeleted {
		return execution.Observation{State: execution.ObserveSatisfied, Evidence: []string{"descriptor_deleted"}, Effects: []execution.Effect{effect("descriptor", intent.DescriptorID, descriptorDeleteOperation, execution.EffectAlreadySatisfied, handler.now(), "descriptor_deleted")}}, nil
	}
	return execution.Observation{State: execution.ObserveNeedsAction, Evidence: []string{"descriptor_retained", "delete_acknowledgement_present"}, Effects: []execution.Effect{effect("descriptor", intent.DescriptorID, descriptorDeleteOperation, execution.EffectPending, handler.now(), "descriptor_delete_required")}}, nil
}

// Dispatch re-observes metadata before invoking the descriptor service's
// durable delete operation. The service retains audit metadata and handles
// unlink/database uncertainty; this handler never retrieves content bytes.
func (handler *DescriptorHandler) Dispatch(ctx context.Context, action execution.Action, attempt execution.Attempt) (execution.DispatchResult, error) {
	if err := handler.validateAction(action); err != nil {
		return execution.DispatchResult{}, err
	}
	if err := handler.guardDispatch(ctx, action); err != nil {
		return execution.DispatchResult{}, err
	}
	intent, err := descriptorIntentFromJSON(action.DesiredState)
	if err != nil {
		return execution.DispatchResult{}, err
	}
	if err := validateDescriptorIntent(intent); err != nil {
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
	_, err = handler.port.Delete(ctx, descriptors.DeleteRequest{DescriptorID: intent.DescriptorID, IrreversibleAcknowledged: intent.IrreversibleAcknowledged})
	if err != nil {
		return execution.DispatchResult{}, handler.failure(err, !isKnownPreDispatchDescriptorError(err))
	}
	return execution.DispatchResult{Accepted: true, Outcome: domain.OutcomeApplied, Evidence: []string{"descriptor_delete_returned", "audit_metadata_retained"}, Effects: terminalEffects(observation.Effects, domain.OutcomeApplied, handler.now(), "descriptor_deleted")}, nil
}

// Reconcile reads durable descriptor metadata and reports an unresolved state
// when the service has retained a pending deletion intent.
func (handler *DescriptorHandler) Reconcile(ctx context.Context, action execution.Action, attempt execution.Attempt) (execution.ReconcileResult, error) {
	observation, err := handler.Observe(ctx, action)
	if err != nil {
		return execution.ReconcileResult{}, handler.failure(err, false)
	}
	switch observation.State {
	case execution.ObserveSatisfied:
		return execution.ReconcileResult{Outcome: domain.OutcomeApplied, Evidence: append(observation.Evidence, "descriptor_reconciled"), Effects: terminalEffects(observation.Effects, domain.OutcomeApplied, handler.now(), "descriptor_reconciled")}, nil
	case execution.ObserveNeedsAction:
		for index := range observation.Effects {
			observation.Effects[index].State = execution.EffectPending
		}
		return execution.ReconcileResult{SafeToRetry: true, Evidence: append(observation.Evidence, "safe_to_retry_descriptor_delete"), Effects: observation.Effects}, nil
	default:
		return execution.ReconcileResult{Evidence: []string{"descriptor_delete_state_unknown"}, Effects: unknownEffects(observation.Effects, handler.now())}, nil
	}
}

func descriptorIntentFromJSON(raw []byte) (DescriptorIntent, error) {
	var intent DescriptorIntent
	if err := decodeIntent(raw, &intent); err != nil {
		return intent, err
	}
	return intent, nil
}

func validateDescriptorIntent(intent DescriptorIntent) error {
	if strings.TrimSpace(intent.DescriptorID) == "" || len(intent.DescriptorID) > 256 {
		return fmt.Errorf("%w: descriptor ID is invalid", ErrInvalidIntent)
	}
	if !intent.IrreversibleAcknowledged {
		return fmt.Errorf("%w: descriptor delete acknowledgement is required", ErrInvalidIntent)
	}
	return nil
}

func isKnownPreDispatchDescriptorError(err error) bool {
	return errors.Is(err, descriptors.ErrDeleteAcknowledgement) || errors.Is(err, descriptors.ErrDescriptorNotFound) || errors.Is(err, descriptors.ErrInvalidRequest)
}
