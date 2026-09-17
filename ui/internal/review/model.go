// Package review renders the API-owned review surface for action plans.
//
// The package deliberately stops at the generated-client boundary: exported
// types below are normalized presentation data and never expose generated
// DTOs, request bodies, or upstream details. The UI can preserve a draft in a
// GET query string, but it cannot approve, reject, retry, or cancel anything.
package review

import (
	"context"
	"errors"
	"html/template"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	DefaultPageSize = 25
	MaxPageSize     = 100
	MaxQueryLength  = 64 << 10
	MaxQueryValues  = 96
	MaxValueLength  = 512
	MaxScopeItems   = 1024
	MaxTextLength   = 4096
)

// ErrorKind is a sanitized API failure category.
type ErrorKind string

const (
	ErrorNotFound    ErrorKind = "not_found"
	ErrorUnavailable ErrorKind = "unavailable"
	ErrorProtocol    ErrorKind = "protocol"
	ErrorCanceled    ErrorKind = "canceled"
	ErrorTimeout     ErrorKind = "timeout"
)

var (
	ErrNotFound    = errors.New("review resource not found")
	ErrUnavailable = errors.New("review API unavailable")
	ErrProtocol    = errors.New("review API response invalid")
)

// APIError intentionally omits URLs, response bodies, credentials, and
// transport detail. Cancellation identity remains available through Unwrap.
type APIError struct {
	kind   ErrorKind
	status int
	cause  error
}

