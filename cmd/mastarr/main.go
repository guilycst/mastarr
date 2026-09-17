// Command mastarr starts the API process.  It deliberately keeps bootstrap
// orchestration here: transport owns HTTP policy, while storage, credentials
// and configuration retain their package-specific invariants.
package main

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"sync"
	"syscall"
	"time"

	"github.com/guilycst/mastarr/internal/bootstrap"
	"github.com/guilycst/mastarr/internal/configuration"
	"github.com/guilycst/mastarr/internal/credentials"
	"github.com/guilycst/mastarr/internal/domain"
	"github.com/guilycst/mastarr/internal/storage"
	"github.com/guilycst/mastarr/internal/storage/sqlc"
	"github.com/guilycst/mastarr/internal/transport"
)

const (
	databaseName           = "mastarr.sqlite"
	maxPersistedConfigRows = int64(10_001)
	shutdownTimeout        = 10 * time.Second
)

var (
	// ErrStartup is a stable category for callers embedding the process.
	ErrStartup = errors.New("mastarr startup failed")
	// ErrServe is returned when the listener stops before shutdown was
	// requested.  The underlying net/http error is intentionally not exposed
	// in Error, but remains available to errors.Is callers.
	ErrServe = errors.New("mastarr HTTP server stopped")
)

// sqliteTransactionKey carries the transaction opened by an API-owned
// configuration persistence callback into the managed-credential store. The
// key is private to this package so an arbitrary caller cannot smuggle a
// transaction into configuration operations.
type sqliteTransactionKey struct{}

// runtimeConfigurationOwner keeps the credential manager and the
// configuration manager that owns it together. Reload swaps both as one
// ownership unit; closing a replaced configuration therefore zeroes only its
// own credential cache and cannot close the crypt manager used by its
// replacement. Shutdown detaches the current pair before closing it, making
// repeated shutdown paths harmless and ensuring the active manager closes
// exactly once.
type runtimeConfigurationOwner struct {
	mu      sync.Mutex
	manager *configuration.Manager
	crypt   *credentials.Manager
}

func (owner *runtimeConfigurationOwner) swap(manager *configuration.Manager, crypt *credentials.Manager) *configuration.Manager {
	if owner == nil {
		return nil
	}
	owner.mu.Lock()
	old := owner.manager
	owner.manager = manager
	owner.crypt = crypt
	owner.mu.Unlock()
	return old
}

func (owner *runtimeConfigurationOwner) close() {
	if owner == nil {
		return
	}
	owner.mu.Lock()
	manager := owner.manager
	crypt := owner.crypt
	owner.manager = nil
	owner.crypt = nil
	owner.mu.Unlock()
	if manager != nil {
		_ = manager.Close()
		return
	}
	if crypt != nil {
		crypt.Close()
	}
}

func withSQLiteTx(ctx context.Context, tx *sql.Tx) context.Context {
	if tx == nil {
		return ctx
	}
	return context.WithValue(ctx, sqliteTransactionKey{}, tx)
}

func sqliteTxFromContext(ctx context.Context) *sql.Tx {
	if ctx == nil {
		return nil
	}
	tx, _ := ctx.Value(sqliteTransactionKey{}).(*sql.Tx)
	return tx
}

// startupError preserves errors.Is identity without putting paths, SQL
// statements, endpoints or other private details into a user-facing message.
type startupError struct {
	stage string
	err   error
}

func (err *startupError) Error() string {
	if err == nil {
		return ErrStartup.Error()
	}
	return "mastarr startup failed during " + err.stage
}

func (err *startupError) Unwrap() error {
	if err == nil {
		return ErrStartup
	}
	return err.err
}

func (err *startupError) Is(target error) bool {
	if target == ErrStartup {
		return true
	}
	return err != nil && errors.Is(err.err, target)
}

func failStartup(stage string, cause error) error {
	if cause == nil {
		cause = ErrStartup
	}
	return &startupError{stage: stage, err: cause}
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	environment, err := bootstrap.Load()
	if err != nil {
		writeStartupError(failStartup("environment", err))
		os.Exit(1)
	}
	if err := Run(ctx, environment); err != nil {
		writeStartupError(err)
		os.Exit(1)
	}
}

