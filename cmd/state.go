package cmd

import (
	"context"
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/horchestra/pack/internal/storage"
	"github.com/horchestra/pack/internal/workdir"
	"github.com/horchestra/pack/pkg/saga"
)

var stateCmd = &cobra.Command{
	Use:   "state",
	Short: "the record and the journal of the installation",
}

var stateMigrateCmd = newStateMigrateCmd()

func init() {
	stateCmd.AddCommand(stateMigrateCmd)
	rootCmd.AddCommand(stateCmd)
}

func newStateMigrateCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "migrate",
		Short: "move the record and the journal between the file and the state database, the history whole",
		Args:  cobra.NoArgs,
	}
	to := cmd.Flags().String("to", "", "where the state goes: db or file (the opposite of release.yaml's state by default)")
	cmd.Run = run(func(*cobra.Command, []string) error {
		from, to2, err := migrateEnds(dir, *to)
		if err != nil {
			return err
		}
		return migrateState(dir, from, to2)
	})
	return cmd
}

// migrateEnds is where the state moves from and to: --to says it, else the opposite of release.yaml's state.
func migrateEnds(d *workdir.Dir, to string) (from, dest string, err error) {
	cur := d.Release.State
	if len(cur) == 0 {
		cur = workdir.StateFile
	}
	if len(to) == 0 {
		if cur == workdir.StateDB {
			to = workdir.StateFile
		} else {
			to = workdir.StateDB
		}
	}
	if to != workdir.StateDB && to != workdir.StateFile {
		return "", "", fmt.Errorf("--to %s: db or file", to)
	}
	if to == cur {
		return "", "", fmt.Errorf("the state is %s already", to)
	}
	if cur != workdir.StateDB && cur != workdir.StateFile {
		return "", "", fmt.Errorf("state: %s migrates not — only file and db", cur)
	}
	return cur, to, nil
}

// migrateState moves the record and every saga of the journal from one storage to the other; nothing is removed
// from where it was: the move is made of copies, the release's `state:` is the user's to change.
func migrateState(d *workdir.Dir, from, to string) error {
	src, srcJ, err := stateSide(d, from)
	if err != nil {
		return err
	}
	defer closeAll(src, srcJ)
	dst, dstJ, err := stateSide(d, to)
	if err != nil {
		return err
	}
	defer closeAll(dst, dstJ)

	rec, err := src.Load()
	if err != nil {
		return err
	}
	if rec == nil {
		return fmt.Errorf("no record in the %s state: nothing was applied here", from)
	}
	if cur, err := dst.Load(); err != nil {
		return err
	} else if cur != nil {
		return fmt.Errorf("the %s state holds a record of %s already: it is not written over", to, cur.ID)
	}
	if err := dst.Save(rec); err != nil {
		return err
	}
	fmt.Println("record of", rec.ID, "moved to", to)

	runs, err := srcJ.Runs()
	if err != nil {
		return err
	}
	for _, r := range runs {
		entries, err := srcJ.Read(r)
		if err != nil {
			return err
		}
		for _, e := range entries {
			if err := dstJ.Append(e); err != nil {
				return err
			}
		}
	}
	fmt.Printf("%d saga(s) of the journal moved to %s\n", len(runs), to)
	fmt.Printf("now write `state: %s` in release.yaml — the %s copies stay until you remove them\n", to, from)
	return nil
}

// journalRW is a journal whole: migrate reads it where it was and appends where it goes.
type journalRW interface {
	saga.Journal
	saga.JournalReader
}

// stateSide is the record and the journal of one storage, whatever release.yaml's `state:` says: migrate moves
// between them, so both are needed at once.
func stateSide(d *workdir.Dir, kind string) (saga.Store, journalRW, error) {
	switch kind {
	case workdir.StateFile:
		id, err := d.InstallationID()
		if err != nil {
			return nil, nil, err
		}
		return d.FileState(), storage.FileJournal{Dir: d.JournalPath(), Installation: id}, nil
	case workdir.StateDB:
		id, err := dbID(d, kind)
		if err != nil {
			return nil, nil, err
		}
		db, err := d.OpenDB(context.Background())
		if err != nil {
			return nil, nil, err
		}
		st, err := db.Store(id)
		if err != nil {
			_ = db.Close()
			return nil, nil, err
		}
		j, err := db.JournalOf(id)
		if err != nil {
			_ = db.Close()
			return nil, nil, err
		}
		return &dbSide{Store: st, Journal: j, db: db}, &dbSide{Store: st, Journal: j, db: db}, nil
	}
	return nil, nil, fmt.Errorf("state %q: file or db", kind)
}

// dbID is the id of the installation a database state is of: the release's `id:` when it says one (state: db),
// else the record's — migrate to a database runs before `id:` is written (pack refresh writes it).
func dbID(d *workdir.Dir, kind string) (string, error) {
	if len(d.Release.ID) > 0 {
		return d.Release.ID, nil
	}
	rec, err := d.FileState().Load()
	if err != nil {
		return "", err
	}
	if rec == nil || len(rec.ID) == 0 {
		return "", fmt.Errorf("no record in the %s state: nothing was applied here", workdir.StateFile)
	}
	return rec.ID, nil
}

// dbSide lets the database go when the migration is done.
type dbSide struct {
	*storage.Store
	*storage.Journal
	db *storage.DB
}

func (s *dbSide) Close() error { return s.db.Close() }

func closeAll(v ...any) {
	for _, x := range v {
		if c, ok := x.(io.Closer); ok {
			_ = c.Close()
		}
	}
}
