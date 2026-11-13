// Package workdir is where pack keeps what it needs between runs, in the directory a command runs in, the way
// terraform has .terraform:
//
//	.pack/state.json                    the record: what is installed, how far a saga got (state: file)
//	.pack/state.<env>.json              the same for --env <env>: a state per environment, as terraform has per workspace
//	.pack/journal/<id>/<run>.jsonl      the sagas' journals: stages and logs (state: file)
//	.pack/facts/<target>.json           the facts the targets told (state: file)
//	.pack/home/<target>/                the HOME of a target's container (runtime docker)
//
// state: db keeps the record, the journal and the facts in a database instead (postgres://… or sqlite://<path>,
// the DSN in the variable state_dsn_env names, PACK_STATE_DSN by default) — then pack is stateless: the directory
// holds nothing a second process, on another checkout, could not read there. The packs and the programs of the
// targets are the cache's (PACK_CACHE, by default ~/.cache/pack), shared by every release.
//
// PACK_DIR moves .pack; PACK_JOURNAL moves the journals only, "off" keeps none.
package workdir

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"uuid"

	"github.com/horchestra/pack/internal/adapter"
	"github.com/horchestra/pack/internal/release"
	"github.com/horchestra/pack/internal/storage"
	"github.com/horchestra/pack/pkg/saga"
	"github.com/horchestra/pack/pkg/target"
	"github.com/horchestra/pack/pkg/target/remote"
)

// Where the record is kept: release.yaml's `state:`.
const (
	StateFile   = "file"
	StateTarget = "target"
	StateDB     = "db"
	// StateObserve keeps no record at all: the plan is derived from what the target says it holds
	// (target.Inspector), and a saga cut short is replanned from the same observation. For self-deploy on the
	// application's own host.
	StateObserve = "observe"
)

// memStore is the record of an installation that keeps none (state: observe): it lives for the run, so the saga
// of the run has its savepoints, and is gone with the process — the next run replans from the target.
type memStore struct{ rec *saga.Record }

func (s *memStore) Load() (*saga.Record, error) { return s.rec, nil }
func (s *memStore) Save(r *saga.Record) error   { s.rec = r; return nil }
func (s *memStore) Delete() error               { s.rec = nil; return nil }

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

// Ensure makes the directory and a .gitignore that keeps it out of the repository: the state is not source.
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

// FactsPath is where the facts the targets told are, <target>.json each (pack discover).
func (d *Dir) FactsPath() string { return filepath.Join(d.Path, "facts") }

// PacksPath is where the packs brought from a registry are, one directory for each digest: the cache's, shared by
// every release.
func (d *Dir) PacksPath() string { return filepath.Join(remote.CacheDir(), "packs") }

// BinPath is where the programs of the targets are, pack-target-<name> each: what `pack refresh` downloads, into
// the cache's bin, where pack looks for them unless PACK_TARGET_PATH says otherwise.
func (d *Dir) BinPath() string { return remote.BinDir() }

// StateDSN is where the state database is: the DSN in the variable release.yaml names (state_dsn_env,
// PACK_STATE_DSN by default).
func (d *Dir) StateDSN() (string, error) {
	name := d.Release.StateDSNEnv
	if len(name) == 0 {
		name = "PACK_STATE_DSN"
	}
	dsn := os.Getenv(name)
	if len(dsn) == 0 {
		return "", fmt.Errorf("state: db, and %s is not set: postgres://… or sqlite://<path> (state_dsn_env names the variable)", name)
	}
	return dsn, nil
}

// OpenDB opens the release's state database.
func (d *Dir) OpenDB(ctx context.Context) (*storage.DB, error) {
	dsn, err := d.StateDSN()
	if err != nil {
		return nil, err
	}
	return storage.OpenDB(ctx, dsn)
}

// JournalFor is the journal of an installation, by its id; nil when it is off.
func (d *Dir) JournalFor(id string) saga.Journal {
	if d.Journal == "off" {
		return nil
	}
	if d.Release.State == StateDB {
		db, err := d.OpenDB(context.Background())
		if err != nil {
			return nil // the saga goes on without a journal it cannot open; the record is what it stands on
		}
		j, err := db.JournalOf(id)
		if err != nil {
			return nil
		}
		return &dbJournal{Journal: j, db: db}
	}
	return storage.FileJournal{Dir: d.JournalPath(), Installation: id}
}

