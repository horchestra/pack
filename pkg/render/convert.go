package render

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"maps"
	"path"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/horchestra/pack/pkg/bundle"
	"github.com/horchestra/pack/pkg/emit"
	"github.com/horchestra/pack/sdk/packschema"
	"github.com/horchestra/pack/sdk/schema"
)

// convert builds the release from the effective document: one unit per
// selected key, in install order.
func (r *render) convert(v Values) (*Release, error) {
	out := &Release{Installation: r.ctx.Installation, Pack: r.b.Name, Version: r.b.Version}
	if v.Rollout != nil {
		out.Rollout = v.Rollout
	} else if r.b.Root.Root.Rollout != nil {
		out.Rollout = r.b.Root.Root.Rollout
	}
	out.Credentials = r.credentials
	for _, key := range r.order {
		u, err := r.unit(key)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", key, err)
		}
		out.Units = append(out.Units, *u)
	}
	out.Report = r.report
	return out, nil
}

// file is one x-pack-file found under a key, with the node it hangs on.
type file struct {
	ptr    string
	node   *schema.Node
	schema *schema.Schema
}

func (r *render) unit(key string) (*Unit, error) {
	comp := r.b.Packs[r.b.KeyPack[key]]
	keyDoc, _ := get(r.doc, "/"+key)
	km, _ := keyDoc.(map[string]any)
	if km == nil {
		km = map[string]any{}
	}
	u := &Unit{Key: key, Component: comp.Name, Replicas: r.replicas(key)}

	// image: empty for a component that runs on hosts, not in a container (the ansible target installs its packages);
	// a target that runs containers refuses a unit that has none
	u.Image = comp.ImageRef
	u.RestartPolicy = comp.Root.RestartPolicy

	// argv: the image's entrypoint+cmd (or x-pack.command) ⊕ annotated leaves in author order
	if len(comp.Root.Command) > 0 {
		u.Command = comp.Root.Command
	} else if comp.Image != nil {
		u.Command = comp.Image.Config.Entrypoint
		u.Args = append([]string{}, comp.Image.Config.Cmd...)
	}
	var files []file
	var envs []EnvVar
	err := r.b.Composition.Visit(func(ptr string, s *schema.Schema, n *schema.Node) error {
		if !strings.HasPrefix(ptr, "/"+key+"/") && ptr != "/"+key {
			return nil
		}
		rel := strings.TrimPrefix(ptr, "/"+key)
		if n.Arg != nil {
			val, ok := get(km, rel)
			args, err := argv(n.Arg, val, ok)
			if err != nil {
				return fmt.Errorf("%s: %w", ptr, err)
			}
			u.Args = append(u.Args, args...)
		}
		if len(n.Env) > 0 {
			val, ok := get(km, rel)
			if ok && val != nil {
				e, err := envVar(n.Env, val)
				if err != nil {
					return fmt.Errorf("%s: %w", ptr, err)
				}
				envs = append(envs, e)
			}
		}
		if n.File != nil {
			f := file{ptr: ptr, node: n, schema: s}
			files = append(files, f)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	// env: the annotated leaves, then the ones the environment adds
	pl := r.ctx.Keys[key]
	for _, e := range pl.Env {
		for _, existing := range envs {
			if existing.Name == e.Name {
				return nil, fmt.Errorf("environment: keys.%s.env %s: the component declares that variable", key, e.Name)
			}
		}
		envs = append(envs, e)
	}
	u.Env = envs

	// resources (cgroups) and metadata: the environment's
	u.Resources = pl.Resources
	u.Labels, u.Annotations = pl.Labels, pl.Annotations

	// ports: the pack's own words
	for _, name := range slices.Sorted(maps.Keys(comp.Root.Ports)) {
		u.Ports = append(u.Ports, Port{Name: name, Port: comp.Root.Ports[name]})
	}

	// readiness
	if rd := comp.Root.Readiness; rd != nil {
		u.Readiness = &Readiness{}
		switch {
		case len(rd.TCP) > 0:
			p := comp.Root.Ports[rd.TCP]
			u.Readiness.TCP = &TCPProbe{Port: p}
		case len(rd.HTTP) > 0:
			pn, pth, _ := strings.Cut(rd.HTTP, "/")
			p := comp.Root.Ports[pn]
			u.Readiness.HTTP = &HTTPProbe{Port: p, Path: "/" + pth}
		case len(rd.Exec) > 0:
			u.Readiness.Exec = rd.Exec
		}
	}

	// data: the volumes the pack keeps
	for _, name := range slices.Sorted(maps.Keys(comp.Root.Data)) {
		d := comp.Root.Data[name]
		u.Data = append(u.Data, Claim{Name: name, Path: d.MountPath, Size: d.Size})
	}
	if u.SecretFiles, err = r.secretFiles(key, km, files); err != nil {
		return nil, err
	}
	if u.Files, err = r.files(key, comp, km, files); err != nil {
		return nil, err
	}
	return u, nil
}

// files renders every x-pack-file of the unit but those of format secret,
// per replica when the file is per-instance. A section nobody filled is a
// file that does not exist.
func (r *render) files(key string, comp *bundle.Pack, km map[string]any, files []file) ([]File, error) {
	var out []File
	for _, f := range files {
		if f.node.File.Format == packschema.FormatSecret {
			continue
		}
		rel := strings.TrimPrefix(f.ptr, "/"+key)
		rf := File{Path: f.node.File.Path}
		mode, err := fileMode(f)
		if err != nil {
			return nil, err
		}
		rf.Mode = mode
		templates := comp.Templates
		renderOne := func(doc map[string]any, instance map[string]any) ([]byte, error) {
			if f.node.File.Format == packschema.FormatRaw { // shipped as it is: nothing in the values asks for it
				tmpl, ok := templates[f.node.File.Template]
				if !ok {
					return nil, fmt.Errorf("%s: file %q not found", f.ptr, f.node.File.Template)
				}
				return tmpl, nil
			}
			sub, ok := get(doc, rel)
			if !ok || sub == nil {
				return nil, nil
			}
			sub = schema.DeepCopy(sub)
			rf.Secrets = union(rf.Secrets, placeholders(sub))
			var b []byte
			var err error
			if f.node.File.Format == "template" {
				tmpl, ok := templates[f.node.File.Template]
				if !ok {
					return nil, fmt.Errorf("%s: template %q not found", f.ptr, f.node.File.Template)
				}
				data := map[string]any{"values": doc, "pack": r.packContext(key, instance)}
				if m, ok := sub.(map[string]any); ok {
					maps.Copy(data, m)
				} else {
					data["value"] = sub
				}
				b, err = emit.Template(f.node.File.Template, tmpl, data, f.node.File.ParseAs)
			} else {
				b, err = emit.Emit(f.node.File.Format, sub)
			}
			if err != nil {
				return nil, fmt.Errorf("%s: %w", f.ptr, err)
			}
			return b, nil
		}
		if f.node.File.PerInstance {
			n := max(r.replicas(key), 1)
			rf.Instances = make([][]byte, n)
			some := false
			for i := range n {
				doc, err := r.instanceDoc(key, i)
				if err != nil {
					return nil, err
				}
				if rf.Instances[i], err = renderOne(doc, r.instanceVars(key, i)); err != nil {
					return nil, err
				}
				some = some || rf.Instances[i] != nil
			}
			if !some {
				continue
			}
		} else {
			if rf.Content, err = renderOne(km, nil); err != nil {
				return nil, err
			}
			if rf.Content == nil {
				continue
			}
		}
		out = append(out, rf)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	for i := 1; i < len(out); i++ {
		if out[i].Path == out[i-1].Path {
			return nil, fmt.Errorf("%s/%s is written twice (two files at one path)", path.Dir(out[i].Path), path.Base(out[i].Path))
		}
	}
	return out, nil
}

func fileMode(f file) (*fs.FileMode, error) {
	if len(f.node.File.Mode) == 0 {
		return nil, nil
	}
	m, err := strconv.ParseUint(f.node.File.Mode, 8, 32)
	if err != nil {
		return nil, fmt.Errorf("%s: mode %q: %w", f.ptr, f.node.File.Mode, err)
	}
	mode := fs.FileMode(m)
	return &mode, nil
}

// secretFiles: every x-pack-file of format secret, as the reference its leaf
// holds — the bytes are the secret's, so a keytab or a keystore arrives as it
// was stored.
func (r *render) secretFiles(key string, km map[string]any, files []file) ([]SecretFile, error) {
	var out []SecretFile
	for _, f := range files {
		if f.node.File.Format != packschema.FormatSecret {
			continue
		}
		rel := strings.TrimPrefix(f.ptr, "/"+key)
		val, ok := get(km, rel)
		if !ok || val == nil {
			continue // not provided: nothing to mount (the schema's required decides whether that is an error)
		}
		m, _ := val.(map[string]any)
		sr, _ := m["secretRef"].(map[string]any)
		if sr == nil {
			return nil, fmt.Errorf("%s: a secret file needs {secretRef: {name, key}}", f.ptr)
		}
		name, k := str(sr["name"]), str(sr["key"])
		if len(name) == 0 || len(k) == 0 {
			return nil, fmt.Errorf("%s: secretRef needs both name and key", f.ptr)
		}
		mode, err := fileMode(f)
		if err != nil {
			return nil, err
		}
		out = append(out, SecretFile{Path: f.node.File.Path, Mode: mode, Secret: SecretRef{Name: name, Key: k}, PerInstance: f.node.File.PerInstance})
	}
	return out, nil
}

// placeholders replaces every {secretRef: {name, key}} in a subtree by the
// placeholder the node resolves, and returns the secret names it saw.
func placeholders(v any) []string {
	var names []string
	var walk func(v any) any
	walk = func(v any) any {
		switch x := v.(type) {
		case map[string]any:
			if sr, ok := x["secretRef"].(map[string]any); ok && len(x) == 1 {
				name, key := str(sr["name"]), str(sr["key"])
				names = union(names, []string{name})
				return fmt.Sprintf("${secret:%s/%s}", name, key)
			}
			for k, e := range x {
				x[k] = walk(e)
			}
		case []any:
			for i, e := range x {
				x[i] = walk(e)
			}
		}
		return v
	}
	walk(v)
	return names
}

// argv renders one annotated leaf into argv elements.
func argv(a *packschema.Arg, val any, present bool) ([]string, error) {
	if !present || val == nil {
		if a.OmitEmpty {
			return nil, nil
		}
		return nil, fmt.Errorf("x-pack-arg %s: no value and not omitEmpty", a.Flag)
	}
	if s, ok := val.(string); ok && len(s) == 0 && a.OmitEmpty {
		return nil, nil
	}
	var values []string
	if list, ok := val.([]any); ok && a.Each {
		for _, e := range list {
			s, err := scalarString(e)
			if err != nil {
				return nil, err
			}
			values = append(values, s)
		}
	} else {
		s, err := scalarString(val)
		if err != nil {
			return nil, err
		}
		values = []string{s}
	}
	var out []string
	for _, v := range values {
		v = a.Prefix + v
		if strings.HasSuffix(a.Flag, "=") || strings.HasSuffix(a.Flag, ":") {
			out = append(out, a.Flag+v)
		} else {
			out = append(out, a.Flag, v)
		}
	}
	return out, nil
}

func envVar(name string, val any) (EnvVar, error) {
	if m, ok := val.(map[string]any); ok {
		if sr, ok := m["secretRef"].(map[string]any); ok {
			return EnvVar{Name: name, Secret: &SecretRef{Name: str(sr["name"]), Key: str(sr["key"])}}, nil
		}
		return EnvVar{}, fmt.Errorf("x-pack-env %s: the leaf is an object", name)
	}
	s, err := scalarString(val)
	if err != nil {
		return EnvVar{}, fmt.Errorf("x-pack-env %s: %w", name, err)
	}
	return EnvVar{Name: name, Value: s}, nil
}

func scalarString(v any) (string, error) {
	switch x := v.(type) {
	case string:
		return x, nil
	case bool:
		return strconv.FormatBool(x), nil
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64), nil
	case int:
		return strconv.Itoa(x), nil
	case int64:
		return strconv.FormatInt(x, 10), nil
	case json.Number:
		return x.String(), nil
	case []any:
		parts := make([]string, 0, len(x))
		for _, e := range x {
			s, err := scalarString(e)
			if err != nil {
				return "", err
			}
			parts = append(parts, s)
		}
		return strings.Join(parts, ","), nil
	}
	return "", fmt.Errorf("not a scalar (%T)", v)
}

func str(v any) string {
	s, _ := v.(string)
	return s
}

func union(a, b []string) []string {
	seen := map[string]bool{}
	for _, s := range a {
		seen[s] = true
	}
	for _, s := range b {
		if !seen[s] {
			seen[s] = true
			a = append(a, s)
		}
	}
	return a
}
