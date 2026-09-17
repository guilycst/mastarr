package inventory

import (
	"context"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeReader struct {
	mu sync.Mutex

	discoveryPage DiscoveryPage
	discoveryErr  error
	discoveries   map[string]Discovery

	mediaPage MediaPage
	mediaErr  error
	media     map[string]Media

	downloadPage DownloadPage
	downloadErr  error
	downloads    map[string]Download

	descriptorPage DescriptorPage
	descriptorErr  error
	descriptors    map[string]Descriptor

	requests []PageRequest
	calls    int
}

func (f *fakeReader) record(query PageRequest) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests = append(f.requests, query)
	f.calls++
}

func (f *fakeReader) ListDiscoveries(_ context.Context, query PageRequest) (DiscoveryPage, error) {
	f.record(query)
	return f.discoveryPage, f.discoveryErr
}

func (f *fakeReader) GetDiscovery(_ context.Context, id string) (Discovery, error) {
	f.record(PageRequest{})
	item, ok := f.discoveries[id]
	if !ok {
		return Discovery{}, ErrNotFound
	}
	return item, nil
}

func (f *fakeReader) ListMedia(_ context.Context, query PageRequest) (MediaPage, error) {
	f.record(query)
	return f.mediaPage, f.mediaErr
}

func (f *fakeReader) GetMedia(_ context.Context, id string) (Media, error) {
	f.record(PageRequest{})
	item, ok := f.media[id]
	if !ok {
		return Media{}, ErrNotFound
	}
	return item, nil
}

func (f *fakeReader) ListDownloads(_ context.Context, query PageRequest) (DownloadPage, error) {
	f.record(query)
	return f.downloadPage, f.downloadErr
}

func (f *fakeReader) GetDownload(_ context.Context, id string) (Download, error) {
	f.record(PageRequest{})
	item, ok := f.downloads[id]
	if !ok {
		return Download{}, ErrNotFound
	}
	return item, nil
}

func (f *fakeReader) ListDescriptors(_ context.Context, query PageRequest) (DescriptorPage, error) {
	f.record(query)
	return f.descriptorPage, f.descriptorErr
}

func (f *fakeReader) GetDescriptor(_ context.Context, id string) (Descriptor, error) {
	f.record(PageRequest{})
	item, ok := f.descriptors[id]
	if !ok {
		return Descriptor{}, ErrNotFound
	}
	return item, nil
}

func (f *fakeReader) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func (f *fakeReader) firstRequest() PageRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.requests) == 0 {
		return PageRequest{}
	}
	return f.requests[0]
}

func fixtureCoverage(now time.Time, completeness string) Coverage {
	return Coverage{
		Completeness: completeness,
		RootID:       "root-a",
		ObservedAt:   now,
		ReasonCodes:  []string{"page_tail"},
	}
}

func fixturePage(now time.Time) PageInfo {
	next := "cursor-next"
	return PageInfo{
		NextCursor: &next,
		ObservedAt: now,
		Coverage:   []Coverage{fixtureCoverage(now, "partial")},
	}
}

func TestHandlerDiscoveryPaginationEscapingAndReadOnlyQuery(t *testing.T) {
	now := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	fake := &fakeReader{discoveryPage: DiscoveryPage{
		Items: []Discovery{{
			ID:         "discovery-1",
			ObservedAt: now,
			Readiness:  "unknown",
			Files: []File{{
				RootID:       "root-a",
				RelativePath: "Shows/<pilot>.mkv",
				Type:         "file",
				Role:         "video",
				Size:         42,
				FileIdentity: "inode-1",
			}},
			Candidates: []Candidate{{
				Title:      "<script>alert(1)</script>",
				Kind:       "episode",
				ProviderID: "tvdb:42",
				Season:     intPtr(1),
				Episodes:   []int{1, 2},
			}},
			Provenance: nil,
			Coverage:   coveragePtr(fixtureCoverage(now, "partial")),
		}},
		Page: fixturePage(now),
	}}

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/discoveries?rootId=root-a&limit=2&cursor=cursor-before", nil)
	NewHandler(fake).ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", recorder.Code, recorder.Body.String())
	}
	if got := fake.firstRequest(); got.RootID != "root-a" || got.Limit != 2 || got.Cursor != "cursor-before" {
		t.Fatalf("page request = %#v", got)
	}
	body := recorder.Body.String()
	if strings.Contains(body, "<script>") {
		t.Fatalf("unsafe markup was rendered: %s", body)
	}
	if !strings.Contains(body, `rel="next"`) || !strings.Contains(body, `/discoveries?cursor=cursor-next&amp;limit=2&amp;rootId=root-a`) {
		t.Fatalf("pagination link did not preserve bounded query state: %s", body)
	}
	if !strings.Contains(body, "unknown") || !strings.Contains(body, "coverage: partial") {
		t.Fatalf("unknown/coverage evidence was not rendered: %s", body)
	}
	if recorder.Header().Get("Cache-Control") != "no-store" || recorder.Header().Get("X-Robots-Tag") != "noindex, nofollow" {
		t.Fatalf("private headers = %#v", recorder.Header())
	}
}