// JournalReaderFor reads the journal of an installation back: the file's, or the state database's.
func (d *Dir) JournalReaderFor(id string) (saga.JournalReader, error) {
	if d.Release.State == StateDB {
		db, err := d.OpenDB(context.Background())
		if err != nil {
			return nil, err
		}
		j, err := db.JournalOf(id)
		if err != nil {
			_ = db.Close()
			return nil, err
		}
		return &dbJournal{Journal: j, db: db}, nil
	}
	return storage.FileJournal{Dir: d.JournalPath(), Installation: id}, nil
}

// dbJournal lets the database go when the journal is.
type dbJournal struct {
	*storage.Journal
	db *storage.DB
}

func (j *dbJournal) Close() error { return j.db.Close() }

// FactsOf is where the facts the targets told are kept for the installation: files of the pack directory, or
// pack_facts of the state database.
func (d *Dir) FactsOf(id string) (FactsStore, error) {
	if d.Release.State == StateDB {
		db, err := d.OpenDB(context.Background())
		if err != nil {
			return nil, err
		}
		f, err := db.FactsOf(id)
		if err != nil {
			_ = db.Close()
			return nil, err
		}
		return &dbFacts{Facts: f, db: db}, nil
	}
	return FileFacts{Dir: d.FactsPath()}, nil
}

// FactsStore keeps the facts the targets told (pack discover), by the target's name.
type FactsStore interface {
	Save(name string, f *target.Facts) error
	Load() (facts map[string]*target.Facts, warnings []string, err error)
	Forget(name string) error
}

type dbFacts struct {
	*storage.Facts
	db *storage.DB
}

func (f *dbFacts) Load() (map[string]*target.Facts, []string, error) {
	facts, err := f.Facts.Load()
	return facts, nil, err
}

func (f *dbFacts) Close() error { return f.db.Close() }

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

// Store is where the record of the installation is kept: the file (the default), the target's own system
// (`state: target` in release.yaml, which needs a target that is a Recorder), the state database (`state: db`,
// the DSN in the environment), or the run's memory alone (`state: observe` — no record outlives the run).
// x may be nil for a file, a database and observe.
func (d *Dir) Store(x target.Recorder) (saga.Store, error) {
	switch kind := d.Release.State; kind {
	case "", StateFile:
		return d.FileState(), nil
	case StateObserve:
		return &memStore{}, nil
	case StateTarget:
		if x == nil {
			return nil, ErrNoExecutor
		}
		return adapter.Store{X: x}, nil
	case StateDB:
		id, err := d.InstallationID()
		if err != nil {
			return nil, err
		}
		db, err := d.OpenDB(context.Background())
		if err != nil {
			return nil, err
		}
		st, err := db.Store(id)
		if err != nil {
			_ = db.Close()
			return nil, err
		}
		return &DBStore{Store: st, db: db}, nil
	default:
		return nil, fmt.Errorf("release.yaml: state %q: file, target, db or observe", kind)
	}
}

// DBStore lets the database go when the store is (Session.Close).
type DBStore struct {
	*storage.Store
	db *storage.DB
}

// Close releases the database.
func (s *DBStore) Close() error { return s.db.Close() }

// ErrNoExecutor a state in the target needs a target that can keep records.
var ErrNoExecutor = errors.New("state: target needs a target that keeps records")

// InstallationID is the id of the installation: the one in the record, else a new UUID v4. It is kept in the
// record by the first apply. A state in the target, in a database, or none (observe — the run's memory has
// nothing to look in) has no file to look in: the release says the id (pack refresh writes it, once).
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
	case StateTarget, StateDB, StateObserve:
		if _, err := uuid.Parse(d.Release.ID); err != nil {
			return "", fmt.Errorf("state: %s needs `id:` in release.yaml, a UUID (pack refresh makes one): %w", d.Release.State, err)
		}
		return d.Release.ID, nil
	}
	return "", fmt.Errorf("release.yaml: state %q: file, target, db or observe", d.Release.State)
}

// NewID makes the id of an installation: a UUID v4.
func NewID() string { return uuid.NewV4().String() }
