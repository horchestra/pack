// Package release is the directory a command runs in.
//
// A command runs in a release: the directory it is started in (or -C names),
// the way terraform runs in the directory of a root module. What a pack is
// installed from, with which values, into which environment is read from that
// directory, so the same bare command gives the same result on a console and in CI:
//
//	release.yaml          pack: ../adh-show, values: [...], state: file|target
//	values.yaml             values: a document, or just the keys
//	*.values.yaml           more of them, merged
//	values/                 a directory of them, merged
//	env.yaml                the environment (target, provider, vault …)
//	env.<name>.yaml         the environment --env <name> (PACK_ENV) picks, instead of env.yaml
//
// The command line adds nothing to it. Values two files give differently are refused, whatever the order of the names; nothing is
// overridden silently.
package release

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"

	"sigs.k8s.io/yaml"

	"github.com/horchestra/pack/internal/oci"
)

// File is the optional file that names the release's pack and how it is kept.
const File = "release.yaml"

type Release struct {
	Dir    string   `json:"-"`
	Pack   string   `json:"pack,omitempty"`   // directory or registry reference to install
	Values []string `json:"values,omitempty"` // more values files or directories than the ones found by name
	// State is where the record of the installation is kept: file (.pack/state.json, the default), target, db,
	// or observe — no record at all: the plan is derived from what the target says it holds.
	State string `json:"state,omitempty"`
	// StateDSNEnv names the variable the DSN of the state database is in (state: db; PACK_STATE_DSN by default):
	// postgres://… or sqlite://<path>. The DSN is never in the file.
	StateDSNEnv string `json:"state_dsn_env,omitempty"`
	// ID is the installation's UUID. A file state makes and keeps its own at the first apply; state: target has
	// no file to keep it in, so it is written here.
	ID string `json:"id,omitempty"`
	// Targets say where `pack refresh` downloads the programs of the targets the environment uses from, by the
	// target's name.
	Targets map[string]TargetSource `json:"targets,omitempty"`
	// Registry is how the pack is read from a registry; a target's source says it for itself.
	Registry Registry `json:"registry"`

	values []string
	env    string // the environment file, "" — none
}

// Registry is how a registry is read: the one of the pack, or of a target's source. The file names the variables the
// credentials are in, never the credentials; a registry that names none is read anonymously, so credentials reach
// only the registry they were written for.
type Registry struct {
	PlainHTTP   bool   `json:"plain_http,omitempty"` // the registry speaks http, not https
	UsernameEnv string `json:"username_env,omitempty"`
	PasswordEnv string `json:"password_env,omitempty"`
}

// Options reads the credentials from the environment. It is called when the registry is about to be read: a release
// that never goes to it needs no variables.
func (r Registry) Options() (oci.Options, error) {
	o := oci.Options{PlainHTTP: r.PlainHTTP}
	if len(r.UsernameEnv) == 0 && len(r.PasswordEnv) == 0 {
		return o, nil
	}
	if len(r.UsernameEnv) == 0 || len(r.PasswordEnv) == 0 {
		return o, errors.New("username_env and password_env go together")
	}
	o.User, o.Password = os.Getenv(r.UsernameEnv), os.Getenv(r.PasswordEnv)
	if len(o.User) == 0 {
		return o, fmt.Errorf("username_env: %s is not set", r.UsernameEnv)
	}
	if len(o.Password) == 0 {
		return o, fmt.Errorf("password_env: %s is not set", r.PasswordEnv)
	}
	return o, nil
}

// TargetSource is where the program of a target comes from: the URL alone (docker: oci://…), or the URL with its
// options (docker: {url: oci://…, plain_http: true, username_env: …, password_env: …}), and the image the program
// runs from in a container (image: oci://host/repo:tag — runtime: {type: docker} in env.yaml).
type TargetSource struct {
	// URL is an http(s) URL of the program or of an archive that holds it, or oci://host/repo:tag. {os} and {arch}
	// stand for the machine's.
	URL string `json:"url,omitempty"`
	// Image is the target's image, oci://host/repo:tag: the program and what it needs (ansible, docker's CLI …).
	Image string `json:"image,omitempty"`
	// Registry is how the registry of an oci:// URL is read.
	Registry
}

func (s *TargetSource) UnmarshalJSON(raw []byte) error {
	if json.Unmarshal(raw, &s.URL) == nil {
		return nil
	}
	type plain TargetSource
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields() // the strictness of the file does not reach through this method
	return dec.Decode((*plain)(s))
}

// Load reads the release in dir. envName picks env.<envName>.yaml.
func Load(dir, envName string) (*Release, error) {
	w := &Release{Dir: dir}
	if raw, err := os.ReadFile(filepath.Join(dir, File)); err == nil {
		if err := yaml.UnmarshalStrict(raw, w); err != nil {
			return nil, fmt.Errorf("%s: %w", filepath.Join(dir, File), err)
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}

	var found []string
	add := func(p string) {
		if st, err := os.Stat(filepath.Join(dir, p)); err == nil && (st.IsDir() || st.Mode().IsRegular()) {
			found = append(found, p)
		}
	}

	for _, ext := range []string{"yaml", "yml", "json"} {
		add("values." + ext)
	}

	more, _ := filepath.Glob(filepath.Join(dir, "*.values.*"))
	slices.Sort(more)

	for _, m := range more {
		if rel, err := filepath.Rel(dir, m); err == nil {
			found = append(found, rel)
		}
	}

	add("values")
	found = append(found, w.Values...)
	w.values = found

	base := "env"
	if len(envName) > 0 {
		base = "env." + envName
	}

	for _, ext := range []string{"yaml", "yml", "json"} {
		if p := filepath.Join(dir, base+"."+ext); fileExists(p) {
			w.env = p
			break
		}
	}

	if len(envName) > 0 && len(w.env) == 0 {
		return nil, fmt.Errorf("--env %s: no %s.yaml in %s", envName, base, dir)
	}
	return w, nil
}

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.Mode().IsRegular()
}

// ValuesFiles are the release's values.
func (w *Release) ValuesFiles() []string {
	out := make([]string, 0, len(w.values))
	for _, p := range w.values {
		out = append(out, filepath.Join(w.Dir, p))
	}
	return out
}

// ErrNoPack is a release that names no pack.
var ErrNoPack = errors.New("no pack")

// PackRef is the release's pack: a directory or a registry reference (oci://…).
func (w *Release) PackRef() (string, error) {
	if len(w.Pack) == 0 {
		return "", fmt.Errorf("%w: write `pack:` in %s", ErrNoPack, File)
	}
	return w.Pack, nil
}

// Config is the release's environment file, "" when it has none.
func (w *Release) Config() string { return w.env }

// Update changes release.yaml the way `pack refresh` needs: set puts values into the document as it is, and the file is
// written back. It is made if there is none. The comments of the file do not survive: it is a few keys.
func Update(dir string, set func(doc map[string]any)) error {
	path := filepath.Join(dir, File)
	doc := map[string]any{}
	if raw, err := os.ReadFile(path); err == nil {
		if err := yaml.Unmarshal(raw, &doc); err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	set(doc)
	raw, err := yaml.Marshal(doc)
	if err != nil {
		return err
	}
	return os.WriteFile(path, raw, 0o644)
}
