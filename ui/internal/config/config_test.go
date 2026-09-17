package config

import (
	"strings"
	"testing"
)

func TestParseRequiresAndNormalizesStartupValues(t *testing.T) {
	t.Parallel()

	got, err := Parse(map[string]string{
		apiURLEnv:       "http://api.example.test/",
		publicOriginEnv: "https://ui.example.test/",
	})
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if got.APIURL != "http://api.example.test" {
		t.Fatalf("APIURL = %q", got.APIURL)
	}
	if got.PublicOrigin != "https://ui.example.test" {
		t.Fatalf("PublicOrigin = %q", got.PublicOrigin)
	}
	if got.ListenAddr != defaultListen {
		t.Fatalf("ListenAddr = %q", got.ListenAddr)
	}
	if got.Source != "startup environment" || !strings.Contains(got.RestartGuidance, "restart") {
		t.Fatalf("startup guidance = %#v", got)
	}
}

func TestLoadReadsEnvironmentOnce(t *testing.T) {
	t.Setenv(apiURLEnv, "http://first.example.test")
	t.Setenv(publicOriginEnv, "https://ui.example.test")
	first, err := Load()
	if err != nil {
		t.Fatalf("first Load() error = %v", err)
	}
	// Load is the one startup read. A handler receives first, rather than
	// calling Load again after process environment changes.
	t.Setenv(apiURLEnv, "http://second.example.test")
	if first.APIURL != "http://first.example.test" {
		t.Fatalf("first snapshot changed: %#v", first)
	}
}

func TestParseRejectsMissingAndSecretBearingValues(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		values map[string]string
	}{
		{name: "missing api", values: map[string]string{publicOriginEnv: "https://ui.example.test"}},
		{name: "missing origin", values: map[string]string{apiURLEnv: "http://api.example.test"}},
		{name: "api userinfo", values: map[string]string{apiURLEnv: "http://user:password@api.example.test", publicOriginEnv: "https://ui.example.test"}},
		{name: "api query", values: map[string]string{apiURLEnv: "http://api.example.test?api_key=secret", publicOriginEnv: "https://ui.example.test"}},
		{name: "origin userinfo", values: map[string]string{apiURLEnv: "http://api.example.test", publicOriginEnv: "https://user:secret@ui.example.test"}},
		{name: "origin query", values: map[string]string{apiURLEnv: "http://api.example.test", publicOriginEnv: "https://ui.example.test?token=secret"}},
		{name: "origin http", values: map[string]string{apiURLEnv: "http://api.example.test", publicOriginEnv: "http://ui.example.test"}},
		{name: "bad listen", values: map[string]string{apiURLEnv: "http://api.example.test", publicOriginEnv: "https://ui.example.test", listenAddrEnv: "not-an-address"}},
		{name: "bad port", values: map[string]string{apiURLEnv: "http://api.example.test", publicOriginEnv: "https://ui.example.test", listenAddrEnv: ":99999"}},
		{name: "bad api port", values: map[string]string{apiURLEnv: "http://api.example.test:99999", publicOriginEnv: "https://ui.example.test"}},
		{name: "bad origin port", values: map[string]string{apiURLEnv: "http://api.example.test", publicOriginEnv: "https://ui.example.test:99999"}},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if _, err := Parse(tc.values); err == nil {
				t.Fatal("Parse() unexpectedly succeeded")
			} else if strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), "password") || strings.Contains(err.Error(), "api_key") {
				t.Fatalf("Parse() leaked input detail: %v", err)
			}
		})
	}
}
