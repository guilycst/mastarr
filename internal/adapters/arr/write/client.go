// Package write implements the explicit Radarr and Sonarr registration and
// manual-import boundary.
//
// Native Arr writes remain disabled while G-01 is open. The public
// constructor therefore exposes the read/reconcile surface and reports both
// mutation capabilities as unknown. The unexported constructor used by the
// package's synthetic tests exercises the typed protocol model without
// creating a production escape hatch for native writes.
package write

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/guilycst/mastarr/internal/domain"
	"github.com/guilycst/mastarr/internal/ports"
)

const (
	defaultMaxRecords  = 10_000
	defaultMaxFiles    = 512
	defaultMaxResponse = 16 << 20
	defaultReconcile   = 5 * time.Second
	maxText            = 512
)

const (
	operationRegistration = "arr.registration"
	operationImport       = "arr.manual-import"
	operationObserve      = "arr.manual-import.observe"
)

// OperationCapability is intentionally separate for registration and import.
// Evidence for one native write cannot enable another native write.
type OperationCapability struct {
	State    domain.CapabilityState
	Version  string
	Evidence []string
}

type capabilities struct {
	registration OperationCapability
	importOp     OperationCapability
}

// Config contains one Arr instance's endpoint, credential and path mapping.
// Native writes are not enabled by configuration in v0.0.1; New always
// installs the G-01 blocked capability set.
type Config struct {
	ConnectionID domain.ConfigID
	Kind         domain.ConnectionKind
	Endpoint     string
	APIKey       string
	HTTPClient   *http.Client
	// PreviewResolver returns the exact current native preview used to bind an
	// import. A command-capable test or a future root composition must provide
	// this callback; a non-empty caller token is never accepted as proof.
	PreviewResolver PreviewResolver
	RootPaths       map[domain.ConfigID]string
	Mappings        []domain.PathMapping

	MaxRecords       int
	MaxFiles         int
	MaxResponseSize  int64
	ReconcileTimeout time.Duration
}

// PreviewResolver supplies server-observed import evidence for one exact
// request. Implementations should call the read adapter's native preview and
// return its immutable revision, accepted selections and rejections.
//
// The callback is deliberately a root-owned seam rather than a generated Arr
// type, so upstream DTOs cannot escape this adapter boundary.
type PreviewResolver func(context.Context, domain.ConfigID, ports.ImportRequest) (ports.ImportPreview, error)

// Client implements the explicit Arr write port. It performs a read-before-
// write and a bounded read-back after any synthetic native dispatch.
type Client struct {
	config       Config
	endpoint     *url.URL
	http         *http.Client
	capabilities capabilities
	rootPaths    map[domain.ConfigID]string
	mappings     []pathMapping
	maxRecords   int
	maxFiles     int
	maxResponse  int64
	reconcile    time.Duration
}

var _ ports.MediaManagerWritePort = (*Client)(nil)
var _ ports.CapabilityPort = (*Client)(nil)

// New constructs an Arr write adapter with native mutations blocked by G-01.
// It still accepts read-only observations needed to identify already-satisfied
// registration/import state.
func New(config Config) (*Client, error) {
	return newClient(config, blockedCapabilities())
}

// newClient is deliberately unexported. Tests in this package use it with a
// synthetic, versioned protocol fixture to exercise request construction and
// reconciliation. No external package can enable native writes.
func newClient(config Config, caps capabilities) (*Client, error) {
	if !config.ConnectionID.Valid() {
		return nil, errors.New("Arr write connection id is invalid")
	}
	if config.Kind != domain.ConnectionRadarr && config.Kind != domain.ConnectionSonarr {
		return nil, errors.New("Arr write client requires radarr or sonarr kind")
	}
	endpoint, err := parseEndpoint(config.Endpoint)
	if err != nil {
		return nil, err
	}
	if config.MaxRecords <= 0 {
		config.MaxRecords = defaultMaxRecords
	}
	if config.MaxFiles <= 0 {
		config.MaxFiles = defaultMaxFiles
	}
	if config.MaxResponseSize <= 0 {
		config.MaxResponseSize = defaultMaxResponse
	}
	if config.ReconcileTimeout == 0 {
		config.ReconcileTimeout = defaultReconcile
	}
	if config.ReconcileTimeout < 0 {
		return nil, errors.New("Arr write reconcile timeout cannot be negative")
	}
	paths, err := validateRootPaths(config.RootPaths)
	if err != nil {
		return nil, err
	}
	mappings, err := normalizeMappings(config.ConnectionID, config.Mappings)
	if err != nil {
		return nil, err
	}
	base := http.DefaultClient
	if config.HTTPClient != nil {
		base = config.HTTPClient
	}
	copyClient := *base
	if copyClient.Timeout == 0 {
		copyClient.Timeout = 30 * time.Second
	}
	// A redirect can change an Arr POST into an unreviewed request at another
	// host or path. Return the response for status normalization instead.
	copyClient.CheckRedirect = func(_ *http.Request, _ []*http.Request) error {
		return http.ErrUseLastResponse
	}
	return &Client{
		config: config, endpoint: endpoint, http: &copyClient, capabilities: caps,
		rootPaths: paths, mappings: mappings, maxRecords: config.MaxRecords,
		maxFiles: config.MaxFiles, maxResponse: config.MaxResponseSize,
		reconcile: config.ReconcileTimeout,
	}, nil
}

func blockedCapabilities() capabilities {
	return capabilities{
		registration: OperationCapability{State: domain.CapabilityUnknown, Evidence: []string{"G-01", "native Arr writes disabled in v0.0.1"}},
		importOp:     OperationCapability{State: domain.CapabilityUnknown, Evidence: []string{"G-01", "CAP-ARR-NO-OVERWRITE"}},
	}
}

// Capabilities reports independent Arr mutation gates. The public client
// never claims a native write is supported while the no-overwrite race is
// unresolved.
func (client *Client) Capabilities(ctx context.Context, connectionID domain.ConfigID) ([]domain.Capability, error) {
	if err := client.scope(ctx, connectionID, operationObserve); err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	return []domain.Capability{
		client.capability(operationRegistration, client.capabilities.registration, now),
		client.capability(operationImport, client.capabilities.importOp, now),
	}, nil
}

func (client *Client) capability(name string, value OperationCapability, now time.Time) domain.Capability {
	state := value.State
	if state == "" {
		state = domain.CapabilityUnknown
	}
	reason := "native Arr write capability is not verified"
	if state == domain.CapabilityUnsupported {
		reason = "native Arr write capability is unsupported"
	}
	return domain.Capability{
		Name: name, State: state, Version: value.Version, Reason: reason,
		Evidence: append([]string(nil), value.Evidence...), ObservedAt: now,
	}
}

