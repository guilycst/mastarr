package review

import (
	"context"
	"errors"
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
	mu       sync.Mutex
	page     ReviewPage
	pageErr  error
	items    map[string]Review
	itemErr  error
	requests int
}

func (f *fakeReader) ListReviews(_ context.Context, _ PageRequest) (ReviewPage, error) {
	f.mu.Lock()
	f.requests++
	f.mu.Unlock()
	return f.page, f.pageErr
}

func (f *fakeReader) GetReview(_ context.Context, id string) (Review, error) {
	f.mu.Lock()
	f.requests++
	f.mu.Unlock()
	if f.itemErr != nil {
		return Review{}, f.itemErr
	}
	item, ok := f.items[id]
	if !ok {
		return Review{}, ErrNotFound
	}
	return item, nil
}

func (f *fakeReader) requestCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.requests
}

func reviewFixture(kind string) Review {
	now := time.Date(2026, 9, 17, 13, 0, 0, 0, time.UTC)
	monitoring := false
	item := Review{
		ID:               "plan-1",
		Revision:         7,
		Digest:           "sha256:immutable-plan",
		Status:           "ready",
		RequiredApproval: "review",
		CreatedAt:        &now,
		ExpiresAt:        func() *time.Time { value := now.Add(15 * time.Minute); return &value }(),
		Action: Action{
			Kind:            kind,
			ConnectionID:    "sonarr-1",
			MediaKind:       "episode",
			ProviderID:      "tvdb:123",
			Monitoring:      &monitoring,
			PreviewRevision: "preview-7",
			Transfer:        "copy",
			Files: []ActionFile{{
				SourceRootID:     "root-downloads",
				SourcePath:       "Series/Season 01/E01.mkv",
				Role:             "video",
				MovieOrEpisodeID: 101,
			}, {
				SourceRootID:     "root-downloads",
				SourcePath:       "Series/Season 01/E01.en.srt",
				Role:             "subtitle",
				MovieOrEpisodeID: 101,
				Subtitle:         func() *bool { value := true; return &value }(),
				Language:         "en",
				Forced:           func() *bool { value := false; return &value }(),
			}},
		},
		Manifest: []ManifestEntry{{
			RootID:       "root-downloads",
			RelativePath: "Series/Season 01/E01.mkv",
			Type:         "file",
			Role:         "video",
			Size:         1024,
			Identity:     "inode-1",
			ObservedAt:   &now,
		}},
		Preconditions:    []string{"registration observed", "exact preview revision"},
		ConnectionFences: map[string]string{"sonarr-1": "connection-r7"},
		MappingFences:    map[string]string{"mapping-1": "mapping-r4"},
		Capabilities:     []string{"read-back"},
		Impacts:          []string{"client import", "future tracking remains opt-in"},
		DesiredStateKeys: []string{"episodes", "providerId"},
	}
	if kind == "arr.registration" {
		item.Action.Files = nil
		item.Action.PreviewRevision = ""
	} else if kind == "arr.import" {
		item.Action.RegisteredExternalID = "series-101"
	}
	return item
}

func TestRegistrationReviewRendersTwoExplicitPhasesAndExactScope(t *testing.T) {
	item := reviewFixture("arr.registration")
	fake := &fakeReader{items: map[string]Review{"plan-1": item}}
	request := httptest.NewRequest(http.MethodGet, "/reviews/plan-1?decision=approve&idempotencyKey=retry-key&reason=keep+draft&monitoring=false&fallback=hardlink&digest=stale", nil)
	recorder := httptest.NewRecorder()
	NewHandler(fake).ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%s", recorder.Code, recorder.Body.String())
	}
	body := recorder.Body.String()
	for _, expected := range []string{
		"1. Registration approval", "2. Exact import approval",
		"Registration approval is never reused as import approval",
		"sha256:immutable-plan", "connection-r7", "mapping-r4",
		"Series/Season 01/E01.mkv", "inode-1", "registration observed",
		"Idempotency key", "retry-key", "keep draft", "Monitoring is unchecked",
		"API-owned observation",
	} {
		if !strings.Contains(strings.ToLower(body), strings.ToLower(expected)) {
			t.Fatalf("body missing %q: %s", expected, body)
		}
	}
	if strings.Contains(body, "<script>") {
		t.Fatalf("unescaped content rendered: %s", body)
	}
	if recorder.Header().Get("Cache-Control") != "no-store" || recorder.Header().Get("X-Robots-Tag") != "noindex, nofollow" {
		t.Fatalf("private headers = %#v", recorder.Header())
	}
	if fake.requestCount() != 1 {
		t.Fatalf("reader requests = %d, want 1", fake.requestCount())
	}
}

