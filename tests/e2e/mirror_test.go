package e2e

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
)

const (
	mirrorEnv = `---
target: main
targets:
  main:
    target: host
    state: ${STATE}
    mirror_registry: ${MIRROR_REGISTRY}
    mirror_plain_http: true
${HOSTS}`
	mirrorValues = `---
release_url: http://127.0.0.1:8081/mirrors/openbao:2.6.5
openbao:
  disable_mlock: true
  api_addr: http://127.0.0.1:8200
  storage:
    file:
      path: /opt/openbao/data
  listener:
    tcp:
      address: 0.0.0.0:8200
      tls_disable: true
`
)

// download is a release's package file, for the architecture the hosts run here.
func download(t *testing.T, name string) []byte {
	t.Helper()
	cache, err := os.UserCacheDir()
	if err != nil {
		t.Fatal(err)
	}
	kept := filepath.Join(cache, "pack-e2e", "openbao-2.6.5", name)
	if b, err := os.ReadFile(kept); err == nil {
		return b
	}
	resp, err := http.Get("https://github.com/openbao/openbao/releases/download/v2.6.5/" + name)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("%s: %s", name, resp.Status)
	}
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(kept), 0o755); err != nil {
		t.Fatal(err)
	}
	// through a rename: a run stopped halfway leaves no half of a package to read next time
	tmp := kept + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(tmp, kept); err != nil {
		t.Fatal(err)
	}
	return b
}

// repoImage is an image with the repository tooling of an OS (apt-ftparchive, createrepo_c), from
// testdata/repo-<name>/Dockerfile; the engine's cache makes the build a no-op after the first run.
func repoImage(t *testing.T, name string) string {
	t.Helper()
	image := "pack-e2e-repo-" + name
	docker(t, "build", "-q", "-t", image, filepath.Join("testdata", "repo-"+name))
	return image
}

// publishTree pushes a directory as an artifact whose files keep their paths, as a mirror is kept.
func (s *suite) publishTree(t *testing.T, tree, repository string) {
	t.Helper()
	dir := filepath.Dir(tree)
	write(t, filepath.Join(dir, "pack.yaml"), "repository: "+repository+"\nitems:\n- from: dir://"+filepath.Base(tree)+"/\n")
	run(t, dir, nil, s.packer, "oci://"+s.registry, "-f", "pack.yaml", "--plain-http")
}

// cutOff takes the host's way to the internet: it starts no connection but in its own network, where the registry is; what
// SSH came in by keeps working.
func cutOff(t *testing.T, c testcontainers.Container) {
	t.Helper()
	hostExec(t, c, "sh", "-c", `net=$(ip -4 route show scope link | awk '{print $1; exit}')
iptables -A OUTPUT -d 127.0.0.0/8 -j ACCEPT
iptables -A OUTPUT -d "$net" -j ACCEPT
iptables -A OUTPUT -m conntrack --ctstate ESTABLISHED,RELATED -j ACCEPT
iptables -A OUTPUT -j REJECT`)
	code, _, err := c.Exec(context.Background(), []string{"python3", "-c", "import urllib.request as u; u.urlopen('https://github.com', timeout=5)"})
	if err != nil {
		t.Fatal(err)
	}
	if code == 0 {
		t.Fatal("the host still reaches the internet")
	}
}

// startProxy runs oci-proxy on the host the way the target's agent does.
func (s *suite) startProxy(t *testing.T, c testcontainers.Container) {
	t.Helper()
	ctx := context.Background()
	if err := c.CopyFileToContainer(ctx, filepath.Join(s.targets, "bin", "oci-proxy-linux-"+runtime.GOARCH), "/usr/local/bin/oci-proxy", 0o755); err != nil {
		t.Fatal(err)
	}
	hostExec(t, c, "systemd-run", "--unit=probe-proxy", "--collect", "--setenv=OCI_PROXY_REGISTRY="+s.registryIP+":5000", "--setenv=OCI_PROXY_PLAIN_HTTP=true", "/usr/local/bin/oci-proxy")
	for range 30 {
		if code, _, err := c.Exec(ctx, []string{"python3", "-c", "import socket; socket.create_connection(('127.0.0.1', 8081), 1)"}); err == nil && code == 0 {
			return
		}
		time.Sleep(time.Second)
	}
	t.Fatal("oci-proxy does not listen")
}

