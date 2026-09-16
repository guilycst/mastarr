package write

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/guilycst/mastarr/internal/domain"
	"github.com/guilycst/mastarr/internal/ports"
)

const (
	testConnection = domain.ConfigID("arr-main")
	testPreview    = "synthetic-preview-revision"
)

func syntheticCapabilities() capabilities {
	return capabilities{
		registration: OperationCapability{State: domain.CapabilitySupported, Version: "synthetic-arr-v3", Evidence: []string{"synthetic-registration-fixture"}},
		importOp:     OperationCapability{State: domain.CapabilitySupported, Version: "synthetic-arr-v3", Evidence: []string{"synthetic-import-fixture"}},
	}
}

func syntheticClient(t *testing.T, server *httptest.Server, kind domain.ConnectionKind, caps capabilities) *Client {
	return syntheticClientWithResolver(t, server, kind, caps, func(_ context.Context, _ domain.ConfigID, request ports.ImportRequest) (ports.ImportPreview, error) {
		return ports.ImportPreview{Revision: request.PreviewRevision, Files: append([]ports.ImportFile(nil), request.Files...), ObservedAt: time.Now().UTC()}, nil
	})
}

func syntheticClientWithResolver(t *testing.T, server *httptest.Server, kind domain.ConnectionKind, caps capabilities, resolver PreviewResolver) *Client {
	t.Helper()
	client, err := newClient(Config{
		ConnectionID:    testConnection,
		Kind:            kind,
		Endpoint:        server.URL,
		APIKey:          "synthetic-arr-key",
		HTTPClient:      server.Client(),
		PreviewResolver: resolver,
		RootPaths:       map[domain.ConfigID]string{"downloads": "/synthetic/downloads", "library": "/synthetic/library"},
		MaxRecords:      32,
		MaxFiles:        32,
	}, caps)
	if err != nil {
		t.Fatalf("newClient: %v", err)
	}
	return client
}

func writeFixtureJSON(t testing.TB, writer http.ResponseWriter, value any) {
	t.Helper()
	writer.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(writer).Encode(value); err != nil {
		t.Errorf("encode fixture: %v", err)
	}
}

