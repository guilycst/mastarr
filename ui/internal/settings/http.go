package settings

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

var errResponseTooLarge = errors.New("settings response too large")

// HTTPReader adapts the generated API client to normalized settings models.
type HTTPReader struct {
	generated *generated.ClientWithResponses
	timeout   time.Duration
}

var _ Reader = (*HTTPReader)(nil)

// NewHTTPReader creates a redirect-safe, bounded settings reader.
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
		return nil, errors.New("create settings API client failed")
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

func (r *HTTPReader) GetConfiguration(parent context.Context) (Configuration, error) {
	if r == nil || r.generated == nil {
		return Configuration{}, unavailableError(nil)
	}
	ctx, cancel := r.requestContext(parent)
	defer cancel()
	response, err := r.generated.GetConfigurationWithResponse(ctx)
	if err != nil {
		return Configuration{}, classifyHTTPError(err, ctx)
	}
	if err := requireHTTPStatus(responseStatus(response), http.StatusOK); err != nil {
		return Configuration{}, err
	}
	body := response.GetBody()
	var source generated.Configuration
	if err := decodeStrict(body, &source); err != nil {
		return Configuration{}, protocolError(err)
	}
	if err := requireConfigurationFields(body); err != nil {
		return Configuration{}, protocolError(err)
	}
	value, convertErr := convertConfiguration(source)
	if convertErr != nil {
		return Configuration{}, convertErr
	}
	value.ETag = headerValue(response.HTTPResponse, "ETag")
	return value, nil
}

func (r *HTTPReader) ListConnections(parent context.Context, query PageRequest) (ConnectionPage, error) {
	if r == nil || r.generated == nil {
		return ConnectionPage{}, unavailableError(nil)
	}
	ctx, cancel := r.requestContext(parent)
	defer cancel()
	response, err := r.generated.ListConnectionsWithResponse(ctx, listConnectionsParams(query))
	if err != nil {
		return ConnectionPage{}, classifyHTTPError(err, ctx)
	}
	if err := requireHTTPStatus(responseStatus(response), http.StatusOK); err != nil {
		return ConnectionPage{}, err
	}
	body := response.GetBody()
	var source generated.ConnectionList
	if err := decodeStrict(body, &source); err != nil {
		return ConnectionPage{}, protocolError(err)
	}
	if err := requireConnectionListFields(body); err != nil {
		return ConnectionPage{}, protocolError(err)
	}
	return convertConnectionPage(source)
}

func (r *HTTPReader) GetConnection(parent context.Context, id string) (Connection, error) {
	if r == nil || r.generated == nil {
		return Connection{}, unavailableError(nil)
	}
	if !validConfigID(id) {
		return Connection{}, protocolError(errors.New("invalid connection identity"))
	}
	ctx, cancel := r.requestContext(parent)
	defer cancel()
	response, err := r.generated.GetConnectionWithResponse(ctx, generated.ConnectionId(id))
	if err != nil {
		return Connection{}, classifyHTTPError(err, ctx)
	}
	if err := requireHTTPStatus(responseStatus(response), http.StatusOK); err != nil {
		return Connection{}, err
	}
	body := response.GetBody()
	var source generated.Connection
	if err := decodeStrict(body, &source); err != nil {
		return Connection{}, protocolError(err)
	}
	if err := requireConnectionFields(body); err != nil {
		return Connection{}, protocolError(err)
	}
	value, convertErr := convertConnection(source)
	if convertErr != nil {
		return Connection{}, convertErr
	}
	if value.ID != id {
		return Connection{}, protocolError(errors.New("connection response identity does not match request"))
	}
	value.ETag = headerValue(response.HTTPResponse, "ETag")
	return value, nil
}

