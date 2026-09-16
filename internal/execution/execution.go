// Package execution implements Mastarr's durable action process manager.
//
// The executor owns local scheduling and journal transitions. Handlers own the
// meaning of a desired state and the upstream/filesystem calls used to make it
// true. The two concerns are deliberately separated so an action can be run
// directly or as one ordered workflow step without a second execution path.
package execution

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/guilycst/mastarr/internal/domain"
	"github.com/guilycst/mastarr/internal/storage/sqlc"
)

var (
	// ErrNoHandler means that a persisted action kind has no registered typed
	// implementation. Such work is held for review and is never dispatched.
	ErrNoHandler = errors.New("execution handler is not registered")
	// ErrLeaseLost means that the worker no longer owns the claimed action.
	// Callers must not dispatch after receiving this error.
	ErrLeaseLost = errors.New("execution lease was lost")
	// ErrReservationConflict means another Mastarr action owns an overlapping
	// resource reservation in this executor process.
	ErrReservationConflict = errors.New("execution reservation is held by another action")
	// ErrInvalidJournal means that persisted state is not safe to interpret.
	ErrInvalidJournal = errors.New("execution journal is invalid")
)

// FailureKind classifies a handler failure at the dispatch boundary. The
// distinction between not-dispatched and uncertain is safety-critical: only a
// proven pre-dispatch dependency failure may be scheduled for mutation retry.
type FailureKind string

const (
	FailureDependency FailureKind = "dependency_unavailable"
	FailureConflict   FailureKind = "conflict"
	FailureInvalid    FailureKind = "invalid"
	FailureUncertain  FailureKind = "uncertain"
	FailureCancelled  FailureKind = "cancelled"
)

// Failure is a typed handler error. Dispatched is advisory evidence from the
// handler; the executor still treats an unknown result after dispatch as
// uncertain unless a read-only reconciliation proves otherwise.
type Failure struct {
	Kind       FailureKind
	Err        error
	Dispatched bool
}

func (failure *Failure) Error() string {
	if failure == nil {
		return "<nil>"
	}
	if failure.Err == nil {
		return string(failure.Kind)
	}
	return fmt.Sprintf("%s: %v", failure.Kind, failure.Err)
}

func (failure *Failure) Unwrap() error {
	if failure == nil {
		return nil
	}
	return failure.Err
}

// NewFailure annotates an error for the executor.
func NewFailure(kind FailureKind, err error) error {
	if err == nil {
		err = errors.New(string(kind))
	}
	return &Failure{Kind: kind, Err: err}
}

// NewDispatchedFailure annotates an error that occurred after a handler may
// have sent its write. It always enters read-only reconciliation.
func NewDispatchedFailure(kind FailureKind, err error) error {
	if err == nil {
		err = errors.New(string(kind))
	}
	return &Failure{Kind: kind, Err: err, Dispatched: true}
}

func failureKind(err error) FailureKind {
	var failure *Failure
	if errors.As(err, &failure) {
		return failure.Kind
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return FailureUncertain
	}
	return FailureInvalid
}

func failureWasDispatched(err error) bool {
	var failure *Failure
	return errors.As(err, &failure) && failure.Dispatched
}

// ObserveState is the result of a read-only desired-state check.
type ObserveState string

const (
	ObserveSatisfied   ObserveState = "satisfied"
	ObserveNeedsAction ObserveState = "needs_action"
	ObserveUnknown     ObserveState = "unknown"
)

func (state ObserveState) valid() bool {
	return state == ObserveSatisfied || state == ObserveNeedsAction || state == ObserveUnknown
}

// AttemptPhase identifies the ordered phase recorded in the attempt journal.
type AttemptPhase string

const (
	AttemptObserve   AttemptPhase = "observe"
	AttemptDispatch  AttemptPhase = "dispatch"
	AttemptReconcile AttemptPhase = "reconcile"
)

func (phase AttemptPhase) valid() bool {
	return phase == AttemptObserve || phase == AttemptDispatch || phase == AttemptReconcile
}

// OutcomeCertainty records what is known about an external effect.
type OutcomeCertainty string

const (
	CertaintyNotDispatched OutcomeCertainty = "not_dispatched"
	CertaintyKnown         OutcomeCertainty = "known"
	CertaintyUncertain     OutcomeCertainty = "uncertain"
)

func (certainty OutcomeCertainty) valid() bool {
	return certainty == CertaintyNotDispatched || certainty == CertaintyKnown || certainty == CertaintyUncertain
}

// EffectState is the local evidence state for one exact target.
type EffectState string

const (
	EffectPending          EffectState = "pending"
	EffectApplied          EffectState = "applied"
	EffectAlreadySatisfied EffectState = "already_satisfied"
	EffectFailed           EffectState = "failed"
	EffectCancelled        EffectState = "cancelled"
	EffectUnknown          EffectState = "unknown"
)

func (state EffectState) valid() bool {
	switch state {
	case EffectPending, EffectApplied, EffectAlreadySatisfied, EffectFailed, EffectCancelled, EffectUnknown:
		return true
	default:
		return false
	}
}

// Action is the generated-query-independent representation passed to typed
// handlers. DesiredState is immutable bytes from the approved plan.
type Action struct {
	ID                      string
	PlanID                  string
	PlanRevision            int64
	PlanDigest              string
	Kind                    domain.ActionKind
	State                   domain.ActionState
	DesiredState            json.RawMessage
	NextAttemptAt           string
	DeadlineAt              string
	CancellationRequestedAt string
	ClaimedBy               string
	LeaseUntil              string
	Version                 int64
	Outcome                 json.RawMessage
	UnresolvedCount         int64
	CreatedAt               string
	UpdatedAt               string
}

func (action Action) validate() error {
	if strings.TrimSpace(action.ID) == "" || strings.TrimSpace(action.PlanID) == "" || strings.TrimSpace(action.PlanDigest) == "" {
		return fmt.Errorf("%w: action identity is incomplete", ErrInvalidJournal)
	}
	if action.PlanRevision <= 0 || action.Version <= 0 {
		return fmt.Errorf("%w: action revision/version is invalid", ErrInvalidJournal)
	}
	if !action.Kind.Valid() {
		return fmt.Errorf("%w: unsupported action kind %q", ErrInvalidJournal, action.Kind)
	}
	if !action.State.Valid() {
		return fmt.Errorf("%w: unsupported action state %q", ErrInvalidJournal, action.State)
	}
	if !json.Valid(action.DesiredState) || !json.Valid(action.Outcome) {
		return fmt.Errorf("%w: action JSON is invalid", ErrInvalidJournal)
	}
	if action.UnresolvedCount < 0 {
		return fmt.Errorf("%w: unresolved count is negative", ErrInvalidJournal)
	}
	return nil
}

// Plan identifies the typed handler for an action without exposing sqlc rows.
type Plan struct {
	ID              string
	Kind            domain.ActionKind
	State           string
	CurrentRevision int64
	CurrentDigest   string
}

func (plan Plan) validate() error {
	if strings.TrimSpace(plan.ID) == "" || !plan.Kind.Valid() || strings.TrimSpace(plan.State) == "" {
		return fmt.Errorf("%w: invalid action plan", ErrInvalidJournal)
	}
	return nil
}

// Attempt is one ordered journal entry. It is safe to expose to handlers for
// operation IDs and never contains a raw upstream response.
type Attempt struct {
	ID               string
	ActionRunID      string
	AttemptNumber    int64
	Phase            AttemptPhase
	State            domain.AttemptState
	StartedAt        string
	FinishedAt       string
	ErrorCode        string
	ErrorDetail      string
	OutcomeCertainty OutcomeCertainty
	ExternalID       string
	Evidence         json.RawMessage
}

func (attempt Attempt) validate() error {
	if strings.TrimSpace(attempt.ID) == "" || strings.TrimSpace(attempt.ActionRunID) == "" || attempt.AttemptNumber <= 0 {
		return fmt.Errorf("%w: attempt identity is invalid", ErrInvalidJournal)
	}
	if !attempt.Phase.valid() || !attempt.State.Valid() || !attempt.OutcomeCertainty.valid() {
		return fmt.Errorf("%w: attempt state is invalid", ErrInvalidJournal)
	}
	if strings.TrimSpace(attempt.StartedAt) == "" || !json.Valid(attempt.Evidence) {
		return fmt.Errorf("%w: attempt evidence is invalid", ErrInvalidJournal)
	}
	return nil
}

// Effect is one exact target in the ordered effect journal.
type Effect struct {
	ID          string
	ActionRunID string
	AttemptID   string
	Ordinal     int64
	TargetKind  string
	TargetID    string
	EffectKind  string
	State       EffectState
	Evidence    json.RawMessage
	ObservedAt  string
}

func (effect Effect) validate() error {
	if strings.TrimSpace(effect.ID) == "" || strings.TrimSpace(effect.ActionRunID) == "" || effect.Ordinal < 0 || strings.TrimSpace(effect.TargetKind) == "" || strings.TrimSpace(effect.TargetID) == "" || strings.TrimSpace(effect.EffectKind) == "" {
		return fmt.Errorf("%w: effect identity is invalid", ErrInvalidJournal)
	}
	if !effect.State.valid() || !json.Valid(effect.Evidence) || strings.TrimSpace(effect.ObservedAt) == "" {
		return fmt.Errorf("%w: effect evidence is invalid", ErrInvalidJournal)
	}
	return nil
}

// Observation is a read-only desired-state result.
type Observation struct {
	State    ObserveState
	Evidence []string
	Effects  []Effect
}

func (observation Observation) validate(actionID string) error {
	if !observation.State.valid() {
		return fmt.Errorf("%w: invalid observation state %q", ErrInvalidJournal, observation.State)
	}
	normalizeReportedEffects(observation.Effects, actionID)
	if err := validateEffects(observation.Effects, actionID); err != nil {
		return err
	}
	switch observation.State {
	case ObserveNeedsAction:
		if len(observation.Effects) == 0 {
			return fmt.Errorf("%w: needs-action observation must identify at least one effect", ErrInvalidJournal)
		}
		return validatePlannedEffectStates(observation.Effects)
	case ObserveSatisfied:
		return validateEffectStates(observation.Effects, EffectAlreadySatisfied)
	default:
		return nil
	}
}

// DispatchResult contains handler evidence after a mutation call returns.
// The executor still performs a fresh Observe before recording success.
type DispatchResult struct {
	Accepted   bool
	Outcome    domain.EffectOutcome
	ExternalID string
	Evidence   []string
	Effects    []Effect
}

func (result DispatchResult) validate(actionID string) error {
	if !result.Accepted || !result.Outcome.Valid() {
		return fmt.Errorf("%w: dispatch result must state accepted and valid outcome", ErrInvalidJournal)
	}
	normalizeReportedEffects(result.Effects, actionID)
	if err := validateEffects(result.Effects, actionID); err != nil {
		return err
	}
	return validateEffectStates(result.Effects, effectStateForOutcome(result.Outcome))
}

// ReconcileResult is read-only evidence after a lost or recovered dispatch.
// SafeToRetry is allowed only when the handler proves that no desired effect
// was materialized. Outcome is required when the desired state is proven.
type ReconcileResult struct {
	Outcome     domain.EffectOutcome
	SafeToRetry bool
	Evidence    []string
	Effects     []Effect
}

func (result ReconcileResult) validate(actionID string) error {
	if result.SafeToRetry && result.Outcome.Valid() {
		return fmt.Errorf("%w: reconciliation cannot be both safe-to-retry and terminal", ErrInvalidJournal)
	}
	if !result.SafeToRetry && result.Outcome != "" && !result.Outcome.Valid() {
		return fmt.Errorf("%w: reconciliation outcome is invalid", ErrInvalidJournal)
	}
	normalizeReportedEffects(result.Effects, actionID)
	if err := validateEffects(result.Effects, actionID); err != nil {
		return err
	}
	if result.Outcome.Valid() && len(result.Effects) == 0 {
		return fmt.Errorf("%w: terminal reconciliation must identify every effect", ErrInvalidJournal)
	}
	if result.SafeToRetry {
		return validateEffectStates(result.Effects, EffectPending)
	}
	if result.Outcome.Valid() {
		return validateEffectStates(result.Effects, effectStateForOutcome(result.Outcome))
	}
	return nil
}

func normalizeReportedEffects(effects []Effect, actionID string) {
	for index := range effects {
		if effects[index].ActionRunID == "" {
			effects[index].ActionRunID = actionID
		}
		if effects[index].Ordinal < 0 {
			effects[index].Ordinal = int64(index)
		}
		if effects[index].State == "" {
			effects[index].State = EffectPending
		}
		if effects[index].Evidence == nil {
			effects[index].Evidence = json.RawMessage(`{}`)
		}
		if effects[index].ObservedAt == "" {
			effects[index].ObservedAt = "execution-observation"
		}
	}
}

func validateEffects(effects []Effect, actionID string) error {
	seen := make(map[int64]struct{}, len(effects))
	seenIdentity := make(map[string]struct{}, len(effects))
	for index, effect := range effects {
		if effect.ActionRunID == "" {
			effect.ActionRunID = actionID
		}
		if effect.ActionRunID != actionID {
			return fmt.Errorf("%w: effect %d belongs to another action", ErrInvalidJournal, index)
		}
		if effect.State == "" {
			effect.State = EffectPending
		}
		if effect.Evidence == nil {
			effect.Evidence = json.RawMessage(`{}`)
		}
		if effect.ObservedAt == "" {
			effect.ObservedAt = "execution-observation"
		}
		if err := validateReportedEffect(effect, actionID); err != nil {
			return fmt.Errorf("effect %d: %w", index, err)
		}
		if _, ok := seen[effect.Ordinal]; ok {
			return fmt.Errorf("%w: duplicate effect ordinal %d", ErrInvalidJournal, effect.Ordinal)
		}
		seen[effect.Ordinal] = struct{}{}
		identity := effectIdentity(effect)
		if _, ok := seenIdentity[identity]; ok {
			return fmt.Errorf("%w: duplicate effect identity %s", ErrInvalidJournal, identity)
		}
		seenIdentity[identity] = struct{}{}
	}
	return nil
}

func effectIdentity(effect Effect) string {
	return strings.Join([]string{effect.TargetKind, effect.TargetID, effect.EffectKind}, "\x00")
}

// validateReportedEffect validates a handler-reported effect before the
// executor assigns its durable ID and attempt binding. Handlers must provide
// the exact target and effect kind, while IDs remain journal-owned so a
// replayed observation cannot invent a second row for the same ordinal.
func validateReportedEffect(effect Effect, actionID string) error {
	if strings.TrimSpace(effect.ActionRunID) == "" || effect.ActionRunID != actionID || effect.Ordinal < 0 || strings.TrimSpace(effect.TargetKind) == "" || strings.TrimSpace(effect.TargetID) == "" || strings.TrimSpace(effect.EffectKind) == "" {
		return fmt.Errorf("%w: effect identity is invalid", ErrInvalidJournal)
	}
	if !effect.State.valid() || !json.Valid(effect.Evidence) || strings.TrimSpace(effect.ObservedAt) == "" {
		return fmt.Errorf("%w: effect evidence is invalid", ErrInvalidJournal)
	}
	return nil
}

func effectStateForOutcome(outcome domain.EffectOutcome) EffectState {
	if outcome == domain.OutcomeAlreadySatisfied {
		return EffectAlreadySatisfied
	}
	return EffectApplied
}

func validatePlannedEffectStates(effects []Effect) error {
	for index, effect := range effects {
		switch effect.State {
		case EffectPending, EffectApplied, EffectAlreadySatisfied:
		default:
			return fmt.Errorf("%w: effect %d state %q cannot be dispatched", ErrInvalidJournal, index, effect.State)
		}
	}
	return nil
}

// validateEffectStates rejects evidence that contradicts the aggregate
// outcome. Pending is an intentionally compact handler representation and is
// promoted by recordEffectsInJournal; failed, cancelled, or unknown evidence
// can never be promoted to a terminal success.
func validateEffectStates(effects []Effect, expected EffectState) error {
	for index, effect := range effects {
		switch expected {
		case EffectPending:
			if effect.State != EffectPending {
				return fmt.Errorf("%w: effect %d state %q contradicts safe retry", ErrInvalidJournal, index, effect.State)
			}
		case EffectApplied, EffectAlreadySatisfied:
			if effect.State != EffectPending && effect.State != EffectApplied && effect.State != EffectAlreadySatisfied {
				return fmt.Errorf("%w: effect %d state %q contradicts terminal outcome", ErrInvalidJournal, index, effect.State)
			}
		case EffectUnknown:
			// Uncertain evidence is deliberately normalized to unknown by the
			// journal path, regardless of a stale handler-provided state.
		default:
			return fmt.Errorf("%w: unsupported expected effect state %q", ErrInvalidJournal, expected)
		}
	}
	return nil
}

// Handler is the typed action contract. Implementations may call external
// systems in Dispatch, but Observe and Reconcile must remain read-only.
type Handler interface {
	Kind() domain.ActionKind
	// Reservations returns canonical local resource keys. Returning a key for
	// a parent path reserves its descendants as well by handler convention.
	Reservations(Action) []string
	Observe(context.Context, Action) (Observation, error)
	Dispatch(context.Context, Action, Attempt) (DispatchResult, error)
	Reconcile(context.Context, Action, Attempt) (ReconcileResult, error)
}

// Journal is the narrow durable boundary used by Executor. SQLJournal adapts
// the generated storage queries; tests can implement this interface with an
// in-memory fault-injecting journal.
type Journal interface {
	GetAction(context.Context, string) (Action, error)
	GetPlan(context.Context, string) (Plan, error)
	ListDue(context.Context, string, int) ([]Action, error)
	ListReconciling(context.Context, string, int) ([]Action, error)
	Claim(context.Context, string, int64, string, string, string) (Action, error)
	RenewLease(context.Context, ClaimFence, string, string) error
	RecoverRunning(context.Context, string) ([]Action, error)
	RecoverExpired(context.Context, string) ([]Action, error)
	RecoverRunningAttempts(context.Context) ([]Attempt, error)
	FinalizeCancelled(context.Context, string) ([]Action, error)
	FinalizeDeadline(context.Context, string) ([]Action, error)
	RequestCancellation(context.Context, string, string) (Action, error)
	ListAttempts(context.Context, string) ([]Attempt, error)
	CreateAttempt(context.Context, Attempt) (Attempt, error)
	UpdateAttempt(context.Context, Attempt) (Attempt, error)
	ListEffects(context.Context, string) ([]Effect, error)
	CreateEffect(context.Context, Effect) (Effect, error)
	UpdateEffect(context.Context, Effect) (Effect, error)
	UpdateOutcome(context.Context, OutcomeUpdate) (Action, error)
}

// TransactionalJournal gives journal phase writes one short transaction. No
// handler call is made while the transaction is open.
type TransactionalJournal interface {
	InTx(context.Context, func(Journal) error) error
}

// ClaimFence is the immutable ownership evidence captured immediately after a
// successful claim. Post-dispatch journal writes must use this fence; a fresh
// action row must never grant a stale worker ownership of a recovered or
// re-claimed generation. Cancellation may advance Version once while keeping
// the same worker and lease, so implementations accept that durable marker as
// part of the original fence.
type ClaimFence struct {
	ActionID   string
	Version    int64
	WorkerID   string
	LeaseUntil string
	Now        string
}