func (e *APIError) Error() string {
	if e == nil {
		return "review read failed"
	}
	switch e.kind {
	case ErrorNotFound:
		return "review record not found"
	case ErrorProtocol:
		return "review response invalid"
	case ErrorCanceled:
		return "review read canceled"
	case ErrorTimeout:
		return "review read timed out"
	default:
		return "review temporarily unavailable"
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

// Kind returns the sanitized category.
func (e *APIError) Kind() ErrorKind {
	if e == nil {
		return ""
	}
	return e.kind
}

// Status returns the observed API status, or zero when none was available.
func (e *APIError) Status() int {
	if e == nil {
		return 0
	}
	return e.status
}

// PageRequest is the bounded list query accepted by the review reader.
type PageRequest struct {
	Cursor string
	Limit  int
}

// PageInfo retains API coverage and cursor evidence without rendering raw
// protocol envelopes.
type PageInfo struct {
	NextCursor string
	ObservedAt time.Time
}

// ReviewPage is one API-owned page of immutable plans.
type ReviewPage struct {
	Items []Review
	Page  PageInfo
}

// Review is a normalized immutable action-plan projection. Approval phase is
// derived from Action.Kind and is rendered explicitly by the handler.
type Review struct {
	ID       string
	Revision int
	Digest   string
	// These optional authority fences are retained when a newer API projects
	// them. Missing values remain visibly unknown.
	SourceRevision   string
	ConfigRevision   string
	ManifestDigest   string
	DesiredDigest    string
	Status           string
	RequiredApproval string
	CreatedAt        *time.Time
	ExpiresAt        *time.Time
	Action           Action
	Manifest         []ManifestEntry
	Preconditions    []string
	ConnectionFences map[string]string
	MappingFences    map[string]string
	Capabilities     []string
	Impacts          []string
	Conflicts        []string
	BlockingIssues   []string
	EstimatedBytes   *int
	Decision         *Decision
	DesiredStateKeys []string
}

// Plan is an alias useful to callers that refer to the API resource by its
// plan name.
type Plan = Review

// PlanPage is an alias for the review list projection.
type PlanPage = ReviewPage

// Decision is an API-owned review decision projection. It is optional because
// an action-plan read does not necessarily include its decision resource.
type Decision struct {
	ID                 string
	PlanID             string
	Revision           int
	Digest             string
	Decision           string
	Actor              string
	UnverifiedLabel    string
	CreatedAt          time.Time
	ResultingActionRun string
}

// ManifestEntry is an exact selected object from a plan.
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

// Action is the normalized subset of an immutable action input needed to
// explain review scope and effects. Fields remain observations; they do not
// authorize a mutation.
type Action struct {
	Kind                 string
	ConnectionID         string
	MediaKind            string
	ProviderID           string
	RegisteredExternalID string
	PreviewRevision      string
	Transfer             string
	Executor             string
	TrashID              string
	RetentionDays        *int
	Monitoring           *bool
	SeasonFolder         *bool
	SeriesType           string
	QualityProfileID     *int
	RootFolder           string
	Seasons              []int
	Episodes             []int
	ClientItemIDs        []string
	StoppedClientIDs     []string
	RetainPayload        *bool
	Permanent            *bool
	Irreversible         bool
	Files                []ActionFile
}

// ActionFile retains source/destination and Arr association identity.
type ActionFile struct {
	SourceRootID      string
	SourcePath        string
	DestinationRootID string
	DestinationPath   string
	Identity          string
	Role              string
	Size              int
	MovieOrEpisodeID  int
	Subtitle          *bool
	Language          string
	Forced            *bool
	HearingImpaired   *bool
}

// Reader is the normalized HTTP-only source consumed by Handler.
type Reader interface {
	ListReviews(context.Context, PageRequest) (ReviewPage, error)
	GetReview(context.Context, string) (Review, error)
}

// Handler serves review lists and detail pages. All controls are draft forms
// using GET, so no browser request can bypass the API review decision route.
type Handler struct {
	reader      Reader
	defaultSize int
	maxSize     int
}

// Options bounds list reads. Zero values select conservative defaults.
type Options struct {
	DefaultPageSize int
	MaxPageSize     int
}

// NewHandler constructs a review handler with fixed bounds.
func NewHandler(reader Reader) http.Handler {
	return NewHandlerWithOptions(reader, Options{})
}

// NewHandlerWithOptions constructs a review handler with explicit page
// bounds. Bounds cannot be increased above the package contract.
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

type draft struct {
	Values map[string]string
	Raw    string
	PageRequest
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	setPrivateHeaders(w)
	if r == nil || r.URL == nil {
		renderError(w, http.StatusBadRequest, "Bad request.", "The review request is invalid.")
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", http.MethodGet+", "+http.MethodHead)
		renderError(w, http.StatusMethodNotAllowed, "Method not allowed.", "Review controls are API-owned and this UI view is read-only.")
		return
	}
	detail, id, ok := parseRoute(r.URL.Path)
	if !ok {
		renderError(w, http.StatusNotFound, "Page not found.", "The requested review page was not found.")
		return
	}
	state, err := parseDraft(r.URL.RawQuery, h.defaultSize, h.maxSize)
	if err != nil {
		renderError(w, http.StatusBadRequest, "Invalid review draft.", "The supplied review fields are invalid or too large.")
		return
	}
	if h.reader == nil {
		renderError(w, http.StatusServiceUnavailable, "Reviews unavailable.", "Retry to load API-owned review state. Draft values remain in the address.")
		return
	}
	if detail {
		item, readErr := h.reader.GetReview(r.Context(), id)
		if readErr != nil {
			h.handleReadError(w, readErr)
			return
		}
		if item.ID != id {
			h.handleReadError(w, validationError("review identity does not match request"))
			return
		}
		if err := validateReview(item); err != nil {
			h.handleReadError(w, err)
			return
		}
		renderReviewDetail(w, id, state, item)
		return
	}
	page, readErr := h.reader.ListReviews(r.Context(), state.PageRequest)
	if readErr != nil {
		h.handleReadError(w, readErr)
		return
	}
	if err := validateReviewPage(page); err != nil {
		h.handleReadError(w, err)
		return
	}
	renderReviewList(w, state, page)
}

func parseRoute(path string) (detail bool, id string, ok bool) {
	if path == "/reviews" {
		return false, "", true
	}
	if !strings.HasPrefix(path, "/reviews/") || strings.Count(strings.TrimPrefix(path, "/reviews/"), "/") != 0 {
		return false, "", false
	}
	id = strings.TrimPrefix(path, "/reviews/")
	if !validIdentity(id) {
		return false, "", false
	}
	return true, id, true
}

func validIdentity(value string) bool {
	if value == "" || len(value) > 128 || !utf8.ValidString(value) {
		return false
	}
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || strings.ContainsRune("-_", r) {
			continue
		}
		return false
	}
	return true
}

var draftKeys = map[string]bool{
	"cursor": true, "limit": true, "decision": true, "idempotencyKey": true,
	"actor": true, "label": true, "reason": true, "monitoring": true,
	"fallback": true, "copyFallback": true, "planId": true, "planRevision": true,
	"digest": true, "sourceRevision": true, "configRevision": true,
	"mappingRevision": true, "manifestDigest": true, "desiredDigest": true,
	"selectedFile": true, "selectedEpisode": true, "selectedSubtitle": true,
}

func parseDraft(raw string, defaultSize, maxSize int) (draft, error) {
	if len(raw) > MaxQueryLength || !utf8.ValidString(raw) {
		return draft{}, errors.New("draft too large")
	}
	values, err := url.ParseQuery(raw)
	if err != nil || len(values) > MaxQueryValues {
		return draft{}, errors.New("draft malformed")
	}
	state := draft{Values: make(map[string]string), Raw: raw, PageRequest: PageRequest{Limit: defaultSize}}
	for key, entries := range values {
		if !draftKeys[key] || len(entries) != 1 {
			return draft{}, errors.New("unsupported or duplicate draft field")
		}
		value := entries[0]
		maxValueLength := MaxValueLength
		if key == "reason" {
			maxValueLength = MaxTextLength
		}
		if !utf8.ValidString(value) || len(value) > maxValueLength {
			return draft{}, errors.New("draft value too large")
		}
		switch key {
		case "cursor":
			if len(value) > 256 || strings.TrimSpace(value) != value {
				return draft{}, errors.New("cursor invalid")
			}
			state.Cursor = value
		case "limit":
			parsed, parseErr := strconv.Atoi(value)
			if parseErr != nil || parsed < 1 || parsed > maxSize {
				return draft{}, errors.New("limit invalid")
			}
			state.Limit = parsed
		case "decision":
			if value != "" && value != "approve" && value != "reject" {
				return draft{}, errors.New("decision invalid")
			}
		case "monitoring":
			if value != "" && value != "true" && value != "false" {
				return draft{}, errors.New("monitoring invalid")
			}
		case "fallback":
			if value != "" && value != "copy" && value != "hardlink" {
				return draft{}, errors.New("fallback invalid")
			}
		case "planRevision":
			if value != "" {
				if parsed, parseErr := strconv.Atoi(value); parseErr != nil || parsed < 1 {
					return draft{}, errors.New("revision invalid")
				}
			}
		}
		state.Values[key] = value
	}
	return state, nil
}

func validateReviewPage(page ReviewPage) error {
	if len(page.Items) > MaxScopeItems || page.Page.ObservedAt.IsZero() {
		return validationError("review page is incomplete")
	}
	for _, item := range page.Items {
		if err := validateReview(item); err != nil {
			return err
		}
	}
	return nil
}

func validateReview(item Review) error {
	if !validIdentity(item.ID) || item.Revision < 1 || !validEvidenceText(item.Digest) {
		return validationError("review identity or immutable binding is incomplete")
	}
	if item.Status != "preparing" && item.Status != "ready" && item.Status != "invalid" && item.Status != "expired" {
		return validationError("review status is unknown")
	}
	if item.RequiredApproval != "review" && item.RequiredApproval != "irreversible" {
		return validationError("review approval requirement is unknown")
	}
	for _, value := range []string{item.SourceRevision, item.ConfigRevision, item.ManifestDigest, item.DesiredDigest} {
		if !validOptionalEvidenceText(value) {
			return validationError("review immutable fence is invalid")
		}
	}
	if item.CreatedAt != nil && item.CreatedAt.IsZero() || item.ExpiresAt != nil && item.ExpiresAt.IsZero() {
		return validationError("review lifecycle timestamp is invalid")
	}
	if item.EstimatedBytes != nil && *item.EstimatedBytes < 0 {
		return validationError("review estimate is invalid")
	}
	for _, values := range [][]string{item.Preconditions, item.Capabilities, item.Impacts, item.Conflicts, item.BlockingIssues, item.DesiredStateKeys} {
		if err := validateTextSlice(values); err != nil {
			return err
		}
	}
	if err := validateFenceMap(item.ConnectionFences); err != nil {
		return err
	}
	if err := validateFenceMap(item.MappingFences); err != nil {
		return err
	}
	if err := validateAction(item.Action); err != nil {
		return err
	}
	if len(item.Manifest) > MaxScopeItems || len(item.Files()) > MaxScopeItems {
		return validationError("review scope is too large")
	}
	for _, entry := range item.Manifest {
		if !validEvidenceText(entry.RootID) || entry.RelativePath == "" || strings.HasPrefix(entry.RelativePath, "/") || strings.Contains(entry.RelativePath, "..") || !utf8.ValidString(entry.RelativePath) || len(entry.RelativePath) > MaxValueLength || entry.Size < 0 || entry.Size > int(^uint(0)>>1) || !validOptionalEvidenceText(entry.Identity) || !validOptionalEvidenceText(entry.Digest) {
			return validationError("review manifest is invalid")
		}
		if entry.Type != "file" && entry.Type != "directory" && entry.Type != "subtitle" && entry.Type != "companion" {
			return validationError("review manifest type is unknown")
		}
		if entry.Role != "" && !validFileRole(entry.Role) {
			return validationError("review manifest role is unknown")
		}
		if entry.ObservedAt != nil && entry.ObservedAt.IsZero() {
			return validationError("review manifest observation is invalid")
		}
	}
	return nil
}

func (r Review) Files() []ActionFile { return r.Action.Files }

func validateAction(action Action) error {
	allowed := map[string]bool{
		"arr.registration": true, "arr.import": true, "fs.copy": true, "fs.hardlink": true,
		"fs.move": true, "fs.rename": true, "fs.trash": true, "fs.restore": true,
		"fs.delete": true, "client.stop": true, "client.remove": true,
		"descriptor.delete": true, "jellyfin.refresh": true,
	}
	if !allowed[action.Kind] || len(action.Kind) > 64 {
		return validationError("review action kind is unknown")
	}
	for _, value := range []string{action.ConnectionID, action.MediaKind, action.ProviderID, action.RegisteredExternalID, action.PreviewRevision, action.Executor, action.TrashID, action.RootFolder} {
		if !validOptionalEvidenceText(value) {
			return validationError("review action authority is invalid")
		}
	}
	if action.Transfer != "" && !validTransfer(action.Transfer) {
		return validationError("review action transfer is unknown")
	}
	if len(action.Seasons) > MaxScopeItems || len(action.Episodes) > MaxScopeItems {
		return validationError("review action episode scope is too large")
	}
	for _, value := range append(append([]int{}, action.Seasons...), action.Episodes...) {
		if value < 0 {
			return validationError("review action episode scope is invalid")
		}
	}
	if action.Kind == "arr.registration" || action.Kind == "arr.import" || action.Kind == "client.stop" || action.Kind == "client.remove" || action.Kind == "jellyfin.refresh" {
		if action.ConnectionID == "" {
			return validationError("review action connection is incomplete")
		}
	}
	if action.Kind == "arr.registration" && (action.MediaKind == "" || action.ProviderID == "") {
		return validationError("review registration identity is incomplete")
	}
	if action.MediaKind != "" && !validMediaKind(action.MediaKind) {
		return validationError("review media kind is unknown")
	}
	if action.Kind == "arr.import" && (action.RegisteredExternalID == "" || action.PreviewRevision == "" || !validTransfer(action.Transfer)) {
		return validationError("review import binding is incomplete")
	}
	if action.Kind == "fs.restore" && action.TrashID == "" {
		return validationError("review restore identity is incomplete")
	}
	if action.RetentionDays != nil && *action.RetentionDays < 0 {
		return validationError("review retention is invalid")
	}
	if action.QualityProfileID != nil && *action.QualityProfileID < 0 {
		return validationError("review quality profile is invalid")
	}
	if len(action.Files) > MaxScopeItems || len(action.ClientItemIDs) > MaxScopeItems || len(action.StoppedClientIDs) > MaxScopeItems {
		return validationError("review action scope is too large")
	}
	if action.Kind == "arr.import" && len(action.Files) == 0 {
		return validationError("review import scope is empty")
	}
	if action.Kind == "fs.copy" || action.Kind == "fs.hardlink" || action.Kind == "fs.move" || action.Kind == "fs.rename" || action.Kind == "fs.trash" || action.Kind == "fs.restore" || action.Kind == "fs.delete" || action.Kind == "descriptor.delete" {
		if len(action.Files) == 0 {
			return validationError("review filesystem scope is empty")
		}
	}
	if (action.Kind == "client.stop" || action.Kind == "client.remove") && len(action.ClientItemIDs) == 0 {
		return validationError("review client scope is empty")
	}
	if err := validateTextSlice(action.ClientItemIDs); err != nil {
		return err
	}
	if err := validateTextSlice(action.StoppedClientIDs); err != nil {
		return err
	}
	if action.Kind == "fs.move" || action.Kind == "fs.rename" {
		if action.Executor != "mastarr" && action.Executor != "native_client" {
			return validationError("review filesystem executor is unknown")
		}
	}
	if err := validateActionScope(action); err != nil {
		return err
	}
	for _, file := range action.Files {
		if file.SourcePath == "" && file.DestinationPath == "" && file.Identity == "" && file.MovieOrEpisodeID == 0 {
			return validationError("review action file identity is incomplete")
		}
		if file.SourcePath != "" && (strings.HasPrefix(file.SourcePath, "/") || strings.Contains(file.SourcePath, "..") || !utf8.ValidString(file.SourcePath) || len(file.SourcePath) > MaxValueLength) {
			return validationError("review action source path is invalid")
		}
		if file.DestinationPath != "" && (strings.HasPrefix(file.DestinationPath, "/") || strings.Contains(file.DestinationPath, "..") || !utf8.ValidString(file.DestinationPath) || len(file.DestinationPath) > MaxValueLength) {
			return validationError("review action destination path is invalid")
		}
		if !validOptionalEvidenceText(file.SourceRootID) || !validOptionalEvidenceText(file.DestinationRootID) || !validOptionalEvidenceText(file.Identity) || file.Size < 0 || file.MovieOrEpisodeID < 0 || !validOptionalEvidenceText(file.Language) {
			return validationError("review action file evidence is invalid")
		}
		if file.Role != "" && !validFileRole(file.Role) {
			return validationError("review action file role is unknown")
		}
	}
	return nil
}

func validateActionScope(action Action) error {
	switch action.Kind {
	case "arr.import":
		for _, file := range action.Files {
			if !validEvidenceText(file.SourceRootID) || file.SourcePath == "" || file.MovieOrEpisodeID <= 0 {
				return validationError("review import file identity is incomplete")
			}
		}
	case "fs.copy", "fs.hardlink", "fs.move", "fs.rename", "fs.restore":
		for _, file := range action.Files {
			if !validEvidenceText(file.SourceRootID) || file.SourcePath == "" || !validEvidenceText(file.DestinationRootID) || file.DestinationPath == "" {
				return validationError("review mapped file identity is incomplete")
			}
		}
	case "fs.trash", "fs.delete":
		for _, file := range action.Files {
			if !validEvidenceText(file.SourceRootID) || file.SourcePath == "" {
				return validationError("review file target identity is incomplete")
			}
		}
	case "descriptor.delete":
		for _, file := range action.Files {
			if file.Identity == "" {
				return validationError("review descriptor identity is incomplete")
			}
		}
	case "jellyfin.refresh":
		if len(action.Files) > 1 {
			return validationError("review refresh scope is ambiguous")
		}
		for _, file := range action.Files {
			if file.Identity == "" {
				return validationError("review refresh item identity is incomplete")
			}
		}
	}
	return nil
}

func validMediaKind(value string) bool {
	switch value {
	case "anime", "episode", "movie", "season":
		return true
	default:
		return false
	}
}

func validTransfer(value string) bool {
	switch value {
	case "copy", "hardlink", "move":
		return true
	default:
		return false
	}
}

func validFileRole(value string) bool {
	switch value {
	case "video", "subtitle", "companion":
		return true
	default:
		return false
	}
}

func validateTextSlice(values []string) error {
	for _, value := range values {
		if !validEvidenceText(value) {
			return validationError("review action identity evidence is invalid")
		}
	}
	return nil
}

func validEvidenceText(value string) bool {
	return value != "" && utf8.ValidString(value) && len(value) <= MaxValueLength && !strings.ContainsAny(value, "\r\n")
}

func validOptionalEvidenceText(value string) bool {
	return value == "" || validEvidenceText(value)
}

func validateFenceMap(values map[string]string) error {
	for key, value := range values {
		if !validEvidenceText(key) || !validEvidenceText(value) {
			return validationError("review immutable fence is invalid")
		}
	}
	return nil
}

func validationError(_ string) error { return &APIError{kind: ErrorProtocol} }

func (h *Handler) handleReadError(w http.ResponseWriter, err error) {
	status := http.StatusServiceUnavailable
	title := "Reviews unavailable."
	message := "Review state is temporarily unavailable. Retry without changing the saved draft."
	if errors.Is(err, ErrNotFound) {
		status = http.StatusNotFound
		title = "Review not found."
		message = "The requested review plan was not found."
	} else if errors.Is(err, ErrProtocol) {
		message = "The API returned incomplete or invalid review evidence."
	}
	renderError(w, status, title, message)
}

func setPrivateHeaders(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
	w.Header().Set("X-Robots-Tag", "noindex, nofollow")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
}

func value(v string) string { return template.HTMLEscapeString(v) }

func writeDocument(w http.ResponseWriter, status int, title string, body func(io.Writer)) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, "<!doctype html><html lang=\"en\"><head><meta charset=\"utf-8\"><meta name=\"viewport\" content=\"width=device-width,initial-scale=1\"><meta name=\"robots\" content=\"noindex,nofollow\"><title>")
	_, _ = io.WriteString(w, value(title)+" · Mastarr</title></head><body><header><a href=\"/\">Mastarr</a><nav aria-label=\"Primary navigation\"><a href=\"/reviews\">Reviews</a><a href=\"/workflows\">Workflows</a></nav></header>")
	body(w)
	_, _ = io.WriteString(w, "</body></html>")
}

