package write

// This file is the root-side translation seam for the standalone Sonarr and
// Radarr modules. The nested clients own HTTP, authentication, bounds,
// generated DTO decoding and upstream error classes. Only their normalized
// observations cross into this package; registration-only fields that are not
// part of the read-only public client models use the narrow metadata bridge
// below and remain private to this adapter.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"sync"

	radarrnative "github.com/guilycst/mastarr/clients/radarr"
	sonarrnative "github.com/guilycst/mastarr/clients/sonarr"
	"github.com/guilycst/mastarr/internal/domain"
)

// The standalone clients intentionally return sanitized typed errors for
// transport and body failures. Keep context identity available to the root
// adapter as an out-of-band signal so errors.Is(Canceled/DeadlineExceeded)
// remains true even when a custom RoundTripper reports a wrapped context error
// without first marking the request context done. Attribution is carried by a
// request-local bucket in the context. A shared FIFO would let concurrent
// standalone reads consume one another's context errors.
type nativeContextKey struct{}

type nativeContextMarker struct {
	bucket *nativeContextBucket
}

type nativeContextBucket struct {
	mu   sync.Mutex
	list []error
}

func (bucket *nativeContextBucket) record(err error) {
	if !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
		return
	}
	bucket.mu.Lock()
	bucket.list = append(bucket.list, err)
	bucket.mu.Unlock()
}

func (bucket *nativeContextBucket) take() error {
	bucket.mu.Lock()
	defer bucket.mu.Unlock()
	if len(bucket.list) == 0 {
		return nil
	}
	err := bucket.list[0]
	bucket.list = bucket.list[1:]
	return err
}

type nativeContextTransport struct {
	base http.RoundTripper
}

func (transport *nativeContextTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	base := transport.base
	if base == nil {
		base = http.DefaultTransport
	}
	var bucket *nativeContextBucket
	if marker, ok := request.Context().Value(nativeContextKey{}).(*nativeContextMarker); ok && marker != nil {
		bucket = marker.bucket
	}
	response, err := base.RoundTrip(request)
	if err != nil {
		if bucket != nil {
			bucket.record(err)
		}
		return nil, err
	}
	if response != nil && response.Body != nil && bucket != nil {
		response.Body = &nativeContextBody{ReadCloser: response.Body, bucket: bucket}
	}
	return response, nil
}

type nativeContextBody struct {
	io.ReadCloser
	bucket *nativeContextBucket
}

func (body *nativeContextBody) Read(destination []byte) (int, error) {
	count, err := body.ReadCloser.Read(destination)
	if err != nil && body.bucket != nil {
		body.bucket.record(err)
	}
	return count, err
}

func (client *Client) nativeContext(ctx context.Context) (context.Context, *nativeContextBucket) {
	bucket := &nativeContextBucket{}
	return context.WithValue(ctx, nativeContextKey{}, &nativeContextMarker{bucket: bucket}), bucket
}

func (client *Client) listTitlesNative(ctx context.Context) ([]nativeTitle, error) {
	if ctx == nil {
		return nil, invalidInput(operationRegistration, "context is nil")
	}
	nativeCtx, contextBucket := client.nativeContext(ctx)
	var titles []nativeTitle
	switch client.config.Kind {
	case domain.ConnectionRadarr:
		page, err := client.radarr.ListMovies(nativeCtx)
		if err != nil {
			return nil, client.translateNativeWriteError(err, operationRegistration, contextBucket)
		}
		if page.Coverage.Completeness != radarrnative.CompletenessComplete || page.NextCursor != "" {
			return nil, malformed(operationRegistration, "Radarr title catalog coverage is incomplete")
		}
		titles = make([]nativeTitle, len(page.Items))
		for index, movie := range page.Items {
			titles[index] = nativeTitleFromRadarrMovie(movie)
		}
	case domain.ConnectionSonarr:
		page, err := client.sonarr.ListSeries(nativeCtx)
		if err != nil {
			return nil, client.translateNativeWriteError(err, operationRegistration, contextBucket)
		}
		if page.Coverage.Completeness != sonarrnative.CompletenessComplete || page.NextCursor != "" {
			return nil, malformed(operationRegistration, "Sonarr title catalog coverage is incomplete")
		}
		titles = make([]nativeTitle, len(page.Items))
		for index, series := range page.Items {
			titles[index] = nativeTitleFromSonarrSeries(series)
		}
	default:
		return nil, invalidInput(operationRegistration, "Arr title kind is invalid")
	}
	if len(titles) == 0 {
		return titles, nil
	}
	return client.mergeRegistrationMetadata(ctx, titles)
}