// FencedTransactionalJournal provides the atomic ownership boundary used for
// all journal writes made by a claimed worker. The callback runs inside the
// transaction after the implementation has acquired its writer/ownership
// fence, and no handler call is made from the callback.
type FencedTransactionalJournal interface {
	InTxClaimed(context.Context, ClaimFence, func(Journal) error) error
}

// ReservationJournal persists canonical resource keys with an action while it
// has unresolved effects. Reserve is called inside InTxClaimed, allowing the
// implementation to inspect other active action rows and reject overlap
// atomically across workers and process restarts.
type ReservationJournal interface {
	Reserve(context.Context, string, []string) error
}

// OutcomeUpdate is the CAS-fenced action transition persisted after attempt
// and effect evidence. Empty optional fields clear their corresponding SQL
// columns; the current action is read before constructing this value.
type OutcomeUpdate struct {
	ID              string
	Version         int64
	State           domain.ActionState
	NextAttemptAt   string
	ClaimedBy       string
	LeaseUntil      string
	Outcome         json.RawMessage
	UnresolvedCount int64
	UpdatedAt       string
}

// RetryPolicy is persisted by converting the delay into NextAttemptAt. A
// zero MaxAttempts means no retry-count deadline; cancellation or DeadlineAt
// remains the explicit stop condition for an approved action.
type RetryPolicy struct {
	Initial     time.Duration
	Maximum     time.Duration
	Multiplier  float64
	MaxAttempts int
}

func (policy RetryPolicy) normalized() RetryPolicy {
	if policy.Initial <= 0 {
		policy.Initial = 5 * time.Second
	}
	if policy.Maximum <= 0 {
		policy.Maximum = 15 * time.Minute
	}
	if policy.Multiplier < 1 {
		policy.Multiplier = 2
	}
	return policy
}

func (policy RetryPolicy) delay(attempt int64) time.Duration {
	policy = policy.normalized()
	if attempt <= 1 {
		return policy.Initial
	}
	delay := float64(policy.Initial)
	for index := int64(1); index < attempt; index++ {
		delay *= policy.Multiplier
		if delay >= float64(policy.Maximum) {
			return policy.Maximum
		}
	}
	if delay > float64(policy.Maximum) {
		return policy.Maximum
	}
	return time.Duration(delay)
}

// Options controls one executor instance.
type Options struct {
	WorkerID                 string
	LeaseDuration            time.Duration
	LeaseRenewalInterval     time.Duration
	JournalTimeout           time.Duration
	CancellationPollInterval time.Duration
	Retry                    RetryPolicy
	MaxBatch                 int
	Now                      func() time.Time
	AttemptID                func(actionID string, number int64, phase AttemptPhase) string
	EffectID                 func(actionID string, ordinal int64) string
}

func (options Options) normalized() Options {
	if strings.TrimSpace(options.WorkerID) == "" {
		options.WorkerID = "mastarr-executor"
	}
	if options.LeaseDuration <= 0 {
		options.LeaseDuration = 30 * time.Second
	}
	if options.LeaseRenewalInterval <= 0 {
		options.LeaseRenewalInterval = options.LeaseDuration / 3
		if options.LeaseRenewalInterval <= 0 {
			options.LeaseRenewalInterval = time.Millisecond
		}
	}
	if options.JournalTimeout <= 0 {
		options.JournalTimeout = 5 * time.Second
	}
	if options.CancellationPollInterval <= 0 {
		options.CancellationPollInterval = 100 * time.Millisecond
	}
	if options.MaxBatch <= 0 {
		options.MaxBatch = 16
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	if options.AttemptID == nil {
		options.AttemptID = func(actionID string, number int64, phase AttemptPhase) string {
			return fmt.Sprintf("%s:attempt:%d:%s", actionID, number, phase)
		}
	}
	if options.EffectID == nil {
		options.EffectID = func(actionID string, ordinal int64) string {
			return fmt.Sprintf("%s:effect:%d", actionID, ordinal)
		}
	}
	options.Retry = options.Retry.normalized()
	return options
}

// Executor is a single process manager. Multiple Executor workers may share
// a journal: Claim's version/lease CAS is the authority, while reservations
// serialize overlapping local actions inside one process.
type Executor struct {
	journal       Journal
	options       Options
	handlersMu    sync.RWMutex
	handlers      map[domain.ActionKind]Handler
	reservations  map[string]string
	reservationMu sync.Mutex
	activeMu      sync.Mutex
	active        map[string]*activeHandler
	barrierMu     sync.Mutex
	barrierRetry  map[string]struct{}
}

// claimState carries the mutable lease portion of one immutable claim
// generation. Its read lock spans each fenced transaction so a renewal cannot
// swap the lease between fence construction and the CAS check.
type claimState struct {
	mu    sync.RWMutex
	fence ClaimFence
}

type claimStateContextKey struct{}

func withClaimState(ctx context.Context, fence ClaimFence) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, claimStateContextKey{}, &claimState{fence: fence})
}

func claimStateFromContext(ctx context.Context) *claimState {
	if ctx == nil {
		return nil
	}
	state, _ := ctx.Value(claimStateContextKey{}).(*claimState)
	return state
}

func (state *claimState) snapshot() ClaimFence {
	state.mu.RLock()
	defer state.mu.RUnlock()
	return state.fence
}

// advanceAfterOutcome accepts the single version increment made by an
// action-level barrier marker while the same claim remains live. The marker
// is an intentionally un-fenced recovery write, so the worker must adopt its
// new version before making another fenced journal write. The exact worker,
// lease and one-step version relationship keep an unrelated generation from
// being adopted accidentally.
func (state *claimState) advanceAfterOutcome(action Action) bool {
	state.mu.Lock()
	defer state.mu.Unlock()
	if action.ID != state.fence.ActionID || action.State != domain.ActionRunning || action.ClaimedBy != state.fence.WorkerID || action.LeaseUntil != state.fence.LeaseUntil || action.CancellationRequestedAt != "" || action.Version != state.fence.Version+1 {
		return false
	}
	state.fence.Version = action.Version
	return true
}

type activeHandler struct {
	cancel context.CancelFunc
}

// New constructs an executor over a durable journal.
func New(journal Journal, options Options) (*Executor, error) {
	if journal == nil {
		return nil, errors.New("execution journal is required")
	}
	if _, ok := journal.(TransactionalJournal); !ok {
		return nil, fmt.Errorf("%w: transactional journal capability is required", ErrInvalidJournal)
	}
	if _, ok := journal.(FencedTransactionalJournal); !ok {
		return nil, fmt.Errorf("%w: fenced transactional journal capability is required", ErrInvalidJournal)
	}
	if _, ok := journal.(ReservationJournal); !ok {
		return nil, fmt.Errorf("%w: durable reservation journal capability is required", ErrInvalidJournal)
	}
	return &Executor{
		journal:      journal,
		options:      options.normalized(),
		handlers:     make(map[domain.ActionKind]Handler),
		reservations: make(map[string]string),
		active:       make(map[string]*activeHandler),
		barrierRetry: make(map[string]struct{}),
	}, nil
}

// RegisterHandler adds one typed action implementation. Duplicate kinds are
// rejected so configuration cannot silently replace a reviewed handler.
func (executor *Executor) RegisterHandler(handler Handler) error {
	if executor == nil || handler == nil {
		return errors.New("execution handler is required")
	}
	kind := handler.Kind()
	if !kind.Valid() {
		return fmt.Errorf("unsupported action handler kind %q", kind)
	}
	executor.handlersMu.Lock()
	defer executor.handlersMu.Unlock()
	if _, exists := executor.handlers[kind]; exists {
		return fmt.Errorf("handler for %q is already registered", kind)
	}
	executor.handlers[kind] = handler
	return nil
}

func (executor *Executor) handler(kind domain.ActionKind) (Handler, error) {
	executor.handlersMu.RLock()
	defer executor.handlersMu.RUnlock()
	handler, ok := executor.handlers[kind]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrNoHandler, kind)
	}
	return handler, nil
}

func nowUTC(options Options) time.Time {
	return options.Now().UTC()
}

func formatTime(value time.Time) string {
	return value.UTC().Format(time.RFC3339Nano)
}

func (executor *Executor) journalContext(parent context.Context) (context.Context, context.CancelFunc) {
	if parent == nil {
		parent = context.Background()
	}
	return context.WithTimeout(context.WithoutCancel(parent), executor.options.JournalTimeout)
}

func (executor *Executor) claimFence(ctx context.Context, action Action) ClaimFence {
	if state := claimStateFromContext(ctx); state != nil {
		fence := state.snapshot()
		if fence.ActionID == action.ID && fence.Version > 0 {
			return fence
		}
	}
	return ClaimFence{
		ActionID:   action.ID,
		Version:    action.Version,
		WorkerID:   executor.options.WorkerID,
		LeaseUntil: action.LeaseUntil,
		Now:        formatTime(nowUTC(executor.options)),
	}
}

func (executor *Executor) withClaimedTransaction(ctx context.Context, action Action, fn func(context.Context, Journal) error) error {
	if fn == nil {
		return errors.New("execution transaction callback is required")
	}
	transactional, ok := executor.journal.(FencedTransactionalJournal)
	if !ok {
		return fmt.Errorf("%w: fenced transactional journal capability is required", ErrInvalidJournal)
	}
	transactionCtx, cancel := executor.journalContext(ctx)
	defer cancel()
	state := claimStateFromContext(ctx)
	if state != nil {
		// Hold the read side of the lease lock through the journal CAS and
		// transaction commit. Renewal therefore cannot replace the exact lease
		// between fence construction and the ownership check.
		state.mu.RLock()
		defer state.mu.RUnlock()
	}
	var err error
	for retry := 0; retry < 4; retry++ {
		fence := executor.claimFence(ctx, action)
		err = transactional.InTxClaimed(transactionCtx, fence, func(journal Journal) error {
			return fn(transactionCtx, journal)
		})
		if err == nil {
			return nil
		}
		// An optimistic transaction can lose only to an unrelated journal
		// commit (for example, a barrier retry updating its exact attempt). A
		// bounded retry lets the same immutable claim generation proceed while
		// still failing closed when the ownership fence itself is stale.
		if !errors.Is(err, ErrLeaseLost) || retry == 3 {
			return err
		}
		time.Sleep(time.Millisecond)
	}
	return err
}

func (executor *Executor) renewClaimLease(ctx context.Context) func() {
	state := claimStateFromContext(ctx)
	if state == nil {
		return func() {}
	}
	renewCtx, stop := context.WithCancel(ctx)
	done := make(chan struct{})
	renew := func() {
		// Hold the write side through the CAS and the in-memory fence update.
		// Claimed transactions hold the read side for their entire commit, so a
		// transaction cannot capture the old lease between these two operations.
		state.mu.Lock()
		defer state.mu.Unlock()
		fence := state.fence
		now := nowUTC(executor.options)
		if expired(fence.LeaseUntil, now) {
			return
		}
		leaseUntil := formatTime(now.Add(executor.options.LeaseDuration))
		queryCtx, queryCancel := context.WithTimeout(renewCtx, executor.options.JournalTimeout)
		err := executor.journal.RenewLease(queryCtx, fence, leaseUntil, formatTime(now))
		queryCancel()
		if err == nil {
			state.fence.LeaseUntil = leaseUntil
			state.fence.Now = formatTime(now)
		}
	}
	// Establish the first extension before the caller can begin another
	// fenced transaction. This avoids a renewal racing the initial attempt
	// journal while still giving the handler a full lease interval.
	renew()
	go func() {
		defer close(done)
		ticker := time.NewTicker(executor.options.LeaseRenewalInterval)
		defer ticker.Stop()
		for {
			select {
			case <-renewCtx.Done():
				return
			case <-ticker.C:
				renew()
			}
		}
	}()
	return func() {
		stop()
		<-done
	}
}

func (executor *Executor) withTransaction(ctx context.Context, fn func(context.Context, Journal) error) error {
	if fn == nil {
		return errors.New("execution transaction callback is required")
	}
	transactional, ok := executor.journal.(TransactionalJournal)
	if !ok {
		return fmt.Errorf("%w: transactional journal capability is required", ErrInvalidJournal)
	}
	transactionCtx, cancel := executor.journalContext(ctx)
	defer cancel()
	return transactional.InTx(transactionCtx, func(journal Journal) error {
		return fn(transactionCtx, journal)
	})
}

func (executor *Executor) registerActiveHandler(actionID string, cancel context.CancelFunc) func() {
	active := &activeHandler{cancel: cancel}
	executor.activeMu.Lock()
	executor.active[actionID] = active
	executor.activeMu.Unlock()
	return func() {
		executor.activeMu.Lock()
		if executor.active[actionID] == active {
			delete(executor.active, actionID)
		}
		executor.activeMu.Unlock()
	}
}

// watchHandlerState makes durable cancellation and ownership loss observable
// to a handler regardless of which executor instance receives the request.
// The handler itself remains responsible for checking its context at bounded
// operation checkpoints; the watcher only supplies that cooperative signal.
func (executor *Executor) watchHandlerState(action Action, cancel context.CancelFunc) func() {
	watchCtx, stop := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		interval := executor.options.CancellationPollInterval
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		check := func() bool {
			queryCtx, queryCancel := context.WithTimeout(watchCtx, executor.options.JournalTimeout)
			current, err := executor.journal.GetAction(queryCtx, action.ID)
			queryCancel()
			if err != nil {
				return false
			}
			if current.State != domain.ActionRunning || current.ClaimedBy != action.ClaimedBy || current.Version != action.Version || current.CancellationRequestedAt != "" || expired(current.LeaseUntil, nowUTC(executor.options)) {
				cancel()
				return true
			}
			return false
		}
		for {
			select {
			case <-watchCtx.Done():
				return
			case <-ticker.C:
				if check() {
					return
				}
			}
		}
	}()
	return func() {
		stop()
		<-done
	}
}

func (executor *Executor) cancelActiveHandler(actionID string) {
	executor.activeMu.Lock()
	active := executor.active[actionID]
	executor.activeMu.Unlock()
	if active != nil && active.cancel != nil {
		active.cancel()
	}
}

func parseTime(value string) (time.Time, error) {
	if strings.TrimSpace(value) == "" {
		return time.Time{}, nil
	}
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}, fmt.Errorf("invalid persisted timestamp %q: %w", value, err)
	}
	return parsed, nil
}

func isNoRows(err error) bool {
	return errors.Is(err, sql.ErrNoRows) || errors.Is(err, ErrNotFound)
}

// ErrNotFound is implemented by SQLJournal and is useful to fake journals.
var ErrNotFound = errors.New("execution resource was not found")

// RecoveryReport records startup recovery without claiming any external
// effect was undone. Running dispatches become read-only reconciliation.
type RecoveryReport struct {
	Actions          int
	Attempts         int
	Expired          int
	Cancelled        int
	DeadlineExceeded int
}

// Recover applies durable startup recovery. It never calls a handler.
func (executor *Executor) Recover(ctx context.Context) (RecoveryReport, error) {
	if executor == nil {
		return RecoveryReport{}, errors.New("execution executor is nil")
	}
	now := formatTime(nowUTC(executor.options))
	var report RecoveryReport
	actions, err := executor.journal.RecoverRunning(ctx, now)
	if err != nil {
		return report, err
	}
	report.Actions = len(actions)
	attempts, err := executor.journal.RecoverRunningAttempts(ctx)
	if err != nil {
		return report, err
	}
	report.Attempts = len(attempts)
	cancelled, err := executor.journal.FinalizeCancelled(ctx, now)
	if err != nil {
		return report, err
	}
	report.Cancelled = len(cancelled)
	deadline, err := executor.journal.FinalizeDeadline(ctx, now)
	if err != nil {
		return report, err
	}
	report.DeadlineExceeded = len(deadline)
	return report, nil
}

// BatchResult describes one polling pass. Item errors are returned in Results
// while the pass continues so one unavailable dependency cannot starve other
// due actions.
type BatchResult struct {
	RecoveredExpired int
	Cancelled        int
	DeadlineExceeded int
	Considered       int
	Claimed          int
	Results          []Result
}

// Result is the durable execution outcome observed by this call.
type Result struct {
	Action     Action
	Outcome    domain.EffectOutcome
	State      domain.ActionState
	Dispatched bool
	AttemptID  string
	Err        error
}

// RunOnce recovers expired leases, claims due work, and processes one bounded
// batch. It does not perform startup recovery of every running row; call
// Recover once during process startup for that boundary.
func (executor *Executor) RunOnce(ctx context.Context) (BatchResult, error) {
	if executor == nil {
		return BatchResult{}, errors.New("execution executor is nil")
	}
	now := formatTime(nowUTC(executor.options))
	var batch BatchResult
	expired, err := executor.journal.RecoverExpired(ctx, now)
	if err != nil {
		return batch, err
	}
	batch.RecoveredExpired = len(expired)
	cancelled, err := executor.journal.FinalizeCancelled(ctx, now)
	if err != nil {
		return batch, err
	}
	batch.Cancelled = len(cancelled)
	deadline, err := executor.journal.FinalizeDeadline(ctx, now)
	if err != nil {
		return batch, err
	}
	batch.DeadlineExceeded = len(deadline)

	due, err := executor.journal.ListDue(ctx, now, executor.options.MaxBatch)
	if err != nil {
		return batch, err
	}
	reconciling, err := executor.journal.ListReconciling(ctx, now, executor.options.MaxBatch)
	if err != nil {
		return batch, err
	}
	seen := make(map[string]struct{}, len(due)+len(reconciling))
	for _, action := range append(due, reconciling...) {
		if _, exists := seen[action.ID]; exists {
			continue
		}
		seen[action.ID] = struct{}{}
		batch.Considered++
		result := executor.claimAndProcess(ctx, action)
		if result.Action.ID != "" {
			batch.Results = append(batch.Results, result)
		}
		if result.Dispatched || result.State == domain.ActionRunning {
			batch.Claimed++
		}
	}
	return batch, nil
}

// RunAction claims and processes one action by ID. It is useful to expose a
// durable worker trigger while preserving the same claim and handler path as
// RunOnce.
func (executor *Executor) RunAction(ctx context.Context, id string) Result {
	if executor == nil {
		return Result{Err: errors.New("execution executor is nil")}
	}
	action, err := executor.journal.GetAction(ctx, id)
	if err != nil {
		return Result{Err: err}
	}
	return executor.claimAndProcess(ctx, action)
}