func renderError(w http.ResponseWriter, status int, title, message string) {
	writeDocument(w, status, title, func(out io.Writer) {
		_, _ = io.WriteString(out, "<main id=\"review-content\" aria-labelledby=\"review-title\"><h1 id=\"review-title\">")
		_, _ = io.WriteString(out, value(title)+"</h1><p role=\"alert\">")
		_, _ = io.WriteString(out, value(message)+"</p></main>")
	})
}

func renderReviewList(w http.ResponseWriter, state draft, page ReviewPage) {
	writeDocument(w, http.StatusOK, "Reviews", func(out io.Writer) {
		_, _ = io.WriteString(out, "<main id=\"review-content\" aria-labelledby=\"review-title\"><h1 id=\"review-title\">Reviews</h1><p>API-owned immutable plans awaiting exact review.</p><ul>")
		for _, item := range page.Items {
			href := "/reviews/" + url.PathEscape(item.ID)
			if state.Raw != "" {
				href += "?" + state.Raw
			}
			_, _ = io.WriteString(out, "<li><a href=\""+value(href)+"\">Plan "+value(item.ID)+"</a><span> revision "+strconv.Itoa(item.Revision)+", "+value(item.Status)+"</span></li>")
		}
		if len(page.Items) == 0 {
			_, _ = io.WriteString(out, "<li>No review plans were observed.</li>")
		}
		_, _ = io.WriteString(out, "</ul>")
		if page.Page.NextCursor != "" {
			next := url.Values{}
			for key, item := range state.Values {
				next.Set(key, item)
			}
			next.Set("cursor", page.Page.NextCursor)
			_, _ = io.WriteString(out, "<a rel=\"next\" href=\"/reviews?"+value(next.Encode())+"\">Next page</a>")
		}
		_, _ = io.WriteString(out, "</main>")
	})
}

