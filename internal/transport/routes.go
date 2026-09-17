// Package transport contains the generated route assembly boundary.
package transport

import (
	"context"
	"errors"

	api "github.com/guilycst/mastarr/internal/api/generated"
	"github.com/guilycst/mastarr/internal/configuration"
	"github.com/guilycst/mastarr/internal/domain"
)

// ErrRouteUnavailable means the route has no assembled application service.
// It is translated to a stable sanitized 503 by the HTTP policy.
var ErrRouteUnavailable = errors.New("route service is unavailable")

// ConfigurationPersistence is the storage-owned transaction seam for
// API-managed configuration. The mutate callback receives the same context
// used to begin the transaction; a storage implementation can therefore
// expose its transaction to the managed-credential store and commit the
// parent resource, encrypted envelopes and revision together.
type ConfigurationPersistence struct {
	CreateConnection func(context.Context, domain.Connection, func(context.Context) (domain.Connection, error)) (domain.Connection, error)
	UpdateConnection func(context.Context, domain.ConfigID, string, func(context.Context) (domain.Connection, error)) (domain.Connection, error)
	RetireConnection func(context.Context, domain.ConfigID, string, func(context.Context) error) error

	CreateStorageRoot func(context.Context, domain.StorageRoot, func(context.Context) (domain.StorageRoot, error)) (domain.StorageRoot, error)
	UpdateStorageRoot func(context.Context, domain.ConfigID, string, func(context.Context) (domain.StorageRoot, error)) (domain.StorageRoot, error)
	RetireStorageRoot func(context.Context, domain.ConfigID, string, func(context.Context) error) error

	CreatePathMapping func(context.Context, domain.PathMapping, func(context.Context) (domain.PathMapping, error)) (domain.PathMapping, error)
	UpdatePathMapping func(context.Context, domain.ConfigID, string, func(context.Context) (domain.PathMapping, error)) (domain.PathMapping, error)
	RetirePathMapping func(context.Context, domain.ConfigID, string, func(context.Context) error) error
}

// ConfigurationReload rebuilds the effective configuration from its durable
// sources. The transport invokes it while holding its configuration write
// gate after a persistence failure so a candidate manager can never remain
// visible to readers. The returned IDs are ownership metadata only; they do
// not contain credential material.
type ConfigurationReload func(context.Context) (*configuration.Manager, []domain.ConfigID, error)

// IdempotencyRecoveryRequest is the bounded in-memory input to a production
// recovery owner. Body is request data already validated by the HTTP policy;
// it must never be persisted or logged. Record is the durable pending attempt
// that must be reconciled before a terminal completion is written.
type IdempotencyRecoveryRequest struct {
	Scope   string
	Key     string
	Digest  string
	Method  string
	Path    string
	IfMatch string
	Body    []byte
	Record  IdempotencyRecord
}

// IdempotencyRecovery reconciles a pending post-dispatch attempt using
// read-only observations. It returns a terminal response only when the
// handler effect is proven; false leaves the durable attempt pending.
type IdempotencyRecovery func(context.Context, IdempotencyRecoveryRequest) (IdempotencyRecord, bool, error)

func cloneConfigurationPersistence(value *ConfigurationPersistence) *ConfigurationPersistence {
	if value == nil {
		return nil
	}
	copyValue := *value
	return &copyValue
}

// IdempotencyRecord is the sanitized durable representation of one idempotent
// mutation reservation or completion. Response bytes are the generated JSON
// body and headers contain only replay-safe metadata such as ETag, Location
// and Cache-Control. A reservation has Pending=true and no replayable body.
type IdempotencyRecord struct {
	Scope        string
	Key          string
	Digest       string
	Status       int
	Headers      map[string][]string
	Body         []byte
	ResourceKind string
	ResourceID   string
	CreatedAt    string
	ExpiresAt    string
	// State is one of reserved or completed for the append-only durable
	// protocol. Empty is accepted only for pre-protocol completed records.
	State string
	// Replayable distinguishes a durable handler outcome that may be replayed
	// from an uncertain/server outcome that must remain held for reconciliation.
	Replayable bool
	// Pending reports a reservation or non-replayable completion. It is derived
	// by the persistence implementation and is never trusted as an HTTP body.
	Pending bool
	// AttemptID binds reservation, release and completion events to one
	// dispatch owner. It is opaque and contains no request or secret data.
	AttemptID string
}

