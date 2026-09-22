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
	if r == nil || r.URL == nil {
		setPrivateHeaders(w)
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		setPrivateHeaders(w)
		w.Header().Set("Allow", http.MethodGet+", "+http.MethodHead)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	path := r.URL.Path
	switch {
	case path == assets.PreviewPath:
		setStaticHeaders(w)
		h.preview.ServeHTTP(w, r)
		return
	case strings.HasPrefix(path, "/assets/"):
		setStaticHeaders(w)
		h.goshtoso.ServeHTTP(w, r)
		return
	case strings.HasPrefix(path, "/consoleshell/assets/"):
		setStaticHeaders(w)
		h.console.ServeHTTP(w, r)
		return
	}

	setPrivateHeaders(w)
	switch {
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
		h.servePrivatePath(w, h.settings, r, r.URL.Path)
		return
	}
	clone := r.Clone(r.Context())
	urlCopy := *r.URL
	urlCopy.Path = rewritten
	urlCopy.RawPath = ""
	clone.URL = &urlCopy
	h.servePrivatePath(w, h.settings, clone, r.URL.Path)
}

func (h *Handler) servePrivate(w http.ResponseWriter, delegate http.Handler, r *http.Request) {
	h.servePrivatePath(w, delegate, r, r.URL.Path)
}

func (h *Handler) servePrivatePath(w http.ResponseWriter, delegate http.Handler, r *http.Request, publicPath string) {
	captured := newComposedResponseWriter()
	delegate.ServeHTTP(captured, r)
	status := captured.status
	if status == 0 {
		status = http.StatusOK
	}
	contentHTML, contentAriaLabelledBy := extractMain(captured.body.String())
	if contentHTML == "" {
		contentAriaLabelledBy = ""
	}
	view := shell.View{
		Route:                 publicPath,
		Active:                shell.ActiveID(publicPath),
		APIState:              shell.APIAvailable,
		ConfigSource:          h.config.Source,
		RestartGuidance:       h.config.RestartGuidance,
		ContentHTML:           contentHTML,
		ContentAriaLabelledBy: contentAriaLabelledBy,
	}
	if status >= http.StatusInternalServerError {
		view.APIState = shell.APIUnavailable
	}
	if captured.tooLarge {
		status = http.StatusServiceUnavailable
		view.APIState = shell.APIUnavailable
		view.ContentHTML = ""
		view.ContentAriaLabelledBy = ""
	}
	h.renderShell(w, r, status, view)
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

// setStaticHeaders applies only security headers that are safe to share with
// public, cacheable assets. In particular, it does not overwrite the asset
// handler's public Cache-Control policy or add a private Pragma directive.
func setStaticHeaders(w http.ResponseWriter) {
	w.Header().Set("X-Content-Type-Options", "nosniff")
}

const maxComposedBody = 1 << 20

type composedResponseWriter struct {
	header   http.Header
	body     bytes.Buffer
	status   int
	tooLarge bool
}

func newComposedResponseWriter() *composedResponseWriter {
	return &composedResponseWriter{header: make(http.Header)}
}

func (w *composedResponseWriter) Header() http.Header { return w.header }

func (w *composedResponseWriter) WriteHeader(status int) {
	if w.status != 0 {
		return
	}
	w.status = status
}

func (w *composedResponseWriter) Write(body []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	if w.body.Len()+len(body) > maxComposedBody {
		w.tooLarge = true
		return len(body), nil
	}
	return w.body.Write(body)
}

func extractMain(document string) (string, string) {
	lower := strings.ToLower(document)
	start := indexMainStart(lower)
	if start < 0 {
		return "", ""
	}
	openEnd := strings.IndexByte(lower[start:], '>')
	if openEnd < 0 {
		return "", ""
	}
	openEnd += start + 1
	endRelative := strings.Index(lower[openEnd:], "</main>")
	if endRelative < 0 {
		return "", ""
	}
	end := openEnd + endRelative
	return document[openEnd:end], mainAttribute(document[start:openEnd], "aria-labelledby")
}

func indexMainStart(lower string) int {
	for offset := 0; offset < len(lower); {
		relative := strings.Index(lower[offset:], "<main")
		if relative < 0 {
			return -1
		}
		start := offset + relative
		after := start + len("<main")
		if after == len(lower) || lower[after] == '>' || lower[after] == '/' || lower[after] == ' ' || lower[after] == '\t' || lower[after] == '\r' || lower[after] == '\n' {
			return start
		}
		offset = after
	}
	return -1
}

func mainAttribute(opening, name string) string {
	lower := strings.ToLower(opening)
	needle := strings.ToLower(name)
	for offset := 0; offset < len(lower); {
		relative := strings.Index(lower[offset:], needle)
		if relative < 0 {
			return ""
		}
		start := offset + relative
		beforeOK := start == 0 || !isHTMLNameChar(lower[start-1])
		after := start + len(needle)
		afterOK := after == len(lower) || !isHTMLNameChar(lower[after])
		if !beforeOK || !afterOK {
			offset = after
			continue
		}
		for after < len(lower) && isHTMLSpace(lower[after]) {
			after++
		}
		if after >= len(lower) || lower[after] != '=' {
			offset = after
			continue
		}
		after++
		for after < len(lower) && isHTMLSpace(lower[after]) {
			after++
		}
		if after >= len(lower) || (lower[after] != '\'' && lower[after] != '"') {
			return ""
		}
		quote := lower[after]
		valueStart := after + 1
		valueEndRelative := strings.IndexByte(lower[valueStart:], quote)
		if valueEndRelative < 0 {
			return ""
		}
		return opening[valueStart : valueStart+valueEndRelative]
	}
	return ""
}

func isHTMLNameChar(value byte) bool {
	return value >= 'a' && value <= 'z' || value >= 'A' && value <= 'Z' || value >= '0' && value <= '9' || value == '-' || value == '_' || value == ':'
}

func isHTMLSpace(value byte) bool {
	return value == ' ' || value == '\t' || value == '\r' || value == '\n' || value == '\f'
}
