package git

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func have(tool string) bool {
	_, err := exec.LookPath(tool)
	return err == nil
}

// A commit signed with the project's key verifies; signed with a foreign key — or not signed — it does not.
func TestVerifySSH(t *testing.T) {
	if !have("ssh-keygen") || !have("git") {
		t.Skip("ssh-keygen, git")
	}
	ctx := context.Background()
	dir := filepath.Join(t.TempDir(), "repo")
	keys := t.TempDir()
	gen := func(name string) string {
		if out, err := exec.Command("ssh-keygen", "-t", "ed25519", "-N", "", "-f", filepath.Join(keys, name)).CombinedOutput(); err != nil {
			t.Fatalf("ssh-keygen: %s", out)
		}
		pub, err := os.ReadFile(filepath.Join(keys, name+".pub"))
		if err != nil {
			t.Fatal(err)
		}
		return strings.TrimSpace(string(pub))
	}
	own, foreign := gen("own"), gen("foreign")
	init := [][]string{
		{"init", "--quiet", dir},
		{"-C", dir, "config", "user.email", "op@example.org"},
		{"-C", dir, "config", "user.name", "op"},
		{"-C", dir, "config", "gpg.format", "ssh"},
		{"-C", dir, "config", "user.signingkey", filepath.Join(keys, "own")},
	}
	for _, args := range init {
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %s", args, out)
		}
	}
	commit := func(message string, sign bool) string {
		if err := os.WriteFile(filepath.Join(dir, "f"), []byte(message), 0o644); err != nil {
			t.Fatal(err)
		}
		if out, err := exec.Command("git", "-C", dir, "add", "f").CombinedOutput(); err != nil {
			t.Fatalf("add: %s", out)
		}
		args := []string{"-C", dir, "commit", "--quiet", "-m", message}
		if sign {
			args = append(args, "-S")
		} else {
			args = append(args, "--no-gpg-sign")
		}
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("commit: %s", out)
		}
		sha, err := exec.Command("git", "-C", dir, "rev-parse", "HEAD").Output()
		if err != nil {
			t.Fatal(err)
		}
		return strings.TrimSpace(string(sha))
	}
	signed := commit("signed", true)
	unsigned := commit("unsigned", false)
	// foreign: the repo's key replaced for one commit
	if out, err := exec.Command("git", "-C", dir, "-c", "user.signingkey="+filepath.Join(keys, "foreign"),
		"commit", "--quiet", "--allow-empty", "-S", "-m", "foreign").CombinedOutput(); err != nil {
		t.Fatalf("foreign commit: %s", out)
	}
	foreignSHA, _ := exec.Command("git", "-C", dir, "rev-parse", "HEAD").Output()
	foreignSign := strings.TrimSpace(string(foreignSHA))

	ok, err := VerifyCommit(ctx, dir, signed, []string{own + " alice"})
	if err != nil || !ok {
		t.Errorf("its own key: %v, %v", ok, err)
	}
	for name, sha := range map[string]string{"a foreign key": foreignSign, "unsigned": unsigned} {
		if ok, err := VerifyCommit(ctx, dir, sha, []string{own + " alice"}); err != nil || ok {
			t.Errorf("%s: %v, %v — must not verify", name, ok, err)
		}
	}
	// and it verifies against the foreign key it was signed with
	if ok, err := VerifyCommit(ctx, dir, foreignSign, []string{foreign + " mallory"}); err != nil || !ok {
		t.Errorf("the foreign key against its own signature: %v, %v", ok, err)
	}
}
