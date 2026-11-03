package session

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/horchestra/pack/internal/workdir"
	"github.com/horchestra/pack/pkg/target"
)

// FactsFile is the file of the facts of a target of the environment: the default one's is render.DefaultTarget's.
func FactsFile(d *workdir.Dir, name string) string { return filepath.Join(d.FactsPath(), name+".json") }

// SaveFacts writes the facts a target told, whole or not at all: a write cut short leaves the file it replaces. A
// target that said not when it told them is stamped now.
func SaveFacts(d *workdir.Dir, name string, f *target.Facts) error {
	if f.At.IsZero() {
		f.At = time.Now().UTC().Truncate(time.Second)
	}
	if err := os.MkdirAll(d.FactsPath(), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(d.FactsPath(), "."+name+"-*.json")
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
	return os.Rename(tmp.Name(), FactsFile(d, name))
}

// ForgetFacts removes the facts of a target that tells none now: what it told once is not what is there.
func ForgetFacts(d *workdir.Dir, name string) error {
	err := os.Remove(FactsFile(d, name))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

// LoadFacts reads the facts the targets told, by the target's name; none told — none. A file that does not read is
// left out and said in warnings: facts are a help to the values, never a reason to stop.
func LoadFacts(d *workdir.Dir) (facts map[string]*target.Facts, warnings []string, err error) {
	ents, err := os.ReadDir(d.FactsPath())
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	facts = map[string]*target.Facts{}
	for _, e := range ents {
		name, ok := strings.CutSuffix(e.Name(), ".json")
		if !ok || e.IsDir() || strings.HasPrefix(name, ".") {
			continue
		}
		path := filepath.Join(d.FactsPath(), e.Name())
		b, err := os.ReadFile(path)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("%s: %v", path, err))
			continue
		}
		var f target.Facts
		if err := json.Unmarshal(b, &f); err != nil {
			warnings = append(warnings, fmt.Sprintf("%s: %v — left out; pack discover tells them again", path, err))
			continue
		}
		facts[name] = &f
	}
	return facts, warnings, nil
}