func TestImportReviewRendersAssociationsAndPreservesDraftFields(t *testing.T) {
	item := reviewFixture("arr.import")
	fake := &fakeReader{items: map[string]Review{"plan-1": item}}
	query := "decision=approve&idempotencyKey=lost-response-key&monitoring=true&fallback=copy&planRevision=7&digest=forged&sourceRevision=source-2&configRevision=config-3&mappingRevision=mapping-4&manifestDigest=manifest-5&desiredDigest=desired-6&selectedEpisode=101"
	recorder := httptest.NewRecorder()
	NewHandler(fake).ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/reviews/plan-1?"+query, nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%s", recorder.Code, recorder.Body.String())
	}
	body := recorder.Body.String()
	for _, expected := range []string{
		"1. Registration prerequisite", "2. Exact import approval", "Preview revision", "preview-7",
		"E01.mkv", "E01.en.srt", "subtitle", "en", "true",
		"lost-response-key", "sourceRevision", "source-2", "configRevision", "config-3",
		"mappingRevision", "mapping-4", "manifestDigest", "manifest-5", "desiredDigest", "desired-6",
		`name="fallback" value="copy" checked`,
	} {
		if !strings.Contains(body, expected) {
			t.Fatalf("body missing %q: %s", expected, body)
		}
	}
	if strings.Contains(body, `name="monitoring"`) || strings.Contains(body, "future monitoring") {
		t.Fatalf("import-only review rendered inapplicable monitoring control: %s", body)
	}
}

func TestReviewRendersActionAuthorityAndScopesControlsByKind(t *testing.T) {
	trueValue := true
	cases := []struct {
		name     string
		kind     string
		prepare  func(*Review)
		contains []string
		controls []string
	}{
		{name: "client stop", kind: "client.stop", prepare: func(item *Review) { item.Action.ClientItemIDs = []string{"torrent-exact-1"} }, contains: []string{"torrent-exact-1", "Client or torrent IDs"}, controls: []string{"monitoring", "Transfer fallback"}},
		{name: "client remove", kind: "client.remove", prepare: func(item *Review) {
			item.Action.ClientItemIDs = []string{"torrent-exact-1"}
			item.Action.RetainPayload = &trueValue
		}, contains: []string{"torrent-exact-1", "Retain payload", "Irreversible scope"}, controls: []string{"monitoring"}},
		{name: "trash", kind: "fs.trash", prepare: func(item *Review) {
			days := 30
			item.Action.RetentionDays = &days
			item.Action.StoppedClientIDs = []string{"torrent-exact-1"}
		}, contains: []string{"Retention days", "30", "Stopped-client prerequisites", "torrent-exact-1"}, controls: []string{"monitoring", "Transfer fallback"}},
		{name: "delete", kind: "fs.delete", prepare: func(item *Review) {
			item.Action.Files = []ActionFile{{SourceRootID: "downloads", SourcePath: "Show/E01.mkv"}}
			item.Action.Permanent = &trueValue
			item.Action.Irreversible = true
		}, contains: []string{"Permanent deletion", "true", "Irreversible scope"}, controls: []string{"monitoring", "Transfer fallback"}},
		{name: "hardlink", kind: "fs.hardlink", prepare: func(item *Review) {
			for index := range item.Action.Files {
				item.Action.Files[index].DestinationRootID = "library"
				item.Action.Files[index].DestinationPath = item.Action.Files[index].SourcePath
			}
		}, contains: []string{"Estimated storage bytes", "unknown", "Hardlink failure requires a new copy plan"}, controls: []string{"monitoring"}},
		{name: "descriptor delete", kind: "descriptor.delete", prepare: func(item *Review) {
			item.Action.Files = []ActionFile{{Identity: "descriptor-1"}}
			item.Action.Irreversible = true
		}, contains: []string{"descriptor-1", "Irreversible scope"}, controls: []string{"monitoring", "Transfer fallback"}},
		{name: "jellyfin refresh", kind: "jellyfin.refresh", prepare: func(item *Review) { item.Action.Files = nil }, contains: []string{"Action kind", "jellyfin.refresh", "Capabilities"}, controls: []string{"monitoring", "Transfer fallback"}},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			item := reviewFixture(testCase.kind)
			testCase.prepare(&item)
			fake := &fakeReader{items: map[string]Review{"plan-1": item}}
			recorder := httptest.NewRecorder()
			NewHandler(fake).ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/reviews/plan-1", nil))
			if recorder.Code != http.StatusOK {
				t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
			}
			body := recorder.Body.String()
			for _, expected := range testCase.contains {
				if !strings.Contains(body, expected) {
					t.Fatalf("body missing %q: %s", expected, body)
				}
			}
			for _, forbidden := range testCase.controls {
				if strings.Contains(body, forbidden) {
					t.Fatalf("body rendered inapplicable control %q: %s", forbidden, body)
				}
			}
		})
	}
}

