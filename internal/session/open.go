package session

import (
	"context"
	"fmt"
	"strings"

	"github.com/horchestra/pack/internal/oci"
	"github.com/horchestra/pack/internal/release"
	"github.com/horchestra/pack/internal/workdir"
	"github.com/horchestra/pack/pkg/bundle"
	"github.com/horchestra/pack/pkg/environment"
)

// OpenPack loads a pack that is not the release's: a directory, or a pack in a registry, which is brought into the
// pack directory first (at the digest the registry has for the tag today, no lock is asked).
func OpenPack(ctx context.Context, d *workdir.Dir, ref string) (*bundle.Bundle, error) {
	if !oci.IsRef(ref) {
		return bundle.Load(ref)
	}
	env, err := environment.Load(ctx, d.Release.Config(), nil)
	if err != nil {
		return nil, err
	}
	o := oci.FromEnv(env.Registry.PlainHTTP)
	pin, err := oci.Pin(ctx, ref, o)
	if err != nil {
		return nil, err
	}
	p, err := oci.Pull(ctx, pin, d.PacksPath(), o)
	if err != nil {
		return nil, err
	}
	return bundle.Load(p.Dir)
}

// Pack opens the pack of the release, or one named by ref: a directory, or a pack in a registry — which the lock
// of the release (pack init) or the digest in the reference says exactly.
func (s *Session) Pack(ref string) (*bundle.Bundle, error) {
	dir := ref
	if oci.IsRef(ref) {
		p, err := s.pull(s.ctx, ref)
		if err != nil {
			return nil, err
		}
		dir = p.Dir
	}
	return bundle.Load(dir)
}

// Pinned is the reference that names this very pack for good: a directory as it is, a pack in a registry by its
// digest.
func (s *Session) Pinned(ref string) (string, error) {
	if !oci.IsRef(ref) {
		return ref, nil
	}
	return s.pin(ref)
}

// pin is the digest-reference of a pack in a registry: the one it has, or the one the lock says.
func (s *Session) pin(ref string) (string, error) {
	if strings.Contains(ref, "@") { // already a digest: oci://host/repo@sha256:…
		return ref, nil
	}
	lock, err := release.ReadLock(s.Dir.Release.Dir)
	if err != nil {
		return "", err
	}
	if lock == nil || lock.Pack != ref {
		return "", fmt.Errorf("%s: %w — run `pack init`", ref, oci.ErrNoLock)
	}
	return oci.WithDigest(ref, lock.Digest)
}

func (s *Session) pull(ctx context.Context, ref string) (oci.Pulled, error) {
	pin, err := s.pin(ref)
	if err != nil {
		return oci.Pulled{}, err
	}
	return oci.Pull(ctx, pin, s.Dir.PacksPath(), oci.FromEnv(s.Env.Registry.PlainHTTP))
}
