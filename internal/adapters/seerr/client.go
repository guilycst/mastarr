// Package seerr translates the standalone Seerr client into Mastarr's
// read-only request-catalog ports. Upstream transport, authentication,
// decoding and error classification remain owned by clients/seerr.
package seerr

import (
	"context"
	"errors"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	upstream "github.com/guilycst/mastarr/clients/seerr"
	"github.com/guilycst/mastarr/internal/domain"
	"github.com/guilycst/mastarr/internal/ports"
)

const (
	defaultPageSize    = 100
	defaultMaxPages    = 100
	defaultMaxRecords  = 10_000
	defaultMaxResponse = 16 << 20
)

// Config contains one Seerr instance's endpoint, API key and observation
// bounds. Token and AuthToken are retained as aliases for APIKey for callers
// that use generic connection credential names; APIKey wins.
type Config struct {
	ConnectionID domain.ConfigID
	Endpoint     string
	APIKey       string
	Token        string
	AuthToken    string
	HTTPClient   *http.Client

	MaxPageSize     int
	MaxPages        int
	MaxRecords      int
	MaxResponseSize int64
}

// Client is a Mastarr-scoped view over the standalone Seerr client. The
// standalone client is deliberately private so generated or upstream types
// cannot cross this adapter boundary.
type Client struct {
	config   Config
	upstream *upstream.Client
}

var _ ports.RequestCatalogReadPort = (*Client)(nil)
var _ ports.CapabilityPort = (*Client)(nil)

// VersionObservation is a sanitized read of Seerr's status endpoint.
type VersionObservation struct {
	ConnectionID domain.ConfigID
	ProductName  string
	Version      string
	CommitTag    string
	Commit       string
	ObservedAt   time.Time
}

// ProviderRelationship preserves every provider identity returned by Seerr.
type ProviderRelationship struct {
	Provider string
	ID       string
}

// ServiceRelationship preserves the Arr service relationship represented by
// one Seerr media record. Service availability is independent from media
// availability and request state.
type ServiceRelationship struct {
	Kind       string
	ServiceID  string
	ExternalID string
	Slug       string
	Is4K       bool
}

// AvailabilityObservation is Seerr's own media availability evidence. It is
// deliberately separate from request status and Jellyfin playability.
type AvailabilityObservation struct {
	Known              bool
	Available          bool
	PartiallyAvailable bool
	NativeStatus       int
	NativeStatusName   string
	ObservedAt         time.Time
	Reason             string
}

// MediaStatus is Seerr's native media status enum. Unknown values remain
// visible through NativeStatus and map to "unknown".
type MediaStatus int

const (
	MediaStatusUnknown            MediaStatus = 1
	MediaStatusPending            MediaStatus = 2
	MediaStatusProcessing         MediaStatus = 3
	MediaStatusPartiallyAvailable MediaStatus = 4
	MediaStatusAvailable          MediaStatus = 5
	MediaStatusBlocklisted        MediaStatus = 6
	MediaStatusDeleted            MediaStatus = 7
)

func (status MediaStatus) String() string {
	switch status {
	case MediaStatusUnknown:
		return "unknown"
	case MediaStatusPending:
		return "pending"
	case MediaStatusProcessing:
		return "processing"
	case MediaStatusPartiallyAvailable:
		return "partially_available"
	case MediaStatusAvailable:
		return "available"
	case MediaStatusBlocklisted:
		return "blocklisted"
	case MediaStatusDeleted:
		return "deleted"
	default:
		return "unknown"
	}
}

// RequestStatus is Seerr's native request status enum.
type RequestStatus int

const (
	RequestStatusPending   RequestStatus = 1
	RequestStatusApproved  RequestStatus = 2
	RequestStatusDeclined  RequestStatus = 3
	RequestStatusFailed    RequestStatus = 4
	RequestStatusCompleted RequestStatus = 5
)

