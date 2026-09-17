package shell

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestRenderUsesConfiguredPrivateSafeMetadataAndRuntimeShell(t *testing.T) {
	var output bytes.Buffer
	err := Render(context.Background(), &output, "/media/secret-record?root=/private/media", "https://ui.example.test/", View{
		Route:    "/media/secret-record",
		Title:    "Media",
		APIState: APIAvailable,
	})
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	body := output.String()
	for _, want := range []string{
		`<title>Media</title>`,
		`<link rel="canonical" href="https://ui.example.test/media">`,
		`<meta property="og:url" content="https://ui.example.test/media">`,
		`<meta name="twitter:card" content="summary_large_image">`,
		`<meta name="robots" content="noindex,nofollow">`,
		`https://ui.example.test/preview.svg`,
		`og:image:width" content="1200"`,
		`og:image:height" content="630"`,
		`/consoleshell/assets/`,
		`/assets/`,
		configurationNote,
		restartNote,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("rendered shell missing %q", want)
		}
	}
	for _, forbidden := range []string{"secret-record", "/private/media", "root=/private"} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("rendered shell leaked %q", forbidden)
		}
	}
}

func TestRenderShowsRetriableOutageWithoutRawError(t *testing.T) {
	var output bytes.Buffer
	err := Render(context.Background(), &output, "/workflows", "https://ui.example.test", View{
		Route:    "/workflows",
		Title:    "Workflows",
		APIState: APIUnavailable,
	})
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	body := output.String()
	for _, want := range []string{"temporarily unavailable", "Retry", "No successful state is inferred"} {
		if !strings.Contains(body, want) {
			t.Fatalf("outage shell missing %q", want)
		}
	}
}

func TestRenderAllowsLocalHTTPOriginWithConfiguredMetadata(t *testing.T) {
	var output bytes.Buffer
	if err := Render(context.Background(), &output, "/", "http://127.0.0.1:8081/", View{
		Title:    "Overview",
		APIState: APIAvailable,
	}); err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	body := output.String()
	for _, want := range []string{
		`<link rel="canonical" href="http://127.0.0.1:8081/">`,
		`<meta property="og:url" content="http://127.0.0.1:8081/">`,
		`<meta name="twitter:card" content="summary_large_image">`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("local HTTP shell missing %q", want)
		}
	}
}

func TestCanonicalPathDropsIdentifiersAndQueries(t *testing.T) {
	if got := CanonicalPath("/discoveries/abc?root=/private"); got != "/discoveries" {
		t.Fatalf("CanonicalPath() = %q", got)
	}
	if got := CanonicalPath("/not-a-route"); got != "/" {
		t.Fatalf("unknown CanonicalPath() = %q", got)
	}
	if got := ActiveID("/settings/connections"); got != "" {
		t.Fatalf("settings ActiveID() = %q", got)
	}
}

func TestRenderRejectsUntrustedOrigin(t *testing.T) {
	for _, origin := range []string{
		"ftp://ui.example.test",
		"https://user:secret@ui.example.test",
		"https://ui.example.test?token=secret",
		"https://ui.example.test/private",
		"https://ui.example.test:99999",
	} {
		var output bytes.Buffer
		if err := Render(context.Background(), &output, "/", origin, View{Title: "Overview"}); err == nil {
			t.Fatalf("Render(%q) unexpectedly succeeded", origin)
		}
	}
}
