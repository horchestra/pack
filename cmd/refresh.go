package cmd

import (
	"context"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"github.com/horchestra/pack/internal/interrupt"
	"github.com/horchestra/pack/internal/oci"
	"github.com/horchestra/pack/internal/release"
	"github.com/horchestra/pack/internal/targets"
	"github.com/horchestra/pack/internal/workdir"
	"github.com/horchestra/pack/pkg/environment"
	"github.com/horchestra/pack/pkg/target"
	"github.com/horchestra/pack/pkg/target/remote"
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
release.yaml names for each (targets: {docker: https://…, k8s: oci://…}), into .pack/bin; for a target that runs
from its image (runtime: {type: docker}), the digest the image's tag means (targets: {ansible: {image: oci://…}});
and the pack, if it is in a registry, with the packs it holds, into .pack/packs. release.lock.json keeps the
checksum of each program, the digest of each image and which digest the pack's tag meant today. apply and plan read the pack from there, so the same
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
			if err := release.WriteLock(fresh.Dir, lock); err != nil {
				return err
			}
		}
		return result(struct {
			Lock    *release.Lock `json:"lock"`
			Changed bool          `json:"changed"`
		}{lock, !reflect.DeepEqual(before, *lock)})
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
	// a target by the runtimes it runs in: the program for native, the image for docker
	used := map[string]map[string]bool{}
	use := func(name string, settings map[string]string) {
		if len(name) == 0 {
			return
		}
		rt := settings[remote.RuntimePrefix+"type"]
		if len(rt) == 0 {
			rt = remote.RuntimeNative
		}
		if used[name] == nil {
			used[name] = map[string]bool{}
		}
		used[name][rt] = true
	}
	use(env.Target, env.Settings)
	for _, t := range env.Targets {
		use(t.Target, t.Settings)
	}
	builtin := target.Names()
	bin := d.BinPath()
	for _, name := range slices.Sorted(maps.Keys(used)) {
		if slices.Contains(builtin, name) {
			if used[name][remote.RuntimeDocker] {
				return fmt.Errorf("target %s is in pack itself: it runs in pack's process, no runtime: says otherwise", name)
			}
			continue
		}
		// what the target no longer runs from leaves the lock
		if tl, ok := lock.Targets[name]; ok {
			if !used[name][remote.RuntimeNative] {
				tl.Source, tl.SHA256 = "", ""
			}
			if !used[name][remote.RuntimeDocker] {
				tl.Image, tl.Digest = "", ""
			}
			lock.Targets[name] = tl
		}
		if used[name][remote.RuntimeDocker] {
			if err := pinImage(ctx, name, rel.Targets[name], lock, upgrade); err != nil {
				return err
			}
		}
		if !used[name][remote.RuntimeNative] {
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
		tl := lock.Targets[name]
		tl.Source, tl.SHA256 = source, sum
		lock.Targets[name] = tl
		fmt.Printf("target %s: downloaded from %s (sha256 %s)\n", name, source, sum)
	}
	return nil
}

// pinImage says in the lock which digest the tag of the target's image means: the locked one, unless upgrade asks
// for the one the registry has now. The container runtime pulls the image when the target starts.
func pinImage(ctx context.Context, name string, from release.TargetSource, lock *release.Lock, upgrade bool) error {
	if len(from.Image) == 0 {
		return fmt.Errorf("target %s: runtime docker, and no image for it in release.yaml (targets: %s: {image: oci://…})", name, name)
	}
	tl := lock.Targets[name]
	if tl.Image == from.Image && len(tl.Digest) > 0 && !upgrade {
		fmt.Printf("target %s: image %s locked at %s\n", name, from.Image, tl.Digest)
		return nil
	}
	o, err := from.Options()
	if err != nil {
		return fmt.Errorf("targets.%s: %w", name, err)
	}
	pin, err := oci.Pin(ctx, from.Image, o)
	if err != nil {
		return fmt.Errorf("target %s: %w", name, err)
	}
	_, dg, _ := strings.Cut(pin, "@")
	if lock.Targets == nil {
		lock.Targets = map[string]release.TargetLock{}
	}
	tl.Image, tl.Digest = from.Image, dg
	lock.Targets[name] = tl
	fmt.Printf("target %s: image %s locked at %s\n", name, from.Image, dg)
	return nil
}
