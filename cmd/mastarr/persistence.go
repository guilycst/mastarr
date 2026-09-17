package main

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/guilycst/mastarr/internal/domain"
	"github.com/guilycst/mastarr/internal/transport"
)

var errConfigurationPersistence = transport.ErrConfigurationStore

// sqliteConfigurationPersistence is the process boundary between the
// in-memory configuration manager and the durable API-owned rows. It opens
// one transaction around the parent row, managed encrypted envelopes, and
// immutable source snapshot. The manager callback receives the transaction so
// a credential FK can never point at an uncommitted parent.
type sqliteConfigurationPersistence struct {
	db  *sql.DB
	now func() time.Time
}

func newSQLiteConfigurationPersistence(db *sql.DB, now func() time.Time) *transport.ConfigurationPersistence {
	persistence := &sqliteConfigurationPersistence{db: db, now: now}
	return &transport.ConfigurationPersistence{
		CreateConnection:  persistence.createConnection,
		UpdateConnection:  persistence.updateConnection,
		RetireConnection:  persistence.retireConnection,
		CreateStorageRoot: persistence.createStorageRoot,
		UpdateStorageRoot: persistence.updateStorageRoot,
		RetireStorageRoot: persistence.retireStorageRoot,
		CreatePathMapping: persistence.createPathMapping,
		UpdatePathMapping: persistence.updatePathMapping,
		RetirePathMapping: persistence.retirePathMapping,
	}
}

func (persistence *sqliteConfigurationPersistence) clock() func() time.Time {
	if persistence != nil && persistence.now != nil {
		return persistence.now
	}
	return time.Now
}

func (persistence *sqliteConfigurationPersistence) begin(ctx context.Context) (*sql.Tx, error) {
	if persistence == nil || persistence.db == nil {
		return nil, errConfigurationPersistence
	}
	if err := contextDone(ctx); err != nil {
		return nil, err
	}
	tx, err := persistence.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, errConfigurationPersistence
	}
	return tx, nil
}

func rollbackConfiguration(tx *sql.Tx) {
	if tx != nil {
		_ = tx.Rollback()
	}
}

