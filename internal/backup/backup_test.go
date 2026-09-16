package backup

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/guilycst/mastarr/internal/credentials"
	"github.com/guilycst/mastarr/internal/storage"
	_ "modernc.org/sqlite"
)

const syntheticTime = "2026-09-16T12:00:00Z"

type backupFixture struct {
	store          *storage.Store
	dataDir        string
	keyPath        string
	descriptorRoot string
	trashRoot      string
	trashRootID    string
}

func newBackupFixture(t *testing.T) *backupFixture {
	t.Helper()
	dataDir := t.TempDir()
	store, err := storage.Open(filepath.Join(dataDir, "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	manager, key, err := credentials.Open(credentials.KeyOptions{DataDir: dataDir})
	if err != nil {
		t.Fatal(err)
	}
	keyPath := key.Path()
	envelope, err := manager.Seal("synthetic-sonarr", "api_key", []byte("synthetic-secret"))
	if err != nil {
		manager.Close()
		key.Close()
		t.Fatal(err)
	}
	fingerprint := manager.Fingerprint()
	manager.Close()
	key.Close()

	const snapshotID = "synthetic-snapshot"
	const descriptorRootID = "synthetic-descriptors"
	const trashRootID = "synthetic-trash"
	mustExec(t, store.DB(), `INSERT INTO config_snapshots
		(id, source, document_id, revision, startup_at, effective_json, created_at)
		VALUES (?, 'api', 'backup-fixture', '1', ?, '{}', ?)`, snapshotID, syntheticTime, syntheticTime)
	for _, connectionID := range []string{"synthetic-sonarr", "synthetic-secondary"} {
		mustExec(t, store.DB(), `INSERT INTO connections
			(id, kind, label, endpoint, source, source_snapshot_id, revision, created_at, updated_at)
			VALUES (?, 'sonarr', ?, 'https://sonarr.invalid/api', 'api', ?, '1', ?, ?)`,
			connectionID, connectionID, snapshotID, syntheticTime, syntheticTime)
	}
	mustExec(t, store.DB(), `INSERT INTO storage_roots
		(id, label, purpose, path, source, source_snapshot_id, revision, capabilities_json, created_at, updated_at)
		VALUES (?, 'Synthetic descriptor root', 'descriptor', '/synthetic/descriptors', 'api', ?, '1', '[]', ?, ?),
		       (?, 'Synthetic trash root', 'trash', '/synthetic/trash', 'api', ?, '1', '[]', ?, ?)`,
		descriptorRootID, snapshotID, syntheticTime, syntheticTime,
		trashRootID, snapshotID, syntheticTime, syntheticTime)
	mustExec(t, store.DB(), `INSERT INTO encrypted_credentials
		(connection_id, name, envelope_version, nonce, ciphertext, key_fingerprint, created_at, updated_at)
		VALUES (?, 'api_key', ?, ?, ?, ?, ?, ?)`,
		"synthetic-sonarr", envelope.Version, envelope.Nonce, envelope.Ciphertext,
		fingerprint, syntheticTime, syntheticTime)

	descriptorRoot := filepath.Join(dataDir, "descriptors")
	if err := os.MkdirAll(descriptorRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	descriptorBytes := []byte("synthetic descriptor bytes")
	if err := os.WriteFile(filepath.Join(descriptorRoot, "release.nzb"), descriptorBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	descriptorDigest := sha256.Sum256(descriptorBytes)
	mustExec(t, store.DB(), `INSERT INTO descriptors
		(id, descriptor_type, storage_path, original_digest, capture_source, captured_at, retention)
		VALUES ('synthetic-descriptor', 'nzb', 'release.nzb', ?, 'synthetic-fixture', ?, 'retain')`,
		hex.EncodeToString(descriptorDigest[:]), syntheticTime)

	trashRoot := filepath.Join(dataDir, "trash")
	trashPayload := filepath.Join(trashRoot, "entry")
	if err := os.MkdirAll(trashPayload, 0o700); err != nil {
		t.Fatal(err)
	}
	trashBytes := []byte("synthetic movie payload")
	if err := os.WriteFile(filepath.Join(trashPayload, "movie.mkv"), trashBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	trashDigest := sha256.Sum256(trashBytes)
	manifestJSON := `[{"path":"movie.mkv","size":23}]`
	mustExec(t, store.DB(), `INSERT INTO trash_entries
		(id, root_id, state, original_prefix, trash_prefix, manifest_json, retention_seconds,
		 trashed_at, expires_at, client_state_json, created_at, updated_at)
		VALUES ('synthetic-trash-entry', ?, 'trashed', 'library/movie', 'entry', ?, 2592000,
		 ?, '2026-10-16T12:00:00Z', '{}', ?, ?)`,
		trashRootID, manifestJSON, syntheticTime, syntheticTime, syntheticTime)
	mustExec(t, store.DB(), `INSERT INTO trash_items
		(id, entry_id, root_id, original_relative_path, trash_relative_path, entry_type,
		 size_bytes, digest, state, trashed_at)
		VALUES ('synthetic-trash-item', 'synthetic-trash-entry', ?, 'movie.mkv',
		 'entry/movie.mkv', 'file', ?, ?, 'trashed', ?)`,
		trashRootID, len(trashBytes), hex.EncodeToString(trashDigest[:]), syntheticTime)

	mustExec(t, store.DB(), `INSERT INTO action_plans
		(id, kind, state, current_revision, current_digest, created_at, updated_at)
		VALUES ('synthetic-plan', 'fs.copy', 'ready', 1, 'synthetic-digest', ?, ?)`, syntheticTime, syntheticTime)
	mustExec(t, store.DB(), `INSERT INTO action_plan_revisions
		(plan_id, revision, digest, state, input_json, preconditions_json, capabilities_json,
		 manifest_json, created_at, expires_at, ready_at)
		VALUES ('synthetic-plan', 1, 'synthetic-digest', 'ready', '{}', '{}', '[]', '[]',
		 ?, '2026-10-16T12:00:00Z', ?)`, syntheticTime, syntheticTime)
	mustExec(t, store.DB(), `INSERT INTO action_runs
		(id, plan_id, plan_revision, plan_digest, state, desired_state_json, version,
		 outcome_json, unresolved_count, created_at, updated_at)
		VALUES ('synthetic-action', 'synthetic-plan', 1, 'synthetic-digest', 'reconciling',
		 '{}', 1, '{"certainty":"uncertain"}', 1, ?, ?)`, syntheticTime, syntheticTime)
	mustExec(t, store.DB(), `INSERT INTO idempotency_records
		(scope, idempotency_key, request_digest, status_code, resource_kind, resource_id,
		 response_json, created_at)
		VALUES ('synthetic', 'backup-key', 'synthetic-request', 200, 'action',
		 'synthetic-action', '{}', ?)`, syntheticTime)

	return &backupFixture{
		store:          store,
		dataDir:        dataDir,
		keyPath:        keyPath,
		descriptorRoot: descriptorRoot,
		trashRoot:      trashRoot,
		trashRootID:    trashRootID,
	}
}

func (fixture *backupFixture) source(quiesced bool) Source {
	return Source{
		Store:             fixture.store,
		CredentialKeyPath: fixture.keyPath,
		DescriptorRoot:    fixture.descriptorRoot,
		TrashRoots:        []Tree{{Name: fixture.trashRootID, Path: fixture.trashRoot}},
		Quiesced:          quiesced,
	}
}

func mustExec(t *testing.T, db *sql.DB, query string, args ...any) {
	t.Helper()
	if _, err := db.Exec(query, args...); err != nil {
		t.Fatal(err)
	}
}

func createAndRestoreFixture(t *testing.T) (string, string) {
	t.Helper()
	fixture := newBackupFixture(t)
	archive := filepath.Join(t.TempDir(), "archive")
	if _, err := Create(context.Background(), fixture.source(true), archive); err != nil {
		t.Fatal(err)
	}
	if err := fixture.store.Close(); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "restored")
	if _, err := Restore(context.Background(), archive, target); err != nil {
		t.Fatal(err)
	}
	return archive, target
}

func TestCreateRestoreAndVerifyPreserveRecoveryState(t *testing.T) {
	archive, target := createAndRestoreFixture(t)
	report, err := Verify(context.Background(), target)
	if err != nil {
		t.Fatal(err)
	}
	if report.SchemaVersion != 9 {
		t.Fatalf("schema version = %d, want 9", report.SchemaVersion)
	}
	if report.EncryptedCredentialCount != 1 {
		t.Fatalf("encrypted credentials = %d, want 1", report.EncryptedCredentialCount)
	}
	if report.DescriptorCount != 1 || report.TrashEntryCount != 1 || report.TrashItemCount != 1 {
		t.Fatalf("restored private state counts = descriptors %d, trash entries %d, trash items %d", report.DescriptorCount, report.TrashEntryCount, report.TrashItemCount)
	}
	if report.ActionRunCount != 1 || report.UnresolvedActionCount != 1 {
		t.Fatalf("recovery state counts = actions %d, unresolved %d", report.ActionRunCount, report.UnresolvedActionCount)
	}
	if report.IdempotencyRecordCount != 1 || !report.TrashRestoreReady {
		t.Fatalf("idempotency/trash readiness = %d/%t", report.IdempotencyRecordCount, report.TrashRestoreReady)
	}
	for _, path := range []string{
		filepath.Join(target, "keys", "credentials.key"),
		filepath.Join(target, "descriptors", "release.nzb"),
		filepath.Join(target, "trash", "synthetic-trash", "entry", "movie.mkv"),
	} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("restored artifact %q: %v", path, err)
		}
	}
	if _, err := os.Stat(filepath.Join(archive, manifestName)); err != nil {
		t.Fatalf("backup manifest: %v", err)
	}

	first, err := storage.Open(filepath.Join(target, databaseName))
	if err != nil {
		t.Fatalf("open verified database: %v", err)
	}
	if _, err := storage.Open(filepath.Join(target, databaseName)); !errors.Is(err, storage.ErrAlreadyLocked) {
		t.Fatalf("second restored database open error = %v, want ErrAlreadyLocked", err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestVerifyRejectsMissingWrongAndTamperedCredentialState(t *testing.T) {
	tests := []struct {
		name string
		edit func(t *testing.T, target string)
		want error
	}{
		{
			name: "missing key",
			edit: func(t *testing.T, target string) {
				t.Helper()
				keyPath := filepath.Join(target, filepath.FromSlash(credentialKeyName))
				if err := os.Remove(keyPath); err != nil {
					t.Fatal(err)
				}
			},
			want: ErrIncomplete,
		},
		{
			name: "wrong key",
			edit: func(t *testing.T, target string) {
				t.Helper()
				keyPath := filepath.Join(target, filepath.FromSlash(credentialKeyName))
				if err := os.WriteFile(keyPath, []byte("01234567890123456789012345678901"), 0o600); err != nil {
					t.Fatal(err)
				}
				rewriteManifestArtifact(t, target, credentialKeyName)
			},
			want: ErrCredentialVerification,
		},
		{
			name: "edited ciphertext",
			edit: func(t *testing.T, target string) {
				t.Helper()
				db := openMutableDatabase(t, filepath.Join(target, databaseName))
				mustExec(t, db, `UPDATE encrypted_credentials SET ciphertext = zeroblob(length(ciphertext))`)
				if err := db.Close(); err != nil {
					t.Fatal(err)
				}
				rewriteManifestArtifact(t, target, databaseName)
			},
			want: ErrCredentialVerification,
		},
		{
			name: "edited connection binding",
			edit: func(t *testing.T, target string) {
				t.Helper()
				db := openMutableDatabase(t, filepath.Join(target, databaseName))
				mustExec(t, db, `UPDATE encrypted_credentials SET connection_id = 'synthetic-secondary' WHERE connection_id = 'synthetic-sonarr'`)
				if err := db.Close(); err != nil {
					t.Fatal(err)
				}
				rewriteManifestArtifact(t, target, databaseName)
			},
			want: ErrCredentialVerification,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, target := createAndRestoreFixture(t)
			test.edit(t, target)
			_, err := Verify(context.Background(), target)
			if !errors.Is(err, test.want) {
				t.Fatalf("verify error = %v, want %v", err, test.want)
			}
			if test.name == "missing key" {
				if _, statErr := os.Stat(filepath.Join(target, filepath.FromSlash(credentialKeyName))); !errors.Is(statErr, os.ErrNotExist) {
					t.Fatalf("missing key was recreated, stat error = %v", statErr)
				}
			}
		})
	}
}

func TestVerifyRejectsEditedRetainedArtifact(t *testing.T) {
	_, target := createAndRestoreFixture(t)
	path := filepath.Join(target, descriptorName, "release.nzb")
	if err := os.WriteFile(path, []byte("edited descriptor bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Verify(context.Background(), target); !errors.Is(err, ErrIncomplete) {
		t.Fatalf("edited descriptor verify error = %v, want ErrIncomplete", err)
	}
}

func TestCreateRequiresQuiescenceCompleteSchemaAndPersistentArtifacts(t *testing.T) {
	fixture := newBackupFixture(t)
	t.Run("non-quiescent", func(t *testing.T) {
		_, err := Create(context.Background(), fixture.source(false), filepath.Join(t.TempDir(), "archive"))
		if !errors.Is(err, ErrNotQuiesced) {
			t.Fatalf("create error = %v, want ErrNotQuiesced", err)
		}
	})
	t.Run("missing-trash-root", func(t *testing.T) {
		source := fixture.source(true)
		source.TrashRoots = nil
		_, err := Create(context.Background(), source, filepath.Join(t.TempDir(), "archive"))
		if !errors.Is(err, ErrIncomplete) {
			t.Fatalf("create error = %v, want ErrIncomplete", err)
		}
	})
	t.Run("dirty-migration", func(t *testing.T) {
		mustExec(t, fixture.store.DB(), `UPDATE schema_migrations SET dirty = 1`)
		_, err := Create(context.Background(), fixture.source(true), filepath.Join(t.TempDir(), "archive"))
		if !errors.Is(err, ErrSchemaMismatch) {
			t.Fatalf("create error = %v, want ErrSchemaMismatch", err)
		}
	})
}

func TestRestoreRejectsManifestSchemaDowngradeWithoutPublishing(t *testing.T) {
	archive, _ := createAndRestoreFixture(t)
	manifestPath := filepath.Join(archive, manifestName)
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	var manifest Manifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatal(err)
	}
	manifest.SchemaVersion--
	data, err = json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manifestPath, append(data, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "restored")
	if _, err := Restore(context.Background(), archive, target); !errors.Is(err, ErrSchemaMismatch) {
		t.Fatalf("restore error = %v, want ErrSchemaMismatch", err)
	}
	if _, err := os.Stat(target); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed restore published target, stat error = %v", err)
	}
}

func TestCreateRejectsInMemoryDatabase(t *testing.T) {
	dataDir := t.TempDir()
	store, err := storage.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = store.Close()
		_ = os.Remove(":memory:.lock")
	})
	manager, key, err := credentials.Open(credentials.KeyOptions{DataDir: dataDir})
	if err != nil {
		t.Fatal(err)
	}
	keyPath := key.Path()
	manager.Close()
	key.Close()
	descriptorRoot := filepath.Join(dataDir, "descriptors")
	trashRoot := filepath.Join(dataDir, "trash")
	for _, path := range []string{descriptorRoot, trashRoot} {
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	_, err = Create(context.Background(), Source{
		Store:             store,
		CredentialKeyPath: keyPath,
		DescriptorRoot:    descriptorRoot,
		TrashRoots:        []Tree{{Name: "synthetic", Path: trashRoot}},
		Quiesced:          true,
	}, filepath.Join(t.TempDir(), "archive"))
	if !errors.Is(err, ErrIncomplete) {
		t.Fatalf("in-memory create error = %v, want ErrIncomplete", err)
	}
}

func TestCreatePublishesOnlyAfterPrivateVerification(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(t *testing.T, fixture *backupFixture, source *Source)
		want   error
	}{
		{
			name: "wrong key",
			mutate: func(t *testing.T, fixture *backupFixture, source *Source) {
				t.Helper()
				wrongDir := t.TempDir()
				manager, key, err := credentials.Open(credentials.KeyOptions{DataDir: wrongDir})
				if err != nil {
					t.Fatal(err)
				}
				manager.Close()
				source.CredentialKeyPath = key.Path()
				key.Close()
			},
			want: ErrCredentialVerification,
		},
		{
			name: "missing descriptor reference",
			mutate: func(t *testing.T, fixture *backupFixture, _ *Source) {
				t.Helper()
				mustExec(t, fixture.store.DB(), `UPDATE descriptors SET storage_path = 'missing.nzb'`)
			},
			want: ErrIncomplete,
		},
		{
			name: "missing trash reference",
			mutate: func(t *testing.T, fixture *backupFixture, _ *Source) {
				t.Helper()
				mustExec(t, fixture.store.DB(), `UPDATE trash_items SET trash_relative_path = 'entry/missing.mkv'`)
			},
			want: ErrIncomplete,
		},
		{
			name: "manifest limit",
			mutate: func(t *testing.T, _ *backupFixture, source *Source) {
				t.Helper()
				source.Limits.MaxManifestBytes = 1
			},
			want: ErrIncomplete,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newBackupFixture(t)
			source := fixture.source(true)
			test.mutate(t, fixture, &source)
			destination := filepath.Join(t.TempDir(), "archive")
			_, err := Create(context.Background(), source, destination)
			if !errors.Is(err, test.want) {
				t.Fatalf("create error = %v, want %v", err, test.want)
			}
			if _, statErr := os.Stat(destination); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("failed create published destination, stat error = %v", statErr)
			}
		})
	}
}

