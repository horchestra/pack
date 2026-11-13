package remote

import (
	"bytes"
	"encoding/json"
	"fmt"
	"maps"
	"slices"

	"github.com/horchestra/pack/pkg/render"
	"github.com/horchestra/pack/pkg/target"
	"github.com/horchestra/pack/pkg/target/remote/remotepb"
)

func stepTo(s target.Step) (*remotepb.ExecuteRequest, error) {
	req := &remotepb.ExecuteRequest{Step: &remotepb.Step{
		N: int64(s.N), Key: s.Key, Kind: s.Kind, Name: s.Name, Phase: s.Phase, Timeout: s.Timeout,
		Exclusive: s.Exclusive, Image: s.Image, Command: s.Command, Rollback: s.Rollback, Destroy: s.Destroy,
	}}
	var err error
	if s.Unit != nil {
		if req.UnitJson, err = json.Marshal(s.Unit); err != nil {
			return nil, fmt.Errorf("the unit of %s: %w", s.Key, err)
		}
	}
	if s.Previous != nil {
		if req.PreviousJson, err = json.Marshal(s.Previous); err != nil {
			return nil, fmt.Errorf("the previous unit of %s: %w", s.Key, err)
		}
	}
	if len(s.Credentials) > 0 {
		if req.CredentialsJson, err = json.Marshal(s.Credentials); err != nil {
			return nil, fmt.Errorf("the credentials of %s: %w", s.Key, err)
		}
	}
	return req, nil
}

func stepFrom(req *remotepb.ExecuteRequest) (target.Step, error) {
	p := req.GetStep()
	s := target.Step{
		N: int(p.GetN()), Key: p.GetKey(), Kind: p.GetKind(), Name: p.GetName(), Phase: p.GetPhase(),
		Timeout: p.GetTimeout(), Exclusive: p.GetExclusive(), Image: p.GetImage(), Command: p.GetCommand(),
		Rollback: p.GetRollback(), Destroy: p.GetDestroy(),
	}
	if len(req.UnitJson) > 0 {
		s.Unit = new(render.Unit)
		if err := json.Unmarshal(req.UnitJson, s.Unit); err != nil {
			return s, fmt.Errorf("the unit: %w", err)
		}
	}
	if len(req.PreviousJson) > 0 {
		s.Previous = new(render.Unit)
		if err := json.Unmarshal(req.PreviousJson, s.Previous); err != nil {
			return s, fmt.Errorf("the previous unit: %w", err)
		}
	}
	if len(req.CredentialsJson) > 0 {
		if err := json.Unmarshal(req.CredentialsJson, &s.Credentials); err != nil {
			return s, fmt.Errorf("the credentials: %w", err)
		}
	}
	return s, nil
}

func placementTo(p render.Placement) ([]byte, error) { return json.Marshal(p) }

// placementFrom reads a placement back with the numbers of its raw part as they were meant: an integer as int64,
// anything else as float64.
func placementFrom(raw []byte) (render.Placement, error) {
	var p render.Placement
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	if err := d.Decode(&p); err != nil {
		return p, err
	}
	if p.Raw != nil {
		p.Raw = numbers(p.Raw).(map[string]any)
	}
	return p, nil
}

func numbers(v any) any {
	switch x := v.(type) {
	case map[string]any:
		for k, e := range x {
			x[k] = numbers(e)
		}
	case []any:
		for i, e := range x {
			x[i] = numbers(e)
		}
	case json.Number:
		if i, err := x.Int64(); err == nil {
			return i
		}
		f, _ := x.Float64()
		return f
	}
	return v
}

// stripFetched is the unit with the bytes of the files a CapFileURL target fetches by their URL left out: the
// registry or the disk serves them, they are not to cross to the target and back. The unit itself is not touched.
func stripFetched(u *render.Unit) *render.Unit {
	if u == nil {
		return nil
	}
	strip := func(fs []render.File) []render.File {
		var res []render.File
		for i, f := range fs {
			if len(f.URL) == 0 || (len(f.Content) == 0 && len(f.Instances) == 0) {
				continue
			}
			if res == nil {
				res = slices.Clone(fs)
			}
			res[i].Content, res[i].Instances = nil, nil
		}
		if res == nil {
			return fs
		}
		return res
	}
	out := *u
	out.Files = strip(out.Files)
	if len(out.Packages) > 0 {
		pk := maps.Clone(out.Packages)
		for os, p := range pk {
			p.Repo, p.Files = strip(p.Repo), strip(p.Files)
			pk[os] = p
		}
		out.Packages = pk
	}
	return &out
}
