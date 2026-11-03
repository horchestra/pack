package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"github.com/horchestra/pack/internal/interrupt"
	"github.com/horchestra/pack/internal/session"
	"github.com/horchestra/pack/pkg/render"
	"github.com/horchestra/pack/pkg/target"
)

func init() {
	discoverCmd.Flags().StringVar(&discoverFixture, "save-fixture", "", "also write the facts told, by target, into this file: a stand written down for pack test")
	rootCmd.AddCommand(discoverCmd)
}

var discoverFixture string

var discoverCmd = &cobra.Command{
	Use:   "discover",
	Short: "ask the targets the facts of where they install — hosts, memory, disks, ports — read only",
	Long: `Asks every target of the environment that can tell (the "discover" capability) the facts
of where it installs: its hosts — machines, nodes, engines — with their OS, CPUs, memory,
disks and free space, ports listened on, what runs workloads, DNS and clock; a cluster's
version and storage classes. Nothing is changed. discover.hosts in env.yaml names the hosts to
ask, none — all.

The facts go to .pack/facts/<target>.json (default.json for the default target), with when
they were told; formulas and rules read their key's target's as facts (facts.hosts.db1.memory_mb),
and pack next says how old they are.`,
	Args: cobra.NoArgs,
	Run: run(func(*cobra.Command, []string) error {
		ctx, stop := interrupt.Context()
		defer stop()
		s, err := open(ctx, true)
		if err != nil {
			return err
		}
		defer s.Close()
		ask := map[string]any{render.DefaultTarget: s.Inst}
		for name, x := range s.Execs {
			ask[name] = x
		}
		told := map[string]*target.Facts{}
		var untold []string
		for _, name := range slices.Sorted(maps.Keys(ask)) {
			d, ok := ask[name].(target.Discoverer)
			if !ok {
				untold = append(untold, name)
				continue
			}
			f, err := d.Discover(ctx, s.Env.Discover.Hosts)
			if errors.Is(err, target.ErrNoFacts) {
				untold = append(untold, name)
				continue
			}
			if err == nil && len(f.Hosts) == 0 && len(f.Unreachable) > 0 {
				err = fmt.Errorf("no host told its facts: %v", f.Unreachable)
			}
			if err != nil {
				return fmt.Errorf("target %s: %w", name, err)
			}
			if err := session.SaveFacts(dir, name, f); err != nil {
				return err
			}
			told[name] = f
			fmt.Printf("%s: %d host(s) told at %s\n", name, len(f.Hosts), f.At.Format("2006-01-02 15:04:05Z"))
			for _, h := range slices.Sorted(maps.Keys(f.Hosts)) {
				fmt.Println("  " + hostLine(h, f.Hosts[h]))
			}
			for _, h := range slices.Sorted(maps.Keys(f.Unreachable)) {
				fmt.Printf("  %s: told nothing: %s\n", h, f.Unreachable[h])
			}
		}
		for _, name := range untold {
			fmt.Printf("%s: tells no facts\n", name)
			if err := session.ForgetFacts(dir, name); err != nil { // what it told once is not what is there
				return err
			}
		}
		if len(discoverFixture) > 0 {
			b, err := json.MarshalIndent(told, "", "  ")
			if err != nil {
				return err
			}
			if err := os.WriteFile(discoverFixture, append(b, '\n'), 0o644); err != nil {
				return err
			}
		}
		return result(struct {
			Told   map[string]*target.Facts `json:"told"`
			Untold []string                 `json:"untold,omitempty"`
		}{told, untold})
	}),
}

// hostLine is a host's facts on one line.
func hostLine(name string, h target.HostFacts) string {
	parts := []string{name}
	if len(h.OS) > 0 {
		parts = append(parts, strings.TrimSpace(h.OS+" "+h.OSVersion))
	}
	if h.CPUs > 0 {
		parts = append(parts, fmt.Sprintf("%d cpu", h.CPUs))
	}
	if h.MemoryMB > 0 {
		parts = append(parts, fmt.Sprintf("%d MB", h.MemoryMB))
	}
	for _, d := range h.Disks {
		parts = append(parts, fmt.Sprintf("%s %d/%d MB free", d.Mount, d.FreeMB, d.SizeMB))
	}
	if len(h.Ports) > 0 {
		parts = append(parts, fmt.Sprintf("%d port(s) in use", len(h.Ports)))
	}
	return strings.Join(parts, ", ")
}
