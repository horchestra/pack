package release

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
)

// LockFile is what `pack init` writes for a pack in a registry: which digest the tag meant on that day. It is
// the release's, like release.yaml — in the repository.
const LockFile = "release.lock.json"

// Lock pins a pack of a registry.
type Lock struct {
	Pack   string `json:"pack"`   // as release.yaml says: oci://host/repo:tag
	Digest string `json:"digest"` // what the registry had for it
}

// ReadLock reads the release's lock; nil when there is none.
func ReadLock(dir string) (*Lock, error) {
	raw, err := os.ReadFile(filepath.Join(dir, LockFile))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var l Lock
	if err = json.Unmarshal(raw, &l); err != nil {
		return nil, errors.New(LockFile + ": " + err.Error())
	}
	return &l, nil
}

// WriteLock writes the lock.
func WriteLock(dir string, l *Lock) error {
	raw, err := json.MarshalIndent(l, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, LockFile), append(raw, '\n'), 0o644)
}
