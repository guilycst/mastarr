// Package reviews owns immutable plan persistence and explicit review
// decisions. It is the only boundary that turns an approved planning.Plan
// into a durable execution action. External effects are never performed here.
package reviews

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
	"reflect"
	"strings"
	"time"
	"unicode"

	"github.com/guilycst/mastarr/internal/domain"
	"github.com/guilycst/mastarr/internal/planning"
	"github.com/guilycst/mastarr/internal/storage"
	"github.com/guilycst/mastarr/internal/storage/sqlc"
)

const (
	defaultIdempotencyScope = "review-decisions"
	defaultActor            = "unauthenticated"
	maxPlanInputBytes       = 1 << 20
	maxIdempotencyScope     = 128
	maxActorBytes           = 128
	maxCallerLabelBytes     = 256
	maxReasonBytes          = 2048
)

var (
	// ErrInvalidDecision means the request is malformed or does not carry a
	// complete immutable approval binding.
	ErrInvalidDecision = errors.New("invalid review decision")
	// ErrPlanNotFound means the referenced plan or revision is not present.
	ErrPlanNotFound = errors.New("review plan was not found")
	// ErrPlanConflict means the plan is not currently approvable.
	ErrPlanConflict = errors.New("review plan is in conflict")
	// ErrPlanRevision means the requested immutable revision is not current.
	ErrPlanRevision = errors.New("review plan revision mismatch")
	// ErrPlanDigest means the request does not bind the exact plan bytes.
	ErrPlanDigest = errors.New("review plan digest mismatch")
	// ErrPlanExpired means the review window has elapsed.
	ErrPlanExpired = errors.New("review plan approval window expired")
	// ErrDecisionConflict means another decision already owns the revision or
	// the existing decision is inconsistent with the requested one.
	ErrDecisionConflict = errors.New("review decision conflicts with existing state")
	// ErrIdempotencyConflict means one key was reused for another request.
	ErrIdempotencyConflict = errors.New("review idempotency key was reused with different intent")
	// ErrBlanketApproval prevents a broad approval from satisfying a typed
	// registration, import or action gate.
	ErrBlanketApproval = errors.New("blanket approval is not permitted")
	// ErrPrerequisite means an exact workflow step or prerequisite is missing.
	ErrPrerequisite = errors.New("review prerequisite is missing")
	// ErrWorkflowClosed means a workflow cancellation/deadline has closed its
	// mutation boundary.
	ErrWorkflowClosed = errors.New("workflow is closed for new mutations")
)

// Decision is the only durable review decision supported by v0.0.1.
type Decision string

const (
	DecisionApprove Decision = "approve"
	DecisionReject  Decision = "reject"
)

func (decision Decision) valid() bool {
	return decision == DecisionApprove || decision == DecisionReject
}

// PlanEnvelope keeps the semantic planning object and the execution intent
// together in the immutable action_plan_revisions.input_json column. The
// optional intent is opaque to this package but remains bounded JSON; action
// handlers consume it only after review.
type PlanEnvelope struct {
	Plan          planning.Plan   `json:"plan"`
	DesiredState  json.RawMessage `json:"desiredState"`
	DesiredDigest string          `json:"desiredDigest"`
}

// PlanSaveRequest persists one immutable plan revision. A plan is normally
// produced by planning.Build. DesiredState may contain the typed action
// intent encoded by internal/actions; when omitted, the semantic desired
// predicates are retained as a safe inspectable fallback.
type PlanSaveRequest struct {
	Plan         planning.Plan
	DesiredState json.RawMessage
}

// PlanSnapshot is the validated persisted plan plus the exact desired-state
// bytes that will be copied into an execution action run.
type PlanSnapshot struct {
	Plan          planning.Plan
	DesiredState  json.RawMessage
	DesiredDigest string
}

// ApprovalRequest binds a decision to one immutable plan revision. Workflow
// fields are optional; when present they make decision, queueing and workflow
// step linkage one SQLite transaction.
type ApprovalRequest struct {
	PlanID           string
	Revision         int64
	Digest           string
	Decision         Decision
	Actor            string
	CallerLabel      string
	Reason           string
	IdempotencyScope string
	IdempotencyKey   string
	At               time.Time
	WorkflowID       string
	WorkflowStepID   string
}

// ApprovalResult is returned for both a new decision and an idempotent
// replay. Replayed is response metadata and is never persisted as authority.
type ApprovalResult struct {
	DecisionID     string   `json:"decisionId"`
	PlanID         string   `json:"planId"`
	Revision       int64    `json:"revision"`
	Digest         string   `json:"digest"`
	Decision       Decision `json:"decision"`
	ActionRunID    string   `json:"actionRunId,omitempty"`
	WorkflowID     string   `json:"workflowId,omitempty"`
	WorkflowStepID string   `json:"workflowStepId,omitempty"`
	Replayed       bool     `json:"-"`
}

// Options controls a review service. IDs are deterministic by default so
// different callers cannot create duplicate decision/action resources for one
// exact plan revision.
type Options struct {
	Store         *storage.Store
	Now           func() time.Time
	DecisionID    func(string) string
	ActionRunID   func(string) string
	MaxInputBytes int
}

// Service persists plans and decisions against one local SQLite store.
type Service struct {
	store         *storage.Store
	now           func() time.Time
	decisionID    func(string) string
	actionRunID   func(string) string
	maxInputBytes int
}

// New constructs a service with safe deterministic defaults.
func New(store *storage.Store) (*Service, error) {
	return NewWithOptions(Options{Store: store})
}

