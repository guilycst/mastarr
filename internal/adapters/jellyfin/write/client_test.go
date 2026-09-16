package write

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/guilycst/mastarr/internal/domain"
	"github.com/guilycst/mastarr/internal/ports"
)

const testConnection = domain.ConfigID("jellyfin-main")

type refreshFixture struct {
	mu           sync.Mutex
	status       int
	systemStatus int
	token        string
	requests     []refreshRequest
	body         string
	systemBody   string
	visibility   atomic.Bool
}

type refreshRequest struct {
	method string
	path   string
	token  string
}

func (fixture *refreshFixture) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	fixture.mu.Lock()
	fixture.requests = append(fixture.requests, refreshRequest{
		method: request.Method,
		path:   request.URL.Path,
		token:  request.Header.Get("X-Emby-Token"),
	})
	fixture.mu.Unlock()
	if request.Method == http.MethodGet && request.URL.Path == "/System/Info/Public" {
		if request.Header.Get("X-Emby-Token") != fixture.token {
			writer.WriteHeader(http.StatusUnauthorized)
			return
		}
		status := fixture.systemStatus
		if status == 0 {
			status = http.StatusOK
		}
		writer.WriteHeader(status)
		body := fixture.systemBody
		if body == "" {
			body = `{"ProductName":"Jellyfin","Version":"10.10.7"}`
		}
		_, _ = io.WriteString(writer, body)
		return
	}
	if request.Method != http.MethodPost || request.URL.Path != "/Library/Refresh" {
		writer.WriteHeader(http.StatusNotFound)
		return
	}
	if request.Header.Get("X-Emby-Token") != fixture.token {
		writer.WriteHeader(http.StatusUnauthorized)
		return
	}
	status := fixture.status
	if status == 0 {
		status = http.StatusAccepted
	}
	writer.WriteHeader(status)
	if fixture.body != "" {
		_, _ = io.WriteString(writer, fixture.body)
	}
}

func newFixtureClient(t *testing.T, fixture *refreshFixture, config Config) (*Client, *httptest.Server) {
	t.Helper()
	server := httptest.NewServer(fixture)
	if config.ConnectionID == "" {
		config.ConnectionID = testConnection
	}
	if config.Endpoint == "" {
		config.Endpoint = server.URL
	}
	if config.APIKey == "" && config.Token == "" && config.AuthToken == "" {
		config.APIKey = fixture.token
	}
	client, err := New(config)
	if err != nil {
		server.Close()
		t.Fatalf("New: %v", err)
	}
	return client, server
}

func matchingRuntimeConfig() Config {
	return Config{ExpectedProduct: "Jellyfin", ExpectedVersion: "10.10.7"}
}

func (fixture *refreshFixture) requestSnapshot() []refreshRequest {
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	return append([]refreshRequest(nil), fixture.requests...)
}

func assertCode(t *testing.T, err error, want domain.UpstreamErrorCode) domain.UpstreamError {
	t.Helper()
	if err == nil {
		t.Fatalf("expected %s error", want)
	}
	var upstream domain.UpstreamError
	if !errors.As(err, &upstream) {
		t.Fatalf("error %T does not carry domain upstream evidence: %v", err, err)
	}
	if upstream.Code != want {
		t.Fatalf("error code = %s, want %s (%v)", upstream.Code, want, err)
	}
	return upstream
}