func (executor *Executor) claimAndProcess(ctx context.Context, action Action) Result {
	result := Result{Action: action, State: action.State}
	if err := action.validate(); err != nil {
		result.Err = err
		return result
	}
	now := nowUTC(executor.options)
	// A cancelled/deadline-exceeded reconciliation cannot use the ordinary
	// ClaimActionRun SQL predicate. It is read-only, so process it without a
	// mutation lease and let its final transition use the action version CAS.
	if action.State == domain.ActionReconciling && (action.CancellationRequestedAt != "" || expired(action.DeadlineAt, now)) {
		result = executor.processReconciliation(ctx, action, false)
		if !retainsLocalReservation(result.State) {
			executor.releaseReservationsForAction(action.ID)
		}
		return result
	}
	plan, err := executor.journal.GetPlan(ctx, action.PlanID)
	if err != nil {
		result.Err = err
		return result
	}
	if err := plan.validate(); err != nil {
		result.Err = err
		return result
	}
	if plan.Kind != action.Kind {
		result.Err = fmt.Errorf("%w: action plan kind %q differs from action kind %q", ErrInvalidJournal, plan.Kind, action.Kind)
		return result
	}
	handler, err := executor.handler(action.Kind)
	if err != nil {
		result.Err = executor.hold(ctx, action, err)
		return result
	}

	claimed, err := executor.journal.Claim(ctx, action.ID, action.Version, executor.options.WorkerID, formatTime(now.Add(executor.options.LeaseDuration)), formatTime(now))
	if err != nil {
		if isNoRows(err) {
			result.Err = ErrLeaseLost
		} else {
			result.Err = err
		}
		return result
	}
	claimed.Kind = action.Kind
	claimed.PlanID = action.PlanID
	claimed.PlanRevision = action.PlanRevision
	claimed.PlanDigest = action.PlanDigest
	if err := claimed.validate(); err != nil {
		result.Action = claimed
		result.Err = err
		return result
	}
	result.Action = claimed
	claimedCtx := withClaimState(ctx, executor.claimFence(nil, claimed))
	_, err = executor.acquireAndPersistReservations(claimedCtx, claimed, handler)
	if err != nil {
		result.Err = executor.waitOwned(claimedCtx, claimed, err)
		result.State = domain.ActionWaitingDependency
		return result
	}
	stopRenewal := executor.renewClaimLease(claimedCtx)
	defer stopRenewal()

	var processed Result
	if action.State == domain.ActionReconciling {
		processed = executor.processReconciliation(claimedCtx, claimed, true)
	} else {
		processed = executor.processObserved(claimedCtx, claimed, handler)
	}
	// Reservations remain durable and local while an effect is unresolved. A
	// stale worker releases only its process-local map on lease loss; the
	// durable outcome metadata remains authoritative for the next worker and
	// avoids a local reservation leak after reconciliation completes elsewhere.
	if !retainsLocalReservation(processed.State) || errors.Is(processed.Err, ErrLeaseLost) {
		executor.releaseReservationsForAction(claimed.ID)
	}
	return processed
}

func (executor *Executor) acquireAndPersistReservations(ctx context.Context, action Action, handler Handler) ([]string, error) {
	keys, err := reservationKeys(action, handler)
	if err != nil {
		return nil, err
	}
	canonical, err := executor.acquireReservations(action.ID, keys)
	if err != nil {
		return nil, err
	}
	err = executor.withClaimedTransaction(ctx, action, func(transactionCtx context.Context, journal Journal) error {
		reservations, ok := journal.(ReservationJournal)
		if !ok {
			return fmt.Errorf("%w: durable reservation journal capability is required", ErrInvalidJournal)
		}
		return reservations.Reserve(transactionCtx, action.ID, canonical)
	})
	if err != nil {
		executor.releaseReservations(canonical, action.ID)
		return nil, err
	}
	return canonical, nil
}

func (executor *Executor) acquireReservations(actionID string, canonical []string) ([]string, error) {
	executor.reservationMu.Lock()
	defer executor.reservationMu.Unlock()
	for _, key := range canonical {
		for existing, owner := range executor.reservations {
			if owner != actionID && reservationConflicts(existing, key) {
				return nil, fmt.Errorf("%w: %s", ErrReservationConflict, key)
			}
		}
	}
	for _, key := range canonical {
		executor.reservations[key] = actionID
	}
	return canonical, nil
}

func (executor *Executor) releaseReservations(keys []string, actionID string) {
	executor.reservationMu.Lock()
	defer executor.reservationMu.Unlock()
	for _, key := range keys {
		if executor.reservations[key] == actionID {
			delete(executor.reservations, key)
		}
	}
}

func (executor *Executor) releaseReservationsForAction(actionID string) {
	executor.reservationMu.Lock()
	defer executor.reservationMu.Unlock()
	for key, owner := range executor.reservations {
		if owner == actionID {
			delete(executor.reservations, key)
		}
	}
}

func retainsLocalReservation(state domain.ActionState) bool {
	return state == domain.ActionReconciling || state == domain.ActionNeedsReview
}

func reservationKeys(action Action, handler Handler) ([]string, error) {
	if handler == nil {
		return nil, errors.New("execution handler is required")
	}
	persisted, err := reservationKeysFromOutcome(action.Outcome)
	if err != nil {
		return nil, err
	}
	return canonicalReservationKeys(append(persisted, handler.Reservations(action)...)), nil
}

func canonicalReservationKeys(keys []string) []string {
	canonical := make([]string, 0, len(keys))
	seen := make(map[string]struct{}, len(keys))
	for _, key := range keys {
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		canonical = append(canonical, key)
	}
	sort.Strings(canonical)
	return canonical
}

func reservationConflicts(left, right string) bool {
	left = strings.TrimSpace(left)
	right = strings.TrimSpace(right)
	if left == "" || right == "" {
		return false
	}
	if left == right {
		return true
	}
	leftNamespace, leftPath := reservationNamespace(left)
	rightNamespace, rightPath := reservationNamespace(right)
	if leftNamespace != rightNamespace || leftNamespace == "" {
		return false
	}
	return pathReservationConflicts(leftPath, rightPath)
}

func reservationNamespace(value string) (string, string) {
	index := strings.IndexByte(value, ':')
	if index < 0 {
		return "", value
	}
	return value[:index], strings.TrimPrefix(value[index+1:], "/")
}

func pathReservationConflicts(left, right string) bool {
	left = strings.TrimSuffix(strings.TrimSpace(left), "/")
	right = strings.TrimSuffix(strings.TrimSpace(right), "/")
	if left == "" || right == "" {
		return true
	}
	return left == right || strings.HasPrefix(left, right+"/") || strings.HasPrefix(right, left+"/")
}

const reservationOutcomeField = "reservations"

const dispatchBarrierReturnedField = "dispatchBarrierReturnedAttempt"

// dispatchBarrierIntentField is written on the exact dispatch attempt before
// the action-level return marker. The attempt row is durable even when an
// action outcome update is temporarily unavailable, so a later executor can
// distinguish a returned handler from one that may still be running.
const dispatchBarrierIntentField = "dispatchBarrierRetryAttempt"

const dispatchBarrierIntentDetail = "dispatch handler returned; barrier release pending"

// dispatchBarrierFallbackRegistry retains returned-handler knowledge while a
// journal cannot accept either exact recovery marker. It is deliberately
// process-local: a fresh Executor in this process can continue recovery after
// writes return, while process restart still uses the ordinary running-attempt
// startup recovery boundary. Keys include both action and attempt identity so
// a stale returned handler cannot release a later dispatch generation.
type dispatchBarrierFallbackKey struct {
	actionID  string
	attemptID string
}

var dispatchBarrierFallbackRegistry = struct {
	sync.Mutex
	pending map[dispatchBarrierFallbackKey]struct{}
}{pending: make(map[dispatchBarrierFallbackKey]struct{})}

func rememberDispatchBarrierFallback(actionID, attemptID string) {
	actionID = strings.TrimSpace(actionID)
	attemptID = strings.TrimSpace(attemptID)
	if actionID == "" || attemptID == "" {
		return
	}
	dispatchBarrierFallbackRegistry.Lock()
	dispatchBarrierFallbackRegistry.pending[dispatchBarrierFallbackKey{actionID: actionID, attemptID: attemptID}] = struct{}{}
	dispatchBarrierFallbackRegistry.Unlock()
}

func forgetDispatchBarrierFallback(actionID, attemptID string) {
	dispatchBarrierFallbackRegistry.Lock()
	delete(dispatchBarrierFallbackRegistry.pending, dispatchBarrierFallbackKey{actionID: actionID, attemptID: attemptID})
	dispatchBarrierFallbackRegistry.Unlock()
}

func pendingDispatchBarrierFallbackAttempts(actionID string) map[string]struct{} {
	dispatchBarrierFallbackRegistry.Lock()
	defer dispatchBarrierFallbackRegistry.Unlock()
	pending := make(map[string]struct{})
	for key := range dispatchBarrierFallbackRegistry.pending {
		if key.actionID == actionID {
			pending[key.attemptID] = struct{}{}
		}
	}
	return pending
}