func (persistence *sqliteConfigurationPersistence) createConnection(ctx context.Context, draft domain.Connection, mutate func(context.Context) (domain.Connection, error)) (domain.Connection, error) {
	tx, err := persistence.begin(ctx)
	if err != nil {
		return domain.Connection{}, err
	}
	defer rollbackConfiguration(tx)
	now := persistence.clock()().UTC()
	pendingRevision := pendingSnapshotRevision("connection", draft.ID.String())
	pendingSnapshot := sqliteSnapshotID("connection", draft.ID.String(), pendingRevision)
	if err := insertConfigSnapshot(ctx, tx, pendingSnapshot, "api", "api", pendingRevision, now, "{}", now); err != nil {
		return domain.Connection{}, errConfigurationPersistence
	}
	created := now.Format(time.RFC3339Nano)
	if _, err := tx.ExecContext(ctx, `INSERT INTO connections (id, kind, label, endpoint, source, source_snapshot_id, revision, retired_at, created_at, updated_at) VALUES (?, ?, ?, ?, 'api', ?, 'pending', NULL, ?, ?)`, draft.ID.String(), string(draft.Kind), draft.Label, draft.Endpoint, pendingSnapshot, created, created); err != nil {
		return domain.Connection{}, errConfigurationPersistence
	}
	result, err := mutate(withSQLiteTx(ctx, tx))
	if err != nil {
		return domain.Connection{}, err
	}
	if err := persistConnection(ctx, tx, result, "pending", now); err != nil {
		return domain.Connection{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.Connection{}, errConfigurationPersistence
	}
	return result, nil
}

func (persistence *sqliteConfigurationPersistence) updateConnection(ctx context.Context, id domain.ConfigID, expectedRevision string, mutate func(context.Context) (domain.Connection, error)) (domain.Connection, error) {
	tx, err := persistence.begin(ctx)
	if err != nil {
		return domain.Connection{}, err
	}
	defer rollbackConfiguration(tx)
	result, err := mutate(withSQLiteTx(ctx, tx))
	if err != nil {
		return domain.Connection{}, err
	}
	if err := persistConnection(ctx, tx, result, expectedRevision, persistence.clock()().UTC()); err != nil {
		return domain.Connection{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.Connection{}, errConfigurationPersistence
	}
	return result, nil
}

func (persistence *sqliteConfigurationPersistence) retireConnection(ctx context.Context, id domain.ConfigID, expectedRevision string, mutate func(context.Context) error) error {
	tx, err := persistence.begin(ctx)
	if err != nil {
		return err
	}
	defer rollbackConfiguration(tx)
	if err := mutate(withSQLiteTx(ctx, tx)); err != nil {
		return err
	}
	retiredAt := persistence.clock()().UTC().Format(time.RFC3339Nano)
	result, err := tx.ExecContext(ctx, `UPDATE connections SET retired_at = ?, updated_at = ? WHERE id = ? AND source = 'api' AND revision = ? AND retired_at IS NULL`, retiredAt, retiredAt, id.String(), expectedRevision)
	if err != nil {
		return errConfigurationPersistence
	}
	if count, err := result.RowsAffected(); err != nil || count != 1 {
		return errConfigurationPersistence
	}
	if err := tx.Commit(); err != nil {
		return errConfigurationPersistence
	}
	return nil
}

func (persistence *sqliteConfigurationPersistence) createStorageRoot(ctx context.Context, draft domain.StorageRoot, mutate func(context.Context) (domain.StorageRoot, error)) (domain.StorageRoot, error) {
	tx, err := persistence.begin(ctx)
	if err != nil {
		return domain.StorageRoot{}, err
	}
	defer rollbackConfiguration(tx)
	now := persistence.clock()().UTC()
	pendingRevision := pendingSnapshotRevision("storage_root", draft.ID.String())
	pendingSnapshot := sqliteSnapshotID("storage_root", draft.ID.String(), pendingRevision)
	if err := insertConfigSnapshot(ctx, tx, pendingSnapshot, "api", "api", pendingRevision, now, "{}", now); err != nil {
		return domain.StorageRoot{}, errConfigurationPersistence
	}
	created := now.Format(time.RFC3339Nano)
	capabilities, err := json.Marshal(draft.Capabilities)
	if err != nil {
		return domain.StorageRoot{}, errConfigurationPersistence
	}
	if len(capabilities) == 0 {
		capabilities = []byte("[]")
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO storage_roots (id, label, purpose, path, source, source_snapshot_id, revision, read_only, watch_enabled, watch_interval_seconds, capabilities_json, retired_at, created_at, updated_at) VALUES (?, ?, ?, ?, 'api', ?, 'pending', ?, ?, ?, ?, NULL, ?, ?)`, draft.ID.String(), draft.Label, string(draft.Purpose), draft.Path, pendingSnapshot, boolInt(draft.ReadOnly), boolInt(draft.Watch.Enabled), int64(draft.Watch.Interval/time.Second), string(capabilities), created, created); err != nil {
		return domain.StorageRoot{}, errConfigurationPersistence
	}
	result, err := mutate(withSQLiteTx(ctx, tx))
	if err != nil {
		return domain.StorageRoot{}, err
	}
	if err := persistStorageRoot(ctx, tx, result, "pending", now); err != nil {
		return domain.StorageRoot{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.StorageRoot{}, errConfigurationPersistence
	}
	return result, nil
}

func (persistence *sqliteConfigurationPersistence) updateStorageRoot(ctx context.Context, id domain.ConfigID, expectedRevision string, mutate func(context.Context) (domain.StorageRoot, error)) (domain.StorageRoot, error) {
	tx, err := persistence.begin(ctx)
	if err != nil {
		return domain.StorageRoot{}, err
	}
	defer rollbackConfiguration(tx)
	result, err := mutate(withSQLiteTx(ctx, tx))
	if err != nil {
		return domain.StorageRoot{}, err
	}
	if err := persistStorageRoot(ctx, tx, result, expectedRevision, persistence.clock()().UTC()); err != nil {
		return domain.StorageRoot{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.StorageRoot{}, errConfigurationPersistence
	}
	return result, nil
}

func (persistence *sqliteConfigurationPersistence) retireStorageRoot(ctx context.Context, id domain.ConfigID, expectedRevision string, mutate func(context.Context) error) error {
	tx, err := persistence.begin(ctx)
	if err != nil {
		return err
	}
	defer rollbackConfiguration(tx)
	if err := mutate(withSQLiteTx(ctx, tx)); err != nil {
		return err
	}
	retiredAt := persistence.clock()().UTC().Format(time.RFC3339Nano)
	result, err := tx.ExecContext(ctx, `UPDATE storage_roots SET retired_at = ?, updated_at = ? WHERE id = ? AND source = 'api' AND revision = ? AND retired_at IS NULL`, retiredAt, retiredAt, id.String(), expectedRevision)
	if err != nil {
		return errConfigurationPersistence
	}
	if count, err := result.RowsAffected(); err != nil || count != 1 {
		return errConfigurationPersistence
	}
	if err := tx.Commit(); err != nil {
		return errConfigurationPersistence
	}
	return nil
}

func (persistence *sqliteConfigurationPersistence) createPathMapping(ctx context.Context, draft domain.PathMapping, mutate func(context.Context) (domain.PathMapping, error)) (domain.PathMapping, error) {
	tx, err := persistence.begin(ctx)
	if err != nil {
		return domain.PathMapping{}, err
	}
	defer rollbackConfiguration(tx)
	now := persistence.clock()().UTC()
	pendingRevision := pendingSnapshotRevision("path_mapping", draft.ID.String())
	pendingSnapshot := sqliteSnapshotID("path_mapping", draft.ID.String(), pendingRevision)
	if err := insertConfigSnapshot(ctx, tx, pendingSnapshot, "api", "api", pendingRevision, now, "{}", now); err != nil {
		return domain.PathMapping{}, errConfigurationPersistence
	}
	created := now.Format(time.RFC3339Nano)
	if _, err := tx.ExecContext(ctx, `INSERT INTO path_mappings (id, connection_id, root_id, source_prefix, destination_prefix, source, source_snapshot_id, revision, retired_at, created_at, updated_at) VALUES (?, ?, ?, ?, ?, 'api', ?, 'pending', NULL, ?, ?)`, draft.ID.String(), draft.ConnectionID.String(), draft.RootID.String(), draft.SourcePrefix, draft.DestinationPrefix, pendingSnapshot, created, created); err != nil {
		return domain.PathMapping{}, errConfigurationPersistence
	}
	result, err := mutate(withSQLiteTx(ctx, tx))
	if err != nil {
		return domain.PathMapping{}, err
	}
	if err := persistPathMapping(ctx, tx, result, "pending", now); err != nil {
		return domain.PathMapping{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.PathMapping{}, errConfigurationPersistence
	}
	return result, nil
}

func (persistence *sqliteConfigurationPersistence) updatePathMapping(ctx context.Context, id domain.ConfigID, expectedRevision string, mutate func(context.Context) (domain.PathMapping, error)) (domain.PathMapping, error) {
	tx, err := persistence.begin(ctx)
	if err != nil {
		return domain.PathMapping{}, err
	}
	defer rollbackConfiguration(tx)
	result, err := mutate(withSQLiteTx(ctx, tx))
	if err != nil {
		return domain.PathMapping{}, err
	}
	if err := persistPathMapping(ctx, tx, result, expectedRevision, persistence.clock()().UTC()); err != nil {
		return domain.PathMapping{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.PathMapping{}, errConfigurationPersistence
	}
	return result, nil
}

func (persistence *sqliteConfigurationPersistence) retirePathMapping(ctx context.Context, id domain.ConfigID, expectedRevision string, mutate func(context.Context) error) error {
	tx, err := persistence.begin(ctx)
	if err != nil {
		return err
	}
	defer rollbackConfiguration(tx)
	if err := mutate(withSQLiteTx(ctx, tx)); err != nil {
		return err
	}
	retiredAt := persistence.clock()().UTC().Format(time.RFC3339Nano)
	result, err := tx.ExecContext(ctx, `UPDATE path_mappings SET retired_at = ?, updated_at = ? WHERE id = ? AND source = 'api' AND revision = ? AND retired_at IS NULL`, retiredAt, retiredAt, id.String(), expectedRevision)
	if err != nil {
		return errConfigurationPersistence
	}
	if count, err := result.RowsAffected(); err != nil || count != 1 {
		return errConfigurationPersistence
	}
	if err := tx.Commit(); err != nil {
		return errConfigurationPersistence
	}
	return nil
}

func insertConfigSnapshot(ctx context.Context, tx *sql.Tx, id, source, documentID, revision string, startupAt time.Time, effectiveJSON string, createdAt time.Time) error {
	if strings.TrimSpace(effectiveJSON) == "" || !json.Valid([]byte(effectiveJSON)) {
		return errConfigurationPersistence
	}
	startup := startupAt.UTC().Format(time.RFC3339Nano)
	if _, err := tx.ExecContext(ctx, `INSERT INTO config_snapshots (id, source, document_id, revision, startup_at, effective_json, created_at) VALUES (?, ?, ?, ?, ?, ?, ?) ON CONFLICT(id) DO NOTHING`, id, source, documentID, revision, startup, effectiveJSON, createdAt.UTC().Format(time.RFC3339Nano)); err != nil {
		return err
	}
	var existingSource, existingDocument, existingRevision, existingJSON string
	if err := tx.QueryRowContext(ctx, `SELECT source, document_id, revision, effective_json FROM config_snapshots WHERE id = ?`, id).Scan(&existingSource, &existingDocument, &existingRevision, &existingJSON); err != nil {
		return err
	}
	// startup_at belongs to the immutable snapshot and can legitimately differ
	// from the current process startup when a no-op update reuses the same
	// revision. It is written only when this snapshot ID is first created.
	if existingSource != source || existingDocument != documentID || existingRevision != revision || existingJSON != effectiveJSON {
		return errConfigurationPersistence
	}
	return nil
}

func sqliteSnapshotID(kind, id, revision string) string {
	digest := sha256.Sum256([]byte("api\x00" + kind + "\x00" + id + "\x00" + revision))
	return "api-" + hex.EncodeToString(digest[:])
}

func pendingSnapshotRevision(kind, id string) string {
	return "pending:" + kind + ":" + id
}

func persistConnection(ctx context.Context, tx *sql.Tx, value domain.Connection, expectedRevision string, now time.Time) error {
	if !value.ID.Valid() || value.Source.Source != domain.SourceAPI || strings.TrimSpace(value.Revision) == "" {
		return errConfigurationPersistence
	}
	snapshotID := sqliteSnapshotID("connection", value.ID.String(), value.Revision)
	effective, err := json.Marshal(struct {
		ID       string                `json:"id"`
		Kind     domain.ConnectionKind `json:"kind"`
		Label    string                `json:"label"`
		Endpoint string                `json:"endpoint"`
		Revision string                `json:"revision"`
	}{ID: value.ID.String(), Kind: value.Kind, Label: value.Label, Endpoint: value.Endpoint, Revision: value.Revision})
	if err != nil {
		return errConfigurationPersistence
	}
	if err := insertConfigSnapshot(ctx, tx, snapshotID, "api", "api", value.Revision, value.Source.StartupAt, string(effective), now); err != nil {
		return errConfigurationPersistence
	}
	var retired any
	if value.RetiredAt != nil {
		retired = value.RetiredAt.UTC().Format(time.RFC3339Nano)
	}
	result, err := tx.ExecContext(ctx, `UPDATE connections SET kind = ?, label = ?, endpoint = ?, source_snapshot_id = ?, revision = ?, retired_at = ?, updated_at = ? WHERE id = ? AND source = 'api' AND revision = ?`, string(value.Kind), value.Label, value.Endpoint, snapshotID, value.Revision, retired, now.UTC().Format(time.RFC3339Nano), value.ID.String(), expectedRevision)
	if err != nil {
		return errConfigurationPersistence
	}
	if count, err := result.RowsAffected(); err != nil || count != 1 {
		return errConfigurationPersistence
	}
	return nil
}

func persistStorageRoot(ctx context.Context, tx *sql.Tx, value domain.StorageRoot, expectedRevision string, now time.Time) error {
	if !value.ID.Valid() || value.Source.Source != domain.SourceAPI || strings.TrimSpace(value.Revision) == "" {
		return errConfigurationPersistence
	}
	snapshotID := sqliteSnapshotID("storage_root", value.ID.String(), value.Revision)
	effective, err := json.Marshal(struct {
		ID       string                `json:"id"`
		Label    string                `json:"label"`
		Purpose  domain.StoragePurpose `json:"purpose"`
		Path     string                `json:"path"`
		ReadOnly bool                  `json:"readOnly"`
		Revision string                `json:"revision"`
	}{ID: value.ID.String(), Label: value.Label, Purpose: value.Purpose, Path: value.Path, ReadOnly: value.ReadOnly, Revision: value.Revision})
	if err != nil {
		return errConfigurationPersistence
	}
	if err := insertConfigSnapshot(ctx, tx, snapshotID, "api", "api", value.Revision, value.Source.StartupAt, string(effective), now); err != nil {
		return errConfigurationPersistence
	}
	capabilities, err := json.Marshal(value.Capabilities)
	if err != nil {
		return errConfigurationPersistence
	}
	var retired any
	if value.RetiredAt != nil {
		retired = value.RetiredAt.UTC().Format(time.RFC3339Nano)
	}
	result, err := tx.ExecContext(ctx, `UPDATE storage_roots SET label = ?, purpose = ?, path = ?, source_snapshot_id = ?, revision = ?, read_only = ?, watch_enabled = ?, watch_interval_seconds = ?, capabilities_json = ?, retired_at = ?, updated_at = ? WHERE id = ? AND source = 'api' AND revision = ?`, value.Label, string(value.Purpose), value.Path, snapshotID, value.Revision, boolInt(value.ReadOnly), boolInt(value.Watch.Enabled), int64(value.Watch.Interval/time.Second), string(capabilities), retired, now.UTC().Format(time.RFC3339Nano), value.ID.String(), expectedRevision)
	if err != nil {
		return errConfigurationPersistence
	}
	if count, err := result.RowsAffected(); err != nil || count != 1 {
		return errConfigurationPersistence
	}
	return nil
}

func persistPathMapping(ctx context.Context, tx *sql.Tx, value domain.PathMapping, expectedRevision string, now time.Time) error {
	if !value.ID.Valid() || !value.ConnectionID.Valid() || !value.RootID.Valid() || value.Source.Source != domain.SourceAPI || strings.TrimSpace(value.Revision) == "" {
		return errConfigurationPersistence
	}
	snapshotID := sqliteSnapshotID("path_mapping", value.ID.String(), value.Revision)
	effective, err := json.Marshal(struct {
		ID                string `json:"id"`
		ConnectionID      string `json:"connectionId"`
		RootID            string `json:"rootId"`
		SourcePrefix      string `json:"sourcePrefix"`
		DestinationPrefix string `json:"destinationPrefix"`
		Revision          string `json:"revision"`
	}{ID: value.ID.String(), ConnectionID: value.ConnectionID.String(), RootID: value.RootID.String(), SourcePrefix: value.SourcePrefix, DestinationPrefix: value.DestinationPrefix, Revision: value.Revision})
	if err != nil {
		return errConfigurationPersistence
	}
	if err := insertConfigSnapshot(ctx, tx, snapshotID, "api", "api", value.Revision, value.Source.StartupAt, string(effective), now); err != nil {
		return errConfigurationPersistence
	}
	result, err := tx.ExecContext(ctx, `UPDATE path_mappings SET connection_id = ?, root_id = ?, source_prefix = ?, destination_prefix = ?, source_snapshot_id = ?, revision = ?, updated_at = ? WHERE id = ? AND source = 'api' AND revision = ?`, value.ConnectionID.String(), value.RootID.String(), value.SourcePrefix, value.DestinationPrefix, snapshotID, value.Revision, now.UTC().Format(time.RFC3339Nano), value.ID.String(), expectedRevision)
	if err != nil {
		return errConfigurationPersistence
	}
	if count, err := result.RowsAffected(); err != nil || count != 1 {
		return errConfigurationPersistence
	}
	return nil
}

func boolInt(value bool) int64 {
	if value {
		return 1
	}
	return 0
}

type sqliteIdempotencyPersistence struct {
	db  *sql.DB
	now func() time.Time
}

func newSQLiteIdempotencyPersistence(db *sql.DB, now func() time.Time) *transport.IdempotencyPersistence {
	persistence := &sqliteIdempotencyPersistence{db: db, now: now}
	return &transport.IdempotencyPersistence{Load: persistence.load, Save: persistence.save}
}

type sqliteIdempotencyResponse struct {
	Status  int                 `json:"status"`
	Headers map[string][]string `json:"headers,omitempty"`
	Body    []byte              `json:"body,omitempty"`
}

const maxPersistedIdempotencyBody = 4 << 20

func (persistence *sqliteIdempotencyPersistence) load(ctx context.Context, scope, key string) (transport.IdempotencyRecord, bool, error) {
	if persistence == nil || persistence.db == nil || strings.TrimSpace(scope) == "" || strings.TrimSpace(key) == "" {
		return transport.IdempotencyRecord{}, false, errConfigurationPersistence
	}
	var record transport.IdempotencyRecord
	var responseJSON string
	var expires sql.NullString
	err := persistence.db.QueryRowContext(ctx, `SELECT scope, idempotency_key, request_digest, status_code, resource_kind, resource_id, response_json, created_at, expires_at FROM idempotency_records WHERE scope = ? AND idempotency_key = ?`, scope, key).Scan(&record.Scope, &record.Key, &record.Digest, &record.Status, &record.ResourceKind, &record.ResourceID, &responseJSON, &record.CreatedAt, &expires)
	if errors.Is(err, sql.ErrNoRows) {
		return transport.IdempotencyRecord{}, false, nil
	}
	if err != nil {
		return transport.IdempotencyRecord{}, false, errConfigurationPersistence
	}
	if expires.Valid {
		record.ExpiresAt = expires.String
	}
	if record.Scope != scope || record.Key != key || strings.TrimSpace(record.Digest) == "" || record.Status < 100 || record.Status > 599 || strings.TrimSpace(record.ResourceKind) == "" || strings.TrimSpace(record.ResourceID) == "" || len(responseJSON) > maxPersistedIdempotencyBody*2 {
		return transport.IdempotencyRecord{}, false, errConfigurationPersistence
	}
	var response sqliteIdempotencyResponse
	if err := json.Unmarshal([]byte(responseJSON), &response); err != nil || response.Status != record.Status || len(response.Body) > maxPersistedIdempotencyBody {
		return transport.IdempotencyRecord{}, false, errConfigurationPersistence
	}
	record.Headers = sanitizeIdempotencyHeaders(response.Headers)
	record.Body = append([]byte(nil), response.Body...)
	return record, true, nil
}

func (persistence *sqliteIdempotencyPersistence) save(ctx context.Context, record transport.IdempotencyRecord) error {
	if persistence == nil || persistence.db == nil || strings.TrimSpace(record.Scope) == "" || strings.TrimSpace(record.Key) == "" || strings.TrimSpace(record.Digest) == "" || record.Status < 100 || record.Status > 599 || len(record.Body) > maxPersistedIdempotencyBody {
		return errConfigurationPersistence
	}
	if _, err := hex.DecodeString(record.Digest); err != nil {
		return errConfigurationPersistence
	}
	if record.ResourceKind == "" {
		record.ResourceKind = "http_response"
	}
	if record.ResourceID == "" {
		record.ResourceID = "http-response"
	}
	createdAt := record.CreatedAt
	if strings.TrimSpace(createdAt) == "" {
		createdAt = persistence.clock()().UTC().Format(time.RFC3339Nano)
	}
	responseJSON, err := json.Marshal(sqliteIdempotencyResponse{Status: record.Status, Headers: sanitizeIdempotencyHeaders(record.Headers), Body: append([]byte(nil), record.Body...)})
	if err != nil || len(responseJSON) > maxPersistedIdempotencyBody*2 {
		return errConfigurationPersistence
	}
	_, err = persistence.db.ExecContext(ctx, `INSERT OR IGNORE INTO idempotency_records (scope, idempotency_key, request_digest, status_code, resource_kind, resource_id, response_json, created_at, expires_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, record.Scope, record.Key, record.Digest, record.Status, record.ResourceKind, record.ResourceID, string(responseJSON), createdAt, nullableString(record.ExpiresAt))
	if err != nil {
		return errConfigurationPersistence
	}
	var existingDigest string
	if err := persistence.db.QueryRowContext(ctx, `SELECT request_digest FROM idempotency_records WHERE scope = ? AND idempotency_key = ?`, record.Scope, record.Key).Scan(&existingDigest); err != nil {
		return errConfigurationPersistence
	}
	if existingDigest != record.Digest {
		return errConfigurationPersistence
	}
	return nil
}

func (persistence *sqliteIdempotencyPersistence) clock() func() time.Time {
	if persistence != nil && persistence.now != nil {
		return persistence.now
	}
	return time.Now
}

func sanitizeIdempotencyHeaders(input map[string][]string) map[string][]string {
	result := make(map[string][]string)
	for key, values := range input {
		canonical := http.CanonicalHeaderKey(key)
		switch canonical {
		case "Cache-Control", "Content-Type", "ETag", "Location", "Retry-After", "Vary":
			result[canonical] = append([]string(nil), values...)
		}
	}
	return result
}

func nullableString(value string) any {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return value
}
