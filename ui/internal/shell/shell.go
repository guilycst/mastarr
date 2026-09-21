// Package shell renders the HTTP-only UI frame. It consumes normalized values
// from the client package and has no access to databases, media or upstreams.
package shell

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/a-h/templ"
	"github.com/araihu/goshtoso-app-shells/consoleshell"
	"github.com/araihu/goshtoso/components/head"
	"github.com/araihu/goshtoso/components/sidebar"
	"github.com/guilycst/mastarr/ui/internal/assets"
)

// APIState is the only API health information rendered by the shell.
type APIState string

const (
	APIAvailable   APIState = "available"
	APIDegraded    APIState = "degraded"
	APIUnavailable APIState = "unavailable"
)

// View is route-owned presentation state. Values are bounded and escaped
// before rendering; callers should pass fixed route labels rather than API
// response text.
type View struct {
	Route           string
	Title           string
	Description     string
	Active          string
	APIState        APIState
	NotFound        bool
	ConfigSource    string
	RestartGuidance string
	// ContentHTML is normalized package output captured by the production
	// router. It is emitted inside the shared shell after the route heading;
	// only trusted, package-owned renderers may populate it.
	ContentHTML string
}

const (
	productName        = "Mastarr"
	defaultDescription = "Private media reconciliation operations"
	configurationNote  = "Configuration source: startup environment."
	restartNote        = "Restart the BFF after changing UI configuration."
)

// NewConfig returns the U-00-selected Goshtoso console shell composition.
// Runtime assets are served locally by the application and no custom copied
// component markup is used here.
func NewConfig() consoleshell.Config {
	return consoleshell.Config{
		Brand: consoleshell.Brand{
			Name:    productName,
			HomeURL: "/",
		},
		Navigation: consoleshell.Navigation{
			Drawer:            true,
			IconOnlyMenu:      false,
			DisableSearch:     true,
			SearchPlaceholder: "Search",
			Items: []sidebar.Item{
				{ID: "home", Label: "Overview", Href: "/"},
				{ID: "discoveries", Label: "Discoveries", Href: "/discoveries"},
				{ID: "media", Label: "Media", Href: "/media"},
				{ID: "reviews", Label: "Reviews", Href: "/reviews"},
				{ID: "workflows", Label: "Workflows", Href: "/workflows"},
				{ID: "trash", Label: "Trash", Href: "/trash"},
			},
		},
		Appearance: consoleshell.AppearanceConfig{
			DefaultTheme:       "goshtoso",
			InitialColorScheme: consoleshell.ColorSchemeSystem,
			PersistPreferences: true,
		},
		Interactions: consoleshell.InteractionConfig{
			EnableHTMX:     true,
			LocalRuntime:   true,
			FragmentTarget: "#main-content",
			NavigationOOB:  true,
		},
		AssetPrefix: "/consoleshell/assets/",
		MainID:      "main-content",
		ContentID:   "console-content",
	}
}

// Render renders a complete private dashboard document into w. Canonical and
// social URLs always derive from the configured origin and a route allowlist;
// request Host, forwarded headers, query strings and opaque IDs are ignored.
func Render(ctx context.Context, w io.Writer, requestPath, publicOrigin string, view View) error {
	if ctx == nil {
		ctx = context.Background()
	}
	origin, err := normalizeOrigin(publicOrigin)
	if err != nil {
		return err
	}
	canonicalPath := CanonicalPath(requestPath)
	if canonicalPath == "" {
		canonicalPath = "/"
	}
	if view.Title == "" {
		if view.NotFound {
			view.Title = "Page not found"
		} else {
			view.Title = titleForPath(canonicalPath)
		}
	}
	if view.Description == "" {
		view.Description = defaultDescription
	}
	if view.Active == "" {
		view.Active = ActiveID(canonicalPath)
	}
	if view.APIState == "" {
		view.APIState = APIAvailable
	}
	if view.ConfigSource == "" {
		view.ConfigSource = configurationNote
	}
	if view.RestartGuidance == "" {
		view.RestartGuidance = restartNote
	}
	view.Title = boundedText(view.Title, 120)
	view.Description = boundedText(view.Description, 240)
	view.ConfigSource = boundedText(view.ConfigSource, 160)
	view.RestartGuidance = boundedText(view.RestartGuidance, 240)
	if view.Title == "" || view.Description == "" || view.ConfigSource == "" || view.RestartGuidance == "" {
		return errors.New("shell view text is required")
	}

	metadata := head.MetadataConfig{
		Title:         view.Title,
		Description:   view.Description,
		CanonicalURL:  origin + canonicalPath,
		OpenGraphType: head.OpenGraphTypeWebsite,
		SiteName:      productName,
		Locale:        "en_US",
		Image: head.SocialImage{
			URL:      origin + assets.PreviewPath,
			MIMEType: assets.PreviewMIMEType,
			Width:    assets.PreviewWidth,
			Height:   assets.PreviewHeight,
			Alt:      assets.PreviewAlt,
		},
		TwitterCard: head.TwitterCardSummaryLargeImage,
	}
	page := consoleshell.Page{
		Metadata:      &metadata,
		Title:         view.Title,
		DocumentTitle: view.Title + " · " + productName,
		Description:   view.Description,
		CanonicalURL:  metadata.CanonicalURL,
		Active:        validActiveID(view.Active),
		Content:       content(view),
		Head:          privateHead(),
	}
	if strings.HasPrefix(origin, "http://") {
		// Goshtoso's public head component intentionally requires HTTPS for
		// crawler-facing metadata. The configuration contract also supports
		// loopback HTTP during local development, so keep the same escaped
		// metadata set in this narrow local-only fallback while retaining the
		// Goshtoso shell layout and runtime.
		page.Metadata = nil
		page.Head = localHTTPMetadata(metadata, privateHead())
	}

	var rendered bytes.Buffer
	if err := consoleshell.Layout(NewConfig(), page).Render(ctx, &rendered); err != nil {
		return fmt.Errorf("render shell: %w", err)
	}
	_, err = rendered.WriteTo(w)
	return err
}

