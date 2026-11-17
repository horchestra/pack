package remote

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"

	"github.com/horchestra/pack/pkg/render"
	"github.com/horchestra/pack/pkg/target"
	"github.com/horchestra/pack/pkg/target/remote/remotepb"
)

const (
	inlineBlobMax = 256 * 1024
	blobChunkSize = 1 << 20
)

func blobRef(data []byte) *render.BlobRef {
	digest := sha256.Sum256(data)
	return &render.BlobRef{Digest: hex.EncodeToString(digest[:]), Size: int64(len(data))}
}

// externalizeUnit copies a unit and moves every file larger than inlineBlobMax into the returned blob set.
func externalizeUnit(unit *render.Unit) (*render.Unit, map[string][]byte) {
	if unit == nil {
		return nil, map[string][]byte{}
	}
	out := *unit
	blobs := map[string][]byte{}
	externalize := func(files []render.File) []render.File {
		files = slices.Clone(files)
		for i := range files {
			file := &files[i]
			if len(file.Content) > inlineBlobMax {
				ref := blobRef(file.Content)
				blobs[ref.Digest] = file.Content
				file.Content, file.Blob = nil, ref
			}
			if len(file.Instances) > 0 {
				file.Instances = slices.Clone(file.Instances)
				file.InstanceBlobs = make([]*render.BlobRef, len(file.Instances))
				for j, content := range file.Instances {
					if len(content) > inlineBlobMax {
						ref := blobRef(content)
						blobs[ref.Digest] = content
						file.Instances[j], file.InstanceBlobs[j] = nil, ref
					}
				}
			}
		}
		return files
	}
	out.Files = externalize(unit.Files)
	if unit.Packages != nil {
		out.Packages = maps.Clone(unit.Packages)
		for name, packages := range unit.Packages {
			packages.Repo = externalize(packages.Repo)
			packages.Files = externalize(packages.Files)
			out.Packages[name] = packages
		}
	}
	return &out, blobs
}

type blobStore struct{ dir string }

func newBlobStore(dir string) *blobStore { return &blobStore{dir: dir} }

func blobPath(dir, digest string) (string, error) {
	raw, err := hex.DecodeString(digest)
	if err != nil || len(raw) != sha256.Size {
		return "", fmt.Errorf("blob digest %q is not sha256", digest)
	}
	return filepath.Join(dir, digest), nil
}

func (s *blobStore) put(digest string, content io.Reader) error {
	path, err := blobPath(s.dir, digest)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(s.dir, ".blob-")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	hash := sha256.New()
	if _, err := io.Copy(io.MultiWriter(tmp, hash), content); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if hex.EncodeToString(hash.Sum(nil)) != digest {
		return fmt.Errorf("blob %q: the content's sha256 differs", digest)
	}
	return os.Rename(tmp.Name(), path)
}

func (s *blobStore) read(ref *render.BlobRef) ([]byte, error) {
	path, err := blobPath(s.dir, ref.Digest)
	if err != nil {
		return nil, err
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if int64(len(content)) != ref.Size {
		return nil, fmt.Errorf("blob %q: size %d, expected %d", ref.Digest, len(content), ref.Size)
	}
	digest := sha256.Sum256(content)
	if hex.EncodeToString(digest[:]) != ref.Digest {
		return nil, fmt.Errorf("blob %q: the content's sha256 differs", ref.Digest)
	}
	return content, nil
}

func (s *blobStore) hydrateUnit(unit *render.Unit) error {
	if unit == nil {
		return nil
	}
	hydrate := func(files []render.File) error {
		for i := range files {
			file := &files[i]
			if file.Blob != nil {
				content, err := s.read(file.Blob)
				if err != nil {
					return err
				}
				file.Content, file.Blob = content, nil
			}
			for j, ref := range file.InstanceBlobs {
				if ref == nil {
					continue
				}
				content, err := s.read(ref)
				if err != nil {
					return err
				}
				file.Instances[j] = content
			}
			file.InstanceBlobs = nil
		}
		return nil
	}
	if err := hydrate(unit.Files); err != nil {
		return err
	}
	for _, packages := range unit.Packages {
		if err := hydrate(packages.Repo); err != nil {
			return err
		}
		if err := hydrate(packages.Files); err != nil {
			return err
		}
	}
	return nil
}

func (p *Plugin) uploadBlob(ctx context.Context, digest string, content []byte) error {
	stream, err := p.client.PutBlob(ctx)
	if err != nil {
		return err
	}
	for len(content) > 0 {
		n := min(len(content), blobChunkSize)
		if err := stream.Send(&remotepb.BlobChunk{Digest: digest, Data: content[:n]}); err != nil {
			return err
		}
		content = content[n:]
	}
	_, err = stream.CloseAndRecv()
	return err
}

type blobStreamReader struct {
	stream remotepb.Target_PutBlobServer
	rest   []byte
	digest string
}

func (r *blobStreamReader) Read(p []byte) (int, error) {
	for len(r.rest) == 0 {
		chunk, err := r.stream.Recv()
		if err != nil {
			return 0, err
		}
		if chunk.Digest != r.digest {
			return 0, fmt.Errorf("the blob's digest changed during upload")
		}
		r.rest = chunk.Data
	}
	n := copy(p, r.rest)
	r.rest = r.rest[n:]
	return n, nil
}

func (p *Plugin) externalizeBlobs(ctx context.Context, step *target.Step) error {
	if !slices.Contains(p.caps, CapBlob) {
		return nil
	}
	var uploads = map[string][]byte{}
	for _, unit := range []*render.Unit{step.Unit, step.Previous} {
		external, blobs := externalizeUnit(unit)
		if unit == step.Unit {
			step.Unit = external
		} else {
			step.Previous = external
		}
		maps.Copy(uploads, blobs)
	}
	for _, digest := range slices.Sorted(maps.Keys(uploads)) {
		if err := p.uploadBlob(ctx, digest, uploads[digest]); err != nil {
			return fmt.Errorf("blob %s: %w", digest, err)
		}
	}
	return nil
}
