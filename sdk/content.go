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
)

// ContentDir is the directory of a pack's files.
const ContentDir = "content"

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

// checkNamed: every file a template tag names is in content/.
func checkNamed(p Pack, have map[string][]byte) error {
	var missing []string
	var walk func(t reflect.Type)
	walk = func(t reflect.Type) {
		for t.Kind() == reflect.Pointer {
			t = t.Elem()
		}
		if t.Kind() != reflect.Struct {
			return
		}
		for f := range t.Fields() {
			if name := f.Tag.Get("template"); len(name) > 0 {
				if _, ok := have[name]; !ok {
					missing = append(missing, fmt.Sprintf("%s.%s: %s", t.Name(), f.Name, name))
				}
			}
			ft := f.Type
			for ft.Kind() == reflect.Pointer {
				ft = ft.Elem()
			}
			if _, isPack := reflect.New(ft).Interface().(Pack); isPack && ft.Kind() == reflect.Struct {
				continue // its own content
			}
			walk(f.Type)
		}
	}
	walk(reflect.TypeOf(p))
	if len(missing) > 0 {
		sort.Strings(missing)
		return fmt.Errorf("not in %s/: %s", ContentDir, strings.Join(missing, ", "))
	}
	return nil
}