func renderReviewDetail(w http.ResponseWriter, id string, state draft, item Review) {
	title := "Review plan " + id
	writeDocument(w, http.StatusOK, title, func(out io.Writer) {
		_, _ = io.WriteString(out, "<main id=\"review-content\" aria-labelledby=\"review-title\"><h1 id=\"review-title\">Review plan "+value(id)+"</h1>")
		_, _ = io.WriteString(out, "<p>API-owned observation. This page never dispatches a mutation.</p>")
		renderBinding(out, item)
		renderPhases(out, item)
		renderScope(out, item)
		renderEffects(out, item)
		renderDraft(out, id, state, item)
		_, _ = io.WriteString(out, "</main>")
	})
}

func renderBinding(out io.Writer, item Review) {
	_, _ = io.WriteString(out, "<section aria-labelledby=\"review-binding\"><h2 id=\"review-binding\">Immutable approval binding</h2><dl>")
	rows := [][2]string{
		{"Plan revision", strconv.Itoa(item.Revision)}, {"Digest", item.Digest}, {"Status", item.Status},
		{"Approval requirement", item.RequiredApproval},
	}
	if item.EstimatedBytes == nil {
		rows = append(rows, [2]string{"Estimated storage bytes", "unknown"})
	} else {
		rows = append(rows, [2]string{"Estimated storage bytes", strconv.Itoa(*item.EstimatedBytes)})
	}
	if len(item.Capabilities) == 0 {
		rows = append(rows, [2]string{"Capabilities", "unknown"})
	} else {
		rows = append(rows, [2]string{"Capabilities", strings.Join(item.Capabilities, ", ")})
	}
	if len(item.DesiredStateKeys) == 0 {
		rows = append(rows, [2]string{"Desired-state fields", "unknown"})
	} else {
		rows = append(rows, [2]string{"Desired-state fields", strings.Join(item.DesiredStateKeys, ", ")})
	}
	for _, fence := range [][2]string{
		{"Source revision", item.SourceRevision},
		{"Config revision", item.ConfigRevision},
		{"Manifest digest", item.ManifestDigest},
		{"Desired-state digest", item.DesiredDigest},
	} {
		label, valueText := fence[0], fence[1]
		rows = append(rows, [2]string{label, valueOrUnknown(valueText)})
	}
	if item.CreatedAt != nil {
		rows = append(rows, [2]string{"Created", item.CreatedAt.UTC().Format(time.RFC3339)})
	}
	if item.ExpiresAt != nil {
		rows = append(rows, [2]string{"Expires", item.ExpiresAt.UTC().Format(time.RFC3339)})
	}
	for key, valueText := range item.ConnectionFences {
		rows = append(rows, [2]string{"Connection fence " + key, valueText})
	}
	for key, valueText := range item.MappingFences {
		rows = append(rows, [2]string{"Mapping fence " + key, valueText})
	}
	for _, row := range rows {
		_, _ = io.WriteString(out, "<dt>"+value(row[0])+"</dt><dd>"+value(row[1])+"</dd>")
	}
	_, _ = io.WriteString(out, "</dl>")
	if len(item.Preconditions) > 0 {
		_, _ = io.WriteString(out, "<h3>Read-before-write preconditions</h3><ul>")
		for _, precondition := range item.Preconditions {
			_, _ = io.WriteString(out, "<li>"+value(precondition)+"</li>")
		}
		_, _ = io.WriteString(out, "</ul>")
	}
	for _, issue := range append(append([]string{}, item.Conflicts...), item.BlockingIssues...) {
		_, _ = io.WriteString(out, "<p role=\"alert\">Conflict or block: "+value(issue)+"</p>")
	}
	_, _ = io.WriteString(out, "</section>")
}