// A package repository kept in the registry: apt and dnf, with no way to the internet, read its indexes and take its
// packages through oci-proxy; the pack's own installation goes the same way, the agent running oci-proxy for the
// install and stopping it after.
func TestFromARegistryMirror(t *testing.T) {
	s := setup(t)
	run(t, s.targets, nil, "make", "oci-proxy")
	key := filepath.Join(t.TempDir(), "id")
	run(t, "", nil, "ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-f", key)
	pub, err := os.ReadFile(key + ".pub")
	if err != nil {
		t.Fatal(err)
	}
	arch := runtime.GOARCH

	t.Run("apt", func(t *testing.T) {
		tree := filepath.Join(t.TempDir(), "repo")
		deb := fmt.Sprintf("openbao_2.6.5_linux_%s.deb", arch)
		pool := filepath.Join(tree, "pool")
		if err := os.MkdirAll(pool, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(pool, deb), download(t, deb), 0o644); err != nil {
			t.Fatal(err)
		}
		docker(t, "run", "--rm", "-v", tree+":/repo", "-w", "/repo", repoImage(t, "debian"), "sh", "-c", fmt.Sprintf(
			"d=dists/stable/main/binary-%[1]s && mkdir -p $d && apt-ftparchive packages pool > $d/Packages && apt-ftparchive release dists/stable > dists/stable/Release", arch))
		s.publishTree(t, tree, "mirrors/apt:1")

		c, _ := startHost(t, "debian", strings.TrimSpace(string(pub)))
		hostExec(t, c, "sh", "-c", "apt-get update -qq && apt-get install -y -qq --no-install-recommends openssl >/dev/null")
		cutOff(t, c)
		s.startProxy(t, c)
		hostExec(t, c, "sh", "-c", "rm -f /etc/apt/sources.list.d/* /etc/apt/sources.list; echo 'deb [trusted=yes] http://127.0.0.1:8081/mirrors/apt:1 stable main' > /etc/apt/sources.list.d/mirror.list; apt-get update && apt-get install -y --no-install-recommends openbao")
		if got := hostExec(t, c, "dpkg-query", "-W", "-f", "${Version}", "openbao"); !strings.Contains(got, "2.6.5") {
			t.Errorf("openbao = %q", got)
		}
	})

	t.Run("dnf", func(t *testing.T) {
		tree := filepath.Join(t.TempDir(), "repo")
		rpm := fmt.Sprintf("openbao_2.6.5_linux_%s.rpm", arch)
		if err := os.MkdirAll(tree, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(tree, rpm), download(t, rpm), 0o644); err != nil {
			t.Fatal(err)
		}
		docker(t, "run", "--rm", "-v", tree+":/repo", repoImage(t, "fedora"), "createrepo_c", "/repo")
		s.publishTree(t, tree, "mirrors/dnf:1")

		c, _ := startHost(t, "fedora", strings.TrimSpace(string(pub)))
		hostExec(t, c, "dnf", "install", "-y", "-q", "openssl") // what the package needs of the OS's own repository
		cutOff(t, c)
		s.startProxy(t, c)
		hostExec(t, c, "sh", "-c", "rm -f /etc/yum.repos.d/*; printf '[mirror]\\nname=mirror\\nbaseurl=http://127.0.0.1:8081/mirrors/dnf:1\\ngpgcheck=0\\n' > /etc/yum.repos.d/mirror.repo; dnf install -y openbao")
		if got := hostExec(t, c, "rpm", "-q", "--qf", "%{VERSION}", "openbao"); !strings.Contains(got, "2.6.5") {
			t.Errorf("openbao = %q", got)
		}
	})

	t.Run("the pack", func(t *testing.T) {
		tree := filepath.Join(t.TempDir(), "repo")
		deb := fmt.Sprintf("openbao_2.6.5_linux_%s.deb", arch)
		if err := os.MkdirAll(filepath.Join(tree, "v2.6.5"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(tree, "v2.6.5", deb), download(t, deb), 0o644); err != nil {
			t.Fatal(err)
		}
		s.publishTree(t, tree, "mirrors/openbao:2.6.5")

		c, port := startHost(t, "debian", strings.TrimSpace(string(pub)))
		// what the OS's own repository gives, which is not this pack's mirror: Prepare's packages
		hostExec(t, c, "sh", "-c", "apt-get update -qq && apt-get install -y -qq --no-install-recommends ca-certificates openssl >/dev/null")
		cutOff(t, c)
		env := strings.NewReplacer("${STATE}", filepath.Join(t.TempDir(), "state.json"),
			"${MIRROR_REGISTRY}", s.registryIP+":5000", "${HOSTS}", hostsBlock(key, map[string]string{"host": port})).Replace(mirrorEnv)
		r := s.packRelease(t, "openbao:2.6.5", "host", env, mirrorValues)
		run(t, r.dir, journal(c), s.pack, "apply")
		mapped, err := c.MappedPort(context.Background(), "8200/tcp")
		if err != nil {
			t.Fatal(err)
		}
		var body []byte
		for range 60 {
			resp, err := http.Get("http://127.0.0.1:" + mapped.Port() + "/v1/sys/seal-status")
			if err == nil {
				body, _ = io.ReadAll(resp.Body)
				_ = resp.Body.Close()
				break
			}
			time.Sleep(time.Second)
		}
		if !strings.Contains(string(body), `"version":"2.6.5"`) {
			t.Errorf("seal-status = %s", body)
		}
		// nothing of oci-proxy stays on the host: the agent stopped its proxy when the install was done
		if got := hostExec(t, c, "ss", "-tln"); strings.Contains(got, ":8081") {
			t.Errorf("oci-proxy still listens:\n%s", got)
		}
	})
}