// Run starts one API process and blocks until ctx is canceled or the HTTP
// listener fails.  It opens the listener before migrations so readiness can
// truthfully report 503 while the process is bootstrapping.
func Run(ctx context.Context, environment bootstrap.Environment) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := environment.Validate(); err != nil {
		return failStartup("environment", err)
	}
	if err := os.MkdirAll(environment.DataDir, 0o700); err != nil {
		return failStartup("data directory", err)
	}

	httpHandler, err := transport.New(transport.Options{
		AllowedOrigin:      environment.UIPublicOrigin,
		MaxManifestBytes:   bootstrap.DefaultBounds().MaxManifestBytes,
		MaxManifestEntries: bootstrap.DefaultBounds().MaxManifestEntries,
	})
	if err != nil {
		return failStartup("HTTP policy", err)
	}
	listener, err := net.Listen("tcp", environment.ListenAddr)
	if err != nil {
		return failStartup("listener", err)
	}
	httpServer := &http.Server{
		Addr:              environment.ListenAddr,
		Handler:           httpHandler.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       65 * time.Second,
		WriteTimeout:      65 * time.Second,
		IdleTimeout:       65 * time.Second,
	}
	serveDone := make(chan error, 1)
	go func() { serveDone <- httpServer.Serve(listener) }()

	store, err := storage.Open(filepath.Join(environment.DataDir, databaseName))
	if err != nil {
		_ = stopHTTP(httpServer)
		return failStartup("storage", err)
	}
	defer func() { _ = store.Close() }()

	existingCredentials, err := encryptedCredentialData(ctx, store.DB())
	if err != nil {
		_ = stopHTTP(httpServer)
		return failStartup("credential inventory", err)
	}
	crypt, key, err := credentials.Open(credentials.KeyOptions{
		DataDir:                environment.DataDir,
		EnvironmentValue:       environment.CredentialKey,
		EnvironmentProvided:    environment.CredentialKey != "",
		KeyFile:                environment.CredentialKeyFile,
		KeyFileProvided:        environment.CredentialKeyFile != "",
		ExistingCredentialData: existingCredentials,
	})
	if err != nil {
		_ = stopHTTP(httpServer)
		return failStartup("credential key", err)
	}
	keySource, keyPath := string(key.Source()), key.Path()
	key.Close()

	credentialStore := &sqlCredentialStore{db: store.DB(), now: time.Now}
	apiState, err := loadAPIState(ctx, store, time.Now().UTC())
	if err != nil {
		crypt.Close()
		_ = stopHTTP(httpServer)
		return failStartup("configuration state", err)
	}
	if err := verifyManagedCredentials(ctx, crypt, credentialStore, apiState); err != nil {
		crypt.Close()
		_ = stopHTTP(httpServer)
		return failStartup("credential readiness", err)
	}
	manager, err := configuration.New(configuration.Options{
		Now:               time.Now,
		YAMLPath:          environment.ConfigFile,
		APIState:          apiState,
		CredentialManager: crypt,
		CredentialStore:   credentialStore,
		KeySource:         keySource,
		KeyPath:           keyPath,
	})
	if err != nil {
		crypt.Close()
		_ = stopHTTP(httpServer)
		return failStartup("configuration", err)
	}
	owner := &runtimeConfigurationOwner{manager: manager, crypt: crypt}
	defer owner.close()
	httpHandler.SetConfigurationPersistence(newSQLiteConfigurationPersistence(store.DB(), time.Now))
	httpHandler.SetIdempotencyPersistence(newSQLiteIdempotencyPersistence(store.DB(), time.Now))
	httpHandler.SetConfigurationReload(func(reloadCtx context.Context) (*configuration.Manager, []domain.ConfigID, error) {
		// Rebuild from committed SQLite rows while the transport write gate is
		// held. Open a new crypt manager for every generation; sharing the old
		// manager would make Manager.Close on replacement invalidate active
		// credential operations.
		reloadedCrypt, reloadedKeySource, reloadedKeyPath, openErr := openRuntimeCredentials(reloadCtx, environment, store.DB())
		if openErr != nil {
			return nil, nil, openErr
		}
		reloadedState, reloadErr := loadAPIState(reloadCtx, store, time.Now().UTC())
		if reloadErr != nil {
			reloadedCrypt.Close()
			return nil, nil, reloadErr
		}
		if reloadErr := verifyManagedCredentials(reloadCtx, reloadedCrypt, credentialStore, reloadedState); reloadErr != nil {
			reloadedCrypt.Close()
			return nil, nil, reloadErr
		}
		reloadedManager, reloadErr := configuration.New(configuration.Options{
			Now:               time.Now,
			YAMLPath:          environment.ConfigFile,
			APIState:          reloadedState,
			CredentialManager: reloadedCrypt,
			CredentialStore:   credentialStore,
			KeySource:         reloadedKeySource,
			KeyPath:           reloadedKeyPath,
		})
		if reloadErr != nil {
			reloadedCrypt.Close()
			return nil, nil, reloadErr
		}
		if previous := owner.swap(reloadedManager, reloadedCrypt); previous != nil {
			_ = previous.Close()
		}
		managedIDs := make([]domain.ConfigID, 0, len(reloadedState.ManagedCredentialFields))
		for id := range reloadedState.ManagedCredentialFields {
			managedIDs = append(managedIDs, id)
		}
		return reloadedManager, managedIDs, nil
	})
	managedCredentialIDs := make([]domain.ConfigID, 0, len(apiState.ManagedCredentialFields))
	for id := range apiState.ManagedCredentialFields {
		managedCredentialIDs = append(managedCredentialIDs, id)
	}
	httpHandler.SetManagedCredentialIDs(managedCredentialIDs)
	httpHandler.SetConfiguration(manager)
	httpHandler.SetReady(true)

	select {
	case <-ctx.Done():
		httpHandler.SetReady(false)
		if err := stopHTTP(httpServer); err != nil {
			return failStartup("shutdown", err)
		}
		return nil
	case serveErr := <-serveDone:
		if errors.Is(serveErr, http.ErrServerClosed) {
			return nil
		}
		return failStartup("HTTP server", fmt.Errorf("%w: %w", ErrServe, serveErr))
	}
}

