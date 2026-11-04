package bus_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/horchestra/pack-operator/bus"
	"github.com/horchestra/pack-operator/bus/bustest"
	"github.com/horchestra/pack-operator/db"
)

// The SQL bus passes the suite on SQLite; on PostgreSQL it is operator/e2e.
func TestSuiteSQLite(t *testing.T) {
	bustest.Test(t, func(t *testing.T, n int) []bus.Bus {
		t.Helper()
		d, err := db.Open(context.Background(), "sqlite://"+filepath.Join(t.TempDir(), "operator.db"))
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