func renderPhases(out io.Writer, item Review) {
	_, _ = io.WriteString(out, "<section aria-labelledby=\"approval-phases\"><h2 id=\"approval-phases\">Approval phases</h2>")
	switch item.Action.Kind {
	case "arr.registration":
		_, _ = io.WriteString(out, "<h3>1. Registration approval</h3><p>Unknown Arr identity requires this exact registration decision before an import preview exists.</p><p class=\"state\">Current phase: "+value(item.Status)+"</p><h3>2. Exact import approval</h3><p>Waiting for registration success and a new server-observed import plan. Registration approval is never reused as import approval.</p>")
	case "arr.import":
		_, _ = io.WriteString(out, "<h3>1. Registration prerequisite</h3><p>Registration must already be observed as successful for the exact external title.</p><h3>2. Exact import approval</h3><p>Only the immutable files, episodes, subtitles and native preview revision below are in scope.</p><p class=\"state\">Current phase: "+value(item.Status)+"</p>")
	default:
		_, _ = io.WriteString(out, "<h3>Standalone action approval</h3><p>This plan is one independently reviewed effect. It is not a blanket approval for other actions.</p><p class=\"state\">Current phase: "+value(item.Status)+"</p>")
	}
	_, _ = io.WriteString(out, "</section>")
}

