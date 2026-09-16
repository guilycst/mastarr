// Package write implements Jellyfin's explicit refresh action boundary.
//
// Refresh acceptance is kept separate from later library availability. The
// standalone client owns Jellyfin transport, authentication, response bounds
// and typed upstream errors; this package translates that boundary into the
// Mastarr refresh port and keeps unsupported scopes fail-closed.
package write

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	upstream "github.com/guilycst/mastarr/clients/jellyfin"
	"github.com/guilycst/mastarr/internal/domain"
	"github.com/guilycst/mastarr/internal/ports"
)

const (
	operationRefresh        = "jellyfin.refresh"
	operationRefreshLibrary = "jellyfin.refresh.library"
	operationRefreshItem    = "jellyfin.refresh.item"
	compatibilityVersion    = "jellyfin-refresh-compat-0.0.1"
)

// Config contains one Jellyfin refresh connection. Token, APIKey and
// AuthToken are aliases accepted for callers using different connection
// credential names; APIKey takes precedence, then Token, then AuthToken.
// Transport, authentication, deadlines and response bounds remain owned by
// the standalone clients/jellyfin module.
type Config struct {
	ConnectionID domain.ConfigID
	Endpoint     string
	APIKey       string
	Token        string
	AuthToken    string
	HTTPClient   *http.Client

	RequestTimeout  time.Duration
	MaxResponseSize int64
}

// Client translates the standalone Jellyfin refresh client into the frozen
// Mastarr port. It never exposes generated upstream DTOs.
type Client struct {
	config   Config
	upstream *upstream.Client
}

var _ ports.MediaServerRefreshPort = (*Client)(nil)
var _ ports.CapabilityPort = (*Client)(nil)

// New validates the Mastarr connection scope and constructs the standalone
// client without contacting Jellyfin.
func New(config Config) (*Client, error) {
	if !config.ConnectionID.Valid() {
		return nil, errors.New("Jellyfin refresh connection id is invalid")
	}
	client, err := upstream.New(upstream.Config{
		Endpoint:         config.Endpoint,
		Token:            configuredToken(config),
		HTTPClient:       config.HTTPClient,
		RequestTimeout:   config.RequestTimeout,
		MaxResponseBytes: config.MaxResponseSize,
		UserAgent:        "mastarr-jellyfin-refresh/0.0.1",
	})
	if err != nil {
		return nil, err
	}
	return &Client{config: config, upstream: client}, nil
}

// NewClient is an explicit constructor alias.
func NewClient(config Config) (*Client, error) { return New(config) }

// ConnectionID returns the configured Mastarr connection scope.
func (client *Client) ConnectionID() domain.ConfigID {
	if client == nil {
		return ""
	}
	return client.config.ConnectionID
}

// Refresh accepts only a library refresh in this lane. Jellyfin item refresh
// is represented by the standalone compatibility client but remains blocked
// here until a positive, version-pinned item-scope fixture exists. A blocked
// item request performs no native call.
func (client *Client) Refresh(ctx context.Context, connectionID domain.ConfigID, request ports.RefreshRequest) (ports.RefreshResult, error) {
	observedAt := time.Now().UTC()
	result := ports.RefreshResult{ObservedAt: observedAt}
	if client == nil || client.upstream == nil {
		return result, upstreamFailure(operationRefresh, domain.OutcomeUnknown, 0, false, "refresh client is unavailable")
	}
	if err := validateConnectionScope(client.config.ConnectionID, connectionID); err != nil {
		return result, err
	}
	if ctx == nil {
		return result, invalidInput(operationRefresh + ".context")
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}

	switch request.Scope {
	case ports.RefreshLibrary:
		if request.ExternalID != "" {
			return result, invalidInput(operationRefreshLibrary)
		}
		accepted, err := client.upstream.Refresh(ctx, upstream.RefreshRequest{Scope: upstream.RefreshLibraryScope})
		if err != nil {
			return result, normalizeError(operationRefreshLibrary, err)
		}
		if !accepted.Accepted {
			return result, upstreamFailure(operationRefreshLibrary, domain.OutcomeUnknown, accepted.Status, false, "refresh acceptance was not confirmed")
		}
		result.Accepted = true
		result.OperationID = operationRefreshLibrary
		result.ObservedAt = observedTime(accepted.ObservedAt, observedAt)
		result.Evidence = append([]string(nil), accepted.Evidence...)
		return result, nil
	case ports.RefreshItem:
		if err := validateItemID(request.ExternalID); err != nil {
			return result, invalidInput(operationRefreshItem)
		}
		result.Evidence = []string{"item_scope_unsupported", "positive_item_refresh_fixture_required", "availability_requires_later_read"}
		return result, upstreamFailure(operationRefreshItem, domain.OutcomeUnsupported, 0, false, "item refresh scope is not enabled")
	default:
		return result, invalidInput(operationRefresh + ".scope")
	}
}

