package cmd

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/horchestra/pack/pkg/api"
)

// streamProcess is pack run as a process of its own (this test binary): a command that fails ends the process, and
// what it wrote on stdout and stderr, and its exit code, are what a UI's backend gets.
func streamProcess(t *testing.T, dir string, args ...string) (stdout, stderr string, code int) {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestStreamHelper$")
	cmd.Env = append(os.Environ(), "PACK_STREAM_HELPER="+strings.Join(append([]string{"-C", dir}, args...), "\x1f"))
	var o, e bytes.Buffer
	cmd.Stdout, cmd.Stderr = &o, &e
	err := cmd.Run()
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		code = exit.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	return o.String(), e.String(), code
}

// TestStreamHelper is the process streamProcess starts; on its own it does nothing.
func TestStreamHelper(t *testing.T) {
	args := os.Getenv("PACK_STREAM_HELPER")
	if len(args) == 0 {
		t.Skip("a helper process")
	}
	rootCmd.SetArgs(strings.Split(args, "\x1f"))
	Execute()
	os.Exit(0)
}

// events reads NDJSON: every line an event, seq 1, 2, … with no gap.
func events(t *testing.T, out string) []api.Event {
	t.Helper()
	var evs []api.Event
	sc := bufio.NewScanner(strings.NewReader(out))
	sc.Buffer(nil, 1<<24)
	for sc.Scan() {
		var e api.Event
		if err := json.Unmarshal(sc.Bytes(), &e); err != nil {
			t.Fatalf("not an event: %v\n%s", err, sc.Text())
		}
		if e.Seq != int64(len(evs)+1) || e.Time.IsZero() || len(e.Type) == 0 {
			t.Fatalf("event %d: %s", len(evs)+1, sc.Text())
		}
		evs = append(evs, e)
	}
	if len(evs) == 0 {
		t.Fatalf("no events:\n%s", out)
	}
	return evs
}

// said is the events as "type key status", what the order of a saga is read by.
func said(evs []api.Event) []string {
	var out []string
	for _, e := range evs {
		if e.Type == api.EventLog {
			continue
		}
		out = append(out, strings.Join(strings.Fields(e.Type+" "+e.Key+" "+e.Status), " "))
	}
	return out
}

// An apply that fails, as a stream: the plan, the keys as they run, the failure, the rollback to the savepoint, the
// result last — on stdout only events, the text on stderr; pack log --stream gives the same saga back.
func TestStreamApplyThatFails(t *testing.T) {
	// one key at a time: the order of the events is the plan's
	d := releaseOf(t, generated(t), map[string]any{"fail_at": "openbao/apply", "fail_error": "bind: address already in use",
		"run": map[string]any{"jobs": 1}})
	out, errOut, code := streamProcess(t, d, "apply", "--stream")
	if code != 1 {
		t.Fatalf("exit %d\nstdout:\n%s\nstderr:\n%s", code, out, errOut)
	}
	evs := events(t, out)
	got := said(evs)
	want := []string{"plan", "saga started", "node postgres running", "node postgres done",
		"node savepoint:postgres running", "node savepoint:postgres done", "node openbao running",
		"node openbao failed", "saga failed", "node openbao compensating", "node openbao rolled-back", "result failed"}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("events:\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if p := evs[0].Plan; p == nil || p.Direction != "install" || len(p.Steps) == 0 {
		t.Errorf("the plan: %+v", evs[0])
	}
	last := evs[len(evs)-1]
	if last.Code != api.CodeSagaFailed || !strings.Contains(last.Error, "address already in use") || len(last.Next) == 0 {
		t.Errorf("the result: %+v", last)
	}
	var stepped bool
	for _, e := range evs {
		if e.Type == api.EventLog && e.Key == "openbao" && len(e.Step) > 0 {
			stepped = true
		}
		if e.Type != api.EventPlan && e.Type != api.EventResult && e.Type != api.EventSaga && len(e.Saga) == 0 && e.Type != api.EventLog {
			t.Errorf("an event of no saga: %+v", e)
		}
	}
	if !stepped {
		t.Errorf("no line of openbao says its step:\n%s", out)
	}
	for _, line := range []string{"apply postgres", "apply openbao", "failed: ", "undo openbao", "pack: "} {
		if !strings.Contains(errOut, line) {
			t.Errorf("stderr has no %q:\n%s", line, errOut)
		}
	}

	// the journal of the same saga, the same events: what a UI shows again after it lost the stream
	logOut, logErr, code := streamProcess(t, d, "log", "--stream")
	if code != 0 {
		t.Fatalf("log --stream: exit %d\n%s", code, logErr)
	}
	back := events(t, logOut)
	if strings.Join(said(back), "\n") != strings.Join(append(slicesWithout(want, "plan", "result failed"), "result ok"), "\n") {
		t.Errorf("log --stream:\n%s", strings.Join(said(back), "\n"))
	}
	if back[1].Saga != evs[1].Saga || len(back[1].Saga) == 0 {
		t.Errorf("another saga: %s, %s", back[1].Saga, evs[1].Saga)
	}
}

