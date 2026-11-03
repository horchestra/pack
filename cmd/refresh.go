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

var refreshUpgrade bool

func init() {
	refreshCmd.Flags().BoolVar(&refreshUpgrade, "upgrade", false, "take the digest the registry has for the tag now and the programs the sources give now, not the ones the lock says")
	rootCmd.AddCommand(refreshCmd)
}

var refreshCmd = &cobra.Command{
	Use:   "refresh",
	Short: "bring the targets and the pack of the release into .pack and lock them",
	Args:  cobra.NoArgs,
	Long: `Brings what the release needs into .pack: the programs of the targets the environment uses, from the source
release.yaml names for each (targets: {docker: https://…, k8s: oci://…}), into .pack/bin, and the pack, if it is in a
registry, with the packs it holds, into .pack/packs. release.lock.json keeps the checksum of each program and
which digest the tag meant today. apply and plan read the pack from there, so the same
release gives the same result whatever the tag becomes. A pack that is a directory has nothing to
bring. It is safe to run again: what is there with the locked checksum stays (pack init makes a
release; refresh brings what one names, after a clone or a change of release.yaml).`,
	Run: run(func(*cobra.Command, []string) error {
		ctx, stop := interrupt.Context()
		defer stop()
		rel := dir.Release
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
		lock, err := release.ReadLock(fresh.Dir)
		if err != nil {
			return err
		}
		if lock == nil {
			lock = &release.Lock{}
		}
		before := *lock
		before.Targets = maps.Clone(lock.Targets)

		d := workdir.New(fresh, envName)
		if err := bringTargets(ctx, d, env, lock, refreshUpgrade); err != nil {
			return err
		}

		if err := lockPack(ctx, d, lock, refreshUpgrade); err != nil {
			return err
		}
		if !reflect.DeepEqual(before, *lock) {
			return release.WriteLock(fresh.Dir, lock)
		}
		return nil
	}),
}

// lockPack brings the release's pack, if it is in a registry, into .pack/packs, and says in the lock which digest
// its tag meant: the locked one, unless upgrade asks for the one the tag has now.
func lockPack(ctx context.Context, d *workdir.Dir, lock *release.Lock, upgrade bool) error {
	rel := d.Release
	ref, err := rel.PackRef()
	if err != nil {
		return err
	}
	if !oci.IsRef(ref) {
		fmt.Printf("%s is a directory: nothing to bring\n", ref)
		return nil
	}
	o, err := rel.Registry.Options()
	if err != nil {
		return fmt.Errorf("registry: %w", err)
	}
	var pin string
	if lock.Pack == ref && !upgrade {
		pin, err = oci.WithDigest(ref, lock.Digest)
	} else {
		pin, err = oci.Pin(ctx, ref, o)
	}
	if err != nil {
		return err
	}
	p, err := oci.Pull(ctx, pin, d.PacksPath(), o)
	if err != nil {
		return err
	}
	lock.Pack, lock.Digest = ref, p.Digest
	fmt.Printf("%s locked at %s\n", ref, p.Digest)
	return nil
}

// bringTargets downloads the program of every target the environment uses that is not in pack itself, from the source
// release.yaml names for it, into .pack/bin; the lock keeps the checksum of what came. A program that is there with
// the checksum the lock has is not downloaded again; one that comes with another is refused unless upgrade says the
// source moved.
func bringTargets(ctx context.Context, d *workdir.Dir, env *environment.Environment, lock *release.Lock, upgrade bool) error {
	rel := d.Release
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
	bin := d.BinPath()
	for _, name := range slices.Sorted(maps.Keys(used)) {
		if slices.Contains(builtin, name) {
			continue
		}
		file := filepath.Join(bin, targets.Binary(name))
		from := rel.Targets[name]
		source := from.URL
		if len(source) == 0 {
			if _, err := os.Stat(file); err != nil {
				return fmt.Errorf("target %s: no source for it in release.yaml (targets: {%s: https://… or oci://…}) and %s is not there", name, name, file)
			}
			fmt.Printf("target %s: %s, no source named\n", name, file)
			continue
		}
		locked, has := lock.Targets[name]
		same := has && locked.Source == source && !upgrade
		if same {
			if sum, err := targets.Sum(file); err == nil && sum == locked.SHA256 {
				fmt.Printf("target %s: up to date (sha256 %s)\n", name, sum)
				continue
			}
		}
		o, err := from.Options()
		if err != nil {
			return fmt.Errorf("targets.%s: %w", name, err)
		}
		sum, err := targets.Fetch(ctx, name, source, bin, o)
		if err != nil {
			return err
		}
		if same && sum != locked.SHA256 {
			_ = os.Remove(file)
			return fmt.Errorf("target %s: %s now gives sha256 %s, the lock has %s — `pack refresh --upgrade` takes the new one", name, source, sum, locked.SHA256)
		}
		if lock.Targets == nil {
			lock.Targets = map[string]release.TargetLock{}
		}
		lock.Targets[name] = release.TargetLock{Source: source, SHA256: sum}
		fmt.Printf("target %s: downloaded from %s (sha256 %s)\n", name, source, sum)
	}
	return nil
}
