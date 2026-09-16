// Package workflows owns Mastarr's small ordered recipe process manager.
//
// A workflow is a durable, ordered view over independently approved action
// plans. The package only coordinates local SQLite rows and the review
// boundary; action handlers and upstream systems remain behind execution and
// domain ports. There is deliberately no general DAG or second queue here.
package workflows

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/guilycst/mastarr/internal/domain"
	"github.com/guilycst/mastarr/internal/planning"
	"github.com/guilycst/mastarr/internal/reviews"
	"github.com/guilycst/mastarr/internal/storage"
	"github.com/guilycst/mastarr/internal/storage/sqlc"
)

const (
	defaultRecipeKind = "media-reconciliation"
	defaultRecipeVer  = 1
	defaultIdemScope  = "workflow-runs"
	defaultStepScope  = "workflow-steps"
	maxWorkflowID     = 256
	maxStepID         = 128
	maxNameBytes      = 256
	maxKindBytes      = 128
	maxReasonBytes    = 2048
	maxSteps          = 128
	maxRecipeBytes    = 1 << 20
)

var (
	// ErrInvalidRecipe means the requested ordered recipe cannot be safely
	// interpreted or persisted.
	ErrInvalidRecipe = errors.New("invalid workflow recipe")
	// ErrWorkflowNotFound means the workflow or referenced step is absent.
	ErrWorkflowNotFound = errors.New("workflow was not found")
	// ErrWorkflowConflict means the durable workflow identity or recipe differs
	// from the requested one.
	ErrWorkflowConflict = errors.New("workflow conflicts with existing state")
	// ErrWorkflowClosed means cancellation, deadline or terminal state prevents
	// a new approval or step from being added.
	ErrWorkflowClosed = errors.New("workflow is closed")
	// ErrStepBlocked means a later step cannot be approved before its ordered
	// prerequisites have completed.
	ErrStepBlocked = errors.New("workflow step is blocked by a prerequisite")
	// ErrStepConflict means a step is already linked to a different decision or
	// action run.
	ErrStepConflict = errors.New("workflow step conflicts with existing state")
	// ErrIdempotencyConflict means a key was reused for a different request.
	ErrIdempotencyConflict = errors.New("workflow idempotency key was reused with different intent")
	// ErrBlanketApproval prevents a recipe from treating an untyped approval as
	// sufficient for a typed action plan.
	ErrBlanketApproval = errors.New("workflow blanket approval is not permitted")
	// ErrPrerequisite means an exact plan, precondition or prior step is absent.
	ErrPrerequisite = errors.New("workflow prerequisite is missing")
)

// StepSpec is the client-facing immutable reference to one saved action plan.
// Revision and Digest are aliases retained for callers that use shorter plan
// terminology; PlanRevision and PlanDigest are the canonical fields.
type StepSpec struct {
	ID           string            `json:"id"`
	PlanID       string            `json:"planId,omitempty"`
	ActionPlanID string            `json:"actionPlanId,omitempty"`
	PlanRevision int64             `json:"planRevision,omitempty"`
	Revision     int64             `json:"revision,omitempty"`
	PlanDigest   string            `json:"planDigest,omitempty"`
	Digest       string            `json:"digest,omitempty"`
	Kind         domain.ActionKind `json:"kind,omitempty"`
}

// Recipe is a bounded ordered sequence. Each item is approved independently;
// its plan's native approval kind is derived server-side and cannot be
// supplied by a caller.
type Recipe struct {
	Name    string     `json:"name,omitempty"`
	Kind    string     `json:"kind,omitempty"`
	Version int64      `json:"version,omitempty"`
	Steps   []StepSpec `json:"steps"`
}

// CreateRequest creates one durable workflow and its ordered step rows. It
// accepts either Recipe or the convenience top-level fields; the normalized
// recipe is what is persisted and included in the idempotency digest.
type CreateRequest struct {
	ID               string     `json:"id,omitempty"`
	Name             string     `json:"name,omitempty"`
	RecipeKind       string     `json:"recipeKind,omitempty"`
	RecipeVersion    int64      `json:"recipeVersion,omitempty"`
	Steps            []StepSpec `json:"steps,omitempty"`
	Recipe           Recipe     `json:"recipe,omitempty"`
	DeadlineAt       time.Time  `json:"deadlineAt,omitempty"`
	IdempotencyScope string     `json:"idempotencyScope,omitempty"`
	IdempotencyKey   string     `json:"idempotencyKey,omitempty"`
	At               time.Time  `json:"at,omitempty"`
}

// CreateWorkflowRequest is a compatibility alias for callers that name the
// route operation explicitly.
type CreateWorkflowRequest = CreateRequest

// ApprovalRequest binds one exact step approval to this workflow. The plan
// revision/digest are loaded from the persisted recipe, never from this
// request.
type ApprovalRequest struct {
	WorkflowID       string
	StepID           string
	Decision         reviews.Decision
	Actor            string
	CallerLabel      string
	Reason           string
	IdempotencyScope string
	IdempotencyKey   string
	At               time.Time
}

// StepApprovalRequest is an expressive alias for API/BFF callers.
type StepApprovalRequest = ApprovalRequest

// CancelRequest closes a workflow and marks every linked action for
// cooperative cancellation. Accepted external effects remain visible in the
// action journal and are never described as rolled back.
type CancelRequest struct {
	WorkflowID       string
	Actor            string
	Reason           string
	IdempotencyScope string
	IdempotencyKey   string
	At               time.Time
}

// AddStepRequest appends one exact plan reference while a workflow is open.
// Appended steps remain ordered and require their own approval.
type AddStepRequest struct {
	WorkflowID       string
	Step             StepSpec
	IdempotencyScope string
	IdempotencyKey   string
	At               time.Time
}

// Effect is a sanitized copy of one execution effect. Generated storage rows
// do not escape this package's domain-facing result.
type Effect struct {
	ID         string          `json:"id"`
	Ordinal    int64           `json:"ordinal"`
	TargetKind string          `json:"targetKind"`
	TargetID   string          `json:"targetId"`
	EffectKind string          `json:"effectKind"`
	State      string          `json:"state"`
	Evidence   json.RawMessage `json:"evidence"`
	ObservedAt string          `json:"observedAt"`
}

// Step is the durable and derived progress view for one recipe item. The
// action run fields make queue linkage and partial/uncertain effects visible.
type Step struct {
	ID              string                `json:"id"`
	Index           int                   `json:"index"`
	Kind            domain.ActionKind     `json:"kind"`
	State           domain.StepState      `json:"state"`
	PlanID          string                `json:"planId"`
	PlanRevision    int64                 `json:"planRevision"`
	PlanDigest      string                `json:"planDigest"`
	ApprovalGate    planning.ApprovalKind `json:"approvalGate"`
	DecisionID      string                `json:"decisionId,omitempty"`
	ActionRunID     string                `json:"actionRunId,omitempty"`
	ActionState     domain.ActionState    `json:"actionState,omitempty"`
	UnresolvedCount int64                 `json:"unresolvedCount,omitempty"`
	Outcome         json.RawMessage       `json:"outcome"`
	Effects         []Effect              `json:"effects,omitempty"`
}

// Workflow is the read model returned by Create, Get and Sync. CurrentStep is
// the zero-based durable step index; CurrentStepID avoids forcing API callers
// to rely on indexes when the recipe is rendered.
type Workflow struct {
	ID                      string                  `json:"id"`
	Name                    string                  `json:"name"`
	RecipeKind              string                  `json:"recipeKind"`
	RecipeVersion           int64                   `json:"recipeVersion"`
	State                   domain.WorkflowState    `json:"state"`
	CurrentStep             int                     `json:"currentStep"`
	CurrentStepID           string                  `json:"currentStepId,omitempty"`
	DeadlineAt              *time.Time              `json:"deadlineAt,omitempty"`
	CancellationRequestedAt *time.Time              `json:"cancellationRequestedAt,omitempty"`
	Steps                   []Step                  `json:"steps"`
	ApprovalGates           []planning.ApprovalKind `json:"approvalGates,omitempty"`
	AggregateEffectCount    int                     `json:"aggregateEffectCount"`
	UnresolvedCount         int64                   `json:"unresolvedCount"`
	Outcome                 json.RawMessage         `json:"outcome"`
	Replayed                bool                    `json:"-"`
}