const (
	// IdempotencyStateReserved is durable before a handler is dispatched.
	IdempotencyStateReserved = "reserved"
	// IdempotencyStateReleased records a durable pre-dispatch/no-effect
	// terminal transition. A later request with the same digest may reserve a
	// new attempt; a changed digest remains a conflict.
	IdempotencyStateReleased = "released"
	// IdempotencyStateCompleted records the handler result in an immutable
	// completion record. Replayable=false means the outcome remains held for
	// reconciliation rather than authorizing a blind retry.
	IdempotencyStateCompleted = "completed"
)

// IdempotencyPersistence retains an append-only reservation and completion
// protocol across process restarts. Reserve must durably bind scope, key and
// digest before dispatch. Complete must durably record the handler outcome
// before transport caches or replays a successful response. Load must return
// found=false for a missing record and must not expose secret or transport-
// internal details. Save is retained for older completed-only stores and is
// not used when the three protocol callbacks are present.
type IdempotencyPersistence struct {
	Load     func(context.Context, string, string) (IdempotencyRecord, bool, error)
	Reserve  func(context.Context, IdempotencyRecord) (bool, error)
	Release  func(context.Context, IdempotencyRecord) error
	Complete func(context.Context, IdempotencyRecord) error
	Save     func(context.Context, IdempotencyRecord) error
}

func cloneIdempotencyPersistence(value *IdempotencyPersistence) *IdempotencyPersistence {
	if value == nil {
		return nil
	}
	copyValue := *value
	return &copyValue
}

