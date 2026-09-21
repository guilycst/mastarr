package settings

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

const checkID = "123e4567-e89b-12d3-a456-426614174000"

func TestHTTPReaderAndHandlersPreserveSettingsProvenanceAndRedactCredentials(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/api/v1/configuration" {
			w.Header().Set("ETag", `"cfg-r1"`)
			fmt.Fprint(w, configurationJSON())
			return
		}
		switch r.URL.Path {
		case "/api/v1/connections":
			fmt.Fprint(w, connectionListJSON())
		case "/api/v1/connections/qbittorrent-main":
			w.Header().Set("ETag", `"conn-r1"`)
			fmt.Fprint(w, connectionJSON())
		case "/api/v1/storage-roots":
			fmt.Fprint(w, storageRootListJSON())
		case "/api/v1/storage-roots/library":
			w.Header().Set("ETag", `"root-r1"`)
			fmt.Fprint(w, storageRootJSON())
		case "/api/v1/path-mappings":
			fmt.Fprint(w, pathMappingListJSON())
		case "/api/v1/path-mappings/mapping-main":
			w.Header().Set("ETag", `"map-r1"`)
			fmt.Fprint(w, pathMappingJSON())
		case "/api/v1/connection-checks":
			fmt.Fprint(w, checkListJSON())
		case "/api/v1/connection-checks/" + checkID:
			fmt.Fprint(w, checkJSON())
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	reader, err := NewHTTPReader(server.URL, server.Client(), time.Second)
	if err != nil {
		t.Fatalf("NewHTTPReader() error = %v", err)
	}
	configuration, err := reader.GetConfiguration(context.Background())
	if err != nil {
		t.Fatalf("GetConfiguration() error = %v", err)
	}
	if configuration.Source.Source != "yaml" || configuration.Source.Editable || configuration.ETag != `"cfg-r1"` || len(configuration.Connections) != 1 || len(configuration.StorageRoots) != 1 || len(configuration.PathMappings) != 1 {
		t.Fatalf("configuration projection lost provenance: %#v", configuration)
	}
	if configuration.Connections[0].Endpoint != "https://example.invalid/api" || strings.Contains(configuration.Connections[0].Endpoint, "secret") {
		t.Fatalf("endpoint credential was not redacted: %q", configuration.Connections[0].Endpoint)
	}
	connection, err := reader.GetConnection(context.Background(), "qbittorrent-main")
	if err != nil || connection.ETag != `"conn-r1"` || connection.CredentialState != "managed" {
		t.Fatalf("connection projection = %#v, err=%v", connection, err)
	}
	root, err := reader.GetStorageRoot(context.Background(), "library")
	if err != nil || root.WatchIntervalSeconds != 30 || root.Permission != "read_write" {
		t.Fatalf("storage root projection = %#v, err=%v", root, err)
	}
	mapping, err := reader.GetPathMapping(context.Background(), "mapping-main")
	if err != nil || mapping.ConnectionID != "qbittorrent-main" || mapping.RootID != "library" {
		t.Fatalf("mapping projection = %#v, err=%v", mapping, err)
	}
	check, err := reader.GetConnectionCheck(context.Background(), checkID)
	if err != nil || !check.ErrorPresent || check.ConnectionID != "qbittorrent-main" {
		t.Fatalf("check projection = %#v, err=%v", check, err)
	}

	recorder := httptest.NewRecorder()
	NewHandler(reader).ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/settings?ifMatch=%22old%22&idempotencyKey=draft&operator=%3Coperator%3E", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("settings status = %d", recorder.Code)
	}
	body := recorder.Body.String()
	for _, forbidden := range []string{"user:secret", "token=secret", "writeOnly", "keyPath"} {
		if strings.Contains(body, forbidden) {
			t.Errorf("settings body leaked %q", forbidden)
		}
	}
	for _, want := range []string{"Source provenance", "yaml", "Editable", "false", "Configuration ETag", "&lt;operator&gt;", "API-owned draft context", "method=\"get\""} {
		if !strings.Contains(body, want) {
			t.Errorf("settings body missing %q", want)
		}
	}
	if recorder.Header().Get("Cache-Control") != "no-store, private" || recorder.Header().Get("X-Robots-Tag") != "noindex, nofollow" {
		t.Fatal("settings response missing private headers")
	}

	connectionRecorder := httptest.NewRecorder()
	NewHandler(reader).ServeHTTP(connectionRecorder, httptest.NewRequest(http.MethodGet, "/connections/qbittorrent-main?label=draft&ifMatch=%22stale%22", nil))
	connectionBody := connectionRecorder.Body.String()
	if strings.Contains(connectionBody, "user:secret") || strings.Contains(connectionBody, "token=secret") {
		t.Fatalf("connection detail leaked credential: %s", connectionBody)
	}
	if !strings.Contains(connectionBody, "stale") || !strings.Contains(connectionBody, "GET draft") {
		t.Fatalf("connection detail violated redaction/draft contract: %s", connectionBody)
	}
}

