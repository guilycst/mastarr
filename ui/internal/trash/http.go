package trash

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

var errResponseTooLarge = errors.New("trash response too large")

// HTTPReader adapts the generated API client to the normalized trash Reader.
// The generated DTOs never cross this package's reader boundary.
type HTTPReader struct {
	generated *generated.ClientWithResponses
	timeout   time.Duration
}

var _ Reader = (*HTTPReader)(nil)

// NewHTTPReader creates a redirect-safe, bounded reader for the API origin.
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
		return nil, errors.New("create trash API client failed")
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
		value, parseErr := strconv.Atoi(port)
		if parseErr != nil || value < 0 || value > 65535 {
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
	hc.CheckRedirect = func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }
	transport := hc.Transport
	if transport == nil {
		transport = http.DefaultTransport
	}
	hc.Transport = boundedTransport{next: transport, max: int64(client.MaxResponseBytes)}
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

func (r *HTTPReader) requestContext(parent context.Context) (context.Context, context.CancelFunc) {
	if parent == nil {
		parent = context.Background()
	}
	return context.WithTimeout(parent, r.timeout)
}

func (r *HTTPReader) ListTrash(parent context.Context, query PageRequest) (TrashPage, error) {
	if r == nil || r.generated == nil {
		return TrashPage{}, unavailableError(nil)
	}
	ctx, cancel := r.requestContext(parent)
	defer cancel()
	response, err := r.generated.ListTrashWithResponse(ctx, trashParams(query))
	if err != nil {
		return TrashPage{}, classifyHTTPError(err, ctx)
	}
	if err := requireHTTPStatus(responseStatus(response), http.StatusOK); err != nil {
		return TrashPage{}, err
	}
	var source generated.TrashList
	body := response.GetBody()
	if err := decodeStrict(body, &source); err != nil {
		return TrashPage{}, protocolError(err)
	}
	if err := requireTrashListFields(body); err != nil {
		return TrashPage{}, protocolError(err)
	}
	return convertTrashPage(source)
}

func (r *HTTPReader) GetTrash(parent context.Context, id string) (TrashEntry, error) {
	if r == nil || r.generated == nil {
		return TrashEntry{}, unavailableError(nil)
	}
	if !validIdentity(id) {
		return TrashEntry{}, protocolError(errors.New("invalid trash identity"))
	}
	parsed, err := parseID(id)
	if err != nil {
		return TrashEntry{}, protocolError(errors.New("invalid trash identity"))
	}
	ctx, cancel := r.requestContext(parent)
	defer cancel()
	response, requestErr := r.generated.GetTrashWithResponse(ctx, generated.TrashId(parsed))
	if requestErr != nil {
		return TrashEntry{}, classifyHTTPError(requestErr, ctx)
	}
	if err := requireHTTPStatus(responseStatus(response), http.StatusOK); err != nil {
		return TrashEntry{}, err
	}
	body := response.GetBody()
	if err := decodeStrict(body, new(generated.TrashEntry)); err != nil {
		return TrashEntry{}, protocolError(err)
	}
	if err := requireTrashFields(body); err != nil {
		return TrashEntry{}, protocolError(err)
	}
	var source generated.TrashEntry
	if err := decodeStrict(body, &source); err != nil {
		return TrashEntry{}, protocolError(err)
	}
	item, convertErr := convertTrash(source)
	if convertErr != nil {
		return TrashEntry{}, convertErr
	}
	if item.ID != id {
		return TrashEntry{}, protocolError(errors.New("trash response identity does not match request"))
	}
	return item, nil
}

func trashParams(query PageRequest) *generated.ListTrashParams {
	params := &generated.ListTrashParams{}
	if query.Cursor != "" {
		cursor := generated.Cursor(query.Cursor)
		params.Cursor = &cursor
	}
	if query.Limit > 0 {
		limit := generated.Limit(query.Limit)
		params.Limit = &limit
	}
	return params
}

