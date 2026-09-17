// Package client is the UI BFF's small, generated-client boundary.
//
// Generated OpenAPI DTOs stay below this package boundary. Callers receive
// only the normalized readiness observation and sanitized errors defined here.
package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	apiclient "github.com/guilycst/mastarr/ui/internal/api/generated"
)

const (
	// DefaultTimeout bounds each BFF-to-API request.
	DefaultTimeout = 5 * time.Second
	// MaxResponseBytes prevents an upstream response from becoming an
	// unbounded allocation in the generated response parser.
	MaxResponseBytes = 1 << 20
)

// ReadinessState is the normalized public state used by the shell.
type ReadinessState string

const (
	StateReady    ReadinessState = "ready"
	StateDegraded ReadinessState = "degraded"
)

// Readiness is an API-owned observation. It contains no upstream body or
// reason text, because those values may contain deployment-specific details.
type Readiness struct {
	State      ReadinessState
	ObservedAt time.Time
}

// Reader is the HTTP-only dependency consumed by the BFF routes.
type Reader interface {
	Ready(context.Context) (Readiness, error)
}

// FailureKind classifies a sanitized API failure without retaining endpoint,
// body, transport or credential text in the displayed error.
type FailureKind string

const (
	FailureCanceled     FailureKind = "canceled"
	FailureTimeout      FailureKind = "timeout"
	FailureUnavailable  FailureKind = "unavailable"
	FailureStatus       FailureKind = "status"
	FailureProtocol     FailureKind = "protocol"
	FailureResponseSize FailureKind = "response_size"
)

var (
	// ErrUnavailable identifies an API that cannot currently provide a
	// readiness observation. It is matched through APIError.Is.
	ErrUnavailable = errors.New("mastarr api unavailable")
	// ErrProtocol identifies a response that violated the frozen API contract.
	ErrProtocol         = errors.New("mastarr api response invalid")
	errResponseTooLarge = errors.New("mastarr api response exceeded limit")
)

// APIError is safe to show in logs and shell state. The underlying cause is
// only exposed for context cancellation/deadline identity through errors.Is.
type APIError struct {
	kind      FailureKind
	status    int
	retryable bool
	cause     error
}

func (e *APIError) Error() string {
	if e == nil {
		return "mastarr api failure"
	}
	switch e.kind {
	case FailureCanceled:
		return "mastarr api request canceled"
	case FailureTimeout:
		return "mastarr api request timed out"
	case FailureUnavailable:
		return "mastarr api unavailable"
	case FailureStatus:
		return "mastarr api returned an unavailable response"
	case FailureResponseSize:
		return "mastarr api response exceeded the UI limit"
	default:
		return "mastarr api response invalid"
	}
}

