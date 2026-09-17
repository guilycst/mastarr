package inventory

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

var errResponseTooLarge = errors.New("inventory response too large")

// HTTPReader adapts the generated API client to the normalized Reader
// boundary. Generated DTOs remain private to this file and never reach the
// renderer or its callers.
type HTTPReader struct {
	generated *generated.ClientWithResponses
	timeout   time.Duration
}

var _ Reader = (*HTTPReader)(nil)

// NewHTTPReader creates a redirect-safe, bounded API reader. The API URL is
// an HTTP-only BFF dependency and may include a deployment path, but never
// credentials, query data, fragments, or opaque URLs.
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
		return nil, errors.New("create inventory API client failed")
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
	hc.CheckRedirect = func(_ *http.Request, _ []*http.Request) error {
		return http.ErrUseLastResponse
	}
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

func (r *HTTPReader) ListDiscoveries(parent context.Context, query PageRequest) (DiscoveryPage, error) {
	if r == nil || r.generated == nil {
		return DiscoveryPage{}, unavailableError(nil)
	}
	ctx, cancel := r.requestContext(parent)
	defer cancel()
	params := discoveryParams(query)
	response, err := r.generated.ListDiscoveriesWithResponse(ctx, params)
	if err != nil {
		return DiscoveryPage{}, classifyHTTPError(err, ctx)
	}
	if err := requireHTTPStatus(responseStatus(response), http.StatusOK); err != nil {
		return DiscoveryPage{}, err
	}
	var source generated.DiscoveryList
	if err := decodeStrict(response.GetBody(), &source); err != nil {
		return DiscoveryPage{}, protocolError(err)
	}
	if err := requireDiscoveryListFields(response.GetBody()); err != nil {
		return DiscoveryPage{}, protocolError(err)
	}
	return convertDiscoveryPage(source, response.GetBody())
}

func (r *HTTPReader) GetDiscovery(parent context.Context, id string) (Discovery, error) {
	if r == nil || r.generated == nil {
		return Discovery{}, unavailableError(nil)
	}
	if !validIdentity(id) {
		return Discovery{}, protocolError(errors.New("invalid discovery identity"))
	}
	ctx, cancel := r.requestContext(parent)
	defer cancel()
	parsedID, parseErr := parseID(id)
	if parseErr != nil {
		return Discovery{}, protocolError(errors.New("invalid discovery identity"))
	}
	response, err := r.generated.GetDiscoveryWithResponse(ctx, generated.DiscoveryId(parsedID))
	if err != nil {
		return Discovery{}, classifyHTTPError(err, ctx)
	}
	if err := requireHTTPStatus(responseStatus(response), http.StatusOK); err != nil {
		return Discovery{}, err
	}
	var source generated.Discovery
	if err := decodeStrict(response.GetBody(), &source); err != nil {
		return Discovery{}, protocolError(err)
	}
	if err := requireDiscoveryFields(response.GetBody()); err != nil {
		return Discovery{}, protocolError(err)
	}
	return convertDiscovery(source)
}

func (r *HTTPReader) ListMedia(parent context.Context, query PageRequest) (MediaPage, error) {
	if r == nil || r.generated == nil {
		return MediaPage{}, unavailableError(nil)
	}
	ctx, cancel := r.requestContext(parent)
	defer cancel()
	params := mediaParams(query)
	response, err := r.generated.ListMediaWithResponse(ctx, params)
	if err != nil {
		return MediaPage{}, classifyHTTPError(err, ctx)
	}
	if err := requireHTTPStatus(responseStatus(response), http.StatusOK); err != nil {
		return MediaPage{}, err
	}
	var source generated.MediaList
	if err := decodeStrict(response.GetBody(), &source); err != nil {
		return MediaPage{}, protocolError(err)
	}
	if err := requireMediaListFields(response.GetBody()); err != nil {
		return MediaPage{}, protocolError(err)
	}
	return convertMediaPage(source, response.GetBody())
}

