package server

import (
	"context"
	"encoding/json"

	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/horchestra/pack/operator/config"
)

// fakePack is a pack binary of a script: version --json and nothing more.
func fakePack(t *testing.T) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("a shell script")
	}
	p := filepath.Join(t.TempDir(), "pack")
	if err := os.WriteFile(p, []byte("#!/bin/sh\necho '{\"version\":\"0.0.0-test\",\"formats\":{\"json\":1,\"stream\":1}}'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

// start starts a server on SQLite with a fake pack, on a port of its own; it returns the base URL.
func start(t *testing.T, dsn string) string {
	t.Helper()
	cfg := &config.Config{
		Listen:  "127.0.0.1:0",
		DB:      dsn,
		Auth:    "none",
		DevUser: "dev",
		Pack:    fakePack(t),
		Projects: []config.Project{
			{ID: "dev", Repo: "ssh://git@example.org/packs.git", Branch: "main"},
		},
	}
	ln, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Start(ctx, cfg, ln) }()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("the server: %v", err)
		}
	})
	return "http://" + ln.Addr().String()
}

// A start on SQLite answers /api/version with the operator's and the pack's versions.
func TestStartSQLite(t *testing.T) {
	base := start(t, "sqlite://"+filepath.Join(t.TempDir(), "operator.db"))
	resp, err := http.Get(base + "/api/version")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var doc struct {
		Operator string `json:"operator"`
		Pack     struct {
			Version string `json:"version"`
		} `json:"pack"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&doc); err != nil {
		t.Fatal(err)
	}
	if doc.Operator != Version || doc.Pack.Version != "0.0.0-test" {
		t.Errorf("the version document: %+v", doc)
	}
}

// A start without a pack binary refuses to half-live: the error says what to do.
func TestStartWithoutPack(t *testing.T) {
	cfg := &config.Config{
		DB:      "sqlite://" + filepath.Join(t.TempDir(), "operator.db"),
		Auth:    "none",
		DevUser: "dev",
		Pack:    filepath.Join(t.TempDir(), "no-such-pack"),
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	err = Start(context.Background(), cfg, ln)
	if err == nil || !strings.Contains(err.Error(), "the pack binary") {
		t.Errorf("no pack, no start: %v", err)
	}
}

// The worker checks its ground too: database and pack, then it idles until stopped.
func TestWorker(t *testing.T) {
	cfg := &config.Config{
		DB:      "sqlite://" + filepath.Join(t.TempDir(), "operator.db"),
		Auth:    "none",
		DevUser: "dev",
		Pack:    fakePack(t),
		DataDir: t.TempDir(),
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Worker(ctx, cfg, WorkerOptions{}) }()
	select {
	case err := <-done:
		t.Fatalf("the worker ended by itself: %v", err)
	case <-time.After(2 * time.Second):
	}
	cancel()
	if err := <-done; err != nil {
		t.Errorf("the worker stopped: %v", err)
	}
}
