package workflows

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	generated "github.com/guilycst/mastarr/ui/internal/api/generated"
	"github.com/guilycst/mastarr/ui/internal/client"
)

const maxResponseBytes = 1 << 20

// HTTPReader adapts generated workflow and action-run responses to Reader.
// Generated DTOs remain private to this adapter file.
type HTTPReader struct {
	generated *generated.ClientWithResponses
	timeout   time.Duration
}

var _ Reader = (*HTTPReader)(nil)

// NewHTTPReader creates a redirect-safe bounded API reader.
func NewHTTPReader(baseURL string, httpClient *http.Client, timeout time.Duration) (*HTTPReader, error) {
	if err := validateAPIURL(baseURL); err != nil {
		return nil, err
	}
	if timeout <= 0 {
		timeout = client.DefaultTimeout
	}
	hc := cloneHTTPClient(httpClient)
	api, err := generated.NewClientWithResponses(strings.TrimRight(baseURL, "/"), generated.WithHTTPClient(hc))
	if err != nil {
		return nil, errors.New("create workflow API client failed")
	}
	return &HTTPReader{generated: api, timeout: timeout}, nil
}

func validateAPIURL(raw string) error {
	if raw == "" || !utf8.ValidString(raw) || strings.TrimSpace(raw) != raw {
		return errors.New("API URL must be an absolute HTTP(S) URL")
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed == nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.Hostname() == "" || parsed.Opaque != "" {
		return errors.New("API URL must be an absolute HTTP(S) URL")
	}
	if parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || strings.ContainsAny(parsed.Host, "\r\n\t") {
		return errors.New("API URL must not contain credentials, query data, or fragments")
	}
	if port := parsed.Port(); port != "" {
		value, err := strconv.Atoi(port)
		if err != nil || value < 0 || value > 65535 {
			return errors.New("API URL contains an invalid port")
		}
	}
	return nil
}

func cloneHTTPClient(input *http.Client) *http.Client {
	var hc http.Client
	if input != nil {
		hc = *input
	}
	hc.CheckRedirect = func(_ *http.Request, _ []*http.Request) error {
		return http.ErrUseLastResponse
	}
	transport := hc.Transport
	if transport == nil {
		transport = http.DefaultTransport
	}
	hc.Transport = boundedTransport{next: transport, max: maxResponseBytes}
	return &hc
}

type boundedTransport struct {
	next http.RoundTripper
	max  int64
}

func (t boundedTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	response, err := t.next.RoundTrip(request)
	if err != nil || response == nil || response.Body == nil {
		return response, err
	}
	response.Body = &boundedBody{reader: response.Body, closer: response.Body, max: t.max}
	return response, nil
}

type boundedBody struct {
	reader io.Reader
	closer io.Closer
	seen   int64
	max    int64
}

func (b *boundedBody) Read(buffer []byte) (int, error) {
	if b.seen >= b.max {
		var one [1]byte
		n, err := b.reader.Read(one[:])
		if n > 0 {
			b.seen += int64(n)
			return 0, errResponseTooLarge
		}
		return 0, err
	}
	remaining := b.max - b.seen
	if int64(len(buffer)) > remaining {
		buffer = buffer[:remaining]
	}
	n, err := b.reader.Read(buffer)
	b.seen += int64(n)
	return n, err
}

func (b *boundedBody) Close() error { return b.closer.Close() }

var errResponseTooLarge = errors.New("workflow response too large")

func (r *HTTPReader) requestContext(parent context.Context) (context.Context, context.CancelFunc) {
	if parent == nil {
		parent = context.Background()
	}
	return context.WithTimeout(parent, r.timeout)
}

func (r *HTTPReader) ListWorkflows(parent context.Context, query PageRequest) (WorkflowPage, error) {
	if r == nil || r.generated == nil {
		return WorkflowPage{}, unavailableError(nil)
	}
	ctx, cancel := r.requestContext(parent)
	defer cancel()
	params := generated.ListWorkflowRunsParams{}
	if query.Cursor != "" {
		cursor := generated.Cursor(query.Cursor)
		params.Cursor = &cursor
	}
	if query.Limit > 0 {
		limit := generated.Limit(query.Limit)
		params.Limit = &limit
	}
	response, err := r.generated.ListWorkflowRunsWithResponse(ctx, &params)
	if err != nil {
		return WorkflowPage{}, classifyHTTPError(err, ctx)
	}
	if err := checkResponse(response, http.StatusOK); err != nil {
		return WorkflowPage{}, err
	}
	body := response.GetBody()
	var source generated.WorkflowRunList
	if err := decodeStrict(body, &source); err != nil {
		return WorkflowPage{}, protocolError(err)
	}
	items := make([]Workflow, 0, len(source.Items))
	for _, item := range source.Items {
		converted, convertErr := r.convertWorkflow(ctx, item)
		if convertErr != nil {
			return WorkflowPage{}, convertErr
		}
		items = append(items, converted)
	}
	if len(items) > MaxSteps {
		return WorkflowPage{}, protocolError(errors.New("workflow page too large"))
	}
	return WorkflowPage{Items: items, Page: PageInfo{NextCursor: stringValue(source.Page.NextCursor), ObservedAt: source.Page.ObservedAt}}, nil
}

func (r *HTTPReader) GetWorkflow(parent context.Context, id string) (Workflow, error) {
	if r == nil || r.generated == nil {
		return Workflow{}, unavailableError(nil)
	}
	parsed, err := parseUUID(id)
	if err != nil {
		return Workflow{}, &APIError{kind: ErrorNotFound}
	}
	ctx, cancel := r.requestContext(parent)
	defer cancel()
	response, err := r.generated.GetWorkflowRunWithResponse(ctx, parsed)
	if err != nil {
		return Workflow{}, classifyHTTPError(err, ctx)
	}
	if err := checkResponse(response, http.StatusOK); err != nil {
		return Workflow{}, err
	}
	body := response.GetBody()
	var source generated.WorkflowRun
	if err := decodeStrict(body, &source); err != nil {
		return Workflow{}, protocolError(err)
	}
	item, err := r.convertWorkflow(ctx, source)
	if err != nil {
		return Workflow{}, err
	}
	if item.ID != id {
		return Workflow{}, protocolError(errors.New("workflow identity mismatch"))
	}
	return item, nil
}

type apiResponse interface {
	GetBody() []byte
	StatusCode() int
}

func checkResponse(response apiResponse, expected int) error {
	if response == nil {
		return protocolError(errors.New("nil API response"))
	}
	if len(response.GetBody()) > maxResponseBytes {
		return protocolError(errResponseTooLarge)
	}
	if response.StatusCode() == expected {
		return nil
	}
	switch response.StatusCode() {
	case http.StatusNotFound:
		return &APIError{kind: ErrorNotFound, status: http.StatusNotFound}
	case http.StatusBadRequest, http.StatusConflict, http.StatusPreconditionFailed, http.StatusUnprocessableEntity:
		return &APIError{kind: ErrorProtocol, status: response.StatusCode()}
	default:
		return &APIError{kind: ErrorUnavailable, status: response.StatusCode()}
	}
}

func decodeStrict(body []byte, destination interface{}) error {
	if len(body) == 0 || len(body) > maxResponseBytes || !utf8.Valid(body) {
		return errors.New("response body invalid")
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	var extra interface{}
	if err := decoder.Decode(&extra); err != io.EOF {
		return errors.New("response has trailing data")
	}
	return nil
}

func classifyHTTPError(err error, ctx context.Context) error {
	if errors.Is(err, errResponseTooLarge) {
		return protocolError(err)
	}
	if errors.Is(ctx.Err(), context.Canceled) || errors.Is(err, context.Canceled) {
		return &APIError{kind: ErrorCanceled, cause: context.Canceled}
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) || errors.Is(err, context.DeadlineExceeded) {
		return &APIError{kind: ErrorTimeout, cause: context.DeadlineExceeded}
	}
	return unavailableError(err)
}

func protocolError(err error) error { return &APIError{kind: ErrorProtocol, cause: err} }

func unavailableError(err error) error { return &APIError{kind: ErrorUnavailable, cause: err} }

func parseUUID(raw string) (generated.Id, error) {
	var result generated.Id
	if len(raw) != 36 || raw[8] != '-' || raw[13] != '-' || raw[18] != '-' || raw[23] != '-' {
		return result, errors.New("invalid identity")
	}
	compact := strings.ReplaceAll(raw, "-", "")
	if len(compact) != 32 {
		return result, errors.New("invalid identity")
	}
	decoded, err := hex.DecodeString(compact)
	if err != nil {
		return result, errors.New("invalid identity")
	}
	copy(result[:], decoded)
	return result, nil
}

func (r *HTTPReader) convertWorkflow(ctx context.Context, source generated.WorkflowRun) (Workflow, error) {
	item := Workflow{
		ID:                   source.Id.String(),
		Name:                 source.Name,
		RecipeVersion:        stringValue(source.RecipeVersion),
		State:                string(source.State),
		CurrentStep:          stringValue(source.CurrentStep),
		DeadlineAt:           cloneTime((*time.Time)(source.DeadlineAt)),
		AggregateEffectCount: cloneInt(source.AggregateEffectCount),
		UnresolvedCount:      cloneInt(source.UnresolvedCount),
		ApprovalGates:        append([]string(nil), stringSliceValue(source.ApprovalGates)...),
		Cancellation:         convertCancellation(source.Cancellation),
		Steps:                make([]Step, 0, len(source.Steps)),
	}
	for index, step := range source.Steps {
		converted := Step{
			Sequence:     index + 1,
			ID:           step.Id,
			ActionPlanID: step.ActionPlanId.String(),
			ApprovalGate: stringValue(step.ApprovalGate),
			State:        string(step.State),
		}
		if step.ActionRunId != nil {
			converted.ActionRunID = step.ActionRunId.String()
			run, err := r.getActionRun(ctx, *step.ActionRunId)
			if err != nil {
				return Workflow{}, err
			}
			mergeActionRun(&converted, run)
		}
		item.Steps = append(item.Steps, converted)
	}
	return item, nil
}

func (r *HTTPReader) getActionRun(ctx context.Context, id generated.Id) (generated.ActionRun, error) {
	response, err := r.generated.GetActionRunWithResponse(ctx, id)
	if err != nil {
		return generated.ActionRun{}, classifyHTTPError(err, ctx)
	}
	if err := checkResponse(response, http.StatusOK); err != nil {
		return generated.ActionRun{}, err
	}
	var source generated.ActionRun
	if err := decodeStrict(response.GetBody(), &source); err != nil {
		return generated.ActionRun{}, protocolError(err)
	}
	return source, nil
}

func mergeActionRun(step *Step, run generated.ActionRun) {
	step.ActionRunID = run.Id.String()
	step.State = string(run.State)
	step.Outcome = string(run.Outcome)
	step.LastObservation = stringValue(run.LastObservation)
	step.LastObservedAt = nil
	step.RetryAt = cloneTime(run.NextAttemptAt)
	step.RetryReason = stringValue(run.RetryReason)
	step.UnresolvedEffects = append([]string(nil), run.UnresolvedEffects...)
	step.Effects = make([]Effect, 0, len(run.Effects)+len(run.UnresolvedEffects))
	for _, effect := range run.Effects {
		state := "observed"
		if run.Outcome == generated.ActionRunOutcomeAlreadySatisfied {
			state = "already_satisfied"
		}
		step.Effects = append(step.Effects, Effect{ID: effect, State: state, Outcome: string(run.Outcome)})
	}
	if run.State == generated.ActionStateNeedsReview || run.Outcome == generated.ActionRunOutcomeUnknown {
		for _, effect := range step.UnresolvedEffects {
			step.Effects = append(step.Effects, Effect{ID: effect, State: "unresolved", Outcome: string(run.Outcome)})
		}
	}
	if run.Cancellation != nil && step.Error == "" {
		step.Error = "cancellation " + string(run.Cancellation.State)
	}
}

func convertCancellation(source *generated.Cancellation) *Cancellation {
	if source == nil {
		return nil
	}
	return &Cancellation{ID: source.Id.String(), State: string(source.State), RequestedAt: source.RequestedAt, EffectiveAt: cloneTime(source.EffectiveAt), Reason: stringValue(source.Reason)}
}

func cloneTime(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	copyValue := *value
	return &copyValue
}

func cloneInt(value *int) *int {
	if value == nil {
		return nil
	}
	copyValue := *value
	return &copyValue
}

func stringValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func stringSliceValue(value *[]string) []string {
	if value == nil {
		return nil
	}
	return *value
}
