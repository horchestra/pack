package session

import (
	"context"
	"errors"
	"fmt"

	"github.com/horchestra/pack/pkg/render"
	"github.com/horchestra/pack/pkg/target"
)

// Health is how the keys of the installed version are, by key, each asked of its own target — the ones whose target
// does not say (target.Checker) are not in it.
func (s *Session) Health(ctx context.Context, rel *render.Release) (map[string]target.Health, error) {
	if rel == nil {
		return nil, nil
	}
	byTarget := map[string]map[string]target.Health{}
	ask := func(name string) (map[string]target.Health, error) {
		if h, ok := byTarget[name]; ok {
			return h, nil
		}
		var x any = s.Inst
		if len(name) > 0 {
			x = s.Execs[name]
		}
		c, ok := x.(target.Checker)
		if !ok {
			byTarget[name] = nil
			return nil, nil
		}
		h, err := c.Health(ctx)
		if errors.Is(err, target.ErrNoHealth) {
			h, err = nil, nil
		}
		if err != nil {
			return nil, fmt.Errorf("target %s: %w", targetName(s, name), err)
		}
		byTarget[name] = h
		return h, nil
	}
	out := map[string]target.Health{}
	for _, u := range rel.Units {
		h, err := ask(rel.Report.Targets[u.Key])
		if err != nil {
			return nil, err
		}
		if kh, ok := h[u.Key]; ok {
			out[u.Key] = kh
		} else if h != nil {
			out[u.Key] = target.Health{Ready: false, Instances: []target.InstanceHealth{{Name: u.Key, Reason: "the target holds none of it"}}}
		}
	}
	return out, nil
}

func targetName(s *Session, name string) string {
	if len(name) == 0 {
		return s.Target
	}
	return name
}