// Options controls one workflow service.
type Options struct {
	Store          *storage.Store
	Reviews        *reviews.Service
	Now            func() time.Time
	WorkflowID     func(string) string
	MaxSteps       int
	MaxRecipeBytes int
}

// Service owns durable workflow/step coordination.
type Service struct {
	store          *storage.Store
	reviews        *reviews.Service
	now            func() time.Time
	workflowID     func(string) string
	maxSteps       int
	maxRecipeBytes int
}

type resolvedStep struct {
	Spec       StepSpec
	Plan       planning.Plan
	PlanDigest string
}

type storedRecipe struct {
	Name    string       `json:"name"`
	Kind    string       `json:"kind"`
	Version int64        `json:"version"`
	Steps   []storedStep `json:"steps"`
}

type storedStep struct {
	ID       string                `json:"id"`
	Index    int                   `json:"index"`
	PlanID   string                `json:"planId"`
	Revision int64                 `json:"revision"`
	Digest   string                `json:"digest"`
	Kind     domain.ActionKind     `json:"kind"`
	Gate     planning.ApprovalKind `json:"approvalGate"`
}

type stepMetadata map[string]json.RawMessage

// New constructs a workflow service with deterministic, safe defaults.
func New(store *storage.Store, reviewer *reviews.Service) (*Service, error) {
	return NewWithOptions(Options{Store: store, Reviews: reviewer})
}

// NewWithOptions allows deterministic IDs, clocks and bounds in tests.
func NewWithOptions(options Options) (*Service, error) {
	if options.Store == nil || options.Store.DB() == nil || options.Store.Queries() == nil {
		return nil, errors.New("workflow storage store is required")
	}
	if options.Reviews == nil {
		return nil, errors.New("workflow review service is required")
	}
	if options.Now == nil {
		options.Now = func() time.Time { return time.Now().UTC() }
	}
	if options.WorkflowID == nil {
		options.WorkflowID = func(digest string) string { return stableID("workflow", digest) }
	}
	if options.MaxSteps <= 0 {
		options.MaxSteps = maxSteps
	}
	if options.MaxRecipeBytes <= 0 {
		options.MaxRecipeBytes = maxRecipeBytes
	}
	return &Service{store: options.Store, reviews: options.Reviews, now: options.Now, workflowID: options.WorkflowID, maxSteps: options.MaxSteps, maxRecipeBytes: options.MaxRecipeBytes}, nil
}

// Create validates every referenced plan before opening the write
// transaction, then rechecks the exact plan revision/digest inside that
// transaction before inserting workflow and step rows plus idempotency.
func (service *Service) Create(ctx context.Context, request CreateRequest) (Workflow, error) {
	if service == nil || service.store == nil || service.reviews == nil {
		return Workflow{}, errors.New("workflow service is unavailable")
	}
	normalized, steps, recipe, requestDigest, err := service.normalizeCreate(ctx, request)
	if err != nil {
		return Workflow{}, err
	}
	recipeJSON, err := marshalBounded(recipe, service.maxRecipeBytes)
	if err != nil {
		return Workflow{}, err
	}
	workflowID := strings.TrimSpace(normalized.ID)
	if workflowID == "" {
		workflowID = service.workflowID(requestDigest)
	}
	if len(workflowID) > maxWorkflowID || strings.TrimSpace(workflowID) == "" {
		return Workflow{}, fmt.Errorf("%w: workflow id is invalid", ErrInvalidRecipe)
	}
	var result Workflow
	err = withTx(ctx, service.store, func(tx *sql.Tx, queries *sqlc.Queries) error {
		if record, getErr := queries.GetIdempotencyRecord(ctx, &sqlc.GetIdempotencyRecordParams{Scope: normalized.IdempotencyScope, IdempotencyKey: normalized.IdempotencyKey}); getErr == nil {
			if record.RequestDigest != requestDigest {
				return ErrIdempotencyConflict
			}
			if record.ResourceKind != "workflow_run" || record.ResourceID != workflowID {
				return fmt.Errorf("%w: idempotency resource differs", ErrWorkflowConflict)
			}
			loaded, loadErr := loadWorkflow(ctx, queries, workflowID)
			if loadErr != nil {
				return loadErr
			}
			loaded.Replayed = true
			result = loaded
			return nil
		} else if !errors.Is(getErr, sql.ErrNoRows) {
			return fmt.Errorf("load workflow idempotency: %w", getErr)
		}

		if existing, getErr := queries.GetWorkflowRun(ctx, workflowID); getErr == nil {
			if existing.RecipeJson != string(recipeJSON) {
				return ErrWorkflowConflict
			}
			loaded, loadErr := loadWorkflow(ctx, queries, workflowID)
			if loadErr != nil {
				return loadErr
			}
			result = loaded
			return createWorkflowIdempotency(ctx, queries, normalized, requestDigest, workflowID, loaded)
		} else if !errors.Is(getErr, sql.ErrNoRows) {
			return fmt.Errorf("load workflow: %w", getErr)
		}

		for _, step := range steps {
			if err := verifyPlanInTransaction(ctx, queries, step); err != nil {
				return err
			}
		}
		createdAt := normalized.At.Format(time.RFC3339Nano)
		deadline := sql.NullString{}
		if !normalized.DeadlineAt.IsZero() {
			deadline = sql.NullString{String: normalized.DeadlineAt.Format(time.RFC3339Nano), Valid: true}
		}
		if _, err := queries.CreateWorkflowRun(ctx, &sqlc.CreateWorkflowRunParams{
			ID: workflowID, RecipeKind: recipe.Kind, RecipeVersion: recipe.Version,
			State: string(domain.WorkflowAwaitingApproval), DeadlineAt: deadline,
			CurrentStep: 0, RecipeJson: string(recipeJSON), OutcomeJson: `{}`,
			CreatedAt: createdAt, UpdatedAt: createdAt,
		}); err != nil {
			return fmt.Errorf("%w: create workflow: %v", ErrWorkflowConflict, err)
		}
		for index, step := range steps {
			state := domain.StepBlocked
			if index == 0 {
				state = domain.StepQueued
			}
			metadata, metadataErr := encodeStepMetadata(step, "", "")
			if metadataErr != nil {
				return metadataErr
			}
			if _, err := queries.CreateWorkflowStep(ctx, &sqlc.CreateWorkflowStepParams{
				ID: step.Spec.ID, WorkflowID: workflowID, StepIndex: int64(index), Kind: string(step.Plan.Action), State: string(state),
				ActionPlanID: sql.NullString{String: step.Plan.ID, Valid: true}, ActionPlanRevision: sql.NullInt64{Int64: step.Plan.Revision, Valid: true},
				GateKind: sql.NullString{String: string(step.Plan.RequiredApproval), Valid: true}, OutcomeJson: string(metadata), CreatedAt: createdAt, UpdatedAt: createdAt,
			}); err != nil {
				return fmt.Errorf("%w: create workflow step %q: %v", ErrWorkflowConflict, step.Spec.ID, err)
			}
		}
		loaded, loadErr := loadWorkflow(ctx, queries, workflowID)
		if loadErr != nil {
			return loadErr
		}
		result = loaded
		return createWorkflowIdempotency(ctx, queries, normalized, requestDigest, workflowID, loaded)
	})
	if err != nil {
		return Workflow{}, err
	}
	return result, nil
}

// Get returns the durable workflow and a read-only projection of linked action
// effects. It never dispatches a handler or upstream request.
func (service *Service) Get(ctx context.Context, workflowID string) (Workflow, error) {
	if service == nil || service.store == nil {
		return Workflow{}, errors.New("workflow service is unavailable")
	}
	workflowID = strings.TrimSpace(workflowID)
	if workflowID == "" || len(workflowID) > maxWorkflowID {
		return Workflow{}, fmt.Errorf("%w: workflow id is invalid", ErrInvalidRecipe)
	}
	return loadWorkflow(ctx, service.store.Queries(), workflowID)
}

