package cmd

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"

	"github.com/spf13/cobra"
	"sigs.k8s.io/yaml"

	"github.com/horchestra/pack/pkg/api"

	"github.com/horchestra/pack/internal/interrupt"
	"github.com/horchestra/pack/internal/oci"
	"github.com/horchestra/pack/internal/release"
	"github.com/horchestra/pack/internal/session"
	"github.com/horchestra/pack/internal/targets"
	"github.com/horchestra/pack/internal/valuesfile"
	"github.com/horchestra/pack/internal/workdir"
	"github.com/horchestra/pack/pkg/environment"
	"github.com/horchestra/pack/pkg/target"
	"github.com/horchestra/pack/pkg/target/remote"
)

func init() {
	initCmd.Flags().StringVar(&initTarget, "target", "", "the target the release is installed on (docker, ansible, k8s, memory); asked when it is a terminal")
	initCmd.Flags().StringVar(&initSource, "target-source", "", "where the target's program is downloaded from (https://… or oci://…), into release.yaml targets:")
	initCmd.Flags().StringVar(&initImage, "target-image", "", "the target's image (oci://…), into release.yaml targets:; with --runtime docker the target runs from it")
	initCmd.Flags().StringVar(&initRuntime, "runtime", "", "where the target's program runs: native (the default), docker (its image, in a container) or k8s (its image, in a Pod)")
	initCmd.Flags().StringVar(&initLang, "lang", "", "the language of the comments of values.yaml (env.yaml lang:)")
	initCmd.Flags().BoolVar(&initForce, "force", false, "write the files over the ones there")
	rootCmd.AddCommand(initCmd)
}

var (
	initTarget  string
	initSource  string
	initImage   string
	initRuntime string
	initLang    string
	initForce   bool
)

// The targets a release is likely installed on: pack's own and the ones of github.com/horchestra/targets.
var knownTargets = []string{"docker", "ansible", "k8s"}

// makesRelease marks a command that makes the release it runs in: the root command reads none before it.
const makesRelease = "makes-release"

var initCmd = &cobra.Command{
	Use:         "init <oci://registry/repo:tag|dir> [release dir]",
	Annotations: map[string]string{makesRelease: "true"},
	Short:       "a release of a pack: release.yaml, env.yaml, a documented values.yaml, AGENTS.md; then init",
	Long: `Makes a release of the pack in the directory (made if there is none, . by default):
release.yaml names the pack (and the target's source, --target-source); env.yaml (env.<name>.yaml
with --env) the target; values.yaml every setting of the pack, documented, left to its default
(pack values write); AGENTS.md how an agent works with the release. Then it does what pack
refresh does: the pack of a registry locked and brought into .pack, the target's program
downloaded.

The target is --target, or asked when pack runs in a terminal: one pack knows (docker,
ansible, k8s, memory), or any with --target-source. A file that is there is not written over
without --force; pack refresh brings what a release that is there needs.`,
	Args: cobra.RangeArgs(1, 2),
	Run: run(func(_ *cobra.Command, args []string) error {
		ctx, stop := interrupt.Context()
		defer stop()
		out := "."
		if len(args) > 1 {
			out = args[1]
		}
		said, err := initRelease(ctx, args[0], out, os.Stdin, isTerminal(os.Stdin) && !jsonOut)
		for _, s := range said {
			fmt.Println(s)
		}
		if err != nil {
			return err
		}
		abs, _ := filepath.Abs(".") // initRelease is in the release's directory now
		if !jsonOut {
			return nil
		}
		return result(struct {
			Dir  string        `json:"dir"`
			Said []string      `json:"said"`
			Next api.NextState `json:"next"` // what pack next says of the release made
		}{abs, said, whatNext(ctx)})
	}),
}

func isTerminal(f *os.File) bool {
	st, err := f.Stat()
	return err == nil && st.Mode()&os.ModeCharDevice != 0
}

