// Package settings renders read-only configuration observations for the UI
// BFF. It consumes the API over HTTP and never reads YAML, secret files, the
// database, storage mounts, or an upstream service directly.
package settings

import (
	"context"
	"errors"
	"html/template"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	DefaultPageSize = 25
	MaxPageSize     = 100
	MaxCursorLength = 256
	MaxQueryLength  = 64 << 10
	MaxQueryValues  = 32
	MaxValueLength  = 512
	MaxItemsPerPage = 100
	MaxCapabilities = 128
)

// ErrorKind identifies a sanitized settings read failure.
type ErrorKind string

const (
	ErrorNotFound    ErrorKind = "not_found"
	ErrorUnavailable ErrorKind = "unavailable"
	ErrorProtocol    ErrorKind = "protocol"
	ErrorCanceled    ErrorKind = "canceled"
	ErrorTimeout     ErrorKind = "timeout"
)

var (
	ErrNotFound    = errors.New("settings resource not found")
	ErrUnavailable = errors.New("settings API unavailable")
	ErrProtocol    = errors.New("settings API response invalid")
)

// APIError omits endpoint, response bodies, credentials, and raw upstream
// errors. Context cancellation remains available through errors.Is.
type APIError struct {
	kind   ErrorKind
	status int
	cause  error
}

func (e *APIError) Error() string {
	if e == nil {
		return "settings read failed"
	}
	switch e.kind {
	case ErrorNotFound:
		return "settings record not found"
	case ErrorProtocol:
		return "settings response invalid"
	case ErrorCanceled:
		return "settings read canceled"
	case ErrorTimeout:
		return "settings read timed out"
	default:
		return "settings temporarily unavailable"
	}
}

func (e *APIError) Is(target error) bool {
	if e == nil {
		return false
	}
	switch target {
	case ErrNotFound:
		return e.kind == ErrorNotFound
	case ErrUnavailable:
		return e.kind == ErrorUnavailable
	case ErrProtocol:
		return e.kind == ErrorProtocol
	default:
		return false
	}
}

func (e *APIError) Unwrap() error {
	if e == nil || e.cause == nil {
		return nil
	}
	if errors.Is(e.cause, context.Canceled) {
		return context.Canceled
	}
	if errors.Is(e.cause, context.DeadlineExceeded) {
		return context.DeadlineExceeded
	}
	return nil
}

// Kind returns the sanitized failure class.
func (e *APIError) Kind() ErrorKind {
	if e == nil {
		return ""
	}
	return e.kind
}

// Status returns the observed API status, or zero for non-HTTP failures.
func (e *APIError) Status() int {
	if e == nil {
		return 0
	}
	return e.status
}

// PageRequest is the bounded list query accepted by settings readers.
type PageRequest struct {
	Cursor string
	Limit  int
}

// Coverage retains API coverage evidence.
type Coverage struct {
	Completeness     string
	ConnectionID     string
	RootID           string
	SourceID         string
	SnapshotRevision string
	StartedAt        *time.Time
	CompletedAt      *time.Time
	ObservedAt       time.Time
	ObservedCount    *int
	ReasonCodes      []string
}

// PageInfo retains pagination and coverage observations.
type PageInfo struct {
	NextCursor string
	ObservedAt time.Time
	Coverage   []Coverage
}

// SourceMetadata identifies YAML or API provenance. Document IDs and
// revisions are safe opaque metadata; no source file contents are exposed.
type SourceMetadata struct {
	Source       string
	Editable     bool
	DocumentID   string
	Revision     string
	StartupAt    time.Time
	ReloadPolicy string
}

// Configuration is the normalized aggregate configuration projection.
type Configuration struct {
	Source          SourceMetadata
	KeySource       string
	RestartRequired *bool
	Connections     []Connection
	StorageRoots    []StorageRoot
	PathMappings    []PathMapping
	ETag            string
}

// Connection is one configured manager/client instance. CredentialState is
// an enum only; no username, token, password, secret path, or writeOnly value
// is retained.
type Connection struct {
	ID              string
	Kind            string
	Label           string
	Endpoint        string
	Health          string
	CredentialState string
	ObservedVersion string
	Capabilities    []string
	RetiredAt       *time.Time
	Revision        string
	Source          SourceMetadata
	ETag            string
}

