// Package workflows renders API-owned ordered workflow state for the UI BFF.
// It consumes normalized observations only. The package has no database,
// upstream, filesystem, credential, or action-dispatch dependency.
package workflows

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
	MaxQueryValues  = 64
	MaxValueLength  = 512
	MaxSteps        = 1024
	MaxEffects      = 4096
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
	ErrNotFound    = errors.New("workflow resource not found")
	ErrUnavailable = errors.New("workflow API unavailable")
	ErrProtocol    = errors.New("workflow API response invalid")
)

// APIError is safe for UI display and logs. Context cancellation/deadline
// identity is retained without exposing an endpoint or transport error.
type APIError struct {
	kind   ErrorKind
	status int
	cause  error
}

func (e *APIError) Error() string {
	if e == nil {
		return "workflow read failed"
	}
	switch e.kind {
	case ErrorNotFound:
		return "workflow record not found"
	case ErrorProtocol:
		return "workflow response invalid"
	case ErrorCanceled:
		return "workflow read canceled"
	case ErrorTimeout:
		return "workflow read timed out"
	default:
		return "workflows temporarily unavailable"
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

// Kind reports the sanitized error category.
func (e *APIError) Kind() ErrorKind {
	if e == nil {
		return ""
	}
	return e.kind
}

// Status reports the observed HTTP status, or zero for non-HTTP failures.
func (e *APIError) Status() int {
	if e == nil {
		return 0
	}
	return e.status
}

// PageRequest is the bounded list query accepted by Handler.
type PageRequest struct {
	Cursor string
	Limit  int
}

// PageInfo retains cursor and observation time from the API.
type PageInfo struct {
	NextCursor string
	ObservedAt time.Time
}

// WorkflowPage is one bounded page of workflow observations.
type WorkflowPage struct {
	Items []Workflow
	Page  PageInfo
}

// Workflow is a normalized ordered workflow projection.
type Workflow struct {
	ID                   string
	Name                 string
	RecipeVersion        string
	PlanRevision         string
	PlanDigest           string
	SourceRevision       string
	ConfigRevision       string
	MappingRevision      string
	ManifestDigest       string
	DesiredDigest        string
	State                string
	CurrentStep          string
	DeadlineAt           *time.Time
	AggregateEffectCount *int
	UnresolvedCount      *int
	ApprovalGates        []string
	Cancellation         *Cancellation
	Steps                []Step
	IdempotencyKey       string
}

// WorkflowRun is an alias for callers using the API name.
type WorkflowRun = Workflow

// Step is one ordered action/approval unit. Action/uncertainty evidence stays
// attached to this step and is never rolled into an aggregate success label.
type Step struct {
	Sequence          int
	ID                string
	ActionPlanID      string
	ActionRunID       string
	ApprovalGate      string
	ActionKind        string
	State             string
	Outcome           string
	LastObservation   string
	LastObservedAt    *time.Time
	RetryAt           *time.Time
	RetryReason       string
	UnresolvedEffects []string
	Effects           []Effect
	Error             string
	Impacts           []string
	Destructive       bool
}

// Effect is an exact handler-provided effect identity and state. An opaque
// evidence value is displayed only as bounded sanitized text.
type Effect struct {
	ID         string
	State      string
	Outcome    string
	ObservedAt *time.Time
	Evidence   string
	Error      string
}

// Cancellation records durable cancellation acknowledgement and timing.
type Cancellation struct {
	ID          string
	State       string
	RequestedAt time.Time
	EffectiveAt *time.Time
	Reason      string
}

// Reader is the normalized HTTP-only dependency consumed by Handler.
type Reader interface {
	ListWorkflows(context.Context, PageRequest) (WorkflowPage, error)
	GetWorkflow(context.Context, string) (Workflow, error)
}

// Handler serves GET/HEAD workflow pages. The forms preserve a draft only;
// API routes own approval, cancellation, retry, and reconciliation effects.
type Handler struct {
	reader      Reader
	defaultSize int
	maxSize     int
}

// Options bounds list reads.
type Options struct {
	DefaultPageSize int
	MaxPageSize     int
}

// NewHandler constructs a bounded read-only workflow handler.
func NewHandler(reader Reader) http.Handler {
	return NewHandlerWithOptions(reader, Options{})
}

// NewHandlerWithOptions constructs a workflow handler with fixed bounds.
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
		renderError(w, http.StatusBadRequest, "Bad request.", "The workflow request is invalid.")
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", http.MethodGet+", "+http.MethodHead)
		renderError(w, http.StatusMethodNotAllowed, "Method not allowed.", "Workflow controls are API-owned and this UI view is read-only.")
		return
	}
	detail, id, ok := parseRoute(r.URL.Path)
	if !ok {
		renderError(w, http.StatusNotFound, "Page not found.", "The requested workflow page was not found.")
		return
	}
	state, err := parseDraft(r.URL.RawQuery, h.defaultSize, h.maxSize)
	if err != nil {
		renderError(w, http.StatusBadRequest, "Invalid workflow draft.", "The supplied workflow fields are invalid or too large.")
		return
	}
	if h.reader == nil {
		renderError(w, http.StatusServiceUnavailable, "Workflows unavailable.", "Retry to load API-owned workflow state. Draft values remain in the address.")
		return
	}
	if detail {
		item, readErr := h.reader.GetWorkflow(r.Context(), id)
		if readErr != nil {
			h.handleReadError(w, readErr)
			return
		}
		if item.ID != id {
			h.handleReadError(w, validationError("workflow identity does not match request"))
			return
		}
		if err := validateWorkflow(item); err != nil {
			h.handleReadError(w, err)
			return
		}
		renderWorkflowDetail(w, id, state, item)
		return
	}
	page, readErr := h.reader.ListWorkflows(r.Context(), state.PageRequest)
	if readErr != nil {
		h.handleReadError(w, readErr)
		return
	}
	if err := validateWorkflowPage(page); err != nil {
		h.handleReadError(w, err)
		return
	}
	renderWorkflowList(w, state, page)
}