// Register performs an explicit field-level upsert. A matching desired title
// returns already_satisfied without a native write. A changed title is only
// dispatched when an independent synthetic capability gate is supported;
// production New leaves that gate unknown because G-01 is open.
func (client *Client) Register(ctx context.Context, connectionID domain.ConfigID, request ports.RegistrationRequest) (ports.RegistrationResult, error) {
	var result ports.RegistrationResult
	if err := client.scope(ctx, connectionID, operationRegistration); err != nil {
		return result, err
	}
	if err := validateRegistrationRequest(request, client.config.Kind); err != nil {
		return result, err
	}
	titles, err := client.listTitles(ctx)
	if err != nil {
		return result, err
	}
	provider := strings.TrimSpace(request.ProviderID)
	var matched *nativeTitle
	for index := range titles {
		if !nativeHasProviderID(titles[index], provider, client.config.Kind) {
			continue
		}
		if matched != nil {
			return result, upstreamFailure(domain.OutcomeUnknown, operationRegistration, "multiple titles match provider identity")
		}
		copyValue := titles[index]
		matched = &copyValue
	}
	if matched != nil {
		if registrationFieldsMatch(*matched, request.Fields, client.config.Kind) {
			result.ExternalID = strconv.FormatInt(matched.ID, 10)
			result.Record = client.titleRecord(*matched, provider)
			result.Effect = effect(operationRegistration, domain.OutcomeAlreadySatisfied, "title_read_back", "fields_match")
			return result, nil
		}
	}
	if err := client.writeAllowed(operationRegistration, client.capabilities.registration); err != nil {
		return result, err
	}
	payload, err := registrationPayload(request, client.config.Kind, matched)
	if err != nil {
		return result, err
	}
	var body []byte
	if matched == nil {
		body, err = client.postJSON(ctx, operationRegistration, catalogPath(client.config.Kind), payload)
	} else {
		body, err = client.putJSON(ctx, operationRegistration, catalogPath(client.config.Kind)+"/"+strconv.FormatInt(matched.ID, 10), payload)
	}
	if err != nil {
		return result, err
	}
	returned, returnedOK := decodeTitleBody(body)
	id := int64(0)
	if returnedOK {
		id = returned.ID
	}
	if id == 0 && matched != nil {
		id = matched.ID
	}
	if id == 0 {
		return result, malformed(operationRegistration, "native registration omitted title identity")
	}
	readback, err := client.readTitle(ctx, id)
	if err != nil {
		return result, err
	}
	readbackFields := request.Fields
	if matched == nil && readbackFields.Monitored == nil {
		monitored := false
		readbackFields.Monitored = &monitored
	}
	if !registrationFieldsMatch(readback, readbackFields, client.config.Kind) || !nativeHasProviderID(readback, provider, client.config.Kind) {
		return result, upstreamFailure(domain.OutcomeUnknown, operationRegistration, "registration read-back did not match requested fields")
	}
	result.ExternalID = strconv.FormatInt(readback.ID, 10)
	result.Record = client.titleRecord(readback, provider)
	result.Effect = effect(operationRegistration, domain.OutcomeApplied, "title_read_back", "explicit_fields_match", "no_search")
	return result, nil
}

// Import executes only a preview-bound ManualImport command. It observes the
// target first, reconciles command/history/file read-back after dispatch, and
// never retries an uncertain response. The public constructor rejects the
// dispatch at the G-01 capability gate.
func (client *Client) Import(ctx context.Context, connectionID domain.ConfigID, request ports.ImportRequest) (ports.ImportObservation, error) {
	var zero ports.ImportObservation
	if err := client.scope(ctx, connectionID, operationImport); err != nil {
		return zero, err
	}
	if err := validateImportRequest(request, client.config.Kind, client.maxFiles); err != nil {
		return zero, err
	}
	initial, _, initialErr := client.observeImport(ctx, request.RegisteredExternalID)
	if initialErr == nil && importMatches(initial.Files, request.Files, client) {
		initial.ExternalID = request.RegisteredExternalID
		effectValue := effect(operationImport, domain.OutcomeAlreadySatisfied, "file_read_back", "all_requested_associations_present")
		initial.Effect = &effectValue
		return initial, nil
	}
	if initialErr != nil {
		// A mutation cannot safely follow an untrusted or unavailable
		// observation. In particular, do not let a malformed preflight be
		// treated as an empty library and dispatch a command anyway.
		return zero, initialErr
	}
	// Keep the native Arr command completely unreachable while G-01 is open.
	// Synthetic package tests may inject an independently reviewed capability;
	// production callers only receive the blocked capability from New.
	if err := client.writeAllowed(operationImport, client.capabilities.importOp); err != nil {
		return zero, err
	}
	preview, err := client.resolvePreview(ctx, connectionID, request)
	if err != nil {
		return zero, err
	}
	if err := validatePreviewBinding(request, preview); err != nil {
		return zero, err
	}
	payload, expected, err := client.commandPayload(request)
	if err != nil {
		return zero, err
	}
	_, writeErr := client.postJSON(ctx, operationImport, "/api/v3/command", payload)
	// A command response can be lost after Arr has accepted the effect. Use a
	// fresh bounded context for reconciliation and never blind-resubmit.
	reconcileCtx, cancel := client.reconcileContext(ctx)
	defer cancel()
	observed, history, readErr := client.observeImport(reconcileCtx, request.RegisteredExternalID)
	if readErr == nil && importMatchesExpected(observed.Files, expected, client) {
		observed.ExternalID = request.RegisteredExternalID
		effectValue := effect(operationImport, domain.OutcomeApplied, "file_read_back", "command_reconciled")
		if writeErr != nil {
			effectValue.Evidence = append(effectValue.Evidence, "command_response_lost")
		}
		if history.Matched {
			effectValue.Evidence = append(effectValue.Evidence, "history_read_back")
		} else {
			effectValue.Evidence = append(effectValue.Evidence, "history_no_matching_event")
		}
		observed.Effect = &effectValue
		return observed, nil
	}
	if readErr != nil {
		return observed, unknownAfterWrite(operationImport, writeErr, readErr)
	}
	return observed, upstreamFailure(domain.OutcomeUnknown, operationImport, "import file associations are incomplete")
}

func (client *Client) resolvePreview(ctx context.Context, connectionID domain.ConfigID, request ports.ImportRequest) (ports.ImportPreview, error) {
	if client.config.PreviewResolver == nil {
		return ports.ImportPreview{}, upstreamFailure(domain.OutcomeUnsupported, operationImport, "exact Arr import preview evidence is unavailable")
	}
	preview, err := client.config.PreviewResolver(ctx, connectionID, request)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return ports.ImportPreview{}, sanitizeContextError(err)
		}
		return ports.ImportPreview{}, upstreamFailure(domain.OutcomeUnknown, operationImport, "Arr import preview could not be resolved")
	}
	return preview, nil
}