// ConnectionCheck retains state and whether the API provided a sanitized
// error. The raw message is intentionally discarded.
type ConnectionCheck struct {
	ID           string
	ConnectionID string
	State        string
	CreatedAt    time.Time
	CompletedAt  *time.Time
	Capabilities []string
	ErrorPresent bool
	ETag         string
}

// StorageRoot is a configured root observation. Path is shown as
// configuration metadata, never accepted as an action target by this package.
type StorageRoot struct {
	ID                   string
	Label                string
	Purpose              string
	Path                 string
	Permission           string
	Capabilities         []string
	WatchEnabled         bool
	WatchIntervalSeconds int
	RetiredAt            *time.Time
	Revision             string
	Source               SourceMetadata
	ETag                 string
}

// PathMapping is one instance-scoped path namespace mapping.
type PathMapping struct {
	ID                string
	ConnectionID      string
	RootID            string
	SourcePrefix      string
	DestinationPrefix string
	Revision          string
	Source            SourceMetadata
	ETag              string
}

// ConnectionPage is one bounded connection page.
type ConnectionPage struct {
	Items []Connection
	Page  PageInfo
}

// StorageRootPage is one bounded storage-root page.
type StorageRootPage struct {
	Items []StorageRoot
	Page  PageInfo
}

// PathMappingPage is one bounded path-mapping page.
type PathMappingPage struct {
	Items []PathMapping
	Page  PageInfo
}

// ConnectionCheckPage is one bounded check page.
type ConnectionCheckPage struct {
	Items []ConnectionCheck
	Page  PageInfo
}

// Reader is the normalized HTTP-only dependency consumed by Handler.
type Reader interface {
	GetConfiguration(context.Context) (Configuration, error)
	ListConnections(context.Context, PageRequest) (ConnectionPage, error)
	GetConnection(context.Context, string) (Connection, error)
	ListStorageRoots(context.Context, PageRequest) (StorageRootPage, error)
	GetStorageRoot(context.Context, string) (StorageRoot, error)
	ListPathMappings(context.Context, PageRequest) (PathMappingPage, error)
	GetPathMapping(context.Context, string) (PathMapping, error)
	ListConnectionChecks(context.Context, PageRequest) (ConnectionCheckPage, error)
	GetConnectionCheck(context.Context, string) (ConnectionCheck, error)
}

// Handler serves read-only settings overview and detail pages.
type Handler struct {
	reader      Reader
	defaultSize int
	maxSize     int
}

// Options bounds settings list pages.
type Options struct {
	DefaultPageSize int
	MaxPageSize     int
}

// NewHandler creates a read-only settings handler.
func NewHandler(reader Reader) http.Handler {
	return NewHandlerWithOptions(reader, Options{})
}

