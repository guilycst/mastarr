// Package inventory renders the read-only discovery and inventory views for
// the UI BFF. It consumes normalized observations and never reaches into the
// root application, database, filesystem, or an upstream service.
package inventory

import (
	"context"
	"errors"
	"fmt"
	"html/template"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	DefaultPageSize       = 25
	MaxPageSize           = 100
	MaxCursorLength       = 256
	MaxIdentityLength     = 128
	MaxInputLength        = 240
	MaxItemsPerPage       = 100
	MaxFilesPerDiscovery  = 1000
	MaxCandidates         = 100
	MaxProvenance         = 100
	MaxTracking           = 200
	MaxAssociationInputs  = 256
	MaxQueryValues        = 512
	MaxRenderedTextLength = 240
)

// ErrorKind identifies a sanitized inventory read failure.
type ErrorKind string

const (
	ErrorNotFound    ErrorKind = "not_found"
	ErrorUnavailable ErrorKind = "unavailable"
	ErrorProtocol    ErrorKind = "protocol"
	ErrorCanceled    ErrorKind = "canceled"
	ErrorTimeout     ErrorKind = "timeout"
)

var (
	// ErrNotFound is returned when a detail identity is absent from an
	// authoritative API response.
	ErrNotFound = errors.New("inventory resource not found")
	// ErrUnavailable is returned when the API cannot provide inventory data.
	ErrUnavailable = errors.New("inventory API unavailable")
	// ErrProtocol is returned when an API response is malformed or incomplete.
	ErrProtocol = errors.New("inventory API response invalid")
)

// APIError is safe to display in a generic UI error state. It intentionally
// omits endpoint, response body, credentials, and transport details while
// retaining context cancellation identity through errors.Is.
type APIError struct {
	kind   ErrorKind
	status int
	cause  error
}

