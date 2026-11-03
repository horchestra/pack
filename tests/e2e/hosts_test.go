package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/testcontainers/testcontainers-go"
	tcexec "github.com/testcontainers/testcontainers-go/exec"
	"github.com/testcontainers/testcontainers-go/wait"
)

const (
	// ${HOSTS} is hostsBlock's: the environment's hosts, one entry per host of the scenario
	dockerEnv = `---
target: main
targets:
  main:
    target: ansible
    ansible: ${ANSIBLE}
    ssh_private_key: ${SSH_KEY}
    state: ${STATE}
${HOSTS}`
	dockerValues = `---
daemon:
  log-driver: local
`
	bothEnv = `---
target: main
targets:
  main:
    target: ansible
    ansible: ${ANSIBLE}
    ssh_private_key: ${SSH_KEY}
    state: ${STATE}
${HOSTS}
secrets:
  provider: generate
  provision:
  - /postgres/superuser/password
keys:
  postgres:
    resources:
      requests:
        cpu: 500m
        memory: 512Mi
`
	bothValues = `---
postgres:
  database: openbao
openbao:
  disable_mlock: true
  api_addr: http://127.0.0.1:8200
  listener:
    tcp:
      address: 0.0.0.0:8200
      tls_disable: true
`
)

// osUnderTest is one OS the packs are installed from packages on: testdata/<name> is its host image, pkgQuery says
// whether a package is installed there. bao is the version the OS's own openbao package is; "" — the OS's repository
// moves, it is not checked.
type osUnderTest struct {
	name     string
	bao      string
	pkgQuery []string
}

var osUnderTests = []osUnderTest{
	{"debian", "2.6.5", []string{"dpkg-query", "-W", "docker-ce"}},
	{"fedora", "", []string{"rpm", "-q", "docker-ce"}},
}

// hostsBlock is the hosts of the environment: name → the SSH port the host's 22 is published at.
func hostsBlock(ports map[string]string) string {
	names := make([]string, 0, len(ports))
	for n := range ports {
		names = append(names, n)
	}
	slices.Sort(names)
	var b strings.Builder
	b.WriteString("    hosts:\n")
	for _, n := range names {
		fmt.Fprintf(&b, "      %s:\n        host: 127.0.0.1\n        port: %s\n        user: root\n", n, ports[n])
	}
	return b.String()
}

// A host: what the hosts of a stand are, an empty OS with systemd that SSH reaches, ansible in a venv of its own.
func startHost(t *testing.T, name, key string) (testcontainers.Container, string) {
	t.Helper()
	ctx := context.Background()
	image := "pack-e2e-host-" + name
	docker(t, "build", "-q", "-t", image, filepath.Join("testdata", name))
	c, err := testcontainers.Run(ctx, image,
		testcontainers.WithExposedPorts("22/tcp", "8200/tcp"),
		testcontainers.WithHostConfigModifier(func(h *container.HostConfig) {
			h.Privileged = true
			h.CgroupnsMode = "host"
			h.Binds = append(h.Binds, "/sys/fs/cgroup:/sys/fs/cgroup:rw")
			h.Tmpfs = map[string]string{"/run": "", "/tmp": ""}
		}),
		testcontainers.WithWaitStrategy(wait.ForListeningPort("22/tcp").WithStartupTimeout(time.Minute)),
	)
	testcontainers.CleanupContainer(t, c)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := c.Exec(ctx, []string{"sh", "-c", "printf '%s\\n' '" + key + "' > /root/.ssh/authorized_keys && chmod 700 /root/.ssh && chmod 600 /root/.ssh/authorized_keys"}); err != nil {
		t.Fatal(err)
	}
	port, err := c.MappedPort(ctx, "22/tcp")
	if err != nil {
		t.Fatal(err)
	}
	return c, port.Port()
}

