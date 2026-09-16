package read

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/guilycst/mastarr/internal/domain"
	"github.com/guilycst/mastarr/internal/ports"
)

func TestArrNativeModulesTranslateTypedReadErrors(t *testing.T) {
	for _, testCase := range []struct {
		name   string
		kind   domain.ConnectionKind
		path   string
		code   domain.UpstreamErrorCode
		status int
	}{
		{name: "radarr rate limit", kind: domain.ConnectionRadarr, path: apiMovies, code: domain.OutcomeRateLimited, status: http.StatusTooManyRequests},
		{name: "sonarr unauthorized", kind: domain.ConnectionSonarr, path: apiSeries, code: domain.OutcomeUnauthorized, status: http.StatusUnauthorized},
		{name: "radarr unavailable", kind: domain.ConnectionRadarr, path: apiMovies, code: domain.OutcomeUnavailable, status: http.StatusBadGateway},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
				if request.Header.Get("X-Api-Key") != "fixture-api-key" {
					response.WriteHeader(http.StatusUnauthorized)
					return
				}
				if request.URL.Path != testCase.path {
					response.WriteHeader(http.StatusNotFound)
					return
				}
				response.WriteHeader(testCase.status)
			}))
			defer server.Close()
			connectionID := domain.ConfigID("native-error-" + strings.ReplaceAll(testCase.name, " ", "-"))
			client, err := New(Config{ConnectionID: connectionID, Kind: testCase.kind, Endpoint: server.URL, APIKey: "fixture-api-key", MaxPageSize: 2, MaxPages: 2, MaxRecords: 10, MaxFiles: 10})
			if err != nil {
				t.Fatal(err)
			}
			_, err = client.List(context.Background(), connectionID, "", 1)
			if err == nil {
				t.Fatal("native read unexpectedly succeeded")
			}
			var upstream domain.UpstreamError
			if !errors.As(err, &upstream) || upstream.Code != testCase.code || upstream.Status != testCase.status {
				t.Fatalf("translated error = %#v (%v), want code=%s status=%d", upstream, err, testCase.code, testCase.status)
			}
			if strings.Contains(err.Error(), server.URL) || strings.Contains(err.Error(), "fixture-api-key") {
				t.Fatalf("translated error leaked endpoint or API key: %v", err)
			}
		})
	}
}

func TestArrNativeSonarrPreviewUsesDownloadedFolderMode(t *testing.T) {
	var seenQuery url.Values
	handler := http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Header.Get("X-Api-Key") != "fixture-api-key" {
			response.WriteHeader(http.StatusUnauthorized)
			return
		}
		if request.Method != http.MethodGet || request.URL.Path != apiManualImport {
			response.WriteHeader(http.StatusNotFound)
			return
		}
		seenQuery = request.URL.Query()
		writeFixture(response, "sonarr-manual-import.json")
	})
	client, server := newSyntheticClient(t, domain.ConnectionSonarr, "sonarr-native-preview", handler, 2, 10, 50, 50)
	defer server.Close()
	preview, err := client.PreviewImport(context.Background(), "sonarr-native-preview", ports.ImportPreviewRequest{
		RegisteredExternalID: "201",
		Transfer:             "copy",
		Files: []ports.ImportFile{{
			Source:           domain.FileTarget{RootID: "library", RelativePath: "series/Synthetic Series - S01E01.mkv"},
			MovieOrEpisodeID: "301",
		}},
	})
	if err != nil || len(preview.Files) != 1 || len(preview.Rejections) != 0 {
		t.Fatalf("native Sonarr preview = %#v, err=%v", preview, err)
	}
	if seenQuery.Get("folder") != "/downloads/series" || seenQuery.Get("filterExistingFiles") != "true" || seenQuery.Get("seriesId") != "" {
		t.Fatalf("Sonarr folder preview query = %#v", seenQuery)
	}
}