func parseID(raw string) (generated.Id, error) {
	var value generated.Id
	if len(raw) != 36 || raw[8] != '-' || raw[13] != '-' || raw[18] != '-' || raw[23] != '-' {
		return value, errors.New("invalid UUID")
	}
	decoded, err := hex.DecodeString(strings.ReplaceAll(raw, "-", ""))
	if err != nil || len(decoded) != len(value) {
		return value, errors.New("invalid UUID")
	}
	copy(value[:], decoded)
	return value, nil
}

func responseStatus(response interface{ StatusCode() int }) int {
	if response == nil {
		return 0
	}
	return response.StatusCode()
}

func requireHTTPStatus(got, want int) error {
	if got == want {
		return nil
	}
	if got == http.StatusNotFound {
		return &APIError{kind: ErrorNotFound, status: got}
	}
	if got == 0 {
		return protocolError(errors.New("missing HTTP response"))
	}
	return &APIError{kind: ErrorUnavailable, status: got}
}

func classifyHTTPError(err error, requestContext context.Context) error {
	if errors.Is(err, errResponseTooLarge) {
		return protocolError(err)
	}
	if errors.Is(err, context.Canceled) || (requestContext != nil && errors.Is(requestContext.Err(), context.Canceled)) {
		return &APIError{kind: ErrorCanceled, cause: context.Canceled}
	}
	if errors.Is(err, context.DeadlineExceeded) || (requestContext != nil && errors.Is(requestContext.Err(), context.DeadlineExceeded)) {
		return &APIError{kind: ErrorTimeout, cause: context.DeadlineExceeded}
	}
	var syntaxErr *json.SyntaxError
	var typeErr *json.UnmarshalTypeError
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) || errors.As(err, &syntaxErr) || errors.As(err, &typeErr) {
		return protocolError(err)
	}
	return unavailableError(err)
}

func unavailableError(cause error) error { return &APIError{kind: ErrorUnavailable, cause: cause} }
func protocolError(cause error) error    { return &APIError{kind: ErrorProtocol, cause: cause} }

func decodeStrict(body []byte, destination any) error {
	if len(body) == 0 || len(body) > client.MaxResponseBytes || !utf8.Valid(body) || !json.Valid(body) {
		return errors.New("response body is not valid JSON")
	}
	if err := rejectDuplicateKeys(body); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return errors.New("response contains trailing JSON")
		}
		return err
	}
	return nil
}

func rejectDuplicateKeys(body []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(body))
	if err := walkJSON(decoder, 0); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return errors.New("response contains trailing JSON")
	}
	return nil
}

const maxJSONDepth = 64

func walkJSON(decoder *json.Decoder, depth int) error {
	if depth > maxJSONDepth {
		return errors.New("response JSON is too deeply nested")
	}
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delimiter {
	case '{':
		keys := make(map[string]struct{})
		for decoder.More() {
			keyToken, keyErr := decoder.Token()
			if keyErr != nil {
				return keyErr
			}
			key, ok := keyToken.(string)
			if !ok {
				return errors.New("response object key is invalid")
			}
			if _, exists := keys[key]; exists {
				return errors.New("response contains duplicate object key")
			}
			keys[key] = struct{}{}
			if err := walkJSON(decoder, depth+1); err != nil {
				return err
			}
		}
		end, endErr := decoder.Token()
		if endErr != nil || end != json.Delim('}') {
			return errors.New("response object is unterminated")
		}
	case '[':
		for decoder.More() {
			if err := walkJSON(decoder, depth+1); err != nil {
				return err
			}
		}
		end, endErr := decoder.Token()
		if endErr != nil || end != json.Delim(']') {
			return errors.New("response array is unterminated")
		}
	default:
		return errors.New("response JSON delimiter is invalid")
	}
	return nil
}

func requiredObjectFields(body []byte, allowNull bool, fields ...string) error {
	var object map[string]json.RawMessage
	decoder := json.NewDecoder(bytes.NewReader(body))
	if err := decoder.Decode(&object); err != nil || object == nil {
		return errors.New("response object is missing")
	}
	for _, field := range fields {
		value, ok := object[field]
		if !ok || (!allowNull && bytes.Equal(bytes.TrimSpace(value), []byte("null"))) {
			return errors.New("response required field is missing")
		}
	}
	return nil
}

