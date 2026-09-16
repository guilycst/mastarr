package seerr

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/guilycst/mastarr/internal/domain"
	"github.com/guilycst/mastarr/internal/ports"
)

type seerrFixture struct {
	Status            json.RawMessage   `json:"status"`
	MediaPages        []json.RawMessage `json:"mediaPages"`
	OverlapMediaPages []json.RawMessage `json:"overlapMediaPages"`
	DriftMediaPages   []json.RawMessage `json:"driftMediaPages"`
	RequestPages      []json.RawMessage `json:"requestPages"`
	DriftRequestPages []json.RawMessage `json:"driftRequestPages"`
}

type seerrFixtureHandler struct {
	mu sync.Mutex

	fixture          seerrFixture
	overlap          bool
	driftMedia       bool
	driftRequests    bool
	catalogNotFound  bool
	zeroPageInfo     bool
	statusCode       int
	statusBody       string
	requestLog       []string
	writeRequest     int
	mediaPageCalls   []url.Values
	requestPageCalls []url.Values
}

func (handler *seerrFixtureHandler) ServeHTTP(response http.ResponseWriter, request *http.Request) {
	handler.mu.Lock()
	handler.requestLog = append(handler.requestLog, request.Method+" "+request.URL.RequestURI())
	if request.Method != http.MethodGet {
		handler.writeRequest++
	}
	handler.mu.Unlock()
	if request.Header.Get("X-Api-Key") != "fixture-api-key" {
		response.WriteHeader(http.StatusUnauthorized)
		return
	}
	if handler.statusCode != 0 {
		response.WriteHeader(handler.statusCode)
		if handler.statusBody != "" {
			_, _ = response.Write([]byte(handler.statusBody))
		}
		return
	}

	switch request.URL.Path {
	case "/api/v1/status":
		writeRaw(response, handler.fixture.Status)
	case "/api/v1/media":
		query := request.URL.Query()
		handler.mu.Lock()
		handler.mediaPageCalls = append(handler.mediaPageCalls, query)
		handler.mu.Unlock()
		if handler.catalogNotFound {
			response.WriteHeader(http.StatusNotFound)
			return
		}
		if handler.zeroPageInfo {
			writeRaw(response, []byte(`{"pageInfo":{"pages":0,"pageSize":2,"results":0,"page":1},"results":[]}`))
			return
		}
		pages := handler.fixture.MediaPages
		if handler.driftMedia {
			pages = handler.fixture.DriftMediaPages
		}
		writeRaw(response, pageAt(query, pages, handler.fixture.OverlapMediaPages, handler.overlap))
	case "/api/v1/request":
		query := request.URL.Query()
		handler.mu.Lock()
		handler.requestPageCalls = append(handler.requestPageCalls, query)
		handler.mu.Unlock()
		if handler.catalogNotFound {
			response.WriteHeader(http.StatusNotFound)
			return
		}
		pages := handler.fixture.RequestPages
		if handler.driftRequests {
			pages = handler.fixture.DriftRequestPages
		}
		writeRaw(response, pageAt(query, pages, nil, false))
	default:
		response.WriteHeader(http.StatusNotFound)
	}
}

func pageAt(query url.Values, pages, alternate []json.RawMessage, useAlternate bool) []byte {
	if useAlternate {
		pages = alternate
	}
	if len(pages) == 0 {
		return []byte(`{"pageInfo":{"pages":1,"pageSize":1,"results":0,"page":1},"results":[]}`)
	}
	skip, _ := strconv.Atoi(query.Get("skip"))
	take, _ := strconv.Atoi(query.Get("take"))
	index := 0
	if take > 0 {
		index = skip / take
	}
	if index < 0 || index >= len(pages) {
		index = len(pages) - 1
	}
	return pages[index]
}