func TestArrNativeCatalogMappingKeepsConnectionScopedIdentity(t *testing.T) {
	handler := http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Header.Get("X-Api-Key") != "fixture-api-key" || request.URL.Path != apiMovies {
			response.WriteHeader(http.StatusNotFound)
			return
		}
		writeFixture(response, "radarr-movies-full.json")
	})
	first, firstServer := newSyntheticClient(t, domain.ConnectionRadarr, "radarr-native-one", handler, 2, 10, 50, 50)
	defer firstServer.Close()
	second, secondServer := newSyntheticClient(t, domain.ConnectionRadarr, "radarr-native-two", handler, 2, 10, 50, 50)
	defer secondServer.Close()
	firstPage, err := first.List(context.Background(), "radarr-native-one", "", 2)
	if err != nil || len(firstPage.Items) == 0 {
		t.Fatalf("first native catalog = %#v, err=%v", firstPage, err)
	}
	secondPage, err := second.List(context.Background(), "radarr-native-two", "", 2)
	if err != nil || len(secondPage.Items) == 0 {
		t.Fatalf("second native catalog = %#v, err=%v", secondPage, err)
	}
	if firstPage.Items[0].ExternalID != secondPage.Items[0].ExternalID || first.ScopedIdentity(firstPage.Items[0].ExternalID) == second.ScopedIdentity(secondPage.Items[0].ExternalID) {
		t.Fatalf("native IDs lost connection scope: first=%q second=%q", first.ScopedIdentity(firstPage.Items[0].ExternalID), second.ScopedIdentity(secondPage.Items[0].ExternalID))
	}
}

func TestArrNativeRadarrCatalogRejectsFileIdentityCollisions(t *testing.T) {
	for index, testCase := range []struct {
		name       string
		fileID     int64
		filePath   string
		fileSize   int64
		wantReason string
	}{
		{name: "same upstream ID changes path and size", fileID: 501, filePath: "/downloads/movies/two.mkv", fileSize: 20, wantReason: "movie_file_conflicting_details"},
		{name: "different upstream IDs claim one path", fileID: 502, filePath: "/downloads/movies/one.mkv", fileSize: 10, wantReason: "movie_file_identity_conflict"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			body, err := json.Marshal([]map[string]any{
				{"id": 101, "title": "Synthetic Film One", "path": "/downloads/movies/one", "monitored": true, "movieFile": map[string]any{
					"id": 501, "movieId": 101, "path": "/downloads/movies/one.mkv", "size": 10,
				}},
				{"id": 102, "title": "Synthetic Film Two", "path": "/downloads/movies/two", "monitored": true, "movieFile": map[string]any{
					"id": testCase.fileID, "movieId": 102, "path": testCase.filePath, "size": testCase.fileSize,
				}},
			})
			if err != nil {
				t.Fatal(err)
			}
			handler := http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
				if request.Header.Get("X-Api-Key") != "fixture-api-key" || request.URL.Path != apiMovies {
					response.WriteHeader(http.StatusNotFound)
					return
				}
				response.Header().Set("Content-Type", "application/json")
				_, _ = response.Write(body)
			})
			connectionID := domain.ConfigID("radarr-cat-id-" + strconv.Itoa(index))
			client, server := newSyntheticClient(t, domain.ConnectionRadarr, connectionID, handler, 2, 10, 50, 50)
			defer server.Close()
			page, err := client.List(context.Background(), connectionID, "", 2)
			if err != nil {
				t.Fatalf("collision was promoted to a transport error: %v", err)
			}
			if len(page.Items) != 2 || page.Coverage.Completeness != domain.CompletenessPartial || !hasReason(page.Coverage, testCase.wantReason) {
				t.Fatalf("collision coverage = %#v", page.Coverage)
			}
			if len(page.Items[0].Files) != 1 || len(page.Items[1].Files) != 0 {
				t.Fatalf("conflicting file escaped catalog mapping: %#v", page.Items)
			}
		})
	}
}

