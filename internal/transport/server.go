// Package transport exposes Mastarr's generated HTTP boundary.
//
// The package deliberately keeps HTTP concerns at the edge.  It translates
// the non-secret configuration service into generated API DTOs and leaves
// action, discovery, and upstream policy to their existing application
// services.  Routes whose service has not yet been assembled return a typed,
// sanitized problem instead of silently pretending that an empty result is
// authoritative.
package transport

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	api "github.com/guilycst/mastarr/internal/api/generated"
	"github.com/guilycst/mastarr/internal/configuration"
	"github.com/guilycst/mastarr/internal/domain"
)

const (
	defaultMaxBodyBytes       int64 = 4 << 20
	defaultMaxManifestBytes         = 1 << 30
	defaultMaxManifestEntries       = 10_000
	maxIdempotencyEntries           = 1024
)

var (
	ErrNotReady            = errors.New("mastarr is not ready")
	ErrRequestTooLarge     = errors.New("request body exceeds configured limit")
	ErrUnknownField        = errors.New("request contains an unknown field")
	ErrDuplicateField      = errors.New("request contains a duplicate field")
	ErrInvalidJSON         = errors.New("request body is invalid JSON")
	ErrOriginForbidden     = errors.New("request origin is not allowed")
	ErrIdempotencyConflict = errors.New("idempotency key was reused with a different request")
	ErrIdempotencyRequired = errors.New("Idempotency-Key is required")
	ErrIfMatchRequired     = errors.New("If-Match precondition is required")
	ErrIfMatchMismatch     = errors.New("If-Match precondition does not match")
	ErrInvalidIdempotency  = errors.New("Idempotency-Key is invalid")
	ErrIdempotencyPending  = errors.New("idempotency request is pending reconciliation")
	ErrInvalidParameter    = errors.New("request parameter is invalid")
	ErrConfigurationStore  = errors.New("configuration persistence is unavailable")
	ErrIdempotencyStore    = errors.New("idempotency persistence is unavailable")
	// ErrIdempotencyRecoveryRequired prevents a durable mutation from being
	// assembled without a route-specific owner that can reconcile a lost
	// completion.  A no-op owner would leave the reservation permanently
	// pending, while the built-in owner only covers configuration routes.
	ErrIdempotencyRecoveryRequired = errors.New("durable mutation recovery owner is required")
)

// Options controls one API server.  Configuration is optional so a process
// can expose health while its durable dependencies are still starting.
type Options struct {
	Configuration *configuration.Manager
	// ConfigurationReload rebuilds the manager from durable state after a
	// configuration transaction fails. It is invoked while the configuration
	// write gate is held, before any reader or retry can observe a candidate.
	ConfigurationReload ConfigurationReload
	// Dependencies is the explicit application-service assembly for generated
	// routes. Nil fields remain unavailable until their owning service is
	// wired; transport never manufactures an authoritative empty response.
	Dependencies *RouteDependencies
	// ConfigurationPersistence wraps a configuration mutation and its manager
	// call in the storage-owned transaction. The callback receives a context
	// carrying the transaction so managed credential envelopes and their parent
	// row can commit together.
	ConfigurationPersistence *ConfigurationPersistence
	// IdempotencyPersistence retains successful mutation records across process
	// restarts. A nil value keeps the transport usable for in-memory tests while
	// production startup supplies the durable implementation.
	IdempotencyPersistence *IdempotencyPersistence
	// IdempotencyRecovery is the production owner for unresolved post-dispatch
	// attempts. It may return a terminal record only after read-only
	// reconciliation proves the requested effect; otherwise the attempt stays
	// pending and no blind redispatch is allowed.
	IdempotencyRecovery IdempotencyRecovery
	// ManagedCredentialIDs contains only stable connection IDs whose encrypted
	// fields are owned by the API. It is metadata, never credential material.
	ManagedCredentialIDs []domain.ConfigID
	Now                  func() time.Time
	Ready                bool
	AllowedOrigin        string
	MaxBodyBytes         int64

	// MaxManifestBytes and MaxManifestEntries are applied to request bodies
	// before generated decoding.  They remain independent from the body limit
	// so callers can choose a smaller action-specific budget.
	MaxManifestBytes   int64
	MaxManifestEntries int
}

// Server implements api.StrictServerInterface and is wrapped by the strict
// generated net/http adapter. Every route is explicitly assembled through
// RouteDependencies; the built-in configuration and health services are the
// only defaults supplied by this package.
type Server struct {
	// configurationGate serializes the effective manager with its durable
	// snapshot. Readers hold RLock for their entire manager read; writers hold
	// Lock through the storage transaction and any recovery reload.
	configurationGate     sync.RWMutex
	configurationMu       sync.RWMutex
	configuration         *configuration.Manager
	configurationReloadMu sync.RWMutex
	configurationReload   ConfigurationReload
	idempotencyRecoveryMu sync.RWMutex
	idempotencyRecovery   IdempotencyRecovery
	recoveryOwnerReady    atomic.Bool
	// idempotencyAssemblyMu serializes persistence/owner publication with an
	// HTTP mutation. Holding its read lock for the policy request means a
	// setter cannot attach durable state or remove its recovery owner between
	// the readiness check and dispatch.
	idempotencyAssemblyMu sync.RWMutex
	dependencies          *RouteDependencies
	persistenceMu         sync.RWMutex
	configurationStore    *ConfigurationPersistence
	idempotencyStore      *IdempotencyPersistence
	managedCredentialMu   sync.RWMutex
	managedCredentialIDs  map[domain.ConfigID]struct{}
	now                   func() time.Time
	allowedOrigin         string
	maxBodyBytes          int64
	maxManifestBytes      int64
	maxManifestEntries    int
	ready                 atomic.Bool

	idempotencyMu sync.Mutex
	idempotency   map[string]idempotencyEntry
	pending       map[string]*idempotencyPending
	sequence      atomic.Uint64
}

type idempotencyEntry struct {
	digest []byte
	status int
	header http.Header
	body   []byte
}

type idempotencyPending struct {
	digest []byte
	done   chan struct{}
}

type idempotencyRequestStateKey struct{}

// idempotencyRequestState binds configuration transaction failure recovery to
// the exact durable reservation owned by the current HTTP request. It is
// deliberately request-local and carries no request body or credential data.
type idempotencyRequestState struct {
	scope     string
	key       string
	digest    string
	attempt   string
	createdAt string
	method    string
	path      string
	ifMatch   string
	body      []byte
	released  atomic.Bool
}

// New constructs a generated-server implementation.  It does not open a
// database or contact an upstream; startup owns those lifecycle decisions.
func New(options Options) (*Server, error) {
	if options.Now == nil {
		options.Now = time.Now
	}
	if options.MaxBodyBytes <= 0 {
		options.MaxBodyBytes = defaultMaxBodyBytes
	}
	if options.MaxBodyBytes < 1 {
		return nil, errors.New("transport max body size must be positive")
	}
	if options.MaxManifestBytes <= 0 {
		options.MaxManifestBytes = defaultMaxManifestBytes
	}
	if options.MaxManifestEntries <= 0 {
		options.MaxManifestEntries = defaultMaxManifestEntries
	}
	if options.IdempotencyPersistence != nil {
		persistence := options.IdempotencyPersistence
		// A configured durable repository must expose the complete reservation
		// protocol. Falling back to a completed-only Save would permit a crash
		// between dispatch and completion to be replayed blindly.
		if persistence.Load == nil || persistence.Reserve == nil || persistence.Release == nil || persistence.Complete == nil {
			return nil, errors.New("transport idempotency persistence lacks reservation protocol")
		}
	}
	if options.AllowedOrigin != "" {
		origin, err := canonicalOrigin(options.AllowedOrigin)
		if err != nil {
			return nil, fmt.Errorf("transport allowed origin: %w", err)
		}
		options.AllowedOrigin = origin
	}
	var dependencies *RouteDependencies
	if options.Dependencies != nil {
		copyDependencies := *options.Dependencies
		dependencies = &copyDependencies
	}
	if options.IdempotencyPersistence != nil && hasDurableMutationDependency(dependencies) && options.IdempotencyRecovery == nil {
		return nil, ErrIdempotencyRecoveryRequired
	}
	recoveryOwnerReady := !hasDurableMutationDependency(dependencies) || options.IdempotencyRecovery != nil
	server := &Server{
		configuration:        options.Configuration,
		configurationReload:  options.ConfigurationReload,
		idempotencyRecovery:  options.IdempotencyRecovery,
		dependencies:         dependencies,
		configurationStore:   cloneConfigurationPersistence(options.ConfigurationPersistence),
		idempotencyStore:     cloneIdempotencyPersistence(options.IdempotencyPersistence),
		managedCredentialIDs: make(map[domain.ConfigID]struct{}, len(options.ManagedCredentialIDs)),
		now:                  options.Now,
		allowedOrigin:        options.AllowedOrigin,
		maxBodyBytes:         options.MaxBodyBytes,
		maxManifestBytes:     options.MaxManifestBytes,
		maxManifestEntries:   options.MaxManifestEntries,
		idempotency:          make(map[string]idempotencyEntry),
		pending:              make(map[string]*idempotencyPending),
	}
	server.recoveryOwnerReady.Store(recoveryOwnerReady)
	for _, id := range options.ManagedCredentialIDs {
		if id.Valid() {
			server.managedCredentialIDs[id] = struct{}{}
		}
	}
	if server.idempotencyRecovery == nil {
		server.idempotencyRecovery = server.recoverConfigurationIdempotency
	}
	server.ready.Store(options.Ready)
	return server, nil
}

// NewServer is an explicit alias for callers that prefer constructor names
// which describe the returned value.
func NewServer(options Options) (*Server, error) { return New(options) }

// SetConfiguration publishes the bootstrapped configuration service. Startup
// may expose health while migrations and key loading are in progress, so the
// generated handler must be able to transition from an unavailable service to
// a ready one without racing request handlers.
func (server *Server) SetConfiguration(manager *configuration.Manager) {
	if server == nil {
		return
	}
	server.configurationMu.Lock()
	server.configuration = manager
	server.configurationMu.Unlock()
}

// SetConfigurationReload publishes the durable-state rebuild callback used
// after a failed configuration transaction. The callback is copied so the
// caller can discard its assembly value safely.
func (server *Server) SetConfigurationReload(reload ConfigurationReload) {
	if server == nil {
		return
	}
	server.configurationReloadMu.Lock()
	server.configurationReload = reload
	server.configurationReloadMu.Unlock()
}

func (server *Server) configurationReloadCallback() ConfigurationReload {
	if server == nil {
		return nil
	}
	server.configurationReloadMu.RLock()
	defer server.configurationReloadMu.RUnlock()
	return server.configurationReload
}

// SetIdempotencyRecovery publishes the durable recovery owner used for
// pending post-dispatch attempts. The callback is copied so the caller may
// discard its assembly value after startup.
func (server *Server) SetIdempotencyRecovery(recovery IdempotencyRecovery) error {
	if server == nil {
		return ErrIdempotencyRecoveryRequired
	}
	server.idempotencyAssemblyMu.Lock()
	defer server.idempotencyAssemblyMu.Unlock()
	if recovery == nil && server.requiresDurableMutationRecovery() {
		// A durable mutation owner cannot be removed while either an injected
		// mutation or the built-in configuration handlers are backed by durable
		// idempotency. Preserve the currently installed owner atomically.
		if server.idempotencyPersistence() != nil {
			return ErrIdempotencyRecoveryRequired
		}
		// Publish the closed state before removing the callback so a concurrent
		// request cannot observe durable persistence with no recovery owner.
		server.recoveryOwnerReady.Store(false)
	} else if recovery != nil {
		// Install the replacement before publishing readiness; requests that see
		// true can therefore resolve the callback immediately.
		server.idempotencyRecoveryMu.Lock()
		server.idempotencyRecovery = recovery
		server.idempotencyRecoveryMu.Unlock()
		server.recoveryOwnerReady.Store(true)
		return nil
	} else {
		server.recoveryOwnerReady.Store(true)
	}
	server.idempotencyRecoveryMu.Lock()
	server.idempotencyRecovery = recovery
	server.idempotencyRecoveryMu.Unlock()
	return nil
}

func (server *Server) idempotencyRecoveryCallback() IdempotencyRecovery {
	if server == nil {
		return nil
	}
	server.idempotencyRecoveryMu.RLock()
	defer server.idempotencyRecoveryMu.RUnlock()
	return server.idempotencyRecovery
}

