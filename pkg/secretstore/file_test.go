package secretstore

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// The file store: written and read as the KV v2 ones are, the directory 0700 and the files 0600.
func TestFileStore(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "secrets")
	st, err := Open(Config{Provider: "file", Path: dir})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, ok, err := st.Get(ctx, "pack/i1/postgres"); err != nil || ok {
		t.Fatalf("none yet: %v %v", ok, err)
	}
	if err := st.Put(ctx, "pack/i1/postgres", map[string]string{"password": "s3cret", "user": "pg"}); err != nil {
		t.Fatal(err)
	}
	fields, ok, err := st.Get(ctx, "pack/i1/postgres")
	if err != nil || !ok || fields["password"] != "s3cret" || fields["user"] != "pg" {
		t.Fatalf("read back: %v %v %v", fields, ok, err)
	}
	if fi, err := os.Stat(dir); err != nil || fi.Mode().Perm() != 0o700 {
		t.Errorf("the directory: %v %v", fi.Mode(), err)
	}
	f := filepath.Join(dir, "pack", "i1", "postgres.json")
	if fi, err := os.Stat(f); err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("the file: %v %v", fi.Mode(), err)
	}
	// replaced whole: the old fields are not merged in
	if err := st.Put(ctx, "pack/i1/postgres", map[string]string{"password": "n3w"}); err != nil {
		t.Fatal(err)
	}
	if fields, _, _ := st.Get(ctx, "pack/i1/postgres"); fields["password"] != "n3w" || len(fields) != 1 {
		t.Errorf("replaced: %v", fields)
	}
	// what was there stays whole when a write is cut short: the file is the last complete rename
	if b, _ := os.ReadFile(f); len(b) == 0 || b[0] != '{' {
		t.Errorf("the file is whole: %q", b)
	}
	if _, err := Open(Config{Provider: "file"}); err == nil {
		t.Error("store.path is needed")
	}
	if _, _, err := st.Get(ctx, "../outside"); err == nil {
		t.Error("a path that leaves the directory is refused")
	}
}