func (status RequestStatus) String() string {
	switch status {
	case RequestStatusPending:
		return "pending"
	case RequestStatusApproved:
		return "approved"
	case RequestStatusDeclined:
		return "declined"
	case RequestStatusFailed:
		return "failed"
	case RequestStatusCompleted:
		return "completed"
	default:
		return "unknown"
	}
}

// SeasonObservation preserves season-level native status and identity.
type SeasonObservation struct {
	ID                 string
	SeasonNumber       int
	NativeStatus       int
	NativeStatusName   string
	NativeStatus4K     int
	NativeStatus4KName string
	ObservedAt         time.Time
}

// RequestSummary preserves request references nested in a media record.
type RequestSummary struct {
	ID               string
	NativeStatus     int
	NativeStatusName string
	Type             string
	Is4K             bool
	CreatedAt        *time.Time
	UpdatedAt        *time.Time
}

// MediaObservation is the detailed Seerr media record. Record is the frozen
// common port value used by aggregators; all other fields retain connector
// evidence needed to explain status and relationships.
type MediaObservation struct {
	Record ports.RequestRecord

	ID                    string
	ScopedIdentity        string
	MediaType             string
	TMDBID                string
	TVDBID                string
	IMDBID                string
	ProviderIDs           map[string]string
	ProviderRelationships []ProviderRelationship

	NativeStatus         int
	NativeStatusName     string
	NativeStatus4K       int
	NativeStatus4KName   string
	Requests             []RequestSummary
	Seasons              []SeasonObservation
	ServiceRelationships []ServiceRelationship

	JellyfinMediaID   string
	JellyfinMediaID4K string
	RatingKey         string
	RatingKey4K       string
	MediaAddedAt      *time.Time
	SourceCreatedAt   *time.Time
	SourceUpdatedAt   *time.Time

	Availability AvailabilityObservation
	Evidence     []string
}

// ServiceError preserves Seerr's serviceErrors relationship without exposing
// arbitrary upstream JSON.
type ServiceError struct {
	Kind string
	ID   string
	Name string
}

// RequestObservation is the detailed Seerr request record. Nested Media
// availability remains independent from this request's native status.
type RequestObservation struct {
	Record ports.RequestRecord

	ID                   string
	ScopedIdentity       string
	Type                 string
	NativeStatus         int
	NativeStatusName     string
	Media                MediaObservation
	SeasonCount          int
	Seasons              []SeasonObservation
	Is4K                 bool
	ServerID             string
	ProfileID            string
	RootFolder           string
	LanguageProfileID    string
	Tags                 []int64
	IsAutoRequest        bool
	IgnoreQuota          bool
	ServiceRelationships []ServiceRelationship
	SourceCreatedAt      *time.Time
	SourceUpdatedAt      *time.Time
	Evidence             []string
}

// PageInfo is the upstream pageInfo object. Complete is false when a
// response omitted or malformed one of its fields.
type PageInfo struct {
	Pages    int
	PageSize int
	Results  int
	Page     int
	Complete bool
}

// MediaPage is the detailed media page returned by ListMediaDetailed.
type MediaPage struct {
	Items      []MediaObservation
	NextCursor string
	Coverage   domain.Coverage
	PageInfo   PageInfo
}

// RequestPage is the detailed request page returned by ListRequestsDetailed.
type RequestPage struct {
	Items         []RequestObservation
	NextCursor    string
	Coverage      domain.Coverage
	PageInfo      PageInfo
	ServiceErrors []ServiceError
}