// recoverConfiguration restores the manager from its durable source while
// the caller still owns configurationGate. A missing or failed callback
// deliberately leaves the service unavailable instead of exposing a manager
// that may contain an uncommitted candidate.
func (server *Server) recoverConfiguration(ctx context.Context) error {
	if server == nil {
		return ErrConfigurationStore
	}
	reload := server.configurationReloadCallback()
	if reload == nil {
		server.SetConfiguration(nil)
		server.SetManagedCredentialIDs(nil)
		server.SetReady(false)
		return ErrConfigurationStore
	}
	base := ctx
	if base == nil {
		base = context.Background()
	}
	base = context.WithoutCancel(base)
	reloadCtx, cancel := context.WithTimeout(base, 5*time.Second)
	defer cancel()
	manager, managedIDs, err := reload(reloadCtx)
	if err != nil || manager == nil {
		server.SetConfiguration(nil)
		server.SetManagedCredentialIDs(nil)
		server.SetReady(false)
		return ErrConfigurationStore
	}
	wasReady := server.Ready()
	server.SetConfiguration(manager)
	server.SetManagedCredentialIDs(managedIDs)
	server.SetReady(wasReady)
	return nil
}

func (server *Server) finishConfigurationMutation(ctx context.Context, err error) {
	if !errors.Is(err, ErrConfigurationStore) {
		return
	}
	// The original persistence error remains the public result. Recovery is a
	// safety action performed before releasing the write gate; a failed reload
	// has already made the manager unavailable.
	if server.recoverConfiguration(ctx) == nil {
		// A failed transaction has no committed effect. Release the exact
		// durable reservation before the configuration gate is handed back so
		// the same request key can retry after the source of the failure heals.
		if known, materialized := server.configurationEffectMaterialized(ctx); known && !materialized {
			_ = server.releaseCurrentIdempotency(ctx)
		}
	}
}

func (server *Server) configurationManager() *configuration.Manager {
	if server == nil {
		return nil
	}
	server.configurationMu.RLock()
	defer server.configurationMu.RUnlock()
	return server.configuration
}

// SetConfigurationPersistence publishes the durable configuration transaction
// seam after SQLite startup has completed. The value is copied so callers can
// safely reuse or discard their assembly struct.
func (server *Server) SetConfigurationPersistence(persistence *ConfigurationPersistence) {
	if server == nil {
		return
	}
	server.persistenceMu.Lock()
	server.configurationStore = cloneConfigurationPersistence(persistence)
	server.persistenceMu.Unlock()
}

func (server *Server) configurationPersistence() *ConfigurationPersistence {
	if server == nil {
		return nil
	}
	server.persistenceMu.RLock()
	defer server.persistenceMu.RUnlock()
	return server.configurationStore
}

// SetIdempotencyPersistence publishes the durable idempotency repository
// after storage startup. Existing in-flight requests continue to use the
// copied repository safely; production callers set it before readiness.
func (server *Server) SetIdempotencyPersistence(persistence *IdempotencyPersistence) error {
	if server == nil {
		return ErrIdempotencyStore
	}
	server.idempotencyAssemblyMu.Lock()
	defer server.idempotencyAssemblyMu.Unlock()
	if persistence != nil {
		if persistence.Load == nil || persistence.Reserve == nil || persistence.Release == nil || persistence.Complete == nil {
			return errors.New("transport idempotency persistence lacks reservation protocol")
		}
		if server.idempotencyRecoveryCallback() == nil {
			// A late durable store cannot be published while the effective
			// recovery callback is absent. This also covers built-in
			// configuration handlers after a caller removed their owner before
			// storage startup completed.
			return ErrIdempotencyRecoveryRequired
		}
		if hasDurableMutationDependency(server.dependencies) && !server.recoveryOwnerReady.Load() {
			// Keep the previously assembled state intact. A late durable store
			// cannot be published until an explicit owner is present.
			return ErrIdempotencyRecoveryRequired
		}
	}
	server.persistenceMu.Lock()
	server.idempotencyStore = cloneIdempotencyPersistence(persistence)
	server.persistenceMu.Unlock()
	if persistence == nil && !hasDurableMutationDependency(server.dependencies) {
		server.recoveryOwnerReady.Store(true)
	}
	return nil
}

func (server *Server) idempotencyPersistence() *IdempotencyPersistence {
	if server == nil {
		return nil
	}
	server.persistenceMu.RLock()
	defer server.persistenceMu.RUnlock()
	return server.idempotencyStore
}

// requiresDurableMutationRecovery reports whether the current server has a
// mutation path whose reservation may outlive the process. The built-in
// configuration handlers are always part of Server, so attaching durable
// idempotency makes them dependencies even when no application handlers have
// been injected.
func (server *Server) requiresDurableMutationRecovery() bool {
	if server == nil {
		return false
	}
	return hasDurableMutationDependency(server.dependencies) || server.idempotencyPersistence() != nil
}

// SetManagedCredentialIDs updates redacted credential ownership metadata after
// startup has loaded durable API state. IDs are copied and no secret material
// crosses this boundary.
func (server *Server) SetManagedCredentialIDs(ids []domain.ConfigID) {
	if server == nil {
		return
	}
	values := make(map[domain.ConfigID]struct{}, len(ids))
	for _, id := range ids {
		if id.Valid() {
			values[id] = struct{}{}
		}
	}
	server.managedCredentialMu.Lock()
	server.managedCredentialIDs = values
	server.managedCredentialMu.Unlock()
}

func (server *Server) hasManagedCredentials(id domain.ConfigID) bool {
	if server == nil {
		return false
	}
	server.managedCredentialMu.RLock()
	defer server.managedCredentialMu.RUnlock()
	_, exists := server.managedCredentialIDs[id]
	return exists
}

func (server *Server) setManagedCredential(id domain.ConfigID, managed bool) {
	if server == nil || !id.Valid() {
		return
	}
	server.managedCredentialMu.Lock()
	defer server.managedCredentialMu.Unlock()
	if managed {
		server.managedCredentialIDs[id] = struct{}{}
		return
	}
	delete(server.managedCredentialIDs, id)
}

// SetReady publishes the process readiness transition after migrations,
// encryption-key setup, and static configuration have completed.
func (server *Server) SetReady(ready bool) {
	if server != nil {
		server.ready.Store(ready)
	}
}

// Ready reports the current process readiness without inspecting an upstream.
func (server *Server) Ready() bool { return server != nil && server.ready.Load() }

// Handler returns the generated strict server surrounded by the transport
// policy layer.  The generated router remains the source of route matching.
func (server *Server) Handler() http.Handler {
	if server == nil {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			writeProblem(w, r, errors.New("transport server is nil"))
		})
	}
	strict := api.NewStrictHandlerWithOptions(server, nil, api.StrictHTTPServerOptions{
		RequestErrorHandlerFunc:  writeProblem,
		ResponseErrorHandlerFunc: writeProblem,
	})
	return server.policy(api.HandlerWithOptions(strict, api.StdHTTPServerOptions{ErrorHandlerFunc: writeProblem}))
}

// ServeHTTP makes Server usable directly in tests and when passed to a
// net/http.Server.  Handler() is constructed once per request only if callers
// use this convenience path; production startup should cache Handler().
func (server *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	server.Handler().ServeHTTP(w, r)
}

// GetLiveHealth reports process liveness and intentionally does not depend on
// migrations, the key, or upstream availability.
func (server *Server) GetLiveHealth(ctx context.Context, request api.GetLiveHealthRequestObject) (api.GetLiveHealthResponseObject, error) {
	if dependency := server.routeDependencies(); dependency != nil && dependency.GetLiveHealth != nil {
		return dependency.GetLiveHealth(ctx, request)
	}
	if err := contextError(ctx); err != nil {
		return nil, err
	}
	return api.GetLiveHealth200JSONResponse(api.Health{
		ObservedAt: server.now().UTC(),
		Status:     api.HealthStatusOk,
	}), nil
}

// GetReadyHealth reports the startup barrier separately from liveness.
func (server *Server) GetReadyHealth(ctx context.Context, request api.GetReadyHealthRequestObject) (api.GetReadyHealthResponseObject, error) {
	if dependency := server.routeDependencies(); dependency != nil && dependency.GetReadyHealth != nil {
		return dependency.GetReadyHealth(ctx, request)
	}
	if err := contextError(ctx); err != nil {
		return nil, err
	}
	if !server.Ready() {
		return api.GetReadyHealth503ApplicationProblemPlusJSONResponse{
			Problem503ApplicationProblemPlusJSONResponse: api.Problem503ApplicationProblemPlusJSONResponse(problemFor(ctx, ErrNotReady, http.StatusServiceUnavailable)),
		}, nil
	}
	return api.GetReadyHealth200JSONResponse(api.Health{
		ObservedAt: server.now().UTC(),
		Status:     api.HealthStatusOk,
		Reason:     nil,
	}), nil
}

// GetConfiguration returns only the effective non-secret snapshot.
func (server *Server) GetConfiguration(ctx context.Context, request api.GetConfigurationRequestObject) (api.GetConfigurationResponseObject, error) {
	if dependency := server.routeDependencies(); dependency != nil && dependency.GetConfiguration != nil {
		return dependency.GetConfiguration(ctx, request)
	}
	server.configurationGate.RLock()
	defer server.configurationGate.RUnlock()
	snapshot, err := server.snapshot(ctx)
	if err != nil {
		return nil, err
	}
	etag := quoteETag(snapshot.Source.Revision)
	return api.GetConfiguration200JSONResponse{
		Body:    server.apiConfiguration(snapshot),
		Headers: api.GetConfiguration200ResponseHeaders{CacheControl: stringPtr("no-store"), ETag: &etag},
	}, nil
}

// ListConnections returns a bounded, complete snapshot page.  Cursor paging
// is rejected until a durable cursor service is wired; an empty response is
// never used to imply that a later page does not exist.
func (server *Server) ListConnections(ctx context.Context, request api.ListConnectionsRequestObject) (api.ListConnectionsResponseObject, error) {
	if dependency := server.routeDependencies(); dependency != nil && dependency.ListConnections != nil {
		return dependency.ListConnections(ctx, request)
	}
	server.configurationGate.RLock()
	defer server.configurationGate.RUnlock()
	manager := server.configurationManager()
	if manager == nil {
		return nil, ErrNotReady
	}
	if request.Params.Cursor != nil && strings.TrimSpace(string(*request.Params.Cursor)) != "" {
		return nil, ErrSnapshotCursor
	}
	limit, err := boundedLimit(request.Params.Limit)
	if err != nil {
		return nil, err
	}
	items, err := manager.ListConnections(ctx, false)
	if err != nil {
		return nil, err
	}
	if len(items) > limit {
		return nil, ErrSnapshotCursor
	}
	now := server.now().UTC()
	return api.ListConnections200JSONResponse{
		Body:    api.ConnectionList{Items: server.mapConnections(items), Page: api.Page{Coverage: []api.Coverage{}, ObservedAt: now}},
		Headers: api.ListConnections200ResponseHeaders{CacheControl: stringPtr("no-store")},
	}, nil
}

// GetConnection returns one source-aware configuration record.
func (server *Server) GetConnection(ctx context.Context, request api.GetConnectionRequestObject) (api.GetConnectionResponseObject, error) {
	if dependency := server.routeDependencies(); dependency != nil && dependency.GetConnection != nil {
		return dependency.GetConnection(ctx, request)
	}
	server.configurationGate.RLock()
	defer server.configurationGate.RUnlock()
	manager := server.configurationManager()
	if manager == nil {
		return nil, ErrNotReady
	}
	connection, err := manager.GetConnection(ctx, domain.ConfigID(request.ConnectionId), false)
	if err != nil {
		return nil, err
	}
	etag := quoteETag(connection.Revision)
	return api.GetConnection200JSONResponse{Body: server.mapConnection(connection), Headers: api.GetConnection200ResponseHeaders{CacheControl: stringPtr("no-store"), ETag: &etag}}, nil
}

// CreateConnection persists an API-owned, encrypted-credential connection.
func (server *Server) CreateConnection(ctx context.Context, request api.CreateConnectionRequestObject) (response api.CreateConnectionResponseObject, err error) {
	if dependency := server.routeDependencies(); dependency != nil && dependency.CreateConnection != nil {
		return dependency.CreateConnection(ctx, request)
	}
	server.configurationGate.Lock()
	defer func() {
		server.finishConfigurationMutation(ctx, err)
		server.configurationGate.Unlock()
	}()
	manager := server.configurationManager()
	if manager == nil {
		return nil, ErrNotReady
	}
	if request.Body == nil {
		return nil, ErrInvalidJSON
	}
	spec, cleanup, err := connectionSpec(*request.Body)
	if err != nil {
		return nil, err
	}
	defer cleanup()
	connection, err := server.createConnection(ctx, spec)
	if err != nil {
		return nil, err
	}
	if len(spec.Credentials) != 0 {
		server.setManagedCredential(connection.ID, true)
	}
	etag := quoteETag(connection.Revision)
	location := "/api/v1/connections/" + url.PathEscape(connection.ID.String())
	return api.CreateConnection201JSONResponse{Body: server.mapConnection(connection), Headers: api.CreateConnection201ResponseHeaders{CacheControl: stringPtr("no-store"), ETag: &etag, Location: &location}}, nil
}