func TestCreateHonorsAggregateLimits(t *testing.T) {
	tests := []struct {
		name  string
		limit Limits
	}{
		{name: "entries", limit: Limits{MaxEntries: 1}},
		{name: "trees", limit: Limits{MaxTrees: 1}},
		{name: "bytes", limit: Limits{MaxBytes: 1}},
		{name: "manifest", limit: Limits{MaxManifestBytes: 1}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newBackupFixture(t)
			source := fixture.source(true)
			source.Limits = test.limit
			destination := filepath.Join(t.TempDir(), "archive")
			_, err := Create(context.Background(), source, destination)
			if !errors.Is(err, ErrIncomplete) {
				t.Fatalf("create error = %v, want ErrIncomplete", err)
			}
			if _, statErr := os.Stat(destination); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("limited create published destination, stat error = %v", statErr)
			}
		})
	}
}

func TestRestoreRejectsFutureSchemaBeforePublication(t *testing.T) {
	fixture := newBackupFixture(t)
	archive := filepath.Join(t.TempDir(), "archive")
	if _, err := Create(context.Background(), fixture.source(true), archive); err != nil {
		t.Fatal(err)
	}
	if err := fixture.store.Close(); err != nil {
		t.Fatal(err)
	}
	manifestPath := filepath.Join(archive, manifestName)
	descriptorBefore, err := os.ReadFile(filepath.Join(archive, descriptorName, "release.nzb"))
	if err != nil {
		t.Fatal(err)
	}
	db := openMutableDatabase(t, filepath.Join(archive, databaseName))
	mustExec(t, db, `UPDATE schema_migrations SET version = 999, dirty = 0`)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	rewriteManifestArtifact(t, archive, databaseName)
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	var manifest Manifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatal(err)
	}
	manifest.SchemaVersion = 999
	data, err = json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manifestPath, append(data, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	manifestBefore, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "restored")
	if _, err := Restore(context.Background(), archive, target); !errors.Is(err, ErrSchemaMismatch) {
		t.Fatalf("future-schema restore error = %v, want ErrSchemaMismatch", err)
	}
	if _, err := os.Stat(target); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("future-schema restore published destination, stat error = %v", err)
	}
	manifestAfter, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	descriptorAfter, err := os.ReadFile(filepath.Join(archive, descriptorName, "release.nzb"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(manifestBefore, manifestAfter) {
		t.Fatal("future-schema refusal changed archive manifest")
	}
	if !bytes.Equal(descriptorBefore, descriptorAfter) {
		t.Fatal("future-schema refusal changed archive descriptor")
	}
	if _, err := supportedMigrationCeiling(); err != nil {
		t.Fatal(err)
	}
}

func TestRestoreRejectsArchiveDestinationOverlapAndAliases(t *testing.T) {
	archive, _ := createAndRestoreFixture(t)
	manifestPath := filepath.Join(archive, manifestName)
	manifestBefore, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	parent := filepath.Dir(archive)
	realAliasRoot := t.TempDir()
	aliasArchive := filepath.Join(realAliasRoot, "archive-alias")
	if err := os.Symlink(archive, aliasArchive); err != nil {
		t.Fatal(err)
	}
	realParent := t.TempDir()
	aliasParent := filepath.Join(realParent, "parent-alias")
	if err := os.Symlink(parent, aliasParent); err != nil {
		t.Fatal(err)
	}
	lexicalParent := t.TempDir()
	lexicalDestination := lexicalParent + string(os.PathSeparator) + "." + string(os.PathSeparator) + "restored"
	tests := []struct {
		name        string
		archive     string
		destination string
		want        []error
	}{
		{name: "target under archive", archive: archive, destination: filepath.Join(archive, "restored"), want: []error{ErrIncomplete}},
		{name: "equal path", archive: archive, destination: archive, want: []error{ErrDestinationExists}},
		{name: "archive under existing target", archive: archive, destination: parent, want: []error{ErrDestinationExists}},
		{name: "archive symlink alias", archive: aliasArchive, destination: filepath.Join(t.TempDir(), "restored"), want: []error{ErrInvalidManifest}},
		{name: "destination parent symlink alias", archive: archive, destination: filepath.Join(aliasParent, "restored"), want: []error{ErrIncomplete}},
		{name: "destination lexical alias", archive: archive, destination: lexicalDestination, want: []error{ErrIncomplete}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := Restore(context.Background(), test.archive, test.destination)
			matched := false
			for _, want := range test.want {
				if errors.Is(err, want) {
					matched = true
					break
				}
			}
			if !matched {
				t.Fatalf("overlap restore error = %v, want one of %v", err, test.want)
			}
		})
	}
	manifestAfter, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(manifestBefore, manifestAfter) {
		t.Fatal("overlap refusal changed archive manifest")
	}
	if overlap, err := pathsOverlapResolved(archive, filepath.Join(archive, "nested")); err != nil || !overlap {
		t.Fatalf("resolved archive overlap = %t, err = %v", overlap, err)
	}
}