// New validates the Mastarr connection scope and constructs the standalone
// client. No upstream request is made here.
func New(config Config) (*Client, error) {
	if !config.ConnectionID.Valid() {
		return nil, errors.New("Seerr connection id is invalid")
	}
	if config.MaxPageSize <= 0 {
		config.MaxPageSize = defaultPageSize
	}
	if config.MaxPages <= 0 {
		config.MaxPages = defaultMaxPages
	}
	if config.MaxRecords <= 0 {
		config.MaxRecords = defaultMaxRecords
	}
	if config.MaxPageSize > config.MaxRecords {
		config.MaxPageSize = config.MaxRecords
	}
	if config.MaxResponseSize <= 0 {
		config.MaxResponseSize = defaultMaxResponse
	}

	apiKey := strings.TrimSpace(config.APIKey)
	if apiKey == "" {
		apiKey = strings.TrimSpace(config.Token)
	}
	if apiKey == "" {
		apiKey = strings.TrimSpace(config.AuthToken)
	}
	client, err := upstream.New(upstream.Config{
		Endpoint:         config.Endpoint,
		APIKey:           apiKey,
		InstanceID:       config.ConnectionID.String(),
		HTTPClient:       config.HTTPClient,
		RequestTimeout:   30 * time.Second,
		MaxResponseBytes: config.MaxResponseSize,
		MaxPageSize:      config.MaxPageSize,
		MaxPages:         config.MaxPages,
		MaxRecords:       config.MaxRecords,
		UserAgent:        "mastarr-seerr-read/0.0.1",
	})
	if err != nil {
		return nil, err
	}
	return &Client{config: config, upstream: client}, nil
}

// NewClient is an explicit constructor alias.
func NewClient(config Config) (*Client, error) { return New(config) }

// ConnectionID identifies the upstream scope for every returned record.
func (client *Client) ConnectionID() domain.ConfigID { return client.config.ConnectionID }

// ScopedIdentity keeps Seerr numeric IDs distinct across configured
// instances. Empty IDs remain unknown rather than becoming a shared key.
func (client *Client) ScopedIdentity(externalID string) string {
	externalID = strings.TrimSpace(externalID)
	if externalID == "" {
		return ""
	}
	return client.config.ConnectionID.String() + ":" + externalID
}

// Version reads Seerr's status endpoint. A missing status route is returned
// as OutcomeUnsupported so callers can expose the version gate explicitly.
func (client *Client) Version(ctx context.Context, connectionID domain.ConfigID) (VersionObservation, error) {
	result := VersionObservation{ConnectionID: connectionID, ProductName: "seerr", ObservedAt: time.Now().UTC()}
	if err := client.validateScope(connectionID); err != nil {
		return result, err
	}
	status, err := client.upstream.Status(ctx)
	if err != nil {
		return result, client.normalizeError(err)
	}
	result.ProductName = status.ProductName
	result.Version = status.Version
	result.CommitTag = status.CommitTag
	result.Commit = status.Commit
	result.ObservedAt = status.ObservedAt
	return result, nil
}

