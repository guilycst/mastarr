package assets

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHandlerServesGenericPreview(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, PreviewPath+"?private=/media/secret.mkv", nil)
	recorder := httptest.NewRecorder()
	Handler().ServeHTTP(recorder, req)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d", recorder.Code)
	}
	if got := recorder.Header().Get("Content-Type"); got != PreviewMIMEType {
		t.Fatalf("content type = %q", got)
	}
	if got := recorder.Header().Get("Cache-Control"); got == "" {
		t.Fatal("missing cache policy")
	}
	body, err := io.ReadAll(recorder.Result().Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	text := string(body)
	for _, want := range []string{`width="1200"`, `height="630"`, `role="img"`, "Mastarr operations dashboard"} {
		if !strings.Contains(text, want) {
			t.Fatalf("preview missing %q", want)
		}
	}
	if strings.Contains(text, "/media/secret") {
		t.Fatal("preview reflected request query")
	}
}

func TestHandlerIsNarrowAndSafe(t *testing.T) {
	for _, tc := range []struct {
		name string
		path string
		code int
	}{
		{name: "wrong path", path: "/preview.svg/other", code: http.StatusNotFound},
		{name: "post", path: PreviewPath, code: http.StatusMethodNotAllowed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, tc.path, nil))
			if recorder.Code != tc.code {
				t.Fatalf("status = %d, want %d", recorder.Code, tc.code)
			}
		})
	}
}
