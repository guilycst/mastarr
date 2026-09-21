package review

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestHTTPReaderNormalizesPlanWithoutLeakingGeneratedTypes(t *testing.T) {
	const planID = "00000000-0000-0000-0000-000000000001"
	const now = "2026-09-17T16:00:00Z"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/v1/action-plans/"+planID {
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"`+planID+`","revision":4,"digest":"sha256:plan","status":"ready","requiredApproval":"review","createdAt":"`+now+`","expiresAt":"2026-09-17T16:15:00Z","action":{"kind":"arr.registration","connectionId":"sonarr-a","mediaKind":"episode","providerId":"tvdb:42","fields":{"monitored":false,"seasonFolder":true,"seriesType":"standard"}},"manifest":[{"rootId":"root-downloads","relativePath":"Show/E01.mkv","type":"file","size":42,"fileIdentity":"inode-1","observedAt":"`+now+`","role":"video"}],"preconditions":["read-back"],"capabilities":["arr.registration"],"impacts":["future monitoring is opt-in"],"desiredState":{"providerId":"tvdb:42"}}`)
	}))
	defer server.Close()
	reader, err := NewHTTPReader(server.URL, server.Client(), time.Second)
	if err != nil {
		t.Fatalf("NewHTTPReader() error = %v", err)
	}
	item, err := reader.GetReview(context.Background(), planID)
	if err != nil {
		t.Fatalf("GetReview() error = %v", err)
	}
	if item.ID != planID || item.Action.Kind != "arr.registration" || item.Action.ConnectionID != "sonarr-a" || item.Action.ProviderID != "tvdb:42" || len(item.Manifest) != 1 || item.Manifest[0].Identity != "inode-1" {
		t.Fatalf("normalized review = %#v", item)
	}
	if item.Action.Monitoring == nil || *item.Action.Monitoring {
		t.Fatalf("monitoring pointer = %#v", item.Action.Monitoring)
	}
}

func generatedReviewPlan(action string) string {
	return `{"id":"00000000-0000-0000-0000-000000000001","revision":4,"digest":"sha256:generated-fixture","status":"ready","requiredApproval":"review","action":` + action + `,"manifest":[{"rootId":"root-downloads","relativePath":"Show/E01.mkv","type":"file","size":42,"fileIdentity":"inode-1","role":"video"}],"preconditions":["read-back"],"capabilities":["read-only"],"impacts":["unselected payload remains outside this exact plan"],"desiredState":{}}`
}

func TestHTTPReaderGeneratedActionFixturesReachReadOnlyHandler(t *testing.T) {
	const planID = "00000000-0000-0000-0000-000000000001"
	actions := map[string]string{
		"arr.registration":  `{"kind":"arr.registration","connectionId":"sonarr-a","mediaKind":"episode","providerId":"tvdb:42","fields":{"monitored":false,"seasonFolder":true,"seriesType":"standard"}}`,
		"arr.import":        `{"kind":"arr.import","connectionId":"sonarr-a","registeredExternalId":"series-42","files":[{"source":{"rootId":"root-downloads","relativePath":"Show/E01.mkv"},"movieOrEpisodeId":42,"subtitle":false}],"previewRevision":"preview-4","transfer":"copy"}`,
		"fs.copy":           `{"kind":"fs.copy","files":[{"source":{"rootId":"root-downloads","relativePath":"Show/E01.mkv"},"destination":{"rootId":"root-library","relativePath":"Show/E01.mkv"}}]}`,
		"fs.hardlink":       `{"kind":"fs.hardlink","files":[{"source":{"rootId":"root-downloads","relativePath":"Show/E01.mkv"},"destination":{"rootId":"root-library","relativePath":"Show/E01.mkv"}}]}`,
		"fs.move":           `{"kind":"fs.move","executor":"mastarr","files":[{"source":{"rootId":"root-downloads","relativePath":"Show/E01.mkv"},"destination":{"rootId":"root-library","relativePath":"Show/E01.mkv"}}]}`,
		"fs.rename":         `{"kind":"fs.rename","executor":"native_client","files":[{"source":{"rootId":"root-downloads","relativePath":"Show/E01.mkv"},"destination":{"rootId":"root-library","relativePath":"Show/E01.mkv"}}]}`,
		"client.stop":       `{"kind":"client.stop","connectionId":"qbittorrent-a","clientItemIds":["torrent-exact-1"]}`,
		"client.remove":     `{"kind":"client.remove","connectionId":"qbittorrent-a","clientItemIds":["torrent-exact-1"],"retainPayload":true}`,
		"fs.trash":          `{"kind":"fs.trash","files":[{"rootId":"root-downloads","relativePath":"Show/E01.mkv"}],"retentionDays":30,"stoppedClientIds":["torrent-exact-1"]}`,
		"fs.restore":        `{"kind":"fs.restore","trashId":"00000000-0000-0000-0000-000000000002","files":[{"source":{"rootId":"root-trash","relativePath":"Show/E01.mkv"},"destination":{"rootId":"root-library","relativePath":"Show/E01.mkv"}}]}`,
		"fs.delete":         `{"kind":"fs.delete","files":[{"rootId":"root-trash","relativePath":"Show/E01.mkv"}],"permanent":true,"irreversibleAcknowledgement":true}`,
		"descriptor.delete": `{"kind":"descriptor.delete","descriptorIds":["00000000-0000-0000-0000-000000000003"],"irreversibleAcknowledgement":true}`,
		"jellyfin.refresh":  `{"kind":"jellyfin.refresh","connectionId":"jellyfin-a","scope":"item","itemId":"item-42"}`,
	}
	for kind, action := range actions {
		t.Run(kind, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/api/v1/action-plans/"+planID {
					t.Fatalf("request path = %q", r.URL.Path)
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, generatedReviewPlan(action))
			}))
			defer server.Close()

			reader, err := NewHTTPReader(server.URL, server.Client(), time.Second)
			if err != nil {
				t.Fatal(err)
			}
			item, err := reader.GetReview(context.Background(), planID)
			if err != nil {
				t.Fatalf("GetReview() error = %v", err)
			}
			if item.Action.Kind != kind {
				t.Fatalf("action kind = %q, want %q", item.Action.Kind, kind)
			}
			if kind == "fs.trash" {
				if item.Action.RetentionDays == nil || *item.Action.RetentionDays != 30 || len(item.Action.StoppedClientIDs) != 1 || item.Action.StoppedClientIDs[0] != "torrent-exact-1" {
					t.Fatalf("trash authority = %#v", item.Action)
				}
			}
			if kind == "fs.delete" {
				if item.Action.Permanent == nil || !*item.Action.Permanent || !item.Action.Irreversible || len(item.Action.Files) != 1 || item.Action.Files[0].SourceRootID != "root-trash" {
					t.Fatalf("delete authority = %#v", item.Action)
				}
			}
			recorder := httptest.NewRecorder()
			NewHandler(reader).ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/reviews/"+planID, nil))
			if recorder.Code != http.StatusOK {
				t.Fatalf("handler status = %d body=%s", recorder.Code, recorder.Body.String())
			}
			if kind == "fs.trash" {
				for _, expected := range []string{"Retention days", "30", "Stopped-client prerequisites", "torrent-exact-1", "unselected payload remains"} {
					if !strings.Contains(recorder.Body.String(), expected) {
						t.Fatalf("trash body missing %q: %s", expected, recorder.Body.String())
					}
				}
			}
			if kind == "fs.delete" {
				for _, expected := range []string{"Permanent deletion", "true", "unselected payload remains"} {
					if !strings.Contains(recorder.Body.String(), expected) {
						t.Fatalf("delete body missing %q: %s", expected, recorder.Body.String())
					}
				}
			}
		})
	}
}

func TestHTTPReaderRejectsCrossShapeActionFiles(t *testing.T) {
	const planID = "00000000-0000-0000-0000-000000000001"
	for _, testCase := range []struct {
		name   string
		action string
	}{
		{
			name:   "trash import file wrapper",
			action: `{"kind":"fs.trash","files":[{"source":{"rootId":"root-downloads","relativePath":"Show/E01.mkv"},"movieOrEpisodeId":42}],"retentionDays":30}`,
		},
		{
			name:   "import direct target",
			action: `{"kind":"arr.import","connectionId":"sonarr-a","registeredExternalId":"series-42","files":[{"rootId":"root-downloads","relativePath":"Show/E01.mkv"}],"previewRevision":"preview-4","transfer":"copy"}`,
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, generatedReviewPlan(testCase.action))
			}))
			defer server.Close()
			reader, err := NewHTTPReader(server.URL, server.Client(), time.Second)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := reader.GetReview(context.Background(), planID); err == nil || !strings.Contains(err.Error(), "response invalid") {
				t.Fatalf("GetReview() error = %v, want strict shape rejection", err)
			}
		})
	}
}

func TestHTTPReaderRejectsUnknownResponseFieldsAndUnsafeURL(t *testing.T) {
	for _, raw := range []string{"https://user:secret@example.test", "https://example.test?token=secret", "ftp://example.test"} {
		if _, err := NewHTTPReader(raw, nil, time.Second); err == nil {
			t.Fatalf("NewHTTPReader(%q) unexpectedly succeeded", raw)
		}
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"00000000-0000-0000-0000-000000000001","revision":1,"digest":"digest","status":"ready","requiredApproval":"review","action":{"kind":"arr.registration","connectionId":"c","mediaKind":"episode","providerId":"p","fields":{"monitored":false}},"manifest":[],"preconditions":[],"capabilities":[],"desiredState":{},"unexpected":true}`)
	}))
	defer server.Close()
	reader, err := NewHTTPReader(server.URL, server.Client(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	_, err = reader.GetReview(context.Background(), "00000000-0000-0000-0000-000000000001")
	if !strings.Contains(err.Error(), "response invalid") {
		t.Fatalf("error = %v", err)
	}
}

func TestHTTPReaderRejectsDuplicateAndUnknownNestedActionFields(t *testing.T) {
	const planID = "00000000-0000-0000-0000-000000000001"
	for _, testCase := range []struct {
		name string
		body string
	}{
		{
			name: "duplicate nested field",
			body: `{"id":"` + planID + `","revision":1,"digest":"digest","status":"ready","requiredApproval":"review","action":{"kind":"arr.registration","connectionId":"c","mediaKind":"episode","providerId":"p","fields":{"monitored":false,"monitored":true}},"manifest":[],"preconditions":[],"capabilities":[],"desiredState":{}}`,
		},
		{
			name: "unknown nested field",
			body: `{"id":"` + planID + `","revision":1,"digest":"digest","status":"ready","requiredApproval":"review","action":{"kind":"arr.registration","connectionId":"c","mediaKind":"episode","providerId":"p","fields":{"unexpected":true}},"manifest":[],"preconditions":[],"capabilities":[],"desiredState":{}}`,
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, testCase.body)
			}))
			defer server.Close()
			reader, err := NewHTTPReader(server.URL, server.Client(), time.Second)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := reader.GetReview(context.Background(), planID); err == nil || !strings.Contains(err.Error(), "response invalid") {
				t.Fatalf("GetReview() error = %v, want sanitized protocol error", err)
			}
		})
	}
}