func dispatchBarrierReturnedAttempt(outcome json.RawMessage) (string, bool) {
	if len(outcome) == 0 || string(outcome) == "null" {
		return "", false
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(outcome, &object); err != nil {
		return "", false
	}
	raw, ok := object[dispatchBarrierReturnedField]
	if !ok {
		return "", false
	}
	var attemptID string
	if err := json.Unmarshal(raw, &attemptID); err != nil || strings.TrimSpace(attemptID) == "" {
		return "", false
	}
	return attemptID, true
}

func outcomeWithDispatchBarrierReturned(outcome json.RawMessage, attemptID string) (json.RawMessage, error) {
	var object map[string]json.RawMessage
	if len(outcome) == 0 || string(outcome) == "null" {
		object = make(map[string]json.RawMessage)
	} else if err := json.Unmarshal(outcome, &object); err != nil {
		return nil, fmt.Errorf("%w: invalid outcome: %v", ErrInvalidJournal, err)
	} else if object == nil {
		object = make(map[string]json.RawMessage)
	}
	encoded, err := json.Marshal(attemptID)
	if err != nil {
		return nil, fmt.Errorf("%w: encode dispatch barrier marker: %v", ErrInvalidJournal, err)
	}
	object[dispatchBarrierReturnedField] = encoded
	encoded, err = json.Marshal(object)
	if err != nil {
		return nil, fmt.Errorf("%w: encode outcome: %v", ErrInvalidJournal, err)
	}
	return encoded, nil
}

func dispatchBarrierRetryIntentAttempt(evidence json.RawMessage) (string, bool) {
	if len(evidence) == 0 || string(evidence) == "null" {
		return "", false
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(evidence, &object); err != nil {
		return "", false
	}
	raw, ok := object[dispatchBarrierIntentField]
	if !ok {
		return "", false
	}
	var attemptID string
	if err := json.Unmarshal(raw, &attemptID); err != nil || strings.TrimSpace(attemptID) == "" {
		return "", false
	}
	return attemptID, true
}

func evidenceWithDispatchBarrierRetryIntent(evidence json.RawMessage, attemptID string) (json.RawMessage, error) {
	var object map[string]json.RawMessage
	if len(evidence) == 0 || string(evidence) == "null" {
		object = make(map[string]json.RawMessage)
	} else if err := json.Unmarshal(evidence, &object); err != nil {
		return nil, fmt.Errorf("%w: invalid attempt evidence: %v", ErrInvalidJournal, err)
	} else if object == nil {
		object = make(map[string]json.RawMessage)
	}
	encoded, err := json.Marshal(attemptID)
	if err != nil {
		return nil, fmt.Errorf("%w: encode dispatch barrier intent: %v", ErrInvalidJournal, err)
	}
	object[dispatchBarrierIntentField] = encoded
	encoded, err = json.Marshal(object)
	if err != nil {
		return nil, fmt.Errorf("%w: encode attempt evidence: %v", ErrInvalidJournal, err)
	}
	return encoded, nil
}

func reservationKeysFromOutcome(outcome json.RawMessage) ([]string, error) {
	if len(outcome) == 0 || string(outcome) == "null" {
		return nil, nil
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(outcome, &object); err != nil {
		return nil, fmt.Errorf("%w: invalid outcome reservation metadata: %v", ErrInvalidJournal, err)
	}
	raw, ok := object[reservationOutcomeField]
	if !ok || string(raw) == "null" {
		return nil, nil
	}
	var keys []string
	if err := json.Unmarshal(raw, &keys); err != nil {
		return nil, fmt.Errorf("%w: invalid outcome reservations: %v", ErrInvalidJournal, err)
	}
	return canonicalReservationKeys(keys), nil
}

func outcomeWithReservations(outcome json.RawMessage, keys []string) (json.RawMessage, error) {
	var object map[string]json.RawMessage
	if len(outcome) == 0 || string(outcome) == "null" {
		object = make(map[string]json.RawMessage)
	} else if err := json.Unmarshal(outcome, &object); err != nil {
		return nil, fmt.Errorf("%w: invalid outcome: %v", ErrInvalidJournal, err)
	} else if object == nil {
		object = make(map[string]json.RawMessage)
	}
	keys = canonicalReservationKeys(keys)
	if len(keys) == 0 {
		delete(object, reservationOutcomeField)
	} else {
		encoded, err := json.Marshal(keys)
		if err != nil {
			return nil, fmt.Errorf("%w: encode reservations: %v", ErrInvalidJournal, err)
		}
		object[reservationOutcomeField] = encoded
	}
	encoded, err := json.Marshal(object)
	if err != nil {
		return nil, fmt.Errorf("%w: encode outcome: %v", ErrInvalidJournal, err)
	}
	return encoded, nil
}

func outcomePreservingReservations(existing, next json.RawMessage, state domain.ActionState) (json.RawMessage, error) {
	keys, err := reservationKeysFromOutcome(existing)
	if err != nil {
		return nil, err
	}
	if state != domain.ActionRunning && state != domain.ActionReconciling && state != domain.ActionNeedsReview {
		keys = nil
	}
	return outcomeWithReservations(next, keys)
}

func claimFenceMatches(action Action, fence ClaimFence) bool {
	if action.ID != fence.ActionID || action.State != domain.ActionRunning || action.ClaimedBy != fence.WorkerID || action.LeaseUntil != fence.LeaseUntil {
		return false
	}
	if action.Version != fence.Version && (action.Version != fence.Version+1 || action.CancellationRequestedAt == "") {
		return false
	}
	now, err := parseTime(fence.Now)
	if err != nil {
		return false
	}
	lease, err := parseTime(action.LeaseUntil)
	return err == nil && !lease.IsZero() && lease.After(now)
}

func expired(value string, now time.Time) bool {
	parsed, err := parseTime(value)
	return err == nil && !parsed.IsZero() && !parsed.After(now)
}

func (executor *Executor) processObserved(ctx context.Context, action Action, handler Handler) Result {
	result := Result{Action: action, State: action.State}
	attempt, err := executor.newAttemptOwned(ctx, action, AttemptObserve, CertaintyNotDispatched)
	if err != nil {
		result.Err = err
		return result
	}
	result.AttemptID = attempt.ID
	var observation Observation
	var observeErr error
	func() {
		observeCtx, cancelObserve := executor.handlerContext(ctx, action)
		unregister := executor.registerActiveHandler(action.ID, cancelObserve)
		stopWatcher := executor.watchHandlerState(action, cancelObserve)
		defer func() {
			stopWatcher()
			unregister()
			cancelObserve()
		}()
		observation, observeErr = handler.Observe(observeCtx, action)
	}()
	err = observeErr
	if err != nil {
		kind := failureKind(err)
		currentCtx, currentCancel := executor.journalContext(context.Background())
		current, currentErr := executor.journal.GetAction(currentCtx, action.ID)
		currentCancel()
		if currentErr == nil {
			if stopState, _ := executor.cancelledOrDeadline(current); stopState != "" {
				finished := finishAttempt(attempt, domain.AttemptCancelled, CertaintyNotDispatched, err, executor.options)
				if _, updateErr := executor.updateAttemptOwned(ctx, action, finished); updateErr != nil {
					result.Err = updateErr
					return result
				}
				updated, updateErr := executor.transitionStopOwned(ctx, action, finished, string(kind))
				result.Action = updated
				result.State = updated.State
				result.Err = updateErr
				if result.Err == nil {
					result.Err = err
				}
				return result
			}
		}
		finished := attempt
		finished.State = domain.AttemptFailed
		finished.FinishedAt = formatTime(nowUTC(executor.options))
		finished.ErrorCode = string(kind)
		finished.ErrorDetail = safeDetail(err)
		if failureWasDispatched(err) || kind == FailureUncertain {
			finished.OutcomeCertainty = CertaintyUncertain
		}
		if _, updateErr := executor.updateAttemptOwned(ctx, action, finished); updateErr != nil {
			result.Err = updateErr
			return result
		}
		if kind == FailureDependency {
			result.Err = executor.waitOwned(ctx, action, err)
			result.Action = action
			result.State = domain.ActionWaitingDependency
			return result
		}
		result.Err = executor.holdOwned(ctx, action, err)
		result.Action = action
		result.State = domain.ActionNeedsReview
		return result
	}
	if err := observation.validate(action.ID); err != nil {
		finished := finishAttempt(attempt, domain.AttemptFailed, CertaintyKnown, err, executor.options)
		if _, updateErr := executor.updateAttemptOwned(ctx, action, finished); updateErr != nil {
			result.Err = updateErr
			return result
		}
		result.Err = executor.holdOwned(ctx, action, err)
		result.State = domain.ActionNeedsReview
		return result
	}
	if observation.State == ObserveUnknown {
		finished := finishAttempt(attempt, domain.AttemptSucceeded, CertaintyKnown, nil, executor.options)
		finished.Evidence = evidenceJSON(observation.Evidence)
		if _, err := executor.updateAttemptOwned(ctx, action, finished); err != nil {
			result.Err = err
			return result
		}
		result.Err = executor.holdOwned(ctx, action, errors.New("desired state could not be proven"))
		result.State = domain.ActionNeedsReview
		return result
	}
	if observation.State == ObserveSatisfied {
		finished := finishAttempt(attempt, domain.AttemptSucceeded, CertaintyKnown, nil, executor.options)
		finished.Evidence = evidenceJSON(observation.Evidence)
		if _, err := executor.updateAttemptOwned(ctx, action, finished); err != nil {
			result.Err = err
			return result
		}
		if err := executor.recordEffectsOwned(ctx, action, finished, observation.Effects, EffectAlreadySatisfied); err != nil {
			result.Err = err
			return result
		}
		updated, err := executor.transitionOwned(ctx, action, finished, domain.ActionSucceeded, domain.OutcomeAlreadySatisfied, 0, "already_satisfied")
		result.Action = updated
		result.State = updated.State
		result.Outcome = domain.OutcomeAlreadySatisfied
		result.Err = err
		return result
	}

	// Observe succeeded and requested a mutation. Close that journal entry and
	// create a separate dispatch entry plus pending effects before the handler
	// can call an upstream API or change a file.
	finishedObserve := finishAttempt(attempt, domain.AttemptSucceeded, CertaintyKnown, nil, executor.options)
	finishedObserve.Evidence = evidenceJSON(observation.Evidence)
	dispatchAttempt, err := executor.prepareDispatch(ctx, action, finishedObserve, observation.Effects)
	if err != nil {
		result.Err = err
		return result
	}
	result.AttemptID = dispatchAttempt.ID

	dispatchCtx, cancelDispatch := executor.handlerContext(ctx, action)
	unregister := executor.registerActiveHandler(action.ID, cancelDispatch)
	stopWatcher := executor.watchHandlerState(action, cancelDispatch)
	defer func() {
		stopWatcher()
		unregister()
		cancelDispatch()
		// A handler that returns after its claim was recovered cannot write
		// through the claim fence. Mark its dispatch attempt as no longer
		// externally active so the next worker may reconcile it. If the handler
		// is still running, this deferred call has not happened and the durable
		// running attempt remains a recovery barrier.
		if err := executor.releaseDispatchBarrier(action.ID, dispatchAttempt.ID); err != nil {
			executor.scheduleBarrierRetry(action.ID, dispatchAttempt.ID)
		}
	}()
	if err := executor.beforeDispatch(dispatchCtx, action); err != nil {
		kind := failureKind(err)
		if errors.Is(err, ErrLeaseLost) {
			result.Err = err
			return result
		}
		finished := finishAttempt(dispatchAttempt, domain.AttemptCancelled, CertaintyNotDispatched, err, executor.options)
		if _, updateErr := executor.updateAttemptOwned(ctx, action, finished); updateErr != nil {
			result.Err = updateErr
			return result
		}
		updated, updateErr := executor.transitionStopOwned(ctx, action, finished, string(kind))
		result.Action = updated
		result.State = updated.State
		result.Err = updateErr
		if result.Err == nil {
			result.Err = err
		}
		return result
	}

	dispatchResult, dispatchErr := handler.Dispatch(dispatchCtx, action, dispatchAttempt)
	result.Dispatched = true
	if dispatchErr != nil {
		return executor.finishDispatchError(ctx, result, dispatchAttempt, dispatchResult, dispatchErr)
	}
	if err := dispatchResult.validate(action.ID); err != nil {
		return executor.finishDispatchError(ctx, result, dispatchAttempt, dispatchResultIdentity(dispatchResult), NewDispatchedFailure(FailureUncertain, err))
	}
	// A returned success is only an input to the final read-back. The handler
	// must prove the desired state through its read-only Observe implementation.
	readBack, readErr := handler.Observe(dispatchCtx, action)
	if readErr != nil {
		return executor.finishDispatchError(ctx, result, dispatchAttempt, dispatchResultIdentity(dispatchResult), NewDispatchedFailure(FailureUncertain, readErr))
	}
	if err := readBack.validate(action.ID); err != nil || readBack.State != ObserveSatisfied || effectSetMismatch(observation.Effects, readBack.Effects, action.ID) {
		if err == nil {
			if effectSetMismatch(observation.Effects, readBack.Effects, action.ID) {
				err = errors.New("dispatch read-back effects did not match the approved target set")
			} else {
				err = errors.New("dispatch read-back did not prove desired state")
			}
		}
		return executor.finishDispatchError(ctx, result, dispatchAttempt, dispatchResultIdentity(dispatchResult), NewDispatchedFailure(FailureUncertain, err))
	}
	if len(dispatchResult.Effects) > 0 && effectSetMismatch(observation.Effects, dispatchResult.Effects, action.ID) {
		return executor.finishDispatchError(ctx, result, dispatchAttempt, dispatchResultIdentity(dispatchResult), NewDispatchedFailure(FailureUncertain, errors.New("dispatch effects did not match the approved target set")))
	}
	finished := finishAttempt(dispatchAttempt, domain.AttemptSucceeded, CertaintyKnown, nil, executor.options)
	finished.ExternalID = dispatchResult.ExternalID
	finished.Evidence = evidenceJSON(append(dispatchResult.Evidence, readBack.Evidence...))
	if _, err := executor.updateAttemptOwned(ctx, action, finished); err != nil {
		result.Err = err
		return result
	}
	if err := executor.recordEffectsOwned(ctx, action, finished, readBack.Effects, effectForOutcome(dispatchResult.Outcome)); err != nil {
		result.Err = err
		return result
	}
	updated, _, outcome, err := executor.transitionDispatchOwned(ctx, action, finished, dispatchResult.Outcome)
	result.Action = updated
	result.State = updated.State
	result.Outcome = outcome
	result.Err = err
	return result
}

func (executor *Executor) activeDispatchAttempt(ctx context.Context, actionID string) (Attempt, bool, error) {
	attempts, err := executor.journal.ListAttempts(ctx, actionID)
	if err != nil {
		return Attempt{}, false, err
	}
	var active Attempt
	found := false
	for _, attempt := range attempts {
		if attempt.Phase != AttemptDispatch || attempt.State != domain.AttemptRunning {
			continue
		}
		if !found || attempt.AttemptNumber > active.AttemptNumber {
			active = attempt
			found = true
		}
	}
	return active, found, nil
}

func (executor *Executor) deferActiveDispatch(ctx context.Context, action Action, attempt Attempt, claimed bool) Result {
	result := Result{Action: action, State: action.State, AttemptID: attempt.ID}
	nextAttemptAt := executor.retryAt(attempt.AttemptNumber)
	var updated Action
	var err error
	if claimed {
		updated, err = executor.transitionOwnedAt(ctx, action, attempt, domain.ActionReconciling, "", 1, "dispatch_still_active", nextAttemptAt)
	} else {
		var latest Action
		latest, err = executor.currentAction(ctx, action)
		if err == nil {
			updated, err = executor.transitionAt(ctx, latest, attempt, domain.ActionReconciling, "", 1, "dispatch_still_active", nextAttemptAt)
		}
	}
	if err != nil {
		result.Err = err
		return result
	}
	result.Action = updated
	result.State = updated.State
	return result
}

func (executor *Executor) scheduleBarrierRetry(actionID, attemptID string) {
	// Keep returned-handler knowledge after this executor's bounded retry loop
	// exits. A later executor in the same process can discover the exact live
	// dispatch even if both journal markers were unavailable throughout it.
	rememberDispatchBarrierFallback(actionID, attemptID)
	key := actionID + "\x00" + attemptID
	executor.barrierMu.Lock()
	if _, exists := executor.barrierRetry[key]; exists {
		executor.barrierMu.Unlock()
		return
	}
	executor.barrierRetry[key] = struct{}{}
	executor.barrierMu.Unlock()
	go func() {
		defer func() {
			executor.barrierMu.Lock()
			delete(executor.barrierRetry, key)
			executor.barrierMu.Unlock()
		}()
		interval := executor.options.CancellationPollInterval
		if interval <= 0 || interval > 100*time.Millisecond {
			interval = 100 * time.Millisecond
		}
		for attempt := 0; attempt < 32; attempt++ {
			if attempt > 0 {
				timer := time.NewTimer(interval)
				<-timer.C
			}
			if err := executor.releaseDispatchBarrier(actionID, attemptID); err == nil {
				return
			}
		}
	}()
}

// releaseDispatchBarrier records that a dispatch handler has returned after
// the normal fenced path could no longer update its attempt. The immutable
// attempt ID prevents an old worker from changing a later dispatch attempt;
// leaving the attempt running keeps recovered work blocked until this point.
func (executor *Executor) releaseDispatchBarrier(actionID, attemptID string) error {
	return executor.releaseDispatchBarrierWithClaim(actionID, attemptID, false)
}

// releaseDispatchBarrierWithClaim is used by a worker that has just claimed
// an action carrying returned-dispatch evidence. That fresh claim remains
// valid while the worker reconciles the exact returned attempt. Background
// release paths pass false so a stale claim is cleared as soon as the barrier
// is durably released.
func (executor *Executor) releaseDispatchBarrierWithClaim(actionID, attemptID string, preserveClaim bool) error {
	rememberDispatchBarrierFallback(actionID, attemptID)
	var err error
	var intentErr error
	for retry := 0; retry < 4; retry++ {
		intentErr = executor.persistDispatchBarrierIntent(actionID, attemptID)
		if intentErr == nil {
			break
		}
		if executor.dispatchBarrierResolvedAndForget(actionID, attemptID) {
			return nil
		}
		if !errors.Is(intentErr, ErrLeaseLost) || retry == 3 {
			break
		}
		time.Sleep(time.Millisecond)
	}
	for retry := 0; retry < 4; retry++ {
		err = executor.markDispatchBarrierReturned(actionID, attemptID, preserveClaim)
		if err == nil {
			break
		}
		if executor.dispatchBarrierResolvedAndForget(actionID, attemptID) {
			return nil
		}
		if !errors.Is(err, ErrLeaseLost) || retry == 3 {
			return err
		}
		time.Sleep(time.Millisecond)
	}
	if err != nil {
		if executor.dispatchBarrierResolvedAndForget(actionID, attemptID) {
			return nil
		}
		return err
	}
	// The action-level returned marker is a second durable recovery path. Keep
	// the exact dispatch attempt running when its earlier evidence write was
	// unavailable; a later executor can discover this marker and retry the
	// evidence/barrier transition after the journal recovers.
	if intentErr != nil {
		if executor.dispatchBarrierResolvedAndForget(actionID, attemptID) {
			return nil
		}
		return intentErr
	}
	journalCtx, cancel := executor.journalContext(context.Background())
	defer cancel()
	for retry := 0; retry < 4; retry++ {
		err = executor.withTransaction(journalCtx, func(transactionCtx context.Context, journal Journal) error {
			attempts, err := journal.ListAttempts(transactionCtx, actionID)
			if err != nil {
				return err
			}
			for _, attempt := range attempts {
				if attempt.ID != attemptID || attempt.Phase != AttemptDispatch || attempt.State != domain.AttemptRunning {
					continue
				}
				effects, err := journal.ListEffects(transactionCtx, actionID)
				if err != nil {
					return err
				}
				for _, effect := range effects {
					if effect.AttemptID != attemptID {
						continue
					}
					effect.State = EffectUnknown
					effect.ObservedAt = formatTime(nowUTC(executor.options))
					if _, err := journal.UpdateEffect(transactionCtx, effect); err != nil {
						return err
					}
				}
				attempt.State = domain.AttemptReconciling
				attempt.OutcomeCertainty = CertaintyUncertain
				attempt.ErrorCode = string(FailureUncertain)
				attempt.ErrorDetail = "dispatch handler returned after ownership boundary"
				attempt.Evidence = evidenceJSON([]string{"dispatch_handler_returned"})
				attempt.FinishedAt = ""
				_, err = journal.UpdateAttempt(transactionCtx, attempt)
				return err
			}
			return nil
		})
		if err == nil {
			forgetDispatchBarrierFallback(actionID, attemptID)
			return nil
		}
		if executor.dispatchBarrierResolvedAndForget(actionID, attemptID) {
			return nil
		}
		if !errors.Is(err, ErrLeaseLost) || retry == 3 {
			break
		}
		time.Sleep(time.Millisecond)
	}
	return err
}

// persistDispatchBarrierIntent writes durable evidence on the exact running
// dispatch attempt before the action-level returned marker. It intentionally
// does not require the old claim: the handler may have returned after that
// claim was recovered, and the attempt ID remains the only safe identity.
func (executor *Executor) persistDispatchBarrierIntent(actionID, attemptID string) error {
	return executor.withTransaction(context.Background(), func(transactionCtx context.Context, journal Journal) error {
		attempts, err := journal.ListAttempts(transactionCtx, actionID)
		if err != nil {
			return err
		}
		for _, attempt := range attempts {
			if attempt.ID != attemptID || attempt.Phase != AttemptDispatch || attempt.State != domain.AttemptRunning {
				continue
			}
			if intentAttemptID, hasIntent := dispatchBarrierRetryIntentAttempt(attempt.Evidence); hasIntent {
				if intentAttemptID == attempt.ID {
					return nil
				}
				return fmt.Errorf("%w: dispatch barrier intent belongs to %q", ErrInvalidJournal, intentAttemptID)
			}
			evidence, err := evidenceWithDispatchBarrierRetryIntent(attempt.Evidence, attempt.ID)
			if err != nil {
				return err
			}
			attempt.OutcomeCertainty = CertaintyUncertain
			attempt.ErrorCode = string(FailureUncertain)
			attempt.ErrorDetail = dispatchBarrierIntentDetail
			attempt.Evidence = evidence
			attempt.FinishedAt = ""
			_, err = journal.UpdateAttempt(transactionCtx, attempt)
			return err
		}
		return nil
	})
}

// dispatchBarrierResolved makes concurrent release attempts idempotent. A
// transaction conflict is safe to treat as success once another worker has
// already moved the exact dispatch attempt out of running; if it is still
// running, the caller retains the error and its retry path remains active.
func (executor *Executor) dispatchBarrierResolved(actionID, attemptID string) (bool, error) {
	journalCtx, cancel := executor.journalContext(context.Background())
	defer cancel()
	attempts, err := executor.journal.ListAttempts(journalCtx, actionID)
	if err != nil {
		return false, err
	}
	for _, attempt := range attempts {
		if attempt.ID == attemptID && attempt.Phase == AttemptDispatch && attempt.State == domain.AttemptRunning {
			return false, nil
		}
	}
	return true, nil
}

func (executor *Executor) dispatchBarrierResolvedAndForget(actionID, attemptID string) bool {
	resolved, err := executor.dispatchBarrierResolved(actionID, attemptID)
	if err != nil || !resolved {
		return false
	}
	forgetDispatchBarrierFallback(actionID, attemptID)
	return true
}

// dispatchBarrierIntentAttempt discovers a returned handler from the
// attempt-level intent when the action-level marker was unavailable. The
// intent carries the attempt ID as well as the row identity; mismatches are
// ignored so evidence cannot release a different generation's barrier.
func (executor *Executor) dispatchBarrierIntentAttempt(ctx context.Context, actionID string) (string, bool, error) {
	attempts, err := executor.journal.ListAttempts(ctx, actionID)
	if err != nil {
		return "", false, err
	}
	var candidate Attempt
	found := false
	for _, attempt := range attempts {
		if attempt.Phase != AttemptDispatch || attempt.State != domain.AttemptRunning {
			continue
		}
		intentAttemptID, hasIntent := dispatchBarrierRetryIntentAttempt(attempt.Evidence)
		if !hasIntent || intentAttemptID != attempt.ID {
			continue
		}
		if !found || attempt.AttemptNumber > candidate.AttemptNumber {
			candidate = attempt
			found = true
		}
	}
	if !found {
		return "", false, nil
	}
	return candidate.ID, true, nil
}

// dispatchBarrierFallbackAttempt discovers a returned handler from the
// process-local fallback retained after both durable marker writes failed. It
// still requires the exact attempt row to be a live dispatch barrier before
// returning an identity, so this fallback can never authorize a new dispatch
// or a later attempt generation.
func (executor *Executor) dispatchBarrierFallbackAttempt(ctx context.Context, actionID string) (string, bool, error) {
	pending := pendingDispatchBarrierFallbackAttempts(actionID)
	if len(pending) == 0 {
		return "", false, nil
	}
	attempts, err := executor.journal.ListAttempts(ctx, actionID)
	if err != nil {
		return "", false, err
	}
	var candidate Attempt
	found := false
	seen := make(map[string]struct{}, len(attempts))
	for _, attempt := range attempts {
		if _, retained := pending[attempt.ID]; !retained {
			continue
		}
		seen[attempt.ID] = struct{}{}
		if attempt.Phase != AttemptDispatch || attempt.State != domain.AttemptRunning {
			forgetDispatchBarrierFallback(actionID, attempt.ID)
			continue
		}
		if !found || attempt.AttemptNumber > candidate.AttemptNumber {
			candidate = attempt
			found = true
		}
	}
	// An attempt can disappear only after its barrier has been resolved or its
	// journal row has been repaired. Drop orphaned process-local entries so the
	// registry cannot retain stale operation IDs forever.
	for attemptID := range pending {
		if _, exists := seen[attemptID]; !exists {
			forgetDispatchBarrierFallback(actionID, attemptID)
		}
	}
	if !found {
		return "", false, nil
	}
	return candidate.ID, true, nil
}

// markDispatchBarrierReturned durably records the action-level evidence that
// permits polling workers to retry a failed barrier release. The marker is
// committed separately from the attempt/effect update so a transient latter
// failure remains retryable across Executor instances. A worker processing a
// durable intent may preserve its fresh claim while a stale/background path
// clears the claim after the handler return is known.
func (executor *Executor) markDispatchBarrierReturned(actionID, attemptID string, preserveClaim bool) error {
	return executor.withTransaction(context.Background(), func(transactionCtx context.Context, journal Journal) error {
		attempts, err := journal.ListAttempts(transactionCtx, actionID)
		if err != nil {
			return err
		}
		active := false
		for _, attempt := range attempts {
			if attempt.ID == attemptID && attempt.Phase == AttemptDispatch && attempt.State == domain.AttemptRunning {
				active = true
				break
			}
		}
		if !active {
			return nil
		}
		current, err := journal.GetAction(transactionCtx, actionID)
		if err != nil {
			return err
		}
		// A worker that just claimed durable return evidence must keep its
		// generation live while it reconciles the returned dispatch. The marker
		// may be retried by that worker (or by the background retry), but a
		// background path must still clear a stale/fresh claim after the handler
		// return is known.
		if returnedAttemptID, returned := dispatchBarrierReturnedAttempt(current.Outcome); returned && returnedAttemptID == attemptID {
			if current.State != domain.ActionRunning || (preserveClaim && current.CancellationRequestedAt == "") {
				return nil
			}
		}
		outcome, err := outcomeWithDispatchBarrierReturned(current.Outcome, attemptID)
		if err != nil {
			return err
		}
		state := current.State
		claimedBy := ""
		leaseUntil := ""
		if state == domain.ActionRunning {
			if preserveClaim && current.CancellationRequestedAt == "" {
				claimedBy = current.ClaimedBy
				leaseUntil = current.LeaseUntil
			} else {
				state = domain.ActionReconciling
			}
		}
		unresolved, err := journalUnresolvedCount(transactionCtx, journal, actionID, true)
		if err != nil {
			return err
		}
		_, err = journal.UpdateOutcome(transactionCtx, OutcomeUpdate{
			ID: current.ID, Version: current.Version, State: state,
			NextAttemptAt: formatTime(nowUTC(executor.options)), ClaimedBy: claimedBy, LeaseUntil: leaseUntil, Outcome: outcome,
			UnresolvedCount: unresolved, UpdatedAt: formatTime(nowUTC(executor.options)),
		})
		return err
	})
}

func (executor *Executor) processReconciliation(ctx context.Context, action Action, claimed bool) Result {
	result := Result{Action: action, State: action.State}
	handler, err := executor.handler(action.Kind)
	if err != nil {
		if claimed {
			result.Err = executor.holdOwned(ctx, action, err)
		} else {
			result.Err = executor.hold(ctx, action, err)
		}
		result.State = domain.ActionNeedsReview
		return result
	}
	returnedAttemptID, returned := dispatchBarrierReturnedAttempt(action.Outcome)
	if !returned {
		returnedAttemptID, returned, err = executor.dispatchBarrierIntentAttempt(ctx, action.ID)
		if err != nil {
			result.Err = err
			return result
		}
	}
	if !returned {
		returnedAttemptID, returned, err = executor.dispatchBarrierFallbackAttempt(ctx, action.ID)
		if err != nil {
			result.Err = err
			return result
		}
	}
	if returned {
		release := executor.releaseDispatchBarrier
		if claimed {
			release = func(actionID, attemptID string) error {
				return executor.releaseDispatchBarrierWithClaim(actionID, attemptID, true)
			}
		}
		if err := release(action.ID, returnedAttemptID); err != nil {
			executor.scheduleBarrierRetry(action.ID, returnedAttemptID)
			result.Err = err
			return result
		}
		// A stale worker may commit the return marker while another worker is
		// claiming the action. Refresh before deciding whether this generation
		// still owns a valid reconciliation lease.
		latest, latestErr := executor.currentAction(ctx, action)
		if latestErr != nil {
			result.Err = latestErr
			return result
		}
		if claimed && (latest.State != domain.ActionRunning || latest.ClaimedBy != executor.options.WorkerID) {
			result.Action = latest
			result.State = latest.State
			return result
		}
		if claimed && latest.Version != action.Version {
			if state := claimStateFromContext(ctx); state == nil || !state.advanceAfterOutcome(latest) {
				result.Action = latest
				result.State = latest.State
				result.Err = ErrLeaseLost
				return result
			}
		}
		action = latest
		result.Action = latest
		result.State = latest.State
	}
	activeDispatch, active, err := executor.activeDispatchAttempt(ctx, action.ID)
	if err != nil {
		result.Err = err
		return result
	}
	if active {
		return executor.deferActiveDispatch(ctx, action, activeDispatch, claimed)
	}
	var attempt Attempt
	if claimed {
		attempt, err = executor.reconciliationAttemptOwned(ctx, action)
	} else {
		attempt, err = executor.reconciliationAttempt(ctx, action)
	}
	if err != nil {
		result.Err = err
		return result
	}
	result.AttemptID = attempt.ID
	reconcileCtx, cancelReconcile := executor.handlerContext(ctx, action)
	var stopWatcher func()
	if claimed {
		unregister := executor.registerActiveHandler(action.ID, cancelReconcile)
		defer unregister()
		stopWatcher = executor.watchHandlerState(action, cancelReconcile)
		defer stopWatcher()
	}
	defer cancelReconcile()
	reconciled, reconcileErr := handler.Reconcile(reconcileCtx, action, attempt)
	if reconcileErr != nil {
		return executor.finishReconciliationError(ctx, result, attempt, reconcileErr, claimed)
	}
	if err := reconciled.validate(action.ID); err != nil {
		return executor.finishReconciliationError(ctx, result, attempt, NewFailure(FailureInvalid, err), claimed)
	}
	if reconciled.Outcome.Valid() {
		finished := finishAttempt(attempt, domain.AttemptSucceeded, CertaintyKnown, nil, executor.options)
		finished.Evidence = evidenceJSON(reconciled.Evidence)
		if _, err := executor.updateAttemptFor(ctx, action, finished, claimed); err != nil {
			result.Err = err
			return result
		}
		if err := executor.recordEffectsFor(ctx, action, finished, reconciled.Effects, effectForOutcome(reconciled.Outcome), claimed); err != nil {
			return executor.finishReconciliationError(ctx, result, attempt, NewFailure(FailureInvalid, err), claimed)
		}
		var updated Action
		var err error
		if claimed {
			updated, _, _, err = executor.transitionDispatchOwned(ctx, action, finished, reconciled.Outcome)
		} else {
			latest, latestErr := executor.currentAction(ctx, action)
			if latestErr != nil {
				result.Err = latestErr
				return result
			}
			state, _ := executor.cancelledOrDeadline(latest)
			if state == "" {
				state = domain.ActionSucceeded
			}
			updated, err = executor.transition(ctx, latest, finished, state, reconciled.Outcome, 0, "reconciled")
		}
		result.Action = updated
		result.State = updated.State
		result.Outcome = reconciled.Outcome
		result.Err = err
		return result
	}
	if reconciled.SafeToRetry {
		finished := finishAttempt(attempt, domain.AttemptSucceeded, CertaintyKnown, nil, executor.options)
		finished.Evidence = evidenceJSON(reconciled.Evidence)
		if _, err := executor.updateAttemptFor(ctx, action, finished, claimed); err != nil {
			result.Err = err
			return result
		}
		if err := executor.recordEffectsFor(ctx, action, finished, reconciled.Effects, EffectPending, claimed); err != nil {
			return executor.finishReconciliationError(ctx, result, attempt, NewFailure(FailureInvalid, err), claimed)
		}
		var updated Action
		var err error
		if claimed {
			updated, err = executor.requeueOwned(ctx, action, finished, "reconciliation_proved_no_effect")
		} else {
			latest, latestErr := executor.currentAction(ctx, action)
			if latestErr != nil {
				result.Err = latestErr
				return result
			}
			if state, _ := executor.cancelledOrDeadline(latest); state != "" {
				updated, err = executor.transition(ctx, latest, finished, state, "", 0, "reconciliation_cancelled")
			} else {
				updated, err = executor.requeue(ctx, latest, finished, "reconciliation_proved_no_effect")
			}
		}
		result.Action = updated
		result.State = updated.State
		result.Err = err
		return result
	}

	finished := attempt
	finished.State = domain.AttemptReconciling
	finished.OutcomeCertainty = CertaintyUncertain
	finished.Evidence = evidenceJSON(reconciled.Evidence)
	finished.FinishedAt = ""
	if _, err := executor.updateAttemptFor(ctx, action, finished, claimed); err != nil {
		result.Err = err
		return result
	}
	if err := executor.recordEffectsFor(ctx, action, finished, reconciled.Effects, EffectUnknown, claimed); err != nil {
		return executor.finishReconciliationError(ctx, result, attempt, NewFailure(FailureInvalid, err), claimed)
	}
	var updated Action
	if claimed {
		updated, err = executor.transitionOwnedAt(ctx, action, finished, domain.ActionReconciling, "", 1, "reconciliation_unresolved", executor.retryAt(attempt.AttemptNumber))
	} else {
		latest, latestErr := executor.currentAction(ctx, action)
		if latestErr != nil {
			result.Err = latestErr
			return result
		}
		updated, err = executor.transitionAt(ctx, latest, finished, domain.ActionReconciling, "", 1, "reconciliation_unresolved", executor.retryAt(attempt.AttemptNumber))
	}
	result.Action = updated
	result.State = updated.State
	result.Err = err
	return result
}

func (executor *Executor) finishDispatchError(ctx context.Context, result Result, attempt Attempt, dispatchResult DispatchResult, dispatchErr error) Result {
	kind := failureKind(dispatchErr)
	uncertain := failureWasDispatched(dispatchErr) || kind == FailureUncertain || dispatchResultHasEvidence(dispatchResult)
	if uncertain {
		finished := attempt
		finished.State = domain.AttemptReconciling
		finished.OutcomeCertainty = CertaintyUncertain
		finished.ErrorCode = string(FailureUncertain)
		finished.ErrorDetail = safeDetail(dispatchErr)
		// A later failure can be reported with an empty DispatchResult (for
		// example, a read-back timeout after the upstream accepted the command).
		// Preserve an already-known command identity so reconciliation can still
		// correlate the uncertain operation after a restart.
		if strings.TrimSpace(dispatchResult.ExternalID) != "" {
			finished.ExternalID = dispatchResult.ExternalID
		}
		finished.Evidence = evidenceJSON(dispatchErrorEvidence(dispatchResult))
		if _, err := executor.updateAttemptOwned(ctx, result.Action, finished); err != nil {
			result.Err = err
			return result
		}
		var recordErr error
		if len(dispatchResult.Effects) > 0 {
			recordErr = executor.recordDispatchErrorEffectsOwned(ctx, result.Action, finished, dispatchResult.Effects)
		} else {
			recordErr = executor.recordUnknownEffectsOwned(ctx, result.Action, finished, EffectUnknown)
		}
		if recordErr != nil {
			// A malformed partial report is untrusted. Preserve the durable
			// uncertainty by falling back to the existing all-unknown path; a
			// journal/lease failure still surfaces and prevents a transition.
			if len(dispatchResult.Effects) == 0 {
				result.Err = recordErr
				return result
			}
			if fallbackErr := executor.recordUnknownEffectsOwned(ctx, result.Action, finished, EffectUnknown); fallbackErr != nil {
				result.Err = fallbackErr
				return result
			}
		}
		updated, _, err := executor.transitionUncertainOwned(ctx, result.Action, finished, "dispatch_uncertain", executor.retryAt(attempt.AttemptNumber))
		result.Action = updated
		result.State = updated.State
		result.Err = err
		return result
	}
	finished := finishAttempt(attempt, domain.AttemptFailed, CertaintyNotDispatched, dispatchErr, executor.options)
	if _, err := executor.updateAttemptOwned(ctx, result.Action, finished); err != nil {
		result.Err = err
		return result
	}
	if kind == FailureDependency {
		result.Err = executor.waitOwned(ctx, result.Action, dispatchErr)
		result.State = domain.ActionWaitingDependency
		return result
	}
	updated, err := executor.transitionOwned(ctx, result.Action, finished, domain.ActionFailed, "", 0, string(kind))
	result.Action = updated
	result.State = updated.State
	result.Err = err
	return result
}

func dispatchResultHasEvidence(result DispatchResult) bool {
	return strings.TrimSpace(result.ExternalID) != "" || result.Accepted || result.Outcome.Valid() || len(result.Evidence) > 0 || len(result.Effects) > 0
}

func dispatchResultIdentity(result DispatchResult) DispatchResult {
	return DispatchResult{ExternalID: result.ExternalID}
}

func dispatchErrorEvidence(result DispatchResult) []string {
	evidence := []string{"dispatch_result_uncertain"}
	evidence = append(evidence, result.Evidence...)
	if result.Accepted {
		evidence = append(evidence, "dispatch_result_accepted")
	}
	if result.Outcome.Valid() {
		evidence = append(evidence, "dispatch_result_outcome="+string(result.Outcome))
	}
	if len(result.Effects) > 0 {
		evidence = append(evidence, fmt.Sprintf("dispatch_effect_count=%d", len(result.Effects)))
	}
	return evidence
}

func (executor *Executor) finishReconciliationError(ctx context.Context, result Result, attempt Attempt, reconcileErr error, claimed bool) Result {
	kind := failureKind(reconcileErr)
	if kind == FailureDependency || kind == FailureUncertain {
		finished := attempt
		finished.State = domain.AttemptReconciling
		finished.OutcomeCertainty = CertaintyUncertain
		finished.ErrorCode = string(kind)
		finished.ErrorDetail = safeDetail(reconcileErr)
		if _, err := executor.updateAttemptFor(ctx, result.Action, finished, claimed); err != nil {
			result.Err = err
			return result
		}
		var updated Action
		var err error
		if claimed {
			updated, _, err = executor.transitionUncertainOwned(ctx, result.Action, finished, string(kind), executor.retryAt(attempt.AttemptNumber))
		} else {
			latest, latestErr := executor.currentAction(ctx, result.Action)
			if latestErr != nil {
				result.Err = latestErr
				return result
			}
			updated, err = executor.transitionAt(ctx, latest, finished, domain.ActionReconciling, "", 1, string(kind), executor.retryAt(attempt.AttemptNumber))
		}
		result.Action = updated
		result.State = updated.State
		result.Err = err
		return result
	}
	finished := finishAttempt(attempt, domain.AttemptFailed, CertaintyKnown, reconcileErr, executor.options)
	if _, err := executor.updateAttemptFor(ctx, result.Action, finished, claimed); err != nil {
		result.Err = err
		return result
	}
	var updated Action
	var err error
	if claimed {
		updated, err = executor.transitionOwned(ctx, result.Action, finished, domain.ActionNeedsReview, "", 0, string(kind))
	} else {
		latest, latestErr := executor.currentAction(ctx, result.Action)
		if latestErr != nil {
			result.Err = latestErr
			return result
		}
		state := domain.ActionNeedsReview
		if stopState, _ := executor.cancelledOrDeadline(latest); stopState != "" {
			state = stopState
		}
		updated, err = executor.transition(ctx, latest, finished, state, "", 0, string(kind))
	}
	result.Action = updated
	result.State = updated.State
	result.Err = err
	return result
}

func (executor *Executor) nextAttempt(ctx context.Context, action Action, phase AttemptPhase, certainty OutcomeCertainty) (Attempt, error) {
	return executor.nextAttemptInJournal(ctx, executor.journal, action, phase, certainty)
}

func (executor *Executor) nextAttemptInJournal(ctx context.Context, journal Journal, action Action, phase AttemptPhase, certainty OutcomeCertainty) (Attempt, error) {
	attempts, err := journal.ListAttempts(ctx, action.ID)
	if err != nil {
		return Attempt{}, err
	}
	var number int64
	for _, existing := range attempts {
		if existing.AttemptNumber > number {
			number = existing.AttemptNumber
		}
	}
	number++
	attempt := Attempt{
		ID:               executor.options.AttemptID(action.ID, number, phase),
		ActionRunID:      action.ID,
		AttemptNumber:    number,
		Phase:            phase,
		State:            domain.AttemptRunning,
		StartedAt:        formatTime(nowUTC(executor.options)),
		OutcomeCertainty: certainty,
		Evidence:         json.RawMessage(`{}`),
	}
	if err := attempt.validate(); err != nil {
		return Attempt{}, err
	}
	return attempt, nil
}

func (executor *Executor) newAttempt(ctx context.Context, action Action, phase AttemptPhase, certainty OutcomeCertainty) (Attempt, error) {
	attempt, err := executor.nextAttempt(ctx, action, phase, certainty)
	if err != nil {
		return Attempt{}, err
	}
	return executor.journal.CreateAttempt(ctx, attempt)
}

func (executor *Executor) newAttemptOwned(ctx context.Context, action Action, phase AttemptPhase, certainty OutcomeCertainty) (Attempt, error) {
	return executor.newAttemptOwnedWithExternalID(ctx, action, phase, certainty, "")
}

func (executor *Executor) newAttemptOwnedWithExternalID(ctx context.Context, action Action, phase AttemptPhase, certainty OutcomeCertainty, externalID string) (Attempt, error) {
	var created Attempt
	err := executor.withClaimedTransaction(ctx, action, func(transactionCtx context.Context, journal Journal) error {
		attempt, err := executor.nextAttemptInJournal(transactionCtx, journal, action, phase, certainty)
		if err != nil {
			return err
		}
		attempt.ExternalID = externalID
		if err := attempt.validate(); err != nil {
			return err
		}
		created, err = journal.CreateAttempt(transactionCtx, attempt)
		return err
	})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Attempt{}, ErrLeaseLost
		}
		return Attempt{}, err
	}
	return created, nil
}

func (executor *Executor) newAttemptWithExternalID(ctx context.Context, action Action, phase AttemptPhase, certainty OutcomeCertainty, externalID string) (Attempt, error) {
	attempt, err := executor.nextAttempt(ctx, action, phase, certainty)
	if err != nil {
		return Attempt{}, err
	}
	attempt.ExternalID = externalID
	if err := attempt.validate(); err != nil {
		return Attempt{}, err
	}
	return executor.journal.CreateAttempt(ctx, attempt)
}

func (executor *Executor) reconciliationAttempt(ctx context.Context, action Action) (Attempt, error) {
	attempts, err := executor.journal.ListAttempts(ctx, action.ID)
	if err != nil {
		return Attempt{}, err
	}
	externalID := latestDispatchExternalID(attempts)
	if len(attempts) > 0 {
		latest := attempts[len(attempts)-1]
		for _, candidate := range attempts {
			if candidate.AttemptNumber > latest.AttemptNumber {
				latest = candidate
			}
		}
		if latest.Phase == AttemptReconcile && latest.State == domain.AttemptReconciling {
			return executor.bindReconciliationExternalID(ctx, action, latest, externalID, false)
		}
	}
	return executor.newAttemptWithExternalID(ctx, action, AttemptReconcile, CertaintyUncertain, externalID)
}

func (executor *Executor) reconciliationAttemptOwned(ctx context.Context, action Action) (Attempt, error) {
	attempts, err := executor.journal.ListAttempts(ctx, action.ID)
	if err != nil {
		return Attempt{}, err
	}
	externalID := latestDispatchExternalID(attempts)
	for _, candidate := range attempts {
		if candidate.Phase == AttemptReconcile && candidate.State == domain.AttemptReconciling {
			return executor.bindReconciliationExternalID(ctx, action, candidate, externalID, true)
		}
	}
	return executor.newAttemptOwnedWithExternalID(ctx, action, AttemptReconcile, CertaintyUncertain, externalID)
}

func latestDispatchExternalID(attempts []Attempt) string {
	var latest Attempt
	found := false
	for _, attempt := range attempts {
		if attempt.Phase != AttemptDispatch {
			continue
		}
		if !found || attempt.AttemptNumber > latest.AttemptNumber {
			latest = attempt
			found = true
		}
	}
	if !found {
		return ""
	}
	return latest.ExternalID
}

func (executor *Executor) bindReconciliationExternalID(ctx context.Context, action Action, attempt Attempt, externalID string, claimed bool) (Attempt, error) {
	if strings.TrimSpace(externalID) == "" || attempt.ExternalID == externalID {
		return attempt, nil
	}
	if strings.TrimSpace(attempt.ExternalID) != "" {
		return Attempt{}, fmt.Errorf("%w: reconciliation external ID conflicts with dispatch identity", ErrInvalidJournal)
	}
	attempt.ExternalID = externalID
	if claimed {
		return executor.updateAttemptOwned(ctx, action, attempt)
	}
	return executor.journal.UpdateAttempt(ctx, attempt)
}

func (executor *Executor) updateAttemptOwned(ctx context.Context, action Action, attempt Attempt) (Attempt, error) {
	var updated Attempt
	err := executor.withClaimedTransaction(ctx, action, func(transactionCtx context.Context, journal Journal) error {
		var err error
		updated, err = journal.UpdateAttempt(transactionCtx, attempt)
		return err
	})
	if errors.Is(err, sql.ErrNoRows) {
		return Attempt{}, ErrLeaseLost
	}
	return updated, err
}

func (executor *Executor) handlerContext(ctx context.Context, action Action) (context.Context, context.CancelFunc) {
	if ctx == nil {
		ctx = context.Background()
	}
	child, cancel := context.WithCancel(ctx)
	deadline, err := parseTime(action.DeadlineAt)
	if err == nil && !deadline.IsZero() {
		duration := deadline.Sub(nowUTC(executor.options))
		withTimeout, timeoutCancel := context.WithTimeout(child, duration)
		return withTimeout, func() {
			timeoutCancel()
			cancel()
		}
	}
	return child, cancel
}

func (executor *Executor) prepareDispatch(ctx context.Context, action Action, observed Attempt, effects []Effect) (Attempt, error) {
	// Create the dispatch intent and all pending effect rows in one short
	// transaction. No handler call occurs until this callback commits.
	var dispatch Attempt
	err := executor.withClaimedTransaction(ctx, action, func(transactionCtx context.Context, journal Journal) error {
		var err error
		dispatch, err = executor.nextAttemptInJournal(transactionCtx, journal, action, AttemptDispatch, CertaintyNotDispatched)
		if err != nil {
			return err
		}
		if _, err := journal.UpdateAttempt(transactionCtx, observed); err != nil {
			return err
		}
		if _, err := journal.CreateAttempt(transactionCtx, dispatch); err != nil {
			return err
		}
		existing, err := journal.ListEffects(transactionCtx, action.ID)
		if err != nil {
			return err
		}
		byOrdinal := make(map[int64]Effect, len(existing))
		for _, candidate := range existing {
			byOrdinal[candidate.Ordinal] = candidate
		}
		for ordinal, effect := range effects {
			if effect.Ordinal < 0 {
				effect.Ordinal = int64(ordinal)
			}
			effect.ID = executor.options.EffectID(action.ID, effect.Ordinal)
			effect.ActionRunID = action.ID
			effect.AttemptID = dispatch.ID
			// Observation is read-only evidence. Every target is pending until a
			// dispatch returns and its fresh read-back proves the effect.
			effect.State = EffectPending
			if effect.Evidence == nil {
				effect.Evidence = json.RawMessage(`{}`)
			}
			if effect.ObservedAt == "" {
				effect.ObservedAt = formatTime(nowUTC(executor.options))
			}
			if previous, ok := byOrdinal[effect.Ordinal]; ok {
				if previous.TargetKind != effect.TargetKind || previous.TargetID != effect.TargetID || previous.EffectKind != effect.EffectKind {
					return fmt.Errorf("%w: effect ordinal %d changed target across attempts", ErrInvalidJournal, effect.Ordinal)
				}
				effect.ID = previous.ID
				if _, err := journal.UpdateEffect(transactionCtx, effect); err != nil {
					return err
				}
				continue
			}
			if _, err := journal.CreateEffect(transactionCtx, effect); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Attempt{}, ErrLeaseLost
		}
		return Attempt{}, err
	}
	return dispatch, nil
}

func (executor *Executor) beforeDispatch(ctx context.Context, action Action) error {
	if err := ctx.Err(); err != nil {
		return NewFailure(FailureCancelled, err)
	}
	current, err := executor.journal.GetAction(ctx, action.ID)
	if err != nil {
		return err
	}
	now := nowUTC(executor.options)
	// Cancellation and deadline are durable stop markers. Check them before
	// the version fence so a marker committed by another caller is surfaced as
	// a terminal pre-dispatch decision rather than an opaque lease conflict.
	if current.CancellationRequestedAt != "" {
		return NewFailure(FailureCancelled, errors.New("cancellation requested before dispatch"))
	}
	if expired(current.DeadlineAt, now) {
		return NewFailure(FailureConflict, errors.New("action deadline exceeded before dispatch"))
	}
	if current.Version != action.Version || current.ClaimedBy != executor.options.WorkerID || current.LeaseUntil == "" {
		return ErrLeaseLost
	}
	lease, err := parseTime(current.LeaseUntil)
	if err != nil || !lease.After(now) {
		return ErrLeaseLost
	}
	return nil
}

func finishAttempt(attempt Attempt, state domain.AttemptState, certainty OutcomeCertainty, err error, options Options) Attempt {
	attempt.State = state
	attempt.OutcomeCertainty = certainty
	if state.Terminal() {
		attempt.FinishedAt = formatTime(nowUTC(options))
	}
	if err != nil {
		attempt.ErrorCode = string(failureKind(err))
		attempt.ErrorDetail = safeDetail(err)
	}
	if attempt.Evidence == nil {
		attempt.Evidence = json.RawMessage(`{}`)
	}
	return attempt
}

func safeDetail(err error) string {
	if err == nil {
		return ""
	}
	detail := strings.TrimSpace(err.Error())
	if len(detail) > 512 {
		detail = detail[:512]
	}
	return detail
}

func evidenceJSON(values []string) json.RawMessage {
	filtered := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if len(value) > 256 {
			value = value[:256]
		}
		filtered = append(filtered, value)
	}
	payload, err := json.Marshal(map[string]any{"evidence": filtered})
	if err != nil {
		return json.RawMessage(`{"evidence":[]}`)
	}
	return payload
}

// validateEffectSet requires a read-back to describe exactly the approved
// targets. Ordinal and the full target/effect identity are checked together;
// an empty or partial report is therefore never promoted to applied.
func validateEffectSet(expected, reported []Effect, actionID string) error {
	normalizeReportedEffects(expected, actionID)
	normalizeReportedEffects(reported, actionID)
	if err := validateEffects(expected, actionID); err != nil {
		return fmt.Errorf("approved effects: %w", err)
	}
	if err := validateEffects(reported, actionID); err != nil {
		return fmt.Errorf("reported effects: %w", err)
	}
	if len(expected) != len(reported) {
		return fmt.Errorf("%w: effect set has %d reported targets, want %d", ErrInvalidJournal, len(reported), len(expected))
	}
	byIdentity := make(map[string]Effect, len(expected))
	for _, effect := range expected {
		byIdentity[effectIdentity(effect)] = effect
	}
	for _, effect := range reported {
		approved, ok := byIdentity[effectIdentity(effect)]
		if !ok {
			return fmt.Errorf("%w: unapproved effect target %s", ErrInvalidJournal, effectIdentity(effect))
		}
		if approved.Ordinal != effect.Ordinal {
			return fmt.Errorf("%w: effect target %s changed ordinal", ErrInvalidJournal, effectIdentity(effect))
		}
	}
	return nil
}

func effectSetMismatch(expected, reported []Effect, actionID string) bool {
	return validateEffectSet(expected, reported, actionID) != nil
}

func effectForOutcome(outcome domain.EffectOutcome) EffectState {
	if outcome == domain.OutcomeAlreadySatisfied {
		return EffectAlreadySatisfied
	}
	if outcome == domain.OutcomeApplied {
		return EffectApplied
	}
	return EffectUnknown
}

func (executor *Executor) recordEffects(ctx context.Context, action Action, attempt Attempt, reported []Effect, defaultState EffectState) error {
	return executor.withTransaction(ctx, func(transactionCtx context.Context, journal Journal) error {
		return executor.recordEffectsInJournal(transactionCtx, journal, action, attempt, reported, defaultState, true)
	})
}

func (executor *Executor) recordEffectsOwned(ctx context.Context, action Action, attempt Attempt, reported []Effect, defaultState EffectState) error {
	return executor.withClaimedTransaction(ctx, action, func(transactionCtx context.Context, journal Journal) error {
		return executor.recordEffectsInJournal(transactionCtx, journal, action, attempt, reported, defaultState, true)
	})
}

func (executor *Executor) recordUnknownEffectsOwned(ctx context.Context, action Action, attempt Attempt, defaultState EffectState) error {
	return executor.withClaimedTransaction(ctx, action, func(transactionCtx context.Context, journal Journal) error {
		return executor.recordEffectsInJournal(transactionCtx, journal, action, attempt, nil, defaultState, false)
	})
}

// recordDispatchErrorEffectsOwned persists the subset an errored Dispatch
// returned before the executor publishes the uncertain transition. Returned
// effects are matched to the already-planned identities; every omitted target
// becomes unknown in the same fenced transaction. This keeps partial material
// and handler evidence durable without ever treating an errored report as a
// safe retry.
func (executor *Executor) recordDispatchErrorEffectsOwned(ctx context.Context, action Action, attempt Attempt, reported []Effect) error {
	return executor.withClaimedTransaction(ctx, action, func(transactionCtx context.Context, journal Journal) error {
		return executor.recordDispatchErrorEffectsInJournal(transactionCtx, journal, action, attempt, reported)
	})
}

func (executor *Executor) recordDispatchErrorEffectsInJournal(ctx context.Context, journal Journal, action Action, attempt Attempt, reported []Effect) error {
	existing, err := journal.ListEffects(ctx, action.ID)
	if err != nil {
		return err
	}
	if len(existing) == 0 {
		return fmt.Errorf("%w: errored dispatch has no planned effects", ErrInvalidJournal)
	}

	normalized := make([]Effect, len(reported))
	copy(normalized, reported)
	normalizeReportedEffects(normalized, action.ID)
	if err := validateEffects(normalized, action.ID); err != nil {
		return fmt.Errorf("errored dispatch effects: %w", err)
	}
	byIdentity := make(map[string]Effect, len(existing))
	for _, candidate := range existing {
		byIdentity[effectIdentity(candidate)] = candidate
	}
	byIdentityReported := make(map[string]Effect, len(normalized))
	for _, effect := range normalized {
		identity := effectIdentity(effect)
		if _, duplicate := byIdentityReported[identity]; duplicate {
			return fmt.Errorf("%w: duplicate errored dispatch effect %s", ErrInvalidJournal, identity)
		}
		approved, ok := byIdentity[identity]
		if !ok {
			return fmt.Errorf("%w: unapproved errored dispatch effect %s", ErrInvalidJournal, identity)
		}
		if approved.Ordinal != effect.Ordinal {
			return fmt.Errorf("%w: errored dispatch effect %s changed ordinal", ErrInvalidJournal, identity)
		}
		byIdentityReported[identity] = effect
	}

	for _, existingEffect := range existing {
		effect := existingEffect
		if returned, ok := byIdentityReported[effectIdentity(existingEffect)]; ok {
			effect = returned
			effect.ID = existingEffect.ID
			effect.ActionRunID = action.ID
			effect.AttemptID = attempt.ID
			if len(effect.Evidence) == 0 || string(effect.Evidence) == `{}` {
				effect.Evidence = append(json.RawMessage(nil), existingEffect.Evidence...)
			}
			if effect.ObservedAt == "" {
				effect.ObservedAt = formatTime(nowUTC(executor.options))
			}
		} else {
			effect.AttemptID = attempt.ID
			effect.State = EffectUnknown
			effect.ObservedAt = formatTime(nowUTC(executor.options))
			effect.Evidence = appendEvidence(effect.Evidence, "dispatch_result_unreported")
		}
		if _, err := journal.UpdateEffect(ctx, effect); err != nil {
			return err
		}
	}
	return nil
}

func appendEvidence(raw json.RawMessage, values ...string) json.RawMessage {
	if len(values) == 0 {
		return append(json.RawMessage(nil), raw...)
	}
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) > 0 && trimmed[0] == '[' {
		// Decode array members from raw JSON first. encoding/json replaces
		// invalid UTF-8 and unpaired surrogate escapes while decoding into
		// []string, which would destroy opaque upstream evidence. Keep the
		// compact array form only when every member is losslessly decodable;
		// otherwise the valid-array fallback below retains the exact bytes in
		// _mastarr_prior.
		if existing, ok := decodeExecutionMarkers(trimmed); ok {
			return evidenceJSON(append(existing, values...))
		}
	}
	// Effect evidence is intentionally opaque. Preserve an object's fields
	// while adding a namespaced execution marker, rather than replacing the
	// handler's fields with the executor's string-list envelope. If the marker
	// field already exists, merge it through the decoded object so repeated
	// annotations cannot emit duplicate JSON keys.
	if len(trimmed) >= 2 && trimmed[0] == '{' && trimmed[len(trimmed)-1] == '}' && json.Valid(trimmed) {
		if result, ok := mergeObjectEvidenceMarkers(trimmed, values); ok {
			return result
		}
	}
	// Non-object valid evidence still needs to survive annotation. Keep its
	// exact bytes inside a durable envelope with a separate marker list.
	if len(trimmed) > 0 && json.Valid(trimmed) {
		marker, err := json.Marshal(values)
		if err == nil {
			result := make([]byte, 0, len(trimmed)+len(marker)+32)
			result = append(result, `{"_mastarr_prior":`...)
			result = append(result, trimmed...)
			result = append(result, `,"_mastarr_execution_markers":`...)
			result = append(result, marker...)
			result = append(result, '}')
			return json.RawMessage(result)
		}
	}
	return evidenceJSON(values)
}