func TestHandlerDetailsPreserveDeepLinkIdentityAndAssociations(t *testing.T) {
	now := time.Date(2026, 9, 17, 12, 30, 0, 0, time.UTC)
	media := Media{
		ID:         "media-1",
		Kind:       "movie",
		ProviderID: "tmdb:101",
		Title:      "Observed title",
		ObservedAt: now,
		Tracking: []Tracking{
			{ConnectionID: "sonarr-a", Dimension: "registration", Value: "present", ProviderID: "tvdb:10", ExternalID: "series-10", ObservedAt: now, CoverageID: "00000000-0000-0000-0000-000000000010"},
			{ConnectionID: "jellyfin-b", Dimension: "availability", Value: "unknown", ObservedAt: now},
			{ConnectionID: "seerr-c", Dimension: "request", Value: "present", ObservedAt: now},
		},
	}
	fake := &fakeReader{media: map[string]Media{"media-1": media}}
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/media/media-1?identity=Draft+Title&providerId=tmdb%3A202&selection=episode-3&kind=anime&episode=3&subtitleLanguage=pt-BR&subtitleForced=true&subtitleSDH=false&subtitlePair=pair-a&limit=7&cursor=cursor-list", nil)
	NewHandler(fake).ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%s", recorder.Code, recorder.Body.String())
	}
	body := recorder.Body.String()
	for _, expected := range []string{"value=\"Draft Title\"", "value=\"tmdb:202\"", "<option value=\"anime\" selected>anime</option>", "name=\"selection\" value=\"episode-3\"", "name=\"episode\" value=\"3\"", "name=\"subtitleLanguage\" value=\"pt-BR\"", "name=\"subtitleForced\" value=\"true\"", "name=\"subtitleSDH\" value=\"false\"", "name=\"subtitlePair\" value=\"pair-a\"", "name=\"limit\" value=\"7\"", "name=\"cursor\" value=\"cursor-list\"", "sonarr-a", "jellyfin-b", "seerr-c", "registration", "availability", "request", "series-10", "00000000-0000-0000-0000-000000000010", "unknown", "/media/media-1"} {
		if !strings.Contains(body, expected) {
			t.Fatalf("detail body missing %q: %s", expected, body)
		}
	}

	discovery := Discovery{
		ID:         "discovery-2",
		ObservedAt: now,
		Readiness:  "ready",
		Files: []File{{
			RootID:       "root-a",
			RelativePath: "Season 1/Episode 01.mkv",
			Type:         "file",
			Role:         "video",
			Size:         99,
		}},
		Candidates: []Candidate{{Title: "<b>unsafe</b>", Kind: "episode", ProviderID: "tvdb:44"}, {Title: "<b>unsafe</b>", Kind: "episode", ProviderID: "tvdb:45"}},
	}
	fake.discoveries = map[string]Discovery{"discovery-2": discovery}
	recorder = httptest.NewRecorder()
	request = httptest.NewRequest(http.MethodGet, "/discoveries/discovery-2?rootId=root-a&limit=9&cursor=cursor-list&association-0-identity=video-id&association-0-episode=3&association-0-language=pt-BR&association-0-forced=true&association-0-sdh=false&association-0-pair=pair-a&association-0-role=video&candidate-0-title=Draft+Candidate&candidate-0-provider=tvdb%3A99&candidate-0-external=episode-99&candidate-0-kind=anime&candidate-0-season=2&candidate-0-episodes=3%2C4&candidate-0-year=2025&candidate-0-score=0.91", nil)
	NewHandler(fake).ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("discovery detail status = %d; body=%s", recorder.Code, recorder.Body.String())
	}
	body = recorder.Body.String()
	for _, expected := range []string{"name=\"association-0-identity\" value=\"video-id\"", "name=\"association-0-episode\" value=\"3\"", "name=\"association-0-language\" value=\"pt-BR\"", "name=\"association-0-forced\" value=\"true\"", "name=\"association-0-sdh\" value=\"false\"", "name=\"association-0-pair\" value=\"pair-a\"", "<option value=\"video\" selected>video</option>", "name=\"candidate-0-title\" value=\"Draft Candidate\"", "name=\"candidate-0-provider\" value=\"tvdb:99\"", "name=\"candidate-0-external\" value=\"episode-99\"", "<option value=\"anime\" selected>anime</option>", "name=\"candidate-0-season\" value=\"2\"", "name=\"candidate-0-episodes\" value=\"3,4\"", "name=\"candidate-0-year\" value=\"2025\"", "name=\"candidate-0-score\" value=\"0.91\"", "value=\"&lt;b&gt;unsafe&lt;/b&gt;\"", "Read-only observation"} {
		if !strings.Contains(body, expected) {
			t.Fatalf("association body missing %q: %s", expected, body)
		}
	}
}

func TestHandlerErrorsAreSanitizedAndMutationsAreRejected(t *testing.T) {
	fake := &fakeReader{mediaErr: errors.New("GET https://private.example/api?token=secret response body private")}
	recorder := httptest.NewRecorder()
	NewHandler(fake).ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/media", nil))
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("error status = %d", recorder.Code)
	}
	body := recorder.Body.String()
	if strings.Contains(body, "private.example") || strings.Contains(body, "secret") || strings.Contains(body, "response body") {
		t.Fatalf("private reader error leaked: %s", body)
	}

	recorder = httptest.NewRecorder()
	NewHandler(fake).ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/media", nil))
	if recorder.Code != http.StatusMethodNotAllowed || recorder.Header().Get("Allow") != "GET, HEAD" {
		t.Fatalf("POST response = %d Allow=%q", recorder.Code, recorder.Header().Get("Allow"))
	}
	if fake.callCount() != 1 {
		t.Fatalf("read-only method dispatched to reader: calls=%d", fake.callCount())
	}

	recorder = httptest.NewRecorder()
	NewHandler(fake).ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/media?limit=2&limit=3", nil))
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("duplicate query status = %d", recorder.Code)
	}
}

func TestHandlerRejectsIncompletePageWithoutInferringAbsence(t *testing.T) {
	fake := &fakeReader{descriptorPage: DescriptorPage{Items: nil, Page: PageInfo{}}}
	recorder := httptest.NewRecorder()
	NewHandler(fake).ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/descriptors", nil))
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("incomplete page status = %d", recorder.Code)
	}
	if !strings.Contains(recorder.Body.String(), "incomplete or invalid inventory evidence") {
		t.Fatalf("incomplete page did not remain explicit: %s", recorder.Body.String())
	}
}

func TestHTTPReaderConvertsAllInventoryPagesAndPreservesQuery(t *testing.T) {
	const (
		idDiscovery  = "00000000-0000-0000-0000-000000000001"
		idMedia      = "00000000-0000-0000-0000-000000000002"
		idDownload   = "00000000-0000-0000-0000-000000000003"
		idDescriptor = "00000000-0000-0000-0000-000000000004"
	)
	now := "2026-09-17T12:00:00Z"
	page := func(items string) string {
		return fmt.Sprintf(`{"items":%s,"page":{"nextCursor":null,"coverage":[{"completeness":"complete","observedAt":%q}],"observedAt":%q}}`, items, now, now)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		switch r.URL.Path {
		case "/api/v1/discoveries":
			if r.URL.Query().Get("cursor") != "before" || r.URL.Query().Get("limit") != "7" || r.URL.Query().Get("rootId") != "root-a" {
				t.Errorf("discovery query = %s", r.URL.RawQuery)
			}
			_, _ = io.WriteString(w, page(fmt.Sprintf(`[{"id":%q,"observedAt":%q,"readiness":"ready","files":[{"relativePath":"Show/E01.mkv","rootId":"root-a","size":42,"type":"file"}]}]`, idDiscovery, now)))
		case "/api/v1/media":
			_, _ = io.WriteString(w, page(fmt.Sprintf(`[{"id":%q,"kind":"movie","providerId":"tmdb:101","observedAt":%q,"tracking":[]}]`, idMedia, now)))
		case "/api/v1/downloads":
			_, _ = io.WriteString(w, page(fmt.Sprintf(`[{"id":%q,"connectionId":"qbit-a","state":"seeding","observedAt":%q}]`, idDownload, now)))
		case "/api/v1/descriptors":
			_, _ = io.WriteString(w, page(fmt.Sprintf(`[{"id":%q,"type":"torrent","size":42,"digest":"sha256:abc","availability":"available","capturedAt":%q}]`, idDescriptor, now)))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	reader, err := NewHTTPReader(server.URL, server.Client(), time.Second)
	if err != nil {
		t.Fatalf("NewHTTPReader() error = %v", err)
	}
	ctx := context.Background()
	discoveries, err := reader.ListDiscoveries(ctx, PageRequest{Cursor: "before", Limit: 7, RootID: "root-a"})
	if err != nil || len(discoveries.Items) != 1 || discoveries.Items[0].ID != idDiscovery {
		t.Fatalf("discoveries = %#v, error=%v", discoveries, err)
	}
	media, err := reader.ListMedia(ctx, PageRequest{Limit: 3, Kind: "movie"})
	if err != nil || len(media.Items) != 1 || media.Items[0].ProviderID != "tmdb:101" {
		t.Fatalf("media = %#v, error=%v", media, err)
	}
	downloads, err := reader.ListDownloads(ctx, PageRequest{Limit: 3, ConnectionID: "qbit-a"})
	if err != nil || len(downloads.Items) != 1 || downloads.Items[0].State != "seeding" {
		t.Fatalf("downloads = %#v, error=%v", downloads, err)
	}
	descriptors, err := reader.ListDescriptors(ctx, PageRequest{Limit: 3})
	if err != nil || len(descriptors.Items) != 1 || descriptors.Items[0].Size != 42 {
		t.Fatalf("descriptors = %#v, error=%v", descriptors, err)
	}
}

func TestHTTPReaderStrictFailuresAndNotFound(t *testing.T) {
	const id = "00000000-0000-0000-0000-000000000001"
	now := "2026-09-17T12:00:00Z"
	valid := fmt.Sprintf(`{"items":[],"page":{"nextCursor":null,"coverage":[],"observedAt":%q}}`, now)
	cases := []struct {
		name string
		body string
		want ErrorKind
	}{
		{name: "unknown field", body: strings.TrimSuffix(valid, "}") + `,"unexpected":true}`, want: ErrorProtocol},
		{name: "duplicate key", body: strings.Replace(valid, `"items":[]`, `"items":[],"items":[]`, 1), want: ErrorProtocol},
		{name: "missing cursor", body: fmt.Sprintf(`{"items":[],"page":{"coverage":[],"observedAt":%q}}`, now), want: ErrorProtocol},
		{name: "empty", body: "", want: ErrorProtocol},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, tc.body)
			}))
			defer server.Close()
			reader, err := NewHTTPReader(server.URL, server.Client(), time.Second)
			if err != nil {
				t.Fatalf("NewHTTPReader() error = %v", err)
			}
			_, err = reader.ListMedia(context.Background(), PageRequest{})
			var apiErr *APIError
			if !errors.As(err, &apiErr) || apiErr.Kind() != tc.want {
				t.Fatalf("error = %T %v; APIError = %#v, want kind %q", err, err, apiErr, tc.want)
			}
		})
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, `{"detail":"private endpoint"}`)
	}))
	defer server.Close()
	reader, err := NewHTTPReader(server.URL, server.Client(), time.Second)
	if err != nil {
		t.Fatalf("NewHTTPReader() error = %v", err)
	}
	_, err = reader.GetMedia(context.Background(), id)
	if !errors.Is(err, ErrNotFound) || strings.Contains(err.Error(), "private") {
		t.Fatalf("not found error = %v", err)
	}
}

