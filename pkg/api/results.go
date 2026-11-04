package api

import (
	"time"

	"github.com/horchestra/pack/sdk/packschema"

	"github.com/horchestra/pack/pkg/plan"
	"github.com/horchestra/pack/pkg/render"
	"github.com/horchestra/pack/pkg/saga"
	"github.com/horchestra/pack/pkg/target"
)

// The versions of the documents pack writes, as pack version --json says them: a reader checks them against what
// it was built for.
const (
	FormatJSON   = 1 // the --json documents
	FormatStream = 1 // the --stream events
)

// VersionResult is what pack version --json says: the binary's version and the versions of its documents.
type VersionResult struct {
	Version string         `json:"version"`
	Formats map[string]int `json:"formats"`
}

// Version is a pack at a version, and where it is from.
type Version struct {
	Name    string `json:"name,omitempty"`
	Version string `json:"version,omitempty"`
	Pack    string `json:"pack"`
}

// VersionOf is the Version of a pack.
func VersionOf(name, v, ref string) Version { return Version{Name: name, Version: v, Pack: ref} }

// InstalledResult is what apply and rollback did, as --json says it.
type InstalledResult struct {
	Installed    Version    `json:"installed"`
	Target       string     `json:"target"`
	Installation string     `json:"installation"`
	Plan         *plan.Plan `json:"plan,omitempty"`
}

// RemovedResult is what delete did or would do (--dry-run): what goes, by the removal plan's digest.
type RemovedResult struct {
	Removed Version  `json:"removed"`
	Keys    []string `json:"keys"`
	Digest  string   `json:"digest"`
	DryRun  bool     `json:"dry_run,omitempty"`
}

// UndoneResult is what rollback of an unfinished saga did or would do (--dry-run).
type UndoneResult struct {
	Undone Version `json:"undone"`
	Back   Version `json:"back"`
	Digest string  `json:"digest"`
	DryRun bool    `json:"dry_run,omitempty"`
}

// Action is an action of a pack, on a key of the release.
type Action struct {
	Key    string            `json:"key"`
	Pack   string            `json:"pack"`
	Action packschema.Action `json:"action"`
}

// ActionsResult is what run --json without an action says: the actions of the release's pack.
type ActionsResult struct {
	Actions []Action `json:"actions"`
}

// RanResult is what run of an action did or would do (--dry-run): the action, its key and parameters, and the
// digest --approve takes.
type RanResult struct {
	Action string         `json:"action"`
	Key    string         `json:"key"`
	Params map[string]any `json:"params,omitempty"`
	Digest string         `json:"digest"`
	DryRun bool           `json:"dry_run,omitempty"`
}

// Drift is a key whose state in the target is not what the record says.
type Drift struct {
	Key string `json:"key"`
	// Why: "missing", or what differs.
	Why string `json:"why"`
}

func (d Drift) String() string { return d.Key + ": " + d.Why }

// Status is what pack status says.
type Status struct {
	State     string                   `json:"state"` // none | installed | in-progress | failed …, the record's
	Installed *Version                 `json:"installed,omitempty"`
	Target    string                   `json:"target"`
	Drift     []Drift                  `json:"drift,omitempty"`
	Health    map[string]target.Health `json:"health,omitempty"`
	// Healthy: installed, no drift, and every key a target says the health of is ready.
	Healthy bool `json:"healthy"`
}

// Revision is a revision of the installation as history --json says it.
type Revision struct {
	saga.Revision
	Current bool `json:"current,omitempty"` // the one installed now
}

// LogResult is what log --json says of a saga: its last entries (Truncated: not all).
type LogResult struct {
	Saga      string       `json:"saga"`
	Entries   []saga.Entry `json:"entries"`
	Truncated bool         `json:"truncated,omitempty"`
}

// LogSagaResult is what log --stream ends with: which saga the events were of.
type LogSagaResult struct {
	Saga string `json:"saga"`
}

// LogRunsResult is what log --list --json says: the sagas of the installation.
type LogRunsResult struct {
	Runs []string `json:"runs"`
}

// LogPruneResult is what log --prune --json says: the sagas removed.
type LogPruneResult struct {
	Removed []string `json:"removed"`
}

// TemplateFile is a rendered file as template --json gives it; a secret is only named.
type TemplateFile struct {
	Key       string `json:"key"`
	Instance  *int   `json:"instance,omitempty"` // a file that differs per replica
	Path      string `json:"path"`
	Content   string `json:"content,omitempty"`
	Truncated bool   `json:"truncated,omitempty"`
	Secret    string `json:"secret,omitempty"` // the secret whose bytes the file is
}

// TemplateResult is what template --json says: the release's config files, rendered.
type TemplateResult struct {
	Files []TemplateFile `json:"files"`
}

// GraphNode is a key of the release as graph --json says it.
type GraphNode struct {
	Name      string   `json:"name"`
	Savepoint bool     `json:"savepoint,omitempty"`
	Waits     []string `json:"waits,omitempty"` // what must be ready before it starts
}

// GraphResult is what graph --json says: the dependency graph of the release.
type GraphResult struct {
	Nodes []GraphNode `json:"nodes"`
}

// CheckResult is what check --json says when the checks pass (a failure is a Failure with the problems).
type CheckResult struct {
	OK       bool             `json:"ok"`
	Problems []render.Problem `json:"problems,omitempty"`
}

// DiscoverResult is what discover --json says: the facts each target told, and which tell none.
type DiscoverResult struct {
	Told   map[string]*target.Facts `json:"told"`
	Untold []string                 `json:"untold,omitempty"`
}

// Explanation is what values explain says of a setting.
type Explanation struct {
	Pointer string             `json:"pointer"`
	Given   bool               `json:"given"`
	Value   any                `json:"value,omitempty"`
	Source  string             `json:"source,omitempty"`
	Why     string             `json:"why,omitempty"`
	Default any                `json:"default,omitempty"`
	Formula string             `json:"formula,omitempty"`
	Offered []render.Candidate `json:"offered,omitempty"`
	Schema  map[string]any     `json:"schema"`
}

// The states of a release, in the order a release goes through them, as pack next says them.
const (
	StateNew         = "new"         // no release here: pack init makes one
	StateUnrefreshed = "unrefreshed" // the pack or a target is not brought into .pack: pack refresh
	StateUnfilled    = "unfilled"    // settings the pack requires are not given
	StateInvalid     = "invalid"     // values the schema or the pack's rules refuse
	StateFailed      = "failed"      // a saga failed or was cut short, or what is installed is not well (pack status)
	StatePlanned     = "planned"     // what the release wants is not what is installed: an apply does it
	StateApplied     = "applied"     // what is installed is what the release wants
	StateError       = "error"       // something else stops it: blocking says what
)

// NextState is what pack next says: where the release is, what stops it, what to run.
type NextState struct {
	State    string   `json:"state"`
	Blocking []Error  `json:"blocking,omitempty"`
	Next     []string `json:"next"`
	// Facts say when the targets told their facts (pack discover), by target: what a formula reads is that old.
	Facts map[string]time.Time `json:"facts,omitempty"`
}
