package values

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"sigs.k8s.io/yaml"

	"github.com/horchestra/pack/pkg/render"
)

// Load reads the operator's values from the places --values names: a file, a directory (every *.yaml, *.yml
// and *.json in it, by name), or several of them. A big set of values is split in files, a key or a topic each:
//
//	values/common.yaml     common: {realm: …}
//	values/kafka.yaml      kafka: {…}
//	values/host.yaml       what is this host's
//
// A file is a whole document (`values:`, `rollout`, `skip`), or just the keys — the `values:` of the document is
// not written in a file that has only values. The files are merged deeply; a value two files both give, and
// differently, is refused: nothing is overridden by the order of the names.
func Load(paths []string) (render.Values, error) {
	var v render.Values
	if len(paths) == 0 {
		return v, nil
	}
	var files []string
	for _, p := range paths {
		st, err := os.Stat(p)
		if err != nil {
			return v, err
		}
		if !st.IsDir() {
			files = append(files, p)
			continue
		}
		entries, err := os.ReadDir(p)
		if err != nil {
			return v, err
		}
		var in []string
		for _, e := range entries {
			if e.IsDir() || strings.HasPrefix(e.Name(), ".") {
				continue
			}
			switch strings.ToLower(filepath.Ext(e.Name())) {
			case ".yaml", ".yml", ".json":
				in = append(in, filepath.Join(p, e.Name()))
			}
		}
		slices.Sort(in)
		if len(in) == 0 {
			return v, fmt.Errorf("%s: no values files in it", p)
		}
		files = append(files, in...)
	}
	v.Values = map[string]any{}
	from := map[string]string{} // pointer of a leaf → the file that gave it
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			return v, err
		}
		var doc map[string]any
		if err := yaml.Unmarshal(raw, &doc); err != nil {
			return v, fmt.Errorf("%s: %w", f, err)
		}
		own := doc
		if isDocument(doc) {
			var one render.Values
			if err := yaml.Unmarshal(raw, &one); err != nil {
				return v, fmt.Errorf("%s: %w", f, err)
			}
			own = one.Values
			if one.Rollout != nil {
				if v.Rollout != nil {
					return v, fmt.Errorf("%s: rollout is given in two files", f)
				}
				v.Rollout = one.Rollout
			}
			v.Skip = append(v.Skip, one.Skip...)
		}
		if err := merge(v.Values, own, "", f, from); err != nil {
			return v, err
		}
	}
	return v, nil
}

// isDocument: the file's top level is only what a values document has.
func isDocument(doc map[string]any) bool {
	if len(doc) == 0 {
		return false
	}
	for k := range doc {
		if k != "values" && k != "rollout" && k != "skip" {
			return false
		}
	}
	return true
}

func merge(dst, src map[string]any, at, file string, from map[string]string) error {
	for k, sv := range src {
		ptr := at + "/" + k
		dv, has := dst[k]
		if !has {
			dst[k] = sv
			markLeaves(sv, ptr, file, from)
			continue
		}
		dm, dok := dv.(map[string]any)
		sm, sok := sv.(map[string]any)
		if dok && sok {
			if err := merge(dm, sm, ptr, file, from); err != nil {
				return err
			}
			continue
		}
		if fmt.Sprint(dv) == fmt.Sprint(sv) {
			continue
		}
		return errors.New(ptr + " is given differently by " + from[ptr] + " and " + file)
	}
	return nil
}

// markLeaves records the file a pointer came from — every pointer, objects
// too, so that a scalar-over-object conflict can name who wrote the object.
func markLeaves(v any, ptr, file string, from map[string]string) {
	from[ptr] = file
	if m, ok := v.(map[string]any); ok {
		for k, e := range m {
			markLeaves(e, ptr+"/"+k, file, from)
		}
	}
}
