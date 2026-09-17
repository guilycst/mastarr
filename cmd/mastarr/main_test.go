package main

import (
	"context"
	"errors"
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
