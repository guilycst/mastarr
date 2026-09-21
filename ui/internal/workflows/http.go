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

const zeroUUID = "00000000-0000-0000-0000-000000000000"

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
	if err := rejectDuplicateKeys(body); err != nil {
		return err
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

func validGeneratedID(id generated.Id) bool { return id.String() != zeroUUID }

func (r *HTTPReader) convertWorkflow(ctx context.Context, source generated.WorkflowRun) (Workflow, error) {
	if !validGeneratedID(source.Id) || source.Name == "" || !source.State.Valid() {
		return Workflow{}, errors.New("workflow identity or state is incomplete")
	}
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
		if step.Id == "" || !step.State.Valid() || !validGeneratedID(step.ActionPlanId) {
			return Workflow{}, errors.New("workflow step identity or state is incomplete")
		}
		if step.ActionRunId != nil && !validGeneratedID(*step.ActionRunId) {
			return Workflow{}, errors.New("workflow action run identity is incomplete")
		}
		converted := Step{
			Sequence:     index + 1,
			ID:           step.Id,
			ActionPlanID: step.ActionPlanId.String(),
			ApprovalGate: stringValue(step.ApprovalGate),
			State:        string(step.State),
		}
		plan, err := r.getActionPlan(ctx, step.ActionPlanId)
		if err != nil {
			return Workflow{}, err
		}
		if err := mergeActionPlan(&converted, plan); err != nil {
			return Workflow{}, protocolError(err)
		}
		if step.ActionRunId != nil {
			converted.ActionRunID = step.ActionRunId.String()
			run, err := r.getActionRun(ctx, *step.ActionRunId, step.ActionPlanId, source.Id.String(), step.Id)
			if err != nil {
				return Workflow{}, err
			}
			if err := mergeActionRun(&converted, run); err != nil {
				return Workflow{}, protocolError(err)
			}
		}
		item.Steps = append(item.Steps, converted)
	}
	return item, nil
}

func (r *HTTPReader) getActionPlan(ctx context.Context, id generated.Id) (generated.ActionPlan, error) {
	if !validGeneratedID(id) {
		return generated.ActionPlan{}, protocolError(errors.New("action plan identity is incomplete"))
	}
	response, err := r.generated.GetActionPlanWithResponse(ctx, id)
	if err != nil {
		return generated.ActionPlan{}, classifyHTTPError(err, ctx)
	}
	if err := checkResponse(response, http.StatusOK); err != nil {
		return generated.ActionPlan{}, err
	}
	var source generated.ActionPlan
	if err := decodeStrict(response.GetBody(), &source); err != nil {
		return generated.ActionPlan{}, protocolError(err)
	}
	if !validGeneratedID(source.Id) || source.Id.String() != id.String() || source.Revision < 1 || source.Digest == "" || !source.Status.Valid() || !source.RequiredApproval.Valid() {
		return generated.ActionPlan{}, protocolError(errors.New("action plan identity mismatch"))
	}
	return source, nil
}

func (r *HTTPReader) getActionRun(ctx context.Context, id, expectedPlan generated.Id, expectedWorkflow, expectedStep string) (generated.ActionRun, error) {
	if !validGeneratedID(id) || !validGeneratedID(expectedPlan) || expectedWorkflow == "" || expectedWorkflow == zeroUUID || expectedStep == "" {
		return generated.ActionRun{}, protocolError(errors.New("action run binding is incomplete"))
	}
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
	if !validGeneratedID(source.Id) || !validGeneratedID(source.PlanId) || !source.State.Valid() || !source.Outcome.Valid() || source.Id.String() != id.String() || source.PlanId.String() != expectedPlan.String() || source.WorkflowRunId == nil || !validGeneratedID(*source.WorkflowRunId) || source.WorkflowRunId.String() != expectedWorkflow || source.StepId == nil || *source.StepId != expectedStep {
		return generated.ActionRun{}, protocolError(errors.New("action run identity does not match workflow step"))
	}
	return source, nil
}

