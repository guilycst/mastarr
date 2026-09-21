package trash

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const trashID = "123e4567-e89b-12d3-a456-426614174000"

func TestHTTPReaderAndHandlerPreserveTrashEvidence(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/trash":
			fmt.Fprint(w, trashListJSON(trashID))
		case "/api/v1/trash/" + trashID:
			fmt.Fprint(w, trashEntryJSON(trashID))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	reader, err := NewHTTPReader(server.URL, server.Client(), time.Second)
	if err != nil {
		t.Fatalf("NewHTTPReader() error = %v", err)
	}
	page, err := reader.ListTrash(context.Background(), PageRequest{Limit: 10})
	if err != nil {
		t.Fatalf("ListTrash() error = %v", err)
	}
	if len(page.Items) != 1 || page.Items[0].ID != trashID || page.Items[0].Files[0].Identity != "file-video-1" || page.Items[0].Files[0].Role != "video" || page.Items[0].Files[0].Size != 2048 {
		t.Fatalf("ListTrash() lost manifest evidence: %#v", page)
	}
	if page.Items[0].OriginalPaths[0].RootID != "library" || page.Items[0].OriginalPaths[0].RelativePath != "Movies/<title>.mkv" {
		t.Fatalf("ListTrash() lost original target: %#v", page.Items[0].OriginalPaths)
	}
	item, err := reader.GetTrash(context.Background(), trashID)
	if err != nil {
		t.Fatalf("GetTrash() error = %v", err)
	}
	if item.State != "held" || len(item.ClientAssociations) != 1 || len(item.Holds) != 1 || item.ExpiresAt.IsZero() {
		t.Fatalf("GetTrash() lost lifecycle evidence: %#v", item)
	}

	recorder := httptest.NewRecorder()
	NewHandler(reader).ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/trash/"+trashID+"?action=purge&idempotencyKey=k1&operator=%3Coperator%3E", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("detail status = %d", recorder.Code)
	}
	body := recorder.Body.String()
	for _, want := range []string{"Original locations", "Selected manifest", "file-video-1", "Restore and purge are two-step", "unknown; no per-effect evidence", "&lt;operator&gt;", "method=\"get\""} {
		if !strings.Contains(body, want) {
			t.Errorf("detail body missing %q", want)
		}
	}
	if strings.Contains(body, "method=\"post\"") || recorder.Header().Get("Cache-Control") != "no-store, private" || recorder.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("detail violated read-only/private response contract")
	}
}

func TestTrashHandlerRejectsMutationMethodsWithoutReader(t *testing.T) {
	called := atomic.Int32{}
	reader := fakeTrashReader{list: func(context.Context, PageRequest) (TrashPage, error) {
		called.Add(1)
		return TrashPage{}, nil
	}}
	recorder := httptest.NewRecorder()
	NewHandler(reader).ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/trash", nil))
	if recorder.Code != http.StatusMethodNotAllowed || called.Load() != 0 || recorder.Header().Get("Allow") != "GET, HEAD" {
		t.Fatalf("POST was not rejected before reader call: status=%d calls=%d", recorder.Code, called.Load())
	}
}

func TestTrashReaderRejectsUnknownDuplicateAndIdentityMismatch(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{name: "unknown", body: `{"items":[],"page":{"nextCursor":null,"coverage":[],"observedAt":"2026-09-21T10:00:00Z"},"unexpected":true}`},
		{name: "duplicate", body: `{"items":[],"items":[],"page":{"nextCursor":null,"coverage":[],"observedAt":"2026-09-21T10:00:00Z"}}`},
		{name: "missing-required", body: `{"items":[{"id":"` + trashID + `","files":[],"originalPaths":[],"expiresAt":"2026-10-21T10:00:00Z"}],"page":{"nextCursor":null,"coverage":[],"observedAt":"2026-09-21T10:00:00Z"}}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprint(w, tc.body)
			}))
			defer server.Close()
			reader, err := NewHTTPReader(server.URL, server.Client(), time.Second)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := reader.ListTrash(context.Background(), PageRequest{}); !errors.Is(err, ErrProtocol) {
				t.Fatalf("ListTrash() error = %v, want protocol", err)
			}
		})
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, trashEntryJSON("123e4567-e89b-12d3-a456-426614174001"))
	}))
	defer server.Close()
	reader, err := NewHTTPReader(server.URL, server.Client(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reader.GetTrash(context.Background(), trashID); !errors.Is(err, ErrProtocol) {
		t.Fatalf("identity mismatch error = %v, want protocol", err)
	}
}

func TestTrashReaderRefusesRedirectAndHonorsCancellation(t *testing.T) {
	var followed atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { followed.Add(1) }))
	defer target.Close()
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Location", target.URL)
		w.WriteHeader(http.StatusTemporaryRedirect)
	}))
	defer origin.Close()
	reader, err := NewHTTPReader(origin.URL, origin.Client(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reader.ListTrash(context.Background(), PageRequest{}); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("redirect error = %v, want unavailable", err)
	}
	if followed.Load() != 0 {
		t.Fatal("redirect target was followed")
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := reader.ListTrash(ctx, PageRequest{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled error = %v, want context canceled", err)
	}
}

type fakeTrashReader struct {
	list func(context.Context, PageRequest) (TrashPage, error)
}

func (f fakeTrashReader) ListTrash(ctx context.Context, query PageRequest) (TrashPage, error) {
	if f.list != nil {
		return f.list(ctx, query)
	}
	return TrashPage{}, nil
}

func (f fakeTrashReader) GetTrash(context.Context, string) (TrashEntry, error) {
	return TrashEntry{}, nil
}

func trashListJSON(id string) string {
	return fmt.Sprintf(`{"items":[%s],"page":{"nextCursor":null,"coverage":[],"observedAt":"2026-09-21T10:00:00Z"}}`, trashEntryJSON(id))
}

func trashEntryJSON(id string) string {
	return fmt.Sprintf(`{"id":%q,"files":[{"rootId":"trash","relativePath":"Movies/<title>.mkv","type":"file","size":2048,"fileIdentity":"file-video-1","role":"video","digest":"sha256:fixture"}],"originalPaths":[{"rootId":"library","relativePath":"Movies/<title>.mkv"}],"expiresAt":"2026-10-21T10:00:00Z","state":"held","capabilities":["restore","purge"],"clientAssociations":["qbittorrent-main"],"holds":["client-stop-pending"]}`, id)
}
