package router

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
	"github.com/guilycst/mastarr/ui/internal/inventory"
	"github.com/guilycst/mastarr/ui/internal/review"
	"github.com/guilycst/mastarr/ui/internal/settings"
	"github.com/guilycst/mastarr/ui/internal/trash"
	"github.com/guilycst/mastarr/ui/internal/workflows"
)

type fakeReadiness struct {
	state client.ReadinessState
	err   error
	calls int
}

func (f *fakeReadiness) Ready(context.Context) (client.Readiness, error) {
	f.calls++
	return client.Readiness{State: f.state, ObservedAt: time.Unix(1, 0).UTC()}, f.err
}

type unavailableInventory struct{}

func (unavailableInventory) ListDiscoveries(context.Context, inventory.PageRequest) (inventory.DiscoveryPage, error) {
	return inventory.DiscoveryPage{}, inventory.ErrUnavailable
}
func (unavailableInventory) GetDiscovery(context.Context, string) (inventory.Discovery, error) {
	return inventory.Discovery{}, inventory.ErrNotFound
}
func (unavailableInventory) ListMedia(context.Context, inventory.PageRequest) (inventory.MediaPage, error) {
	return inventory.MediaPage{}, inventory.ErrUnavailable
}
func (unavailableInventory) GetMedia(context.Context, string) (inventory.Media, error) {
	return inventory.Media{}, inventory.ErrNotFound
}
func (unavailableInventory) ListDownloads(context.Context, inventory.PageRequest) (inventory.DownloadPage, error) {
	return inventory.DownloadPage{}, inventory.ErrUnavailable
}
func (unavailableInventory) GetDownload(context.Context, string) (inventory.Download, error) {
	return inventory.Download{}, inventory.ErrNotFound
}
func (unavailableInventory) ListDescriptors(context.Context, inventory.PageRequest) (inventory.DescriptorPage, error) {
	return inventory.DescriptorPage{}, inventory.ErrUnavailable
}
func (unavailableInventory) GetDescriptor(context.Context, string) (inventory.Descriptor, error) {
	return inventory.Descriptor{}, inventory.ErrNotFound
}

type unavailableReview struct{}

func (unavailableReview) ListReviews(context.Context, review.PageRequest) (review.ReviewPage, error) {
	return review.ReviewPage{}, review.ErrUnavailable
}
func (unavailableReview) GetReview(context.Context, string) (review.Review, error) {
	return review.Review{}, review.ErrNotFound
}

type unavailableWorkflows struct{}

func (unavailableWorkflows) ListWorkflows(context.Context, workflows.PageRequest) (workflows.WorkflowPage, error) {
	return workflows.WorkflowPage{}, workflows.ErrUnavailable
}
func (unavailableWorkflows) GetWorkflow(context.Context, string) (workflows.Workflow, error) {
	return workflows.Workflow{}, workflows.ErrNotFound
}

type unavailableTrash struct{}

func (unavailableTrash) ListTrash(context.Context, trash.PageRequest) (trash.TrashPage, error) {
	return trash.TrashPage{}, trash.ErrUnavailable
}
func (unavailableTrash) GetTrash(context.Context, string) (trash.TrashEntry, error) {
	return trash.TrashEntry{}, trash.ErrNotFound
}

type unavailableSettings struct{}

func (unavailableSettings) GetConfiguration(context.Context) (settings.Configuration, error) {
	return settings.Configuration{}, settings.ErrUnavailable
}
func (unavailableSettings) ListConnections(context.Context, settings.PageRequest) (settings.ConnectionPage, error) {
	return settings.ConnectionPage{}, settings.ErrUnavailable
}
func (unavailableSettings) GetConnection(context.Context, string) (settings.Connection, error) {
	return settings.Connection{}, settings.ErrNotFound
}
func (unavailableSettings) ListStorageRoots(context.Context, settings.PageRequest) (settings.StorageRootPage, error) {
	return settings.StorageRootPage{}, settings.ErrUnavailable
}
func (unavailableSettings) GetStorageRoot(context.Context, string) (settings.StorageRoot, error) {
	return settings.StorageRoot{}, settings.ErrNotFound
}
func (unavailableSettings) ListPathMappings(context.Context, settings.PageRequest) (settings.PathMappingPage, error) {
	return settings.PathMappingPage{}, settings.ErrUnavailable
}
func (unavailableSettings) GetPathMapping(context.Context, string) (settings.PathMapping, error) {
	return settings.PathMapping{}, settings.ErrNotFound
}
func (unavailableSettings) ListConnectionChecks(context.Context, settings.PageRequest) (settings.ConnectionCheckPage, error) {
	return settings.ConnectionCheckPage{}, settings.ErrUnavailable
}
func (unavailableSettings) GetConnectionCheck(context.Context, string) (settings.ConnectionCheck, error) {
	return settings.ConnectionCheck{}, settings.ErrNotFound
}

func testConfig() config.Config {
	return config.Config{
		APIURL:          "http://api.example.test",
		PublicOrigin:    "https://ui.example.test",
		ListenAddr:      "127.0.0.1:0",
		Source:          "synthetic test",
		RestartGuidance: "restart the BFF",
	}
}

func testRouter(api client.Reader) http.Handler {
	return New(testConfig(), Dependencies{
		API:       api,
		Inventory: unavailableInventory{},
		Review:    unavailableReview{},
		Workflows: unavailableWorkflows{},
		Trash:     unavailableTrash{},
		Settings:  unavailableSettings{},
	})
}

