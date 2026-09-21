// Package browser records browser-facing verification against the production
// HTTP readers, handlers, shell, and static asset boundary. The checkout does
// not yet compose those handlers into one application router, so these tests
// exercise the same request/response boundary with synthetic API fixtures.
package browser_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/guilycst/mastarr/ui/internal/assets"
	"github.com/guilycst/mastarr/ui/internal/client"
	"github.com/guilycst/mastarr/ui/internal/settings"
	"github.com/guilycst/mastarr/ui/internal/shell"
	"github.com/guilycst/mastarr/ui/internal/trash"
)

const browserTrashID = "123e4567-e89b-12d3-a456-426614174000"

func TestA26TrashActionsStayReadOnlyAndBindDraftChoices(t *testing.T) {
	var reads atomic.Int32
	var mutations atomic.Int32
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			mutations.Add(1)
			http.Error(w, "mutation fixture must not be called", http.StatusMethodNotAllowed)
			return
		}
		reads.Add(1)
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/trash":
			_, _ = io.WriteString(w, trashListJSON(browserTrashID))
		case "/api/v1/trash/" + browserTrashID:
			_, _ = io.WriteString(w, trashEntryJSON(browserTrashID))
		default:
			http.NotFound(w, r)
		}
	}))
	defer api.Close()

	reader, err := trash.NewHTTPReader(api.URL, api.Client(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	handler := trash.NewHandler(reader)

	for _, action := range []string{"restore", "purge"} {
		recorder := perform(handler, http.MethodGet, "/trash/"+browserTrashID+"?action="+action+"&confirm="+action+"&operator=%3Csynthetic-operator%3E", nil)
		if recorder.Code != http.StatusOK {
			t.Fatalf("%s draft status = %d; body=%s", action, recorder.Code, recorder.Body.String())
		}
		body := recorder.Body.String()
		for _, want := range []string{
			`method="get"`,
			`name="action" value="` + action + `"`,
			`name="confirm" value="` + action + `"`,
			"two-step API-owned actions",
			"no automatic re-add",
			"&lt;synthetic-operator&gt;",
		} {
			if !strings.Contains(body, want) {
				t.Errorf("%s body missing %q", action, want)
			}
		}
		if strings.Contains(body, `method="post"`) || strings.Contains(body, `method="delete"`) {
			t.Errorf("%s draft exposed mutating form", action)
		}
	}

	beforeRejects := reads.Load()
	for _, path := range []string{
		"/trash/" + browserTrashID + "?action=delete&confirm=delete",
		"/trash/" + browserTrashID + "?action=restore&confirm=purge",
		"/trash/" + browserTrashID + "?action=restore&action=purge",
	} {
		recorder := perform(handler, http.MethodGet, path, nil)
		if recorder.Code != http.StatusBadRequest {
			t.Errorf("invalid draft %q status = %d", path, recorder.Code)
		}
	}
	if reads.Load() != beforeRejects {
		t.Fatalf("invalid choices reached API reader: before=%d after=%d", beforeRejects, reads.Load())
	}
	for _, method := range []string{http.MethodPost, http.MethodDelete} {
		recorder := perform(handler, method, "/trash/"+browserTrashID, nil)
		if recorder.Code != http.StatusMethodNotAllowed || recorder.Header().Get("Allow") != "GET, HEAD" {
			t.Errorf("forged %s status=%d allow=%q", method, recorder.Code, recorder.Header().Get("Allow"))
		}
	}
	if mutations.Load() != 0 {
		t.Fatalf("UI dispatched %d upstream mutations", mutations.Load())
	}

	// Repeated consequential-action drafts remain reads. Exact effect count is
	// zero even though each draft may re-observe current API state.
	first := perform(handler, http.MethodGet, "/trash/"+browserTrashID+"?action=purge&confirm=purge", nil)
	second := perform(handler, http.MethodGet, "/trash/"+browserTrashID+"?action=purge&confirm=purge", nil)
	if first.Code != http.StatusOK || second.Code != http.StatusOK || mutations.Load() != 0 {
		t.Fatalf("repeated purge drafts status=(%d,%d) mutations=%d", first.Code, second.Code, mutations.Load())
	}
	if reads.Load() != 4 {
		t.Fatalf("API read count=%d, want 4 allowed drafts only", reads.Load())
	}
}

func TestA42SettingsRedactsCredentialsAndRejectsEncodedEndpointDrafts(t *testing.T) {
	var connectionReads atomic.Int32
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/configuration":
			w.Header().Set("ETag", `"cfg-r1"`)
			_, _ = io.WriteString(w, configurationJSON())
		case "/api/v1/connections/qbittorrent-main":
			connectionReads.Add(1)
			w.Header().Set("ETag", `"conn-r1"`)
			_, _ = io.WriteString(w, connectionJSON())
		default:
			http.NotFound(w, r)
		}
	}))
	defer api.Close()

	reader, err := settings.NewHTTPReader(api.URL, api.Client(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	counting := &countingSettingsReader{Reader: reader}
	handler := settings.NewHandler(counting)

	configuration := perform(handler, http.MethodGet, "/settings", nil)
	if configuration.Code != http.StatusOK {
		t.Fatalf("settings status = %d; body=%s", configuration.Code, configuration.Body.String())
	}
	body := configuration.Body.String()
	for _, want := range []string{"Source provenance", "yaml", "Editable", "false", "API-owned draft context", "method=\"get\""} {
		if !strings.Contains(body, want) {
			t.Errorf("settings body missing %q", want)
		}
	}
	for _, secret := range []string{"user:secret", "token=secret", "synthetic-secret", "writeOnly", "localStorage", "sessionStorage"} {
		if strings.Contains(body, secret) {
			t.Errorf("settings body leaked %q", secret)
		}
	}
	if configuration.Header().Get("Cache-Control") != "no-store, private" || configuration.Header().Get("X-Robots-Tag") != "noindex, nofollow" {
		t.Fatal("settings response missing private cache/index headers")
	}

	unsafeEndpoints := []string{
		"https://operator:synthetic-secret@example.invalid/api",
		"https://example.invalid/api?token=synthetic-token",
		"https://example.invalid/api#synthetic-secret",
		"https://example.invalid/api/s%65cret=synthetic-value",
		"https://example.invalid/api/s%252565cret=synthetic-value",
	}
	for _, endpoint := range unsafeEndpoints {
		path := "/connections/qbittorrent-main?endpoint=" + url.QueryEscape(endpoint)
		recorder := perform(handler, http.MethodGet, path, nil)
		if recorder.Code != http.StatusBadRequest {
			t.Errorf("unsafe endpoint %q status=%d body=%s", endpoint, recorder.Code, recorder.Body.String())
		}
		for _, marker := range []string{"synthetic-secret", "synthetic-token", "synthetic-value", "%65", "%2525"} {
			if strings.Contains(recorder.Body.String(), marker) {
				t.Errorf("unsafe endpoint %q echoed marker %q", endpoint, marker)
			}
		}
	}
	if counting.getConnection.Load() != 0 {
		t.Fatalf("unsafe endpoint drafts called reader %d times", counting.getConnection.Load())
	}

	safe := perform(handler, http.MethodGet, "/connections/qbittorrent-main?endpoint="+url.QueryEscape("https://example.invalid/api"), nil)
	if safe.Code != http.StatusOK || !strings.Contains(safe.Body.String(), "https://example.invalid/api") {
		t.Fatalf("safe endpoint draft status=%d body=%s", safe.Code, safe.Body.String())
	}
	if counting.getConnection.Load() != 1 {
		t.Fatalf("safe endpoint reader calls=%d, want 1", counting.getConnection.Load())
	}
}

func TestA47A48A49A51ShellMetadataAccessibilityAndAssetBoundary(t *testing.T) {
	var rendered bytes.Buffer
	err := shell.Render(context.Background(), &rendered, "/media/opaque-record?root=/private/media", "https://ui.example.test/", shell.View{
		Route:    "/media/opaque-record?root=/private/media",
		Title:    "Media <synthetic>",
		APIState: shell.APIAvailable,
	})
	if err != nil {
		t.Fatal(err)
	}
	body := rendered.String()
	for _, want := range []string{
		"<title>Media &lt;synthetic&gt;</title>",
		`rel="canonical" href="https://ui.example.test/media"`,
		`property="og:url" content="https://ui.example.test/media"`,
		`name="twitter:card" content="summary_large_image"`,
		`name="robots" content="noindex,nofollow"`,
		"https://ui.example.test/preview.svg",
		`aria-labelledby="mastarr-page-title"`,
		`<h1 id="mastarr-page-title">`,
		`role="status"`,
		`aria-label="Configuration guidance"`,
		`class="console-shell__skip"`,
		`tabindex="-1"`,
		`aria-label="Open navigation"`,
		`aria-current="page"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("shell missing %q", want)
		}
	}
	for _, forbidden := range []string{"opaque-record", "/private/media", "root=/private", "delete"} {
		if strings.Contains(strings.ToLower(body), strings.ToLower(forbidden)) {
			t.Errorf("shell leaked or exposed forbidden %q", forbidden)
		}
	}
	if got := strings.Count(body, "<h1"); got != 1 {
		t.Fatalf("shell h1 count=%d, want 1", got)
	}

	config := shell.NewConfig()
	if config.Appearance.DefaultTheme != "goshtoso" || !config.Appearance.PersistPreferences || config.Appearance.InitialColorScheme == "" {
		t.Fatalf("shell appearance does not preserve theme/system preference contract: %#v", config.Appearance)
	}
	if !config.Navigation.Drawer || !config.Navigation.DisableSearch || len(config.Navigation.Items) < 6 {
		t.Fatalf("shell navigation is not keyboard-discoverable: %#v", config.Navigation)
	}
	for _, item := range config.Navigation.Items {
		if item.Label == "" || item.Href == "" {
			t.Fatalf("navigation item lacks accessible label or href: %#v", item)
		}
	}

	missing := new(bytes.Buffer)
	if err := shell.Render(context.Background(), missing, "/not-a-route?secret=synthetic", "https://ui.example.test", shell.View{NotFound: true}); err != nil {
		t.Fatal(err)
	}
	missingBody := missing.String()
	if !strings.Contains(missingBody, "Page not found") || !strings.Contains(missingBody, `role="alert"`) || strings.Contains(missingBody, "synthetic") {
		t.Fatalf("unknown-route structural response = %s", missingBody)
	}

	preview := assets.Handler()
	asset := perform(preview, http.MethodGet, "/preview.svg?private=%2Fmedia%2Fsynthetic.mkv", nil)
	if asset.Code != http.StatusOK || asset.Header().Get("Content-Type") != assets.PreviewMIMEType || !strings.Contains(asset.Body.String(), `width="1200"`) || !strings.Contains(asset.Body.String(), `height="630"`) {
		t.Fatalf("preview response status=%d type=%q body=%s", asset.Code, asset.Header().Get("Content-Type"), asset.Body.String())
	}
	if strings.Contains(asset.Body.String(), "synthetic.mkv") {
		t.Fatal("preview reflected request-controlled media path")
	}
	for _, method := range []string{http.MethodPost, http.MethodPut} {
		if got := perform(preview, method, "/preview.svg", nil).Code; got != http.StatusMethodNotAllowed {
			t.Errorf("preview %s status=%d", method, got)
		}
	}
}

func TestA50TransportFailuresTimeoutCancellationAndEscapedErrors(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/health/ready" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = io.WriteString(w, `{"reason":"private-hostname.example/synthetic-secret"}`)
	}))
	defer api.Close()
	reader, err := client.New(api.URL, api.Client(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	_, err = reader.Ready(context.Background())
	if err == nil || !errors.Is(err, client.ErrUnavailable) || strings.Contains(err.Error(), "synthetic-secret") || strings.Contains(err.Error(), "private-hostname") {
		t.Fatalf("sanitized transport failure = %v", err)
	}

	blocking := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer blocking.Close()
	timed, err := client.New(blocking.URL, blocking.Client(), 10*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	_, err = timed.Ready(context.Background())
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("timeout identity = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = timed.Ready(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation identity = %v", err)
	}

	var unavailable bytes.Buffer
	if err := shell.Render(context.Background(), &unavailable, "/workflows?opaque=synthetic-secret", "https://ui.example.test", shell.View{
		Route:    "/workflows?opaque=synthetic-secret",
		APIState: shell.APIUnavailable,
	}); err != nil {
		t.Fatal(err)
	}
	body := unavailable.String()
	if !strings.Contains(body, "temporarily unavailable") || !strings.Contains(body, "Retry") || strings.Contains(body, "synthetic-secret") || strings.Contains(body, "private-hostname") {
		t.Fatalf("unavailable shell = %s", body)
	}
}

func perform(handler http.Handler, method, target string, body io.Reader) *httptest.ResponseRecorder {
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(method, target, body))
	return recorder
}

type countingSettingsReader struct {
	settings.Reader
	getConnection atomic.Int32
}

func (r *countingSettingsReader) GetConnection(ctx context.Context, id string) (settings.Connection, error) {
	r.getConnection.Add(1)
	return r.Reader.GetConnection(ctx, id)
}

func trashListJSON(id string) string {
	return fmt.Sprintf(`{"items":[%s],"page":{"nextCursor":null,"coverage":[],"observedAt":"2026-09-21T10:00:00Z"}}`, trashEntryJSON(id))
}

func trashEntryJSON(id string) string {
	return fmt.Sprintf(`{"id":%q,"files":[{"rootId":"trash","relativePath":"Movies/<title>.mkv","type":"file","size":2048,"fileIdentity":"file-video-1","role":"video","digest":"sha256:fixture","observedAt":"2026-09-21T09:55:00Z"},{"rootId":"trash","relativePath":"Movies/<title>.srt","type":"subtitle","size":512,"fileIdentity":"file-subtitle-1","role":"subtitle","digest":"sha256:subtitle","observedAt":"2026-09-21T09:56:00Z"}],"originalPaths":[{"rootId":"library","relativePath":"Movies/<title>.mkv"},{"rootId":"library","relativePath":"Movies/<title>.srt"}],"expiresAt":"2026-10-21T10:00:00Z","state":"held","capabilities":["restore","purge"],"clientAssociations":["qbittorrent-main"],"holds":["client-stop-pending"]}`, id)
}

func configurationJSON() string {
	return `{"source":{"source":"yaml","editable":false,"documentId":"config.yaml","revision":"yaml-r1","startupAt":"2026-09-21T09:00:00Z","reloadPolicy":"restart_required"},"keySource":"secret_file","restartRequired":true,"connections":[{"id":"qbittorrent-main","kind":"qbittorrent","label":"qBittorrent <main>","endpoint":"https://user:secret@example.invalid/api?token=secret","health":"healthy","credentialState":"managed","observedVersion":"5.1.0","capabilities":["stop","read"],"revision":"conn-r1","source":{"source":"api","editable":true,"documentId":"connections","revision":"conn-r1","startupAt":"2026-09-21T09:00:00Z","reloadPolicy":"restart_required"}}],"storageRoots":[{"id":"library","label":"Library","purpose":"library","path":"/srv/library","permission":"read_write","capabilities":["watch","read"],"revision":"root-r1","watch":{"enabled":true,"intervalSeconds":30},"source":{"source":"yaml","editable":false,"documentId":"config.yaml","revision":"yaml-r1","startupAt":"2026-09-21T09:00:00Z","reloadPolicy":"restart_required"}}],"pathMappings":[{"id":"mapping-main","connectionId":"qbittorrent-main","sourcePrefix":"/downloads","rootId":"library","destinationPrefix":"/srv/library","revision":"map-r1","source":{"source":"api","editable":true,"documentId":"path-mappings","revision":"map-r1","startupAt":"2026-09-21T09:00:00Z","reloadPolicy":"restart_required"}}]}`
}

func connectionJSON() string {
	return `{"id":"qbittorrent-main","kind":"qbittorrent","label":"qBittorrent <main>","endpoint":"https://user:secret@example.invalid/api?token=secret","health":"healthy","credentialState":"managed","observedVersion":"5.1.0","capabilities":["stop","read"],"retiredAt":"2026-09-20T12:00:00Z","revision":"conn-r1","source":{"source":"api","editable":true,"documentId":"connections","revision":"conn-r1","startupAt":"2026-09-21T09:00:00Z","reloadPolicy":"restart_required"}}`
}
