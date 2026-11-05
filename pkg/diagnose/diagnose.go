// Package diagnose turns a failed saga into a case: what failed, the lines that show why, what kind of failure it
// is, and the ways out — the pack's own diagnoses first (x-pack-diagnose), then the kinds every pack shares. It is
// deterministic: the same journal and logs give the same case, for a person or a model to act on.
//
// The case itself — the types and Make — is pkg/diagnosis, a core without pack's internals that pack-ui (another
// module) works the same cases with; this package adds CaseOf, the reading of them out of a session's journal
// and targets.
package diagnose

import (
	"github.com/horchestra/pack/pkg/diagnosis"
)

// The kinds of failure every pack shares.
const (
	PortConflict = diagnosis.PortConflict
	ImagePull    = diagnosis.ImagePull
	OOM          = diagnosis.OOM
	Timeout      = diagnosis.Timeout
	Permission   = diagnosis.Permission
	Disk         = diagnosis.Disk
	Unknown      = diagnosis.Unknown
)

// Remedy is a way out: a value for a setting (Patch, the operator's pointer, and Value) or a command to run.
type Remedy = diagnosis.Remedy

// Case is a failure as pack diagnose says it.
type Case = diagnosis.Case

// Input is what a case is made of.
type Input = diagnosis.Input

// Make is the case of a failure.
func Make(in Input) Case { return diagnosis.Make(in) }