func TestArrNativeRadarrObserveRejectsFileIdentityCollisions(t *testing.T) {
	for index, testCase := range []struct {
		name      string
		body      string
		wantError bool
	}{
		{
			name:      "same upstream ID changes path",
			body:      `[{"id":501,"movieId":101,"path":"/downloads/movies/one.mkv","size":10},{"id":501,"movieId":101,"path":"/downloads/movies/two.mkv","size":20}]`,
			wantError: true,
		},
		{
			name:      "different upstream IDs claim one path",
			body:      `[{"id":501,"movieId":101,"path":"/downloads/movies/one.mkv","size":10},{"id":502,"movieId":101,"path":"/downloads/movies/one.mkv","size":10}]`,
			wantError: true,
		},
		{
			name:      "one native file is accepted",
			body:      `[{"id":501,"movieId":101,"path":"/downloads/movies/one.mkv","size":10}]`,
			wantError: false,
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			handler := http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
				if request.Header.Get("X-Api-Key") != "fixture-api-key" {
					response.WriteHeader(http.StatusUnauthorized)
					return
				}
				switch request.URL.Path {
				case apiMovies + "/101":
					writeJSON(response, map[string]any{"id": 101, "title": "Synthetic Film", "path": "/downloads/movies/Synthetic Film", "monitored": true})
				case apiMovieFiles:
					response.Header().Set("Content-Type", "application/json")
					_, _ = response.Write([]byte(testCase.body))
				default:
					response.WriteHeader(http.StatusNotFound)
				}
			})
			connectionID := domain.ConfigID("radarr-obs-id-" + strconv.Itoa(index))
			client, server := newSyntheticClient(t, domain.ConnectionRadarr, connectionID, handler, 2, 10, 50, 50)
			defer server.Close()
			observation, err := client.ObserveImport(context.Background(), connectionID, "101")
			if testCase.wantError {
				assertUpstreamCode(t, err, domain.OutcomeUnknown)
				if len(observation.Files) != 0 {
					t.Fatalf("conflicting observation returned files: %#v", observation.Files)
				}
				return
			}
			if err != nil || len(observation.Files) != 1 || observation.Files[0].ExternalID != "501" {
				t.Fatalf("valid observation = %#v, %v", observation, err)
			}
		})
	}
}