func hasEvidence(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func TestLibraryRefreshMapsAcceptanceAndSeparatesAvailability(t *testing.T) {
	fixture := &refreshFixture{status: http.StatusAccepted, token: "fixture-jellyfin-token"}
	client, server := newFixtureClient(t, fixture, matchingRuntimeConfig())
	defer server.Close()

	result, err := client.Refresh(context.Background(), testConnection, ports.RefreshRequest{Scope: ports.RefreshLibrary})
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if !result.Accepted || result.OperationID != operationRefreshLibrary || result.ObservedAt.IsZero() {
		t.Fatalf("refresh result = %#v", result)
	}
	if !hasEvidence(result.Evidence, "refresh_request_accepted") || !hasEvidence(result.Evidence, "availability_requires_later_read") {
		t.Fatalf("refresh evidence = %#v", result.Evidence)
	}
	requests := fixture.requestSnapshot()
	if len(requests) != 2 || requests[0].method != http.MethodGet || requests[0].path != "/System/Info/Public" || requests[0].token != fixture.token || requests[1].method != http.MethodPost || requests[1].path != "/Library/Refresh" || requests[1].token != fixture.token {
		t.Fatalf("native requests = %#v", requests)
	}
	if fixture.visibility.Load() {
		t.Fatal("refresh fixture unexpectedly claimed library visibility")
	}
}

func TestItemRefreshIsUnsupportedBeforeNativeDispatch(t *testing.T) {
	fixture := &refreshFixture{status: http.StatusAccepted, token: "fixture-jellyfin-token"}
	client, server := newFixtureClient(t, fixture, Config{})
	defer server.Close()

	result, err := client.Refresh(context.Background(), testConnection, ports.RefreshRequest{Scope: ports.RefreshItem, ExternalID: "item-101"})
	assertCode(t, err, domain.OutcomeUnsupported)
	if result.Accepted || !hasEvidence(result.Evidence, "item_scope_unsupported") {
		t.Fatalf("item result = %#v", result)
	}
	if requests := fixture.requestSnapshot(); len(requests) != 0 {
		t.Fatalf("unsupported item refresh dispatched native requests: %#v", requests)
	}

	_, err = client.Refresh(context.Background(), testConnection, ports.RefreshRequest{Scope: ports.RefreshItem})
	assertCode(t, err, domain.OutcomeInvalidInput)
	if requests := fixture.requestSnapshot(); len(requests) != 0 {
		t.Fatalf("invalid item refresh dispatched native requests: %#v", requests)
	}
}

func TestRefreshScopeValidationPrecedesNativeDispatch(t *testing.T) {
	fixture := &refreshFixture{status: http.StatusAccepted, token: "fixture-jellyfin-token"}
	client, server := newFixtureClient(t, fixture, Config{})
	defer server.Close()

	cases := []struct {
		name    string
		conn    domain.ConfigID
		request ports.RefreshRequest
	}{
		{name: "wrong connection", conn: "jellyfin-other", request: ports.RefreshRequest{Scope: ports.RefreshLibrary}},
		{name: "library item id", conn: testConnection, request: ports.RefreshRequest{Scope: ports.RefreshLibrary, ExternalID: "item-101"}},
		{name: "unknown scope", conn: testConnection, request: ports.RefreshRequest{Scope: ports.RefreshScope("folder")}},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			_, err := client.Refresh(context.Background(), testCase.conn, testCase.request)
			assertCode(t, err, domain.OutcomeInvalidInput)
		})
	}
	if requests := fixture.requestSnapshot(); len(requests) != 0 {
		t.Fatalf("invalid refreshes dispatched native requests: %#v", requests)
	}
}

func TestRefreshStatusErrorsAreSanitizedAndNormalized(t *testing.T) {
	cases := []struct {
		status    int
		code      domain.UpstreamErrorCode
		retryable bool
	}{
		{status: http.StatusUnauthorized, code: domain.OutcomeUnauthorized},
		{status: http.StatusForbidden, code: domain.OutcomeUnauthorized},
		{status: http.StatusTooManyRequests, code: domain.OutcomeRateLimited, retryable: true},
		{status: http.StatusBadRequest, code: domain.OutcomeInvalidInput},
		{status: http.StatusConflict, code: domain.OutcomeConflict},
		{status: http.StatusNotFound, code: domain.OutcomeUnsupported},
		{status: http.StatusInternalServerError, code: domain.OutcomeUnavailable, retryable: true},
	}
	for _, testCase := range cases {
		t.Run(http.StatusText(testCase.status), func(t *testing.T) {
			fixture := &refreshFixture{status: testCase.status, token: "fixture-jellyfin-token", body: `{"secret":"fixture-secret"}`}
			client, server := newFixtureClient(t, fixture, matchingRuntimeConfig())
			defer server.Close()

			_, err := client.Refresh(context.Background(), testConnection, ports.RefreshRequest{Scope: ports.RefreshLibrary})
			mapped := assertCode(t, err, testCase.code)
			if mapped.Retryable != testCase.retryable {
				t.Fatalf("retryable = %t, want %t", mapped.Retryable, testCase.retryable)
			}
			if strings.Contains(err.Error(), "fixture-secret") || strings.Contains(err.Error(), server.URL) || strings.Contains(err.Error(), "X-Emby-Token") {
				t.Fatalf("sanitized error leaked fixture details: %v", err)
			}
		})
	}
}