func (client *Client) readTitleNative(ctx context.Context, id int64) (nativeTitle, error) {
	if ctx == nil {
		return nativeTitle{}, invalidInput(operationObserve, "context is nil")
	}
	nativeCtx, contextBucket := client.nativeContext(ctx)
	switch client.config.Kind {
	case domain.ConnectionRadarr:
		movie, err := client.radarr.GetMovie(nativeCtx, id)
		if err != nil {
			return nativeTitle{}, client.translateNativeWriteError(err, operationObserve, contextBucket)
		}
		if movie.ID != id {
			return nativeTitle{}, malformed(operationObserve, "Radarr title identity does not match the requested id")
		}
		return nativeTitleFromRadarrMovie(movie), nil
	case domain.ConnectionSonarr:
		series, err := client.sonarr.GetSeries(nativeCtx, id)
		if err != nil {
			return nativeTitle{}, client.translateNativeWriteError(err, operationObserve, contextBucket)
		}
		if series.ID != id {
			return nativeTitle{}, malformed(operationObserve, "Sonarr title identity does not match the requested id")
		}
		return nativeTitleFromSonarrSeries(series), nil
	default:
		return nativeTitle{}, invalidInput(operationObserve, "Arr title kind is invalid")
	}
}

func (client *Client) readRegistrationTitle(ctx context.Context, id int64) (nativeTitle, error) {
	title, err := client.readTitleNative(ctx, id)
	if err != nil {
		return nativeTitle{}, err
	}
	metadata, err := client.readRegistrationMetadata(ctx, id)
	if err != nil {
		return nativeTitle{}, err
	}
	if err := mergeRegistrationMetadata(&title, metadata); err != nil {
		return nativeTitle{}, err
	}
	return title, nil
}

// nativeRegistrationMetadata is intentionally smaller than an Arr title DTO.
// Sonarr/Radarr's standalone read models omit fields needed by the existing
// explicit registration payload (rootFolderPath, qualityProfileId and, for
// Sonarr, seasons). The adapter reads those fields through this private,
// strict, identity-bound bridge after the standalone client has authenticated
// and validated the title response.
type nativeRegistrationMetadata struct {
	ID             int64          `json:"id"`
	RootFolderPath string         `json:"rootFolderPath"`
	QualityProfile *int64         `json:"qualityProfileId"`
	Monitored      *bool          `json:"monitored"`
	SeriesType     string         `json:"seriesType"`
	SeasonFolder   *bool          `json:"seasonFolder"`
	Seasons        []nativeSeason `json:"seasons"`

	rootFolderPathPresent bool
	qualityProfilePresent bool
	monitoredPresent      bool
	seriesTypePresent     bool
	seasonFolderPresent   bool
	seasonsPresent        bool
}

// UnmarshalJSON keeps field-presence information so an intentionally narrow
// metadata projection cannot erase values already validated by the typed
// standalone client merely because an optional member was omitted or null.
// Non-null contradictory shared settings are rejected by the merge below.
func (metadata *nativeRegistrationMetadata) UnmarshalJSON(data []byte) error {
	type plain nativeRegistrationMetadata
	var value plain
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	*metadata = nativeRegistrationMetadata(value)
	metadata.rootFolderPathPresent = nonNullJSONField(fields, "rootFolderPath")
	metadata.qualityProfilePresent = nonNullJSONField(fields, "qualityProfileId")
	metadata.monitoredPresent = nonNullJSONField(fields, "monitored")
	metadata.seriesTypePresent = nonNullJSONField(fields, "seriesType")
	metadata.seasonFolderPresent = nonNullJSONField(fields, "seasonFolder")
	metadata.seasonsPresent = nonNullJSONField(fields, "seasons")
	return nil
}

