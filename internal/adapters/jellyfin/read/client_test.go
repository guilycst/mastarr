package read

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/guilycst/mastarr/internal/domain"
	"github.com/guilycst/mastarr/internal/ports"
)

const (
	apiSystemPublic = "/System/Info/Public"
	apiMediaFolders = "/Library/MediaFolders"
	apiViews        = "/UserViews"
	apiItems        = "/Items"
)

type jellyfinFixture struct {
	System             json.RawMessage            `json:"system"`
	Libraries          json.RawMessage            `json:"libraries"`
	DuplicateLibraries json.RawMessage            `json:"duplicateLibraries"`
	EmptyItems         json.RawMessage            `json:"emptyItems"`
	SourceVariants     map[string]json.RawMessage `json:"sourceVariants"`
	MovieItems         json.RawMessage            `json:"movieItems"`
	SeriesItems        json.RawMessage            `json:"seriesItems"`
	SingleItem         json.RawMessage            `json:"singleItem"`
	PathOnlyItem       json.RawMessage            `json:"pathOnlyItem"`
}

type jellyfinFixtureHandler struct {
	mu sync.Mutex

	fixture            jellyfinFixture
	fallback           bool
	omitTotal          bool
	duplicateLibraries bool
	requestLog         []string
	libraryCalls       int
	itemCalls          int
}

func (handler *jellyfinFixtureHandler) ServeHTTP(response http.ResponseWriter, request *http.Request) {
	handler.mu.Lock()
	handler.requestLog = append(handler.requestLog, request.Method+" "+request.URL.RequestURI())
	handler.mu.Unlock()
	if request.Header.Get("X-Emby-Token") != "fixture-api-key" {
		response.WriteHeader(http.StatusUnauthorized)
		return
	}
	switch request.URL.Path {
	case apiSystemPublic:
		writeJellyfinRaw(response, handler.fixture.System)
	case apiMediaFolders:
		handler.mu.Lock()
		handler.libraryCalls++
		handler.mu.Unlock()
		if handler.fallback {
			response.WriteHeader(http.StatusNotFound)
			return
		}
		if handler.duplicateLibraries {
			writeJellyfinRaw(response, handler.fixture.DuplicateLibraries)
			return
		}
		writeJellyfinRaw(response, handler.fixture.Libraries)
	case apiViews:
		writeJellyfinRaw(response, handler.fixture.Libraries)
	case apiItems:
		handler.mu.Lock()
		handler.itemCalls++
		handler.mu.Unlock()
		query := request.URL.Query()
		if query.Get("ParentId") == "library-duplicate" {
			writeJellyfinRaw(response, handler.fixture.EmptyItems)
			return
		}
		if query.Get("Ids") != "" {
			if variant, ok := handler.fixture.SourceVariants[query.Get("Ids")]; ok {
				writeJellyfinRaw(response, variant)
				return
			}
			if query.Get("Ids") == "jf-path-only" {
				writeJellyfinRaw(response, handler.fixture.PathOnlyItem)
				return
			}
			writeJellyfinRaw(response, handler.fixture.SingleItem)
			return
		}
		if query.Get("ParentId") == "library-series" {
			writeJellyfinRaw(response, handler.fixture.SeriesItems)
			return
		}
		if handler.omitTotal {
			var envelope map[string]any
			if err := json.Unmarshal(handler.fixture.MovieItems, &envelope); err == nil {
				delete(envelope, "TotalRecordCount")
				if data, marshalErr := json.Marshal(envelope); marshalErr == nil {
					writeJellyfinRaw(response, data)
					return
				}
			}
		}
		writeJellyfinRaw(response, handler.fixture.MovieItems)
	default:
		if strings.HasPrefix(request.URL.Path, "/Users/") && strings.HasSuffix(request.URL.Path, "/Views") {
			writeJellyfinRaw(response, handler.fixture.Libraries)
			return
		}
		response.WriteHeader(http.StatusNotFound)
	}
}

