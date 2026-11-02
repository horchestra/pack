package cmd

import (
	"context"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"slices"

	"github.com/spf13/cobra"

	"github.com/horchestra/pack/internal/interrupt"
	"github.com/horchestra/pack/internal/oci"
	"github.com/horchestra/pack/internal/release"
	"github.com/horchestra/pack/internal/targets"
	"github.com/horchestra/pack/internal/workdir"
	"github.com/horchestra/pack/pkg/environment"
	"github.com/horchestra/pack/pkg/target"
)

var initUpgrade bool

func init() {
	initCmd.Flags().BoolVar(&initUpgrade, "upgrade", false, "take the digest the registry has for the tag now and the programs the sources give now, not the ones the lock says")
	rootCmd.AddCommand(initCmd)
}

var initCmd = &cobra.Command{
	Use:   "init [oci://registry/repo:tag]",
	Short: "bring the targets and the pack of the release into .pack and lock them",
	Args:  cobra.MaximumNArgs(1),
	Long: `Brings what the release needs into .pack: the programs of the targets the environment uses, from the source
release.yaml names for each (targets: {docker: https://…, k8s: oci://…}), into .pack/bin, and the pack, if it is in a
registry, with the packs it holds, into .pack/packs. release.lock.json keeps the checksum of each program and
which digest the tag meant today. apply and plan read the pack from there, so the same
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
		env, err := environment.Load(ctx, fresh.Config(), nil)
		if err != nil {
			return err
		}
		o := oci.FromEnv(env.Registry.PlainHTTP)
		lock, err := release.ReadLock(fresh.Dir)
		if err != nil {
			return err
		}
		if lock == nil {
			lock = &release.Lock{}
		}
		before := *lock
		before.Targets = maps.Clone(lock.Targets)

		if err := bringTargets(ctx, fresh, env, lock, o); err != nil {
			return err
		}

		ref, err := fresh.PackRef()
		if err != nil {
			return err
		}
		if !oci.IsRef(ref) {
			fmt.Printf("%s is a directory: nothing to bring\n", ref)
		} else {
			var pin string
			if lock.Pack == ref && !initUpgrade {
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
			lock.Pack, lock.Digest = ref, p.Digest
			fmt.Printf("%s locked at %s\n", ref, p.Digest)
		}
		if !reflect.DeepEqual(before, *lock) {
			return release.WriteLock(fresh.Dir, lock)
		}
		return nil
	}),
}

// bringTargets downloads the program of every target the environment uses that is not in pack itself, from the source
// release.yaml names for it, into .pack/bin; the lock keeps the checksum of what came. A program that is there with
// the checksum the lock has is not downloaded again; one that comes with another is refused unless --upgrade says the
// source moved.
func bringTargets(ctx context.Context, rel *release.Release, env *environment.Environment, lock *release.Lock, o oci.Options) error {
	used := map[string]bool{}
	if len(env.Target) > 0 {
		used[env.Target] = true
	}
	for _, t := range env.Targets {
		if len(t.Target) > 0 {
			used[t.Target] = true
		}
	}
	builtin := target.Names()
	bin := dir.BinPath()
	for _, name := range slices.Sorted(maps.Keys(used)) {
		if slices.Contains(builtin, name) {
			continue
		}
		file := filepath.Join(bin, targets.Binary(name))
		source := rel.Targets[name]
		if len(source) == 0 {
			if _, err := os.Stat(file); err != nil {
				return fmt.Errorf("target %s: no source for it in release.yaml (targets: {%s: https://… or oci://…}) and %s is not there", name, name, file)
			}
			fmt.Printf("target %s: %s, no source named\n", name, file)
			continue
		}
		locked, has := lock.Targets[name]
		same := has && locked.Source == source && !initUpgrade
		if same {
			if sum, err := targets.Sum(file); err == nil && sum == locked.SHA256 {
				fmt.Printf("target %s: up to date (sha256 %s)\n", name, sum)
				continue
			}
		}
		sum, err := targets.Fetch(ctx, name, source, bin, o)
		if err != nil {
			return err
		}
		if same && sum != locked.SHA256 {
			_ = os.Remove(file)
			return fmt.Errorf("target %s: %s now gives sha256 %s, the lock has %s — `pack init --upgrade` takes the new one", name, source, sum, locked.SHA256)
		}
		if lock.Targets == nil {
			lock.Targets = map[string]release.TargetLock{}
		}
		lock.Targets[name] = release.TargetLock{Source: source, SHA256: sum}
		fmt.Printf("target %s: downloaded from %s (sha256 %s)\n", name, source, sum)
	}
	return nil
}