func TestHTTPReaderRejectsMissingRequiredFieldsAndUnsafePaths(t *testing.T) {
	const (
		idDiscovery  = "00000000-0000-0000-0000-000000000001"
		idMedia      = "00000000-0000-0000-0000-000000000002"
		idDescriptor = "00000000-0000-0000-0000-000000000003"
	)
	now := "2026-09-17T12:00:00Z"
	page := func(items string) string {
		return fmt.Sprintf(`{"items":%s,"page":{"nextCursor":null,"coverage":[],"observedAt":%q}}`, items, now)
	}
	cases := []struct {
		name string
		path string
		body string
	}{
		{
			name: "media id presence",
			path: "/api/v1/media",
			body: page(fmt.Sprintf(`[{"kind":"movie","providerId":"tmdb:1","observedAt":%q,"tracking":[]}]`, now)),
		},
		{
			name: "discovery relative path",
			path: "/api/v1/discoveries",
			body: page(fmt.Sprintf(`[{"id":%q,"readiness":"ready","observedAt":%q,"files":[{"relativePath":"../secret.mkv","rootId":"root-a","type":"file","size":1}]}]`, idDiscovery, now)),
		},
		{
			name: "descriptor digest presence",
			path: "/api/v1/descriptors",
			body: page(fmt.Sprintf(`[{"id":%q,"type":"torrent","size":1,"availability":"available","capturedAt":%q}]`, idDescriptor, now)),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if r.URL.Path != tc.path {
					t.Errorf("request path = %q, want %q", r.URL.Path, tc.path)
				}
				_, _ = io.WriteString(w, tc.body)
			}))
			defer server.Close()
			reader, err := NewHTTPReader(server.URL, server.Client(), time.Second)
			if err != nil {
				t.Fatalf("NewHTTPReader() error = %v", err)
			}
			var readErr error
			switch tc.path {
			case "/api/v1/media":
				_, readErr = reader.ListMedia(context.Background(), PageRequest{})
			case "/api/v1/discoveries":
				_, readErr = reader.ListDiscoveries(context.Background(), PageRequest{})
			case "/api/v1/descriptors":
				_, readErr = reader.ListDescriptors(context.Background(), PageRequest{})
			}
			var apiErr *APIError
			if !errors.As(readErr, &apiErr) || apiErr.Kind() != ErrorProtocol {
				t.Fatalf("error = %T %v; APIError = %#v", readErr, readErr, apiErr)
			}
		})
	}
}

func TestHTTPReaderBoundsRedirectAndContextIdentity(t *testing.T) {
	for _, raw := range []string{"http://user:secret@example.test", "http://example.test?token=secret", "http://example.test#secret"} {
		if _, err := NewHTTPReader(raw, nil, time.Second); err == nil {
			t.Fatalf("NewHTTPReader(%q) unexpectedly succeeded", raw)
		}
	}

	var targetCalls int
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { targetCalls++ }))
	defer target.Close()
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusFound)
	}))
	defer origin.Close()
	reader, err := NewHTTPReader(origin.URL, origin.Client(), time.Second)
	if err != nil {
		t.Fatalf("NewHTTPReader() error = %v", err)
	}
	_, err = reader.ListMedia(context.Background(), PageRequest{})
	if err == nil || targetCalls != 0 {
		t.Fatalf("redirect result = %v targetCalls=%d", err, targetCalls)
	}

	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer slow.Close()
	reader, err = NewHTTPReader(slow.URL, slow.Client(), 10*time.Millisecond)
	if err != nil {
		t.Fatalf("NewHTTPReader() error = %v", err)
	}
	_, err = reader.ListMedia(context.Background(), PageRequest{})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("timeout error = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = reader.ListMedia(ctx, PageRequest{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel error = %v", err)
	}
}

func TestHandlerRejectsForeignDetailIdentity(t *testing.T) {
	now := time.Date(2026, 9, 17, 13, 0, 0, 0, time.UTC)
	fake := &fakeReader{
		discoveries: map[string]Discovery{"requested": {ID: "foreign", ObservedAt: now, Readiness: "unknown", Files: []File{}}},
		media:       map[string]Media{"requested": {ID: "foreign", Kind: "movie", ProviderID: "tmdb:1", ObservedAt: now, Tracking: []Tracking{}}},
		downloads:   map[string]Download{"requested": {ID: "foreign", ConnectionID: "qbit-a", State: "unknown", ObservedAt: now}},
		descriptors: map[string]Descriptor{"requested": {ID: "foreign", Type: "unknown", Size: 0, Digest: "sha256:empty", Availability: "unknown", CapturedAt: now}},
	}
	for _, route := range []string{"discoveries", "media", "downloads", "descriptors"} {
		t.Run(route, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			NewHandler(fake).ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/"+route+"/requested", nil))
			if recorder.Code != http.StatusServiceUnavailable {
				t.Fatalf("status = %d; body=%s", recorder.Code, recorder.Body.String())
			}
			if strings.Contains(recorder.Body.String(), "foreign") {
				t.Fatalf("foreign identity leaked: %s", recorder.Body.String())
			}
		})
	}
}