func TestNewArrWriteCapabilitiesStayBlockedAndNeverDispatch(t *testing.T) {
	var getCalls, writeCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method == http.MethodGet {
			getCalls.Add(1)
			switch request.URL.Path {
			case "/api/v3/movie":
				writeFixtureJSON(t, writer, []nativeTitle{})
			case "/api/v3/movie/101":
				writeFixtureJSON(t, writer, nativeTitle{ID: 101})
			case "/api/v3/history":
				writeFixtureJSON(t, writer, []nativeHistory{})
			default:
				http.Error(writer, "unexpected synthetic read", http.StatusNotFound)
			}
			return
		}
		writeCalls.Add(1)
		t.Fatalf("blocked adapter dispatched %s %s", request.Method, request.URL.Path)
	}))
	t.Cleanup(server.Close)

	client, err := New(Config{ConnectionID: testConnection, Kind: domain.ConnectionRadarr, Endpoint: server.URL, APIKey: "synthetic-key"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	caps, err := client.Capabilities(context.Background(), testConnection)
	if err != nil {
		t.Fatalf("Capabilities: %v", err)
	}
	if len(caps) != 2 || caps[0].State != domain.CapabilityUnknown || caps[1].State != domain.CapabilityUnknown {
		t.Fatalf("capabilities = %#v, want two unknown native write gates", caps)
	}
	registration := ports.RegistrationRequest{ProviderID: "4242", Kind: domain.MediaMovie, Fields: ports.RegistrationFields{RootFolder: "/synthetic/library", QualityProfileID: "7"}}
	if _, err := client.Register(context.Background(), testConnection, registration); !hasCode(err, domain.OutcomeUnsupported) {
		t.Fatalf("Register error = %v, want blocked unsupported result", err)
	}
	importRequest := ports.ImportRequest{RegisteredExternalID: "101", PreviewRevision: testPreview, Transfer: "copy", Files: []ports.ImportFile{{Source: domain.FileTarget{RootID: "downloads", RelativePath: "movie.mkv"}, MovieOrEpisodeID: "101"}}}
	if _, err := client.Import(context.Background(), testConnection, importRequest); !hasCode(err, domain.OutcomeUnsupported) {
		t.Fatalf("Import error = %v, want blocked unsupported result", err)
	}
	if writeCalls.Load() != 0 {
		t.Fatalf("native write calls = %d, want zero", writeCalls.Load())
	}
	if getCalls.Load() == 0 {
		t.Fatal("expected read-before-write observation")
	}
}

func TestRadarrRegistrationDefaultsNoSearchAndPreservesExistingFields(t *testing.T) {
	var mu sync.Mutex
	title := nativeTitle{ID: 101, Title: "Synthetic Film", TMDBID: int64Ptr(4242), RootFolderPath: "/synthetic/old", QualityProfile: int64Ptr(3), Monitored: boolPtr(false)}
	var putCalls atomic.Int32
	var payload map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("X-Api-Key") != "synthetic-arr-key" {
			http.Error(writer, "missing api key", http.StatusUnauthorized)
			return
		}
		switch {
		case request.Method == http.MethodGet && request.URL.Path == "/api/v3/movie":
			mu.Lock()
			current := title
			mu.Unlock()
			writeFixtureJSON(t, writer, []nativeTitle{current})
		case request.Method == http.MethodPut && request.URL.Path == "/api/v3/movie/101":
			putCalls.Add(1)
			if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
				t.Errorf("decode PUT: %v", err)
			}
			mu.Lock()
			title.RootFolderPath = stringValue(payload["rootFolderPath"])
			if value, ok := payload["qualityProfileId"].(float64); ok {
				title.QualityProfile = int64Ptr(int64(value))
			}
			if value, ok := payload["monitored"].(bool); ok {
				title.Monitored = boolPtr(value)
			}
			mu.Unlock()
			writeFixtureJSON(t, writer, title)
		case request.Method == http.MethodGet && request.URL.Path == "/api/v3/movie/101":
			mu.Lock()
			current := title
			mu.Unlock()
			writeFixtureJSON(t, writer, current)
		default:
			http.Error(writer, "unexpected synthetic route", http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)

	monitored := true
	client := syntheticClient(t, server, domain.ConnectionRadarr, syntheticCapabilities())
	result, err := client.Register(context.Background(), testConnection, ports.RegistrationRequest{
		ProviderID: "4242", Kind: domain.MediaMovie,
		Fields: ports.RegistrationFields{RootFolder: "/synthetic/library", QualityProfileID: "7", Monitored: &monitored},
	})
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	if result.ExternalID != "101" || result.Effect.Outcome != domain.OutcomeApplied || result.Record.ProviderID != "4242" {
		t.Fatalf("registration result = %#v, want applied read-back", result)
	}
	if putCalls.Load() != 1 {
		t.Fatalf("PUT calls = %d, want one field patch", putCalls.Load())
	}
	if stringValue(payload["rootFolderPath"]) != "/synthetic/library" || payload["qualityProfileId"] != float64(7) || payload["monitored"] != true {
		t.Fatalf("PUT payload = %#v, want explicit fields", payload)
	}
	if _, exists := payload["addOptions"]; exists {
		t.Fatalf("existing upsert payload unexpectedly contains addOptions: %#v", payload)
	}
	if payload["tmdbId"] != float64(4242) {
		t.Fatalf("existing provider identity was not preserved: %#v", payload)
	}
}

func TestRadarrRegistrationNewDefaultsUnmonitoredAndNoSearch(t *testing.T) {
	var mu sync.Mutex
	var title nativeTitle
	var postCalls atomic.Int32
	var payload map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch {
		case request.Method == http.MethodGet && request.URL.Path == "/api/v3/movie":
			mu.Lock()
			present := title.ID > 0
			current := title
			mu.Unlock()
			if present {
				writeFixtureJSON(t, writer, []nativeTitle{current})
			} else {
				writeFixtureJSON(t, writer, []nativeTitle{})
			}
		case request.Method == http.MethodPost && request.URL.Path == "/api/v3/movie":
			postCalls.Add(1)
			if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
				t.Errorf("decode POST: %v", err)
			}
			mu.Lock()
			title = nativeTitle{ID: 102, Title: "Synthetic Film", TMDBID: int64Ptr(4242), RootFolderPath: stringValue(payload["rootFolderPath"]), QualityProfile: int64Ptr(int64(payload["qualityProfileId"].(float64))), Monitored: boolPtr(payload["monitored"].(bool))}
			mu.Unlock()
			writeFixtureJSON(t, writer, title)
		case request.Method == http.MethodGet && request.URL.Path == "/api/v3/movie/102":
			mu.Lock()
			current := title
			mu.Unlock()
			writeFixtureJSON(t, writer, current)
		default:
			http.Error(writer, "unexpected synthetic route", http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)

	client := syntheticClient(t, server, domain.ConnectionRadarr, syntheticCapabilities())
	result, err := client.Register(context.Background(), testConnection, ports.RegistrationRequest{
		ProviderID: "4242", Kind: domain.MediaMovie,
		Fields: ports.RegistrationFields{RootFolder: "/synthetic/library", QualityProfileID: "7"},
	})
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	if result.ExternalID != "102" || result.Effect.Outcome != domain.OutcomeApplied || result.Record.Monitored {
		t.Fatalf("new result = %#v, want unmonitored applied registration", result)
	}
	if postCalls.Load() != 1 {
		t.Fatalf("POST calls = %d, want one", postCalls.Load())
	}
	if payload["monitored"] != false {
		t.Fatalf("new payload monitored = %#v, want false", payload["monitored"])
	}
	options, ok := payload["addOptions"].(map[string]any)
	if !ok || options["monitor"] != "none" || options["searchForMovie"] != false || options["searchForMissingEpisodes"] != false || options["searchForCutoffUnmet"] != false {
		t.Fatalf("new addOptions = %#v, want explicit no-search options", payload["addOptions"])
	}
}

func TestExistingRegistrationAlreadySatisfiedDoesNotWrite(t *testing.T) {
	title := nativeTitle{ID: 101, Title: "Synthetic Film", TMDBID: int64Ptr(4242), RootFolderPath: "/synthetic/library", QualityProfile: int64Ptr(7), Monitored: boolPtr(false)}
	var writes atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet || request.URL.Path != "/api/v3/movie" {
			writes.Add(1)
			http.Error(writer, "unexpected write", http.StatusTeapot)
			return
		}
		writeFixtureJSON(t, writer, []nativeTitle{title})
	}))
	t.Cleanup(server.Close)

	client := syntheticClient(t, server, domain.ConnectionRadarr, syntheticCapabilities())
	result, err := client.Register(context.Background(), testConnection, ports.RegistrationRequest{ProviderID: "4242", Kind: domain.MediaMovie, Fields: ports.RegistrationFields{RootFolder: "/synthetic/library", QualityProfileID: "7"}})
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	if result.Effect.Outcome != domain.OutcomeAlreadySatisfied || writes.Load() != 0 {
		t.Fatalf("result = %#v writes=%d, want already satisfied with no write", result, writes.Load())
	}
}