// RouteDependencies is the explicit assembly point for generated operations.
// Each field is one typed application service boundary. A nil field means the
// corresponding service has not been assembled yet; it never fabricates an
// empty success response. The transport copies this value during New.
type RouteDependencies struct {
	ListActionPlans             func(context.Context, api.ListActionPlansRequestObject) (api.ListActionPlansResponseObject, error)
	CreateActionPlan            func(context.Context, api.CreateActionPlanRequestObject) (api.CreateActionPlanResponseObject, error)
	GetActionPlan               func(context.Context, api.GetActionPlanRequestObject) (api.GetActionPlanResponseObject, error)
	CreateActionPlanRevision    func(context.Context, api.CreateActionPlanRevisionRequestObject) (api.CreateActionPlanRevisionResponseObject, error)
	ListActionRuns              func(context.Context, api.ListActionRunsRequestObject) (api.ListActionRunsResponseObject, error)
	GetActionRun                func(context.Context, api.GetActionRunRequestObject) (api.GetActionRunResponseObject, error)
	ListActionAttempts          func(context.Context, api.ListActionAttemptsRequestObject) (api.ListActionAttemptsResponseObject, error)
	CancelActionRun             func(context.Context, api.CancelActionRunRequestObject) (api.CancelActionRunResponseObject, error)
	RequestActionReconciliation func(context.Context, api.RequestActionReconciliationRequestObject) (api.RequestActionReconciliationResponseObject, error)
	RequestActionRetry          func(context.Context, api.RequestActionRetryRequestObject) (api.RequestActionRetryResponseObject, error)
	ListAuditEvents             func(context.Context, api.ListAuditEventsRequestObject) (api.ListAuditEventsResponseObject, error)
	GetConfiguration            func(context.Context, api.GetConfigurationRequestObject) (api.GetConfigurationResponseObject, error)
	ListConnectionChecks        func(context.Context, api.ListConnectionChecksRequestObject) (api.ListConnectionChecksResponseObject, error)
	CreateConnectionCheck       func(context.Context, api.CreateConnectionCheckRequestObject) (api.CreateConnectionCheckResponseObject, error)
	GetConnectionCheck          func(context.Context, api.GetConnectionCheckRequestObject) (api.GetConnectionCheckResponseObject, error)
	ListConnections             func(context.Context, api.ListConnectionsRequestObject) (api.ListConnectionsResponseObject, error)
	CreateConnection            func(context.Context, api.CreateConnectionRequestObject) (api.CreateConnectionResponseObject, error)
	RetireConnection            func(context.Context, api.RetireConnectionRequestObject) (api.RetireConnectionResponseObject, error)
	GetConnection               func(context.Context, api.GetConnectionRequestObject) (api.GetConnectionResponseObject, error)
	PatchConnection             func(context.Context, api.PatchConnectionRequestObject) (api.PatchConnectionResponseObject, error)
	GetConnectionOptions        func(context.Context, api.GetConnectionOptionsRequestObject) (api.GetConnectionOptionsResponseObject, error)
	ListDescriptors             func(context.Context, api.ListDescriptorsRequestObject) (api.ListDescriptorsResponseObject, error)
	GetDescriptor               func(context.Context, api.GetDescriptorRequestObject) (api.GetDescriptorResponseObject, error)
	GetDescriptorContent        func(context.Context, api.GetDescriptorContentRequestObject) (api.GetDescriptorContentResponseObject, error)
	ListDiscoveries             func(context.Context, api.ListDiscoveriesRequestObject) (api.ListDiscoveriesResponseObject, error)
	GetDiscovery                func(context.Context, api.GetDiscoveryRequestObject) (api.GetDiscoveryResponseObject, error)
	ListDownloads               func(context.Context, api.ListDownloadsRequestObject) (api.ListDownloadsResponseObject, error)
	GetDownload                 func(context.Context, api.GetDownloadRequestObject) (api.GetDownloadResponseObject, error)
	ListMedia                   func(context.Context, api.ListMediaRequestObject) (api.ListMediaResponseObject, error)
	GetMedia                    func(context.Context, api.GetMediaRequestObject) (api.GetMediaResponseObject, error)
	ListMetadataCandidates      func(context.Context, api.ListMetadataCandidatesRequestObject) (api.ListMetadataCandidatesResponseObject, error)
	ListPathMappings            func(context.Context, api.ListPathMappingsRequestObject) (api.ListPathMappingsResponseObject, error)
	CreatePathMapping           func(context.Context, api.CreatePathMappingRequestObject) (api.CreatePathMappingResponseObject, error)
	RetirePathMapping           func(context.Context, api.RetirePathMappingRequestObject) (api.RetirePathMappingResponseObject, error)
	GetPathMapping              func(context.Context, api.GetPathMappingRequestObject) (api.GetPathMappingResponseObject, error)
	PatchPathMapping            func(context.Context, api.PatchPathMappingRequestObject) (api.PatchPathMappingResponseObject, error)
	CreateReviewDecision        func(context.Context, api.CreateReviewDecisionRequestObject) (api.CreateReviewDecisionResponseObject, error)
	GetReviewDecision           func(context.Context, api.GetReviewDecisionRequestObject) (api.GetReviewDecisionResponseObject, error)
	ListScans                   func(context.Context, api.ListScansRequestObject) (api.ListScansResponseObject, error)
	CreateScan                  func(context.Context, api.CreateScanRequestObject) (api.CreateScanResponseObject, error)
	GetScan                     func(context.Context, api.GetScanRequestObject) (api.GetScanResponseObject, error)
	CancelScan                  func(context.Context, api.CancelScanRequestObject) (api.CancelScanResponseObject, error)
	ListStorageRoots            func(context.Context, api.ListStorageRootsRequestObject) (api.ListStorageRootsResponseObject, error)
	CreateStorageRoot           func(context.Context, api.CreateStorageRootRequestObject) (api.CreateStorageRootResponseObject, error)
	RetireStorageRoot           func(context.Context, api.RetireStorageRootRequestObject) (api.RetireStorageRootResponseObject, error)
	GetStorageRoot              func(context.Context, api.GetStorageRootRequestObject) (api.GetStorageRootResponseObject, error)
	PatchStorageRoot            func(context.Context, api.PatchStorageRootRequestObject) (api.PatchStorageRootResponseObject, error)
	ListTrash                   func(context.Context, api.ListTrashRequestObject) (api.ListTrashResponseObject, error)
	GetTrash                    func(context.Context, api.GetTrashRequestObject) (api.GetTrashResponseObject, error)
	ListWorkflowRuns            func(context.Context, api.ListWorkflowRunsRequestObject) (api.ListWorkflowRunsResponseObject, error)
	CreateWorkflowRun           func(context.Context, api.CreateWorkflowRunRequestObject) (api.CreateWorkflowRunResponseObject, error)
	GetWorkflowRun              func(context.Context, api.GetWorkflowRunRequestObject) (api.GetWorkflowRunResponseObject, error)
	CancelWorkflowRun           func(context.Context, api.CancelWorkflowRunRequestObject) (api.CancelWorkflowRunResponseObject, error)
	GetLiveHealth               func(context.Context, api.GetLiveHealthRequestObject) (api.GetLiveHealthResponseObject, error)
	GetReadyHealth              func(context.Context, api.GetReadyHealthRequestObject) (api.GetReadyHealthResponseObject, error)
}

