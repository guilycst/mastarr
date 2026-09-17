package review

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

// HTTPReader adapts the generated UI client to the normalized review Reader.
// Generated request/response types are confined to this file.
type HTTPReader struct {
	generated *generated.ClientWithResponses
	timeout   time.Duration
}

var _ Reader = (*HTTPReader)(nil)

// NewHTTPReader builds a redirect-safe, bounded API reader.
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
		return nil, errors.New("create review API client failed")
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

var errResponseTooLarge = errors.New("review response too large")

func (r *HTTPReader) requestContext(parent context.Context) (context.Context, context.CancelFunc) {
	if parent == nil {
		parent = context.Background()
	}
	return context.WithTimeout(parent, r.timeout)
}

func (r *HTTPReader) ListReviews(parent context.Context, query PageRequest) (ReviewPage, error) {
	if r == nil || r.generated == nil {
		return ReviewPage{}, unavailableError(nil)
	}
	ctx, cancel := r.requestContext(parent)
	defer cancel()
	params := generated.ListActionPlansParams{}
	if query.Cursor != "" {
		cursor := generated.Cursor(query.Cursor)
		params.Cursor = &cursor
	}
	if query.Limit > 0 {
		limit := generated.Limit(query.Limit)
		params.Limit = &limit
	}
	response, err := r.generated.ListActionPlansWithResponse(ctx, &params)
	if err != nil {
		return ReviewPage{}, classifyHTTPError(err, ctx)
	}
	if err := checkResponse(response, http.StatusOK); err != nil {
		return ReviewPage{}, err
	}
	body := response.GetBody()
	var source generated.ActionPlanList
	if err := decodeStrict(body, &source); err != nil {
		return ReviewPage{}, protocolError(err)
	}
	items := make([]Review, 0, len(source.Items))
	for _, plan := range source.Items {
		item, convertErr := convertPlan(plan)
		if convertErr != nil {
			return ReviewPage{}, protocolError(convertErr)
		}
		items = append(items, item)
	}
	if len(items) > MaxScopeItems {
		return ReviewPage{}, protocolError(errors.New("review page too large"))
	}
	return ReviewPage{Items: items, Page: PageInfo{NextCursor: stringValue(source.Page.NextCursor), ObservedAt: source.Page.ObservedAt}}, nil
}