// NewHandlerWithOptions creates a settings handler with explicit bounds.
func NewHandlerWithOptions(reader Reader, options Options) http.Handler {
	defaultSize := options.DefaultPageSize
	if defaultSize <= 0 || defaultSize > DefaultPageSize {
		defaultSize = DefaultPageSize
	}
	maxSize := options.MaxPageSize
	if maxSize <= 0 || maxSize > MaxPageSize {
		maxSize = MaxPageSize
	}
	if defaultSize > maxSize {
		defaultSize = maxSize
	}
	return &Handler{reader: reader, defaultSize: defaultSize, maxSize: maxSize}
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	setPrivateHeaders(w)
	if r == nil || r.URL == nil {
		h.renderError(w, http.StatusBadRequest, "Bad request.", "The settings request is invalid.")
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", http.MethodGet+", "+http.MethodHead)
		h.renderError(w, http.StatusMethodNotAllowed, "Method not allowed.", "Settings views are read-only; changes belong to API-owned endpoints.")
		return
	}
	route, id, detail, ok := parseRoute(r.URL.Path)
	if !ok {
		h.renderError(w, http.StatusNotFound, "Page not found.", "The requested settings page was not found.")
		return
	}
	query, err := parseQuery(r.URL.RawQuery, detail, route, h.defaultSize, h.maxSize)
	if err != nil {
		h.renderError(w, http.StatusBadRequest, "Invalid settings query.", "The supplied settings draft is invalid or too large.")
		return
	}
	if h.reader == nil {
		h.renderError(w, http.StatusServiceUnavailable, "Settings unavailable.", "Retry to load API-owned configuration state.")
		return
	}
	ctx := r.Context()
	switch route {
	case "settings", "configuration":
		configuration, readErr := h.reader.GetConfiguration(ctx)
		if readErr != nil {
			h.handleReadError(w, readErr)
			return
		}
		if err := validateConfiguration(configuration); err != nil {
			h.handleReadError(w, err)
			return
		}
		h.renderConfiguration(w, query, configuration)
	case "connections":
		if detail {
			item, readErr := h.reader.GetConnection(ctx, id)
			if readErr != nil {
				h.handleReadError(w, readErr)
				return
			}
			if item.ID != id || !validateConnection(item) {
				h.handleReadError(w, &APIError{kind: ErrorProtocol})
				return
			}
			h.renderConnection(w, query, item)
			return
		}
		page, readErr := h.reader.ListConnections(ctx, query.PageRequest)
		if readErr != nil {
			h.handleReadError(w, readErr)
			return
		}
		if !validateConnectionPage(page) {
			h.handleReadError(w, &APIError{kind: ErrorProtocol})
			return
		}
		h.renderConnectionList(w, query, page)
	case "storage-roots":
		if detail {
			item, readErr := h.reader.GetStorageRoot(ctx, id)
			if readErr != nil {
				h.handleReadError(w, readErr)
				return
			}
			if item.ID != id || !validateStorageRoot(item) {
				h.handleReadError(w, &APIError{kind: ErrorProtocol})
				return
			}
			h.renderStorageRoot(w, query, item)
			return
		}
		page, readErr := h.reader.ListStorageRoots(ctx, query.PageRequest)
		if readErr != nil {
			h.handleReadError(w, readErr)
			return
		}
		if !validateStorageRootPage(page) {
			h.handleReadError(w, &APIError{kind: ErrorProtocol})
			return
		}
		h.renderStorageRootList(w, query, page)
	case "path-mappings":
		if detail {
			item, readErr := h.reader.GetPathMapping(ctx, id)
			if readErr != nil {
				h.handleReadError(w, readErr)
				return
			}
			if item.ID != id || !validatePathMapping(item) {
				h.handleReadError(w, &APIError{kind: ErrorProtocol})
				return
			}
			h.renderPathMapping(w, query, item)
			return
		}
		page, readErr := h.reader.ListPathMappings(ctx, query.PageRequest)
		if readErr != nil {
			h.handleReadError(w, readErr)
			return
		}
		if !validatePathMappingPage(page) {
			h.handleReadError(w, &APIError{kind: ErrorProtocol})
			return
		}
		h.renderPathMappingList(w, query, page)
	case "connection-checks":
		if detail {
			item, readErr := h.reader.GetConnectionCheck(ctx, id)
			if readErr != nil {
				h.handleReadError(w, readErr)
				return
			}
			if item.ID != id || !validateConnectionCheck(item) {
				h.handleReadError(w, &APIError{kind: ErrorProtocol})
				return
			}
			h.renderConnectionCheck(w, query, item)
			return
		}
		page, readErr := h.reader.ListConnectionChecks(ctx, query.PageRequest)
		if readErr != nil {
			h.handleReadError(w, readErr)
			return
		}
		if !validateConnectionCheckPage(page) {
			h.handleReadError(w, &APIError{kind: ErrorProtocol})
			return
		}
		h.renderConnectionCheckList(w, query, page)
	default:
		h.renderError(w, http.StatusNotFound, "Page not found.", "The requested settings page was not found.")
	}
}

func (h *Handler) handleReadError(w http.ResponseWriter, err error) {
	status := http.StatusServiceUnavailable
	message := "Settings data is temporarily unavailable. Retry to load API-owned state."
	if errors.Is(err, ErrNotFound) {
		status = http.StatusNotFound
		message = "The requested settings record was not found."
	} else if errors.Is(err, ErrProtocol) {
		message = "The API returned incomplete or invalid settings evidence."
	}
	h.renderError(w, status, "Settings unavailable.", message)
}

func (h *Handler) renderError(w http.ResponseWriter, status int, title, message string) {
	p := pageWriter{w: w, status: status, title: title}
	p.start()
	p.text("<main id=\"settings-content\" aria-labelledby=\"settings-title\"><h1 id=\"settings-title\">")
	p.value(title)
	p.text("</h1><p role=\"alert\">")
	p.value(message)
	p.text("</p></main></body></html>")
	p.finish()
}

