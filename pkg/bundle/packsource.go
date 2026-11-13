package bundle

import (
	"fmt"
	"strings"

	"github.com/horchestra/pack/sdk"
)

// FromPack reads a pack held as Go — the author's own type, no directory written and read back: the schema is the
// one the type reflects, the templates its content/ files, the images its pack file's. The packs it holds are
// members of the same source.
func FromPack(root sdk.Pack) (*Bundle, error) {
	packs, err := sdk.Packs(root)
	if err != nil {
		return nil, err
	}
	src := &packSource{members: map[string]sdk.Pack{}}
	for _, p := range packs {
		src.members[p.Meta().Name] = p
	}
	src.root = src.members[root.Meta().Name]
	if src.root == nil {
		return nil, fmt.Errorf("%T: Meta needs a Name", root)
	}
	b, err := loadFrom(src, "")
	if err != nil {
		return nil, err
	}
	b.Dir = "sdk://" + b.Name
	return b, nil
}

// packSource is a Source over the packs the author holds as Go types.
type packSource struct {
	root    sdk.Pack
	members map[string]sdk.Pack
}

func (s *packSource) Root() (*Files, error) { return packFiles(s.root) }

func (s *packSource) Member(name string) (*Files, error) {
	p, ok := s.members[name]
	if !ok {
		return nil, fmt.Errorf("no pack %q", name)
	}
	return packFiles(p)
}

// packFiles is one pack's contribution, straight from its Go type.
func packFiles(p sdk.Pack) (*Files, error) {
	m := p.Meta()
	raw, err := sdk.Schema(p)
	if err != nil {
		return nil, err
	}
	ts, err := sdk.Templates(p)
	if err != nil {
		return nil, err
	}
	pf, err := sdk.PackFile(p)
	if err != nil {
		return nil, err
	}
	// no Origin: the pack has no directory, so its files are delivered by content, not a file:// URL
	f := &Files{Name: m.Name, Version: m.Version, Schema: raw, Templates: ts}
	for _, it := range pf.Items {
		if ref, ok := strings.CutPrefix(it.From, "oci://"); ok {
			f.Images = append(f.Images, ImageItem{Ref: ref, Marked: it.Annotations[AnnotationImage] == "true"})
		}
	}
	return f, nil
}