// hasDurableMutationDependency reports whether the caller assembled any
// application operation that can mutate durable state.  The built-in
// configuration handlers have their own read-only recovery owner; injected
// handlers must supply an owner through Options.IdempotencyRecovery before a
// durable idempotency store is enabled.
func hasDurableMutationDependency(dependency *RouteDependencies) bool {
	if dependency == nil {
		return false
	}
	return dependency.CreateActionPlan != nil ||
		dependency.CreateActionPlanRevision != nil ||
		dependency.CancelActionRun != nil ||
		dependency.RequestActionReconciliation != nil ||
		dependency.RequestActionRetry != nil ||
		dependency.CreateConnectionCheck != nil ||
		dependency.CreateConnection != nil ||
		dependency.RetireConnection != nil ||
		dependency.PatchConnection != nil ||
		dependency.CreatePathMapping != nil ||
		dependency.RetirePathMapping != nil ||
		dependency.PatchPathMapping != nil ||
		dependency.CreateReviewDecision != nil ||
		dependency.CreateScan != nil ||
		dependency.CancelScan != nil ||
		dependency.CreateStorageRoot != nil ||
		dependency.RetireStorageRoot != nil ||
		dependency.PatchStorageRoot != nil ||
		dependency.CreateWorkflowRun != nil ||
		dependency.CancelWorkflowRun != nil
}

// routeDependencies returns the immutable dependency snapshot captured by New.
func (server *Server) routeDependencies() *RouteDependencies {
	if server == nil {
		return nil
	}
	return server.dependencies
}

func (server *Server) ListActionPlans(ctx context.Context, request api.ListActionPlansRequestObject) (api.ListActionPlansResponseObject, error) {
	if dependency := server.routeDependencies(); dependency != nil && dependency.ListActionPlans != nil {
		return dependency.ListActionPlans(ctx, request)
	}
	return nil, ErrRouteUnavailable
}

func (server *Server) CreateActionPlan(ctx context.Context, request api.CreateActionPlanRequestObject) (api.CreateActionPlanResponseObject, error) {
	if dependency := server.routeDependencies(); dependency != nil && dependency.CreateActionPlan != nil {
		return dependency.CreateActionPlan(ctx, request)
	}
	return nil, ErrRouteUnavailable
}

func (server *Server) GetActionPlan(ctx context.Context, request api.GetActionPlanRequestObject) (api.GetActionPlanResponseObject, error) {
	if dependency := server.routeDependencies(); dependency != nil && dependency.GetActionPlan != nil {
		return dependency.GetActionPlan(ctx, request)
	}
	return nil, ErrRouteUnavailable
}

func (server *Server) CreateActionPlanRevision(ctx context.Context, request api.CreateActionPlanRevisionRequestObject) (api.CreateActionPlanRevisionResponseObject, error) {
	if dependency := server.routeDependencies(); dependency != nil && dependency.CreateActionPlanRevision != nil {
		return dependency.CreateActionPlanRevision(ctx, request)
	}
	return nil, ErrRouteUnavailable
}

