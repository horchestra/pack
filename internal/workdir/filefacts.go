package workdir

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/horchestra/pack/pkg/target"
)

// FileFacts keeps the facts the targets told as files of the pack directory, <Dir>/<target>.json each, whole or
// not at all: a write cut short leaves the file it replaces.
type FileFacts struct {
	Dir string
}

func (f FileFacts) file(name string) string { return filepath.Join(f.Dir, name+".json") }

// Save writes the facts a target told; one that said not when it told them is stamped now.
func (f FileFacts) Save(name string, facts *target.Facts) error {
	if facts.At.IsZero() {
		facts.At = time.Now().UTC().Truncate(time.Second)
	}
	if err := os.MkdirAll(f.Dir, 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(facts, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(f.Dir, "."+name+"-*.json")
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
	return os.Rename(tmp.Name(), f.file(name))
}

// Load reads the facts the targets told. A file that does not read is left out and said in warnings: facts are a
// help to the values, never a reason to stop.
func (f FileFacts) Load() (facts map[string]*target.Facts, warnings []string, err error) {
	ents, err := os.ReadDir(f.Dir)
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
		path := filepath.Join(f.Dir, e.Name())
		b, err := os.ReadFile(path)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("%s: %v", path, err))
			continue
		}
		var one target.Facts
		if err := json.Unmarshal(b, &one); err != nil {
			warnings = append(warnings, fmt.Sprintf("%s: %v — left out; pack discover tells them again", path, err))
			continue
		}
		facts[name] = &one
	}
	return facts, warnings, nil
}

// Forget removes the facts of a target that tells none now: what it told once is not what is there.
func (f FileFacts) Forget(name string) error {
	err := os.Remove(f.file(name))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}
