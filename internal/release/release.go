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
	"fmt"
	"os"
	"path/filepath"
	"slices"

	"sigs.k8s.io/yaml"
)

// File is the optional file that names the release's pack and how it is kept.
const File = "release.yaml"

type Release struct {
	Dir    string   `json:"-"`
	Pack   string   `json:"pack,omitempty"`   // directory or registry reference to install
	Values []string `json:"values,omitempty"` // more values files or directories than the ones found by name
	// State is where the record of the installation is kept: file (.pack/state.json, the default) or target.
	State string `json:"state,omitempty"`
	// ID is the installation's UUID. A file state makes and keeps its own at the first apply; state: target has
	// no file to keep it in, so it is written here.
	ID string `json:"id,omitempty"`
	// Targets say where `pack init` downloads the programs of the targets the environment uses from, by the
	// target's name: an http(s) URL of the program or of an archive that holds it, or oci://host/repo:tag. {os}
	// and {arch} stand for the machine's.
	Targets map[string]string `json:"targets,omitempty"`

	values []string
	env    string // the environment file, "" — none
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

// PackRef is the release's pack: a directory or a registry reference (oci://…).
func (w *Release) PackRef() (string, error) {
	if len(w.Pack) == 0 {
		return "", fmt.Errorf("no pack: write `pack:` in %s", File)
	}
	return w.Pack, nil
}

// Config is the release's environment file, "" when it has none.
func (w *Release) Config() string { return w.env }

// Update changes release.yaml the way `pack init` needs: set puts values into the document as it is, and the file is
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
