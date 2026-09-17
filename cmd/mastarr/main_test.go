package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/guilycst/mastarr/internal/bootstrap"
	"github.com/guilycst/mastarr/internal/configuration"
	"github.com/guilycst/mastarr/internal/credentials"
	"github.com/guilycst/mastarr/internal/domain"
	"github.com/guilycst/mastarr/internal/storage"
	"github.com/guilycst/mastarr/internal/transport"
)

func testEnvironment(t *testing.T, dataDir, listenAddr string) bootstrap.Environment {
	t.Helper()
	return bootstrap.Environment{DataDir: dataDir, ListenAddr: listenAddr, UIListenAddr: ":0", LogLevel: "info"}
}

func freeListenAddr(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	return address
}

func waitReady(t *testing.T, address string) {
	t.Helper()
	client := &http.Client{Timeout: 100 * time.Millisecond}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		response, err := client.Get("http://" + address + "/health/ready")
		if err == nil {
			_ = response.Body.Close()
			if response.StatusCode == http.StatusOK {
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("server did not become ready at %s", address)
}

func httpJSON(t *testing.T, address, method, path string, body []byte, headers map[string]string) (int, http.Header, []byte) {
	t.Helper()
	request, err := http.NewRequest(method, "http://"+address+path, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	for key, value := range headers {
		request.Header.Set(key, value)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	data, readErr := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if readErr != nil {
		t.Fatal(readErr)
	}
	return response.StatusCode, response.Header, data
}

func TestRunBootstrapsPersistentKeyAndGracefullyShutsDown(t *testing.T) {
	dataDir := t.TempDir()
	address := freeListenAddr(t)
	environment := testEnvironment(t, dataDir, address)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Run(ctx, environment) }()
	waitReady(t, address)
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not stop after cancellation")
	}
	keyPath := filepath.Join(dataDir, "keys", "credentials.key")
	info, err := os.Stat(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("key permissions = %o, want 600", info.Mode().Perm())
	}
	if _, err := os.Stat(filepath.Join(dataDir, databaseName)); err != nil {
		t.Fatal(err)
	}
}

func TestRunReportsStorageLockWithoutLeakingPrivateDetails(t *testing.T) {
	dataDir := t.TempDir()
	firstAddress := freeListenAddr(t)
	secondAddress := freeListenAddr(t)
	firstContext, cancelFirst := context.WithCancel(context.Background())
	firstDone := make(chan error, 1)
	go func() { firstDone <- Run(firstContext, testEnvironment(t, dataDir, firstAddress)) }()
	waitReady(t, firstAddress)
	second := Run(context.Background(), testEnvironment(t, dataDir, secondAddress))
	if !errors.Is(second, storage.ErrAlreadyLocked) {
		t.Fatalf("second startup error = %v, want storage lock", second)
	}
	if strings.Contains(second.Error(), dataDir) || strings.Contains(second.Error(), "mastarr.sqlite") {
		t.Fatalf("startup error leaked private path: %v", second)
	}
	cancelFirst()
	if err := <-firstDone; err != nil {
		t.Fatal(err)
	}
}

func TestRunPersistsAPIConfigurationAndIdempotencyAcrossRestart(t *testing.T) {
	dataDir := t.TempDir()
	firstAddress := freeListenAddr(t)
	firstContext, cancelFirst := context.WithCancel(context.Background())
	firstDone := make(chan error, 1)
	go func() { firstDone <- Run(firstContext, testEnvironment(t, dataDir, firstAddress)) }()
	waitReady(t, firstAddress)

	body := []byte(`{"id":"persisted-qbt","kind":"qbittorrent","label":"qBittorrent","endpoint":"http://qbt.test","credentials":{"kind":"api_key","apiKey":"synthetic-api-key"}}`)
	request, err := http.NewRequest(http.MethodPost, "http://"+firstAddress+"/api/v1/connections", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", "persisted-create")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		cancelFirst()
		t.Fatal(err)
	}
	firstResponseBody, readErr := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if readErr != nil {
		cancelFirst()
		t.Fatal(readErr)
	}
	if response.StatusCode != http.StatusCreated {
		cancelFirst()
		t.Fatalf("create status = %d: %s", response.StatusCode, firstResponseBody)
	}
	rootStatus, _, rootBody := httpJSON(t, firstAddress, http.MethodPost, "/api/v1/storage-roots", []byte(`{"id":"persisted-library","label":"Library","purpose":"library","path":"`+filepath.ToSlash(filepath.Join(dataDir, "library"))+`"}`), map[string]string{"Idempotency-Key": "persisted-root"})
	if rootStatus != http.StatusCreated {
		cancelFirst()
		t.Fatalf("root status = %d: %s", rootStatus, rootBody)
	}
	mappingStatus, _, mappingBody := httpJSON(t, firstAddress, http.MethodPost, "/api/v1/path-mappings", []byte(`{"id":"persisted-mapping","connectionId":"persisted-qbt","rootId":"persisted-library","sourcePrefix":"/downloads","destinationPrefix":"library"}`), map[string]string{"Idempotency-Key": "persisted-mapping"})
	if mappingStatus != http.StatusCreated {
		cancelFirst()
		t.Fatalf("mapping status = %d: %s", mappingStatus, mappingBody)
	}
	cancelFirst()
	if err := <-firstDone; err != nil {
		t.Fatal(err)
	}

	secondAddress := freeListenAddr(t)
	secondContext, cancelSecond := context.WithCancel(context.Background())
	secondDone := make(chan error, 1)
	go func() { secondDone <- Run(secondContext, testEnvironment(t, dataDir, secondAddress)) }()
	waitReady(t, secondAddress)
	get, err := http.Get("http://" + secondAddress + "/api/v1/connections/persisted-qbt")
	if err != nil {
		cancelSecond()
		t.Fatal(err)
	}
	getBody, readErr := io.ReadAll(get.Body)
	_ = get.Body.Close()
	if readErr != nil {
		cancelSecond()
		t.Fatal(readErr)
	}
	if get.StatusCode != http.StatusOK || !bytes.Contains(getBody, []byte(`"id":"persisted-qbt"`)) || !bytes.Contains(getBody, []byte(`"credentialState":"managed"`)) {
		cancelSecond()
		t.Fatalf("persisted connection response = %d: %s", get.StatusCode, getBody)
	}
	rootGetStatus, _, rootGetBody := httpJSON(t, secondAddress, http.MethodGet, "/api/v1/storage-roots/persisted-library", nil, nil)
	if rootGetStatus != http.StatusOK || !bytes.Contains(rootGetBody, []byte(`"id":"persisted-library"`)) {
		cancelSecond()
		t.Fatalf("persisted root response = %d: %s", rootGetStatus, rootGetBody)
	}
	mappingGetStatus, _, mappingGetBody := httpJSON(t, secondAddress, http.MethodGet, "/api/v1/path-mappings/persisted-mapping", nil, nil)
	if mappingGetStatus != http.StatusOK || !bytes.Contains(mappingGetBody, []byte(`"id":"persisted-mapping"`)) {
		cancelSecond()
		t.Fatalf("persisted mapping response = %d: %s", mappingGetStatus, mappingGetBody)
	}
	patchRequest, err := http.NewRequest(http.MethodPatch, "http://"+secondAddress+"/api/v1/connections/persisted-qbt", bytes.NewReader([]byte(`{"label":"qBittorrent"}`)))
	if err != nil {
		cancelSecond()
		t.Fatal(err)
	}
	patchRequest.Header.Set("Content-Type", "application/json")
	patchRequest.Header.Set("Idempotency-Key", "persisted-noop-patch")
	patchRequest.Header.Set("If-Match", get.Header.Get("ETag"))
	patchResponse, err := http.DefaultClient.Do(patchRequest)
	if err != nil {
		cancelSecond()
		t.Fatal(err)
	}
	patchBody, readErr := io.ReadAll(patchResponse.Body)
	_ = patchResponse.Body.Close()
	if readErr != nil {
		cancelSecond()
		t.Fatal(readErr)
	}
	if patchResponse.StatusCode != http.StatusOK {
		cancelSecond()
		t.Fatalf("idempotent no-op patch status = %d: %s", patchResponse.StatusCode, patchBody)
	}
	replayRequest, err := http.NewRequest(http.MethodPost, "http://"+secondAddress+"/api/v1/connections", bytes.NewReader(body))
	if err != nil {
		cancelSecond()
		t.Fatal(err)
	}
	replayRequest.Header.Set("Content-Type", "application/json")
	replayRequest.Header.Set("Idempotency-Key", "persisted-create")
	replay, err := http.DefaultClient.Do(replayRequest)
	if err != nil {
		cancelSecond()
		t.Fatal(err)
	}
	replayBody, readErr := io.ReadAll(replay.Body)
	_ = replay.Body.Close()
	if readErr != nil {
		cancelSecond()
		t.Fatal(readErr)
	}
	if replay.StatusCode != http.StatusCreated || !bytes.Equal(replayBody, firstResponseBody) {
		cancelSecond()
		t.Fatalf("durable replay = %d %q, want %d %q", replay.StatusCode, replayBody, http.StatusCreated, firstResponseBody)
	}
	changedReplayBody := []byte(`{"id":"persisted-qbt","kind":"qbittorrent","label":"changed","endpoint":"http://qbt.test","credentials":{"kind":"api_key","apiKey":"synthetic-api-key"}}`)
	changedReplayRequest, err := http.NewRequest(http.MethodPost, "http://"+secondAddress+"/api/v1/connections", bytes.NewReader(changedReplayBody))
	if err != nil {
		cancelSecond()
		t.Fatal(err)
	}
	changedReplayRequest.Header.Set("Content-Type", "application/json")
	changedReplayRequest.Header.Set("Idempotency-Key", "persisted-create")
	changedReplay, err := http.DefaultClient.Do(changedReplayRequest)
	if err != nil {
		cancelSecond()
		t.Fatal(err)
	}
	changedReplayResponseBody, readErr := io.ReadAll(changedReplay.Body)
	_ = changedReplay.Body.Close()
	if readErr != nil {
		cancelSecond()
		t.Fatal(readErr)
	}
	if changedReplay.StatusCode != http.StatusConflict || !bytes.Contains(changedReplayResponseBody, []byte(`"idempotency_conflict"`)) {
		cancelSecond()
		t.Fatalf("changed durable replay = %d: %s", changedReplay.StatusCode, changedReplayResponseBody)
	}
	cancelSecond()
	if err := <-secondDone; err != nil {
		t.Fatal(err)
	}
}

func TestStartupErrorMessageIsSanitized(t *testing.T) {
	secret := "synthetic-secret-value"
	err := failStartup("configuration", errors.New(secret+" at /private/runtime/path"))
	if strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), "/private/runtime/path") {
		t.Fatalf("startup error leaked cause: %v", err)
	}
	if !errors.Is(err, ErrStartup) {
		t.Fatalf("startup error does not preserve category: %v", err)
	}
}

