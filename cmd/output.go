package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/horchestra/pack/internal/oci"
	"github.com/horchestra/pack/internal/release"
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

// The codes of an error, the closed list a reader of --json acts on.
const (
	CodeNoRelease        = "no_release"        // no release.yaml, or it names no pack: pack init
	CodeNotRefreshed     = "not_refreshed"     // the pack or a target is not brought into .pack: pack refresh
	CodeValuesMissing    = "values_missing"    // settings the schema requires are not given
	CodeValuesInvalid    = "values_invalid"    // the schema refuses a value, a rule of the pack does not hold, the environment's resources fall short
	CodeCheckFailed      = "check_failed"      // pack check found problems
	CodeSagaFailed       = "saga_failed"       // a saga failed; the record says what is in effect
	CodeSagaUnfinished   = "saga_unfinished"   // a saga did not finish: apply it again, or take it back
	CodeNothingInstalled = "nothing_installed" // nothing is installed to remove or roll back
	CodeForeignComments  = "foreign_comments"  // values.yaml has comments pack did not write
	CodeTarget           = "target"            // the target refused, or could not be reached
	CodeError            = "error"             // anything else
)

// coded is an error with the code a command knows it by, and the problems it found.
type coded struct {
	code     string
	err      error
	problems []render.Problem
}

func (c *coded) Error() string { return c.err.Error() }
func (c *coded) Unwrap() error { return c.err }

func withCode(code string, err error) error {
	if err == nil {
		return nil
	}
	return &coded{code: code, err: err}
}

// withProblems is withCode with the problems the command found: the error's document carries them.
func withProblems(code string, err error, problems []render.Problem) error {
	return &coded{code: code, err: err, problems: problems}
}

// apiError is an error as --json writes it.
type apiError struct {
	Code     string           `json:"code"`
	Message  string           `json:"message"`
	Problems []render.Problem `json:"problems,omitempty"`
}

// failure is the JSON of a command that failed: what went wrong, and what to run next.
type failure struct {
	Error apiError `json:"error"`
	Next  []string `json:"next,omitempty"`
}

// classify gives an error its code: the one a command said, or the one its kind says.
func classify(err error) apiError {
	out := apiError{Code: CodeError, Message: err.Error()}
	var c *coded
	var ps *render.Problems
	switch {
	case errors.As(err, &ps):
		out.Code, out.Problems = CodeValuesInvalid, ps.Problems
		if ps.OnlyRequired() {
			out.Code = CodeValuesMissing
		}
	case errors.As(err, &c):
		out.Code, out.Problems = c.code, c.problems
	case errors.Is(err, oci.ErrNoLock), errors.Is(err, target.ErrNotBrought):
		out.Code = CodeNotRefreshed
	case errors.Is(err, release.ErrNoPack):
		out.Code = CodeNoRelease
	case errors.As(err, new(*target.Error)):
		out.Code = CodeTarget
	}
	return out
}

// nextFor is what to run after an error of the code.
func nextFor(code string) []string {
	switch code {
	case CodeNoRelease:
		return []string{"pack init <oci://registry/repo:tag|dir> ."}
	case CodeNotRefreshed:
		return []string{"pack refresh"}
	case CodeValuesMissing:
		return []string{"pack fill", "pack answer <pointer> <value>", "pack check"}
	case CodeValuesInvalid:
		return []string{"pack answer <pointer> <value>", "edit values.yaml", "pack check"}
	case CodeCheckFailed:
		return []string{"pack check"}
	case CodeSagaFailed, CodeSagaUnfinished:
		return []string{"pack log", "pack apply", "pack rollback"}
	case CodeForeignComments:
		return []string{"pack values write --rewrite"}
	case CodeNothingInstalled:
		return []string{"pack plan", "pack apply"}
	}
	return nil
}

// fail ends the process for an error: "pack: …" on stderr, and with --json the error's document on stdout.
func fail(err error) {
	if jsonOut {
		e := classify(err)
		b, _ := json.MarshalIndent(failure{Error: e, Next: nextFor(e.Code)}, "", "  ")
		_, _ = stdout.Write(append(b, '\n'))
	}
	_, _ = fmt.Fprintln(os.Stderr, "pack:", err)
	os.Exit(1)
}
