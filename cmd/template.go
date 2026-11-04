package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	"github.com/spf13/cobra"

	"github.com/horchestra/pack/internal/interrupt"
	"github.com/horchestra/pack/pkg/api"

	"github.com/horchestra/pack/pkg/render"
)

func init() {
	templateCmd.Flags().StringVarP(&templateOut, "output", "o", "", "write the files into this directory, <key>/<path>, instead of printing them")
	templateCmd.Flags().StringVar(&templateKey, "key", "", "only the files of this key")
	rootCmd.AddCommand(templateCmd)
}

var (
	templateOut string
	templateKey string
)

var templateCmd = &cobra.Command{
	Use:   "template",
	Short: "the config files of the release, rendered, without installing anything",
	Long: `Renders the release's pack with the release's values and prints the config files of
each key, or writes them under -o as <key>/<path> (a file that differs per replica:
<key>/<n>/<path>). A file whose bytes are a secret is only named.`,
	Args: cobra.NoArgs,
	Run: run(func(*cobra.Command, []string) error {
		ctx, stop := interrupt.Context()
		defer stop()
		s, err := open(ctx, true)
		if err != nil {
			return err
		}
		defer s.Close()
		_, rel, err := s.Render()
		if err != nil {
			return err
		}
		if jsonOut && len(templateOut) == 0 {
			files, err := templateFiles(rel, templateKey)
			if err != nil {
				return err
			}
			return result(api.TemplateResult{Files: files})
		}
		return emit(rel, templateKey, templateOut)
	}),
}

// maxContent is how much of a file template --json gives: the rest is said truncated.
const maxContent = 64 << 10

func templateFiles(rel *render.Release, key string) ([]api.TemplateFile, error) {
	var out []api.TemplateFile
	add := func(k string, inst *int, path string, b []byte) {
		f := api.TemplateFile{Key: k, Instance: inst, Path: path, Content: string(b)}
		if len(b) > maxContent {
			f.Content, f.Truncated = string(b[:maxContent]), true
		}
		out = append(out, f)
	}
	found := len(key) == 0
	for _, u := range rel.Units {
		if len(key) > 0 && u.Key != key {
			continue
		}
		found = true
		for _, f := range u.Files {
			if f.PerInstance() {
				for i, b := range f.Instances {
					if b != nil {
						add(u.Key, &i, f.Path, b)
					}
				}
				continue
			}
			add(u.Key, nil, f.Path, f.Content)
		}
		for _, f := range u.SecretFiles {
			out = append(out, api.TemplateFile{Key: u.Key, Path: f.Path, Secret: f.Secret.Name})
		}
	}
	if !found {
		return nil, fmt.Errorf("no key %q in the release", key)
	}
	return out, nil
}

// emit prints or writes the files of the keys of rel (key: all when empty).
func emit(rel *render.Release, key, out string) error {
	found := len(key) == 0
	for _, u := range rel.Units {
		if len(key) > 0 && u.Key != key {
			continue
		}
		found = true
		for _, f := range u.Files {
			if f.PerInstance() {
				for i, b := range f.Instances {
					if b != nil {
						if err := put(out, u.Key+"/"+strconv.Itoa(i), f.Path, b, f.Mode); err != nil {
							return err
						}
					}
				}
				continue
			}
			if err := put(out, u.Key, f.Path, f.Content, f.Mode); err != nil {
				return err
			}
		}
		for _, f := range u.SecretFiles {
			if len(out) == 0 {
				fmt.Printf("---\n# %s: %s (secret %s)\n", u.Key, f.Path, f.Secret.Name)
			}
		}
	}
	if !found {
		return fmt.Errorf("no key %q in the release", key)
	}
	return nil
}

func put(out, where, path string, b []byte, mode *os.FileMode) error {
	if len(out) == 0 {
		fmt.Printf("---\n# %s: %s\n%s", where, path, b)
		if len(b) > 0 && b[len(b)-1] != '\n' {
			fmt.Println()
		}
		return nil
	}
	dst := filepath.Join(out, where, filepath.Clean("/"+path))
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	perm := os.FileMode(0o644)
	if mode != nil {
		perm = *mode
	}
	return os.WriteFile(dst, b, perm)
}