func TestCreateRejectsSymlinkAndLexicalSourceAliases(t *testing.T) {
	tests := []struct {
		name  string
		setup func(t *testing.T, fixture *backupFixture, source *Source)
	}{
		{
			name: "descriptor symlink",
			setup: func(t *testing.T, fixture *backupFixture, source *Source) {
				t.Helper()
				alias := filepath.Join(t.TempDir(), "descriptors-alias")
				if err := os.Symlink(fixture.descriptorRoot, alias); err != nil {
					t.Fatal(err)
				}
				source.DescriptorRoot = alias
			},
		},
		{
			name: "descriptor lexical alias",
			setup: func(t *testing.T, fixture *backupFixture, source *Source) {
				t.Helper()
				source.DescriptorRoot = fixture.descriptorRoot + string(os.PathSeparator) + "."
			},
		},
		{
			name: "trash symlink",
			setup: func(t *testing.T, fixture *backupFixture, source *Source) {
				t.Helper()
				alias := filepath.Join(t.TempDir(), "trash-alias")
				if err := os.Symlink(fixture.trashRoot, alias); err != nil {
					t.Fatal(err)
				}
				source.TrashRoots[0].Path = alias
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newBackupFixture(t)
			source := fixture.source(true)
			test.setup(t, fixture, &source)
			destination := filepath.Join(t.TempDir(), "archive")
			_, err := Create(context.Background(), source, destination)
			if !errors.Is(err, ErrIncomplete) {
				t.Fatalf("aliased source error = %v, want ErrIncomplete", err)
			}
			if _, statErr := os.Stat(destination); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("aliased source published destination, stat error = %v", statErr)
			}
		})
	}
}

