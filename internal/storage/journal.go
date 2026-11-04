package storage

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/horchestra/pack/pkg/saga"
)

// FileJournal keeps a journal as JSON lines, a file per saga: <Dir>/<installation>/<run>.jsonl. What a target said
// on the way may name hosts and paths, so the files are the user's alone (0600, the directories 0700).
type FileJournal struct {
	Dir          string
	Installation string
}

func (f FileJournal) path(run string) string {
	return filepath.Join(f.Dir, f.Installation, run+".jsonl")
}

func (f FileJournal) Append(e saga.Entry) error {
	if err := os.MkdirAll(filepath.Join(f.Dir, f.Installation), 0o700); err != nil {
		return err
	}
	line, err := json.Marshal(e)
	if err != nil {
		return err
	}
	fh, err := os.OpenFile(f.path(e.Run), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := fh.Write(append(line, '\n')); err != nil {
		_ = fh.Close()
		return err
	}
	return fh.Close()
}

// Runs lists the sagas the journal holds, oldest first (an id sorts by its time).
func (f FileJournal) Runs() ([]string, error) {
	entries, err := os.ReadDir(filepath.Join(f.Dir, f.Installation))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var runs []string
	for _, e := range entries {
		if id, ok := strings.CutSuffix(e.Name(), ".jsonl"); ok {
			runs = append(runs, id)
		}
	}
	sort.Strings(runs)
	return runs, nil
}

// Read returns the entries of one saga.
func (f FileJournal) Read(run string) ([]saga.Entry, error) {
	fh, err := os.Open(f.path(run))
	if err != nil {
		return nil, err
	}
	defer func() { _ = fh.Close() }()
	var out []saga.Entry
	sc := bufio.NewScanner(fh)
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	for sc.Scan() {
		var e saga.Entry
		if err := json.Unmarshal(sc.Bytes(), &e); err != nil {
			return nil, fmt.Errorf("%s: %w", f.path(run), err)
		}
		out = append(out, e)
	}
	return out, sc.Err()
}

// Prune removes the oldest sagas, so that keep newest stay; keep below 0 keeps all. It returns the ones removed.
func (f FileJournal) Prune(keep int) ([]string, error) {
	if keep < 0 {
		return nil, nil
	}
	runs, err := f.Runs()
	if err != nil || len(runs) <= keep {
		return nil, err
	}
	old := runs[:len(runs)-keep]
	for _, r := range old {
		if err := os.Remove(f.path(r)); err != nil && !os.IsNotExist(err) {
			return nil, err
		}
	}
	return old, nil
}
