// Package actions contains the typed action handlers used by the durable
// execution process manager. Each handler owns one action kind and calls only
// the frozen domain port for that kind. The package deliberately contains no
// SQL, HTTP, generated upstream types or workflow composition logic.
package actions

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/guilycst/mastarr/internal/descriptors"
	"github.com/guilycst/mastarr/internal/domain"
	"github.com/guilycst/mastarr/internal/execution"
	"github.com/guilycst/mastarr/internal/planning"
	"github.com/guilycst/mastarr/internal/ports"
)

var (
	// ErrInvalidIntent means an action payload cannot be safely interpreted.
	ErrInvalidIntent = errors.New("action intent is invalid")
	// ErrDependencyRequired means a handler cannot perform its required
	// read-before-write check because a typed port was not wired.
	ErrDependencyRequired = errors.New("action dependency is required")
	// ErrCapabilityUnknown means the operation has not been proven safe for
	// the configured upstream or host.
	ErrCapabilityUnknown = errors.New("action capability is unknown")
	// ErrCapabilityUnsupported means a reviewed capability explicitly refuses
	// this operation. It is distinct from a transient dependency outage.
	ErrCapabilityUnsupported = errors.New("action capability is unsupported")
	// ErrStateUnknown means a read did not prove either side of a desired
	// predicate and therefore cannot authorize a mutation.
	ErrStateUnknown = errors.New("action desired state is unknown")
)

const (
	defaultMaxItems = 10_000
	defaultMaxFiles = 1_000
	maxIntentBytes  = 1 << 20
)

// HandlerOptions controls bounded observations and direct-call safety. Now is
// injectable for deterministic tests; production callers should leave it nil.
type HandlerOptions struct {
	Now          func() time.Time
	MaxItems     int
	MaxFiles     int
	LinkedClient *LinkedClient
	// IsNotFound may identify an authoritative absence for a port whose error
	// vocabulary cannot expose a separate not-found sentinel. Returning false
	// is the safe default: an unavailable read remains unknown.
	IsNotFound func(error) bool
}

func (options HandlerOptions) normalized() HandlerOptions {
	if options.Now == nil {
		options.Now = func() time.Time { return time.Now().UTC() }
	}
	if options.MaxItems <= 0 {
		options.MaxItems = defaultMaxItems
	}
	if options.MaxFiles <= 0 {
		options.MaxFiles = defaultMaxFiles
	}
	return options
}

// LinkedClient is an optional prerequisite for filesystem actions. When an
// intent carries a LinkedDownload reference, the handler requires this exact
// qBittorrent/NZBGet observation and refuses mutation unless it is stopped.
type LinkedClient struct {
	Control ports.DownloadControlPort
	Ref     ports.DownloadRef
	// RequireStopped defaults to true. It is retained as a field so a future
	// reviewed client action can express a different prerequisite explicitly.
	RequireStopped bool
}

// Dependencies is the wiring surface for all v0.0.1 action handlers. The
// standalone client modules are hidden behind these root ports; their DTOs
// cannot leak into action intents or execution effects.
type Dependencies struct {
	ArrRead         ports.MediaManagerReadPort
	ArrWrite        ports.MediaManagerWritePort
	ArrCapabilities ports.CapabilityPort

	FilesystemRead   ports.FilesystemReadPort
	FilesystemAction ports.FilesystemActionPort

	DownloadControl      ports.DownloadControlPort
	DownloadCapabilities ports.CapabilityPort

	Descriptor DescriptorPort

	Refresh             ports.MediaServerRefreshPort
	RefreshCapabilities ports.CapabilityPort
}

// DescriptorPort is the small metadata-only descriptor boundary needed by
// descriptor.delete. Content bytes and storage paths never enter handlers.
type DescriptorPort interface {
	Get(context.Context, string) (descriptors.Record, error)
	Delete(context.Context, descriptors.DeleteRequest) (descriptors.Record, error)
}

// RegistrationIntent is the canonical JSON payload for arr.registration.
type RegistrationIntent struct {
	ConnectionID domain.ConfigID          `json:"connectionId"`
	ProviderID   string                   `json:"providerId"`
	Kind         domain.MediaKind         `json:"kind"`
	ExternalID   string                   `json:"externalId,omitempty"`
	Fields       ports.RegistrationFields `json:"fields"`
}

