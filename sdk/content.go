package sdk

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"

	"github.com/horchestra/pack/sdk/packschema"
)

// ContentDir is the directory of a pack's files.
const ContentDir = "content"

// Templates are the pack's files: everything under its content/ directory, by the name relative to it. The apply
// of a pack held as Go reads them here — nothing is written first.
func Templates(p Pack) (map[string][]byte, error) { return content(p) }

// annotations are the artifact's annotations: the pack's own words and the OCI image spec's, from Meta.
func (m Meta) annotations() (map[string]string, error) {
	out := map[string]string{"pack.name": m.Name, "pack.version": m.Version, "org.opencontainers.image.title": m.Name, "org.opencontainers.image.version": m.Version}
	for k, v := range map[string]string{
		"org.opencontainers.image.description":   m.Description,
		"org.opencontainers.image.documentation": m.Documentation,
		"org.opencontainers.image.source":        m.Source,
		"org.opencontainers.image.url":           m.URL,
		"org.opencontainers.image.licenses":      m.License,
		"org.opencontainers.image.vendor":        m.Vendor,
		"org.opencontainers.image.authors":       m.Authors,
		"org.opencontainers.image.created":       m.Created,
	} {
		if len(v) > 0 {
			out[k] = v
		}
	}
	for k, v := range m.Annotations {
		if _, taken := out[k]; taken {
			return nil, fmt.Errorf("annotation %s is written by the sdk from Meta: set the field, not the annotation", k)
		}
		out[k] = v
	}
	return out, nil
}

// dirOf is the directory of the pack: Meta.Dir, else where its Meta method is defined.
func dirOf(p Pack) (string, error) {
	if d := p.Meta().Dir; len(d) > 0 {
		return d, nil
	}
	m, ok := reflect.TypeOf(p).MethodByName("Meta")
	if !ok {
		return "", fmt.Errorf("%T has no Meta", p)
	}
	fn := runtime.FuncForPC(m.Func.Pointer())
	if fn == nil {
		return "", fmt.Errorf("%T: where is Meta defined? set Meta.Dir", p)
	}
	file, _ := fn.FileLine(fn.Entry())
	if len(file) == 0 {
		return "", fmt.Errorf("%T: where is Meta defined? set Meta.Dir", p)
	}
	return filepath.Dir(file), nil
}

// content reads the pack's content/ directory: every regular file under it, by its name relative to it. A
// pack with none has no files. A symlink is not content — it is ignored, not followed — so nothing outside the
// directory is ever read. The files the fields name must be there.
func content(p Pack) (map[string][]byte, error) {
	dir, err := dirOf(p)
	if err != nil {
		return nil, err
	}
	root := filepath.Join(dir, ContentDir)
	out := map[string][]byte{}
	if _, err := os.Lstat(root); os.IsNotExist(err) {
		return out, checkNamed(p, out)
	}
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type()&fs.ModeSymlink != 0 {
			return nil // not content, and not followed
		}
		if !d.Type().IsRegular() {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		out[filepath.ToSlash(rel)] = b
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, checkNamed(p, out)
}

// checkNamed: every file the Content of the pack names is in content/.
func checkNamed(p Pack, have map[string][]byte) error {
	var missing []string
	root := p.PackRoot()
	lists := [][]packschema.File{root.Content}
	for _, os := range root.Packages {
		lists = append(lists, os.Repo, os.Content)
	}
	for _, list := range lists {
		for _, f := range list {
			for _, name := range []string{f.Source, f.Template} {
				if _, ok := have[name]; len(name) > 0 && !ok {
					missing = append(missing, fmt.Sprintf("%s: %s", f.Path, name))
				}
			}
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return fmt.Errorf("not in %s/: %s", ContentDir, strings.Join(missing, ", "))
	}
	return nil
}
