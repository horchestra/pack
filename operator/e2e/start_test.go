// The operator on PostgreSQL: start, migrate, seed the policy, answer /api/version — the same as on SQLite
// (the module's own tests), here against a real database. testcontainers lives only in this module.
package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/horchestra/pack/operator/authz"
	"github.com/horchestra/pack/operator/config"
	"github.com/horchestra/pack/operator/db"
	"github.com/horchestra/pack/operator/server"
)

func postgres(t *testing.T) string {
	t.Helper()
	ctx := context.Background()
	pg, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image:        "postgres:16-alpine",
			Env:          map[string]string{"POSTGRES_PASSWORD": "pack", "POSTGRES_DB": "operator"},
			ExposedPorts: []string{"5432/tcp"},
			WaitingFor:   wait.ForLog("database system is ready to accept connections").WithOccurrence(2),
		},
		Started: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pg.Terminate(context.Background()) })
	host, err := pg.Host(ctx)
	if err != nil {
		t.Fatal(err)
	}
	port, err := pg.MappedPort(ctx, "5432")
	if err != nil {
		t.Fatal(err)
	}
	return fmt.Sprintf("postgres://postgres:pack@%s:%s/operator?sslmode=disable", host, port.Port())
}

func TestStartPostgres(t *testing.T) {
	dsn := postgres(t)
	ctx := context.Background()

	// the policy seeded on PostgreSQL enforces as on SQLite
	d, err := db.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	az, err := authz.New(d, "none", "dev")
	if err != nil {
		t.Fatal(err)
	}
	if err := az.SeedProject("dev"); err != nil {
		t.Fatal(err)
	}
	if err := az.Grant("alice", "approver", "dev"); err != nil {
		t.Fatal(err)
	}
	if ok, err := az.Enforce("alice", "dev", "release:/releases/prod", "approve"); err != nil || !ok {
		t.Fatalf("alice approves: %v, %v", ok, err)
	}
	d.Close()

	// and a serve answers /api/version
	pack := filepath.Join(t.TempDir(), "pack")
	if err := os.WriteFile(pack, []byte("#!/bin/sh\necho '{\"version\":\"0.0.0-test\",\"formats\":{\"json\":1,\"stream\":1}}'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{DB: dsn, Auth: "none", DevUser: "dev", Pack: pack,
		Projects: []config.Project{{ID: "dev", Repo: "ssh://git@example.org/packs.git", Branch: "main"}}}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	run, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- server.Start(run, cfg, ln) }()
	defer func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("the server: %v", err)
		}
	}()
	resp, err := http.Get("http://" + ln.Addr().String() + "/api/version")
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
	if doc.Pack.Version != "0.0.0-test" || len(doc.Operator) == 0 {
		t.Errorf("the version document: %+v", doc)
	}
}
