package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/horchestra/pack/internal/interrupt"
	"github.com/horchestra/pack/internal/oci"
	"github.com/horchestra/pack/internal/release"
	"github.com/horchestra/pack/internal/workdir"
	"github.com/horchestra/pack/pkg/environment"
)

var initUpgrade bool

func init() {
	initCmd.Flags().BoolVar(&initUpgrade, "upgrade", false, "take the digest the registry has for the tag now, not the one the lock says")
	rootCmd.AddCommand(initCmd)
}

var initCmd = &cobra.Command{
	Use:   "init [oci://registry/repo:tag]",
	Short: "bring the pack of the release from a registry and lock its digest",
	Args:  cobra.MaximumNArgs(1),
	Long: `Brings the pack of the release from a registry into .pack/packs, with the packs it holds, and writes
release.lock.json: which digest the tag meant today. apply and plan read the pack from there, so the same
release gives the same result whatever the tag becomes. With an argument, release.yaml says it first
(pack: oci://…; the file is made if there is none). A pack that is a directory has nothing to bring.`,
	Run: run(func(_ *cobra.Command, args []string) error {
		ctx, stop := interrupt.Context()
		defer stop()
		rel := dir.Release
		if len(args) > 0 {
			if err := release.Update(rel.Dir, func(doc map[string]any) { doc["pack"] = args[0] }); err != nil {
				return err
			}
		}
		// a state kept in the target has no file to keep its id in: it is written here, once
		if rel.State == workdir.StateTarget && len(rel.ID) == 0 {
			id := workdir.NewID()
			if err := release.Update(rel.Dir, func(doc map[string]any) { doc["id"] = id }); err != nil {
				return err
			}
			fmt.Println("id:", id)
		}
		fresh, err := release.Load(rel.Dir, envName)
		if err != nil {
			return err
		}
		ref, err := fresh.PackRef()
		if err != nil {
			return err
		}
		if !oci.IsRef(ref) {
			fmt.Printf("%s is a directory: nothing to bring\n", ref)
			return nil
		}
		env, err := environment.Load(ctx, fresh.Config(), nil)
		if err != nil {
			return err
		}
		o := oci.FromEnv(env.Registry.PlainHTTP)
		lock, err := release.ReadLock(fresh.Dir)
		if err != nil {
			return err
		}
		var pin string
		if lock != nil && lock.Pack == ref && !initUpgrade {
			pin, err = oci.WithDigest(ref, lock.Digest)
		} else {
			pin, err = oci.Pin(ctx, ref, o)
		}
		if err != nil {
			return err
		}
		p, err := oci.Pull(ctx, pin, dir.PacksPath(), o)
		if err != nil {
			return err
		}
		if lock == nil || lock.Pack != ref || lock.Digest != p.Digest {
			if err := release.WriteLock(fresh.Dir, &release.Lock{Pack: ref, Digest: p.Digest}); err != nil {
				return err
			}
		}
		fmt.Printf("%s locked at %s\n", ref, p.Digest)
		return nil
	}),
}