func TestHandlerRendersEvidenceAndUnknownStates(t *testing.T) {
	now := time.Date(2026, 9, 17, 13, 10, 0, 0, time.UTC)
	fileObserved := now.Add(-time.Minute)
	completed := now.Add(-2 * time.Minute)
	count := 2
	coverage := Coverage{
		Completeness:     "partial",
		ConnectionID:     "sonarr-a",
		RootID:           "root-a",
		SourceID:         "00000000-0000-0000-0000-000000000005",
		SnapshotRevision: "snapshot-7",
		ObservedAt:       now,
		ObservedCount:    &count,
		ReasonCodes:      []string{"page_tail", "manager_offline"},
	}
	discovery := Discovery{
		ID:         "discovery-evidence",
		ObservedAt: now,
		Readiness:  "unknown",
		Files:      []File{{RootID: "root-a", RelativePath: "Show/E01.mkv", Type: "file", Role: "video", Size: 42, FileIdentity: "inode-7", ObservedAt: &fileObserved}},
		Provenance: []Provenance{{ConnectionID: "qbit-a", ClientItemID: "item-7", DescriptorID: "00000000-0000-0000-0000-000000000006", Hash: "sha256:download", SourcePath: "root-a:Show/E01.mkv", CompletedAt: &completed}},
		Coverage:   &coverage,
	}
	fake := &fakeReader{discoveries: map[string]Discovery{"discovery-evidence": discovery}}
	recorder := httptest.NewRecorder()
	NewHandler(fake).ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/discoveries/discovery-evidence", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("discovery status = %d; body=%s", recorder.Code, recorder.Body.String())
	}
	body := recorder.Body.String()
	for _, expected := range []string{"2026-09-17T13:09:00Z", "sha256:download", "2026-09-17T13:08:00Z", "snapshot-7", "manager_offline", "2", "00000000-0000-0000-0000-000000000005"} {
		if !strings.Contains(body, expected) {
			t.Fatalf("discovery evidence missing %q: %s", expected, body)
		}
	}

	trackingObserved := now.Add(-3 * time.Minute)
	media := Media{
		ID:         "media-evidence",
		Kind:       "episode",
		ProviderID: "tvdb:77",
		ObservedAt: now,
		Tracking:   []Tracking{{ConnectionID: "sonarr-a", Dimension: "registration", Value: "unknown", ProviderID: "tvdb:77", ExternalID: "series-77", ObservedAt: trackingObserved, CoverageID: "00000000-0000-0000-0000-000000000007"}},
	}
	fake.media = map[string]Media{"media-evidence": media}
	recorder = httptest.NewRecorder()
	NewHandler(fake).ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/media/media-evidence", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("media status = %d; body=%s", recorder.Code, recorder.Body.String())
	}
	body = recorder.Body.String()
	for _, expected := range []string{"series-77", "2026-09-17T13:07:00Z", "00000000-0000-0000-0000-000000000007"} {
		if !strings.Contains(body, expected) {
			t.Fatalf("tracking evidence missing %q: %s", expected, body)
		}
	}

	download := Download{
		ID: "download-evidence", ConnectionID: "qbit-a", ClientItemID: "item-7", Hash: "sha256:download", NzbID: "nzb-7", DeprecatedID: "legacy-7", DescriptorID: "00000000-0000-0000-0000-000000000006", SourcePath: "root-a:Show/E01.mkv", State: "seeding", ObservedAt: now, Coverage: &coverage,
	}
	fake.downloads = map[string]Download{"download-evidence": download}
	recorder = httptest.NewRecorder()
	NewHandler(fake).ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/downloads/download-evidence", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("download status = %d; body=%s", recorder.Code, recorder.Body.String())
	}
	body = recorder.Body.String()
	for _, expected := range []string{"legacy-7", "00000000-0000-0000-0000-000000000006", "snapshot-7", "manager_offline"} {
		if !strings.Contains(body, expected) {
			t.Fatalf("download evidence missing %q: %s", expected, body)
		}
	}
}

func TestHandlerOptionMarkupClosesEveryValueAndKeepsSelection(t *testing.T) {
	now := time.Date(2026, 9, 17, 13, 20, 0, 0, time.UTC)
	fake := &fakeReader{mediaPage: MediaPage{Items: []Media{{ID: "media-1", Kind: "anime", ProviderID: "tmdb:1", ObservedAt: now, Tracking: []Tracking{}}}, Page: fixturePage(now)}}
	recorder := httptest.NewRecorder()
	NewHandler(fake).ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/media?kind=anime", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("media status = %d; body=%s", recorder.Code, recorder.Body.String())
	}
	body := recorder.Body.String()
	for _, option := range []string{"movie", "episode", "season", "anime"} {
		if !strings.Contains(body, "<option value=\""+option+"\"") {
			t.Fatalf("media option %q is malformed: %s", option, body)
		}
	}
	if !strings.Contains(body, `<option value="anime" selected>anime</option>`) {
		t.Fatalf("selected media option missing: %s", body)
	}

	fake.discoveries = map[string]Discovery{"discovery-1": {ID: "discovery-1", ObservedAt: now, Readiness: "ready", Files: []File{{RootID: "root-a", RelativePath: "Show/E01.mkv", Type: "file", Role: "video", Size: 1}}}}
	recorder = httptest.NewRecorder()
	NewHandler(fake).ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/discoveries/discovery-1", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("discovery status = %d; body=%s", recorder.Code, recorder.Body.String())
	}
	body = recorder.Body.String()
	for _, option := range []string{"video", "subtitle", "companion"} {
		if !strings.Contains(body, "<option value=\""+option+"\"") {
			t.Fatalf("role option %q is malformed: %s", option, body)
		}
	}
	if !strings.Contains(body, `<option value="video" selected>video</option>`) {
		t.Fatalf("selected role option missing: %s", body)
	}
}

func TestCandidateDraftBoundaryAndScoreRoundTrip(t *testing.T) {
	now := time.Date(2026, 9, 17, 14, 0, 0, 0, time.UTC)
	episodes := make([]int, 0, MaxAssociationInputs)
	for episode := 1000; episode < 1000+MaxAssociationInputs; episode++ {
		episodes = append(episodes, episode)
	}
	episodeText := joinInts(episodes)
	if len(episodeText) <= MaxInputLength {
		t.Fatalf("boundary fixture is too short: %d", len(episodeText))
	}
	score := float32(0.9137)
	scoreText := floatPointerValue(&score)
	fake := &fakeReader{discoveries: map[string]Discovery{
		"discovery-boundary": {
			ID:         "discovery-boundary",
			ObservedAt: now,
			Readiness:  "ready",
			Files:      []File{},
			Candidates: []Candidate{{
				Title:    "Season pack",
				Kind:     "season",
				Episodes: episodes,
				Score:    &score,
			}},
		},
	}}
	handler := NewHandler(fake)
	initial := httptest.NewRecorder()
	handler.ServeHTTP(initial, httptest.NewRequest(http.MethodGet, "/discoveries/discovery-boundary?rootId=root-a&limit=7&cursor=cursor-list", nil))
	if initial.Code != http.StatusOK {
		t.Fatalf("initial status = %d; body=%s", initial.Code, initial.Body.String())
	}
	if !strings.Contains(initial.Body.String(), `name="candidate-0-episodes" value="`+episodeText+`"`) {
		t.Fatalf("initial episode draft was not losslessly rendered: %s", initial.Body.String())
	}
	if !strings.Contains(initial.Body.String(), `name="candidate-0-score" value="`+scoreText+`"`) {
		t.Fatalf("initial score draft lost precision: %s", initial.Body.String())
	}

	values := url.Values{}
	values.Set("rootId", "root-a")
	values.Set("limit", "7")
	values.Set("cursor", "cursor-list")
	values.Set("candidate-0-episodes", episodeText)
	values.Set("candidate-0-score", scoreText)
	reload := httptest.NewRecorder()
	handler.ServeHTTP(reload, httptest.NewRequest(http.MethodGet, "/discoveries/discovery-boundary?"+values.Encode(), nil))
	if reload.Code != http.StatusOK {
		t.Fatalf("reload status = %d; body=%s", reload.Code, reload.Body.String())
	}
	body := reload.Body.String()
	for _, expected := range []string{
		`name="candidate-0-episodes" value="` + episodeText + `"`,
		`name="candidate-0-score" value="` + scoreText + `"`,
		`name="rootId" value="root-a"`,
		`name="limit" value="7"`,
		`name="cursor" value="cursor-list"`,
	} {
		if !strings.Contains(body, expected) {
			t.Fatalf("reload lost candidate or list context %q: %s", expected, body)
		}
	}
}

