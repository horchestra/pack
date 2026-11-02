package targets

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/horchestra/pack/internal/oci"
)

var program = []byte("#!/bin/sh\necho target\n")

func tarGz(t *testing.T, entries map[string][]byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, data := range entries {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(data)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		_, _ = tw.Write(data)
	}
	_ = tw.Close()
	_ = gz.Close()
	return buf.Bytes()
}

func zipOf(t *testing.T, entries map[string][]byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, data := range entries {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = w.Write(data)
	}
	_ = zw.Close()
	return buf.Bytes()
}

func TestExpand(t *testing.T) {
	got := Expand("https://x/pack-target-docker_{os}_{arch}")
	if got != "https://x/pack-target-docker_"+runtime.GOOS+"_"+runtime.GOARCH {
		t.Errorf("Expand = %s", got)
	}
}

func TestFetchOverHTTP(t *testing.T) {
	files := map[string][]byte{
		"/raw/pack-target-docker_" + runtime.GOOS + "_" + runtime.GOARCH: program,
		"/a.tar.gz":  tarGz(t, map[string][]byte{"README": []byte("hi"), "bin/pack-target-docker": program}),
		"/a.zip":     zipOf(t, map[string][]byte{"dist/pack-target-docker": program}),
		"/empty.tgz": tarGz(t, map[string][]byte{"README": []byte("hi")}),
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, ok := files[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(b)
	}))
	defer srv.Close()
	want := sha256.Sum256(program)

	for name, src := range map[string]string{
		"the program itself": srv.URL + "/raw/pack-target-docker_{os}_{arch}",
		"a tar.gz":           srv.URL + "/a.tar.gz",
		"a zip":              srv.URL + "/a.zip",
	} {
		t.Run(name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "bin")
			sum, err := Fetch(context.Background(), "docker", src, dir, oci.Options{})
			if err != nil {
				t.Fatal(err)
			}
			if sum != hex.EncodeToString(want[:]) {
				t.Errorf("sum = %s", sum)
			}
			file := filepath.Join(dir, "pack-target-docker")
			got, err := os.ReadFile(file)
			if err != nil || !bytes.Equal(got, program) {
				t.Fatalf("file: %q %v", got, err)
			}
			if fi, _ := os.Stat(file); fi.Mode().Perm() != 0o755 {
				t.Errorf("mode %v", fi.Mode())
			}
			if s, err := Sum(file); err != nil || s != sum {
				t.Errorf("Sum = %s, %v", s, err)
			}
			if ents, _ := os.ReadDir(dir); len(ents) != 1 {
				t.Errorf("nothing but the program in %s: %v", dir, ents)
			}
		})
	}

	dir := t.TempDir()
	for name, src := range map[string]string{
		"not found":                 srv.URL + "/missing",
		"an archive without it":     srv.URL + "/empty.tgz",
		"a scheme it does not know": "ftp://example.org/x",
	} {
		if _, err := Fetch(context.Background(), "docker", src, dir, oci.Options{}); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if ents, _ := os.ReadDir(dir); len(ents) != 0 {
		t.Errorf("a failed fetch leaves nothing: %v", ents)
	}
}

func TestFetchErrorNamesTheSource(t *testing.T) {
	_, err := Fetch(context.Background(), "docker", "ftp://example.org/x", t.TempDir(), oci.Options{})
	if err == nil || !strings.Contains(err.Error(), "oci://") {
		t.Errorf("the error says what a source can be: %v", err)
	}
}