func TestArrNativeSonarrFileIdentityCollisionsStayIncomplete(t *testing.T) {
	for index, testCase := range []struct {
		name           string
		fileID         int64
		secondPath     string
		secondSize     int64
		wantReason     string
		wantComplete   bool
		wantEpisodeIDs int
	}{
		{name: "same upstream ID changes path and size", fileID: 801, secondPath: "/downloads/series/two.mkv", secondSize: 20, wantReason: "episode_file_conflicting_details", wantEpisodeIDs: 1},
		{name: "different upstream IDs claim one path", fileID: 802, secondPath: "/downloads/series/one.mkv", secondSize: 10, wantReason: "episode_file_identity_conflict", wantEpisodeIDs: 1},
		{name: "same ID and physical details span episodes", fileID: 801, secondPath: "/downloads/series/one.mkv", secondSize: 10, wantComplete: true, wantEpisodeIDs: 2},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			seriesBody, err := json.Marshal([]map[string]any{{"id": 201, "title": "Synthetic Series", "path": "/downloads/series/Synthetic Series", "monitored": true}})
			if err != nil {
				t.Fatal(err)
			}
			episodeBody, err := json.Marshal([]map[string]any{
				{"id": 301, "seriesId": 201, "seasonNumber": 1, "episodeNumber": 1, "hasFile": true, "episodeFileId": 801, "episodeFile": map[string]any{"id": 801, "seriesId": 201, "path": "/downloads/series/one.mkv", "size": 10}},
				{"id": 302, "seriesId": 201, "seasonNumber": 1, "episodeNumber": 2, "hasFile": true, "episodeFileId": testCase.fileID, "episodeFile": map[string]any{"id": testCase.fileID, "seriesId": 201, "path": testCase.secondPath, "size": testCase.secondSize}},
			})
			if err != nil {
				t.Fatal(err)
			}
			handler := http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
				if request.Header.Get("X-Api-Key") != "fixture-api-key" {
					response.WriteHeader(http.StatusUnauthorized)
					return
				}
				switch request.URL.Path {
				case apiSeries:
					response.Header().Set("Content-Type", "application/json")
					_, _ = response.Write(seriesBody)
				case apiEpisodes:
					if request.URL.Query().Get("seriesId") != "201" || request.URL.Query().Get("includeEpisodeFile") != "true" {
						response.WriteHeader(http.StatusBadRequest)
						return
					}
					response.Header().Set("Content-Type", "application/json")
					_, _ = response.Write(episodeBody)
				default:
					response.WriteHeader(http.StatusNotFound)
				}
			})
			connectionID := domain.ConfigID("sonarr-id-" + strconv.Itoa(index))
			client, server := newSyntheticClient(t, domain.ConnectionSonarr, connectionID, handler, 2, 10, 50, 50)
			defer server.Close()
			page, err := client.List(context.Background(), connectionID, "", 2)
			if err != nil || len(page.Items) != 1 {
				t.Fatalf("catalog = %#v, %v", page, err)
			}
			if testCase.wantComplete {
				if page.Coverage.Completeness != domain.CompletenessComplete || len(page.Items[0].Files) != 1 || len(page.Items[0].Files[0].EpisodeIDs) != testCase.wantEpisodeIDs {
					t.Fatalf("valid multi-episode catalog = %#v", page)
				}
			} else {
				if page.Coverage.Completeness != domain.CompletenessPartial || !hasReason(page.Coverage, testCase.wantReason) || len(page.Items[0].Files) != 1 || len(page.Items[0].Files[0].EpisodeIDs) != testCase.wantEpisodeIDs {
					t.Fatalf("collision catalog = %#v", page)
				}
			}
			observation, observeErr := client.ObserveImport(context.Background(), connectionID, "201")
			if testCase.wantComplete {
				if observeErr != nil || len(observation.Files) != 1 || len(observation.Files[0].EpisodeIDs) != testCase.wantEpisodeIDs {
					t.Fatalf("valid multi-episode observation = %#v, %v", observation, observeErr)
				}
			} else {
				assertUpstreamCode(t, observeErr, domain.OutcomeUnknown)
				if len(observation.Files) != 0 {
					t.Fatalf("conflicting observation returned files: %#v", observation.Files)
				}
			}
		})
	}
}