// initRelease makes the release of ref in out, and says what it did.
func initRelease(ctx context.Context, ref, out string, in io.Reader, ask bool) ([]string, error) {
	var said []string
	if !oci.IsRef(ref) {
		abs, err := filepath.Abs(ref)
		if err != nil {
			return nil, err
		}
		if st, err := os.Stat(filepath.Join(abs, "schema.json")); err != nil || st.IsDir() {
			return nil, fmt.Errorf("%s: not a pack (no schema.json) and not oci://…", ref)
		}
		ref = abs
	}
	if err := os.MkdirAll(out, 0o755); err != nil {
		return nil, err
	}
	out, err := filepath.Abs(out)
	if err != nil {
		return nil, err
	}
	if !oci.IsRef(ref) {
		// the release names a pack near it from where it is, so that the two move together; one far away by its path
		if rel, err := filepath.Rel(out, ref); err == nil && strings.Count(rel, "..") <= 2 {
			ref = rel
		}
	}
	name := initTarget
	if len(name) == 0 {
		if !ask {
			return nil, fmt.Errorf("--target names the target the release is installed on (%s)", strings.Join(targetChoices(), ", "))
		}
		if name, err = askTarget(in); err != nil {
			return nil, err
		}
	}
	if !slices.Contains(targetChoices(), name) && len(initSource) == 0 && len(initImage) == 0 {
		return nil, fmt.Errorf("target %q: not one pack knows (%s) — --target-source or --target-image names where a target of another name comes from", name, strings.Join(targetChoices(), ", "))
	}
	if len(initRuntime) == 0 && len(initImage) > 0 && len(initSource) == 0 {
		initRuntime = remote.RuntimeDocker // an image and no program: the target runs from its image
	}
	switch initRuntime {
	case "":
	case remote.RuntimeNative:
		if len(initSource) == 0 && len(initImage) > 0 {
			return nil, errors.New("--runtime native runs the target's program: --target-source names where it comes from (the image is for --runtime docker)")
		}
	case remote.RuntimeDocker, remote.RuntimeK8s:
		if len(initImage) == 0 {
			return nil, fmt.Errorf("--runtime %s runs the target's image: --target-image oci://… names it", initRuntime)
		}
	default:
		return nil, fmt.Errorf("--runtime %s: native, docker or k8s", initRuntime)
	}
	envFile := "env.yaml"
	if len(envName) > 0 {
		envFile = "env." + envName + ".yaml"
	}
	files := []string{release.File, envFile, "values.yaml", "AGENTS.md"}
	if !initForce {
		var there []string
		for _, f := range files {
			if _, err := os.Stat(filepath.Join(out, f)); err == nil {
				there = append(there, f)
			}
		}
		if len(there) > 0 {
			return nil, fmt.Errorf("%s: %s already there — pack refresh brings what a release that is there needs; --force writes over", out, strings.Join(there, ", "))
		}
	}

	rel := map[string]any{"pack": ref}
	if len(initSource) > 0 || len(initImage) > 0 {
		src := map[string]any{}
		if len(initSource) > 0 {
			src["url"] = initSource
		}
		if len(initImage) > 0 {
			src["image"] = initImage
		}
		rel["targets"] = map[string]any{name: src}
	}
	env := map[string]any{"target": name}
	if remote.FromImage(initRuntime) {
		env["runtime"] = map[string]any{"type": initRuntime}
	}
	if len(initLang) > 0 {
		env["lang"] = initLang
	}
	docs := map[string]any{release.File: rel, envFile: env}
	for _, f := range []string{release.File, envFile} {
		b, err := yaml.Marshal(docs[f])
		if err != nil {
			return nil, err
		}
		if err := os.WriteFile(filepath.Join(out, f), b, 0o644); err != nil {
			return nil, err
		}
		said = append(said, "wrote "+f)
	}

	// from here on the release is the directory's: what the root command does for -C
	if err := os.Chdir(out); err != nil {
		return said, err
	}
	r, err := release.Load(".", envName)
	if err != nil {
		return said, err
	}
	dir = workdir.New(r, envName)
	dir.Ensure()
	lock, err := release.ReadLock(r.Dir)
	if err != nil {
		return said, err
	}
	if lock == nil {
		lock = &release.Lock{}
	}
	before := *lock
	before.Targets = maps.Clone(lock.Targets)
	if err := lockPack(ctx, dir, lock, false); err != nil {
		return said, err
	}
	if slices.Contains(target.Names(), name) {
		// in pack itself: nothing to download
	} else if _, err := os.Stat(filepath.Join(dir.BinPath(), targets.Binary(name))); err == nil || len(initSource) > 0 || remote.FromImage(initRuntime) {
		e, err := environment.Load(ctx, r.Config(), nil)
		if err != nil {
			return said, err
		}
		if err := bringTargets(ctx, dir, e, lock, false); err != nil {
			return said, err
		}
	} else {
		said = append(said, fmt.Sprintf("target %s: no source — name it in release.yaml (targets: %s: https://… or oci://…) and run pack refresh", name, name))
	}
	if !reflect.DeepEqual(before, *lock) {
		if err := release.WriteLock(r.Dir, lock); err != nil {
			return said, err
		}
	}

	b, err := session.ReleasePack(ctx, dir)
	if err != nil {
		return said, err
	}
	values, err := valuesfile.Write(b, nil, nil, valuesfile.Lang(initLang))
	if err != nil {
		return said, err
	}
	if err := os.WriteFile("values.yaml", values, 0o644); err != nil {
		return said, err
	}
	said = append(said, "wrote values.yaml")
	if err := os.WriteFile("AGENTS.md", []byte(agents), 0o644); err != nil {
		return said, err
	}
	said = append(said, "wrote AGENTS.md", "next: set values.yaml, then pack check, pack plan, pack apply")
	return said, nil
}