func validatePreviewBinding(request ports.ImportRequest, preview ports.ImportPreview) error {
	if strings.TrimSpace(request.PreviewRevision) == "" || preview.Revision == "" || preview.Revision != request.PreviewRevision {
		return upstreamFailure(domain.OutcomeConflict, operationImport, "import preview revision does not match the approved request")
	}
	if preview.ObservedAt.IsZero() {
		return upstreamFailure(domain.OutcomeUnknown, operationImport, "import preview observation time is missing")
	}
	if len(preview.Files) != len(request.Files) {
		return upstreamFailure(domain.OutcomeConflict, operationImport, "import preview selections do not match the approved request")
	}
	wanted := make(map[string]struct{}, len(request.Files))
	for _, file := range request.Files {
		if err := validateImportFile(file); err != nil {
			return err
		}
		key := importFileKey(file)
		if _, exists := wanted[key]; exists {
			return upstreamFailure(domain.OutcomeConflict, operationImport, "import request contains duplicate preview identity")
		}
		wanted[key] = struct{}{}
	}
	for _, file := range preview.Files {
		if err := validateImportFile(file); err != nil {
			return upstreamFailure(domain.OutcomeUnknown, operationImport, "import preview contains malformed file evidence")
		}
		key := importFileKey(file)
		if _, exists := wanted[key]; !exists {
			return upstreamFailure(domain.OutcomeConflict, operationImport, "import preview contains an unselected file")
		}
		delete(wanted, key)
	}
	if len(wanted) != 0 {
		return upstreamFailure(domain.OutcomeConflict, operationImport, "import preview omitted a selected file")
	}
	for _, rejection := range preview.Rejections {
		if err := rejection.Source.Validate(); err != nil || strings.TrimSpace(rejection.Code) == "" || strings.TrimSpace(rejection.Reason) == "" {
			return upstreamFailure(domain.OutcomeUnknown, operationImport, "import preview contains malformed rejection evidence")
		}
		for _, file := range request.Files {
			if rejection.Source == file.Source {
				return upstreamFailure(domain.OutcomeConflict, operationImport, "selected import file was rejected by native preview")
			}
		}
	}
	return nil
}

func validateImportFile(file ports.ImportFile) error {
	if err := file.Source.Validate(); err != nil {
		return invalidInput(operationImport, "import source is invalid")
	}
	if _, err := parsePositiveID(file.MovieOrEpisodeID); err != nil {
		return invalidInput(operationImport, "import media identity is invalid")
	}
	if len(file.Language) > maxText || !utf8.ValidString(file.Language) || !validSubtitleRole(file) {
		return invalidInput(operationImport, "import file role or language is invalid")
	}
	return nil
}

func importFileKey(file ports.ImportFile) string {
	return strings.Join([]string{
		file.Source.RootID.String(), file.Source.RelativePath, file.MovieOrEpisodeID,
		strconv.FormatBool(file.Subtitle), strings.TrimSpace(file.Language),
		strconv.FormatBool(file.Forced), strconv.FormatBool(file.HearingImpaired),
	}, "\x00")
}

func (client *Client) observeImport(ctx context.Context, externalID string) (ports.ImportObservation, importHistory, error) {
	var result ports.ImportObservation
	if err := ctx.Err(); err != nil {
		return result, importHistory{}, err
	}
	id, err := parsePositiveID(externalID)
	if err != nil {
		return result, importHistory{}, invalidInput(operationObserve, "registered external id is invalid")
	}
	var files []ports.MediaFile
	if client.config.Kind == domain.ConnectionRadarr {
		title, readErr := client.readTitle(ctx, id)
		if readErr != nil {
			return result, importHistory{}, readErr
		}
		if title.MovieFile != nil {
			if title.MovieFile.MovieID != 0 && title.MovieFile.MovieID != id {
				return result, importHistory{}, malformed(operationObserve, "movie file identity does not match requested movie")
			}
			file, mapErr := client.mapNativeFile(*title.MovieFile, strconv.FormatInt(id, 10), nil)
			if mapErr != nil {
				return result, importHistory{}, mapErr
			}
			files = append(files, file)
		}
	} else {
		body, status, readErr := client.request(ctx, operationObserve, http.MethodGet, "/api/v3/episode", url.Values{
			"seriesId": {strconv.FormatInt(id, 10)}, "includeEpisodeFile": {"true"},
		}, nil)
		if readErr != nil {
			return result, importHistory{}, readErr
		}
		if status < 200 || status >= 300 {
			return result, importHistory{}, normalizeStatus(operationObserve, status)
		}
		var episodes []nativeEpisode
		if err := decodeStrictJSON(body, &episodes); err != nil {
			return result, importHistory{}, malformed(operationObserve, "episode read-back is malformed")
		}
		byFile := make(map[int64]int)
		byPath := make(map[domain.FileTarget]int64)
		type episodeAssociation struct {
			fileID int64
			path   domain.FileTarget
		}
		byEpisode := make(map[int64]episodeAssociation)
		for _, episode := range episodes {
			if episode.ID <= 0 || episode.SeriesID != id {
				return result, importHistory{}, malformed(operationObserve, "episode identity does not match requested series")
			}
			if episode.EpisodeFile == nil {
				if episode.EpisodeFileID != 0 {
					return result, importHistory{}, malformed(operationObserve, "episode file identity is incomplete")
				}
				continue
			}
			if episode.EpisodeFile.ID <= 0 || episode.EpisodeFile.ID != episode.EpisodeFileID || strings.TrimSpace(episode.EpisodeFile.Path) == "" {
				return result, importHistory{}, malformed(operationObserve, "episode file identity is incomplete")
			}
			if episode.EpisodeFile.SeriesID <= 0 || episode.EpisodeFile.SeriesID != id {
				return result, importHistory{}, malformed(operationObserve, "episode file identity is missing or foreign")
			}
			mapped, mapErr := client.mapNativeFile(*episode.EpisodeFile, strconv.FormatInt(id, 10), nil)
			if mapErr != nil {
				return result, importHistory{}, mapErr
			}
			if previous, episodeExists := byEpisode[episode.ID]; episodeExists {
				if previous.fileID != episode.EpisodeFile.ID || previous.path != mapped.Path {
					return result, importHistory{}, malformed(operationObserve, "episode identity claims multiple files")
				}
			}
			position, exists := byFile[episode.EpisodeFile.ID]
			if !exists {
				if otherID, pathExists := byPath[mapped.Path]; pathExists && otherID != episode.EpisodeFile.ID {
					return result, importHistory{}, malformed(operationObserve, "distinct episode files claim the same mapped path")
				}
				mapped.EpisodeIDs = []string{strconv.FormatInt(episode.ID, 10)}
				files = append(files, mapped)
				byFile[episode.EpisodeFile.ID] = len(files) - 1
				byPath[mapped.Path] = episode.EpisodeFile.ID
				byEpisode[episode.ID] = episodeAssociation{fileID: episode.EpisodeFile.ID, path: mapped.Path}
				continue
			}
			if files[position].Path != mapped.Path || files[position].Size != mapped.Size {
				return result, importHistory{}, malformed(operationObserve, "one episode file id has contradictory path or size")
			}
			episodeID := strconv.FormatInt(episode.ID, 10)
			if containsString(files[position].EpisodeIDs, episodeID) {
				return result, importHistory{}, malformed(operationObserve, "episode file association is duplicated")
			}
			files[position].EpisodeIDs = append(files[position].EpisodeIDs, episodeID)
			byEpisode[episode.ID] = episodeAssociation{fileID: episode.EpisodeFile.ID, path: mapped.Path}
		}
	}
	history, historyErr := client.readHistory(ctx, id)
	if historyErr != nil {
		return result, importHistory{}, historyErr
	}
	result.ExternalID = externalID
	result.Files = files
	result.ObservedAt = time.Now().UTC()
	return result, history, nil
}