// ImportIntent is the canonical JSON payload for arr.import. Files are an
// exact reviewed set; a directory or wildcard is never implied.
type ImportIntent struct {
	ConnectionID         domain.ConfigID    `json:"connectionId"`
	RegisteredExternalID string             `json:"registeredExternalId"`
	PreviewRevision      string             `json:"previewRevision"`
	Transfer             string             `json:"transfer"`
	Files                []ports.ImportFile `json:"files"`
}

// FileIntent is the canonical payload for copy, hardlink, move, rename and
// restore. Delete/trash use Manifest. LinkedDownload is optional and, when
// present, must be satisfied before any filesystem effect.
type FileIntent struct {
	// Executor selects the implementation for move and rename. Empty keeps the
	// direct-handler default of mastarr for callers created before the HTTP
	// envelope made this field mandatory. The API layer should always persist
	// either "mastarr" or "native_client".
	Executor       string                     `json:"executor,omitempty"`
	Files          []ports.FileMap            `json:"files,omitempty"`
	Manifest       []domain.FileManifestEntry `json:"manifest,omitempty"`
	Retention      time.Duration              `json:"retention,omitempty"`
	LinkedDownload *ports.DownloadRef         `json:"linkedDownload,omitempty"`
}

// ClientIntent is the canonical payload for client.stop and client.remove.
type ClientIntent struct {
	Ref         ports.DownloadRef  `json:"ref"`
	Destination *domain.FileTarget `json:"destination,omitempty"`
	Source      *domain.FileTarget `json:"source,omitempty"`
	NewName     string             `json:"newName,omitempty"`
}

// DescriptorIntent is the canonical payload for descriptor.delete.
type DescriptorIntent struct {
	DescriptorID             string `json:"descriptorId"`
	IrreversibleAcknowledged bool   `json:"irreversibleAcknowledged"`
}

// RefreshIntent is the canonical payload for jellyfin.refresh.
type RefreshIntent struct {
	ConnectionID domain.ConfigID    `json:"connectionId"`
	Scope        ports.RefreshScope `json:"scope"`
	ExternalID   string             `json:"externalId,omitempty"`
}

// EncodeIntent serializes an action intent with a bounded JSON representation.
// Callers should pass one of the exported intent types. The execution layer
// stores the returned bytes as immutable approved desired state.
func EncodeIntent(value any) (json.RawMessage, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("%w: encode intent: %v", ErrInvalidIntent, err)
	}
	if len(encoded) == 0 || len(encoded) > maxIntentBytes || !json.Valid(encoded) {
		return nil, fmt.Errorf("%w: encoded intent exceeds bounds", ErrInvalidIntent)
	}
	return append(json.RawMessage(nil), encoded...), nil
}

// NewHandlers constructs the complete v0.0.1 handler set. Missing required
// dependencies fail construction instead of producing handlers that might
// bypass observe-before-write at runtime. Filesystem action ports are shared
// by the typed filesystem handlers; Arr, download, descriptor and refresh
// dependencies are each checked independently.
func NewHandlers(deps Dependencies, options HandlerOptions) ([]execution.Handler, error) {
	options = options.normalized()
	if deps.ArrRead == nil || deps.ArrWrite == nil {
		return nil, fmt.Errorf("%w: Arr read and write ports", ErrDependencyRequired)
	}
	if deps.FilesystemRead == nil || deps.FilesystemAction == nil {
		return nil, fmt.Errorf("%w: filesystem read and action ports", ErrDependencyRequired)
	}
	if deps.DownloadControl == nil || deps.DownloadCapabilities == nil {
		return nil, fmt.Errorf("%w: download control and capability ports", ErrDependencyRequired)
	}
	if deps.Descriptor == nil {
		return nil, fmt.Errorf("%w: descriptor port", ErrDependencyRequired)
	}
	if deps.Refresh == nil || deps.RefreshCapabilities == nil {
		return nil, fmt.Errorf("%w: refresh and capability ports", ErrDependencyRequired)
	}
	registration, err := NewRegistrationHandler(RegistrationConfig{Read: deps.ArrRead, Write: deps.ArrWrite, Capabilities: deps.ArrCapabilities, Options: options})
	if err != nil {
		return nil, err
	}
	importHandler, err := NewImportHandler(ImportConfig{Read: deps.ArrRead, Write: deps.ArrWrite, Capabilities: deps.ArrCapabilities, Options: options})
	if err != nil {
		return nil, err
	}
	filesystem, err := NewFilesystemHandlers(FilesystemConfig{Read: deps.FilesystemRead, Action: deps.FilesystemAction, Options: options})
	if err != nil {
		return nil, err
	}
	client, err := NewClientHandlers(ClientConfig{Control: deps.DownloadControl, Capabilities: deps.DownloadCapabilities, Options: options})
	if err != nil {
		return nil, err
	}
	descriptor, err := NewDescriptorHandler(DescriptorConfig{Port: deps.Descriptor, Options: options})
	if err != nil {
		return nil, err
	}
	refresh, err := NewRefreshHandler(RefreshConfig{Refresh: deps.Refresh, Capabilities: deps.RefreshCapabilities, Options: options})
	if err != nil {
		return nil, err
	}
	result := []execution.Handler{
		registration, importHandler,
		filesystem.Copy, filesystem.Hardlink, filesystem.Move, filesystem.Rename,
		filesystem.Trash, filesystem.Restore, filesystem.Delete,
		client.Stop, client.Remove, descriptor, refresh,
	}
	return result, nil
}

