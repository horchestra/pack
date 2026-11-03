package cmd

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"github.com/horchestra/pack/internal/interrupt"
	"github.com/horchestra/pack/internal/session"
	"github.com/horchestra/pack/pkg/saga"
	"github.com/horchestra/pack/pkg/target"
)

func init() { rootCmd.AddCommand(statusCmd) }

// status is what pack status says.
type status struct {
	State     string                   `json:"state"` // none | installed | in-progress | failed …, the record's
	Installed *version                 `json:"installed,omitempty"`
	Target    string                   `json:"target"`
	Drift     []session.Drift          `json:"drift,omitempty"`
	Health    map[string]target.Health `json:"health,omitempty"`
	// Healthy: installed, no drift, and every key a target says the health of is ready.
	Healthy bool `json:"healthy"`
}

var statusCmd = &cobra.Command{
	Use:   "status",
	Short: "how the installation is: what is installed, what drifted from the record, which keys are not ready",
	Long: `Says how the installation is now, asking the targets, changing nothing: the version
installed, the keys whose state in the target is not what the record says (drift: missing,
another image, other replicas — targets that can tell), and the health of each key's
instances (targets that can tell: the health capability). Healthy is all of it in order;
pack next says failed when it is not.`,
	Args: cobra.NoArgs,
	Run: run(func(*cobra.Command, []string) error {
		ctx, stop := interrupt.Context()
		defer stop()
		s, err := open(ctx, true)
		if err != nil {
			return err
		}
		defer s.Close()
		st, err := statusOf(ctx, s)
		if err != nil {
			return err
		}
		printStatus(st)
		return result(st)
	}),
}

// statusOf asks the targets how the installation is.
func statusOf(ctx context.Context, s *session.Session) (status, error) {
	st := status{State: "none", Target: s.Target}
	rec, _, rel, err := s.Installed()
	if err != nil {
		return st, err
	}
	if rec == nil {
		return st, nil
	}
	st.State = string(rec.State)
	if len(rec.Pack) > 0 {
		v := versionOf("", rec.Version, rec.Pack)
		st.Installed = &v
	}
	if st.Drift, err = s.Drifts(ctx, rec, rel); err != nil {
		return st, err
	}
	if st.Health, err = s.Health(ctx, rel); err != nil {
		return st, err
	}
	st.Healthy = rec.State == saga.Installed && len(rel.Units) > 0 && len(st.Drift) == 0
	for _, h := range st.Health {
		st.Healthy = st.Healthy && h.Ready
	}
	return st, nil
}

// unwell is what keeps the installation from being healthy, one line each.
func (st status) unwell() []string {
	var out []string
	if st.State != string(saga.Installed) {
		out = append(out, "the record says "+st.State)
	}
	for _, d := range st.Drift {
		out = append(out, "drift: "+d.String())
	}
	for _, key := range slices.Sorted(maps.Keys(st.Health)) {
		h := st.Health[key]
		if h.Ready {
			continue
		}
		for _, in := range h.Instances {
			if !in.Ready {
				out = append(out, fmt.Sprintf("%s: %s is not ready: %s", key, in.Name, strings.TrimSpace(in.State+" "+in.Reason)))
			}
		}
		if len(h.Instances) == 0 {
			out = append(out, key+": not ready")
		}
	}
	return out
}

func printStatus(st status) {
	if st.Installed == nil {
		fmt.Printf("nothing is installed on %s (%s)\n", st.Target, st.State)
		return
	}
	fmt.Printf("%s %s on %s: %s\n", st.Installed.Pack, st.Installed.Version, st.Target, st.State)
	for _, key := range slices.Sorted(maps.Keys(st.Health)) {
		h := st.Health[key]
		word := "ready"
		if !h.Ready {
			word = "not ready"
		}
		fmt.Printf("  %s: %s\n", key, word)
		for _, in := range h.Instances {
			fmt.Printf("    %s: %s %s\n", in.Name, in.State, in.Reason)
		}
	}
	for _, d := range st.Drift {
		fmt.Println("  drift:", d)
	}
	if st.Healthy {
		fmt.Println("healthy")
	} else {
		fmt.Println("not healthy")
	}
}