func localHTTPMetadata(metadata head.MetadataConfig, private templ.Component) templ.Component {
	return templ.ComponentFunc(func(ctx context.Context, w io.Writer) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		writeMeta := func(kind, name, value string) error {
			if value == "" {
				return nil
			}
			_, err := io.WriteString(w, `<meta `+kind+`="`+name+`" content="`+templ.EscapeString(value)+`">`)
			return err
		}
		for _, item := range []struct {
			kind  string
			name  string
			value string
		}{
			{kind: "property", name: "og:url", value: metadata.CanonicalURL},
			{kind: "property", name: "og:type", value: string(metadata.OpenGraphType)},
			{kind: "property", name: "og:title", value: metadata.Title},
			{kind: "property", name: "og:description", value: metadata.Description},
			{kind: "property", name: "og:site_name", value: metadata.SiteName},
			{kind: "property", name: "og:locale", value: metadata.Locale},
			{kind: "property", name: "og:image", value: metadata.Image.URL},
			{kind: "property", name: "og:image:type", value: metadata.Image.MIMEType},
			{kind: "property", name: "og:image:width", value: strconv.Itoa(metadata.Image.Width)},
			{kind: "property", name: "og:image:height", value: strconv.Itoa(metadata.Image.Height)},
			{kind: "property", name: "og:image:alt", value: metadata.Image.Alt},
			{kind: "name", name: "twitter:card", value: string(metadata.TwitterCard)},
			{kind: "name", name: "twitter:title", value: metadata.Title},
			{kind: "name", name: "twitter:description", value: metadata.Description},
			{kind: "name", name: "twitter:image", value: metadata.Image.URL},
			{kind: "name", name: "twitter:image:alt", value: metadata.Image.Alt},
		} {
			if err := writeMeta(item.kind, item.name, item.value); err != nil {
				return err
			}
		}
		return private.Render(ctx, w)
	})
}

func privateHead() templ.Component {
	return templ.ComponentFunc(func(ctx context.Context, w io.Writer) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		_, err := io.WriteString(w, `<meta name="robots" content="noindex,nofollow"><meta name="referrer" content="no-referrer">`)
		return err
	})
}

func content(view View) templ.Component {
	return templ.ComponentFunc(func(ctx context.Context, w io.Writer) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		write := func(value string) error {
			_, err := io.WriteString(w, templ.EscapeString(value))
			return err
		}
		section := `<section class="mastarr-page" aria-labelledby="mastarr-page-title">`
		if view.ContentHTML != "" {
			section = `<section class="mastarr-page">`
		}
		if _, err := io.WriteString(w, section); err != nil {
			return err
		}
		if view.ContentHTML != "" {
			if _, err := io.WriteString(w, view.ContentHTML); err != nil {
				return err
			}
		} else {
			if _, err := io.WriteString(w, `<h1 id="mastarr-page-title">`); err != nil {
				return err
			}
			if err := write(view.Title); err != nil {
				return err
			}
			if _, err := io.WriteString(w, `</h1>`); err != nil {
				return err
			}
			switch {
			case view.NotFound:
				if _, err := io.WriteString(w, `<p role="alert">The requested page was not found.</p>`); err != nil {
					return err
				}
			case view.APIState == APIUnavailable:
				if _, err := io.WriteString(w, `<div role="alert" class="mastarr-status mastarr-status--unavailable"><p>The Mastarr API is temporarily unavailable.</p><p>Retry to load API-owned state. No successful state is inferred while the API is unavailable.</p><a href="`); err != nil {
					return err
				}
				if err := write(CanonicalPath(view.Route)); err != nil {
					return err
				}
				if _, err := io.WriteString(w, `">Retry</a></div>`); err != nil {
					return err
				}
			case view.APIState == APIDegraded:
				if _, err := io.WriteString(w, `<p role="status" class="mastarr-status mastarr-status--degraded">The API is ready with degraded services. Observed state remains authoritative.</p>`); err != nil {
					return err
				}
			default:
				if _, err := io.WriteString(w, `<p role="status" class="mastarr-status">The API is ready. Observed state is authoritative.</p>`); err != nil {
					return err
				}
			}
		}
		if _, err := io.WriteString(w, `<aside class="mastarr-config-note" aria-label="Configuration guidance"><p>`); err != nil {
			return err
		}
		if err := write(view.ConfigSource); err != nil {
			return err
		}
		if _, err := io.WriteString(w, `</p><p>`); err != nil {
			return err
		}
		if err := write(view.RestartGuidance); err != nil {
			return err
		}
		_, err := io.WriteString(w, `</p></aside></section>`)
		return err
	})
}

