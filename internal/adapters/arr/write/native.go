package write

// This file is the root-side translation seam for the standalone Sonarr and
// Radarr modules. The nested clients own HTTP, authentication, bounds,
// generated DTO decoding and upstream error classes. Only their normalized
// observations cross into this package; registration-only fields that are not
// part of the read-only public client models use the narrow metadata bridge
// below and remain private to this adapter.

import (
	"context"
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
// without first marking the request context done.
type nativeContextErrors struct {
	base http.RoundTripper
	mu   sync.Mutex
	list []error
}

func (tracker *nativeContextErrors) RoundTrip(request *http.Request) (*http.Response, error) {
	base := tracker.base
	if base == nil {
		base = http.DefaultTransport
	}
	response, err := base.RoundTrip(request)
	if err != nil {
		tracker.record(err)
		return nil, err
	}
	if response != nil && response.Body != nil {
		response.Body = &nativeContextBody{ReadCloser: response.Body, tracker: tracker}
	}
	return response, nil
}

func (tracker *nativeContextErrors) record(err error) {
	if !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
		return
	}
	tracker.mu.Lock()
	tracker.list = append(tracker.list, err)
	tracker.mu.Unlock()
}

func (tracker *nativeContextErrors) take() error {
	tracker.mu.Lock()
	defer tracker.mu.Unlock()
	if len(tracker.list) == 0 {
		return nil
	}
	err := tracker.list[0]
	tracker.list = tracker.list[1:]
	return err
}

type nativeContextBody struct {
	io.ReadCloser
	tracker *nativeContextErrors
}

func (body *nativeContextBody) Read(destination []byte) (int, error) {
	count, err := body.ReadCloser.Read(destination)
	if err != nil {
		body.tracker.record(err)
	}
	return count, err
}

func (client *Client) listTitlesNative(ctx context.Context) ([]nativeTitle, error) {
	var titles []nativeTitle
	switch client.config.Kind {
	case domain.ConnectionRadarr:
		page, err := client.radarr.ListMovies(ctx)
		if err != nil {
			return nil, client.translateNativeWriteError(err, operationRegistration)
		}
		if page.Coverage.Completeness != radarrnative.CompletenessComplete || page.NextCursor != "" {
			return nil, malformed(operationRegistration, "Radarr title catalog coverage is incomplete")
		}
		titles = make([]nativeTitle, len(page.Items))
		for index, movie := range page.Items {
			titles[index] = nativeTitleFromRadarrMovie(movie)
		}
	case domain.ConnectionSonarr:
		page, err := client.sonarr.ListSeries(ctx)
		if err != nil {
			return nil, client.translateNativeWriteError(err, operationRegistration)
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
	switch client.config.Kind {
	case domain.ConnectionRadarr:
		movie, err := client.radarr.GetMovie(ctx, id)
		if err != nil {
			return nativeTitle{}, client.translateNativeWriteError(err, operationObserve)
		}
		if movie.ID != id {
			return nativeTitle{}, malformed(operationObserve, "Radarr title identity does not match the requested id")
		}
		return nativeTitleFromRadarrMovie(movie), nil
	case domain.ConnectionSonarr:
		series, err := client.sonarr.GetSeries(ctx, id)
		if err != nil {
			return nativeTitle{}, client.translateNativeWriteError(err, operationObserve)
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
	title.RootFolderPath = metadata.RootFolderPath
	title.QualityProfile = cloneInt64Pointer(metadata.QualityProfile)
	if metadata.Monitored != nil {
		title.Monitored = cloneBoolPointer(metadata.Monitored)
	}
	title.SeriesType = metadata.SeriesType
	title.SeasonFolder = cloneBoolPointer(metadata.SeasonFolder)
	title.Seasons = cloneNativeSeasons(metadata.Seasons)
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
	page, err := client.sonarr.ListEpisodesWithFiles(ctx, id)
	if err != nil {
		return nil, client.translateNativeWriteError(err, operationObserve)
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

func (client *Client) translateNativeWriteError(err error, operation string) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	if client != nil && client.contextErrors != nil {
		if contextErr := client.contextErrors.take(); contextErr != nil {
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