// NewWithOptions constructs a service with injectable clock and IDs for
// deterministic tests. The store remains the only persistence dependency.
func NewWithOptions(options Options) (*Service, error) {
	if options.Store == nil || options.Store.DB() == nil || options.Store.Queries() == nil {
		return nil, errors.New("review storage store is required")
	}
	if options.Now == nil {
		options.Now = func() time.Time { return time.Now().UTC() }
	}
	if options.MaxInputBytes <= 0 {
		options.MaxInputBytes = maxPlanInputBytes
	}
	if options.DecisionID == nil {
		options.DecisionID = func(key string) string { return stableRuntimeID("review", key) }
	}
	if options.ActionRunID == nil {
		options.ActionRunID = func(key string) string { return stableRuntimeID("action", key) }
	}
	return &Service{store: options.Store, now: options.Now, decisionID: options.DecisionID, actionRunID: options.ActionRunID, maxInputBytes: options.MaxInputBytes}, nil
}

// StorePlan validates and persists an immutable plan revision. Repeating the
// same exact plan is an idempotent read; attempting to reuse a plan ID for
// changed authority-bearing content is rejected.
func (service *Service) StorePlan(ctx context.Context, request PlanSaveRequest) (PlanSnapshot, error) {
	if service == nil || service.store == nil {
		return PlanSnapshot{}, errors.New("review service is nil")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := request.Plan.Validate(); err != nil {
		return PlanSnapshot{}, fmt.Errorf("%w: %v", ErrInvalidDecision, err)
	}
	desired, err := desiredStateBytes(request.DesiredState, request.Plan, service.maxInputBytes)
	if err != nil {
		return PlanSnapshot{}, err
	}
	desiredDigest := digestJSON(desired)
	envelope := PlanEnvelope{Plan: request.Plan, DesiredState: desired, DesiredDigest: desiredDigest}
	input, err := json.Marshal(envelope)
	if err != nil {
		return PlanSnapshot{}, fmt.Errorf("%w: encode plan: %v", ErrInvalidDecision, err)
	}
	if len(input) > service.maxInputBytes {
		return PlanSnapshot{}, fmt.Errorf("%w: plan input exceeds %d bytes", ErrInvalidDecision, service.maxInputBytes)
	}
	preconditions, err := jsonArray(request.Plan.Preconditions)
	if err != nil {
		return PlanSnapshot{}, fmt.Errorf("%w: encode preconditions: %v", ErrInvalidDecision, err)
	}
	capabilities, err := jsonArray(request.Plan.Capabilities)
	if err != nil {
		return PlanSnapshot{}, fmt.Errorf("%w: encode capabilities: %v", ErrInvalidDecision, err)
	}
	manifest, err := jsonArray(request.Plan.Manifest)
	if err != nil {
		return PlanSnapshot{}, fmt.Errorf("%w: encode manifest: %v", ErrInvalidDecision, err)
	}
	createdAt := request.Plan.CreatedAt.UTC().Format(time.RFC3339Nano)
	updatedAt := createdAt
	readyAt := sql.NullString{}
	if request.Plan.Status == planning.StatusReady {
		readyAt = sql.NullString{String: createdAt, Valid: true}
	}

	var result PlanSnapshot
	err = withTx(ctx, service.store, func(tx *sql.Tx, queries *sqlc.Queries) error {
		existing, getErr := queries.GetActionPlan(ctx, request.Plan.ID)
		if getErr == nil {
			if existing.Kind != string(request.Plan.Action) {
				return fmt.Errorf("%w: plan action cannot change across revisions", ErrPlanConflict)
			}
			if request.Plan.Revision <= existing.CurrentRevision {
				if existing.CurrentRevision != request.Plan.Revision || existing.CurrentDigest != request.Plan.Digest || existing.State != string(request.Plan.Status) {
					return fmt.Errorf("%w: plan identity is already bound to another revision", ErrPlanConflict)
				}
				stored, loadErr := loadPlanSnapshot(ctx, queries, existing, request.Plan.Revision, request.Plan.Digest)
				if loadErr != nil {
					return loadErr
				}
				if !jsonEqual(stored.DesiredState, desired) {
					return fmt.Errorf("%w: desired state changed for existing plan", ErrPlanConflict)
				}
				result = stored
				return nil
			}
			if request.Plan.Revision != existing.CurrentRevision+1 {
				return fmt.Errorf("%w: revisions must be appended in order", ErrPlanRevision)
			}
			if err := insertPlanRevision(ctx, queries, request.Plan, input, preconditions, capabilities, manifest, readyAt); err != nil {
				return err
			}
			update, err := tx.ExecContext(ctx, `UPDATE action_plans SET state = ?, current_revision = ?, current_digest = ?, updated_at = ? WHERE id = ? AND current_revision = ?`, string(request.Plan.Status), request.Plan.Revision, request.Plan.Digest, updatedAt, request.Plan.ID, existing.CurrentRevision)
			if err != nil {
				return fmt.Errorf("%w: advance current plan revision: %v", ErrPlanConflict, err)
			}
			if affected, err := update.RowsAffected(); err != nil || affected != 1 {
				return fmt.Errorf("%w: current plan revision changed while appending", ErrPlanConflict)
			}
			result = PlanSnapshot{Plan: request.Plan, DesiredState: cloneJSON(desired), DesiredDigest: desiredDigest}
			return nil
		}
		if !errors.Is(getErr, sql.ErrNoRows) {
			return fmt.Errorf("load action plan: %w", getErr)
		}
		if _, err := queries.CreateActionPlan(ctx, &sqlc.CreateActionPlanParams{
			ID: request.Plan.ID, Kind: string(request.Plan.Action), State: string(request.Plan.Status), CurrentRevision: request.Plan.Revision,
			CurrentDigest: request.Plan.Digest, CreatedAt: createdAt, UpdatedAt: updatedAt,
		}); err != nil {
			return fmt.Errorf("create action plan: %w", err)
		}
		if err := insertPlanRevision(ctx, queries, request.Plan, input, preconditions, capabilities, manifest, readyAt); err != nil {
			return err
		}
		result = PlanSnapshot{Plan: request.Plan, DesiredState: cloneJSON(desired), DesiredDigest: desiredDigest}
		return nil
	})
	if err != nil {
		return PlanSnapshot{}, err
	}
	return result, nil
}

func insertPlanRevision(ctx context.Context, queries *sqlc.Queries, plan planning.Plan, input, preconditions, capabilities, manifest []byte, readyAt sql.NullString) error {
	if _, err := queries.CreateActionPlanRevision(ctx, &sqlc.CreateActionPlanRevisionParams{
		PlanID: plan.ID, Revision: plan.Revision, Digest: plan.Digest, State: string(plan.Status), InputJson: string(input),
		PreconditionsJson: string(preconditions), CapabilitiesJson: string(capabilities), ManifestJson: string(manifest), CreatedAt: plan.CreatedAt.UTC().Format(time.RFC3339Nano),
		ExpiresAt: plan.ExpiresAt.UTC().Format(time.RFC3339Nano), ReadyAt: readyAt,
	}); err != nil {
		return fmt.Errorf("create action plan revision: %w", err)
	}
	entries := flattenManifest(plan.Manifest)
	for ordinal, entry := range entries {
		entryJSON, marshalErr := json.Marshal(entry)
		if marshalErr != nil {
			return fmt.Errorf("%w: encode manifest entry: %v", ErrInvalidDecision, marshalErr)
		}
		if _, err := queries.CreatePlanManifest(ctx, &sqlc.CreatePlanManifestParams{
			PlanID: plan.ID, Revision: plan.Revision, Ordinal: int64(ordinal), RootID: entry.RootID.String(), RelativePath: entry.RelativePath,
			EntryType: string(entry.Type), SizeBytes: entry.Size, Digest: nullable(entry.Digest), FileIdentity: nullable(entry.FileIdentity), Role: nullable(string(entry.Role)), ManifestJson: string(entryJSON),
		}); err != nil {
			return fmt.Errorf("create plan manifest: %w", err)
		}
	}
	return nil
}

// SavePlan is an alias useful to callers that prefer the persistence verb.
func (service *Service) SavePlan(ctx context.Context, request PlanSaveRequest) (PlanSnapshot, error) {
	return service.StorePlan(ctx, request)
}

// GetPlan returns one exact persisted revision and verifies its durable
// manifest rows before exposing it to an approval caller.
func (service *Service) GetPlan(ctx context.Context, planID string, revision int64) (PlanSnapshot, error) {
	if service == nil || service.store == nil {
		return PlanSnapshot{}, errors.New("review service is nil")
	}
	if strings.TrimSpace(planID) == "" || revision <= 0 {
		return PlanSnapshot{}, fmt.Errorf("%w: plan identity is invalid", ErrInvalidDecision)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	queries := service.store.Queries()
	revisionRow, err := queries.GetActionPlanRevision(ctx, &sqlc.GetActionPlanRevisionParams{PlanID: planID, Revision: revision})
	if errors.Is(err, sql.ErrNoRows) {
		return PlanSnapshot{}, ErrPlanNotFound
	}
	if err != nil {
		return PlanSnapshot{}, fmt.Errorf("load action plan revision: %w", err)
	}
	planRow, err := queries.GetActionPlan(ctx, planID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return PlanSnapshot{}, ErrPlanNotFound
		}
		return PlanSnapshot{}, fmt.Errorf("load action plan: %w", err)
	}
	return loadPlanSnapshot(ctx, queries, planRow, revisionRow.Revision, revisionRow.Digest)
}

// Approve validates an immutable review request and atomically persists the
// decision, optional action run and optional workflow-step linkage. A
// rejected decision never creates an action run.
func (service *Service) Approve(ctx context.Context, request ApprovalRequest) (ApprovalResult, error) {
	if service == nil || service.store == nil {
		return ApprovalResult{}, errors.New("review service is nil")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	normalized, requestDigest, err := service.normalizeApproval(request)
	if err != nil {
		return ApprovalResult{}, err
	}
	var result ApprovalResult
	err = withTx(ctx, service.store, func(tx *sql.Tx, queries *sqlc.Queries) error {
		if record, getErr := queries.GetIdempotencyRecord(ctx, &sqlc.GetIdempotencyRecordParams{Scope: normalized.IdempotencyScope, IdempotencyKey: normalized.IdempotencyKey}); getErr == nil {
			if record.RequestDigest != requestDigest {
				return ErrIdempotencyConflict
			}
			stored, decodeErr := decodeApprovalResult(record.ResponseJson)
			if decodeErr != nil {
				return fmt.Errorf("%w: idempotency response is malformed", ErrDecisionConflict)
			}
			stored.Replayed = true
			result = stored
			return nil
		} else if !errors.Is(getErr, sql.ErrNoRows) {
			return fmt.Errorf("load review idempotency record: %w", getErr)
		}

		planRow, getErr := queries.GetActionPlan(ctx, normalized.PlanID)
		if errors.Is(getErr, sql.ErrNoRows) {
			return ErrPlanNotFound
		}
		if getErr != nil {
			return fmt.Errorf("load action plan: %w", getErr)
		}
		revisionRow, getErr := queries.GetActionPlanRevisionByDigest(ctx, &sqlc.GetActionPlanRevisionByDigestParams{PlanID: normalized.PlanID, Revision: normalized.Revision, Digest: normalized.Digest})
		if errors.Is(getErr, sql.ErrNoRows) {
			return fmt.Errorf("%w: requested revision or digest is not present", ErrPlanDigest)
		}
		if getErr != nil {
			return fmt.Errorf("load exact action plan revision: %w", getErr)
		}
		if planRow.CurrentRevision != normalized.Revision {
			return ErrPlanRevision
		}
		if planRow.CurrentDigest != normalized.Digest {
			return ErrPlanDigest
		}
		if planRow.Kind == "" || !domain.ActionKind(planRow.Kind).Valid() {
			return fmt.Errorf("%w: persisted action kind is invalid", ErrPlanConflict)
		}
		snapshot, loadErr := loadPlanSnapshot(ctx, queries, planRow, revisionRow.Revision, revisionRow.Digest)
		if loadErr != nil {
			return loadErr
		}
		if err := validateApprovalGate(snapshot.Plan); err != nil {
			return err
		}
		if err := validateApprovalPrerequisites(snapshot.Plan); err != nil {
			return err
		}

		decisionID := service.decisionID(normalized.PlanID + "\x00" + fmt.Sprint(normalized.Revision) + "\x00" + normalized.Digest)
		actionID := service.actionRunID(normalized.PlanID + "\x00" + fmt.Sprint(normalized.Revision) + "\x00" + normalized.Digest)
		existingDecision, existingErr := queries.GetReviewDecision(ctx, decisionID)
		if existingErr == nil {
			if existingDecision.PlanID != normalized.PlanID || existingDecision.PlanRevision != normalized.Revision || existingDecision.PlanDigest != normalized.Digest || existingDecision.Decision != string(normalized.Decision) {
				return ErrDecisionConflict
			}
			// A decision ID is deterministic for the immutable plan revision, so a
			// fresh idempotency key can reach this branch even though the request
			// itself is not a byte-for-byte replay. The durable attribution is part
			// of the decision authority and must never be rewritten by that replay.
			if !sameDecisionAttribution(existingDecision, normalized) {
				return ErrDecisionConflict
			}
			workflowDeadline, err := ensureWorkflowBinding(ctx, tx, queries, normalized, snapshot.Plan, decisionID, actionID, false)
			if err != nil {
				return err
			}
			if normalized.Decision == DecisionApprove {
				action, actionErr := queries.GetActionRun(ctx, actionID)
				if actionErr != nil {
					return fmt.Errorf("%w: approved decision has no action run", ErrDecisionConflict)
				}
				if !sameNullableString(action.DeadlineAt, workflowDeadline) {
					return fmt.Errorf("%w: approved action deadline is not bound to workflow", ErrDecisionConflict)
				}
			}
			result = ApprovalResult{DecisionID: decisionID, PlanID: normalized.PlanID, Revision: normalized.Revision, Digest: normalized.Digest, Decision: normalized.Decision, WorkflowID: normalized.WorkflowID, WorkflowStepID: normalized.WorkflowStepID, Replayed: true}
			if normalized.Decision == DecisionApprove {
				result.ActionRunID = actionID
			}
			return createIdempotency(ctx, queries, normalized, requestDigest, result)
		}
		if !errors.Is(existingErr, sql.ErrNoRows) {
			return fmt.Errorf("load existing review decision: %w", existingErr)
		}
		if err := snapshot.Plan.ValidateApproval(planning.Approval{PlanID: normalized.PlanID, Revision: normalized.Revision, Digest: normalized.Digest, At: normalized.At}, normalized.Now); err != nil {
			switch {
			case errors.Is(err, planning.ErrPlanExpired):
				return ErrPlanExpired
			case errors.Is(err, planning.ErrPlanRevision):
				return ErrPlanRevision
			case errors.Is(err, planning.ErrPlanDigest):
				return ErrPlanDigest
			default:
				return fmt.Errorf("%w: %v", ErrPlanConflict, err)
			}
		}
		workflowDeadline, err := ensureWorkflowBinding(ctx, tx, queries, normalized, snapshot.Plan, decisionID, actionID, true)
		if err != nil {
			return err
		}
		if _, err := queries.CreateReviewDecision(ctx, &sqlc.CreateReviewDecisionParams{
			ID: decisionID, PlanID: normalized.PlanID, PlanRevision: normalized.Revision, PlanDigest: normalized.Digest, Decision: string(normalized.Decision), Actor: normalized.Actor,
			CallerLabel: nullable(normalized.CallerLabel), Reason: nullable(normalized.Reason), IdempotencyScope: normalized.IdempotencyScope, IdempotencyKey: normalized.IdempotencyKey, CreatedAt: normalized.At.Format(time.RFC3339Nano),
		}); err != nil {
			return fmt.Errorf("%w: create review decision: %v", ErrDecisionConflict, err)
		}
		result = ApprovalResult{DecisionID: decisionID, PlanID: normalized.PlanID, Revision: normalized.Revision, Digest: normalized.Digest, Decision: normalized.Decision, WorkflowID: normalized.WorkflowID, WorkflowStepID: normalized.WorkflowStepID}
		if normalized.Decision == DecisionApprove {
			if _, err := queries.CreateActionRun(ctx, &sqlc.CreateActionRunParams{
				ID: actionID, PlanID: normalized.PlanID, PlanRevision: normalized.Revision, PlanDigest: normalized.Digest, State: string(domain.ActionQueued), DesiredStateJson: string(snapshot.DesiredState), DeadlineAt: workflowDeadline, Version: 1, OutcomeJson: `{}`, UnresolvedCount: 0,
				CreatedAt: normalized.At.Format(time.RFC3339Nano), UpdatedAt: normalized.At.Format(time.RFC3339Nano),
			}); err != nil {
				return fmt.Errorf("%w: create action run: %v", ErrDecisionConflict, err)
			}
			result.ActionRunID = actionID
		}
		return createIdempotency(ctx, queries, normalized, requestDigest, result)
	})
	if err != nil {
		return ApprovalResult{}, err
	}
	return result, nil
}

// Decide is an alias for callers that model the endpoint as a decision.
func (service *Service) Decide(ctx context.Context, request ApprovalRequest) (ApprovalResult, error) {
	return service.Approve(ctx, request)
}

type normalizedApproval struct {
	ApprovalRequest
	Now time.Time
}

func (service *Service) normalizeApproval(request ApprovalRequest) (normalizedApproval, string, error) {
	now := service.now().UTC()
	if now.IsZero() {
		now = time.Now().UTC()
	}
	request.PlanID = strings.TrimSpace(request.PlanID)
	request.Digest = strings.TrimSpace(request.Digest)
	request.Actor = strings.TrimSpace(request.Actor)
	request.CallerLabel = strings.TrimSpace(request.CallerLabel)
	request.Reason = strings.TrimSpace(request.Reason)
	request.IdempotencyScope = strings.TrimSpace(request.IdempotencyScope)
	request.IdempotencyKey = strings.TrimSpace(request.IdempotencyKey)
	request.WorkflowID = strings.TrimSpace(request.WorkflowID)
	request.WorkflowStepID = strings.TrimSpace(request.WorkflowStepID)
	if request.Actor == "" {
		request.Actor = defaultActor
	}
	if request.IdempotencyScope == "" {
		request.IdempotencyScope = defaultIdempotencyScope
	}
	if request.At.IsZero() {
		request.At = now
	}
	request.At = request.At.UTC()
	if request.PlanID == "" || len(request.PlanID) > 256 || request.Revision <= 0 || request.Digest == "" || !request.Decision.valid() {
		return normalizedApproval{}, "", fmt.Errorf("%w: plan binding or decision is invalid", ErrInvalidDecision)
	}
	if request.At.After(now) {
		return normalizedApproval{}, "", fmt.Errorf("%w: decision time is in the future", ErrInvalidDecision)
	}
	if len(request.IdempotencyScope) == 0 || len(request.IdempotencyScope) > maxIdempotencyScope || len(request.IdempotencyKey) == 0 || len(request.IdempotencyKey) > 200 {
		return normalizedApproval{}, "", fmt.Errorf("%w: idempotency scope/key is invalid", ErrInvalidDecision)
	}
	if len(request.Actor) > maxActorBytes || len(request.CallerLabel) > maxCallerLabelBytes || len(request.Reason) > maxReasonBytes || len(request.WorkflowID) > 256 || len(request.WorkflowStepID) > 256 {
		return normalizedApproval{}, "", fmt.Errorf("%w: decision field exceeds bounds", ErrInvalidDecision)
	}
	for field, value := range map[string]string{"actor": request.Actor, "callerLabel": request.CallerLabel, "reason": request.Reason} {
		for _, r := range value {
			if unicode.IsControl(r) {
				return normalizedApproval{}, "", fmt.Errorf("%w: %s contains a control character", ErrInvalidDecision, field)
			}
		}
	}
	requestDigest, err := digestApproval(request)
	if err != nil {
		return normalizedApproval{}, "", err
	}
	return normalizedApproval{ApprovalRequest: request, Now: now}, requestDigest, nil
}

func digestApproval(request ApprovalRequest) (string, error) {
	canonical := struct {
		PlanID         string   `json:"planId"`
		Revision       int64    `json:"revision"`
		Digest         string   `json:"digest"`
		Decision       Decision `json:"decision"`
		Actor          string   `json:"actor"`
		CallerLabel    string   `json:"callerLabel,omitempty"`
		Reason         string   `json:"reason,omitempty"`
		WorkflowID     string   `json:"workflowId,omitempty"`
		WorkflowStepID string   `json:"workflowStepId,omitempty"`
	}{request.PlanID, request.Revision, request.Digest, request.Decision, request.Actor, request.CallerLabel, request.Reason, request.WorkflowID, request.WorkflowStepID}
	encoded, err := json.Marshal(canonical)
	if err != nil {
		return "", fmt.Errorf("%w: canonicalize decision: %v", ErrInvalidDecision, err)
	}
	digest := sha256.Sum256(encoded)
	return "sha256:" + hex.EncodeToString(digest[:]), nil
}

func validateApprovalGate(plan planning.Plan) error {
	if plan.RequiredApproval == planning.ApprovalNone {
		return ErrBlanketApproval
	}
	switch plan.Action {
	case domain.ActionArrRegistration:
		if plan.RequiredApproval != planning.ApprovalRegistration {
			return fmt.Errorf("%w: registration requires its own approval gate", ErrBlanketApproval)
		}
	case domain.ActionArrImport:
		if plan.RequiredApproval != planning.ApprovalImport {
			return fmt.Errorf("%w: import requires a later exact import approval", ErrBlanketApproval)
		}
	default:
		if plan.RequiredApproval != planning.ApprovalAction {
			return fmt.Errorf("%w: action requires an exact action gate", ErrBlanketApproval)
		}
	}
	return nil
}

func validateApprovalPrerequisites(plan planning.Plan) error {
	for _, precondition := range plan.Preconditions {
		if precondition.Required {
			return nil
		}
	}
	return fmt.Errorf("%w: at least one required precondition must be reviewed", ErrPrerequisite)
}

// ensureWorkflowBinding is intentionally storage-level rather than a
// dependency on the workflow package. It keeps review decision + action queue
// + step linkage in one transaction and makes cancellation a serializable
// gate. Workflow-specific prerequisite ordering is checked by workflows before
// this function is called.
func ensureWorkflowBinding(ctx context.Context, tx *sql.Tx, queries *sqlc.Queries, request normalizedApproval, plan planning.Plan, decisionID, actionID string, creating bool) (sql.NullString, error) {
	if request.WorkflowID == "" && request.WorkflowStepID == "" {
		return sql.NullString{}, nil
	}
	if request.WorkflowID == "" || request.WorkflowStepID == "" {
		return sql.NullString{}, fmt.Errorf("%w: workflow and step must be supplied together", ErrInvalidDecision)
	}
	run, err := queries.GetWorkflowRun(ctx, request.WorkflowID)
	if errors.Is(err, sql.ErrNoRows) {
		return sql.NullString{}, ErrPrerequisite
	}
	if err != nil {
		return sql.NullString{}, fmt.Errorf("load workflow binding: %w", err)
	}
	if run.State == string(domain.WorkflowCancelled) || run.State == string(domain.WorkflowDeadlineExceeded) || run.State == string(domain.WorkflowFailed) || run.State == string(domain.WorkflowSucceeded) {
		return sql.NullString{}, ErrWorkflowClosed
	}
	workflowDeadline := sql.NullString{}
	if run.DeadlineAt.Valid {
		deadline, parseErr := time.Parse(time.RFC3339Nano, run.DeadlineAt.String)
		if parseErr != nil || !request.Now.Before(deadline.UTC()) {
			return sql.NullString{}, ErrWorkflowClosed
		}
		workflowDeadline = sql.NullString{String: deadline.UTC().Format(time.RFC3339Nano), Valid: true}
	}
	steps, err := queries.ListWorkflowSteps(ctx, request.WorkflowID)
	if err != nil {
		return sql.NullString{}, fmt.Errorf("load workflow steps: %w", err)
	}
	var matched *sqlc.WorkflowStep
	for _, step := range steps {
		if step.ID == request.WorkflowStepID {
			matched = step
			break
		}
	}
	if matched == nil || !matched.ActionPlanID.Valid || !matched.ActionPlanRevision.Valid || matched.ActionPlanID.String != request.PlanID || matched.ActionPlanRevision.Int64 != request.Revision {
		return sql.NullString{}, fmt.Errorf("%w: workflow step is not bound to the requested plan", ErrPrerequisite)
	}
	if matched.State != string(domain.StepQueued) && matched.State != string(domain.StepBlocked) {
		return sql.NullString{}, ErrDecisionConflict
	}
	metadata := map[string]json.RawMessage{}
	if strings.TrimSpace(matched.OutcomeJson) != "" {
		if err := json.Unmarshal([]byte(matched.OutcomeJson), &metadata); err != nil || metadata == nil {
			return sql.NullString{}, fmt.Errorf("%w: workflow step evidence is malformed", ErrDecisionConflict)
		}
	}
	if raw, ok := metadata["decisionId"]; ok {
		var existingDecision string
		if json.Unmarshal(raw, &existingDecision) != nil || existingDecision != decisionID {
			return sql.NullString{}, ErrDecisionConflict
		}
	}
	if raw, ok := metadata["actionRunId"]; ok && request.Decision == DecisionApprove {
		var existingAction string
		if json.Unmarshal(raw, &existingAction) != nil || existingAction != actionID {
			return sql.NullString{}, ErrDecisionConflict
		}
	}
	if raw, ok := metadata["actionRunId"]; ok && request.Decision == DecisionReject && string(raw) != "null" {
		return sql.NullString{}, ErrDecisionConflict
	}
	decisionJSON, _ := json.Marshal(decisionID)
	metadata["decisionId"] = decisionJSON
	gateJSON, _ := json.Marshal(string(plan.RequiredApproval))
	metadata["approvalRequired"] = gateJSON
	stepState := domain.StepQueued
	if request.Decision == DecisionApprove {
		actionJSON, _ := json.Marshal(actionID)
		metadata["actionRunId"] = actionJSON
	} else {
		if err := preserveWorkflowEvidence(metadata, "decision", string(DecisionReject)); err != nil {
			return sql.NullString{}, err
		}
		if err := preserveWorkflowEvidence(metadata, "decisionActor", request.Actor); err != nil {
			return sql.NullString{}, err
		}
		if err := preserveWorkflowEvidence(metadata, "decisionCallerLabel", request.CallerLabel); err != nil {
			return sql.NullString{}, err
		}
		if err := preserveWorkflowEvidence(metadata, "decisionReason", request.Reason); err != nil {
			return sql.NullString{}, err
		}
		delete(metadata, "actionRunId")
		stepState = domain.StepBlocked
	}
	encoded, marshalErr := json.Marshal(metadata)
	if marshalErr != nil {
		return sql.NullString{}, fmt.Errorf("%w: encode workflow step binding: %v", ErrInvalidDecision, marshalErr)
	}
	if creating || string(encoded) != matched.OutcomeJson {
		result, execErr := tx.ExecContext(ctx, `UPDATE workflow_steps SET outcome_json = ?, updated_at = ?, state = ? WHERE id = ? AND workflow_id = ? AND state IN ('queued', 'blocked')`, string(encoded), request.At.Format(time.RFC3339Nano), string(stepState), request.WorkflowStepID, request.WorkflowID)
		if execErr != nil {
			return sql.NullString{}, fmt.Errorf("%w: bind workflow step: %v", ErrDecisionConflict, execErr)
		}
		if affected, affectedErr := result.RowsAffected(); affectedErr != nil || affected != 1 {
			return sql.NullString{}, fmt.Errorf("%w: workflow step changed while binding", ErrDecisionConflict)
		}
	}
	if request.Decision == DecisionApprove || request.Decision == DecisionReject {
		if _, err := tx.ExecContext(ctx, `UPDATE workflow_runs SET state = CASE WHEN state = 'awaiting_approval' THEN 'running' ELSE state END, updated_at = ? WHERE id = ?`, request.At.Format(time.RFC3339Nano), request.WorkflowID); err != nil {
			return sql.NullString{}, fmt.Errorf("%w: advance workflow state: %v", ErrDecisionConflict, err)
		}
	}
	return workflowDeadline, nil
}

func sameNullableString(left, right sql.NullString) bool {
	if left.Valid != right.Valid {
		return false
	}
	if !left.Valid {
		return true
	}
	return left.String == right.String
}

func sameDecisionAttribution(stored *sqlc.ReviewDecision, request normalizedApproval) bool {
	if stored == nil {
		return false
	}
	return stored.Actor == request.Actor &&
		sameNullableString(stored.CallerLabel, nullable(request.CallerLabel)) &&
		sameNullableString(stored.Reason, nullable(request.Reason))
}

// preserveWorkflowEvidence makes semantic replay idempotent at the workflow
// projection as well as in review_decisions. Existing evidence is validated
// and retained byte-for-byte; contradictory metadata is a durable conflict.
func preserveWorkflowEvidence(metadata map[string]json.RawMessage, key, expected string) error {
	raw, ok := metadata[key]
	if ok {
		var actual string
		if err := json.Unmarshal(raw, &actual); err != nil || actual != expected {
			return ErrDecisionConflict
		}
		return nil
	}
	encoded, err := json.Marshal(expected)
	if err != nil {
		return fmt.Errorf("%w: encode workflow decision evidence: %v", ErrDecisionConflict, err)
	}
	metadata[key] = encoded
	return nil
}

func createIdempotency(ctx context.Context, queries *sqlc.Queries, request normalizedApproval, requestDigest string, result ApprovalResult) error {
	result.Replayed = false
	response, err := json.Marshal(result)
	if err != nil {
		return fmt.Errorf("%w: encode decision response: %v", ErrDecisionConflict, err)
	}
	status := int64(201)
	if result.ActionRunID != "" {
		status = 202
	}
	if _, err := queries.CreateIdempotencyRecord(ctx, &sqlc.CreateIdempotencyRecordParams{Scope: request.IdempotencyScope, IdempotencyKey: request.IdempotencyKey, RequestDigest: requestDigest, StatusCode: status, ResourceKind: "review_decision", ResourceID: result.DecisionID, ResponseJson: string(response), CreatedAt: request.At.Format(time.RFC3339Nano)}); err != nil {
		return fmt.Errorf("%w: create idempotency record: %v", ErrDecisionConflict, err)
	}
	return nil
}

func decodeApprovalResult(raw string) (ApprovalResult, error) {
	var result ApprovalResult
	if err := json.Unmarshal([]byte(raw), &result); err != nil || result.DecisionID == "" || result.PlanID == "" || !result.Decision.valid() {
		return ApprovalResult{}, errors.New("malformed approval response")
	}
	return result, nil
}

func desiredStateBytes(raw json.RawMessage, plan planning.Plan, maxBytes int) (json.RawMessage, error) {
	if len(raw) == 0 {
		encoded, err := json.Marshal(plan.Desired)
		if err != nil {
			return nil, fmt.Errorf("%w: encode desired state: %v", ErrInvalidDecision, err)
		}
		return encoded, nil
	}
	if maxBytes <= 0 {
		maxBytes = maxPlanInputBytes
	}
	trimmed := bytes.TrimSpace(raw)
	if len(raw) > maxBytes || len(trimmed) == 0 || trimmed[0] != '{' || !json.Valid(trimmed) {
		return nil, fmt.Errorf("%w: desired state is invalid or oversized", ErrInvalidDecision)
	}
	return cloneJSON(trimmed), nil
}

func jsonArray(value any) ([]byte, error) {
	if value == nil {
		return []byte("[]"), nil
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	if string(encoded) == "null" {
		return []byte("[]"), nil
	}
	return encoded, nil
}

func loadPlanSnapshot(ctx context.Context, queries *sqlc.Queries, planRow *sqlc.ActionPlan, revision int64, digest string) (PlanSnapshot, error) {
	if planRow == nil {
		return PlanSnapshot{}, ErrPlanNotFound
	}
	revisionRow, err := queries.GetActionPlanRevisionByDigest(ctx, &sqlc.GetActionPlanRevisionByDigestParams{PlanID: planRow.ID, Revision: revision, Digest: digest})
	if errors.Is(err, sql.ErrNoRows) {
		return PlanSnapshot{}, ErrPlanNotFound
	}
	if err != nil {
		return PlanSnapshot{}, fmt.Errorf("load action plan revision: %w", err)
	}
	var envelope PlanEnvelope
	decoder := json.NewDecoder(strings.NewReader(revisionRow.InputJson))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&envelope); err != nil || len(envelope.DesiredState) == 0 || !json.Valid(envelope.DesiredState) {
		return PlanSnapshot{}, fmt.Errorf("%w: persisted plan envelope is malformed", ErrPlanConflict)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return PlanSnapshot{}, fmt.Errorf("%w: persisted plan envelope has trailing data", ErrPlanConflict)
	}
	if envelope.DesiredDigest == "" || envelope.DesiredDigest != digestJSON(envelope.DesiredState) {
		return PlanSnapshot{}, fmt.Errorf("%w: persisted desired intent digest is invalid", ErrPlanDigest)
	}
	if envelope.Plan.ID != planRow.ID || envelope.Plan.Revision != revisionRow.Revision || envelope.Plan.Digest != revisionRow.Digest || string(envelope.Plan.Action) != planRow.Kind || string(envelope.Plan.Status) != revisionRow.State {
		return PlanSnapshot{}, fmt.Errorf("%w: persisted plan identity changed", ErrPlanDigest)
	}
	if err := envelope.Plan.Validate(); err != nil {
		return PlanSnapshot{}, fmt.Errorf("%w: persisted plan failed validation: %v", ErrPlanConflict, err)
	}
	if err := verifyPlanRows(ctx, queries, envelope.Plan); err != nil {
		return PlanSnapshot{}, err
	}
	return PlanSnapshot{Plan: envelope.Plan, DesiredState: cloneJSON(envelope.DesiredState), DesiredDigest: envelope.DesiredDigest}, nil
}

func verifyPlanRows(ctx context.Context, queries *sqlc.Queries, plan planning.Plan) error {
	rows, err := queries.ListPlanManifests(ctx, &sqlc.ListPlanManifestsParams{PlanID: plan.ID, Revision: plan.Revision})
	if err != nil {
		return fmt.Errorf("load plan manifest rows: %w", err)
	}
	entries := flattenManifest(plan.Manifest)
	if len(rows) != len(entries) {
		return fmt.Errorf("%w: persisted manifest row count differs", ErrPlanDigest)
	}
	for index, entry := range entries {
		row := rows[index]
		if row.Ordinal != int64(index) || row.RootID != entry.RootID.String() || row.RelativePath != entry.RelativePath || row.EntryType != string(entry.Type) || row.SizeBytes != entry.Size || nullableValue(row.Digest) != entry.Digest || nullableValue(row.FileIdentity) != entry.FileIdentity || nullableValue(row.Role) != string(entry.Role) {
			return fmt.Errorf("%w: persisted manifest row %d differs", ErrPlanDigest, index)
		}
		encoded, marshalErr := json.Marshal(entry)
		if marshalErr != nil || !jsonEqual([]byte(row.ManifestJson), encoded) {
			return fmt.Errorf("%w: persisted manifest evidence %d differs", ErrPlanDigest, index)
		}
	}
	return nil
}

func flattenManifest(entries []domain.FileManifestEntry) []domain.FileManifestEntry {
	result := make([]domain.FileManifestEntry, 0, len(entries))
	var visit func(domain.FileManifestEntry)
	visit = func(entry domain.FileManifestEntry) {
		result = append(result, entry)
		for _, child := range entry.Children {
			visit(child)
		}
	}
	for _, entry := range entries {
		visit(entry)
	}
	return result
}

func nullable(value string) sql.NullString {
	if value == "" {
		return sql.NullString{}
	}
	return sql.NullString{String: value, Valid: true}
}

func nullableValue(value sql.NullString) string {
	if !value.Valid {
		return ""
	}
	return value.String
}

func cloneJSON(value json.RawMessage) json.RawMessage {
	return append(json.RawMessage(nil), value...)
}

func jsonEqual(left, right []byte) bool {
	var leftValue any
	var rightValue any
	leftDecoder := json.NewDecoder(bytes.NewReader(left))
	leftDecoder.UseNumber()
	rightDecoder := json.NewDecoder(bytes.NewReader(right))
	rightDecoder.UseNumber()
	if leftDecoder.Decode(&leftValue) != nil || rightDecoder.Decode(&rightValue) != nil {
		return false
	}
	return reflect.DeepEqual(leftValue, rightValue)
}

func digestJSON(value []byte) string {
	digest := sha256.Sum256(value)
	return "sha256:" + hex.EncodeToString(digest[:])
}

func stableRuntimeID(prefix, value string) string {
	digest := sha256.Sum256([]byte(prefix + "\x00" + value))
	raw := digest[:16]
	raw[6] = raw[6]&0x0f | 0x40
	raw[8] = raw[8]&0x3f | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", raw[0:4], raw[4:6], raw[6:8], raw[8:10], raw[10:16])
}

func withTx(ctx context.Context, store *storage.Store, fn func(*sql.Tx, *sqlc.Queries) error) error {
	if fn == nil {
		return errors.New("review transaction callback is required")
	}
	tx, err := store.DB().BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin review transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := fn(tx, sqlc.New(tx)); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit review transaction: %w", err)
	}
	return nil
}