// --stream and --json are two ways of the same stdout: not both.
func TestStreamNotWithJSON(t *testing.T) {
	d := releaseOf(t, generated(t), nil)
	out, errOut, code := streamProcess(t, d, "plan", "--stream", "--json")
	if code != 1 || !strings.Contains(errOut, "not both") {
		t.Errorf("exit %d\n%s\n%s", code, out, errOut)
	}
}

func slicesWithout(xs []string, drop ...string) []string {
	var out []string
	for _, x := range xs {
		keep := true
		for _, d := range drop {
			keep = keep && x != d
		}
		if keep {
			out = append(out, x)
		}
	}
	return out
}

// A plan as a stream: the plan, and the result with it; the next command of the process streams nothing.
func TestStreamPlan(t *testing.T) {
	d := releaseOf(t, generated(t), nil)
	evs := events(t, pack(t, d, "plan", "--stream"))
	if len(evs) != 2 || evs[0].Type != api.EventPlan || evs[0].Plan == nil || evs[1].Type != api.EventResult || evs[1].Status != api.StatusOK || evs[1].Result == nil {
		t.Errorf("plan --stream: %+v", evs)
	}
	if out := pack(t, d, "plan"); strings.HasPrefix(out, "{") {
		t.Errorf("a plan after the stream streams:\n%s", out)
	}
}

// A removal and a taking back as streams: the plan first, the keys as they go, the result last.
func TestStreamDeleteAndRollback(t *testing.T) {
	d := releaseOf(t, generated(t), map[string]any{"run": map[string]any{"jobs": 1}})
	pack(t, d, "apply")
	write(t, d, "values.yaml", map[string]any{"openbao": map[string]any{"api_addr": "https://bao:8201"}, "postgres": map[string]any{"id": "n1"}})
	pack(t, d, "apply")

	back := events(t, pack(t, d, "rollback", "--stream"))
	got := said(back)
	if got[0] != "plan" || got[len(got)-1] != "result ok" || !slices.Contains(got, "node openbao done") {
		t.Errorf("rollback --stream:\n%s", strings.Join(got, "\n"))
	}

	gone := events(t, pack(t, d, "delete", "--stream"))
	got = said(gone)
	// a removal's graph is the keys': no savepoint is taken away
	want := []string{"plan", "saga started", "node openbao removing", "node openbao removed", "node postgres removing",
		"node postgres removed", "saga ended", "result ok"}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("delete --stream:\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	for _, e := range gone {
		if e.Type == api.EventNode && len(e.Step) > 0 {
			t.Errorf("a node's event with a step: %+v", e)
		}
	}
}

// An interrupt of a streaming apply: said as it comes, of the saga, then the step cut short, the rollback, the result.
func TestStreamInterrupt(t *testing.T) {
	d := releaseOf(t, generated(t), map[string]any{"hold_at": "openbao/apply", "run": map[string]any{"jobs": 1}})
	cmd := exec.Command(os.Args[0], "-test.run=^TestStreamHelper$")
	cmd.Env = append(os.Environ(), "PACK_STREAM_HELPER="+strings.Join([]string{"-C", d, "apply", "--stream"}, "\x1f"))
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var errOut bytes.Buffer
	cmd.Stderr = &errOut
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	var all strings.Builder
	sc := bufio.NewScanner(out)
	for sc.Scan() {
		all.WriteString(sc.Text() + "\n")
		if strings.Contains(sc.Text(), "held until cut short") {
			if err := cmd.Process.Signal(os.Interrupt); err != nil {
				t.Fatal(err)
			}
		}
	}
	_ = cmd.Wait()
	evs := events(t, all.String())
	got := said(evs)
	i := slices.Index(got, "saga interrupted")
	if i < 0 || !slices.Equal(got[i:], []string{"saga interrupted", "node openbao failed", "saga failed", "node openbao compensating",
		"node openbao rolled-back", "result failed"}) {
		t.Fatalf("events:\n%s\nstderr:\n%s", strings.Join(got, "\n"), errOut.String())
	}
	var interrupted api.Event
	for _, e := range evs {
		if e.Status == api.StatusInterrupted {
			interrupted = e
		}
	}
	if len(interrupted.Saga) == 0 || interrupted.Saga != evs[1].Saga {
		t.Errorf("the interrupt is of no saga: %+v", interrupted)
	}
}