func (e *APIError) Error() string {
	if e == nil {
		return "inventory read failed"
	}
	switch e.kind {
	case ErrorNotFound:
		return "inventory record not found"
	case ErrorCanceled:
		return "inventory read canceled"
	case ErrorTimeout:
		return "inventory read timed out"
	case ErrorProtocol:
		return "inventory response invalid"
	default:
		return "inventory temporarily unavailable"
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

// Status returns the observed HTTP status, or zero for non-HTTP failures.
func (e *APIError) Status() int {
	if e == nil {
		return 0
	}
	return e.status
}

// PageRequest is the bounded, normalized subset of URL query state accepted
// by inventory list reads.
type PageRequest struct {
	Cursor       string
	Limit        int
	RootID       string
	ConnectionID string
	Kind         string
}

// PageInfo contains API-owned pagination and coverage evidence.
type PageInfo struct {
	NextCursor *string
	ObservedAt time.Time
	Coverage   []Coverage
}

// Coverage keeps incomplete and unknown observations explicit in the UI.
type Coverage struct {
	Completeness     string
	ConnectionID     string
	RootID           string
	SourceID         string
	SnapshotRevision string
	ObservedAt       time.Time
	ObservedCount    *int
	ReasonCodes      []string
}

// File is a root-relative discovery file observation. Association fields are
// intentionally rendered as editable UI inputs without a write action here.
type File struct {
	RelativePath string
	RootID       string
	Type         string
	Role         string
	Size         int
	Digest       string
	FileIdentity string
	ObservedAt   *time.Time
}

// Provenance is download/client evidence attached to a discovery.
type Provenance struct {
	ConnectionID string
	ClientItemID string
	DescriptorID string
	Hash         string
	SourcePath   string
	CompletedAt  *time.Time
}

// Candidate is a metadata identity suggestion. It remains editable and is
// never treated as an approval or an executed action by this package.
type Candidate struct {
	Title      string
	Kind       string
	ProviderID string
	ExternalID string
	Season     *int
	Episodes   []int
	Year       *int
	Score      *float32
}

// Discovery is a normalized observed file group.
type Discovery struct {
	ID         string
	ObservedAt time.Time
	Readiness  string
	Files      []File
	Provenance []Provenance
	Candidates []Candidate
	Coverage   *Coverage
}

// Tracking is an instance-scoped observation. Empty values are displayed as
// unknown; no value is inferred from a missing observation.
type Tracking struct {
	ConnectionID string
	Dimension    string
	Value        string
	ProviderID   string
	ExternalID   string
	ObservedAt   time.Time
	CoverageID   string
	Evidence     []string
}

// Media is an aggregated identity with independent per-instance tracking.
type Media struct {
	ID           string
	Kind         string
	ProviderID   string
	Title        string
	ObservedAt   time.Time
	DiscoveryIDs []string
	Tracking     []Tracking
}

// Download is a download-client observation with explicit state and
// provenance. State is never interpreted as media availability.
type Download struct {
	ID           string
	ConnectionID string
	ClientItemID string
	Hash         string
	NzbID        string
	DeprecatedID string
	DescriptorID string
	SourcePath   string
	State        string
	ObservedAt   time.Time
	CompletedAt  *time.Time
	Coverage     *Coverage
}

// Descriptor is metadata about retained original bytes. Content itself is
// not fetched or rendered by this read-only view.
type Descriptor struct {
	ID             string
	Type           string
	Size           int
	Digest         string
	Availability   string
	Source         string
	CapturedAt     time.Time
	RetentionUntil *time.Time
}

// DiscoveryPage is one bounded API page.
type DiscoveryPage struct {
	Items []Discovery
	Page  PageInfo
}

// MediaPage is one bounded API page.
type MediaPage struct {
	Items []Media
	Page  PageInfo
}

// DownloadPage is one bounded API page.
type DownloadPage struct {
	Items []Download
	Page  PageInfo
}

// DescriptorPage is one bounded API page.
type DescriptorPage struct {
	Items []Descriptor
	Page  PageInfo
}

// Reader is the normalized HTTP-only dependency consumed by Handler. No
// generated API DTO crosses this boundary.
type Reader interface {
	ListDiscoveries(context.Context, PageRequest) (DiscoveryPage, error)
	GetDiscovery(context.Context, string) (Discovery, error)
	ListMedia(context.Context, PageRequest) (MediaPage, error)
	GetMedia(context.Context, string) (Media, error)
	ListDownloads(context.Context, PageRequest) (DownloadPage, error)
	GetDownload(context.Context, string) (Download, error)
	ListDescriptors(context.Context, PageRequest) (DescriptorPage, error)
	GetDescriptor(context.Context, string) (Descriptor, error)
}

// Options bounds the handler's page size. Zero values select the fixed
// default. Options do not enable mutation routes.
type Options struct {
	DefaultPageSize int
	MaxPageSize     int
}

// Handler serves read-only inventory lists and detail pages.
type Handler struct {
	reader      Reader
	defaultSize int
	maxSize     int
}

// NewHandler constructs a bounded read-only inventory handler.
func NewHandler(reader Reader) http.Handler {
	return NewHandlerWithOptions(reader, Options{})
}

// NewHandlerWithOptions constructs a handler with explicit page bounds.
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
		h.renderError(w, http.StatusBadRequest, "Bad request.", "The inventory request is invalid.")
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", http.MethodGet+", "+http.MethodHead)
		h.renderError(w, http.StatusMethodNotAllowed, "Method not allowed.", "Inventory views are read-only.")
		return
	}
	route, id, detail, ok := parseRoute(r.URL.Path)
	if !ok {
		h.renderError(w, http.StatusNotFound, "Page not found.", "The requested inventory page was not found.")
		return
	}
	query, err := parseQuery(r.URL.RawQuery, route, detail, h.defaultSize, h.maxSize)
	if err != nil {
		h.renderError(w, http.StatusBadRequest, "Invalid inventory query.", "The supplied inventory filters are invalid or too large.")
		return
	}
	if h.reader == nil {
		h.renderError(w, http.StatusServiceUnavailable, "Inventory unavailable.", "Retry to load API-owned inventory state.")
		return
	}

	ctx := r.Context()
	switch route {
	case "discoveries":
		if detail {
			item, readErr := h.reader.GetDiscovery(ctx, id)
			if readErr != nil {
				h.handleReadError(w, readErr)
				return
			}
			if item.ID != id {
				h.handleReadError(w, validationError("discovery detail identity does not match request"))
				return
			}
			if err := validateDiscovery(item); err != nil {
				h.handleReadError(w, err)
				return
			}
			h.renderDiscoveryDetail(w, route, query, item)
			return
		}
		page, readErr := h.reader.ListDiscoveries(ctx, query.PageRequest)
		if readErr != nil {
			h.handleReadError(w, readErr)
			return
		}
		if err := validateDiscoveryPage(page); err != nil {
			h.handleReadError(w, err)
			return
		}
		h.renderDiscoveryList(w, query, page)
	case "media":
		if detail {
			item, readErr := h.reader.GetMedia(ctx, id)
			if readErr != nil {
				h.handleReadError(w, readErr)
				return
			}
			if item.ID != id {
				h.handleReadError(w, validationError("media detail identity does not match request"))
				return
			}
			if err := validateMedia(item); err != nil {
				h.handleReadError(w, err)
				return
			}
			h.renderMediaDetail(w, route, query, item)
			return
		}
		page, readErr := h.reader.ListMedia(ctx, query.PageRequest)
		if readErr != nil {
			h.handleReadError(w, readErr)
			return
		}
		if err := validateMediaPage(page); err != nil {
			h.handleReadError(w, err)
			return
		}
		h.renderMediaList(w, query, page)
	case "downloads":
		if detail {
			item, readErr := h.reader.GetDownload(ctx, id)
			if readErr != nil {
				h.handleReadError(w, readErr)
				return
			}
			if item.ID != id {
				h.handleReadError(w, validationError("download detail identity does not match request"))
				return
			}
			if err := validateDownload(item); err != nil {
				h.handleReadError(w, err)
				return
			}
			h.renderDownloadDetail(w, route, query, item)
			return
		}
		page, readErr := h.reader.ListDownloads(ctx, query.PageRequest)
		if readErr != nil {
			h.handleReadError(w, readErr)
			return
		}
		if err := validateDownloadPage(page); err != nil {
			h.handleReadError(w, err)
			return
		}
		h.renderDownloadList(w, query, page)
	case "descriptors":
		if detail {
			item, readErr := h.reader.GetDescriptor(ctx, id)
			if readErr != nil {
				h.handleReadError(w, readErr)
				return
			}
			if item.ID != id {
				h.handleReadError(w, validationError("descriptor detail identity does not match request"))
				return
			}
			if err := validateDescriptor(item); err != nil {
				h.handleReadError(w, err)
				return
			}
			h.renderDescriptorDetail(w, route, query, item)
			return
		}
		page, readErr := h.reader.ListDescriptors(ctx, query.PageRequest)
		if readErr != nil {
			h.handleReadError(w, readErr)
			return
		}
		if err := validateDescriptorPage(page); err != nil {
			h.handleReadError(w, err)
			return
		}
		h.renderDescriptorList(w, query, page)
	default:
		h.renderError(w, http.StatusNotFound, "Page not found.", "The requested inventory page was not found.")
	}
}