type importHistory struct {
	Matched bool
}

func (client *Client) readHistory(ctx context.Context, externalID int64) (importHistory, error) {
	body, status, err := client.request(ctx, operationObserve, http.MethodGet, "/api/v3/history", url.Values{
		"page": {"1"}, "pageSize": {"100"}, "eventType": {"downloadFolderImported"},
	}, nil)
	if err != nil {
		return importHistory{}, err
	}
	if status < 200 || status >= 300 {
		return importHistory{}, normalizeStatus(operationObserve, status)
	}
	records, err := decodeHistory(body)
	if err != nil {
		return importHistory{}, malformed(operationObserve, "history read-back is malformed")
	}
	for _, record := range records {
		if record.EventType != "downloadFolderImported" {
			continue
		}
		if client.config.Kind == domain.ConnectionRadarr && record.MovieID == externalID {
			return importHistory{Matched: true}, nil
		}
		if client.config.Kind == domain.ConnectionSonarr && record.SeriesID == externalID {
			return importHistory{Matched: true}, nil
		}
	}
	return importHistory{}, nil
}

type nativeHistory struct {
	ID          int64  `json:"id"`
	EventType   string `json:"eventType"`
	MovieID     int64  `json:"movieId"`
	SeriesID    int64  `json:"seriesId"`
	EpisodeID   int64  `json:"episodeId"`
	SourceTitle string `json:"sourceTitle"`
	DownloadID  string `json:"downloadId"`
}

func decodeHistory(body []byte) ([]nativeHistory, error) {
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 {
		return nil, errors.New("empty history response")
	}
	if trimmed[0] == '[' {
		var records []nativeHistory
		return records, decodeStrictJSON(trimmed, &records)
	}
	var envelope struct {
		Records []nativeHistory `json:"records"`
		Total   *int64          `json:"totalRecords"`
	}
	if err := decodeStrictJSON(trimmed, &envelope); err != nil {
		return nil, err
	}
	return envelope.Records, nil
}

type nativeTitle struct {
	ID             int64          `json:"id"`
	Title          string         `json:"title"`
	TMDBID         *int64         `json:"tmdbId"`
	TVDBID         *int64         `json:"tvdbId"`
	IMDBID         string         `json:"imdbId"`
	RootFolderPath string         `json:"rootFolderPath"`
	QualityProfile *int64         `json:"qualityProfileId"`
	Monitored      *bool          `json:"monitored"`
	SeriesType     string         `json:"seriesType"`
	SeasonFolder   *bool          `json:"seasonFolder"`
	Seasons        []nativeSeason `json:"seasons"`
	MovieFile      *nativeFile    `json:"movieFile"`
}

type nativeSeason struct {
	SeasonNumber int   `json:"seasonNumber"`
	Monitored    *bool `json:"monitored"`
}

type nativeEpisode struct {
	ID            int64       `json:"id"`
	SeriesID      int64       `json:"seriesId"`
	EpisodeFileID int64       `json:"episodeFileId"`
	EpisodeFile   *nativeFile `json:"episodeFile"`
}

type nativeFile struct {
	ID       int64  `json:"id"`
	Path     string `json:"path"`
	Size     int64  `json:"size"`
	MovieID  int64  `json:"movieId"`
	SeriesID int64  `json:"seriesId"`
}

func (client *Client) listTitles(ctx context.Context) ([]nativeTitle, error) {
	body, status, err := client.request(ctx, operationRegistration, http.MethodGet, catalogPath(client.config.Kind), nil, nil)
	if err != nil {
		return nil, err
	}
	if status < 200 || status >= 300 {
		return nil, normalizeStatus(operationRegistration, status)
	}
	var titles []nativeTitle
	if err := decodeStrictJSON(body, &titles); err != nil {
		return nil, malformed(operationRegistration, "title catalog is malformed")
	}
	if len(titles) > client.maxRecords {
		return nil, upstreamFailure(domain.OutcomeUnknown, operationRegistration, "title catalog exceeds configured bound")
	}
	for _, title := range titles {
		if title.ID <= 0 {
			return nil, malformed(operationRegistration, "title identity is missing")
		}
	}
	return titles, nil
}

func (client *Client) readTitle(ctx context.Context, id int64) (nativeTitle, error) {
	body, status, err := client.request(ctx, operationObserve, http.MethodGet, catalogPath(client.config.Kind)+"/"+strconv.FormatInt(id, 10), nil, nil)
	if err != nil {
		return nativeTitle{}, err
	}
	if status < 200 || status >= 300 {
		return nativeTitle{}, normalizeStatus(operationObserve, status)
	}
	var title nativeTitle
	if err := decodeStrictJSON(body, &title); err != nil || title.ID <= 0 || title.ID != id {
		return nativeTitle{}, malformed(operationObserve, "title read-back is malformed")
	}
	return title, nil
}

type registrationPayloadDTO struct {
	ID               int64          `json:"id,omitempty"`
	TMDBID           *int64         `json:"tmdbId,omitempty"`
	TVDBID           *int64         `json:"tvdbId,omitempty"`
	IMDBID           string         `json:"imdbId,omitempty"`
	RootFolderPath   string         `json:"rootFolderPath,omitempty"`
	QualityProfileID *int64         `json:"qualityProfileId,omitempty"`
	Monitored        *bool          `json:"monitored,omitempty"`
	SeasonFolder     *bool          `json:"seasonFolder,omitempty"`
	SeriesType       string         `json:"seriesType,omitempty"`
	Seasons          []nativeSeason `json:"seasons,omitempty"`
	AddOptions       *addOptions    `json:"addOptions,omitempty"`
}

type addOptions struct {
	Monitor                  string `json:"monitor,omitempty"`
	SearchForMissingEpisodes bool   `json:"searchForMissingEpisodes"`
	SearchForCutoffUnmet     bool   `json:"searchForCutoffUnmet"`
	SearchForMovie           bool   `json:"searchForMovie"`
}