func TestRefreshPreservesContextCancellationIdentity(t *testing.T) {
	blocked := make(chan struct{})
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		select {
		case <-blocked:
			return nil, errors.New("fixture transport stopped")
		case <-request.Context().Done():
			return nil, request.Context().Err()
		}
	})
	client, err := New(Config{
		ConnectionID:    testConnection,
		Endpoint:        "https://jellyfin.invalid",
		APIKey:          "fixture-jellyfin-token",
		HTTPClient:      &http.Client{Transport: transport},
		RequestTimeout:  5 * time.Millisecond,
		ExpectedProduct: "Jellyfin",
		ExpectedVersion: "10.10.7",
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_, err = client.Refresh(context.Background(), testConnection, ports.RefreshRequest{Scope: ports.RefreshLibrary})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("timeout error = %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = client.Refresh(ctx, testConnection, ports.RefreshRequest{Scope: ports.RefreshLibrary})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation error = %v", err)
	}
	close(blocked)
}

func TestCapabilitiesKeepRefreshScopesIndependent(t *testing.T) {
	fixture := &refreshFixture{status: http.StatusAccepted, token: "fixture-jellyfin-token"}
	client, server := newFixtureClient(t, fixture, matchingRuntimeConfig())
	defer server.Close()

	capabilities, err := client.Capabilities(context.Background(), testConnection)
	if err != nil {
		t.Fatalf("Capabilities: %v", err)
	}
	if len(capabilities) != 2 {
		t.Fatalf("capabilities = %#v", capabilities)
	}
	if capabilities[0].Name != operationRefreshLibrary || capabilities[0].State != domain.CapabilitySupported || capabilities[1].Name != operationRefreshItem || capabilities[1].State != domain.CapabilityUnsupported {
		t.Fatalf("capabilities = %#v", capabilities)
	}
	for _, capability := range capabilities {
		if err := capability.Validate(); err != nil {
			t.Fatalf("capability %s: %v", capability.Name, err)
		}
	}
	requests := fixture.requestSnapshot()
	if len(requests) != 1 || requests[0].method != http.MethodGet || requests[0].path != "/System/Info/Public" {
		t.Fatalf("capability runtime observation = %#v", requests)
	}
	if hasEvidence(capabilities[0].Evidence, "refresh_request_accepted") {
		t.Fatal("capability observation claimed a refresh request was accepted")
	}
}