func loadSeerrFixture(t *testing.T) seerrFixture {
	t.Helper()
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	data, err := os.ReadFile(filepath.Join(filepath.Dir(source), "../../../tests/fixtures/seerr/media-pages.json"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	var fixture seerrFixture
	if err := json.Unmarshal(data, &fixture); err != nil {
		t.Fatalf("decode fixture: %v", err)
	}
	requestData, err := os.ReadFile(filepath.Join(filepath.Dir(source), "../../../tests/fixtures/seerr/request-pages.json"))
	if err != nil {
		t.Fatalf("read request fixture: %v", err)
	}
	var requests struct {
		RequestPages      []json.RawMessage `json:"requestPages"`
		DriftRequestPages []json.RawMessage `json:"driftRequestPages"`
	}
	if err := json.Unmarshal(requestData, &requests); err != nil {
		t.Fatalf("decode request fixture: %v", err)
	}
	fixture.RequestPages = requests.RequestPages
	fixture.DriftRequestPages = requests.DriftRequestPages
	return fixture
}

func writeRaw(response http.ResponseWriter, data []byte) {
	response.Header().Set("Content-Type", "application/json")
	_, _ = response.Write(data)
}

func newSeerrFixtureClient(t *testing.T, handler *seerrFixtureHandler, connectionID domain.ConfigID) (*Client, *httptest.Server) {
	t.Helper()
	server := httptest.NewServer(handler)
	client, err := New(Config{
		ConnectionID: connectionID,
		Endpoint:     server.URL,
		APIKey:       "fixture-api-key",
		MaxPageSize:  2,
		MaxPages:     10,
		MaxRecords:   50,
	})
	if err != nil {
		server.Close()
		t.Fatalf("New: %v", err)
	}
	return client, server
}

func assertSeerrCode(t *testing.T, err error, want domain.UpstreamErrorCode) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected upstream %s error", want)
	}
	var upstream domain.UpstreamError
	if !errors.As(err, &upstream) {
		t.Fatalf("error %T does not carry upstream evidence: %v", err, err)
	}
	if upstream.Code != want {
		t.Fatalf("upstream code = %s, want %s", upstream.Code, want)
	}
}

func hasReason(reasons []string, want string) bool {
	for _, reason := range reasons {
		if reason == want {
			return true
		}
	}
	return false
}

func TestMediaPaginationPreservesNativeEvidence(t *testing.T) {
	fixture := loadSeerrFixture(t)
	handler := &seerrFixtureHandler{fixture: fixture}
	client, server := newSeerrFixtureClient(t, handler, "seerr-main")
	defer server.Close()

	first, err := client.ListMediaDetailed(context.Background(), "seerr-main", "", 2)
	if err != nil {
		t.Fatalf("first media page: %v", err)
	}
	if len(first.Items) != 2 || first.NextCursor == "" {
		t.Fatalf("first media page = %d items, cursor %q", len(first.Items), first.NextCursor)
	}
	if !first.PageInfo.Complete || first.PageInfo.Page != 1 || first.PageInfo.Results != 4 {
		t.Fatalf("first page info = %+v", first.PageInfo)
	}
	if err := first.Coverage.Validate(); err != nil {
		t.Fatalf("first coverage validation: %v", err)
	}
	film := first.Items[0]
	if film.Record.ExternalID != "101" || film.Record.MediaID != "101" || film.ScopedIdentity != "seerr-main:101" || film.Record.ProviderID != "550" {
		t.Fatalf("film common record = %+v", film.Record)
	}
	if film.NativeStatus != int(MediaStatusAvailable) || film.NativeStatusName != "available" || !film.Availability.Available || film.Availability.PartiallyAvailable {
		t.Fatalf("film availability/status = %+v / %+v", film.Availability, film.NativeStatusName)
	}
	if len(film.ProviderRelationships) != 2 || film.ProviderRelationships[0].Provider != "tmdb" {
		t.Fatalf("film providers = %+v", film.ProviderRelationships)
	}
	if len(film.ServiceRelationships) != 1 || film.ServiceRelationships[0].Kind != "radarr" {
		t.Fatalf("film services = %+v", film.ServiceRelationships)
	}
	second, err := client.ListMediaDetailed(context.Background(), "seerr-main", first.NextCursor, 2)
	if err != nil {
		t.Fatalf("second media page: %v", err)
	}
	if len(second.Items) != 2 || second.NextCursor != "" {
		t.Fatalf("second media page = %d items, cursor %q", len(second.Items), second.NextCursor)
	}
	if second.Items[1].NativeStatus4K != 99 || second.Items[1].NativeStatus4KName != "unknown" {
		t.Fatalf("unknown native 4k status = %+v", second.Items[1])
	}
	series := first.Items[1]
	if series.Record.ProviderID != "2002" {
		t.Fatalf("series provider id = %q", series.Record.ProviderID)
	}
	if series.JellyfinMediaID4K != "jf-series-102-4k" || series.RatingKey4K != "fixture-series-102-4k" || len(series.ServiceRelationships) != 2 || series.ServiceRelationships[1].Slug != "sonarr-4k" || !series.ServiceRelationships[1].Is4K {
		t.Fatalf("4k relationships = %+v", series)
	}
	if second.Coverage.Completeness != domain.CompletenessPartial || second.Coverage.ObservedCount != 4 || second.Coverage.CompletedAt == nil || !hasReason(second.Coverage.ReasonCodes, "pagination_snapshot_unverified") {
		t.Fatalf("second coverage = %+v", second.Coverage)
	}
	if err := second.Coverage.Validate(); err != nil {
		t.Fatalf("second coverage validation: %v", err)
	}
	handler.mu.Lock()
	defer handler.mu.Unlock()
	if len(handler.mediaPageCalls) != 2 || handler.mediaPageCalls[0].Get("take") != "2" || handler.mediaPageCalls[0].Get("skip") != "0" || handler.mediaPageCalls[1].Get("skip") != "2" {
		t.Fatalf("media pagination calls = %+v", handler.mediaPageCalls)
	}
	for _, request := range handler.requestLog {
		if strings.HasPrefix(request, "POST ") || strings.HasPrefix(request, "PUT ") || strings.HasPrefix(request, "PATCH ") || strings.HasPrefix(request, "DELETE ") {
			t.Fatalf("read adapter issued write request %q", request)
		}
	}
}