// Capabilities reports read surfaces and the explicit v0.0.1 write boundary.
// A successful status response establishes version metadata only; it cannot
// promote unpinned media/request routes into supported capabilities.
func (client *Client) Capabilities(ctx context.Context, connectionID domain.ConfigID) ([]domain.Capability, error) {
	if err := client.validateScope(connectionID); err != nil {
		return nil, err
	}
	version, versionErr := client.Version(ctx, connectionID)
	if versionErr != nil {
		if errors.Is(versionErr, context.Canceled) || errors.Is(versionErr, context.DeadlineExceeded) {
			return nil, versionErr
		}
		if code, ok := upstreamCode(versionErr); ok && code == domain.OutcomeUnauthorized {
			return nil, versionErr
		}
	}

	now := time.Now().UTC()
	versionState := domain.CapabilityUnknown
	versionReason := "version observation is unavailable"
	if version.Version != "" {
		versionState, versionReason = domain.CapabilitySupported, ""
	} else if code, ok := upstreamCode(versionErr); ok && code == domain.OutcomeUnsupported {
		versionState, versionReason = domain.CapabilityUnsupported, "Seerr status endpoint is unsupported"
	}
	readState := domain.CapabilityUnknown
	readReason := "Seerr media/request route compatibility is not pinned to a verified release"
	if versionErr != nil {
		switch versionState {
		case domain.CapabilityUnsupported:
			readReason = "Seerr media/request compatibility is unknown because the status endpoint is unsupported"
		case domain.CapabilityUnknown:
			readReason = "Seerr media/request compatibility is unknown because the version observation is unavailable"
		}
	}
	return []domain.Capability{
		{Name: "seerr.version", State: versionState, Version: version.Version, Reason: versionReason, Evidence: []string{"GET /api/v1/status"}, ObservedAt: now},
		{Name: "seerr.media.read", State: readState, Version: version.Version, Reason: readReason, Evidence: []string{"GET /api/v1/media with take/skip/pageInfo", "compatibility_version_unpinned"}, ObservedAt: now},
		{Name: "seerr.requests.read", State: readState, Version: version.Version, Reason: readReason, Evidence: []string{"GET /api/v1/request with take/skip/pageInfo", "compatibility_version_unpinned"}, ObservedAt: now},
		{Name: "seerr.provider-relationships", State: readState, Version: version.Version, Reason: readReason, Evidence: []string{"typed tmdb/tvdb/imdb and service IDs", "compatibility_version_unpinned"}, ObservedAt: now},
		{Name: "seerr.writes", State: domain.CapabilityUnsupported, Version: version.Version, Reason: "Seerr connector is read-only in v0.0.1", Evidence: []string{"no mutation port or HTTP write method"}, ObservedAt: now},
	}, nil
}

// ListMedia implements ports.RequestCatalogReadPort with the common record
// view. Use ListMediaDetailed when connector evidence is needed.
func (client *Client) ListMedia(ctx context.Context, connectionID domain.ConfigID, cursor string, limit int) (ports.Page[ports.RequestRecord], error) {
	detailed, err := client.ListMediaDetailed(ctx, connectionID, cursor, limit)
	if err != nil {
		return ports.Page[ports.RequestRecord]{}, err
	}
	items := make([]ports.RequestRecord, 0, len(detailed.Items))
	for _, item := range detailed.Items {
		items = append(items, item.Record)
	}
	return ports.Page[ports.RequestRecord]{Items: items, NextCursor: detailed.NextCursor, Coverage: detailed.Coverage}, nil
}

// ListRequests implements ports.RequestCatalogReadPort with the common
// request view.
func (client *Client) ListRequests(ctx context.Context, connectionID domain.ConfigID, cursor string, limit int) (ports.Page[ports.RequestRecord], error) {
	detailed, err := client.ListRequestsDetailed(ctx, connectionID, cursor, limit)
	if err != nil {
		return ports.Page[ports.RequestRecord]{}, err
	}
	items := make([]ports.RequestRecord, 0, len(detailed.Items))
	for _, item := range detailed.Items {
		items = append(items, item.Record)
	}
	return ports.Page[ports.RequestRecord]{Items: items, NextCursor: detailed.NextCursor, Coverage: detailed.Coverage}, nil
}

// ListMediaDetailed reads one bounded page from Seerr's media catalog.
func (client *Client) ListMediaDetailed(ctx context.Context, connectionID domain.ConfigID, cursor string, requestedLimit int) (MediaPage, error) {
	if err := client.validateScope(connectionID); err != nil {
		return MediaPage{}, err
	}
	page, err := client.upstream.ListMedia(ctx, cursor, requestedLimit)
	if err != nil {
		return MediaPage{}, client.normalizeError(err)
	}
	return client.mapMediaPage(page), nil
}

// ListRequestsDetailed reads one bounded page from Seerr's request catalog.
func (client *Client) ListRequestsDetailed(ctx context.Context, connectionID domain.ConfigID, cursor string, requestedLimit int) (RequestPage, error) {
	if err := client.validateScope(connectionID); err != nil {
		return RequestPage{}, err
	}
	page, err := client.upstream.ListRequests(ctx, cursor, requestedLimit)
	if err != nil {
		return RequestPage{}, client.normalizeError(err)
	}
	return client.mapRequestPage(page), nil
}

