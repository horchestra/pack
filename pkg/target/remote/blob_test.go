package remote

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"testing"

	"github.com/horchestra/pack/pkg/render"
)

type interruptedReader struct{ sent bool }

func (r *interruptedReader) Read(p []byte) (int, error) {
	if r.sent {
		return 0, io.ErrUnexpectedEOF
	}
	r.sent = true
	copy(p, "partial")
	return 7, nil
}

func TestBlobRoundTrip(t *testing.T) {
	content := bytes.Repeat([]byte("x"), inlineBlobMax+1)
	unit := &render.Unit{Files: []render.File{{Path: "/large", Content: content}}}
	external, blobs := externalizeUnit(unit)
	if len(blobs) != 1 || external.Files[0].Content != nil || external.Files[0].Blob == nil {
		t.Fatalf("the large file was not externalized: %+v", external.Files[0])
	}
	store := newBlobStore(t.TempDir())
	for digest, content := range blobs {
		if err := store.put(digest, bytes.NewReader(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.hydrateUnit(external); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(external.Files[0].Content, content) || external.Files[0].Blob != nil {
		t.Fatal("the hydrated file is not the original content")
	}
	if unit.Files[0].Blob != nil {
		t.Fatal("externalization mutated the caller's unit")
	}
}

func TestBlobVerification(t *testing.T) {
	store := newBlobStore(t.TempDir())
	expected := sha256.Sum256([]byte("expected"))
	digest := hex.EncodeToString(expected[:])
	if err := store.put(digest, bytes.NewReader([]byte("corrupted"))); err == nil {
		t.Fatal("corrupted content passed digest verification")
	}
	if err := store.put(digest, &interruptedReader{}); err == nil || !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("an interrupted upload was accepted: %v", err)
	}
	if err := store.put(digest, bytes.NewReader([]byte("expected"))); err != nil {
		t.Fatal(err)
	}
	path, err := blobPath(store.dir, digest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("modified"), 0o600); err != nil {
		t.Fatal(err)
	}
	unit := &render.Unit{Files: []render.File{{Blob: &render.BlobRef{Digest: digest, Size: 8}}}}
	if err := store.hydrateUnit(unit); err == nil {
		t.Fatal("same-length modification passed hydration")
	}
}
