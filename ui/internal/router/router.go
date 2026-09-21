// Package router composes the HTTP-only UI readers into the production BFF.
// It owns route selection and startup wiring only; safety checks, normalized
// reads, and HTML rendering remain in the package handlers it delegates to.
package router

import (
	"bytes"
	"net/http"
	"strings"

	consoleshellassets "github.com/araihu/goshtoso-app-shells/consoleshell/assets"
	goshtosoassets "github.com/araihu/goshtoso/assets"
	"github.com/guilycst/mastarr/ui/internal/assets"
	"github.com/guilycst/mastarr/ui/internal/client"
	"github.com/guilycst/mastarr/ui/internal/config"
	"github.com/guilycst/mastarr/ui/internal/inventory"
	"github.com/guilycst/mastarr/ui/internal/review"
	"github.com/guilycst/mastarr/ui/internal/settings"
	"github.com/guilycst/mastarr/ui/internal/shell"
	"github.com/guilycst/mastarr/ui/internal/trash"
	"github.com/guilycst/mastarr/ui/internal/workflows"
)

// Dependencies are normalized HTTP-only readers. Generated client DTOs never
// cross this package boundary. Nil readers remain a safe unavailable state,
// which lets the shell start even when an API dependency is temporarily down.
type Dependencies struct {
	API       client.Reader
	Inventory inventory.Reader
	Review    review.Reader
	Workflows workflows.Reader
	Trash     trash.Reader
	Settings  settings.Reader
}

// Handler is the single production route composition point for the BFF.
// Delegated handlers retain ownership of request bounds, error sanitization,
// and normalized rendering.
type Handler struct {
	config config.Config
	deps   Dependencies

	inventory http.Handler
	review    http.Handler
	workflows http.Handler
	trash     http.Handler
	settings  http.Handler
	preview   http.Handler
	goshtoso  http.Handler
	console   http.Handler
}

// New composes the production BFF handler from startup configuration and
// normalized readers. Static handlers are local, synthetic-safe assets.
func New(cfg config.Config, deps Dependencies) http.Handler {
	return &Handler{
		config:    cfg,
		deps:      deps,
		inventory: inventory.NewHandler(deps.Inventory),
		review:    review.NewHandler(deps.Review),
		workflows: workflows.NewHandler(deps.Workflows),
		trash:     trash.NewHandler(deps.Trash),
		settings:  settings.NewHandler(deps.Settings),
		preview:   assets.Handler(),
		goshtoso:  goshtosoassets.Handler(),
		console:   consoleshellassets.Handler(),
	}
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	setPrivateHeaders(w)
	if r == nil || r.URL == nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", http.MethodGet+", "+http.MethodHead)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	path := r.URL.Path
	switch {
	case path == assets.PreviewPath:
		h.preview.ServeHTTP(w, r)
		return
	case strings.HasPrefix(path, "/assets/"):
		h.goshtoso.ServeHTTP(w, r)
		return
	case strings.HasPrefix(path, "/consoleshell/assets/"):
		h.console.ServeHTTP(w, r)
		return
	case routePrefix(path, "/discoveries") || routePrefix(path, "/media") || routePrefix(path, "/downloads") || routePrefix(path, "/descriptors"):
		h.servePrivate(w, h.inventory, r)
		return
	case routePrefix(path, "/reviews"):
		h.servePrivate(w, h.review, r)
		return
	case routePrefix(path, "/workflows"):
		h.servePrivate(w, h.workflows, r)
		return
	case routePrefix(path, "/trash"):
		h.servePrivate(w, h.trash, r)
		return
	case settingsPath(path):
		h.serveSettings(w, r)
		return
	case path == "/" || path == "/actions":
		h.serveShell(w, r)
		return
	default:
		h.renderShell(w, r, http.StatusNotFound, shell.View{
			Route:           "/",
			Title:           "Page not found",
			NotFound:        true,
			ConfigSource:    h.config.Source,
			RestartGuidance: h.config.RestartGuidance,
		})
		return
	}
}

