// Package packschema is the vocabulary a component or a composition speaks on
// top of JSON Schema — the x-pack-* keywords — and the generator that turns a
// Go default value into such a schema.
//
// A validator ignores keywords it does not know, which is what lets the
// vocabulary live inside a standard schema; it is also why nothing but this
// package checks the keywords. The types here are that check: an annotation is
// decoded into them strictly, and a key they do not have is a refusal.
package packschema

import (
	"fmt"
	"regexp"
	"slices"
)

// Root is `x-pack` on the root of a component's schema: what the component is to the system around it — the
// ports it listens on, the volumes it keeps, the least it needs, when it is ready — and how it changes versions.
// How many instances run, where, and with what resources is the environment's, not the component's.
type Root struct {
	// Ports are the ports the component listens on, by name; readiness and the neighbours' addresses refer to a
	// port by its name.
	Ports map[string]int `json:"ports,omitempty"`
	// Data are the volumes that outlive an instance, by name.
	Data map[string]Volume `json:"data,omitempty"`
	// Content are the files of the component: each is written from one of the pack's content/ files or from a
	// subtree of its values.
	Content []File `json:"content,omitempty"`
	// Packages is how a target that installs OS packages runs the component instead of the image, by OS: the key is the
	// target's to match, "debian-13", "debian", a family, any OS at all — none of them is known here.
	Packages map[string]OSPackages `json:"packages,omitempty"`
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

// OSPackages is the installation on one OS, in this order: Prepare installed, Repo delivered, Names installed, Commands
// run, Content delivered, Services started. Each OS has its own files and units: the same application reads another
// path there, and versions differ between OSes and from the image's, so a name says its version in the OS's own words
// ("postgresql-16=16.4-1.pgdg12+1"), Meta.Version says nothing of it. A name, a command or the URL of a file is a template too, so
// that where the packages come from can be a value of the pack: upstream by default, a mirror (oci-packer proxy) when
// the operator says. The target resolves ${os:codename}, ${os:version} and ${os:arch} in what it delivers and installs.
type OSPackages struct {
	Prepare  []string   `json:"prepare,omitempty"`
	Repo     []File     `json:"repo,omitempty"`
	Names    []string   `json:"names" jsonschema:"required,minItems=1"`
	Commands [][]string `json:"commands,omitempty"`
	Content  []File     `json:"content,omitempty"`
	Services []string   `json:"services,omitempty"`
}

// File is one file in the component's containers, at Path. It comes from exactly one of:
//
//	Source    a file of the pack's content/, byte for byte: whatever {{ }} it holds is not pack's to read
//	Template  a file of content/, a template over the values the pack sees: its own and those of the packs it holds,
//	          by their fields, without their passwords; pack is the context. With Values, it is written only when
//	          that value is there
//	URL       downloaded where it lands; only in Packages
//	Values    a field's subtree (a pointer without the leading /: "conf", "tls/key"), written in Format, the name of
//	          Path saying it when there is no Format; format secret mounts a password leaf's key untouched
type File struct {
	Path     string `json:"path" jsonschema:"required,pattern=^/"`
	Source   string `json:"source,omitempty"`
	Template string `json:"template,omitempty"`
	URL      string `json:"url,omitempty"`
	Values   string `json:"values,omitempty"`
	Format   string `json:"format,omitempty" jsonschema:"enum=json,enum=yaml,enum=properties,enum=env,enum=shell,enum=ini,enum=flagfile,enum=lines,enum=hadoop-xml,enum=toml,enum=text,enum=secret"`
	// ParseAs is what a template's output is checked to be, when it is a format that can be checked.
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
var Formats = []string{"json", "yaml", "properties", "env", "shell", "ini", "flagfile", "lines", "hadoop-xml", "toml", "text"}

// FormatSecret is a File of a password leaf: not an emitter — the
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

// Diagnosis is one of `x-pack-diagnose` on the root of a pack: what a failure of the pack looks like (Match, a regular
// expression of RE2 over the error of the failed step and the lines of its logs), what it is (Cause, said in other
// languages by docs/<lang>.yaml's diagnose:), and the ways out pack diagnose offers.
type Diagnosis struct {
	ID    string `json:"id" jsonschema:"required,minLength=1"`
	Match string `json:"match" jsonschema:"required,minLength=1"`
	Cause string `json:"cause" jsonschema:"required,minLength=1"`
	Fix   []Fix  `json:"fix,omitempty"`
	// I18n is the cause in the other languages of docs/ — written by the sdk, not the author.
	I18n map[string]map[string]string `json:"x-pack-i18n,omitempty"`
}

// Fix is a way out of a failure: a value for a setting — Patch, the setting's pointer in the pack, and Expr, CEL over
// what a formula reads (the facts among it) — or a command to run, Command. Why says what it does.
type Fix struct {
	Patch   string `json:"patch,omitempty"`
	Expr    string `json:"expr,omitempty"`
	Command string `json:"command,omitempty"`
	Why     string `json:"why,omitempty"`
}

// Suggest is `x-pack-suggest` on a node: a CEL expression over what a formula reads — the facts of the key's
// target (facts), the values, the other keys (pack) — whose answer pack fill offers for the setting, with why; null
// offers nothing. The operator, or a model, takes it or not: a suggestion is never a value by itself.
type Suggest struct {
	Expr string `json:"expr" jsonschema:"required,minLength=1"`
	Why  string `json:"why,omitempty"`
}

// Rule is `x-pack-rules` on a node: a CEL expression over the same variables
// a formula reads (values, limits, me, pack), which must hold — a false one
// is a refusal with Message, an evaluation error is a refusal with the error.
// Empty Phases means preMutate: the document as the operator wrote it, so a
// rule never trips on what a formula filled in.
type Rule struct {
	// ID names the rule among the pack's, so that docs/<lang>.yaml can say its message (rules: {<id>: …}) in other
	// languages; unique in the pack.
	ID      string  `json:"id,omitempty"`
	Rule    string  `json:"rule" jsonschema:"required,minLength=1"`
	Message string  `json:"message,omitempty"`
	Phases  []Phase `json:"phases,omitempty"`
	// I18n is the message in the other languages of docs/, by language — written by the sdk, not the author.
	I18n map[string]map[string]string `json:"x-pack-i18n,omitempty"`
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

// Action is one of `x-pack-actions` on the root of a pack: an operation the author names — backup, restore, rotate,
// vacuum — that pack run performs on a key of the pack, as a hook is performed, with the installed version's image.
// The list is closed: what a person or a model may run on the installation besides apply is what the pack says here.
// Command's words are templates over the parameters ({{ .to }}); Params are what it takes, written as the JSON
// Schema of an object (params) so that a form is built on it.
type Action struct {
	Name        string   `json:"name" jsonschema:"required,pattern=^[a-z][a-z0-9-]*$"`
	Description string   `json:"description,omitempty"`
	Command     []string `json:"command" jsonschema:"required,minItems=1"`
	Timeout     string   `json:"timeout,omitempty"`
	// Mutating: the action changes the installation (restore, rotate) — not only reads it (backup): it is a plan
	// of its own to run.approve, asked for under plan.
	Mutating bool `json:"mutating,omitempty"`
	// Approve: the action is never run unasked but under run.approve: auto (a restore over the data).
	Approve bool `json:"approve,omitempty"`
	// Params are the author's: the sdk writes them as Schema.
	Params []Param       `json:"-"`
	Schema *ParamsSchema `json:"params,omitempty"`
	// I18n is the description in the other languages of docs/ — written by the sdk, not the author.
	I18n map[string]map[string]string `json:"x-pack-i18n,omitempty"`
}

// Param is a parameter of an action: a scalar of Type (string, integer, number, boolean).
type Param struct {
	Name        string                       `json:"-"`
	Required    bool                         `json:"-"`
	Type        string                       `json:"type"`
	Description string                       `json:"description,omitempty"`
	Default     any                          `json:"default,omitempty"`
	Enum        []any                        `json:"enum,omitempty"`
	I18n        map[string]map[string]string `json:"x-pack-i18n,omitempty"`
}

// ParamsSchema is the parameters of an action as the JSON Schema of an object.
type ParamsSchema struct {
	Type                 string           `json:"type"`
	Properties           map[string]Param `json:"properties"`
	Required             []string         `json:"required,omitempty"`
	AdditionalProperties bool             `json:"additionalProperties"`
}

// ParamTypes are the types a parameter may be.
var ParamTypes = []string{"string", "integer", "number", "boolean"}

// ActionName and ParamName are what the names of an action and of its parameter look like.
var (
	ActionName = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)
	ParamName  = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)
)

// Seal checks an action as the author wrote it and makes Schema of its Params.
func (a *Action) Seal() error {
	if !ActionName.MatchString(a.Name) {
		return fmt.Errorf("x-pack-actions: %q: a name of lower case letters, digits and dashes", a.Name)
	}
	if len(a.Command) == 0 {
		return fmt.Errorf("x-pack-actions %s: no command", a.Name)
	}
	if len(a.Params) == 0 {
		a.Schema = nil
		return nil
	}
	ps := &ParamsSchema{Type: "object", Properties: map[string]Param{}}
	for _, p := range a.Params {
		if !ParamName.MatchString(p.Name) {
			return fmt.Errorf("x-pack-actions %s: parameter %q: a name of lower case letters, digits and underscores", a.Name, p.Name)
		}
		if _, dup := ps.Properties[p.Name]; dup {
			return fmt.Errorf("x-pack-actions %s: parameter %q twice", a.Name, p.Name)
		}
		if !slices.Contains(ParamTypes, p.Type) {
			return fmt.Errorf("x-pack-actions %s: parameter %s: type is one of %v", a.Name, p.Name, ParamTypes)
		}
		ps.Properties[p.Name] = p
		if p.Required {
			ps.Required = append(ps.Required, p.Name)
		}
	}
	slices.Sort(ps.Required)
	a.Schema = ps
	return nil
}