func TestArrNativeMalformedResponsesDoNotUseLegacyLastWinsDecoder(t *testing.T) {
	duplicateCatalog := `[{"id":101,"id":102,"title":"Synthetic Film","path":"/downloads/movies/Synthetic Film","monitored":true}]`
	missingCatalogID := `[{"title":"Synthetic Film","path":"/downloads/movies/Synthetic Film","monitored":true}]`
	for _, testCase := range []struct {
		name string
		body string
	}{
		{name: "duplicate native identity", body: duplicateCatalog},
		{name: "missing native identity", body: missingCatalogID},
	} {
		t.Run("radarr catalog/"+testCase.name, func(t *testing.T) {
			handler := http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
				if request.URL.Path != apiMovies {
					response.WriteHeader(http.StatusNotFound)
					return
				}
				response.Header().Set("Content-Type", "application/json")
				_, _ = response.Write([]byte(testCase.body))
			})
			connectionID := domain.ConfigID("radarr-native-malformed-" + strings.ReplaceAll(strings.ToLower(testCase.name), " ", "-"))
			client, server := newSyntheticClient(t, domain.ConnectionRadarr, connectionID, handler, 2, 10, 50, 50)
			defer server.Close()
			page, err := client.List(context.Background(), connectionID, "", 2)
			assertUpstreamCode(t, err, domain.OutcomeUnknown)
			if len(page.Items) != 0 {
				t.Fatalf("malformed native catalog was promoted: %#v", page.Items)
			}
		})
	}

	for _, product := range []struct {
		name    string
		kind    domain.ConnectionKind
		body    string
		request ports.ImportPreviewRequest
	}{
		{
			name:    "radarr",
			kind:    domain.ConnectionRadarr,
			body:    `[{"id":701,"id":702,"path":"/downloads/incoming/movie.mkv","relativePath":"movie.mkv","name":"movie.mkv","size":10,"movie":{"id":101},"rejections":[]}]`,
			request: ports.ImportPreviewRequest{RegisteredExternalID: "101", Transfer: "copy", Files: []ports.ImportFile{{Source: domain.FileTarget{RootID: "library", RelativePath: "incoming/movie.mkv"}, MovieOrEpisodeID: "101"}}},
		},
		{
			name:    "sonarr",
			kind:    domain.ConnectionSonarr,
			body:    `[{"id":901,"id":902,"path":"/downloads/incoming/episode.mkv","relativePath":"episode.mkv","name":"episode.mkv","size":10,"series":{"id":201},"episodes":[{"id":301,"seriesId":201,"seasonNumber":1,"episodeNumber":1}],"rejections":[]}]`,
			request: ports.ImportPreviewRequest{RegisteredExternalID: "201", Transfer: "copy", Files: []ports.ImportFile{{Source: domain.FileTarget{RootID: "library", RelativePath: "incoming/episode.mkv"}, MovieOrEpisodeID: "301"}}},
		},
	} {
		t.Run(product.name+" preview", func(t *testing.T) {
			handler := http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
				if request.URL.Path != apiManualImport {
					response.WriteHeader(http.StatusNotFound)
					return
				}
				response.Header().Set("Content-Type", "application/json")
				_, _ = response.Write([]byte(product.body))
			})
			connectionID := domain.ConfigID("arr-native-malformed-preview-" + product.name)
			client, server := newSyntheticClient(t, product.kind, connectionID, handler, 2, 10, 50, 50)
			defer server.Close()
			preview, err := client.PreviewImport(context.Background(), connectionID, product.request)
			assertUpstreamCode(t, err, domain.OutcomeUnknown)
			if len(preview.Files) != 0 || len(preview.Rejections) != 0 {
				t.Fatalf("malformed native preview was promoted: %#v", preview)
			}
		})
	}
}

func TestArrNativeLegacyPreviewRequiresCompleteRows(t *testing.T) {
	const requestBody = `[{"id":901,"path":"/downloads/incoming/episode.mkv","relativePath":"episode.mkv","name":"episode.mkv","size":10,"series":{"id":201,"title":"Synthetic Series"},"episodes":[{"id":301,%s"seasonNumber":1,"episodeNumber":1}],"rejections":[]},{"id":902,"path":"/downloads/incoming/other.mkv","relativePath":"other.mkv","name":"other.mkv","size":10,"series":{"id":201,"title":"Synthetic Series"},"episodes":[{"id":302,"seriesId":201,"seasonNumber":1,"episodeNumber":2}],"rejections":[{"code":"EpisodeFileExists","reason":"Legacy synthetic rejection"}]}]`
	for index, testCase := range []struct {
		name        string
		seriesField string
		wantCalls   int32
	}{
		{name: "missing episode series identity", seriesField: "", wantCalls: 1},
		{name: "null episode series identity", seriesField: `"seriesId":null,`, wantCalls: 1},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			body := []byte(fmt.Sprintf(requestBody, testCase.seriesField))
			var calls atomic.Int32
			handler := http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
				if request.URL.Path != apiManualImport {
					response.WriteHeader(http.StatusNotFound)
					return
				}
				calls.Add(1)
				response.Header().Set("Content-Type", "application/json")
				_, _ = response.Write(body)
			})
			connectionID := domain.ConfigID("sonarr-legacy-incomplete-" + strconv.Itoa(index))
			client, server := newSyntheticClient(t, domain.ConnectionSonarr, connectionID, handler, 2, 10, 50, 50)
			defer server.Close()
			preview, err := client.PreviewImport(context.Background(), connectionID, ports.ImportPreviewRequest{
				RegisteredExternalID: "201",
				Transfer:             "copy",
				Files: []ports.ImportFile{{
					Source:           domain.FileTarget{RootID: "library", RelativePath: "incoming/episode.mkv"},
					MovieOrEpisodeID: "301",
				}},
			})
			assertUpstreamCode(t, err, domain.OutcomeUnknown)
			if len(preview.Files) != 0 || len(preview.Rejections) != 0 {
				t.Fatalf("incomplete preview was promoted: %#v", preview)
			}
			if got := calls.Load(); got != testCase.wantCalls {
				t.Fatalf("manual-import requests = %d, want %d", got, testCase.wantCalls)
			}
		})
	}
}

