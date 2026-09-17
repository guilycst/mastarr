package transport

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	api "github.com/guilycst/mastarr/internal/api/generated"
	"github.com/guilycst/mastarr/internal/configuration"
	"github.com/guilycst/mastarr/internal/domain"
)

// recoverConfigurationIdempotency is the built-in recovery owner for the
// configuration routes implemented by this package. It proves the durable
// effect with a manager read and reconstructs the same sanitized generated
// response; it never invokes a mutating manager method.
func (server *Server) recoverConfigurationIdempotency(ctx context.Context, request IdempotencyRecoveryRequest) (IdempotencyRecord, bool, error) {
	if server == nil {
		return IdempotencyRecord{}, false, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	server.configurationGate.RLock()
	defer server.configurationGate.RUnlock()
	manager := server.configurationManager()
	if manager == nil {
		return IdempotencyRecord{}, false, nil
	}
	switch {
	case request.Method == http.MethodPost && request.Path == "/api/v1/connections":
		var body api.ConnectionCreate
		if !decodeRecoveryBody(request.Body, &body) {
			return IdempotencyRecord{}, false, nil
		}
		connection, err := manager.GetConnection(ctx, domain.ConfigID(body.Id), false)
		if err != nil || !sameConnectionCreate(connection, body, server.hasManagedCredentials(connection.ID)) {
			return IdempotencyRecord{}, false, nil
		}
		if body.Credentials != nil && (!server.hasManagedCredentials(connection.ID) || !sameManagedCredentialInput(ctx, manager, connection, body.Credentials)) {
			return IdempotencyRecord{}, false, nil
		}
		if !configurationEffectMatches(request, "create", "connection", connection.ID.String(), connection.Revision) {
			return IdempotencyRecord{}, false, nil
		}
		return server.configurationRecoveryRecord(request, http.StatusCreated, connection.ID.String(), server.mapConnection(connection), map[string][]string{
			"ETag":     {quoteETagValue(connection.Revision)},
			"Location": {"/api/v1/connections/" + url.PathEscape(connection.ID.String())},
		}), true, nil
	case request.Method == http.MethodPatch && strings.HasPrefix(request.Path, "/api/v1/connections/"):
		var body api.ConnectionPatch
		if !decodeRecoveryBody(request.Body, &body) {
			return IdempotencyRecord{}, false, nil
		}
		id, ok := recoveryPathID(request.Path, "/api/v1/connections/")
		if !ok {
			return IdempotencyRecord{}, false, nil
		}
		connection, err := manager.GetConnection(ctx, domain.ConfigID(id), false)
		if err != nil || !sameConnectionPatch(connection, body, request.IfMatch) {
			return IdempotencyRecord{}, false, nil
		}
		if body.Credentials != nil && (!server.hasManagedCredentials(connection.ID) || !sameManagedCredentialInput(ctx, manager, connection, body.Credentials)) {
			return IdempotencyRecord{}, false, nil
		}
		if !configurationEffectMatches(request, "patch", "connection", connection.ID.String(), connection.Revision) {
			return IdempotencyRecord{}, false, nil
		}
		return server.configurationRecoveryRecord(request, http.StatusOK, connection.ID.String(), mapConnection(connection), map[string][]string{
			"ETag": {quoteETagValue(connection.Revision)},
		}), true, nil
	case request.Method == http.MethodDelete && strings.HasPrefix(request.Path, "/api/v1/connections/"):
		id, ok := recoveryPathID(request.Path, "/api/v1/connections/")
		if !ok {
			return IdempotencyRecord{}, false, nil
		}
		connection, err := manager.GetConnection(ctx, domain.ConfigID(id), true)
		if err != nil || connection.RetiredAt == nil || !strongRecoveryMatch(request.IfMatch, connection.Revision) {
			return IdempotencyRecord{}, false, nil
		}
		if !configurationEffectMatches(request, "delete", "connection", connection.ID.String(), connection.Revision) {
			return IdempotencyRecord{}, false, nil
		}
		return server.configurationRecoveryRecord(request, http.StatusNoContent, connection.ID.String(), nil, nil), true, nil
	case request.Method == http.MethodPost && request.Path == "/api/v1/storage-roots":
		var body api.StorageRootCreate
		if !decodeRecoveryBody(request.Body, &body) {
			return IdempotencyRecord{}, false, nil
		}
		root, err := manager.GetStorageRoot(ctx, domain.ConfigID(body.Id), false)
		if err != nil || !sameStorageRootCreate(root, body) {
			return IdempotencyRecord{}, false, nil
		}
		if !configurationEffectMatches(request, "create", "storage_root", root.ID.String(), root.Revision) {
			return IdempotencyRecord{}, false, nil
		}
		return server.configurationRecoveryRecord(request, http.StatusCreated, root.ID.String(), mapStorageRoot(root), map[string][]string{
			"ETag":     {quoteETagValue(root.Revision)},
			"Location": {"/api/v1/storage-roots/" + url.PathEscape(root.ID.String())},
		}), true, nil
	case request.Method == http.MethodPatch && strings.HasPrefix(request.Path, "/api/v1/storage-roots/"):
		var body api.StorageRootPatch
		if !decodeRecoveryBody(request.Body, &body) {
			return IdempotencyRecord{}, false, nil
		}
		id, ok := recoveryPathID(request.Path, "/api/v1/storage-roots/")
		if !ok {
			return IdempotencyRecord{}, false, nil
		}
		root, err := manager.GetStorageRoot(ctx, domain.ConfigID(id), false)
		if err != nil || !sameStorageRootPatch(root, body, request.IfMatch) {
			return IdempotencyRecord{}, false, nil
		}
		if !configurationEffectMatches(request, "patch", "storage_root", root.ID.String(), root.Revision) {
			return IdempotencyRecord{}, false, nil
		}
		return server.configurationRecoveryRecord(request, http.StatusOK, root.ID.String(), mapStorageRoot(root), map[string][]string{
			"ETag": {quoteETagValue(root.Revision)},
		}), true, nil
	case request.Method == http.MethodDelete && strings.HasPrefix(request.Path, "/api/v1/storage-roots/"):
		id, ok := recoveryPathID(request.Path, "/api/v1/storage-roots/")
		if !ok {
			return IdempotencyRecord{}, false, nil
		}
		root, err := manager.GetStorageRoot(ctx, domain.ConfigID(id), true)
		if err != nil || root.RetiredAt == nil || !strongRecoveryMatch(request.IfMatch, root.Revision) {
			return IdempotencyRecord{}, false, nil
		}
		if !configurationEffectMatches(request, "delete", "storage_root", root.ID.String(), root.Revision) {
			return IdempotencyRecord{}, false, nil
		}
		return server.configurationRecoveryRecord(request, http.StatusNoContent, root.ID.String(), nil, nil), true, nil
	case request.Method == http.MethodPost && request.Path == "/api/v1/path-mappings":
		var body api.PathMappingCreate
		if !decodeRecoveryBody(request.Body, &body) {
			return IdempotencyRecord{}, false, nil
		}
		mapping, err := manager.GetPathMapping(ctx, domain.ConfigID(body.Id), false)
		if err != nil || !samePathMappingCreate(mapping, body) {
			return IdempotencyRecord{}, false, nil
		}
		if !configurationEffectMatches(request, "create", "path_mapping", mapping.ID.String(), mapping.Revision) {
			return IdempotencyRecord{}, false, nil
		}
		return server.configurationRecoveryRecord(request, http.StatusCreated, mapping.ID.String(), mapPathMapping(mapping), map[string][]string{
			"ETag":     {quoteETagValue(mapping.Revision)},
			"Location": {"/api/v1/path-mappings/" + url.PathEscape(mapping.ID.String())},
		}), true, nil
	case request.Method == http.MethodPatch && strings.HasPrefix(request.Path, "/api/v1/path-mappings/"):
		var body api.PathMappingPatch
		if !decodeRecoveryBody(request.Body, &body) {
			return IdempotencyRecord{}, false, nil
		}
		id, ok := recoveryPathID(request.Path, "/api/v1/path-mappings/")
		if !ok {
			return IdempotencyRecord{}, false, nil
		}
		mapping, err := manager.GetPathMapping(ctx, domain.ConfigID(id), false)
		if err != nil || !samePathMappingPatch(mapping, body, request.IfMatch) {
			return IdempotencyRecord{}, false, nil
		}
		if !configurationEffectMatches(request, "patch", "path_mapping", mapping.ID.String(), mapping.Revision) {
			return IdempotencyRecord{}, false, nil
		}
		return server.configurationRecoveryRecord(request, http.StatusOK, mapping.ID.String(), mapPathMapping(mapping), map[string][]string{
			"ETag": {quoteETagValue(mapping.Revision)},
		}), true, nil
	case request.Method == http.MethodDelete && strings.HasPrefix(request.Path, "/api/v1/path-mappings/"):
		id, ok := recoveryPathID(request.Path, "/api/v1/path-mappings/")
		if !ok {
			return IdempotencyRecord{}, false, nil
		}
		// Retired mappings retain their immutable revision but are omitted from
		// the active lookup. The two lookups together prove that this exact API
		// mapping was retired; an active, YAML-owned, or revision-mismatched row
		// remains unresolved and cannot be replayed as a successful DELETE.
		if _, err := manager.GetPathMapping(ctx, domain.ConfigID(id), false); err == nil {
			return IdempotencyRecord{}, false, nil
		} else if !errors.Is(err, configuration.ErrResourceNotFound) {
			return IdempotencyRecord{}, false, nil
		}
		mapping, err := manager.GetPathMapping(ctx, domain.ConfigID(id), true)
		if err != nil || mapping.Source.Source != domain.SourceAPI || !strongRecoveryMatch(request.IfMatch, mapping.Revision) {
			return IdempotencyRecord{}, false, nil
		}
		if !configurationEffectMatches(request, "delete", "path_mapping", mapping.ID.String(), mapping.Revision) {
			return IdempotencyRecord{}, false, nil
		}
		return server.configurationRecoveryRecord(request, http.StatusNoContent, mapping.ID.String(), nil, nil), true, nil
	default:
		return IdempotencyRecord{}, false, nil
	}
}

func (server *Server) configurationRecoveryRecord(request IdempotencyRecoveryRequest, status int, resourceID string, body any, headers map[string][]string) IdempotencyRecord {
	encoded := []byte(nil)
	if body != nil {
		if value, err := json.Marshal(body); err == nil {
			encoded = append(value, '\n')
		}
	}
	return IdempotencyRecord{
		Scope:        request.Scope,
		Key:          request.Key,
		Digest:       request.Digest,
		Status:       status,
		Headers:      headers,
		Body:         encoded,
		ResourceKind: "http_response",
		ResourceID:   resourceID,
		CreatedAt:    request.Record.CreatedAt,
		ExpiresAt:    request.Record.ExpiresAt,
		State:        IdempotencyStateCompleted,
		Replayable:   true,
		AttemptID:    request.Record.AttemptID,
		Effect:       request.Record.Effect,
	}
}

// configurationEffectMatches is deliberately conservative for SQLite-backed
// production recovery. A markerless reservation can have coincident current
// fields after a rejected request or a different process, so read-back alone
// cannot authorize a terminal response. Older injected stores may opt out
// while they provide their own route-specific evidence contract.
func configurationEffectMatches(request IdempotencyRecoveryRequest, operation, resourceKind, resourceID, revision string) bool {
	if !request.RequireEffectEvidence {
		return true
	}
	effect := request.Record.Effect
	return effect != nil && effect.Scope == request.Scope && effect.Key == request.Key && effect.Digest == request.Digest && effect.AttemptID == request.Record.AttemptID && effect.Operation == operation && effect.ResourceKind == resourceKind && effect.ResourceID == resourceID && effect.Revision == revision
}

func decodeRecoveryBody(data []byte, target any) bool {
	if len(bytes.TrimSpace(data)) == 0 {
		return false
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return false
	}
	var extra any
	return decoder.Decode(&extra) == io.EOF
}

func recoveryPathID(path, prefix string) (string, bool) {
	if !strings.HasPrefix(path, prefix) {
		return "", false
	}
	value, err := url.PathUnescape(strings.TrimPrefix(path, prefix))
	if err != nil || strings.TrimSpace(value) == "" || strings.Contains(value, "/") {
		return "", false
	}
	return value, true
}

func quoteETagValue(revision string) string { return `"` + revision + `"` }

func strongRecoveryMatch(value, revision string) bool {
	parsed, err := strongETag(value)
	return err == nil && parsed == revision
}

func sameConnectionCreate(current domain.Connection, body api.ConnectionCreate, managed bool) bool {
	if current.Source.Source != domain.SourceAPI || current.RetiredAt != nil {
		return false
	}
	if current.ID != domain.ConfigID(body.Id) || current.Kind != domain.ConnectionKind(body.Kind) || current.Label != body.Label || current.Endpoint != body.Endpoint {
		return false
	}
	return (body.Credentials != nil) == managed
}

func sameConnectionPatch(current domain.Connection, body api.ConnectionPatch, ifMatch string) bool {
	if current.Source.Source != domain.SourceAPI || current.RetiredAt != nil || !validRecoveryETag(ifMatch) {
		return false
	}
	if body.Label != nil && *body.Label != current.Label {
		return false
	}
	if body.Endpoint != nil && *body.Endpoint != current.Endpoint {
		return false
	}
	return true
}

// sameManagedCredentialInput proves a recovered connection create/patch by
// resolving the exact requested fields from the current managed store. The
// resolved plaintext is held only for the byte comparison and is cleared on
// every path; no credential value enters the recovery record or response.
func sameManagedCredentialInput(ctx context.Context, manager *configuration.Manager, current domain.Connection, input *api.CredentialInput) bool {
	if manager == nil || input == nil || current.Source.Source != domain.SourceAPI || current.RetiredAt != nil {
		return false
	}
	values, cleanup, err := credentialInputs(input)
	if err != nil {
		return false
	}
	defer cleanup()
	// Managed references are intentionally omitted from the public domain
	// object, so discover the complete active field set through the bounded
	// credential vocabulary and read each value back from the manager. This
	// rejects both missing requested fields and extra current fields without
	// exposing the plaintext in a durable record.
	observedFields := 0
	for _, field := range []string{"apiKey", "token", "username", "password"} {
		observed, resolveErr := manager.ResolveCredential(ctx, current.ID, field)
		requested, requestedOK := values[field]
		if resolveErr != nil {
			zeroCredentialBytes(observed)
			if requestedOK {
				return false
			}
			continue
		}
		observedFields++
		if !requestedOK || !bytes.Equal(observed, requested.Value) {
			zeroCredentialBytes(observed)
			return false
		}
		zeroCredentialBytes(observed)
	}
	return observedFields == len(values)
}

func zeroCredentialBytes(value []byte) {
	for index := range value {
		value[index] = 0
	}
}

func sameStorageRootCreate(current domain.StorageRoot, body api.StorageRootCreate) bool {
	return current.Source.Source == domain.SourceAPI && current.RetiredAt == nil && current.ID == domain.ConfigID(body.Id) && current.Label == body.Label && current.Purpose == domain.StoragePurpose(body.Purpose) && current.Path == body.Path
}

func sameStorageRootPatch(current domain.StorageRoot, body api.StorageRootPatch, ifMatch string) bool {
	if current.Source.Source != domain.SourceAPI || current.RetiredAt != nil || !validRecoveryETag(ifMatch) {
		return false
	}
	if body.Label != nil && *body.Label != current.Label || body.Path != nil && *body.Path != current.Path {
		return false
	}
	if body.WatchEnabled != nil && *body.WatchEnabled != current.Watch.Enabled || body.WatchIntervalSeconds != nil && *body.WatchIntervalSeconds != int(current.Watch.Interval/time.Second) {
		return false
	}
	return true
}

func samePathMappingCreate(current domain.PathMapping, body api.PathMappingCreate) bool {
	return current.Source.Source == domain.SourceAPI && current.ID == domain.ConfigID(body.Id) && current.ConnectionID == domain.ConfigID(body.ConnectionId) && current.RootID == domain.ConfigID(body.RootId) && current.SourcePrefix == body.SourcePrefix && current.DestinationPrefix == body.DestinationPrefix
}

func samePathMappingPatch(current domain.PathMapping, body api.PathMappingPatch, ifMatch string) bool {
	if current.Source.Source != domain.SourceAPI || !validRecoveryETag(ifMatch) {
		return false
	}
	return (body.SourcePrefix == nil || *body.SourcePrefix == current.SourcePrefix) && (body.DestinationPrefix == nil || *body.DestinationPrefix == current.DestinationPrefix)
}

func validRecoveryETag(value string) bool {
	_, err := strongETag(value)
	return err == nil
}

// configurationEffectMaterialized classifies a persistence failure after the
// manager has been rebuilt from durable state. Only an authoritative absence
// permits release of the reservation. A present, changed, or unreadable
// target remains pending for read-only reconciliation, which covers commit
// failures whose visible outcome is uncertain.
func (server *Server) configurationEffectMaterialized(ctx context.Context) (known, materialized bool) {
	if ctx == nil {
		ctx = context.Background()
	}
	state, ok := ctx.Value(idempotencyRequestStateKey{}).(*idempotencyRequestState)
	if !ok || state == nil || server == nil {
		return false, false
	}
	manager := server.configurationManager()
	if manager == nil {
		return false, false
	}
	findActive := func(kind string, id string) (bool, bool) {
		var err error
		switch kind {
		case "connection":
			_, err = manager.GetConnection(ctx, domain.ConfigID(id), false)
		case "storage_root":
			_, err = manager.GetStorageRoot(ctx, domain.ConfigID(id), false)
		case "path_mapping":
			_, err = manager.GetPathMapping(ctx, domain.ConfigID(id), false)
		default:
			return false, false
		}
		if err == nil {
			return true, true
		}
		if errors.Is(err, configuration.ErrResourceNotFound) {
			return true, false
		}
		return false, false
	}
	switch {
	case state.method == http.MethodPost && state.path == "/api/v1/connections":
		var body api.ConnectionCreate
		if !decodeRecoveryBody(state.body, &body) {
			return false, false
		}
		_, err := manager.GetConnection(ctx, domain.ConfigID(body.Id), false)
		if err == nil {
			// A present candidate is not proof that this request created it.  It
			// may be a pre-existing or conflicting resource, and release would
			// make the caller retry against that unrelated state.  Keep the
			// reservation pending until the exact recovery owner can prove the
			// request's identity and response.
			return true, true
		}
		if errors.Is(err, configuration.ErrResourceNotFound) {
			return true, false
		}
		return false, false
	case state.method == http.MethodPatch && strings.HasPrefix(state.path, "/api/v1/connections/"):
		id, ok := recoveryPathID(state.path, "/api/v1/connections/")
		if !ok {
			return false, false
		}
		found, present := findActive("connection", id)
		if !found || !present {
			return found, present
		}
		connection, err := manager.GetConnection(ctx, domain.ConfigID(id), false)
		if err != nil {
			return false, false
		}
		var body api.ConnectionPatch
		if !decodeRecoveryBody(state.body, &body) {
			return false, false
		}
		if !validRecoveryETag(state.ifMatch) {
			return false, false
		}
		// The reload is serialized behind the failed request's write gate. An
		// unchanged revision is therefore authoritative evidence that the
		// candidate never reached durable state, regardless of the requested
		// values. A changed revision is an uncertain/potentially materialized
		// target and must stay pending; it is never released merely because a
		// field differs.
		if expected, _ := strongETag(state.ifMatch); expected == connection.Revision {
			return true, false
		}
		return true, true
	case state.method == http.MethodDelete && strings.HasPrefix(state.path, "/api/v1/connections/"):
		id, ok := recoveryPathID(state.path, "/api/v1/connections/")
		if !ok {
			return false, false
		}
		_, err := manager.GetConnection(ctx, domain.ConfigID(id), false)
		if err == nil {
			return true, false
		}
		if errors.Is(err, configuration.ErrResourceNotFound) {
			retired, retiredErr := manager.GetConnection(ctx, domain.ConfigID(id), true)
			if retiredErr == nil && retired.RetiredAt != nil {
				return true, true
			}
			if errors.Is(retiredErr, configuration.ErrResourceNotFound) {
				return true, false
			}
		}
		return false, false
	case state.method == http.MethodPost && state.path == "/api/v1/storage-roots":
		var body api.StorageRootCreate
		if !decodeRecoveryBody(state.body, &body) {
			return false, false
		}
		_, err := manager.GetStorageRoot(ctx, domain.ConfigID(body.Id), false)
		if err == nil {
			// Presence alone cannot distinguish a committed request from a
			// conflicting pre-existing resource.  Preserve the reservation for
			// exact read-back recovery instead of releasing it.
			return true, true
		}
		if errors.Is(err, configuration.ErrResourceNotFound) {
			return true, false
		}
		return false, false
	case state.method == http.MethodPatch && strings.HasPrefix(state.path, "/api/v1/storage-roots/"):
		id, ok := recoveryPathID(state.path, "/api/v1/storage-roots/")
		if !ok {
			return false, false
		}
		root, err := manager.GetStorageRoot(ctx, domain.ConfigID(id), false)
		if err != nil {
			if errors.Is(err, configuration.ErrResourceNotFound) {
				return true, false
			}
			return false, false
		}
		var body api.StorageRootPatch
		if !decodeRecoveryBody(state.body, &body) {
			return false, false
		}
		if !validRecoveryETag(state.ifMatch) {
			return false, false
		}
		if expected, _ := strongETag(state.ifMatch); expected == root.Revision {
			return true, false
		}
		return true, true
	case state.method == http.MethodDelete && strings.HasPrefix(state.path, "/api/v1/storage-roots/"):
		id, ok := recoveryPathID(state.path, "/api/v1/storage-roots/")
		if !ok {
			return false, false
		}
		if _, err := manager.GetStorageRoot(ctx, domain.ConfigID(id), false); err == nil {
			return true, false
		}
		root, err := manager.GetStorageRoot(ctx, domain.ConfigID(id), true)
		if err == nil && root.RetiredAt != nil {
			return true, true
		}
		if errors.Is(err, configuration.ErrResourceNotFound) {
			return true, false
		}
		return false, false
	case state.method == http.MethodPost && state.path == "/api/v1/path-mappings":
		var body api.PathMappingCreate
		if !decodeRecoveryBody(state.body, &body) {
			return false, false
		}
		_, err := manager.GetPathMapping(ctx, domain.ConfigID(body.Id), false)
		if err == nil {
			// A pre-existing mapping with this identifier is ambiguous.  It is
			// never safe to release the request merely because a row exists.
			return true, true
		}
		if errors.Is(err, configuration.ErrResourceNotFound) {
			return true, false
		}
		return false, false
	case state.method == http.MethodPatch && strings.HasPrefix(state.path, "/api/v1/path-mappings/"):
		id, ok := recoveryPathID(state.path, "/api/v1/path-mappings/")
		if !ok {
			return false, false
		}
		mapping, err := manager.GetPathMapping(ctx, domain.ConfigID(id), false)
		if err != nil {
			if errors.Is(err, configuration.ErrResourceNotFound) {
				return true, false
			}
			return false, false
		}
		var body api.PathMappingPatch
		if !decodeRecoveryBody(state.body, &body) {
			return false, false
		}
		if !validRecoveryETag(state.ifMatch) {
			return false, false
		}
		if expected, _ := strongETag(state.ifMatch); expected == mapping.Revision {
			return true, false
		}
		return true, true
	case state.method == http.MethodDelete && strings.HasPrefix(state.path, "/api/v1/path-mappings/"):
		id, ok := recoveryPathID(state.path, "/api/v1/path-mappings/")
		if !ok {
			return false, false
		}
		if _, err := manager.GetPathMapping(ctx, domain.ConfigID(id), false); err == nil {
			return true, false
		} else if !errors.Is(err, configuration.ErrResourceNotFound) {
			return false, false
		}
		mapping, err := manager.GetPathMapping(ctx, domain.ConfigID(id), true)
		if err == nil && mapping.Source.Source == domain.SourceAPI && strongRecoveryMatch(state.ifMatch, mapping.Revision) {
			return true, true
		}
		if errors.Is(err, configuration.ErrResourceNotFound) {
			return true, false
		}
		return false, false
	default:
		return false, false
	}
}