func TestSettingsHandlerRejectsMutationMethodsBeforeReader(t *testing.T) {
	called := atomic.Int32{}
	fake := fakeSettingsReader{configuration: func(context.Context) (Configuration, error) {
		called.Add(1)
		return Configuration{}, nil
	}}
	recorder := httptest.NewRecorder()
	NewHandler(fake).ServeHTTP(recorder, httptest.NewRequest(http.MethodPatch, "/settings", nil))
	if recorder.Code != http.StatusMethodNotAllowed || called.Load() != 0 || recorder.Header().Get("Allow") != "GET, HEAD" {
		t.Fatalf("mutation was not rejected before reader call: status=%d calls=%d", recorder.Code, called.Load())
	}
}

func TestSettingsReaderRejectsDuplicateUnknownAndMissingProvenance(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{name: "unknown", body: `{"items":[],"page":{"nextCursor":null,"coverage":[],"observedAt":"2026-09-21T10:00:00Z"},"unexpected":true}`},
		{name: "duplicate", body: `{"items":[],"items":[],"page":{"nextCursor":null,"coverage":[],"observedAt":"2026-09-21T10:00:00Z"}}`},
		{name: "missing-source", body: `{"items":[{"id":"qbittorrent-main","kind":"qbittorrent","label":"main","endpoint":"https://example.invalid","revision":"r1","health":"healthy","credentialState":"managed"}],"page":{"nextCursor":null,"coverage":[],"observedAt":"2026-09-21T10:00:00Z"}}`},
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
			if _, err := reader.ListConnections(context.Background(), PageRequest{}); !errors.Is(err, ErrProtocol) {
				t.Fatalf("ListConnections() error = %v, want protocol", err)
			}
		})
	}
}

func TestSettingsReaderRefusesRedirect(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Fatal("redirect target followed") }))
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
	if _, err := reader.GetConfiguration(context.Background()); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("redirect error = %v, want unavailable", err)
	}
}

type fakeSettingsReader struct {
	configuration func(context.Context) (Configuration, error)
}

func (f fakeSettingsReader) GetConfiguration(ctx context.Context) (Configuration, error) {
	if f.configuration != nil {
		return f.configuration(ctx)
	}
	return Configuration{}, nil
}
func (fakeSettingsReader) ListConnections(context.Context, PageRequest) (ConnectionPage, error) {
	return ConnectionPage{}, nil
}
func (fakeSettingsReader) GetConnection(context.Context, string) (Connection, error) {
	return Connection{}, nil
}
func (fakeSettingsReader) ListStorageRoots(context.Context, PageRequest) (StorageRootPage, error) {
	return StorageRootPage{}, nil
}
func (fakeSettingsReader) GetStorageRoot(context.Context, string) (StorageRoot, error) {
	return StorageRoot{}, nil
}
func (fakeSettingsReader) ListPathMappings(context.Context, PageRequest) (PathMappingPage, error) {
	return PathMappingPage{}, nil
}
func (fakeSettingsReader) GetPathMapping(context.Context, string) (PathMapping, error) {
	return PathMapping{}, nil
}
func (fakeSettingsReader) ListConnectionChecks(context.Context, PageRequest) (ConnectionCheckPage, error) {
	return ConnectionCheckPage{}, nil
}
func (fakeSettingsReader) GetConnectionCheck(context.Context, string) (ConnectionCheck, error) {
	return ConnectionCheck{}, nil
}

