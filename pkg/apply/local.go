// Package apply runs packs in process without the command-line package.
package apply

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"uuid"

	"github.com/horchestra/pack/internal/adapter"
	"github.com/horchestra/pack/internal/graph"
	"github.com/horchestra/pack/internal/release"
	"github.com/horchestra/pack/pkg/bundle"
	"github.com/horchestra/pack/pkg/environment"
	"github.com/horchestra/pack/pkg/plan"
	"github.com/horchestra/pack/pkg/render"
	"github.com/horchestra/pack/pkg/saga"
	"github.com/horchestra/pack/pkg/target"
	"github.com/horchestra/pack/sdk"
)

// LocalOptions is an observe-only apply of an in-memory pack to one target.
type LocalOptions struct {
	Dir            string
	InstallationID string
	Pack           sdk.Pack
	Values         map[string]any
	Target         target.Installation
	Planned        func(*plan.Plan)
	Hear           func(saga.Entry)
}

var localMu sync.Mutex

// Local applies an in-memory pack to an already opened target. The release's
// observe state and installation ID are read from Dir; no state outlives the run.
func Local(ctx context.Context, options LocalOptions) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if options.Pack == nil {
		return fmt.Errorf("local apply: pack is required")
	}
	if options.Target == nil {
		return fmt.Errorf("local apply: target is required")
	}
	if len(options.Dir) == 0 {
		options.Dir = "."
	}
	rel, err := release.Load(options.Dir, "")
	if err != nil {
		return err
	}
	if rel.State != "observe" {
		return fmt.Errorf("local apply: release state %q is not observe", rel.State)
	}
	if len(options.InstallationID) > 0 && options.InstallationID != rel.ID {
		return fmt.Errorf("local apply: target installation %q does not match release %q", options.InstallationID, rel.ID)
	}
	if _, err := uuid.Parse(rel.ID); err != nil {
		return fmt.Errorf("local apply: installation ID: %w", err)
	}

	localMu.Lock()
	defer localMu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	env, err := environment.Load(ctx, rel.Config(), nil)
	if err != nil {
		return err
	}
	if env.HistoryCount != nil {
		if *env.HistoryCount < 0 {
			return fmt.Errorf("history_count: %d is below zero", *env.HistoryCount)
		}
		if *env.HistoryCount == 0 {
			env.Run.NoRollback = true
		}
	}
	b, err := bundle.FromPack(options.Pack)
	if err != nil {
		return err
	}
	values := render.Values{Values: options.Values}
	credentials, err := render.Provision(b, &values, rel.ID, render.Provider(env.Secrets.Provider), env.Secrets.Provision)
	if err != nil {
		return err
	}
	rendered, err := render.Render(b, values, render.Context{
		Installation: rel.ID, Target: options.Target, Keys: environment.Placements(env.Keys),
	})
	if err != nil {
		return err
	}
	credentials = append(credentials, rendered.Credentials...)
	if err := render.WriteBack(&values, credentials); err != nil {
		return err
	}
	inspector, ok := options.Target.(target.Inspector)
	if !ok {
		return fmt.Errorf("state: observe: the local target cannot tell whether it holds the pack")
	}
	held := make(map[string]target.Hold, len(rendered.Units))
	for _, unit := range rendered.Units {
		if name := rendered.Report.Targets[unit.Key]; len(name) > 0 {
			return fmt.Errorf("%s: local apply does not support additional target %q", unit.Key, name)
		}
		var unitCredentials []render.Credential
		for _, credential := range credentials {
			if credential.Key == unit.Key {
				unitCredentials = append(unitCredentials, credential)
			}
		}
		hold, err := inspector.Inspect(ctx, unit, unitCredentials)
		if err != nil {
			return fmt.Errorf("%s: %w", unit.Key, err)
		}
		held[unit.Key] = hold
	}
	p, err := plan.Build(plan.Input{
		To: b, ToOut: rendered, Held: held,
		Policy: plan.Policy{Strict: env.Run.Strict, NoRollbackAll: env.Run.NoRollback},
	})
	if err != nil {
		return err
	}
	p.Revision = release.Revision(options.Dir)
	plan.Seal(p, nil, rendered)
	if options.Planned != nil {
		options.Planned(p)
	}
	g, err := graph.New(adapter.Specs(rendered, p))
	if err != nil {
		return err
	}
	rawValues, err := json.Marshal(values)
	if err != nil {
		return err
	}
	run := &saga.Run{
		Graph: g,
		Exec: &adapter.Exec{
			Plan: p, To: rendered, Credentials: credentials, X: options.Target,
		},
		Store:      &localStore{},
		ID:         rel.ID,
		To:         saga.Installation{Pack: b.Dir, Version: b.Version, Values: rawValues},
		Jobs:       env.Run.Jobs,
		NoRollback: env.Run.NoRollback,
		Keep:       env.Run.Keep,
		Revision:   p.Revision,
	}
	run.Hear = func(entry saga.Entry) {
		if line, ok := entry.Line(); ok {
			fmt.Println(line)
		}
		if options.Hear != nil {
			options.Hear(entry)
		}
	}
	return run.Execute(ctx)
}

type localStore struct{ record *saga.Record }

func (s *localStore) Load() (*saga.Record, error) { return s.record, nil }
func (s *localStore) Save(record *saga.Record) error {
	s.record = record
	return nil
}
func (s *localStore) Delete() error {
	s.record = nil
	return nil
}