// RegisterAll installs all typed handlers into one executor and rejects
// duplicate action kinds. It is the composition seam used by application
// startup; individual constructors remain available for direct action calls.
func RegisterAll(executor *execution.Executor, deps Dependencies, options HandlerOptions) error {
	if executor == nil {
		return errors.New("action executor is required")
	}
	handlers, err := NewHandlers(deps, options)
	if err != nil {
		return err
	}
	for _, handler := range handlers {
		if err := executor.RegisterHandler(handler); err != nil {
			return err
		}
	}
	return nil
}

type baseHandler struct {
	kind    domain.ActionKind
	options HandlerOptions
}

func (handler baseHandler) Kind() domain.ActionKind { return handler.kind }

func (handler baseHandler) validateAction(action execution.Action) error {
	if action.Kind != handler.kind {
		return fmt.Errorf("%w: action kind %q does not match handler %q", ErrInvalidIntent, action.Kind, handler.kind)
	}
	if strings.TrimSpace(action.ID) == "" || strings.TrimSpace(action.PlanID) == "" || strings.TrimSpace(action.PlanDigest) == "" {
		return fmt.Errorf("%w: action identity is incomplete", ErrInvalidIntent)
	}
	if action.PlanRevision <= 0 || action.Version <= 0 {
		return fmt.Errorf("%w: action revision/version is invalid", ErrInvalidIntent)
	}
	if !action.State.Valid() {
		return fmt.Errorf("%w: action state is invalid", ErrInvalidIntent)
	}
	if len(action.DesiredState) == 0 || len(action.DesiredState) > maxIntentBytes || !json.Valid(action.DesiredState) {
		return fmt.Errorf("%w: desired state JSON is invalid or too large", ErrInvalidIntent)
	}
	return nil
}

func (handler baseHandler) now() time.Time {
	now := handler.options.Now()
	if now.IsZero() {
		now = time.Now().UTC()
	}
	return now.UTC()
}

func (handler baseHandler) guardDispatch(ctx context.Context, action execution.Action) error {
	if ctx == nil {
		return execution.NewFailure(execution.FailureCancelled, context.Canceled)
	}
	if err := ctx.Err(); err != nil {
		return execution.NewFailure(execution.FailureCancelled, err)
	}
	if strings.TrimSpace(action.CancellationRequestedAt) != "" {
		return execution.NewFailure(execution.FailureCancelled, context.Canceled)
	}
	if strings.TrimSpace(action.DeadlineAt) != "" {
		deadline, err := time.Parse(time.RFC3339Nano, action.DeadlineAt)
		if err != nil {
			return fmt.Errorf("%w: invalid deadline", ErrInvalidIntent)
		}
		if !deadline.After(handler.now()) {
			return execution.NewFailure(execution.FailureCancelled, context.DeadlineExceeded)
		}
	}
	return nil
}