// Capabilities reports independent refresh scope gates. Library support means
// only that Jellyfin accepted the native refresh request through the pinned
// compatibility contract; it does not claim scan completion or availability.
func (client *Client) Capabilities(ctx context.Context, connectionID domain.ConfigID) ([]domain.Capability, error) {
	if client == nil {
		return nil, upstreamFailure(operationRefresh, domain.OutcomeUnknown, 0, false, "refresh client is unavailable")
	}
	if err := validateConnectionScope(client.config.ConnectionID, connectionID); err != nil {
		return nil, err
	}
	if ctx == nil {
		return nil, invalidInput(operationRefresh + ".context")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	return []domain.Capability{
		{
			Name: operationRefreshLibrary, State: domain.CapabilitySupported,
			Version:    compatibilityVersion,
			Evidence:   []string{"POST /Library/Refresh", "refresh_request_accepted", "availability_requires_later_read"},
			ObservedAt: now,
		},
		{
			Name: operationRefreshItem, State: domain.CapabilityUnsupported,
			Version:    compatibilityVersion,
			Reason:     "item refresh has no positive version-pinned fixture",
			Evidence:   []string{"item_scope_unsupported", "positive_item_refresh_fixture_required"},
			ObservedAt: now,
		},
	}, nil
}

func configuredToken(config Config) string {
	switch {
	case config.APIKey != "":
		return config.APIKey
	case config.Token != "":
		return config.Token
	default:
		return config.AuthToken
	}
}

func validateConnectionScope(expected, requested domain.ConfigID) error {
	if !requested.Valid() || requested != expected {
		return invalidInput(operationRefresh + ".connection")
	}
	return nil
}

func validateItemID(value string) error {
	if value == "" || strings.TrimSpace(value) != value || len(value) > 256 || strings.ContainsAny(value, "/\\?#\r\n") {
		return errors.New("item id is invalid")
	}
	return nil
}

func observedTime(value, fallback time.Time) time.Time {
	if value.IsZero() {
		return fallback
	}
	return value.UTC()
}

func normalizeError(operation string, err error) error {
	if err == nil {
		return nil
	}
	// Preserve cancellation identity without exposing upstream transport,
	// endpoint or response details.
	if errors.Is(err, context.Canceled) {
		return context.Canceled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return context.DeadlineExceeded
	}
	var source upstream.UpstreamError
	if errors.As(err, &source) {
		return upstreamFailure(operation, normalizeCode(source.Code), source.Status, source.Retryable, "upstream refresh request failed")
	}
	return upstreamFailure(operation, domain.OutcomeUnknown, 0, false, "upstream refresh request failed")
}

func normalizeCode(code upstream.ErrorCode) domain.UpstreamErrorCode {
	switch code {
	case upstream.ErrorUnavailable:
		return domain.OutcomeUnavailable
	case upstream.ErrorRateLimited:
		return domain.OutcomeRateLimited
	case upstream.ErrorUnauthorized, upstream.ErrorForbidden:
		return domain.OutcomeUnauthorized
	case upstream.ErrorInvalidInput:
		return domain.OutcomeInvalidInput
	case upstream.ErrorConflict:
		return domain.OutcomeConflict
	case upstream.ErrorUnsupported, upstream.ErrorNotFound:
		return domain.OutcomeUnsupported
	default:
		return domain.OutcomeUnknown
	}
}

func invalidInput(operation string) error {
	return domain.UpstreamError{Code: domain.OutcomeInvalidInput, Operation: operation, Detail: "request is invalid"}
}

func upstreamFailure(operation string, code domain.UpstreamErrorCode, status int, retryable bool, detail string) error {
	return domain.UpstreamError{Code: code, Operation: operation, Status: status, Retryable: retryable, Detail: detail}
}

// String keeps diagnostics safe if a Client is accidentally formatted. The
// endpoint and credentials remain owned by the standalone client and are not
// included in adapter errors or ordinary logs.
func (client *Client) String() string {
	if client == nil {
		return "JellyfinRefreshClient{}"
	}
	return fmt.Sprintf("JellyfinRefreshClient{connection:%s}", client.config.ConnectionID)
}
