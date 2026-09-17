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