func (handler baseHandler) guardObserve(ctx context.Context) error {
	if ctx == nil {
		return execution.NewFailure(execution.FailureCancelled, context.Canceled)
	}
	if err := ctx.Err(); err != nil {
		return execution.NewFailure(execution.FailureCancelled, err)
	}
	return nil
}

func (handler baseHandler) failure(err error, dispatched bool) error {
	if err == nil {
		return nil
	}
	if dispatched {
		return execution.NewDispatchedFailure(execution.FailureUncertain, err)
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return execution.NewFailure(execution.FailureCancelled, err)
	}
	var upstream domain.UpstreamError
	if errors.As(err, &upstream) {
		switch upstream.Code {
		case domain.OutcomeUnavailable, domain.OutcomeRateLimited:
			return execution.NewFailure(execution.FailureDependency, err)
		case domain.OutcomeUnauthorized, domain.OutcomeInvalidInput:
			return execution.NewFailure(execution.FailureInvalid, err)
		case domain.OutcomeConflict, domain.OutcomeUnsupported:
			return execution.NewFailure(execution.FailureConflict, err)
		default:
			return execution.NewFailure(execution.FailureDependency, err)
		}
	}
	if errors.Is(err, ErrCapabilityUnknown) || errors.Is(err, ErrDependencyRequired) {
		return execution.NewFailure(execution.FailureDependency, err)
	}
	if errors.Is(err, ErrCapabilityUnsupported) || errors.Is(err, ErrStateUnknown) {
		return execution.NewFailure(execution.FailureConflict, err)
	}
	return execution.NewFailure(execution.FailureInvalid, err)
}

func decodeIntent(raw json.RawMessage, destination any) error {
	if len(raw) == 0 || len(raw) > maxIntentBytes {
		return fmt.Errorf("%w: intent size is out of bounds", ErrInvalidIntent)
	}
	if err := rejectDuplicateJSONKeys(raw); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidIntent, err)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return fmt.Errorf("%w: decode intent: %v", ErrInvalidIntent, err)
	}
	var extra any
	if err := decoder.Decode(&extra); err == nil {
		return fmt.Errorf("%w: intent contains multiple JSON values", ErrInvalidIntent)
	} else if !errors.Is(err, io.EOF) {
		return fmt.Errorf("%w: trailing intent data", ErrInvalidIntent)
	}
	return nil
}

func rejectDuplicateJSONKeys(raw []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if err := consumeJSONValue(decoder); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("intent contains multiple JSON values")
		}
		return fmt.Errorf("intent has trailing data: %v", err)
	}
	return nil
}

func consumeJSONValue(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	switch delimiter := token.(type) {
	case json.Delim:
		switch delimiter {
		case '{':
			seen := make(map[string]struct{})
			for decoder.More() {
				key, err := decoder.Token()
				if err != nil {
					return err
				}
				name, ok := key.(string)
				if !ok {
					return errors.New("object key is not a string")
				}
				if _, exists := seen[name]; exists {
					return fmt.Errorf("duplicate object key %q", name)
				}
				seen[name] = struct{}{}
				if err := consumeJSONValue(decoder); err != nil {
					return err
				}
			}
			_, err = decoder.Token()
			return err
		case '[':
			for decoder.More() {
				if err := consumeJSONValue(decoder); err != nil {
					return err
				}
			}
			_, err = decoder.Token()
			return err
		case '}', ']':
			return errors.New("unexpected JSON delimiter")
		}
	}
	return nil
}

func decodePlanningDesired(raw json.RawMessage) (planning.DesiredState, bool, error) {
	if len(raw) == 0 || len(raw) > maxIntentBytes {
		return planning.DesiredState{}, false, fmt.Errorf("%w: intent size is out of bounds", ErrInvalidIntent)
	}
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(raw, &probe); err != nil {
		return planning.DesiredState{}, false, fmt.Errorf("%w: intent is not an object", ErrInvalidIntent)
	}
	if _, found := probe["predicates"]; !found {
		if _, found = probe["Predicates"]; !found {
			return planning.DesiredState{}, false, nil
		}
	}
	var desired planning.DesiredState
	if err := decodeIntent(raw, &desired); err != nil {
		return planning.DesiredState{}, true, err
	}
	if err := desired.Validate(); err != nil {
		return planning.DesiredState{}, true, fmt.Errorf("%w: planning desired state: %v", ErrInvalidIntent, err)
	}
	return desired, true, nil
}