func renderScope(out io.Writer, item Review) {
	_, _ = io.WriteString(out, "<section aria-labelledby=\"exact-scope\"><h2 id=\"exact-scope\">Exact selected scope</h2>")
	if len(item.Manifest) == 0 && len(item.Action.Files) == 0 {
		_, _ = io.WriteString(out, "<p>Scope evidence is unknown or empty; the API must resolve it before approval.</p>")
	}
	if len(item.Manifest) > 0 {
		_, _ = io.WriteString(out, "<h3>Plan manifest</h3><table><caption>Every selected file or directory</caption><thead><tr><th>Root</th><th>Relative path</th><th>Type</th><th>Role</th><th>Size</th><th>Identity</th><th>Observed</th></tr></thead><tbody>")
		for _, entry := range item.Manifest {
			observed := "unknown"
			if entry.ObservedAt != nil && !entry.ObservedAt.IsZero() {
				observed = entry.ObservedAt.UTC().Format(time.RFC3339)
			}
			_, _ = io.WriteString(out, "<tr><td>"+value(entry.RootID)+"</td><td>"+value(entry.RelativePath)+"</td><td>"+value(entry.Type)+"</td><td>"+valueOrUnknown(entry.Role)+"</td><td>"+strconv.Itoa(entry.Size)+"</td><td>"+valueOrUnknown(entry.Identity)+"</td><td>"+value(observed)+"</td></tr>")
		}
		_, _ = io.WriteString(out, "</tbody></table>")
	}
	if len(item.Action.Files) > 0 {
		_, _ = io.WriteString(out, "<h3>Action file and association scope</h3><table><caption>Source, destination and episode/subtitle associations</caption><thead><tr><th>Source</th><th>Destination</th><th>Identity</th><th>Role</th><th>Episode or movie</th><th>Subtitle</th><th>Language</th><th>Forced</th><th>SDH</th></tr></thead><tbody>")
		for _, file := range item.Action.Files {
			_, _ = io.WriteString(out, "<tr><td>"+value(file.SourceRootID+"/"+file.SourcePath)+"</td><td>"+value(file.DestinationRootID+"/"+file.DestinationPath)+"</td><td>"+valueOrUnknown(file.Identity)+"</td><td>"+valueOrUnknown(file.Role)+"</td><td>"+intOrUnknownValue(file.MovieOrEpisodeID)+"</td><td>"+value(boolOrUnknown(file.Subtitle))+"</td><td>"+valueOrUnknown(file.Language)+"</td><td>"+value(boolOrUnknown(file.Forced))+"</td><td>"+value(boolOrUnknown(file.HearingImpaired))+"</td></tr>")
		}
		_, _ = io.WriteString(out, "</tbody></table>")
	}
	_, _ = io.WriteString(out, "<h3>Action details</h3><dl>")
	details := [][2]string{{"Action kind", item.Action.Kind}, {"Connection", item.Action.ConnectionID}, {"Media kind", item.Action.MediaKind}, {"Provider ID", item.Action.ProviderID}, {"Registered external ID", item.Action.RegisteredExternalID}, {"Preview revision", item.Action.PreviewRevision}, {"Transfer", item.Action.Transfer}, {"Executor", item.Action.Executor}, {"Root folder", item.Action.RootFolder}, {"Series type", item.Action.SeriesType}}
	for _, row := range details {
		if row[1] != "" {
			_, _ = io.WriteString(out, "<dt>"+value(row[0])+"</dt><dd>"+value(row[1])+"</dd>")
		}
	}
	renderActionAuthority(out, item.Action)
	if supportsMonitoring(item.Action.Kind) {
		if item.Action.Monitoring == nil || !*item.Action.Monitoring {
			_, _ = io.WriteString(out, "<p>Monitoring is unchecked. Enabling it would be a separate future-acquisition choice.</p>")
		} else {
			_, _ = io.WriteString(out, "<p>Monitoring was explicitly selected in this immutable plan.</p>")
		}
	}
	if supportsFallback(item.Action.Kind, item.Action.Transfer) {
		_, _ = io.WriteString(out, "<p>Hardlink failure requires a new copy plan with its own digest, scope and space estimate; no automatic fallback is implied.</p>")
	}
	if item.Action.Irreversible || item.RequiredApproval == "irreversible" || len(item.Impacts) > 0 {
		_, _ = io.WriteString(out, "<h3>Destructive impact</h3><ul>")
		for _, impact := range item.Impacts {
			_, _ = io.WriteString(out, "<li>"+value(impact)+"</li>")
		}
		if len(item.Impacts) == 0 {
			_, _ = io.WriteString(out, "<li>Irreversible effects require an explicit acknowledgement and exact scope.</li>")
		}
		_, _ = io.WriteString(out, "</ul>")
	}
	_, _ = io.WriteString(out, "</dl></section>")
}