func TestCoverageWindowsRoundTripAndZeroRejection(t *testing.T) {
	const downloadID = "00000000-0000-0000-0000-000000000008"
	now := "2026-09-17T14:10:00Z"
	started := "2026-09-17T14:00:00Z"
	completed := "2026-09-17T14:05:00Z"
	page := func(items string, window bool) string {
		coverage := fmt.Sprintf(`{"completeness":"partial","observedAt":%q}`, now)
		if window {
			coverage = fmt.Sprintf(`{"completeness":"partial","startedAt":%q,"completedAt":%q,"observedAt":%q}`, started, completed, now)
		}
		return fmt.Sprintf(`{"items":%s,"page":{"nextCursor":null,"coverage":[%s],"observedAt":%q}}`, items, coverage, now)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/descriptors":
			_, _ = io.WriteString(w, page(fmt.Sprintf(`[{"id":"00000000-0000-0000-0000-000000000009","type":"torrent","size":1,"digest":"sha256:1","availability":"available","capturedAt":%q}]`, now), true))
		case "/api/v1/downloads/" + downloadID:
			_, _ = io.WriteString(w, fmt.Sprintf(`{"id":%q,"connectionId":"qbit-a","state":"complete","observedAt":%q,"coverage":{"completeness":"complete","startedAt":%q,"completedAt":%q,"observedAt":%q}}`, downloadID, now, started, completed, now))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	reader, err := NewHTTPReader(server.URL, server.Client(), time.Second)
	if err != nil {
		t.Fatalf("NewHTTPReader() error = %v", err)
	}
	descriptors, err := reader.ListDescriptors(context.Background(), PageRequest{})
	if err != nil || len(descriptors.Page.Coverage) != 1 {
		t.Fatalf("page coverage = %#v, error=%v", descriptors.Page.Coverage, err)
	}
	if descriptors.Page.Coverage[0].StartedAt == nil || descriptors.Page.Coverage[0].CompletedAt == nil || descriptors.Page.Coverage[0].StartedAt.UTC().Format(time.RFC3339) != started || descriptors.Page.Coverage[0].CompletedAt.UTC().Format(time.RFC3339) != completed {
		t.Fatalf("page coverage window = %#v", descriptors.Page.Coverage[0])
	}
	download, err := reader.GetDownload(context.Background(), downloadID)
	if err != nil || download.Coverage == nil || download.Coverage.StartedAt == nil || download.Coverage.CompletedAt == nil {
		t.Fatalf("item coverage = %#v, error=%v", download.Coverage, err)
	}

	startTime, _ := time.Parse(time.RFC3339, started)
	completeTime, _ := time.Parse(time.RFC3339, completed)
	nowTime, _ := time.Parse(time.RFC3339, now)
	coverage := Coverage{Completeness: "partial", StartedAt: &startTime, CompletedAt: &completeTime, ObservedAt: nowTime}
	fake := &fakeReader{
		descriptorPage: DescriptorPage{Items: []Descriptor{{ID: "descriptor-window", Type: "torrent", Digest: "sha256:1", Availability: "available", CapturedAt: nowTime}}, Page: PageInfo{ObservedAt: nowTime, Coverage: []Coverage{coverage}}},
		downloads:      map[string]Download{"download-window": {ID: "download-window", ConnectionID: "qbit-a", State: "complete", ObservedAt: nowTime, Coverage: &coverage}},
	}
	recorder := httptest.NewRecorder()
	NewHandler(fake).ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/descriptors", nil))
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), started) || !strings.Contains(recorder.Body.String(), completed) {
		t.Fatalf("page coverage was not rendered: status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	recorder = httptest.NewRecorder()
	NewHandler(fake).ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/downloads/download-window", nil))
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), started) || !strings.Contains(recorder.Body.String(), completed) {
		t.Fatalf("item coverage was not rendered: status=%d body=%s", recorder.Code, recorder.Body.String())
	}

	unknownCoverage := Coverage{Completeness: "unknown", ObservedAt: nowTime}
	fake.descriptorPage.Page.Coverage = []Coverage{unknownCoverage}
	recorder = httptest.NewRecorder()
	NewHandler(fake).ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/descriptors", nil))
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), "Started at") || !strings.Contains(recorder.Body.String(), "unknown") {
		t.Fatalf("absent coverage window was not explicit: status=%d body=%s", recorder.Code, recorder.Body.String())
	}

	zeroServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		zeroCoverage := fmt.Sprintf(`{"completeness":"partial","startedAt":"0001-01-01T00:00:00Z","observedAt":%q}`, now)
		_, _ = io.WriteString(w, fmt.Sprintf(`{"items":[{"id":"00000000-0000-0000-0000-000000000009","type":"torrent","size":1,"digest":"sha256:1","availability":"available","capturedAt":%q}],"page":{"nextCursor":null,"coverage":[%s],"observedAt":%q}}`, now, zeroCoverage, now))
	}))
	defer zeroServer.Close()
	// Replace the valid page's coverage with a present, zero timestamp while
	// retaining otherwise complete page metadata.
	zeroReader, err := NewHTTPReader(zeroServer.URL, zeroServer.Client(), time.Second)
	if err != nil {
		t.Fatalf("NewHTTPReader() zero fixture error = %v", err)
	}
	_, err = zeroReader.ListDescriptors(context.Background(), PageRequest{})
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Kind() != ErrorProtocol {
		t.Fatalf("zero coverage window error = %T %v; APIError=%#v", err, err, apiErr)
	}
}