func (r *HTTPReader) ListStorageRoots(parent context.Context, query PageRequest) (StorageRootPage, error) {
	if r == nil || r.generated == nil {
		return StorageRootPage{}, unavailableError(nil)
	}
	ctx, cancel := r.requestContext(parent)
	defer cancel()
	response, err := r.generated.ListStorageRootsWithResponse(ctx, listStorageRootsParams(query))
	if err != nil {
		return StorageRootPage{}, classifyHTTPError(err, ctx)
	}
	if err := requireHTTPStatus(responseStatus(response), http.StatusOK); err != nil {
		return StorageRootPage{}, err
	}
	body := response.GetBody()
	var source generated.StorageRootList
	if err := decodeStrict(body, &source); err != nil {
		return StorageRootPage{}, protocolError(err)
	}
	if err := requireStorageRootListFields(body); err != nil {
		return StorageRootPage{}, protocolError(err)
	}
	return convertStorageRootPage(source)
}

func (r *HTTPReader) GetStorageRoot(parent context.Context, id string) (StorageRoot, error) {
	if r == nil || r.generated == nil {
		return StorageRoot{}, unavailableError(nil)
	}
	if !validConfigID(id) {
		return StorageRoot{}, protocolError(errors.New("invalid storage root identity"))
	}
	ctx, cancel := r.requestContext(parent)
	defer cancel()
	response, err := r.generated.GetStorageRootWithResponse(ctx, generated.StorageRootId(id))
	if err != nil {
		return StorageRoot{}, classifyHTTPError(err, ctx)
	}
	if err := requireHTTPStatus(responseStatus(response), http.StatusOK); err != nil {
		return StorageRoot{}, err
	}
	body := response.GetBody()
	var source generated.StorageRoot
	if err := decodeStrict(body, &source); err != nil {
		return StorageRoot{}, protocolError(err)
	}
	if err := requireStorageRootFields(body); err != nil {
		return StorageRoot{}, protocolError(err)
	}
	value, convertErr := convertStorageRoot(source)
	if convertErr != nil {
		return StorageRoot{}, convertErr
	}
	if value.ID != id {
		return StorageRoot{}, protocolError(errors.New("storage root response identity does not match request"))
	}
	value.ETag = headerValue(response.HTTPResponse, "ETag")
	return value, nil
}

func (r *HTTPReader) ListPathMappings(parent context.Context, query PageRequest) (PathMappingPage, error) {
	if r == nil || r.generated == nil {
		return PathMappingPage{}, unavailableError(nil)
	}
	ctx, cancel := r.requestContext(parent)
	defer cancel()
	response, err := r.generated.ListPathMappingsWithResponse(ctx, listPathMappingsParams(query))
	if err != nil {
		return PathMappingPage{}, classifyHTTPError(err, ctx)
	}
	if err := requireHTTPStatus(responseStatus(response), http.StatusOK); err != nil {
		return PathMappingPage{}, err
	}
	body := response.GetBody()
	var source generated.PathMappingList
	if err := decodeStrict(body, &source); err != nil {
		return PathMappingPage{}, protocolError(err)
	}
	if err := requirePathMappingListFields(body); err != nil {
		return PathMappingPage{}, protocolError(err)
	}
	return convertPathMappingPage(source)
}

func (r *HTTPReader) GetPathMapping(parent context.Context, id string) (PathMapping, error) {
	if r == nil || r.generated == nil {
		return PathMapping{}, unavailableError(nil)
	}
	if !validConfigID(id) {
		return PathMapping{}, protocolError(errors.New("invalid path mapping identity"))
	}
	ctx, cancel := r.requestContext(parent)
	defer cancel()
	response, err := r.generated.GetPathMappingWithResponse(ctx, generated.PathMappingId(id))
	if err != nil {
		return PathMapping{}, classifyHTTPError(err, ctx)
	}
	if err := requireHTTPStatus(responseStatus(response), http.StatusOK); err != nil {
		return PathMapping{}, err
	}
	body := response.GetBody()
	var source generated.PathMapping
	if err := decodeStrict(body, &source); err != nil {
		return PathMapping{}, protocolError(err)
	}
	if err := requirePathMappingFields(body); err != nil {
		return PathMapping{}, protocolError(err)
	}
	value, convertErr := convertPathMapping(source)
	if convertErr != nil {
		return PathMapping{}, convertErr
	}
	if value.ID != id {
		return PathMapping{}, protocolError(errors.New("path mapping response identity does not match request"))
	}
	value.ETag = headerValue(response.HTTPResponse, "ETag")
	return value, nil
}