// List returns a bounded deterministic page of workflow runs. Pagination is
// intentionally small here; the HTTP layer can add its opaque cursor later.
func (service *Service) List(ctx context.Context, limit int) ([]Workflow, error) {
	if service == nil || service.store == nil {
		return nil, errors.New("workflow service is unavailable")
	}
	if limit <= 0 || limit > service.maxSteps*16 {
		limit = service.maxSteps * 16
	}
	rows, err := service.store.DB().QueryContext(ctx, `SELECT id FROM workflow_runs ORDER BY created_at DESC, id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	result := make([]Workflow, 0, len(ids))
	for _, id := range ids {
		workflow, err := service.Get(ctx, id)
		if err != nil {
			return nil, err
		}
		result = append(result, workflow)
	}
	return result, nil
}

// Sync projects linked action state/effects into ordered step and workflow
// rows. It also closes expired workflows and marks linked action runs for
// cooperative cancellation; no external calls occur here.
func (service *Service) Sync(ctx context.Context, workflowID string) (Workflow, error) {
	if service == nil || service.store == nil {
		return Workflow{}, errors.New("workflow service is unavailable")
	}
	workflowID = strings.TrimSpace(workflowID)
	if workflowID == "" {
		return Workflow{}, fmt.Errorf("%w: workflow id is required", ErrInvalidRecipe)
	}
	now := service.currentTime()
	err := withTx(ctx, service.store, func(tx *sql.Tx, queries *sqlc.Queries) error {
		row, err := queries.GetWorkflowRun(ctx, workflowID)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrWorkflowNotFound
		}
		if err != nil {
			return err
		}
		steps, err := queries.ListWorkflowSteps(ctx, workflowID)
		if err != nil {
			return err
		}
		closedState := domain.WorkflowState(row.State)
		if !closedState.Valid() {
			return fmt.Errorf("%w: workflow state %q", ErrWorkflowConflict, row.State)
		}
		if !closedState.Terminal() && row.DeadlineAt.Valid {
			deadline, parseErr := time.Parse(time.RFC3339Nano, row.DeadlineAt.String)
			if parseErr != nil {
				return fmt.Errorf("%w: workflow deadline is malformed", ErrWorkflowConflict)
			}
			if !now.Before(deadline.UTC()) {
				closedState = domain.WorkflowDeadlineExceeded
				if err := cancelLinkedActions(ctx, tx, steps, now, "workflow_deadline_exceeded"); err != nil {
					return err
				}
			}
		}
		for index, step := range steps {
			metadata, metadataErr := decodeMetadata(step.OutcomeJson)
			if metadataErr != nil {
				return metadataErr
			}
			if raw, ok := metadata["actionRunId"]; ok {
				var actionID string
				if json.Unmarshal(raw, &actionID) != nil || strings.TrimSpace(actionID) == "" {
					return fmt.Errorf("%w: workflow step %q action link is malformed", ErrWorkflowConflict, step.ID)
				}
				action, actionErr := queries.GetActionRun(ctx, actionID)
				if errors.Is(actionErr, sql.ErrNoRows) {
					return fmt.Errorf("%w: action run %q is missing", ErrWorkflowConflict, actionID)
				}
				if actionErr != nil {
					return actionErr
				}
				metadata["actionState"], _ = json.Marshal(action.State)
				metadata["actionOutcome"] = json.RawMessage(action.OutcomeJson)
				metadata["unresolvedCount"], _ = json.Marshal(action.UnresolvedCount)
				if err := updateProjectedStep(ctx, tx, step, actionStateToStep(domain.ActionState(action.State), closedState), metadata, now); err != nil {
					return err
				}
				continue
			}
			if closedState == domain.WorkflowCancelled || closedState == domain.WorkflowDeadlineExceeded {
				if step.State == string(domain.StepQueued) || step.State == string(domain.StepBlocked) || step.State == string(domain.StepRunning) {
					if err := updateStepState(ctx, tx, step, domain.StepCancelled, metadata, now); err != nil {
						return err
					}
				}
			}
			_ = index
		}
		steps, err = queries.ListWorkflowSteps(ctx, workflowID)
		if err != nil {
			return err
		}
		currentStep, nextState, err := projectAggregate(ctx, tx, row, steps, closedState, now)
		if err != nil {
			return err
		}
		if nextState != domain.WorkflowState(row.State) || currentStep != int(row.CurrentStep) {
			if nextState != domain.WorkflowState(row.State) && !domain.CanTransitionWorkflow(domain.WorkflowState(row.State), nextState) && nextState != domain.WorkflowState(row.State) {
				return fmt.Errorf("%w: workflow cannot transition from %q to %q", ErrWorkflowConflict, row.State, nextState)
			}
			outcome := row.OutcomeJson
			if outcome == "" {
				outcome = `{}`
			}
			if _, err := tx.ExecContext(ctx, `UPDATE workflow_runs SET state = ?, current_step = ?, outcome_json = ?, updated_at = ? WHERE id = ?`, string(nextState), currentStep, outcome, now.Format(time.RFC3339Nano), workflowID); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return Workflow{}, err
	}
	return service.Get(ctx, workflowID)
}

// Reconcile is an explicit alias for Sync used by worker/BFF callers.
func (service *Service) Reconcile(ctx context.Context, workflowID string) (Workflow, error) {
	return service.Sync(ctx, workflowID)
}

// ApproveStep validates ordered prerequisites and delegates the exact plan
// binding to reviews.Service. The review transaction links the resulting
// decision/action to the step; no workflow code can approve a blanket gate.
func (service *Service) ApproveStep(ctx context.Context, request ApprovalRequest) (reviews.ApprovalResult, error) {
	if service == nil || service.store == nil || service.reviews == nil {
		return reviews.ApprovalResult{}, errors.New("workflow service is unavailable")
	}
	workflow, err := service.Get(ctx, request.WorkflowID)
	if err != nil {
		return reviews.ApprovalResult{}, err
	}
	now := request.At
	if now.IsZero() {
		now = service.currentTime()
	}
	now = now.UTC()
	if workflow.State.Terminal() || workflow.State == domain.WorkflowCancelled || workflow.State == domain.WorkflowDeadlineExceeded {
		return reviews.ApprovalResult{}, ErrWorkflowClosed
	}
	if workflow.DeadlineAt != nil && !now.Before(workflow.DeadlineAt.UTC()) {
		return reviews.ApprovalResult{}, ErrWorkflowClosed
	}
	var selected *Step
	for index := range workflow.Steps {
		if workflow.Steps[index].ID == strings.TrimSpace(request.StepID) {
			selected = &workflow.Steps[index]
			break
		}
	}
	if selected == nil {
		return reviews.ApprovalResult{}, ErrWorkflowNotFound
	}
	if selected.Index != workflow.CurrentStep {
		return reviews.ApprovalResult{}, ErrStepBlocked
	}
	for index := 0; index < selected.Index; index++ {
		if workflow.Steps[index].State != domain.StepSucceeded && workflow.Steps[index].State != domain.StepSkipped {
			return reviews.ApprovalResult{}, ErrStepBlocked
		}
	}
	if selected.PlanID == "" || selected.PlanRevision <= 0 || selected.PlanDigest == "" || selected.ApprovalGate == planning.ApprovalNone {
		return reviews.ApprovalResult{}, fmt.Errorf("%w: exact step plan binding is missing", ErrPrerequisite)
	}
	if strings.TrimSpace(request.IdempotencyScope) == "" {
		request.IdempotencyScope = "workflow-approvals"
	}
	if strings.TrimSpace(request.IdempotencyKey) == "" {
		request.IdempotencyKey = "approve:" + workflow.ID + ":" + selected.ID
	}
	decision, err := service.reviews.Approve(ctx, reviews.ApprovalRequest{
		PlanID: selected.PlanID, Revision: selected.PlanRevision, Digest: selected.PlanDigest,
		Decision: request.Decision, Actor: request.Actor, CallerLabel: request.CallerLabel,
		Reason: request.Reason, IdempotencyScope: request.IdempotencyScope, IdempotencyKey: request.IdempotencyKey,
		At: now, WorkflowID: workflow.ID, WorkflowStepID: selected.ID,
	})
	if err != nil {
		return reviews.ApprovalResult{}, err
	}
	if request.Decision == reviews.DecisionReject {
		if err := service.markRejected(ctx, workflow.ID, selected.ID, decision.DecisionID, now); err != nil {
			return reviews.ApprovalResult{}, err
		}
	}
	return decision, nil
}

// Approve is an alias for ApproveStep.
func (service *Service) Approve(ctx context.Context, request ApprovalRequest) (reviews.ApprovalResult, error) {
	return service.ApproveStep(ctx, request)
}

// AddStep appends one plan reference only while the workflow remains open.
// It revalidates the referenced plan under the same transaction used for the
// insert, so cancellation/deadline closure cannot race a newly added step.
func (service *Service) AddStep(ctx context.Context, request AddStepRequest) (Workflow, error) {
	if service == nil || service.store == nil || service.reviews == nil {
		return Workflow{}, errors.New("workflow service is unavailable")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	request.WorkflowID = strings.TrimSpace(request.WorkflowID)
	request.IdempotencyScope = strings.TrimSpace(request.IdempotencyScope)
	request.IdempotencyKey = strings.TrimSpace(request.IdempotencyKey)
	if request.WorkflowID == "" || len(request.WorkflowID) > maxWorkflowID {
		return Workflow{}, fmt.Errorf("%w: workflow id is invalid", ErrInvalidRecipe)
	}
	if request.IdempotencyScope == "" {
		request.IdempotencyScope = defaultStepScope
	}
	if request.IdempotencyKey == "" {
		request.IdempotencyKey = "append:" + request.WorkflowID + ":" + strings.TrimSpace(request.Step.ID)
	}
	if len(request.IdempotencyScope) > maxKindBytes || len(request.IdempotencyKey) > 200 || request.IdempotencyKey == "" {
		return Workflow{}, fmt.Errorf("%w: idempotency scope/key is invalid", ErrInvalidRecipe)
	}
	now := request.At
	if now.IsZero() {
		now = service.currentTime()
	}
	request.At = now.UTC()
	resolved, err := service.resolveStep(ctx, request.Step, request.At)
	if err != nil {
		return Workflow{}, err
	}
	if strings.TrimSpace(resolved.Spec.ID) == "" {
		resolved.Spec.ID = stableID("step", resolved.Plan.ID)
	}
	digest, err := digestValue(struct {
		WorkflowID string   `json:"workflowId"`
		Step       StepSpec `json:"step"`
	}{request.WorkflowID, resolved.Spec})
	if err != nil {
		return Workflow{}, err
	}
	workflowID := request.WorkflowID
	var result Workflow
	err = withTx(ctx, service.store, func(tx *sql.Tx, queries *sqlc.Queries) error {
		if record, getErr := queries.GetIdempotencyRecord(ctx, &sqlc.GetIdempotencyRecordParams{Scope: request.IdempotencyScope, IdempotencyKey: request.IdempotencyKey}); getErr == nil {
			if record.RequestDigest != digest {
				return ErrIdempotencyConflict
			}
			loaded, loadErr := loadWorkflow(ctx, queries, workflowID)
			if loadErr != nil {
				return loadErr
			}
			loaded.Replayed = true
			result = loaded
			return nil
		} else if !errors.Is(getErr, sql.ErrNoRows) {
			return fmt.Errorf("load step idempotency: %w", getErr)
		}
		row, err := queries.GetWorkflowRun(ctx, workflowID)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrWorkflowNotFound
		}
		if err != nil {
			return err
		}
		if err := ensureOpen(row, request.At); err != nil {
			return err
		}
		steps, err := queries.ListWorkflowSteps(ctx, workflowID)
		if err != nil {
			return err
		}
		if len(steps) >= service.maxSteps {
			return fmt.Errorf("%w: workflow step count is outside bounds", ErrInvalidRecipe)
		}
		for _, existing := range steps {
			if existing.ID == resolved.Spec.ID {
				return ErrWorkflowConflict
			}
			if existing.ActionPlanID.Valid && existing.ActionPlanID.String == resolved.Plan.ID {
				return fmt.Errorf("%w: plan %q is already used by this workflow", ErrWorkflowConflict, resolved.Plan.ID)
			}
			if resolved.Plan.Action == domain.ActionArrRegistration && existing.Kind == string(domain.ActionArrImport) {
				return fmt.Errorf("%w: Arr import review must precede no later registration", ErrBlanketApproval)
			}
		}
		if err := verifyPlanInTransaction(ctx, queries, resolved); err != nil {
			return err
		}
		metadata, err := encodeStepMetadata(resolved, "", "")
		if err != nil {
			return err
		}
		state := domain.StepBlocked
		if len(steps) == 0 {
			state = domain.StepQueued
		}
		createdAt := request.At.Format(time.RFC3339Nano)
		if _, err := queries.CreateWorkflowStep(ctx, &sqlc.CreateWorkflowStepParams{
			ID: resolved.Spec.ID, WorkflowID: workflowID, StepIndex: int64(len(steps)), Kind: string(resolved.Plan.Action), State: string(state),
			ActionPlanID: sql.NullString{String: resolved.Plan.ID, Valid: true}, ActionPlanRevision: sql.NullInt64{Int64: resolved.Plan.Revision, Valid: true}, GateKind: sql.NullString{String: string(resolved.Plan.RequiredApproval), Valid: true}, OutcomeJson: string(metadata), CreatedAt: createdAt, UpdatedAt: createdAt,
		}); err != nil {
			return fmt.Errorf("%w: append workflow step: %v", ErrWorkflowConflict, err)
		}
		recipe, err := decodeRecipe(row.RecipeJson)
		if err != nil {
			return err
		}
		recipe.Steps = append(recipe.Steps, storedStep{ID: resolved.Spec.ID, Index: len(steps), PlanID: resolved.Plan.ID, Revision: resolved.Plan.Revision, Digest: resolved.Plan.Digest, Kind: resolved.Plan.Action, Gate: resolved.Plan.RequiredApproval})
		recipeJSON, err := marshalBounded(recipe, service.maxRecipeBytes)
		if err != nil {
			return err
		}
		updated, err := tx.ExecContext(ctx, `UPDATE workflow_runs SET recipe_json = ?, updated_at = ? WHERE id = ?`, string(recipeJSON), request.At.Format(time.RFC3339Nano), workflowID)
		if err != nil {
			return err
		}
		if affected, affectedErr := updated.RowsAffected(); affectedErr != nil || affected != 1 {
			return fmt.Errorf("%w: workflow recipe changed", ErrWorkflowConflict)
		}
		loaded, err := loadWorkflow(ctx, queries, workflowID)
		if err != nil {
			return err
		}
		result = loaded
		response, err := json.Marshal(loaded)
		if err != nil {
			return err
		}
		if _, err := queries.CreateIdempotencyRecord(ctx, &sqlc.CreateIdempotencyRecordParams{Scope: request.IdempotencyScope, IdempotencyKey: request.IdempotencyKey, RequestDigest: digest, StatusCode: 202, ResourceKind: "workflow_run", ResourceID: workflowID, ResponseJson: string(response), CreatedAt: request.At.Format(time.RFC3339Nano)}); err != nil {
			return fmt.Errorf("%w: create step idempotency: %v", ErrWorkflowConflict, err)
		}
		return nil
	})
	if err != nil {
		return Workflow{}, err
	}
	return result, nil
}

// AppendStep is an alias for AddStep.
func (service *Service) AppendStep(ctx context.Context, request AddStepRequest) (Workflow, error) {
	return service.AddStep(ctx, request)
}

// Cancel closes one workflow and fences linked action runs against future
// dispatch. Repeating the same idempotency key returns the same durable view.
func (service *Service) Cancel(ctx context.Context, request CancelRequest) (Workflow, error) {
	if service == nil || service.store == nil {
		return Workflow{}, errors.New("workflow service is unavailable")
	}
	request.WorkflowID = strings.TrimSpace(request.WorkflowID)
	request.Actor = strings.TrimSpace(request.Actor)
	request.Reason = strings.TrimSpace(request.Reason)
	request.IdempotencyScope = strings.TrimSpace(request.IdempotencyScope)
	request.IdempotencyKey = strings.TrimSpace(request.IdempotencyKey)
	if request.WorkflowID == "" || len(request.WorkflowID) > maxWorkflowID {
		return Workflow{}, fmt.Errorf("%w: workflow id is invalid", ErrInvalidRecipe)
	}
	if request.Actor == "" {
		request.Actor = "unauthenticated"
	}
	if request.IdempotencyScope == "" {
		request.IdempotencyScope = "workflow-cancellations"
	}
	if request.IdempotencyKey == "" {
		request.IdempotencyKey = "cancel:" + request.WorkflowID
	}
	if len(request.IdempotencyScope) > maxNameBytes || len(request.IdempotencyKey) > 200 || len(request.Actor) > maxNameBytes || len(request.Reason) > maxReasonBytes {
		return Workflow{}, fmt.Errorf("%w: cancellation field exceeds bounds", ErrInvalidRecipe)
	}
	for field, value := range map[string]string{"actor": request.Actor, "reason": request.Reason} {
		for _, r := range value {
			if unicode.IsControl(r) {
				return Workflow{}, fmt.Errorf("%w: %s contains a control character", ErrInvalidRecipe, field)
			}
		}
	}
	if request.At.IsZero() {
		request.At = service.currentTime()
	}
	request.At = request.At.UTC()
	digest, err := digestValue(struct {
		WorkflowID string `json:"workflowId"`
		Actor      string `json:"actor"`
		Reason     string `json:"reason,omitempty"`
	}{request.WorkflowID, request.Actor, request.Reason})
	if err != nil {
		return Workflow{}, err
	}
	var result Workflow
	err = withTx(ctx, service.store, func(tx *sql.Tx, queries *sqlc.Queries) error {
		if record, getErr := queries.GetIdempotencyRecord(ctx, &sqlc.GetIdempotencyRecordParams{Scope: request.IdempotencyScope, IdempotencyKey: request.IdempotencyKey}); getErr == nil {
			if record.RequestDigest != digest {
				return ErrIdempotencyConflict
			}
			loaded, loadErr := loadWorkflow(ctx, queries, request.WorkflowID)
			if loadErr != nil {
				return loadErr
			}
			loaded.Replayed = true
			result = loaded
			return nil
		} else if !errors.Is(getErr, sql.ErrNoRows) {
			return getErr
		}
		row, err := queries.GetWorkflowRun(ctx, request.WorkflowID)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrWorkflowNotFound
		}
		if err != nil {
			return err
		}
		state := domain.WorkflowState(row.State)
		if !state.Valid() {
			return fmt.Errorf("%w: workflow state is invalid", ErrWorkflowConflict)
		}
		if !state.Terminal() {
			if err := domain.TransitionWorkflow(state, domain.WorkflowCancelled); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `UPDATE workflow_runs SET state = ?, cancellation_requested_at = COALESCE(cancellation_requested_at, ?), outcome_json = ?, updated_at = ? WHERE id = ?`, string(domain.WorkflowCancelled), request.At.Format(time.RFC3339Nano), cancellationOutcome(request.Reason), request.At.Format(time.RFC3339Nano), request.WorkflowID); err != nil {
				return err
			}
		}
		steps, err := queries.ListWorkflowSteps(ctx, request.WorkflowID)
		if err != nil {
			return err
		}
		if err := cancelLinkedActions(ctx, tx, steps, request.At, "workflow_cancelled"); err != nil {
			return err
		}
		for _, step := range steps {
			if step.State == string(domain.StepQueued) || step.State == string(domain.StepBlocked) || step.State == string(domain.StepRunning) {
				metadata, metadataErr := decodeMetadata(step.OutcomeJson)
				if metadataErr != nil {
					return metadataErr
				}
				metadata["cancellationRequested"], _ = json.Marshal(true)
				if err := updateStepState(ctx, tx, step, domain.StepCancelled, metadata, request.At); err != nil {
					return err
				}
			}
		}
		loaded, loadErr := loadWorkflow(ctx, queries, request.WorkflowID)
		if loadErr != nil {
			return loadErr
		}
		result = loaded
		response, marshalErr := json.Marshal(loaded)
		if marshalErr != nil {
			return marshalErr
		}
		_, err = queries.CreateIdempotencyRecord(ctx, &sqlc.CreateIdempotencyRecordParams{Scope: request.IdempotencyScope, IdempotencyKey: request.IdempotencyKey, RequestDigest: digest, StatusCode: 202, ResourceKind: "workflow_run", ResourceID: request.WorkflowID, ResponseJson: string(response), CreatedAt: request.At.Format(time.RFC3339Nano)})
		return err
	})
	if err != nil {
		return Workflow{}, err
	}
	return result, nil
}

// CancelWorkflow is a convenience form for scheduler and API adapters.
func (service *Service) CancelWorkflow(ctx context.Context, workflowID string) (Workflow, error) {
	return service.Cancel(ctx, CancelRequest{WorkflowID: workflowID})
}

func (service *Service) markRejected(ctx context.Context, workflowID, stepID, decisionID string, at time.Time) error {
	return withTx(ctx, service.store, func(tx *sql.Tx, queries *sqlc.Queries) error {
		step, err := findWorkflowStep(ctx, queries, workflowID, stepID)
		if err != nil {
			return err
		}
		metadata, err := decodeMetadata(step.OutcomeJson)
		if err != nil {
			return err
		}
		metadata["decisionId"], _ = json.Marshal(decisionID)
		metadata["decision"], _ = json.Marshal(string(reviews.DecisionReject))
		if err := updateStepState(ctx, tx, step, domain.StepBlocked, metadata, at); err != nil {
			return err
		}
		return nil
	})
}

func (service *Service) normalizeCreate(ctx context.Context, request CreateRequest) (CreateRequest, []resolvedStep, storedRecipe, string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	now := request.At
	if now.IsZero() {
		now = service.currentTime()
	}
	request.At = now.UTC()
	name := strings.TrimSpace(request.Name)
	kind := strings.TrimSpace(request.RecipeKind)
	version := request.RecipeVersion
	steps := request.Steps
	if request.Recipe.Name != "" {
		if name != "" && strings.TrimSpace(request.Recipe.Name) != name {
			return CreateRequest{}, nil, storedRecipe{}, "", fmt.Errorf("%w: recipe names differ", ErrInvalidRecipe)
		}
		name = strings.TrimSpace(request.Recipe.Name)
	}
	if request.Recipe.Kind != "" {
		if kind != "" && strings.TrimSpace(request.Recipe.Kind) != kind {
			return CreateRequest{}, nil, storedRecipe{}, "", fmt.Errorf("%w: recipe kinds differ", ErrInvalidRecipe)
		}
		kind = strings.TrimSpace(request.Recipe.Kind)
	}
	if request.Recipe.Version != 0 {
		if version != 0 && request.Recipe.Version != version {
			return CreateRequest{}, nil, storedRecipe{}, "", fmt.Errorf("%w: recipe versions differ", ErrInvalidRecipe)
		}
		version = request.Recipe.Version
	}
	if len(request.Recipe.Steps) != 0 {
		if len(steps) != 0 && !reflectStepSpecsEqual(steps, request.Recipe.Steps) {
			return CreateRequest{}, nil, storedRecipe{}, "", fmt.Errorf("%w: recipe steps differ", ErrInvalidRecipe)
		}
		steps = request.Recipe.Steps
	}
	if name == "" || len(name) > maxNameBytes || containsControl(name) {
		return CreateRequest{}, nil, storedRecipe{}, "", fmt.Errorf("%w: workflow name is invalid", ErrInvalidRecipe)
	}
	if kind == "" {
		kind = defaultRecipeKind
	}
	if len(kind) > maxKindBytes || containsControl(kind) {
		return CreateRequest{}, nil, storedRecipe{}, "", fmt.Errorf("%w: recipe kind is invalid", ErrInvalidRecipe)
	}
	if version == 0 {
		version = defaultRecipeVer
	}
	if version <= 0 {
		return CreateRequest{}, nil, storedRecipe{}, "", fmt.Errorf("%w: recipe version is invalid", ErrInvalidRecipe)
	}
	if len(steps) == 0 || len(steps) > service.maxSteps {
		return CreateRequest{}, nil, storedRecipe{}, "", fmt.Errorf("%w: workflow step count is outside bounds", ErrInvalidRecipe)
	}
	if !request.DeadlineAt.IsZero() && !request.At.Before(request.DeadlineAt.UTC()) {
		return CreateRequest{}, nil, storedRecipe{}, "", fmt.Errorf("%w: deadline must follow creation", ErrInvalidRecipe)
	}
	request.Name, request.RecipeKind, request.RecipeVersion, request.Steps = name, kind, version, append([]StepSpec(nil), steps...)
	request.IdempotencyScope = strings.TrimSpace(request.IdempotencyScope)
	request.IdempotencyKey = strings.TrimSpace(request.IdempotencyKey)
	if request.IdempotencyScope == "" {
		request.IdempotencyScope = defaultIdemScope
	}
	if request.IdempotencyKey == "" {
		request.IdempotencyKey = "request:" + strings.TrimSpace(request.ID)
		if strings.TrimSuffix(request.IdempotencyKey, "request:") == "" {
			request.IdempotencyKey = "request:" + request.Name
		}
	}
	if len(request.IdempotencyScope) > maxKindBytes || len(request.IdempotencyKey) == 0 || len(request.IdempotencyKey) > 200 {
		return CreateRequest{}, nil, storedRecipe{}, "", fmt.Errorf("%w: idempotency scope/key is invalid", ErrInvalidRecipe)
	}
	resolved := make([]resolvedStep, 0, len(steps))
	seenIDs := make(map[string]struct{}, len(steps))
	seenPlans := make(map[string]struct{}, len(steps))
	for index, spec := range steps {
		spec.ID = strings.TrimSpace(spec.ID)
		if spec.ID == "" {
			spec.ID = fmt.Sprintf("step-%d", index+1)
		}
		if len(spec.ID) > maxStepID || containsControl(spec.ID) {
			return CreateRequest{}, nil, storedRecipe{}, "", fmt.Errorf("%w: step %d id is invalid", ErrInvalidRecipe, index)
		}
		if _, exists := seenIDs[spec.ID]; exists {
			return CreateRequest{}, nil, storedRecipe{}, "", fmt.Errorf("%w: duplicate step id %q", ErrInvalidRecipe, spec.ID)
		}
		seenIDs[spec.ID] = struct{}{}
		resolvedStepValue, err := service.resolveStep(ctx, spec, request.At)
		if err != nil {
			return CreateRequest{}, nil, storedRecipe{}, "", fmt.Errorf("%w: step %q: %v", ErrInvalidRecipe, spec.ID, err)
		}
		planKey := resolvedStepValue.Plan.ID
		if _, exists := seenPlans[planKey]; exists {
			return CreateRequest{}, nil, storedRecipe{}, "", fmt.Errorf("%w: plan %q is reused by multiple steps", ErrInvalidRecipe, planKey)
		}
		seenPlans[planKey] = struct{}{}
		resolvedStepValue.Spec.ID = spec.ID
		resolved = append(resolved, resolvedStepValue)
	}
	if err := validateOrderedKinds(resolved); err != nil {
		return CreateRequest{}, nil, storedRecipe{}, "", err
	}
	recipe := storedRecipe{Name: name, Kind: kind, Version: version, Steps: make([]storedStep, len(resolved))}
	for index, step := range resolved {
		recipe.Steps[index] = storedStep{ID: step.Spec.ID, Index: index, PlanID: step.Plan.ID, Revision: step.Plan.Revision, Digest: step.Plan.Digest, Kind: step.Plan.Action, Gate: step.Plan.RequiredApproval}
	}
	digest, err := digestValue(struct {
		ID         string       `json:"id,omitempty"`
		Name       string       `json:"name"`
		Kind       string       `json:"kind"`
		Version    int64        `json:"version"`
		Steps      []storedStep `json:"steps"`
		DeadlineAt string       `json:"deadlineAt,omitempty"`
	}{request.ID, name, kind, version, recipe.Steps, formatOptionalTime(request.DeadlineAt)})
	if err != nil {
		return CreateRequest{}, nil, storedRecipe{}, "", err
	}
	return request, resolved, recipe, digest, nil
}

func (service *Service) resolveStep(ctx context.Context, spec StepSpec, at time.Time) (resolvedStep, error) {
	spec.PlanID = strings.TrimSpace(spec.PlanID)
	if spec.PlanID == "" {
		spec.PlanID = strings.TrimSpace(spec.ActionPlanID)
	}
	if spec.PlanID == "" {
		return resolvedStep{}, fmt.Errorf("%w: plan id is required", ErrPrerequisite)
	}
	revision := spec.PlanRevision
	if revision == 0 {
		revision = spec.Revision
	}
	digest := strings.TrimSpace(spec.PlanDigest)
	if digest == "" {
		digest = strings.TrimSpace(spec.Digest)
	}
	if revision == 0 {
		row, err := service.store.Queries().GetActionPlan(ctx, spec.PlanID)
		if errors.Is(err, sql.ErrNoRows) {
			return resolvedStep{}, ErrWorkflowNotFound
		}
		if err != nil {
			return resolvedStep{}, err
		}
		revision = row.CurrentRevision
		if digest == "" {
			digest = row.CurrentDigest
		}
	}
	snapshot, err := service.reviews.GetPlan(ctx, spec.PlanID, revision)
	if err != nil {
		return resolvedStep{}, fmt.Errorf("%w: load plan: %v", ErrPrerequisite, err)
	}
	if snapshot.Plan.Revision != revision {
		return resolvedStep{}, fmt.Errorf("%w: plan revision is not exact", ErrPrerequisite)
	}
	if digest != "" && digest != snapshot.Plan.Digest {
		return resolvedStep{}, fmt.Errorf("%w: plan digest differs", ErrPrerequisite)
	}
	if snapshot.Plan.Status != planning.StatusReady || snapshot.Plan.StatusAt(at) != planning.StatusReady {
		return resolvedStep{}, fmt.Errorf("%w: plan is not ready at workflow creation", ErrPrerequisite)
	}
	if snapshot.Plan.RequiredApproval == planning.ApprovalNone {
		return resolvedStep{}, ErrBlanketApproval
	}
	if expected := expectedApproval(snapshot.Plan.Action); expected == planning.ApprovalNone || expected != snapshot.Plan.RequiredApproval {
		return resolvedStep{}, fmt.Errorf("%w: plan approval gate does not match action", ErrBlanketApproval)
	}
	if !hasRequiredPrecondition(snapshot.Plan) {
		return resolvedStep{}, fmt.Errorf("%w: plan %q has no required precondition", ErrPrerequisite, snapshot.Plan.ID)
	}
	if spec.Kind != "" && spec.Kind != snapshot.Plan.Action {
		return resolvedStep{}, fmt.Errorf("%w: step kind differs from plan action", ErrInvalidRecipe)
	}
	spec.PlanRevision, spec.Revision, spec.PlanDigest, spec.Digest = snapshot.Plan.Revision, snapshot.Plan.Revision, snapshot.Plan.Digest, snapshot.Plan.Digest
	spec.ActionPlanID = snapshot.Plan.ID
	return resolvedStep{Spec: spec, Plan: snapshot.Plan, PlanDigest: snapshot.Plan.Digest}, nil
}

func validateOrderedKinds(steps []resolvedStep) error {
	registration := -1
	importIndex := -1
	for index, step := range steps {
		switch step.Plan.Action {
		case domain.ActionArrRegistration:
			if registration == -1 {
				registration = index
			}
		case domain.ActionArrImport:
			if importIndex == -1 {
				importIndex = index
			}
		}
	}
	if registration >= 0 && importIndex >= 0 && importIndex <= registration {
		return fmt.Errorf("%w: Arr import review must follow registration review", ErrBlanketApproval)
	}
	return nil
}

func verifyPlanInTransaction(ctx context.Context, queries *sqlc.Queries, step resolvedStep) error {
	plan, err := queries.GetActionPlan(ctx, step.Plan.ID)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrWorkflowNotFound
	}
	if err != nil {
		return err
	}
	if plan.CurrentRevision != step.Plan.Revision || plan.CurrentDigest != step.Plan.Digest || plan.State != string(planning.StatusReady) {
		return fmt.Errorf("%w: plan %q changed before workflow publication", ErrPrerequisite, step.Plan.ID)
	}
	revision, err := queries.GetActionPlanRevision(ctx, &sqlc.GetActionPlanRevisionParams{PlanID: step.Plan.ID, Revision: step.Plan.Revision})
	if err != nil {
		return fmt.Errorf("%w: exact plan revision disappeared", ErrPrerequisite)
	}
	if revision.Digest != step.Plan.Digest || revision.State != string(planning.StatusReady) {
		return fmt.Errorf("%w: exact plan revision changed", ErrPrerequisite)
	}
	return nil
}

func encodeStepMetadata(step resolvedStep, decisionID, actionID string) ([]byte, error) {
	metadata := stepMetadata{
		"planId":           mustJSON(step.Plan.ID),
		"planRevision":     mustJSON(step.Plan.Revision),
		"planDigest":       mustJSON(step.Plan.Digest),
		"approvalRequired": mustJSON(string(step.Plan.RequiredApproval)),
	}
	if decisionID != "" {
		metadata["decisionId"] = mustJSON(decisionID)
	}
	if actionID != "" {
		metadata["actionRunId"] = mustJSON(actionID)
	}
	return json.Marshal(metadata)
}

func loadWorkflow(ctx context.Context, queries *sqlc.Queries, workflowID string) (Workflow, error) {
	row, err := queries.GetWorkflowRun(ctx, workflowID)
	if errors.Is(err, sql.ErrNoRows) {
		return Workflow{}, ErrWorkflowNotFound
	}
	if err != nil {
		return Workflow{}, err
	}
	recipe, err := decodeRecipe(row.RecipeJson)
	if err != nil {
		return Workflow{}, err
	}
	rows, err := queries.ListWorkflowSteps(ctx, workflowID)
	if err != nil {
		return Workflow{}, err
	}
	if len(rows) != len(recipe.Steps) {
		return Workflow{}, fmt.Errorf("%w: recipe/step count differs", ErrWorkflowConflict)
	}
	workflow := Workflow{ID: row.ID, Name: recipe.Name, RecipeKind: row.RecipeKind, RecipeVersion: row.RecipeVersion, State: domain.WorkflowState(row.State), CurrentStep: int(row.CurrentStep), Steps: make([]Step, 0, len(rows)), Outcome: json.RawMessage(row.OutcomeJson)}
	if !workflow.State.Valid() {
		return Workflow{}, fmt.Errorf("%w: workflow state %q", ErrWorkflowConflict, row.State)
	}
	if row.DeadlineAt.Valid {
		deadline, err := time.Parse(time.RFC3339Nano, row.DeadlineAt.String)
		if err != nil {
			return Workflow{}, fmt.Errorf("%w: workflow deadline is malformed", ErrWorkflowConflict)
		}
		deadline = deadline.UTC()
		workflow.DeadlineAt = &deadline
	}
	if row.CancellationRequestedAt.Valid {
		cancelled, err := time.Parse(time.RFC3339Nano, row.CancellationRequestedAt.String)
		if err != nil {
			return Workflow{}, fmt.Errorf("%w: cancellation timestamp is malformed", ErrWorkflowConflict)
		}
		cancelled = cancelled.UTC()
		workflow.CancellationRequestedAt = &cancelled
	}
	for index, item := range rows {
		if int(item.StepIndex) != index || item.ID != recipe.Steps[index].ID || item.WorkflowID != workflowID || item.Kind != string(recipe.Steps[index].Kind) || !domain.StepState(item.State).Valid() {
			return Workflow{}, fmt.Errorf("%w: workflow step ordering or identity differs", ErrWorkflowConflict)
		}
		metadata, err := decodeMetadata(item.OutcomeJson)
		if err != nil {
			return Workflow{}, err
		}
		step := Step{ID: item.ID, Index: index, Kind: domain.ActionKind(item.Kind), State: domain.StepState(item.State), PlanID: recipe.Steps[index].PlanID, PlanRevision: recipe.Steps[index].Revision, PlanDigest: recipe.Steps[index].Digest, ApprovalGate: recipe.Steps[index].Gate, Outcome: json.RawMessage(item.OutcomeJson)}
		if raw, ok := metadata["decisionId"]; ok {
			_ = json.Unmarshal(raw, &step.DecisionID)
		}
		if raw, ok := metadata["actionRunId"]; ok {
			_ = json.Unmarshal(raw, &step.ActionRunID)
		}
		if step.ActionRunID != "" {
			action, actionErr := queries.GetActionRun(ctx, step.ActionRunID)
			if actionErr == nil {
				step.ActionState = domain.ActionState(action.State)
				step.UnresolvedCount = action.UnresolvedCount
				if mapped := actionStateToStep(step.ActionState, workflow.State); mapped.Valid() {
					step.State = mapped
				}
				effects, effectsErr := queries.ListActionEffects(ctx, step.ActionRunID)
				if effectsErr != nil {
					return Workflow{}, effectsErr
				}
				step.Effects = make([]Effect, 0, len(effects))
				for _, effect := range effects {
					step.Effects = append(step.Effects, Effect{ID: effect.ID, Ordinal: effect.Ordinal, TargetKind: effect.TargetKind, TargetID: effect.TargetID, EffectKind: effect.EffectKind, State: effect.State, Evidence: json.RawMessage(effect.EvidenceJson), ObservedAt: effect.ObservedAt})
				}
			} else if !errors.Is(actionErr, sql.ErrNoRows) {
				return Workflow{}, actionErr
			}
		}
		workflow.AggregateEffectCount += len(step.Effects)
		workflow.UnresolvedCount += step.UnresolvedCount
		workflow.Steps = append(workflow.Steps, step)
		workflow.ApprovalGates = append(workflow.ApprovalGates, step.ApprovalGate)
	}
	if workflow.CurrentStep >= 0 && workflow.CurrentStep < len(workflow.Steps) {
		workflow.CurrentStepID = workflow.Steps[workflow.CurrentStep].ID
	}
	return workflow, nil
}

func decodeRecipe(raw string) (storedRecipe, error) {
	var recipe storedRecipe
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&recipe); err != nil {
		return storedRecipe{}, fmt.Errorf("%w: recipe JSON: %v", ErrWorkflowConflict, err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return storedRecipe{}, fmt.Errorf("%w: recipe JSON has trailing data", ErrWorkflowConflict)
	}
	if recipe.Name == "" || recipe.Kind == "" || recipe.Version <= 0 || len(recipe.Steps) == 0 || len(recipe.Steps) > maxSteps {
		return storedRecipe{}, fmt.Errorf("%w: recipe envelope is incomplete", ErrWorkflowConflict)
	}
	for index, step := range recipe.Steps {
		if step.Index != index || step.ID == "" || step.PlanID == "" || step.Revision <= 0 || step.Digest == "" || !step.Kind.Valid() || step.Gate == planning.ApprovalNone {
			return storedRecipe{}, fmt.Errorf("%w: recipe step %d is incomplete", ErrWorkflowConflict, index)
		}
	}
	return recipe, nil
}

func decodeMetadata(raw string) (stepMetadata, error) {
	if strings.TrimSpace(raw) == "" {
		return stepMetadata{}, fmt.Errorf("%w: empty step evidence", ErrWorkflowConflict)
	}
	decoder := json.NewDecoder(strings.NewReader(raw))
	var metadata stepMetadata
	if err := decoder.Decode(&metadata); err != nil || metadata == nil {
		return nil, fmt.Errorf("%w: step evidence is malformed", ErrWorkflowConflict)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return nil, fmt.Errorf("%w: step evidence has trailing data", ErrWorkflowConflict)
	}
	return metadata, nil
}

func findWorkflowStep(ctx context.Context, queries *sqlc.Queries, workflowID, stepID string) (*sqlc.WorkflowStep, error) {
	steps, err := queries.ListWorkflowSteps(ctx, workflowID)
	if err != nil {
		return nil, err
	}
	for _, step := range steps {
		if step.ID == stepID {
			return step, nil
		}
	}
	return nil, ErrWorkflowNotFound
}

func updateStepState(ctx context.Context, tx *sql.Tx, step *sqlc.WorkflowStep, state domain.StepState, metadata stepMetadata, at time.Time) error {
	if !state.Valid() {
		return fmt.Errorf("%w: invalid step state", ErrWorkflowConflict)
	}
	encoded, err := json.Marshal(metadata)
	if err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `UPDATE workflow_steps SET state = ?, outcome_json = ?, updated_at = ? WHERE id = ? AND workflow_id = ?`, string(state), string(encoded), at.UTC().Format(time.RFC3339Nano), step.ID, step.WorkflowID)
	if err != nil {
		return err
	}
	if count, err := result.RowsAffected(); err != nil || count != 1 {
		return fmt.Errorf("%w: workflow step changed", ErrStepConflict)
	}
	return nil
}

func updateProjectedStep(ctx context.Context, tx *sql.Tx, step *sqlc.WorkflowStep, state domain.StepState, metadata stepMetadata, at time.Time) error {
	return updateStepState(ctx, tx, step, state, metadata, at)
}

func projectAggregate(ctx context.Context, tx *sql.Tx, row *sqlc.WorkflowRun, steps []*sqlc.WorkflowStep, closed domain.WorkflowState, now time.Time) (int, domain.WorkflowState, error) {
	if closed == domain.WorkflowCancelled || closed == domain.WorkflowDeadlineExceeded {
		return int(row.CurrentStep), closed, nil
	}
	current := int(row.CurrentStep)
	for current < len(steps) && (steps[current].State == string(domain.StepSucceeded) || steps[current].State == string(domain.StepSkipped)) {
		current++
	}
	if current < len(steps) && steps[current].State == string(domain.StepBlocked) {
		metadata, err := decodeMetadata(steps[current].OutcomeJson)
		if err != nil {
			return current, domain.WorkflowNeedsReview, err
		}
		if raw, ok := metadata["decision"]; !ok || string(raw) != `"reject"` {
			if err := updateStepState(ctx, tx, steps[current], domain.StepQueued, metadata, now); err != nil {
				return current, domain.WorkflowNeedsReview, err
			}
			steps[current].State = string(domain.StepQueued)
		}
	}
	if current >= len(steps) {
		return current, domain.WorkflowSucceeded, nil
	}
	var next domain.WorkflowState = domain.WorkflowRunning
	for _, step := range steps {
		switch domain.StepState(step.State) {
		case domain.StepFailed:
			return current, domain.WorkflowFailed, nil
		case domain.StepCancelled:
			return current, domain.WorkflowCancelled, nil
		}
		if step.State == string(domain.StepBlocked) {
			var metadata stepMetadata
			metadata, _ = decodeMetadata(step.OutcomeJson)
			if raw, ok := metadata["actionState"]; ok && string(raw) == `"needs_review"` {
				return current, domain.WorkflowNeedsReview, nil
			}
		}
		if raw, ok := mustActionState(ctx, tx, step); ok {
			switch raw {
			case domain.ActionWaitingDependency:
				next = domain.WorkflowWaitingDependency
			case domain.ActionNeedsReview, domain.ActionReconciling:
				next = domain.WorkflowNeedsReview
			}
		}
	}
	return current, next, nil
}

func mustActionState(ctx context.Context, tx *sql.Tx, step *sqlc.WorkflowStep) (domain.ActionState, bool) {
	metadata, err := decodeMetadata(step.OutcomeJson)
	if err != nil {
		return "", false
	}
	raw, ok := metadata["actionState"]
	if !ok {
		return "", false
	}
	var state domain.ActionState
	if json.Unmarshal(raw, &state) != nil {
		return "", false
	}
	return state, true
}

func actionStateToStep(actionState domain.ActionState, workflowState domain.WorkflowState) domain.StepState {
	if (workflowState == domain.WorkflowCancelled || workflowState == domain.WorkflowDeadlineExceeded) &&
		actionState != domain.ActionSucceeded && actionState != domain.ActionFailed {
		return domain.StepCancelled
	}
	switch actionState {
	case domain.ActionQueued:
		return domain.StepQueued
	case domain.ActionRunning, domain.ActionReconciling:
		return domain.StepRunning
	case domain.ActionWaitingDependency:
		return domain.StepRunning
	case domain.ActionNeedsReview:
		return domain.StepBlocked
	case domain.ActionSucceeded:
		return domain.StepSucceeded
	case domain.ActionFailed:
		return domain.StepFailed
	case domain.ActionCancelled, domain.ActionDeadlineExceeded:
		if workflowState == domain.WorkflowDeadlineExceeded || workflowState == domain.WorkflowCancelled {
			return domain.StepCancelled
		}
		return domain.StepCancelled
	default:
		return domain.StepBlocked
	}
}

func cancelLinkedActions(ctx context.Context, tx *sql.Tx, steps []*sqlc.WorkflowStep, at time.Time, reason string) error {
	for _, step := range steps {
		metadata, err := decodeMetadata(step.OutcomeJson)
		if err != nil {
			return err
		}
		raw, ok := metadata["actionRunId"]
		if !ok {
			continue
		}
		var actionID string
		if json.Unmarshal(raw, &actionID) != nil || strings.TrimSpace(actionID) == "" {
			return fmt.Errorf("%w: linked action id is malformed", ErrWorkflowConflict)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE action_runs SET cancellation_requested_at = COALESCE(cancellation_requested_at, ?), updated_at = ?, outcome_json = CASE WHEN json_valid(outcome_json) THEN json_set(outcome_json, '$.workflowCancellation', ?) ELSE outcome_json END WHERE id = ? AND state NOT IN ('succeeded', 'failed', 'cancelled', 'deadline_exceeded')`, at.UTC().Format(time.RFC3339Nano), at.UTC().Format(time.RFC3339Nano), reason, actionID); err != nil {
			return err
		}
	}
	return nil
}

