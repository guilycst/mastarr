package client

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestReadyNormalizesHealthWithoutLeakingReason(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/health/ready" || r.Method != http.MethodGet {
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"degraded","observedAt":"2026-09-17T12:00:00Z","reason":"private-hostname.example/secret"}`))
	}))
	defer server.Close()

	api, err := New(server.URL, server.Client(), time.Second)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	got, err := api.Ready(context.Background())
	if err != nil {
		t.Fatalf("Ready() error = %v", err)
	}
	if got.State != StateDegraded || got.ObservedAt.IsZero() {
		t.Fatalf("Readiness = %#v", got)
	}
}

func TestReadyRejectsMalformedAndNonReadyResponses(t *testing.T) {
	cases := []struct {
		name       string
		status     int
		content    string
		wantStatus int
		wantKind   FailureKind
	}{
		{name: "malformed", status: http.StatusOK, content: `{not-json}`, wantKind: FailureProtocol},
		{name: "missing fields", status: http.StatusOK, content: `{}`, wantKind: FailureProtocol},
		{name: "not ready", status: http.StatusOK, content: `{"status":"not_ready","observedAt":"2026-09-17T12:00:00Z"}`, wantStatus: http.StatusServiceUnavailable, wantKind: FailureUnavailable},
		{name: "server error", status: http.StatusServiceUnavailable, content: `{"detail":"private body"}`, wantStatus: http.StatusServiceUnavailable, wantKind: FailureUnavailable},
		{name: "accepted", status: http.StatusAccepted, content: `{"status":"ok","observedAt":"2026-09-17T12:00:00Z"}`, wantStatus: http.StatusAccepted, wantKind: FailureStatus},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.content))
			}))
			defer server.Close()
			api, err := New(server.URL, server.Client(), time.Second)
			if err != nil {
				t.Fatalf("New() error = %v", err)
			}
			_, err = api.Ready(context.Background())
			if err == nil {
				t.Fatal("Ready() unexpectedly succeeded")
			}
			var apiErr *APIError
			if !errors.As(err, &apiErr) {
				t.Fatalf("error type = %T (%v)", err, err)
			}
			if apiErr.Kind() != tc.wantKind || apiErr.Status() != tc.wantStatus {
				t.Fatalf("APIError = kind %q status %d, want %q %d", apiErr.Kind(), apiErr.Status(), tc.wantKind, tc.wantStatus)
			}
			if strings.Contains(err.Error(), "private") || strings.Contains(err.Error(), "detail") {
				t.Fatalf("error leaked response detail: %v", err)
			}
		})
	}
}

func TestReadyPreservesCancellationAndDeadlineIdentity(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer server.Close()
	api, err := New(server.URL, server.Client(), 10*time.Millisecond)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	_, err = api.Ready(context.Background())
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("timeout error = %v; errors.Is deadline false", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = api.Ready(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel error = %v; errors.Is canceled false", err)
	}
}

func TestNewRejectsCredentialsAndDoesNotFollowRedirect(t *testing.T) {
	for _, raw := range []string{"http://user:secret@example.test", "http://example.test?token=secret", "http://example.test#secret", "http://example.test:99999"} {
		if _, err := New(raw, nil, time.Second); err == nil {
			t.Fatalf("New(%q) unexpectedly succeeded", raw)
		}
	}
	var redirected int
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		redirected++
		w.WriteHeader(http.StatusOK)
	}))
	defer target.Close()
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusFound)
	}))
	defer origin.Close()
	api, err := New(origin.URL, origin.Client(), time.Second)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	_, err = api.Ready(context.Background())
	if err == nil {
		t.Fatal("redirect unexpectedly became success")
	}
	if redirected != 0 {
		t.Fatalf("redirect target received %d requests", redirected)
	}
}

func TestReadyRejectsOversizedBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok","observedAt":"2026-09-17T12:00:00Z","reason":"` + strings.Repeat("x", MaxResponseBytes) + `"}`))
	}))
	defer server.Close()
	api, err := New(server.URL, server.Client(), time.Second)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	_, err = api.Ready(context.Background())
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Kind() != FailureResponseSize {
		t.Fatalf("oversized error = %T %v", err, err)
	}
}