func TestSonarrImportReconcilesLostCommandPerEpisode(t *testing.T) {
	var mu sync.Mutex
	imported := false
	var commandCalls atomic.Int32
	var command manualImportCommand
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch {
		case request.Method == http.MethodGet && request.URL.Path == "/api/v3/episode":
			if request.URL.Query().Get("seriesId") != "201" || request.URL.Query().Get("includeEpisodeFile") != "true" {
				http.Error(writer, "bad episode scope", http.StatusBadRequest)
				return
			}
			mu.Lock()
			done := imported
			mu.Unlock()
			if !done {
				writeFixtureJSON(t, writer, []nativeEpisode{{ID: 301, SeriesID: 201}, {ID: 302, SeriesID: 201}})
				return
			}
			writeFixtureJSON(t, writer, []nativeEpisode{
				{ID: 301, SeriesID: 201, EpisodeFileID: 801, EpisodeFile: &nativeFile{ID: 801, SeriesID: 201, Path: "/synthetic/downloads/Synthetic Show/S01E01.mkv", Size: 10}},
				{ID: 302, SeriesID: 201, EpisodeFileID: 802, EpisodeFile: &nativeFile{ID: 802, SeriesID: 201, Path: "/synthetic/downloads/Synthetic Show/S01E02.mkv", Size: 11}},
			})
		case request.Method == http.MethodGet && request.URL.Path == "/api/v3/history":
			mu.Lock()
			done := imported
			mu.Unlock()
			if done {
				writeFixtureJSON(t, writer, []nativeHistory{{ID: 9001, EventType: "downloadFolderImported", SeriesID: 201, EpisodeID: 301, SourceTitle: "S01E01.mkv"}})
			} else {
				writeFixtureJSON(t, writer, []nativeHistory{})
			}
		case request.Method == http.MethodPost && request.URL.Path == "/api/v3/command":
			commandCalls.Add(1)
			if err := json.NewDecoder(request.Body).Decode(&command); err != nil {
				t.Errorf("decode command: %v", err)
			}
			mu.Lock()
			imported = true
			mu.Unlock()
			if hijacker, ok := writer.(http.Hijacker); ok {
				connection, _, hijackErr := hijacker.Hijack()
				if hijackErr == nil {
					_ = connection.Close()
					return
				}
			}
			_, _ = io.WriteString(writer, "")
		default:
			http.Error(writer, "unexpected synthetic route", http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)

	client := syntheticClient(t, server, domain.ConnectionSonarr, syntheticCapabilities())
	result, err := client.Import(context.Background(), testConnection, ports.ImportRequest{
		RegisteredExternalID: "201", PreviewRevision: testPreview, Transfer: "copy",
		Files: []ports.ImportFile{
			{Source: domain.FileTarget{RootID: "downloads", RelativePath: "Synthetic Show/S01E01.mkv"}, MovieOrEpisodeID: "301"},
			{Source: domain.FileTarget{RootID: "downloads", RelativePath: "Synthetic Show/S01E02.mkv"}, MovieOrEpisodeID: "302"},
		},
	})
	if err != nil {
		t.Fatalf("Import lost response: %v", err)
	}
	if result.Effect == nil || result.Effect.Outcome != domain.OutcomeApplied || len(result.Files) != 2 {
		t.Fatalf("result = %#v, want applied two-file read-back", result)
	}
	if !containsString(result.Effect.Evidence, "command_response_lost") || !containsString(result.Effect.Evidence, "history_read_back") {
		t.Fatalf("effect evidence = %#v, want lost command and history evidence", result.Effect.Evidence)
	}
	if commandCalls.Load() != 1 || command.Name != "ManualImport" || command.ImportMode != "copy" || len(command.Files) != 2 {
		t.Fatalf("command calls=%d payload=%#v, want one exact copy command", commandCalls.Load(), command)
	}
	if command.Files[0].SeriesID == nil || *command.Files[0].SeriesID != 201 || len(command.Files[0].EpisodeIDs) != 1 || command.Files[0].EpisodeIDs[0] != 301 {
		t.Fatalf("first command file = %#v, want series and episode identity", command.Files[0])
	}
}

func TestSonarrImportPartialReadbackRemainsUnknownPerFile(t *testing.T) {
	var imported atomic.Bool
	var commandCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch {
		case request.Method == http.MethodGet && request.URL.Path == "/api/v3/episode":
			if imported.Load() {
				writeFixtureJSON(t, writer, []nativeEpisode{{ID: 301, SeriesID: 201, EpisodeFileID: 801, EpisodeFile: &nativeFile{ID: 801, SeriesID: 201, Path: "/synthetic/downloads/Synthetic Show/S01E01.mkv", Size: 10}}, {ID: 302, SeriesID: 201}})
			} else {
				writeFixtureJSON(t, writer, []nativeEpisode{{ID: 301, SeriesID: 201}, {ID: 302, SeriesID: 201}})
			}
		case request.Method == http.MethodGet && request.URL.Path == "/api/v3/history":
			if imported.Load() {
				writeFixtureJSON(t, writer, []nativeHistory{{ID: 9001, EventType: "downloadFolderImported", SeriesID: 201, EpisodeID: 301}})
			} else {
				writeFixtureJSON(t, writer, []nativeHistory{})
			}
		case request.Method == http.MethodPost && request.URL.Path == "/api/v3/command":
			commandCalls.Add(1)
			imported.Store(true)
			writeFixtureJSON(t, writer, map[string]any{"id": 7001, "status": "completed"})
		default:
			http.Error(writer, "unexpected synthetic route", http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)
	client := syntheticClient(t, server, domain.ConnectionSonarr, syntheticCapabilities())
	result, err := client.Import(context.Background(), testConnection, ports.ImportRequest{
		RegisteredExternalID: "201", PreviewRevision: testPreview, Transfer: "copy",
		Files: []ports.ImportFile{
			{Source: domain.FileTarget{RootID: "downloads", RelativePath: "Synthetic Show/S01E01.mkv"}, MovieOrEpisodeID: "301"},
			{Source: domain.FileTarget{RootID: "downloads", RelativePath: "Synthetic Show/S01E02.mkv"}, MovieOrEpisodeID: "302"},
		},
	})
	if err == nil || !hasCode(err, domain.OutcomeUnknown) {
		t.Fatalf("Import error = %v, want unknown partial result", err)
	}
	if len(result.Files) != 1 || result.Effect != nil || commandCalls.Load() != 1 {
		t.Fatalf("partial result = %#v calls=%d, want one observed file and no success effect", result, commandCalls.Load())
	}
}

func TestImportAlreadySatisfiedSkipsCommand(t *testing.T) {
	var commandCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch {
		case request.Method == http.MethodGet && request.URL.Path == "/api/v3/episode":
			writeFixtureJSON(t, writer, []nativeEpisode{{ID: 301, SeriesID: 201, EpisodeFileID: 801, EpisodeFile: &nativeFile{ID: 801, SeriesID: 201, Path: "/synthetic/downloads/Synthetic Show/S01E01.mkv", Size: 10}}})
		case request.Method == http.MethodGet && request.URL.Path == "/api/v3/history":
			writeFixtureJSON(t, writer, []nativeHistory{{ID: 9001, EventType: "downloadFolderImported", SeriesID: 201, EpisodeID: 301}})
		case request.Method == http.MethodPost:
			commandCalls.Add(1)
			http.Error(writer, "already satisfied should not write", http.StatusTeapot)
		default:
			http.Error(writer, "unexpected synthetic route", http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)
	client := syntheticClient(t, server, domain.ConnectionSonarr, syntheticCapabilities())
	result, err := client.Import(context.Background(), testConnection, ports.ImportRequest{
		RegisteredExternalID: "201", PreviewRevision: testPreview, Transfer: "copy",
		Files: []ports.ImportFile{{Source: domain.FileTarget{RootID: "downloads", RelativePath: "Synthetic Show/S01E01.mkv"}, MovieOrEpisodeID: "301"}},
	})
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if result.Effect == nil || result.Effect.Outcome != domain.OutcomeAlreadySatisfied || commandCalls.Load() != 0 {
		t.Fatalf("result = %#v calls=%d, want already satisfied without command", result, commandCalls.Load())
	}
}

func TestImportRejectsInvalidSubtitleRoleAndNeverCallsCommand(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		calls.Add(1)
		writeFixtureJSON(t, writer, []nativeEpisode{})
	}))
	t.Cleanup(server.Close)
	client := syntheticClient(t, server, domain.ConnectionSonarr, syntheticCapabilities())
	_, err := client.Import(context.Background(), testConnection, ports.ImportRequest{
		RegisteredExternalID: "201", PreviewRevision: testPreview, Transfer: "copy",
		Files: []ports.ImportFile{{Source: domain.FileTarget{RootID: "downloads", RelativePath: "episode.mkv"}, MovieOrEpisodeID: "301", Subtitle: true}},
	})
	if err == nil || !hasCode(err, domain.OutcomeInvalidInput) {
		t.Fatalf("Import error = %v, want invalid subtitle role", err)
	}
	if calls.Load() != 0 {
		t.Fatalf("invalid subtitle reached network: %d", calls.Load())
	}
}