// CanonicalPath maps a request path to an allowlisted public route. It removes
// filters, query strings and private record IDs from shared metadata.
func CanonicalPath(requestPath string) string {
	if requestPath == "" {
		return "/"
	}
	if index := strings.IndexByte(requestPath, '?'); index >= 0 {
		requestPath = requestPath[:index]
	}
	if index := strings.IndexByte(requestPath, '#'); index >= 0 {
		requestPath = requestPath[:index]
	}
	if requestPath == "/" {
		return "/"
	}
	for _, route := range []string{
		"/discoveries", "/media", "/downloads", "/descriptors", "/reviews", "/workflows", "/actions", "/trash",
		"/settings/connections", "/settings/storage", "/settings/mappings",
		"/settings",
	} {
		if requestPath == route || strings.HasPrefix(requestPath, route+"/") {
			return route
		}
	}
	for _, alias := range []struct {
		path      string
		canonical string
	}{
		{path: "/configuration", canonical: "/settings"},
		{path: "/connections", canonical: "/settings/connections"},
		{path: "/storage-roots", canonical: "/settings/storage"},
		{path: "/path-mappings", canonical: "/settings/mappings"},
		{path: "/connection-checks", canonical: "/settings"},
	} {
		if requestPath == alias.path || strings.HasPrefix(requestPath, alias.path+"/") {
			return alias.canonical
		}
	}
	return "/"
}

// ActiveID returns the configured navigation identity for a public route.
func ActiveID(requestPath string) string {
	switch CanonicalPath(requestPath) {
	case "/":
		return "home"
	case "/discoveries":
		return "discoveries"
	case "/media":
		return "media"
	case "/reviews":
		return "reviews"
	case "/workflows":
		return "workflows"
	case "/trash":
		return "trash"
	default:
		return ""
	}
}

func titleForPath(path string) string {
	switch path {
	case "/":
		return "Overview"
	case "/discoveries":
		return "Discoveries"
	case "/media":
		return "Media"
	case "/downloads":
		return "Downloads"
	case "/descriptors":
		return "Descriptors"
	case "/reviews":
		return "Reviews"
	case "/workflows":
		return "Workflows"
	case "/actions":
		return "Actions"
	case "/trash":
		return "Trash"
	case "/settings":
		return "Settings"
	case "/settings/connections":
		return "Connection settings"
	case "/settings/storage":
		return "Storage settings"
	case "/settings/mappings":
		return "Mapping settings"
	default:
		return "Page not found"
	}
}

func validActiveID(value string) string {
	switch value {
	case "home", "discoveries", "media", "reviews", "workflows", "trash":
		return value
	default:
		return ""
	}
}

func normalizeOrigin(raw string) (string, error) {
	if !utf8.ValidString(raw) || strings.TrimSpace(raw) != raw {
		return "", errors.New("public origin must be valid UTF-8 without surrounding whitespace")
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed == nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.Hostname() == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.Opaque != "" {
		return "", errors.New("public origin must be an absolute HTTP(S) origin")
	}
	if parsed.Path != "" && parsed.Path != "/" {
		return "", errors.New("public origin must not contain a path")
	}
	if port := parsed.Port(); port != "" {
		value, err := strconv.Atoi(port)
		if err != nil || value < 0 || value > 65535 {
			return "", errors.New("public origin contains an invalid port")
		}
	}
	parsed.Path = ""
	parsed.RawPath = ""
	return strings.TrimRight(parsed.String(), "/"), nil
}

func boundedText(value string, max int) string {
	value = strings.TrimSpace(value)
	if len(value) > max {
		value = value[:max]
		for !utf8.ValidString(value) {
			value = value[:len(value)-1]
		}
	}
	if strings.IndexFunc(value, func(r rune) bool { return r < 0x20 || r == 0x7f }) >= 0 {
		return ""
	}
	return value
}