func request(t *testing.T, handler http.Handler, method, path string) *httptest.ResponseRecorder {
	t.Helper()
	recorder := httptest.NewRecorder()
	req := httptest.NewRequest(method, "http://attacker.example"+path, nil)
	req.Header.Set("Host", "attacker.example")
	req.Header.Set("X-Forwarded-Host", "forwarded.attacker.example")
	handler.ServeHTTP(recorder, req)
	return recorder
}

func TestProductionRouterDispatchesReaderRoutes(t *testing.T) {
	router := testRouter(&fakeReadiness{state: client.StateReady})
	cases := []struct {
		path string
		want string
	}{
		{path: "/discoveries", want: "Inventory unavailable."},
		{path: "/media", want: "Inventory unavailable."},
		{path: "/downloads", want: "Inventory unavailable."},
		{path: "/descriptors", want: "Inventory unavailable."},
		{path: "/reviews", want: "Reviews unavailable."},
		{path: "/workflows", want: "Workflows unavailable."},
		{path: "/trash", want: "Trash unavailable."},
		{path: "/settings", want: "Settings unavailable."},
		{path: "/settings/connections", want: "Settings unavailable."},
		{path: "/settings/storage", want: "Settings unavailable."},
		{path: "/settings/mappings", want: "Settings unavailable."},
	}
	for _, tc := range cases {
		t.Run(tc.path, func(t *testing.T) {
			response := request(t, router, http.MethodGet, tc.path)
			if response.Code != http.StatusServiceUnavailable {
				t.Fatalf("status = %d, want 503; body=%s", response.Code, response.Body.String())
			}
			if !strings.Contains(response.Body.String(), tc.want) {
				t.Fatalf("body = %q, want %q", response.Body.String(), tc.want)
			}
			if got := response.Header().Get("Cache-Control"); got != "no-store, private" {
				t.Fatalf("Cache-Control = %q, want private no-store", got)
			}
		})
	}
}

func TestProductionRouterHandlesDetailsUnknownRoutesAndMethods(t *testing.T) {
	readiness := &fakeReadiness{state: client.StateReady}
	handler := testRouter(readiness)
	for _, path := range []string{"/media/missing", "/reviews/missing", "/workflows/missing", "/trash/missing", "/settings/connections/missing"} {
		response := request(t, handler, http.MethodGet, path)
		if response.Code != http.StatusNotFound {
			t.Fatalf("%s status = %d, want 404; body=%s", path, response.Code, response.Body.String())
		}
	}
	before := readiness.calls
	unknown := request(t, handler, http.MethodGet, "/unknown/private-id")
	if unknown.Code != http.StatusNotFound {
		t.Fatalf("unknown status = %d, want 404", unknown.Code)
	}
	if readiness.calls != before {
		t.Fatalf("unknown route called readiness: before=%d after=%d", before, readiness.calls)
	}
	for _, path := range []string{"/media", "/settings", "/preview.svg"} {
		response := request(t, handler, http.MethodPost, path)
		if response.Code != http.StatusMethodNotAllowed {
			t.Fatalf("POST %s status = %d, want 405", path, response.Code)
		}
		if got := response.Header().Get("Allow"); got != "GET, HEAD" {
			t.Fatalf("POST %s Allow = %q, want GET, HEAD", path, got)
		}
	}
}

func TestProductionRouterShellMetadataAssetsAndActionOverview(t *testing.T) {
	readiness := &fakeReadiness{state: client.StateReady}
	handler := testRouter(readiness)
	root := request(t, handler, http.MethodGet, "/")
	if root.Code != http.StatusOK {
		t.Fatalf("root status = %d, want 200", root.Code)
	}
	if body := root.Body.String(); !strings.Contains(body, "https://ui.example.test/") || strings.Contains(body, "attacker.example") || strings.Contains(body, "forwarded.attacker.example") {
		t.Fatalf("root metadata did not stay on configured origin: %s", body)
	}
	actions := request(t, handler, http.MethodGet, "/actions")
	if actions.Code != http.StatusOK || !strings.Contains(actions.Body.String(), "Actions") {
		t.Fatalf("actions response = %d %q", actions.Code, actions.Body.String())
	}
	preview := request(t, handler, http.MethodGet, "/preview.svg")
	if preview.Code != http.StatusOK || preview.Header().Get("Content-Type") != "image/svg+xml" {
		t.Fatalf("preview response = %d content-type=%q", preview.Code, preview.Header().Get("Content-Type"))
	}
}

func TestProductionRouterSanitizesReadinessOutageAndCancellation(t *testing.T) {
	for _, name := range []string{"outage", "cancellation"} {
		t.Run(name, func(t *testing.T) {
			err := errors.New("private upstream detail")
			if name == "cancellation" {
				err = context.Canceled
			}
			response := request(t, testRouter(&fakeReadiness{err: err}), http.MethodGet, "/")
			if response.Code != http.StatusServiceUnavailable {
				t.Fatalf("status = %d, want 503", response.Code)
			}
			if strings.Contains(response.Body.String(), "private upstream detail") || strings.Contains(response.Body.String(), "context canceled") {
				t.Fatalf("response leaked readiness detail: %s", response.Body.String())
			}
		})
	}
}

func TestProductionRouterPreviewHeadHasNoBody(t *testing.T) {
	response := request(t, testRouter(&fakeReadiness{state: client.StateReady}), http.MethodHead, "/preview.svg")
	if response.Code != http.StatusOK {
		t.Fatalf("HEAD status = %d, want 200", response.Code)
	}
	if got := response.Header().Get("Content-Type"); got != "image/svg+xml" {
		t.Fatalf("HEAD content-type = %q, want image/svg+xml", got)
	}
}
