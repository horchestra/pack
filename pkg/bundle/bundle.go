// Package bundle reads a pack from a Source: a directory tree — the author's working tree, or what oci-packer
// extracted from a registry. A pack's directory holds schema.json, templates/, pack.yaml, optionally image.json,
// and the packs it holds are the sibling directories named by pack://<name>#.
//
// A pack may hold other packs in its fields; their settings are part of its schema. The render works on a flat view
// of that: every instance of a pack — the root, and each pack held in a field — is a KEY with the settings of its
// own pack, and the bundle keeps the way between the two shapes (Instance): the operator writes the nested one.
package bundle

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	packer "github.com/arenadata/oci-packer/types"
	"github.com/horchestra/pack/sdk/packschema"
	"github.com/horchestra/pack/sdk/schema"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"sigs.k8s.io/yaml"
)

// Pack is one pack as published.
type Pack struct {
	Name, Version string
	Dir           string
	// Schema is the pack's own: what it holds in its fields is cut out, those settings are the held pack's.
	Schema    *schema.Schema
	SchemaRaw []byte // as published, the held packs in it
	Root      *packschema.Root
	Templates map[string][]byte
	Image     *ocispec.Image // nil when image.json is absent
	ImageRef  string         // registry reference of the image, from pack.yaml or image.ref
	// Holds are the packs in its fields, in the schema's order.
	Holds []Hold
}

// Hold is a pack held in a field of another.
type Hold struct {
	Field    string // the json name of the field
	Pack     string // the held pack's name
	Required bool   // the field has no omitempty: the pack is always installed
	Default  any    // what the author set in the field; nil — nothing
}

// Instance is a pack at a place of a bundle: the root, or a pack held in a field of another instance.
type Instance struct {
	Key    string // the name the instance goes by: the root's pack name, a held pack's field (parent.field below the first level)
	Pack   string // the pack's name
	Path   string // the operator's JSON Pointer of the instance's settings: "" for the root, /field for a held pack
	Parent string // key of the instance that holds it; "" for the root
	Field  string // the field of the parent that holds it
	Hold   Hold
}

type Bundle struct {
	Name, Version string
	Dir           string
	Root          *Pack
	Packs         map[string]*Pack // by name: the root and every pack held, transitively
	// The flat view.
	Composition *schema.Schema       // {key: pack://<pack>#} for every instance
	Keys        []string             // the instances, held packs before the pack that holds them
	KeyPack     map[string]string    // key → pack name
	Instances   map[string]*Instance // by key
}

// Load reads dir as a pack.
func Load(dir string) (*Bundle, error) {
	b, err := LoadFrom(Dir(dir))
	if err != nil {
		return nil, err
	}
	b.Dir = dir
	return b, nil
}

// LoadFrom builds a bundle from a source.
func LoadFrom(src Source) (*Bundle, error) {
	root, err := src.Root()
	if err != nil {
		return nil, err
	}
	b := &Bundle{Name: root.Name, Version: root.Version, Packs: map[string]*Pack{}, KeyPack: map[string]string{}, Instances: map[string]*Instance{}}
	if b.Root, err = b.load(src, root); err != nil {
		return nil, err
	}
	if err := b.instances(b.Root, &Instance{Key: b.Root.Name, Pack: b.Root.Name}, nil); err != nil {
		return nil, err
	}
	if err := b.compose(); err != nil {
		return nil, err
	}
	return b, nil
}

