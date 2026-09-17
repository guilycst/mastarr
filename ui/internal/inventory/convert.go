package inventory

import (
	"bytes"
	"encoding/json"
	"errors"
	"time"
	"unicode/utf8"

	generated "github.com/guilycst/mastarr/ui/internal/api/generated"
)

func convertDiscoveryPage(source generated.DiscoveryList, body []byte) (DiscoveryPage, error) {
	if source.Items == nil || len(source.Items) > MaxItemsPerPage {
		return DiscoveryPage{}, validationError("discovery items are missing or too large")
	}
	if err := requireListPageFields(body); err != nil {
		return DiscoveryPage{}, protocolError(err)
	}
	page, err := convertPage(source.Page)
	if err != nil {
		return DiscoveryPage{}, err
	}
	items := make([]Discovery, 0, len(source.Items))
	for _, item := range source.Items {
		converted, convertErr := convertDiscovery(item)
		if convertErr != nil {
			return DiscoveryPage{}, convertErr
		}
		items = append(items, converted)
	}
	return DiscoveryPage{Items: items, Page: page}, nil
}

func convertDiscovery(source generated.Discovery) (Discovery, error) {
	if !validIdentity(idString(source.Id)) || source.Files == nil || source.Readiness == "" || source.ObservedAt.IsZero() || len(source.Files) > MaxFilesPerDiscovery {
		return Discovery{}, validationError("discovery required evidence is missing")
	}
	files := make([]File, 0, len(source.Files))
	for _, file := range source.Files {
		converted, err := convertFile(file)
		if err != nil {
			return Discovery{}, err
		}
		files = append(files, converted)
	}
	provenance := make([]Provenance, 0)
	if source.Provenance != nil {
		if len(*source.Provenance) > MaxProvenance {
			return Discovery{}, validationError("discovery provenance is too large")
		}
		provenance = make([]Provenance, 0, len(*source.Provenance))
		for _, item := range *source.Provenance {
			provenance = append(provenance, convertProvenance(item))
		}
	}
	candidates := make([]Candidate, 0)
	if source.Candidates != nil {
		if len(*source.Candidates) > MaxCandidates {
			return Discovery{}, validationError("discovery candidates are too large")
		}
		candidates = make([]Candidate, 0, len(*source.Candidates))
		for _, item := range *source.Candidates {
			candidates = append(candidates, convertCandidate(item))
		}
	}
	var coverage *Coverage
	if source.Coverage != nil {
		converted, err := convertCoverage(*source.Coverage)
		if err != nil {
			return Discovery{}, err
		}
		coverage = &converted
	}
	return Discovery{
		ID:         idString(source.Id),
		ObservedAt: source.ObservedAt,
		Readiness:  string(source.Readiness),
		Files:      files,
		Provenance: provenance,
		Candidates: candidates,
		Coverage:   coverage,
	}, nil
}

func convertFile(source generated.FileManifestEntry) (File, error) {
	if !validRelativePath(source.RelativePath) || source.RootId == "" || source.Type == "" || source.Size < 0 || !validIdentity(source.RootId) {
		return File{}, validationError("file manifest entry is incomplete")
	}
	return File{
		RelativePath: source.RelativePath,
		RootID:       source.RootId,
		Type:         string(source.Type),
		Role:         optionalRole(source.Role),
		Size:         source.Size,
		Digest:       optionalString(source.Digest),
		FileIdentity: optionalString(source.FileIdentity),
		ObservedAt:   copyTime(source.ObservedAt),
	}, nil
}

func optionalRole(value *generated.FileManifestEntryRole) string {
	if value == nil {
		return ""
	}
	return string(*value)
}

func convertProvenance(source generated.Provenance) Provenance {
	return Provenance{
		ConnectionID: optionalString(source.ConnectionId),
		ClientItemID: optionalString(source.ClientItemId),
		DescriptorID: optionalID(source.DescriptorId),
		Hash:         optionalString(source.Hash),
		SourcePath:   optionalTarget(source.SourcePath),
		CompletedAt:  copyTime(source.CompletedAt),
	}
}

func convertCandidate(source generated.MetadataCandidate) Candidate {
	var season *int
	if source.Season != nil {
		value := *source.Season
		season = &value
	}
	var year *int
	if source.Year != nil {
		value := *source.Year
		year = &value
	}
	var score *float32
	if source.Score != nil {
		value := *source.Score
		score = &value
	}
	episodes := append([]int(nil), optionalInts(source.Episodes)...)
	return Candidate{
		Title:      source.Title,
		Kind:       string(source.Kind),
		ProviderID: optionalString(source.ProviderId),
		ExternalID: optionalString(source.ExternalId),
		Season:     season,
		Episodes:   episodes,
		Year:       year,
		Score:      score,
	}
}

