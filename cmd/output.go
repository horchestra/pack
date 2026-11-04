package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/horchestra/pack/internal/oci"
	"github.com/horchestra/pack/internal/release"
	"github.com/horchestra/pack/pkg/api"
	"github.com/horchestra/pack/pkg/logging"
	"github.com/horchestra/pack/pkg/render"
	"github.com/horchestra/pack/pkg/target"
)

// --json: a command's result is one JSON document on stdout, and nothing else is there — what it says along the way
// (the plan's text, the saga's events) goes to stderr. A command that fails writes {"error": …, "next": […]} and
// exits with 1. What a model or a script reads, the same as a person reads in the text.
var jsonOut bool

// stdout is where the JSON goes: the process's stdout, while os.Stdout is stderr for the rest (asJSON).
var stdout io.Writer = os.Stdout

// asJSON makes stdout the JSON's alone: what the command prints along the way goes to stderr.
func asJSON() {
	stdout = os.Stdout
	os.Stdout = os.Stderr
}

// result writes v, the command's result, as the JSON document of --json; nothing without it.
func result(v any) error {
	if streamOut {
		streamEvent(api.Event{Type: api.EventResult, Status: api.StatusOK, Result: v})
		return nil
	}
	if !jsonOut {
		return nil
	}
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	_, err = stdout.Write(append(b, '\n'))
	return err
}

// coded is an error with the code a command knows it by, and the problems it found.
type coded struct {
	code     string
	err      error
	problems []render.Problem
	next     []string // what to run next, when the code alone does not say it
	detail   any      // what the command found besides problems (author check, pack test): in the error's document
}

func (c *coded) Error() string { return c.err.Error() }
func (c *coded) Unwrap() error { return c.err }

func withCode(code string, err error) error {
	if err == nil {
		return nil
	}
	return &coded{code: code, err: err}
}

// withNext is withCode with what to run next.
func withNext(code string, err error, next []string) error {
	return &coded{code: code, err: err, next: next}
}

// withDetail is withCode with what the command found, for the error's document: one document, the error's.
func withDetail(code string, err error, detail any) error {
	return &coded{code: code, err: err, detail: detail}
}

// withProblems is withCode with the problems the command found: the error's document carries them.
func withProblems(code string, err error, problems []render.Problem) error {
	return &coded{code: code, err: err, problems: problems}
}

// classify gives an error its code: the one a command said, or the one its kind says.
func classify(err error) api.Error {
	out := api.Error{Code: api.CodeError, Message: err.Error()}
	var c *coded
	var ps *render.Problems
	switch {
	case errors.As(err, &ps):
		out.Code, out.Problems = api.CodeValuesInvalid, ps.Problems
		if ps.OnlyRequired() {
			out.Code = api.CodeValuesMissing
		}
	case errors.As(err, &c):
		out.Code, out.Problems, out.Detail = c.code, c.problems, c.detail
	case errors.Is(err, oci.ErrNoLock), errors.Is(err, target.ErrNotBrought):
		out.Code = api.CodeNotRefreshed
	case errors.Is(err, release.ErrNoPack):
		out.Code = api.CodeNoRelease
	case errors.As(err, new(*target.Error)):
		out.Code = api.CodeTarget
	}
	return out
}

// nextOf is what to run after the error: what the command said, or what its code says.
func nextOf(err error, code string) []string {
	var c *coded
	if errors.As(err, &c) && len(c.next) > 0 {
		return c.next
	}
	return nextFor(code)
}

// nextFor is what to run after an error of the code.
func nextFor(code string) []string {
	switch code {
	case api.CodeNoRelease:
		return []string{"pack init <oci://registry/repo:tag|dir> ."}
	case api.CodeNotRefreshed:
		return []string{"pack refresh"}
	case api.CodeValuesMissing:
		return []string{"pack fill", "pack answer <pointer> <value>", "pack check"}
	case api.CodeValuesInvalid:
		return []string{"pack answer <pointer> <value>", "edit values.yaml", "pack check"}
	case api.CodeCheckFailed:
		return []string{"pack check"}
	case api.CodeSagaFailed, api.CodeSagaUnfinished:
		return []string{"pack log", "pack apply", "pack rollback"}
	case api.CodeForeignComments:
		return []string{"pack values write --rewrite"}
	case api.CodeNothingInstalled:
		return []string{"pack plan", "pack apply"}
	case api.CodeUnhealthy:
		return []string{"pack status", "pack log", "pack apply"}
	case api.CodeNeedsApproval:
		return []string{"pack plan", "pack apply --approve <the plan's digest>"} // the command's own, as a rule (approved)
	}
	return nil
}

// fail ends the process for an error: "pack: …" on stderr, and with --json the error's document on stdout.
func fail(err error) {
	if streamOut {
		e := classify(err)
		streamEvent(api.Event{Type: api.EventResult, Status: api.StatusFailed, Code: e.Code, Error: e.Message, Next: nextOf(err, e.Code)})
	}
	if jsonOut {
		e := classify(err)
		b, _ := json.MarshalIndent(api.Failure{Error: e, Next: nextOf(err, e.Code)}, "", "  ")
		_, _ = stdout.Write(append(b, '\n'))
	}
	logging.For("cmd").Debug().Err(err).Dur("took", time.Since(commandStart)).Msg("failed")
	_, _ = fmt.Fprintln(os.Stderr, "pack:", err)
	os.Exit(1)
}