// A reader that goes away does not end pack in the middle of a saga: the apply goes on to its end, and says so on
// stderr.
func TestStreamReaderGone(t *testing.T) {
	d := releaseOf(t, generated(t), nil)
	cmd := exec.Command(os.Args[0], "-test.run=^TestStreamHelper$")
	cmd.Env = append(os.Environ(), "PACK_STREAM_HELPER="+strings.Join([]string{"-C", d, "apply", "--stream"}, "\x1f"))
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	_ = r.Close() // gone before the first event
	cmd.Stdout = w
	var errOut bytes.Buffer
	cmd.Stderr = &errOut
	err = cmd.Run()
	_ = w.Close()
	if err != nil {
		t.Fatalf("%v\n%s", err, errOut.String())
	}
	if !strings.Contains(errOut.String(), "the events stop, the command goes on") || !strings.Contains(errOut.String(), "installed ") {
		t.Errorf("stderr:\n%s", errOut.String())
	}
	if out := pack(t, d, "history"); !strings.Contains(out, "*") {
		t.Errorf("nothing installed:\n%s", out)
	}
}

// The log is on stderr at the level asked, as JSON when asked; stdout is the command's alone; a level of none is
// refused.
func TestLogFlags(t *testing.T) {
	d := releaseOf(t, generated(t), nil)
	out, errOut, code := streamProcess(t, d, "plan", "--json", "--log-level", "trace", "--log-format", "json")
	if code != 0 {
		t.Fatalf("exit %d\n%s", code, errOut)
	}
	var p map[string]any
	if err := json.Unmarshal([]byte(out), &p); err != nil || p["direction"] != "install" {
		t.Errorf("stdout is not the plan alone: %v\n%s", err, out)
	}
	for _, want := range []string{`"component":"cmd"`, `"message":"start"`, `"component":"session"`, `"component":"cel"`, `"message":"done"`} {
		if !strings.Contains(errOut, want) {
			t.Errorf("stderr has no %s:\n%s", want, errOut)
		}
	}
	// the default: nothing of what goes right
	_, errOut, _ = streamProcess(t, d, "plan")
	if strings.Contains(errOut, `"level"`) || strings.Contains(errOut, "DBG") {
		t.Errorf("a log at the default level:\n%s", errOut)
	}
	_, errOut, code = streamProcess(t, d, "plan", "--log-level", "loud")
	if code != 1 || !strings.Contains(errOut, `log level "loud"`) {
		t.Errorf("a level of none: %d\n%s", code, errOut)
	}
}

// --stream of the commands it was added to: stdout is events alone, the result the last of them.
func TestStreamEveryCommand(t *testing.T) {
	d := releaseOf(t, generated(t), nil)

	out, errOut, code := streamProcess(t, d, "refresh", "--stream")
	if code != 0 {
		t.Fatalf("refresh: %d\n%s", code, errOut)
	}
	if evs := said(events(t, out)); evs[len(evs)-1] != "result ok" {
		t.Errorf("refresh --stream:\n%s", strings.Join(evs, "\n"))
	}

	out, errOut, code = streamProcess(t, d, "discover", "--stream")
	if code != 0 {
		t.Fatalf("discover: %d\n%s", code, errOut)
	}
	if evs := said(events(t, out)); evs[len(evs)-1] != "result ok" {
		t.Errorf("discover --stream:\n%s", strings.Join(evs, "\n"))
	}

	pack(t, d, "apply")
	runParams = nil
	out, errOut, code = streamProcess(t, d, "run", "backup", "--param", "to=/backups/one", "--stream")
	if code != 0 {
		t.Fatalf("run: %d\n%s", code, errOut)
	}
	evs := events(t, out)
	got := said(evs)
	want := []string{"saga started", "node postgres running", "node postgres done", "saga ended", "result ok"}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("run --stream:\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	var logged bool
	for _, e := range evs {
		if e.Type == api.EventLog && e.Key == "postgres" && len(e.Saga) > 0 {
			logged = true
		}
	}
	if !logged {
		t.Errorf("no line of the action:\n%s", out)
	}

	// test --release: the scenarios on the release's target
	write(t, d, "release.lock.json", map[string]any{})
	sc := filepath.Join(t.TempDir(), "ok.yaml")
	scenario := "name: installs\nvalues:\n  openbao:\n    api_addr: https://bao:8200\n  postgres:\n    id: n1\nexpect:\n  apply:\n    state: installed\n    in_effect: [postgres, openbao]\n"
	if err := os.WriteFile(sc, []byte(scenario), 0o644); err != nil {
		t.Fatal(err)
	}
	out, errOut, code = streamProcess(t, d, "test", "--release", d, sc, "--stream")
	testRelease = "" // a flag keeps its value between runs in one process
	if code != 0 {
		t.Fatalf("test --release: %d\n%s", code, errOut)
	}
	if evs := said(events(t, out)); evs[len(evs)-1] != "result ok" {
		t.Errorf("test --release --stream:\n%s", strings.Join(evs, "\n"))
	}
	if strings.Contains(out, "scenario(s) ok") && !strings.Contains(errOut, "scenario(s) ok") {
		t.Errorf("the text is not on stderr:\n%s", errOut)
	}
}
