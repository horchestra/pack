// Package targets brings the programs pack starts for a target (pack-target-<name>) into the release's .pack/bin,
// the way terraform brings its providers: release.yaml says where each comes from, `pack refresh` downloads it, and
// release.lock.json keeps the checksum it had that day.
//
// A source is
//
//	https://host/path/pack-target-docker_{os}_{arch}          the program itself
//	https://host/path/pack-target-docker_{os}_{arch}.tar.gz   or .tgz, .zip: an archive that holds it
//	oci://host/repo:tag-{os}-{arch}                           an OCI artifact with the file pack-target-<name>
//	http://…                                                  the same, in the clear (a mirror inside)
//
// {os} and {arch} stand for GOOS and GOARCH of the machine pack runs on.
package targets

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/horchestra/pack/internal/oci"
)

// Prefix names the program of a target: pack-target-<name>.
const Prefix = "pack-target-"

// Binary is the file name of a target's program.
func Binary(name string) string { return Prefix + name }

// maxSize bounds what is read from a source: a program, not a disk image.
const maxSize = 1 << 30

// Expand puts the machine in the source: {os} and {arch}.
func Expand(source string) string {
	return strings.NewReplacer("{os}", runtime.GOOS, "{arch}", runtime.GOARCH).Replace(source)
}

// Sum is the SHA-256 of a file, hex.
func Sum(file string) (string, error) {
	f, err := os.Open(file)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// Fetch downloads the program of target name from source into dir and returns the SHA-256 of what it wrote. The
// file appears whole or not at all.
func Fetch(ctx context.Context, name, source, dir string, o oci.Options) (string, error) {
	src := Expand(source)
	u, err := url.Parse(src)
	if err != nil {
		return "", fmt.Errorf("target %s: source %q: %w", name, source, err)
	}
	var data []byte
	switch u.Scheme {
	case "http", "https":
		data, err = fromHTTP(ctx, name, src, u)
	case "oci":
		data, err = oci.PullFile(ctx, src, Binary(name), o)
	default:
		return "", fmt.Errorf("target %s: source %q: https://…, http://… or oci://host/repo:tag", name, source)
	}
	if err != nil {
		return "", fmt.Errorf("target %s: %s: %w", name, source, err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	tmp, err := os.CreateTemp(dir, ".fetch-")
	if err != nil {
		return "", err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return "", err
	}
	if err := tmp.Close(); err != nil {
		return "", err
	}
	if err := os.Chmod(tmp.Name(), 0o755); err != nil {
		return "", err
	}
	if err := os.Rename(tmp.Name(), filepath.Join(dir, Binary(name))); err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

func fromHTTP(ctx context.Context, name, src string, u *url.URL) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, src, nil)
	if err != nil {
		return nil, err
	}
	resp, err := (&http.Client{Timeout: 10 * time.Minute}).Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s", resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxSize+1))
	if err != nil {
		return nil, err
	}
	if len(body) > maxSize {
		return nil, errors.New("more than 1 GiB: not a program")
	}
	switch p := strings.ToLower(u.Path); {
	case strings.HasSuffix(p, ".tar.gz"), strings.HasSuffix(p, ".tgz"):
		return fromTarGz(name, body)
	case strings.HasSuffix(p, ".zip"):
		return fromZip(name, body)
	}
	return body, nil
}

// wanted: the entry of an archive that is the program, wherever it is in it.
func wanted(name, entry string) bool { return path.Base(entry) == Binary(name) }

func fromTarGz(name string, body []byte) ([]byte, error) {
	gz, err := gzip.NewReader(bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil, fmt.Errorf("the archive has no %s", Binary(name))
		}
		if err != nil {
			return nil, err
		}
		if h.Typeflag == tar.TypeReg && wanted(name, h.Name) {
			return io.ReadAll(io.LimitReader(tr, maxSize))
		}
	}
}

func fromZip(name string, body []byte) ([]byte, error) {
	zr, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
	if err != nil {
		return nil, err
	}
	for _, f := range zr.File {
		if f.FileInfo().Mode().IsRegular() && wanted(name, f.Name) {
			rc, err := f.Open()
			if err != nil {
				return nil, err
			}
			defer func() { _ = rc.Close() }()
			return io.ReadAll(io.LimitReader(rc, maxSize))
		}
	}
	return nil, fmt.Errorf("the archive has no %s", Binary(name))
}