func (r *HTTPReader) GetMedia(parent context.Context, id string) (Media, error) {
	if r == nil || r.generated == nil {
		return Media{}, unavailableError(nil)
	}
	if !validIdentity(id) {
		return Media{}, protocolError(errors.New("invalid media identity"))
	}
	ctx, cancel := r.requestContext(parent)
	defer cancel()
	parsedID, parseErr := parseID(id)
	if parseErr != nil {
		return Media{}, protocolError(errors.New("invalid media identity"))
	}
	response, err := r.generated.GetMediaWithResponse(ctx, generated.MediaId(parsedID))
	if err != nil {
		return Media{}, classifyHTTPError(err, ctx)
	}
	if err := requireHTTPStatus(responseStatus(response), http.StatusOK); err != nil {
		return Media{}, err
	}
	var source generated.Media
	if err := decodeStrict(response.GetBody(), &source); err != nil {
		return Media{}, protocolError(err)
	}
	if err := requireMediaFields(response.GetBody()); err != nil {
		return Media{}, protocolError(err)
	}
	return convertMedia(source)
}

func (r *HTTPReader) ListDownloads(parent context.Context, query PageRequest) (DownloadPage, error) {
	if r == nil || r.generated == nil {
		return DownloadPage{}, unavailableError(nil)
	}
	ctx, cancel := r.requestContext(parent)
	defer cancel()
	params := downloadParams(query)
	response, err := r.generated.ListDownloadsWithResponse(ctx, params)
	if err != nil {
		return DownloadPage{}, classifyHTTPError(err, ctx)
	}
	if err := requireHTTPStatus(responseStatus(response), http.StatusOK); err != nil {
		return DownloadPage{}, err
	}
	var source generated.DownloadList
	if err := decodeStrict(response.GetBody(), &source); err != nil {
		return DownloadPage{}, protocolError(err)
	}
	if err := requireDownloadListFields(response.GetBody()); err != nil {
		return DownloadPage{}, protocolError(err)
	}
	return convertDownloadPage(source, response.GetBody())
}

func (r *HTTPReader) GetDownload(parent context.Context, id string) (Download, error) {
	if r == nil || r.generated == nil {
		return Download{}, unavailableError(nil)
	}
	if !validIdentity(id) {
		return Download{}, protocolError(errors.New("invalid download identity"))
	}
	ctx, cancel := r.requestContext(parent)
	defer cancel()
	parsedID, parseErr := parseID(id)
	if parseErr != nil {
		return Download{}, protocolError(errors.New("invalid download identity"))
	}
	response, err := r.generated.GetDownloadWithResponse(ctx, generated.DownloadId(parsedID))
	if err != nil {
		return Download{}, classifyHTTPError(err, ctx)
	}
	if err := requireHTTPStatus(responseStatus(response), http.StatusOK); err != nil {
		return Download{}, err
	}
	var source generated.Download
	if err := decodeStrict(response.GetBody(), &source); err != nil {
		return Download{}, protocolError(err)
	}
	if err := requiredObjectFields(response.GetBody(), "id", "connectionId", "state", "observedAt"); err != nil {
		return Download{}, protocolError(err)
	}
	return convertDownload(source)
}

func (r *HTTPReader) ListDescriptors(parent context.Context, query PageRequest) (DescriptorPage, error) {
	if r == nil || r.generated == nil {
		return DescriptorPage{}, unavailableError(nil)
	}
	ctx, cancel := r.requestContext(parent)
	defer cancel()
	params := descriptorParams(query)
	response, err := r.generated.ListDescriptorsWithResponse(ctx, params)
	if err != nil {
		return DescriptorPage{}, classifyHTTPError(err, ctx)
	}
	if err := requireHTTPStatus(responseStatus(response), http.StatusOK); err != nil {
		return DescriptorPage{}, err
	}
	var source generated.DescriptorList
	if err := decodeStrict(response.GetBody(), &source); err != nil {
		return DescriptorPage{}, protocolError(err)
	}
	if err := requireDescriptorListFields(response.GetBody()); err != nil {
		return DescriptorPage{}, protocolError(err)
	}
	return convertDescriptorPage(source, response.GetBody())
}