func rawArrayField(body []byte, name string) ([]json.RawMessage, error) {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(body, &object); err != nil || object == nil {
		return nil, errors.New("response object is missing")
	}
	value, ok := object[name]
	if !ok || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
		return nil, errors.New("response array is missing")
	}
	var items []json.RawMessage
	if err := json.Unmarshal(value, &items); err != nil || items == nil {
		return nil, errors.New("response array is invalid")
	}
	return items, nil
}

func requirePageFields(body []byte) error {
	object, err := rawObjectField(body, "page")
	if err != nil {
		return err
	}
	if err := requiredObjectFields(object, true, "nextCursor", "coverage", "observedAt"); err != nil {
		return err
	}
	coverage, err := rawArrayField(object, "coverage")
	if err != nil {
		return err
	}
	for _, value := range coverage {
		if err := requiredObjectFields(value, false, "completeness", "observedAt"); err != nil {
			return err
		}
	}
	return nil
}

func rawObjectField(body []byte, name string) ([]byte, error) {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(body, &object); err != nil || object == nil {
		return nil, errors.New("response object is missing")
	}
	value, ok := object[name]
	if !ok || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
		return nil, errors.New("response object field is missing")
	}
	var nested map[string]json.RawMessage
	if err := json.Unmarshal(value, &nested); err != nil || nested == nil {
		return nil, errors.New("response object field is invalid")
	}
	return value, nil
}

func requireTrashListFields(body []byte) error {
	if err := requiredObjectFields(body, false, "items", "page"); err != nil {
		return err
	}
	items, err := rawArrayField(body, "items")
	if err != nil {
		return err
	}
	for _, value := range items {
		if err := requireTrashFields(value); err != nil {
			return err
		}
	}
	return requirePageFields(body)
}

func requireTrashFields(body []byte) error {
	if err := requiredObjectFields(body, false, "id", "files", "originalPaths", "expiresAt", "state"); err != nil {
		return err
	}
	files, err := rawArrayField(body, "files")
	if err != nil {
		return err
	}
	for _, value := range files {
		if err := requiredObjectFields(value, false, "rootId", "relativePath", "type", "size"); err != nil {
			return err
		}
	}
	paths, err := rawArrayField(body, "originalPaths")
	if err != nil {
		return err
	}
	for _, value := range paths {
		if err := requiredObjectFields(value, false, "rootId", "relativePath"); err != nil {
			return err
		}
	}
	return nil
}

func convertTrashPage(source generated.TrashList) (TrashPage, error) {
	if source.Items == nil || len(source.Items) > MaxItemsPerPage {
		return TrashPage{}, protocolError(errors.New("trash items are missing or too large"))
	}
	page, err := convertPage(source.Page)
	if err != nil {
		return TrashPage{}, err
	}
	items := make([]TrashEntry, 0, len(source.Items))
	seen := make(map[string]struct{}, len(source.Items))
	for _, value := range source.Items {
		item, convertErr := convertTrash(value)
		if convertErr != nil {
			return TrashPage{}, convertErr
		}
		if _, exists := seen[item.ID]; exists {
			return TrashPage{}, protocolError(errors.New("trash page contains duplicate identity"))
		}
		seen[item.ID] = struct{}{}
		items = append(items, item)
	}
	return TrashPage{Items: items, Page: page}, nil
}

