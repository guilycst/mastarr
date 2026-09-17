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
	"testing"
	"time"

	api "github.com/guilycst/mastarr/internal/api/generated"
	"github.com/guilycst/mastarr/internal/configuration"
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