func (server *Server) ListActionRuns(ctx context.Context, request api.ListActionRunsRequestObject) (api.ListActionRunsResponseObject, error) {
	if dependency := server.routeDependencies(); dependency != nil && dependency.ListActionRuns != nil {
		return dependency.ListActionRuns(ctx, request)
	}
	return nil, ErrRouteUnavailable
}

func (server *Server) GetActionRun(ctx context.Context, request api.GetActionRunRequestObject) (api.GetActionRunResponseObject, error) {
	if dependency := server.routeDependencies(); dependency != nil && dependency.GetActionRun != nil {
		return dependency.GetActionRun(ctx, request)
	}
	return nil, ErrRouteUnavailable
}

func (server *Server) ListActionAttempts(ctx context.Context, request api.ListActionAttemptsRequestObject) (api.ListActionAttemptsResponseObject, error) {
	if dependency := server.routeDependencies(); dependency != nil && dependency.ListActionAttempts != nil {
		return dependency.ListActionAttempts(ctx, request)
	}
	return nil, ErrRouteUnavailable
}

func (server *Server) CancelActionRun(ctx context.Context, request api.CancelActionRunRequestObject) (api.CancelActionRunResponseObject, error) {
	if dependency := server.routeDependencies(); dependency != nil && dependency.CancelActionRun != nil {
		return dependency.CancelActionRun(ctx, request)
	}
	return nil, ErrRouteUnavailable
}

func (server *Server) RequestActionReconciliation(ctx context.Context, request api.RequestActionReconciliationRequestObject) (api.RequestActionReconciliationResponseObject, error) {
	if dependency := server.routeDependencies(); dependency != nil && dependency.RequestActionReconciliation != nil {
		return dependency.RequestActionReconciliation(ctx, request)
	}
	return nil, ErrRouteUnavailable
}

func (server *Server) RequestActionRetry(ctx context.Context, request api.RequestActionRetryRequestObject) (api.RequestActionRetryResponseObject, error) {
	if dependency := server.routeDependencies(); dependency != nil && dependency.RequestActionRetry != nil {
		return dependency.RequestActionRetry(ctx, request)
	}
	return nil, ErrRouteUnavailable
}

func (server *Server) ListAuditEvents(ctx context.Context, request api.ListAuditEventsRequestObject) (api.ListAuditEventsResponseObject, error) {
	if dependency := server.routeDependencies(); dependency != nil && dependency.ListAuditEvents != nil {
		return dependency.ListAuditEvents(ctx, request)
	}
	return nil, ErrRouteUnavailable
}

func (server *Server) ListConnectionChecks(ctx context.Context, request api.ListConnectionChecksRequestObject) (api.ListConnectionChecksResponseObject, error) {
	if dependency := server.routeDependencies(); dependency != nil && dependency.ListConnectionChecks != nil {
		return dependency.ListConnectionChecks(ctx, request)
	}
	return nil, ErrRouteUnavailable
}

func (server *Server) CreateConnectionCheck(ctx context.Context, request api.CreateConnectionCheckRequestObject) (api.CreateConnectionCheckResponseObject, error) {
	if dependency := server.routeDependencies(); dependency != nil && dependency.CreateConnectionCheck != nil {
		return dependency.CreateConnectionCheck(ctx, request)
	}
	return nil, ErrRouteUnavailable
}

func (server *Server) GetConnectionCheck(ctx context.Context, request api.GetConnectionCheckRequestObject) (api.GetConnectionCheckResponseObject, error) {
	if dependency := server.routeDependencies(); dependency != nil && dependency.GetConnectionCheck != nil {
		return dependency.GetConnectionCheck(ctx, request)
	}
	return nil, ErrRouteUnavailable
}