func (client *Client) validateScope(connectionID domain.ConfigID) error {
	if !connectionID.Valid() || connectionID != client.config.ConnectionID {
		return domain.UpstreamError{Code: domain.OutcomeInvalidInput, Operation: "seerr.connection", Detail: "request scope is invalid"}
	}
	return nil
}

func (client *Client) mapMediaPage(page upstream.MediaPage) MediaPage {
	result := MediaPage{
		Items:      make([]MediaObservation, 0, len(page.Items)),
		NextCursor: page.NextCursor,
		Coverage:   client.mapCoverage(page.Coverage),
		PageInfo:   mapPageInfo(page.PageInfo),
	}
	for _, item := range page.Items {
		result.Items = append(result.Items, client.mapMedia(item))
	}
	return result
}

func (client *Client) mapRequestPage(page upstream.RequestPage) RequestPage {
	observedAt := page.Coverage.ObservedAt
	if observedAt.IsZero() {
		observedAt = time.Now().UTC()
	}
	result := RequestPage{
		Items:         make([]RequestObservation, 0, len(page.Items)),
		NextCursor:    page.NextCursor,
		Coverage:      client.mapCoverage(page.Coverage),
		PageInfo:      mapPageInfo(page.PageInfo),
		ServiceErrors: make([]ServiceError, 0, len(page.ServiceErrors)),
	}
	for _, item := range page.Items {
		result.Items = append(result.Items, client.mapRequest(item, observedAt))
	}
	for _, item := range page.ServiceErrors {
		result.ServiceErrors = append(result.ServiceErrors, ServiceError{Kind: item.Kind, ID: formatOptionalID(item.ID, item.IDKnown), Name: item.Name})
	}
	return result
}

func (client *Client) mapCoverage(value upstream.Coverage) domain.Coverage {
	completeness := domain.CompletenessUnknown
	switch value.Completeness {
	case upstream.CompletenessComplete:
		completeness = domain.CompletenessComplete
	case upstream.CompletenessPartial:
		completeness = domain.CompletenessPartial
	}
	return domain.Coverage{
		ConnectionID:  client.config.ConnectionID,
		Completeness:  completeness,
		ReasonCodes:   append([]string(nil), value.ReasonCodes...),
		ObservedCount: int64(value.ObservedCount),
		CompletedAt:   cloneTime(value.CompletedAt),
		ObservedAt:    value.ObservedAt,
	}
}

func mapPageInfo(value upstream.PageInfo) PageInfo {
	return PageInfo{Pages: value.Pages, PageSize: value.PageSize, Results: value.Results, Page: value.Page, Complete: value.Complete}
}

func (client *Client) mapMedia(value upstream.MediaObservation) MediaObservation {
	id := strconv.FormatInt(value.ID, 10)
	observedAt := value.Availability.ObservedAt
	if observedAt.IsZero() {
		observedAt = time.Now().UTC()
	}
	providerIDs := cloneStringMap(value.ProviderIDs)
	media := MediaObservation{
		ID:                    id,
		ScopedIdentity:        value.ScopedIdentity,
		MediaType:             value.MediaType,
		TMDBID:                value.TMDBID,
		TVDBID:                value.TVDBID,
		IMDBID:                value.IMDBID,
		ProviderIDs:           providerIDs,
		ProviderRelationships: mapProviderRelationships(value.ProviderRelationships),
		NativeStatus:          value.NativeStatus,
		NativeStatusName:      value.NativeStatusName,
		NativeStatus4K:        value.NativeStatus4K,
		NativeStatus4KName:    value.NativeStatus4KName,
		JellyfinMediaID:       value.JellyfinMediaID,
		JellyfinMediaID4K:     value.JellyfinMediaID4K,
		RatingKey:             value.RatingKey,
		RatingKey4K:           value.RatingKey4K,
		MediaAddedAt:          cloneTime(value.MediaAddedAt),
		SourceCreatedAt:       cloneTime(value.SourceCreatedAt),
		SourceUpdatedAt:       cloneTime(value.SourceUpdatedAt),
		Availability:          mapAvailability(value.Availability),
		Evidence:              append([]string(nil), value.Evidence...),
		Requests:              mapRequestSummaries(value.Requests),
		Seasons:               mapSeasons(value.Seasons),
		ServiceRelationships:  mapServiceRelationships(value.ServiceRelationships),
	}
	media.Record = ports.RequestRecord{
		ExternalID: id,
		ProviderID: firstProviderID(value.MediaType, providerIDs),
		Status:     value.NativeStatusName,
		MediaID:    id,
		ObservedAt: observedAt,
	}
	return media
}

