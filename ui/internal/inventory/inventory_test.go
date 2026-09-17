package inventory

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
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
			Readiness:  "<script>alert(1)</script>",
			Files: []File{{
				RootID:       "root-a",
				RelativePath: "Shows/<pilot>.mkv",
				Type:         "video",
				Role:         "video",
				Size:         42,
				FileIdentity: "inode-1",
			}},
			Candidates: []Candidate{{
				Title:      "Candidate",
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
	if strings.Contains(body, "<script>") || !strings.Contains(body, "&lt;script&gt;alert(1)&lt;/script&gt;") {
		t.Fatalf("state was not safely escaped: %s", body)
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
			{ConnectionID: "sonarr-a", Dimension: "registration", Value: "registered", ProviderID: "tvdb:10", ObservedAt: now},
			{ConnectionID: "jellyfin-b", Dimension: "availability", Value: "unknown", ObservedAt: now},
			{ConnectionID: "seerr-c", Dimension: "request", Value: "approved", ObservedAt: now},
		},
	}
	fake := &fakeReader{media: map[string]Media{"media-1": media}}
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/media/media-1?identity=Draft+Title&selection=episode-3&kind=movie", nil)
	NewHandler(fake).ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%s", recorder.Code, recorder.Body.String())
	}
	body := recorder.Body.String()
	for _, expected := range []string{"value=\"Draft Title\"", "sonarr-a", "jellyfin-b", "seerr-c", "registration", "availability", "request", "unknown", "/media/media-1"} {
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
			Type:         "video",
			Role:         "video",
			Size:         99,
		}},
		Candidates: []Candidate{{Title: "<b>unsafe</b>", Kind: "episode", ProviderID: "tvdb:44"}},
	}
	fake.discoveries = map[string]Discovery{"discovery-2": discovery}
	recorder = httptest.NewRecorder()
	request = httptest.NewRequest(http.MethodGet, "/discoveries/discovery-2?association-0-language=pt-BR&association-0-forced=true", nil)
	NewHandler(fake).ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("discovery detail status = %d; body=%s", recorder.Code, recorder.Body.String())
	}
	body = recorder.Body.String()
	for _, expected := range []string{"name=\"association-0-language\" value=\"pt-BR\"", "name=\"association-0-forced\" value=\"true\"", "value=\"&lt;b&gt;unsafe&lt;/b&gt;\"", "Read-only observation"} {
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
			_, _ = io.WriteString(w, page(fmt.Sprintf(`[{"id":%q,"observedAt":%q,"readiness":"ready","files":[{"relativePath":"Show/E01.mkv","rootId":"root-a","size":42,"type":"video"}]}]`, idDiscovery, now)))
		case "/api/v1/media":
			_, _ = io.WriteString(w, page(fmt.Sprintf(`[{"id":%q,"kind":"movie","providerId":"tmdb:101","observedAt":%q,"tracking":[]}]`, idMedia, now)))
		case "/api/v1/downloads":
			_, _ = io.WriteString(w, page(fmt.Sprintf(`[{"id":%q,"connectionId":"qbit-a","state":"seeding","observedAt":%q}]`, idDownload, now)))
		case "/api/v1/descriptors":
			_, _ = io.WriteString(w, page(fmt.Sprintf(`[{"id":%q,"type":"original","size":42,"digest":"sha256:abc","availability":"available","capturedAt":%q}]`, idDescriptor, now)))
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

func intPtr(value int) *int { return &value }

func coveragePtr(value Coverage) *Coverage { return &value }
