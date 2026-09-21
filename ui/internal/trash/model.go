// Package trash renders the read-only trash view for the UI BFF.
//
// The package consumes the generated API client only through HTTPReader. The
// exported types are normalized observations and deliberately contain no
// command input or filesystem handles. Restore, purge, and permanent delete
// remain API-owned actions; the forms rendered here only preserve a GET draft.
package trash

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
	DefaultPageSize  = 25
	MaxPageSize      = 100
	MaxCursorLength  = 256
	MaxQueryLength   = 64 << 10
	MaxQueryValues   = 24
	MaxValueLength   = 512
	MaxItemsPerPage  = 100
	MaxFilesPerEntry = 1024
	MaxTextLength    = 4096
)

// ErrorKind identifies a sanitized trash read failure.
type ErrorKind string

const (
	ErrorNotFound    ErrorKind = "not_found"
	ErrorUnavailable ErrorKind = "unavailable"
	ErrorProtocol    ErrorKind = "protocol"
	ErrorCanceled    ErrorKind = "canceled"
	ErrorTimeout     ErrorKind = "timeout"
)

var (
	ErrNotFound    = errors.New("trash resource not found")
	ErrUnavailable = errors.New("trash API unavailable")
	ErrProtocol    = errors.New("trash API response invalid")
)

// APIError omits endpoint, body, credentials, and transport details.
type APIError struct {
	kind   ErrorKind
	status int
	cause  error
}