func (client *Client) mapRequest(value upstream.RequestObservation, observedAt time.Time) RequestObservation {
	id := strconv.FormatInt(value.ID, 10)
	request := RequestObservation{
		ID:                   id,
		ScopedIdentity:       value.ScopedIdentity,
		Type:                 value.Type,
		NativeStatus:         value.NativeStatus,
		NativeStatusName:     value.NativeStatusName,
		Media:                client.mapMedia(value.Media),
		SeasonCount:          value.SeasonCount,
		Seasons:              mapSeasons(value.Seasons),
		Is4K:                 value.Is4K,
		ServerID:             formatOptionalID(value.ServerID, value.ServerIDKnown),
		ProfileID:            formatOptionalID(value.ProfileID, value.ProfileIDKnown),
		RootFolder:           value.RootFolder,
		LanguageProfileID:    formatOptionalID(value.LanguageProfileID, value.LanguageProfileKnown),
		Tags:                 append([]int64(nil), value.Tags...),
		IsAutoRequest:        value.IsAutoRequest,
		IgnoreQuota:          value.IgnoreQuota,
		ServiceRelationships: mapServiceRelationships(value.ServiceRelationships),
		SourceCreatedAt:      cloneTime(value.SourceCreatedAt),
		SourceUpdatedAt:      cloneTime(value.SourceUpdatedAt),
		Evidence:             append([]string(nil), value.Evidence...),
	}
	if !value.MediaKnown {
		request.Media = MediaObservation{}
	}
	mediaID := ""
	providerID := ""
	if value.MediaKnown {
		mediaID = strconv.FormatInt(value.Media.ID, 10)
		providerID = firstProviderID(value.Media.MediaType, value.Media.ProviderIDs)
	}
	request.Record = ports.RequestRecord{ExternalID: id, ProviderID: providerID, Status: value.NativeStatusName, MediaID: mediaID, ObservedAt: observedAt}
	return request
}

func mapAvailability(value upstream.AvailabilityObservation) AvailabilityObservation {
	return AvailabilityObservation{
		Known:              value.Known,
		Available:          value.Available,
		PartiallyAvailable: value.PartiallyAvailable,
		NativeStatus:       value.NativeStatus,
		NativeStatusName:   value.NativeStatusName,
		ObservedAt:         value.ObservedAt,
		Reason:             value.Reason,
	}
}

func mapProviderRelationships(values []upstream.ProviderRelationship) []ProviderRelationship {
	byProvider := make(map[string]ProviderRelationship, len(values))
	for _, value := range values {
		byProvider[value.Provider] = ProviderRelationship{Provider: value.Provider, ID: value.ID}
	}
	result := make([]ProviderRelationship, 0, len(values))
	for _, provider := range []string{"tmdb", "tvdb", "imdb"} {
		if value, ok := byProvider[provider]; ok {
			result = append(result, value)
			delete(byProvider, provider)
		}
	}
	remaining := make([]string, 0, len(byProvider))
	for provider := range byProvider {
		remaining = append(remaining, provider)
	}
	sort.Strings(remaining)
	for _, provider := range remaining {
		result = append(result, byProvider[provider])
	}
	return result
}

