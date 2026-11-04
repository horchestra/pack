package server

import (
	"context"
	"fmt"

	"github.com/horchestra/pack/pkg/logging"

	"github.com/horchestra/pack-operator/config"
	"github.com/horchestra/pack-operator/db"
	"github.com/horchestra/pack-operator/packbin"
)

// Worker is an executor of the operator's jobs: it checks what a job needs — the database the jobs come from,
// the pack binary they run — and waits for the executor of B6 to take them. Until then it idles, ready.
func Worker(ctx context.Context, cfg *config.Config) error {
	log := logging.For("worker")
	d, err := db.Open(ctx, cfg.DB)
	if err != nil {
		return fmt.Errorf("db: %w", err)
	}
	defer d.Close()
	pck, err := packbin.Version(ctx, cfg.Pack)
	if err != nil {
		return err
	}
	log.Info().Str("pack", pck.Version).Str("db", d.Driver()).Msg("ready — the executor comes with B6; idling")
	<-ctx.Done()
	return nil
}
