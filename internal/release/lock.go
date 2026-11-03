package release

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
)

// LockFile is what `pack refresh` (and `pack init`) writes for a pack in a registry: which digest the tag meant on that day. It is
// the release's, like release.yaml — in the repository.
const LockFile = "release.lock.json"

// Lock pins a pack of a registry and the programs of the targets.
type Lock struct {
	Pack   string `json:"pack,omitempty"`   // as release.yaml says: oci://host/repo:tag
	Digest string `json:"digest,omitempty"` // what the registry had for it
	// Targets are the programs `pack refresh` downloaded, by the target's name.
	Targets map[string]TargetLock `json:"targets,omitempty"`
}

// TargetLock is a downloaded program — where it came from, as release.yaml says, and its SHA-256 that day — and the
// image the target runs from in a container, with the digest its tag meant.
type TargetLock struct {
	Source string `json:"source,omitempty"`
	SHA256 string `json:"sha256,omitempty"`
	Image  string `json:"image,omitempty"`
	Digest string `json:"digest,omitempty"`
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
