// Package packschema is the vocabulary a component or a composition speaks on
// top of JSON Schema — the x-pack-* keywords — and the generator that turns a
// Go default value into such a schema.
//
// A validator ignores keywords it does not know, which is what lets the
// vocabulary live inside a standard schema; it is also why nothing but this
// package checks the keywords. The types here are that check: an annotation is
// decoded into them strictly, and a key they do not have is a refusal.
package packschema

// Root is `x-pack` on the root of a component's schema: what the component is to the system around it — the
// ports it listens on, the volumes it keeps, the least it needs, when it is ready — and how it changes versions.
// How many instances run, where, and with what resources is the environment's, not the component's.
type Root struct {
	// Ports are the ports the component listens on, by name; readiness and the neighbours' addresses refer to a
	// port by its name.
	Ports map[string]int `json:"ports,omitempty"`
	// Data are the volumes that outlive an instance, by name.
	Data map[string]Volume `json:"data,omitempty"`
	// Minimum is the least the component can run on: a lower bound for the resource requests the environment
	// gives it (cpu, memory).
	Minimum   map[string]string `json:"minimum,omitempty"`
	Readiness *Readiness        `json:"readiness,omitempty"`
	Upgrade   *Upgrade          `json:"upgrade,omitempty"`
	Hooks     []Hook            `json:"hooks,omitempty"`
	Command   []string          `json:"command,omitempty"` // replaces the image's entrypoint+cmd wholesale
	// RestartPolicy Never makes the component a run-to-completion step of the
	// set (a bootstrap, a schema migration): it runs once, in order, and the
	// set waits for it to finish rather than to become ready.
	RestartPolicy string `json:"restartPolicy,omitempty" jsonschema:"enum=Always,enum=OnFailure,enum=Never"`

	// Savepoints are the dependencies (by their field's json name) after which a rollback stops: when the
	// dependency is done, it and what it waits for stay installed through a later failure. Only the root of a
	// pack has them.
	Savepoints []string `json:"savepoints,omitempty"`
	Rollout    *Rollout `json:"rollout,omitempty"`
}

// Volume is one volume a component keeps: where it is mounted is the component's fact, how big is the
// operator's.
type Volume struct {
	MountPath string `json:"mountPath" jsonschema:"required,pattern=^/"`
	Size      string `json:"size,omitempty"` // the default size
}

type Readiness struct {
	TCP  string   `json:"tcp,omitempty"`  // a port name
	HTTP string   `json:"http,omitempty"` // "<port>/<path>"
	Exec []string `json:"exec,omitempty"`
}

type Upgrade struct {
	From string `json:"from" jsonschema:"required"` // version constraint on the version being upgraded from
}

// Hook is one step the component contributes to the saga of its own
// installation or upgrade: an install hook runs once around the first apply
// (`hdfs namenode -format` before it, a schema bootstrap after), an upgrade or
// downgrade hook around a version transition.
type Hook struct {
	Name       string   `json:"name" jsonschema:"required,minLength=1"`
	Phase      Phase    `json:"phase" jsonschema:"required,enum=preInstall,enum=postInstall,enum=preUpgrade,enum=postUpgrade,enum=preDowngrade,enum=postDowngrade"`
	When       *When    `json:"when,omitempty"`  // nil: on any image change; install hooks may constrain `to` only
	Image      string   `json:"image,omitempty"` // component hook: new (default) | old; composition hook: a member's name
	Command    []string `json:"command" jsonschema:"required,minItems=1"`
	Exclusive  bool     `json:"exclusive,omitempty"`
	Timeout    string   `json:"timeout,omitempty"`
	Idempotent bool     `json:"idempotent,omitempty"`
	Compensate *Step    `json:"compensate,omitempty"`
}

type Step struct {
	Image   string   `json:"image,omitempty" jsonschema:"enum=new,enum=old"`
	Command []string `json:"command" jsonschema:"required,minItems=1"`
}

type When struct {
	From string `json:"from,omitempty"`
	To   string `json:"to,omitempty"`
}