func TestDynamicDraftKeysAreStrictAndBoundToEvidence(t *testing.T) {
	now := time.Date(2026, 9, 17, 14, 20, 0, 0, time.UTC)
	fake := &fakeReader{discoveries: map[string]Discovery{
		"discovery-dynamic": {
			ID:         "discovery-dynamic",
			ObservedAt: now,
			Readiness:  "ready",
			Files:      []File{{RootID: "root-a", RelativePath: "Show/E01.mkv", Type: "file", Role: "video", Size: 1}},
			Candidates: []Candidate{{Title: "Episode", Kind: "episode"}},
		},
	}}
	invalid := []struct {
		name  string
		query string
	}{
		{name: "non-numeric candidate index", query: "candidate-x-title=draft"},
		{name: "leading-zero candidate index", query: "candidate-00-title=draft"},
		{name: "unknown candidate field", query: "candidate-0-unknown=draft"},
		{name: "extra candidate suffix", query: "candidate-0-title-extra=draft"},
		{name: "non-numeric association index", query: "association-x-role=video"},
		{name: "unknown association field", query: "association-0-unknown=draft"},
		{name: "out-of-bound maximum index", query: "candidate-999-title=draft"},
		{name: "invalid candidate kind", query: "candidate-0-kind=future"},
		{name: "invalid association role", query: "association-0-role=future"},
		{name: "out-of-response candidate index", query: "candidate-1-title=draft"},
		{name: "out-of-response association index", query: "association-1-role=video"},
	}
	for _, tc := range invalid {
		t.Run(tc.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			NewHandler(fake).ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/discoveries/discovery-dynamic?"+tc.query, nil))
			if recorder.Code != http.StatusBadRequest {
				t.Fatalf("status = %d; body=%s", recorder.Code, recorder.Body.String())
			}
			if strings.Contains(recorder.Body.String(), tc.query) {
				t.Fatalf("invalid query was retained: %s", recorder.Body.String())
			}
		})
	}

	valid := httptest.NewRecorder()
	NewHandler(fake).ServeHTTP(valid, httptest.NewRequest(http.MethodGet, "/discoveries/discovery-dynamic?candidate-0-kind=episode&candidate-0-episodes=1%2C2&association-0-role=video", nil))
	if valid.Code != http.StatusOK {
		t.Fatalf("valid dynamic draft status = %d; body=%s", valid.Code, valid.Body.String())
	}
}

func TestDetailBackLinksFollowUsableListRoutes(t *testing.T) {
	now := time.Date(2026, 9, 17, 15, 0, 0, 0, time.UTC)
	discovery := Discovery{
		ID:         "discovery-back-link",
		ObservedAt: now,
		Readiness:  "ready",
		Files:      []File{{RootID: "root-a", RelativePath: "Show/E01.mkv", Type: "file", Role: "video", Size: 1}},
		Candidates: []Candidate{{Title: "Episode", Kind: "episode"}},
	}
	media := Media{ID: "media-back-link", Kind: "movie", ProviderID: "tmdb:1", ObservedAt: now, Tracking: []Tracking{}}
	fake := &fakeReader{
		discoveries:   map[string]Discovery{discovery.ID: discovery},
		discoveryPage: DiscoveryPage{Items: []Discovery{discovery}, Page: fixturePage(now)},
		media:         map[string]Media{media.ID: media},
		mediaPage:     MediaPage{Items: []Media{media}, Page: fixturePage(now)},
	}
	tests := []struct {
		name  string
		route string
		query string
		want  map[string]string
		omit  []string
	}{
		{
			name:  "discovery candidate and association drafts",
			route: "/discoveries/" + discovery.ID,
			query: "rootId=root-a&limit=7&cursor=cursor-list&association-0-role=video&candidate-0-title=Draft",
			want:  map[string]string{"rootId": "root-a", "limit": "7", "cursor": "cursor-list"},
			omit:  []string{"association-0-role", "candidate-0-title"},
		},
		{
			name:  "media identity draft",
			route: "/media/" + media.ID,
			query: "kind=anime&limit=8&cursor=cursor-media&identity=Draft&providerId=tmdb%3A2&selection=episode-1&subtitleForced=true",
			want:  map[string]string{"kind": "anime", "limit": "8", "cursor": "cursor-media"},
			omit:  []string{"identity", "providerId", "selection", "subtitleForced"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			NewHandler(fake).ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, tc.route+"?"+tc.query, nil))
			if recorder.Code != http.StatusOK {
				t.Fatalf("detail status = %d; body=%s", recorder.Code, recorder.Body.String())
			}
			back := detailBackURL(t, recorder.Body.String())
			for key, want := range tc.want {
				if got := back.Query().Get(key); got != want {
					t.Fatalf("back query %s = %q, want %q; URL=%s", key, got, want, back)
				}
			}
			for _, key := range tc.omit {
				if value := back.Query().Get(key); value != "" {
					t.Fatalf("detail-only key %s leaked into list URL with value %q: %s", key, value, back)
				}
			}
			follow := httptest.NewRecorder()
			NewHandler(fake).ServeHTTP(follow, httptest.NewRequest(http.MethodGet, back.String(), nil))
			if follow.Code != http.StatusOK {
				t.Fatalf("generated back URL %s was not usable: status=%d body=%s", back, follow.Code, follow.Body.String())
			}
		})
	}
}

func detailBackURL(t *testing.T, body string) *url.URL {
	t.Helper()
	marker := `<main id="inventory-content" aria-labelledby="inventory-title"><p><a href="`
	start := strings.Index(body, marker)
	if start < 0 {
		t.Fatalf("detail back link was not rendered: %s", body)
	}
	start += len(marker)
	end := strings.IndexByte(body[start:], '"')
	if end < 0 {
		t.Fatalf("detail back link has no closing quote: %s", body)
	}
	href := html.UnescapeString(body[start : start+end])
	parsed, err := url.Parse(href)
	if err != nil {
		t.Fatalf("parse generated back URL %q: %v", href, err)
	}
	return parsed
}

