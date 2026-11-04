package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The image of pack (the Dockerfile): pack version runs in it, and an apply on the memory target runs in it with
// the release's checkout mounted — the way an operator's runtime job runs it.
func TestPackInAContainer(t *testing.T) {
	repo, err := filepath.Abs("../..") // the repository root, the Dockerfile's context
	if err != nil {
		t.Fatal(err)
	}
	docker(t, "build", "-t", "pack:test", repo)
	out := docker(t, "run", "--rm", "pack:test", "version")
	if !strings.Contains(out, "pack") {
		t.Fatalf("version in the container:\n%s", out)
	}

	work := t.TempDir()
	if err := os.Chmod(work, 0o777); err != nil { // the container's user writes the state here
		t.Fatal(err)
	}
	write(t, filepath.Join(work, "release.yaml"), "pack: /src/examples/openbao/golden/openbao\n")
	write(t, filepath.Join(work, "env.yaml"), `---
target: memory
store:
  provider: file
  path: /work/secrets # the password pack makes is kept here
keys:
  postgres:
    resources:
      requests:
        cpu: 500m
        memory: 512Mi
`)
	write(t, filepath.Join(work, "values.yaml"), "openbao: {}\npostgres:\n  database: bao\n")
	out = docker(t, "run", "--rm",
		"-v", repo+":/src:ro", "-v", work+":/work", "-w", "/work",
		"pack:test", "apply")
	if !strings.Contains(out, "installed openbao") || !strings.Contains(out, "on memory") {
		t.Fatalf("apply in the container:\n%s", out)
	}
	if _, err := os.Stat(filepath.Join(work, ".pack", "state.json")); err != nil {
		t.Fatalf("the state is the mounted checkout's: %v\n%s", err, out)
	}
}