type Rollout struct {
	MaxUnavailable int `json:"maxUnavailable,omitempty"`
}

// File is `x-pack-file` on a subtree: the subtree, whole, becomes one file.
type File struct {
	Path        string `json:"path" jsonschema:"required,pattern=^/"`
	Format      string `json:"format" jsonschema:"required,enum=json,enum=yaml,enum=properties,enum=env,enum=shell,enum=ini,enum=flagfile,enum=lines,enum=hadoop-xml,enum=toml,enum=text,enum=template,enum=secret"`
	Template    string `json:"template,omitempty"`
	ParseAs     string `json:"parseAs,omitempty"`
	Mode        string `json:"mode,omitempty" jsonschema:"pattern=^0?[0-7]{3}$"`
	PerInstance bool   `json:"perInstance,omitempty"`
}

// Arg is `x-pack-arg` on a leaf: the leaf lands in argv.
type Arg struct {
	Flag      string `json:"flag" jsonschema:"required,minLength=1"`
	Prefix    string `json:"prefix,omitempty"`
	Each      bool   `json:"each,omitempty"`
	OmitEmpty bool   `json:"omitEmpty,omitempty"`
}

// Formats every emitter knows, in the order the spec lists them.
var Formats = []string{"json", "yaml", "properties", "env", "shell", "ini", "flagfile", "lines", "hadoop-xml", "toml", "text", "template", FormatRaw}

// FormatRaw is a file of the component's, verbatim: the template it names,
// byte for byte, no template language — a script, a dump, an archive.
const FormatRaw = "raw"

// FormatSecret is `x-pack-file` on a password leaf: not an emitter — the
// referenced secret's key is mounted at the path, bytes untouched (a keytab, a
// keystore, a certificate).
const FormatSecret = "secret"

type Phase string

const (
	PreMutate     Phase = "preMutate"
	PostMutate    Phase = "postMutate"
	PreInstall    Phase = "preInstall"
	PostInstall   Phase = "postInstall"
	PreUpgrade    Phase = "preUpgrade"
	PostUpgrade   Phase = "postUpgrade"
	PreDowngrade  Phase = "preDowngrade"
	PostDowngrade Phase = "postDowngrade"
)

// Phases a hook may belong to — the saga's. The mutate phases are not a
// hook's: a hook is a step around an installation or a transition, and no
// document is being mutated there.
var Phases = []Phase{PreInstall, PostInstall, PreUpgrade, PostUpgrade, PreDowngrade, PostDowngrade}

// RulePhases a rule may be checked at: the document phases — preMutate, on
// the values as written, before the formulas run; postMutate, on the
// effective document they produce.
var RulePhases = []Phase{PreMutate, PostMutate}

// Rule is `x-pack-rules` on a node: a CEL expression over the same variables
// a formula reads (values, limits, me, pack), which must hold — a false one
// is a refusal with Message, an evaluation error is a refusal with the error.
// Empty Phases means preMutate: the document as the operator wrote it, so a
// rule never trips on what a formula filled in.
type Rule struct {
	Rule    string  `json:"rule" jsonschema:"required,minLength=1"`
	Message string  `json:"message,omitempty"`
	Phases  []Phase `json:"phases,omitempty"`
}

// IsInstall says whether the phase belongs to the first installation rather
// than to a version transition.
func IsInstall(phase Phase) bool { return phase == PreInstall || phase == PostInstall }

// The foreign $ref scheme a component schema may use: a component of the pack, by name.
const PackScheme = "pack://"

// SecretRef is the only thing a `format: password` leaf may hold.
type SecretRef struct {
	Name string `json:"name" jsonschema:"required,minLength=1"`
	Key  string `json:"key" jsonschema:"required,minLength=1"`
}

// Secret is the Go type of password leaf: it marshals as {secretRef: …} and
// its schema is a string with format password (the validator swaps that for
// $defs/secretRef, see the loader).
type Secret struct {
	SecretRef *SecretRef `json:"secretRef,omitempty"`
}

func (s Secret) IsZero() bool {
	return s.SecretRef == nil
}
