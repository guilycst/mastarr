// Command mastarr serves the HTTP-only BFF for the Mastarr operations UI.
// It owns no database, media mount, credentials or upstream connection.
package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	consoleshellassets "github.com/araihu/goshtoso-app-shells/consoleshell/assets"
	goshtosoassets "github.com/araihu/goshtoso/assets"
	uiassets "github.com/guilycst/mastarr/ui/internal/assets"
	"github.com/guilycst/mastarr/ui/internal/client"
	"github.com/guilycst/mastarr/ui/internal/config"
	"github.com/guilycst/mastarr/ui/internal/shell"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Print(configurationFailure(err))
		os.Exit(1)
	}
	api, err := client.New(cfg.APIURL, nil, client.DefaultTimeout)
	if err != nil {
		log.Print("mastarr UI API client could not be initialized")
		os.Exit(1)
	}
	if err := run(context.Background(), cfg, api); err != nil {
		log.Print("mastarr UI server stopped with an error")
		os.Exit(1)
	}
}

func configurationFailure(err error) string {
	if err == nil {
		return ""
	}
	return fmt.Sprintf("mastarr UI configuration is invalid: %v; %s", err, config.StartupRestartGuidance)
}

// NewHandler constructs the BFF handler from one startup configuration and a
// normalized API reader. It never reads environment variables.
func NewHandler(cfg config.Config, api client.Reader) http.Handler {
	return &server{
		config:        cfg,
		api:           api,
		preview:       uiassets.Handler(),
		goshtoso:      goshtosoassets.Handler(),
		consoleAssets: consoleshellassets.Handler(),
	}
}

type server struct {
	config        config.Config
	api           client.Reader
	preview       http.Handler
	goshtoso      http.Handler
	consoleAssets http.Handler
}

func (s *server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
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

	switch {
	case r.URL.Path == uiassets.PreviewPath:
		s.preview.ServeHTTP(w, r)
		return
	case strings.HasPrefix(r.URL.Path, "/assets/"):
		s.goshtoso.ServeHTTP(w, r)
		return
	case strings.HasPrefix(r.URL.Path, "/consoleshell/assets/"):
		s.consoleAssets.ServeHTTP(w, r)
		return
	}

	requestPath := r.URL.Path
	if !knownRoute(requestPath) {
		s.render(w, r, http.StatusNotFound, shell.View{
			Route:           "/",
			Title:           "Page not found",
			NotFound:        true,
			ConfigSource:    s.config.Source,
			RestartGuidance: s.config.RestartGuidance,
		})
		return
	}

	view := shell.View{
		Route:           requestPath,
		Title:           "",
		Active:          shell.ActiveID(requestPath),
		APIState:        shell.APIUnavailable,
		ConfigSource:    s.config.Source,
		RestartGuidance: s.config.RestartGuidance,
	}
	status := http.StatusServiceUnavailable
	if s.api != nil {
		readiness, err := s.api.Ready(r.Context())
		if err == nil {
			switch readiness.State {
			case client.StateReady:
				status = http.StatusOK
				view.APIState = shell.APIAvailable
			case client.StateDegraded:
				status = http.StatusOK
				view.APIState = shell.APIDegraded
			default:
				// A nil error from an injected reader is not enough to claim
				// readiness. Unknown/future states remain unavailable until
				// the client package explicitly understands them.
				status = http.StatusServiceUnavailable
				view.APIState = shell.APIUnavailable
			}
		}
	}
	s.render(w, r, status, view)
}

func (s *server) render(w http.ResponseWriter, r *http.Request, status int, view shell.View) {
	var body bytes.Buffer
	if err := shell.Render(r.Context(), &body, r.URL.Path, s.config.PublicOrigin, view); err != nil {
		// The configured origin is validated before startup. If a caller builds a
		// handler by hand with invalid input, fail closed without reflecting it.
		http.Error(w, "UI shell unavailable", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write(body.Bytes())
}

func setPrivateHeaders(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
	w.Header().Set("X-Robots-Tag", "noindex, nofollow")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
}

func knownRoute(requestPath string) bool {
	if requestPath == "/" {
		return true
	}
	for _, route := range []string{
		"/discoveries", "/media", "/reviews", "/workflows", "/actions", "/trash",
		"/settings/connections", "/settings/storage", "/settings/mappings",
	} {
		if requestPath == route || strings.HasPrefix(requestPath, route+"/") {
			return true
		}
	}
	return false
}

// run owns only listener lifecycle. Shutdown stops accepting requests and
// gives active rendering/API requests a bounded window to finish.
func run(ctx context.Context, cfg config.Config, api client.Reader) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	listener, err := net.Listen("tcp", cfg.ListenAddr)
	if err != nil {
		return err
	}
	server := &http.Server{
		Handler:           NewHandler(cfg, api),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	serveErr := make(chan error, 1)
	go func() {
		serveErr <- server.Serve(listener)
	}()
	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			return err
		}
		return nil
	case err := <-serveErr:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}