func TestAtomicPublicationRejectsExistingDestination(t *testing.T) {
	parent := t.TempDir()
	staging := filepath.Join(parent, "stage")
	destination := filepath.Join(parent, "destination")
	if err := os.Mkdir(staging, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(staging, "marker"), []byte("stage"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(destination, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(destination, "marker"), []byte("destination"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := publishDirectory(staging, destination, parent)
	if errors.Is(err, ErrPublicationUnsupported) {
		t.Skipf("platform does not provide no-replace directory publication: %v", err)
	}
	if !errors.Is(err, ErrDestinationExists) {
		t.Fatalf("publication error = %v, want ErrDestinationExists", err)
	}
	if data, readErr := os.ReadFile(filepath.Join(destination, "marker")); readErr != nil || string(data) != "destination" {
		t.Fatalf("existing destination changed: data=%q err=%v", data, readErr)
	}
	if _, statErr := os.Stat(filepath.Join(staging, "marker")); statErr != nil {
		t.Fatalf("staging tree disappeared after collision: %v", statErr)
	}
}

func TestPublicationParentSyncFailureIsUncertainAndReconciles(t *testing.T) {
	previous := syncPublishedParent
	syncPublishedParent = func(string) error { return errors.New("synthetic parent sync failure") }
	t.Cleanup(func() { syncPublishedParent = previous })
	fixture := newBackupFixture(t)
	destination := filepath.Join(t.TempDir(), "archive")
	_, err := Create(context.Background(), fixture.source(true), destination)
	if !errors.Is(err, ErrPublicationUncertain) {
		t.Fatalf("create error = %v, want ErrPublicationUncertain", err)
	}
	if _, statErr := os.Stat(destination); statErr != nil {
		t.Fatalf("uncertain publication destination missing: %v", statErr)
	}
	if _, verifyErr := Verify(context.Background(), destination); verifyErr != nil {
		t.Fatalf("uncertain destination did not reconcile: %v", verifyErr)
	}
}

func TestFilesystemWorkHonorsCancellationAndLeavesNoDestination(t *testing.T) {
	tests := []struct {
		name   string
		build  func(context.Context, *backupFixture, string) error
		cancel string
	}{
		{
			name: "mid-copy",
			build: func(ctx context.Context, fixture *backupFixture, destination string) error {
				_, err := Create(ctx, fixture.source(true), destination)
				return err
			},
			cancel: "copy",
		},
		{
			name: "mid-walk",
			build: func(ctx context.Context, fixture *backupFixture, destination string) error {
				source := fixture.source(true)
				_, err := Create(ctx, source, destination)
				return err
			},
			cancel: "walk",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newBackupFixture(t)
			ctx, cancel := context.WithCancel(context.Background())
			observer := progressObserver{}
			if test.cancel == "copy" {
				observer.CopyChunk = cancel
			} else {
				observer.WalkEntry = cancel
			}
			ctx = context.WithValue(ctx, progressObserverKey{}, observer)
			destination := filepath.Join(t.TempDir(), "archive")
			err := test.build(ctx, fixture, destination)
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("canceled operation error = %v, want context.Canceled", err)
			}
			if _, statErr := os.Stat(destination); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("canceled operation published destination, stat error = %v", statErr)
			}
			cancel()
		})
	}
}

func TestRestoreHonorsMidCopyCancellation(t *testing.T) {
	fixture := newBackupFixture(t)
	archive := filepath.Join(t.TempDir(), "archive")
	if _, err := Create(context.Background(), fixture.source(true), archive); err != nil {
		t.Fatal(err)
	}
	if err := fixture.store.Close(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	ctx = context.WithValue(ctx, progressObserverKey{}, progressObserver{CopyChunk: cancel})
	target := filepath.Join(t.TempDir(), "restored")
	_, err := Restore(ctx, archive, target)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled restore error = %v, want context.Canceled", err)
	}
	if _, statErr := os.Stat(target); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("canceled restore published target, stat error = %v", statErr)
	}
}

func openMutableDatabase(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path)+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	if err := db.Ping(); err != nil {
		db.Close()
		t.Fatal(err)
	}
	return db
}

func rewriteManifestArtifact(t *testing.T, target, relative string) {
	t.Helper()
	manifestPath := filepath.Join(target, manifestName)
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	var manifest Manifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatal(err)
	}
	artifact, err := fileArtifact(filepath.Join(target, filepath.FromSlash(relative)), relative)
	if err != nil {
		t.Fatal(err)
	}
	switch relative {
	case databaseName:
		manifest.Database = artifact
	case credentialKeyName:
		manifest.CredentialKey = artifact
	default:
		t.Fatalf("unsupported manifest artifact %q", relative)
	}
	data, err = json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manifestPath, append(data, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
}