func (e *APIError) Error() string {
	if e == nil {
		return "trash read failed"
	}
	switch e.kind {
	case ErrorNotFound:
		return "trash record not found"
	case ErrorProtocol:
		return "trash response invalid"
	case ErrorCanceled:
		return "trash read canceled"
	case ErrorTimeout:
		return "trash read timed out"
	default:
		return "trash temporarily unavailable"
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

// Status returns the upstream status, or zero for non-HTTP failures.
func (e *APIError) Status() int {
	if e == nil {
		return 0
	}
	return e.status
}

// PageRequest is the bounded list query accepted by the reader.
type PageRequest struct {
	Cursor string
	Limit  int
}

// Coverage keeps API coverage evidence visible to the operator.
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

// PageInfo retains cursor and coverage evidence without exposing DTOs.
type PageInfo struct {
	NextCursor string
	ObservedAt time.Time
	Coverage   []Coverage
}

// FileTarget is an exact root-relative location. It never contains an
// effective host path.
type FileTarget struct {
	RootID       string
	RelativePath string
}

// Target is a concise alias used by callers that model all root-relative
// action targets uniformly.
type Target = FileTarget

// ManifestEntry is one selected file with its stable identity and role.
type ManifestEntry struct {
	RootID       string
	RelativePath string
	Type         string
	Role         string
	Size         int
	Identity     string
	Digest       string
	ObservedAt   *time.Time
}

// TrashFile is an alias for one selected manifest entry.
type TrashFile = ManifestEntry

// EffectEvidence describes an optional client or filesystem effect exposed by
// a newer API revision. Empty values remain unknown and are rendered as such.
type EffectEvidence struct {
	Identity   string
	State      string
	Observed   string
	ObservedAt *time.Time
}

// ItemOutcome retains partial per-item reconciliation evidence when supplied
// by the API. It is intentionally independent from the entry's aggregate
// state.
type ItemOutcome struct {
	Identity string
	State    string
	Reason   string
}

// TrashEntry is the normalized API-owned trash observation.
type TrashEntry struct {
	ID                 string
	State              string
	ExpiresAt          time.Time
	CreatedAt          *time.Time
	RetentionDays      *int
	RetentionDefault   *bool
	Files              []ManifestEntry
	OriginalPaths      []FileTarget
	Capabilities       []string
	ClientAssociations []string
	Holds              []string
	StopEvidence       []EffectEvidence
	RemoveEvidence     []EffectEvidence
	RestoreConflicts   []string
	Outcomes           []ItemOutcome
	UnresolvedEffects  []string
	LateEffects        []string
	ETag               string
}

// Trash is the resource name used by the UI route and API documentation.
type Trash = TrashEntry

// TrashPage is one bounded page of trash entries.
type TrashPage struct {
	Items []TrashEntry
	Page  PageInfo
}

// TrashList is an alias for one paginated trash response.
type TrashList = TrashPage

// Reader is the normalized HTTP-only dependency consumed by Handler.
type Reader interface {
	ListTrash(context.Context, PageRequest) (TrashPage, error)
	GetTrash(context.Context, string) (TrashEntry, error)
}

// Handler serves read-only trash list and detail views.
type Handler struct {
	reader      Reader
	defaultSize int
	maxSize     int
}

// Options bounds the handler's page size.
type Options struct {
	DefaultPageSize int
	MaxPageSize     int
}

// NewHandler creates a bounded, read-only trash handler.
func NewHandler(reader Reader) http.Handler {
	return NewHandlerWithOptions(reader, Options{})
}

// NewHandlerWithOptions creates a handler with explicit page bounds.
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
		h.renderError(w, http.StatusBadRequest, "Bad request.", "The trash request is invalid.")
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", http.MethodGet+", "+http.MethodHead)
		h.renderError(w, http.StatusMethodNotAllowed, "Method not allowed.", "Trash views are read-only; restore and purge are API-owned actions.")
		return
	}
	route, id, detail, ok := parseRoute(r.URL.Path)
	if !ok || route != "trash" {
		h.renderError(w, http.StatusNotFound, "Page not found.", "The requested trash page was not found.")
		return
	}
	query, err := parseQuery(r.URL.RawQuery, detail, h.defaultSize, h.maxSize)
	if err != nil {
		h.renderError(w, http.StatusBadRequest, "Invalid trash query.", "The supplied trash draft is invalid or too large.")
		return
	}
	if h.reader == nil {
		h.renderError(w, http.StatusServiceUnavailable, "Trash unavailable.", "Retry to load API-owned trash state.")
		return
	}
	if detail {
		item, readErr := h.reader.GetTrash(r.Context(), id)
		if readErr != nil {
			h.handleReadError(w, readErr)
			return
		}
		if item.ID != id || !validTrashEntry(item) {
			h.handleReadError(w, &APIError{kind: ErrorProtocol})
			return
		}
		h.renderDetail(w, query, item)
		return
	}
	page, readErr := h.reader.ListTrash(r.Context(), query.PageRequest)
	if readErr != nil {
		h.handleReadError(w, readErr)
		return
	}
	if !validTrashPage(page) {
		h.handleReadError(w, &APIError{kind: ErrorProtocol})
		return
	}
	h.renderList(w, query, page)
}

func (h *Handler) handleReadError(w http.ResponseWriter, err error) {
	status := http.StatusServiceUnavailable
	message := "Trash data is temporarily unavailable. Retry to load API-owned state."
	if errors.Is(err, ErrNotFound) {
		status = http.StatusNotFound
		message = "The requested trash record was not found."
	} else if errors.Is(err, ErrProtocol) {
		message = "The API returned incomplete or invalid trash evidence."
	}
	h.renderError(w, status, "Trash unavailable.", message)
}

func (h *Handler) renderError(w http.ResponseWriter, status int, title, message string) {
	p := pageWriter{w: w, status: status, title: title}
	p.start()
	p.text("<main id=\"trash-content\" aria-labelledby=\"trash-title\"><h1 id=\"trash-title\">")
	p.value(title)
	p.text("</h1><p role=\"alert\">")
	p.value(message)
	p.text("</p></main></body></html>")
	p.finish()
}