func (h *Handler) handleReadError(w http.ResponseWriter, err error) {
	status := http.StatusServiceUnavailable
	message := "Inventory data is temporarily unavailable. Retry to load API-owned state."
	if errors.Is(err, ErrNotFound) {
		status = http.StatusNotFound
		message = "The requested inventory record was not found."
	} else if errors.Is(err, ErrProtocol) {
		message = "The API returned incomplete or invalid inventory evidence."
	}
	h.renderError(w, status, "Inventory unavailable.", message)
}

func (h *Handler) renderError(w http.ResponseWriter, status int, title, message string) {
	data := pageWriter{w: w, status: status, title: title}
	data.start()
	data.text("<main id=\"inventory-content\" aria-labelledby=\"inventory-title\">")
	data.text("<h1 id=\"inventory-title\">")
	data.value(title)
	data.text("</h1><p role=\"alert\">")
	data.value(message)
	data.text("</p></main></body></html>")
	data.finish()
}

func setPrivateHeaders(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
	w.Header().Set("X-Robots-Tag", "noindex, nofollow")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
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
	_, _ = io.WriteString(p.w, " · Mastarr</title></head><body><header><a href=\"/\">Mastarr</a><nav aria-label=\"Primary navigation\"><a href=\"/discoveries\">Discoveries</a><a href=\"/media\">Media</a><a href=\"/downloads\">Downloads</a><a href=\"/descriptors\">Descriptors</a></nav></header>")
}

func (p *pageWriter) text(value string) {
	_, _ = io.WriteString(p.w, value)
}

func (p *pageWriter) value(value string) {
	_, _ = io.WriteString(p.w, template.HTMLEscapeString(value))
}

func (p *pageWriter) attr(name, value string) {
	_, _ = io.WriteString(p.w, " "+name+"=\""+template.HTMLEscapeString(value)+"\"")
}

func (p *pageWriter) finish() {
	_, _ = io.WriteString(p.w, "")
}

type queryState struct {
	PageRequest
	Values map[string]string
	Raw    string
}

func parseRoute(rawPath string) (route, id string, detail, ok bool) {
	if rawPath == "" || !utf8.ValidString(rawPath) || !strings.HasPrefix(rawPath, "/") {
		return "", "", false, false
	}
	parts := strings.Split(strings.TrimPrefix(rawPath, "/"), "/")
	if len(parts) == 1 {
		switch parts[0] {
		case "discoveries", "media", "downloads", "descriptors":
			return parts[0], "", false, true
		default:
			return "", "", false, false
		}
	}
	if len(parts) != 2 {
		return "", "", false, false
	}
	switch parts[0] {
	case "discoveries", "media", "downloads", "descriptors":
	default:
		return "", "", false, false
	}
	if !validIdentity(parts[1]) {
		return "", "", false, false
	}
	return parts[0], parts[1], true, true
}