func ensureOpen(row *sqlc.WorkflowRun, at time.Time) error {
	state := domain.WorkflowState(row.State)
	if !state.Valid() {
		return fmt.Errorf("%w: workflow state is invalid", ErrWorkflowConflict)
	}
	if state.Terminal() {
		return ErrWorkflowClosed
	}
	if row.DeadlineAt.Valid {
		deadline, err := time.Parse(time.RFC3339Nano, row.DeadlineAt.String)
		if err != nil || !at.Before(deadline.UTC()) {
			return ErrWorkflowClosed
		}
	}
	return nil
}

func createWorkflowIdempotency(ctx context.Context, queries *sqlc.Queries, request CreateRequest, digest, workflowID string, workflow Workflow) error {
	workflow.Replayed = false
	response, err := json.Marshal(workflow)
	if err != nil {
		return err
	}
	_, err = queries.CreateIdempotencyRecord(ctx, &sqlc.CreateIdempotencyRecordParams{Scope: request.IdempotencyScope, IdempotencyKey: request.IdempotencyKey, RequestDigest: digest, StatusCode: 202, ResourceKind: "workflow_run", ResourceID: workflowID, ResponseJson: string(response), CreatedAt: request.At.Format(time.RFC3339Nano)})
	if err != nil {
		return fmt.Errorf("%w: create workflow idempotency: %v", ErrWorkflowConflict, err)
	}
	return nil
}