// PatchConnection applies the generated If-Match value to the configuration
// manager's revision CAS.
func (server *Server) PatchConnection(ctx context.Context, request api.PatchConnectionRequestObject) (response api.PatchConnectionResponseObject, err error) {
	if dependency := server.routeDependencies(); dependency != nil && dependency.PatchConnection != nil {
		return dependency.PatchConnection(ctx, request)
	}
	server.configurationGate.Lock()
	defer func() {
		server.finishConfigurationMutation(ctx, err)
		server.configurationGate.Unlock()
	}()
	manager := server.configurationManager()
	if manager == nil {
		return nil, ErrNotReady
	}
	if request.Body == nil {
		return nil, ErrInvalidJSON
	}
	patch, cleanup, err := connectionPatch(*request.Body)
	if err != nil {
		return nil, err
	}
	defer cleanup()
	expectedRevision, err := strongETag(string(request.Params.IfMatch))
	if err != nil {
		return nil, err
	}
	connection, err := server.updateConnection(ctx, manager, domain.ConfigID(request.ConnectionId), expectedRevision, patch)
	if err != nil {
		return nil, err
	}
	if patch.Credentials != nil {
		server.setManagedCredential(connection.ID, len(patch.Credentials) != 0)
	}
	etag := quoteETag(connection.Revision)
	return api.PatchConnection200JSONResponse{Body: server.mapConnection(connection), Headers: api.PatchConnection200ResponseHeaders{CacheControl: stringPtr("no-store"), ETag: &etag}}, nil
}

// RetireConnection preserves the configuration tombstone and requires the
// exact revision supplied by If-Match.
func (server *Server) RetireConnection(ctx context.Context, request api.RetireConnectionRequestObject) (response api.RetireConnectionResponseObject, err error) {
	if dependency := server.routeDependencies(); dependency != nil && dependency.RetireConnection != nil {
		return dependency.RetireConnection(ctx, request)
	}
	server.configurationGate.Lock()
	defer func() {
		server.finishConfigurationMutation(ctx, err)
		server.configurationGate.Unlock()
	}()
	manager := server.configurationManager()
	if manager == nil {
		return nil, ErrNotReady
	}
	expectedRevision, err := strongETag(string(request.Params.IfMatch))
	if err != nil {
		return nil, err
	}
	err = server.retireConnection(ctx, manager, domain.ConfigID(request.ConnectionId), expectedRevision)
	if err != nil {
		return nil, err
	}
	return api.RetireConnection204Response{}, nil
}

func (server *Server) ListStorageRoots(ctx context.Context, request api.ListStorageRootsRequestObject) (api.ListStorageRootsResponseObject, error) {
	if dependency := server.routeDependencies(); dependency != nil && dependency.ListStorageRoots != nil {
		return dependency.ListStorageRoots(ctx, request)
	}
	server.configurationGate.RLock()
	defer server.configurationGate.RUnlock()
	manager := server.configurationManager()
	if manager == nil {
		return nil, ErrNotReady
	}
	if request.Params.Cursor != nil && strings.TrimSpace(string(*request.Params.Cursor)) != "" {
		return nil, ErrSnapshotCursor
	}
	limit, err := boundedLimit(request.Params.Limit)
	if err != nil {
		return nil, err
	}
	items, err := manager.ListStorageRoots(ctx, false)
	if err != nil {
		return nil, err
	}
	if len(items) > limit {
		return nil, ErrSnapshotCursor
	}
	return api.ListStorageRoots200JSONResponse{Body: api.StorageRootList{Items: mapStorageRoots(items), Page: api.Page{Coverage: []api.Coverage{}, ObservedAt: server.now().UTC()}}, Headers: api.ListStorageRoots200ResponseHeaders{CacheControl: stringPtr("no-store")}}, nil
}

func (server *Server) GetStorageRoot(ctx context.Context, request api.GetStorageRootRequestObject) (api.GetStorageRootResponseObject, error) {
	if dependency := server.routeDependencies(); dependency != nil && dependency.GetStorageRoot != nil {
		return dependency.GetStorageRoot(ctx, request)
	}
	server.configurationGate.RLock()
	defer server.configurationGate.RUnlock()
	manager := server.configurationManager()
	if manager == nil {
		return nil, ErrNotReady
	}
	root, err := manager.GetStorageRoot(ctx, domain.ConfigID(request.StorageRootId), false)
	if err != nil {
		return nil, err
	}
	etag := quoteETag(root.Revision)
	return api.GetStorageRoot200JSONResponse{Body: mapStorageRoot(root), Headers: api.GetStorageRoot200ResponseHeaders{CacheControl: stringPtr("no-store"), ETag: &etag}}, nil
}

func (server *Server) CreateStorageRoot(ctx context.Context, request api.CreateStorageRootRequestObject) (response api.CreateStorageRootResponseObject, err error) {
	if dependency := server.routeDependencies(); dependency != nil && dependency.CreateStorageRoot != nil {
		return dependency.CreateStorageRoot(ctx, request)
	}
	server.configurationGate.Lock()
	defer func() {
		server.finishConfigurationMutation(ctx, err)
		server.configurationGate.Unlock()
	}()
	manager := server.configurationManager()
	if manager == nil {
		return nil, ErrNotReady
	}
	if request.Body == nil {
		return nil, ErrInvalidJSON
	}
	spec := configuration.StorageRootSpec{ID: domain.ConfigID(request.Body.Id), Label: request.Body.Label, Purpose: domain.StoragePurpose(request.Body.Purpose), Path: request.Body.Path}
	root, err := server.createStorageRoot(ctx, manager, spec)
	if err != nil {
		return nil, err
	}
	etag := quoteETag(root.Revision)
	location := "/api/v1/storage-roots/" + url.PathEscape(root.ID.String())
	return api.CreateStorageRoot201JSONResponse{Body: mapStorageRoot(root), Headers: api.CreateStorageRoot201ResponseHeaders{CacheControl: stringPtr("no-store"), ETag: &etag, Location: &location}}, nil
}

func (server *Server) PatchStorageRoot(ctx context.Context, request api.PatchStorageRootRequestObject) (response api.PatchStorageRootResponseObject, err error) {
	if dependency := server.routeDependencies(); dependency != nil && dependency.PatchStorageRoot != nil {
		return dependency.PatchStorageRoot(ctx, request)
	}
	server.configurationGate.Lock()
	defer func() {
		server.finishConfigurationMutation(ctx, err)
		server.configurationGate.Unlock()
	}()
	manager := server.configurationManager()
	if manager == nil {
		return nil, ErrNotReady
	}
	if request.Body == nil {
		return nil, ErrInvalidJSON
	}
	body := request.Body
	patch := configuration.StorageRootPatch{Label: body.Label, Path: body.Path, WatchEnabled: body.WatchEnabled, WatchIntervalSeconds: body.WatchIntervalSeconds}
	expectedRevision, err := strongETag(string(request.Params.IfMatch))
	if err != nil {
		return nil, err
	}
	root, err := server.updateStorageRoot(ctx, manager, domain.ConfigID(request.StorageRootId), expectedRevision, patch)
	if err != nil {
		return nil, err
	}
	etag := quoteETag(root.Revision)
	return api.PatchStorageRoot200JSONResponse{Body: mapStorageRoot(root), Headers: api.PatchStorageRoot200ResponseHeaders{CacheControl: stringPtr("no-store"), ETag: &etag}}, nil
}

func (server *Server) RetireStorageRoot(ctx context.Context, request api.RetireStorageRootRequestObject) (response api.RetireStorageRootResponseObject, err error) {
	if dependency := server.routeDependencies(); dependency != nil && dependency.RetireStorageRoot != nil {
		return dependency.RetireStorageRoot(ctx, request)
	}
	server.configurationGate.Lock()
	defer func() {
		server.finishConfigurationMutation(ctx, err)
		server.configurationGate.Unlock()
	}()
	manager := server.configurationManager()
	if manager == nil {
		return nil, ErrNotReady
	}
	expectedRevision, err := strongETag(string(request.Params.IfMatch))
	if err != nil {
		return nil, err
	}
	err = server.retireStorageRoot(ctx, manager, domain.ConfigID(request.StorageRootId), expectedRevision)
	if err != nil {
		return nil, err
	}
	return api.RetireStorageRoot204Response{}, nil
}

func (server *Server) ListPathMappings(ctx context.Context, request api.ListPathMappingsRequestObject) (api.ListPathMappingsResponseObject, error) {
	if dependency := server.routeDependencies(); dependency != nil && dependency.ListPathMappings != nil {
		return dependency.ListPathMappings(ctx, request)
	}
	server.configurationGate.RLock()
	defer server.configurationGate.RUnlock()
	manager := server.configurationManager()
	if manager == nil {
		return nil, ErrNotReady
	}
	if request.Params.Cursor != nil && strings.TrimSpace(string(*request.Params.Cursor)) != "" {
		return nil, ErrSnapshotCursor
	}
	limit, err := boundedLimit(request.Params.Limit)
	if err != nil {
		return nil, err
	}
	items, err := manager.ListPathMappings(ctx, false)
	if err != nil {
		return nil, err
	}
	if len(items) > limit {
		return nil, ErrSnapshotCursor
	}
	return api.ListPathMappings200JSONResponse{Body: api.PathMappingList{Items: mapPathMappings(items), Page: api.Page{Coverage: []api.Coverage{}, ObservedAt: server.now().UTC()}}, Headers: api.ListPathMappings200ResponseHeaders{CacheControl: stringPtr("no-store")}}, nil
}

func (server *Server) GetPathMapping(ctx context.Context, request api.GetPathMappingRequestObject) (api.GetPathMappingResponseObject, error) {
	if dependency := server.routeDependencies(); dependency != nil && dependency.GetPathMapping != nil {
		return dependency.GetPathMapping(ctx, request)
	}
	server.configurationGate.RLock()
	defer server.configurationGate.RUnlock()
	manager := server.configurationManager()
	if manager == nil {
		return nil, ErrNotReady
	}
	mapping, err := manager.GetPathMapping(ctx, domain.ConfigID(request.PathMappingId), false)
	if err != nil {
		return nil, err
	}
	etag := quoteETag(mapping.Revision)
	return api.GetPathMapping200JSONResponse{Body: mapPathMapping(mapping), Headers: api.GetPathMapping200ResponseHeaders{CacheControl: stringPtr("no-store"), ETag: &etag}}, nil
}

func (server *Server) CreatePathMapping(ctx context.Context, request api.CreatePathMappingRequestObject) (response api.CreatePathMappingResponseObject, err error) {
	if dependency := server.routeDependencies(); dependency != nil && dependency.CreatePathMapping != nil {
		return dependency.CreatePathMapping(ctx, request)
	}
	server.configurationGate.Lock()
	defer func() {
		server.finishConfigurationMutation(ctx, err)
		server.configurationGate.Unlock()
	}()
	manager := server.configurationManager()
	if manager == nil {
		return nil, ErrNotReady
	}
	if request.Body == nil {
		return nil, ErrInvalidJSON
	}
	body := request.Body
	spec := configuration.PathMappingSpec{ID: domain.ConfigID(body.Id), ConnectionID: domain.ConfigID(body.ConnectionId), SourcePrefix: body.SourcePrefix, RootID: domain.ConfigID(body.RootId), DestinationPrefix: body.DestinationPrefix}
	mapping, err := server.createPathMapping(ctx, manager, spec)
	if err != nil {
		return nil, err
	}
	etag := quoteETag(mapping.Revision)
	location := "/api/v1/path-mappings/" + url.PathEscape(mapping.ID.String())
	return api.CreatePathMapping201JSONResponse{Body: mapPathMapping(mapping), Headers: api.CreatePathMapping201ResponseHeaders{CacheControl: stringPtr("no-store"), ETag: &etag, Location: &location}}, nil
}

func (server *Server) PatchPathMapping(ctx context.Context, request api.PatchPathMappingRequestObject) (response api.PatchPathMappingResponseObject, err error) {
	if dependency := server.routeDependencies(); dependency != nil && dependency.PatchPathMapping != nil {
		return dependency.PatchPathMapping(ctx, request)
	}
	server.configurationGate.Lock()
	defer func() {
		server.finishConfigurationMutation(ctx, err)
		server.configurationGate.Unlock()
	}()
	manager := server.configurationManager()
	if manager == nil {
		return nil, ErrNotReady
	}
	if request.Body == nil {
		return nil, ErrInvalidJSON
	}
	patch := configuration.PathMappingPatch{SourcePrefix: request.Body.SourcePrefix, DestinationPrefix: request.Body.DestinationPrefix}
	expectedRevision, err := strongETag(string(request.Params.IfMatch))
	if err != nil {
		return nil, err
	}
	mapping, err := server.updatePathMapping(ctx, manager, domain.ConfigID(request.PathMappingId), expectedRevision, patch)
	if err != nil {
		return nil, err
	}
	etag := quoteETag(mapping.Revision)
	return api.PatchPathMapping200JSONResponse{Body: mapPathMapping(mapping), Headers: api.PatchPathMapping200ResponseHeaders{CacheControl: stringPtr("no-store"), ETag: &etag}}, nil
}