func parseQuery(raw, route string, detail bool, defaultSize, maxSize int) (queryState, error) {
	if len(raw) > MaxCursorLength+MaxQueryValues*MaxInputLength {
		return queryState{}, errors.New("query too large")
	}
	values, err := url.ParseQuery(raw)
	if err != nil {
		return queryState{}, errors.New("query malformed")
	}
	if len(values) > MaxQueryValues {
		return queryState{}, errors.New("query has too many fields")
	}
	state := queryState{PageRequest: PageRequest{Limit: defaultSize}, Values: make(map[string]string), Raw: raw}
	detailInputCount := 0
	for key, entries := range values {
		if len(entries) != 1 {
			return queryState{}, errors.New("duplicate query value")
		}
		value := entries[0]
		if !utf8.ValidString(value) || len(value) > MaxInputLength {
			return queryState{}, errors.New("query value too large")
		}
		if key == "cursor" {
			if len(value) > MaxCursorLength || strings.TrimSpace(value) != value {
				return queryState{}, errors.New("cursor invalid")
			}
			state.Cursor = value
			state.Values[key] = value
			continue
		}
		if key == "limit" {
			parsed, parseErr := strconv.Atoi(value)
			if parseErr != nil || parsed < 1 || parsed > maxSize {
				return queryState{}, errors.New("limit invalid")
			}
			state.Limit = parsed
			state.Values[key] = value
			continue
		}
		if key == "rootId" {
			if !validIdentity(value) {
				return queryState{}, errors.New("root identity invalid")
			}
			state.RootID = value
			state.Values[key] = value
			continue
		}
		if key == "connectionId" {
			if !validIdentity(value) {
				return queryState{}, errors.New("connection identity invalid")
			}
			state.ConnectionID = value
			state.Values[key] = value
			continue
		}
		if key == "kind" {
			if !validKind(value) {
				return queryState{}, errors.New("kind invalid")
			}
			state.Kind = value
			state.Values[key] = value
			continue
		}
		if detail && allowedDetailInput(key) {
			detailInputCount++
			if detailInputCount > MaxAssociationInputs {
				return queryState{}, errors.New("too many detail inputs")
			}
			state.Values[key] = value
			continue
		}
		if !detail {
			return queryState{}, errors.New("unknown list query")
		}
		return queryState{}, errors.New("unknown detail query")
	}
	if route != "discoveries" && state.RootID != "" {
		return queryState{}, errors.New("root filter not valid for route")
	}
	if route != "downloads" && state.ConnectionID != "" {
		return queryState{}, errors.New("connection filter not valid for route")
	}
	if route != "media" && state.Kind != "" {
		return queryState{}, errors.New("kind filter not valid for route")
	}
	return state, nil
}

func allowedDetailInput(key string) bool {
	for _, fixed := range []string{"selection", "identity", "providerId", "episode", "subtitleLanguage", "subtitleForced", "subtitleSDH", "subtitlePair", "kind"} {
		if key == fixed {
			return true
		}
	}
	for _, prefix := range []string{"association-", "candidate-"} {
		if strings.HasPrefix(key, prefix) && len(key) <= MaxIdentityLength {
			return validInputKey(key)
		}
	}
	return false
}

func validInputKey(value string) bool {
	if value == "" || len(value) > MaxIdentityLength || !utf8.ValidString(value) {
		return false
	}
	for _, character := range value {
		if (character < 'a' || character > 'z') && (character < 'A' || character > 'Z') &&
			(character < '0' || character > '9') && character != '-' && character != '_' {
			return false
		}
	}
	return true
}

func validIdentity(value string) bool {
	if value == "" || value == "." || value == ".." || len(value) > MaxIdentityLength || !utf8.ValidString(value) {
		return false
	}
	for _, character := range value {
		if character == '/' || character == '\\' || character < 0x20 || character == 0x7f {
			return false
		}
	}
	return true
}