func targetChoices() []string {
	out := slices.Clone(knownTargets)
	for _, n := range target.Names() {
		if !slices.Contains(out, n) {
			out = append(out, n)
		}
	}
	return out
}

// askTarget asks which target, the first of the choices when the answer is empty.
func askTarget(in io.Reader) (string, error) {
	choices := targetChoices()
	fmt.Printf("target (%s) [%s]: ", strings.Join(choices, ", "), choices[0])
	line, err := bufio.NewReader(in).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	line = strings.TrimSpace(line)
	if len(line) == 0 {
		return choices[0], nil
	}
	return line, nil
}

// agents is AGENTS.md: how an agent — a model, or a person new to it — works with the release, in the commands
// pack has.
const agents = `# Working with this release

This directory is a release of a pack: release.yaml says which pack, env.yaml where it is installed,
values.yaml its settings. Work through the pack command; do not edit .pack/ or release.lock.json.

0. ` + "`pack next --json`" + ` says where the release is, what stops it and the commands to run next: follow it, and
   run it again after each step. Every command takes --json: one JSON document on stdout, an error as
   {"error": {"code", "message", "problems"}, "next": [...]}.
1. ` + "`pack fill --json`" + ` lists what is to be answered — the settings required, the ones the pack suggests a
   value for, each with its schema and the answers offered; ` + "`pack answer <pointer> <value> --source llm`" + `
   answers one (` + "`--patch file`" + ` a JSON Patch of many). An answer the schema or a rule refuses is not written.
   The settings are in values.yaml, each documented in the comments above it. A line ` + "`#key: value`" + ` is
   left to its default: remove the ` + "`#`" + ` to set it. Write no comments of your own: pack values write
   refuses a file that has them.
2. Never write a password or a token into a file. A secret is a reference to one kept elsewhere:

       password:
         secretRef:
           name: db-admin
           key: password

3. After changing values.yaml run ` + "`pack check`" + `. Fix what it says and run it again until it says ok.
4. ` + "`pack plan`" + ` says what an apply would change. Read it, then ` + "`pack apply`" + `.
5. When an apply fails, ` + "`pack log`" + ` shows what happened, ` + "`pack rollback`" + ` takes back what it left
   unfinished.
6. ` + "`pack refresh --upgrade`" + ` brings the version the pack's tag has now; ` + "`pack values write`" + ` then
   writes values.yaml for it, the values kept.

An apply refused with needs_approval is a plan with risks (data, credentials, a downgrade …): show
it to a person; ` + "`--approve`" + ` is theirs to give, not yours.

` + "`pack status`" + ` says how the installation is; ` + "`pack run`" + ` lists what may be run on it besides an apply (a
backup, a restore …) and runs one: run only those, never a command of your own on the hosts.
A client of MCP has the same as tools: ` + "`pack mcp`" + ` in this directory.

Stop and ask a person when a plan removes something that holds data, when a problem of pack check is not
about a value, or when you are not sure what a setting means.
`