func mergeObjectEvidenceMarkers(raw json.RawMessage, values []string) (json.RawMessage, bool) {
	if !jsonObjectHasUniqueKeys(raw) {
		return nil, false
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil || object == nil {
		return nil, false
	}
	markers := make([]string, 0, len(values))
	if existing, ok := object["_mastarr_execution_markers"]; ok {
		decoded, ok := decodeExecutionMarkers(existing)
		if !ok {
			return nil, false
		}
		markers = decoded
	}
	for _, value := range values {
		if value == "" {
			continue
		}
		seen := false
		for _, marker := range markers {
			if marker == value {
				seen = true
				break
			}
		}
		if !seen {
			markers = append(markers, value)
		}
	}
	encoded, err := json.Marshal(markers)
	if err != nil {
		return nil, false
	}
	object["_mastarr_execution_markers"] = encoded
	encoded, err = json.Marshal(object)
	if err != nil {
		return nil, false
	}
	return json.RawMessage(encoded), true
}

// jsonObjectHasUniqueKeys checks the object syntax without decoding it into a
// map. Evidence is opaque: map decoding would silently discard the first of
// two keys with the same name. Such an object must use appendEvidence's raw
// prior-value envelope instead of the field-merging path.
func jsonObjectHasUniqueKeys(raw json.RawMessage) bool {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	token, err := decoder.Token()
	if err != nil {
		return false
	}
	opening, ok := token.(json.Delim)
	if !ok || opening != '{' {
		return false
	}
	seen := make(map[string]struct{})
	for decoder.More() {
		token, err := decoder.Token()
		key, ok := token.(string)
		if err != nil || !ok {
			return false
		}
		if _, duplicate := seen[key]; duplicate {
			return false
		}
		seen[key] = struct{}{}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return false
		}
	}
	closing, err := decoder.Token()
	if err != nil {
		return false
	}
	if delimiter, ok := closing.(json.Delim); !ok || delimiter != '}' {
		return false
	}
	var trailing json.RawMessage
	return decoder.Decode(&trailing) == io.EOF
}