func (r *HTTPReader) ListConnectionChecks(parent context.Context, query PageRequest) (ConnectionCheckPage, error) {
	if r == nil || r.generated == nil {
		return ConnectionCheckPage{}, unavailableError(nil)
	}
	ctx, cancel := r.requestContext(parent)
	defer cancel()
	response, err := r.generated.ListConnectionChecksWithResponse(ctx, listConnectionChecksParams(query))
	if err != nil {
		return ConnectionCheckPage{}, classifyHTTPError(err, ctx)
	}
	if err := requireHTTPStatus(responseStatus(response), http.StatusOK); err != nil {
		return ConnectionCheckPage{}, err
	}
	body := response.GetBody()
	var source generated.ConnectionCheckList
	if err := decodeStrict(body, &source); err != nil {
		return ConnectionCheckPage{}, protocolError(err)
	}
	if err := requireConnectionCheckListFields(body); err != nil {
		return ConnectionCheckPage{}, protocolError(err)
	}
	return convertConnectionCheckPage(source)
}

func (r *HTTPReader) GetConnectionCheck(parent context.Context, id string) (ConnectionCheck, error) {
	if r == nil || r.generated == nil {
		return ConnectionCheck{}, unavailableError(nil)
	}
	if !validIdentity(id) {
		return ConnectionCheck{}, protocolError(errors.New("invalid connection check identity"))
	}
	parsed, err := parseID(id)
	if err != nil {
		return ConnectionCheck{}, protocolError(errors.New("invalid connection check identity"))
	}
	ctx, cancel := r.requestContext(parent)
	defer cancel()
	response, requestErr := r.generated.GetConnectionCheckWithResponse(ctx, generated.ConnectionCheckId(parsed))
	if requestErr != nil {
		return ConnectionCheck{}, classifyHTTPError(requestErr, ctx)
	}
	if err := requireHTTPStatus(responseStatus(response), http.StatusOK); err != nil {
		return ConnectionCheck{}, err
	}
	body := response.GetBody()
	var source generated.ConnectionCheck
	if err := decodeStrict(body, &source); err != nil {
		return ConnectionCheck{}, protocolError(err)
	}
	if err := requireConnectionCheckFields(body); err != nil {
		return ConnectionCheck{}, protocolError(err)
	}
	value, convertErr := convertConnectionCheck(source)
	if convertErr != nil {
		return ConnectionCheck{}, convertErr
	}
	if value.ID != id {
		return ConnectionCheck{}, protocolError(errors.New("connection check response identity does not match request"))
	}
	value.ETag = headerValue(response.HTTPResponse, "ETag")
	return value, nil
}