// Is supports stable sentinel checks without exposing the wrapped transport
// error. Context identity is handled by Unwrap below.
func (e *APIError) Is(target error) bool {
	if e == nil {
		return false
	}
	return (target == ErrUnavailable && (e.kind == FailureUnavailable || e.kind == FailureStatus)) ||
		(target == ErrProtocol && (e.kind == FailureProtocol || e.kind == FailureResponseSize))
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
func (e *APIError) Kind() FailureKind {
	if e == nil {
		return ""
	}
	return e.kind
}

// Status returns the HTTP status when one was observed, or zero for transport
// and protocol failures.
func (e *APIError) Status() int {
	if e == nil {
		return 0
	}
	return e.status
}

// Retryable reports whether trying the same read later is reasonable.
func (e *APIError) Retryable() bool {
	if e == nil {
		return false
	}
	return e.retryable
}

// Client reads the frozen HTTP health endpoint through generated code.
type Client struct {
	generated *apiclient.ClientWithResponses
	timeout   time.Duration
}

var _ Reader = (*Client)(nil)

// New validates an API origin and creates a redirect-safe generated client.
// Redirects are returned as responses and are never followed, preventing a
// misconfigured API from forwarding requests to an arbitrary host.
func New(baseURL string, httpClient *http.Client, timeout time.Duration) (*Client, error) {
	if err := validateBaseURL(baseURL); err != nil {
		return nil, err
	}
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	hc := cloneHTTPClient(httpClient)
	generated, err := apiclient.NewClientWithResponses(strings.TrimRight(baseURL, "/"), apiclient.WithHTTPClient(hc))
	if err != nil {
		return nil, fmt.Errorf("create API client: %w", err)
	}
	return &Client{generated: generated, timeout: timeout}, nil
}

func cloneHTTPClient(input *http.Client) *http.Client {
	var hc http.Client
	if input != nil {
		hc = *input
	}
	// A caller-provided redirect hook is not part of the BFF contract. Replace
	// it so a future caller cannot accidentally enable credential or host
	// forwarding when this client grows more read methods.
	hc.CheckRedirect = func(_ *http.Request, _ []*http.Request) error {
		return http.ErrUseLastResponse
	}
	transport := hc.Transport
	if transport == nil {
		transport = http.DefaultTransport
	}
	hc.Transport = boundedTransport{next: transport, maxBytes: MaxResponseBytes}
	return &hc
}

type boundedTransport struct {
	next     http.RoundTripper
	maxBytes int64
}

func (t boundedTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	response, err := t.next.RoundTrip(req)
	if err != nil {
		return nil, err
	}
	if response == nil || response.Body == nil {
		return response, nil
	}
	response.Body = &boundedReadCloser{
		reader: response.Body,
		closer: response.Body,
		max:    t.maxBytes,
	}
	return response, nil
}

type boundedReadCloser struct {
	reader io.Reader
	closer io.Closer
	seen   int64
	max    int64
}

func (r *boundedReadCloser) Read(p []byte) (int, error) {
	if r.seen >= r.max {
		var one [1]byte
		n, err := r.reader.Read(one[:])
		if n > 0 {
			r.seen += int64(n)
			return 0, errResponseTooLarge
		}
		if err != nil {
			return 0, err
		}
		return 0, nil
	}
	remaining := r.max - r.seen
	if int64(len(p)) > remaining {
		p = p[:remaining]
	}
	n, err := r.reader.Read(p)
	r.seen += int64(n)
	return n, err
}

func (r *boundedReadCloser) Close() error { return r.closer.Close() }

// Ready obtains one current readiness observation. A parent deadline always
// remains the upper bound; the per-request timeout can only shorten it.
func (c *Client) Ready(parent context.Context) (Readiness, error) {
	if c == nil || c.generated == nil {
		return Readiness{}, &APIError{kind: FailureUnavailable, retryable: true}
	}
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithTimeout(parent, c.timeout)
	defer cancel()
	response, err := c.generated.GetReadyHealthWithResponse(ctx)
	if err != nil {
		return Readiness{}, classifyClientError(err, ctx)
	}
	if response == nil || response.HTTPResponse == nil {
		return Readiness{}, &APIError{kind: FailureProtocol}
	}
	if len(response.GetBody()) > MaxResponseBytes {
		return Readiness{}, &APIError{kind: FailureResponseSize}
	}
	if response.StatusCode() != http.StatusOK {
		status := response.StatusCode()
		return Readiness{}, &APIError{
			kind:      classifyStatus(status),
			status:    status,
			retryable: status == http.StatusTooManyRequests || status >= 500,
		}
	}
	health := response.GetJSON200()
	if health == nil || health.ObservedAt.IsZero() {
		return Readiness{}, &APIError{kind: FailureProtocol}
	}
	var state ReadinessState
	switch health.Status {
	case apiclient.HealthStatus("ok"):
		state = StateReady
	case apiclient.HealthStatus("degraded"):
		state = StateDegraded
	case apiclient.HealthStatus("not_ready"):
		return Readiness{}, &APIError{kind: FailureUnavailable, status: http.StatusServiceUnavailable, retryable: true}
	default:
		return Readiness{}, &APIError{kind: FailureProtocol}
	}
	return Readiness{State: state, ObservedAt: health.ObservedAt}, nil
}

func classifyTransport(err error, requestContext context.Context) error {
	if errors.Is(requestContext.Err(), context.Canceled) || errors.Is(err, context.Canceled) {
		return &APIError{kind: FailureCanceled, cause: context.Canceled}
	}
	if errors.Is(requestContext.Err(), context.DeadlineExceeded) || errors.Is(err, context.DeadlineExceeded) {
		return &APIError{kind: FailureTimeout, cause: context.DeadlineExceeded, retryable: true}
	}
	return &APIError{kind: FailureUnavailable, cause: err, retryable: true}
}

func classifyClientError(err error, requestContext context.Context) error {
	if errors.Is(err, errResponseTooLarge) {
		return &APIError{kind: FailureResponseSize}
	}
	if errors.Is(requestContext.Err(), context.Canceled) || errors.Is(err, context.Canceled) ||
		errors.Is(requestContext.Err(), context.DeadlineExceeded) || errors.Is(err, context.DeadlineExceeded) {
		return classifyTransport(err, requestContext)
	}
	var syntaxErr *json.SyntaxError
	var typeErr *json.UnmarshalTypeError
	var timeErr *time.ParseError
	if errors.As(err, &syntaxErr) || errors.As(err, &typeErr) || errors.As(err, &timeErr) || errors.Is(err, io.ErrUnexpectedEOF) {
		return &APIError{kind: FailureProtocol}
	}
	return &APIError{kind: FailureUnavailable, cause: err, retryable: true}
}

func classifyStatus(status int) FailureKind {
	if status == http.StatusServiceUnavailable || status >= 500 {
		return FailureUnavailable
	}
	return FailureStatus
}

func validateBaseURL(raw string) error {
	if !utf8.ValidString(raw) || strings.TrimSpace(raw) != raw {
		return errors.New("API URL must be valid UTF-8 without surrounding whitespace")
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed == nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.Hostname() == "" {
		return errors.New("API URL must be an absolute HTTP(S) URL")
	}
	if parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.Opaque != "" {
		return errors.New("API URL must not contain credentials, query data, or fragments")
	}
	if strings.ContainsAny(parsed.Host, "\r\n\t") {
		return errors.New("API URL contains invalid host data")
	}
	if port := parsed.Port(); port != "" {
		value, err := strconv.Atoi(port)
		if err != nil || value < 0 || value > 65535 {
			return errors.New("API URL contains an invalid port")
		}
	}
	return nil
}