func TestImportRequiresExactServerPreviewBindingBeforeCommand(t *testing.T) {
	cases := []struct {
		name     string
		resolver PreviewResolver
	}{
		{
			name: "forged revision",
			resolver: func(_ context.Context, _ domain.ConfigID, request ports.ImportRequest) (ports.ImportPreview, error) {
				return ports.ImportPreview{Revision: "sha256:trusted-preview", Files: append([]ports.ImportFile(nil), request.Files...), ObservedAt: time.Now().UTC()}, nil
			},
		},
		{
			name: "omitted selection",
			resolver: func(_ context.Context, _ domain.ConfigID, request ports.ImportRequest) (ports.ImportPreview, error) {
				return ports.ImportPreview{Revision: request.PreviewRevision, ObservedAt: time.Now().UTC()}, nil
			},
		},
		{
			name: "selected rejection",
			resolver: func(_ context.Context, _ domain.ConfigID, request ports.ImportRequest) (ports.ImportPreview, error) {
				return ports.ImportPreview{
					Revision: request.PreviewRevision, Files: append([]ports.ImportFile(nil), request.Files...), ObservedAt: time.Now().UTC(),
					Rejections: []ports.ImportRejection{{Source: request.Files[0].Source, Code: "existingFile", Reason: "synthetic destination collision"}},
				}, nil
			},
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			var commands atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				switch {
				case request.Method == http.MethodGet && request.URL.Path == "/api/v3/episode":
					writeFixtureJSON(t, writer, []nativeEpisode{{ID: 301, SeriesID: 201}})
				case request.Method == http.MethodGet && request.URL.Path == "/api/v3/history":
					writeFixtureJSON(t, writer, []nativeHistory{})
				case request.Method == http.MethodPost && request.URL.Path == "/api/v3/command":
					commands.Add(1)
					http.Error(writer, "preview rejection must prevent command", http.StatusTeapot)
				default:
					http.Error(writer, "unexpected synthetic route", http.StatusNotFound)
				}
			}))
			t.Cleanup(server.Close)
			client := syntheticClientWithResolver(t, server, domain.ConnectionSonarr, syntheticCapabilities(), testCase.resolver)
			request := ports.ImportRequest{
				RegisteredExternalID: "201", PreviewRevision: "sha256:requested-preview", Transfer: "copy",
				Files: []ports.ImportFile{{Source: domain.FileTarget{RootID: "downloads", RelativePath: "episode.mkv"}, MovieOrEpisodeID: "301"}},
			}
			_, err := client.Import(context.Background(), testConnection, request)
			if err == nil || (!hasCode(err, domain.OutcomeConflict) && !hasCode(err, domain.OutcomeUnknown)) {
				t.Fatalf("Import error = %v, want preview binding failure", err)
			}
			if commands.Load() != 0 {
				t.Fatalf("command calls = %d, want zero", commands.Load())
			}
		})
	}
}