func decodeExecutionMarkers(raw json.RawMessage) ([]string, bool) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || trimmed[0] != '[' {
		return nil, false
	}
	var encoded []json.RawMessage
	if err := json.Unmarshal(trimmed, &encoded); err != nil {
		return nil, false
	}
	markers := make([]string, 0, len(encoded))
	for _, value := range encoded {
		if !losslessJSONString(value) {
			return nil, false
		}
		var marker string
		if err := json.Unmarshal(value, &marker); err != nil {
			return nil, false
		}
		markers = append(markers, marker)
	}
	return markers, true
}

// losslessJSONString rejects JSON strings whose Go decoder would rewrite the
// source bytes. encoding/json accepts invalid UTF-8 and unpaired UTF-16
// surrogates by replacing them with U+FFFD; opaque evidence must instead take
// appendEvidence's raw prior-value envelope.
func losslessJSONString(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) < 2 || trimmed[0] != '"' || trimmed[len(trimmed)-1] != '"' || !utf8.Valid(trimmed) {
		return false
	}
	var decoded string
	if err := json.Unmarshal(trimmed, &decoded); err != nil {
		return false
	}
	for index := 1; index < len(trimmed)-1; {
		if trimmed[index] != '\\' {
			index++
			continue
		}
		if index+1 >= len(trimmed)-1 {
			return false
		}
		if trimmed[index+1] != 'u' {
			index += 2
			continue
		}
		if index+6 > len(trimmed)-1 {
			return false
		}
		code, ok := jsonHexUint16(trimmed[index+2 : index+6])
		if !ok {
			return false
		}
		switch {
		case code >= 0xDC00 && code <= 0xDFFF:
			return false
		case code >= 0xD800 && code <= 0xDBFF:
			if index+12 > len(trimmed)-1 || trimmed[index+6] != '\\' || trimmed[index+7] != 'u' {
				return false
			}
			low, ok := jsonHexUint16(trimmed[index+8 : index+12])
			if !ok || low < 0xDC00 || low > 0xDFFF {
				return false
			}
			index += 12
		default:
			index += 6
		}
	}
	return true
}

func jsonHexUint16(raw []byte) (uint16, bool) {
	if len(raw) != 4 {
		return 0, false
	}
	var value uint16
	for _, digit := range raw {
		value <<= 4
		switch {
		case digit >= '0' && digit <= '9':
			value += uint16(digit - '0')
		case digit >= 'a' && digit <= 'f':
			value += uint16(digit-'a') + 10
		case digit >= 'A' && digit <= 'F':
			value += uint16(digit-'A') + 10
		default:
			return 0, false
		}
	}
	return value, true
}

func (executor *Executor) recordEffectsFor(ctx context.Context, action Action, attempt Attempt, reported []Effect, defaultState EffectState, claimed bool) error {
	if claimed {
		return executor.recordEffectsOwned(ctx, action, attempt, reported, defaultState)
	}
	return executor.recordEffects(ctx, action, attempt, reported, defaultState)
}

func (executor *Executor) recordEffectsInJournal(ctx context.Context, journal Journal, action Action, attempt Attempt, reported []Effect, defaultState EffectState, requireComplete bool) error {
	existing, err := journal.ListEffects(ctx, action.ID)
	if err != nil {
		return err
	}
	if !requireComplete && len(existing) == 0 {
		return fmt.Errorf("%w: uncertain dispatch has no planned effects", ErrInvalidJournal)
	}
	if !requireComplete && len(reported) == 0 {
		// The dispatch result is uncertain, so every already-planned target must
		// move out of pending even when the handler could not return a report.
		// Reuse the immutable journal identities and attach them to this
		// uncertain attempt; no applied evidence is synthesized.
		reported = make([]Effect, 0, len(existing))
		for _, effect := range existing {
			effect.ID = ""
			effect.AttemptID = attempt.ID
			effect.State = defaultState
			effect.ObservedAt = formatTime(nowUTC(executor.options))
			reported = append(reported, effect)
		}
	}
	if len(existing) > 0 && (requireComplete || len(reported) > 0) {
		if err := validateEffectSet(existing, reported, action.ID); err != nil {
			return err
		}
	}
	if err := validateEffectStates(reported, defaultState); err != nil {
		return err
	}
	byOrdinal := make(map[int64]Effect, len(existing))
	for _, candidate := range existing {
		byOrdinal[candidate.Ordinal] = candidate
	}
	for index, effect := range reported {
		if effect.Ordinal < 0 {
			effect.Ordinal = int64(index)
		}
		if existingEffect, ok := byOrdinal[effect.Ordinal]; ok {
			effect.ID = existingEffect.ID
			effect.ActionRunID = action.ID
			effect.AttemptID = attempt.ID
			if defaultState == EffectUnknown {
				effect.State = defaultState
			} else if effect.State == "" || effect.State == EffectPending {
				effect.State = defaultState
			}
			if effect.Evidence == nil {
				effect.Evidence = existingEffect.Evidence
			}
			if effect.ObservedAt == "" {
				effect.ObservedAt = formatTime(nowUTC(executor.options))
			}
			if _, err := journal.UpdateEffect(ctx, effect); err != nil {
				return err
			}
			continue
		}
		effect.ID = executor.options.EffectID(action.ID, effect.Ordinal)
		effect.ActionRunID = action.ID
		effect.AttemptID = attempt.ID
		if defaultState == EffectUnknown {
			effect.State = defaultState
		} else if effect.State == "" || effect.State == EffectPending {
			effect.State = defaultState
		}
		if effect.Evidence == nil {
			effect.Evidence = json.RawMessage(`{}`)
		}
		if effect.ObservedAt == "" {
			effect.ObservedAt = formatTime(nowUTC(executor.options))
		}
		if _, err := journal.CreateEffect(ctx, effect); err != nil {
			return err
		}
	}
	return nil
}

func (executor *Executor) updateAttemptFor(ctx context.Context, action Action, attempt Attempt, claimed bool) (Attempt, error) {
	if claimed {
		return executor.updateAttemptOwned(ctx, action, attempt)
	}
	return executor.journal.UpdateAttempt(ctx, attempt)
}

func (executor *Executor) cancelledOrDeadline(action Action) (domain.ActionState, domain.EffectOutcome) {
	if action.CancellationRequestedAt != "" {
		return domain.ActionCancelled, ""
	}
	if expired(action.DeadlineAt, nowUTC(executor.options)) {
		return domain.ActionDeadlineExceeded, ""
	}
	return "", ""
}

func (executor *Executor) retryAt(attemptNumber int64) string {
	if attemptNumber <= 0 {
		attemptNumber = 1
	}
	return formatTime(nowUTC(executor.options).Add(executor.options.Retry.delay(attemptNumber)))
}

