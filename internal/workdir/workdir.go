// Package workdir is where pack keeps what it needs between runs, in the directory a command runs in, the way
// terraform has .terraform:
//
//	.pack/state.json                    the record: what is installed, how far a saga got
//	.pack/state.<env>.json              the same for --env <env>: a state per environment, as terraform has per workspace
//	.pack/journal/<id>/<run>.jsonl      the sagas' journals: stages and logs
//	.pack/packs/<digest>/               the packs brought from a registry (pack init, pack refresh): each pack beside the ones it holds
//
// PACK_DIR moves it; PACK_JOURNAL moves the journals only, "off" keeps none.
package workdir

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"uuid"

	"github.com/horchestra/pack/internal/release"
	"github.com/horchestra/pack/pkg/adapter"
	"github.com/horchestra/pack/pkg/saga"
	"github.com/horchestra/pack/pkg/saga/storage"
	"github.com/horchestra/pack/pkg/target"
)

// Where the record is kept: release.yaml's `state:`.
const (
	StateFile   = "file"
	StateTarget = "target"
)

// Dir is the pack directory of one run.
type Dir struct {
	Release *release.Release
	Env     string // --env: picks env.<Env>.yaml and state.<Env>.json
	Path    string // the pack directory, .pack
	Journal string // "" — <Path>/journal; "off" — none
}

// New is the directory of a run in rel, with the locations the environment variables say.
func New(rel *release.Release, env string) *Dir {
	return &Dir{Release: rel, Env: env, Path: envOr("PACK_DIR", ".pack"), Journal: envOr("PACK_JOURNAL", "")}
}

func envOr(name, def string) string {
	if v := os.Getenv(name); len(v) > 0 {
		return v
	}
	return def
}

// Ensure makes the directory and a .gitignore that keeps it out of the repository: the state is not source, and
// the cache is not either.
func (d *Dir) Ensure() {
	if err := os.MkdirAll(d.Path, 0o700); err != nil {
		return
	}
	p := filepath.Join(d.Path, ".gitignore")
	if _, err := os.Stat(p); os.IsNotExist(err) {
		_ = os.WriteFile(p, []byte("*\n"), 0o644)
	}
}

// JournalPath is where the journals are.
func (d *Dir) JournalPath() string {
	if len(d.Journal) > 0 {
		return d.Journal
	}
	return filepath.Join(d.Path, "journal")
}

// PacksPath is where the packs brought from a registry are, one directory for each digest.
func (d *Dir) PacksPath() string { return filepath.Join(d.Path, "packs") }

// BinPath is where the programs of the targets are, pack-target-<name> each: what `pack refresh` downloads, and where
// pack looks for them unless PACK_TARGET_PATH says otherwise.
func (d *Dir) BinPath() string { return filepath.Join(d.Path, "bin") }

// JournalFor is the journal of an installation, by its id; nil when it is off.
func (d *Dir) JournalFor(id string) saga.Journal {
	if d.Journal == "off" {
		return nil
	}
	return storage.FileJournal{Dir: d.JournalPath(), Installation: id}
}

// StateName is the state's file: state, and state.<env> for --env <env>.
func (d *Dir) StateName() string {
	if len(d.Env) == 0 {
		return "state"
	}
	return "state." + d.Env
}

// FileState is the record in the pack directory.
func (d *Dir) FileState() storage.FileStore {
	return storage.FileStore{Dir: d.Path, Name: d.StateName()}
}

// Store is where the record of the installation is kept: the file (the default), or the target's own system
// (`state: target` in release.yaml, which needs a target that is a Recorder). x may be nil for a file.
func (d *Dir) Store(x target.Recorder) (saga.Store, error) {
	switch kind := d.Release.State; kind {
	case "", StateFile:
		return d.FileState(), nil
	case StateTarget:
		if x == nil {
			return nil, ErrNoExecutor
		}
		return adapter.Store{X: x}, nil
	default:
		return nil, fmt.Errorf("release.yaml: state %q: file or target", kind)
	}
}

// ErrNoExecutor a state in the target needs a target that can keep records.
var ErrNoExecutor = errors.New("state: target needs a target that keeps records")

// InstallationID is the id of the installation: the one in the record, else a new UUID v4. It is kept in the
// record by the first apply. A state in the target has no file to look in: the release says the id.
func (d *Dir) InstallationID() (string, error) {
	switch d.Release.State {
	case "", StateFile:
		rec, err := d.FileState().Load()
		if err != nil {
			return "", err
		}
		if rec != nil && len(rec.ID) > 0 {
			return rec.ID, nil
		}
		return NewID(), nil
	case StateTarget:
		if _, err := uuid.Parse(d.Release.ID); err != nil {
			return "", fmt.Errorf("state: target needs `id:` in release.yaml, a UUID: %w", err)
		}
		return d.Release.ID, nil
	}
	return "", fmt.Errorf("release.yaml: state %q: file or target", d.Release.State)
}

// NewID makes the id of an installation: a UUID v4.
func NewID() string { return uuid.NewV4().String() }