func TestArrWriteRejectsContradictoryReadbackIdentity(t *testing.T) {
	type testCase struct {
		name    string
		kind    domain.ConnectionKind
		handler func(testing.TB, http.ResponseWriter, *http.Request)
		request ports.ImportRequest
	}
	cases := []testCase{
		{
			name:    "sonarr same file id changes path",
			kind:    domain.ConnectionSonarr,
			request: ports.ImportRequest{RegisteredExternalID: "201", PreviewRevision: testPreview, Transfer: "copy", Files: []ports.ImportFile{{Source: domain.FileTarget{RootID: "downloads", RelativePath: "one.mkv"}, MovieOrEpisodeID: "301"}}},
			handler: func(t testing.TB, writer http.ResponseWriter, request *http.Request) {
				if request.URL.Path == "/api/v3/episode" {
					writeFixtureJSON(t, writer, []nativeEpisode{
						{ID: 301, SeriesID: 201, EpisodeFileID: 801, EpisodeFile: &nativeFile{ID: 801, SeriesID: 201, Path: "/synthetic/downloads/one.mkv", Size: 10}},
						{ID: 302, SeriesID: 201, EpisodeFileID: 801, EpisodeFile: &nativeFile{ID: 801, SeriesID: 201, Path: "/synthetic/downloads/two.mkv", Size: 20}},
					})
					return
				}
				writeFixtureJSON(t, writer, []nativeHistory{})
			},
		},
		{
			name:    "sonarr distinct file ids share path",
			kind:    domain.ConnectionSonarr,
			request: ports.ImportRequest{RegisteredExternalID: "201", PreviewRevision: testPreview, Transfer: "copy", Files: []ports.ImportFile{{Source: domain.FileTarget{RootID: "downloads", RelativePath: "one.mkv"}, MovieOrEpisodeID: "301"}}},
			handler: func(t testing.TB, writer http.ResponseWriter, request *http.Request) {
				if request.URL.Path == "/api/v3/episode" {
					writeFixtureJSON(t, writer, []nativeEpisode{
						{ID: 301, SeriesID: 201, EpisodeFileID: 801, EpisodeFile: &nativeFile{ID: 801, SeriesID: 201, Path: "/synthetic/downloads/one.mkv", Size: 10}},
						{ID: 302, SeriesID: 201, EpisodeFileID: 802, EpisodeFile: &nativeFile{ID: 802, SeriesID: 201, Path: "/synthetic/downloads/one.mkv", Size: 10}},
					})
					return
				}
				writeFixtureJSON(t, writer, []nativeHistory{})
			},
		},
		{
			name:    "radarr foreign title and file identity",
			kind:    domain.ConnectionRadarr,
			request: ports.ImportRequest{RegisteredExternalID: "101", PreviewRevision: testPreview, Transfer: "copy", Files: []ports.ImportFile{{Source: domain.FileTarget{RootID: "downloads", RelativePath: "one.mkv"}, MovieOrEpisodeID: "101"}}},
			handler: func(t testing.TB, writer http.ResponseWriter, request *http.Request) {
				if request.URL.Path == "/api/v3/movie/101" {
					writeFixtureJSON(t, writer, nativeTitle{ID: 101, MovieFile: &nativeFile{ID: 501, MovieID: 999, Path: "/synthetic/downloads/one.mkv", Size: 10}})
					return
				}
				writeFixtureJSON(t, writer, []nativeHistory{})
			},
		},
		{
			name:    "radarr foreign title id",
			kind:    domain.ConnectionRadarr,
			request: ports.ImportRequest{RegisteredExternalID: "101", PreviewRevision: testPreview, Transfer: "copy", Files: []ports.ImportFile{{Source: domain.FileTarget{RootID: "downloads", RelativePath: "one.mkv"}, MovieOrEpisodeID: "101"}}},
			handler: func(t testing.TB, writer http.ResponseWriter, request *http.Request) {
				if request.URL.Path == "/api/v3/movie/101" {
					writeFixtureJSON(t, writer, nativeTitle{ID: 999, MovieFile: &nativeFile{ID: 501, MovieID: 999, Path: "/synthetic/downloads/one.mkv", Size: 10}})
					return
				}
				writeFixtureJSON(t, writer, []nativeHistory{})
			},
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			var writes atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				if request.Method != http.MethodGet {
					writes.Add(1)
					http.Error(writer, "contradictory readback must prevent command", http.StatusTeapot)
					return
				}
				testCase.handler(t, writer, request)
			}))
			t.Cleanup(server.Close)
			client, err := New(Config{ConnectionID: testConnection, Kind: testCase.kind, Endpoint: server.URL, APIKey: "synthetic-key", HTTPClient: server.Client(), RootPaths: map[domain.ConfigID]string{"downloads": "/synthetic/downloads"}})
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			result, err := client.Import(context.Background(), testConnection, testCase.request)
			if err == nil || !hasCode(err, domain.OutcomeUnknown) {
				t.Fatalf("Import error = %v result=%#v, want malformed unknown observation", err, result)
			}
			if result.Effect != nil || writes.Load() != 0 {
				t.Fatalf("result=%#v writes=%d, want no effect and no writes", result, writes.Load())
			}
		})
	}
}