func TestMediaOverlapIsDeduplicatedAndPartial(t *testing.T) {
	fixture := loadSeerrFixture(t)
	handler := &seerrFixtureHandler{fixture: fixture, overlap: true}
	client, server := newSeerrFixtureClient(t, handler, "seerr-main")
	defer server.Close()

	first, err := client.ListMediaDetailed(context.Background(), "seerr-main", "", 2)
	if err != nil {
		t.Fatalf("first media page: %v", err)
	}
	second, err := client.ListMediaDetailed(context.Background(), "seerr-main", first.NextCursor, 2)
	if err != nil {
		t.Fatalf("second media page: %v", err)
	}
	if len(second.Items) != 1 || second.Items[0].ID != "103" {
		t.Fatalf("deduplicated second page = %+v", second.Items)
	}
	if second.Coverage.Completeness != domain.CompletenessPartial || !hasReason(second.Coverage.ReasonCodes, "pagination_overlap") {
		t.Fatalf("overlap coverage = %+v", second.Coverage)
	}
}

func TestRequestPaginationPreservesStatusRelationshipsAndServiceErrors(t *testing.T) {
	fixture := loadSeerrFixture(t)
	handler := &seerrFixtureHandler{fixture: fixture}
	client, server := newSeerrFixtureClient(t, handler, "seerr-main")
	defer server.Close()

	first, err := client.ListRequestsDetailed(context.Background(), "seerr-main", "", 2)
	if err != nil {
		t.Fatalf("first request page: %v", err)
	}
	if len(first.Items) != 2 || first.NextCursor == "" {
		t.Fatalf("first request page = %d items, cursor %q", len(first.Items), first.NextCursor)
	}
	if len(first.ServiceErrors) != 1 || first.ServiceErrors[0].Kind != "radarr" || first.ServiceErrors[0].ID != "99" {
		t.Fatalf("service errors = %+v", first.ServiceErrors)
	}
	request := first.Items[1]
	if request.Record.ExternalID != "9002" || request.Record.MediaID != "102" || request.ScopedIdentity != "seerr-main:9002" || request.NativeStatus != int(RequestStatusApproved) || request.NativeStatusName != "approved" {
		t.Fatalf("request = %+v", request)
	}
	if request.Media.Availability.Known != true || !request.Media.Availability.PartiallyAvailable || request.Media.Availability.Available {
		t.Fatalf("nested media availability = %+v", request.Media.Availability)
	}
	if len(request.Media.ProviderRelationships) != 3 || len(request.ServiceRelationships) != 2 || request.ServiceRelationships[0].Kind != "sonarr" || !request.Is4K || len(request.Tags) != 1 || request.Tags[0] != 30 {
		t.Fatalf("request relationships/fields = %+v / %+v / %+v", request.Media.ProviderRelationships, request.ServiceRelationships, request)
	}

	second, err := client.ListRequestsDetailed(context.Background(), "seerr-main", first.NextCursor, 2)
	if err != nil {
		t.Fatalf("second request page: %v", err)
	}
	if len(second.Items) != 1 || second.NextCursor != "" || second.Coverage.Completeness != domain.CompletenessPartial || !hasReason(second.Coverage.ReasonCodes, "pagination_snapshot_unverified") {
		t.Fatalf("second request page = %+v", second)
	}
	if err := second.Coverage.Validate(); err != nil {
		t.Fatalf("second request coverage validation: %v", err)
	}
	handler.mu.Lock()
	defer handler.mu.Unlock()
	if len(handler.requestPageCalls) != 2 || handler.requestPageCalls[1].Get("skip") != "2" {
		t.Fatalf("request pagination calls = %+v", handler.requestPageCalls)
	}
}