func parseRoute(path string) (detail bool, id string, ok bool) {
	if path == "/workflows" {
		return false, "", true
	}
	if !strings.HasPrefix(path, "/workflows/") || strings.Count(strings.TrimPrefix(path, "/workflows/"), "/") != 0 {
		return false, "", false
	}
	id = strings.TrimPrefix(path, "/workflows/")
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
	"cursor": true, "limit": true, "idempotencyKey": true, "reason": true,
	"selectedStep": true, "planId": true, "planRevision": true, "digest": true,
	"sourceRevision": true, "configRevision": true, "mappingRevision": true,
	"manifestDigest": true, "desiredDigest": true,
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
		if !utf8.ValidString(value) || len(value) > MaxValueLength {
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
		}
		state.Values[key] = value
	}
	return state, nil
}

func validateWorkflowPage(page WorkflowPage) error {
	if len(page.Items) > MaxSteps || page.Page.ObservedAt.IsZero() {
		return validationError("workflow page is incomplete")
	}
	for _, item := range page.Items {
		if err := validateWorkflow(item); err != nil {
			return err
		}
	}
	return nil
}

func validateWorkflow(item Workflow) error {
	if !validIdentity(item.ID) || item.Name == "" || len(item.Name) > MaxTextLength {
		return validationError("workflow identity or name is incomplete")
	}
	if !validWorkflowState(item.State) {
		return validationError("workflow state is unknown")
	}
	if len(item.Steps) > MaxSteps {
		return validationError("workflow has too many steps")
	}
	seen := make(map[string]struct{}, len(item.Steps))
	effects := 0
	for index, step := range item.Steps {
		if step.ID == "" || len(step.ID) > 128 {
			return validationError("workflow step identity is incomplete")
		}
		if _, ok := seen[step.ID]; ok {
			return validationError("workflow step identity is duplicated")
		}
		seen[step.ID] = struct{}{}
		if step.Sequence == 0 {
			item.Steps[index].Sequence = index + 1
		}
		if !validStepState(step.State) || !validOutcome(step.Outcome) {
			return validationError("workflow step state is unknown")
		}
		if len(step.Effects)+len(step.UnresolvedEffects) > MaxEffects {
			return validationError("workflow effects exceed the UI limit")
		}
		effects += len(step.Effects) + len(step.UnresolvedEffects)
		if effects > MaxEffects {
			return validationError("workflow effects exceed the UI limit")
		}
	}
	return nil
}

