package diagnose

import (
	"context"
	"encoding/json"
	"io"
	"slices"
	"strings"

	"github.com/horchestra/pack/internal/session"
	"github.com/horchestra/pack/internal/workdir"
	"github.com/horchestra/pack/pkg/render"
	"github.com/horchestra/pack/pkg/saga"
	"github.com/horchestra/pack/pkg/target"
)

// LogLines is how many lines of each instance a case asks the target for.
const LogLines = 50

// CaseOf makes the case of the record's unfinished saga: the node that failed in its journal, its lines there
// and — with logs — the tail of its logs from its target, worked against the diagnoses of the pack of the version
// the saga went to. Without logs it asks the target nothing: what next needs, cheaply. lang is the language of
// the causes, "" — the base.
func CaseOf(ctx context.Context, d *workdir.Dir, s *session.Session, rec *saga.Record, logs bool, lang string) (Case, error) {
	sg := rec.Saga
	in := Input{Saga: sg.ID, Error: sg.Error}
	if j, jerr := d.JournalReaderFor(s.ID); jerr == nil && j != nil {
		if c, ok := j.(io.Closer); ok {
			defer func() { _ = c.Close() }()
		}
		entries, err := j.Read(sg.ID)
		if err == nil {
			for _, e := range slices.Backward(entries) {
				if e.Kind == saga.KindFail && len(e.Node) > 0 {
					in.Key = e.Node
					if len(e.Error) > 0 {
						in.Error = e.Error
					}
					break
				}
			}

			for _, e := range entries {
				if len(in.Key) > 0 && e.Node == in.Key && (e.Kind == saga.KindLog || e.Kind == saga.KindFail) {
					in.Lines = append(in.Lines, strings.TrimSpace(e.Message+" "+e.Error))
				}
			}
		}
	}

	guessed := len(in.Key) == 0
	if guessed {
		in.Key, _, _ = strings.Cut(sg.Error, ":") // the error names the node that failed — when it is a key, below
	}
	// the diagnoses of the key's pack in the version the saga went to, their fixes worked out on its values
	if b, err := s.Pack(sg.Pack); err == nil {
		if _, ok := b.KeyPack[in.Key]; !ok && guessed {
			in.Key = "" // what the error began with is no key of the pack
		}
		if p := b.Packs[b.KeyPack[in.Key]]; p != nil {
			in.Diagnoses = p.Schema.Root.Diagnoses
		}
		var v render.Values
		if len(sg.Values) > 0 {
			_ = json.Unmarshal(sg.Values, &v)
		}
		key := in.Key
		in.Eval = func(expr string) (any, error) { return render.Eval(b, v, s.Ctx, key, expr) }
		in.Pointer = func(ptr string) string { return b.External("/" + key + ptr) }
	}
	// the tail of the key's logs, where its target gives them — after the version is rendered, for the target to know
	// the key (where it runs, its services)
	if logs && len(in.Key) > 0 {
		_, _, _ = s.Version(sg.Installation)
		tg := any(s.Inst)
		if name := s.Env.Keys[in.Key].Target; len(name) > 0 {
			tg = s.Execs[name]
		}
		if l, ok := tg.(target.Logger); ok {
			if logs, err := l.Logs(ctx, in.Key, LogLines); err == nil {
				for inst, lines := range logs {
					for _, line := range lines {
						in.Lines = append(in.Lines, inst+": "+line)
					}
				}
			}
		}
	}
	in.Lang = lang
	return Make(in), nil
}