// ansibleBin is ansible in a venv kept between runs: nothing is installed on the machine.
func ansibleBin(t *testing.T) string {
	t.Helper()
	cache, err := os.UserCacheDir()
	if err != nil {
		t.Fatal(err)
	}
	venv := filepath.Join(cache, "pack-e2e", "ansible-venv")
	bin := filepath.Join(venv, "bin", "ansible")
	if _, err := os.Stat(bin); err != nil {
		run(t, "", nil, "python3", "-m", "venv", venv)
		run(t, "", nil, filepath.Join(venv, "bin", "pip"), "install", "-q", "ansible-core")
	}
	return bin
}

func hostExec(t *testing.T, c testcontainers.Container, cmd ...string) string {
	t.Helper()
	code, r, err := c.Exec(context.Background(), cmd, tcexec.Multiplexed())
	if err != nil {
		t.Fatal(err)
	}
	out, _ := io.ReadAll(r)
	if code != 0 {
		t.Fatalf("%v: exit %d: %s", cmd, code, out)
	}
	return string(out)
}

// The packs from OS packages: the same packs as in TestOpenBao, on the OSes they name.
func TestFromOSPackages(t *testing.T) {
	s := setup(t)
	s.withAnsible(t)
	t.Setenv("ANSIBLE_HOST_KEY_CHECKING", "False")
	t.Setenv("LC_ALL", "en_US.UTF-8")
	t.Setenv("OBJC_DISABLE_INITIALIZE_FORK_SAFETY", "YES")
	bin := ansibleBin(t)
	keyDir := t.TempDir()
	key := filepath.Join(keyDir, "id")
	run(t, "", nil, "ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-f", key)
	pub, err := os.ReadFile(key + ".pub")
	if err != nil {
		t.Fatal(err)
	}

	for _, os := range osUnderTests {
		t.Run(os.name, func(t *testing.T) {
			c, port := startHost(t, os.name, strings.TrimSpace(string(pub)))
			fill := func(env string, hosts map[string]string) string {
				return strings.NewReplacer("${ANSIBLE}", bin, "${SSH_KEY}", key, "${STATE}", filepath.Join(t.TempDir(), "state.json"),
					"${HOSTS}", hostsBlock(hosts)).Replace(env)
			}

			t.Run("docker", func(t *testing.T) {
				// a second host: every host of the environment gets the key, one apply
				c2, port2 := startHost(t, os.name, strings.TrimSpace(string(pub)))
				r := s.packRelease(t, "docker:1.0.0", "ansible", fill(dockerEnv, map[string]string{"host": port, "host2": port2}), dockerValues)
				out := run(t, r.dir, journal(c), s.pack, "apply")
				if !strings.Contains(out, "installed docker 1.0.0") {
					t.Errorf("apply:\n%s", out)
				}
				for _, h := range []testcontainers.Container{c, c2} {
					if got := hostExec(t, h, "systemctl", "is-active", "docker"); strings.TrimSpace(got) != "active" {
						t.Errorf("docker = %q", got)
					}
					// the value of the release reached the engine: a restart was notified by the file
					if got := hostExec(t, h, "docker", "info", "-f", "{{.LoggingDriver}}"); !strings.Contains(got, "local") {
						t.Errorf("the log driver = %q", got)
					}
				}

				// a second apply changes nothing, and what runs is not restarted
				uptime := strings.TrimSpace(hostExec(t, c, "systemctl", "show", "docker", "-p", "ActiveEnterTimestamp"))
				run(t, r.dir, journal(c), s.pack, "apply")
				if got := strings.TrimSpace(hostExec(t, c, "systemctl", "show", "docker", "-p", "ActiveEnterTimestamp")); got != uptime {
					t.Errorf("a no-op apply restarted docker: %s → %s", uptime, got)
				}
			})

			t.Run("delete", func(t *testing.T) {
				c3, port3 := startHost(t, os.name, strings.TrimSpace(string(pub)))
				r := s.packRelease(t, "docker:1.0.0", "ansible", fill(dockerEnv, map[string]string{"host": port3}), dockerValues)
				run(t, r.dir, journal(c3), s.pack, "apply")
				run(t, r.dir, journal(c3), s.pack, "delete")
				if code, _, _ := c3.Exec(context.Background(), []string{"test", "-e", "/etc/docker/daemon.json"}); code == 0 {
					t.Error("daemon.json is still there")
				}
				if code, out, _ := c3.Exec(context.Background(), []string{"systemctl", "is-active", "docker"}, tcexec.Multiplexed()); code == 0 {
					b, _ := io.ReadAll(out)
					t.Errorf("docker still runs: %s", b)
				}
				// the packages stay: another key may need them
				if code, out, err := c3.Exec(context.Background(), os.pkgQuery, tcexec.Multiplexed()); err != nil || code != 0 {
					b, _ := io.ReadAll(out)
					t.Errorf("docker-ce is gone: %d %v %s", code, err, b)
				}
			})

			t.Run("openbao on postgres", func(t *testing.T) {
				r := s.packRelease(t, "openbao:2.6.5", "ansible", fill(bothEnv, map[string]string{"host": port}), bothValues)
				run(t, r.dir, journal(c), s.pack, "apply")
				mapped, err := c.MappedPort(context.Background(), "8200/tcp")
				if err != nil {
					t.Fatal(err)
				}
				var status struct {
					Storage string `json:"storage_type"`
					Version string `json:"version"`
				}
				deadline := time.Now().Add(time.Minute)
				for {
					resp, err := http.Get("http://127.0.0.1:" + mapped.Port() + "/v1/sys/seal-status")
					if err == nil {
						err = json.NewDecoder(resp.Body).Decode(&status)
						_ = resp.Body.Close()
					}
					if err == nil && len(status.Storage) > 0 {
						break
					}
					if time.Now().After(deadline) {
						t.Fatalf("openbao does not answer: %v\n%s", err, hostExec(t, c, "journalctl", "-u", "openbao", "--no-pager", "-n", "30"))
					}
					time.Sleep(time.Second)
				}
				if status.Storage != "postgresql" {
					t.Errorf("storage = %q", status.Storage)
				}
				// the version is the OS's, not the pack's: Debian takes the release's file, Fedora its own package
				if len(os.bao) > 0 && status.Version != os.bao {
					t.Errorf("version = %q, want %s", status.Version, os.bao)
				}
				got := hostExec(t, c, "runuser", "-u", "postgres", "--", "psql", "-d", "openbao", "-tAc", "select to_regclass('openbao_kv_store') is not null")
				if strings.TrimSpace(got) != "t" {
					t.Errorf("openbao keeps nothing in postgres: %q", got)
				}
			})
		})
	}
}