func TestMaximumRenderedCollectionsAcceptFullDraftSubmission(t *testing.T) {
	now := time.Date(2026, 9, 17, 15, 20, 0, 0, time.UTC)
	files := make([]File, MaxAssociationInputs+1)
	for index := range files {
		files[index] = File{
			RootID:       "root-a",
			RelativePath: fmt.Sprintf("Show/%03d.mkv", index),
			Type:         "file",
			Role:         "video",
			Size:         1,
			FileIdentity: fmt.Sprintf("inode-%d", index),
		}
	}
	candidates := make([]Candidate, MaxCandidates)
	for index := range candidates {
		provider := fmt.Sprintf("tvdb:%d", index+1)
		candidates[index] = Candidate{Title: fmt.Sprintf("Episode %d", index+1), Kind: "episode", ProviderID: provider}
	}
	discovery := Discovery{ID: "discovery-collections", ObservedAt: now, Readiness: "ready", Files: files, Candidates: candidates}
	fake := &fakeReader{discoveries: map[string]Discovery{discovery.ID: discovery}}
	handler := NewHandler(fake)
	initial := httptest.NewRecorder()
	handler.ServeHTTP(initial, httptest.NewRequest(http.MethodGet, "/discoveries/"+discovery.ID+"?rootId=root-a&limit=7&cursor=cursor-list", nil))
	if initial.Code != http.StatusOK {
		t.Fatalf("initial maximum collection status = %d; body=%s", initial.Code, initial.Body.String())
	}
	if !strings.Contains(initial.Body.String(), `name="association-256-role"`) || !strings.Contains(initial.Body.String(), `name="candidate-32-title"`) {
		t.Fatalf("initial response did not render the last collection indexes: %s", initial.Body.String())
	}

	values := url.Values{}
	values.Set("rootId", "root-a")
	values.Set("limit", "7")
	values.Set("cursor", "cursor-list")
	for index := range files {
		prefix := fmt.Sprintf("association-%d-", index)
		values.Set(prefix+"identity", fmt.Sprintf("draft-file-%d", index))
		values.Set(prefix+"episode", strconv.Itoa(index+1))
		values.Set(prefix+"language", "en")
		values.Set(prefix+"pair", fmt.Sprintf("pair-%d", index))
		values.Set(prefix+"forced", "false")
		values.Set(prefix+"sdh", "false")
		values.Set(prefix+"role", "video")
	}
	for index := range candidates {
		prefix := fmt.Sprintf("candidate-%d-", index)
		values.Set(prefix+"title", fmt.Sprintf("Draft candidate %d", index))
		values.Set(prefix+"provider", fmt.Sprintf("tvdb:%d", index+1000))
		values.Set(prefix+"external", fmt.Sprintf("episode-%d", index+1000))
		values.Set(prefix+"kind", "episode")
		values.Set(prefix+"season", "1")
		values.Set(prefix+"episodes", "1,2")
		values.Set(prefix+"year", "2026")
		values.Set(prefix+"score", "0.9137")
	}
	reload := httptest.NewRecorder()
	handler.ServeHTTP(reload, httptest.NewRequest(http.MethodGet, "/discoveries/"+discovery.ID+"?"+values.Encode(), nil))
	if reload.Code != http.StatusOK {
		t.Fatalf("full draft submission status = %d; body length=%d body=%s", reload.Code, reload.Body.Len(), reload.Body.String())
	}
	body := reload.Body.String()
	for _, expected := range []string{
		`name="association-256-identity" value="draft-file-256"`,
		`name="association-256-role" value="video"`,
		`name="candidate-32-title" value="Draft candidate 32"`,
		`name="candidate-32-score" value="0.9137"`,
		`name="rootId" value="root-a"`,
		`name="limit" value="7"`,
		`name="cursor" value="cursor-list"`,
	} {
		if !strings.Contains(body, expected) {
			t.Fatalf("full draft response lost %q: %s", expected, body)
		}
	}
	if strings.Index(body, `name="association-255-identity"`) >= strings.Index(body, `name="association-256-identity"`) {
		t.Fatalf("association controls are not stable at the collection boundary")
	}
	if strings.Index(body, `name="candidate-31-title"`) >= strings.Index(body, `name="candidate-32-title"`) {
		t.Fatalf("candidate controls are not stable at the collection boundary")
	}
}

func TestMaximumDraftFitsDefaultHTTPServerHeaderBudget(t *testing.T) {
	now := time.Date(2026, 9, 17, 16, 0, 0, 0, time.UTC)
	files := make([]File, MaxFilesPerDiscovery)
	for index := range files {
		files[index] = File{
			RootID:       "root-a",
			RelativePath: fmt.Sprintf("Show/%03d.mkv", index),
			Type:         "file",
			Role:         "video",
			Size:         1,
			FileIdentity: fmt.Sprintf("inode-%d", index),
		}
	}
	candidates := make([]Candidate, MaxCandidates)
	for index := range candidates {
		candidates[index] = Candidate{Title: "Episode", Kind: "episode", ProviderID: fmt.Sprintf("tvdb:%d", index+1)}
	}
	discovery := Discovery{ID: "discovery-transport-boundary", ObservedAt: now, Readiness: "ready", Files: files, Candidates: candidates}
	fake := &fakeReader{discoveries: map[string]Discovery{discovery.ID: discovery}}
	values := maximumTransportDraftValues(len(files), len(candidates))
	rawQuery := values.Encode()
	if len(rawQuery) > MaxQueryRawLength {
		t.Fatalf("maximum rendered query length = %d, exceeds parser bound %d", len(rawQuery), MaxQueryRawLength)
	}

	// ServeHTTP bypasses the net/http request-line/header parser. Keep this
	// direct assertion alongside the real server request so both boundaries
	// are covered.
	direct := httptest.NewRecorder()
	NewHandler(fake).ServeHTTP(direct, httptest.NewRequest(http.MethodGet, "/discoveries/"+discovery.ID+"?"+rawQuery, nil))
	if direct.Code != http.StatusOK {
		t.Fatalf("direct maximum request status = %d; body length=%d", direct.Code, direct.Body.Len())
	}
	directCalls := fake.callCount()

	server := httptest.NewServer(NewHandler(fake))
	defer server.Close()
	request, err := http.NewRequest(http.MethodGet, server.URL+"/discoveries/"+discovery.ID+"?"+rawQuery, nil)
	if err != nil {
		t.Fatalf("maximum request construction failed: %v", err)
	}
	response, err := server.Client().Do(request)
	if err != nil {
		t.Fatalf("default http.Server rejected maximum rendered request: %v", err)
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, response.Body)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("default http.Server maximum request status = %d; want 200", response.StatusCode)
	}
	if fake.callCount() <= directCalls {
		t.Fatalf("real server request did not reach the inventory reader: calls before=%d after=%d", directCalls, fake.callCount())
	}
}

func maximumTransportDraftValues(files, candidates int) url.Values {
	values := url.Values{}
	values.Set("rootId", "root-a")
	values.Set("limit", "7")
	values.Set("cursor", "cursor-list")
	// Ampersands exercise the escaped-value expansion while remaining valid
	// bounded draft text after url.ParseQuery decodes them.
	scalar := strings.Repeat("&", MaxInputLength)
	for index := 0; index < files; index++ {
		prefix := fmt.Sprintf("association-%d-", index)
		values.Set(prefix+"identity", scalar)
		values.Set(prefix+"episode", scalar)
		values.Set(prefix+"language", scalar)
		values.Set(prefix+"pair", scalar)
		values.Set(prefix+"forced", "false")
		values.Set(prefix+"sdh", "false")
		values.Set(prefix+"role", "video")
	}
	episodes := strings.Repeat("2147483647,", MaxAssociationInputs-1) + "2147483647"
	for index := 0; index < candidates; index++ {
		prefix := fmt.Sprintf("candidate-%d-", index)
		values.Set(prefix+"title", scalar)
		values.Set(prefix+"provider", scalar)
		values.Set(prefix+"external", scalar)
		values.Set(prefix+"kind", "episode")
		values.Set(prefix+"season", "2147483647")
		values.Set(prefix+"episodes", episodes)
		values.Set(prefix+"year", "2147483647")
		values.Set(prefix+"score", "0.9137")
	}
	return values
}