func loadJellyfinFixture(t *testing.T) jellyfinFixture {
	t.Helper()
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	data, err := os.ReadFile(filepath.Join(filepath.Dir(source), "../../../../tests/fixtures/jellyfin/read.json"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	var fixture jellyfinFixture
	if err := json.Unmarshal(data, &fixture); err != nil {
		t.Fatalf("decode fixture: %v", err)
	}
	return fixture
}

func writeJellyfinRaw(response http.ResponseWriter, data []byte) {
	response.Header().Set("Content-Type", "application/json")
	_, _ = response.Write(data)
}

func newJellyfinFixtureClient(t *testing.T, handler *jellyfinFixtureHandler, connectionID domain.ConfigID, mappings []domain.PathMapping) (*Client, *httptest.Server) {
	t.Helper()
	server := httptest.NewServer(handler)
	client, err := New(Config{
		ConnectionID: connectionID,
		Endpoint:     server.URL,
		APIKey:       "fixture-api-key",
		Mappings:     mappings,
		MaxPageSize:  1,
		MaxPages:     10,
		MaxItems:     50,
	})
	if err != nil {
		server.Close()
		t.Fatalf("New: %v", err)
	}
	return client, server
}

func assertJellyfinCode(t *testing.T, err error, want domain.UpstreamErrorCode) {
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

func hasJellyfinReason(reasons []string, want string) bool {
	for _, reason := range reasons {
		if reason == want {
			return true
		}
	}
	return false
}

func TestJellyfinLibrariesItemsProvidersAndMappedAvailability(t *testing.T) {
	fixture := loadJellyfinFixture(t)
	handler := &jellyfinFixtureHandler{fixture: fixture}
	client, server := newJellyfinFixtureClient(t, handler, "jellyfin-main", []domain.PathMapping{{
		ConnectionID: "jellyfin-main", SourcePrefix: "/fixture/media", RootID: "library",
	}})
	defer server.Close()

	version, err := client.Version(context.Background(), "jellyfin-main")
	if err != nil || version.Version != "10.10.7" {
		t.Fatalf("version = %+v, err %v", version, err)
	}
	libraries, err := client.Libraries(context.Background(), "jellyfin-main")
	if err != nil || len(libraries) != 2 || libraries[0].ExternalID != "library-movies" {
		t.Fatalf("libraries = %+v, err %v", libraries, err)
	}
	item, err := client.ObserveItem(context.Background(), "jellyfin-main", "jf-film-101")
	if err != nil {
		t.Fatalf("ObserveItem: %v", err)
	}
	if !item.Item.Playable || item.Item.ExternalID != "jf-film-101" || item.Item.ProviderID != "550" || len(item.ProviderRelationships) != 2 {
		t.Fatalf("item = %+v", item)
	}
	if len(item.MediaSources) != 1 || item.MediaSources[0].MappedTarget == nil || item.MediaSources[0].MappedTarget.RelativePath != "Movies/Fixture Film (1999)/Fixture Film.mkv" {
		t.Fatalf("media sources = %+v", item.MediaSources)
	}
	pathOnlyClient, pathOnlyServer := newJellyfinFixtureClient(t, handler, "jellyfin-main", nil)
	defer pathOnlyServer.Close()
	pathOnly, err := pathOnlyClient.ObserveItem(context.Background(), "jellyfin-main", "jf-path-only")
	if err != nil || pathOnly.Item.Playable || !hasJellyfinReason(pathOnly.Evidence, "media_source_from_item_path") || !hasJellyfinReason(pathOnly.Evidence, "media_source_path_only_unverified") {
		t.Fatalf("path-only item = %+v, err %v", pathOnly, err)
	}
	mappedPathOnly, err := client.ObserveItem(context.Background(), "jellyfin-main", "jf-path-only")
	if err != nil || mappedPathOnly.Item.Playable || len(mappedPathOnly.MediaSources) != 1 || mappedPathOnly.MediaSources[0].MappedTarget == nil || !hasJellyfinReason(mappedPathOnly.Evidence, "media_source_path_only_unverified") {
		t.Fatalf("mapped path-only item = %+v, err %v", mappedPathOnly, err)
	}

	first, err := client.ListDetailed(context.Background(), "jellyfin-main", "", 1)
	if err != nil {
		t.Fatalf("first inventory page: %v", err)
	}
	if len(first.Items) != 1 || first.NextCursor == "" || first.Coverage.Completeness != domain.CompletenessPartial {
		t.Fatalf("first inventory page = %+v", first)
	}
	second, err := client.ListDetailed(context.Background(), "jellyfin-main", first.NextCursor, 1)
	if err != nil {
		t.Fatalf("second inventory page: %v", err)
	}
	if len(second.Items) != 1 || second.NextCursor != "" || second.Coverage.Completeness != domain.CompletenessPartial || second.Coverage.ObservedCount != 2 || !hasJellyfinReason(second.Coverage.ReasonCodes, "pagination_snapshot_unverified") {
		t.Fatalf("second inventory page = %+v", second)
	}
	handler.mu.Lock()
	defer handler.mu.Unlock()
	for _, request := range handler.requestLog {
		if !strings.HasPrefix(request, "GET ") {
			t.Fatalf("non-read request = %q", request)
		}
	}
}

func TestJellyfinFallbackMappingAndUnsupportedRefresh(t *testing.T) {
	fixture := loadJellyfinFixture(t)
	handler := &jellyfinFixtureHandler{fixture: fixture, fallback: true}
	client, server := newJellyfinFixtureClient(t, handler, "jellyfin-main", nil)
	defer server.Close()

	libraries, err := client.ListLibraries(context.Background(), "jellyfin-main")
	if err != nil || len(libraries) != 2 {
		t.Fatalf("fallback libraries = %+v, err %v", libraries, err)
	}
	item, err := client.ObserveItem(context.Background(), "jellyfin-main", "jf-film-101")
	if err != nil {
		t.Fatalf("ObserveItem without mapping: %v", err)
	}
	if !item.Item.Playable || !hasJellyfinReason(item.Evidence, "media_source_present") {
		t.Fatalf("unmapped source = %+v", item)
	}
	_, err = client.Refresh(context.Background(), "jellyfin-main", ports.RefreshRequest{Scope: ports.RefreshItem, ExternalID: "jf-film-101"})
	assertJellyfinCode(t, err, domain.OutcomeUnsupported)
	capabilities, err := client.Capabilities(context.Background(), "jellyfin-main")
	if err != nil {
		t.Fatalf("Capabilities: %v", err)
	}
	var refresh domain.CapabilityState
	for _, capability := range capabilities {
		if capability.Name == "jellyfin.refresh" {
			refresh = capability.State
		}
	}
	if refresh != domain.CapabilityUnsupported {
		t.Fatalf("refresh capability = %s", refresh)
	}
	_, err = client.Capabilities(context.Background(), "other")
	assertJellyfinCode(t, err, domain.OutcomeInvalidInput)
}

func TestJellyfinCapabilitiesKeepUnpinnedCatalogUnknown(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Header.Get("X-Emby-Token") != "fixture-api-key" {
			response.WriteHeader(http.StatusUnauthorized)
			return
		}
		if request.URL.Path == apiSystemPublic {
			writeJellyfinRaw(response, []byte(`{"ProductName":"Jellyfin","Version":"999.0.0"}`))
			return
		}
		response.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()
	client, err := New(Config{ConnectionID: "jellyfin-main", Endpoint: server.URL, APIKey: "fixture-api-key"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	capabilities, err := client.Capabilities(context.Background(), "jellyfin-main")
	if err != nil {
		t.Fatalf("Capabilities: %v", err)
	}
	for _, capability := range capabilities {
		if capability.Name == "jellyfin.libraries" || capability.Name == "jellyfin.items" || capability.Name == "jellyfin.provider-ids" || capability.Name == "jellyfin.playable-media" {
			if capability.State != domain.CapabilityUnknown || capability.Reason == "" {
				t.Fatalf("unverified capability = %+v", capability)
			}
		}
	}
}

func TestJellyfinDuplicateLibraryIdentityIsPartial(t *testing.T) {
	fixture := loadJellyfinFixture(t)
	client, server := newJellyfinFixtureClient(t, &jellyfinFixtureHandler{fixture: fixture, duplicateLibraries: true}, "jellyfin-main", nil)
	defer server.Close()
	_, err := client.ListDetailed(context.Background(), "jellyfin-main", "", 1)
	// The standalone client rejects duplicate native library IDs before the
	// adapter can aggregate them. A malformed identity is therefore an
	// explicit unknown result, rather than a partial absence claim.
	assertJellyfinCode(t, err, domain.OutcomeUnknown)
}

func TestJellyfinUnsupportedNativeSourceShapesRemainUnavailable(t *testing.T) {
	fixture := loadJellyfinFixture(t)
	client, server := newJellyfinFixtureClient(t, &jellyfinFixtureHandler{fixture: fixture}, "jellyfin-main", []domain.PathMapping{{
		ConnectionID: "jellyfin-main", SourcePrefix: "/fixture/media", RootID: "library",
	}})
	defer server.Close()
	cases := []struct {
		id                string
		reason            string
		unavailableReason string
	}{
		{id: "jf-source-remote", reason: "media_source_location_unsupported", unavailableReason: "playable_media_unverified"},
		{id: "jf-source-virtual", reason: "media_source_location_unsupported", unavailableReason: "playable_media_unverified"},
		{id: "jf-source-offline", reason: "media_source_location_unsupported", unavailableReason: "playable_media_unverified"},
		{id: "jf-item-remote", reason: "location_not_playable", unavailableReason: "location_not_playable"},
		{id: "jf-item-virtual", reason: "location_not_playable", unavailableReason: "location_not_playable"},
		{id: "jf-item-offline", reason: "location_not_playable", unavailableReason: "location_not_playable"},
		{id: "jf-source-http", reason: "media_source_protocol_unsupported", unavailableReason: "playable_media_unverified"},
		{id: "jf-source-missing-protocol", reason: "media_source_protocol_missing", unavailableReason: "playable_media_unverified"},
		{id: "jf-source-missing-location", reason: "media_source_location_missing", unavailableReason: "playable_media_unverified"},
		{id: "jf-source-missing-media-type", reason: "media_source_media_type_missing", unavailableReason: "playable_media_unverified"},
		{id: "jf-item-missing-media-type", reason: "item_media_type_missing", unavailableReason: "playable_media_unverified"},
		{id: "jf-source-missing-path", reason: "media_source_path_missing", unavailableReason: "playable_media_unverified"},
		{id: "jf-source-invalid-path", reason: "media_source_path_invalid", unavailableReason: "playable_media_unverified"},
		{id: "jf-source-mismatch", reason: "media_source_media_type_mismatch", unavailableReason: "playable_media_unverified"},
	}
	for _, testCase := range cases {
		t.Run(testCase.id, func(t *testing.T) {
			item, err := client.ObserveItem(context.Background(), "jellyfin-main", testCase.id)
			if err != nil {
				t.Fatalf("ObserveItem: %v", err)
			}
			if item.Item.Playable || !hasJellyfinReason(item.Evidence, testCase.reason) || item.UnavailableReason != testCase.unavailableReason {
				t.Fatalf("unsupported source = %+v, want reason %q", item, testCase.reason)
			}
		})
	}
}

func TestJellyfinMissingSourceIdentityIsMalformed(t *testing.T) {
	fixture := loadJellyfinFixture(t)
	client, server := newJellyfinFixtureClient(t, &jellyfinFixtureHandler{fixture: fixture}, "jellyfin-main", nil)
	defer server.Close()
	_, err := client.ObserveItem(context.Background(), "jellyfin-main", "jf-source-missing-id")
	assertJellyfinCode(t, err, domain.OutcomeUnknown)
}

func TestJellyfinAmbiguousMappingRemainsUnavailable(t *testing.T) {
	fixture := loadJellyfinFixture(t)
	client, server := newJellyfinFixtureClient(t, &jellyfinFixtureHandler{fixture: fixture}, "jellyfin-main", []domain.PathMapping{
		{ConnectionID: "jellyfin-main", SourcePrefix: "/fixture/media", RootID: "library-a"},
		{ConnectionID: "jellyfin-main", SourcePrefix: "/fixture/media", RootID: "library-b"},
	})
	defer server.Close()
	item, err := client.ObserveItem(context.Background(), "jellyfin-main", "jf-film-101")
	if err != nil {
		t.Fatalf("ObserveItem: %v", err)
	}
	if item.Item.Playable || item.UnavailableReason != "playable_media_unverified" || !hasJellyfinReason(item.Evidence, "media_source_mapping_ambiguous") {
		t.Fatalf("ambiguous mapping item = %+v", item)
	}
}

func TestJellyfinUserScopeIsSentToReadRoutes(t *testing.T) {
	fixture := loadJellyfinFixture(t)
	handler := &jellyfinFixtureHandler{fixture: fixture, fallback: true}
	server := httptest.NewServer(handler)
	defer server.Close()
	client, err := New(Config{ConnectionID: "jellyfin-main", Endpoint: server.URL, APIKey: "fixture-api-key", UserID: "fixture-user"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := client.Libraries(context.Background(), "jellyfin-main"); err != nil {
		t.Fatalf("Libraries: %v", err)
	}
	if _, err := client.ObserveItem(context.Background(), "jellyfin-main", "jf-film-101"); err != nil {
		t.Fatalf("ObserveItem: %v", err)
	}
	handler.mu.Lock()
	defer handler.mu.Unlock()
	var sawViews, sawItem bool
	for _, request := range handler.requestLog {
		sawViews = sawViews || strings.Contains(request, "GET /UserViews?userId=fixture-user")
		sawItem = sawItem || strings.Contains(request, "UserId=fixture-user")
	}
	if !sawViews || !sawItem {
		t.Fatalf("user scope missing from requests: %v", handler.requestLog)
	}
	if _, err := New(Config{ConnectionID: "jellyfin-main", Endpoint: server.URL, UserID: "bad/user"}); err == nil {
		t.Fatal("New accepted a path-like user id")
	}
}

func TestJellyfinMissingTotalCannotProveCompleteInventory(t *testing.T) {
	fixture := loadJellyfinFixture(t)
	handler := &jellyfinFixtureHandler{fixture: fixture, omitTotal: true}
	client, server := newJellyfinFixtureClient(t, handler, "jellyfin-main", nil)
	defer server.Close()
	page, err := client.ListDetailed(context.Background(), "jellyfin-main", "", 1)
	if err != nil {
		t.Fatalf("ListDetailed: %v", err)
	}
	if page.Coverage.Completeness != domain.CompletenessPartial || !hasJellyfinReason(page.Coverage.ReasonCodes, "pagination_total_missing") || page.NextCursor != "" {
		t.Fatalf("missing total coverage = %+v", page.Coverage)
	}
}

func TestJellyfinWrongMappingRemainsUnavailable(t *testing.T) {
	fixture := loadJellyfinFixture(t)
	client, server := newJellyfinFixtureClient(t, &jellyfinFixtureHandler{fixture: fixture}, "jellyfin-main", []domain.PathMapping{{
		ConnectionID: "jellyfin-main", SourcePrefix: "/other/media", RootID: "library",
	}})
	defer server.Close()
	item, err := client.ObserveItem(context.Background(), "jellyfin-main", "jf-film-101")
	if err != nil {
		t.Fatalf("ObserveItem: %v", err)
	}
	if item.Item.Playable || item.UnavailableReason != "playable_media_unverified" || !hasJellyfinReason(item.Evidence, "media_source_path_unmapped") {
		t.Fatalf("wrong mapping item = %+v", item)
	}
}

func TestJellyfinErrorsAreScopedAndUnauthorized(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Header.Get("X-Emby-Token") != "fixture-api-key" {
			response.WriteHeader(http.StatusUnauthorized)
			return
		}
		response.WriteHeader(http.StatusTooManyRequests)
	}))
	defer server.Close()
	client, err := New(Config{ConnectionID: "jellyfin-main", Endpoint: server.URL, APIKey: "wrong"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_, err = client.Version(context.Background(), "jellyfin-main")
	assertJellyfinCode(t, err, domain.OutcomeUnauthorized)
	client, err = New(Config{ConnectionID: "jellyfin-main", Endpoint: server.URL, APIKey: "fixture-api-key"})
	if err != nil {
		t.Fatalf("New rate-limited: %v", err)
	}
	_, err = client.Version(context.Background(), "jellyfin-main")
	assertJellyfinCode(t, err, domain.OutcomeRateLimited)
	_, err = client.Version(context.Background(), "other")
	assertJellyfinCode(t, err, domain.OutcomeInvalidInput)
}

type jellyfinTimeoutTransport struct{}

func (jellyfinTimeoutTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, jellyfinTimeoutError{}
}

type jellyfinTimeoutError struct{}

func (jellyfinTimeoutError) Error() string   { return "fixture timeout" }
func (jellyfinTimeoutError) Timeout() bool   { return true }
func (jellyfinTimeoutError) Temporary() bool { return true }

func TestJellyfinTimeoutAndMalformedResponse(t *testing.T) {
	timeoutClient, err := New(Config{ConnectionID: "jellyfin-main", Endpoint: "http://fixture.invalid", APIKey: "fixture-api-key", HTTPClient: &http.Client{Transport: jellyfinTimeoutTransport{}}})
	if err != nil {
		t.Fatalf("New timeout client: %v", err)
	}
	_, err = timeoutClient.Version(context.Background(), "jellyfin-main")
	assertJellyfinCode(t, err, domain.OutcomeUnavailable)

	malformedServer := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Header.Get("X-Emby-Token") != "fixture-api-key" {
			response.WriteHeader(http.StatusUnauthorized)
			return
		}
		response.Header().Set("Content-Type", "application/json")
		_, _ = response.Write([]byte("{"))
	}))
	defer malformedServer.Close()
	malformedClient, err := New(Config{ConnectionID: "jellyfin-main", Endpoint: malformedServer.URL, APIKey: "fixture-api-key"})
	if err != nil {
		t.Fatalf("New malformed client: %v", err)
	}
	_, err = malformedClient.Libraries(context.Background(), "jellyfin-main")
	assertJellyfinCode(t, err, domain.OutcomeUnknown)
}

func TestJellyfinPathAliasesRemainUnmapped(t *testing.T) {
	for _, value := range []string{"/fixture/media/../secret/movie.mkv", "/fixture//media/movie.mkv", `/fixture/media\\movie.mkv`, "fixture/media/movie.mkv"} {
		if got := normalizeRemotePath(value); got != "" {
			t.Fatalf("normalizeRemotePath(%q) = %q", value, got)
		}
	}
	if got := normalizeMappingPrefix("/fixture/media/../secret"); got != "" {
		t.Fatalf("normalizeMappingPrefix traversal = %q", got)
	}
	if got := normalizeMappingPrefix("C:/"); got != "C:/" {
		t.Fatalf("normalizeMappingPrefix windows root = %q", got)
	}
	_, err := New(Config{ConnectionID: "jellyfin-main", Endpoint: "http://127.0.0.1:8096", Mappings: []domain.PathMapping{{
		ConnectionID: "jellyfin-main", SourcePrefix: "/fixture/media/../secret", RootID: "library",
	}}})
	if err == nil {
		t.Fatal("New accepted traversal mapping")
	}
}

var _ ports.MediaServerReadPort = (*Client)(nil)
