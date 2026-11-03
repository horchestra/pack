// fake is a target for tests: it performs steps by writing them down. PACK_FAKE_DIR holds its journal (log) and the
// record pack keeps with it (record.json); PACK_FAKE_FAIL names a step to fail, as the journal writes it
// ("apply kafka").
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"

	"github.com/horchestra/pack/pkg/render"
	"github.com/horchestra/pack/pkg/target"
	"github.com/horchestra/pack/pkg/target/remote"
)

type inst struct{ name, dir string }

func factory(installation string, s map[string]string) (target.Installation, error) {
	dir := target.Settings(s).Get("fake_dir", os.Getenv("PACK_FAKE_DIR"))
	return &inst{name: installation, dir: dir}, nil
}

func (i *inst) Namespace() string { return "fake-ns" }

// Resolve: replicas from the placement, and what the target alone reads of the entry (raw.zone, an integer over the
// wire); a key with replicas has instances.
func (i *inst) Resolve(key string, p render.Placement) (*render.Resolved, error) {
	svc := i.name + "-" + key
	res := &render.Resolved{Service: svc, Address: svc + "." + i.Namespace()}
	if p.Replicas != nil {
		for k := range *p.Replicas {
			n := fmt.Sprintf("%s-%d", svc, k)
			res.Instances = append(res.Instances, render.Instance{Name: n, Address: n})
		}
	}
	if z, ok := p.Raw["zone"].(int64); ok {
		res.Address = fmt.Sprintf("%s.zone%d", svc, z)
	}
	if p.Reach != nil {
		res.Reach = &render.Reach{Instances: p.Reach.Instances, Ports: p.Reach.Ports}
	}
	return res, nil
}

func (i *inst) ReadInstalled() ([]byte, bool, error) {
	b, err := os.ReadFile(filepath.Join(i.dir, "record.json"))
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	return b, err == nil, err
}

func (i *inst) WriteInstalled(r []byte) error {
	return os.WriteFile(filepath.Join(i.dir, "record.json"), r, 0o600)
}

func (i *inst) DeleteInstalled() error {
	err := os.Remove(filepath.Join(i.dir, "record.json"))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

// Discover tells one host, the hosts asked for being the ones it keeps.
func (i *inst) Discover(_ context.Context, hosts []string) (*target.Facts, error) {
	f := &target.Facts{Hosts: map[string]target.HostFacts{"h1": {OS: "debian", MemoryMB: 2048}, "h2": {OS: "fedora"}}}
	if len(hosts) > 0 {
		for n := range f.Hosts {
			if !slices.Contains(hosts, n) {
				delete(f.Hosts, n)
			}
		}
	}
	return f, nil
}

func (i *inst) Observe(context.Context) (map[string]target.Observed, error) {
	return map[string]target.Observed{"kafka": {Image: "images/kafka:3", Replicas: 3}}, nil
}

func (i *inst) Execute(_ context.Context, s target.Step, events func(string)) error {
	what := s.Kind + " " + s.Key
	if s.Rollback {
		what = "undo " + what
	}
	if s.Unit != nil && s.Kind == "apply" {
		what += " " + s.Unit.Image
	}
	f, err := os.OpenFile(filepath.Join(i.dir, "log"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	_, _ = fmt.Fprintln(f, what)
	events("fake: " + what)
	if (s.Kind+" "+s.Key) == os.Getenv("PACK_FAKE_FAIL") && !s.Rollback {
		return errors.New("fake failure")
	}
	return nil
}

func main() {
	if err := remote.Serve("fake", factory, remote.CapRecord, remote.CapObserve, remote.CapDiscover); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, "pack-target-fake:", err)
		os.Exit(1)
	}
}