func registrationPayload(request ports.RegistrationRequest, kind domain.ConnectionKind, existing *nativeTitle) (registrationPayloadDTO, error) {
	fields := request.Fields
	payload := registrationPayloadDTO{}
	if existing != nil {
		payload.ID = existing.ID
		payload.TMDBID = cloneInt64Pointer(existing.TMDBID)
		payload.TVDBID = cloneInt64Pointer(existing.TVDBID)
		payload.IMDBID = existing.IMDBID
		payload.RootFolderPath = existing.RootFolderPath
		payload.QualityProfileID = cloneInt64Pointer(existing.QualityProfile)
		payload.Monitored = cloneBoolPointer(existing.Monitored)
		payload.SeasonFolder = cloneBoolPointer(existing.SeasonFolder)
		payload.SeriesType = existing.SeriesType
		payload.Seasons = append([]nativeSeason(nil), existing.Seasons...)
	}
	provider := strings.TrimSpace(request.ProviderID)
	if number, parseErr := parsePositiveID(provider); parseErr == nil {
		if kind == domain.ConnectionRadarr {
			payload.TMDBID = &number
		} else {
			payload.TVDBID = &number
		}
	} else if strings.HasPrefix(strings.ToLower(provider), "tt") {
		payload.IMDBID = provider
	} else {
		return registrationPayloadDTO{}, invalidInput(operationRegistration, "provider identity must be a supported provider id")
	}
	if fields.RootFolder != "" {
		payload.RootFolderPath = strings.TrimSpace(fields.RootFolder)
	}
	if fields.QualityProfileID != "" {
		profile, err := parsePositiveID(fields.QualityProfileID)
		if err != nil {
			return registrationPayloadDTO{}, invalidInput(operationRegistration, "quality profile id is invalid")
		}
		payload.QualityProfileID = &profile
	}
	if fields.Monitored != nil {
		payload.Monitored = cloneBoolPointer(fields.Monitored)
	}
	if fields.SeasonFolder != nil {
		payload.SeasonFolder = cloneBoolPointer(fields.SeasonFolder)
	}
	if fields.SeriesType != "" {
		payload.SeriesType = strings.TrimSpace(fields.SeriesType)
	}
	if len(fields.Seasons) > 0 {
		seasons := make([]nativeSeason, 0, len(fields.Seasons))
		for _, value := range fields.Seasons {
			number, err := parsePositiveID(value)
			if err != nil {
				return registrationPayloadDTO{}, invalidInput(operationRegistration, "season id is invalid")
			}
			monitored := true
			seasons = append(seasons, nativeSeason{SeasonNumber: int(number), Monitored: &monitored})
		}
		payload.Seasons = seasons
	}
	if existing == nil {
		if payload.Monitored == nil {
			monitored := false
			payload.Monitored = &monitored
		}
		payload.AddOptions = &addOptions{Monitor: "none", SearchForMissingEpisodes: false, SearchForCutoffUnmet: false, SearchForMovie: false}
		if payload.RootFolderPath == "" || payload.QualityProfileID == nil {
			return registrationPayloadDTO{}, invalidInput(operationRegistration, "new registration requires root folder and quality profile")
		}
		if kind == domain.ConnectionSonarr && payload.SeriesType == "" {
			return registrationPayloadDTO{}, invalidInput(operationRegistration, "new Sonarr registration requires series type")
		}
	}
	return payload, nil
}

func registrationFieldsMatch(title nativeTitle, fields ports.RegistrationFields, kind domain.ConnectionKind) bool {
	if fields.RootFolder != "" && title.RootFolderPath != strings.TrimSpace(fields.RootFolder) {
		return false
	}
	if fields.QualityProfileID != "" {
		value, err := parsePositiveID(fields.QualityProfileID)
		if err != nil || title.QualityProfile == nil || *title.QualityProfile != value {
			return false
		}
	}
	if fields.Monitored != nil && (title.Monitored == nil || *title.Monitored != *fields.Monitored) {
		return false
	}
	if fields.SeasonFolder != nil && (title.SeasonFolder == nil || *title.SeasonFolder != *fields.SeasonFolder) {
		return false
	}
	if fields.SeriesType != "" && (kind != domain.ConnectionSonarr || title.SeriesType != strings.TrimSpace(fields.SeriesType)) {
		return false
	}
	if len(fields.Seasons) > 0 {
		want := make(map[int]struct{}, len(fields.Seasons))
		for _, value := range fields.Seasons {
			number, err := parsePositiveID(value)
			if err != nil {
				return false
			}
			want[int(number)] = struct{}{}
		}
		got := make(map[int]struct{}, len(title.Seasons))
		for _, season := range title.Seasons {
			if season.Monitored != nil && *season.Monitored {
				got[season.SeasonNumber] = struct{}{}
			}
		}
		if len(want) != len(got) {
			return false
		}
		for season := range want {
			if _, ok := got[season]; !ok {
				return false
			}
		}
	}
	return true
}

type manualImportCommand struct {
	Name       string             `json:"name"`
	ImportMode string             `json:"importMode"`
	Files      []manualImportFile `json:"files"`
}

type manualImportFile struct {
	Path            string  `json:"path"`
	MovieID         *int64  `json:"movieId,omitempty"`
	SeriesID        *int64  `json:"seriesId,omitempty"`
	EpisodeIDs      []int64 `json:"episodeIds,omitempty"`
	IsSubtitle      bool    `json:"isSubtitle,omitempty"`
	Language        string  `json:"language,omitempty"`
	Forced          bool    `json:"forced,omitempty"`
	HearingImpaired bool    `json:"hearingImpaired,omitempty"`
}

type expectedImport struct {
	Path           domain.FileTarget
	MovieOrEpisode string
	Subtitle       bool
}

func (client *Client) commandPayload(request ports.ImportRequest) (manualImportCommand, []expectedImport, error) {
	if request.Transfer != "copy" && request.Transfer != "move" {
		return manualImportCommand{}, nil, invalidInput(operationImport, "native Arr supports only copy or move import mode")
	}
	registered, err := parsePositiveID(request.RegisteredExternalID)
	if err != nil {
		return manualImportCommand{}, nil, invalidInput(operationImport, "registered external id is invalid")
	}
	type grouped struct {
		file     manualImportFile
		expected []expectedImport
	}
	groups := make(map[string]*grouped)
	order := make([]string, 0, len(request.Files))
	for _, item := range request.Files {
		remote, ok := client.absolutePath(item.Source)
		if !ok {
			return manualImportCommand{}, nil, invalidInput(operationImport, "source path is not mapped")
		}
		if !validSubtitleRole(item) {
			return manualImportCommand{}, nil, invalidInput(operationImport, "subtitle role does not match source path")
		}
		key := item.Source.RootID.String() + "\x00" + item.Source.RelativePath
		entry, exists := groups[key]
		if !exists {
			entry = &grouped{file: manualImportFile{Path: remote, Language: strings.TrimSpace(item.Language), IsSubtitle: item.Subtitle, Forced: item.Forced, HearingImpaired: item.HearingImpaired}}
			if client.config.Kind == domain.ConnectionRadarr {
				if item.MovieOrEpisodeID != request.RegisteredExternalID {
					return manualImportCommand{}, nil, invalidInput(operationImport, "movie association does not match registered title")
				}
				entry.file.MovieID = &registered
			} else {
				entry.file.SeriesID = &registered
			}
			groups[key] = entry
			order = append(order, key)
		}
		if entry.file.IsSubtitle != item.Subtitle || entry.file.Language != strings.TrimSpace(item.Language) || entry.file.Forced != item.Forced || entry.file.HearingImpaired != item.HearingImpaired {
			return manualImportCommand{}, nil, invalidInput(operationImport, "one source has contradictory import attributes")
		}
		if client.config.Kind == domain.ConnectionSonarr {
			episode, episodeErr := parsePositiveID(item.MovieOrEpisodeID)
			if episodeErr != nil || containsInt64(entry.file.EpisodeIDs, episode) {
				if episodeErr != nil {
					return manualImportCommand{}, nil, invalidInput(operationImport, "episode association is invalid")
				}
				return manualImportCommand{}, nil, invalidInput(operationImport, "duplicate episode association")
			}
			entry.file.EpisodeIDs = append(entry.file.EpisodeIDs, episode)
		}
		entry.expected = append(entry.expected, expectedImport{Path: item.Source, MovieOrEpisode: item.MovieOrEpisodeID, Subtitle: item.Subtitle})
	}
	files := make([]manualImportFile, 0, len(order))
	expected := make([]expectedImport, 0, len(request.Files))
	for _, key := range order {
		entry := groups[key]
		if client.config.Kind == domain.ConnectionSonarr && len(entry.file.EpisodeIDs) == 0 {
			return manualImportCommand{}, nil, invalidInput(operationImport, "episode association is missing")
		}
		files = append(files, entry.file)
		expected = append(expected, entry.expected...)
	}
	return manualImportCommand{Name: "ManualImport", ImportMode: request.Transfer, Files: files}, expected, nil
}