type pageWriter struct {
	w      http.ResponseWriter
	status int
	title  string
}

func (p *pageWriter) start() {
	p.w.Header().Set("Content-Type", "text/html; charset=utf-8")
	p.w.WriteHeader(p.status)
	_, _ = io.WriteString(p.w, "<!doctype html><html lang=\"en\"><head><meta charset=\"utf-8\"><meta name=\"viewport\" content=\"width=device-width, initial-scale=1\"><meta name=\"robots\" content=\"noindex,nofollow\"><title>")
	p.value(p.title)
	_, _ = io.WriteString(p.w, " · Mastarr</title></head><body><header><a href=\"/\">Mastarr</a><nav aria-label=\"Primary navigation\"><a href=\"/trash\">Trash</a><a href=\"/settings\">Settings</a></nav></header>")
}

func (p *pageWriter) text(value string) { _, _ = io.WriteString(p.w, value) }
func (p *pageWriter) value(value string) {
	_, _ = io.WriteString(p.w, template.HTMLEscapeString(value))
}
func (p *pageWriter) finish() {}

func setPrivateHeaders(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store, private")
	w.Header().Set("Pragma", "no-cache")
	w.Header().Set("X-Robots-Tag", "noindex, nofollow")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
}

type queryState struct {
	PageRequest
	Values map[string]string
}

func parseRoute(path string) (route, id string, detail, ok bool) {
	if path == "" || !utf8.ValidString(path) || !strings.HasPrefix(path, "/") {
		return "", "", false, false
	}
	parts := strings.Split(strings.TrimPrefix(path, "/"), "/")
	if len(parts) == 1 {
		switch parts[0] {
		case "settings", "configuration", "connections", "storage-roots", "path-mappings", "connection-checks":
			return parts[0], "", false, true
		default:
			return "", "", false, false
		}
	}
	if len(parts) == 2 {
		switch parts[0] {
		case "connections", "storage-roots", "path-mappings", "connection-checks":
			if validIdentity(parts[1]) {
				return parts[0], parts[1], true, true
			}
		}
	}
	return "", "", false, false
}

func parseQuery(raw string, detail bool, route string, defaultSize, maxSize int) (queryState, error) {
	if len(raw) > MaxQueryLength {
		return queryState{}, errors.New("query too large")
	}
	values, err := url.ParseQuery(raw)
	if err != nil || len(values) > MaxQueryValues {
		return queryState{}, errors.New("query malformed")
	}
	state := queryState{PageRequest: PageRequest{Limit: defaultSize}, Values: make(map[string]string)}
	for key, entries := range values {
		if len(entries) != 1 || !validInputKey(key) || !validBounded(entries[0], MaxValueLength, true) {
			return queryState{}, errors.New("query value invalid")
		}
		value := entries[0]
		switch key {
		case "cursor":
			if value == "" || len(value) > MaxCursorLength || strings.TrimSpace(value) != value {
				return queryState{}, errors.New("cursor invalid")
			}
			state.Cursor = value
		case "limit":
			parsed, parseErr := strconv.Atoi(value)
			if parseErr != nil || parsed < 1 || parsed > maxSize {
				return queryState{}, errors.New("limit invalid")
			}
			state.Limit = parsed
		default:
			if (!detail && route != "settings" && route != "configuration") || !validDraftKey(route, key) {
				return queryState{}, errors.New("unknown query")
			}
		}
		state.Values[key] = value
	}
	return state, nil
}

func validDraftKey(route, key string) bool {
	if route == "settings" || route == "configuration" || route == "connections" || route == "storage-roots" || route == "path-mappings" {
		switch key {
		case "label", "endpoint", "ifMatch", "idempotencyKey", "operator", "reason", "path", "watchEnabled", "watchIntervalSeconds", "sourcePrefix", "destinationPrefix":
			return true
		}
	}
	return false
}

func validInputKey(value string) bool {
	if value == "" || len(value) > 64 || !utf8.ValidString(value) {
		return false
	}
	for _, r := range value {
		if (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9') && r != '-' && r != '_' {
			return false
		}
	}
	return true
}