// load reads a pack once, and the packs it holds.
func (b *Bundle) load(src Source, f *Files) (*Pack, error) {
	if p, ok := b.Packs[f.Name]; ok {
		return p, nil
	}
	full, err := schema.Parse(f.Schema)
	if err != nil {
		return nil, fmt.Errorf("pack %q: %w", f.Name, err)
	}
	root, err := full.Root.RootAnnotation()
	if err != nil {
		return nil, fmt.Errorf("pack %q: %w", f.Name, err)
	}
	p := &Pack{Name: f.Name, Version: f.Version, Dir: f.Origin, SchemaRaw: f.Schema, Root: root, Templates: f.Templates}
	if p.Templates == nil {
		p.Templates = map[string][]byte{}
	}
	if f.ImageConfig != nil {
		var img ocispec.Image
		if err := json.Unmarshal(f.ImageConfig, &img); err != nil {
			return nil, fmt.Errorf("pack %q: image config: %w", f.Name, err)
		}
		p.Image = &img
	}
	b.Packs[p.Name] = p
	defer func() { p.ImageRef = imageOf(f.Images, len(p.Holds)) }() // known once the packs it holds are

	drop := map[string]bool{}
	for _, field := range full.Root.Order {
		n := full.Root.Properties[field]
		if !n.IsForeign() || !strings.HasPrefix(n.Ref, packschema.PackScheme) {
			continue
		}
		url, _ := schema.SplitRef(n.Ref)
		h := Hold{Field: field, Pack: strings.TrimPrefix(url, packschema.PackScheme), Required: slices.Contains(full.Root.Required, field)}
		if n.HasDefault {
			h.Default = n.Default
		}
		mf, err := src.Member(h.Pack)
		if err != nil {
			return nil, fmt.Errorf("pack %q holds %q: %w", p.Name, h.Pack, err)
		}
		if mf.Name != h.Pack {
			mf.Name = h.Pack // a directory is named after the pack it holds
		}
		if _, err := b.load(src, mf); err != nil {
			return nil, err
		}
		p.Holds = append(p.Holds, h)
		drop[field] = true
	}
	own := f.Schema
	if len(drop) > 0 {
		if own, err = dropProperties(f.Schema, drop); err != nil {
			return nil, fmt.Errorf("pack %q: %w", p.Name, err)
		}
	}
	if p.Schema, err = schema.Parse(own); err != nil {
		return nil, fmt.Errorf("pack %q: %w", p.Name, err)
	}
	return p, nil
}

// instances lists the instances under in, held packs first; a pack that holds itself is refused.
func (b *Bundle) instances(p *Pack, in *Instance, path []string) error {
	if slices.Contains(path, p.Name) {
		return fmt.Errorf("pack %q holds itself: %s", p.Name, strings.Join(append(path, p.Name), " → "))
	}
	path = append(slices.Clone(path), p.Name)
	for _, h := range p.Holds {
		key := h.Field
		if len(in.Parent) > 0 || len(in.Path) > 0 {
			key = in.Key + "." + h.Field
		}
		held := &Instance{Key: key, Pack: h.Pack, Path: in.Path + "/" + h.Field, Parent: in.Key, Field: h.Field, Hold: h}
		if err := b.instances(b.Packs[h.Pack], held, path); err != nil {
			return err
		}
	}
	if _, dup := b.Instances[in.Key]; dup {
		return fmt.Errorf("two instances are keyed %q", in.Key)
	}
	b.Instances[in.Key] = in
	b.Keys = append(b.Keys, in.Key)
	b.KeyPack[in.Key] = in.Pack
	return nil
}

// compose builds the flat view: a schema whose keys are the instances, each the own schema of its pack.
func (b *Bundle) compose() error {
	var props []string
	for _, key := range b.Keys {
		in := b.Instances[key]
		prop := fmt.Sprintf(`%q:{"$ref":"pack://%s#"`, key, in.Pack)
		if in.Hold.Default != nil {
			d, err := json.Marshal(in.Hold.Default)
			if err != nil {
				return err
			}
			prop += `,"default":` + string(d)
		}
		props = append(props, prop+"}")
	}
	doc := fmt.Sprintf(`{"$schema":"https://json-schema.org/draft/2020-12/schema","type":"object","additionalProperties":false,"properties":{%s}}`, strings.Join(props, ","))
	comp, err := schema.Parse([]byte(doc))
	if err != nil {
		return err
	}
	for name, p := range b.Packs {
		p.Schema.Foreign = comp.Foreign // one table for the whole bundle
		comp.Attach(packschema.PackScheme+name, p.Schema)
	}
	b.Composition = comp
	if len(b.Keys) == 0 {
		return errors.New("a pack with no instances")
	}
	return nil
}

// ---- the two shapes of the settings

// Split takes the operator's document apart into one per instance, by key. A pack held in a field and named in
// the document is there (even as {}); the rest of the document is the pack that holds it.
func (b *Bundle) Split(op map[string]any) map[string]map[string]any {
	out := map[string]map[string]any{}
	for _, key := range b.Keys {
		in := b.Instances[key]
		sub, ok := dig(op, in.Path)
		if !ok {
			continue
		}
		own := map[string]any{}
		for k, v := range sub {
			if b.holds(in, k) {
				continue
			}
			own[k] = v
		}
		out[key] = own
	}
	return out
}

// holds reports whether field of the instance's pack is a pack of its own.
func (b *Bundle) holds(in *Instance, field string) bool {
	for _, h := range b.Packs[in.Pack].Holds {
		if h.Field == field {
			return true
		}
	}
	return false
}

// Internal turns a pointer of the operator's document into the flat view's: /key/….
func (b *Bundle) Internal(ptr string) string {
	best := b.Instances[b.Root.Name]
	for _, in := range b.Instances {
		if (in.Path == ptr || strings.HasPrefix(ptr, in.Path+"/")) && len(in.Path) > len(best.Path) {
			best = in
		}
	}
	return "/" + best.Key + strings.TrimPrefix(ptr, best.Path)
}