func importMatches(observed []ports.MediaFile, requested []ports.ImportFile, client *Client) bool {
	expected := make([]expectedImport, 0, len(requested))
	for _, file := range requested {
		expected = append(expected, expectedImport{Path: file.Source, MovieOrEpisode: file.MovieOrEpisodeID, Subtitle: file.Subtitle})
	}
	return importMatchesExpected(observed, expected, client)
}

func importMatchesExpected(observed []ports.MediaFile, expected []expectedImport, client *Client) bool {
	if len(expected) == 0 || len(observed) == 0 {
		return false
	}
	for _, want := range expected {
		found := false
		for _, got := range observed {
			if got.Path != want.Path || (want.Subtitle && got.MovieID != "" && got.MovieID != want.MovieOrEpisode) {
				continue
			}
			if client.config.Kind == domain.ConnectionRadarr {
				if got.MovieID != want.MovieOrEpisode {
					continue
				}
			} else if !containsString(got.EpisodeIDs, want.MovieOrEpisode) {
				continue
			}
			found = true
			break
		}
		if !found {
			return false
		}
	}
	return true
}

func (client *Client) mapNativeFile(file nativeFile, movieID string, episodeIDs []string) (ports.MediaFile, error) {
	if file.ID <= 0 || strings.TrimSpace(file.Path) == "" || file.Size < 0 {
		return ports.MediaFile{}, malformed(operationObserve, "native file identity is incomplete")
	}
	if client.config.Kind == domain.ConnectionRadarr {
		id, err := parsePositiveID(movieID)
		if err != nil || file.MovieID <= 0 || file.MovieID != id {
			return ports.MediaFile{}, malformed(operationObserve, "native movie file identity is missing or foreign")
		}
	}
	if client.config.Kind == domain.ConnectionSonarr {
		id, err := parsePositiveID(movieID)
		if err != nil || file.SeriesID <= 0 || file.SeriesID != id {
			return ports.MediaFile{}, malformed(operationObserve, "native episode file identity is missing or foreign")
		}
	}
	target, ok := client.remoteToTarget(file.Path)
	if !ok {
		return ports.MediaFile{}, upstreamFailure(domain.OutcomeUnknown, operationObserve, "native file path is not mapped")
	}
	result := ports.MediaFile{ExternalID: strconv.FormatInt(file.ID, 10), Path: target, Size: file.Size, EpisodeIDs: append([]string(nil), episodeIDs...)}
	if client.config.Kind == domain.ConnectionRadarr {
		result.MovieID = movieID
	}
	return result, nil
}

func (client *Client) titleRecord(title nativeTitle, provider string) ports.MediaRecord {
	result := ports.MediaRecord{ExternalID: strconv.FormatInt(title.ID, 10), ProviderID: provider, Title: title.Title, Monitored: boolValue(title.Monitored), Kind: domain.MediaMovie}
	if client.config.Kind == domain.ConnectionSonarr {
		result.Kind = domain.MediaEpisode
	}
	return result
}

func (client *Client) writeAllowed(operation string, capability OperationCapability) error {
	if capability.State != domain.CapabilitySupported || strings.TrimSpace(capability.Version) == "" || len(capability.Evidence) == 0 {
		return upstreamFailure(domain.OutcomeUnsupported, operation, "native Arr write capability is blocked while G-01 remains open")
	}
	return nil
}

func (client *Client) scope(ctx context.Context, connectionID domain.ConfigID, operation string) error {
	if ctx == nil {
		return invalidInput(operation, "context is nil")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if connectionID != client.config.ConnectionID {
		return invalidInput(operation, "connection scope is invalid")
	}
	return nil
}

func (client *Client) reconcileContext(parent context.Context) (context.Context, context.CancelFunc) {
	if parent == nil {
		parent = context.Background()
	}
	return context.WithTimeout(context.WithoutCancel(parent), client.reconcile)
}

func (client *Client) request(ctx context.Context, operation, method, endpoint string, query url.Values, body io.Reader) ([]byte, int, error) {
	if ctx == nil {
		return nil, 0, invalidInput(operation, "context is nil")
	}
	if err := ctx.Err(); err != nil {
		return nil, 0, err
	}
	target := *client.endpoint
	target.Path = strings.TrimRight(client.endpoint.Path, "/") + endpoint
	target.RawPath = ""
	target.RawQuery = ""
	if query != nil {
		target.RawQuery = query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, method, target.String(), body)
	if err != nil {
		return nil, 0, upstreamFailure(domain.OutcomeInvalidInput, operation, "Arr request could not be created")
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "mastarr-arr-write/0.0.1")
	if client.config.APIKey != "" {
		req.Header.Set("X-Api-Key", client.config.APIKey)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	response, err := client.http.Do(req)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, 0, ctxErr
		}
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil, 0, sanitizeContextError(err)
		}
		if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
			return nil, 0, domain.UpstreamError{Code: domain.OutcomeUnavailable, Retryable: true, Operation: operation, Detail: "upstream request timed out"}
		}
		return nil, 0, domain.UpstreamError{Code: domain.OutcomeUnavailable, Retryable: true, Operation: operation, Detail: "Arr upstream is unavailable"}
	}
	defer response.Body.Close()
	data, readErr := readBounded(response.Body, client.maxResponse)
	if readErr != nil {
		if errors.Is(readErr, context.Canceled) || errors.Is(readErr, context.DeadlineExceeded) {
			return nil, response.StatusCode, sanitizeContextError(readErr)
		}
		return nil, response.StatusCode, domain.UpstreamError{Code: domain.OutcomeUnknown, Status: response.StatusCode, Operation: operation, Detail: "upstream response exceeded configured bound"}
	}
	return data, response.StatusCode, nil
}

func (client *Client) postJSON(ctx context.Context, operation, endpoint string, payload any) ([]byte, error) {
	return client.mutateJSON(ctx, operation, http.MethodPost, endpoint, payload)
}

func (client *Client) putJSON(ctx context.Context, operation, endpoint string, payload any) ([]byte, error) {
	return client.mutateJSON(ctx, operation, http.MethodPut, endpoint, payload)
}