func validWorkflowState(state string) bool {
	switch state {
	case "awaiting_approval", "running", "waiting_dependency", "needs_review", "succeeded", "failed", "cancelled", "deadline_exceeded":
		return true
	default:
		return false
	}
}

func validStepState(state string) bool {
	switch state {
	case "queued", "running", "succeeded", "failed", "blocked", "reconciling", "cancelled", "deadline_exceeded", "already_satisfied", "needs_review", "skipped", "waiting_dependency":
		return true
	default:
		return false
	}
}

func validOutcome(outcome string) bool {
	if outcome == "" {
		return true
	}
	switch outcome {
	case "applied", "already_satisfied", "cancelled", "failed", "pending", "unknown":
		return true
	default:
		return false
	}
}

func validationError(_ string) error { return &APIError{kind: ErrorProtocol} }

func (h *Handler) handleReadError(w http.ResponseWriter, err error) {
	status := http.StatusServiceUnavailable
	title := "Workflows unavailable."
	message := "Workflow state is temporarily unavailable. Retry without changing the saved draft."
	if errors.Is(err, ErrNotFound) {
		status = http.StatusNotFound
		title = "Workflow not found."
		message = "The requested workflow was not found."
	} else if errors.Is(err, ErrProtocol) {
		message = "The API returned incomplete or invalid workflow evidence."
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
		_, _ = io.WriteString(out, "<main id=\"workflow-content\" aria-labelledby=\"workflow-title\"><h1 id=\"workflow-title\">")
		_, _ = io.WriteString(out, value(title)+"</h1><p role=\"alert\">")
		_, _ = io.WriteString(out, value(message)+"</p></main>")
	})
}

func renderWorkflowList(w http.ResponseWriter, state draft, page WorkflowPage) {
	writeDocument(w, http.StatusOK, "Workflows", func(out io.Writer) {
		_, _ = io.WriteString(out, "<main id=\"workflow-content\" aria-labelledby=\"workflow-title\"><h1 id=\"workflow-title\">Workflows</h1><p>Ordered API-owned action recipes and their durable effects.</p><ul>")
		for _, item := range page.Items {
			href := "/workflows/" + url.PathEscape(item.ID)
			if state.Raw != "" {
				href += "?" + state.Raw
			}
			_, _ = io.WriteString(out, "<li><a href=\""+value(href)+"\">"+value(item.Name)+" ("+value(item.ID)+")</a><span> "+value(item.State)+"</span></li>")
		}
		if len(page.Items) == 0 {
			_, _ = io.WriteString(out, "<li>No workflows were observed.</li>")
		}
		if page.Page.NextCursor != "" {
			next := url.Values{}
			for key, item := range state.Values {
				next.Set(key, item)
			}
			next.Set("cursor", page.Page.NextCursor)
			_, _ = io.WriteString(out, "<a rel=\"next\" href=\"/workflows?"+value(next.Encode())+"\">Next page</a>")
		}
		_, _ = io.WriteString(out, "</ul></main>")
	})
}

