package e2e

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"strings"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/horchestra/pack/operator/bus"
	"github.com/horchestra/pack/operator/bus/bustest"
	"github.com/horchestra/pack/operator/db"
)

// The SQL bus passes the shared suite on PostgreSQL too; every subtest on a database of its own.
func TestSuitePostgres(t *testing.T) {
	dsn := postgres(t)
	bustest.Test(t, func(t *testing.T, n int) []bus.Bus {
		t.Helper()
		d, err := db.Open(context.Background(), freshDB(t, dsn))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { d.Close() })
		out := make([]bus.Bus, n)
		for i := range out {
			b := bus.NewSQL(d)
			b.Poll = 10 * time.Millisecond
			out[i] = b
		}
		return out
	})
}

// freshDB is a database of its own in the container of dsn, dropped with the test.
func freshDB(t *testing.T, dsn string) string {
	t.Helper()
	var b [6]byte
	if _, err := rand.Read(b[:]); err != nil {
		t.Fatal(err)
	}
	name := "t_" + hex.EncodeToString(b[:])
	admin, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	if _, err := admin.Exec(`create database "` + name + `"`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		admin, err := sql.Open("pgx", dsn)
		if err != nil {
			return
		}
		defer admin.Close()
		_, _ = admin.Exec(`drop database "` + name + `" with (force)`)
	})
	// the DSN with the database's name instead of the path's
	i := strings.LastIndex(dsn, "/")
	if j := strings.Index(dsn[i:], "?"); j >= 0 {
		return dsn[:i] + "/" + name + dsn[i+j:]
	}
	return dsn[:i] + "/" + name
}