func (h *Handler) renderList(w http.ResponseWriter, query queryState, page TrashPage) {
	p := pageWriter{w: w, status: http.StatusOK, title: "Trash"}
	p.start()
	p.text("<main id=\"trash-content\" aria-labelledby=\"trash-title\"><h1 id=\"trash-title\">Trash</h1><p>Entries remain held until the API-owned retention and effect checks permit purge. A browser request never restores or purges an entry.</p>")
	p.text("<table><caption>Trash entries</caption><thead><tr><th scope=\"col\">Entry</th><th scope=\"col\">State</th><th scope=\"col\">Expires</th><th scope=\"col\">Files</th><th scope=\"col\">Client associations</th><th scope=\"col\">Coverage</th></tr></thead><tbody>")
	if len(page.Items) == 0 {
		p.text("<tr><td colspan=\"6\">No entries on this page; incomplete coverage does not prove absence.</td></tr>")
	}
	for _, item := range page.Items {
		p.text("<tr><th scope=\"row\"><a href=\"")
		p.value("/trash/" + url.PathEscape(item.ID) + query.encoded(true))
		p.text("\">")
		p.value(known(item.ID))
		p.text("</a></th><td>")
		p.value(stateLabel(item.State))
		p.text("</td><td>")
		p.value(timeLabel(item.ExpiresAt))
		p.text("</td><td>")
		p.value(strconv.Itoa(len(item.Files)))
		p.text("</td><td>")
		p.value(countOrUnknown(len(item.ClientAssociations)))
		p.text("</td><td>")
		p.value(coverageLabel(page.Page))
		p.text("</td></tr>")
	}
	p.text("</tbody></table>")
	if page.Page.NextCursor != "" {
		p.text("<p><a rel=\"next\" href=\"/trash?")
		p.value(query.next(page.Page.NextCursor))
		p.text("\">Next page</a></p>")
	}
	p.text("</main></body></html>")
	p.finish()
}

func (h *Handler) renderDetail(w http.ResponseWriter, query queryState, item TrashEntry) {
	p := pageWriter{w: w, status: http.StatusOK, title: "Trash detail"}
	p.start()
	p.text("<main id=\"trash-content\" aria-labelledby=\"trash-title\"><p><a href=\"/trash")
	p.value(query.listEncoded())
	p.text("\">Back to Trash</a></p><h1 id=\"trash-title\">Trash entry ")
	p.value(known(item.ID))
	p.text("</h1><p>Restore and purge are two-step API-owned actions. These GET forms preserve operator context only; they never dispatch a mutation. Permanent delete has no UI shortcut outside this trash flow. Restore policy keeps any associated client stopped; restore performs no automatic re-add or automatic resume. Stop, remove, and client association observations remain unknown when the API supplies no evidence.</p><dl>")
	detailTerm(&p, "State", stateLabel(item.State))
	detailTerm(&p, "Expires at", timeLabel(item.ExpiresAt))
	detailTerm(&p, "Retention", retentionLabel(item))
	detailTerm(&p, "ETag", known(item.ETag))
	detailTerm(&p, "Capabilities", strings.Join(item.Capabilities, ", "))
	detailTerm(&p, "Coverage", "unknown; detail response has no page envelope")
	p.text("</dl>")
	p.text("<section aria-labelledby=\"trash-draft\"><h2 id=\"trash-draft\">Action draft</h2><p id=\"trash-draft-help\">Keep the idempotency and operator context here, then submit the corresponding API action after reviewing the exact plan and current ETag.</p><form method=\"get\" action=\"/trash/")
	p.value(url.PathEscape(item.ID))
	action := query.value("action", "restore")
	if !validTrashAction(action) {
		action = "restore"
	}
	p.text("\" aria-describedby=\"trash-draft-help\"><fieldset><legend>Step 1: choose the API-owned action</legend><label>Action<select name=\"action\">")
	p.text("<option value=\"restore\"")
	if action == "restore" {
		p.text(" selected")
	}
	p.text(">restore</option><option value=\"purge\"")
	if action == "purge" {
		p.text(" selected")
	}
	p.text(">purge</option></select></label>")
	writeInput(&p, "idempotencyKey", "Idempotency key", query.value("idempotencyKey", ""))
	writeInput(&p, "operator", "Operator context", query.value("operator", ""))
	writeInput(&p, "reason", "Reason", query.value("reason", ""))
	writeInput(&p, "ifMatch", "If-Match ETag", query.value("ifMatch", item.ETag))
	p.text("<button type=\"submit\">Keep action draft</button></fieldset></form><form method=\"get\" action=\"/trash/")
	p.value(url.PathEscape(item.ID))
	p.text("\" aria-label=\"Confirm trash action draft\"><fieldset><legend>Step 2: confirmation context</legend><input type=\"hidden\" name=\"action\" value=\"")
	p.value(action)
	p.text("\"><input type=\"hidden\" name=\"idempotencyKey\" value=\"")
	p.value(query.value("idempotencyKey", ""))
	p.text("\"><input type=\"hidden\" name=\"operator\" value=\"")
	p.value(query.value("operator", ""))
	p.text("\"><input type=\"hidden\" name=\"reason\" value=\"")
	p.value(query.value("reason", ""))
	p.text("\"><input type=\"hidden\" name=\"ifMatch\" value=\"")
	p.value(query.value("ifMatch", item.ETag))
	p.text("\"><label>Confirm exact manifest and current state<input name=\"confirm\" value=\"")
	p.value(query.value("confirm", action))
	p.text("\"></label><button type=\"submit\">Keep confirmation draft</button></fieldset></form></section>")
	writeTargets(&p, "Original locations", item.OriginalPaths)
	writeManifest(&p, item.Files)
	writeStrings(&p, "Client associations", item.ClientAssociations)
	writeStrings(&p, "Operational holds", item.Holds)
	writeEffects(&p, "Stop evidence", item.StopEvidence)
	writeEffects(&p, "Remove evidence", item.RemoveEvidence)
	writeStrings(&p, "Restore conflicts", item.RestoreConflicts)
	writeOutcomes(&p, item.Outcomes)
	writeStrings(&p, "Unresolved effects", item.UnresolvedEffects)
	writeStrings(&p, "Late effects", item.LateEffects)
	p.text("</main></body></html>")
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
	Raw    string
}

