package author

import (
	"path/filepath"
	"strings"
	"testing"
)

// A field of the schema is found in the source of each language: a json tag, a field, a json = or a camel field.
func TestLocate(t *testing.T) {
	ex := filepath.Join("..", "..", "examples", "postgres")
	for _, c := range []struct {
		lang, dir, ptr, file string
	}{
		{Go, "go", "/conf/max_connections", "postgres.go"},
		{Python, "python", "/conf/max_connections", "postgres.py"},
		{Java, "java", "/conf/max_connections", "Postgres.java"},
		{Java, "java", "/conf/port", "Postgres.java"},
		{Python, "python", "/superuser/password", "postgres.py"},
	} {
		w, ok := Locate(filepath.Join(ex, c.dir), c.lang, c.ptr)
		if !ok || filepath.Base(w.File) != c.file || w.Line == 0 {
			t.Errorf("%s %s: %v %v", c.lang, c.ptr, w, ok)
		}
	}
	if w, ok := Locate(filepath.Join(ex, "go"), Go, "/nope"); ok {
		t.Errorf("a field nobody writes: %v", w)
	}
	if lang, err := Detect(filepath.Join(ex, "java")); err != nil || lang != Java {
		t.Errorf("detect: %s %v", lang, err)
	}
	if _, err := Detect(t.TempDir()); err == nil || !strings.Contains(err.Error(), "no .go, .py or .java") {
		t.Errorf("detect nothing: %v", err)
	}
}