func (server *Server) RetirePathMapping(ctx context.Context, request api.RetirePathMappingRequestObject) (response api.RetirePathMappingResponseObject, err error) {
	if dependency := server.routeDependencies(); dependency != nil && dependency.RetirePathMapping != nil {
		return dependency.RetirePathMapping(ctx, request)
	}
	server.configurationGate.Lock()
	defer func() {
		server.finishConfigurationMutation(ctx, err)
		server.configurationGate.Unlock()
	}()
	manager := server.configurationManager()
	if manager == nil {
		return nil, ErrNotReady
	}
	expectedRevision, err := strongETag(string(request.Params.IfMatch))
	if err != nil {
		return nil, err
	}
	err = server.retirePathMapping(ctx, manager, domain.ConfigID(request.PathMappingId), expectedRevision)
	if err != nil {
		return nil, err
	}
	return api.RetirePathMapping204Response{}, nil
}

func (server *Server) createConnection(ctx context.Context, spec configuration.ConnectionSpec) (domain.Connection, error) {
	manager := server.configurationManager()
	if manager == nil {
		return domain.Connection{}, ErrNotReady
	}
	mutate := func(mutationCtx context.Context) (domain.Connection, error) {
		return manager.CreateConnection(mutationCtx, spec)
	}
	if persistence := server.configurationPersistence(); persistence != nil && persistence.CreateConnection != nil {
		draft := domain.Connection{
			ID:       spec.ID,
			Kind:     spec.Kind,
			Label:    spec.Label,
			Endpoint: spec.Endpoint,
			Source: domain.SourceMetadata{
				Source:       domain.SourceAPI,
				Editable:     true,
				DocumentID:   "api",
				Revision:     "pending",
				StartupAt:    server.now().UTC(),
				ReloadPolicy: domain.ReloadOnRestart,
			},
		}
		return persistence.CreateConnection(ctx, draft, mutate)
	}
	return mutate(ctx)
}

func (server *Server) updateConnection(ctx context.Context, manager *configuration.Manager, id domain.ConfigID, expectedRevision string, patch configuration.ConnectionPatch) (domain.Connection, error) {
	mutate := func(mutationCtx context.Context) (domain.Connection, error) {
		return manager.PatchConnection(mutationCtx, id, expectedRevision, patch)
	}
	if persistence := server.configurationPersistence(); persistence != nil && persistence.UpdateConnection != nil {
		return persistence.UpdateConnection(ctx, id, expectedRevision, mutate)
	}
	return mutate(ctx)
}

func (server *Server) retireConnection(ctx context.Context, manager *configuration.Manager, id domain.ConfigID, expectedRevision string) error {
	mutate := func(mutationCtx context.Context) error {
		return manager.RetireConnection(mutationCtx, id, expectedRevision)
	}
	if persistence := server.configurationPersistence(); persistence != nil && persistence.RetireConnection != nil {
		return persistence.RetireConnection(ctx, id, expectedRevision, mutate)
	}
	return mutate(ctx)
}

func (server *Server) createStorageRoot(ctx context.Context, manager *configuration.Manager, spec configuration.StorageRootSpec) (domain.StorageRoot, error) {
	mutate := func(mutationCtx context.Context) (domain.StorageRoot, error) {
		return manager.CreateStorageRoot(mutationCtx, spec)
	}
	if persistence := server.configurationPersistence(); persistence != nil && persistence.CreateStorageRoot != nil {
		draft := domain.StorageRoot{ID: spec.ID, Label: spec.Label, Purpose: spec.Purpose, Path: spec.Path, Source: domain.SourceMetadata{Source: domain.SourceAPI, Editable: true, DocumentID: "api", Revision: "pending", StartupAt: server.now().UTC(), ReloadPolicy: domain.ReloadOnRestart}, ReadOnly: spec.ReadOnly, Capabilities: append([]domain.Capability(nil), spec.Capabilities...), Watch: spec.Watch}
		return persistence.CreateStorageRoot(ctx, draft, mutate)
	}
	return mutate(ctx)
}

func (server *Server) updateStorageRoot(ctx context.Context, manager *configuration.Manager, id domain.ConfigID, expectedRevision string, patch configuration.StorageRootPatch) (domain.StorageRoot, error) {
	mutate := func(mutationCtx context.Context) (domain.StorageRoot, error) {
		return manager.PatchStorageRoot(mutationCtx, id, expectedRevision, patch)
	}
	if persistence := server.configurationPersistence(); persistence != nil && persistence.UpdateStorageRoot != nil {
		return persistence.UpdateStorageRoot(ctx, id, expectedRevision, mutate)
	}
	return mutate(ctx)
}

func (server *Server) retireStorageRoot(ctx context.Context, manager *configuration.Manager, id domain.ConfigID, expectedRevision string) error {
	mutate := func(mutationCtx context.Context) error {
		return manager.RetireStorageRoot(mutationCtx, id, expectedRevision)
	}
	if persistence := server.configurationPersistence(); persistence != nil && persistence.RetireStorageRoot != nil {
		return persistence.RetireStorageRoot(ctx, id, expectedRevision, mutate)
	}
	return mutate(ctx)
}

func (server *Server) createPathMapping(ctx context.Context, manager *configuration.Manager, spec configuration.PathMappingSpec) (domain.PathMapping, error) {
	mutate := func(mutationCtx context.Context) (domain.PathMapping, error) {
		return manager.CreatePathMapping(mutationCtx, spec)
	}
	if persistence := server.configurationPersistence(); persistence != nil && persistence.CreatePathMapping != nil {
		draft := domain.PathMapping{ID: spec.ID, ConnectionID: spec.ConnectionID, SourcePrefix: spec.SourcePrefix, RootID: spec.RootID, DestinationPrefix: spec.DestinationPrefix, Source: domain.SourceMetadata{Source: domain.SourceAPI, Editable: true, DocumentID: "api", Revision: "pending", StartupAt: server.now().UTC(), ReloadPolicy: domain.ReloadOnRestart}}
		return persistence.CreatePathMapping(ctx, draft, mutate)
	}
	return mutate(ctx)
}

func (server *Server) updatePathMapping(ctx context.Context, manager *configuration.Manager, id domain.ConfigID, expectedRevision string, patch configuration.PathMappingPatch) (domain.PathMapping, error) {
	mutate := func(mutationCtx context.Context) (domain.PathMapping, error) {
		return manager.PatchPathMapping(mutationCtx, id, expectedRevision, patch)
	}
	if persistence := server.configurationPersistence(); persistence != nil && persistence.UpdatePathMapping != nil {
		return persistence.UpdatePathMapping(ctx, id, expectedRevision, mutate)
	}
	return mutate(ctx)
}

func (server *Server) retirePathMapping(ctx context.Context, manager *configuration.Manager, id domain.ConfigID, expectedRevision string) error {
	mutate := func(mutationCtx context.Context) error {
		return manager.RetirePathMapping(mutationCtx, id, expectedRevision)
	}
	if persistence := server.configurationPersistence(); persistence != nil && persistence.RetirePathMapping != nil {
		return persistence.RetirePathMapping(ctx, id, expectedRevision, mutate)
	}
	return mutate(ctx)
}

func (server *Server) snapshot(ctx context.Context) (domain.ConfigurationSnapshot, error) {
	manager := server.configurationManager()
	if manager == nil {
		return domain.ConfigurationSnapshot{}, ErrNotReady
	}
	return manager.Snapshot(ctx)
}

func contextError(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		return nil
	}
}

var ErrSnapshotCursor = errors.New("snapshot cursor is expired or unsupported")

func boundedLimit(value *api.Limit) (int, error) {
	if value == nil {
		return 100, nil
	}
	if *value < 1 || *value > 1000 {
		return 0, ErrInvalidParameter
	}
	return int(*value), nil
}

func stringPtr(value string) *string { return &value }

func quoteETag(value string) string { return `"` + strings.ReplaceAll(value, `"`, "") + `"` }

func strongETag(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", ErrIfMatchRequired
	}
	if strings.HasPrefix(strings.ToLower(value), "w/") {
		return "", ErrIfMatchMismatch
	}
	if value == "*" || len(value) < 2 || value[0] != '"' || value[len(value)-1] != '"' || strings.Contains(value[1:len(value)-1], `"`) {
		return "", ErrIfMatchMismatch
	}
	return value[1 : len(value)-1], nil
}

func normalizeETag(value string) string {
	return strings.TrimSpace(value)
}

func canonicalOrigin(value string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil || parsed.User != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.RawQuery != "" || parsed.Fragment != "" || (parsed.Path != "" && parsed.Path != "/") {
		return "", errors.New("origin must be an absolute HTTP(S) origin without credentials or a path")
	}
	return strings.ToLower(parsed.Scheme) + "://" + strings.ToLower(parsed.Host), nil
}

func apiSource(metadata domain.SourceMetadata) api.SourceMetadata {
	source := api.Api
	if metadata.Source == domain.SourceYAML {
		source = api.Yaml
	}
	return api.SourceMetadata{DocumentId: metadata.DocumentID, Editable: metadata.Editable, ReloadPolicy: api.RestartRequired, Revision: metadata.Revision, Source: source, StartupAt: metadata.StartupAt.UTC()}
}

func (server *Server) apiConfiguration(snapshot domain.ConfigurationSnapshot) api.Configuration {
	keySource := api.ConfigurationKeySource(snapshot.KeySource)
	return api.Configuration{Connections: server.mapConnections(snapshot.Connections), StorageRoots: mapStorageRoots(snapshot.StorageRoots), PathMappings: mapPathMappings(snapshot.PathMappings), KeyPath: optionalString(snapshot.KeyPath), KeySource: keySource, RestartRequired: boolPtr(snapshot.RestartRequired), Source: apiSource(snapshot.Source)}
}

func (server *Server) mapConnections(items []domain.Connection) []api.Connection {
	result := make([]api.Connection, 0, len(items))
	for _, item := range items {
		result = append(result, server.mapConnection(item))
	}
	return result
}

func (server *Server) mapConnection(item domain.Connection) api.Connection {
	result := mapConnection(item)
	if server.hasManagedCredentials(item.ID) {
		result.CredentialState = api.ConnectionCredentialStateManaged
	}
	return result
}

func mapConnections(items []domain.Connection) []api.Connection {
	result := make([]api.Connection, 0, len(items))
	for _, item := range items {
		result = append(result, mapConnection(item))
	}
	return result
}

func mapConnection(item domain.Connection) api.Connection {
	credentialState := api.ConnectionCredentialStateMissing
	if len(item.Credentials) > 0 {
		credentialState = api.ConnectionCredentialStateStaticReference
	}
	var retired *time.Time
	if item.RetiredAt != nil {
		value := item.RetiredAt.UTC()
		retired = &value
	}
	return api.Connection{Id: item.ID.String(), Kind: api.ConnectionKind(item.Kind), Label: item.Label, Endpoint: item.Endpoint, Revision: item.Revision, Source: apiSource(item.Source), CredentialState: credentialState, Health: api.ConnectionHealthUnknown, RetiredAt: retired}
}

func mapStorageRoots(items []domain.StorageRoot) []api.StorageRoot {
	result := make([]api.StorageRoot, 0, len(items))
	for _, item := range items {
		result = append(result, mapStorageRoot(item))
	}
	return result
}

func mapStorageRoot(item domain.StorageRoot) api.StorageRoot {
	permission := api.StorageRootPermissionReadWrite
	if item.ReadOnly {
		permission = api.StorageRootPermissionReadOnly
	}
	capabilities := make([]string, 0, len(item.Capabilities))
	for _, capability := range item.Capabilities {
		capabilities = append(capabilities, capability.Name+":"+string(capability.State))
	}
	var retired *time.Time
	if item.RetiredAt != nil {
		value := item.RetiredAt.UTC()
		retired = &value
	}
	return api.StorageRoot{Id: item.ID.String(), Label: item.Label, Purpose: api.StoragePurpose(item.Purpose), Path: item.Path, Revision: item.Revision, Source: apiSource(item.Source), Permission: &permission, Capabilities: capabilities, RetiredAt: retired, Watch: api.WatchSettings{Enabled: item.Watch.Enabled, IntervalSeconds: int(item.Watch.Interval / time.Second)}}
}

func mapPathMappings(items []domain.PathMapping) []api.PathMapping {
	result := make([]api.PathMapping, 0, len(items))
	for _, item := range items {
		result = append(result, mapPathMapping(item))
	}
	return result
}