func mergeActionPlan(step *Step, plan generated.ActionPlan) error {
	if step == nil || plan.Revision < 1 || plan.Digest == "" {
		return errors.New("action plan binding is incomplete")
	}
	kind, err := plan.Action.Discriminator()
	if err != nil || !validActionKind(kind) {
		return errors.New("action plan kind is unknown")
	}
	requiredApproval := string(plan.RequiredApproval)
	if requiredApproval != "review" && requiredApproval != "irreversible" {
		return errors.New("action plan approval is unknown")
	}
	if step.ApprovalGate != "" && step.ApprovalGate != requiredApproval {
		return errors.New("workflow approval gate differs from action plan")
	}
	step.ApprovalGate = requiredApproval
	step.PlanRevision = plan.Revision
	step.PlanDigest = plan.Digest
	step.ActionKind = kind
	step.Impacts = copyStrings(plan.Impacts)
	step.Capabilities = append([]string(nil), plan.Capabilities...)
	step.EstimatedBytes = cloneInt(plan.EstimatedBytes)
	step.ConnectionFences = copyMap(plan.ConnectionRevisions)
	step.MappingFences = copyMap(plan.MappingRevisions)
	step.Manifest = make([]ManifestEntry, 0, len(plan.Manifest))
	for _, entry := range plan.Manifest {
		step.Manifest = append(step.Manifest, ManifestEntry{
			RootID:       entry.RootId,
			RelativePath: entry.RelativePath,
			Type:         string(entry.Type),
			Role:         stringValueEnum(entry.Role),
			Size:         entry.Size,
			Identity:     stringValue(entry.FileIdentity),
			Digest:       stringValue(entry.Digest),
			ObservedAt:   cloneTime(entry.ObservedAt),
		})
	}
	step.ActionIrreversible = requiredApproval == "irreversible"
	return mergeActionInput(step, plan.Action, kind)
}

func mergeActionInput(step *Step, input generated.ActionInput, kind string) error {
	switch kind {
	case "arr.registration":
		item, err := input.AsArrRegistrationInput()
		if err != nil {
			return err
		}
		step.ActionConnection = item.ConnectionId
		step.ActionMediaKind = string(item.MediaKind)
		step.ActionProviderID = item.ProviderId
		step.ActionMonitoring = cloneBool(item.Fields.Monitored)
		step.ActionSeasonFolder = cloneBool(item.Fields.SeasonFolder)
		step.ActionSeriesType = stringValue(item.Fields.SeriesType)
		step.ActionQualityID = cloneInt(item.Fields.QualityProfileId)
		step.ActionRootFolder = stringValue(item.Fields.RootFolder)
		step.ActionSeasons = copyInts(item.Fields.Seasons)
	case "arr.import":
		item, err := input.AsArrImportInput()
		if err != nil {
			return err
		}
		step.ActionConnection = item.ConnectionId
		step.ActionExternalID = item.RegisteredExternalId
		step.ActionPreview = item.PreviewRevision
		step.ActionTransfer = string(item.Transfer)
		step.ActionFiles = convertImportFiles(item.Files)
	case "fs.copy":
		item, err := input.AsFsCopyInput()
		if err != nil {
			return err
		}
		step.ActionFiles = convertFileMaps(item.Files)
	case "fs.hardlink":
		item, err := input.AsFsHardlinkInput()
		if err != nil {
			return err
		}
		step.ActionFiles = convertFileMaps(item.Files)
	case "fs.move":
		item, err := input.AsFsMoveInput()
		if err != nil {
			return err
		}
		step.ActionExecutor = string(item.Executor)
		step.ActionFiles = convertFileMaps(item.Files)
	case "fs.rename":
		item, err := input.AsFsRenameInput()
		if err != nil {
			return err
		}
		step.ActionExecutor = string(item.Executor)
		step.ActionFiles = convertFileMaps(item.Files)
	case "client.stop":
		item, err := input.AsClientStopInput()
		if err != nil {
			return err
		}
		step.ActionConnection = item.ConnectionId
		step.ActionClientIDs = append([]string(nil), item.ClientItemIds...)
	case "client.remove":
		item, err := input.AsClientRemoveInput()
		if err != nil {
			return err
		}
		step.ActionConnection = item.ConnectionId
		step.ActionClientIDs = append([]string(nil), item.ClientItemIds...)
		retain := bool(item.RetainPayload)
		step.ActionRetainPayload = &retain
		step.ActionIrreversible = true
	case "fs.trash":
		item, err := input.AsFsTrashInput()
		if err != nil {
			return err
		}
		step.ActionRetention = &item.RetentionDays
		step.ActionStoppedIDs = append([]string(nil), stringSliceValue(item.StoppedClientIds)...)
		step.ActionFiles = convertTargets(item.Files)
	case "fs.restore":
		item, err := input.AsFsRestoreInput()
		if err != nil {
			return err
		}
		step.ActionTrashID = item.TrashId.String()
		step.ActionFiles = convertFileMaps(item.Files)
	case "fs.delete":
		item, err := input.AsFsDeleteInput()
		if err != nil {
			return err
		}
		step.ActionFiles = convertTargets(item.Files)
		permanent := bool(item.Permanent)
		step.ActionPermanent = &permanent
		step.ActionIrreversible = permanent || bool(item.IrreversibleAcknowledgement)
	case "descriptor.delete":
		item, err := input.AsDescriptorDeleteInput()
		if err != nil {
			return err
		}
		for _, id := range item.DescriptorIds {
			step.ActionFiles = append(step.ActionFiles, ActionFile{Identity: id.String()})
		}
		step.ActionIrreversible = bool(item.IrreversibleAcknowledgement)
	case "jellyfin.refresh":
		refresh, err := input.AsJellyfinRefreshInput()
		if err != nil {
			return err
		}
		item, itemErr := refresh.AsJellyfinItemRefreshInput()
		if itemErr == nil && string(item.Scope) == "item" && item.ItemId != "" {
			step.ActionConnection = item.ConnectionId
			step.ActionFiles = []ActionFile{{Identity: item.ItemId}}
			break
		}
		library, libraryErr := refresh.AsJellyfinLibraryRefreshInput()
		if libraryErr == nil && string(library.Scope) == "library" {
			step.ActionConnection = library.ConnectionId
			break
		}
		return errors.New("unknown refresh input")
	}
	return nil
}

