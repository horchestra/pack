package logging

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/rs/zerolog"
)

// syncBuffer is a buffer a log writes to from several goroutines.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// lines are the JSON events written, one a line.
func lines(t *testing.T, s string) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, l := range strings.Split(strings.TrimSpace(s), "\n") {
		if len(l) == 0 {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(l), &m); err != nil {
			t.Fatalf("not JSON: %q", l)
		}
		out = append(out, m)
	}
	return out
}

func setup(t *testing.T, level string) *syncBuffer {
	t.Helper()
	var b syncBuffer
	if err := Setup(Config{Level: level, Format: FormatJSON}, &b); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = Setup(Config{}, nil) })
	return &b
}

func TestLevels(t *testing.T) {
	for _, l := range Levels {
		if _, err := ParseLevel(l); err != nil {
			t.Errorf("%s: %v", l, err)
		}
	}
	if _, err := ParseLevel("loud"); err == nil {
		t.Error("a level of none")
	}
	if err := Setup(Config{Format: "xml"}, nil); err == nil {
		t.Error("a format of none")
	}
	// the default says what goes wrong, and nothing of what goes right
	b := setup(t, "")
	For("x").Info().Msg("fine")
	For("x").Warn().Msg("odd")
	if got := lines(t, b.String()); len(got) != 1 || got[0]["message"] != "odd" || got[0]["component"] != "x" {
		t.Errorf("warn: %v", got)
	}
	b = setup(t, "trace")
	For("x").Trace().Msg("deep")
	if got := lines(t, b.String()); len(got) != 1 || got[0]["level"] != "trace" || !Enabled(zerolog.TraceLevel) {
		t.Errorf("trace: %v", got)
	}
	b = setup(t, "off")
	For("x").Error().Msg("nothing")
	if len(b.String()) > 0 || Enabled(zerolog.ErrorLevel) {
		t.Errorf("off: %s", b.String())
	}
	if env := (Config{Level: "debug"}).Env(); strings.Join(env, " ") != "PACK_LOG_LEVEL=debug PACK_LOG_FORMAT=json" {
		t.Errorf("env: %v", env)
	}
	if env := (Config{}).Env(); env[0] != "PACK_LOG_LEVEL=warn" {
		t.Errorf("env: %v", env)
	}
}

func TestConsole(t *testing.T) {
	var b syncBuffer
	if err := Setup(Config{Level: "info"}, &b); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = Setup(Config{}, nil) })
	For("cmd").Info().Str("k", "v").Msg("hello")
	if s := b.String(); !strings.Contains(s, "INF") || !strings.Contains(s, "hello") || !strings.Contains(s, "k=v") || strings.Contains(s, "\x1b[") {
		t.Errorf("console: %q", s)
	}
}

// A target's lines come back under pack's log: the level and the fields of a JSON line kept, the target named,
// another line at warn as it is, a last line without its newline at Close.
func TestWriter(t *testing.T) {
	b := setup(t, "debug")
	w := Writer("docker")
	_, _ = w.Write([]byte(`{"level":"debug","component":"rpc","method":"/x","time":"2026-11-04T10:00:00Z","message":"call"}` + "\n" + "panic: boom\n{\"level\":\"info\",\"mess"))
	_, _ = w.Write([]byte("age\":\"split\"}\n" + `{"level":"trace","message":"too deep"}` + "\nno newline"))
	_ = w.Close()
	got := lines(t, b.String())
	if len(got) != 4 {
		t.Fatalf("%d lines:\n%s", len(got), b.String())
	}
	if got[0]["level"] != "debug" || got[0]["target"] != "docker" || got[0]["method"] != "/x" || got[0]["target_component"] != "rpc" || got[0]["component"] != "target" || got[0]["message"] != "call" {
		t.Errorf("a log line: %v", got[0])
	}
	if got[1]["level"] != "warn" || got[1]["message"] != "panic: boom" {
		t.Errorf("a line of no log: %v", got[1])
	}
	if got[2]["message"] != "split" || got[3]["message"] != "no newline" {
		t.Errorf("lines across writes: %v", got[2:])
	}
}

// pack's own command: its log logged again, what it says kept for the answer.
func TestSplit(t *testing.T) {
	b := setup(t, "debug")
	var said bytes.Buffer
	w := Split("pack plan", &said)
	_, _ = w.Write([]byte(`{"level":"debug","message":"start"}` + "\napply openbao\n" + `{"not":"a log"}` + "\n" + `{"level":"warn","message":"panic: boom"}` + "\n"))
	_ = w.Close()
	if said.String() != "apply openbao\n{\"not\":\"a log\"}\nwarn: panic: boom\n" {
		t.Errorf("said: %q", said.String())
	}
	if got := lines(t, b.String()); len(got) != 2 || got[0]["child"] != "pack plan" || got[0]["message"] != "start" {
		t.Errorf("log: %v", got)
	}
}

// A request at trace: no query, no user — where a token may be.
func TestTransport(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusTeapot) }))
	defer srv.Close()
	b := setup(t, "trace")
	c := &http.Client{Transport: Transport("store", nil)}
	resp, err := c.Get(strings.Replace(srv.URL, "http://", "http://user:secret@", 1) + "/v1/kv?token=secret")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	s := b.String()
	if strings.Contains(s, "secret") || !strings.Contains(s, `"status":418`) || !strings.Contains(s, "/v1/kv") {
		t.Errorf("log: %s", s)
	}
}
