// Package logging is the log of pack and of the targets it starts: zerolog, on stderr, at a level — trace, debug,
// info, warn (the default), error, off — in a format a person reads (console) or a program does (json).
//
// What a command says to a person (its text, --json, --stream) is not the log: the log is what is under it, for
// whoever looks into what pack does. A target logs JSON on its stderr; pack reads it (Writer) and logs each line
// again, the target named. Nothing secret is logged: no token, no value of a secret, no payload of a request.
package logging

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog"
)

// The environment of a process: the level and the format of its log — pack's own, and what pack gives a target.
const (
	EnvLevel  = "PACK_LOG_LEVEL"
	EnvFormat = "PACK_LOG_FORMAT"
)

// The formats.
const (
	FormatConsole = "console" // a line a person reads
	FormatJSON    = "json"    // a JSON object a line
)

// Levels are the levels, the least said last.
var Levels = []string{"trace", "debug", "info", "warn", "error", "off"}

// DefaultLevel is the level without one: what goes wrong, and nothing of what goes right.
const DefaultLevel = "warn"

// Config is how a process logs.
type Config struct {
	Level  string // one of Levels; "" — DefaultLevel
	Format string // console | json; "" — console
}

// FromEnv is the config the environment gives (PACK_LOG_LEVEL, PACK_LOG_FORMAT).
func FromEnv() Config { return Config{Level: os.Getenv(EnvLevel), Format: os.Getenv(EnvFormat)} }

// Env is the environment that gives a process this level, its log as JSON: what pack reads back of a target.
func (c Config) Env() []string {
	level := c.Level
	if len(level) == 0 {
		level = DefaultLevel
	}
	return []string{EnvLevel + "=" + level, EnvFormat + "=" + FormatJSON}
}

// ParseLevel is a level by its name.
func ParseLevel(s string) (zerolog.Level, error) {
	switch strings.ToLower(s) {
	case "":
		return zerolog.WarnLevel, nil
	case "trace":
		return zerolog.TraceLevel, nil
	case "debug":
		return zerolog.DebugLevel, nil
	case "info":
		return zerolog.InfoLevel, nil
	case "warn", "warning":
		return zerolog.WarnLevel, nil
	case "error":
		return zerolog.ErrorLevel, nil
	case "off", "none", "disabled":
		return zerolog.Disabled, nil
	}
	return zerolog.NoLevel, fmt.Errorf("log level %q: one of %s", s, strings.Join(Levels, ", "))
}

var (
	mu   sync.RWMutex
	root = zerolog.New(os.Stderr).Level(zerolog.WarnLevel).With().Timestamp().Logger()
	cfg  = Config{Level: DefaultLevel, Format: FormatConsole}
)

// Setup makes the log of the process: at c's level, in c's format, on w (nil — stderr).
func Setup(c Config, w io.Writer) error {
	level, err := ParseLevel(c.Level)
	if err != nil {
		return err
	}
	if w == nil {
		w = os.Stderr
	}
	switch c.Format {
	case "", FormatConsole:
		c.Format = FormatConsole
		w = zerolog.ConsoleWriter{Out: w, TimeFormat: "15:04:05.000", NoColor: !isTerminal(w)}
	case FormatJSON:
	default:
		return fmt.Errorf("log format %q: console or json", c.Format)
	}
	if len(c.Level) == 0 {
		c.Level = DefaultLevel
	}
	zerolog.TimeFieldFormat = time.RFC3339Nano
	mu.Lock()
	root, cfg = zerolog.New(w).Level(level).With().Timestamp().Logger(), c
	mu.Unlock()
	return nil
}

// Current is the config Setup made: what a target pack starts is given.
func Current() Config {
	mu.RLock()
	defer mu.RUnlock()
	return cfg
}

// For is the log of a part of the process: component=<name> on every event.
func For(component string) *zerolog.Logger {
	mu.RLock()
	l := root.With().Str("component", component).Logger()
	mu.RUnlock()
	return &l
}

// Enabled says whether the log says anything at level: what is costly to work out is worked out only then.
func Enabled(level zerolog.Level) bool {
	mu.RLock()
	defer mu.RUnlock()
	return root.GetLevel() <= level && level != zerolog.Disabled
}

func isTerminal(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	st, err := f.Stat()
	return err == nil && st.Mode()&os.ModeCharDevice != 0
}

// Writer is where a target's stdout and stderr go: each line it writes logged again under pack's log, the target
// named, at the level the line says — a JSON line of its log, its fields kept; a line that is not one (a panic, a
// program's own output) at warn, as it is. Close says what is left of a last line.
func Writer(target string) io.WriteCloser {
	return &relay{log: For("target").With().Str("target", target).Logger()}
}

// Split is where a process of pack's own writes its stderr (pack mcp's commands): a line of its log logged again,
// the process named (child=<name>), and the other lines — what the command says — written to said as they are; a
// line of the log at warn or above is said too.
func Split(child string, said io.Writer) io.WriteCloser {
	return &relay{log: For("child").With().Str("child", child).Logger(), said: said}
}

type relay struct {
	mu   sync.Mutex
	log  zerolog.Logger
	said io.Writer // where a line that is not of the log goes; nil — logged at warn
	rest []byte
}

func (r *relay) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.rest = append(r.rest, p...)
	for {
		i := bytes.IndexByte(r.rest, '\n')
		if i < 0 {
			break
		}
		r.line(r.rest[:i])
		r.rest = r.rest[i+1:]
	}
	return len(p), nil
}

func (r *relay) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.rest) > 0 {
		r.line(r.rest)
		r.rest = nil
	}
	return nil
}

func (r *relay) line(b []byte) {
	b = bytes.TrimRight(b, "\r")
	if len(bytes.TrimSpace(b)) == 0 {
		return
	}
	var m map[string]any
	if len(b) == 0 || b[0] != '{' || json.Unmarshal(b, &m) != nil || (r.said != nil && m[zerolog.LevelFieldName] == nil) {
		if r.said != nil {
			_, _ = r.said.Write(append(append([]byte(nil), b...), '\n'))
			return
		}
		r.log.Warn().Msg(string(b))
		return
	}
	level, err := zerolog.ParseLevel(fmt.Sprint(m[zerolog.LevelFieldName]))
	if err != nil || level == zerolog.NoLevel {
		level = zerolog.InfoLevel
	}
	msg, _ := m[zerolog.MessageFieldName].(string)
	for _, k := range []string{zerolog.LevelFieldName, zerolog.MessageFieldName, zerolog.TimestampFieldName} {
		delete(m, k)
	}
	if c, ok := m["component"]; ok {
		m["target_component"] = c // the target's part; component is pack's: target
		delete(m, "component")
	}
	r.log.WithLevel(level).Fields(m).Msg(msg)
	if r.said != nil && level >= zerolog.WarnLevel && level != zerolog.Disabled {
		// what went wrong is said too: the answer of pack mcp carries it, as it carried the stderr of before
		_, _ = fmt.Fprintf(r.said, "%s: %s\n", level, msg)
	}
}