func TestSQLiteIdempotencyReservationAndCompletionSurviveRestart(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), databaseName)
	store, err := storage.Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	now := func() time.Time { return time.Date(2026, 9, 17, 10, 0, 0, 0, time.UTC) }
	persistence := newSQLiteIdempotencyPersistence(store.DB(), now)
	digest := strings.Repeat("ab", 32)
	reservation := transport.IdempotencyRecord{
		Scope:        "POST /api/v1/connections",
		Key:          "restart-safe",
		Digest:       digest,
		Status:       http.StatusProcessing,
		ResourceKind: "idempotency_reservation",
		ResourceID:   "http-restart-safe",
		CreatedAt:    now().Format(time.RFC3339Nano),
		State:        transport.IdempotencyStateReserved,
	}
	acquired, err := persistence.Reserve(context.Background(), reservation)
	if err != nil || !acquired {
		t.Fatalf("reserve = %t, %v; want acquired", acquired, err)
	}
	loaded, found, err := persistence.Load(context.Background(), reservation.Scope, reservation.Key)
	if err != nil || !found || !loaded.Pending || loaded.State != transport.IdempotencyStateReserved {
		t.Fatalf("reserved load = %#v, found=%t, err=%v; want pending reservation", loaded, found, err)
	}
	completed := reservation
	completed.State = transport.IdempotencyStateCompleted
	completed.Status = http.StatusCreated
	completed.ResourceKind = "connection"
	completed.ResourceID = "restart-safe"
	completed.Replayable = true
	completed.Headers = map[string][]string{"Content-Type": {"application/json"}}
	completed.Body = []byte(`{"id":"restart-safe"}`)
	if err := persistence.Complete(context.Background(), completed); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	restarted, err := storage.Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	reloadedPersistence := newSQLiteIdempotencyPersistence(restarted.DB(), now)
	reloaded, found, err := reloadedPersistence.Load(context.Background(), reservation.Scope, reservation.Key)
	if err != nil || !found || reloaded.Pending || !reloaded.Replayable || reloaded.State != transport.IdempotencyStateCompleted {
		t.Fatalf("completed load after restart = %#v, found=%t, err=%v; want replayable completion", reloaded, found, err)
	}
	if !bytes.Equal(reloaded.Body, completed.Body) || reloaded.Status != http.StatusCreated || reloaded.Digest != digest {
		t.Fatalf("completed response after restart = %#v; want status/body/digest preserved", reloaded)
	}
}