func parseRoute(path string) (route, id string, detail, ok bool) {
	if path == "" || !utf8.ValidString(path) || !strings.HasPrefix(path, "/") {
		return "", "", false, false
	}
	parts := strings.Split(strings.TrimPrefix(path, "/"), "/")
	if len(parts) == 1 && parts[0] == "trash" {
		return "trash", "", false, true
	}
	if len(parts) == 2 && parts[0] == "trash" && validIdentity(parts[1]) {
		return "trash", parts[1], true, true
	}
	return "", "", false, false
}

func parseQuery(raw string, detail bool, defaultSize, maxSize int) (queryState, error) {
	if len(raw) > MaxQueryLength {
		return queryState{}, errors.New("query too large")
	}
	values, err := url.ParseQuery(raw)
	if err != nil || len(values) > MaxQueryValues {
		return queryState{}, errors.New("query malformed")
	}
	state := queryState{PageRequest: PageRequest{Limit: defaultSize}, Values: make(map[string]string), Raw: raw}
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
			if !detail || !validDraftKey(key) {
				return queryState{}, errors.New("unknown query")
			}
		}
		state.Values[key] = value
	}
	if action, present := state.Values["action"]; present && !validTrashAction(action) {
		return queryState{}, errors.New("trash action invalid")
	}
	if confirm, present := state.Values["confirm"]; present {
		action, actionPresent := state.Values["action"]
		if !actionPresent || !validTrashAction(action) || confirm != action {
			return queryState{}, errors.New("trash confirmation is not bound to action")
		}
	}
	return state, nil
}

func validTrashAction(value string) bool {
	return value == "restore" || value == "purge"
}