func validConfigID(value string) bool {
	if value == "" || len(value) > 63 || !utf8.ValidString(value) {
		return false
	}
	for index, character := range value {
		alphaNumeric := (character >= 'a' && character <= 'z') || (character >= '0' && character <= '9')
		if index == 0 || index == len(value)-1 {
			if !alphaNumeric {
				return false
			}
			continue
		}
		if !alphaNumeric && character != '.' && character != '_' && character != '-' {
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
	for _, character := range value {
		if character < 0x20 || character == 0x7f {
			return false
		}
	}
	return true
}

func validOptionalIdentity(value string) bool {
	return value == "" || validIdentity(value)
}

func validOptionalConfigID(value string) bool {
	return value == "" || validConfigID(value)
}

func validRelativePath(value string) bool {
	if value == "" || !utf8.ValidString(value) || strings.HasPrefix(value, "/") || strings.HasPrefix(value, `\`) || strings.ContainsRune(value, '\x00') {
		return false
	}
	for _, component := range strings.Split(value, "/") {
		if component == "" || component == "." || component == ".." {
			return false
		}
	}
	return true
}

func validKind(value string) bool {
	switch value {
	case "movie", "episode", "season", "anime":
		return true
	default:
		return false
	}
}

func (q queryState) encoded(keepDetail bool) string {
	values := url.Values{}
	for key, value := range q.Values {
		if !keepDetail && (key == "identity" || key == "episode" || key == "subtitleLanguage" || key == "subtitleForced" || key == "subtitleSDH" || key == "subtitlePair" || strings.HasPrefix(key, "association-") || strings.HasPrefix(key, "candidate-")) {
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

func (q queryState) value(key, fallback string) string {
	if value, ok := q.Values[key]; ok {
		return value
	}
	return fallback
}

func (q queryState) pageLink(route string, keepDetail bool, cursor string) string {
	values := url.Values{}
	for key, value := range q.Values {
		if !keepDetail && (key == "identity" || key == "episode" || key == "subtitleLanguage" || key == "subtitleForced" || key == "subtitleSDH" || key == "subtitlePair" || strings.HasPrefix(key, "association-") || strings.HasPrefix(key, "candidate-")) {
			continue
		}
		if key != "cursor" {
			values.Set(key, value)
		}
	}
	if cursor != "" {
		values.Set("cursor", cursor)
	}
	encoded := values.Encode()
	if encoded != "" {
		return "/" + route + "?" + encoded
	}
	return "/" + route
}

func pathFor(route, id string) string {
	if !validIdentity(id) {
		return "/" + route
	}
	return "/" + route + "/" + url.PathEscape(id)
}

func titleForRoute(route string, detail bool) string {
	title := map[string]string{
		"discoveries": "Discoveries",
		"media":       "Media",
		"downloads":   "Downloads",
		"descriptors": "Descriptors",
	}[route]
	if title == "" {
		title = "Inventory"
	}
	if detail {
		switch route {
		case "discoveries":
			return "Discovery detail"
		case "media":
			return "Media detail"
		case "downloads":
			return "Download detail"
		case "descriptors":
			return "Descriptor detail"
		}
	}
	return title
}

func knownOrUnknown(value string) string {
	if value == "" {
		return "unknown"
	}
	return boundedText(value)
}

func boundedText(value string) string {
	if !utf8.ValidString(value) {
		return "unknown"
	}
	runes := []rune(value)
	if len(runes) > MaxRenderedTextLength {
		return string(runes[:MaxRenderedTextLength]) + "…"
	}
	return string(runes)
}

func stateLabel(value string) string {
	return knownOrUnknown(value)
}

func coverageLabel(coverage *Coverage) string {
	if coverage == nil {
		return "coverage: unknown"
	}
	return "coverage: " + knownOrUnknown(coverage.Completeness)
}

func coveragePageLabel(page PageInfo) string {
	if len(page.Coverage) == 0 {
		return "coverage: unknown"
	}
	parts := make([]string, 0, len(page.Coverage))
	for _, coverage := range page.Coverage {
		parts = append(parts, knownOrUnknown(coverage.Completeness))
	}
	return "coverage: " + strings.Join(parts, ", ")
}

func countLabel(value int) string {
	if value < 0 {
		return "unknown"
	}
	return strconv.Itoa(value)
}

func intPointerValue(value *int) string {
	if value == nil {
		return ""
	}
	return strconv.Itoa(*value)
}

func floatPointerValue(value *float32) string {
	if value == nil {
		return ""
	}
	return strconv.FormatFloat(float64(*value), 'f', 2, 32)
}

func validationError(message string) error {
	return &APIError{kind: ErrorProtocol, cause: errors.New(message)}
}

func validatePage(page PageInfo) error {
	if page.ObservedAt.IsZero() {
		return validationError("page observation time is missing")
	}
	if len(page.Coverage) > MaxTracking {
		return validationError("coverage exceeds UI limit")
	}
	if page.NextCursor != nil {
		if len(*page.NextCursor) == 0 || len(*page.NextCursor) > MaxCursorLength || !utf8.ValidString(*page.NextCursor) {
			return validationError("pagination cursor is invalid")
		}
	}
	for _, coverage := range page.Coverage {
		if err := validateCoverage(coverage); err != nil {
			return err
		}
	}
	return nil
}

func validateCoverage(coverage Coverage) error {
	if !validCompleteness(coverage.Completeness) || coverage.ObservedAt.IsZero() {
		return validationError("coverage evidence is incomplete")
	}
	if !validOptionalConfigID(coverage.ConnectionID) || !validOptionalConfigID(coverage.RootID) || !validOptionalIdentity(coverage.SourceID) || !validBounded(coverage.SnapshotRevision, MaxInputLength, true) {
		return validationError("coverage identity or revision is invalid")
	}
	if len(coverage.ReasonCodes) > MaxTracking {
		return validationError("coverage reasons exceed UI limit")
	}
	for _, reason := range coverage.ReasonCodes {
		if !validBounded(reason, MaxInputLength, false) {
			return validationError("coverage reason is invalid")
		}
	}
	if coverage.ObservedCount != nil && *coverage.ObservedCount < 0 {
		return validationError("coverage count is invalid")
	}
	return nil
}

func validateDiscoveryPage(page DiscoveryPage) error {
	if len(page.Items) > MaxItemsPerPage {
		return validationError("discovery page exceeds UI limit")
	}
	if err := validatePage(page.Page); err != nil {
		return err
	}
	for _, item := range page.Items {
		if err := validateDiscovery(item); err != nil {
			return err
		}
	}
	return nil
}

func validateDiscovery(item Discovery) error {
	if !validIdentity(item.ID) || len(item.Files) > MaxFilesPerDiscovery || len(item.Candidates) > MaxCandidates || len(item.Provenance) > MaxProvenance {
		return validationError("discovery identity or size is invalid")
	}
	if !validReadiness(item.Readiness) || item.ObservedAt.IsZero() {
		return validationError("discovery readiness is missing")
	}
	if item.Coverage != nil {
		if err := validateCoverage(*item.Coverage); err != nil {
			return err
		}
	}
	for _, file := range item.Files {
		if err := validateFile(file); err != nil {
			return err
		}
	}
	for _, provenance := range item.Provenance {
		if err := validateProvenance(provenance); err != nil {
			return err
		}
	}
	for _, candidate := range item.Candidates {
		if err := validateCandidate(candidate); err != nil {
			return err
		}
	}
	return nil
}

func validateMediaPage(page MediaPage) error {
	if len(page.Items) > MaxItemsPerPage {
		return validationError("media page exceeds UI limit")
	}
	if err := validatePage(page.Page); err != nil {
		return err
	}
	for _, item := range page.Items {
		if err := validateMedia(item); err != nil {
			return err
		}
	}
	return nil
}

func validateMedia(item Media) error {
	if !validIdentity(item.ID) || !validKind(item.Kind) || !validBounded(item.ProviderID, MaxInputLength, false) || !validBounded(item.Title, MaxRenderedTextLength, true) || item.ObservedAt.IsZero() || len(item.Tracking) > MaxTracking || len(item.DiscoveryIDs) > MaxFilesPerDiscovery {
		return validationError("media identity or size is invalid")
	}
	for _, discoveryID := range item.DiscoveryIDs {
		if !validIdentity(discoveryID) {
			return validationError("media discovery identity is invalid")
		}
	}
	for _, tracking := range item.Tracking {
		if err := validateTracking(tracking); err != nil {
			return err
		}
	}
	return nil
}

func validateDownloadPage(page DownloadPage) error {
	if len(page.Items) > MaxItemsPerPage {
		return validationError("download page exceeds UI limit")
	}
	if err := validatePage(page.Page); err != nil {
		return err
	}
	for _, item := range page.Items {
		if err := validateDownload(item); err != nil {
			return err
		}
	}
	return nil
}

func validateDownload(item Download) error {
	if !validIdentity(item.ID) || !validConfigID(item.ConnectionID) || !validDownloadState(item.State) || item.ObservedAt.IsZero() {
		return validationError("download identity or state is invalid")
	}
	if !validBounded(item.ClientItemID, MaxInputLength, true) || !validBounded(item.Hash, MaxInputLength, true) || !validBounded(item.NzbID, MaxInputLength, true) || !validBounded(item.DeprecatedID, MaxInputLength, true) || !validOptionalIdentity(item.DescriptorID) || !validOptionalTarget(item.SourcePath) || (item.CompletedAt != nil && item.CompletedAt.IsZero()) {
		return validationError("download provenance is invalid")
	}
	if item.Coverage != nil {
		if err := validateCoverage(*item.Coverage); err != nil {
			return err
		}
	}
	return nil
}

func validateDescriptorPage(page DescriptorPage) error {
	if len(page.Items) > MaxItemsPerPage {
		return validationError("descriptor page exceeds UI limit")
	}
	if err := validatePage(page.Page); err != nil {
		return err
	}
	for _, item := range page.Items {
		if err := validateDescriptor(item); err != nil {
			return err
		}
	}
	return nil
}

func validateDescriptor(item Descriptor) error {
	if !validIdentity(item.ID) || !validDescriptorType(item.Type) || item.Size < 0 || !validDescriptorAvailability(item.Availability) || !validBounded(item.Digest, MaxInputLength, false) || !validBounded(item.Source, MaxInputLength, true) || item.CapturedAt.IsZero() || (item.RetentionUntil != nil && item.RetentionUntil.IsZero()) {
		return validationError("descriptor identity or state is invalid")
	}
	return nil
}

func validateFile(file File) error {
	if !validConfigID(file.RootID) || !validRelativePath(file.RelativePath) || len(file.RelativePath) > MaxInputLength || !validFileType(file.Type) || !validFileRole(file.Role) || file.Size < 0 || !validBounded(file.Digest, MaxInputLength, true) || !validBounded(file.FileIdentity, MaxIdentityLength, true) || (file.ObservedAt != nil && file.ObservedAt.IsZero()) {
		return validationError("discovery file is invalid")
	}
	return nil
}

func validateProvenance(provenance Provenance) error {
	if !validOptionalConfigID(provenance.ConnectionID) || !validBounded(provenance.ClientItemID, MaxInputLength, true) || !validOptionalIdentity(provenance.DescriptorID) || !validBounded(provenance.Hash, MaxInputLength, true) || !validOptionalTarget(provenance.SourcePath) || (provenance.CompletedAt != nil && provenance.CompletedAt.IsZero()) {
		return validationError("provenance evidence is invalid")
	}
	return nil
}

func validateCandidate(candidate Candidate) error {
	if !validBounded(candidate.Title, MaxInputLength, false) || !validKind(candidate.Kind) || !validBounded(candidate.ProviderID, MaxInputLength, true) || !validBounded(candidate.ExternalID, MaxInputLength, true) || len(candidate.Episodes) > MaxAssociationInputs {
		return validationError("candidate evidence is invalid")
	}
	if candidate.Season != nil && *candidate.Season < 0 {
		return validationError("candidate season is invalid")
	}
	if candidate.Year != nil && *candidate.Year < 0 {
		return validationError("candidate year is invalid")
	}
	for _, episode := range candidate.Episodes {
		if episode < 0 {
			return validationError("candidate episode is invalid")
		}
	}
	if candidate.Score != nil && (math.IsNaN(float64(*candidate.Score)) || math.IsInf(float64(*candidate.Score), 0)) {
		return validationError("candidate score is invalid")
	}
	return nil
}

func validateTracking(tracking Tracking) error {
	if len(tracking.Evidence) > MaxTracking || !validConfigID(tracking.ConnectionID) || !validTrackingDimension(tracking.Dimension) || !validTrackingValue(tracking.Value) || !validBounded(tracking.ProviderID, MaxInputLength, true) || !validBounded(tracking.ExternalID, MaxInputLength, true) || !validOptionalIdentity(tracking.CoverageID) || tracking.ObservedAt.IsZero() {
		return validationError("media tracking is invalid")
	}
	for _, evidence := range tracking.Evidence {
		if !validBounded(evidence, MaxInputLength, false) {
			return validationError("tracking evidence is invalid")
		}
	}
	return nil
}

func validOptionalTarget(value string) bool {
	if value == "" {
		return true
	}
	root, relative, ok := strings.Cut(value, ":")
	return ok && validConfigID(root) && validRelativePath(relative) && len(value) <= MaxInputLength && validBounded(value, MaxInputLength, false)
}

func validFileType(value string) bool {
	switch value {
	case "file", "directory", "subtitle", "companion":
		return true
	default:
		return false
	}
}

func validFileRole(value string) bool {
	switch value {
	case "", "video", "subtitle", "companion":
		return true
	default:
		return false
	}
}

func validReadiness(value string) bool {
	switch value {
	case "ready", "downloading", "processing", "changing", "unsupported", "unknown":
		return true
	default:
		return false
	}
}

func validCompleteness(value string) bool {
	switch value {
	case "complete", "partial", "unknown":
		return true
	default:
		return false
	}
}

func validTrackingDimension(value string) bool {
	switch value {
	case "registration", "import", "availability", "request":
		return true
	default:
		return false
	}
}

func validTrackingValue(value string) bool {
	switch value {
	case "present", "absent", "unknown":
		return true
	default:
		return false
	}
}

func validDownloadState(value string) bool {
	switch value {
	case "queued", "downloading", "seeding", "complete", "processing", "failed", "unknown":
		return true
	default:
		return false
	}
}

func validDescriptorType(value string) bool {
	switch value {
	case "torrent", "nzb", "unknown":
		return true
	default:
		return false
	}
}

func validDescriptorAvailability(value string) bool {
	switch value {
	case "available", "unavailable", "unknown":
		return true
	default:
		return false
	}
}

func writeListPageWithCount(w http.ResponseWriter, route string, query queryState, title, intro string, count int, renderRows func(*pageWriter), page PageInfo) {
	p := pageWriter{w: w, status: http.StatusOK, title: title}
	p.start()
	p.text("<main id=\"inventory-content\" aria-labelledby=\"inventory-title\"><h1 id=\"inventory-title\">")
	p.value(title)
	p.text("</h1><p>")
	p.value(intro)
	p.text("</p>")
	writeFilterForm(&p, route, query)
	p.text("<p role=\"status\">")
	p.value(fmt.Sprintf("Observed %d record(s); %s.", count, coveragePageLabel(page)))
	p.text("</p><div class=\"inventory-table\">")
	renderRows(&p)
	p.text("</div>")
	writeCoverageSet(&p, "Page coverage evidence", page.Coverage)
	writePagination(&p, route, query, page)
	p.text("</main></body></html>")
	p.finish()
}

func writeFilterForm(p *pageWriter, route string, query queryState) {
	p.text("<form method=\"get\" action=\"/" + route + "\" aria-label=\"Inventory filters\"><fieldset><legend>Read-only filters</legend>")
	p.text("<label for=\"inventory-limit\">Page size</label><input id=\"inventory-limit\" name=\"limit\" inputmode=\"numeric\" value=\"")
	p.value(strconv.Itoa(query.Limit))
	p.text("\">")
	if route == "discoveries" {
		writeInput(p, "rootId", "Root instance", query.RootID)
	}
	if route == "downloads" {
		writeInput(p, "connectionId", "Connection instance", query.ConnectionID)
	}
	if route == "media" {
		p.text("<label for=\"inventory-kind\">Media kind</label><select id=\"inventory-kind\" name=\"kind\"><option value=\"\"")
		if query.Kind == "" {
			p.text(" selected")
		}
		p.text(">All kinds</option>")
		for _, kind := range []string{"movie", "episode", "season", "anime"} {
			p.text("<option value=\"")
			p.value(kind)
			p.text("\"")
			if query.Kind == kind {
				p.text(" selected")
			}
			p.text(">")
			p.value(kind)
			p.text("</option>")
		}
		p.text("</select>")
	}
	p.text("<button type=\"submit\">Apply filters</button></fieldset></form>")
}

func writeInput(p *pageWriter, name, label, value string) {
	p.text("<label for=\"inventory-" + name + "\">")
	p.value(label)
	p.text("</label><input id=\"inventory-" + name + "\" name=\"")
	p.value(name)
	p.text("\" value=\"")
	p.value(value)
	p.text("\">")
}

func writePagination(p *pageWriter, route string, query queryState, page PageInfo) {
	if page.NextCursor == nil || *page.NextCursor == "" {
		return
	}
	p.text("<nav aria-label=\"Pagination\"><a rel=\"next\" href=\"")
	p.value(query.pageLink(route, false, *page.NextCursor))
	p.text("\">Next page</a></nav>")
}

func writeCoverageSet(p *pageWriter, title string, coverage []Coverage) {
	p.text("<section aria-labelledby=\"page-coverage-title\"><h2 id=\"page-coverage-title\">")
	p.value(title)
	p.text("</h2>")
	if len(coverage) == 0 {
		p.text("<p>Coverage: unknown.</p></section>")
		return
	}
	for index := range coverage {
		p.text("<h3>Coverage observation ")
		p.value(strconv.Itoa(index + 1))
		p.text("</h3><dl>")
		writeCoverageTerms(p, coverage[index])
		p.text("</dl>")
	}
	p.text("</section>")
}

func writeCoverageTerms(p *pageWriter, coverage Coverage) {
	detailTerm(p, "Completeness", coverage.Completeness)
	detailTerm(p, "Connection ID", coverage.ConnectionID)
	detailTerm(p, "Root ID", coverage.RootID)
	detailTerm(p, "Source ID", coverage.SourceID)
	detailTerm(p, "Observed at", timeLabel(coverage.ObservedAt))
	count := "unknown"
	if coverage.ObservedCount != nil {
		count = countLabel(*coverage.ObservedCount)
	}
	detailTerm(p, "Observed count", count)
	detailTerm(p, "Snapshot revision", coverage.SnapshotRevision)
	reasons := "unknown"
	if len(coverage.ReasonCodes) > 0 {
		reasons = strings.Join(coverage.ReasonCodes, ", ")
	}
	detailTerm(p, "Reason codes", reasons)
}
