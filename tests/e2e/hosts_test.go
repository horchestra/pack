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
	"strings"
	"testing"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/testcontainers/testcontainers-go"
	tcexec "github.com/testcontainers/testcontainers-go/exec"
	"github.com/testcontainers/testcontainers-go/wait"
)

const (
	dockerEnv = `---
target: main
targets:
  main:
    target: ansible
    ansible: ${ANSIBLE}
    ssh_private_key: ${SSH_KEY}
    state: ${STATE}
    hosts:
      host:
        host: 127.0.0.1
        port: ${SSH_PORT}
        user: root
`
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
    hosts:
      host:
        host: 127.0.0.1
        port: ${SSH_PORT}
        user: root
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

	for name, bao := range map[string]string{"debian": "2.6.5", "fedora": ""} {
		t.Run(name, func(t *testing.T) {
			c, port := startHost(t, name, strings.TrimSpace(string(pub)))
			fill := func(env string) string {
				return strings.NewReplacer("${ANSIBLE}", bin, "${SSH_KEY}", key, "${SSH_PORT}", port, "${STATE}", filepath.Join(t.TempDir(), "state.json")).Replace(env)
			}

			t.Run("docker", func(t *testing.T) {
				r := s.packRelease(t, "docker:1.0.0", "ansible", fill(dockerEnv), dockerValues)
				out := run(t, r.dir, journal(c), s.pack, "apply")
				if !strings.Contains(out, "installed docker 1.0.0") {
					t.Errorf("apply:\n%s", out)
				}
				if got := hostExec(t, c, "systemctl", "is-active", "docker"); strings.TrimSpace(got) != "active" {
					t.Errorf("docker = %q", got)
				}
				// the value of the release reached the engine: a restart was notified by the file
				if got := hostExec(t, c, "docker", "info", "-f", "{{.LoggingDriver}}"); !strings.Contains(got, "local") {
					t.Errorf("the log driver = %q", got)
				}
			})

			t.Run("openbao on postgres", func(t *testing.T) {
				r := s.packRelease(t, "openbao:2.6.5", "ansible", fill(bothEnv), bothValues)
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
				if len(bao) > 0 && status.Version != bao {
					t.Errorf("version = %q, want %s", status.Version, bao)
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