func TestSonarrReadbackRequiresNestedSeriesIdentity(t *testing.T) {
	cases := []struct {
		name      string
		seriesID  string
		includeID bool
	}{
		{name: "missing", includeID: false},
		{name: "null", seriesID: "null", includeID: true},
		{name: "zero", seriesID: "0", includeID: true},
		{name: "foreign", seriesID: "999", includeID: true},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			var writes atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				if request.Method != http.MethodGet {
					writes.Add(1)
					http.Error(writer, "incomplete episode evidence must prevent mutation", http.StatusTeapot)
					return
				}
				switch request.URL.Path {
				case "/api/v3/episode":
					file := `{"id":801,"path":"/synthetic/downloads/one.mkv","size":10}`
					if testCase.includeID {
						file = fmt.Sprintf(`{"id":801,"seriesId":%s,"path":"/synthetic/downloads/one.mkv","size":10}`, testCase.seriesID)
					}
					writer.Header().Set("Content-Type", "application/json")
					_, _ = io.WriteString(writer, fmt.Sprintf(`[{"id":301,"seriesId":201,"episodeFileId":801,"episodeFile":%s}]`, file))
				case "/api/v3/history":
					writeFixtureJSON(t, writer, []nativeHistory{})
				default:
					http.Error(writer, "unexpected synthetic read", http.StatusNotFound)
				}
			}))
			t.Cleanup(server.Close)

			client, err := New(Config{
				ConnectionID: testConnection, Kind: domain.ConnectionSonarr, Endpoint: server.URL,
				APIKey: "synthetic-key", HTTPClient: server.Client(),
				RootPaths: map[domain.ConfigID]string{"downloads": "/synthetic/downloads"},
			})
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			result, err := client.Import(context.Background(), testConnection, ports.ImportRequest{
				RegisteredExternalID: "201", PreviewRevision: testPreview, Transfer: "copy",
				Files: []ports.ImportFile{{Source: domain.FileTarget{RootID: "downloads", RelativePath: "one.mkv"}, MovieOrEpisodeID: "301"}},
			})
			if err == nil || !hasCode(err, domain.OutcomeUnknown) {
				t.Fatalf("Import error = %v result=%#v, want unknown incomplete observation", err, result)
			}
			if result.Effect != nil || writes.Load() != 0 {
				t.Fatalf("result=%#v writes=%d, want no effect and no writes", result, writes.Load())
			}
		})
	}
}