func TestArrNativeLegacyPreviewAllowsCompleteMixedRows(t *testing.T) {
	for _, testCase := range []struct {
		name string
		body string
	}{
		{
			name: "legacy rejection alias",
			body: `[ {"id":901,"path":"/downloads/incoming/episode.mkv","relativePath":"episode.mkv","name":"episode.mkv","size":10,"series":{"id":201,"title":"Synthetic Series"},"episodes":[{"id":301,"seriesId":201,"seasonNumber":1,"episodeNumber":1}],"rejections":[]}, {"id":902,"path":"/downloads/incoming/other.mkv","relativePath":"other.mkv","name":"other.mkv","size":10,"series":{"id":201,"title":"Synthetic Series"},"episodes":[{"id":302,"seriesId":201,"seasonNumber":1,"episodeNumber":2}],"rejections":[{"code":"EpisodeFileExists","reason":"Legacy synthetic rejection"}]} ]`,
		},
		{
			name: "native rejection type",
			body: `[ {"id":901,"path":"/downloads/incoming/episode.mkv","relativePath":"episode.mkv","name":"episode.mkv","size":10,"series":{"id":201,"title":"Synthetic Series"},"episodes":[{"id":301,"seriesId":201,"seasonNumber":1,"episodeNumber":1}],"rejections":[]}, {"id":902,"path":"/downloads/incoming/other.mkv","relativePath":"other.mkv","name":"other.mkv","size":10,"series":{"id":201,"title":"Synthetic Series"},"episodes":[{"id":302,"seriesId":201,"seasonNumber":1,"episodeNumber":2}],"rejections":[{"type":"EpisodeFileExists","reason":"Native synthetic rejection"}]} ]`,
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			var calls atomic.Int32
			handler := http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
				if request.URL.Path != apiManualImport {
					response.WriteHeader(http.StatusNotFound)
					return
				}
				calls.Add(1)
				response.Header().Set("Content-Type", "application/json")
				_, _ = response.Write([]byte(testCase.body))
			})
			connectionID := domain.ConfigID("sonarr-legacy-preview-complete-" + strings.ReplaceAll(testCase.name, " ", "-"))
			client, server := newSyntheticClient(t, domain.ConnectionSonarr, connectionID, handler, 2, 10, 50, 50)
			defer server.Close()
			preview, err := client.PreviewImport(context.Background(), connectionID, ports.ImportPreviewRequest{
				RegisteredExternalID: "201",
				Transfer:             "copy",
				Files: []ports.ImportFile{{
					Source:           domain.FileTarget{RootID: "library", RelativePath: "incoming/episode.mkv"},
					MovieOrEpisodeID: "301",
				}},
			})
			if err != nil || len(preview.Files) != 1 || preview.Files[0].MovieOrEpisodeID != "301" || len(preview.Rejections) != 0 {
				t.Fatalf("complete mixed preview = %#v, %v", preview, err)
			}
			wantCalls := int32(1)
			if testCase.name == "legacy rejection alias" {
				wantCalls = 2
			}
			if got := calls.Load(); got != wantCalls {
				t.Fatalf("manual-import requests = %d, want %d", got, wantCalls)
			}
		})
	}
}

