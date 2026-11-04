// Package storage holds where a saga keeps its record and its journal: files in the directory pack runs in, by
// default, the way terraform keeps its state. The interfaces are the saga's (saga.Store, saga.Journal); a
// store of another kind — a target, an object store — implements them elsewhere.
package storage

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	"github.com/horchestra/pack/pkg/saga"
)

// FileStore keeps the record in a file, <Dir>/<Name>.json, the way terraform keeps its state in the
// directory it runs in. The file may hold references to credentials, never the credentials: it is 0600.
// A write goes to a temporary file and is renamed over the old one, so a record is never half written.
type FileStore struct {
	Dir  string
	Name string
}

func (s FileStore) path() string { return filepath.Join(s.Dir, s.Name+".json") }

func (s FileStore) Load() (*saga.Record, error) {
	raw, err := os.ReadFile(s.path())
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var r saga.Record
	if err := json.Unmarshal(raw, &r); err != nil {
		return nil, fmt.Errorf("%s: %w", s.path(), err)
	}
	return &r, nil
}

func (s FileStore) Save(r *saga.Record) error {
	raw, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(s.Dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(s.Dir, ".state-")
	if err != nil {
		return err
	}
	if _, err := tmp.Write(raw); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmp.Name())
		return err
	}
	return os.Rename(tmp.Name(), s.path())
}

func (s FileStore) Delete() error {
	if err := os.Remove(s.path()); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// Lock takes the installation: a second saga on it, from this directory, is refused while the first runs.
// The lock is a file made exclusively; it holds the pid of the owner. A crash leaves it behind, and the error
// says which file to remove.
func (s FileStore) Lock() (unlock func(), err error) {
	if err := os.MkdirAll(s.Dir, 0o700); err != nil {
		return nil, err
	}
	p := filepath.Join(s.Dir, s.Name+".lock")
	f, err := os.OpenFile(p, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if os.IsExist(err) {
		owner, _ := os.ReadFile(p)
		return nil, fmt.Errorf("%s is locked by pid %s: another pack is working on it; if that pack is gone, remove %s", s.Name, string(owner), p)
	}
	if err != nil {
		return nil, err
	}
	_, _ = f.WriteString(strconv.Itoa(os.Getpid()))
	_ = f.Close()
	return func() { _ = os.Remove(p) }, nil
}