// journal says why a unit of the host did not start.
func journal(c testcontainers.Container) func(*testing.T) {
	return func(t *testing.T) {
		code, r, err := c.Exec(context.Background(), []string{"journalctl", "--no-pager", "-n", "40"}, tcexec.Multiplexed())
		if err != nil {
			return
		}
		b, _ := io.ReadAll(r)
		t.Logf("the journal of the host (%d):\n%s", code, b)
	}
}

// packRelease is a new release of a pack, on a target of the registry.
func (s *suite) packRelease(t *testing.T, ref, target, env, values string) *release {
	t.Helper()
	dir := t.TempDir()
	write(t, filepath.Join(dir, "env.yaml"), env)
	write(t, filepath.Join(dir, "values.yaml"), values)
	write(t, filepath.Join(dir, "release.yaml"), strings.NewReplacer("${REGISTRY}", s.registry, "${TARGET}", target, "${TARGET_TAG}", targetTag).Replace(hostReleaseYAML))
	full := fmt.Sprintf("oci://%s/packs/%s", s.registry, ref)
	if out, err := runCmd(dir, s.pack, "init", full); err != nil {
		t.Fatalf("init: %v\n%s", err, out)
	}
	return &release{suite: s, dir: dir}
}

func runCmd(dir, name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	return string(out), err
}

const hostReleaseYAML = `---
registry:
  plain_http: true
targets:
  ${TARGET}:
    url: oci://${REGISTRY}/targets/${TARGET}:${TARGET_TAG}-{os}-{arch}
    plain_http: true
`