func validBounded(value string, max int, allowEmpty bool) bool {
	if !allowEmpty && value == "" {
		return false
	}
	if len(value) > max || !utf8.ValidString(value) {
		return false
	}
	for _, r := range value {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}

func validIdentity(value string) bool {
	if value == "" || value == "." || value == ".." || len(value) > 128 || !utf8.ValidString(value) {
		return false
	}
	for _, r := range value {
		if r == '/' || r == '\\' || r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}

func validConfigID(value string) bool {
	if value == "" || len(value) > 63 || !utf8.ValidString(value) {
		return false
	}
	for i, r := range value {
		alphaNumeric := (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')
		if i == 0 || i == len(value)-1 {
			if !alphaNumeric {
				return false
			}
			continue
		}
		if !alphaNumeric && r != '.' && r != '_' && r != '-' {
			return false
		}
	}
	return true
}

func (q queryState) value(key, fallback string) string {
	if value, ok := q.Values[key]; ok {
		return value
	}
	return fallback
}

func (q queryState) listEncoded() string {
	values := url.Values{}
	if q.Cursor != "" {
		values.Set("cursor", q.Cursor)
	}
	if q.Limit > 0 && q.Limit != DefaultPageSize {
		values.Set("limit", strconv.Itoa(q.Limit))
	}
	encoded := values.Encode()
	if encoded == "" {
		return ""
	}
	return "?" + encoded
}

func (q queryState) encodedDraft(route string) string {
	values := url.Values{}
	for key, value := range q.Values {
		if validDraftKey(route, key) {
			continue
		}
		values.Set(key, value)
	}
	encoded := values.Encode()
	if encoded == "" {
		return ""
	}
	return "?" + encoded
}

func (q queryState) next(cursor string) string {
	values := url.Values{"cursor": []string{cursor}}
	if q.Limit > 0 {
		values.Set("limit", strconv.Itoa(q.Limit))
	}
	return values.Encode()
}

func validateSource(source SourceMetadata) bool {
	return (source.Source == "yaml" || source.Source == "api") && validBounded(source.DocumentID, 256, false) && validBounded(source.Revision, 256, false) && !source.StartupAt.IsZero() && source.ReloadPolicy == "restart_required"
}

func validateConfiguration(value Configuration) error {
	if !validateSource(value.Source) || value.KeySource == "" || value.Connections == nil || value.StorageRoots == nil || value.PathMappings == nil {
		return &APIError{kind: ErrorProtocol}
	}
	seenConnections := map[string]struct{}{}
	for _, item := range value.Connections {
		if !validateConnection(item) {
			return &APIError{kind: ErrorProtocol}
		}
		if _, exists := seenConnections[item.ID]; exists {
			return &APIError{kind: ErrorProtocol}
		}
		seenConnections[item.ID] = struct{}{}
	}
	seenRoots := map[string]struct{}{}
	for _, item := range value.StorageRoots {
		if !validateStorageRoot(item) {
			return &APIError{kind: ErrorProtocol}
		}
		if _, exists := seenRoots[item.ID]; exists {
			return &APIError{kind: ErrorProtocol}
		}
		seenRoots[item.ID] = struct{}{}
	}
	seenMappings := map[string]struct{}{}
	for _, item := range value.PathMappings {
		if !validatePathMapping(item) {
			return &APIError{kind: ErrorProtocol}
		}
		if _, exists := seenMappings[item.ID]; exists {
			return &APIError{kind: ErrorProtocol}
		}
		seenMappings[item.ID] = struct{}{}
	}
	return nil
}

func validateConnectionPage(page ConnectionPage) bool {
	if page.Items == nil || len(page.Items) > MaxItemsPerPage || page.Page.ObservedAt.IsZero() {
		return false
	}
	seen := map[string]struct{}{}
	for _, item := range page.Items {
		if !validateConnection(item) {
			return false
		}
		if _, exists := seen[item.ID]; exists {
			return false
		}
		seen[item.ID] = struct{}{}
	}
	return validatePage(page.Page)
}

func validateStorageRootPage(page StorageRootPage) bool {
	if page.Items == nil || len(page.Items) > MaxItemsPerPage || page.Page.ObservedAt.IsZero() {
		return false
	}
	seen := map[string]struct{}{}
	for _, item := range page.Items {
		if !validateStorageRoot(item) {
			return false
		}
		if _, exists := seen[item.ID]; exists {
			return false
		}
		seen[item.ID] = struct{}{}
	}
	return validatePage(page.Page)
}

func validatePathMappingPage(page PathMappingPage) bool {
	if page.Items == nil || len(page.Items) > MaxItemsPerPage || page.Page.ObservedAt.IsZero() {
		return false
	}
	seen := map[string]struct{}{}
	for _, item := range page.Items {
		if !validatePathMapping(item) {
			return false
		}
		if _, exists := seen[item.ID]; exists {
			return false
		}
		seen[item.ID] = struct{}{}
	}
	return validatePage(page.Page)
}

func validateConnectionCheckPage(page ConnectionCheckPage) bool {
	if page.Items == nil || len(page.Items) > MaxItemsPerPage || page.Page.ObservedAt.IsZero() {
		return false
	}
	seen := map[string]struct{}{}
	for _, item := range page.Items {
		if !validateConnectionCheck(item) {
			return false
		}
		if _, exists := seen[item.ID]; exists {
			return false
		}
		seen[item.ID] = struct{}{}
	}
	return validatePage(page.Page)
}

func validatePage(page PageInfo) bool {
	if page.ObservedAt.IsZero() {
		return false
	}
	if page.NextCursor != "" && (!validBounded(page.NextCursor, MaxCursorLength, false) || strings.TrimSpace(page.NextCursor) != page.NextCursor) {
		return false
	}
	for _, coverage := range page.Coverage {
		if !knownCompleteness(coverage.Completeness) || coverage.ObservedAt.IsZero() {
			return false
		}
	}
	return true
}

func knownCompleteness(value string) bool {
	return value == "complete" || value == "partial" || value == "unknown"
}

func validateConnection(value Connection) bool {
	return validConfigID(value.ID) && knownKind(value.Kind) && validBounded(value.Label, 256, false) && validBounded(value.Endpoint, 1024, false) && knownHealth(value.Health) && knownCredentialState(value.CredentialState) && validateSource(value.Source) && value.Revision != "" && len(value.Capabilities) <= MaxCapabilities
}

func validateStorageRoot(value StorageRoot) bool {
	return validConfigID(value.ID) && validBounded(value.Label, 256, false) && knownPurpose(value.Purpose) && validBounded(value.Path, 4096, false) && validPermission(value.Permission) && validateSource(value.Source) && value.Revision != "" && len(value.Capabilities) <= MaxCapabilities && value.WatchIntervalSeconds > 0
}

func validatePathMapping(value PathMapping) bool {
	return validConfigID(value.ID) && validConfigID(value.ConnectionID) && validConfigID(value.RootID) && validBounded(value.SourcePrefix, 4096, false) && validBounded(value.DestinationPrefix, 4096, false) && validateSource(value.Source) && value.Revision != ""
}

func validateConnectionCheck(value ConnectionCheck) bool {
	return validIdentity(value.ID) && validConfigID(value.ConnectionID) && knownCheckState(value.State) && !value.CreatedAt.IsZero() && len(value.Capabilities) <= MaxCapabilities
}

func knownCheckState(value string) bool {
	switch value {
	case "queued", "running", "succeeded", "failed", "cancelled":
		return true
	default:
		return false
	}
}

func knownKind(value string) bool {
	switch value {
	case "qbittorrent", "nzbget", "radarr", "sonarr", "jellyfin", "seerr":
		return true
	default:
		return false
	}
}

func knownHealth(value string) bool {
	switch value {
	case "healthy", "degraded", "unavailable", "unknown":
		return true
	default:
		return false
	}
}

func knownCredentialState(value string) bool {
	switch value {
	case "managed", "missing", "invalid", "static_reference":
		return true
	default:
		return false
	}
}

func knownPurpose(value string) bool {
	switch value {
	case "download", "library", "descriptor", "trash":
		return true
	default:
		return false
	}
}

func validPermission(value string) bool {
	return value == "" || value == "read_only" || value == "read_write" || value == "unavailable" || value == "unknown"
}

func timeLabel(value time.Time) string {
	if value.IsZero() {
		return "unknown"
	}
	return value.UTC().Format(time.RFC3339)
}

func optionalTimeLabel(value *time.Time) string {
	if value == nil {
		return "unknown"
	}
	return timeLabel(*value)
}

func countLabel(value int) string {
	if value == 0 {
		return "unknown"
	}
	return strconv.Itoa(value)
}
