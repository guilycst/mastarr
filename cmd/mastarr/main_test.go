package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/guilycst/mastarr/internal/bootstrap"
	"github.com/guilycst/mastarr/internal/storage"
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