func (r *HTTPReader) GetDescriptor(parent context.Context, id string) (Descriptor, error) {
	if r == nil || r.generated == nil {
		return Descriptor{}, unavailableError(nil)
	}
	if !validIdentity(id) {
		return Descriptor{}, protocolError(errors.New("invalid descriptor identity"))
	}
	ctx, cancel := r.requestContext(parent)
	defer cancel()
	parsedID, parseErr := parseID(id)
	if parseErr != nil {
		return Descriptor{}, protocolError(errors.New("invalid descriptor identity"))
	}
	response, err := r.generated.GetDescriptorWithResponse(ctx, generated.DescriptorId(parsedID))
	if err != nil {
		return Descriptor{}, classifyHTTPError(err, ctx)
	}
	if err := requireHTTPStatus(responseStatus(response), http.StatusOK); err != nil {
		return Descriptor{}, err
	}
	var source generated.Descriptor
	if err := decodeStrict(response.GetBody(), &source); err != nil {
		return Descriptor{}, protocolError(err)
	}
	if err := requiredObjectFields(response.GetBody(), "id", "type", "size", "digest", "availability", "capturedAt"); err != nil {
		return Descriptor{}, protocolError(err)
	}
	return convertDescriptor(source)
}

func discoveryParams(query PageRequest) *generated.ListDiscoveriesParams {
	params := &generated.ListDiscoveriesParams{}
	if query.Cursor != "" {
		cursor := generated.Cursor(query.Cursor)
		params.Cursor = &cursor
	}
	if query.Limit > 0 {
		limit := generated.Limit(query.Limit)
		params.Limit = &limit
	}
	if query.RootID != "" {
		rootID := generated.RootFilter(query.RootID)
		params.RootId = &rootID
	}
	return params
}

func mediaParams(query PageRequest) *generated.ListMediaParams {
	params := &generated.ListMediaParams{}
	if query.Cursor != "" {
		cursor := generated.Cursor(query.Cursor)
		params.Cursor = &cursor
	}
	if query.Limit > 0 {
		limit := generated.Limit(query.Limit)
		params.Limit = &limit
	}
	if query.Kind != "" {
		kind := generated.MediaKindFilter(query.Kind)
		params.Kind = &kind
	}
	return params
}

func downloadParams(query PageRequest) *generated.ListDownloadsParams {
	params := &generated.ListDownloadsParams{}
	if query.Cursor != "" {
		cursor := generated.Cursor(query.Cursor)
		params.Cursor = &cursor
	}
	if query.Limit > 0 {
		limit := generated.Limit(query.Limit)
		params.Limit = &limit
	}
	if query.ConnectionID != "" {
		connectionID := generated.ConnectionFilter(query.ConnectionID)
		params.ConnectionId = &connectionID
	}
	return params
}

func descriptorParams(query PageRequest) *generated.ListDescriptorsParams {
	params := &generated.ListDescriptorsParams{}
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
	compact := strings.ReplaceAll(raw, "-", "")
	decoded, err := hex.DecodeString(compact)
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
	var syntaxErr *json.SyntaxError
	var typeErr *json.UnmarshalTypeError
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) || errors.As(err, &syntaxErr) || errors.As(err, &typeErr) {
		return protocolError(err)
	}
	if errors.Is(err, context.Canceled) || (requestContext != nil && errors.Is(requestContext.Err(), context.Canceled)) {
		return &APIError{kind: ErrorCanceled, cause: context.Canceled}
	}
	if errors.Is(err, context.DeadlineExceeded) || (requestContext != nil && errors.Is(requestContext.Err(), context.DeadlineExceeded)) {
		return &APIError{kind: ErrorTimeout, cause: context.DeadlineExceeded}
	}
	return unavailableError(err)
}

