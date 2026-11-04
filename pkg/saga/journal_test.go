package saga

import (
	"strings"
	"testing"
	"time"
)

// What a person reads of an entry as the saga goes, and what pack log writes of it: the verb of an undo is its
// step, not a message after it.
func TestEntryLine(t *testing.T) {
	for _, c := range []struct {
		e    Entry
		line string
	}{
		{Entry{Kind: KindStart, Node: "db"}, "apply db"},
		{Entry{Kind: KindUndo, Node: "db", Step: "undo"}, "undo db"},
		{Entry{Kind: KindUndo, Node: "db", Step: "remove"}, "remove db"},
		{Entry{Kind: KindLog, Node: "db", Step: "apply", Message: "up"}, "db: up"},
		{Entry{Kind: KindLog, Message: "undo cut-short db"}, "undo cut-short db"},
		{Entry{Kind: KindFail, Message: "saga failed", Error: "db: boom"}, "failed: db: boom"},
		{Entry{Kind: KindFail, Node: "db", Error: "boom"}, ""},
		{Entry{Kind: KindDone, Node: "db"}, ""},
		{Entry{Kind: KindUndone, Node: "db", Step: "undo"}, ""},
		{Entry{Kind: KindSaga, Message: "towards p 1"}, ""},
		{Entry{Kind: KindEnd, Message: "installed p 1"}, ""},
	} {
		line, ok := c.e.Line()
		if line != c.line || ok != (len(c.line) > 0) {
			t.Errorf("%+v: %q %v, want %q", c.e, line, ok, c.line)
		}
	}
	at := time.Date(2026, 11, 4, 10, 0, 0, 0, time.UTC)
	if s := (Entry{Time: at, Kind: KindUndo, Node: "db", Step: "undo"}).String(); !strings.HasSuffix(s, " undo db") {
		t.Errorf("pack log of an undo: %q", s)
	}
}