func writeStartupError(err error) {
	if err == nil {
		return
	}
	var startup *startupError
	if errors.As(err, &startup) {
		_, _ = fmt.Fprintln(os.Stderr, startup.Error())
		return
	}
	_, _ = fmt.Fprintln(os.Stderr, "mastarr startup failed")
}

func stopHTTP(server *http.Server) error {
	if server == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	if err := server.Shutdown(ctx); err != nil {
		_ = server.Close()
		return err
	}
	return nil
}

func encryptedCredentialData(ctx context.Context, db *sql.DB) (bool, error) {
	if err := contextDone(ctx); err != nil {
		return false, err
	}
	var present bool
	if err := db.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM encrypted_credentials)`).Scan(&present); err != nil {
		return false, errors.New("encrypted credential inventory failed")
	}
	return present, nil
}

func openRuntimeCredentials(ctx context.Context, environment bootstrap.Environment, db *sql.DB) (*credentials.Manager, string, string, error) {
	existing, err := encryptedCredentialData(ctx, db)
	if err != nil {
		return nil, "", "", err
	}
	crypt, key, err := credentials.Open(credentials.KeyOptions{
		DataDir:                environment.DataDir,
		EnvironmentValue:       environment.CredentialKey,
		EnvironmentProvided:    environment.CredentialKey != "",
		KeyFile:                environment.CredentialKeyFile,
		KeyFileProvided:        environment.CredentialKeyFile != "",
		ExistingCredentialData: existing,
	})
	if err != nil {
		return nil, "", "", err
	}
	keySource, keyPath := string(key.Source()), key.Path()
	key.Close()
	return crypt, keySource, keyPath, nil
}

// loadAPIState translates durable SQL rows into configuration's domain state.
// It intentionally imports sqlc only in cmd/mastarr; generated SQL models do
// not cross the transport or domain boundary.
func loadAPIState(ctx context.Context, store *storage.Store, startupAt time.Time) (configuration.APIState, error) {
	if err := contextDone(ctx); err != nil {
		return configuration.APIState{}, err
	}
	if store == nil || store.Queries() == nil {
		return configuration.APIState{}, errors.New("configuration storage is unavailable")
	}
	connections, err := store.Queries().ListConnections(ctx, &sqlc.ListConnectionsParams{IncludeRetired: true, Limit: maxPersistedConfigRows})
	if err != nil {
		return configuration.APIState{}, errors.New("configuration connections could not be read")
	}
	if len(connections) >= int(maxPersistedConfigRows) {
		return configuration.APIState{}, errors.New("configuration connection count exceeds startup bound")
	}
	roots, err := store.Queries().ListStorageRoots(ctx, &sqlc.ListStorageRootsParams{IncludeRetired: true, Limit: maxPersistedConfigRows})
	if err != nil {
		return configuration.APIState{}, errors.New("configuration storage roots could not be read")
	}
	if len(roots) >= int(maxPersistedConfigRows) {
		return configuration.APIState{}, errors.New("configuration storage root count exceeds startup bound")
	}
	mappings, err := store.Queries().ListPathMappings(ctx, &sqlc.ListPathMappingsParams{IncludeRetired: true, Limit: maxPersistedConfigRows})
	if err != nil {
		return configuration.APIState{}, errors.New("configuration path mappings could not be read")
	}
	if len(mappings) >= int(maxPersistedConfigRows) {
		return configuration.APIState{}, errors.New("configuration path mapping count exceeds startup bound")
	}
	state := configuration.APIState{
		ManagedCredentialFields:  make(map[domain.ConfigID][]string),
		ManagedCredentialDigests: make(map[domain.ConfigID]string),
	}
	for _, row := range connections {
		if row == nil || row.Source != string(domain.SourceAPI) {
			continue
		}
		retired, err := storedTime(row.RetiredAt)
		if err != nil {
			return configuration.APIState{}, err
		}
		state.Connections = append(state.Connections, domain.Connection{
			ID:        domain.ConfigID(row.ID),
			Kind:      domain.ConnectionKind(row.Kind),
			Label:     row.Label,
			Endpoint:  row.Endpoint,
			Source:    apiSource(row.Revision, startupAt),
			Revision:  row.Revision,
			RetiredAt: retired,
		})
		if retired != nil {
			continue
		}
		fields, digest, err := loadManagedCredentialMetadata(ctx, store, row.ID)
		if err != nil {
			return configuration.APIState{}, err
		}
		if len(fields) != 0 {
			state.ManagedCredentialFields[domain.ConfigID(row.ID)] = fields
			state.ManagedCredentialDigests[domain.ConfigID(row.ID)] = digest
		}
	}
	for _, row := range roots {
		if row == nil || row.Source != string(domain.SourceAPI) {
			continue
		}
		retired, err := storedTime(row.RetiredAt)
		if err != nil {
			return configuration.APIState{}, err
		}
		capabilities, err := decodeCapabilities(row.CapabilitiesJson)
		if err != nil {
			return configuration.APIState{}, err
		}
		state.StorageRoots = append(state.StorageRoots, domain.StorageRoot{
			ID:           domain.ConfigID(row.ID),
			Label:        row.Label,
			Purpose:      domain.StoragePurpose(row.Purpose),
			Path:         row.Path,
			Source:       apiSource(row.Revision, startupAt),
			Revision:     row.Revision,
			ReadOnly:     row.ReadOnly != 0,
			Capabilities: capabilities,
			Watch:        domain.WatchSettings{Enabled: row.WatchEnabled != 0, Interval: time.Duration(row.WatchIntervalSeconds) * time.Second},
			RetiredAt:    retired,
		})
	}
	for _, row := range mappings {
		if row == nil || row.Source != string(domain.SourceAPI) {
			continue
		}
		// PathMapping intentionally has no retired tombstone in the domain
		// snapshot. Keep retired rows in SQL history, but never reactivate one
		// during startup.
		if row.RetiredAt.Valid {
			continue
		}
		state.PathMappings = append(state.PathMappings, domain.PathMapping{
			ID:                domain.ConfigID(row.ID),
			ConnectionID:      domain.ConfigID(row.ConnectionID),
			SourcePrefix:      row.SourcePrefix,
			RootID:            domain.ConfigID(row.RootID),
			DestinationPrefix: row.DestinationPrefix,
			Source:            apiSource(row.Revision, startupAt),
			Revision:          row.Revision,
		})
	}
	return state, nil
}

func apiSource(revision string, startupAt time.Time) domain.SourceMetadata {
	return domain.SourceMetadata{
		Source:       domain.SourceAPI,
		Editable:     true,
		DocumentID:   "api",
		Revision:     revision,
		StartupAt:    startupAt.UTC(),
		ReloadPolicy: domain.ReloadOnRestart,
	}
}

func storedTime(value sql.NullString) (*time.Time, error) {
	if !value.Valid || value.String == "" {
		return nil, nil
	}
	parsed, err := time.Parse(time.RFC3339Nano, value.String)
	if err != nil || parsed.IsZero() {
		return nil, errors.New("stored configuration timestamp is invalid")
	}
	parsed = parsed.UTC()
	return &parsed, nil
}

func decodeCapabilities(value string) ([]domain.Capability, error) {
	if value == "" {
		value = "[]"
	}
	var capabilities []domain.Capability
	if err := json.Unmarshal([]byte(value), &capabilities); err != nil {
		return nil, errors.New("stored root capabilities are invalid")
	}
	for _, capability := range capabilities {
		if err := capability.Validate(); err != nil {
			return nil, errors.New("stored root capability is invalid")
		}
	}
	return capabilities, nil
}

func loadManagedCredentialMetadata(ctx context.Context, store *storage.Store, connectionID string) ([]string, string, error) {
	rows, err := store.Queries().ListEncryptedCredentials(ctx, connectionID)
	if err != nil {
		return nil, "", errors.New("encrypted credential metadata could not be read")
	}
	fields := make([]string, 0, len(rows))
	values := make(map[string]credentials.Envelope, len(rows))
	for _, row := range rows {
		if row == nil || row.Name == "" {
			return nil, "", errors.New("encrypted credential metadata is invalid")
		}
		envelope, err := store.Queries().GetEncryptedCredential(ctx, &sqlc.GetEncryptedCredentialParams{ConnectionID: connectionID, Name: row.Name})
		if err != nil || envelope == nil {
			return nil, "", errors.New("encrypted credential could not be read")
		}
		value := credentials.Envelope{Version: envelope.EnvelopeVersion, Nonce: envelope.Nonce, Ciphertext: envelope.Ciphertext, KeyFingerprint: envelope.KeyFingerprint}
		if err := value.Validate(); err != nil {
			return nil, "", errors.New("encrypted credential envelope is invalid")
		}
		fields = append(fields, row.Name)
		values[row.Name] = value
	}
	sort.Strings(fields)
	return fields, credentialEnvelopeDigest(values), nil
}

func credentialEnvelopeDigest(values map[string]credentials.Envelope) string {
	type item struct {
		Field          string
		Version        int64
		Nonce          []byte
		Ciphertext     []byte
		KeyFingerprint string
	}
	items := make([]item, 0, len(values))
	for field, value := range values {
		items = append(items, item{Field: field, Version: value.Version, Nonce: value.Nonce, Ciphertext: value.Ciphertext, KeyFingerprint: value.KeyFingerprint})
	}
	sort.Slice(items, func(left, right int) bool { return items[left].Field < items[right].Field })
	data, err := json.Marshal(items)
	if err != nil {
		return "sha256:invalid"
	}
	digest := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(digest[:])
}

func verifyManagedCredentials(ctx context.Context, crypt *credentials.Manager, store *sqlCredentialStore, state configuration.APIState) error {
	if crypt == nil || store == nil {
		return errors.New("credential services are unavailable")
	}
	for id := range state.ManagedCredentialFields {
		values, err := store.Load(ctx, id)
		if err != nil {
			return errors.New("managed credential data is unavailable")
		}
		for field, envelope := range values {
			plaintext, err := crypt.Open(envelope, id.String(), field)
			if err != nil {
				zeroBytes(plaintext)
				return errors.New("managed credential key verification failed")
			}
			zeroBytes(plaintext)
		}
	}
	return nil
}

type sqlCredentialStore struct {
	db  *sql.DB
	now func() time.Time
}

func (store *sqlCredentialStore) Load(ctx context.Context, connectionID domain.ConfigID) (map[string]credentials.Envelope, error) {
	if err := contextDone(ctx); err != nil {
		return nil, err
	}
	if store == nil || store.db == nil || !connectionID.Valid() {
		return nil, errors.New("credential store is unavailable")
	}
	var queryer interface {
		QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	} = store.db
	if tx := sqliteTxFromContext(ctx); tx != nil {
		queryer = tx
	}
	rows, err := queryer.QueryContext(ctx, `SELECT name, envelope_version, nonce, ciphertext, key_fingerprint FROM encrypted_credentials WHERE connection_id = ? ORDER BY name`, connectionID.String())
	if err != nil {
		return nil, errors.New("encrypted credentials could not be read")
	}
	defer rows.Close()
	values := make(map[string]credentials.Envelope)
	for rows.Next() {
		var name, fingerprint string
		var version int64
		var nonce, ciphertext []byte
		if err := rows.Scan(&name, &version, &nonce, &ciphertext, &fingerprint); err != nil {
			return nil, errors.New("encrypted credentials could not be decoded")
		}
		envelope := credentials.Envelope{Version: version, Nonce: append([]byte(nil), nonce...), Ciphertext: append([]byte(nil), ciphertext...), KeyFingerprint: fingerprint}
		if err := envelope.Validate(); err != nil {
			return nil, errors.New("encrypted credential envelope is invalid")
		}
		values[name] = envelope
	}
	if err := rows.Err(); err != nil {
		return nil, errors.New("encrypted credentials could not be read")
	}
	return values, nil
}

func (store *sqlCredentialStore) Replace(ctx context.Context, connectionID domain.ConfigID, values map[string]credentials.Envelope) error {
	if err := contextDone(ctx); err != nil {
		return err
	}
	if store == nil || store.db == nil || !connectionID.Valid() {
		return errors.New("credential store is unavailable")
	}
	now := time.Now
	if store.now != nil {
		now = store.now
	}
	tx := sqliteTxFromContext(ctx)
	owned := false
	if tx == nil {
		var err error
		tx, err = store.db.BeginTx(ctx, nil)
		if err != nil {
			return errors.New("credential transaction could not start")
		}
		owned = true
	}
	if owned {
		defer func() { _ = tx.Rollback() }()
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM encrypted_credentials WHERE connection_id = ?`, connectionID.String()); err != nil {
		return errors.New("credential replacement could not clear old values")
	}
	created := now().UTC().Format(time.RFC3339Nano)
	for field, envelope := range values {
		if err := envelope.Validate(); err != nil {
			return errors.New("credential replacement envelope is invalid")
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO encrypted_credentials (connection_id, name, envelope_version, nonce, ciphertext, key_fingerprint, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, connectionID.String(), field, envelope.Version, envelope.Nonce, envelope.Ciphertext, envelope.KeyFingerprint, created, created); err != nil {
			return errors.New("credential replacement could not be stored")
		}
	}
	if owned {
		if err := tx.Commit(); err != nil {
			return errors.New("credential replacement could not commit")
		}
	}
	return nil
}

func contextDone(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		return nil
	}
}

func zeroBytes(value []byte) {
	for index := range value {
		value[index] = 0
	}
}