func unavailableError(cause error) error {
	return &APIError{kind: ErrorUnavailable, cause: cause}
}

func protocolError(cause error) error {
	return &APIError{kind: ErrorProtocol, cause: cause}
}

func decodeStrict(body []byte, destination any) error {
	if len(body) == 0 || len(body) > client.MaxResponseBytes || !json.Valid(body) {
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

func requiredObjectFields(body []byte, fields ...string) error {
	var object map[string]json.RawMessage
	decoder := json.NewDecoder(bytes.NewReader(body))
	if err := decoder.Decode(&object); err != nil || object == nil {
		return errors.New("response object is missing")
	}
	for _, field := range fields {
		value, ok := object[field]
		if !ok || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return errors.New("response required field is missing")
		}
	}
	return nil
}

func requireListItemFields(body []byte, fields ...string) error {
	items, err := rawListItems(body)
	if err != nil {
		return err
	}
	for _, item := range items {
		if err := requiredObjectFields(item, fields...); err != nil {
			return err
		}
	}
	return nil
}

func requireNestedItemFields(body []byte, parent string, fields ...string) error {
	items, err := rawListItems(body)
	if err != nil {
		return err
	}
	for _, item := range items {
		if err := requireNestedObjectFields(item, parent, fields...); err != nil {
			return err
		}
	}
	return nil
}

func rawListItems(body []byte) ([]json.RawMessage, error) {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(body, &object); err != nil || object == nil {
		return nil, errors.New("list response is not an object")
	}
	rawItems, ok := object["items"]
	if !ok || bytes.Equal(bytes.TrimSpace(rawItems), []byte("null")) {
		return nil, errors.New("list items are missing")
	}
	var items []json.RawMessage
	if err := json.Unmarshal(rawItems, &items); err != nil || items == nil {
		return nil, errors.New("list items are not an array")
	}
	return items, nil
}

func requireNestedObjectFields(body []byte, parent string, fields ...string) error {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(body, &object); err != nil || object == nil {
		return errors.New("response is not an object")
	}
	nested, ok := object[parent]
	if !ok || bytes.Equal(bytes.TrimSpace(nested), []byte("null")) {
		return errors.New("nested response field is missing")
	}
	var values []json.RawMessage
	if err := json.Unmarshal(nested, &values); err != nil || values == nil {
		return errors.New("nested response field is not an array")
	}
	for _, value := range values {
		if err := requiredObjectFields(value, fields...); err != nil {
			return err
		}
	}
	return nil
}

func requireDiscoveryListFields(body []byte) error {
	if err := requireListItemFields(body, "id", "files", "readiness", "observedAt"); err != nil {
		return err
	}
	return requireNestedItemFields(body, "files", "rootId", "relativePath", "type", "size")
}

func requireDiscoveryFields(body []byte) error {
	if err := requiredObjectFields(body, "id", "files", "readiness", "observedAt"); err != nil {
		return err
	}
	return requireNestedObjectFields(body, "files", "rootId", "relativePath", "type", "size")
}

func requireMediaListFields(body []byte) error {
	if err := requireListItemFields(body, "id", "kind", "providerId", "tracking", "observedAt"); err != nil {
		return err
	}
	return requireNestedItemFields(body, "tracking", "connectionId", "dimension", "value", "observedAt")
}

func requireMediaFields(body []byte) error {
	if err := requiredObjectFields(body, "id", "kind", "providerId", "tracking", "observedAt"); err != nil {
		return err
	}
	return requireNestedObjectFields(body, "tracking", "connectionId", "dimension", "value", "observedAt")
}

func requireDownloadListFields(body []byte) error {
	return requireListItemFields(body, "id", "connectionId", "state", "observedAt")
}

func requireDescriptorListFields(body []byte) error {
	return requireListItemFields(body, "id", "type", "size", "digest", "availability", "capturedAt")
}