func renderActionAuthority(out io.Writer, action Action) {
	if len(action.ClientItemIDs) == 0 {
		if action.Kind == "client.stop" || action.Kind == "client.remove" {
			_, _ = io.WriteString(out, "<dt>Client or torrent IDs</dt><dd>unknown</dd>")
		}
	} else {
		_, _ = io.WriteString(out, "<dt>Client or torrent IDs</dt><dd>"+value(strings.Join(action.ClientItemIDs, ", "))+"</dd>")
	}
	if action.Kind == "fs.trash" {
		if action.RetentionDays == nil {
			_, _ = io.WriteString(out, "<dt>Retention days</dt><dd>unknown</dd>")
		} else {
			_, _ = io.WriteString(out, "<dt>Retention days</dt><dd>"+strconv.Itoa(*action.RetentionDays)+"</dd>")
		}
		if len(action.StoppedClientIDs) == 0 {
			_, _ = io.WriteString(out, "<dt>Stopped-client prerequisites</dt><dd>unknown</dd>")
		} else {
			_, _ = io.WriteString(out, "<dt>Stopped-client prerequisites</dt><dd>"+value(strings.Join(action.StoppedClientIDs, ", "))+"</dd>")
		}
	}
	if action.Kind == "client.remove" {
		_, _ = io.WriteString(out, "<dt>Retain payload</dt><dd>"+value(boolOrUnknown(action.RetainPayload))+"</dd>")
	}
	if action.Kind == "fs.delete" {
		_, _ = io.WriteString(out, "<dt>Permanent deletion</dt><dd>"+value(boolOrUnknown(action.Permanent))+"</dd>")
	}
	if action.Irreversible || action.Kind == "client.remove" {
		_, _ = io.WriteString(out, "<dt>Irreversible scope</dt><dd>true; unselected payload remains outside this exact plan</dd>")
	}
	if action.Kind == "arr.registration" {
		_, _ = io.WriteString(out, "<dt>Monitoring</dt><dd>"+value(boolOrUnknown(action.Monitoring))+"</dd><dt>Season folder</dt><dd>"+value(boolOrUnknown(action.SeasonFolder))+"</dd><dt>Quality profile</dt><dd>"+value(intOrUnknown(action.QualityProfileID))+"</dd><dt>Root folder</dt><dd>"+valueOrUnknown(action.RootFolder)+"</dd>")
		if len(action.Seasons) == 0 {
			_, _ = io.WriteString(out, "<dt>Selected seasons</dt><dd>unknown</dd>")
		} else {
			_, _ = io.WriteString(out, "<dt>Selected seasons</dt><dd>"+value(intListText(action.Seasons))+"</dd>")
		}
	}
}

func supportsMonitoring(kind string) bool { return kind == "arr.registration" }

func supportsFallback(kind, transfer string) bool {
	return kind == "fs.hardlink" || (kind == "arr.import" && (transfer == "hardlink" || transfer == "copy"))
}