func effect(targetKind, targetID, effectKind string, state execution.EffectState, observedAt time.Time, evidence ...string) execution.Effect {
	return execution.Effect{
		Ordinal:    -1,
		TargetKind: targetKind,
		TargetID:   targetID,
		EffectKind: effectKind,
		State:      state,
		Evidence:   evidenceJSON(evidence),
		ObservedAt: observedAt.UTC().Format(time.RFC3339Nano),
	}
}

func evidenceJSON(values []string) json.RawMessage {
	if len(values) == 0 {
		return json.RawMessage(`{}`)
	}
	encoded, err := json.Marshal(values)
	if err != nil {
		return json.RawMessage(`{"error":"evidence_encoding"}`)
	}
	return encoded
}

func effectState(outcome domain.EffectOutcome) execution.EffectState {
	if outcome == domain.OutcomeAlreadySatisfied {
		return execution.EffectAlreadySatisfied
	}
	return execution.EffectApplied
}

func targetID(target domain.FileTarget) string {
	return target.RootID.String() + ":" + target.RelativePath
}

func refID(ref ports.DownloadRef) string {
	return ref.ConnectionID.String() + ":" + strings.TrimSpace(ref.ExternalID)
}

func validConnection(id domain.ConfigID) bool { return id.Valid() }

func validateConnection(expected, supplied domain.ConfigID) error {
	if !validConnection(expected) || !validConnection(supplied) || expected != supplied {
		return fmt.Errorf("%w: connection scope does not match configured handler", ErrInvalidIntent)
	}
	return nil
}

func validateRef(ref ports.DownloadRef) error {
	if !ref.ConnectionID.Valid() || strings.TrimSpace(ref.ExternalID) == "" || len(ref.ExternalID) > 256 {
		return fmt.Errorf("%w: download reference is invalid", ErrInvalidIntent)
	}
	return nil
}

func isStoppedDownload(observation ports.DownloadObservation) bool {
	state := strings.ToLower(strings.TrimSpace(observation.State))
	return state == "pauseddl" || state == "pausedup" || state == "stopped" || state == "stoppeddl" || state == "stoppedup" || state == "paused"
}

func exactTarget(left, right domain.FileTarget) bool {
	return left.RootID == right.RootID && left.RelativePath == right.RelativePath
}

func cloneImportFiles(files []ports.ImportFile) []ports.ImportFile {
	result := make([]ports.ImportFile, len(files))
	copy(result, files)
	return result
}

func cloneRegistrationFields(fields ports.RegistrationFields) ports.RegistrationFields {
	result := fields
	result.Seasons = append([]string(nil), fields.Seasons...)
	if fields.Monitored != nil {
		value := *fields.Monitored
		result.Monitored = &value
	}
	if fields.SeasonFolder != nil {
		value := *fields.SeasonFolder
		result.SeasonFolder = &value
	}
	return result
}

func sortedStrings(values []string) []string {
	result := append([]string(nil), values...)
	sort.Strings(result)
	return result
}

func pathForChild(parent, child string) (string, bool) {
	if parent == "" || child == "" || child == parent {
		return "", false
	}
	prefix := strings.TrimSuffix(parent, "/") + "/"
	if !strings.HasPrefix(child, prefix) {
		return "", false
	}
	return strings.TrimPrefix(child, prefix), true
}

func joinTarget(parent domain.FileTarget, suffix string) (domain.FileTarget, error) {
	if suffix == "" {
		return domain.FileTarget{}, fmt.Errorf("%w: empty child suffix", ErrInvalidIntent)
	}
	target := domain.FileTarget{RootID: parent.RootID, RelativePath: path.Join(parent.RelativePath, suffix)}
	if err := target.Validate(); err != nil {
		return domain.FileTarget{}, fmt.Errorf("%w: child target: %v", ErrInvalidIntent, err)
	}
	return target, nil
}

func classifyNotFound(err error, options HandlerOptions) bool {
	return err != nil && options.IsNotFound != nil && options.IsNotFound(err)
}