func (executor *Executor) currentAction(ctx context.Context, action Action) (Action, error) {
	latest, err := executor.journal.GetAction(ctx, action.ID)
	if err != nil {
		return Action{}, err
	}
	// Journal implementations derive Kind from the persisted plan. Preserve
	// the caller's immutable plan identity for journals that intentionally keep
	// their action rows transport-independent.
	latest.Kind = action.Kind
	latest.PlanID = action.PlanID
	latest.PlanRevision = action.PlanRevision
	latest.PlanDigest = action.PlanDigest
	return latest, nil
}

func outcomeJSON(outcome domain.EffectOutcome, reason string, unresolved int64) json.RawMessage {
	payload := map[string]any{"reason": reason}
	if outcome.Valid() {
		payload["outcome"] = outcome
	}
	if unresolved > 0 {
		payload["unresolvedEffects"] = unresolved
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return json.RawMessage(`{"reason":"journal_encoding_failed"}`)
	}
	return encoded
}

func unresolvedEffectCount(effects []Effect, includePending bool) int64 {
	var count int64
	for _, effect := range effects {
		switch effect.State {
		case EffectUnknown, EffectFailed, EffectCancelled:
			count++
		case EffectPending:
			if includePending {
				count++
			}
		}
	}
	return count
}

func journalUnresolvedCount(ctx context.Context, journal Journal, actionID string, includePending bool) (int64, error) {
	effects, err := journal.ListEffects(ctx, actionID)
	if err != nil {
		return 0, err
	}
	return unresolvedEffectCount(effects, includePending), nil
}

func (executor *Executor) transition(ctx context.Context, action Action, attempt Attempt, state domain.ActionState, outcome domain.EffectOutcome, unresolved int64, reason string) (Action, error) {
	return executor.transitionAt(ctx, action, attempt, state, outcome, unresolved, reason, "")
}

func (executor *Executor) transitionAt(ctx context.Context, action Action, attempt Attempt, state domain.ActionState, outcome domain.EffectOutcome, unresolved int64, reason, nextAttemptAt string) (Action, error) {
	var updated Action
	err := executor.withTransaction(ctx, func(transactionCtx context.Context, journal Journal) error {
		current, err := journal.GetAction(transactionCtx, action.ID)
		if err != nil {
			return err
		}
		if state == "" {
			state = domain.ActionNeedsReview
		}
		if err := domain.TransitionAction(current.State, state); err != nil && current.State != state {
			return err
		}
		unresolved, err := journalUnresolvedCount(transactionCtx, journal, action.ID, state == domain.ActionReconciling)
		if err != nil {
			return err
		}
		updated, err = journal.UpdateOutcome(transactionCtx, OutcomeUpdate{
			ID:              current.ID,
			Version:         current.Version,
			State:           state,
			NextAttemptAt:   nextAttemptAt,
			Outcome:         outcomeJSON(outcome, reason, unresolved),
			UnresolvedCount: unresolved,
			UpdatedAt:       formatTime(nowUTC(executor.options)),
		})
		return err
	})
	if err != nil {
		if isNoRows(err) {
			return action, ErrLeaseLost
		}
		return action, err
	}
	updated = preserveActionPlan(updated, action)
	return updated, nil
}

func preserveActionPlan(updated, source Action) Action {
	updated.Kind = source.Kind
	updated.PlanID = source.PlanID
	updated.PlanRevision = source.PlanRevision
	updated.PlanDigest = source.PlanDigest
	return updated
}

func (executor *Executor) transitionOwned(ctx context.Context, action Action, attempt Attempt, state domain.ActionState, outcome domain.EffectOutcome, unresolved int64, reason string) (Action, error) {
	return executor.transitionOwnedAt(ctx, action, attempt, state, outcome, unresolved, reason, "")
}

func (executor *Executor) transitionOwnedAt(ctx context.Context, action Action, attempt Attempt, state domain.ActionState, outcome domain.EffectOutcome, unresolved int64, reason, nextAttemptAt string) (Action, error) {
	var updated Action
	err := executor.withClaimedTransaction(ctx, action, func(transactionCtx context.Context, journal Journal) error {
		current, err := journal.GetAction(transactionCtx, action.ID)
		if err != nil {
			return err
		}
		if state == "" {
			state = domain.ActionNeedsReview
		}
		if err := domain.TransitionAction(current.State, state); err != nil && current.State != state {
			return err
		}
		unresolved, err := journalUnresolvedCount(transactionCtx, journal, action.ID, state == domain.ActionReconciling)
		if err != nil {
			return err
		}
		updated, err = journal.UpdateOutcome(transactionCtx, OutcomeUpdate{
			ID:              current.ID,
			Version:         current.Version,
			State:           state,
			NextAttemptAt:   nextAttemptAt,
			Outcome:         outcomeJSON(outcome, reason, unresolved),
			UnresolvedCount: unresolved,
			UpdatedAt:       formatTime(nowUTC(executor.options)),
		})
		return err
	})
	if err != nil {
		if isNoRows(err) {
			return action, ErrLeaseLost
		}
		return action, err
	}
	return preserveActionPlan(updated, action), nil
}

func (executor *Executor) transitionStopOwned(ctx context.Context, action Action, attempt Attempt, reason string) (Action, error) {
	var updated Action
	err := executor.withClaimedTransaction(ctx, action, func(transactionCtx context.Context, journal Journal) error {
		current, err := journal.GetAction(transactionCtx, action.ID)
		if err != nil {
			return err
		}
		state, _ := executor.cancelledOrDeadline(current)
		if state == "" {
			return ErrLeaseLost
		}
		updated, err = journal.UpdateOutcome(transactionCtx, OutcomeUpdate{
			ID: current.ID, Version: current.Version, State: state,
			Outcome: outcomeJSON("", reason, 0), UpdatedAt: formatTime(nowUTC(executor.options)),
		})
		return err
	})
	if err != nil {
		if isNoRows(err) {
			return action, ErrLeaseLost
		}
		return action, err
	}
	return preserveActionPlan(updated, action), nil
}

func (executor *Executor) transitionDispatchOwned(ctx context.Context, action Action, attempt Attempt, applied domain.EffectOutcome) (Action, domain.ActionState, domain.EffectOutcome, error) {
	var updated Action
	var state domain.ActionState
	outcome := applied
	err := executor.withClaimedTransaction(ctx, action, func(transactionCtx context.Context, journal Journal) error {
		current, err := journal.GetAction(transactionCtx, action.ID)
		if err != nil {
			return err
		}
		state, outcome = executor.cancelledOrDeadline(current)
		if state == "" {
			state = domain.ActionSucceeded
			outcome = applied
		}
		unresolved, err := journalUnresolvedCount(transactionCtx, journal, action.ID, false)
		if err != nil {
			return err
		}
		updated, err = journal.UpdateOutcome(transactionCtx, OutcomeUpdate{
			ID: current.ID, Version: current.Version, State: state,
			Outcome: outcomeJSON(outcome, "dispatch_applied", unresolved), UnresolvedCount: unresolved, UpdatedAt: formatTime(nowUTC(executor.options)),
		})
		return err
	})
	if err != nil {
		if isNoRows(err) {
			err = ErrLeaseLost
		}
		return action, state, outcome, err
	}
	return preserveActionPlan(updated, action), state, outcome, nil
}

func (executor *Executor) transitionUncertainOwned(ctx context.Context, action Action, attempt Attempt, reason, nextAttemptAt string) (Action, domain.ActionState, error) {
	var updated Action
	var state domain.ActionState
	err := executor.withClaimedTransaction(ctx, action, func(transactionCtx context.Context, journal Journal) error {
		current, err := journal.GetAction(transactionCtx, action.ID)
		if err != nil {
			return err
		}
		state, _ = executor.cancelledOrDeadline(current)
		if state == "" {
			state = domain.ActionReconciling
		} else {
			nextAttemptAt = ""
		}
		unresolved, err := journalUnresolvedCount(transactionCtx, journal, action.ID, state == domain.ActionReconciling)
		if err != nil {
			return err
		}
		updated, err = journal.UpdateOutcome(transactionCtx, OutcomeUpdate{ID: current.ID, Version: current.Version, State: state, NextAttemptAt: nextAttemptAt, Outcome: outcomeJSON("", reason, unresolved), UnresolvedCount: unresolved, UpdatedAt: formatTime(nowUTC(executor.options))})
		return err
	})
	if err != nil {
		if isNoRows(err) {
			return action, state, ErrLeaseLost
		}
		return action, state, err
	}
	return preserveActionPlan(updated, action), state, nil
}

func (executor *Executor) requeue(ctx context.Context, action Action, attempt Attempt, reason string) (Action, error) {
	var updated Action
	err := executor.withTransaction(ctx, func(transactionCtx context.Context, journal Journal) error {
		current, err := journal.GetAction(transactionCtx, action.ID)
		if err != nil {
			return err
		}
		if current.State == domain.ActionRunning {
			current, err = journal.UpdateOutcome(transactionCtx, OutcomeUpdate{ID: current.ID, Version: current.Version, State: domain.ActionReconciling, Outcome: outcomeJSON("", reason, 0), UnresolvedCount: 0, UpdatedAt: formatTime(nowUTC(executor.options))})
			if err != nil {
				return err
			}
		}
		now := nowUTC(executor.options)
		updated, err = journal.UpdateOutcome(transactionCtx, OutcomeUpdate{ID: current.ID, Version: current.Version, State: domain.ActionQueued, NextAttemptAt: formatTime(now), Outcome: outcomeJSON("", reason, 0), UpdatedAt: formatTime(now)})
		return err
	})
	if err != nil {
		if isNoRows(err) {
			return action, ErrLeaseLost
		}
		return action, err
	}
	return preserveActionPlan(updated, action), nil
}

func (executor *Executor) wait(ctx context.Context, action Action, cause error) error {
	return executor.waitFor(ctx, action, cause, false)
}

func (executor *Executor) hold(ctx context.Context, action Action, cause error) error {
	return executor.holdFor(ctx, action, cause, false)
}

func (executor *Executor) requeueOwned(ctx context.Context, action Action, attempt Attempt, reason string) (Action, error) {
	var updated Action
	err := executor.withClaimedTransaction(ctx, action, func(transactionCtx context.Context, journal Journal) error {
		current, err := journal.GetAction(transactionCtx, action.ID)
		if err != nil {
			return err
		}
		if current.State == domain.ActionRunning {
			current, err = journal.UpdateOutcome(transactionCtx, OutcomeUpdate{ID: current.ID, Version: current.Version, State: domain.ActionReconciling, Outcome: outcomeJSON("", reason, 0), UnresolvedCount: 0, UpdatedAt: formatTime(nowUTC(executor.options))})
			if err != nil {
				return err
			}
		}
		now := nowUTC(executor.options)
		updated, err = journal.UpdateOutcome(transactionCtx, OutcomeUpdate{ID: current.ID, Version: current.Version, State: domain.ActionQueued, NextAttemptAt: formatTime(now), Outcome: outcomeJSON("", reason, 0), UpdatedAt: formatTime(now)})
		return err
	})
	if err != nil {
		if isNoRows(err) {
			return action, ErrLeaseLost
		}
		return action, err
	}
	return preserveActionPlan(updated, action), nil
}

func (executor *Executor) waitOwned(ctx context.Context, action Action, cause error) error {
	return executor.waitFor(ctx, action, cause, true)
}

func (executor *Executor) waitFor(ctx context.Context, action Action, cause error, claimed bool) error {
	var err error
	var current Action
	if claimed {
		err = executor.withClaimedTransaction(ctx, action, func(transactionCtx context.Context, journal Journal) error {
			var innerErr error
			current, innerErr = journal.GetAction(transactionCtx, action.ID)
			if innerErr != nil {
				return innerErr
			}
			return executor.applyWait(transactionCtx, journal, current, cause)
		})
	} else {
		err = executor.withTransaction(ctx, func(transactionCtx context.Context, journal Journal) error {
			var innerErr error
			current, innerErr = journal.GetAction(transactionCtx, action.ID)
			if innerErr != nil {
				return innerErr
			}
			return executor.applyWait(transactionCtx, journal, current, cause)
		})
	}
	if isNoRows(err) {
		return ErrLeaseLost
	}
	if err != nil {
		return err
	}
	return cause
}

func (executor *Executor) applyWait(ctx context.Context, journal Journal, action Action, cause error) error {
	attempts, err := journal.ListAttempts(ctx, action.ID)
	if err != nil {
		return err
	}
	attemptNumber := int64(len(attempts))
	if executor.options.Retry.MaxAttempts > 0 && int(attemptNumber) >= executor.options.Retry.MaxAttempts {
		return executor.applyHold(ctx, journal, action, fmt.Errorf("retry limit reached: %w", cause))
	}
	next := nowUTC(executor.options).Add(executor.options.Retry.delay(attemptNumber))
	_, err = journal.UpdateOutcome(ctx, OutcomeUpdate{ID: action.ID, Version: action.Version, State: domain.ActionWaitingDependency, NextAttemptAt: formatTime(next), Outcome: outcomeJSON("", "dependency_wait", 0), UpdatedAt: formatTime(nowUTC(executor.options))})
	return err
}

func (executor *Executor) holdOwned(ctx context.Context, action Action, cause error) error {
	return executor.holdFor(ctx, action, cause, true)
}

func (executor *Executor) holdFor(ctx context.Context, action Action, cause error, claimed bool) error {
	apply := func(transactionCtx context.Context, journal Journal) error {
		current, err := journal.GetAction(transactionCtx, action.ID)
		if err != nil {
			return err
		}
		return executor.applyHold(transactionCtx, journal, current, cause)
	}
	var err error
	if claimed {
		err = executor.withClaimedTransaction(ctx, action, apply)
	} else {
		err = executor.withTransaction(ctx, apply)
	}
	if isNoRows(err) {
		return ErrLeaseLost
	}
	if err != nil {
		return err
	}
	return cause
}

func (executor *Executor) applyHold(ctx context.Context, journal Journal, action Action, cause error) error {
	unresolved, err := journalUnresolvedCount(ctx, journal, action.ID, false)
	if err != nil {
		return err
	}
	_, err = journal.UpdateOutcome(ctx, OutcomeUpdate{ID: action.ID, Version: action.Version, State: domain.ActionNeedsReview, Outcome: outcomeJSON("", safeDetail(cause), unresolved), UnresolvedCount: unresolved, UpdatedAt: formatTime(nowUTC(executor.options))})
	return err
}

// Cancel persists one cancellation marker. Repeating it is idempotent at the
// SQL boundary; an in-flight dispatch is never claimed to be rolled back.
func (executor *Executor) Cancel(ctx context.Context, id string) (Action, error) {
	if executor == nil {
		return Action{}, errors.New("execution executor is nil")
	}
	requestedAt := formatTime(nowUTC(executor.options))
	var action Action
	err := executor.withTransaction(ctx, func(transactionCtx context.Context, journal Journal) error {
		var err error
		action, err = journal.RequestCancellation(transactionCtx, id, requestedAt)
		return err
	})
	if err != nil {
		return Action{}, err
	}
	if err := action.validate(); err != nil {
		return Action{}, err
	}
	executor.cancelActiveHandler(id)
	return action, nil
}

// SQLJournal adapts the generated D-01 queries without exposing generated
// rows to handlers or the public Mastarr API.
type SQLJournal struct {
	store  SQLStore
	query  sqlQueryer
	db     *sql.DB
	runner sqlRunner
}

// SQLStore is implemented by storage.Store. It is kept as a local interface
// so execution tests can provide a transaction-capable fixture store.
type SQLStore interface {
	DB() *sql.DB
	Queries() *sqlc.Queries
	WithTx(context.Context, func(*sqlc.Queries) error) error
}

type sqlRunner interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

// NewSQLJournal binds the action executor to an opened storage.Store.
func NewSQLJournal(store SQLStore) (*SQLJournal, error) {
	if store == nil || store.Queries() == nil || store.DB() == nil {
		return nil, errors.New("execution SQL store is required")
	}
	return &SQLJournal{store: store, query: store.Queries(), db: store.DB(), runner: store.DB()}, nil
}

type sqlQueryer interface {
	GetActionRun(context.Context, string) (*sqlc.ActionRun, error)
	GetActionPlan(context.Context, string) (*sqlc.ActionPlan, error)
	ListDueActionRuns(context.Context, *sqlc.ListDueActionRunsParams) ([]*sqlc.ActionRun, error)
	ClaimActionRun(context.Context, *sqlc.ClaimActionRunParams) (*sqlc.ActionRun, error)
	RecoverRunningActionRuns(context.Context, sql.NullString) ([]*sqlc.ActionRun, error)
	RecoverExpiredActionRuns(context.Context, sql.NullString) ([]*sqlc.ActionRun, error)
	RecoverRunningActionAttempts(context.Context) ([]*sqlc.ActionAttempt, error)
	FinalizeCancelledActionRuns(context.Context, string) ([]*sqlc.ActionRun, error)
	FinalizeDeadlineActionRuns(context.Context, string) ([]*sqlc.ActionRun, error)
	RequestActionCancellation(context.Context, *sqlc.RequestActionCancellationParams) (*sqlc.ActionRun, error)
	ListActionAttempts(context.Context, string) ([]*sqlc.ActionAttempt, error)
	CreateActionAttempt(context.Context, *sqlc.CreateActionAttemptParams) (*sqlc.ActionAttempt, error)
	UpdateActionAttempt(context.Context, *sqlc.UpdateActionAttemptParams) (*sqlc.ActionAttempt, error)
	ListActionEffects(context.Context, string) ([]*sqlc.ActionEffect, error)
	CreateActionEffect(context.Context, *sqlc.CreateActionEffectParams) (*sqlc.ActionEffect, error)
	UpdateActionEffect(context.Context, *sqlc.UpdateActionEffectParams) (*sqlc.ActionEffect, error)
	UpdateActionRunOutcome(context.Context, *sqlc.UpdateActionRunOutcomeParams) (*sqlc.ActionRun, error)
}

func (journal *SQLJournal) clone(query sqlQueryer) *SQLJournal {
	return &SQLJournal{store: journal.store, query: query, db: journal.db}
}

func (journal *SQLJournal) cloneWithRunner(query sqlQueryer, runner sqlRunner) *SQLJournal {
	return &SQLJournal{store: journal.store, query: query, db: journal.db, runner: runner}
}

// InTx implements TransactionalJournal. The handler is never called from the
// callback by Executor; this method only groups local journal records.
func (journal *SQLJournal) InTx(ctx context.Context, fn func(Journal) error) error {
	if journal == nil || journal.store == nil {
		return errors.New("execution SQL transaction is unavailable")
	}
	if fn == nil {
		return errors.New("execution transaction callback is required")
	}
	return journal.store.WithTx(ctx, func(query *sqlc.Queries) error {
		return fn(journal.clone(query))
	})
}