func (r *HTTPReader) GetReview(parent context.Context, id string) (Review, error) {
	if r == nil || r.generated == nil {
		return Review{}, unavailableError(nil)
	}
	parsed, err := parseUUID(id)
	if err != nil {
		return Review{}, &APIError{kind: ErrorNotFound}
	}
	ctx, cancel := r.requestContext(parent)
	defer cancel()
	response, err := r.generated.GetActionPlanWithResponse(ctx, parsed)
	if err != nil {
		return Review{}, classifyHTTPError(err, ctx)
	}
	if err := checkResponse(response, http.StatusOK); err != nil {
		return Review{}, err
	}
	body := response.GetBody()
	var source generated.ActionPlan
	if err := decodeStrict(body, &source); err != nil {
		return Review{}, protocolError(err)
	}
	item, err := convertPlan(source)
	if err != nil {
		return Review{}, protocolError(err)
	}
	if item.ID != id {
		return Review{}, protocolError(errors.New("review identity mismatch"))
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

func stringValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func convertPlan(source generated.ActionPlan) (Review, error) {
	action, err := convertAction(source.Action)
	if err != nil {
		return Review{}, err
	}
	item := Review{
		ID:               source.Id.String(),
		Revision:         source.Revision,
		Digest:           source.Digest,
		Status:           string(source.Status),
		RequiredApproval: string(source.RequiredApproval),
		CreatedAt:        cloneTime(source.CreatedAt),
		ExpiresAt:        cloneTime(source.ExpiresAt),
		Action:           action,
		Manifest:         make([]ManifestEntry, 0, len(source.Manifest)),
		Preconditions:    append([]string(nil), source.Preconditions...),
		Capabilities:     append([]string(nil), source.Capabilities...),
		Impacts:          copyStrings(source.Impacts),
		Conflicts:        copyStrings(source.Conflicts),
		BlockingIssues:   copyStrings(source.BlockingIssues),
		EstimatedBytes:   cloneInt(source.EstimatedBytes),
		ConnectionFences: copyMap(source.ConnectionRevisions),
		MappingFences:    copyMap(source.MappingRevisions),
	}
	for key := range source.DesiredState {
		item.DesiredStateKeys = append(item.DesiredStateKeys, key)
	}
	sortStrings(item.DesiredStateKeys)
	for _, entry := range source.Manifest {
		item.Manifest = append(item.Manifest, ManifestEntry{
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
	return item, nil
}

func convertAction(input generated.ActionInput) (Action, error) {
	kind, err := input.Discriminator()
	if err != nil {
		return Action{}, err
	}
	action := Action{Kind: kind}
	switch kind {
	case "arr.registration":
		item, err := input.AsArrRegistrationInput()
		if err != nil {
			return Action{}, err
		}
		action.ConnectionID = item.ConnectionId
		action.MediaKind = string(item.MediaKind)
		action.ProviderID = item.ProviderId
		action.Monitoring = cloneBool(item.Fields.Monitored)
		action.SeasonFolder = cloneBool(item.Fields.SeasonFolder)
		action.SeriesType = stringValue(item.Fields.SeriesType)
		action.QualityProfileID = cloneInt(item.Fields.QualityProfileId)
		action.RootFolder = stringValue(item.Fields.RootFolder)
		action.Seasons = copyInts(item.Fields.Seasons)
	case "arr.import":
		item, err := input.AsArrImportInput()
		if err != nil {
			return Action{}, err
		}
		action.ConnectionID = item.ConnectionId
		action.RegisteredExternalID = item.RegisteredExternalId
		action.PreviewRevision = item.PreviewRevision
		action.Transfer = string(item.Transfer)
		action.Files = convertImportFiles(item.Files)
	case "fs.copy":
		item, err := input.AsFsCopyInput()
		if err != nil {
			return Action{}, err
		}
		action.Files = convertFileMaps(item.Files)
	case "fs.hardlink":
		item, err := input.AsFsHardlinkInput()
		if err != nil {
			return Action{}, err
		}
		action.Files = convertFileMaps(item.Files)
	case "fs.move":
		item, err := input.AsFsMoveInput()
		if err != nil {
			return Action{}, err
		}
		action.Executor = string(item.Executor)
		action.Files = convertFileMaps(item.Files)
	case "fs.rename":
		item, err := input.AsFsRenameInput()
		if err != nil {
			return Action{}, err
		}
		action.Executor = string(item.Executor)
		action.Files = convertFileMaps(item.Files)
	case "client.stop":
		item, err := input.AsClientStopInput()
		if err != nil {
			return Action{}, err
		}
		action.ConnectionID = item.ConnectionId
		action.ClientItemIDs = append([]string(nil), item.ClientItemIds...)
	case "client.remove":
		item, err := input.AsClientRemoveInput()
		if err != nil {
			return Action{}, err
		}
		action.ConnectionID = item.ConnectionId
		action.ClientItemIDs = append([]string(nil), item.ClientItemIds...)
	case "fs.trash":
		item, err := input.AsFsTrashInput()
		if err != nil {
			return Action{}, err
		}
		action.RetentionDays = &item.RetentionDays
		action.StoppedClientIDs = append([]string(nil), stringSliceValue(item.StoppedClientIds)...)
		action.Files = convertTargets(item.Files)
		action.Irreversible = false
	case "fs.restore":
		item, err := input.AsFsRestoreInput()
		if err != nil {
			return Action{}, err
		}
		action.TrashID = item.TrashId.String()
		action.Files = convertFileMaps(item.Files)
	case "fs.delete":
		item, err := input.AsFsDeleteInput()
		if err != nil {
			return Action{}, err
		}
		action.Files = convertTargets(item.Files)
		action.Irreversible = bool(item.Permanent) || bool(item.IrreversibleAcknowledgement)
	case "descriptor.delete":
		item, err := input.AsDescriptorDeleteInput()
		if err != nil {
			return Action{}, err
		}
		for _, id := range item.DescriptorIds {
			action.Files = append(action.Files, ActionFile{Identity: id.String()})
		}
		action.Irreversible = bool(item.IrreversibleAcknowledgement)
	case "jellyfin.refresh":
		item, err := input.ValueByDiscriminator()
		if err != nil {
			return Action{}, err
		}
		switch typed := item.(type) {
		case generated.JellyfinLibraryRefreshInput:
			action.ConnectionID = typed.ConnectionId
		case generated.JellyfinItemRefreshInput:
			action.ConnectionID = typed.ConnectionId
			action.Files = []ActionFile{{Identity: typed.ItemId}}
		default:
			return Action{}, errors.New("unknown refresh input")
		}
	default:
		return Action{}, errors.New("unknown action kind")
	}
	return action, nil
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

func copyInts(value *[]int) []int {
	if value == nil {
		return nil
	}
	return append([]int(nil), (*value)...)
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

func sortStrings(value []string) {
	for i := 1; i < len(value); i++ {
		for j := i; j > 0 && value[j] < value[j-1]; j-- {
			value[j], value[j-1] = value[j-1], value[j]
		}
	}
}

func stringValueEnum(value *generated.FileManifestEntryRole) string {
	if value == nil {
		return ""
	}
	return string(*value)
}
