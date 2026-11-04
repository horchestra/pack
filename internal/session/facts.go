package session

import (
	"path/filepath"

	"github.com/horchestra/pack/internal/workdir"
	"github.com/horchestra/pack/pkg/target"
)

// FactsFile is the file of the facts of a target of the environment (state: file): the default one's is
// render.DefaultTarget's.
func FactsFile(d *workdir.Dir, name string) string { return filepath.Join(d.FactsPath(), name+".json") }

// factsOf is where the installation's facts are kept: files of the pack directory, or the state database's.
func factsOf(d *workdir.Dir) (workdir.FactsStore, error) {
	id, err := d.InstallationID()
	if err != nil {
		return nil, err
	}
	return d.FactsOf(id)
}

// SaveFacts writes the facts a target told.
func SaveFacts(d *workdir.Dir, name string, f *target.Facts) error {
	fs, err := factsOf(d)
	if err != nil {
		return err
	}
	if c, ok := fs.(interface{ Close() error }); ok {
		defer func() { _ = c.Close() }()
	}
	return fs.Save(name, f)
}

// ForgetFacts removes the facts of a target that tells none now: what it told once is not what is there.
func ForgetFacts(d *workdir.Dir, name string) error {
	fs, err := factsOf(d)
	if err != nil {
		return err
	}
	if c, ok := fs.(interface{ Close() error }); ok {
		defer func() { _ = c.Close() }()
	}
	return fs.Forget(name)
}

// LoadFacts reads the facts the targets told, by the target's name; none told — none. Facts that do not read are
// said in warnings: facts are a help to the values, never a reason to stop.
func LoadFacts(d *workdir.Dir) (map[string]*target.Facts, []string, error) {
	fs, err := factsOf(d)
	if err != nil {
		return nil, nil, err
	}
	if c, ok := fs.(interface{ Close() error }); ok {
		defer func() { _ = c.Close() }()
	}
	return fs.Load()
}