func (client *Client) mutateJSON(ctx context.Context, operation, method, endpoint string, payload any) ([]byte, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, invalidInput(operation, "native payload could not be encoded")
	}
	response, status, requestErr := client.request(ctx, operation, method, endpoint, nil, bytes.NewReader(body))
	if requestErr != nil {
		return nil, requestErr
	}
	if status < 200 || status >= 300 {
		return nil, normalizeStatus(operation, status)
	}
	return response, nil
}

func decodeTitleBody(body []byte) (nativeTitle, bool) {
	if len(bytes.TrimSpace(body)) == 0 {
		return nativeTitle{}, false
	}
	var title nativeTitle
	if decodeStrictJSON(body, &title) != nil || title.ID <= 0 {
		return nativeTitle{}, false
	}
	return title, true
}

func decodeStrictJSON(data []byte, target any) error {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || !utf8.Valid(trimmed) {
		return errors.New("invalid JSON response")
	}
	if err := validateJSONTokens(trimmed); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(trimmed))
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return errors.New("trailing JSON response data")
	}
	return nil
}

func validateJSONTokens(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var walk func() error
	walk = func() error {
		token, err := decoder.Token()
		if err != nil {
			return err
		}
		switch delimiter := token.(type) {
		case json.Delim:
			switch delimiter {
			case '{':
				seen := map[string]struct{}{}
				for decoder.More() {
					keyToken, keyErr := decoder.Token()
					if keyErr != nil {
						return keyErr
					}
					key, ok := keyToken.(string)
					if !ok {
						return errors.New("JSON object key is invalid")
					}
					if _, exists := seen[key]; exists {
						return errors.New("duplicate JSON object key")
					}
					seen[key] = struct{}{}
					if err := walk(); err != nil {
						return err
					}
				}
			case '[':
				for decoder.More() {
					if err := walk(); err != nil {
						return err
					}
				}
			default:
				return nil
			}
			_, err = decoder.Token()
			return err
		default:
			return nil
		}
	}
	if err := walk(); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return errors.New("trailing JSON response data")
	}
	return nil
}

func validateRegistrationRequest(request ports.RegistrationRequest, kind domain.ConnectionKind) error {
	if strings.TrimSpace(request.ProviderID) == "" || len(request.ProviderID) > maxText || strings.IndexFunc(request.ProviderID, func(r rune) bool { return r < 0x20 }) >= 0 {
		return invalidInput(operationRegistration, "provider identity is invalid")
	}
	if kind == domain.ConnectionRadarr && request.Kind != domain.MediaMovie {
		return invalidInput(operationRegistration, "Radarr registrations require movie kind")
	}
	if kind == domain.ConnectionSonarr && request.Kind != domain.MediaEpisode && request.Kind != domain.MediaSeason && request.Kind != domain.MediaAnime {
		return invalidInput(operationRegistration, "Sonarr registrations require episode, season or anime kind")
	}
	fields := request.Fields
	if len(fields.Seasons) > 0 {
		seen := make(map[string]struct{}, len(fields.Seasons))
		for _, season := range fields.Seasons {
			if _, exists := seen[season]; exists || strings.TrimSpace(season) == "" {
				return invalidInput(operationRegistration, "registration seasons contain duplicates or empty values")
			}
			seen[season] = struct{}{}
		}
	}
	if kind == domain.ConnectionRadarr && (fields.SeriesType != "" || fields.SeasonFolder != nil || len(fields.Seasons) > 0) {
		return invalidInput(operationRegistration, "series-only registration fields were supplied to Radarr")
	}
	if fields.RootFolder != "" && strings.TrimSpace(fields.RootFolder) != fields.RootFolder {
		return invalidInput(operationRegistration, "root folder has surrounding whitespace")
	}
	return nil
}

func validateImportRequest(request ports.ImportRequest, kind domain.ConnectionKind, maxFiles int) error {
	if _, err := parsePositiveID(request.RegisteredExternalID); err != nil {
		return invalidInput(operationImport, "registered external id is invalid")
	}
	if strings.TrimSpace(request.PreviewRevision) == "" || len(request.PreviewRevision) > maxText {
		return invalidInput(operationImport, "preview revision is required")
	}
	if request.Transfer != "copy" && request.Transfer != "move" {
		return invalidInput(operationImport, "transfer mode is invalid")
	}
	if len(request.Files) == 0 || len(request.Files) > maxFiles {
		return invalidInput(operationImport, "import file set is invalid")
	}
	seen := make(map[string]struct{}, len(request.Files))
	for index, file := range request.Files {
		if err := file.Source.Validate(); err != nil {
			return invalidInput(operationImport, fmt.Sprintf("file %d source is invalid", index))
		}
		if _, err := parsePositiveID(file.MovieOrEpisodeID); err != nil {
			return invalidInput(operationImport, fmt.Sprintf("file %d media identity is invalid", index))
		}
		key := file.Source.RootID.String() + "\x00" + file.Source.RelativePath + "\x00" + file.MovieOrEpisodeID
		if _, exists := seen[key]; exists {
			return invalidInput(operationImport, "duplicate exact import association")
		}
		seen[key] = struct{}{}
		if len(file.Language) > maxText || !utf8.ValidString(file.Language) {
			return invalidInput(operationImport, "subtitle language is invalid")
		}
		if !validSubtitleRole(file) {
			return invalidInput(operationImport, "subtitle role does not match source path")
		}
		if kind == domain.ConnectionRadarr && file.Subtitle && file.MovieOrEpisodeID == "" {
			return invalidInput(operationImport, "subtitle movie identity is missing")
		}
	}
	return nil
}

func validSubtitleRole(file ports.ImportFile) bool {
	ext := strings.ToLower(path.Ext(file.Source.RelativePath))
	subtitle := extIn(ext, ".srt", ".ass", ".ssa", ".vtt", ".sub", ".idx")
	return file.Subtitle == subtitle
}

func extIn(value string, values ...string) bool {
	for _, candidate := range values {
		if value == candidate {
			return true
		}
	}
	return false
}

func (client *Client) absolutePath(target domain.FileTarget) (string, bool) {
	if err := target.Validate(); err != nil {
		return "", false
	}
	root, ok := client.rootPaths[target.RootID]
	if !ok {
		return "", false
	}
	return path.Join(root, target.RelativePath), true
}

func (client *Client) remoteToTarget(remote string) (domain.FileTarget, bool) {
	remote = cleanAbsolute(remote)
	if remote == "" {
		return domain.FileTarget{}, false
	}
	best := -1
	var target domain.FileTarget
	for rootID, root := range client.rootPaths {
		root = cleanAbsolute(root)
		if root == "" || !pathBoundary(remote, root) || len(root) <= best {
			continue
		}
		relative := strings.TrimPrefix(remote, root)
		relative = strings.TrimPrefix(relative, "/")
		candidate := domain.FileTarget{RootID: rootID, RelativePath: relative}
		if candidate.Validate() != nil {
			continue
		}
		best, target = len(root), candidate
	}
	for _, mapping := range client.mappings {
		if !pathBoundary(remote, mapping.source) || len(mapping.source) <= best {
			continue
		}
		relative := strings.TrimPrefix(remote, mapping.source)
		relative = strings.TrimPrefix(relative, "/")
		mapped := mapping.destination
		if relative != "" {
			mapped = path.Join(mapped, relative)
		}
		candidate := domain.FileTarget{RootID: mapping.rootID, RelativePath: mapped}
		if candidate.Validate() != nil {
			continue
		}
		best, target = len(mapping.source), candidate
	}
	return target, best >= 0
}