func mapPathMapping(item domain.PathMapping) api.PathMapping {
	return api.PathMapping{Id: item.ID.String(), ConnectionId: item.ConnectionID.String(), SourcePrefix: item.SourcePrefix, RootId: item.RootID.String(), DestinationPrefix: item.DestinationPrefix, Revision: item.Revision, Source: apiSource(item.Source)}
}

func boolPtr(value bool) *bool { return &value }

func optionalString(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

// connectionSpec translates the generated credential union at the transport
// boundary.  Values are copied into the configuration service and cleared by
// the returned cleanup function; no generated credential DTO crosses into the
// domain or configuration packages.
func connectionSpec(body api.ConnectionCreate) (configuration.ConnectionSpec, func(), error) {
	values, cleanup, err := credentialInputs(body.Credentials)
	if err != nil {
		return configuration.ConnectionSpec{}, func() {}, err
	}
	return configuration.ConnectionSpec{ID: domain.ConfigID(body.Id), Kind: domain.ConnectionKind(body.Kind), Label: body.Label, Endpoint: body.Endpoint, Credentials: values}, cleanup, nil
}

func connectionPatch(body api.ConnectionPatch) (configuration.ConnectionPatch, func(), error) {
	values, cleanup, err := credentialInputs(body.Credentials)
	if err != nil {
		return configuration.ConnectionPatch{}, func() {}, err
	}
	return configuration.ConnectionPatch{Label: body.Label, Endpoint: body.Endpoint, Credentials: values}, cleanup, nil
}

func credentialInputs(input *api.CredentialInput) (map[string]configuration.CredentialInput, func(), error) {
	if input == nil {
		return nil, func() {}, nil
	}
	raw, err := json.Marshal(input)
	if err != nil || len(raw) == 0 || !json.Valid(raw) {
		return nil, func() {}, errors.New("credential input is invalid")
	}
	var value struct {
		Kind     string  `json:"kind"`
		APIKey   *string `json:"apiKey"`
		Token    *string `json:"token"`
		Username *string `json:"username"`
		Password *string `json:"password"`
	}
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, func() {}, errors.New("credential input is invalid")
	}
	result := make(map[string]configuration.CredentialInput)
	owned := make([][]byte, 0, 2)
	add := func(field string, source *string) error {
		if source == nil || *source == "" {
			return errors.New("credential input is invalid")
		}
		copyValue := []byte(*source)
		owned = append(owned, copyValue)
		result[field] = configuration.CredentialInput{Value: copyValue}
		return nil
	}
	switch value.Kind {
	case "api_key":
		if err := add("apiKey", value.APIKey); err != nil {
			return nil, func() {}, err
		}
	case "token":
		if err := add("token", value.Token); err != nil {
			return nil, func() {}, err
		}
	case "username_password":
		if err := add("username", value.Username); err != nil {
			return nil, func() {}, err
		}
		if err := add("password", value.Password); err != nil {
			return nil, func() {}, err
		}
	default:
		return nil, func() {}, errors.New("credential input is invalid")
	}
	cleanup := func() {
		for _, bytes := range owned {
			for index := range bytes {
				bytes[index] = 0
			}
		}
	}
	return result, cleanup, nil
}

func problemFor(ctx context.Context, err error, status int) api.Problem {
	requestID := ""
	if id, ok := ctx.Value(requestIDKey{}).(string); ok {
		requestID = id
	}
	if requestID == "" {
		requestID = "unavailable"
	}
	code := "internal_error"
	title := "Request failed"
	retryable := false
	switch {
	case errors.Is(err, ErrNotReady):
		code, title, retryable = "not_ready", "Service is not ready", true
	case errors.Is(err, ErrRouteUnavailable):
		code, title, retryable = "service_unavailable", "Service is unavailable", true
	case errors.Is(err, ErrIdempotencyStore):
		code, title, retryable = "persistence_unavailable", "Durable persistence is unavailable", true
	case errors.Is(err, ErrIdempotencyRecoveryRequired):
		code, title, retryable = "service_unavailable", "Mutation recovery is unavailable", true
	case errors.Is(err, ErrIdempotencyPending):
		code, title, retryable = "idempotency_pending", "Earlier request outcome requires reconciliation", true
	case errors.Is(err, ErrConfigurationStore):
		code, title, retryable = "persistence_unavailable", "Durable persistence is unavailable", true
	case errors.Is(err, ErrRequestTooLarge):
		code, title = "request_too_large", "Request is too large"
	case errors.Is(err, ErrUnknownField):
		code, title = "unknown_field", "Request contains an unknown field"
	case errors.Is(err, ErrDuplicateField):
		code, title = "duplicate_field", "Request contains a duplicate field"
	case errors.Is(err, ErrInvalidJSON):
		code, title = "invalid_json", "Request body is invalid"
	case errors.Is(err, ErrInvalidParameter):
		code, title = "invalid_parameter", "Request parameter is invalid"
	case errors.Is(err, ErrOriginForbidden):
		code, title = "origin_forbidden", "Request origin is not allowed"
	case errors.Is(err, ErrIdempotencyConflict):
		code, title = "idempotency_conflict", "Idempotency key conflicts with an earlier request"
	case errors.Is(err, ErrInvalidIdempotency):
		code, title = "invalid_idempotency_key", "Idempotency key is invalid"
	case errors.Is(err, ErrIdempotencyRequired):
		code, title = "idempotency_required", "Idempotency-Key is required"
	case errors.Is(err, ErrIfMatchRequired):
		code, title = "precondition_required", "If-Match precondition is required"
	case errors.Is(err, ErrIfMatchMismatch):
		code, title = "precondition_failed", "If-Match precondition failed"
	case errors.Is(err, ErrSnapshotCursor):
		code, title, retryable = "snapshot_expired", "Snapshot cursor is expired", true
	case errors.Is(err, configuration.ErrResourceNotFound):
		code, title = "resource_not_found", "Resource was not found"
	case errors.Is(err, configuration.ErrConfigSourceReadOnly):
		code, title = "configuration_read_only", "Configuration source is read-only"
	case errors.Is(err, configuration.ErrRevisionMismatch):
		code, title = "precondition_failed", "Configuration revision does not match"
	case errors.Is(err, configuration.ErrPreconditionRequired):
		code, title = "precondition_required", "A revision precondition is required"
	case errors.Is(err, configuration.ErrConfigSourceConflict), errors.Is(err, configuration.ErrMappingAmbiguous), errors.Is(err, configuration.ErrMappingInvalid):
		code, title = "configuration_conflict", "Configuration change conflicts with current state"
	case errors.Is(err, configuration.ErrInvalidDocument), errors.Is(err, configuration.ErrCredentialInvalid):
		code, title = "invalid_request", "Request is invalid"
	case errors.Is(err, context.Canceled):
		code, title, retryable = "request_canceled", "Request was canceled", true
	case errors.Is(err, context.DeadlineExceeded):
		code, title, retryable = "request_deadline", "Request deadline exceeded", true
	default:
		if status == 0 {
			status = http.StatusInternalServerError
		}
	}
	if status == 0 {
		status = statusFor(err)
	}
	return api.Problem{Type: "https://mastarr.dev/problems/" + code, Title: title, Status: status, Code: code, RequestId: requestID, Retryable: retryable}
}

func statusFor(err error) int {
	switch {
	case errors.Is(err, ErrRequestTooLarge):
		return http.StatusRequestEntityTooLarge
	case errors.Is(err, ErrOriginForbidden):
		return http.StatusForbidden
	case errors.Is(err, ErrInvalidIdempotency):
		return http.StatusUnprocessableEntity
	case errors.Is(err, ErrIdempotencyConflict), errors.Is(err, ErrSnapshotCursor), errors.Is(err, configuration.ErrConfigSourceConflict), errors.Is(err, configuration.ErrMappingAmbiguous), errors.Is(err, configuration.ErrMappingInvalid):
		return http.StatusConflict
	case errors.Is(err, ErrIdempotencyPending):
		return http.StatusConflict
	case errors.Is(err, ErrIfMatchMismatch), errors.Is(err, configuration.ErrRevisionMismatch):
		return http.StatusPreconditionFailed
	case errors.Is(err, ErrIfMatchRequired), errors.Is(err, ErrIdempotencyRequired), errors.Is(err, configuration.ErrPreconditionRequired):
		return http.StatusPreconditionRequired
	case errors.Is(err, configuration.ErrInvalidDocument), errors.Is(err, configuration.ErrCredentialInvalid), errors.Is(err, ErrInvalidJSON), errors.Is(err, ErrUnknownField), errors.Is(err, ErrDuplicateField), errors.Is(err, ErrInvalidParameter):
		return http.StatusUnprocessableEntity
	case errors.Is(err, configuration.ErrResourceNotFound):
		return http.StatusNotFound
	case errors.Is(err, ErrNotReady), errors.Is(err, ErrRouteUnavailable):
		return http.StatusServiceUnavailable
	case errors.Is(err, ErrIdempotencyStore):
		return http.StatusServiceUnavailable
	case errors.Is(err, ErrIdempotencyRecoveryRequired):
		return http.StatusServiceUnavailable
	case errors.Is(err, ErrConfigurationStore):
		return http.StatusServiceUnavailable
	case errors.Is(err, context.Canceled):
		return http.StatusRequestTimeout
	case errors.Is(err, context.DeadlineExceeded):
		return http.StatusGatewayTimeout
	default:
		return http.StatusInternalServerError
	}
}

type requestIDKey struct{}

func requestID() string {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err == nil {
		return hex.EncodeToString(raw[:])
	}
	return strconv.FormatInt(time.Now().UnixNano(), 10)
}

func writeProblem(w http.ResponseWriter, r *http.Request, err error) {
	if err == nil {
		err = errors.New("request failed")
	}
	err = normalizeError(err)
	status := statusFor(err)
	problem := problemFor(r.Context(), err, status)
	w.Header().Set("Content-Type", "application/problem+json")
	w.Header().Set("Cache-Control", "no-store")
	if id := problem.RequestId; id != "" {
		w.Header().Set("X-Request-ID", id)
	}
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(problem)
}

func normalizeError(err error) error {
	var headerErr *api.RequiredHeaderError
	if errors.As(err, &headerErr) {
		switch headerErr.ParamName {
		case "If-Match":
			return ErrIfMatchRequired
		case "Idempotency-Key":
			return ErrIdempotencyRequired
		default:
			return ErrInvalidParameter
		}
	}
	var paramErr *api.RequiredParamError
	if errors.As(err, &paramErr) {
		return ErrInvalidParameter
	}
	var formatErr *api.InvalidParamFormatError
	if errors.As(err, &formatErr) {
		return ErrInvalidParameter
	}
	var valuesErr *api.TooManyValuesForParamError
	if errors.As(err, &valuesErr) {
		return ErrInvalidParameter
	}
	return err
}

