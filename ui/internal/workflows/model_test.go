package workflows

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
	page     WorkflowPage
	pageErr  error
	items    map[string]Workflow
	itemErr  error
	requests int
}

func (f *fakeReader) ListWorkflows(_ context.Context, _ PageRequest) (WorkflowPage, error) {
	f.mu.Lock()
	f.requests++
	f.mu.Unlock()
	return f.page, f.pageErr
}

func (f *fakeReader) GetWorkflow(_ context.Context, id string) (Workflow, error) {
	f.mu.Lock()
	f.requests++
	f.mu.Unlock()
	if f.itemErr != nil {
		return Workflow{}, f.itemErr
	}
	item, ok := f.items[id]
	if !ok {
		return Workflow{}, ErrNotFound
	}
	return item, nil
}

func workflowFixture() Workflow {
	now := time.Date(2026, 9, 17, 15, 0, 0, 0, time.UTC)
	retryAt := now.Add(30 * time.Second)
	return Workflow{
		ID:                   "workflow-1",
		Name:                 "register then import",
		RecipeVersion:        "recipe-3",
		State:                "needs_review",
		CurrentStep:          "import",
		DeadlineAt:           func() *time.Time { value := now.Add(time.Hour); return &value }(),
		AggregateEffectCount: func() *int { value := 2; return &value }(),
		UnresolvedCount:      func() *int { value := 2; return &value }(),
		ApprovalGates:        []string{"registration approval", "exact import approval"},
		Cancellation: &Cancellation{
			ID:          "cancel-1",
			State:       "acknowledged",
			RequestedAt: now,
			Reason:      "operator requested stop",
		},
		Steps: []Step{
			{
				Sequence:         1,
				ID:               "registration",
				ActionPlanID:     "plan-registration",
				ActionRunID:      "run-registration",
				ApprovalGate:     "registration approval",
				ActionKind:       "arr.registration",
				ActionConnection: "sonarr-1",
				ActionMediaKind:  "episode",
				ActionProviderID: "tvdb:123",
				ActionMonitoring: func() *bool { value := false; return &value }(),
				State:            "succeeded",
				Outcome:          "applied",
				LastObservation:  "registration accepted",
				LastObservedAt:   &now,
				Effects:          []Effect{{ID: "series:101", State: "applied", Outcome: "applied", ObservedAt: &now}},
			},
			{
				Sequence:          2,
				ID:                "import",
				ActionPlanID:      "plan-import",
				ActionRunID:       "run-import",
				ApprovalGate:      "exact import approval",
				ActionKind:        "arr.import",
				ActionConnection:  "sonarr-1",
				ActionExternalID:  "series-101",
				ActionPreview:     "preview-7",
				ActionTransfer:    "copy",
				State:             "reconciling",
				Outcome:           "unknown",
				LastObservation:   "lost response; read-back pending",
				LastObservedAt:    &now,
				RetryAt:           &retryAt,
				RetryReason:       "reconcile before retry",
				UnresolvedEffects: []string{"episode:101", "subtitle:en"},
				Effects:           []Effect{{ID: "file:101", State: "unknown", Evidence: "read-back incomplete"}},
				ActionFiles:       []ActionFile{{SourceRootID: "downloads", SourcePath: "Show/S01E01.mkv", MovieOrEpisodeID: 101, Role: "video"}, {SourceRootID: "downloads", SourcePath: "Show/S01E01.en.srt", MovieOrEpisodeID: 101, Role: "subtitle"}},
				Error:             "external result unresolved",
				Impacts:           []string{"client import", "torrent state remains unchanged"},
			},
		},
	}
}

func TestWorkflowDetailRendersOrderedPhasesEffectsAndCancellation(t *testing.T) {
	item := workflowFixture()
	fake := &fakeReader{items: map[string]Workflow{"workflow-1": item}}
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/workflows/workflow-1?idempotencyKey=retry-key&reason=lost+response&selectedStep=import&digest=plan-digest", nil)
	NewHandler(fake).ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	body := recorder.Body.String()
	for _, expected := range []string{
		"register then import", "recipe-3", "needs_review", "registration approval", "exact import approval",
		"Step 1: registration", "Step 2: import", "arr.registration", "arr.import", "applied", "reconciling",
		"lost response; read-back pending", "Retry at", "reconcile before retry", "episode:101", "subtitle:en",
		"external result unresolved", "client import", "Cancellation acknowledged", "No further undispatched step",
		"retry-key", "lost response", "GET fields preserve review context", "API owns approval",
	} {
		if !strings.Contains(body, expected) {
			t.Fatalf("body missing %q: %s", expected, body)
		}
	}
	if recorder.Header().Get("Cache-Control") != "no-store" || recorder.Header().Get("X-Robots-Tag") != "noindex, nofollow" {
		t.Fatalf("private headers=%#v", recorder.Header())
	}
}

func TestWorkflowListPaginationAndReadOnlyGate(t *testing.T) {
	now := time.Date(2026, 9, 17, 15, 30, 0, 0, time.UTC)
	next := "next-workflow"
	fake := &fakeReader{page: WorkflowPage{Items: []Workflow{workflowFixture()}, Page: PageInfo{NextCursor: next, ObservedAt: now}}}
	recorder := httptest.NewRecorder()
	NewHandler(fake).ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/workflows?cursor=before&limit=3&reason=keep", nil))
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), "cursor=next-workflow") || !strings.Contains(recorder.Body.String(), "reason=keep") {
		t.Fatalf("list status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	before := fake.requests
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete} {
		recorder = httptest.NewRecorder()
		NewHandler(fake).ServeHTTP(recorder, httptest.NewRequest(method, "/workflows/workflow-1", nil))
		if recorder.Code != http.StatusMethodNotAllowed || recorder.Header().Get("Allow") != "GET, HEAD" || fake.requests != before {
			t.Fatalf("%s status=%d allow=%q requests=%d before=%d", method, recorder.Code, recorder.Header().Get("Allow"), fake.requests, before)
		}
	}
}

