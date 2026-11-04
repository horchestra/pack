// Package author is the author's side of pack for a person or a model writing a pack: the language of a pack's
// source, its generator run (the SDK's own: sdk.Run in Go, packsdk.main in Python, PackSdk.main in Java), and where in
// the source a field of the schema is written — so that what pack check says of /conf/port is said at
// postgres.go:36.
package author

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

// The languages of a pack's source.
const (
	Go     = "go"
	Python = "python"
	Java   = "java"
)

// Langs are the languages, as --lang names them.
var Langs = []string{Go, Python, Java}

// Detect is the language of the source in dir: the files it has.
func Detect(dir string) (string, error) {
	has := func(glob string) bool {
		m, _ := filepath.Glob(filepath.Join(dir, glob))
		return len(m) > 0
	}
	switch {
	case has("*.go"):
		return Go, nil
	case has("*.py"):
		return Python, nil
	case has("*.java"):
		return Java, nil
	}
	return "", fmt.Errorf("%s: no .go, .py or .java of a pack", dir)
}

// Options are what a generator needs besides the source.
type Options struct {
	// Classpath is the Java SDK's classes (and what else the pack needs) for javac and java: CLASSPATH by default.
	Classpath string
	// Sources are more source files compiled with the pack's (Java: a pack it holds, written elsewhere).
	Sources []string
}

// Generate runs the source's generator in dir, writing the artifacts under out; what it said is the error when it
// fails.
func Generate(ctx context.Context, dir, lang, out string, o Options) error {
	abs, err := filepath.Abs(out)
	if err != nil {
		return err
	}
	switch lang {
	case Go:
		pkg := "."
		if st, err := os.Stat(filepath.Join(dir, "cmd")); err == nil && st.IsDir() {
			pkg = "./cmd"
		}
		return runIn(ctx, dir, nil, "go", "run", pkg, "generate", "-o", abs)
	case Python:
		main, err := withLine(dir, "*.py", `__name__\s*==\s*["']__main__["']`)
		if err != nil {
			return err
		}
		return runIn(ctx, dir, nil, "python3", filepath.Base(main), "generate", "-o", abs)
	case Java:
		main, err := withLine(dir, "*.java", `static\s+void\s+main\s*\(`)
		if err != nil {
			return err
		}
		srcs, _ := filepath.Glob(filepath.Join(dir, "*.java"))
		srcs = append(srcs, o.Sources...)
		cp := o.Classpath
		if len(cp) == 0 {
			cp = os.Getenv("CLASSPATH")
		}
		classes, err := os.MkdirTemp("", "pack-author-classes-")
		if err != nil {
			return err
		}
		defer os.RemoveAll(classes)
		args := []string{"--release", "21", "-d", classes}
		if len(cp) > 0 {
			args = append(args, "-cp", cp)
		}
		if err := runIn(ctx, dir, nil, "javac", append(args, srcs...)...); err != nil {
			return err
		}
		run := classes
		if len(cp) > 0 {
			run += string(os.PathListSeparator) + cp
		}
		class := strings.TrimSuffix(filepath.Base(main), ".java")
		return runIn(ctx, dir, nil, "java", "-cp", run, "-Dpack.dir="+dir, class, "generate", "-o", abs)
	}
	return fmt.Errorf("--lang %q: one of %s", lang, strings.Join(Langs, ", "))
}

func runIn(ctx context.Context, dir string, env []string, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	if env != nil {
		cmd.Env = append(os.Environ(), env...)
	}
	var said bytes.Buffer
	cmd.Stdout, cmd.Stderr = &said, &said
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(said.String())
		if len(msg) == 0 {
			msg = err.Error()
		}
		return &GenerateError{Command: name + " " + strings.Join(args, " "), Said: msg}
	}
	return nil
}

// GenerateError is a generator that failed: what it said — a compiler's file:line, the SDK's refusal.
type GenerateError struct {
	Command string
	Said    string
}

func (e *GenerateError) Error() string { return e.Said }

// withLine is the file of the glob in dir with a line matching re: the one the language starts the generator from.
func withLine(dir, glob, re string) (string, error) {
	files, _ := filepath.Glob(filepath.Join(dir, glob))
	slices.Sort(files)
	r := regexp.MustCompile(re)
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err == nil && r.Match(b) {
			return f, nil
		}
	}
	return "", fmt.Errorf("%s: no %s starts the generator (a line /%s/)", dir, glob, re)
}

// Where is a place of the source: file:line.
type Where struct {
	File string `json:"file"`
	Line int    `json:"line"`
}

func (w Where) String() string { return fmt.Sprintf("%s:%d", w.File, w.Line) }

// Locate is where in the source of dir the field of a pointer (/conf/max_connections) is written: the line that
// names its last segment as the language does — a json tag in Go, a field in Python, a json = or a field of its
// camel name in Java, the first of these that any line matches. Nothing when no line does, or several do.
func Locate(dir, lang, pointer string) (Where, bool) {
	segs := strings.Split(strings.Trim(pointer, "/"), "/")
	if len(segs) == 0 || len(segs[0]) == 0 {
		return Where{}, false
	}
	last := segs[len(segs)-1]
	q := regexp.QuoteMeta(last)
	var res []*regexp.Regexp
	var glob string
	switch lang {
	case Go:
		glob, res = "*.go", []*regexp.Regexp{regexp.MustCompile(`json:"` + q + `[,"]`)}
	case Python:
		glob, res = "*.py", []*regexp.Regexp{regexp.MustCompile(`^\s*` + q + `\s*:`)}
	case Java:
		glob, res = "*.java", []*regexp.Regexp{regexp.MustCompile(`json\s*=\s*"` + q + `"`),
			regexp.MustCompile(`^\s*public\s+[\w<>\[\], .]+\s+` + regexp.QuoteMeta(camel(last)) + `\s*[=;]`)}
	default:
		return Where{}, false
	}
	files, _ := filepath.Glob(filepath.Join(dir, glob))
	slices.Sort(files)
	found := make([][]Where, len(res)) // by pattern: the first one that matches says it
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		fh, err := os.Open(f)
		if err != nil {
			continue
		}
		sc := bufio.NewScanner(fh)
		for n := 1; sc.Scan(); n++ {
			for i, re := range res {
				if re.MatchString(sc.Text()) {
					found[i] = append(found[i], Where{File: f, Line: n})
					break
				}
			}
		}
		_ = fh.Close()
	}
	for _, ws := range found {
		if len(ws) > 0 {
			return ws[0], len(ws) == 1
		}
	}
	return Where{}, false
}

// camel is a json name as a Java field names it: max_connections → maxConnections.
func camel(s string) string {
	parts := strings.FieldsFunc(s, func(r rune) bool { return r == '_' || r == '-' })
	for i := 1; i < len(parts); i++ {
		if len(parts[i]) > 0 {
			parts[i] = strings.ToUpper(parts[i][:1]) + parts[i][1:]
		}
	}
	return strings.Join(parts, "")
}