func convertTrash(source generated.TrashEntry) (TrashEntry, error) {
	id := source.Id.String()
	if !validIdentity(id) || !knownTrashState(string(source.State)) || source.ExpiresAt.IsZero() || source.Files == nil || source.OriginalPaths == nil || len(source.Files) > MaxFilesPerEntry {
		return TrashEntry{}, protocolError(errors.New("trash required evidence is missing"))
	}
	item := TrashEntry{
		ID:                 id,
		State:              string(source.State),
		ExpiresAt:          source.ExpiresAt,
		Files:              make([]ManifestEntry, 0, len(source.Files)),
		OriginalPaths:      make([]FileTarget, 0, len(source.OriginalPaths)),
		Capabilities:       copyStrings(source.Capabilities),
		ClientAssociations: copyStrings(source.ClientAssociations),
		Holds:              copyStrings(source.Holds),
	}
	for _, target := range source.OriginalPaths {
		if !validConfigID(target.RootId) || !validRelativePath(target.RelativePath) {
			return TrashEntry{}, protocolError(errors.New("trash original target is invalid"))
		}
		item.OriginalPaths = append(item.OriginalPaths, FileTarget{RootID: target.RootId, RelativePath: target.RelativePath})
	}
	for _, file := range source.Files {
		if !validConfigID(file.RootId) || !validRelativePath(file.RelativePath) || !knownFileType(string(file.Type)) || file.Size < 0 {
			return TrashEntry{}, protocolError(errors.New("trash manifest entry is invalid"))
		}
		entry := ManifestEntry{RootID: file.RootId, RelativePath: file.RelativePath, Type: string(file.Type), Size: file.Size, Identity: optionalString(file.FileIdentity), Digest: optionalString(file.Digest), ObservedAt: copyTime(file.ObservedAt)}
		if file.Role != nil {
			entry.Role = string(*file.Role)
		}
		if !validBounded(entry.Identity, 128, true) || !validBounded(entry.Digest, 256, true) || !validBounded(entry.Role, 64, true) {
			return TrashEntry{}, protocolError(errors.New("trash manifest identity is invalid"))
		}
		item.Files = append(item.Files, entry)
	}
	if !validTrashEntry(item) {
		return TrashEntry{}, protocolError(errors.New("trash entry is invalid"))
	}
	return item, nil
}

func convertPage(source generated.Page) (PageInfo, error) {
	if source.ObservedAt.IsZero() {
		return PageInfo{}, protocolError(errors.New("trash page observation is missing"))
	}
	page := PageInfo{ObservedAt: source.ObservedAt}
	if source.NextCursor != nil {
		if *source.NextCursor == "" || len(*source.NextCursor) > MaxCursorLength || strings.TrimSpace(*source.NextCursor) != *source.NextCursor {
			return PageInfo{}, protocolError(errors.New("trash page cursor is invalid"))
		}
		page.NextCursor = *source.NextCursor
	}
	page.Coverage = make([]Coverage, 0, len(source.Coverage))
	for _, value := range source.Coverage {
		if value.ObservedAt.IsZero() || !knownCompleteness(string(value.Completeness)) {
			return PageInfo{}, protocolError(errors.New("trash coverage is incomplete"))
		}
		coverage := Coverage{Completeness: string(value.Completeness), ObservedAt: value.ObservedAt, StartedAt: copyTime(value.StartedAt), CompletedAt: copyTime(value.CompletedAt), ObservedCount: copyInt(value.ObservedCount), ReasonCodes: copyStrings(value.ReasonCodes)}
		if value.ConnectionId != nil {
			if !validConfigID(*value.ConnectionId) {
				return PageInfo{}, protocolError(errors.New("trash coverage connection identity is invalid"))
			}
			coverage.ConnectionID = *value.ConnectionId
		}
		if value.RootId != nil {
			if !validConfigID(*value.RootId) {
				return PageInfo{}, protocolError(errors.New("trash coverage root identity is invalid"))
			}
			coverage.RootID = *value.RootId
		}
		if value.SourceId != nil {
			coverage.SourceID = value.SourceId.String()
		}
		if value.SnapshotRevision != nil {
			coverage.SnapshotRevision = *value.SnapshotRevision
		}
		page.Coverage = append(page.Coverage, coverage)
	}
	return page, nil
}

func copyStrings(value *[]string) []string {
	if value == nil {
		return nil
	}
	return append([]string(nil), (*value)...)
}

func copyTime(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	copyValue := *value
	return &copyValue
}

func copyInt(value *int) *int {
	if value == nil {
		return nil
	}
	copyValue := *value
	return &copyValue
}

func optionalString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
