// Package oci brings a pack from a registry into a directory the bundle reads. oci-packer does the work: it
// talks to the registry (pkg/registry/remote) and writes the tree an artifact was packed from (pkg/extract) — the
// pack's directory, and one directory beside it for each pack it holds.
package oci

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/opencontainers/go-digest"

	"github.com/arenadata/oci-packer/pkg/extract"
	"github.com/arenadata/oci-packer/pkg/registry/reference"
	"github.com/arenadata/oci-packer/pkg/registry/remote"
	"github.com/horchestra/pack/pkg/format"
)

// Options of a registry.
type Options struct {
	PlainHTTP      bool
	User, Password string
}

// IsRef reports whether s names a pack in a registry.
func IsRef(s string) bool { return reference.OciScheme.IsPrefix(s) && reference.IsRemote(s) }

// Pulled is a pack brought into a directory.
type Pulled struct {
	Digest string // the artifact's: what the reference was resolved to
	Ref    string // oci://<host>/<repo>@<digest>: the reference that always names this very pack
	Dir    string // the directory of the pack itself; the packs it holds are beside it
}

func client(parsed reference.Reference, o Options) (*remote.Client, error) {
	opts := []remote.Option{remote.WithCreds(o.User, o.Password)}
	if o.PlainHTTP {
		opts = append(opts, remote.WithPlainHttp())
	}
	c, err := remote.NewRegistryClient(parsed, opts...)
	if err != nil {
		return nil, err
	}
	return c.(*remote.Client), nil
}

func pinned(parsed reference.Reference, d digest.Digest) string {
	return fmt.Sprintf("%s%s/%s@%s", reference.OciScheme, parsed.Host, parsed.Path, d)
}

// WithDigest names the pack of a reference by a digest: oci://host/repo:tag becomes oci://host/repo@digest.
func WithDigest(ref, d string) (string, error) {
	parsed, err := reference.Parse(ref)
	if err != nil {
		return "", fmt.Errorf("%s: %w", ref, err)
	}
	return pinned(parsed, digest.Digest(d)), nil
}

// Pin resolves a reference to the digest the registry has for it now, and names the pack by it.
func Pin(ctx context.Context, ref string, o Options) (string, error) {
	parsed, err := reference.Parse(ref)
	if err != nil {
		return "", fmt.Errorf("%s: %w", ref, err)
	}
	if isDigest(parsed.Ref) {
		return pinned(parsed, digest.Digest(parsed.Ref)), nil
	}
	c, err := client(parsed, o)
	if err != nil {
		return "", err
	}
	desc, err := c.Resolve(ctx, reference.Reference{Ref: parsed.Ref})
	if err != nil {
		return "", fmt.Errorf("%s: %w", ref, err)
	}
	return pinned(parsed, desc.Digest), nil
}

// Pull makes sure the pack a reference names is in <packs>/<digest>/, and returns where. A pack by its digest is
// content: once there, it is not fetched again. It is written to a directory of its own and moved into place, so
// a pull that stops halfway leaves nothing a command could read.
func Pull(ctx context.Context, ref, packs string, o Options) (Pulled, error) {
	parsed, err := reference.Parse(ref)
	if err != nil {
		return Pulled{}, fmt.Errorf("%s: %w", ref, err)
	}
	c, err := client(parsed, o)
	if err != nil {
		return Pulled{}, err
	}
	var d digest.Digest
	if isDigest(parsed.Ref) {
		d = digest.Digest(parsed.Ref)
	} else {
		desc, err := c.Resolve(ctx, reference.Reference{Ref: parsed.Ref})
		if err != nil {
			return Pulled{}, fmt.Errorf("%s: %w", ref, err)
		}
		d = desc.Digest
	}
	if err := d.Validate(); err != nil {
		return Pulled{}, fmt.Errorf("%s: %w", ref, err)
	}
	out := Pulled{Digest: d.String(), Ref: pinned(parsed, d)}
	dest := filepath.Join(packs, d.Encoded())
	if root, err := os.ReadFile(filepath.Join(dest, rootFile)); err == nil {
		out.Dir = filepath.Join(dest, strings.TrimSpace(string(root)))
		return out, nil
	}

	if err := os.MkdirAll(packs, 0o700); err != nil {
		return Pulled{}, err
	}
	tmp, err := os.MkdirTemp(packs, ".pull-")
	if err != nil {
		return Pulled{}, err
	}
	defer func() { _ = os.RemoveAll(tmp) }()
	// Repo with the host: a mounted member's ref names where it pulls from — the target adds no host of its own
	dir, err := extract.Extract(ctx, c, reference.Reference{Ref: d.String()}, tmp,
		extract.Options{Repo: parsed.Host + "/" + parsed.Path, NameBy: format.AnnotationName, ImageConfig: true})
	if err != nil {
		return Pulled{}, fmt.Errorf("%s: %w", ref, err)
	}
	name := filepath.Base(dir)
	if err := os.WriteFile(filepath.Join(tmp, rootFile), []byte(name), 0o600); err != nil {
		return Pulled{}, err
	}
	if err := os.Rename(tmp, dest); err != nil {
		if _, ok := readRoot(dest); ok { // another pull got there first: same content
			out.Dir = filepath.Join(dest, name)
			return out, nil
		}
		return Pulled{}, err
	}
	out.Dir = filepath.Join(dest, name)
	return out, nil
}

// rootFile names the directory of the pack itself among the ones a pull wrote.
const rootFile = ".root"

func readRoot(dest string) (string, bool) {
	b, err := os.ReadFile(filepath.Join(dest, rootFile))
	return strings.TrimSpace(string(b)), err == nil
}

func isDigest(s string) bool {
	return strings.HasPrefix(s, "sha256:") || strings.HasPrefix(s, "sha512:")
}

// ErrNoLock is returned when a pack in a registry has no lock.
var ErrNoLock = errors.New("the pack is in a registry and has no lock")

// PullFile reads one file out of an OCI artifact that is not a pack — a program published with oci-packer — wherever the
// artifact put it: the file with that base name. A target is brought this way.
func PullFile(ctx context.Context, ref, name string, o Options) ([]byte, error) {
	parsed, err := reference.Parse(ref)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", ref, err)
	}
	c, err := client(parsed, o)
	if err != nil {
		return nil, err
	}
	tmp, err := os.MkdirTemp("", "pack-file-")
	if err != nil {
		return nil, err
	}
	defer func() { _ = os.RemoveAll(tmp) }()
	if _, err := extract.Extract(ctx, c, reference.Reference{Ref: parsed.Ref}, tmp, extract.Options{Repo: parsed.Path}); err != nil {
		return nil, fmt.Errorf("%s: %w", ref, err)
	}
	var found string
	_ = filepath.WalkDir(tmp, func(p string, d os.DirEntry, err error) error {
		if err == nil && d.Type().IsRegular() && d.Name() == name && len(found) == 0 {
			found = p
		}
		return nil
	})
	if len(found) == 0 {
		return nil, fmt.Errorf("%s: the artifact has no file %s", ref, name)
	}
	return os.ReadFile(found)
}