func nonNullJSONField(fields map[string]json.RawMessage, name string) bool {
	value, present := fields[name]
	return present && !bytes.Equal(bytes.TrimSpace(value), []byte("null"))
}

func (client *Client) mergeRegistrationMetadata(ctx context.Context, titles []nativeTitle) ([]nativeTitle, error) {
	metadata, err := client.listRegistrationMetadata(ctx)
	if err != nil {
		return nil, err
	}
	byID := make(map[int64]nativeRegistrationMetadata, len(metadata))
	for _, item := range metadata {
		if item.ID <= 0 {
			return nil, malformed(operationRegistration, "native title metadata identity is missing")
		}
		if _, exists := byID[item.ID]; exists {
			return nil, malformed(operationRegistration, "native title metadata identity is duplicated")
		}
		byID[item.ID] = item
	}
	if len(metadata) != len(titles) {
		return nil, malformed(operationRegistration, "native title metadata snapshot does not match catalog")
	}
	result := make([]nativeTitle, len(titles))
	for index := range titles {
		result[index] = titles[index]
		item, exists := byID[titles[index].ID]
		if !exists {
			return nil, malformed(operationRegistration, "native title metadata omitted catalog identity")
		}
		if err := mergeRegistrationMetadata(&result[index], item); err != nil {
			return nil, err
		}
	}
	return result, nil
}

func (client *Client) listRegistrationMetadata(ctx context.Context) ([]nativeRegistrationMetadata, error) {
	body, status, err := client.request(ctx, operationRegistration, http.MethodGet, catalogPath(client.config.Kind), nil, nil)
	if err != nil {
		return nil, err
	}
	if status < http.StatusOK || status >= http.StatusMultipleChoices {
		return nil, normalizeStatus(operationRegistration, status)
	}
	var metadata []nativeRegistrationMetadata
	if err := decodeStrictJSON(body, &metadata); err != nil {
		return nil, malformed(operationRegistration, "native title metadata is malformed")
	}
	if len(metadata) > client.maxRecords {
		return nil, upstreamFailure(domain.OutcomeUnknown, operationRegistration, "native title metadata exceeds configured bound")
	}
	return metadata, nil
}

func (client *Client) readRegistrationMetadata(ctx context.Context, id int64) (nativeRegistrationMetadata, error) {
	body, status, err := client.request(ctx, operationObserve, http.MethodGet, catalogPath(client.config.Kind)+"/"+strconv.FormatInt(id, 10), nil, nil)
	if err != nil {
		return nativeRegistrationMetadata{}, err
	}
	if status < http.StatusOK || status >= http.StatusMultipleChoices {
		return nativeRegistrationMetadata{}, normalizeStatus(operationObserve, status)
	}
	var metadata nativeRegistrationMetadata
	if err := decodeStrictJSON(body, &metadata); err != nil || metadata.ID <= 0 || metadata.ID != id {
		return nativeRegistrationMetadata{}, malformed(operationObserve, "native title metadata is malformed")
	}
	return metadata, nil
}

