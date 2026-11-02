package bundle

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/horchestra/pack/pkg/format"
)

// The artifact types and annotations of a pack's members are format's.
const (
	ArtifactPack      = format.ArtifactPack
	ArtifactSchema    = format.ArtifactSchema
	ArtifactTemplate  = format.ArtifactTemplate
	AnnotationName    = format.AnnotationName
	AnnotationVersion = format.AnnotationVersion
	AnnotationImage   = format.AnnotationImage
)

// ImageItem is an oci:// item of a pack file.
type ImageItem struct {
	Ref    string
	Marked bool // the item says it is the image (pack.image)
}

// Files is what one pack contributes to a bundle: the files of its directory.
type Files struct {
	Name, Version string
	Origin        string // the directory — for messages
	Schema        []byte
	Templates     map[string][]byte
	// Images are the oci:// items of the pack file, in order: the image the pack runs, and the packs it holds.
	Images      []ImageItem
	ImageConfig []byte // the image's OCI config, optional
}

// Source hands out the files of the artifact a reference names and of the
// components it refers to by name.
type Source interface {
	Root() (*Files, error)
	Member(name string) (*Files, error)
}

// ---- a directory tree: the author's working tree, or oci-packer extract's output

type dirSource struct{ dir string }

// Dir is the source rooted at a composition or component directory; members
// are the sibling directories.
func Dir(dir string) Source {
	if abs, err := filepath.Abs(dir); err == nil {
		dir = abs // "." and "dir/" have no sibling or name of their own
	}
	return dirSource{dir}
}

func (d dirSource) Root() (*Files, error) { return dirFiles(d.dir) }

func (d dirSource) Member(name string) (*Files, error) {
	return dirFiles(filepath.Join(filepath.Dir(d.dir), name))
}

func dirFiles(dir string) (*Files, error) {
	raw, err := os.ReadFile(filepath.Join(dir, "schema.json"))
	if err != nil {
		return nil, err
	}
	f := &Files{Origin: dir, Schema: raw, Templates: map[string][]byte{}}
	root := filepath.Join(dir, "templates")
	if _, serr := os.Stat(root); serr == nil {
		err = filepath.WalkDir(root, func(p string, d fs.DirEntry, werr error) error {
			if werr != nil {
				return werr
			}
			if !d.Type().IsRegular() { // a directory has none of its own; a symlink is not content, and not followed
				return nil
			}
			rel, rerr := filepath.Rel(root, p)
			if rerr != nil {
				return rerr
			}
			b, rerr := os.ReadFile(p)
			if rerr != nil {
				return rerr
			}
			f.Templates[filepath.ToSlash(rel)] = b
			return nil
		})
		if err != nil {
			return nil, fmt.Errorf("%s: templates: %w", dir, err)
		}
	} else if !errors.Is(serr, fs.ErrNotExist) {
		return nil, serr
	}
	if cfg, err := os.ReadFile(filepath.Join(dir, "image.json")); err == nil {
		f.ImageConfig = cfg
	}
	f.Name, f.Version = packFileIdentity(dir)
	if len(f.Name) == 0 {
		f.Name = filepath.Base(dir)
	}
	f.Images = imageItems(dir)
	return f, nil
}
