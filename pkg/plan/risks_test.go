package plan

import (
	"testing"

	"github.com/horchestra/pack/pkg/render"
)

func TestRisks(t *testing.T) {
	from := &render.Release{Units: []render.Unit{
		{Key: "db", Data: []render.Claim{{Name: "data"}}},
		{Key: "app", Env: []render.EnvVar{{Name: "PW", Secret: &render.SecretRef{Name: "a", Key: "k"}}}},
	}}
	to := &render.Release{Units: []render.Unit{
		{Key: "app", Env: []render.EnvVar{{Name: "PW", Secret: &render.SecretRef{Name: "b", Key: "k"}}}},
	}}
	p := &Plan{Direction: "upgrade",
		Steps:   []Step{{N: 1, Key: "app", Kind: Hook, Name: "migrate", Irreversible: true}},
		Changes: []Change{{Key: "db", Kind: "removed"}, {Key: "app", Direction: "downgrade", From: "2", To: "1"}},
		Units:   []Unit{{From: 1, To: 1}, {From: 2, To: 2}}}
	Seal(p, from, to)
	kinds := map[string]bool{}
	for _, r := range p.Risks {
		kinds[r.Kind] = true
	}
	for _, k := range []string{RiskIrreversible, RiskDowngrade, RiskData, RiskSavepoint, RiskSecret} {
		if !kinds[k] {
			t.Errorf("no %s risk: %+v", k, p.Risks)
		}
	}
	d := p.Digest
	Seal(p, from, to)
	if p.Digest != d || len(d) == 0 {
		t.Errorf("the digest is not the plan's: %s, %s", d, p.Digest)
	}
	p.Steps[0].Name = "migrate2"
	if Digest(p) == d {
		t.Error("another plan, the same digest")
	}
	safe := &Plan{Direction: "install", Steps: []Step{{N: 1, Key: "app", Kind: Apply}}}
	Seal(safe, nil, to)
	if len(safe.Risks) != 0 {
		t.Errorf("a first installation is no risk: %+v", safe.Risks)
	}
}