func mergeRegistrationMetadata(title *nativeTitle, metadata nativeRegistrationMetadata) error {
	if title == nil || title.ID <= 0 || metadata.ID != title.ID {
		return malformed(operationRegistration, "native title metadata identity does not match catalog identity")
	}
	if metadata.Monitored != nil && title.Monitored != nil && *metadata.Monitored != *title.Monitored {
		return malformed(operationRegistration, "native title monitored identity is contradictory")
	}
	if metadata.seriesTypePresent && title.SeriesType != "" && metadata.SeriesType != title.SeriesType {
		return malformed(operationRegistration, "native title series type evidence is contradictory")
	}
	if metadata.seasonFolderPresent && metadata.SeasonFolder != nil && title.SeasonFolder != nil && *metadata.SeasonFolder != *title.SeasonFolder {
		return malformed(operationRegistration, "native title season folder evidence is contradictory")
	}
	if metadata.rootFolderPathPresent || metadata.RootFolderPath != "" {
		title.RootFolderPath = metadata.RootFolderPath
	}
	if metadata.qualityProfilePresent || metadata.QualityProfile != nil {
		title.QualityProfile = cloneInt64Pointer(metadata.QualityProfile)
	}
	if metadata.monitoredPresent || metadata.Monitored != nil {
		title.Monitored = cloneBoolPointer(metadata.Monitored)
	}
	if metadata.seriesTypePresent || metadata.SeriesType != "" {
		title.SeriesType = metadata.SeriesType
	}
	if metadata.seasonFolderPresent || metadata.SeasonFolder != nil {
		title.SeasonFolder = cloneBoolPointer(metadata.SeasonFolder)
	}
	if metadata.seasonsPresent || len(metadata.Seasons) > 0 {
		title.Seasons = cloneNativeSeasons(metadata.Seasons)
	}
	return nil
}

func cloneNativeSeasons(values []nativeSeason) []nativeSeason {
	if len(values) == 0 {
		return nil
	}
	result := make([]nativeSeason, len(values))
	for index, value := range values {
		result[index] = nativeSeason{SeasonNumber: value.SeasonNumber, Monitored: cloneBoolPointer(value.Monitored)}
	}
	return result
}

func (client *Client) observeSonarrEpisodes(ctx context.Context, id int64) ([]nativeEpisode, error) {
	if ctx == nil {
		return nil, invalidInput(operationObserve, "context is nil")
	}
	nativeCtx, contextBucket := client.nativeContext(ctx)
	page, err := client.sonarr.ListEpisodesWithFiles(nativeCtx, id)
	if err != nil {
		return nil, client.translateNativeWriteError(err, operationObserve, contextBucket)
	}
	if page.Coverage.Completeness != sonarrnative.CompletenessComplete || page.NextCursor != "" {
		return nil, malformed(operationObserve, "Sonarr episode coverage is incomplete")
	}
	result := make([]nativeEpisode, len(page.Items))
	for index, episode := range page.Items {
		result[index] = nativeEpisodeFromSonarrEpisode(episode)
	}
	return result, nil
}

func nativeTitleFromRadarrMovie(movie radarrnative.Movie) nativeTitle {
	result := nativeTitle{
		ID:          movie.ID,
		Title:       movie.Title,
		Path:        movie.Path,
		Monitored:   boolPointer(movie.Monitored),
		IMDBID:      movie.IMDBID,
		ProviderIDs: cloneProviderIDs(movie.ProviderIDs),
	}
	if movie.TMDBID != nil {
		value := *movie.TMDBID
		result.TMDBID = &value
	}
	if movie.MovieFile != nil {
		file := nativeFileFromRadarrMovieFile(*movie.MovieFile)
		result.MovieFile = &file
	}
	return result
}

func nativeTitleFromSonarrSeries(series sonarrnative.Series) nativeTitle {
	result := nativeTitle{
		ID:          series.ID,
		Title:       series.Title,
		Path:        series.Path,
		Monitored:   boolPointer(series.Monitored),
		SeriesType:  series.SeriesType,
		IMDBID:      series.IMDBID,
		ProviderIDs: cloneProviderIDs(series.ProviderIDs),
	}
	if series.TVDBID != nil {
		value := *series.TVDBID
		result.TVDBID = &value
	}
	if series.SeasonFolder != nil {
		value := *series.SeasonFolder
		result.SeasonFolder = &value
	}
	return result
}

func nativeFileFromRadarrMovieFile(file radarrnative.MovieFile) nativeFile {
	return nativeFile{ID: file.ID, Path: file.Path, Size: file.Size, MovieID: file.MovieID}
}

func nativeFileFromSonarrEpisodeFile(file sonarrnative.EpisodeFile) nativeFile {
	return nativeFile{ID: file.ID, Path: file.Path, Size: file.Size, SeriesID: file.SeriesID}
}