func validActionKind(kind string) bool {
	switch kind {
	case "arr.registration", "arr.import", "fs.copy", "fs.hardlink", "fs.move", "fs.rename", "fs.trash", "fs.restore", "fs.delete", "client.stop", "client.remove", "descriptor.delete", "jellyfin.refresh":
		return true
	default:
		return false
	}
}

func convertImportFiles(files []generated.ImportFile) []ActionFile {
	result := make([]ActionFile, 0, len(files))
	for _, file := range files {
		result = append(result, ActionFile{
			SourceRootID:     file.Source.RootId,
			SourcePath:       file.Source.RelativePath,
			MovieOrEpisodeID: file.MovieOrEpisodeId,
			Subtitle:         cloneBool(file.Subtitle),
			Language:         stringValue(file.Language),
			Forced:           cloneBool(file.Forced),
			HearingImpaired:  cloneBool(file.HearingImpaired),
		})
	}
	return result
}

func convertFileMaps(files []generated.FileMap) []ActionFile {
	result := make([]ActionFile, 0, len(files))
	for _, file := range files {
		result = append(result, ActionFile{
			SourceRootID:      file.Source.RootId,
			SourcePath:        file.Source.RelativePath,
			DestinationRootID: file.Destination.RootId,
			DestinationPath:   file.Destination.RelativePath,
		})
	}
	return result
}

func convertTargets(files []generated.FileTarget) []ActionFile {
	result := make([]ActionFile, 0, len(files))
	for _, file := range files {
		result = append(result, ActionFile{SourceRootID: file.RootId, SourcePath: file.RelativePath})
	}
	return result
}

func mergeActionRun(step *Step, run generated.ActionRun) error {
	if err := validateEffectLists(run.Effects, run.UnresolvedEffects); err != nil {
		return err
	}
	if len(run.Effects) == 0 && len(run.UnresolvedEffects) == 0 && (run.Outcome == generated.ActionRunOutcomeApplied || run.Outcome == generated.ActionRunOutcomeAlreadySatisfied) {
		return errors.New("complete action run has no effect identity")
	}
	step.ActionRunID = run.Id.String()
	step.State = string(run.State)
	step.Outcome = string(run.Outcome)
	step.LastObservation = stringValue(run.LastObservation)
	step.LastObservedAt = nil
	step.RetryAt = cloneTime(run.NextAttemptAt)
	step.RetryReason = stringValue(run.RetryReason)
	step.UnresolvedEffects = append([]string(nil), run.UnresolvedEffects...)
	step.Effects = make([]Effect, 0, len(run.Effects))
	for _, effect := range run.Effects {
		state := "observed"
		if run.Outcome == generated.ActionRunOutcomeAlreadySatisfied {
			state = "already_satisfied"
		}
		step.Effects = append(step.Effects, Effect{ID: effect, State: state, Outcome: string(run.Outcome)})
	}
	if run.Cancellation != nil && step.Error == "" {
		step.Error = "cancellation " + string(run.Cancellation.State)
	}
	return nil
}

func validateEffectLists(effects, unresolved []string) error {
	seen := make(map[string]struct{}, len(effects)+len(unresolved))
	for _, effect := range effects {
		if !validEvidenceID(effect) {
			return errors.New("action run effect identity is empty or invalid")
		}
		if _, exists := seen[effect]; exists {
			return errors.New("action run effect identity is duplicated")
		}
		seen[effect] = struct{}{}
	}
	for _, effect := range unresolved {
		if !validEvidenceID(effect) {
			return errors.New("action run unresolved identity is empty or invalid")
		}
		if _, exists := seen[effect]; exists {
			return errors.New("action run effect is both observed and unresolved")
		}
		seen[effect] = struct{}{}
	}
	return nil
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

func cloneBool(value *bool) *bool {
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

func copyStrings(value *[]string) []string {
	return append([]string(nil), stringSliceValue(value)...)
}

func copyInts(value *[]int) []int {
	if value == nil {
		return nil
	}
	return append([]int(nil), (*value)...)
}

func copyMap(value *map[string]string) map[string]string {
	if value == nil {
		return nil
	}
	result := make(map[string]string, len(*value))
	for key, item := range *value {
		result[key] = item
	}
	return result
}

func stringValueEnum(value *generated.FileManifestEntryRole) string {
	if value == nil {
		return ""
	}
	return string(*value)
}
