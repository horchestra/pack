package session

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/horchestra/pack/pkg/api"
	"github.com/horchestra/pack/pkg/render"
	"github.com/horchestra/pack/pkg/saga"
	"github.com/horchestra/pack/pkg/target"
)

// Drift is a key whose state in the target is not what the record says.
type Drift = api.Drift

// Drifts compares the installed version (from, rendered with the recorded values) with what the target holds.
// Only an installation that finished is compared — one that is under way or failed has its own state — and only
// by a target that can be asked (target.Observer); for others there is no drift to see.
func (s *Session) Drifts(ctx context.Context, rec *saga.Record, from *render.Release) ([]Drift, error) {
	obs, ok := s.Inst.(target.Observer)
	if !ok || rec == nil || rec.State != saga.Installed || from == nil {
		return nil, nil
	}
	held, err := obs.Observe(ctx)
	if errors.Is(err, target.ErrNotObserved) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read the state of the target: %w", err)
	}
	var out []Drift
	for _, u := range from.Units {
		h, there := held[u.Key]
		switch {
		case !there:
			out = append(out, Drift{Key: u.Key, Why: "missing in the target"})
		case h.Image != u.Image:
			out = append(out, Drift{Key: u.Key, Why: fmt.Sprintf("runs %s, the record says %s", h.Image, u.Image)})
		case h.Replicas != u.Replicas:
			out = append(out, Drift{Key: u.Key, Why: fmt.Sprintf("%d replica(s), the record says %d", h.Replicas, u.Replicas)})
		}
	}
	slices.SortFunc(out, func(a, b Drift) int { return strings.Compare(a.Key, b.Key) })
	return out, nil
}