func (h *Handler) serveSettings(w http.ResponseWriter, r *http.Request) {
	rewritten, ok := settingsPathRewrite(r.URL.Path)
	if !ok {
		h.renderShell(w, r, http.StatusNotFound, shell.View{
			Route:           "/",
			Title:           "Page not found",
			NotFound:        true,
			ConfigSource:    h.config.Source,
			RestartGuidance: h.config.RestartGuidance,
		})
		return
	}
	if rewritten == r.URL.Path {
		h.servePrivate(w, h.settings, r)
		return
	}
	clone := r.Clone(r.Context())
	urlCopy := *r.URL
	urlCopy.Path = rewritten
	urlCopy.RawPath = ""
	clone.URL = &urlCopy
	h.servePrivate(w, h.settings, clone)
}

func (h *Handler) servePrivate(w http.ResponseWriter, delegate http.Handler, r *http.Request) {
	delegate.ServeHTTP(&privateResponseWriter{ResponseWriter: w}, r)
}

func (h *Handler) serveShell(w http.ResponseWriter, r *http.Request) {
	view := shell.View{
		Route:           r.URL.Path,
		Active:          shell.ActiveID(r.URL.Path),
		APIState:        shell.APIUnavailable,
		ConfigSource:    h.config.Source,
		RestartGuidance: h.config.RestartGuidance,
	}
	status := http.StatusServiceUnavailable
	if h.deps.API != nil {
		readiness, err := h.deps.API.Ready(r.Context())
		if err == nil {
			switch readiness.State {
			case client.StateReady:
				status = http.StatusOK
				view.APIState = shell.APIAvailable
			case client.StateDegraded:
				status = http.StatusOK
				view.APIState = shell.APIDegraded
			}
		}
	}
	h.renderShell(w, r, status, view)
}

func (h *Handler) renderShell(w http.ResponseWriter, r *http.Request, status int, view shell.View) {
	var body bytes.Buffer
	if err := shell.Render(r.Context(), &body, r.URL.Path, h.config.PublicOrigin, view); err != nil {
		http.Error(w, "UI shell unavailable", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write(body.Bytes())
}

func routePrefix(path, route string) bool {
	return path == route || strings.HasPrefix(path, route+"/")
}

func settingsPath(path string) bool {
	if routePrefix(path, "/settings") {
		return true
	}
	for _, route := range []string{
		"/configuration", "/connections", "/storage-roots", "/path-mappings", "/connection-checks",
	} {
		if routePrefix(path, route) {
			return true
		}
	}
	return false
}

func settingsPathRewrite(path string) (string, bool) {
	if path == "/settings" || path == "/configuration" || routePrefix(path, "/connections") || routePrefix(path, "/storage-roots") || routePrefix(path, "/path-mappings") || routePrefix(path, "/connection-checks") {
		return path, true
	}
	for _, mapping := range []struct {
		public string
		inner  string
	}{
		{public: "/settings/configuration", inner: "/configuration"},
		{public: "/settings/connections", inner: "/connections"},
		{public: "/settings/storage", inner: "/storage-roots"},
		{public: "/settings/mappings", inner: "/path-mappings"},
		{public: "/settings/checks", inner: "/connection-checks"},
	} {
		if path == mapping.public {
			return mapping.inner, true
		}
		if strings.HasPrefix(path, mapping.public+"/") {
			return mapping.inner + strings.TrimPrefix(path, mapping.public), true
		}
	}
	return "", false
}

func setPrivateHeaders(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store, private")
	w.Header().Set("Pragma", "no-cache")
	w.Header().Set("X-Robots-Tag", "noindex, nofollow")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
}

type privateResponseWriter struct {
	http.ResponseWriter
	wroteHeader bool
}

func (w *privateResponseWriter) WriteHeader(status int) {
	if w.wroteHeader {
		return
	}
	setPrivateHeaders(w.ResponseWriter)
	w.wroteHeader = true
	w.ResponseWriter.WriteHeader(status)
}

func (w *privateResponseWriter) Write(body []byte) (int, error) {
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(body)
}