// policy is intentionally outside the generated handler: it bounds raw body
// bytes before generated decoding, applies origin policy, and preserves
// idempotent HTTP responses without changing the generated contract.
func (server *Server) policy(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Keep the persistence/owner pair stable for the whole mutation. A
		// startup setter may not attach a durable repository or remove its
		// recovery owner between this gate and the handler dispatch.
		server.idempotencyAssemblyMu.RLock()
		defer server.idempotencyAssemblyMu.RUnlock()
		id := requestID()
		ctx := context.WithValue(r.Context(), requestIDKey{}, id)
		r = r.WithContext(ctx)
		w.Header().Set("X-Request-ID", id)
		if err := server.checkOrigin(r); err != nil {
			writeProblem(w, r, err)
			return
		}
		if r.Method == http.MethodOptions {
			server.writePreflight(w, r)
			return
		}
		if err := server.prepareBody(r); err != nil {
			writeProblem(w, r, err)
			return
		}
		server.setCORS(w, r)
		if isMutation(r.Method) && server.idempotencyPersistence() != nil && (!server.recoveryOwnerReady.Load() || server.idempotencyRecoveryCallback() == nil) {
			writeProblem(w, r, ErrIdempotencyRecoveryRequired)
			return
		}
		if !isMutation(r.Method) || strings.TrimSpace(r.Header.Get("Idempotency-Key")) == "" {
			next.ServeHTTP(w, r)
			return
		}
		if err := validateIdempotencyHeader(r.Header.Get("Idempotency-Key")); err != nil {
			writeProblem(w, r, err)
			return
		}
		scope := r.Method + " " + r.URL.Path
		idempotencyKey := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
		key := scope + "\x00" + idempotencyKey
		digest := requestDigest(r)
		body := requestBody(r)
		for {
			entry, found, err := server.lookupDurableIdempotency(r.Context(), scope, idempotencyKey, digest)
			if err != nil {
				if errors.Is(err, ErrIdempotencyPending) {
					if recovered, ok, recoveryErr := server.recoverPendingIdempotency(r.Context(), scope, idempotencyKey, digest, r.Method, r.URL.Path, r.Header.Get("If-Match"), body); recoveryErr != nil {
						writeProblem(w, r, recoveryErr)
						return
					} else if ok {
						server.rememberIdempotency(key, digest, *recovered)
						replay(w, recovered)
						return
					}
				}
				writeProblem(w, r, err)
				return
			}
			if found {
				server.rememberIdempotency(key, digest, *entry)
				replay(w, entry)
				return
			}
			entry, pending, conflict := server.beginIdempotency(key, digest)
			if conflict {
				writeProblem(w, r, ErrIdempotencyConflict)
				return
			}
			if entry != nil {
				replay(w, entry)
				return
			}
			if pending == nil {
				break
			}
			select {
			case <-pending.done:
				continue
			case <-r.Context().Done():
				writeProblem(w, r, r.Context().Err())
				return
			}
		}
		attemptState := &idempotencyRequestState{scope: scope, key: idempotencyKey, digest: hex.EncodeToString(digest), method: r.Method, path: r.URL.Path, ifMatch: r.Header.Get("If-Match"), body: append([]byte(nil), body...)}
		r = r.WithContext(context.WithValue(r.Context(), idempotencyRequestStateKey{}, attemptState))
		capture := newCapture()
		// Always release the process-local reservation, including a panic in the
		// generated handler. A stuck reservation would make later requests wait
		// forever and would turn a recoverable transport failure into a denial.
		defer server.finishIdempotency(key)
		if persistence := server.idempotencyPersistence(); persistence != nil && persistence.Reserve != nil {
			acquired, err := server.reserveIdempotency(r.Context(), scope, idempotencyKey, digest, attemptState)
			if err != nil {
				writeProblem(w, r, err)
				return
			}
			if !acquired {
				// Another process won the durable reservation after the initial
				// lookup. Read it back and either replay its completed response
				// or expose the pending/uncertain state; never dispatch twice.
				entry, found, err := server.lookupDurableIdempotency(r.Context(), scope, idempotencyKey, digest)
				if err != nil {
					if errors.Is(err, ErrIdempotencyPending) {
						if recovered, ok, recoveryErr := server.recoverPendingIdempotency(r.Context(), scope, idempotencyKey, digest, r.Method, r.URL.Path, r.Header.Get("If-Match"), body); recoveryErr != nil {
							writeProblem(w, r, recoveryErr)
							return
						} else if ok {
							server.rememberIdempotency(key, digest, *recovered)
							replay(w, recovered)
							return
						}
					}
					writeProblem(w, r, err)
					return
				}
				if !found || entry == nil {
					writeProblem(w, r, ErrIdempotencyStore)
					return
				}
				server.rememberIdempotency(key, digest, *entry)
				replay(w, entry)
				return
			}
		}
		next.ServeHTTP(capture, r)
		entry := capture.entry()
		if attemptState.released.Load() {
			// The configuration transaction was rolled back and the exact
			// reservation was durably released. Return the observed failure
			// without recording it as an uncertain completion; a later request
			// with this digest may acquire a fresh attempt.
			replay(w, &entry)
			return
		}
		if server.knownPreEffectRouteFailure(r.Method, r.URL.Path, entry) && server.releaseCurrentIdempotency(r.Context()) == nil {
			// A route-level unavailable/not-ready response is produced before any
			// application service is assembled or dispatched. Release only this
			// explicitly classified no-effect reservation; arbitrary 503 and
			// persistence failures remain held for reconciliation.
			replay(w, &entry)
			return
		}
		if server.knownNoEffectConfigurationResponse(r.Method, r.URL.Path, entry) && server.releaseCurrentIdempotency(r.Context()) == nil {
			// A built-in configuration precondition/conflict is an
			// attempt-specific no-effect result. Release binds the exact digest and
			// reservation attempt, so a later retry cannot enter the read-back
			// recovery path and infer success from coincident state.
			replay(w, &entry)
			return
		}
		if persistence := server.idempotencyPersistence(); persistence != nil && persistence.Complete != nil {
			if err := server.persistIdempotency(r.Context(), scope, idempotencyKey, digest, entry); err != nil {
				writeProblem(w, r, err)
				return
			}
			if shouldRememberIdempotency(entry) {
				// The local cache is populated only after the durable
				// completion has committed.
				server.rememberIdempotency(key, digest, entry)
			}
		} else if shouldRememberIdempotency(entry) {
			// In-memory tests without a configured durable store retain the
			// historical single-process optimization.
			server.rememberIdempotency(key, digest, entry)
			if err := server.persistIdempotency(r.Context(), scope, idempotencyKey, digest, entry); err != nil {
				writeProblem(w, r, err)
				return
			}
		}
		replay(w, &entry)
	})
}

func isMutation(method string) bool {
	return method == http.MethodPost || method == http.MethodPatch || method == http.MethodPut || method == http.MethodDelete
}

func (server *Server) checkOrigin(r *http.Request) error {
	if !isMutation(r.Method) {
		return nil
	}
	if r.Header.Get("Forwarded") != "" || r.Header.Get("X-Forwarded-Host") != "" || r.Header.Get("X-Forwarded-Proto") != "" || r.Header.Get("X-Forwarded-Port") != "" {
		// Proxy headers are never trusted for authorization or URL formation.
		// A mutation carrying them is rejected rather than guessing its origin.
		return ErrOriginForbidden
	}
	origin := strings.TrimSpace(r.Header.Get("Origin"))
	if origin == "" {
		if strings.EqualFold(strings.TrimSpace(r.Header.Get("Sec-Fetch-Site")), "cross-site") {
			return ErrOriginForbidden
		}
		return nil
	}
	canonical, err := canonicalOrigin(origin)
	if err != nil || server.allowedOrigin == "" || canonical != server.allowedOrigin {
		return ErrOriginForbidden
	}
	return nil
}

func (server *Server) setCORS(w http.ResponseWriter, r *http.Request) {
	if server.allowedOrigin != "" && strings.TrimSpace(r.Header.Get("Origin")) != "" {
		if origin, err := canonicalOrigin(r.Header.Get("Origin")); err == nil && origin == server.allowedOrigin {
			w.Header().Set("Access-Control-Allow-Origin", server.allowedOrigin)
			w.Header().Add("Vary", "Origin")
		}
	}
}

func (server *Server) writePreflight(w http.ResponseWriter, r *http.Request) {
	origin, err := canonicalOrigin(r.Header.Get("Origin"))
	if err != nil || server.allowedOrigin == "" || origin != server.allowedOrigin {
		writeProblem(w, r, ErrOriginForbidden)
		return
	}
	w.Header().Set("Access-Control-Allow-Origin", server.allowedOrigin)
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PATCH, DELETE, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Idempotency-Key, If-Match")
	w.Header().Set("Access-Control-Max-Age", "600")
	w.Header().Add("Vary", "Origin")
	w.WriteHeader(http.StatusNoContent)
}

func (server *Server) prepareBody(r *http.Request) error {
	if r.Body == nil || r.Method == http.MethodGet || r.Method == http.MethodHead {
		return nil
	}
	if r.ContentLength > server.maxBodyBytes {
		return ErrRequestTooLarge
	}
	data, err := io.ReadAll(io.LimitReader(r.Body, server.maxBodyBytes+1))
	if err != nil {
		return ErrInvalidJSON
	}
	if int64(len(data)) > server.maxBodyBytes {
		return ErrRequestTooLarge
	}
	r.Body = io.NopCloser(bytes.NewReader(data))
	if len(bytes.TrimSpace(data)) == 0 {
		return nil
	}
	if !utf8.Valid(data) || !json.Valid(data) {
		return ErrInvalidJSON
	}
	if err := validateJSONSyntax(data); err != nil {
		return err
	}
	if err := validateArrayBoundsWithBytes(data, server.maxManifestEntries, server.maxManifestBytes); err != nil {
		return err
	}
	allowed := allowedBodyFields(r.URL.Path, r.Method)
	if allowed == nil {
		return nil
	}
	if err := validateTopLevelObject(data, allowed); err != nil {
		return err
	}
	if target := bodyTarget(r.URL.Path, r.Method); target != nil {
		decoder := json.NewDecoder(bytes.NewReader(data))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(target); err != nil {
			if strings.Contains(err.Error(), "unknown field") {
				return ErrUnknownField
			}
			return ErrInvalidJSON
		}
		var extra any
		if err := decoder.Decode(&extra); err != io.EOF {
			return ErrInvalidJSON
		}
	}
	return nil
}

func bodyTarget(path, method string) any {
	switch {
	case method == http.MethodPatch && strings.Contains(path, "/connections/"):
		return new(api.ConnectionPatch)
	case method == http.MethodPatch && strings.Contains(path, "/storage-roots/"):
		return new(api.StorageRootPatch)
	case method == http.MethodPatch && strings.Contains(path, "/path-mappings/"):
		return new(api.PathMappingPatch)
	case method == http.MethodPost && strings.HasSuffix(path, "/connections"):
		return new(api.ConnectionCreate)
	case method == http.MethodPost && strings.HasSuffix(path, "/storage-roots"):
		return new(api.StorageRootCreate)
	case method == http.MethodPost && strings.HasSuffix(path, "/path-mappings"):
		return new(api.PathMappingCreate)
	case method == http.MethodPost && strings.Contains(path, "/action-plans"):
		return new(api.ActionPlanCreate)
	case method == http.MethodPost && strings.Contains(path, "/connection-checks"):
		return new(api.ConnectionCheckCreate)
	case method == http.MethodPost && strings.Contains(path, "/review-decisions"):
		return new(api.ReviewDecisionCreate)
	case method == http.MethodPost && strings.HasSuffix(path, "/scans"):
		return new(api.ScanCreate)
	case method == http.MethodPost && strings.HasSuffix(path, "/workflow-runs"):
		return new(api.WorkflowRunCreate)
	case method == http.MethodPost && strings.Contains(path, "/cancellations"):
		return new(api.CancellationCreate)
	default:
		return nil
	}
}

const maxJSONDepth = 64

func validateJSONSyntax(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := scanJSONValue(decoder, 0); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return ErrInvalidJSON
	}
	return nil
}

func scanJSONValue(decoder *json.Decoder, depth int) error {
	if depth > maxJSONDepth {
		return ErrRequestTooLarge
	}
	token, err := decoder.Token()
	if err != nil {
		return ErrInvalidJSON
	}
	delim, isDelim := token.(json.Delim)
	if !isDelim {
		return nil
	}
	switch delim {
	case '{':
		seen := make(map[string]struct{})
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return ErrInvalidJSON
			}
			key, ok := keyToken.(string)
			if !ok {
				return ErrInvalidJSON
			}
			if _, exists := seen[key]; exists {
				return ErrDuplicateField
			}
			seen[key] = struct{}{}
			if err := scanJSONValue(decoder, depth+1); err != nil {
				return err
			}
		}
		end, err := decoder.Token()
		if err != nil {
			return ErrInvalidJSON
		}
		if end != json.Delim('}') {
			return ErrInvalidJSON
		}
	case '[':
		for decoder.More() {
			if err := scanJSONValue(decoder, depth+1); err != nil {
				return err
			}
		}
		end, err := decoder.Token()
		if err != nil {
			return ErrInvalidJSON
		}
		if end != json.Delim(']') {
			return ErrInvalidJSON
		}
	default:
		return ErrInvalidJSON
	}
	return nil
}

func allowedBodyFields(path, method string) map[string]struct{} {
	if method == http.MethodPatch && strings.Contains(path, "/connections/") {
		return fields("credentials", "endpoint", "label")
	}
	if method == http.MethodPatch && strings.Contains(path, "/storage-roots/") {
		return fields("label", "path", "watchEnabled", "watchIntervalSeconds")
	}
	if method == http.MethodPatch && strings.Contains(path, "/path-mappings/") {
		return fields("sourcePrefix", "destinationPrefix")
	}
	switch {
	case method == http.MethodPost && strings.HasSuffix(path, "/connections"):
		return fields("credentials", "endpoint", "id", "kind", "label")
	case method == http.MethodPost && strings.HasSuffix(path, "/storage-roots"):
		return fields("id", "label", "path", "purpose")
	case method == http.MethodPost && strings.HasSuffix(path, "/path-mappings"):
		return fields("connectionId", "destinationPrefix", "id", "rootId", "sourcePrefix")
	case method == http.MethodPost && strings.Contains(path, "/action-plans"):
		return fields("action", "expiresInSeconds")
	case method == http.MethodPost && strings.Contains(path, "/connection-checks"):
		return fields("connectionId")
	case method == http.MethodPost && strings.Contains(path, "/review-decisions"):
		return fields("decision", "digest", "irreversibleAcknowledgement", "planId", "revision", "unverifiedLabel")
	case method == http.MethodPost && strings.HasSuffix(path, "/scans"):
		return fields("connectionIds", "rootIds", "scope")
	case method == http.MethodPost && strings.HasSuffix(path, "/workflow-runs"):
		return fields("deadlineSeconds", "name", "recipeVersion", "steps")
	case method == http.MethodPost && strings.Contains(path, "/cancellations"):
		return fields("reason")
	case method == http.MethodPost && strings.Contains(path, "/reconciliations"):
		return fields("scope")
	case method == http.MethodPost && strings.Contains(path, "/retry-requests"):
		return fields("expectedRevision", "reason")
	default:
		return nil
	}
}

