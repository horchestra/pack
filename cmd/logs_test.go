package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// pack logs of a memory target whose settings name a file of logs: by key, instance and tail, as text and as --json.
func TestLogs(t *testing.T) {
	logs := filepath.Join(t.TempDir(), "logs.json")
	if err := os.WriteFile(logs, []byte(`{
		"postgres": {"postgres-0": ["LOG:  starting PostgreSQL", "LOG:  ready"], "postgres-1": ["LOG:  replica"]},
		"openbao": {"openbao-0": ["==> OpenBao server started"]}
	}`), 0o644); err != nil {
		t.Fatal(err)
	}
	d := releaseOf(t, generated(t), map[string]any{"logs": logs})
	pack(t, d, "apply")
	t.Cleanup(func() { logsKey, logsInstance, logsTail, jsonOut = "", "", 50, false })

	out := pack(t, d, "logs", "--key", "postgres", "--instance", "postgres-0", "--tail", "1")
	if out != "postgres/postgres-0:\n  LOG:  ready\n" {
		t.Errorf("a key, an instance, a tail:\n%s", out)
	}
	logsKey, logsInstance, logsTail = "", "", 50

	out = pack(t, d, "logs", "--key", "postgres", "--json")
	jsonOut = false
	var doc logsDoc
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("logs --json: %v\n%s", err, out)
	}
	if len(doc.Logs) != 1 || strings.Join(doc.Logs["postgres"]["postgres-0"], " ") != "LOG:  starting PostgreSQL LOG:  ready" ||
		strings.Join(doc.Logs["postgres"]["postgres-1"], " ") != "LOG:  replica" {
		t.Errorf("the logs of the key: %+v", doc)
	}

	// the keys of the installation, each target asked: openbao's logs too, as text
	logsKey = "" // a flag of cobra's outlives a call in this process
	out = pack(t, d, "logs")
	if !strings.Contains(out, "postgres/postgres-1:\n  LOG:  replica\n") || !strings.Contains(out, "openbao/openbao-0:\n  ==> OpenBao server started\n") {
		t.Errorf("every key:\n%s", out)
	}
}