func TestFixedDetailDraftVocabularyIsRouteScopedAndValidated(t *testing.T) {
	now := time.Date(2026, 9, 17, 15, 40, 0, 0, time.UTC)
	fake := &fakeReader{
		downloads:   map[string]Download{"download-fixed": {ID: "download-fixed", ConnectionID: "qbit-a", State: "complete", ObservedAt: now}},
		descriptors: map[string]Descriptor{"descriptor-fixed": {ID: "descriptor-fixed", Type: "torrent", Size: 1, Digest: "sha256:1", Availability: "available", CapturedAt: now}},
		media:       map[string]Media{"media-fixed": {ID: "media-fixed", Kind: "movie", ProviderID: "tmdb:1", ObservedAt: now, Tracking: []Tracking{}}},
	}
	for _, tc := range []struct {
		name string
		path string
	}{
		{name: "download identity", path: "/downloads/download-fixed?identity=draft"},
		{name: "download episode", path: "/downloads/download-fixed?episode=1"},
		{name: "descriptor selection", path: "/descriptors/descriptor-fixed?selection=one"},
		{name: "descriptor subtitle flag", path: "/descriptors/descriptor-fixed?subtitleForced=true"},
		{name: "descriptor kind", path: "/descriptors/descriptor-fixed?kind=movie"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			NewHandler(fake).ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, tc.path, nil))
			if recorder.Code != http.StatusBadRequest {
				t.Fatalf("status = %d; body=%s", recorder.Code, recorder.Body.String())
			}
		})
	}

	for _, query := range []string{"subtitleForced=maybe", "subtitleSDH=1", "providerId=tmdb%3A1%2Fforeign"} {
		recorder := httptest.NewRecorder()
		NewHandler(fake).ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/media/media-fixed?"+query, nil))
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("invalid media detail query %q status = %d; body=%s", query, recorder.Code, recorder.Body.String())
		}
	}
	recorder := httptest.NewRecorder()
	NewHandler(fake).ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/media/media-fixed?identity=draft&providerId=tmdb%3A2&subtitleForced=false&kind=anime", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("valid media detail draft status = %d; body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestHTTPReaderRejectsForeignDetailIdentity(t *testing.T) {
	const (
		requested = "00000000-0000-0000-0000-000000000001"
		foreign   = "00000000-0000-0000-0000-000000000002"
	)
	now := "2026-09-17T13:30:00Z"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		var body string
		switch {
		case strings.HasPrefix(r.URL.Path, "/api/v1/discoveries/"):
			body = fmt.Sprintf(`{"id":%q,"files":[],"readiness":"unknown","observedAt":%q}`, foreign, now)
		case strings.HasPrefix(r.URL.Path, "/api/v1/media/"):
			body = fmt.Sprintf(`{"id":%q,"kind":"movie","providerId":"tmdb:1","tracking":[],"observedAt":%q}`, foreign, now)
		case strings.HasPrefix(r.URL.Path, "/api/v1/downloads/"):
			body = fmt.Sprintf(`{"id":%q,"connectionId":"qbit-a","state":"unknown","observedAt":%q}`, foreign, now)
		case strings.HasPrefix(r.URL.Path, "/api/v1/descriptors/"):
			body = fmt.Sprintf(`{"id":%q,"type":"unknown","size":0,"digest":"sha256:empty","availability":"unknown","capturedAt":%q}`, foreign, now)
		default:
			http.NotFound(w, r)
			return
		}
		_, _ = io.WriteString(w, body)
	}))
	defer server.Close()
	reader, err := NewHTTPReader(server.URL, server.Client(), time.Second)
	if err != nil {
		t.Fatalf("NewHTTPReader() error = %v", err)
	}
	checks := []struct {
		name string
		read func() error
	}{
		{name: "discovery", read: func() error { _, err := reader.GetDiscovery(context.Background(), requested); return err }},
		{name: "media", read: func() error { _, err := reader.GetMedia(context.Background(), requested); return err }},
		{name: "download", read: func() error { _, err := reader.GetDownload(context.Background(), requested); return err }},
		{name: "descriptor", read: func() error { _, err := reader.GetDescriptor(context.Background(), requested); return err }},
	}
	for _, check := range checks {
		t.Run(check.name, func(t *testing.T) {
			readErr := check.read()
			var apiErr *APIError
			if !errors.As(readErr, &apiErr) || apiErr.Kind() != ErrorProtocol {
				t.Fatalf("error = %T %v; APIError = %#v", readErr, readErr, apiErr)
			}
		})
	}
}

func TestHTTPReaderRejectsMalformedNestedEvidence(t *testing.T) {
	now := "2026-09-17T13:40:00Z"
	page := func(items string) string {
		return fmt.Sprintf(`{"items":%s,"page":{"nextCursor":null,"coverage":[],"observedAt":%q}}`, items, now)
	}
	cases := []struct {
		name string
		path string
		body string
		read func(*HTTPReader) error
	}{
		{name: "candidate kind", path: "/api/v1/discoveries", body: page(fmt.Sprintf(`[{"id":"00000000-0000-0000-0000-000000000001","files":[],"readiness":"unknown","observedAt":%q,"candidates":[{"kind":"","title":"title"}]}]`, now)), read: func(r *HTTPReader) error {
			_, err := r.ListDiscoveries(context.Background(), PageRequest{})
			return err
		}},
		{name: "provenance target", path: "/api/v1/discoveries", body: page(fmt.Sprintf(`[{"id":"00000000-0000-0000-0000-000000000001","files":[],"readiness":"unknown","observedAt":%q,"provenance":[{"sourcePath":{"rootId":"root-a","relativePath":"../secret"}}]}]`, now)), read: func(r *HTTPReader) error {
			_, err := r.ListDiscoveries(context.Background(), PageRequest{})
			return err
		}},
		{name: "file role", path: "/api/v1/discoveries", body: page(fmt.Sprintf(`[{"id":"00000000-0000-0000-0000-000000000001","files":[{"rootId":"root-a","relativePath":"Show/E01.mkv","type":"file","size":1,"role":"future"}],"readiness":"unknown","observedAt":%q}]`, now)), read: func(r *HTTPReader) error {
			_, err := r.ListDiscoveries(context.Background(), PageRequest{})
			return err
		}},
		{name: "tracking dimension", path: "/api/v1/media", body: page(fmt.Sprintf(`[{"id":"00000000-0000-0000-0000-000000000001","kind":"movie","providerId":"tmdb:1","tracking":[{"connectionId":"sonarr-a","dimension":"future","value":"unknown","observedAt":%q}],"observedAt":%q}]`, now, now)), read: func(r *HTTPReader) error { _, err := r.ListMedia(context.Background(), PageRequest{}); return err }},
		{name: "download state", path: "/api/v1/downloads", body: page(fmt.Sprintf(`[{"id":"00000000-0000-0000-0000-000000000001","connectionId":"qbit-a","state":"future","observedAt":%q}]`, now)), read: func(r *HTTPReader) error { _, err := r.ListDownloads(context.Background(), PageRequest{}); return err }},
		{name: "descriptor availability", path: "/api/v1/descriptors", body: page(fmt.Sprintf(`[{"id":"00000000-0000-0000-0000-000000000001","type":"torrent","size":1,"digest":"sha256:1","availability":"future","capturedAt":%q}]`, now)), read: func(r *HTTPReader) error {
			_, err := r.ListDescriptors(context.Background(), PageRequest{})
			return err
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != tc.path {
					t.Errorf("request path = %q, want %q", r.URL.Path, tc.path)
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, tc.body)
			}))
			defer server.Close()
			reader, err := NewHTTPReader(server.URL, server.Client(), time.Second)
			if err != nil {
				t.Fatalf("NewHTTPReader() error = %v", err)
			}
			readErr := tc.read(reader)
			var apiErr *APIError
			if !errors.As(readErr, &apiErr) || apiErr.Kind() != ErrorProtocol {
				t.Fatalf("error = %T %v; APIError = %#v", readErr, readErr, apiErr)
			}
		})
	}
}

func intPtr(value int) *int { return &value }

func coveragePtr(value Coverage) *Coverage { return &value }