func TestCapabilitiesExposeUnsupportedVersionAndNoWrites(t *testing.T) {
	fixture := loadSeerrFixture(t)
	handler := &seerrFixtureHandler{fixture: fixture, statusCode: http.StatusNotFound}
	client, server := newSeerrFixtureClient(t, handler, "seerr-main")
	defer server.Close()

	_, err := client.Version(context.Background(), "seerr-main")
	assertSeerrCode(t, err, domain.OutcomeUnsupported)
	capabilities, err := client.Capabilities(context.Background(), "seerr-main")
	if err != nil {
		t.Fatalf("Capabilities: %v", err)
	}
	states := make(map[string]domain.CapabilityState, len(capabilities))
	for _, capability := range capabilities {
		states[capability.Name] = capability.State
	}
	if states["seerr.version"] != domain.CapabilityUnsupported || states["seerr.media.read"] != domain.CapabilityUnknown || states["seerr.requests.read"] != domain.CapabilityUnknown || states["seerr.writes"] != domain.CapabilityUnsupported {
		t.Fatalf("capability states = %+v", states)
	}
	handler.mu.Lock()
	defer handler.mu.Unlock()
	if handler.writeRequest != 0 {
		t.Fatalf("write requests = %d", handler.writeRequest)
	}
}

func TestCapabilitiesKeepUnpinnedCatalogUnknown(t *testing.T) {
	fixture := loadSeerrFixture(t)
	fixture.Status = json.RawMessage(`{"version":"999.0.0","commitTag":"future","commit":"future-commit"}`)
	handler := &seerrFixtureHandler{fixture: fixture, catalogNotFound: true}
	client, server := newSeerrFixtureClient(t, handler, "seerr-main")
	defer server.Close()
	capabilities, err := client.Capabilities(context.Background(), "seerr-main")
	if err != nil {
		t.Fatalf("Capabilities: %v", err)
	}
	for _, capability := range capabilities {
		if capability.Name == "seerr.media.read" || capability.Name == "seerr.requests.read" || capability.Name == "seerr.provider-relationships" {
			if capability.State != domain.CapabilityUnknown || capability.Reason == "" {
				t.Fatalf("unverified capability = %+v", capability)
			}
		}
	}
}

