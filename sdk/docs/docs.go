// Package docs is the documentation of a pack's settings, in languages: docs/<lang>.yaml next to the pack's source,
// keyed by the JSON Pointer of a field. The sdk writes it into schema.json — the base language as the schema's own
// title, description and examples, the others as x-pack-i18n beside them — so that one schema serves the comments
// of a values.yaml and a form built on it.
//
//	# docs/en.yaml — the base language (base: true names another)
//	/:
//	  title: OpenBao
//	/openbao/log_level:
//	  title: Log level
//	  description: |
//	    How much the server says.
//	  examples:
//	    - debug
//	  group: logging
//	groups:
//	  logging: Logging
//	rules:
//	  storage-from-postgres: storage is filled from the embedded postgres and must not be set
//
//	# docs/ru.yaml — a translation: titles, descriptions, groups and rules only
//	/openbao/log_level:
//	  title: Уровень журнала
//
// The Python and Java SDKs read the same files the same way (the YAML is a subset of its own, see Parse).
package docs

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

// Dir is the directory of the documentation, next to content/.
const Dir = "docs"

// DefaultBase is the base language when no file says base: true.
const DefaultBase = "en"

// The keywords docs adds to a schema.
const (
	KeyI18n   = "x-pack-i18n"   // on a node and on a rule: {<lang>: {title, description} | {message}}
	KeyGroup  = "x-pack-group"  // on a node: the id of its group
	KeyGroups = "x-pack-groups" // on the root: [{id, title, x-pack-i18n}] in the order the fields first use them
	KeyLangs  = "x-pack-langs"  // on the root: the languages, the base first
)

var langName = regexp.MustCompile(`^[a-z]{2,3}(-[A-Za-z0-9]{2,8})*$`)

// Entry is what a file says of one field.
type Entry struct {
	Title, Description string
	Examples           []string // base only
	Group              string   // base only
}

// File is one language.
type File struct {
	Lang   string
	Base   bool
	Fields map[string]Entry
	Order  []string // the pointers, in the order of the file
	Groups map[string]string
	Rules  map[string]string
}

// Set is the documentation of a pack: the base language and the others, by name.
type Set struct {
	Base   *File
	Others []*File // sorted by language
}

// Langs are the languages, the base first.
func (s *Set) Langs() []string {
	out := []string{s.Base.Lang}
	for _, f := range s.Others {
		out = append(out, f.Lang)
	}
	return out
}

// Load reads dir/docs/*.yaml. No docs/ — nil and no error: the pack is not documented.
func Load(dir string) (*Set, error) {
	root := filepath.Join(dir, Dir)
	ents, err := os.ReadDir(root)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var files []*File
	for _, e := range ents {
		name := e.Name()
		if e.IsDir() || strings.HasPrefix(name, ".") {
			continue
		}
		lang, ok := strings.CutSuffix(name, ".yaml")
		if !ok || !langName.MatchString(lang) {
			return nil, fmt.Errorf("%s/%s: a file of %s/ is <lang>.yaml (en.yaml, ru.yaml, pt-BR.yaml)", Dir, name, Dir)
		}
		src, err := os.ReadFile(filepath.Join(root, name))
		if err != nil {
			return nil, err
		}
		f, err := ParseFile(lang, src)
		if err != nil {
			return nil, fmt.Errorf("%s/%s: %w", Dir, name, err)
		}
		files = append(files, f)
	}
	if len(files) == 0 {
		return nil, nil
	}
	return assemble(files)
}

func assemble(files []*File) (*Set, error) {
	s := &Set{}
	for _, f := range files {
		if f.Base {
			if s.Base != nil {
				return nil, fmt.Errorf("%s/: %s.yaml and %s.yaml both say base: true", Dir, s.Base.Lang, f.Lang)
			}
			s.Base = f
		}
	}
	if s.Base == nil {
		for _, f := range files {
			if f.Lang == DefaultBase {
				f.Base, s.Base = true, f
			}
		}
	}
	if s.Base == nil {
		return nil, fmt.Errorf("%s/: no base language: write %s.yaml, or say base: true in one file", Dir, DefaultBase)
	}
	for _, f := range files {
		if f == s.Base {
			continue
		}
		for ptr, e := range f.Fields {
			if len(e.Examples) > 0 || len(e.Group) > 0 {
				return nil, fmt.Errorf("%s/%s.yaml: %s: examples and group are the base language's (%s.yaml)", Dir, f.Lang, ptr, s.Base.Lang)
			}
		}
		for id := range f.Groups {
			if _, ok := s.Base.Groups[id]; !ok {
				return nil, fmt.Errorf("%s/%s.yaml: groups: %q is not a group of %s.yaml", Dir, f.Lang, id, s.Base.Lang)
			}
		}
		s.Others = append(s.Others, f)
	}
	slices.SortFunc(s.Others, func(a, b *File) int { return strings.Compare(a.Lang, b.Lang) })
	return s, nil
}

// ParseFile reads one language's file.
func ParseFile(lang string, src []byte) (*File, error) {
	m, err := Parse(src)
	if err != nil {
		return nil, err
	}
	f := &File{Lang: lang, Fields: map[string]Entry{}, Groups: map[string]string{}, Rules: map[string]string{}}
	for _, k := range m.Keys {
		v := m.Values[k]
		switch {
		case k == "base":
			if v != "true" {
				return nil, fmt.Errorf("base: true or nothing")
			}
			f.Base = true
		case k == "groups" || k == "rules":
			texts, err := strings2(k, v)
			if err != nil {
				return nil, err
			}
			if k == "groups" {
				f.Groups = texts
			} else {
				f.Rules = texts
			}
		case strings.HasPrefix(k, "/"):
			e, err := entry(k, v)
			if err != nil {
				return nil, err
			}
			f.Fields[k] = e
			f.Order = append(f.Order, k)
		default:
			return nil, fmt.Errorf("%q: a key is a JSON Pointer of a field (/…, / the pack), base, groups or rules", k)
		}
	}
	return f, nil
}

func strings2(key string, v any) (map[string]string, error) {
	m, ok := v.(*Map)
	if !ok {
		return nil, fmt.Errorf("%s: a mapping of an id to its text", key)
	}
	out := map[string]string{}
	for _, id := range m.Keys {
		s, ok := m.Values[id].(string)
		if !ok {
			return nil, fmt.Errorf("%s: %s: a text", key, id)
		}
		out[id] = s
	}
	return out, nil
}

func entry(ptr string, v any) (Entry, error) {
	m, ok := v.(*Map)
	if !ok {
		return Entry{}, fmt.Errorf("%s: a mapping of title, description, examples, group", ptr)
	}
	var e Entry
	for _, k := range m.Keys {
		val := m.Values[k]
		s, isText := val.(string)
		switch k {
		case "title", "description", "group":
			if !isText {
				return Entry{}, fmt.Errorf("%s: %s: a text", ptr, k)
			}
			switch k {
			case "title":
				if strings.Contains(s, "\n") {
					return Entry{}, fmt.Errorf("%s: title: one line", ptr)
				}
				e.Title = s
			case "description":
				e.Description = s
			case "group":
				e.Group = s
			}
		case "examples":
			l, ok := val.([]string)
			if !ok {
				return Entry{}, fmt.Errorf("%s: examples: a list", ptr)
			}
			e.Examples = l
		default:
			return Entry{}, fmt.Errorf("%s: %q: a field has title, description, examples and group", ptr, k)
		}
	}
	return e, nil
}