func TestSonarrReadbackRejectsEpisodeClaimingMultipleFiles(t *testing.T) {
	var writes atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet {
			writes.Add(1)
			http.Error(writer, "ambiguous episode evidence must prevent mutation", http.StatusTeapot)
			return
		}
		switch request.URL.Path {
		case "/api/v3/episode":
			writeFixtureJSON(t, writer, []nativeEpisode{
				{ID: 301, SeriesID: 201, EpisodeFileID: 801, EpisodeFile: &nativeFile{ID: 801, SeriesID: 201, Path: "/synthetic/downloads/one.mkv", Size: 10}},
				{ID: 301, SeriesID: 201, EpisodeFileID: 802, EpisodeFile: &nativeFile{ID: 802, SeriesID: 201, Path: "/synthetic/downloads/two.mkv", Size: 10}},
			})
		case "/api/v3/history":
			writeFixtureJSON(t, writer, []nativeHistory{})
		default:
			http.Error(writer, "unexpected synthetic read", http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)

	client, err := New(Config{
		ConnectionID: testConnection, Kind: domain.ConnectionSonarr, Endpoint: server.URL,
		APIKey: "synthetic-key", HTTPClient: server.Client(),
		RootPaths: map[domain.ConfigID]string{"downloads": "/synthetic/downloads"},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	result, err := client.Import(context.Background(), testConnection, ports.ImportRequest{
		RegisteredExternalID: "201", PreviewRevision: testPreview, Transfer: "copy",
		Files: []ports.ImportFile{{Source: domain.FileTarget{RootID: "downloads", RelativePath: "one.mkv"}, MovieOrEpisodeID: "301"}},
	})
	if err == nil || !hasCode(err, domain.OutcomeUnknown) {
		t.Fatalf("Import error = %v result=%#v, want unknown ambiguous observation", err, result)
	}
	if result.Effect != nil || writes.Load() != 0 {
		t.Fatalf("result=%#v writes=%d, want no effect and no writes", result, writes.Load())
	}
}

func TestSonarrReadbackRejectsDuplicateEpisodeAcrossFilelessRows(t *testing.T) {
	cases := []struct {
		name string
		rows []nativeEpisode
	}{
		{
			name: "fileless then complete",
			rows: []nativeEpisode{
				{ID: 301, SeriesID: 201},
				{ID: 301, SeriesID: 201, EpisodeFileID: 801, EpisodeFile: &nativeFile{ID: 801, SeriesID: 201, Path: "/synthetic/downloads/one.mkv", Size: 10}},
			},
		},
		{
			name: "complete then fileless",
			rows: []nativeEpisode{
				{ID: 301, SeriesID: 201, EpisodeFileID: 801, EpisodeFile: &nativeFile{ID: 801, SeriesID: 201, Path: "/synthetic/downloads/one.mkv", Size: 10}},
				{ID: 301, SeriesID: 201},
			},
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			var writes atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				if request.Method != http.MethodGet {
					writes.Add(1)
					http.Error(writer, "duplicate episode evidence must prevent mutation", http.StatusTeapot)
					return
				}
				switch request.URL.Path {
				case "/api/v3/episode":
					writeFixtureJSON(t, writer, testCase.rows)
				case "/api/v3/history":
					writeFixtureJSON(t, writer, []nativeHistory{})
				default:
					http.Error(writer, "unexpected synthetic read", http.StatusNotFound)
				}
			}))
			t.Cleanup(server.Close)

			client, err := New(Config{
				ConnectionID: testConnection, Kind: domain.ConnectionSonarr, Endpoint: server.URL,
				APIKey: "synthetic-key", HTTPClient: server.Client(),
				RootPaths: map[domain.ConfigID]string{"downloads": "/synthetic/downloads"},
			})
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			result, err := client.Import(context.Background(), testConnection, ports.ImportRequest{
				RegisteredExternalID: "201", PreviewRevision: testPreview, Transfer: "copy",
				Files: []ports.ImportFile{{Source: domain.FileTarget{RootID: "downloads", RelativePath: "one.mkv"}, MovieOrEpisodeID: "301"}},
			})
			if err == nil || !hasCode(err, domain.OutcomeUnknown) {
				t.Fatalf("Import error = %v result=%#v, want unknown duplicate observation", err, result)
			}
			if result.Effect != nil || writes.Load() != 0 {
				t.Fatalf("result=%#v writes=%d, want no effect and no writes", result, writes.Load())
			}
		})
	}
}