func TestRefreshVersionGateBlocksUnverifiedRuntime(t *testing.T) {
	cases := []struct {
		name            string
		config          Config
		systemStatus    int
		systemBody      string
		wantState       domain.CapabilityState
		wantRefreshCode domain.UpstreamErrorCode
		wantSystemReads int
	}{
		{
			name:            "missing version",
			config:          matchingRuntimeConfig(),
			systemBody:      `{"ProductName":"Jellyfin"}`,
			wantState:       domain.CapabilityUnknown,
			wantRefreshCode: domain.OutcomeUnknown,
			wantSystemReads: 2,
		},
		{
			name:            "unknown version",
			config:          matchingRuntimeConfig(),
			systemBody:      `{"ProductName":"Jellyfin","Version":"unknown"}`,
			wantState:       domain.CapabilityUnknown,
			wantRefreshCode: domain.OutcomeUnknown,
			wantSystemReads: 2,
		},
		{
			name:            "unavailable version endpoint",
			config:          matchingRuntimeConfig(),
			systemStatus:    http.StatusInternalServerError,
			systemBody:      `{"secret":"fixture-secret"}`,
			wantState:       domain.CapabilityUnknown,
			wantRefreshCode: domain.OutcomeUnavailable,
			wantSystemReads: 2,
		},
		{
			name:            "mismatched version",
			config:          matchingRuntimeConfig(),
			systemBody:      `{"ProductName":"Jellyfin","Version":"10.11.0"}`,
			wantState:       domain.CapabilityUnsupported,
			wantRefreshCode: domain.OutcomeUnsupported,
			wantSystemReads: 2,
		},
		{
			name:            "missing configured fence",
			config:          Config{},
			systemBody:      `{"ProductName":"Jellyfin","Version":"10.10.7"}`,
			wantState:       domain.CapabilityUnknown,
			wantRefreshCode: domain.OutcomeUnknown,
			wantSystemReads: 0,
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			fixture := &refreshFixture{
				token:        "fixture-jellyfin-token",
				systemStatus: testCase.systemStatus,
				systemBody:   testCase.systemBody,
			}
			client, server := newFixtureClient(t, fixture, testCase.config)
			defer server.Close()

			capabilities, err := client.Capabilities(context.Background(), testConnection)
			if err != nil {
				t.Fatalf("Capabilities: %v", err)
			}
			if len(capabilities) != 2 || capabilities[0].State != testCase.wantState {
				t.Fatalf("capabilities = %#v, want library state %s", capabilities, testCase.wantState)
			}
			if hasEvidence(capabilities[0].Evidence, "refresh_request_accepted") {
				t.Fatal("blocked capability claimed a refresh request was accepted")
			}

			_, err = client.Refresh(context.Background(), testConnection, ports.RefreshRequest{Scope: ports.RefreshLibrary})
			assertCode(t, err, testCase.wantRefreshCode)

			requests := fixture.requestSnapshot()
			postCount := 0
			getCount := 0
			for _, request := range requests {
				switch {
				case request.method == http.MethodPost && request.path == "/Library/Refresh":
					postCount++
				case request.method == http.MethodGet && request.path == "/System/Info/Public":
					getCount++
				}
			}
			if postCount != 0 {
				t.Fatalf("blocked refresh dispatched %d native POST requests: %#v", postCount, requests)
			}
			if getCount != testCase.wantSystemReads {
				t.Fatalf("system-info reads = %d, want %d; requests = %#v", getCount, testCase.wantSystemReads, requests)
			}
		})
	}
}

func TestMatchingRuntimeObservationEnablesOnlyLibraryRefresh(t *testing.T) {
	fixture := &refreshFixture{status: http.StatusAccepted, token: "fixture-jellyfin-token"}
	client, server := newFixtureClient(t, fixture, matchingRuntimeConfig())
	defer server.Close()

	capabilities, err := client.Capabilities(context.Background(), testConnection)
	if err != nil {
		t.Fatalf("Capabilities: %v", err)
	}
	if capabilities[0].State != domain.CapabilitySupported || capabilities[0].Version != "10.10.7" {
		t.Fatalf("library capability = %#v", capabilities[0])
	}
	if !hasEvidence(capabilities[0].Evidence, "runtime_compatibility_matched") || hasEvidence(capabilities[0].Evidence, "refresh_request_accepted") {
		t.Fatalf("library capability evidence = %#v", capabilities[0].Evidence)
	}

	result, err := client.Refresh(context.Background(), testConnection, ports.RefreshRequest{Scope: ports.RefreshLibrary})
	if err != nil || !result.Accepted {
		t.Fatalf("Refresh = %#v, %v", result, err)
	}
	requests := fixture.requestSnapshot()
	if len(requests) != 3 || requests[0].method != http.MethodGet || requests[0].path != "/System/Info/Public" || requests[1].method != http.MethodGet || requests[1].path != "/System/Info/Public" || requests[2].method != http.MethodPost || requests[2].path != "/Library/Refresh" {
		t.Fatalf("native request order = %#v", requests)
	}
}

func TestNilContextIsRejectedBeforeNativeDispatch(t *testing.T) {
	fixture := &refreshFixture{status: http.StatusAccepted, token: "fixture-jellyfin-token"}
	client, server := newFixtureClient(t, fixture, Config{})
	defer server.Close()

	_, err := client.Refresh(nil, testConnection, ports.RefreshRequest{Scope: ports.RefreshLibrary})
	assertCode(t, err, domain.OutcomeInvalidInput)
	if requests := fixture.requestSnapshot(); len(requests) != 0 {
		t.Fatalf("nil context dispatched native requests: %#v", requests)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}
