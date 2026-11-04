package secretstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/horchestra/pack/pkg/logging"
)

func init() {
	Register("file", openFile)
}

// fileStore keeps the secrets as files of a directory, `<path>/<secret>.json` — `{"key": "value"}` — plaintext.
// It is for a machine of one's own, a home lab, a CI job that wants no server: the warning is said at open, once.
// The directory is 0700, the files 0600; a write is a temporary file and a rename, whole or not at all.
type fileStore struct {
	dir string
}

func openFile(c Config) (Store, error) {
	if len(c.Path) == 0 {
		return nil, fmt.Errorf("store.provider file needs store.path: the directory the secrets are kept in")
	}
	logging.For("secretstore").Warn().Str("dir", c.Path).
		Msg("store.provider file: the secrets are PLAINTEXT files on disk — for a machine of one's own, not for production")
	if err := os.MkdirAll(c.Path, 0o700); err != nil {
		return nil, err
	}
	return &fileStore{dir: c.Path}, nil
}

// file is the secret's file; a path that would leave the directory is refused.
func (s *fileStore) file(path string) (string, error) {
	p := filepath.Join(s.dir, filepath.FromSlash(strings.Trim(path, "/"))+".json")
	if p != s.dir && !strings.HasPrefix(p, s.dir+string(filepath.Separator)) {
		return "", fmt.Errorf("secret %q leaves store.path", path)
	}
	return p, nil
}

func (s *fileStore) Get(_ context.Context, path string) (map[string]string, bool, error) {
	f, err := s.file(path)
	if err != nil {
		return nil, false, err
	}
	b, err := os.ReadFile(f)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	fields := map[string]string{}
	if err := json.Unmarshal(b, &fields); err != nil {
		return nil, false, fmt.Errorf("secret %s: %w", f, err)
	}
	return fields, true, nil
}

func (s *fileStore) Put(_ context.Context, path string, fields map[string]string) error {
	f, err := s.file(path)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(f), 0o700); err != nil {
		return err
	}
	b, err := json.Marshal(fields)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(f), ".tmp-*.json") // 0600 from the start: a secret never is readable
	if err != nil {
		return err
	}
	if _, err := tmp.Write(append(b, '\n')); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmp.Name())
		return err
	}
	return os.Rename(tmp.Name(), f)
}
