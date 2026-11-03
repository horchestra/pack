package plan

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/horchestra/pack/pkg/render"
)

// The risks of a plan: what an apply should not do unasked (run.approve: auto-safe).
const (
	RiskIrreversible = "irreversible" // a step that cannot be undone (a hook without compensate)
	RiskDowngrade    = "downgrade"    // a key goes back a version
	RiskSavepoint    = "savepoint"    // a failure after a savepoint leaves what is before it in effect
	RiskData         = "data"         // a key with data volumes is dropped
	RiskSecret       = "secret"       // a key's credentials change
	RiskAction       = "action"       // an action the pack says is never run unasked (pack run)
)

// The directions of a plan that has no steps and still does something (approved asks for them under run.approve: plan).
const (
	DirUninstall = "uninstall" // the installation taken away (pack delete)
	DirUndo      = "undo"      // an unfinished saga taken back (pack rollback)
	DirAction    = "action"    // an action of the pack performed on a key (pack run): one step, a hook
)

// Risk is one thing of a plan an apply should not do unasked.
type Risk struct {
	Kind string `json:"kind"`
	Key  string `json:"key,omitempty"`
	Why  string `json:"why"`
}

// Seal says the plan's risks and its digest: the digest is what --approve names, so that the plan applied is the one
// shown.
func Seal(p *Plan, from, to *render.Release) {
	p.Risks, p.Digest = nil, ""
	p.Risks = Risks(p, from, to)
	p.Digest = Digest(p)
}

// Digest is the plan's: sha256 of what it does (its JSON, the digest itself and its risks aside).
func Digest(p *Plan) string {
	q := *p
	q.Digest, q.Risks = "", nil
	b, err := json.Marshal(q)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:])[:16]
}

// Risks are what the plan does that an apply should not do unasked.
func Risks(p *Plan, from, to *render.Release) []Risk {
	var out []Risk
	add := func(kind, key, format string, a ...any) {
		out = append(out, Risk{Kind: kind, Key: key, Why: fmt.Sprintf(format, a...)})
	}
	// taking the installation away is no risk of itself: the data and the credentials stay (pack delete); run.approve:
	// plan asks for it as for any plan
	for _, s := range p.Steps {
		if s.Irreversible {
			add(RiskIrreversible, s.Key, "step %d (%s %s) cannot be undone", s.N, s.Kind, s.Name)
		}
	}
	for _, c := range p.Changes {
		if c.Direction == "downgrade" {
			add(RiskDowngrade, c.Key, "%s goes back from %s to %s", c.Key, c.From, c.To)
		}
		if c.Kind == "removed" {
			if u := unitIn(from, c.Key); u != nil && len(u.Data) > 0 {
				names := make([]string, len(u.Data))
				for i, d := range u.Data {
					names[i] = d.Name
				}
				add(RiskData, c.Key, "%s is dropped, and it has data: %s", c.Key, strings.Join(names, ", "))
			}
		}
	}
	if p.Direction != "install" && p.Direction != DirUninstall && p.Direction != DirUndo && p.Direction != DirAction && len(p.Units) > 1 {
		add(RiskSavepoint, "", "a failure after a savepoint keeps what is before it: no going back whole")
	}
	if to != nil {
		for _, u := range to.Units {
			if f := unitIn(from, u.Key); f != nil && !slices.Equal(secretsOf(f), secretsOf(&u)) {
				add(RiskSecret, u.Key, "the credentials of %s change", u.Key)
			}
		}
	}
	return out
}

func unitIn(rel *render.Release, key string) *render.Unit {
	if rel == nil {
		return nil
	}
	for i := range rel.Units {
		if rel.Units[i].Key == key {
			return &rel.Units[i]
		}
	}
	return nil
}

// secretsOf are the references of a unit's credentials, sorted.
func secretsOf(u *render.Unit) []string {
	var out []string
	for _, e := range u.Env {
		if e.Secret != nil {
			out = append(out, e.Name+"="+e.Secret.Name+"/"+e.Secret.Key)
		}
	}
	for _, f := range u.SecretFiles {
		out = append(out, f.Path+"="+f.Secret.Name+"/"+f.Secret.Key)
	}
	slices.Sort(out)
	return out
}