func (server *Server) GetConnectionOptions(ctx context.Context, request api.GetConnectionOptionsRequestObject) (api.GetConnectionOptionsResponseObject, error) {
	if dependency := server.routeDependencies(); dependency != nil && dependency.GetConnectionOptions != nil {
		return dependency.GetConnectionOptions(ctx, request)
	}
	return nil, ErrRouteUnavailable
}

func (server *Server) ListDescriptors(ctx context.Context, request api.ListDescriptorsRequestObject) (api.ListDescriptorsResponseObject, error) {
	if dependency := server.routeDependencies(); dependency != nil && dependency.ListDescriptors != nil {
		return dependency.ListDescriptors(ctx, request)
	}
	return nil, ErrRouteUnavailable
}

func (server *Server) GetDescriptor(ctx context.Context, request api.GetDescriptorRequestObject) (api.GetDescriptorResponseObject, error) {
	if dependency := server.routeDependencies(); dependency != nil && dependency.GetDescriptor != nil {
		return dependency.GetDescriptor(ctx, request)
	}
	return nil, ErrRouteUnavailable
}

func (server *Server) GetDescriptorContent(ctx context.Context, request api.GetDescriptorContentRequestObject) (api.GetDescriptorContentResponseObject, error) {
	if dependency := server.routeDependencies(); dependency != nil && dependency.GetDescriptorContent != nil {
		return dependency.GetDescriptorContent(ctx, request)
	}
	return nil, ErrRouteUnavailable
}

func (server *Server) ListDiscoveries(ctx context.Context, request api.ListDiscoveriesRequestObject) (api.ListDiscoveriesResponseObject, error) {
	if dependency := server.routeDependencies(); dependency != nil && dependency.ListDiscoveries != nil {
		return dependency.ListDiscoveries(ctx, request)
	}
	return nil, ErrRouteUnavailable
}

func (server *Server) GetDiscovery(ctx context.Context, request api.GetDiscoveryRequestObject) (api.GetDiscoveryResponseObject, error) {
	if dependency := server.routeDependencies(); dependency != nil && dependency.GetDiscovery != nil {
		return dependency.GetDiscovery(ctx, request)
	}
	return nil, ErrRouteUnavailable
}

func (server *Server) ListDownloads(ctx context.Context, request api.ListDownloadsRequestObject) (api.ListDownloadsResponseObject, error) {
	if dependency := server.routeDependencies(); dependency != nil && dependency.ListDownloads != nil {
		return dependency.ListDownloads(ctx, request)
	}
	return nil, ErrRouteUnavailable
}

func (server *Server) GetDownload(ctx context.Context, request api.GetDownloadRequestObject) (api.GetDownloadResponseObject, error) {
	if dependency := server.routeDependencies(); dependency != nil && dependency.GetDownload != nil {
		return dependency.GetDownload(ctx, request)
	}
	return nil, ErrRouteUnavailable
}

func (server *Server) ListMedia(ctx context.Context, request api.ListMediaRequestObject) (api.ListMediaResponseObject, error) {
	if dependency := server.routeDependencies(); dependency != nil && dependency.ListMedia != nil {
		return dependency.ListMedia(ctx, request)
	}
	return nil, ErrRouteUnavailable
}

func (server *Server) GetMedia(ctx context.Context, request api.GetMediaRequestObject) (api.GetMediaResponseObject, error) {
	if dependency := server.routeDependencies(); dependency != nil && dependency.GetMedia != nil {
		return dependency.GetMedia(ctx, request)
	}
	return nil, ErrRouteUnavailable
}

func (server *Server) ListMetadataCandidates(ctx context.Context, request api.ListMetadataCandidatesRequestObject) (api.ListMetadataCandidatesResponseObject, error) {
	if dependency := server.routeDependencies(); dependency != nil && dependency.ListMetadataCandidates != nil {
		return dependency.ListMetadataCandidates(ctx, request)
	}
	return nil, ErrRouteUnavailable
}

func (server *Server) CreateReviewDecision(ctx context.Context, request api.CreateReviewDecisionRequestObject) (api.CreateReviewDecisionResponseObject, error) {
	if dependency := server.routeDependencies(); dependency != nil && dependency.CreateReviewDecision != nil {
		return dependency.CreateReviewDecision(ctx, request)
	}
	return nil, ErrRouteUnavailable
}