func convertMediaPage(source generated.MediaList, body []byte) (MediaPage, error) {
	if source.Items == nil || len(source.Items) > MaxItemsPerPage {
		return MediaPage{}, validationError("media items are missing or too large")
	}
	if err := requireListPageFields(body); err != nil {
		return MediaPage{}, protocolError(err)
	}
	page, err := convertPage(source.Page)
	if err != nil {
		return MediaPage{}, err
	}
	items := make([]Media, 0, len(source.Items))
	for _, item := range source.Items {
		converted, convertErr := convertMedia(item)
		if convertErr != nil {
			return MediaPage{}, convertErr
		}
		items = append(items, converted)
	}
	return MediaPage{Items: items, Page: page}, nil
}

func convertMedia(source generated.Media) (Media, error) {
	if !validIdentity(idString(source.Id)) || source.Kind == "" || source.Tracking == nil || source.ObservedAt.IsZero() || len(source.Tracking) > MaxTracking {
		return Media{}, validationError("media required evidence is missing")
	}
	tracking := make([]Tracking, 0, len(source.Tracking))
	for _, item := range source.Tracking {
		converted, err := convertTracking(item)
		if err != nil {
			return Media{}, err
		}
		tracking = append(tracking, converted)
	}
	discoveryIDs := append([]string(nil), optionalIDs(source.DiscoveryIds)...)
	return Media{
		ID:           idString(source.Id),
		Kind:         string(source.Kind),
		ProviderID:   source.ProviderId,
		Title:        optionalString(source.Title),
		ObservedAt:   source.ObservedAt,
		DiscoveryIDs: discoveryIDs,
		Tracking:     tracking,
	}, nil
}

func convertTracking(source generated.TrackingObservation) (Tracking, error) {
	if !validIdentity(source.ConnectionId) || source.Dimension == "" || source.Value == "" || source.ObservedAt.IsZero() || len(optionalStrings(source.Evidence)) > MaxTracking {
		return Tracking{}, validationError("tracking observation is incomplete")
	}
	return Tracking{
		ConnectionID: source.ConnectionId,
		Dimension:    string(source.Dimension),
		Value:        string(source.Value),
		ProviderID:   optionalString(source.ProviderId),
		ExternalID:   optionalString(source.ExternalId),
		ObservedAt:   source.ObservedAt,
		CoverageID:   optionalID(source.CoverageId),
		Evidence:     append([]string(nil), optionalStrings(source.Evidence)...),
	}, nil
}

func convertDownloadPage(source generated.DownloadList, body []byte) (DownloadPage, error) {
	if source.Items == nil || len(source.Items) > MaxItemsPerPage {
		return DownloadPage{}, validationError("download items are missing or too large")
	}
	if err := requireListPageFields(body); err != nil {
		return DownloadPage{}, protocolError(err)
	}
	page, err := convertPage(source.Page)
	if err != nil {
		return DownloadPage{}, err
	}
	items := make([]Download, 0, len(source.Items))
	for _, item := range source.Items {
		converted, convertErr := convertDownload(item)
		if convertErr != nil {
			return DownloadPage{}, convertErr
		}
		items = append(items, converted)
	}
	return DownloadPage{Items: items, Page: page}, nil
}

func convertDownload(source generated.Download) (Download, error) {
	if !validIdentity(idString(source.Id)) || !validIdentity(source.ConnectionId) || source.State == "" || source.ObservedAt.IsZero() {
		return Download{}, validationError("download required evidence is missing")
	}
	var coverage *Coverage
	if source.Coverage != nil {
		converted, err := convertCoverage(*source.Coverage)
		if err != nil {
			return Download{}, err
		}
		coverage = &converted
	}
	return Download{
		ID:           idString(source.Id),
		ConnectionID: source.ConnectionId,
		ClientItemID: optionalString(source.ClientItemId),
		Hash:         optionalString(source.Hash),
		NzbID:        optionalString(source.NzbId),
		DeprecatedID: optionalString(source.DeprecatedId),
		DescriptorID: optionalID(source.DescriptorId),
		SourcePath:   optionalTarget(source.SourcePath),
		State:        string(source.State),
		ObservedAt:   source.ObservedAt,
		CompletedAt:  copyTime(source.CompletedAt),
		Coverage:     coverage,
	}, nil
}