func TestSonarrMultiEpisodeFileReadbackRemainsOneAssociation(t *testing.T) {
	var imported atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch {
		case request.Method == http.MethodGet && request.URL.Path == "/api/v3/episode":
			if imported.Load() {
				writeFixtureJSON(t, writer, []nativeEpisode{
					{ID: 301, SeriesID: 201, EpisodeFileID: 801, EpisodeFile: &nativeFile{ID: 801, SeriesID: 201, Path: "/synthetic/downloads/pack.mkv", Size: 20}},
					{ID: 302, SeriesID: 201, EpisodeFileID: 801, EpisodeFile: &nativeFile{ID: 801, SeriesID: 201, Path: "/synthetic/downloads/pack.mkv", Size: 20}},
				})
				return
			}
			writeFixtureJSON(t, writer, []nativeEpisode{{ID: 301, SeriesID: 201}, {ID: 302, SeriesID: 201}})
		case request.Method == http.MethodGet && request.URL.Path == "/api/v3/history":
			writeFixtureJSON(t, writer, []nativeHistory{})
		case request.Method == http.MethodPost && request.URL.Path == "/api/v3/command":
			imported.Store(true)
			writeFixtureJSON(t, writer, map[string]any{"id": 7001, "status": "completed"})
		default:
			http.Error(writer, "unexpected synthetic route", http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)
	client := syntheticClient(t, server, domain.ConnectionSonarr, syntheticCapabilities())
	result, err := client.Import(context.Background(), testConnection, ports.ImportRequest{
		RegisteredExternalID: "201", PreviewRevision: testPreview, Transfer: "copy",
		Files: []ports.ImportFile{
			{Source: domain.FileTarget{RootID: "downloads", RelativePath: "pack.mkv"}, MovieOrEpisodeID: "301"},
			{Source: domain.FileTarget{RootID: "downloads", RelativePath: "pack.mkv"}, MovieOrEpisodeID: "302"},
		},
	})
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if len(result.Files) != 1 || result.Files[0].MovieID != "" || len(result.Files[0].EpisodeIDs) != 2 || !containsString(result.Files[0].EpisodeIDs, "301") || !containsString(result.Files[0].EpisodeIDs, "302") {
		t.Fatalf("result files = %#v, want one file with two episode associations", result.Files)
	}
}

func TestNewRegistrationExplicitMonitoringOptInIsPreserved(t *testing.T) {
	monitored := true
	payload, err := registrationPayload(ports.RegistrationRequest{
		ProviderID: "4242", Kind: domain.MediaMovie,
		Fields: ports.RegistrationFields{RootFolder: "/synthetic/library", QualityProfileID: "7", Monitored: &monitored},
	}, domain.ConnectionRadarr, nil)
	if err != nil {
		t.Fatalf("registrationPayload: %v", err)
	}
	if payload.Monitored == nil || !*payload.Monitored {
		t.Fatalf("new registration monitored = %#v, want explicit true", payload.Monitored)
	}
	if payload.AddOptions == nil || payload.AddOptions.Monitor != "none" || payload.AddOptions.SearchForMovie || payload.AddOptions.SearchForMissingEpisodes || payload.AddOptions.SearchForCutoffUnmet {
		t.Fatalf("new registration add options = %#v, want no-search defaults", payload.AddOptions)
	}
}

func TestArrWriteSanitizesWrappedContextErrors(t *testing.T) {
	for _, testCase := range []struct {
		name  string
		cause error
		want  error
	}{
		{name: "cancel", cause: context.Canceled, want: context.Canceled},
		{name: "deadline", cause: context.DeadlineExceeded, want: context.DeadlineExceeded},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			transport := roundTripFunc(func(_ *http.Request) (*http.Response, error) {
				return nil, fmt.Errorf("private endpoint and transport detail: %w", testCase.cause)
			})
			client, err := newClient(Config{ConnectionID: testConnection, Kind: domain.ConnectionRadarr, Endpoint: "http://fixture.invalid/private-prefix", HTTPClient: &http.Client{Transport: transport}}, blockedCapabilities())
			if err != nil {
				t.Fatalf("newClient: %v", err)
			}
			_, err = client.listTitles(context.Background())
			if !errors.Is(err, testCase.want) {
				t.Fatalf("list error = %v, want errors.Is(%v)", err, testCase.want)
			}
			if strings.Contains(err.Error(), "private") || strings.Contains(err.Error(), "fixture.invalid") || strings.Contains(err.Error(), "transport detail") {
				t.Fatalf("context error leaked private detail: %v", err)
			}
		})
	}
}

func TestArrWriteSanitizesWrappedContextBodyErrors(t *testing.T) {
	transport := roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Body: contextErrorBody{err: fmt.Errorf("private body detail: %w", context.Canceled)}}, nil
	})
	client, err := newClient(Config{ConnectionID: testConnection, Kind: domain.ConnectionRadarr, Endpoint: "http://fixture.invalid", HTTPClient: &http.Client{Transport: transport}}, blockedCapabilities())
	if err != nil {
		t.Fatalf("newClient: %v", err)
	}
	_, err = client.listTitles(context.Background())
	if !errors.Is(err, context.Canceled) || strings.Contains(err.Error(), "private") || strings.Contains(err.Error(), "fixture.invalid") {
		t.Fatalf("body error = %v, want sanitized canceled identity", err)
	}
}

type contextErrorBody struct{ err error }

func (body contextErrorBody) Read([]byte) (int, error) { return 0, body.err }
func (body contextErrorBody) Close() error             { return nil }

func TestStrictArrWriteDecoderRejectsDuplicateKeysAndInvalidUTF8(t *testing.T) {
	var target nativeTitle
	if err := decodeStrictJSON([]byte(`{"id":101,"id":102}`), &target); err == nil {
		t.Fatal("duplicate title identity unexpectedly decoded")
	}
	if err := decodeStrictJSON([]byte{'{', '"', 'i', 'd', '"', ':', 1, '}', 0xff}, &target); err == nil {
		t.Fatal("invalid UTF-8 unexpectedly decoded")
	}
}

func TestArrWritePreservesContextDeadlineIdentity(t *testing.T) {
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		<-request.Context().Done()
		return nil, request.Context().Err()
	})
	client, err := newClient(Config{ConnectionID: testConnection, Kind: domain.ConnectionRadarr, Endpoint: "http://arr.invalid", HTTPClient: &http.Client{Transport: transport}, ReconcileTimeout: time.Hour}, blockedCapabilities())
	if err != nil {
		t.Fatalf("newClient: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	_, err = client.listTitles(ctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("list error = %v, want context deadline", err)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

func int64Ptr(value int64) *int64 { return &value }
func boolPtr(value bool) *bool    { return &value }

func stringValue(value any) string {
	text, _ := value.(string)
	return text
}

func hasCode(err error, code domain.UpstreamErrorCode) bool {
	var upstream domain.UpstreamError
	return errors.As(err, &upstream) && upstream.Code == code
}