func validDraftKey(value string) bool {
	switch value {
	case "action", "confirm", "idempotencyKey", "operator", "reason", "ifMatch":
		return true
	default:
		return false
	}
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

func (q queryState) encoded(includeDraft bool) string {
	values := url.Values{}
	for key, value := range q.Values {
		if !includeDraft && validDraftKey(key) {
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

func validTrashPage(page TrashPage) bool {
	if page.Items == nil || len(page.Items) > MaxItemsPerPage || page.Page.ObservedAt.IsZero() {
		return false
	}
	for _, coverage := range page.Page.Coverage {
		if !knownCompleteness(coverage.Completeness) || coverage.ObservedAt.IsZero() {
			return false
		}
	}
	seen := make(map[string]struct{}, len(page.Items))
	for _, item := range page.Items {
		if _, ok := seen[item.ID]; ok || !validTrashEntry(item) {
			return false
		}
		seen[item.ID] = struct{}{}
	}
	return true
}

func knownCompleteness(value string) bool {
	return value == "complete" || value == "partial" || value == "unknown"
}

func validTrashEntry(item TrashEntry) bool {
	if !validIdentity(item.ID) || !knownTrashState(item.State) || item.ExpiresAt.IsZero() || item.Files == nil || item.OriginalPaths == nil || len(item.Files) > MaxFilesPerEntry {
		return false
	}
	if !validStringList(item.Capabilities, 128) || !validStringList(item.ClientAssociations, 256) || !validStringList(item.Holds, 256) || !validStringList(item.RestoreConflicts, 256) || !validStringList(item.UnresolvedEffects, 256) || !validStringList(item.LateEffects, 256) {
		return false
	}
	seenTargets := make(map[string]struct{}, len(item.OriginalPaths))
	for _, target := range item.OriginalPaths {
		if !validConfigID(target.RootID) || !validRelativePath(target.RelativePath) {
			return false
		}
		key := target.RootID + "\x00" + target.RelativePath
		if _, exists := seenTargets[key]; exists {
			return false
		}
		seenTargets[key] = struct{}{}
	}
	seenFiles := make(map[string]struct{}, len(item.Files))
	seenIdentities := make(map[string]struct{}, len(item.Files))
	for _, file := range item.Files {
		if !validConfigID(file.RootID) || !validRelativePath(file.RelativePath) || !knownFileType(file.Type) || !knownFileRole(file.Role) || file.Size < 0 || !validBounded(file.Identity, 128, true) || !validBounded(file.Digest, 256, true) {
			return false
		}
		fileKey := file.RootID + "\x00" + file.RelativePath
		if _, exists := seenFiles[fileKey]; exists {
			return false
		}
		seenFiles[fileKey] = struct{}{}
		if file.Identity != "" {
			if _, exists := seenIdentities[file.Identity]; exists {
				return false
			}
			seenIdentities[file.Identity] = struct{}{}
		}
	}
	return true
}

func validStringList(values []string, max int) bool {
	for _, value := range values {
		if !validBounded(value, max, false) {
			return false
		}
	}
	return true
}

func knownTrashState(value string) bool {
	switch value {
	case "held", "ready", "restoring", "purging", "purged", "blocked":
		return true
	default:
		return false
	}
}

func knownFileType(value string) bool {
	switch value {
	case "file", "directory", "subtitle", "companion":
		return true
	default:
		return false
	}
}

func knownFileRole(value string) bool {
	return value == "" || value == "video" || value == "subtitle" || value == "companion"
}

func known(value string) string {
	if value == "" {
		return "unknown"
	}
	return value
}

func stateLabel(value string) string {
	if value == "" {
		return "unknown"
	}
	return value
}

func countOrUnknown(value int) string {
	if value == 0 {
		return "unknown"
	}
	return strconv.Itoa(value)
}

func coverageLabel(page PageInfo) string {
	if len(page.Coverage) == 0 {
		return "unknown"
	}
	values := make([]string, 0, len(page.Coverage))
	for _, coverage := range page.Coverage {
		values = append(values, coverage.Completeness)
	}
	return strings.Join(values, ", ")
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

func retentionLabel(item TrashEntry) string {
	if item.RetentionDays != nil && *item.RetentionDays >= 0 {
		kind := "custom"
		if item.RetentionDefault != nil && *item.RetentionDefault {
			kind = "default"
		}
		return kind + " (" + strconv.Itoa(*item.RetentionDays) + " days)"
	}
	return "unknown; expiry is API-owned"
}

func detailTerm(p *pageWriter, term, value string) {
	p.text("<dt>")
	p.value(term)
	p.text("</dt><dd>")
	p.value(value)
	p.text("</dd>")
}

func writeHiddenQuery(p *pageWriter, query queryState, keys ...string) {
	for _, key := range keys {
		if value := query.value(key, ""); value != "" {
			p.text("<input type=\"hidden\" name=\"")
			p.value(key)
			p.text("\" value=\"")
			p.value(value)
			p.text("\">")
		}
	}
}

func writeInput(p *pageWriter, name, label, value string) {
	p.text("<label>")
	p.value(label)
	p.text("<input name=\"")
	p.value(name)
	p.text("\" value=\"")
	p.value(value)
	p.text("\"></label>")
}

func writeTargets(p *pageWriter, heading string, targets []FileTarget) {
	p.text("<section><h2>")
	p.value(heading)
	p.text("</h2>")
	if len(targets) == 0 {
		p.text("<p>unknown.</p></section>")
		return
	}
	p.text("<ul>")
	for _, target := range targets {
		p.text("<li>")
		p.value(target.RootID + ":" + target.RelativePath)
		p.text("</li>")
	}
	p.text("</ul></section>")
}

func writeManifest(p *pageWriter, files []ManifestEntry) {
	p.text("<section><h2>Selected manifest</h2>")
	if len(files) == 0 {
		p.text("<p>unknown.</p></section>")
		return
	}
	p.text("<table><caption>Selected file identities</caption><thead><tr><th scope=\"col\">Identity</th><th scope=\"col\">Type</th><th scope=\"col\">Role</th><th scope=\"col\">Size</th><th scope=\"col\">Digest</th><th scope=\"col\">Observed at</th><th scope=\"col\">Target</th></tr></thead><tbody>")
	for _, file := range files {
		p.text("<tr><th scope=\"row\">")
		p.value(known(file.Identity))
		p.text("</th><td>")
		p.value(known(file.Type))
		p.text("</td><td>")
		p.value(known(file.Role))
		p.text("</td><td>")
		p.value(strconv.Itoa(file.Size))
		p.text("</td><td>")
		p.value(known(file.Digest))
		p.text("</td><td>")
		p.value(optionalTimeLabel(file.ObservedAt))
		p.text("</td><td>")
		p.value(file.RootID + ":" + file.RelativePath)
		p.text("</td></tr>")
	}
	p.text("</tbody></table></section>")
}

func writeStrings(p *pageWriter, heading string, values []string) {
	p.text("<section><h2>")
	p.value(heading)
	p.text("</h2>")
	if len(values) == 0 {
		p.text("<p>unknown.</p></section>")
		return
	}
	p.text("<ul>")
	for _, value := range values {
		p.text("<li>")
		p.value(known(value))
		p.text("</li>")
	}
	p.text("</ul></section>")
}

func writeEffects(p *pageWriter, heading string, values []EffectEvidence) {
	p.text("<section><h2>")
	p.value(heading)
	p.text("</h2>")
	if len(values) == 0 {
		p.text("<p>unknown; no per-effect evidence was returned.</p></section>")
		return
	}
	p.text("<ul>")
	for _, value := range values {
		p.text("<li>")
		p.value(known(value.Identity) + ": " + known(value.State) + " (" + known(value.Observed) + ")")
		p.text("</li>")
	}
	p.text("</ul></section>")
}

func writeOutcomes(p *pageWriter, values []ItemOutcome) {
	p.text("<section><h2>Per-item outcomes</h2>")
	if len(values) == 0 {
		p.text("<p>unknown; this response did not include per-item outcomes.</p></section>")
		return
	}
	p.text("<ul>")
	for _, value := range values {
		p.text("<li>")
		p.value(known(value.Identity) + ": " + known(value.State) + " (" + known(value.Reason) + ")")
		p.text("</li>")
	}
	p.text("</ul></section>")
}