func fields(values ...string) map[string]struct{} {
	result := make(map[string]struct{}, len(values))
	for _, value := range values {
		result[value] = struct{}{}
	}
	return result
}

func validateTopLevelObject(data []byte, allowed map[string]struct{}) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	token, err := decoder.Token()
	if err != nil {
		return ErrInvalidJSON
	}
	delim, ok := token.(json.Delim)
	if !ok || delim != '{' {
		return ErrInvalidJSON
	}
	seen := make(map[string]struct{})
	for decoder.More() {
		keyToken, err := decoder.Token()
		if err != nil {
			return ErrInvalidJSON
		}
		key, ok := keyToken.(string)
		if !ok {
			return ErrInvalidJSON
		}
		if _, duplicate := seen[key]; duplicate {
			return ErrDuplicateField
		}
		seen[key] = struct{}{}
		if _, known := allowed[key]; !known {
			return ErrUnknownField
		}
		var raw json.RawMessage
		if err := decoder.Decode(&raw); err != nil {
			return ErrInvalidJSON
		}
	}
	if _, err := decoder.Token(); err != nil {
		return ErrInvalidJSON
	}
	if decoder.More() {
		return ErrInvalidJSON
	}
	return nil
}

func validateArrayBounds(data []byte, maximum int) error {
	return validateArrayBoundsWithBytes(data, maximum, 0)
}

