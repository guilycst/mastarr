package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/guilycst/mastarr/ui/internal/client"
	"github.com/guilycst/mastarr/ui/internal/config"
)

type fakeReader struct {
	readiness client.Readiness
	err       error
	calls     int
}

func TestConfigurationFailureIncludesOnlySanitizedNameAndRestartGuidance(t *testing.T) {
	message := configurationFailure(errors.New(`required environment variable "MASTARR_UI_API_URL" is not set`))
	for _, want := range []string{"MASTARR_UI_API_URL", "restart the BFF"} {
		if !strings.Contains(message, want) {
			t.Fatalf("configuration failure missing %q: %s", want, message)
		}
	}
	for _, forbidden := range []string{"secret", "https://", "token=", "private"} {
		if strings.Contains(message, forbidden) {
			t.Fatalf("configuration failure leaked %q: %s", forbidden, message)
		}
	}
}

func (f *fakeReader) Ready(context.Context) (client.Readiness, error) {
	f.calls++
	return f.readiness, f.err
}

func testConfig() config.Config {
	return config.Config{
		APIURL:          "http://api.example.test",
		ListenAddr:      ":0",
		PublicOrigin:    "https://ui.example.test",
		Source:          "startup environment",
		RestartGuidance: "Restart the BFF after changing UI configuration.",
	}
}

func TestHandlerRendersConfiguredMetadataAndAuthoritativeStates(t *testing.T) {
	reader := &fakeReader{readiness: client.Readiness{State: client.StateDegraded, ObservedAt: time.Now()}}
	handler := NewHandler(testConfig(), reader)
	req := httptest.NewRequest(http.MethodGet, "https://attacker.example/media/private-id?root=/media/secret", nil)
	req.Host = "attacker.example"
	req.Header.Set("X-Forwarded-Host", "attacker.example")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d", recorder.Code)
	}
	body := recorder.Body.String()
	for _, want := range []string{
		`<link rel="canonical" href="https://ui.example.test/media">`,
		`<meta property="og:url" content="https://ui.example.test/media">`,
		`The API is ready with degraded services`,
		`noindex,nofollow`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("body missing %q", want)
		}
	}
	for _, forbidden := range []string{"attacker.example", "private-id", "/media/secret", "root=/media"} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("body leaked %q", forbidden)
		}
	}
	if reader.calls != 1 {
		t.Fatalf("Ready calls = %d", reader.calls)
	}
}

func TestHandlerRendersRetriableOutageWithoutErrorDetails(t *testing.T) {
	reader := &fakeReader{err: errors.New("GET https://private.example/api/ready: dial secret-host")}
	handler := NewHandler(testConfig(), reader)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/workflows", nil))
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d", recorder.Code)
	}
	body := recorder.Body.String()
	for _, want := range []string{"temporarily unavailable", "Retry", "No successful state is inferred"} {
		if !strings.Contains(body, want) {
			t.Fatalf("outage body missing %q", want)
		}
	}
	for _, forbidden := range []string{"private.example", "secret-host", "dial"} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("outage body leaked %q", forbidden)
		}
	}
	if got := recorder.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q", got)
	}
}

func TestHandlerUnknownRouteDoesNotCallAPIAndReturnsShell404(t *testing.T) {
	reader := &fakeReader{readiness: client.Readiness{State: client.StateReady}}
	handler := NewHandler(testConfig(), reader)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/unknown/private-id", nil))
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("status = %d", recorder.Code)
	}
	if reader.calls != 0 {
		t.Fatalf("unknown route called API %d times", reader.calls)
	}
	if !strings.Contains(recorder.Body.String(), "The requested page was not found") {
		t.Fatal("missing shell 404")
	}
	if strings.Contains(recorder.Body.String(), "private-id") {
		t.Fatal("404 reflected private path")
	}
}

func TestHandlerRejectsMutationMethodsBeforeEveryAssetPrefix(t *testing.T) {
	reader := &fakeReader{readiness: client.Readiness{State: client.StateReady}}
	handler := NewHandler(testConfig(), reader)
	for _, path := range []string{"/preview.svg", "/assets/goshtoso.css", "/consoleshell/assets/shell.css"} {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{"secret":"value"}`)))
		if recorder.Code != http.StatusMethodNotAllowed {
			t.Errorf("POST %s status = %d, want %d", path, recorder.Code, http.StatusMethodNotAllowed)
		}
		if got := recorder.Header().Get("Allow"); got != "GET, HEAD" {
			t.Errorf("POST %s Allow = %q", path, got)
		}
		if reader.calls != 0 {
			t.Errorf("POST %s called API %d times", path, reader.calls)
		}
	}
}

func TestHandlerKeepsUnknownReadinessUnavailable(t *testing.T) {
	reader := &fakeReader{readiness: client.Readiness{State: client.ReadinessState("future")}}
	handler := NewHandler(testConfig(), reader)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/media", nil))
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("unknown readiness status = %d", recorder.Code)
	}
	if !strings.Contains(recorder.Body.String(), "temporarily unavailable") {
		t.Fatal("unknown readiness did not render unavailable state")
	}
}

func TestHandlerServesPreviewAndRejectsMutationMethods(t *testing.T) {
	handler := NewHandler(testConfig(), nil)
	asset := httptest.NewRecorder()
	handler.ServeHTTP(asset, httptest.NewRequest(http.MethodGet, "/preview.svg", nil))
	if asset.Code != http.StatusOK || asset.Header().Get("Content-Type") != "image/svg+xml" {
		t.Fatalf("asset response = %d %q", asset.Code, asset.Header().Get("Content-Type"))
	}
	if !strings.Contains(asset.Body.String(), `width="1200"`) {
		t.Fatal("asset dimensions missing")
	}
	mutation := httptest.NewRecorder()
	handler.ServeHTTP(mutation, httptest.NewRequest(http.MethodPost, "/media", strings.NewReader(`{"secret":"value"}`)))
	if mutation.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST status = %d", mutation.Code)
	}
	if strings.Contains(mutation.Body.String(), "secret") {
		t.Fatal("mutation response reflected body")
	}
}

func TestRunStopsGracefullyOnCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- run(ctx, testConfig(), nil) }()
	time.Sleep(20 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("run() error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("run() did not stop")
	}
}