func renderEffects(out io.Writer, item Review) {
	_, _ = io.WriteString(out, "<section aria-labelledby=\"review-state\"><h2 id=\"review-state\">Decision and retry state</h2>")
	if item.Decision == nil {
		_, _ = io.WriteString(out, "<p>No decision is recorded. The API owns approval, idempotency and action-run creation.</p>")
	} else {
		decision := item.Decision
		_, _ = io.WriteString(out, "<dl><dt>Decision</dt><dd>"+value(decision.Decision)+"</dd><dt>Actor</dt><dd>"+valueOrUnknown(decision.Actor)+"</dd><dt>Caller label</dt><dd>"+valueOrUnknown(decision.UnverifiedLabel)+"</dd><dt>Decision digest</dt><dd>"+value(decision.Digest)+"</dd><dt>Resulting action run</dt><dd>"+valueOrUnknown(decision.ResultingActionRun)+"</dd></dl>")
	}
	_, _ = io.WriteString(out, "<p>Repeated submissions must retain the same idempotency key and immutable binding. A failed transport does not authorize a new mutation.</p></section>")
}

func renderDraft(out io.Writer, id string, state draft, item Review) {
	_, _ = io.WriteString(out, "<section aria-labelledby=\"review-draft\"><h2 id=\"review-draft\">Review draft</h2><p id=\"review-draft-help\">These GET fields only preserve your draft. Submit the decision through the API-owned review endpoint.</p><form method=\"get\" action=\"/reviews/"+value(id)+"\" aria-describedby=\"review-draft-help\">")
	keys := make([]string, 0, len(state.Values))
	for key := range state.Values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if key == "monitoring" || key == "decision" || key == "idempotencyKey" || key == "reason" || key == "fallback" {
			continue
		}
		_, _ = io.WriteString(out, "<input type=\"hidden\" name=\""+value(key)+"\" value=\""+value(state.Values[key])+"\">")
	}
	decision := state.Values["decision"]
	_, _ = io.WriteString(out, "<label for=\"review-decision\">Decision</label><select id=\"review-decision\" name=\"decision\"><option value=\"\"")
	if decision == "" {
		_, _ = io.WriteString(out, " selected")
	}
	_, _ = io.WriteString(out, ">Choose later</option><option value=\"approve\"")
	if decision == "approve" {
		_, _ = io.WriteString(out, " selected")
	}
	_, _ = io.WriteString(out, ">Approve exact plan</option><option value=\"reject\"")
	if decision == "reject" {
		_, _ = io.WriteString(out, " selected")
	}
	_, _ = io.WriteString(out, ">Reject exact plan</option></select>")
	_, _ = io.WriteString(out, "<label for=\"review-idempotency\">Idempotency key</label><input id=\"review-idempotency\" name=\"idempotencyKey\" value=\""+value(state.Values["idempotencyKey"])+"\" maxlength=\""+strconv.Itoa(MaxValueLength)+"\">")
	_, _ = io.WriteString(out, "<label for=\"review-reason\">Reason or note</label><textarea id=\"review-reason\" name=\"reason\" maxlength=\""+strconv.Itoa(MaxTextLength)+"\">"+value(state.Values["reason"])+"</textarea>")
	if supportsMonitoring(item.Action.Kind) {
		_, _ = io.WriteString(out, "<label><input type=\"checkbox\" name=\"monitoring\" value=\"true\"")
		if state.Values["monitoring"] == "true" {
			_, _ = io.WriteString(out, " checked")
		}
		_, _ = io.WriteString(out, "> Opt in to future monitoring</label><p>Monitoring starts future acquisition only when the API records it; it does not change this plan.</p>")
	}
	if supportsFallback(item.Action.Kind, item.Action.Transfer) {
		fallback := state.Values["fallback"]
		_, _ = io.WriteString(out, "<fieldset><legend>Transfer fallback</legend><label><input type=\"radio\" name=\"fallback\" value=\"hardlink\"")
		if fallback == "hardlink" {
			_, _ = io.WriteString(out, " checked")
		}
		_, _ = io.WriteString(out, "> Hardlink</label><label><input type=\"radio\" name=\"fallback\" value=\"copy\"")
		if fallback == "copy" {
			_, _ = io.WriteString(out, " checked")
		}
		_, _ = io.WriteString(out, "> Copy as a new plan</label></fieldset>")
	}
	_, _ = io.WriteString(out, "<button type=\"submit\">Save review draft</button></form><p>Cancel requests acknowledge no further dispatch; late or partial effects stay visible for reconciliation.</p></section>")
	_ = item
}

func valueOrUnknown(valueText string) string {
	if valueText == "" {
		return "unknown"
	}
	return value(valueText)
}

func boolOrUnknown(value *bool) string {
	if value == nil {
		return "unknown"
	}
	return strconv.FormatBool(*value)
}

func intOrUnknown(value *int) string {
	if value == nil {
		return "unknown"
	}
	return strconv.Itoa(*value)
}

func intOrUnknownValue(value int) string {
	if value <= 0 {
		return "unknown"
	}
	return strconv.Itoa(value)
}

func intListText(values []int) string {
	result := make([]string, 0, len(values))
	for _, item := range values {
		result = append(result, strconv.Itoa(item))
	}
	return strings.Join(result, ", ")
}