func listConnectionsParams(query PageRequest) *generated.ListConnectionsParams {
	params := &generated.ListConnectionsParams{}
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

func listStorageRootsParams(query PageRequest) *generated.ListStorageRootsParams {
	params := &generated.ListStorageRootsParams{}
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

func listPathMappingsParams(query PageRequest) *generated.ListPathMappingsParams {
	params := &generated.ListPathMappingsParams{}
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

func listConnectionChecksParams(query PageRequest) *generated.ListConnectionChecksParams {
	params := &generated.ListConnectionChecksParams{}
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

func responseStatus(response interface{ StatusCode() int }) int {
	if response == nil {
		return 0
	}
	return response.StatusCode()
}

func headerValue(response *http.Response, name string) string {
	if response == nil {
		return ""
	}
	return response.Header.Get(name)
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
	page, err := rawObjectField(body, "page")
	if err != nil {
		return err
	}
	if err := requiredObjectFields(page, true, "nextCursor", "coverage", "observedAt"); err != nil {
		return err
	}
	coverage, err := rawArrayField(page, "coverage")
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

func requireListFields(body []byte, itemFields ...string) error {
	if err := requiredObjectFields(body, false, "items", "page"); err != nil {
		return err
	}
	items, err := rawArrayField(body, "items")
	if err != nil {
		return err
	}
	for _, item := range items {
		if err := requiredObjectFields(item, false, itemFields...); err != nil {
			return err
		}
	}
	return requirePageFields(body)
}

func requireConfigurationFields(body []byte) error {
	if err := requiredObjectFields(body, false, "source", "connections", "storageRoots", "pathMappings", "keySource"); err != nil {
		return err
	}
	if err := requireSourceFieldsFromParent(body, "source"); err != nil {
		return err
	}
	for _, field := range []string{"connections", "storageRoots", "pathMappings"} {
		items, err := rawArrayField(body, field)
		if err != nil {
			return err
		}
		for _, item := range items {
			var required []string
			switch field {
			case "connections":
				required = []string{"id", "kind", "label", "endpoint", "source", "revision", "health", "credentialState"}
			case "storageRoots":
				required = []string{"id", "label", "purpose", "path", "source", "revision", "capabilities", "watch"}
			case "pathMappings":
				required = []string{"id", "connectionId", "sourcePrefix", "rootId", "destinationPrefix", "source", "revision"}
			}
			if err := requiredObjectFields(item, false, required...); err != nil {
				return err
			}
			if err := requireSourceFieldsFromParent(item, "source"); err != nil {
				return err
			}
			if field == "storageRoots" {
				watch, watchErr := rawObjectField(item, "watch")
				if watchErr != nil || requiredObjectFields(watch, false, "enabled", "intervalSeconds") != nil {
					return errors.New("storage root watch is incomplete")
				}
			}
		}
	}
	return nil
}

func requireSourceFieldsFromParent(body []byte, field string) error {
	nested, err := rawObjectField(body, field)
	if err != nil {
		return err
	}
	return requiredObjectFields(nested, false, "source", "editable", "documentId", "revision", "startupAt", "reloadPolicy")
}

func requireConnectionFields(body []byte) error {
	if err := requiredObjectFields(body, false, "id", "kind", "label", "endpoint", "source", "revision", "health", "credentialState"); err != nil {
		return err
	}
	return requireSourceFieldsFromParent(body, "source")
}

func requireConnectionListFields(body []byte) error {
	return requireListFields(body, "id", "kind", "label", "endpoint", "source", "revision", "health", "credentialState")
}

func requireStorageRootFields(body []byte) error {
	if err := requiredObjectFields(body, false, "id", "label", "purpose", "path", "source", "revision", "capabilities", "watch"); err != nil {
		return err
	}
	if err := requireSourceFieldsFromParent(body, "source"); err != nil {
		return err
	}
	watch, err := rawObjectField(body, "watch")
	if err != nil {
		return err
	}
	return requiredObjectFields(watch, false, "enabled", "intervalSeconds")
}

func requireStorageRootListFields(body []byte) error {
	return requireListFields(body, "id", "label", "purpose", "path", "source", "revision", "capabilities", "watch")
}

func requirePathMappingFields(body []byte) error {
	if err := requiredObjectFields(body, false, "id", "connectionId", "sourcePrefix", "rootId", "destinationPrefix", "source", "revision"); err != nil {
		return err
	}
	return requireSourceFieldsFromParent(body, "source")
}

func requirePathMappingListFields(body []byte) error {
	return requireListFields(body, "id", "connectionId", "sourcePrefix", "rootId", "destinationPrefix", "source", "revision")
}

func requireConnectionCheckFields(body []byte) error {
	return requiredObjectFields(body, false, "id", "connectionId", "state", "createdAt")
}

func requireConnectionCheckListFields(body []byte) error {
	return requireListFields(body, "id", "connectionId", "state", "createdAt")
}

func convertConfiguration(source generated.Configuration) (Configuration, error) {
	metadata, err := convertSource(source.Source)
	if err != nil {
		return Configuration{}, err
	}
	if !knownKeySource(string(source.KeySource)) || source.Connections == nil || source.StorageRoots == nil || source.PathMappings == nil {
		return Configuration{}, protocolError(errors.New("configuration evidence is incomplete"))
	}
	value := Configuration{Source: metadata, KeySource: string(source.KeySource), RestartRequired: copyBool(source.RestartRequired), Connections: make([]Connection, 0, len(source.Connections)), StorageRoots: make([]StorageRoot, 0, len(source.StorageRoots)), PathMappings: make([]PathMapping, 0, len(source.PathMappings))}
	for _, item := range source.Connections {
		converted, convertErr := convertConnection(item)
		if convertErr != nil {
			return Configuration{}, convertErr
		}
		value.Connections = append(value.Connections, converted)
	}
	for _, item := range source.StorageRoots {
		converted, convertErr := convertStorageRoot(item)
		if convertErr != nil {
			return Configuration{}, convertErr
		}
		value.StorageRoots = append(value.StorageRoots, converted)
	}
	for _, item := range source.PathMappings {
		converted, convertErr := convertPathMapping(item)
		if convertErr != nil {
			return Configuration{}, convertErr
		}
		value.PathMappings = append(value.PathMappings, converted)
	}
	if validateConfiguration(value) != nil {
		return Configuration{}, protocolError(errors.New("configuration evidence is invalid"))
	}
	return value, nil
}

func convertConnectionPage(source generated.ConnectionList) (ConnectionPage, error) {
	if source.Items == nil || len(source.Items) > MaxItemsPerPage {
		return ConnectionPage{}, protocolError(errors.New("connections are missing or too large"))
	}
	page, err := convertPage(source.Page)
	if err != nil {
		return ConnectionPage{}, err
	}
	items := make([]Connection, 0, len(source.Items))
	seen := map[string]struct{}{}
	for _, item := range source.Items {
		converted, convertErr := convertConnection(item)
		if convertErr != nil {
			return ConnectionPage{}, convertErr
		}
		if _, exists := seen[converted.ID]; exists {
			return ConnectionPage{}, protocolError(errors.New("connections contain duplicate identity"))
		}
		seen[converted.ID] = struct{}{}
		items = append(items, converted)
	}
	return ConnectionPage{Items: items, Page: page}, nil
}

func convertConnection(source generated.Connection) (Connection, error) {
	metadata, err := convertSource(source.Source)
	if err != nil {
		return Connection{}, err
	}
	endpoint, err := redactEndpoint(source.Endpoint)
	if err != nil || !validConfigID(source.Id) || !knownKind(string(source.Kind)) || !validBounded(source.Label, 256, false) || !knownHealth(string(source.Health)) || !knownCredentialState(string(source.CredentialState)) || !validBounded(source.Revision, 256, false) {
		return Connection{}, protocolError(errors.New("connection evidence is invalid"))
	}
	value := Connection{ID: source.Id, Kind: string(source.Kind), Label: source.Label, Endpoint: endpoint, Health: string(source.Health), CredentialState: string(source.CredentialState), ObservedVersion: optionalString(source.ObservedVersion), Capabilities: copyStrings(source.Capabilities), RetiredAt: copyTime(source.RetiredAt), Revision: source.Revision, Source: metadata}
	if !validBounded(value.ObservedVersion, 256, true) || len(value.Capabilities) > MaxCapabilities {
		return Connection{}, protocolError(errors.New("connection metadata is invalid"))
	}
	return value, nil
}

func convertStorageRootPage(source generated.StorageRootList) (StorageRootPage, error) {
	if source.Items == nil || len(source.Items) > MaxItemsPerPage {
		return StorageRootPage{}, protocolError(errors.New("storage roots are missing or too large"))
	}
	page, err := convertPage(source.Page)
	if err != nil {
		return StorageRootPage{}, err
	}
	items := make([]StorageRoot, 0, len(source.Items))
	seen := map[string]struct{}{}
	for _, item := range source.Items {
		converted, convertErr := convertStorageRoot(item)
		if convertErr != nil {
			return StorageRootPage{}, convertErr
		}
		if _, exists := seen[converted.ID]; exists {
			return StorageRootPage{}, protocolError(errors.New("storage roots contain duplicate identity"))
		}
		seen[converted.ID] = struct{}{}
		items = append(items, converted)
	}
	return StorageRootPage{Items: items, Page: page}, nil
}

func convertStorageRoot(source generated.StorageRoot) (StorageRoot, error) {
	metadata, err := convertSource(source.Source)
	if err != nil || !validConfigID(source.Id) || !validBounded(source.Label, 256, false) || !knownPurpose(string(source.Purpose)) || !validBounded(source.Path, 4096, false) || !validBounded(source.Revision, 256, false) || len(source.Capabilities) > MaxCapabilities || source.Watch.IntervalSeconds <= 0 {
		return StorageRoot{}, protocolError(errors.New("storage root evidence is invalid"))
	}
	permission := ""
	if source.Permission != nil {
		permission = string(*source.Permission)
		if !validPermission(permission) {
			return StorageRoot{}, protocolError(errors.New("storage root permission is invalid"))
		}
	}
	value := StorageRoot{ID: source.Id, Label: source.Label, Purpose: string(source.Purpose), Path: source.Path, Permission: permission, Capabilities: copyStringsValue(source.Capabilities), WatchEnabled: source.Watch.Enabled, WatchIntervalSeconds: source.Watch.IntervalSeconds, RetiredAt: copyTime(source.RetiredAt), Revision: source.Revision, Source: metadata}
	if !validateStorageRoot(value) {
		return StorageRoot{}, protocolError(errors.New("storage root evidence is invalid"))
	}
	return value, nil
}

func convertPathMappingPage(source generated.PathMappingList) (PathMappingPage, error) {
	if source.Items == nil || len(source.Items) > MaxItemsPerPage {
		return PathMappingPage{}, protocolError(errors.New("path mappings are missing or too large"))
	}
	page, err := convertPage(source.Page)
	if err != nil {
		return PathMappingPage{}, err
	}
	items := make([]PathMapping, 0, len(source.Items))
	seen := map[string]struct{}{}
	for _, item := range source.Items {
		converted, convertErr := convertPathMapping(item)
		if convertErr != nil {
			return PathMappingPage{}, convertErr
		}
		if _, exists := seen[converted.ID]; exists {
			return PathMappingPage{}, protocolError(errors.New("path mappings contain duplicate identity"))
		}
		seen[converted.ID] = struct{}{}
		items = append(items, converted)
	}
	return PathMappingPage{Items: items, Page: page}, nil
}

func convertPathMapping(source generated.PathMapping) (PathMapping, error) {
	metadata, err := convertSource(source.Source)
	if err != nil || !validConfigID(source.Id) || !validConfigID(source.ConnectionId) || !validConfigID(source.RootId) || !validBounded(source.SourcePrefix, 4096, false) || !validBounded(source.DestinationPrefix, 4096, false) || !validBounded(source.Revision, 256, false) {
		return PathMapping{}, protocolError(errors.New("path mapping evidence is invalid"))
	}
	return PathMapping{ID: source.Id, ConnectionID: source.ConnectionId, RootID: source.RootId, SourcePrefix: source.SourcePrefix, DestinationPrefix: source.DestinationPrefix, Revision: source.Revision, Source: metadata}, nil
}

func convertConnectionCheckPage(source generated.ConnectionCheckList) (ConnectionCheckPage, error) {
	if source.Items == nil || len(source.Items) > MaxItemsPerPage {
		return ConnectionCheckPage{}, protocolError(errors.New("connection checks are missing or too large"))
	}
	page, err := convertPage(source.Page)
	if err != nil {
		return ConnectionCheckPage{}, err
	}
	items := make([]ConnectionCheck, 0, len(source.Items))
	seen := map[string]struct{}{}
	for _, item := range source.Items {
		converted, convertErr := convertConnectionCheck(item)
		if convertErr != nil {
			return ConnectionCheckPage{}, convertErr
		}
		if _, exists := seen[converted.ID]; exists {
			return ConnectionCheckPage{}, protocolError(errors.New("connection checks contain duplicate identity"))
		}
		seen[converted.ID] = struct{}{}
		items = append(items, converted)
	}
	return ConnectionCheckPage{Items: items, Page: page}, nil
}

func convertConnectionCheck(source generated.ConnectionCheck) (ConnectionCheck, error) {
	id := source.Id.String()
	if !validIdentity(id) || !validConfigID(source.ConnectionId) || !knownCheckState(string(source.State)) || source.CreatedAt.IsZero() || len(optionalStrings(source.Capabilities)) > MaxCapabilities {
		return ConnectionCheck{}, protocolError(errors.New("connection check evidence is invalid"))
	}
	return ConnectionCheck{ID: id, ConnectionID: source.ConnectionId, State: string(source.State), CreatedAt: source.CreatedAt, CompletedAt: copyTime(source.CompletedAt), Capabilities: optionalStrings(source.Capabilities), ErrorPresent: source.SanitizedError != nil && *source.SanitizedError != ""}, nil
}

func convertPage(source generated.Page) (PageInfo, error) {
	if source.ObservedAt.IsZero() {
		return PageInfo{}, protocolError(errors.New("settings page observation is missing"))
	}
	page := PageInfo{ObservedAt: source.ObservedAt}
	if source.NextCursor != nil {
		if *source.NextCursor == "" || len(*source.NextCursor) > MaxCursorLength || strings.TrimSpace(*source.NextCursor) != *source.NextCursor {
			return PageInfo{}, protocolError(errors.New("settings page cursor is invalid"))
		}
		page.NextCursor = *source.NextCursor
	}
	page.Coverage = make([]Coverage, 0, len(source.Coverage))
	for _, value := range source.Coverage {
		if !knownCompleteness(string(value.Completeness)) || value.ObservedAt.IsZero() {
			return PageInfo{}, protocolError(errors.New("settings coverage is incomplete"))
		}
		coverage := Coverage{Completeness: string(value.Completeness), ObservedAt: value.ObservedAt, StartedAt: copyTime(value.StartedAt), CompletedAt: copyTime(value.CompletedAt), ObservedCount: copyInt(value.ObservedCount), ReasonCodes: optionalStrings(value.ReasonCodes)}
		if value.ConnectionId != nil {
			if !validConfigID(*value.ConnectionId) {
				return PageInfo{}, protocolError(errors.New("settings coverage connection identity is invalid"))
			}
			coverage.ConnectionID = *value.ConnectionId
		}
		if value.RootId != nil {
			if !validConfigID(*value.RootId) {
				return PageInfo{}, protocolError(errors.New("settings coverage root identity is invalid"))
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

func convertSource(source generated.SourceMetadata) (SourceMetadata, error) {
	documentID := source.DocumentId
	if strings.ContainsAny(documentID, `/\\`) || strings.Contains(documentID, "..") {
		documentID = "configured document"
	}
	value := SourceMetadata{Source: string(source.Source), Editable: source.Editable, DocumentID: documentID, Revision: source.Revision, StartupAt: source.StartupAt, ReloadPolicy: string(source.ReloadPolicy)}
	if !validateSource(value) {
		return SourceMetadata{}, protocolError(errors.New("source metadata is invalid"))
	}
	return value, nil
}

func redactEndpoint(raw string) (string, error) {
	if !validBounded(raw, 4096, false) {
		return "", errors.New("endpoint is invalid")
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed == nil || parsed.Opaque != "" {
		return "", errors.New("endpoint is invalid")
	}
	parsed.User = nil
	parsed.RawQuery = ""
	parsed.Fragment = ""
	value := parsed.String()
	if value == "" || !validBounded(value, 1024, false) {
		return "", errors.New("endpoint is invalid")
	}
	return value, nil
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

func copyStrings(value *[]string) []string {
	if value == nil {
		return nil
	}
	return append([]string(nil), (*value)...)
}

func copyStringsValue(value []string) []string { return append([]string(nil), value...) }

func optionalStrings(value *[]string) []string { return copyStrings(value) }

func optionalString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
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

func copyBool(value *bool) *bool {
	if value == nil {
		return nil
	}
	copyValue := *value
	return &copyValue
}

func knownKeySource(value string) bool {
	return value == "environment" || value == "secret_file" || value == "generated_persistent"
}
