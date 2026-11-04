package e2e

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

// state: db on PostgreSQL: one installation from two checkouts of the release — the second apply reads what the
// first wrote to the state database and does nothing.
func TestStateInPostgres(t *testing.T) {
	ctx := context.Background()
	pg, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image:        "postgres:16-alpine",
			Env:          map[string]string{"POSTGRES_PASSWORD": "pack", "POSTGRES_DB": "pack"},
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
	t.Setenv("PACK_STATE_DSN", fmt.Sprintf("postgres://postgres:pack@%s:%s/pack?sslmode=disable", host, port.Port()))

	s := setup(t)
	api := freePort(t)
	ref := "oci://" + s.registry + "/" + openbaoRef
	id := "44444444-4444-4444-8444-444444444444"
	mk := func() string {
		dir := t.TempDir()
		write(t, filepath.Join(dir, "env.yaml"), strings.ReplaceAll(devEnv, "${API_PORT}", api))
		write(t, filepath.Join(dir, "values.yaml"), devValues)
		write(t, filepath.Join(dir, "release.yaml"), "pack: "+ref+"\nstate: db\nid: "+id+"\n")
		return dir
	}
	d1, d2 := mk(), mk()
	run(t, d1, nil, s.pack, "refresh")
	run(t, d2, nil, s.pack, "refresh")
	out := run(t, d1, nil, s.pack, "apply")
	if !strings.Contains(out, "installed openbao") {
		t.Fatalf("the first applies:\n%s", out)
	}
	out = run(t, d2, nil, s.pack, "apply")
	if !strings.Contains(out, "nothing to do") {
		t.Fatalf("the second, of another checkout, sees the installed:\n%s", out)
	}
	runs := run(t, d2, nil, s.pack, "log", "--list")
	if !strings.Contains(runs, "\n") && len(strings.TrimSpace(runs)) == 0 {
		t.Fatalf("the journal is the database's, read from another checkout:\n%s", runs)
	}
}