type pathMapping struct {
	source      string
	rootID      domain.ConfigID
	destination string
}

func normalizeMappings(connectionID domain.ConfigID, mappings []domain.PathMapping) ([]pathMapping, error) {
	result := make([]pathMapping, 0, len(mappings))
	for _, mapping := range mappings {
		if mapping.ConnectionID != connectionID {
			continue
		}
		if err := mapping.Validate(); err != nil {
			return nil, errors.New("Arr write path mapping is invalid")
		}
		source := cleanAbsolute(mapping.SourcePrefix)
		if source == "" || strings.TrimSpace(mapping.DestinationPrefix) != mapping.DestinationPrefix || strings.HasPrefix(mapping.DestinationPrefix, "/") || strings.Contains(mapping.DestinationPrefix, "..") {
			return nil, errors.New("Arr write path mapping path is invalid")
		}
		result = append(result, pathMapping{source: source, rootID: mapping.RootID, destination: strings.Trim(mapping.DestinationPrefix, "/")})
	}
	return result, nil
}

func validateRootPaths(paths map[domain.ConfigID]string) (map[domain.ConfigID]string, error) {
	result := make(map[domain.ConfigID]string, len(paths))
	for rootID, value := range paths {
		if !rootID.Valid() || cleanAbsolute(value) == "" {
			return nil, errors.New("Arr write root path is invalid")
		}
		result[rootID] = cleanAbsolute(value)
	}
	return result, nil
}

func catalogPath(kind domain.ConnectionKind) string {
	if kind == domain.ConnectionRadarr {
		return "/api/v3/movie"
	}
	return "/api/v3/series"
}

func nativeHasProviderID(title nativeTitle, wanted string, kind domain.ConnectionKind) bool {
	wanted = strings.TrimSpace(wanted)
	if wanted == "" {
		return false
	}
	if kind == domain.ConnectionRadarr {
		if title.TMDBID != nil && strconv.FormatInt(*title.TMDBID, 10) == wanted {
			return true
		}
	} else {
		if title.TVDBID != nil && strconv.FormatInt(*title.TVDBID, 10) == wanted {
			return true
		}
	}
	return strings.TrimSpace(title.IMDBID) == wanted
}

func parsePositiveID(value string) (int64, error) {
	value = strings.TrimSpace(value)
	if value == "" || strings.ContainsAny(value, "+-.eE") {
		return 0, errors.New("id is not a positive decimal")
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil || parsed <= 0 {
		return 0, errors.New("id is not positive")
	}
	return parsed, nil
}

func cloneInt64Pointer(value *int64) *int64 {
	if value == nil {
		return nil
	}
	copyValue := *value
	return &copyValue
}

func cloneBoolPointer(value *bool) *bool {
	if value == nil {
		return nil
	}
	copyValue := *value
	return &copyValue
}

func boolValue(value *bool) bool {
	return value != nil && *value
}

func containsInt64(values []int64, wanted int64) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func containsString(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func cleanAbsolute(value string) string {
	value = strings.ReplaceAll(strings.TrimSpace(value), `\\`, "/")
	if value == "" || strings.ContainsRune(value, 0) || !strings.HasPrefix(value, "/") {
		return ""
	}
	cleaned := path.Clean(value)
	if cleaned == "." || strings.HasPrefix(cleaned, "/../") || cleaned == "/.." {
		return ""
	}
	return cleaned
}

func pathBoundary(value, prefix string) bool {
	return value == prefix || strings.HasPrefix(value, strings.TrimRight(prefix, "/")+"/")
}

func parseEndpoint(value string) (*url.URL, error) {
	if strings.TrimSpace(value) != value || value == "" {
		return nil, errors.New("Arr endpoint is invalid")
	}
	parsed, err := url.Parse(value)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, errors.New("Arr endpoint must be absolute and credential-free")
	}
	return parsed, nil
}

func readBounded(reader io.Reader, maxBytes int64) ([]byte, error) {
	if maxBytes <= 0 {
		return nil, errors.New("response bound is invalid")
	}
	data, err := io.ReadAll(io.LimitReader(reader, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maxBytes {
		return nil, errors.New("response exceeds configured bound")
	}
	return data, nil
}

func sanitizeContextError(err error) error {
	switch {
	case errors.Is(err, context.Canceled):
		return fmt.Errorf("Arr request canceled: %w", context.Canceled)
	case errors.Is(err, context.DeadlineExceeded):
		return fmt.Errorf("Arr request deadline exceeded: %w", context.DeadlineExceeded)
	default:
		return errors.New("Arr request context failed")
	}
}

func normalizeStatus(operation string, status int) error {
	code := domain.OutcomeUnknown
	retryable := false
	switch {
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		code = domain.OutcomeUnauthorized
	case status == http.StatusTooManyRequests:
		code, retryable = domain.OutcomeRateLimited, true
	case status == http.StatusBadRequest:
		code = domain.OutcomeInvalidInput
	case status == http.StatusConflict:
		code = domain.OutcomeConflict
	case status == http.StatusNotFound || status == http.StatusMethodNotAllowed || status == http.StatusNotImplemented:
		code = domain.OutcomeUnsupported
	case status == http.StatusRequestTimeout || status >= http.StatusInternalServerError:
		code, retryable = domain.OutcomeUnavailable, true
	}
	return domain.UpstreamError{Code: code, Status: status, Retryable: retryable, Operation: operation, Detail: "Arr upstream request failed"}
}

func invalidInput(operation, detail string) error {
	return upstreamFailure(domain.OutcomeInvalidInput, operation, detail)
}

func malformed(operation, detail string) error {
	return upstreamFailure(domain.OutcomeUnknown, operation, detail)
}

func upstreamFailure(code domain.UpstreamErrorCode, operation, detail string) error {
	return domain.UpstreamError{Code: code, Operation: operation, Detail: detail}
}

func unknownAfterWrite(operation string, writeErr, readErr error) error {
	detail := "native write outcome is unknown; read-back did not prove the desired state"
	if writeErr != nil && readErr != nil {
		detail = "native write and read-back both failed; no retry was attempted"
	}
	return domain.UpstreamError{Code: domain.OutcomeUnknown, Retryable: true, Operation: operation, Detail: detail}
}

func effect(operation string, outcome domain.EffectOutcome, evidence ...string) ports.ClientEffect {
	return ports.ClientEffect{OperationID: operation + ":" + strconv.FormatInt(time.Now().UnixNano(), 10), Outcome: outcome, ObservedAt: time.Now().UTC(), Evidence: append([]string(nil), evidence...)}
}