func cancellationOutcome(reason string) string {
	encoded, _ := json.Marshal(map[string]any{"reason": reason, "cancellationRequested": true})
	return string(encoded)
}

func hasRequiredPrecondition(plan planning.Plan) bool {
	for _, precondition := range plan.Preconditions {
		if precondition.Required {
			return true
		}
	}
	return false
}

func expectedApproval(kind domain.ActionKind) planning.ApprovalKind {
	switch kind {
	case domain.ActionArrRegistration:
		return planning.ApprovalRegistration
	case domain.ActionArrImport:
		return planning.ApprovalImport
	default:
		return planning.ApprovalAction
	}
}

func marshalBounded(value any, limit int) ([]byte, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("%w: encode recipe: %v", ErrInvalidRecipe, err)
	}
	if len(encoded) == 0 || len(encoded) > limit || !json.Valid(encoded) {
		return nil, fmt.Errorf("%w: recipe exceeds bounds", ErrInvalidRecipe)
	}
	return encoded, nil
}

func digestValue(value any) (string, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", fmt.Errorf("%w: canonicalize request: %v", ErrInvalidRecipe, err)
	}
	digest := sha256.Sum256(encoded)
	return "sha256:" + hex.EncodeToString(digest[:]), nil
}

func stableID(prefix, digest string) string {
	digest = strings.TrimPrefix(digest, "sha256:")
	if len(digest) > 24 {
		digest = digest[:24]
	}
	return prefix + "-" + digest
}

func mustJSON(value any) json.RawMessage {
	encoded, _ := json.Marshal(value)
	return encoded
}

func formatOptionalTime(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.UTC().Format(time.RFC3339Nano)
}

func containsControl(value string) bool {
	for _, r := range value {
		if unicode.IsControl(r) {
			return true
		}
	}
	return false
}

func reflectStepSpecsEqual(left, right []StepSpec) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func (service *Service) currentTime() time.Time {
	now := service.now()
	if now.IsZero() {
		now = time.Now().UTC()
	}
	return now.UTC()
}

func withTx(ctx context.Context, store *storage.Store, fn func(*sql.Tx, *sqlc.Queries) error) error {
	if ctx == nil {
		ctx = context.Background()
	}
	tx, err := store.DB().BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	queries := sqlc.New(tx)
	if err := fn(tx, queries); err != nil {
		return err
	}
	return tx.Commit()
}

// Keep these imports and helpers intentionally local: workflow translation
// must not expose sqlc or generated upstream types to the API/domain layer.
var _ = sort.Slice
