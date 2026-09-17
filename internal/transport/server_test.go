package transport

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	api "github.com/guilycst/mastarr/internal/api/generated"
	"github.com/guilycst/mastarr/internal/configuration"
	"github.com/guilycst/mastarr/internal/credentials"
	"github.com/guilycst/mastarr/internal/domain"
)

func testServer(t *testing.T, ready bool, origin string) (*Server, *configuration.Manager) {
	t.Helper()
	now := time.Date(2026, 9, 17, 10, 0, 0, 0, time.UTC)
	manager, err := configuration.New(configuration.Options{Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	server, err := New(Options{Configuration: manager, Ready: ready, AllowedOrigin: origin, Now: func() time.Time { return now }, MaxBodyBytes: 4096})
	if err != nil {
		manager.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		manager.Close()
	})
	return server, manager
}

func TestHealthSeparatesLivenessAndReadiness(t *testing.T) {
	server, _ := testServer(t, false, "")
	handler := server.Handler()

	live := httptest.NewRecorder()
	handler.ServeHTTP(live, httptest.NewRequest(http.MethodGet, "/health/live", nil))
	if live.Code != http.StatusOK {
		t.Fatalf("live status = %d, want 200", live.Code)
	}
	ready := httptest.NewRecorder()
	handler.ServeHTTP(ready, httptest.NewRequest(http.MethodGet, "/health/ready", nil))
	if ready.Code != http.StatusServiceUnavailable {
		t.Fatalf("ready status = %d, want 503", ready.Code)
	}
	if got := ready.Header().Get("Content-Type"); got != "application/problem+json" {
		t.Fatalf("ready content type = %q", got)
	}
	server.SetReady(true)
	ready = httptest.NewRecorder()
	handler.ServeHTTP(ready, httptest.NewRequest(http.MethodGet, "/health/ready", nil))
	if ready.Code != http.StatusOK {
		t.Fatalf("ready status after transition = %d, want 200", ready.Code)
	}
}

func TestUnassembledRoutesFailClosedAndAssembledRoutesDelegate(t *testing.T) {
	server, _ := testServer(t, true, "")
	if _, err := server.ListActionPlans(context.Background(), api.ListActionPlansRequestObject{}); !errors.Is(err, ErrRouteUnavailable) {
		t.Fatalf("unassembled route error = %v, want ErrRouteUnavailable", err)
	}
	called := false
	want := errors.New("synthetic action-plan service failure")
	assembled, err := New(Options{
		Now:   func() time.Time { return time.Date(2026, 9, 17, 10, 0, 0, 0, time.UTC) },
		Ready: true,
		Dependencies: &RouteDependencies{
			ListActionPlans: func(context.Context, api.ListActionPlansRequestObject) (api.ListActionPlansResponseObject, error) {
				called = true
				return nil, want
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := assembled.ListActionPlans(context.Background(), api.ListActionPlansRequestObject{}); !errors.Is(err, want) || !called {
		t.Fatalf("assembled route = called %t, error %v; want delegated synthetic error", called, err)
	}
}

func TestConfigurationConnectionETagAndIfMatch(t *testing.T) {
	server, _ := testServer(t, true, "")
	handler := server.Handler()
	body := `{"id":"qbt","kind":"qbittorrent","label":"qBittorrent","endpoint":"http://qbt.test"}`
	first := doJSON(handler, http.MethodPost, "/api/v1/connections", body, map[string]string{"Idempotency-Key": "create-1"})
	if first.Code != http.StatusCreated {
		t.Fatalf("create status = %d: %s", first.Code, first.Body.String())
	}
	var connection api.Connection
	if err := json.Unmarshal(first.Body.Bytes(), &connection); err != nil {
		t.Fatal(err)
	}
	if connection.Revision == "" || first.Header().Get("ETag") == "" {
		t.Fatalf("create did not return revision/etag: %#v %q", connection, first.Header().Get("ETag"))
	}

	get := httptest.NewRecorder()
	handler.ServeHTTP(get, httptest.NewRequest(http.MethodGet, "/api/v1/connections/qbt", nil))
	if get.Code != http.StatusOK || get.Header().Get("ETag") == "" {
		t.Fatalf("get status/etag = %d/%q", get.Code, get.Header().Get("ETag"))
	}
	patch := doJSON(handler, http.MethodPatch, "/api/v1/connections/qbt", `{"label":"new"}`, map[string]string{"Idempotency-Key": "patch-1", "If-Match": `"stale"`})
	if patch.Code != http.StatusPreconditionFailed {
		t.Fatalf("stale patch status = %d: %s", patch.Code, patch.Body.String())
	}
	if got := patch.Header().Get("Content-Type"); got != "application/problem+json" {
		t.Fatalf("stale patch content type = %q", got)
	}
	weak := doJSON(handler, http.MethodPatch, "/api/v1/connections/qbt", `{"label":"weak"}`, map[string]string{"Idempotency-Key": "patch-weak", "If-Match": `W/"` + connection.Revision + `"`})
	if weak.Code != http.StatusPreconditionFailed {
		t.Fatalf("weak patch status = %d: %s", weak.Code, weak.Body.String())
	}
	getAfterWeak := httptest.NewRecorder()
	handler.ServeHTTP(getAfterWeak, httptest.NewRequest(http.MethodGet, "/api/v1/connections/qbt", nil))
	if getAfterWeak.Code != http.StatusOK || strings.Contains(getAfterWeak.Body.String(), `"label":"weak"`) {
		t.Fatalf("weak patch changed configuration: %d %s", getAfterWeak.Code, getAfterWeak.Body.String())
	}
}

func TestDurableMutationsRequireAnExplicitRecoveryOwner(t *testing.T) {
	store := newTestDurableIdempotency()
	mutation := func(context.Context, api.CreateActionPlanRequestObject) (api.CreateActionPlanResponseObject, error) {
		return nil, nil
	}
	if _, err := New(Options{
		Dependencies:           &RouteDependencies{CreateActionPlan: mutation},
		IdempotencyPersistence: store.persistence(),
		Ready:                  true,
	}); !errors.Is(err, ErrIdempotencyRecoveryRequired) {
		t.Fatalf("durable mutation without owner = %v, want %v", err, ErrIdempotencyRecoveryRequired)
	}
	if _, err := New(Options{
		Dependencies:           &RouteDependencies{CreateActionPlan: mutation},
		IdempotencyPersistence: store.persistence(),
		IdempotencyRecovery:    testPendingRecovery,
		Ready:                  true,
	}); err != nil {
		t.Fatalf("durable mutation with owner = %v", err)
	}
	if _, err := New(Options{
		Dependencies: &RouteDependencies{ListActionPlans: func(context.Context, api.ListActionPlansRequestObject) (api.ListActionPlansResponseObject, error) {
			return nil, nil
		}},
		IdempotencyPersistence: store.persistence(),
		Ready:                  true,
	}); err != nil {
		t.Fatalf("durable read dependency = %v", err)
	}
}

func TestDurableMutationSettersKeepRecoveryAssemblyAtomic(t *testing.T) {
	store := newTestDurableIdempotency()
	var dispatches atomic.Int32
	dependency := func(context.Context, api.CreateConnectionRequestObject) (api.CreateConnectionResponseObject, error) {
		dispatches.Add(1)
		return syntheticConnectionResponse("late-owner"), nil
	}
	server, err := New(Options{
		Dependencies: &RouteDependencies{CreateConnection: dependency},
		Ready:        true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := server.SetIdempotencyPersistence(store.persistence()); !errors.Is(err, ErrIdempotencyRecoveryRequired) {
		t.Fatalf("late persistence without owner = %v, want %v", err, ErrIdempotencyRecoveryRequired)
	}
	if server.idempotencyPersistence() != nil {
		t.Fatal("rejected late persistence replaced the previous nil store")
	}
	if err := server.SetIdempotencyPersistence(&IdempotencyPersistence{Load: store.load}); err == nil {
		t.Fatal("incomplete late persistence was accepted")
	}
	if err := server.SetIdempotencyRecovery(testPendingRecovery); err != nil {
		t.Fatalf("install recovery owner = %v", err)
	}
	if err := server.SetIdempotencyPersistence(store.persistence()); err != nil {
		t.Fatalf("late persistence with owner = %v", err)
	}
	if err := server.SetIdempotencyRecovery(nil); !errors.Is(err, ErrIdempotencyRecoveryRequired) {
		t.Fatalf("removing live recovery owner = %v, want %v", err, ErrIdempotencyRecoveryRequired)
	}
	response := doJSON(server.Handler(), http.MethodPost, "/api/v1/connections", `{"id":"late-owner","kind":"qbittorrent","label":"late-owner","endpoint":"http://qbt.test"}`, map[string]string{"Idempotency-Key": "late-owner"})
	if response.Code != http.StatusCreated || dispatches.Load() != 1 {
		t.Fatalf("request after rejected owner removal = %d dispatches=%d: %s", response.Code, dispatches.Load(), response.Body.String())
	}
	if err := server.SetIdempotencyRecovery(func(context.Context, IdempotencyRecoveryRequest) (IdempotencyRecord, bool, error) {
		return IdempotencyRecord{}, false, nil
	}); err != nil {
		t.Fatalf("replace recovery owner = %v", err)
	}
	second := doJSON(server.Handler(), http.MethodPost, "/api/v1/connections", `{"id":"late-owner-2","kind":"qbittorrent","label":"late-owner-2","endpoint":"http://qbt.test"}`, map[string]string{"Idempotency-Key": "late-owner-2"})
	if second.Code != http.StatusCreated || dispatches.Load() != 2 {
		t.Fatalf("request after owner replacement = %d dispatches=%d: %s", second.Code, dispatches.Load(), second.Body.String())
	}
}

func TestBuiltInConfigurationRecoveryOwnerCannotBeRemovedWithDurablePersistence(t *testing.T) {
	store := newTestDurableIdempotency()
	server, err := New(Options{IdempotencyPersistence: store.persistence(), Ready: true})
	if err != nil {
		t.Fatal(err)
	}
	if server.idempotencyRecoveryCallback() == nil || !server.recoveryOwnerReady.Load() {
		t.Fatal("built-in configuration recovery owner was not ready")
	}
	if err := server.SetIdempotencyRecovery(nil); !errors.Is(err, ErrIdempotencyRecoveryRequired) {
		t.Fatalf("removing built-in recovery owner = %v, want %v", err, ErrIdempotencyRecoveryRequired)
	}
	if server.idempotencyRecoveryCallback() == nil || !server.recoveryOwnerReady.Load() {
		t.Fatal("failed built-in owner removal left a nil or unready recovery owner")
	}

	withoutPersistence, err := New(Options{Ready: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := withoutPersistence.SetIdempotencyRecovery(nil); err != nil {
		t.Fatalf("removing owner before durable startup = %v", err)
	}
	if err := withoutPersistence.SetIdempotencyPersistence(store.persistence()); !errors.Is(err, ErrIdempotencyRecoveryRequired) {
		t.Fatalf("attaching durable persistence without an effective owner = %v, want %v", err, ErrIdempotencyRecoveryRequired)
	}
	if withoutPersistence.idempotencyPersistence() != nil {
		t.Fatal("rejected durable persistence replaced the previous nil store")
	}
}

func TestBuiltInConfigurationRecoveryOwnerRemovalIsRaceSafe(t *testing.T) {
	for iteration := 0; iteration < 20; iteration++ {
		server, err := New(Options{IdempotencyPersistence: newTestDurableIdempotency().persistence(), Ready: true})
		if err != nil {
			t.Fatal(err)
		}
		start := make(chan struct{})
		var wg sync.WaitGroup
		var failures atomic.Int32
		for worker := 0; worker < 8; worker++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				if err := server.SetIdempotencyRecovery(nil); !errors.Is(err, ErrIdempotencyRecoveryRequired) {
					failures.Add(1)
				}
				if server.idempotencyRecoveryCallback() == nil || !server.recoveryOwnerReady.Load() {
					failures.Add(1)
				}
			}()
		}
		close(start)
		wg.Wait()
		if failures.Load() != 0 {
			t.Fatalf("iteration %d observed a nil or unready built-in owner", iteration)
		}
		if server.idempotencyRecoveryCallback() == nil || !server.recoveryOwnerReady.Load() {
			t.Fatalf("iteration %d ended without an effective recovery owner", iteration)
		}
	}
}

func TestConfigurationRecoveryAuthenticatesManagedCredentialIdentity(t *testing.T) {
	crypt, err := credentials.NewManager(bytes.Repeat([]byte{0x31}, 32))
	if err != nil {
		t.Fatal(err)
	}
	store := configuration.NewMemoryCredentialStore()
	manager, err := configuration.New(configuration.Options{CredentialManager: crypt, CredentialStore: store})
	if err != nil {
		crypt.Close()
		t.Fatal(err)
	}
	defer manager.Close()
	connection, err := manager.CreateConnection(context.Background(), configuration.ConnectionSpec{
		ID:       "managed-recovery",
		Kind:     domain.ConnectionQBittorrent,
		Label:    "Managed",
		Endpoint: "http://qbt.test",
		Credentials: map[string]configuration.CredentialInput{
			"apiKey": {Value: []byte("synthetic-api-key")},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	server, err := New(Options{Configuration: manager, ManagedCredentialIDs: []domain.ConfigID{connection.ID}, Now: time.Now, Ready: true})
	if err != nil {
		t.Fatal(err)
	}
	recovery := func(body string, method string) (IdempotencyRecord, bool) {
		path := "/api/v1/connections/managed-recovery"
		if method == http.MethodPost {
			path = "/api/v1/connections"
		}
		result, ok, recoveryErr := server.recoverConfigurationIdempotency(context.Background(), IdempotencyRecoveryRequest{
			Scope:   method + " " + path,
			Key:     "credential-recovery",
			Digest:  strings.Repeat("ab", 32),
			Method:  method,
			Path:    path,
			IfMatch: quoteETagValue(connection.Revision),
			Body:    []byte(body),
			Record: IdempotencyRecord{
				Scope:     method + " " + path,
				Key:       "credential-recovery",
				Digest:    strings.Repeat("ab", 32),
				State:     IdempotencyStateReserved,
				AttemptID: "attempt-credential",
				CreatedAt: "2026-09-17T10:00:00Z",
			},
		})
		if recoveryErr != nil {
			t.Fatalf("credential recovery error = %v", recoveryErr)
		}
		return result, ok
	}
	createBody := `{"id":"managed-recovery","kind":"qbittorrent","label":"Managed","endpoint":"http://qbt.test","credentials":{"kind":"api_key","apiKey":"synthetic-api-key"}}`
	created, ok := recovery(createBody, http.MethodPost)
	if !ok || created.Status != http.StatusCreated || !created.Replayable {
		t.Fatalf("managed create recovery = %#v, ok=%t; want exact identity", created, ok)
	}
	patchBody := `{"credentials":{"kind":"api_key","apiKey":"synthetic-api-key"}}`
	patched, ok := recovery(patchBody, http.MethodPatch)
	if !ok || patched.Status != http.StatusOK || !patched.Replayable {
		t.Fatalf("managed patch recovery = %#v, ok=%t; want exact identity", patched, ok)
	}
	_, ok = recovery(strings.Replace(createBody, "synthetic-api-key", "wrong-api-key", 1), http.MethodPost)
	if ok {
		t.Fatal("managed credential mismatch recovered as successful create")
	}
	_, ok = recovery(strings.Replace(patchBody, "synthetic-api-key", "wrong-api-key", 1), http.MethodPatch)
	if ok {
		t.Fatal("managed credential mismatch recovered as successful patch")
	}
}

func TestIdempotencyReplaysSameRequestAndRejectsChangedRequest(t *testing.T) {
	server, _ := testServer(t, true, "")
	handler := server.Handler()
	body := `{"id":"qbt","kind":"qbittorrent","label":"qBittorrent","endpoint":"http://qbt.test"}`
	first := doJSON(handler, http.MethodPost, "/api/v1/connections", body, map[string]string{"Idempotency-Key": "same"})
	second := doJSON(handler, http.MethodPost, "/api/v1/connections", body, map[string]string{"Idempotency-Key": "same"})
	if first.Code != http.StatusCreated || second.Code != first.Code {
		t.Fatalf("statuses = %d/%d", first.Code, second.Code)
	}
	if !bytes.Equal(first.Body.Bytes(), second.Body.Bytes()) {
		t.Fatalf("same key did not replay body: %q != %q", first.Body.String(), second.Body.String())
	}
	changed := doJSON(handler, http.MethodPost, "/api/v1/connections", strings.Replace(body, "qBittorrent", "changed", 1), map[string]string{"Idempotency-Key": "same"})
	if changed.Code != http.StatusConflict {
		t.Fatalf("changed request status = %d: %s", changed.Code, changed.Body.String())
	}
	if !strings.Contains(changed.Body.String(), "idempotency_conflict") {
		t.Fatalf("changed request problem = %s", changed.Body.String())
	}
}

func TestRetryableMutationResponsesAreNotRemembered(t *testing.T) {
	now := time.Date(2026, 9, 17, 10, 0, 0, 0, time.UTC)
	manager, err := configuration.New(configuration.Options{Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	server, err := New(Options{Now: func() time.Time { return now }, Ready: true})
	if err != nil {
		t.Fatal(err)
	}
	handler := server.Handler()
	body := `{"id":"recovered","kind":"qbittorrent","label":"Recovered","endpoint":"http://qbt.test"}`
	first := doJSON(handler, http.MethodPost, "/api/v1/connections", body, map[string]string{"Idempotency-Key": "recoverable"})
	if first.Code != http.StatusServiceUnavailable || !strings.Contains(first.Body.String(), "not_ready") {
		t.Fatalf("pre-start response = %d: %s", first.Code, first.Body.String())
	}
	server.SetConfiguration(manager)
	second := doJSON(handler, http.MethodPost, "/api/v1/connections", body, map[string]string{"Idempotency-Key": "recoverable"})
	if second.Code != http.StatusCreated {
		t.Fatalf("recovered response = %d: %s", second.Code, second.Body.String())
	}
}

func TestIdempotencySerializesConcurrentMutation(t *testing.T) {
	server, _ := testServer(t, true, "")
	handler := server.Handler()
	body := `{"id":"qbt","kind":"qbittorrent","label":"qBittorrent","endpoint":"http://qbt.test"}`
	const callers = 12
	responses := make(chan *httptest.ResponseRecorder, callers)
	for range callers {
		go func() {
			responses <- doJSON(handler, http.MethodPost, "/api/v1/connections", body, map[string]string{"Idempotency-Key": "concurrent"})
		}()
	}
	for range callers {
		response := <-responses
		if response.Code != http.StatusCreated {
			t.Fatalf("concurrent status = %d: %s", response.Code, response.Body.String())
		}
	}
}

func TestBoundedStrictJSONAndOriginPolicy(t *testing.T) {
	server, _ := testServer(t, true, "https://ui.test")
	handler := server.Handler()
	missingKey := doJSON(handler, http.MethodPost, "/api/v1/connections", `{"id":"missing-key","kind":"qbittorrent","label":"x","endpoint":"http://qbt.test"}`, nil)
	if missingKey.Code != http.StatusPreconditionRequired || !strings.Contains(missingKey.Body.String(), "idempotency_required") {
		t.Fatalf("missing idempotency response = %d %s", missingKey.Code, missingKey.Body.String())
	}
	unknown := doJSON(handler, http.MethodPost, "/api/v1/connections", `{"id":"qbt","kind":"qbittorrent","label":"x","endpoint":"http://qbt.test","unexpected":true}`, map[string]string{"Idempotency-Key": "unknown"})
	if unknown.Code != http.StatusUnprocessableEntity || !strings.Contains(unknown.Body.String(), "unknown_field") {
		t.Fatalf("unknown field response = %d %s", unknown.Code, unknown.Body.String())
	}
	large := doJSON(handler, http.MethodPost, "/api/v1/connections", `{"id":"qbt","kind":"qbittorrent","label":"`+strings.Repeat("x", 5000)+`","endpoint":"http://qbt.test"}`, map[string]string{"Idempotency-Key": "large"})
	if large.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("large response = %d %s", large.Code, large.Body.String())
	}
	forged := doJSON(handler, http.MethodPost, "/api/v1/connections", `{"id":"qbt","kind":"qbittorrent","label":"x","endpoint":"http://qbt.test"}`, map[string]string{"Idempotency-Key": "origin", "Origin": "https://evil.test"})
	if forged.Code != http.StatusForbidden || !strings.Contains(forged.Body.String(), "origin_forbidden") {
		t.Fatalf("forged origin response = %d %s", forged.Code, forged.Body.String())
	}
	allowed := doJSON(handler, http.MethodPost, "/api/v1/connections", `{"id":"qbt","kind":"qbittorrent","label":"x","endpoint":"http://qbt.test"}`, map[string]string{"Idempotency-Key": "origin-ok", "Origin": "https://ui.test"})
	if allowed.Code != http.StatusCreated || allowed.Header().Get("Access-Control-Allow-Origin") != "https://ui.test" {
		t.Fatalf("allowed origin response = %d %q", allowed.Code, allowed.Header().Get("Access-Control-Allow-Origin"))
	}
	duplicate := doJSON(handler, http.MethodPost, "/api/v1/connections", `{"id":"duplicate","kind":"qbittorrent","label":"x","endpoint":"http://qbt.test","credentials":{"kind":"api_key","apiKey":"one","apiKey":"two"}}`, map[string]string{"Idempotency-Key": "duplicate"})
	if duplicate.Code != http.StatusUnprocessableEntity || !strings.Contains(duplicate.Body.String(), "duplicate_field") {
		t.Fatalf("nested duplicate response = %d %s", duplicate.Code, duplicate.Body.String())
	}
	nestedUnknown := doJSON(handler, http.MethodPost, "/api/v1/workflow-runs", `{"name":"import","steps":[{"id":"step-1","actionPlanId":"00000000-0000-0000-0000-000000000001","unexpected":true}]}`, map[string]string{"Idempotency-Key": "nested-unknown"})
	if nestedUnknown.Code != http.StatusUnprocessableEntity || !strings.Contains(nestedUnknown.Body.String(), "unknown_field") {
		t.Fatalf("nested unknown response = %d %s", nestedUnknown.Code, nestedUnknown.Body.String())
	}
	invalidLimit := httptest.NewRecorder()
	handler.ServeHTTP(invalidLimit, httptest.NewRequest(http.MethodGet, "/api/v1/connections?limit=1001", nil))
	if invalidLimit.Code != http.StatusUnprocessableEntity || !strings.Contains(invalidLimit.Body.String(), "invalid_parameter") {
		t.Fatalf("invalid limit response = %d %s", invalidLimit.Code, invalidLimit.Body.String())
	}
}

func TestRecursiveActionBoundsRejectBeforeRouteDispatch(t *testing.T) {
	server, _ := testServer(t, true, "")
	server.maxManifestEntries = 2
	server.maxManifestBytes = 64
	if err := validateArrayBoundsWithBytes([]byte(`{"action":{"files":[{"path":"a"},{"path":"b"},{"path":"c"}]}}`), server.maxManifestEntries, server.maxManifestBytes); !errors.Is(err, ErrRequestTooLarge) {
		t.Fatalf("nested manifest bounds error = %v", err)
	}
	if err := validateArrayBoundsWithBytes([]byte(`{"action":{"files":[{"path":"this path is deliberately larger than the byte budget"}]}}`), server.maxManifestEntries, server.maxManifestBytes); !errors.Is(err, ErrRequestTooLarge) {
		t.Fatalf("nested manifest byte bounds error = %v", err)
	}
}

func TestConfigurationFailureReloadsDurableManagerBeforeUnlock(t *testing.T) {
	now := time.Date(2026, 9, 17, 10, 0, 0, 0, time.UTC)
	candidate, err := configuration.New(configuration.Options{Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	defer candidate.Close()
	reloaded, err := configuration.New(configuration.Options{Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	defer reloaded.Close()

	var fail atomic.Bool
	fail.Store(true)
	var reloads atomic.Int32
	persistence := &ConfigurationPersistence{CreateConnection: func(ctx context.Context, draft domain.Connection, mutate func(context.Context) (domain.Connection, error)) (domain.Connection, error) {
		value, mutateErr := mutate(ctx)
		if fail.CompareAndSwap(true, false) {
			// The callback models a storage transaction that rolled back after
			// the manager produced a candidate. The recovery callback must make
			// that candidate unobservable before the write gate is released.
			return value, ErrConfigurationStore
		}
		return value, mutateErr
	}}
	server, err := New(Options{
		Configuration:            candidate,
		ConfigurationPersistence: persistence,
		ConfigurationReload: func(context.Context) (*configuration.Manager, []domain.ConfigID, error) {
			reloads.Add(1)
			return reloaded, nil, nil
		},
		Ready: true,
		Now:   func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	body := api.ConnectionCreate{Id: "reloaded", Kind: api.ConnectionCreateKindQbittorrent, Label: "reloaded", Endpoint: "http://qbt.test"}
	if _, err := server.CreateConnection(context.Background(), api.CreateConnectionRequestObject{Body: &body}); !errors.Is(err, ErrConfigurationStore) {
		t.Fatalf("failed create error = %v, want ErrConfigurationStore", err)
	}
	if got := reloads.Load(); got != 1 {
		t.Fatalf("reload calls = %d, want 1", got)
	}
	connections, err := server.ListConnections(context.Background(), api.ListConnectionsRequestObject{})
	if err != nil {
		t.Fatal(err)
	}
	page, ok := connections.(api.ListConnections200JSONResponse)
	if !ok {
		t.Fatalf("list response type = %T, want 200 response", connections)
	}
	if len(page.Body.Items) != 0 {
		t.Fatalf("failed candidate remained visible: %#v", page.Body.Items)
	}
	if _, err := server.CreateConnection(context.Background(), api.CreateConnectionRequestObject{Body: &body}); err != nil {
		t.Fatalf("retry after durable reload = %v", err)
	}
}

func TestRequestDigestPreservesJSONNumberText(t *testing.T) {
	request := func(body string) *http.Request {
		return httptest.NewRequest(http.MethodPost, "/api/v1/connections", strings.NewReader(body))
	}
	low := request(`{"value":9007199254740992}`)
	high := request(`{"value":9007199254740993}`)
	if bytes.Equal(requestDigest(low), requestDigest(high)) {
		t.Fatal("2^53-adjacent JSON numbers produced the same request digest")
	}
	ordered := request(`{"b":1,"value":9007199254740993}`)
	reordered := request(`{"value":9007199254740993,"b":1}`)
	if !bytes.Equal(requestDigest(ordered), requestDigest(reordered)) {
		t.Fatal("equivalent object key order produced different request digests")
	}
}

func TestDurableIdempotencyReservationSurvivesLostCompletion(t *testing.T) {
	store := newTestDurableIdempotency()
	store.failCompletion.Store(true)
	var dispatches atomic.Int32
	dependency := func(context.Context, api.CreateConnectionRequestObject) (api.CreateConnectionResponseObject, error) {
		dispatches.Add(1)
		return syntheticConnectionResponse("durable"), nil
	}
	options := func() Options {
		return Options{
			Ready:                  true,
			Dependencies:           &RouteDependencies{CreateConnection: dependency},
			IdempotencyPersistence: store.persistence(),
			IdempotencyRecovery:    testPendingRecovery,
		}
	}
	body := `{"id":"durable","kind":"qbittorrent","label":"durable","endpoint":"http://qbt.test"}`
	firstServer, err := New(options())
	if err != nil {
		t.Fatal(err)
	}
	first := doJSON(firstServer.Handler(), http.MethodPost, "/api/v1/connections", body, map[string]string{"Idempotency-Key": "lost-completion"})
	if first.Code != http.StatusServiceUnavailable || !strings.Contains(first.Body.String(), "persistence_unavailable") {
		t.Fatalf("lost completion response = %d: %s", first.Code, first.Body.String())
	}
	if got := dispatches.Load(); got != 1 {
		t.Fatalf("dispatches after lost completion = %d, want 1", got)
	}

	secondServer, err := New(options())
	if err != nil {
		t.Fatal(err)
	}
	second := doJSON(secondServer.Handler(), http.MethodPost, "/api/v1/connections", body, map[string]string{"Idempotency-Key": "lost-completion"})
	if second.Code != http.StatusConflict || !strings.Contains(second.Body.String(), "idempotency_pending") {
		t.Fatalf("pending restart response = %d: %s", second.Code, second.Body.String())
	}
	if got := dispatches.Load(); got != 1 {
		t.Fatalf("pending restart dispatched again = %d", got)
	}
	store.completeLast(t)

	thirdServer, err := New(options())
	if err != nil {
		t.Fatal(err)
	}
	third := doJSON(thirdServer.Handler(), http.MethodPost, "/api/v1/connections", body, map[string]string{"Idempotency-Key": "lost-completion"})
	if third.Code != http.StatusCreated || !bytes.Contains(third.Body.Bytes(), []byte(`"id":"durable"`)) {
		t.Fatalf("durable completion replay = %d: %s", third.Code, third.Body.String())
	}
	if got := dispatches.Load(); got != 1 {
		t.Fatalf("durable replay dispatched again = %d", got)
	}
}

func TestDurableIdempotencyRecoveryOwnerCompletesWithoutRedispatch(t *testing.T) {
	store := newTestDurableIdempotency()
	store.failCompletion.Store(true)
	var dispatches atomic.Int32
	var recoveryCalls atomic.Int32
	dependency := func(context.Context, api.CreateConnectionRequestObject) (api.CreateConnectionResponseObject, error) {
		dispatches.Add(1)
		return syntheticConnectionResponse("recovered-after-readback"), nil
	}
	recovery := func(_ context.Context, request IdempotencyRecoveryRequest) (IdempotencyRecord, bool, error) {
		recoveryCalls.Add(1)
		if request.Method != http.MethodPost || request.Path != "/api/v1/connections" || len(request.Body) == 0 || request.Record.AttemptID == "" {
			return IdempotencyRecord{}, false, nil
		}
		recovered := request.Record
		recovered.State = IdempotencyStateCompleted
		recovered.Status = http.StatusCreated
		recovered.ResourceKind = "connection"
		recovered.ResourceID = "recovered-after-readback"
		recovered.Replayable = true
		recovered.Body = []byte(`{"id":"recovered-after-readback"}` + "\n")
		return recovered, true, nil
	}
	options := func() Options {
		return Options{
			Ready:                  true,
			Dependencies:           &RouteDependencies{CreateConnection: dependency},
			IdempotencyPersistence: store.persistence(),
			IdempotencyRecovery:    recovery,
		}
	}
	body := `{"id":"recovered-after-readback","kind":"qbittorrent","label":"recovered-after-readback","endpoint":"http://qbt.test"}`
	firstServer, err := New(options())
	if err != nil {
		t.Fatal(err)
	}
	first := doJSON(firstServer.Handler(), http.MethodPost, "/api/v1/connections", body, map[string]string{"Idempotency-Key": "reconcile-me"})
	if first.Code != http.StatusServiceUnavailable || !strings.Contains(first.Body.String(), "persistence_unavailable") {
		t.Fatalf("initial lost completion response = %d: %s", first.Code, first.Body.String())
	}
	store.failCompletion.Store(false)
	secondServer, err := New(options())
	if err != nil {
		t.Fatal(err)
	}
	second := doJSON(secondServer.Handler(), http.MethodPost, "/api/v1/connections", body, map[string]string{"Idempotency-Key": "reconcile-me"})
	if second.Code != http.StatusCreated || !strings.Contains(second.Body.String(), "recovered-after-readback") {
		t.Fatalf("reconciled response = %d: %s", second.Code, second.Body.String())
	}
	if got := dispatches.Load(); got != 1 {
		t.Fatalf("reconciled dispatches = %d, want 1", got)
	}
	if got := recoveryCalls.Load(); got != 1 {
		t.Fatalf("recovery calls = %d, want 1", got)
	}
}

func TestBuiltInRejectedConfigurationOutcomeReleasesExactAttempt(t *testing.T) {
	store := newTestDurableIdempotency()
	store.failCompletion.Store(true)
	now := time.Date(2026, 9, 17, 10, 0, 0, 0, time.UTC)
	manager, err := configuration.New(configuration.Options{Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	connection, err := manager.CreateConnection(context.Background(), configuration.ConnectionSpec{ID: "rejected-patch", Kind: domain.ConnectionQBittorrent, Label: "before", Endpoint: "http://qbt.test"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.UpdateConnection(context.Background(), connection.ID, connection.Revision, configuration.ConnectionPatch{Label: stringPtr("after")}); err != nil {
		t.Fatal(err)
	}
	server, err := New(Options{Configuration: manager, IdempotencyPersistence: store.persistence(), Now: func() time.Time { return now }, Ready: true})
	if err != nil {
		t.Fatal(err)
	}
	body := `{"label":"after"}`
	request := func() *httptest.ResponseRecorder {
		return doJSON(server.Handler(), http.MethodPatch, "/api/v1/connections/rejected-patch", body, map[string]string{
			"Idempotency-Key": "stale-patch",
			"If-Match":        quoteETagValue(connection.Revision),
		})
	}
	first := request()
	if first.Code != http.StatusPreconditionFailed || !strings.Contains(first.Body.String(), "precondition_failed") {
		t.Fatalf("stale patch response = %d: %s", first.Code, first.Body.String())
	}
	store.mu.Lock()
	released := cloneTestIdempotencyRecord(store.records["PATCH /api/v1/connections/rejected-patch\x00stale-patch"])
	store.mu.Unlock()
	if released.State != IdempotencyStateReleased || released.AttemptID == "" || released.CreatedAt == "" {
		t.Fatalf("stale patch reservation = %#v; want exact released attempt", released)
	}
	store.failCompletion.Store(false)
	second := request()
	if second.Code != http.StatusPreconditionFailed || strings.Contains(second.Body.String(), "persistence_unavailable") {
		t.Fatalf("stale patch retry = %d: %s; want rejection, never recovery success", second.Code, second.Body.String())
	}
}

func TestBuiltInDuplicateCreateDoesNotRecoverAsCreated(t *testing.T) {
	store := newTestDurableIdempotency()
	store.failCompletion.Store(true)
	now := time.Date(2026, 9, 17, 10, 0, 0, 0, time.UTC)
	manager, err := configuration.New(configuration.Options{Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	if _, err := manager.CreateConnection(context.Background(), configuration.ConnectionSpec{ID: "duplicate-create", Kind: domain.ConnectionQBittorrent, Label: "existing", Endpoint: "http://qbt.test"}); err != nil {
		t.Fatal(err)
	}
	server, err := New(Options{Configuration: manager, IdempotencyPersistence: store.persistence(), Now: func() time.Time { return now }, Ready: true})
	if err != nil {
		t.Fatal(err)
	}
	body := `{"id":"duplicate-create","kind":"qbittorrent","label":"existing","endpoint":"http://qbt.test"}`
	request := func() *httptest.ResponseRecorder {
		return doJSON(server.Handler(), http.MethodPost, "/api/v1/connections", body, map[string]string{"Idempotency-Key": "duplicate-create"})
	}
	first := request()
	if first.Code != http.StatusConflict || !strings.Contains(first.Body.String(), "configuration_conflict") {
		t.Fatalf("duplicate create response = %d: %s", first.Code, first.Body.String())
	}
	store.mu.Lock()
	released := cloneTestIdempotencyRecord(store.records["POST /api/v1/connections\x00duplicate-create"])
	store.mu.Unlock()
	if released.State != IdempotencyStateReleased || released.AttemptID == "" {
		t.Fatalf("duplicate create reservation = %#v; want exact released attempt", released)
	}
	store.failCompletion.Store(false)
	second := request()
	if second.Code != http.StatusConflict || strings.Contains(second.Body.String(), `"status":201`) {
		t.Fatalf("duplicate create retry = %d: %s; want conflict, never recovery create", second.Code, second.Body.String())
	}
}

func TestDefaultConfigurationRecoveryOwnerReadsBackWithoutRedispatch(t *testing.T) {
	now := time.Date(2026, 9, 17, 10, 0, 0, 0, time.UTC)
	manager, err := configuration.New(configuration.Options{Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Close() })
	store := newTestDurableIdempotency()
	store.failCompletion.Store(true)
	options := func() Options {
		return Options{
			Configuration:          manager,
			Ready:                  true,
			Now:                    func() time.Time { return now },
			IdempotencyPersistence: store.persistence(),
		}
	}
	body := `{"id":"default-recovery","kind":"qbittorrent","label":"default-recovery","endpoint":"http://qbt.test"}`
	firstServer, err := New(options())
	if err != nil {
		t.Fatal(err)
	}
	first := doJSON(firstServer.Handler(), http.MethodPost, "/api/v1/connections", body, map[string]string{"Idempotency-Key": "default-recovery"})
	if first.Code != http.StatusServiceUnavailable || !strings.Contains(first.Body.String(), "persistence_unavailable") {
		t.Fatalf("initial lost completion = %d: %s", first.Code, first.Body.String())
	}
	store.failCompletion.Store(false)
	secondServer, err := New(options())
	if err != nil {
		t.Fatal(err)
	}
	second := doJSON(secondServer.Handler(), http.MethodPost, "/api/v1/connections", body, map[string]string{"Idempotency-Key": "default-recovery"})
	if second.Code != http.StatusCreated || !strings.Contains(second.Body.String(), "default-recovery") {
		t.Fatalf("read-back recovery = %d: %s", second.Code, second.Body.String())
	}
	store.mu.Lock()
	record := cloneTestIdempotencyRecord(store.records["POST /api/v1/connections\x00default-recovery"])
	store.mu.Unlock()
	if record.State != IdempotencyStateCompleted || !record.Replayable || record.AttemptID == "" {
		t.Fatalf("recovered durable record = %#v; want completed attempt", record)
	}
}

func TestDefaultPatchRecoveryRecognizesPostMutationRevision(t *testing.T) {
	now := time.Date(2026, 9, 17, 10, 0, 0, 0, time.UTC)
	type patchCase struct {
		name  string
		setup func(*configuration.Manager) (path, body, ifMatch, marker string, err error)
		read  func(*configuration.Manager) (string, error)
	}
	cases := []patchCase{
		{
			name: "connection",
			setup: func(manager *configuration.Manager) (string, string, string, string, error) {
				connection, err := manager.CreateConnection(context.Background(), configuration.ConnectionSpec{
					ID:       domain.ConfigID("patch-connection"),
					Kind:     domain.ConnectionQBittorrent,
					Label:    "before",
					Endpoint: "http://qbt.test",
				})
				return "/api/v1/connections/patch-connection", `{"label":"after"}`, quoteETagValue(connection.Revision), `"label":"after"`, err
			},
			read: func(manager *configuration.Manager) (string, error) {
				connection, err := manager.GetConnection(context.Background(), domain.ConfigID("patch-connection"), false)
				return connection.Revision, err
			},
		},
		{
			name: "storage-root",
			setup: func(manager *configuration.Manager) (string, string, string, string, error) {
				root, err := manager.CreateStorageRoot(context.Background(), configuration.StorageRootSpec{
					ID:      domain.ConfigID("patch-root"),
					Label:   "before",
					Purpose: domain.StorageLibrary,
					Path:    "/synthetic/library",
				})
				return "/api/v1/storage-roots/patch-root", `{"label":"after"}`, quoteETagValue(root.Revision), `"label":"after"`, err
			},
			read: func(manager *configuration.Manager) (string, error) {
				root, err := manager.GetStorageRoot(context.Background(), domain.ConfigID("patch-root"), false)
				return root.Revision, err
			},
		},
		{
			name: "path-mapping",
			setup: func(manager *configuration.Manager) (string, string, string, string, error) {
				if _, err := manager.CreateConnection(context.Background(), configuration.ConnectionSpec{
					ID:       domain.ConfigID("mapping-connection"),
					Kind:     domain.ConnectionQBittorrent,
					Label:    "qbt",
					Endpoint: "http://qbt.test",
				}); err != nil {
					return "", "", "", "", err
				}
				if _, err := manager.CreateStorageRoot(context.Background(), configuration.StorageRootSpec{
					ID:      domain.ConfigID("mapping-root"),
					Label:   "library",
					Purpose: domain.StorageLibrary,
					Path:    "/synthetic/library",
				}); err != nil {
					return "", "", "", "", err
				}
				mapping, err := manager.CreatePathMapping(context.Background(), configuration.PathMappingSpec{
					ID:                domain.ConfigID("patch-mapping"),
					ConnectionID:      domain.ConfigID("mapping-connection"),
					SourcePrefix:      "/downloads",
					RootID:            domain.ConfigID("mapping-root"),
					DestinationPrefix: "library",
				})
				return "/api/v1/path-mappings/patch-mapping", `{"destinationPrefix":"library-v2"}`, quoteETagValue(mapping.Revision), `"destinationPrefix":"library-v2"`, err
			},
			read: func(manager *configuration.Manager) (string, error) {
				mapping, err := manager.GetPathMapping(context.Background(), domain.ConfigID("patch-mapping"), false)
				return mapping.Revision, err
			},
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			manager, err := configuration.New(configuration.Options{Now: func() time.Time { return now }})
			if err != nil {
				t.Fatal(err)
			}
			defer manager.Close()
			path, body, ifMatch, marker, err := testCase.setup(manager)
			if err != nil {
				t.Fatal(err)
			}
			before, err := testCase.read(manager)
			if err != nil {
				t.Fatal(err)
			}
			store := newTestDurableIdempotency()
			store.failCompletion.Store(true)
			options := Options{
				Configuration:          manager,
				IdempotencyPersistence: store.persistence(),
				Now:                    func() time.Time { return now },
				Ready:                  true,
			}
			firstServer, err := New(options)
			if err != nil {
				t.Fatal(err)
			}
			first := doJSON(firstServer.Handler(), http.MethodPatch, path, body, map[string]string{
				"Idempotency-Key": "patch-lost-" + testCase.name,
				"If-Match":        ifMatch,
			})
			if first.Code != http.StatusServiceUnavailable || !strings.Contains(first.Body.String(), "persistence_unavailable") {
				t.Fatalf("lost completion response = %d: %s", first.Code, first.Body.String())
			}
			after, err := testCase.read(manager)
			if err != nil {
				t.Fatal(err)
			}
			if after == before {
				t.Fatalf("mutation revision did not change: %q", after)
			}
			snapshot, err := manager.Snapshot(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			reloaded, err := configuration.New(configuration.Options{
				Now: func() time.Time { return now },
				APIState: configuration.APIState{
					Connections:  snapshot.Connections,
					StorageRoots: snapshot.StorageRoots,
					PathMappings: snapshot.PathMappings,
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			defer reloaded.Close()
			store.failCompletion.Store(false)
			recoveryOptions := options
			recoveryOptions.Configuration = reloaded
			secondServer, err := New(recoveryOptions)
			if err != nil {
				t.Fatal(err)
			}
			second := doJSON(secondServer.Handler(), http.MethodPatch, path, body, map[string]string{
				"Idempotency-Key": "patch-lost-" + testCase.name,
				"If-Match":        ifMatch,
			})
			if second.Code != http.StatusOK || !strings.Contains(second.Body.String(), marker) || second.Header().Get("ETag") != quoteETagValue(after) {
				t.Fatalf("read-back recovery = %d etag=%q: %s; want etag %q", second.Code, second.Header().Get("ETag"), second.Body.String(), quoteETagValue(after))
			}
			final, err := testCase.read(reloaded)
			if err != nil {
				t.Fatal(err)
			}
			if final != after {
				t.Fatalf("recovery changed revision: before retry %q, after %q", after, final)
			}
			store.mu.Lock()
			record := cloneTestIdempotencyRecord(store.records["PATCH "+path+"\x00patch-lost-"+testCase.name])
			store.mu.Unlock()
			if record.State != IdempotencyStateCompleted || !record.Replayable || record.AttemptID == "" {
				t.Fatalf("recovered record = %#v; want completed original attempt", record)
			}
		})
	}
}

func TestConfigurationFailureClassifierDoesNotReleasePresentCreate(t *testing.T) {
	now := time.Date(2026, 9, 17, 10, 0, 0, 0, time.UTC)
	manager, err := configuration.New(configuration.Options{Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	connection, err := manager.CreateConnection(context.Background(), configuration.ConnectionSpec{
		ID:       domain.ConfigID("existing-create"),
		Kind:     domain.ConnectionQBittorrent,
		Label:    "existing",
		Endpoint: "http://qbt.test",
	})
	if err != nil {
		t.Fatal(err)
	}
	server, err := New(Options{Configuration: manager, Now: func() time.Time { return now }, Ready: true})
	if err != nil {
		t.Fatal(err)
	}
	state := &idempotencyRequestState{
		method: http.MethodPost,
		path:   "/api/v1/connections",
		body:   []byte(`{"id":"existing-create","kind":"qbittorrent","label":"different","endpoint":"http://qbt.test"}`),
	}
	known, materialized := server.configurationEffectMaterialized(context.WithValue(context.Background(), idempotencyRequestStateKey{}, state))
	if !known || !materialized {
		t.Fatalf("present conflicting create classified as known=%t materialized=%t; want pending", known, materialized)
	}
	if connection.ID != domain.ConfigID("existing-create") {
		t.Fatal("test setup lost existing connection")
	}
}

func TestDefaultPathMappingDeleteRecoveryUsesRetainedRevision(t *testing.T) {
	now := time.Date(2026, 9, 17, 10, 0, 0, 0, time.UTC)
	manager, err := configuration.New(configuration.Options{Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	if _, err := manager.CreateConnection(context.Background(), configuration.ConnectionSpec{
		ID:       domain.ConfigID("delete-connection"),
		Kind:     domain.ConnectionQBittorrent,
		Label:    "qbt",
		Endpoint: "http://qbt.test",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.CreateStorageRoot(context.Background(), configuration.StorageRootSpec{
		ID:      domain.ConfigID("delete-root"),
		Label:   "library",
		Purpose: domain.StorageLibrary,
		Path:    "/synthetic/library",
	}); err != nil {
		t.Fatal(err)
	}
	mapping, err := manager.CreatePathMapping(context.Background(), configuration.PathMappingSpec{
		ID:                domain.ConfigID("delete-mapping"),
		ConnectionID:      domain.ConfigID("delete-connection"),
		SourcePrefix:      "/downloads",
		RootID:            domain.ConfigID("delete-root"),
		DestinationPrefix: "library",
	})
	if err != nil {
		t.Fatal(err)
	}
	store := newTestDurableIdempotency()
	store.failCompletion.Store(true)
	options := Options{
		Configuration:          manager,
		IdempotencyPersistence: store.persistence(),
		Now:                    func() time.Time { return now },
		Ready:                  true,
	}
	firstServer, err := New(options)
	if err != nil {
		t.Fatal(err)
	}
	path := "/api/v1/path-mappings/delete-mapping"
	first := doJSON(firstServer.Handler(), http.MethodDelete, path, "", map[string]string{
		"Idempotency-Key": "delete-lost",
		"If-Match":        quoteETagValue(mapping.Revision),
	})
	if first.Code != http.StatusServiceUnavailable || !strings.Contains(first.Body.String(), "persistence_unavailable") {
		t.Fatalf("lost delete response = %d: %s", first.Code, first.Body.String())
	}
	retired, err := manager.GetPathMapping(context.Background(), domain.ConfigID("delete-mapping"), true)
	if err != nil || retired.Source.Source != domain.SourceAPI || retired.Revision != mapping.Revision {
		t.Fatalf("retained mapping = %#v, err=%v; want API tombstone with original revision", retired, err)
	}
	reloaded, err := configuration.New(configuration.Options{Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	defer reloaded.Close()
	if _, err := reloaded.CreateConnection(context.Background(), configuration.ConnectionSpec{
		ID:       domain.ConfigID("delete-connection"),
		Kind:     domain.ConnectionQBittorrent,
		Label:    "qbt",
		Endpoint: "http://qbt.test",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := reloaded.CreateStorageRoot(context.Background(), configuration.StorageRootSpec{
		ID:      domain.ConfigID("delete-root"),
		Label:   "library",
		Purpose: domain.StorageLibrary,
		Path:    "/synthetic/library",
	}); err != nil {
		t.Fatal(err)
	}
	reloadedMapping, err := reloaded.CreatePathMapping(context.Background(), configuration.PathMappingSpec{
		ID:                domain.ConfigID("delete-mapping"),
		ConnectionID:      domain.ConfigID("delete-connection"),
		SourcePrefix:      "/downloads",
		RootID:            domain.ConfigID("delete-root"),
		DestinationPrefix: "library",
	})
	if err != nil {
		t.Fatal(err)
	}
	if reloadedMapping.Revision != mapping.Revision {
		t.Fatalf("reloaded mapping revision = %q, want %q", reloadedMapping.Revision, mapping.Revision)
	}
	if err := reloaded.RetirePathMapping(context.Background(), reloadedMapping.ID, reloadedMapping.Revision); err != nil {
		t.Fatal(err)
	}
	store.failCompletion.Store(false)
	recoveryOptions := options
	recoveryOptions.Configuration = reloaded
	secondServer, err := New(recoveryOptions)
	if err != nil {
		t.Fatal(err)
	}
	second := doJSON(secondServer.Handler(), http.MethodDelete, path, "", map[string]string{
		"Idempotency-Key": "delete-lost",
		"If-Match":        quoteETagValue(mapping.Revision),
	})
	if second.Code != http.StatusNoContent {
		t.Fatalf("retained-mapping recovery = %d: %s", second.Code, second.Body.String())
	}
	store.mu.Lock()
	record := cloneTestIdempotencyRecord(store.records["DELETE "+path+"\x00delete-lost"])
	store.mu.Unlock()
	if record.State != IdempotencyStateCompleted || !record.Replayable || record.AttemptID == "" {
		t.Fatalf("recovered delete record = %#v; want completed original attempt", record)
	}
}

func TestKnownPreEffectRouteFailureReleasesDurableKey(t *testing.T) {
	store := newTestDurableIdempotency()
	body := `{"id":"route-retry","kind":"qbittorrent","label":"route-retry","endpoint":"http://qbt.test"}`
	firstServer, err := New(Options{IdempotencyPersistence: store.persistence(), Now: time.Now, Ready: true})
	if err != nil {
		t.Fatal(err)
	}
	first := doJSON(firstServer.Handler(), http.MethodPost, "/api/v1/connections", body, map[string]string{"Idempotency-Key": "route-retry"})
	if first.Code != http.StatusServiceUnavailable || !strings.Contains(first.Body.String(), "not_ready") {
		t.Fatalf("unassembled route response = %d: %s", first.Code, first.Body.String())
	}
	store.mu.Lock()
	record := cloneTestIdempotencyRecord(store.records["POST /api/v1/connections\x00route-retry"])
	store.mu.Unlock()
	if record.State != IdempotencyStateReleased || record.AttemptID == "" || record.CreatedAt == "" {
		t.Fatalf("route release record = %#v; want timestamped released reservation", record)
	}
	var dispatches atomic.Int32
	secondServer, err := New(Options{
		IdempotencyPersistence: store.persistence(),
		Now:                    time.Now,
		Ready:                  true,
		Dependencies: &RouteDependencies{CreateConnection: func(context.Context, api.CreateConnectionRequestObject) (api.CreateConnectionResponseObject, error) {
			dispatches.Add(1)
			return syntheticConnectionResponse("route-retry"), nil
		}},
		IdempotencyRecovery: testPendingRecovery,
	})
	if err != nil {
		t.Fatal(err)
	}
	second := doJSON(secondServer.Handler(), http.MethodPost, "/api/v1/connections", body, map[string]string{"Idempotency-Key": "route-retry"})
	if second.Code != http.StatusCreated || dispatches.Load() != 1 {
		t.Fatalf("assembled retry = %d, dispatches=%d: %s", second.Code, dispatches.Load(), second.Body.String())
	}
}

func TestInjectedUnavailableRouteRemainsPending(t *testing.T) {
	store := newTestDurableIdempotency()
	body := `{"id":"injected-unavailable","kind":"qbittorrent","label":"injected-unavailable","endpoint":"http://qbt.test"}`
	server, err := New(Options{
		IdempotencyPersistence: store.persistence(),
		Now:                    time.Now,
		Ready:                  true,
		Dependencies: &RouteDependencies{CreateConnection: func(context.Context, api.CreateConnectionRequestObject) (api.CreateConnectionResponseObject, error) {
			return nil, ErrNotReady
		}},
		IdempotencyRecovery: testPendingRecovery,
	})
	if err != nil {
		t.Fatal(err)
	}
	response := doJSON(server.Handler(), http.MethodPost, "/api/v1/connections", body, map[string]string{"Idempotency-Key": "injected-unavailable"})
	if response.Code != http.StatusServiceUnavailable || !strings.Contains(response.Body.String(), "not_ready") {
		t.Fatalf("injected route response = %d: %s", response.Code, response.Body.String())
	}
	store.mu.Lock()
	record := cloneTestIdempotencyRecord(store.records["POST /api/v1/connections\x00injected-unavailable"])
	store.mu.Unlock()
	if record.State != IdempotencyStateCompleted || record.Replayable || record.AttemptID == "" {
		t.Fatalf("injected unavailable record = %#v; want unreplayable pending evidence", record)
	}
}

func TestConfigurationRollbackReleasesExactDurableReservation(t *testing.T) {
	store := newTestDurableIdempotency()
	storeFail := atomic.Bool{}
	storeFail.Store(true)
	now := time.Date(2026, 9, 17, 10, 0, 0, 0, time.UTC)
	candidate, err := configuration.New(configuration.Options{Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	defer candidate.Close()
	reloaded, err := configuration.New(configuration.Options{Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	defer reloaded.Close()
	persistence := &ConfigurationPersistence{CreateConnection: func(ctx context.Context, draft domain.Connection, mutate func(context.Context) (domain.Connection, error)) (domain.Connection, error) {
		value, mutateErr := mutate(ctx)
		if storeFail.CompareAndSwap(true, false) {
			return value, ErrConfigurationStore
		}
		return value, mutateErr
	}}
	server, err := New(Options{
		Configuration:            candidate,
		ConfigurationPersistence: persistence,
		ConfigurationReload: func(context.Context) (*configuration.Manager, []domain.ConfigID, error) {
			return reloaded, nil, nil
		},
		IdempotencyPersistence: store.persistence(),
		Ready:                  true,
		Now:                    func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	body := `{"id":"released-on-rollback","kind":"qbittorrent","label":"released-on-rollback","endpoint":"http://qbt.test"}`
	first := doJSON(server.Handler(), http.MethodPost, "/api/v1/connections", body, map[string]string{"Idempotency-Key": "release-me"})
	if first.Code != http.StatusServiceUnavailable || !strings.Contains(first.Body.String(), "persistence_unavailable") {
		t.Fatalf("rollback response = %d: %s", first.Code, first.Body.String())
	}
	store.mu.Lock()
	if record := store.records["POST /api/v1/connections\x00release-me"]; record.State != IdempotencyStateReleased {
		store.mu.Unlock()
		t.Fatalf("durable rollback state = %#v, want released", record)
	}
	store.mu.Unlock()
	second := doJSON(server.Handler(), http.MethodPost, "/api/v1/connections", body, map[string]string{"Idempotency-Key": "release-me"})
	if second.Code != http.StatusCreated {
		t.Fatalf("retry after rollback release = %d: %s", second.Code, second.Body.String())
	}
}

func TestDurableReservationSerializesConcurrentServers(t *testing.T) {
	store := newTestDurableIdempotency()
	var dispatches atomic.Int32
	dispatchStarted := make(chan struct{})
	releaseDispatch := make(chan struct{})
	var startOnce sync.Once
	dependency := func(context.Context, api.CreateConnectionRequestObject) (api.CreateConnectionResponseObject, error) {
		dispatches.Add(1)
		startOnce.Do(func() { close(dispatchStarted) })
		<-releaseDispatch
		return syntheticConnectionResponse("race"), nil
	}
	options := func() Options {
		return Options{Ready: true, Dependencies: &RouteDependencies{CreateConnection: dependency}, IdempotencyPersistence: store.persistence(), IdempotencyRecovery: testPendingRecovery}
	}
	firstServer, err := New(options())
	if err != nil {
		t.Fatal(err)
	}
	secondServer, err := New(options())
	if err != nil {
		t.Fatal(err)
	}
	body := `{"id":"race","kind":"qbittorrent","label":"race","endpoint":"http://qbt.test"}`
	firstResponse := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		firstResponse <- doJSON(firstServer.Handler(), http.MethodPost, "/api/v1/connections", body, map[string]string{"Idempotency-Key": "same-durable-key"})
	}()
	select {
	case <-dispatchStarted:
	case <-time.After(time.Second):
		t.Fatal("first durable dispatch did not start")
	}
	secondResponse := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		secondResponse <- doJSON(secondServer.Handler(), http.MethodPost, "/api/v1/connections", body, map[string]string{"Idempotency-Key": "same-durable-key"})
	}()
	if !store.waitForLoadCalls(2, time.Second) {
		t.Fatal("second server did not inspect the durable reservation")
	}
	close(releaseDispatch)
	first := <-firstResponse
	second := <-secondResponse
	if first.Code != http.StatusCreated {
		t.Fatalf("first response = %d: %s", first.Code, first.Body.String())
	}
	if second.Code != http.StatusConflict || !strings.Contains(second.Body.String(), "idempotency_pending") {
		t.Fatalf("second response = %d: %s", second.Code, second.Body.String())
	}
	if got := dispatches.Load(); got != 1 {
		t.Fatalf("durable concurrent dispatch count = %d, want 1", got)
	}
	if got := store.reserveCalls.Load(); got != 1 {
		t.Fatalf("durable concurrent reservation count = %d, want 1", got)
	}
}

type testDurableIdempotency struct {
	mu             sync.Mutex
	records        map[string]IdempotencyRecord
	lastCompletion IdempotencyRecord
	loadCalls      atomic.Int32
	reserveCalls   atomic.Int32
	failCompletion atomic.Bool
}

func newTestDurableIdempotency() *testDurableIdempotency {
	return &testDurableIdempotency{records: make(map[string]IdempotencyRecord)}
}

func (store *testDurableIdempotency) persistence() *IdempotencyPersistence {
	return &IdempotencyPersistence{Load: store.load, Reserve: store.reserve, Release: store.release, Complete: store.complete}
}

func (store *testDurableIdempotency) load(_ context.Context, scope, key string) (IdempotencyRecord, bool, error) {
	store.loadCalls.Add(1)
	store.mu.Lock()
	defer store.mu.Unlock()
	record, found := store.records[scope+"\x00"+key]
	if !found {
		return IdempotencyRecord{}, false, nil
	}
	if record.State == IdempotencyStateReleased {
		return IdempotencyRecord{}, false, nil
	}
	return cloneTestIdempotencyRecord(record), true, nil
}

func (store *testDurableIdempotency) reserve(_ context.Context, record IdempotencyRecord) (bool, error) {
	store.reserveCalls.Add(1)
	store.mu.Lock()
	defer store.mu.Unlock()
	key := record.Scope + "\x00" + record.Key
	if current, found := store.records[key]; found {
		if current.Digest != record.Digest {
			return false, ErrIdempotencyConflict
		}
		if current.State == IdempotencyStateReleased {
			record.Pending = true
			store.records[key] = cloneTestIdempotencyRecord(record)
			return true, nil
		}
		return false, nil
	}
	record.Pending = true
	store.records[key] = cloneTestIdempotencyRecord(record)
	return true, nil
}

func (store *testDurableIdempotency) release(_ context.Context, record IdempotencyRecord) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	key := record.Scope + "\x00" + record.Key
	current, found := store.records[key]
	if !found || current.Digest != record.Digest || current.State != IdempotencyStateReserved || current.AttemptID != record.AttemptID {
		return ErrIdempotencyStore
	}
	record.State = IdempotencyStateReleased
	record.Pending = false
	store.records[key] = cloneTestIdempotencyRecord(record)
	return nil
}

func (store *testDurableIdempotency) complete(_ context.Context, record IdempotencyRecord) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.failCompletion.Load() {
		store.lastCompletion = cloneTestIdempotencyRecord(record)
		return errors.New("synthetic completion outage")
	}
	key := record.Scope + "\x00" + record.Key
	current, found := store.records[key]
	if !found || current.Digest != record.Digest || current.State != IdempotencyStateReserved || current.AttemptID != record.AttemptID {
		return ErrIdempotencyStore
	}
	store.records[key] = cloneTestIdempotencyRecord(record)
	return nil
}

func (store *testDurableIdempotency) completeLast(t *testing.T) {
	t.Helper()
	store.failCompletion.Store(false)
	store.mu.Lock()
	record := cloneTestIdempotencyRecord(store.lastCompletion)
	store.mu.Unlock()
	if record.State != IdempotencyStateCompleted {
		t.Fatalf("lost completion record state = %q", record.State)
	}
	if err := store.complete(context.Background(), record); err != nil {
		t.Fatalf("complete recovered reservation: %v", err)
	}
}

func (store *testDurableIdempotency) waitForLoadCalls(want int32, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if store.loadCalls.Load() >= want {
			return true
		}
		time.Sleep(time.Millisecond)
	}
	return store.loadCalls.Load() >= want
}

func cloneTestIdempotencyRecord(record IdempotencyRecord) IdempotencyRecord {
	clone := record
	clone.Headers = make(map[string][]string, len(record.Headers))
	for key, values := range record.Headers {
		clone.Headers[key] = append([]string(nil), values...)
	}
	clone.Body = append([]byte(nil), record.Body...)
	if record.Effect != nil {
		effect := *record.Effect
		clone.Effect = &effect
	}
	return clone
}

func syntheticConnectionResponse(id string) api.CreateConnection201JSONResponse {
	return api.CreateConnection201JSONResponse{Body: api.Connection{Id: id, Kind: api.ConnectionKindQbittorrent, Label: id, Endpoint: "http://qbt.test", Revision: "revision", CredentialState: api.ConnectionCredentialStateMissing, Health: api.ConnectionHealthUnknown}}
}

func testPendingRecovery(context.Context, IdempotencyRecoveryRequest) (IdempotencyRecord, bool, error) {
	return IdempotencyRecord{}, false, nil
}

func doJSON(handler http.Handler, method, path, body string, headers map[string]string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	for key, value := range headers {
		request.Header.Set(key, value)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func TestProblemResponsesDoNotEchoTransportDetails(t *testing.T) {
	server, _ := testServer(t, true, "")
	handler := server.Handler()
	request := httptest.NewRequest(http.MethodGet, "/api/v1/descriptors", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", response.Code)
	}
	data, _ := io.ReadAll(response.Body)
	if strings.Contains(string(data), "internal/transport") || strings.Contains(string(data), "http://") {
		t.Fatalf("problem leaked implementation detail: %s", data)
	}
}