// External turns a pointer of the flat view (/key/…) into the operator's.
func (b *Bundle) External(ptr string) string {
	segs := schema.Split(ptr)
	if len(segs) == 0 {
		return ""
	}
	in, ok := b.Instances[segs[0]]
	if !ok {
		return ptr
	}
	return in.Path + strings.TrimPrefix(ptr, "/"+segs[0])
}

// dig is the object at a JSON Pointer of a document.
func dig(doc map[string]any, ptr string) (map[string]any, bool) {
	cur := doc
	for _, seg := range schema.Split(ptr) {
		next, ok := cur[seg].(map[string]any)
		if !ok {
			return nil, false
		}
		cur = next
	}
	return cur, true
}

// dropProperties removes properties of the root of a schema, and their mention in required, keeping the order of
// the document: it is the order the author wrote the settings in.
func dropProperties(raw []byte, drop map[string]bool) ([]byte, error) {
	top, err := pairs(raw)
	if err != nil {
		return nil, err
	}
	for i, kv := range top {
		switch kv.key {
		case "properties":
			props, err := pairs(kv.val)
			if err != nil {
				return nil, err
			}
			props = slices.DeleteFunc(props, func(p pair) bool { return drop[p.key] })
			top[i].val = object(props)
		case "required":
			var req []string
			if err := json.Unmarshal(kv.val, &req); err != nil {
				return nil, err
			}
			req = slices.DeleteFunc(req, func(s string) bool { return drop[s] })
			if top[i].val, err = json.Marshal(req); err != nil {
				return nil, err
			}
		}
	}
	return object(top), nil
}

type pair struct {
	key string
	val json.RawMessage
}

// pairs reads a JSON object as its members, in the order of the document.
func pairs(raw []byte) ([]pair, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	if t, err := dec.Token(); err != nil || t != json.Delim('{') {
		return nil, fmt.Errorf("not an object")
	}
	var out []pair
	for dec.More() {
		k, err := dec.Token()
		if err != nil {
			return nil, err
		}
		var v json.RawMessage
		if err := dec.Decode(&v); err != nil {
			return nil, err
		}
		out = append(out, pair{key: k.(string), val: v})
	}
	return out, nil
}

func object(ps []pair) []byte {
	var buf bytes.Buffer
	buf.WriteByte('{')
	for i, p := range ps {
		if i > 0 {
			buf.WriteByte(',')
		}
		k, _ := json.Marshal(p.key)
		buf.Write(k)
		buf.WriteByte(':')
		buf.Write(p.val)
	}
	buf.WriteByte('}')
	return buf.Bytes()
}

// readPackFile reads the directory's pack file — oci-packer's own type.
func readPackFile(dir string) *packer.Pack {
	for _, name := range []string{"pack.yaml", filepath.Base(dir) + ".pack.yaml"} {
		raw, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			continue
		}
		var pf packer.Pack
		if yaml.Unmarshal(raw, &pf) == nil {
			return &pf
		}
	}
	return nil
}

func packFileIdentity(dir string) (name, version string) {
	if pf := readPackFile(dir); pf != nil {
		return pf.Annotations[AnnotationName], pf.Annotations[AnnotationVersion]
	}
	return "", ""
}

// imageItems are the oci:// items of the directory's pack file, in order — a repository of the registry the pack
// is pushed to, no host — or the content of image.ref.
func imageItems(dir string) []ImageItem {
	var out []ImageItem
	if pf := readPackFile(dir); pf != nil {
		for _, it := range pf.Items {
			if ref, ok := strings.CutPrefix(it.From, ociScheme); ok {
				out = append(out, ImageItem{Ref: ref, Marked: it.Annotations[AnnotationImage] == "true"})
			}
		}
	}
	if len(out) == 0 {
		if raw, err := os.ReadFile(filepath.Join(dir, "image.ref")); err == nil {
			out = append(out, ImageItem{Ref: strings.TrimSpace(string(raw)), Marked: true})
		}
	}
	return out
}

// imageOf is the image a pack runs among the oci:// items of its file: the one that says it is; failing that, the
// first, if the pack holds no pack — then every oci:// item is the image.
func imageOf(items []ImageItem, holds int) string {
	for _, it := range items {
		if it.Marked {
			return it.Ref
		}
	}
	if holds == 0 && len(items) > 0 {
		return items[0].Ref
	}
	return ""
}

const ociScheme = "oci://"
