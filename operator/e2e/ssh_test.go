// Git over SSH against a real sshd (a container the test builds): the operator clones and pushes with a key,
// with a passphrase-protected key, and is refused when the host key is not the one of its known_hosts.
package e2e

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/docker/docker/pkg/stdcopy"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
	"golang.org/x/crypto/ssh"

	pgit "github.com/horchestra/pack/operator/git"
)

// sshd is a container with an sshd and a bare repository /home/git/repo.git of one commit on master.
// It returns the ssh URL, the known_hosts line of the container's host key, and a write of authorized_keys.
func sshd(t *testing.T) (url string, knownHosts string, authorize func(pub string)) {
	t.Helper()
	ctx := context.Background()
	c, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			FromDockerfile: testcontainers.FromDockerfile{
				Context: "testdata/sshd",
				Repo:    "pack-operator-e2e-sshd",
				Tag:     "test",
			},
			ExposedPorts: []string{"22/tcp"},
			WaitingFor:   wait.ForListeningPort("22/tcp"),
		},
		Started: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Terminate(context.Background()) })
	host, err := c.Host(ctx)
	if err != nil {
		t.Fatal(err)
	}
	port, err := c.MappedPort(ctx, "22")
	if err != nil {
		t.Fatal(err)
	}
	exec := func(cmd string) string {
		t.Helper()
		code, rd, err := c.Exec(ctx, []string{"sh", "-c", cmd})
		if err != nil {
			t.Fatal(err)
		}
		var out bytes.Buffer // the stream is multiplexed: stdcopy strips the headers
		if _, err := stdcopy.StdCopy(&out, io.Discard, rd); err != nil {
			t.Fatal(err)
		}
		if code != 0 {
			t.Fatalf("%s: %d %s", cmd, code, out.String())
		}
		return out.String()
	}
	// a bare repository of one commit on master
	exec("git init -q /tmp/seed" +
		" && git -C /tmp/seed -c user.email=t@t -c user.name=t commit -q --allow-empty -m one" +
		" && git init -q --bare /home/git/repo.git" +
		" && git -C /home/git/repo.git fetch -q /tmp/seed refs/heads/master:refs/heads/master" +
		" && git -C /home/git/repo.git symbolic-ref HEAD refs/heads/master" +
		" && chown -R git:git /home/git")
	// every host key of the sshd: which one the handshake picks is the server's choice
	var lines []string
	for _, name := range []string{"ssh_host_ed25519_key.pub", "ssh_host_ecdsa_key.pub", "ssh_host_rsa_key.pub"} {
		fields := strings.Fields(exec("cat /etc/ssh/" + name))
		if len(fields) < 2 {
			t.Fatalf("the host key %s: %q", name, fields)
		}
		lines = append(lines, fmt.Sprintf("[%s]:%s %s %s", host, port.Port(), fields[0], fields[1]))
	}
	knownHosts = strings.Join(lines, "\n") + "\n"
	return fmt.Sprintf("ssh://git@%s:%s/home/git/repo.git", host, port.Port()), knownHosts,
		func(pub string) {
			exec("printf '%s\n' '" + pub + "' >> /home/git/.ssh/authorized_keys" +
				" && chown git:git /home/git/.ssh/authorized_keys && chmod 600 /home/git/.ssh/authorized_keys")
		}
}

// keypair is an ed25519 key: the private key PEM (the passphrase encrypts it when given), the public key.
func keypair(t *testing.T, passphrase string) (privateKey, publicKey string) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	spub, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	var block *pem.Block
	if len(passphrase) > 0 {
		block, err = ssh.MarshalPrivateKeyWithPassphrase(priv, "", []byte(passphrase))
	} else {
		block, err = ssh.MarshalPrivateKey(priv, "")
	}
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(block)), strings.TrimSpace(string(ssh.MarshalAuthorizedKey(spub)))
}

func TestSSH(t *testing.T) {
	ctx := context.Background()
	url, knownHosts, authorize := sshd(t)
	plain, plainPub := keypair(t, "")
	guarded, guardedPub := keypair(t, "the passphrase")
	authorize(plainPub)
	authorize(guardedPub)

	clone := func(creds *pgit.Credentials) *pgit.Repo {
		t.Helper()
		r, err := pgit.Clone(ctx, url, filepath.Join(t.TempDir(), "work"), creds)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { r.Close() })
		return r
	}

	// a key without a passphrase clones, commits, pushes
	r := clone(&pgit.Credentials{SSHKey: plain, KnownHosts: knownHosts})
	if head, err := r.HEAD(ctx); err != nil || len(head) != 40 {
		t.Fatalf("HEAD: %q, %v", head, err)
	}
	if err := os.WriteFile(filepath.Join(r.Dir, "from-operator"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Commit(ctx, r.Dir, "pack-operator <pack-operator@example.org>", "over ssh", nil); err != nil {
		t.Fatal(err)
	}
	if err := r.Push(ctx, "refs/heads/master:refs/heads/master"); err != nil {
		t.Fatalf("the push: %v", err)
	}

	// a passphrase-protected key clones too
	g := clone(&pgit.Credentials{SSHKey: guarded, Passphrase: "the passphrase", KnownHosts: knownHosts})
	if log, err := g.Log(ctx, "refs/remotes/origin/master", 0); err != nil || len(log) != 2 {
		t.Errorf("the log after the push: %v, %v", log, err)
	}

	// a host key that is not the known_hosts one refuses
	_, strangerPub := keypair(t, "")
	wrong := fmt.Sprintf("%s %s %s", strings.Fields(knownHosts)[0],
		strings.Fields(strangerPub)[0], strings.Fields(strangerPub)[1])
	if cl, err := pgit.Clone(ctx, url, filepath.Join(t.TempDir(), "spy"),
		&pgit.Credentials{SSHKey: plain, KnownHosts: wrong}); err == nil {
		cl.Close()
		t.Error("an unknown host key cloned")
	}
}
