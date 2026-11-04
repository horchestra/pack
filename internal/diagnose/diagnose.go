// Package diagnose turns a failed saga into a case: what failed, the lines that show why, what kind of failure it
// is, and the ways out — the pack's own diagnoses first (x-pack-diagnose), then the kinds every pack shares. It is
// deterministic: the same journal and logs give the same case, for a person or a model to act on.
package diagnose

import (
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/horchestra/pack/sdk/packschema"
)

// The kinds of failure every pack shares.
const (
	PortConflict = "port_conflict"
	ImagePull    = "image_pull"
	OOM          = "oom"
	Timeout      = "timeout"
	Permission   = "permission"
	Disk         = "disk"
	Unknown      = "unknown"
)

// Remedy is a way out: a value for a setting (Patch, the operator's pointer, and Value) or a command to run.
type Remedy struct {
	ID      string `json:"id"`
	Patch   string `json:"patch,omitempty"`
	Value   any    `json:"value,omitempty"`
	Command string `json:"command,omitempty"`
	Why     string `json:"why,omitempty"`
}

// Case is a failure as pack diagnose says it.
type Case struct {
	Saga      string   `json:"saga,omitempty"`
	Key       string   `json:"key,omitempty"`
	Error     string   `json:"error"`
	Class     string   `json:"class"`
	Diagnosis string   `json:"diagnosis,omitempty"` // the id of the pack's diagnosis that matched
	Cause     string   `json:"cause"`
	Evidence  []string `json:"evidence,omitempty"`
	Truncated bool     `json:"truncated,omitempty"`
	Remedies  []Remedy `json:"remedies,omitempty"`
}

// Input is what a case is made of.
type Input struct {
	Saga, Key, Error string
	Lines            []string // the key's lines of the journal, the tail of its logs
	// Diagnoses are the key's pack's; Patch of a fix is the operator's pointer of it, Value its expr worked out
	// (Eval).
	Diagnoses []packschema.Diagnosis
	// Eval works out the value of a fix's expr; nil — a fix with an expr offers no value.
	Eval func(expr string) (any, error)
	// Lang is the language of the causes (docs/), "" — the base.
	Lang string
	// Pointer turns a pointer of the pack into the operator's.
	Pointer func(string) string
}

// MaxEvidence bounds what a case shows of the lines: the last ones, the ones that matched first.
const (
	maxLines   = 40
	maxLineLen = 300
)

// kinds are the failures every pack shares: the first that matches says what it is.
var kinds = []struct {
	class, cause string
	match        *regexp.Regexp
	remedies     []Remedy
}{
	{PortConflict, "a port is taken where the key runs", regexp.MustCompile(`(?i)address already in use|port is already allocated|bind: .*in use|EADDRINUSE`),
		[]Remedy{{Command: "pack discover", Why: "see what listens on the hosts"}, {Command: "pack fill", Why: "take another port the pack offers"}}},
	{ImagePull, "an image cannot be pulled", regexp.MustCompile(`(?i)pull access denied|manifest unknown|ErrImagePull|ImagePullBackOff|no such image|not found: manifest`),
		[]Remedy{{Command: "pack refresh", Why: "bring what the release names again"}}},
	{OOM, "an instance ran out of memory", regexp.MustCompile(`(?i)OOMKilled|out of memory|exit code 137|oom-kill`),
		[]Remedy{{Why: "give the key more memory: keys.<key>.resources.limits.memory in env.yaml"}}},
	{Disk, "a disk is full", regexp.MustCompile(`(?i)no space left on device|ENOSPC|disk quota exceeded`),
		[]Remedy{{Command: "pack discover", Why: "see the free space of the hosts"}}},
	{Permission, "something was refused for want of a right", regexp.MustCompile(`(?i)permission denied|EACCES|forbidden|unauthorized|access denied`),
		nil},
	{Timeout, "something did not become ready in time", regexp.MustCompile(`(?i)timed? ?out|deadline exceeded|did not become (ready|active)|not ready after`),
		[]Remedy{{Command: "pack log", Why: "see what the key said while it was waited for"}}},
}

// Make is the case of a failure.
func Make(in Input) Case {
	c := Case{Saga: in.Saga, Key: in.Key, Error: in.Error, Class: Unknown, Cause: "pack cannot tell: a person should look"}
	text := in.Error + "\n" + strings.Join(in.Lines, "\n")
	c.Evidence, c.Truncated = evidence(in.Lines)
	for _, d := range in.Diagnoses {
		re, err := regexp.Compile(d.Match)
		if err != nil || !re.MatchString(text) {
			continue
		}
		c.Class, c.Diagnosis, c.Cause = d.ID, d.ID, causeOf(d, in.Lang)
		for _, f := range d.Fix {
			r := Remedy{Command: f.Command, Why: f.Why}
			if len(f.Patch) > 0 {
				if in.Eval == nil {
					continue
				}
				v, err := in.Eval(f.Expr)
				if err != nil || v == nil {
					continue // a fix the values do not let be worked out is none
				}
				r.Patch, r.Value = f.Patch, v
				if in.Pointer != nil {
					r.Patch = in.Pointer(f.Patch)
				}
			}
			c.Remedies = append(c.Remedies, r)
		}
		number(c.Remedies)
		return c
	}
	for _, k := range kinds {
		if k.match.MatchString(text) {
			c.Class, c.Cause = k.class, k.cause
			c.Remedies = append([]Remedy(nil), k.remedies...)
			number(c.Remedies)
			return c
		}
	}
	return c
}

func causeOf(d packschema.Diagnosis, lang string) string {
	if len(lang) > 0 {
		short, _, _ := strings.Cut(lang, "-")
		for _, l := range []string{lang, short} {
			if t := d.I18n[l]["cause"]; len(t) > 0 {
				return t
			}
		}
	}
	return d.Cause
}

func number(rs []Remedy) {
	for i := range rs {
		rs[i].ID = fmt.Sprintf("r%d", i+1)
	}
}

// evidence are the last lines, each cut short: what a case shows of what the key said.
func evidence(lines []string) ([]string, bool) {
	var out []string
	for _, l := range lines {
		if l = strings.TrimSpace(l); len(l) > 0 {
			if len(l) > maxLineLen {
				n := maxLineLen
				for n > 0 && !utf8.RuneStart(l[n]) {
					n-- // not inside a character: a line in Russian stays readable
				}
				l = l[:n] + "…"
			}
			out = append(out, l)
		}
	}
	if len(out) > maxLines {
		return out[len(out)-maxLines:], true
	}
	return out, false
}