func (server *Server) GetReviewDecision(ctx context.Context, request api.GetReviewDecisionRequestObject) (api.GetReviewDecisionResponseObject, error) {
	if dependency := server.routeDependencies(); dependency != nil && dependency.GetReviewDecision != nil {
		return dependency.GetReviewDecision(ctx, request)
	}
	return nil, ErrRouteUnavailable
}

func (server *Server) ListScans(ctx context.Context, request api.ListScansRequestObject) (api.ListScansResponseObject, error) {
	if dependency := server.routeDependencies(); dependency != nil && dependency.ListScans != nil {
		return dependency.ListScans(ctx, request)
	}
	return nil, ErrRouteUnavailable
}

func (server *Server) CreateScan(ctx context.Context, request api.CreateScanRequestObject) (api.CreateScanResponseObject, error) {
	if dependency := server.routeDependencies(); dependency != nil && dependency.CreateScan != nil {
		return dependency.CreateScan(ctx, request)
	}
	return nil, ErrRouteUnavailable
}

func (server *Server) GetScan(ctx context.Context, request api.GetScanRequestObject) (api.GetScanResponseObject, error) {
	if dependency := server.routeDependencies(); dependency != nil && dependency.GetScan != nil {
		return dependency.GetScan(ctx, request)
	}
	return nil, ErrRouteUnavailable
}

func (server *Server) CancelScan(ctx context.Context, request api.CancelScanRequestObject) (api.CancelScanResponseObject, error) {
	if dependency := server.routeDependencies(); dependency != nil && dependency.CancelScan != nil {
		return dependency.CancelScan(ctx, request)
	}
	return nil, ErrRouteUnavailable
}

func (server *Server) ListTrash(ctx context.Context, request api.ListTrashRequestObject) (api.ListTrashResponseObject, error) {
	if dependency := server.routeDependencies(); dependency != nil && dependency.ListTrash != nil {
		return dependency.ListTrash(ctx, request)
	}
	return nil, ErrRouteUnavailable
}

func (server *Server) GetTrash(ctx context.Context, request api.GetTrashRequestObject) (api.GetTrashResponseObject, error) {
	if dependency := server.routeDependencies(); dependency != nil && dependency.GetTrash != nil {
		return dependency.GetTrash(ctx, request)
	}
	return nil, ErrRouteUnavailable
}

func (server *Server) ListWorkflowRuns(ctx context.Context, request api.ListWorkflowRunsRequestObject) (api.ListWorkflowRunsResponseObject, error) {
	if dependency := server.routeDependencies(); dependency != nil && dependency.ListWorkflowRuns != nil {
		return dependency.ListWorkflowRuns(ctx, request)
	}
	return nil, ErrRouteUnavailable
}

func (server *Server) CreateWorkflowRun(ctx context.Context, request api.CreateWorkflowRunRequestObject) (api.CreateWorkflowRunResponseObject, error) {
	if dependency := server.routeDependencies(); dependency != nil && dependency.CreateWorkflowRun != nil {
		return dependency.CreateWorkflowRun(ctx, request)
	}
	return nil, ErrRouteUnavailable
}

func (server *Server) GetWorkflowRun(ctx context.Context, request api.GetWorkflowRunRequestObject) (api.GetWorkflowRunResponseObject, error) {
	if dependency := server.routeDependencies(); dependency != nil && dependency.GetWorkflowRun != nil {
		return dependency.GetWorkflowRun(ctx, request)
	}
	return nil, ErrRouteUnavailable
}

func (server *Server) CancelWorkflowRun(ctx context.Context, request api.CancelWorkflowRunRequestObject) (api.CancelWorkflowRunResponseObject, error) {
	if dependency := server.routeDependencies(); dependency != nil && dependency.CancelWorkflowRun != nil {
		return dependency.CancelWorkflowRun(ctx, request)
	}
	return nil, ErrRouteUnavailable
}