func TestReviewIsReadOnlyAndRejectsMalformedDraft(t *testing.T) {
	fake := &fakeReader{items: map[string]Review{"plan-1": reviewFixture("arr.registration")}}
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete} {
		recorder := httptest.NewRecorder()
		NewHandler(fake).ServeHTTP(recorder, httptest.NewRequest(method, "/reviews/plan-1", nil))
		if recorder.Code != http.StatusMethodNotAllowed || recorder.Header().Get("Allow") != "GET, HEAD" {
			t.Fatalf("%s status=%d allow=%q", method, recorder.Code, recorder.Header().Get("Allow"))
		}
	}
	before := fake.requestCount()
	recorder := httptest.NewRecorder()
	NewHandler(fake).ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/reviews/plan-1?unknown=write&decision=approve&decision=reject", nil))
	if recorder.Code != http.StatusBadRequest || fake.requestCount() != before {
		t.Fatalf("malformed draft status=%d requests=%d before=%d", recorder.Code, fake.requestCount(), before)
	}
}

func TestReviewRejectsForeignIdentityAndSanitizesReaderError(t *testing.T) {
	item := reviewFixture("arr.registration")
	item.ID = "foreign-plan"
	fake := &fakeReader{items: map[string]Review{"plan-1": item}}
	recorder := httptest.NewRecorder()
	NewHandler(fake).ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/reviews/plan-1", nil))
	if recorder.Code != http.StatusServiceUnavailable || !strings.Contains(recorder.Body.String(), "unavailable") {
		t.Fatalf("foreign identity status=%d body=%s", recorder.Code, recorder.Body.String())
	}

	fake = &fakeReader{itemErr: errors.New("GET https://private.example/review?token=secret body=private")}
	recorder = httptest.NewRecorder()
	NewHandler(fake).ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/reviews/plan-1", nil))
	body := recorder.Body.String()
	if recorder.Code != http.StatusServiceUnavailable || strings.Contains(body, "private.example") || strings.Contains(body, "secret") || strings.Contains(body, "body=private") {
		t.Fatalf("unsanitized error status=%d body=%s", recorder.Code, body)
	}
}

func TestReviewListPreservesCursorAndEmptyIsExplicit(t *testing.T) {
	now := time.Date(2026, 9, 17, 14, 0, 0, 0, time.UTC)
	next := "next-cursor"
	fake := &fakeReader{page: ReviewPage{Items: []Review{reviewFixture("fs.delete")}, Page: PageInfo{NextCursor: next, ObservedAt: now}}}
	recorder := httptest.NewRecorder()
	NewHandler(fake).ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/reviews?cursor=old-cursor&limit=2&reason=review", nil))
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), "cursor=next-cursor") || !strings.Contains(recorder.Body.String(), "reason=review") {
		t.Fatalf("list response status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	fake.page = ReviewPage{Page: PageInfo{ObservedAt: now}}
	recorder = httptest.NewRecorder()
	NewHandler(fake).ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/reviews", nil))
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), "No review plans were observed") {
		t.Fatalf("empty list status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestReviewValidationRejectsUnknownStatusAndInvalidScope(t *testing.T) {
	item := reviewFixture("arr.registration")
	item.Status = "future-state"
	fake := &fakeReader{items: map[string]Review{"plan-1": item}}
	recorder := httptest.NewRecorder()
	NewHandler(fake).ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/reviews/plan-1", nil))
	if recorder.Code != http.StatusServiceUnavailable || !strings.Contains(recorder.Body.String(), "incomplete or invalid") {
		t.Fatalf("unknown status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	item = reviewFixture("fs.copy")
	item.Manifest[0].RelativePath = "../outside"
	fake.items["plan-1"] = item
	recorder = httptest.NewRecorder()
	NewHandler(fake).ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/reviews/plan-1", nil))
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("invalid scope status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestReviewReasonDraftBoundaryMatchesRenderedLimit(t *testing.T) {
	fake := &fakeReader{items: map[string]Review{"plan-1": reviewFixture("arr.registration")}}
	for _, size := range []int{512, 513, 4096, 4097} {
		t.Run(strconv.Itoa(size), func(t *testing.T) {
			recorder := httptest.NewRecorder()
			query := "reason=" + url.QueryEscape(strings.Repeat("r", size))
			NewHandler(fake).ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/reviews/plan-1?"+query, nil))
			if size <= MaxTextLength && recorder.Code != http.StatusOK {
				t.Fatalf("size=%d status=%d body=%s", size, recorder.Code, recorder.Body.String())
			}
			if size > MaxTextLength && recorder.Code != http.StatusBadRequest {
				t.Fatalf("size=%d status=%d body=%s", size, recorder.Code, recorder.Body.String())
			}
		})
	}
}