func mapServiceRelationships(values []upstream.ServiceRelationship) []ServiceRelationship {
	result := make([]ServiceRelationship, 0, len(values))
	for _, value := range values {
		result = append(result, ServiceRelationship{Kind: value.Kind, ServiceID: value.ServiceID, ExternalID: value.ExternalID, Slug: value.Slug, Is4K: value.Is4K})
	}
	return result
}

func mapRequestSummaries(values []upstream.RequestSummary) []RequestSummary {
	result := make([]RequestSummary, 0, len(values))
	for _, value := range values {
		result = append(result, RequestSummary{ID: strconv.FormatInt(value.ID, 10), NativeStatus: value.NativeStatus, NativeStatusName: value.NativeStatusName, Type: value.Type, Is4K: value.Is4K, CreatedAt: cloneTime(value.CreatedAt), UpdatedAt: cloneTime(value.UpdatedAt)})
	}
	return result
}

func mapSeasons(values []upstream.SeasonObservation) []SeasonObservation {
	result := make([]SeasonObservation, 0, len(values))
	for _, value := range values {
		result = append(result, SeasonObservation{ID: strconv.FormatInt(value.ID, 10), SeasonNumber: value.SeasonNumber, NativeStatus: value.NativeStatus, NativeStatusName: value.NativeStatusName, NativeStatus4K: value.NativeStatus4K, NativeStatus4KName: value.NativeStatus4KName, ObservedAt: value.ObservedAt})
	}
	return result
}

func firstProviderID(mediaType string, values map[string]string) string {
	preferred := []string{"tmdb", "tvdb", "imdb"}
	if strings.EqualFold(mediaType, "tv") || strings.EqualFold(mediaType, "series") {
		preferred = []string{"tvdb", "tmdb", "imdb"}
	}
	for _, key := range preferred {
		if value := values[key]; value != "" {
			return value
		}
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if values[key] != "" {
			return values[key]
		}
	}
	return ""
}

func formatOptionalID(value int64, known bool) string {
	if !known || value < 0 {
		return ""
	}
	return strconv.FormatInt(value, 10)
}

func cloneStringMap(values map[string]string) map[string]string {
	if values == nil {
		return nil
	}
	result := make(map[string]string, len(values))
	for key, value := range values {
		result[key] = value
	}
	return result
}

func cloneTime(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	copy := value.UTC()
	return &copy
}

func (client *Client) normalizeError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	var upstreamErr upstream.UpstreamError
	if !errors.As(err, &upstreamErr) {
		return domain.UpstreamError{Code: domain.OutcomeUnknown, Operation: "seerr", Detail: "upstream operation failed"}
	}
	code := domain.OutcomeUnknown
	switch upstreamErr.Code {
	case upstream.ErrorUnavailable:
		code = domain.OutcomeUnavailable
	case upstream.ErrorRateLimited:
		code = domain.OutcomeRateLimited
	case upstream.ErrorUnauthorized, upstream.ErrorForbidden:
		code = domain.OutcomeUnauthorized
	case upstream.ErrorInvalidInput:
		code = domain.OutcomeInvalidInput
	case upstream.ErrorConflict:
		code = domain.OutcomeConflict
	case upstream.ErrorUnsupported, upstream.ErrorNotFound:
		code = domain.OutcomeUnsupported
	case upstream.ErrorMalformed, upstream.ErrorResponseTooLarge, upstream.ErrorUnknown:
		code = domain.OutcomeUnknown
	}
	return domain.UpstreamError{Code: code, Status: upstreamErr.Status, Retryable: upstreamErr.Retryable, Operation: upstreamErr.Operation, Detail: "upstream operation failed"}
}

func upstreamCode(err error) (domain.UpstreamErrorCode, bool) {
	var value domain.UpstreamError
	if !errors.As(err, &value) {
		return "", false
	}
	return value.Code, true
}