func TestOffsetTraversalWithoutSnapshotBoundaryStaysPartial(t *testing.T) {
	fixture := loadSeerrFixture(t)
	mediaHandler := &seerrFixtureHandler{fixture: fixture, driftMedia: true}
	mediaClient, mediaServer := newSeerrFixtureClient(t, mediaHandler, "seerr-main")
	defer mediaServer.Close()
	firstMedia, err := mediaClient.ListMediaDetailed(context.Background(), "seerr-main", "", 2)
	if err != nil {
		t.Fatalf("first media page: %v", err)
	}
	secondMedia, err := mediaClient.ListMediaDetailed(context.Background(), "seerr-main", firstMedia.NextCursor, 2)
	if err != nil {
		t.Fatalf("second media page: %v", err)
	}
	if len(secondMedia.Items) != 2 || secondMedia.Items[0].ID != "104" || secondMedia.Items[1].ID != "105" || secondMedia.Coverage.Completeness != domain.CompletenessPartial || !hasReason(secondMedia.Coverage.ReasonCodes, "pagination_snapshot_unverified") {
		t.Fatalf("media drift coverage = %+v", secondMedia)
	}

	requestHandler := &seerrFixtureHandler{fixture: fixture, driftRequests: true}
	requestClient, requestServer := newSeerrFixtureClient(t, requestHandler, "seerr-main")
	defer requestServer.Close()
	firstRequest, err := requestClient.ListRequestsDetailed(context.Background(), "seerr-main", "", 2)
	if err != nil {
		t.Fatalf("first request page: %v", err)
	}
	secondRequest, err := requestClient.ListRequestsDetailed(context.Background(), "seerr-main", firstRequest.NextCursor, 2)
	if err != nil {
		t.Fatalf("second request page: %v", err)
	}
	if len(secondRequest.Items) != 2 || secondRequest.Items[0].ID != "9004" || secondRequest.Items[1].ID != "9005" || secondRequest.Coverage.Completeness != domain.CompletenessPartial || !hasReason(secondRequest.Coverage.ReasonCodes, "pagination_snapshot_unverified") {
		t.Fatalf("request drift coverage = %+v", secondRequest)
	}
}

func TestSeerrErrorsScopeAndCursor(t *testing.T) {
	fixture := loadSeerrFixture(t)
	handler := &seerrFixtureHandler{fixture: fixture}
	client, server := newSeerrFixtureClient(t, handler, "seerr-main")
	defer server.Close()

	_, err := client.ListMedia(context.Background(), "seerr-other", "", 2)
	assertSeerrCode(t, err, domain.OutcomeInvalidInput)
	page, err := client.ListMedia(context.Background(), "seerr-main", "", 2)
	if err != nil {
		t.Fatalf("ListMedia: %v", err)
	}
	if page.NextCursor == "" {
		t.Fatal("expected continuation cursor")
	}
	tampered := page.NextCursor + "x"
	_, err = client.ListMedia(context.Background(), "seerr-main", tampered, 2)
	assertSeerrCode(t, err, domain.OutcomeInvalidInput)
	_, err = client.ListMedia(context.Background(), "seerr-main", "", 3)
	assertSeerrCode(t, err, domain.OutcomeInvalidInput)
}

func TestSeerrConnectionScopedIdentity(t *testing.T) {
	fixture := loadSeerrFixture(t)
	left, leftServer := newSeerrFixtureClient(t, &seerrFixtureHandler{fixture: fixture}, "seerr-one")
	defer leftServer.Close()
	right, rightServer := newSeerrFixtureClient(t, &seerrFixtureHandler{fixture: fixture}, "seerr-two")
	defer rightServer.Close()
	if left.ScopedIdentity("101") == right.ScopedIdentity("101") || left.ScopedIdentity("101") != "seerr-one:101" || right.ScopedIdentity("101") != "seerr-two:101" {
		t.Fatalf("scoped identities = %q and %q", left.ScopedIdentity("101"), right.ScopedIdentity("101"))
	}
}

func TestSeerrUnauthorizedAndRateLimited(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/api/v1/media" {
			response.WriteHeader(http.StatusTooManyRequests)
			return
		}
		response.WriteHeader(http.StatusUnauthorized)
	}))
	defer server.Close()
	client, err := New(Config{ConnectionID: "seerr-main", Endpoint: server.URL, APIKey: "wrong", MaxPageSize: 2})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_, err = client.Version(context.Background(), "seerr-main")
	assertSeerrCode(t, err, domain.OutcomeUnauthorized)
	client, err = New(Config{ConnectionID: "seerr-main", Endpoint: server.URL, APIKey: "fixture-api-key", MaxPageSize: 2})
	if err != nil {
		t.Fatalf("New rate-limited: %v", err)
	}
	_, err = client.ListMedia(context.Background(), "seerr-main", "", 2)
	assertSeerrCode(t, err, domain.OutcomeRateLimited)
}

type seerrTimeoutTransport struct{}

func (seerrTimeoutTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, seerrTimeoutError{}
}

type seerrTimeoutError struct{}

func (seerrTimeoutError) Error() string   { return "fixture timeout" }
func (seerrTimeoutError) Timeout() bool   { return true }
func (seerrTimeoutError) Temporary() bool { return true }