func renderWorkflowDetail(w http.ResponseWriter, id string, state draft, item Workflow) {
	writeDocument(w, http.StatusOK, "Workflow "+id, func(out io.Writer) {
		_, _ = io.WriteString(out, "<main id=\"workflow-content\" aria-labelledby=\"workflow-title\"><h1 id=\"workflow-title\">Workflow "+value(id)+"</h1><p>API-owned ordered state. This UI never dispatches, cancels, retries or reconciles an action.</p>")
		_, _ = io.WriteString(out, "<section aria-labelledby=\"workflow-binding\"><h2 id=\"workflow-binding\">Workflow binding</h2><dl><dt>Name</dt><dd>"+value(item.Name)+"</dd><dt>Recipe version</dt><dd>"+valueOrUnknown(item.RecipeVersion)+"</dd><dt>Plan revision</dt><dd>"+valueOrUnknown(item.PlanRevision)+"</dd><dt>Plan digest</dt><dd>"+valueOrUnknown(item.PlanDigest)+"</dd><dt>Source revision</dt><dd>"+valueOrUnknown(item.SourceRevision)+"</dd><dt>Config revision</dt><dd>"+valueOrUnknown(item.ConfigRevision)+"</dd><dt>Mapping revision</dt><dd>"+valueOrUnknown(item.MappingRevision)+"</dd><dt>Manifest digest</dt><dd>"+valueOrUnknown(item.ManifestDigest)+"</dd><dt>Desired-state digest</dt><dd>"+valueOrUnknown(item.DesiredDigest)+"</dd><dt>State</dt><dd>"+value(item.State)+"</dd><dt>Current step</dt><dd>"+valueOrUnknown(item.CurrentStep)+"</dd>")
		if item.DeadlineAt == nil || item.DeadlineAt.IsZero() {
			_, _ = io.WriteString(out, "<dt>Deadline</dt><dd>unknown</dd>")
		} else {
			_, _ = io.WriteString(out, "<dt>Deadline</dt><dd>"+value(item.DeadlineAt.UTC().Format(time.RFC3339))+"</dd>")
		}
		if item.AggregateEffectCount != nil {
			_, _ = io.WriteString(out, "<dt>Observed effects</dt><dd>"+strconv.Itoa(*item.AggregateEffectCount)+"</dd>")
		}
		if item.UnresolvedCount != nil {
			_, _ = io.WriteString(out, "<dt>Unresolved effects</dt><dd>"+strconv.Itoa(*item.UnresolvedCount)+"</dd>")
		}
		_, _ = io.WriteString(out, "</dl>")
		if len(item.ApprovalGates) > 0 {
			_, _ = io.WriteString(out, "<h3>Server-derived approval gates</h3><ul>")
			for _, gate := range item.ApprovalGates {
				_, _ = io.WriteString(out, "<li>"+value(gate)+"</li>")
			}
			_, _ = io.WriteString(out, "</ul>")
		}
		if item.Cancellation != nil {
			_, _ = io.WriteString(out, "<p role=\"status\">Cancellation "+value(item.Cancellation.State)+" acknowledged at "+value(item.Cancellation.RequestedAt.UTC().Format(time.RFC3339))+". No further undispatched step is sent; late or partial external effects remain visible for reconciliation and are not claimed rolled back.</p>")
		}
		_, _ = io.WriteString(out, "</section>")
		renderSteps(out, item)
		renderDraft(out, id, state)
		_, _ = io.WriteString(out, "</main>")
	})
}