func configurationJSON() string {
	return `{"source":{"source":"yaml","editable":false,"documentId":"config.yaml","revision":"yaml-r1","startupAt":"2026-09-21T09:00:00Z","reloadPolicy":"restart_required"},"keySource":"secret_file","restartRequired":true,"connections":[{"id":"qbittorrent-main","kind":"qbittorrent","label":"qBittorrent <main>","endpoint":"https://user:secret@example.invalid/api?token=secret","health":"healthy","credentialState":"managed","observedVersion":"5.1.0","capabilities":["stop","read"],"revision":"conn-r1","source":{"source":"api","editable":true,"documentId":"connections","revision":"conn-r1","startupAt":"2026-09-21T09:00:00Z","reloadPolicy":"restart_required"}}],"storageRoots":[{"id":"library","label":"Library","purpose":"library","path":"/srv/library","permission":"read_write","capabilities":["watch","read"],"revision":"root-r1","watch":{"enabled":true,"intervalSeconds":30},"source":{"source":"yaml","editable":false,"documentId":"config.yaml","revision":"yaml-r1","startupAt":"2026-09-21T09:00:00Z","reloadPolicy":"restart_required"}}],"pathMappings":[{"id":"mapping-main","connectionId":"qbittorrent-main","sourcePrefix":"/downloads","rootId":"library","destinationPrefix":"/srv/library","revision":"map-r1","source":{"source":"api","editable":true,"documentId":"path-mappings","revision":"map-r1","startupAt":"2026-09-21T09:00:00Z","reloadPolicy":"restart_required"}}]}`
}

func connectionJSON() string {
	return `{"id":"qbittorrent-main","kind":"qbittorrent","label":"qBittorrent <main>","endpoint":"https://user:secret@example.invalid/api?token=secret","health":"healthy","credentialState":"managed","observedVersion":"5.1.0","capabilities":["stop","read"],"revision":"conn-r1","source":{"source":"api","editable":true,"documentId":"connections","revision":"conn-r1","startupAt":"2026-09-21T09:00:00Z","reloadPolicy":"restart_required"}}`
}

func connectionListJSON() string {
	return fmt.Sprintf(`{"items":[%s],"page":{"nextCursor":null,"coverage":[],"observedAt":"2026-09-21T10:00:00Z"}}`, connectionJSON())
}

func storageRootJSON() string {
	return `{"id":"library","label":"Library","purpose":"library","path":"/srv/library","permission":"read_write","capabilities":["watch","read"],"revision":"root-r1","watch":{"enabled":true,"intervalSeconds":30},"source":{"source":"yaml","editable":false,"documentId":"config.yaml","revision":"yaml-r1","startupAt":"2026-09-21T09:00:00Z","reloadPolicy":"restart_required"}}`
}

func storageRootListJSON() string {
	return fmt.Sprintf(`{"items":[%s],"page":{"nextCursor":null,"coverage":[],"observedAt":"2026-09-21T10:00:00Z"}}`, storageRootJSON())
}

func pathMappingJSON() string {
	return `{"id":"mapping-main","connectionId":"qbittorrent-main","sourcePrefix":"/downloads","rootId":"library","destinationPrefix":"/srv/library","revision":"map-r1","source":{"source":"api","editable":true,"documentId":"path-mappings","revision":"map-r1","startupAt":"2026-09-21T09:00:00Z","reloadPolicy":"restart_required"}}`
}

func pathMappingListJSON() string {
	return fmt.Sprintf(`{"items":[%s],"page":{"nextCursor":null,"coverage":[],"observedAt":"2026-09-21T10:00:00Z"}}`, pathMappingJSON())
}

func checkJSON() string {
	return fmt.Sprintf(`{"id":%q,"connectionId":"qbittorrent-main","state":"failed","createdAt":"2026-09-21T10:00:00Z","completedAt":"2026-09-21T10:00:01Z","capabilities":["read"],"sanitizedError":"upstream secret must never render"}`, checkID)
}

func checkListJSON() string {
	return fmt.Sprintf(`{"items":[%s],"page":{"nextCursor":null,"coverage":[],"observedAt":"2026-09-21T10:00:00Z"}}`, checkJSON())
}