func TestArrNativeLegacyRadarrPreviewRequiresMovieIdentity(t *testing.T) {
	for index, movieValue := range []string{"{}", "null"} {
		var calls atomic.Int32
		body := []byte(fmt.Sprintf(`[{"id":701,"path":"/downloads/incoming/movie.mkv","relativePath":"movie.mkv","name":"movie.mkv","size":10,"movie":%s,"rejections":[]},{"id":702,"path":"/downloads/incoming/other.mkv","relativePath":"other.mkv","name":"other.mkv","size":10,"movie":{"id":101},"rejections":[{"code":"MovieFileExists","reason":"Legacy synthetic rejection"}]}]`, movieValue))
		handler := http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
			if request.URL.Path != apiManualImport {
				response.WriteHeader(http.StatusNotFound)
				return
			}
			calls.Add(1)
			response.Header().Set("Content-Type", "application/json")
			_, _ = response.Write(body)
		})
		connectionID := domain.ConfigID("radarr-legacy-incomplete-" + strconv.Itoa(index))
		client, server := newSyntheticClient(t, domain.ConnectionRadarr, connectionID, handler, 2, 10, 50, 50)
		defer server.Close()
		preview, err := client.PreviewImport(context.Background(), connectionID, ports.ImportPreviewRequest{
			RegisteredExternalID: "101",
			Transfer:             "copy",
			Files: []ports.ImportFile{{
				Source:           domain.FileTarget{RootID: "library", RelativePath: "incoming/movie.mkv"},
				MovieOrEpisodeID: "101",
			}},
		})
		assertUpstreamCode(t, err, domain.OutcomeUnknown)
		if len(preview.Files) != 0 || len(preview.Rejections) != 0 || calls.Load() != 1 {
			t.Fatalf("incomplete Radarr preview was promoted: preview=%#v calls=%d", preview, calls.Load())
		}
	}
}

func TestArrNativeInvalidUTF8DoesNotUseLegacyFallback(t *testing.T) {
	body := []byte(`[{"id":101,"title":"Synthetic `)
	body = append(body, 0xff)
	body = append(body, []byte(`","path":"/downloads/movies/Synthetic Film","monitored":true}]`)...)
	handler := http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path != apiMovies {
			response.WriteHeader(http.StatusNotFound)
			return
		}
		response.Header().Set("Content-Type", "application/json")
		_, _ = response.Write(body)
	})
	client, server := newSyntheticClient(t, domain.ConnectionRadarr, "radarr-native-invalid-utf8", handler, 2, 10, 50, 50)
	defer server.Close()
	page, err := client.List(context.Background(), "radarr-native-invalid-utf8", "", 2)
	assertUpstreamCode(t, err, domain.OutcomeUnknown)
	if len(page.Items) != 0 {
		t.Fatalf("invalid UTF-8 native catalog was promoted: %#v", page.Items)
	}
}

func TestArrNativeLegacyCatalogFallbackRequiresExplicitOldShape(t *testing.T) {
	handler := http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case apiMovies:
			response.Header().Set("Content-Type", "application/json")
			_, _ = response.Write([]byte(`[{"id":101,"title":"Legacy Synthetic Film","tmdbId":4242}]`))
		case apiMovieFiles:
			writeJSON(response, []any{})
		default:
			response.WriteHeader(http.StatusNotFound)
		}
	})
	client, server := newSyntheticClient(t, domain.ConnectionRadarr, "radarr-native-legacy-catalog", handler, 2, 10, 50, 50)
	defer server.Close()
	page, err := client.List(context.Background(), "radarr-native-legacy-catalog", "", 2)
	if err != nil || len(page.Items) != 1 || page.Items[0].ExternalID != "101" {
		t.Fatalf("explicit legacy catalog fallback = %#v, %v", page, err)
	}
}
