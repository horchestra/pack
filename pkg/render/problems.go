package render

import (
	"fmt"
	"sort"
	"strings"
)

// The kinds of a problem of the values.
const (
	KindRequired    = "required"    // a setting the schema requires is not given
	KindSchema      = "schema"      // a value the schema refuses
	KindRule        = "rule"        // a rule of the pack does not hold
	KindEnvironment = "environment" // the environment's placement of a key: its resources
	KindCheck       = "check"       // an author check of the pack (pack check)
	KindWarning     = "warning"     // what does not make the pack wrong: what its docs/ does not say
	KindRisk        = "risk"        // what a plan does that an apply should not do unasked (run.approve); Rule its kind
)

// Problem is one thing wrong with the values: where (the operator's pointer, or the environment's setting), what
// kind, and why.
type Problem struct {
	Kind    string `json:"kind"`
	Where   string `json:"where"`
	Rule    string `json:"rule,omitempty"` // the rule's id, for a rule
	Message string `json:"message"`
}

func (p Problem) String() string { return p.Where + ": " + p.Message }

// Problems is the error of values that do not validate: every problem, sorted as their text is.
type Problems struct {
	Phase    string // the phase of the rules that failed; "" — the schema and the environment
	Problems []Problem
}

func newProblems(phase string, ps []Problem) *Problems {
	sort.Slice(ps, func(i, j int) bool { return ps[i].String() < ps[j].String() })
	return &Problems{Phase: phase, Problems: ps}
}

func (e *Problems) Error() string {
	lines := make([]string, len(e.Problems))
	for i, p := range e.Problems {
		lines[i] = p.String()
	}
	head := "values do not validate"
	if len(e.Phase) > 0 {
		head = fmt.Sprintf("values do not validate (%s)", e.Phase)
	}
	return head + ":\n  " + strings.Join(lines, "\n  ")
}

// OnlyRequired says whether every problem is a setting not given: the values are unfinished, not wrong.
func (e *Problems) OnlyRequired() bool {
	for _, p := range e.Problems {
		if p.Kind != KindRequired {
			return false
		}
	}
	return len(e.Problems) > 0
}