func TestSQLiteIdempotencyReleaseAllowsExactRetryAndPreservesConflict(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), databaseName)
	store, err := storage.Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	now := func() time.Time { return time.Date(2026, 9, 17, 10, 0, 0, 0, time.UTC) }
	persistence := newSQLiteIdempotencyPersistence(store.DB(), now)
	reservation := transport.IdempotencyRecord{
		Scope:        "POST /api/v1/connections",
		Key:          "release-and-retry",
		Digest:       strings.Repeat("cd", 32),
		Status:       http.StatusProcessing,
		ResourceKind: "idempotency_reservation",
		ResourceID:   "http-release-and-retry",
		CreatedAt:    now().Format(time.RFC3339Nano),
		State:        transport.IdempotencyStateReserved,
		AttemptID:    "attempt-one",
	}
	if acquired, err := persistence.Reserve(context.Background(), reservation); err != nil || !acquired {
		t.Fatalf("initial reserve = %t, %v", acquired, err)
	}
	if err := persistence.Release(context.Background(), reservation); err != nil {
		t.Fatal(err)
	}
	if released, found, err := persistence.Load(context.Background(), reservation.Scope, reservation.Key); err != nil || found || released.State != transport.IdempotencyStateReleased {
		t.Fatalf("released load = %#v, found=%t, err=%v; want no active reservation", released, found, err)
	}
	retry := reservation
	retry.AttemptID = "attempt-two"
	if acquired, err := persistence.Reserve(context.Background(), retry); err != nil || !acquired {
		t.Fatalf("retry reserve = %t, %v", acquired, err)
	}
	completed := retry
	completed.State = transport.IdempotencyStateCompleted
	completed.Status = http.StatusCreated
	completed.ResourceKind = "connection"
	completed.ResourceID = "release-and-retry"
	completed.Replayable = true
	completed.Body = []byte(`{"id":"release-and-retry"}`)
	if err := persistence.Complete(context.Background(), completed); err != nil {
		t.Fatal(err)
	}
	reloaded, found, err := persistence.Load(context.Background(), reservation.Scope, reservation.Key)
	if err != nil || !found || reloaded.State != transport.IdempotencyStateCompleted || !reloaded.Replayable {
		t.Fatalf("reloaded completion = %#v, found=%t, err=%v", reloaded, found, err)
	}
	conflict := retry
	conflict.Digest = strings.Repeat("ef", 32)
	if acquired, err := persistence.Reserve(context.Background(), conflict); !errors.Is(err, transport.ErrIdempotencyConflict) || acquired {
		t.Fatalf("changed digest reserve = %t, %v; want conflict", acquired, err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestPublicSQLiteRollbackReleasePersistsCreatedAtAndAllowsRetry(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), databaseName)
	store, err := storage.Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Date(2026, 9, 17, 10, 0, 0, 0, time.UTC)
	candidate, err := configuration.New(configuration.Options{Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	defer candidate.Close()
	reloaded, err := configuration.New(configuration.Options{Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	defer reloaded.Close()
	var failOnce atomic.Bool
	failOnce.Store(true)
	configurationPersistence := &transport.ConfigurationPersistence{
		CreateStorageRoot: func(ctx context.Context, _ domain.StorageRoot, mutate func(context.Context) (domain.StorageRoot, error)) (domain.StorageRoot, error) {
			root, mutateErr := mutate(ctx)
			if failOnce.CompareAndSwap(true, false) {
				return root, transport.ErrConfigurationStore
			}
			return root, mutateErr
		},
	}
	idempotency := newSQLiteIdempotencyPersistence(store.DB(), func() time.Time { return now })
	server, err := transport.New(transport.Options{
		Configuration:            candidate,
		ConfigurationPersistence: configurationPersistence,
		ConfigurationReload: func(context.Context) (*configuration.Manager, []domain.ConfigID, error) {
			return reloaded, nil, nil
		},
		IdempotencyPersistence: idempotency,
		Ready:                  true,
		Now:                    func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	body := `{"id":"sqlite-release-root","label":"Library","purpose":"library","path":"/synthetic/library"}`
	request := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/storage-roots", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Idempotency-Key", "sqlite-release")
		response := httptest.NewRecorder()
		server.Handler().ServeHTTP(response, req)
		return response
	}
	first := request()
	if first.Code != http.StatusServiceUnavailable || !bytes.Contains(first.Body.Bytes(), []byte(`"persistence_unavailable"`)) {
		t.Fatalf("rollback response = %d: %s", first.Code, first.Body.String())
	}
	released, found, err := idempotency.Load(context.Background(), "POST /api/v1/storage-roots", "sqlite-release")
	if err != nil || found || released.State != transport.IdempotencyStateReleased || strings.TrimSpace(released.CreatedAt) == "" || released.AttemptID == "" {
		t.Fatalf("released SQLite record = %#v, found=%t, err=%v; want timestamped release", released, found, err)
	}
	second := request()
	if second.Code != http.StatusCreated {
		t.Fatalf("same-key retry after SQLite release = %d: %s", second.Code, second.Body.String())
	}
	completed, found, err := idempotency.Load(context.Background(), "POST /api/v1/storage-roots", "sqlite-release")
	if err != nil || !found || completed.State != transport.IdempotencyStateCompleted || !completed.Replayable {
		t.Fatalf("completed SQLite retry record = %#v, found=%t, err=%v", completed, found, err)
	}
}

func TestRuntimeConfigurationOwnerSwapsAndClosesCredentialManagers(t *testing.T) {
	firstCrypt, err := credentials.NewManager(bytes.Repeat([]byte{0x11}, 32))
	if err != nil {
		t.Fatal(err)
	}
	first, err := configuration.New(configuration.Options{CredentialManager: firstCrypt})
	if err != nil {
		firstCrypt.Close()
		t.Fatal(err)
	}
	secondCrypt, err := credentials.NewManager(bytes.Repeat([]byte{0x22}, 32))
	if err != nil {
		_ = first.Close()
		t.Fatal(err)
	}
	second, err := configuration.New(configuration.Options{CredentialManager: secondCrypt})
	if err != nil {
		_ = first.Close()
		secondCrypt.Close()
		t.Fatal(err)
	}
	owner := &runtimeConfigurationOwner{manager: first, crypt: firstCrypt}
	replaced := owner.swap(second, secondCrypt)
	if replaced != first {
		t.Fatalf("replaced manager = %p, want first manager %p", replaced, first)
	}
	if err := replaced.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := secondCrypt.Seal("synthetic-connection", "token", []byte("synthetic-secret")); err != nil {
		t.Fatalf("replacement credential manager was closed by old manager: %v", err)
	}
	owner.close()
	owner.close()
	if _, err := secondCrypt.Seal("synthetic-connection", "token", []byte("synthetic-secret")); !errors.Is(err, credentials.ErrInvalidKey) {
		t.Fatalf("active credential manager after shutdown = %v, want invalid key", err)
	}
}