func TestWorkflowErrorsStaySanitizedAndUnknownEvidenceFailsClosed(t *testing.T) {
	fake := &fakeReader{itemErr: errors.New("GET https://private.example/workflow?token=secret body=private")}
	recorder := httptest.NewRecorder()
	NewHandler(fake).ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/workflows/workflow-1", nil))
	body := recorder.Body.String()
	if recorder.Code != http.StatusServiceUnavailable || strings.Contains(body, "private.example") || strings.Contains(body, "secret") || strings.Contains(body, "body=private") {
		t.Fatalf("sanitization status=%d body=%s", recorder.Code, body)
	}

	item := workflowFixture()
	item.State = "future-state"
	fake = &fakeReader{items: map[string]Workflow{"workflow-1": item}}
	recorder = httptest.NewRecorder()
	NewHandler(fake).ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/workflows/workflow-1", nil))
	if recorder.Code != http.StatusServiceUnavailable || !strings.Contains(recorder.Body.String(), "incomplete or invalid") {
		t.Fatalf("unknown state status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestWorkflowAlreadySatisfiedAndDeadlineStatesRemainVisible(t *testing.T) {
	item := workflowFixture()
	item.State = "deadline_exceeded"
	item.Cancellation = nil
	item.AggregateEffectCount = func() *int { value := 1; return &value }()
	item.UnresolvedCount = func() *int { value := 0; return &value }()
	item.Steps = []Step{{ID: "copy", State: "already_satisfied", Outcome: "already_satisfied", ActionPlanID: "plan-copy", Effects: []Effect{{ID: "file-1", State: "already_satisfied", Outcome: "already_satisfied"}}}}
	fake := &fakeReader{items: map[string]Workflow{"workflow-1": item}}
	recorder := httptest.NewRecorder()
	NewHandler(fake).ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/workflows/workflow-1", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	for _, expected := range []string{"deadline_exceeded", "already_satisfied", "file-1"} {
		if !strings.Contains(recorder.Body.String(), expected) {
			t.Fatalf("body missing %q: %s", expected, recorder.Body.String())
		}
	}
}

func TestWorkflowRejectsDuplicateStepAndMalformedDraft(t *testing.T) {
	item := workflowFixture()
	item.Steps[1].ID = item.Steps[0].ID
	fake := &fakeReader{items: map[string]Workflow{"workflow-1": item}}
	recorder := httptest.NewRecorder()
	NewHandler(fake).ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/workflows/workflow-1", nil))
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("duplicate step status=%d body=%s", recorder.Code, recorder.Body.String())
	}

	item = workflowFixture()
	fake.items["workflow-1"] = item
	recorder = httptest.NewRecorder()
	NewHandler(fake).ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/workflows/workflow-1?unknown=dispatch&idempotencyKey=a&idempotencyKey=b", nil))
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("malformed draft status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestWorkflowRejectsContradictoryEffectEvidence(t *testing.T) {
	cases := []struct {
		name  string
		apply func(*Workflow)
	}{
		{name: "duplicate observed identity", apply: func(item *Workflow) {
			item.AggregateEffectCount = func() *int { value := 3; return &value }()
			item.Steps[0].Effects = append(item.Steps[0].Effects, Effect{ID: "series:101", State: "applied", Outcome: "applied"})
		}},
		{name: "resolved and unresolved overlap", apply: func(item *Workflow) {
			item.Steps[1].UnresolvedEffects = append(item.Steps[1].UnresolvedEffects, "file:101")
		}},
		{name: "complete outcome without identity", apply: func(item *Workflow) {
			item.AggregateEffectCount = func() *int { value := 0; return &value }()
			item.UnresolvedCount = func() *int { value := 0; return &value }()
			item.Steps = []Step{{ID: "copy", ActionPlanID: "plan-copy", State: "succeeded", Outcome: "applied"}}
		}},
		{name: "aggregate count mismatch", apply: func(item *Workflow) {
			item.AggregateEffectCount = func() *int { value := 1; return &value }()
		}},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			item := workflowFixture()
			testCase.apply(&item)
			fake := &fakeReader{items: map[string]Workflow{"workflow-1": item}}
			recorder := httptest.NewRecorder()
			NewHandler(fake).ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/workflows/workflow-1", nil))
			if recorder.Code != http.StatusServiceUnavailable || !strings.Contains(recorder.Body.String(), "incomplete or invalid") {
				t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
			}
		})
	}
}

func TestWorkflowReasonDraftBoundaryMatchesRenderedLimit(t *testing.T) {
	fake := &fakeReader{items: map[string]Workflow{"workflow-1": workflowFixture()}}
	for _, size := range []int{512, 513, 4096, 4097} {
		t.Run(strconv.Itoa(size), func(t *testing.T) {
			recorder := httptest.NewRecorder()
			query := "reason=" + url.QueryEscape(strings.Repeat("r", size))
			NewHandler(fake).ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/workflows/workflow-1?"+query, nil))
			if size <= MaxTextLength && recorder.Code != http.StatusOK {
				t.Fatalf("size=%d status=%d body=%s", size, recorder.Code, recorder.Body.String())
			}
			if size > MaxTextLength && recorder.Code != http.StatusBadRequest {
				t.Fatalf("size=%d status=%d body=%s", size, recorder.Code, recorder.Body.String())
			}
		})
	}
}