func validateArrayBoundsWithBytes(data []byte, maximum int, maximumBytes int64) error {
	if maximum <= 0 {
		return ErrRequestTooLarge
	}
	if maximumBytes < 0 {
		return ErrRequestTooLarge
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	var value any
	if err := decoder.Decode(&value); err != nil {
		return ErrInvalidJSON
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return ErrInvalidJSON
	}
	limits := arrayBounds{maximumEntries: maximum, maximumBytes: maximumBytes}
	if err := limits.walk(value, ""); err != nil {
		return err
	}
	return nil
}

type arrayBounds struct {
	maximumEntries int
	maximumBytes   int64
	entries        int
	bytes          int64
}

func (limits *arrayBounds) walk(value any, field string) error {
	switch typed := value.(type) {
	case map[string]any:
		for name, child := range typed {
			if err := limits.walk(child, name); err != nil {
				return err
			}
		}
	case []any:
		if boundedCollectionField(field) {
			limits.entries += len(typed)
			if limits.entries > limits.maximumEntries {
				return ErrRequestTooLarge
			}
			encoded, err := json.Marshal(typed)
			if err != nil {
				return ErrInvalidJSON
			}
			limits.bytes += int64(len(encoded))
			if limits.maximumBytes > 0 && limits.bytes > limits.maximumBytes {
				return ErrRequestTooLarge
			}
		}
		for _, child := range typed {
			if err := limits.walk(child, ""); err != nil {
				return err
			}
		}
	}
	return nil
}

func boundedCollectionField(field string) bool {
	switch field {
	case "manifest", "files", "steps", "targets", "connectionIds", "rootIds", "descriptorIds", "clientItemIds":
		return true
	default:
		return false
	}
}

func validateIdempotencyHeader(value string) error {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 200 || !utf8.ValidString(value) {
		return ErrInvalidIdempotency
	}
	for _, char := range value {
		if char < 0x20 || char == 0x7f {
			return ErrInvalidIdempotency
		}
	}
	return nil
}

func requestDigest(r *http.Request) []byte {
	if r.Body == nil {
		r.Body = http.NoBody
	}
	data, err := io.ReadAll(r.Body)
	if err != nil {
		return nil
	}
	r.Body = io.NopCloser(bytes.NewReader(data))
	data = canonicalJSON(data)
	var canonical bytes.Buffer
	canonical.WriteString(r.Method)
	canonical.WriteByte('\n')
	canonical.WriteString(r.URL.Path)
	canonical.WriteByte('\n')
	canonical.WriteString(normalizeETag(r.Header.Get("If-Match")))
	canonical.WriteByte('\n')
	canonical.Write(data)
	digest := sha256.Sum256(canonical.Bytes())
	return digest[:]
}

func requestBody(r *http.Request) []byte {
	if r == nil || r.Body == nil {
		return nil
	}
	data, err := io.ReadAll(r.Body)
	if err != nil {
		return nil
	}
	r.Body = io.NopCloser(bytes.NewReader(data))
	return append([]byte(nil), data...)
}

// canonicalJSON normalizes object key order through encoding/json while
// preserving JSON number text exactly. Decoding into interface{} without
// UseNumber turns values around the 2^53 boundary into float64 and can make
// two distinct idempotency requests share a digest.
func canonicalJSON(data []byte) []byte {
	if !json.Valid(data) {
		return data
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return data
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return data
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return data
	}
	return encoded
}

func (server *Server) lookupIdempotency(key string, digest []byte) (*idempotencyEntry, bool) {
	server.idempotencyMu.Lock()
	defer server.idempotencyMu.Unlock()
	entry, exists := server.idempotency[key]
	if !exists {
		return nil, false
	}
	if !bytes.Equal(entry.digest, digest) {
		return nil, true
	}
	copyEntry := entry
	copyEntry.digest = append([]byte(nil), entry.digest...)
	copyEntry.header = entry.header.Clone()
	copyEntry.body = append([]byte(nil), entry.body...)
	return &copyEntry, true
}

// beginIdempotency reserves one request key. A second caller with the same
// digest waits for the first response, while a changed digest is rejected
// before the application service can be invoked.
func (server *Server) beginIdempotency(key string, digest []byte) (*idempotencyEntry, *idempotencyPending, bool) {
	server.idempotencyMu.Lock()
	defer server.idempotencyMu.Unlock()
	if entry, exists := server.idempotency[key]; exists {
		if !bytes.Equal(entry.digest, digest) {
			return nil, nil, true
		}
		copyEntry := entry
		copyEntry.digest = append([]byte(nil), entry.digest...)
		copyEntry.header = entry.header.Clone()
		copyEntry.body = append([]byte(nil), entry.body...)
		return &copyEntry, nil, false
	}
	if pending, exists := server.pending[key]; exists {
		if !bytes.Equal(pending.digest, digest) {
			return nil, nil, true
		}
		return nil, pending, false
	}
	pending := &idempotencyPending{digest: append([]byte(nil), digest...), done: make(chan struct{})}
	server.pending[key] = pending
	return nil, nil, false
}

func (server *Server) finishIdempotency(key string) {
	server.idempotencyMu.Lock()
	defer server.idempotencyMu.Unlock()
	if pending, exists := server.pending[key]; exists {
		delete(server.pending, key)
		close(pending.done)
	}
}

func (server *Server) rememberIdempotency(key string, digest []byte, entry idempotencyEntry) {
	server.idempotencyMu.Lock()
	defer server.idempotencyMu.Unlock()
	if len(server.idempotency) >= maxIdempotencyEntries {
		// Remove one deterministic key.  The cache is an optimization around
		// the durable service; a bounded cache cannot grow with caller input.
		for oldKey := range server.idempotency {
			delete(server.idempotency, oldKey)
			break
		}
	}
	entry.digest = append([]byte(nil), digest...)
	entry.header = entry.header.Clone()
	entry.body = append([]byte(nil), entry.body...)
	server.idempotency[key] = entry
}

func shouldRememberIdempotency(entry idempotencyEntry) bool {
	// A response that represents a transport/server failure must remain
	// retryable. Persisting it would turn a transient outage into a permanent
	// replay even after the dependency recovers.
	return entry.status >= http.StatusOK && entry.status < http.StatusInternalServerError && entry.status != http.StatusRequestTimeout && entry.status != http.StatusTooManyRequests
}

func (server *Server) knownPreEffectRouteFailure(method, path string, entry idempotencyEntry) bool {
	if entry.status != http.StatusServiceUnavailable || len(bytes.TrimSpace(entry.body)) == 0 {
		return false
	}
	if !server.unassembledMutationRoute(method, path) {
		return false
	}
	decoder := json.NewDecoder(bytes.NewReader(entry.body))
	decoder.DisallowUnknownFields()
	var problem api.Problem
	if err := decoder.Decode(&problem); err != nil {
		return false
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return false
	}
	// These are the stable sanitized outcomes emitted by the transport before
	// an unassembled route can invoke application code. A generic 503, upstream
	// outage, or persistence failure is deliberately excluded.
	return problem.Code == "service_unavailable" || problem.Code == "not_ready"
}

// knownNoEffectConfigurationResponse recognizes only deterministic failures
// produced by the built-in configuration handlers.  These responses are
// observed before a mutation can take effect: a stale If-Match is rejected by
// the manager CAS, and a create conflict is rejected before insertion.  An
// injected dependency is excluded because it may have performed an effect
// before returning the same HTTP status.
func (server *Server) knownNoEffectConfigurationResponse(method, path string, entry idempotencyEntry) bool {
	if server == nil || !server.builtInConfigurationMutationRoute(method, path) {
		return false
	}
	if entry.status != http.StatusPreconditionFailed && entry.status != http.StatusConflict {
		return false
	}
	decoder := json.NewDecoder(bytes.NewReader(entry.body))
	decoder.DisallowUnknownFields()
	var problem api.Problem
	if err := decoder.Decode(&problem); err != nil {
		return false
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return false
	}
	switch {
	case entry.status == http.StatusPreconditionFailed && problem.Code == "precondition_failed":
		return method == http.MethodPatch || method == http.MethodDelete
	case entry.status == http.StatusConflict && problem.Code == "configuration_conflict":
		return method == http.MethodPost || method == http.MethodPatch || method == http.MethodDelete
	default:
		return false
	}
}

func (server *Server) builtInConfigurationMutationRoute(method, path string) bool {
	if server == nil {
		return false
	}
	dependency := server.routeDependencies()
	switch {
	case method == http.MethodPost && path == "/api/v1/connections":
		return dependency == nil || dependency.CreateConnection == nil
	case method == http.MethodPatch && strings.HasPrefix(path, "/api/v1/connections/"):
		return dependency == nil || dependency.PatchConnection == nil
	case method == http.MethodDelete && strings.HasPrefix(path, "/api/v1/connections/"):
		return dependency == nil || dependency.RetireConnection == nil
	case method == http.MethodPost && path == "/api/v1/storage-roots":
		return dependency == nil || dependency.CreateStorageRoot == nil
	case method == http.MethodPatch && strings.HasPrefix(path, "/api/v1/storage-roots/"):
		return dependency == nil || dependency.PatchStorageRoot == nil
	case method == http.MethodDelete && strings.HasPrefix(path, "/api/v1/storage-roots/"):
		return dependency == nil || dependency.RetireStorageRoot == nil
	case method == http.MethodPost && path == "/api/v1/path-mappings":
		return dependency == nil || dependency.CreatePathMapping == nil
	case method == http.MethodPatch && strings.HasPrefix(path, "/api/v1/path-mappings/"):
		return dependency == nil || dependency.PatchPathMapping == nil
	case method == http.MethodDelete && strings.HasPrefix(path, "/api/v1/path-mappings/"):
		return dependency == nil || dependency.RetirePathMapping == nil
	default:
		return false
	}
}

// unassembledMutationRoute proves that the generated route returned before
// invoking an application service. Injected dependencies are deliberately
// excluded: a service may have performed an effect before returning a
// sanitized unavailable error, so that response must remain pending.
func (server *Server) unassembledMutationRoute(method, path string) bool {
	if server == nil {
		return false
	}
	dependency := server.routeDependencies()
	switch {
	case method == http.MethodPost && path == "/api/v1/action-plans":
		return dependency == nil || dependency.CreateActionPlan == nil
	case method == http.MethodPost && strings.HasPrefix(path, "/api/v1/action-plans/") && strings.HasSuffix(path, "/revisions"):
		return dependency == nil || dependency.CreateActionPlanRevision == nil
	case method == http.MethodPost && strings.HasPrefix(path, "/api/v1/action-runs/") && strings.HasSuffix(path, "/cancellations"):
		return dependency == nil || dependency.CancelActionRun == nil
	case method == http.MethodPost && strings.HasPrefix(path, "/api/v1/action-runs/") && strings.HasSuffix(path, "/reconciliations"):
		return dependency == nil || dependency.RequestActionReconciliation == nil
	case method == http.MethodPost && strings.HasPrefix(path, "/api/v1/action-runs/") && strings.HasSuffix(path, "/retry-requests"):
		return dependency == nil || dependency.RequestActionRetry == nil
	case method == http.MethodPost && path == "/api/v1/connection-checks":
		return dependency == nil || dependency.CreateConnectionCheck == nil
	case method == http.MethodPost && path == "/api/v1/connections":
		return (dependency == nil || dependency.CreateConnection == nil) && server.configurationManager() == nil
	case method == http.MethodPatch && strings.HasPrefix(path, "/api/v1/connections/"):
		return (dependency == nil || dependency.PatchConnection == nil) && server.configurationManager() == nil
	case method == http.MethodDelete && strings.HasPrefix(path, "/api/v1/connections/"):
		return (dependency == nil || dependency.RetireConnection == nil) && server.configurationManager() == nil
	case method == http.MethodPost && path == "/api/v1/storage-roots":
		return (dependency == nil || dependency.CreateStorageRoot == nil) && server.configurationManager() == nil
	case method == http.MethodPatch && strings.HasPrefix(path, "/api/v1/storage-roots/"):
		return (dependency == nil || dependency.PatchStorageRoot == nil) && server.configurationManager() == nil
	case method == http.MethodDelete && strings.HasPrefix(path, "/api/v1/storage-roots/"):
		return (dependency == nil || dependency.RetireStorageRoot == nil) && server.configurationManager() == nil
	case method == http.MethodPost && path == "/api/v1/path-mappings":
		return (dependency == nil || dependency.CreatePathMapping == nil) && server.configurationManager() == nil
	case method == http.MethodPatch && strings.HasPrefix(path, "/api/v1/path-mappings/"):
		return (dependency == nil || dependency.PatchPathMapping == nil) && server.configurationManager() == nil
	case method == http.MethodDelete && strings.HasPrefix(path, "/api/v1/path-mappings/"):
		return (dependency == nil || dependency.RetirePathMapping == nil) && server.configurationManager() == nil
	case method == http.MethodPost && path == "/api/v1/review-decisions":
		return dependency == nil || dependency.CreateReviewDecision == nil
	case method == http.MethodPost && path == "/api/v1/scans":
		return dependency == nil || dependency.CreateScan == nil
	case method == http.MethodPost && strings.HasPrefix(path, "/api/v1/scans/") && strings.HasSuffix(path, "/cancellations"):
		return dependency == nil || dependency.CancelScan == nil
	case method == http.MethodPost && path == "/api/v1/workflow-runs":
		return dependency == nil || dependency.CreateWorkflowRun == nil
	case method == http.MethodPost && strings.HasPrefix(path, "/api/v1/workflow-runs/") && strings.HasSuffix(path, "/cancellations"):
		return dependency == nil || dependency.CancelWorkflowRun == nil
	default:
		return false
	}
}

func (server *Server) lookupDurableIdempotency(ctx context.Context, scope, key string, digest []byte) (*idempotencyEntry, bool, error) {
	persistence := server.idempotencyPersistence()
	if persistence == nil || persistence.Load == nil {
		return nil, false, nil
	}
	record, found, err := persistence.Load(ctx, scope, key)
	if err != nil {
		return nil, false, ErrIdempotencyStore
	}
	if !found {
		return nil, false, nil
	}
	storedDigest, err := hex.DecodeString(record.Digest)
	if err != nil || len(storedDigest) == 0 {
		return nil, false, ErrIdempotencyStore
	}
	if !bytes.Equal(storedDigest, digest) {
		return nil, false, ErrIdempotencyConflict
	}
	if record.Pending || record.State == IdempotencyStateReserved || (record.State == IdempotencyStateCompleted && !record.Replayable) {
		return nil, false, ErrIdempotencyPending
	}
	if record.State != "" && record.State != IdempotencyStateCompleted {
		return nil, false, ErrIdempotencyStore
	}
	entry := &idempotencyEntry{digest: storedDigest, status: record.Status, header: sanitizeReplayHeaders(record.Headers), body: append([]byte(nil), record.Body...)}
	if entry.status < 100 || entry.status > 599 {
		return nil, false, ErrIdempotencyStore
	}
	return entry, true, nil
}

// recoverPendingIdempotency gives the configured production owner one
// read-only chance to reconcile a durable reservation or uncertain outcome.
// It is intentionally separate from lookup: a pending state is never treated
// as a successful response and never authorizes a second dispatch.
func (server *Server) recoverPendingIdempotency(ctx context.Context, scope, key string, digest []byte, method, path, ifMatch string, body []byte) (*idempotencyEntry, bool, error) {
	persistence := server.idempotencyPersistence()
	if persistence == nil || persistence.Load == nil || persistence.Complete == nil {
		return nil, false, nil
	}
	record, found, err := persistence.Load(ctx, scope, key)
	if err != nil {
		return nil, false, ErrIdempotencyStore
	}
	if !found {
		return nil, false, nil
	}
	storedDigest, err := hex.DecodeString(record.Digest)
	if err != nil || len(storedDigest) == 0 {
		return nil, false, ErrIdempotencyStore
	}
	if !bytes.Equal(storedDigest, digest) {
		return nil, false, ErrIdempotencyConflict
	}
	if !record.Pending && record.State != IdempotencyStateReserved && !(record.State == IdempotencyStateCompleted && !record.Replayable) {
		return nil, false, nil
	}
	recovery := server.idempotencyRecoveryCallback()
	if recovery == nil {
		return nil, false, nil
	}
	request := IdempotencyRecoveryRequest{
		Scope:                 scope,
		Key:                   key,
		Digest:                hex.EncodeToString(digest),
		Method:                method,
		Path:                  path,
		IfMatch:               ifMatch,
		Body:                  append([]byte(nil), body...),
		Record:                record,
		RequireEffectEvidence: persistence.RequireRecoveryEvidence,
	}
	terminal, ok, err := recovery(ctx, request)
	if err != nil {
		return nil, false, ErrIdempotencyStore
	}
	if !ok {
		return nil, false, nil
	}
	if terminal.Scope != scope || terminal.Key != key || terminal.Digest != request.Digest || terminal.State != IdempotencyStateCompleted {
		return nil, false, ErrIdempotencyStore
	}
	if terminal.AttemptID == "" {
		terminal.AttemptID = record.AttemptID
	}
	if terminal.AttemptID == "" || record.AttemptID != "" && terminal.AttemptID != record.AttemptID {
		return nil, false, ErrIdempotencyStore
	}
	entry := idempotencyEntry{status: terminal.Status, header: sanitizeReplayHeaders(terminal.Headers), body: append([]byte(nil), terminal.Body...)}
	if entry.status < 100 || entry.status > 599 || !shouldRememberIdempotency(entry) {
		// A non-replayable observation remains held. The owner may return it
		// as evidence through its own reconciliation surface, but it cannot
		// turn an uncertain reservation into a retryable HTTP cache entry.
		return nil, false, nil
	}
	terminal.Pending = false
	terminal.Replayable = true
	if err := persistence.Complete(ctx, terminal); err != nil {
		return nil, false, ErrIdempotencyStore
	}
	return &entry, true, nil
}

func (server *Server) releaseCurrentIdempotency(ctx context.Context) error {
	if server == nil {
		return nil
	}
	state, ok := ctx.Value(idempotencyRequestStateKey{}).(*idempotencyRequestState)
	if !ok || state == nil || state.released.Load() || state.attempt == "" {
		return nil
	}
	persistence := server.idempotencyPersistence()
	if persistence == nil || persistence.Release == nil {
		return ErrIdempotencyStore
	}
	if strings.TrimSpace(state.createdAt) == "" {
		return ErrIdempotencyStore
	}
	err := persistence.Release(ctx, IdempotencyRecord{
		Scope:        state.scope,
		Key:          state.key,
		Digest:       state.digest,
		Status:       http.StatusProcessing,
		ResourceKind: "idempotency_reservation",
		ResourceID:   stableIdempotencyResourceID(state.scope, state.key),
		CreatedAt:    state.createdAt,
		State:        IdempotencyStateReserved,
		AttemptID:    state.attempt,
	})
	if err == nil {
		state.released.Store(true)
	}
	return err
}

func (server *Server) idempotencyAttemptID(ctx context.Context) string {
	if ctx != nil {
		if state, ok := ctx.Value(idempotencyRequestStateKey{}).(*idempotencyRequestState); ok && state != nil && strings.TrimSpace(state.attempt) != "" {
			return state.attempt
		}
		if id, ok := ctx.Value(requestIDKey{}).(string); ok && strings.TrimSpace(id) != "" {
			return id
		}
	}
	return requestID()
}

func (server *Server) reserveIdempotency(ctx context.Context, scope, key string, digest []byte, state *idempotencyRequestState) (bool, error) {
	persistence := server.idempotencyPersistence()
	if persistence == nil || persistence.Reserve == nil {
		return true, nil
	}
	if len(digest) == 0 {
		return false, ErrIdempotencyStore
	}
	attempt := requestID()
	createdAt := server.now().UTC().Format(time.RFC3339Nano)
	acquired, err := persistence.Reserve(ctx, IdempotencyRecord{
		Scope:        scope,
		Key:          key,
		Digest:       hex.EncodeToString(digest),
		Status:       http.StatusProcessing,
		ResourceKind: "idempotency_reservation",
		ResourceID:   stableIdempotencyResourceID(scope, key),
		CreatedAt:    createdAt,
		State:        IdempotencyStateReserved,
		AttemptID:    attempt,
	})
	if err != nil {
		if errors.Is(err, ErrIdempotencyConflict) {
			return false, ErrIdempotencyConflict
		}
		return false, ErrIdempotencyStore
	}
	if acquired && state != nil {
		state.attempt = attempt
		state.createdAt = createdAt
	}
	return acquired, nil
}

func (server *Server) persistIdempotency(ctx context.Context, scope, key string, digest []byte, entry idempotencyEntry) error {
	persistence := server.idempotencyPersistence()
	if persistence == nil {
		return nil
	}
	if len(digest) == 0 {
		return ErrIdempotencyStore
	}
	record := IdempotencyRecord{Scope: scope, Key: key, Digest: hex.EncodeToString(digest), Status: entry.status, Headers: sanitizeReplayHeaders(entry.header), Body: append([]byte(nil), entry.body...), ResourceKind: "http_response", ResourceID: stableIdempotencyResourceID(scope, key), CreatedAt: server.now().UTC().Format(time.RFC3339Nano), State: IdempotencyStateCompleted, Replayable: shouldRememberIdempotency(entry), AttemptID: server.idempotencyAttemptID(ctx)}
	if persistence.Complete != nil {
		if err := persistence.Complete(ctx, record); err != nil {
			return ErrIdempotencyStore
		}
		return nil
	}
	if persistence.Save != nil && record.Replayable {
		if err := persistence.Save(ctx, record); err != nil {
			return ErrIdempotencyStore
		}
		return nil
	}
	return nil
}

func sanitizeReplayHeaders(input http.Header) http.Header {
	result := make(http.Header)
	for key, values := range input {
		canonical := replayHeaderKey(key)
		switch canonical {
		case "Cache-Control", "Content-Type", "ETag", "Location", "Retry-After", "Vary":
			result[canonical] = append(result[canonical], values...)
		}
	}
	return result
}

// replayHeaderKey keeps ETag's conventional spelling while normalizing the
// other replay-safe headers with net/http's canonicalizer.  The standard
// library canonicalizes ETag to Etag, which would otherwise miss the explicit
// allowlist and silently drop the caller's next If-Match token.
func replayHeaderKey(key string) string {
	key = strings.TrimSpace(key)
	if strings.EqualFold(key, "etag") {
		return "ETag"
	}
	return http.CanonicalHeaderKey(key)
}

func stableIdempotencyResourceID(scope, key string) string {
	digest := sha256.Sum256([]byte(scope + "\x00" + key))
	return "http-" + hex.EncodeToString(digest[:])
}

type responseCapture struct {
	header http.Header
	status int
	body   bytes.Buffer
}

func newCapture() *responseCapture                   { return &responseCapture{header: make(http.Header)} }
func (capture *responseCapture) Header() http.Header { return capture.header }
func (capture *responseCapture) WriteHeader(status int) {
	if capture.status == 0 {
		capture.status = status
	}
}
func (capture *responseCapture) Write(data []byte) (int, error) {
	if capture.status == 0 {
		capture.status = http.StatusOK
	}
	return capture.body.Write(data)
}
func (capture *responseCapture) entry() idempotencyEntry {
	status := capture.status
	if status == 0 {
		status = http.StatusOK
	}
	return idempotencyEntry{status: status, header: capture.header.Clone(), body: append([]byte(nil), capture.body.Bytes()...)}
}
func replay(w http.ResponseWriter, entry *idempotencyEntry) {
	if entry == nil {
		return
	}
	for key, values := range entry.header {
		if key == "X-Request-ID" {
			continue
		}
		for _, value := range values {
			w.Header().Add(key, value)
		}
	}
	w.WriteHeader(entry.status)
	_, _ = w.Write(entry.body)
}

// Keep compile-time proof that the implementation still satisfies the
// generated strict contract when the OpenAPI bundle changes.
var _ api.StrictServerInterface = (*Server)(nil)