func nativeEpisodeFromSonarrEpisode(episode sonarrnative.Episode) nativeEpisode {
	result := nativeEpisode{
		ID:            episode.ID,
		SeriesID:      episode.SeriesID,
		SeasonNumber:  episode.SeasonNumber,
		EpisodeNumber: episode.EpisodeNumber,
		HasFile:       episode.HasFile,
	}
	if episode.EpisodeFileID != nil {
		result.EpisodeFileID = *episode.EpisodeFileID
	}
	if episode.EpisodeFile != nil {
		file := nativeFileFromSonarrEpisodeFile(*episode.EpisodeFile)
		result.EpisodeFile = &file
	}
	return result
}

func boolPointer(value bool) *bool { return &value }

func cloneProviderIDs(values map[string]string) map[string]string {
	if len(values) == 0 {
		return nil
	}
	result := make(map[string]string, len(values))
	for key, value := range values {
		result[key] = value
	}
	return result
}

func (client *Client) translateNativeWriteError(err error, operation string, contextBucket *nativeContextBucket) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	if contextBucket != nil {
		if contextErr := contextBucket.take(); contextErr != nil {
			return sanitizeContextError(contextErr)
		}
	}
	var sonarrErr sonarrnative.UpstreamError
	if errors.As(err, &sonarrErr) {
		code := translateSonarrWriteCode(sonarrErr.Code)
		if sonarrErr.Status >= http.StatusMultipleChoices && sonarrErr.Status < 400 {
			code = domain.OutcomeUnknown
		}
		return domain.UpstreamError{Code: code, Status: sonarrErr.Status, Retryable: sonarrErr.Retryable, Operation: operation, Detail: "Sonarr upstream request failed"}
	}
	var radarrErr radarrnative.UpstreamError
	if errors.As(err, &radarrErr) {
		code := translateRadarrWriteCode(radarrErr.Code)
		if radarrErr.Status >= http.StatusMultipleChoices && radarrErr.Status < 400 {
			code = domain.OutcomeUnknown
		}
		return domain.UpstreamError{Code: code, Status: radarrErr.Status, Retryable: radarrErr.Retryable, Operation: operation, Detail: "Radarr upstream request failed"}
	}
	return domain.UpstreamError{Code: domain.OutcomeUnavailable, Retryable: true, Operation: operation, Detail: "Arr upstream is unavailable"}
}

func translateSonarrWriteCode(code sonarrnative.ErrorCode) domain.UpstreamErrorCode {
	switch code {
	case sonarrnative.ErrorUnavailable:
		return domain.OutcomeUnavailable
	case sonarrnative.ErrorRateLimited:
		return domain.OutcomeRateLimited
	case sonarrnative.ErrorUnauthorized, sonarrnative.ErrorForbidden:
		return domain.OutcomeUnauthorized
	case sonarrnative.ErrorInvalidInput:
		return domain.OutcomeInvalidInput
	case sonarrnative.ErrorConflict:
		return domain.OutcomeConflict
	case sonarrnative.ErrorUnsupported, sonarrnative.ErrorNotFound:
		return domain.OutcomeUnsupported
	default:
		return domain.OutcomeUnknown
	}
}

func translateRadarrWriteCode(code radarrnative.ErrorCode) domain.UpstreamErrorCode {
	switch code {
	case radarrnative.ErrorUnavailable:
		return domain.OutcomeUnavailable
	case radarrnative.ErrorRateLimited:
		return domain.OutcomeRateLimited
	case radarrnative.ErrorUnauthorized, radarrnative.ErrorForbidden:
		return domain.OutcomeUnauthorized
	case radarrnative.ErrorInvalidInput:
		return domain.OutcomeInvalidInput
	case radarrnative.ErrorConflict:
		return domain.OutcomeConflict
	case radarrnative.ErrorUnsupported, radarrnative.ErrorNotFound:
		return domain.OutcomeUnsupported
	default:
		return domain.OutcomeUnknown
	}
}