func TestSeerrTimeoutAndMalformedResponse(t *testing.T) {
	timeoutClient, err := New(Config{ConnectionID: "seerr-main", Endpoint: "http://fixture.invalid", APIKey: "fixture-api-key", HTTPClient: &http.Client{Transport: seerrTimeoutTransport{}}})
	if err != nil {
		t.Fatalf("New timeout client: %v", err)
	}
	_, err = timeoutClient.Version(context.Background(), "seerr-main")
	assertSeerrCode(t, err, domain.OutcomeUnavailable)

	malformedServer := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Header.Get("X-Api-Key") != "fixture-api-key" {
			response.WriteHeader(http.StatusUnauthorized)
			return
		}
		response.Header().Set("Content-Type", "application/json")
		_, _ = response.Write([]byte("{"))
	}))
	defer malformedServer.Close()
	malformedClient, err := New(Config{ConnectionID: "seerr-main", Endpoint: malformedServer.URL, APIKey: "fixture-api-key"})
	if err != nil {
		t.Fatalf("New malformed client: %v", err)
	}
	_, err = malformedClient.ListRequests(context.Background(), "seerr-main", "", 2)
	assertSeerrCode(t, err, domain.OutcomeUnknown)
}

func TestSeerrEmptyMalformedAndPaginationBounds(t *testing.T) {
	emptyHandler := &seerrFixtureHandler{zeroPageInfo: true}
	emptyClient, emptyServer := newSeerrFixtureClient(t, emptyHandler, "seerr-main")
	defer emptyServer.Close()
	empty, err := emptyClient.ListMediaDetailed(context.Background(), "seerr-main", "", 2)
	if err != nil || len(empty.Items) != 0 || empty.NextCursor != "" || empty.Coverage.Completeness != domain.CompletenessComplete {
		t.Fatalf("empty media = %+v, err %v", empty, err)
	}

	fixture := loadSeerrFixture(t)
	boundedHandler := &seerrFixtureHandler{fixture: fixture}
	server := httptest.NewServer(boundedHandler)
	defer server.Close()
	pageLimited, err := New(Config{ConnectionID: "seerr-main", Endpoint: server.URL, APIKey: "fixture-api-key", MaxPageSize: 2, MaxPages: 1, MaxRecords: 50})
	if err != nil {
		t.Fatalf("New page-limited: %v", err)
	}
	page, err := pageLimited.ListMediaDetailed(context.Background(), "seerr-main", "", 2)
	if err != nil || len(page.Items) != 2 || page.NextCursor != "" || !hasReason(page.Coverage.ReasonCodes, "pagination_limit") {
		t.Fatalf("page bound = %+v, err %v", page, err)
	}
	recordLimited, err := New(Config{ConnectionID: "seerr-main", Endpoint: server.URL, APIKey: "fixture-api-key", MaxPageSize: 2, MaxPages: 10, MaxRecords: 3})
	if err != nil {
		t.Fatalf("New record-limited: %v", err)
	}
	limitedFirst, err := recordLimited.ListMediaDetailed(context.Background(), "seerr-main", "", 2)
	if err != nil || len(limitedFirst.Items) != 2 || limitedFirst.NextCursor == "" {
		t.Fatalf("record-bound first page = %+v, err %v", limitedFirst, err)
	}
	limited, err := recordLimited.ListMediaDetailed(context.Background(), "seerr-main", limitedFirst.NextCursor, 2)
	if err != nil || len(limited.Items) != 1 || limited.Coverage.ObservedCount != 3 || limited.NextCursor != "" || !hasReason(limited.Coverage.ReasonCodes, "records_limit") {
		t.Fatalf("record bound = %+v, err %v", limited, err)
	}

	malformedServer := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Content-Type", "application/json")
		_, _ = response.Write([]byte("{"))
	}))
	defer malformedServer.Close()
	malformedClient, err := New(Config{ConnectionID: "seerr-main", Endpoint: malformedServer.URL, APIKey: "fixture-api-key", MaxPageSize: 2})
	if err != nil {
		t.Fatalf("New malformed: %v", err)
	}
	_, err = malformedClient.ListMedia(context.Background(), "seerr-main", "", 2)
	assertSeerrCode(t, err, domain.OutcomeUnknown)
}

var _ ports.RequestCatalogReadPort = (*Client)(nil)