func convertDescriptorPage(source generated.DescriptorList, body []byte) (DescriptorPage, error) {
	if source.Items == nil || len(source.Items) > MaxItemsPerPage {
		return DescriptorPage{}, validationError("descriptor items are missing or too large")
	}
	if err := requireListPageFields(body); err != nil {
		return DescriptorPage{}, protocolError(err)
	}
	page, err := convertPage(source.Page)
	if err != nil {
		return DescriptorPage{}, err
	}
	items := make([]Descriptor, 0, len(source.Items))
	for _, item := range source.Items {
		converted, convertErr := convertDescriptor(item)
		if convertErr != nil {
			return DescriptorPage{}, convertErr
		}
		items = append(items, converted)
	}
	return DescriptorPage{Items: items, Page: page}, nil
}

func convertDescriptor(source generated.Descriptor) (Descriptor, error) {
	if !validIdentity(idString(source.Id)) || source.Type == "" || source.Availability == "" || source.Size < 0 || source.CapturedAt.IsZero() {
		return Descriptor{}, validationError("descriptor required evidence is missing")
	}
	return Descriptor{
		ID:             idString(source.Id),
		Type:           string(source.Type),
		Size:           source.Size,
		Digest:         source.Digest,
		Availability:   string(source.Availability),
		Source:         optionalString(source.Source),
		CapturedAt:     source.CapturedAt,
		RetentionUntil: copyTime(source.RetentionUntil),
	}, nil
}

func convertPage(source generated.Page) (PageInfo, error) {
	if source.Coverage == nil || source.ObservedAt.IsZero() {
		return PageInfo{}, validationError("page coverage or observation time is missing")
	}
	if len(source.Coverage) > MaxTracking {
		return PageInfo{}, validationError("page coverage is too large")
	}
	coverage := make([]Coverage, 0, len(source.Coverage))
	for _, item := range source.Coverage {
		converted, err := convertCoverage(item)
		if err != nil {
			return PageInfo{}, err
		}
		coverage = append(coverage, converted)
	}
	var nextCursor *string
	if source.NextCursor != nil {
		value := *source.NextCursor
		if value == "" || len(value) > MaxCursorLength || !utf8.ValidString(value) {
			return PageInfo{}, validationError("page cursor is invalid")
		}
		nextCursor = &value
	}
	return PageInfo{NextCursor: nextCursor, ObservedAt: source.ObservedAt, Coverage: coverage}, nil
}

func convertCoverage(source generated.Coverage) (Coverage, error) {
	if source.Completeness == "" || source.ObservedAt.IsZero() {
		return Coverage{}, validationError("coverage evidence is incomplete")
	}
	return Coverage{
		Completeness:     string(source.Completeness),
		ConnectionID:     optionalString(source.ConnectionId),
		RootID:           optionalString(source.RootId),
		SourceID:         optionalID(source.SourceId),
		SnapshotRevision: optionalString(source.SnapshotRevision),
		ObservedAt:       source.ObservedAt,
		ObservedCount:    copyInt(source.ObservedCount),
		ReasonCodes:      append([]string(nil), optionalStrings(source.ReasonCodes)...),
	}, nil
}

func optionalString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func optionalID(value *generated.Id) string {
	if value == nil {
		return ""
	}
	return idString(*value)
}

func idString(value generated.Id) string {
	return value.String()
}

func optionalTarget(value *generated.FileTarget) string {
	if value == nil {
		return ""
	}
	if value.RootId == "" || value.RelativePath == "" {
		return ""
	}
	return value.RootId + ":" + value.RelativePath
}

func optionalInts(value *[]int) []int {
	if value == nil {
		return nil
	}
	return *value
}

func optionalIDs(value *[]generated.Id) []string {
	if value == nil {
		return nil
	}
	items := make([]string, 0, len(*value))
	for _, item := range *value {
		items = append(items, idString(item))
	}
	return items
}

func optionalStrings(value *[]string) []string {
	if value == nil {
		return nil
	}
	return *value
}

func copyTime(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	copyValue := *value
	return &copyValue
}

func copyInt(value *int) *int {
	if value == nil {
		return nil
	}
	copyValue := *value
	return &copyValue
}

func requireListPageFields(body []byte) error {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(body, &object); err != nil || object == nil {
		return errors.New("list response is not an object")
	}
	page, ok := object["page"]
	if !ok || bytes.Equal(bytes.TrimSpace(page), []byte("null")) {
		return errors.New("list page is missing")
	}
	var pageObject map[string]json.RawMessage
	if err := json.Unmarshal(page, &pageObject); err != nil || pageObject == nil {
		return errors.New("list page is not an object")
	}
	if _, ok := pageObject["nextCursor"]; !ok {
		return errors.New("list page cursor is missing")
	}
	return requiredObjectFields(page, "coverage", "observedAt")
}
