package git

import (
	"fmt"
	"os"
	"path/filepath"

	gossh "golang.org/x/crypto/ssh"

	"github.com/go-git/go-git/v6/plumbing/client"
	githttp "github.com/go-git/go-git/v6/plumbing/transport/http"
	"github.com/go-git/go-git/v6/plumbing/transport/ssh"
	"github.com/go-git/go-git/v6/plumbing/transport/ssh/knownhosts"
)

// Credentials is how the operator proves itself to a repository (К5): an HTTPS token, or an SSH key — with a
// passphrase or without — and the known_hosts that is always checked. A field empty is a way not used.
type Credentials struct {
	Username   string `json:"username,omitempty"`   // with the token
	Token      string `json:"token,omitempty"`      // HTTPS
	SSHKey     string `json:"ssh_key,omitempty"`    // the private key, PEM
	Passphrase string `json:"passphrase,omitempty"` // the key's
	KnownHosts string `json:"known_hosts,omitempty"`
}

// auth is the transport credentials of a Repo: a basic auth of the token, or the SSH key with a strict,
// always-checked known_hosts (written to a file of the repo's own, 0600, gone with Close). Nothing of this
// ever reaches a command line — there is none (К5).
func (c *Credentials) auth(tmp string) ([]client.Option, error) {
	if c == nil {
		return nil, nil
	}
	if len(c.Token) > 0 {
		user := c.Username
		if len(user) == 0 {
			user = "pack-operator"
		}
		return []client.Option{client.WithHTTPAuth(&githttp.BasicAuth{Username: user, Password: c.Token})}, nil
	}
	if len(c.SSHKey) > 0 {
		if len(c.KnownHosts) == 0 {
			return nil, fmt.Errorf("SSH credentials without known_hosts: the host key is always checked (К5)")
		}
		hosts := filepath.Join(tmp, "known_hosts")
		if err := os.WriteFile(hosts, []byte(c.KnownHosts), 0o600); err != nil {
			return nil, err
		}
		cb, err := knownhosts.New(hosts)
		if err != nil {
			return nil, fmt.Errorf("known_hosts: %w", err)
		}
		keys, err := ssh.NewPublicKeys("git", []byte(c.SSHKey), c.Passphrase)
		if err != nil {
			return nil, fmt.Errorf("the SSH key: %w", err)
		}
		keys.HostKeyCallback = gossh.HostKeyCallback(cb)
		return []client.Option{client.WithSSHAuth(keys)}, nil
	}
	return nil, nil
}