func renderSteps(out io.Writer, item Workflow) {
	if len(item.Steps) == 1 {
		_, _ = io.WriteString(out, "<p>This is a standalone action run; its effects remain independently attributable.</p>")
	} else {
		_, _ = io.WriteString(out, "<p>This is a composed workflow. Steps are ordered and each effect remains independently attributable.</p>")
	}
	_, _ = io.WriteString(out, "<section aria-labelledby=\"workflow-steps\"><h2 id=\"workflow-steps\">Ordered steps</h2><ol>")
	for index, step := range item.Steps {
		sequence := step.Sequence
		if sequence == 0 {
			sequence = index + 1
		}
		_, _ = io.WriteString(out, "<li id=\"step-"+value(step.ID)+"\"><h3>Step "+strconv.Itoa(sequence)+": "+value(step.ID)+"</h3><dl><dt>Plan</dt><dd>"+valueOrUnknown(step.ActionPlanID)+"</dd><dt>Action run</dt><dd>"+valueOrUnknown(step.ActionRunID)+"</dd><dt>State</dt><dd>"+value(stepDisplayState(step))+"</dd>")
		if step.ApprovalGate != "" {
			_, _ = io.WriteString(out, "<dt>Approval gate</dt><dd>"+value(step.ApprovalGate)+"</dd>")
		}
		if step.ActionKind != "" {
			_, _ = io.WriteString(out, "<dt>Action kind</dt><dd>"+value(step.ActionKind)+"</dd>")
		}
		if step.Outcome != "" {
			_, _ = io.WriteString(out, "<dt>Outcome</dt><dd>"+value(step.Outcome)+"</dd>")
		}
		if step.LastObservation != "" {
			_, _ = io.WriteString(out, "<dt>Last observation</dt><dd>"+value(step.LastObservation)+"</dd>")
		}
		if step.LastObservedAt != nil && !step.LastObservedAt.IsZero() {
			_, _ = io.WriteString(out, "<dt>Observed at</dt><dd>"+value(step.LastObservedAt.UTC().Format(time.RFC3339))+"</dd>")
		}
		if step.RetryAt != nil && !step.RetryAt.IsZero() {
			_, _ = io.WriteString(out, "<dt>Retry at</dt><dd>"+value(step.RetryAt.UTC().Format(time.RFC3339))+"</dd>")
		}
		if step.RetryReason != "" {
			_, _ = io.WriteString(out, "<dt>Retry reason</dt><dd>"+value(step.RetryReason)+"</dd>")
		}
		if step.Error != "" {
			_, _ = io.WriteString(out, "<dt>Error</dt><dd role=\"alert\">"+value(step.Error)+"</dd>")
		}
		_, _ = io.WriteString(out, "</dl>")
		if len(step.Effects) > 0 || len(step.UnresolvedEffects) > 0 {
			_, _ = io.WriteString(out, "<h4>Effects</h4><ul>")
			for _, effect := range step.Effects {
				_, _ = io.WriteString(out, "<li>"+value(effect.ID)+": "+value(effectDisplayState(effect))+valueOptionalDetail(effect.Error, " error: ")+valueOptionalDetail(effect.Evidence, " evidence")+"</li>")
			}
			for _, unresolved := range step.UnresolvedEffects {
				_, _ = io.WriteString(out, "<li>"+value(unresolved)+": unresolved; read-only reconciliation is required before retry</li>")
			}
			_, _ = io.WriteString(out, "</ul>")
		}
		if len(step.Impacts) > 0 || step.Destructive {
			_, _ = io.WriteString(out, "<p>Destructive or client/torrent effects are separate and exact: ")
			if len(step.Impacts) > 0 {
				_, _ = io.WriteString(out, value(strings.Join(step.Impacts, "; ")))
			} else {
				_, _ = io.WriteString(out, "explicit confirmation and API-owned scope are required")
			}
			_, _ = io.WriteString(out, "</p>")
		}
		_, _ = io.WriteString(out, "</li>")
	}
	_, _ = io.WriteString(out, "</ol></section>")
}

func stepDisplayState(step Step) string {
	if step.State != "" {
		return step.State
	}
	if step.Outcome == "already_satisfied" {
		return "already_satisfied"
	}
	return "unknown"
}

func effectDisplayState(effect Effect) string {
	if effect.State != "" {
		return effect.State
	}
	if effect.Outcome != "" {
		return effect.Outcome
	}
	return "unknown"
}

func valueOptionalDetail(detail, prefix string) string {
	if detail == "" {
		return ""
	}
	return prefix + value(detail)
}

func renderDraft(out io.Writer, id string, state draft) {
	_, _ = io.WriteString(out, "<section aria-labelledby=\"workflow-draft\"><h2 id=\"workflow-draft\">Workflow draft</h2><p id=\"workflow-draft-help\">GET fields preserve review context only. The API owns approval, cancellation and retry decisions.</p><form method=\"get\" action=\"/workflows/"+value(id)+"\" aria-describedby=\"workflow-draft-help\">")
	keys := make([]string, 0, len(state.Values))
	for key := range state.Values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if key == "idempotencyKey" || key == "reason" {
			continue
		}
		_, _ = io.WriteString(out, "<input type=\"hidden\" name=\""+value(key)+"\" value=\""+value(state.Values[key])+"\">")
	}
	_, _ = io.WriteString(out, "<label for=\"workflow-idempotency\">Idempotency key</label><input id=\"workflow-idempotency\" name=\"idempotencyKey\" value=\""+value(state.Values["idempotencyKey"])+"\" maxlength=\""+strconv.Itoa(MaxValueLength)+"\"><label for=\"workflow-reason\">Reason or note</label><textarea id=\"workflow-reason\" name=\"reason\" maxlength=\""+strconv.Itoa(MaxTextLength)+"\">"+value(state.Values["reason"])+"</textarea><button type=\"submit\">Save workflow draft</button></form><p>Retry preserves the same immutable plan and idempotency key. A cancellation acknowledgement prevents later dispatch while late effects remain visible.</p></section>")
}

func valueOrUnknown(valueText string) string {
	if valueText == "" {
		return "unknown"
	}
	return value(valueText)
}