// InTxClaimed acquires SQLite's writer lock and verifies the immutable claim
// before exposing a transactional journal. Every effect/attempt/transition
// written by the callback is therefore part of the same ownership generation;
// lease recovery or re-claim cannot interleave between the check and commit.
func (journal *SQLJournal) InTxClaimed(ctx context.Context, fence ClaimFence, fn func(Journal) error) error {
	if journal == nil || journal.db == nil {
		return errors.New("execution SQL claimed transaction is unavailable")
	}
	if fn == nil {
		return errors.New("execution claimed transaction callback is required")
	}
	tx, err := journal.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	rollback := func() { _ = tx.Rollback() }
	defer rollback()
	result, err := tx.ExecContext(ctx, `
		UPDATE action_runs
		SET updated_at = updated_at
		WHERE id = ?1
		  AND state = 'running'
		  AND claimed_by = ?2
		  AND lease_until = ?3
		  AND lease_until > ?4
		  AND (version = ?5 OR (version = ?5 + 1 AND cancellation_requested_at IS NOT NULL))`,
		fence.ActionID, fence.WorkerID, fence.LeaseUntil, fence.Now, fence.Version)
	if err != nil {
		return err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows != 1 {
		return ErrLeaseLost
	}
	if err := fn(journal.cloneWithRunner(sqlc.New(tx), tx)); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	return nil
}

func (journal *SQLJournal) Reserve(ctx context.Context, actionID string, keys []string) error {
	if journal == nil || journal.runner == nil {
		return fmt.Errorf("%w: durable reservation transaction is unavailable", ErrInvalidJournal)
	}
	keys = canonicalReservationKeys(keys)
	rows, err := journal.runner.QueryContext(ctx, `SELECT id, outcome_json FROM action_runs WHERE state IN ('running', 'reconciling', 'needs_review')`)
	if err != nil {
		return err
	}
	defer rows.Close()
	var ownOutcome json.RawMessage
	var ownFound bool
	for rows.Next() {
		var id, raw string
		if err := rows.Scan(&id, &raw); err != nil {
			return err
		}
		candidate, err := reservationKeysFromOutcome(json.RawMessage(raw))
		if err != nil {
			return err
		}
		if id == actionID {
			ownOutcome = json.RawMessage(raw)
			ownFound = true
			continue
		}
		for _, requested := range keys {
			for _, held := range candidate {
				if reservationConflicts(requested, held) {
					return fmt.Errorf("%w: %s", ErrReservationConflict, requested)
				}
			}
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if !ownFound {
		return ErrNotFound
	}
	current, err := reservationKeysFromOutcome(ownOutcome)
	if err != nil {
		return err
	}
	merged, err := outcomeWithReservations(ownOutcome, append(current, keys...))
	if err != nil {
		return err
	}
	result, err := journal.runner.ExecContext(ctx, `UPDATE action_runs SET outcome_json = ?1 WHERE id = ?2`, string(merged), actionID)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return ErrLeaseLost
	}
	return nil
}

func (journal *SQLJournal) GetAction(ctx context.Context, id string) (Action, error) {
	run, err := journal.query.GetActionRun(ctx, id)
	if err != nil {
		if isNoRows(err) {
			return Action{}, ErrNotFound
		}
		return Action{}, err
	}
	plan, err := journal.query.GetActionPlan(ctx, run.PlanID)
	if err != nil {
		return Action{}, err
	}
	action, err := actionFromSQL(run, plan)
	return action, err
}

func (journal *SQLJournal) GetPlan(ctx context.Context, id string) (Plan, error) {
	plan, err := journal.query.GetActionPlan(ctx, id)
	if err != nil {
		if isNoRows(err) {
			return Plan{}, ErrNotFound
		}
		return Plan{}, err
	}
	return planFromSQL(plan)
}

func (journal *SQLJournal) ListDue(ctx context.Context, now string, limit int) ([]Action, error) {
	rows, err := journal.query.ListDueActionRuns(ctx, &sqlc.ListDueActionRunsParams{Now: sql.NullString{String: now, Valid: true}, Limit: int64(limit)})
	if err != nil {
		return nil, err
	}
	return journal.actionsWithPlans(ctx, rows)
}

func (journal *SQLJournal) ListReconciling(ctx context.Context, now string, limit int) ([]Action, error) {
	if journal.db == nil {
		return nil, errors.New("execution reconciliation listing requires a database")
	}
	rows, err := journal.db.QueryContext(ctx, `
		SELECT id, plan_id, plan_revision, plan_digest, state, desired_state_json,
		       next_attempt_at, deadline_at, cancellation_requested_at, claimed_by,
		       lease_until, version, outcome_json, unresolved_count, created_at, updated_at
		FROM action_runs
		WHERE state = 'reconciling'
		  AND (
		      cancellation_requested_at IS NOT NULL
		      OR (deadline_at IS NOT NULL AND deadline_at <= ?1)
		      OR next_attempt_at IS NULL
		      OR next_attempt_at <= ?1
		  )
		  AND (claimed_by IS NULL OR lease_until IS NULL OR lease_until <= ?1)
		  AND NOT EXISTS (
		      SELECT 1 FROM janitor_records AS approval
		      WHERE approval.approval_action_run_id = action_runs.id
		        AND approval.operation = 'purge'
		        AND approval.approval_plan_id IS NOT NULL
		        AND approval.state IN ('queued', 'running', 'waiting_dependency', 'reconciling', 'succeeded', 'failed', 'held', 'cancelled')
		  )
		ORDER BY COALESCE(next_attempt_at, created_at), created_at, id
		LIMIT ?2`, now, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var runs []*sqlc.ActionRun
	for rows.Next() {
		run := new(sqlc.ActionRun)
		if err := rows.Scan(&run.ID, &run.PlanID, &run.PlanRevision, &run.PlanDigest, &run.State, &run.DesiredStateJson, &run.NextAttemptAt, &run.DeadlineAt, &run.CancellationRequestedAt, &run.ClaimedBy, &run.LeaseUntil, &run.Version, &run.OutcomeJson, &run.UnresolvedCount, &run.CreatedAt, &run.UpdatedAt); err != nil {
			return nil, err
		}
		runs = append(runs, run)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	var actions []Action
	for _, run := range runs {
		plan, err := journal.query.GetActionPlan(ctx, run.PlanID)
		if err != nil {
			return nil, err
		}
		action, err := actionFromSQL(run, plan)
		if err != nil {
			return nil, err
		}
		actions = append(actions, action)
	}
	return actions, nil
}

func (journal *SQLJournal) actionsWithPlans(ctx context.Context, rows []*sqlc.ActionRun) ([]Action, error) {
	actions := make([]Action, 0, len(rows))
	for _, row := range rows {
		plan, err := journal.query.GetActionPlan(ctx, row.PlanID)
		if err != nil {
			return nil, err
		}
		action, err := actionFromSQL(row, plan)
		if err != nil {
			return nil, err
		}
		actions = append(actions, action)
	}
	return actions, nil
}

func (journal *SQLJournal) Claim(ctx context.Context, id string, version int64, workerID, leaseUntil, now string) (Action, error) {
	run, err := journal.query.ClaimActionRun(ctx, &sqlc.ClaimActionRunParams{WorkerID: nullString(workerID), LeaseUntil: nullString(leaseUntil), Now: now, ID: id, Version: version})
	if err != nil {
		return Action{}, err
	}
	plan, err := journal.query.GetActionPlan(ctx, run.PlanID)
	if err != nil {
		return Action{}, err
	}
	return actionFromSQL(run, plan)
}

// RenewLease extends one live claim without advancing its generation. The
// exact previous lease and immutable version are part of the CAS, so a stale
// worker or a cancelled generation cannot renew ownership.
func (journal *SQLJournal) RenewLease(ctx context.Context, fence ClaimFence, leaseUntil, now string) error {
	if journal == nil || journal.runner == nil {
		return fmt.Errorf("%w: execution lease renewal is unavailable", ErrInvalidJournal)
	}
	result, err := journal.runner.ExecContext(ctx, `
		UPDATE action_runs
		SET lease_until = ?1, updated_at = ?2
		WHERE id = ?3
		  AND state = 'running'
		  AND claimed_by = ?4
		  AND lease_until = ?5
		  AND lease_until > ?6
		  AND version = ?7
		  AND cancellation_requested_at IS NULL`,
		leaseUntil, now, fence.ActionID, fence.WorkerID, fence.LeaseUntil, fence.Now, fence.Version)
	if err != nil {
		return err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows != 1 {
		return ErrLeaseLost
	}
	return nil
}

func (journal *SQLJournal) RecoverRunning(ctx context.Context, now string) ([]Action, error) {
	rows, err := journal.query.RecoverRunningActionRuns(ctx, nullString(now))
	if err != nil {
		return nil, err
	}
	return journal.actionsWithPlans(ctx, rows)
}

func (journal *SQLJournal) RecoverExpired(ctx context.Context, now string) ([]Action, error) {
	rows, err := journal.query.RecoverExpiredActionRuns(ctx, nullString(now))
	if err != nil {
		return nil, err
	}
	return journal.actionsWithPlans(ctx, rows)
}

func (journal *SQLJournal) RecoverRunningAttempts(ctx context.Context) ([]Attempt, error) {
	rows, err := journal.query.RecoverRunningActionAttempts(ctx)
	if err != nil {
		return nil, err
	}
	attempts := make([]Attempt, 0, len(rows))
	for _, row := range rows {
		attempt, err := attemptFromSQL(row)
		if err != nil {
			return nil, err
		}
		attempts = append(attempts, attempt)
	}
	return attempts, nil
}

func (journal *SQLJournal) FinalizeCancelled(ctx context.Context, now string) ([]Action, error) {
	rows, err := journal.query.FinalizeCancelledActionRuns(ctx, now)
	if err != nil {
		return nil, err
	}
	return journal.actionsWithPlans(ctx, rows)
}

func (journal *SQLJournal) FinalizeDeadline(ctx context.Context, now string) ([]Action, error) {
	rows, err := journal.query.FinalizeDeadlineActionRuns(ctx, now)
	if err != nil {
		return nil, err
	}
	return journal.actionsWithPlans(ctx, rows)
}

func (journal *SQLJournal) RequestCancellation(ctx context.Context, id, requestedAt string) (Action, error) {
	run, err := journal.query.RequestActionCancellation(ctx, &sqlc.RequestActionCancellationParams{RequestedAt: nullString(requestedAt), UpdatedAt: requestedAt, ID: id})
	if err != nil {
		return Action{}, err
	}
	plan, err := journal.query.GetActionPlan(ctx, run.PlanID)
	if err != nil {
		return Action{}, err
	}
	return actionFromSQL(run, plan)
}

func (journal *SQLJournal) ListAttempts(ctx context.Context, id string) ([]Attempt, error) {
	rows, err := journal.query.ListActionAttempts(ctx, id)
	if err != nil {
		return nil, err
	}
	attempts := make([]Attempt, 0, len(rows))
	for _, row := range rows {
		attempt, err := attemptFromSQL(row)
		if err != nil {
			return nil, err
		}
		attempts = append(attempts, attempt)
	}
	return attempts, nil
}

func (journal *SQLJournal) CreateAttempt(ctx context.Context, attempt Attempt) (Attempt, error) {
	if err := attempt.validate(); err != nil {
		return Attempt{}, err
	}
	row, err := journal.query.CreateActionAttempt(ctx, &sqlc.CreateActionAttemptParams{ID: attempt.ID, ActionRunID: attempt.ActionRunID, AttemptNumber: attempt.AttemptNumber, Phase: string(attempt.Phase), State: string(attempt.State), StartedAt: attempt.StartedAt, FinishedAt: nullString(attempt.FinishedAt), ErrorCode: nullString(attempt.ErrorCode), ErrorDetail: nullString(attempt.ErrorDetail), OutcomeCertainty: string(attempt.OutcomeCertainty), EvidenceJson: string(attempt.Evidence), ExternalID: nullString(attempt.ExternalID)})
	if err != nil {
		return Attempt{}, err
	}
	return attemptFromSQL(row)
}

func (journal *SQLJournal) UpdateAttempt(ctx context.Context, attempt Attempt) (Attempt, error) {
	if err := attempt.validate(); err != nil {
		return Attempt{}, err
	}
	row, err := journal.query.UpdateActionAttempt(ctx, &sqlc.UpdateActionAttemptParams{ID: attempt.ID, State: string(attempt.State), FinishedAt: nullString(attempt.FinishedAt), ErrorCode: nullString(attempt.ErrorCode), ErrorDetail: nullString(attempt.ErrorDetail), OutcomeCertainty: string(attempt.OutcomeCertainty), EvidenceJson: string(attempt.Evidence), ExternalID: nullString(attempt.ExternalID)})
	if err != nil {
		return Attempt{}, err
	}
	return attemptFromSQL(row)
}

func (journal *SQLJournal) ListEffects(ctx context.Context, id string) ([]Effect, error) {
	rows, err := journal.query.ListActionEffects(ctx, id)
	if err != nil {
		return nil, err
	}
	effects := make([]Effect, 0, len(rows))
	for _, row := range rows {
		effect, err := effectFromSQL(row)
		if err != nil {
			return nil, err
		}
		effects = append(effects, effect)
	}
	return effects, nil
}

func (journal *SQLJournal) CreateEffect(ctx context.Context, effect Effect) (Effect, error) {
	if err := effect.validate(); err != nil {
		return Effect{}, err
	}
	row, err := journal.query.CreateActionEffect(ctx, &sqlc.CreateActionEffectParams{ID: effect.ID, ActionRunID: effect.ActionRunID, AttemptID: nullString(effect.AttemptID), Ordinal: effect.Ordinal, TargetKind: effect.TargetKind, TargetID: effect.TargetID, EffectKind: effect.EffectKind, State: string(effect.State), EvidenceJson: string(effect.Evidence), ObservedAt: effect.ObservedAt})
	if err != nil {
		return Effect{}, err
	}
	return effectFromSQL(row)
}

func (journal *SQLJournal) UpdateEffect(ctx context.Context, effect Effect) (Effect, error) {
	if err := effect.validate(); err != nil {
		return Effect{}, err
	}
	row, err := journal.query.UpdateActionEffect(ctx, &sqlc.UpdateActionEffectParams{ID: effect.ID, State: string(effect.State), EvidenceJson: string(effect.Evidence), ObservedAt: effect.ObservedAt, AttemptID: nullString(effect.AttemptID)})
	if err != nil {
		return Effect{}, err
	}
	return effectFromSQL(row)
}

func (journal *SQLJournal) UpdateOutcome(ctx context.Context, update OutcomeUpdate) (Action, error) {
	if update.ID == "" || update.Version <= 0 || !update.State.Valid() || !json.Valid(update.Outcome) || update.UnresolvedCount < 0 {
		return Action{}, fmt.Errorf("%w: invalid outcome update", ErrInvalidJournal)
	}
	current, err := journal.query.GetActionRun(ctx, update.ID)
	if err != nil {
		if isNoRows(err) {
			return Action{}, ErrNotFound
		}
		return Action{}, err
	}
	preserved, err := outcomePreservingReservations(json.RawMessage(current.OutcomeJson), update.Outcome, update.State)
	if err != nil {
		return Action{}, err
	}
	run, err := journal.query.UpdateActionRunOutcome(ctx, &sqlc.UpdateActionRunOutcomeParams{ID: update.ID, Version: update.Version, State: string(update.State), NextAttemptAt: nullString(update.NextAttemptAt), ClaimedBy: nullString(update.ClaimedBy), LeaseUntil: nullString(update.LeaseUntil), OutcomeJson: string(preserved), UnresolvedCount: update.UnresolvedCount, UpdatedAt: update.UpdatedAt})
	if err != nil {
		return Action{}, err
	}
	plan, err := journal.query.GetActionPlan(ctx, run.PlanID)
	if err != nil {
		return Action{}, err
	}
	return actionFromSQL(run, plan)
}

func nullString(value string) sql.NullString {
	return sql.NullString{String: value, Valid: value != ""}
}

func actionFromSQL(run *sqlc.ActionRun, plan *sqlc.ActionPlan) (Action, error) {
	if run == nil || plan == nil {
		return Action{}, fmt.Errorf("%w: action or plan row is nil", ErrInvalidJournal)
	}
	actionKind, err := parseActionKind(plan.Kind)
	if err != nil {
		return Action{}, err
	}
	action := Action{ID: run.ID, PlanID: run.PlanID, PlanRevision: run.PlanRevision, PlanDigest: run.PlanDigest, Kind: actionKind, State: domain.ActionState(run.State), DesiredState: json.RawMessage(run.DesiredStateJson), NextAttemptAt: run.NextAttemptAt.String, DeadlineAt: run.DeadlineAt.String, CancellationRequestedAt: run.CancellationRequestedAt.String, ClaimedBy: run.ClaimedBy.String, LeaseUntil: run.LeaseUntil.String, Version: run.Version, Outcome: json.RawMessage(run.OutcomeJson), UnresolvedCount: run.UnresolvedCount, CreatedAt: run.CreatedAt, UpdatedAt: run.UpdatedAt}
	if err := action.validate(); err != nil {
		return Action{}, err
	}
	return action, nil
}

func parseActionKind(value string) (domain.ActionKind, error) {
	kind := domain.ActionKind(value)
	if !kind.Valid() {
		return "", fmt.Errorf("%w: unsupported action kind %q", ErrInvalidJournal, value)
	}
	return kind, nil
}

func planFromSQL(plan *sqlc.ActionPlan) (Plan, error) {
	if plan == nil {
		return Plan{}, fmt.Errorf("%w: action plan row is nil", ErrInvalidJournal)
	}
	kind, err := parseActionKind(plan.Kind)
	if err != nil {
		return Plan{}, err
	}
	value := Plan{ID: plan.ID, Kind: kind, State: plan.State, CurrentRevision: plan.CurrentRevision, CurrentDigest: plan.CurrentDigest}
	if err := value.validate(); err != nil {
		return Plan{}, err
	}
	return value, nil
}

func attemptFromSQL(row *sqlc.ActionAttempt) (Attempt, error) {
	if row == nil {
		return Attempt{}, fmt.Errorf("%w: attempt row is nil", ErrInvalidJournal)
	}
	attempt := Attempt{ID: row.ID, ActionRunID: row.ActionRunID, AttemptNumber: row.AttemptNumber, Phase: AttemptPhase(row.Phase), State: domain.AttemptState(row.State), StartedAt: row.StartedAt, FinishedAt: row.FinishedAt.String, ErrorCode: row.ErrorCode.String, ErrorDetail: row.ErrorDetail.String, OutcomeCertainty: OutcomeCertainty(row.OutcomeCertainty), ExternalID: row.ExternalID.String, Evidence: json.RawMessage(row.EvidenceJson)}
	if err := attempt.validate(); err != nil {
		return Attempt{}, err
	}
	return attempt, nil
}

func effectFromSQL(row *sqlc.ActionEffect) (Effect, error) {
	if row == nil {
		return Effect{}, fmt.Errorf("%w: effect row is nil", ErrInvalidJournal)
	}
	effect := Effect{ID: row.ID, ActionRunID: row.ActionRunID, AttemptID: row.AttemptID.String, Ordinal: row.Ordinal, TargetKind: row.TargetKind, TargetID: row.TargetID, EffectKind: row.EffectKind, State: EffectState(row.State), Evidence: json.RawMessage(row.EvidenceJson), ObservedAt: row.ObservedAt}
	if err := effect.validate(); err != nil {
		return Effect{}, err
	}
	return effect, nil
}

// Compile-time checks keep generated storage changes visible to this adapter.
var _ sqlQueryer = (*sqlc.Queries)(nil)
