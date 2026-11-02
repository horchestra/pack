// Package render is the core of the tool, and knows no target: a bundle and
// a values document become a Release — the selection and its order, every
// formula evaluated, every config file rendered. Where it runs is a target's
// business; the one thing render asks of a
// target is what a key is there — names,
// addresses, instances, limits (Target.Resolve) — because a neighbour's
// address ends up inside a config file.
package render

import (
	"io/fs"

	"github.com/horchestra/pack/sdk/packschema"
)

// Release is one render, before any target: what installs, in what order,
// built from what. Everything in it is settled — formulas evaluated, files
// rendered — and nothing in it names an object
// of a particular system; a target turns it into its own objects.
type Release struct {
	Installation string
	Pack         string
	Version      string
	// Rollout is the operator's, else the composition's; nil when neither says.
	Rollout *packschema.Rollout
	// Units are the selected keys in install order.
	Units []Unit
	// Credentials are the ones the render took from its CredentialSource: generated for a password the values
	// left out, or read from a path the values referred to.
	Credentials []Credential
	Report      Report
}

// Unit is one key of the pack: one program, run from an image, as
// one instance or as Replicas of them.
type Unit struct {
	Key       string
	Component string
	// Replicas is 0 for a key the environment gave no replicas — one instance,
	// addressed as the key — and n ≥ 1 where it did.
	Replicas      int
	Image         string
	RestartPolicy string
	// Command and Args: the image's entrypoint and cmd (or x-pack.command),
	// then the annotated leaves in author order.
	Command []string
	Args    []string
	// Env are the variables the component's annotated leaves make, then the
	// ones the environment adds.
	Env   []EnvVar
	Ports []Port
	// Resources are the cgroup requests and limits, as the environment gives them.
	Resources *Resources
	// Labels and Annotations are the environment's metadata.
	Labels      map[string]string
	Annotations map[string]string
	Readiness   *Readiness
	// Data are the volumes that outlive an instance.
	Data []Claim
	// Files are the rendered config files, ordered by path.
	Files []File
	// SecretFiles are files whose bytes are a secret's value, untouched.
	SecretFiles []SecretFile
	// Packages is the installation from OS packages, by the OS the pack names, rendered: for a target that runs no
	// image, instead of Image, Files and Services of its own.
	Packages map[string]Packages `json:",omitempty"`
}

// Packages is a pack's installation on one OS, rendered; packschema.OSPackages says the order.
type Packages struct {
	Prepare  []string
	Repo     []File
	Names    []string
	Commands [][]string
	Files    []File
	Services []string
}

type EnvVar struct {
	Name   string
	Value  string
	Secret *SecretRef // instead of Value
}

// SecretRef names a value the operator keeps outside the values document.
type SecretRef struct {
	Name string
	Key  string
}

type Port struct {
	Name string
	Port int
}

// Resources are cgroup settings: requests and limits by resource name.
type Resources struct {
	Requests map[string]string
	Limits   map[string]string
}

// Readiness is one probe, its port resolved to a number.
type Readiness struct {
	TCP  *TCPProbe
	HTTP *HTTPProbe
	Exec []string
}

type TCPProbe struct {
	Port int
}

type HTTPProbe struct {
	Port int
	Path string
}

type Claim struct {
	Name string
	Path string
	Size string
}

// File is one rendered config file. Its bytes may carry ${secret:<name>/<key>}
// where the values held a reference: the target resolves them where the file
// lands, so a credential never passes through the render as plaintext.
type File struct {
	Path string
	Mode *fs.FileMode
	// URL is where the file is downloaded from where it lands, instead of Content.
	URL string `json:",omitempty"`
	// Content is the file of a unit; nil for a per-instance file.
	Content []byte
	// Instances are a per-instance file's bytes, replica i at i; nil where a
	// replica's section was left empty and the file does not exist for it.
	Instances [][]byte
	// Secrets are the names the placeholders refer to, in order of appearance.
	Secrets []string
}

// PerInstance reports whether the file differs per replica.
func (f File) PerInstance() bool { return f.Instances != nil }

// SecretFile is a file whose bytes are a secret's value — a keytab, a
// keystore. Per-instance: the key is <key>.<index>, one per replica.
type SecretFile struct {
	Path        string
	Mode        *fs.FileMode
	Secret      SecretRef
	PerInstance bool
}

// Report is what came from where — for the operator's eyes.
type Report struct {
	Installation string   `json:"installation"`
	Namespace    string   `json:"namespace"`
	Pack         string   `json:"pack"`
	Order        []string `json:"order"`
	// Deps is the graph the order is a walk of: key → the keys it waits for (the packs it holds).
	Deps map[string][]string `json:"deps,omitempty"`
	// Targets: key → the name of the target it runs on, for the keys that do not run on the default one.
	Targets map[string]string `json:"targets,omitempty"`
	// Computed is what the formulas wrote, in the order they ran.
	Computed []Computed          `json:"computed,omitempty"`
	Secrets  map[string][]string `json:"secrets,omitempty"` // a target's: config object name → files
}

// Computed is one value a formula wrote: where — the key it was computed for and the pointer in that pack, as
// the author wrote it in PackCompute — and what. Two instances of one pack have the same pointers and different
// keys, so the pair never repeats.
type Computed struct {
	Key     string `json:"key"`
	Pointer string `json:"pointer"`
	Value   any    `json:"value"`
}
