// Package e2e installs a pack the way an operator does, against a real Docker engine: the packs are generated and
// pushed to a registry (distribution, in a testcontainer), `pack init oci://…` starts a new release and brings the
// pack and the docker target from that registry, `pack apply` runs openbao, and openbao itself is asked what it runs.
//
//	make test-e2e        the docker target (fast)
//	make test-e2e-hosts  the ansible one: real Debian and Fedora hosts (slow, needs the internet)
//
// It needs a Docker engine, python3 for a venv of ansible (TestFromOSPackages), the internet for the hosts' packages, and the targets checkout (PACK_E2E_TARGETS, by default targets/ of this repository); the
// images come from the engine, pulled if it does not have them.
package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

const (
	registryImage = "distribution/distribution:3.0.0"
	openbaoRef    = "packs/openbao:2.6.5"
	targetTag     = "0.1.0"

	// a command that hangs (a server that never gets ready) fails the test instead of eating the whole timeout
	commandTimeout = 5 * time.Minute
	healthTimeout  = time.Minute
)

// What the operator writes for each scenario, whole. Only what a run decides is a placeholder: ${REGISTRY}, ${TARGET_TAG} and
// ${API_PORT} — where the api of openbao is published.
const (
	releaseYAML = `---
registry:
  plain_http: true
targets:
  docker:
    url: oci://${REGISTRY}/targets/docker:${TARGET_TAG}-{os}-{arch}
    plain_http: true
`

	devEnv = `---
target: main
targets:
  main:
    target: docker
keys:
  openbao:
    publish:
      api: ${API_PORT}
`
	devValues = `---
dev: true
`

	// raft and postgres start with a static seal and initialize themselves: the pack's readiness asks /v1/sys/health
	// and an uninitialized server answers it with 501, so nothing else would ever become ready. The server parses
	// JSON config through HCL: request has to be a list, or the keys of the blocks flatten into the type's name.
	raftEnv = `---
target: main
targets:
  main:
    target: docker
keys:
  openbao:
    publish:
      api: ${API_PORT}
    env:
    - name: BAO_SEAL_KEY
      value: 0123456789abcdef0123456789abcdef
`
	raftValues = `---
openbao:
  api_addr: http://127.0.0.1:8200
  cluster_addr: http://127.0.0.1:8201
  disable_mlock: true
  storage:
    raft:
      path: /openbao/file
      node_id: e2e
  listener:
    tcp:
      address: 0.0.0.0:8200
      tls_disable: true
  seal:
    static:
      current_key_id: e2e
      current_key: env://BAO_SEAL_KEY
  initialize:
  - e2e:
      request:
      - kv:
          operation: update
          path: sys/mounts/kv
          data:
            type: kv
`

	postgresEnv = `---
target: main
targets:
  main:
    target: docker
secrets:
  provider: generate
  provision:
  - /postgres/superuser/password
keys:
  openbao:
    publish:
      api: ${API_PORT}
    env:
    - name: BAO_SEAL_KEY
      value: 0123456789abcdef0123456789abcdef
  postgres:
    resources:
      requests:
        cpu: 500m
        memory: 512Mi
`
	postgresValues = `---
postgres:
  database: openbao
openbao:
  api_addr: http://127.0.0.1:8200
  disable_mlock: true
  listener:
    tcp:
      address: 0.0.0.0:8200
      tls_disable: true
  seal:
    static:
      current_key_id: e2e
      current_key: env://BAO_SEAL_KEY
  initialize:
  - e2e:
      request:
      - kv:
          operation: update
          path: sys/mounts/kv
          data:
            type: kv
`
)

// suite is what every scenario shares: the registry, with the packs and the docker target pushed. It is built once
// for the whole run — the builds, the registry and the pushes are the slow part — and torn down by TestMain.
type suite struct {
	pack       string
	packer     string
	targets    string
	registry   string
	registryIP string // how a host reaches the registry, on the engine's network

	reg      testcontainers.Container
	tagged   []string // the images pushed into the registry, removed at the end
	tmp      []string // the directories of the builds
	ansibleOnce sync.Once
	ansibleE error
}

var (
	shared     *suite
	sharedOnce sync.Once
)

func TestMain(m *testing.M) {
	code := m.Run()
	if shared != nil {
		shared.close()
	}
	os.Exit(code)
}

// setup is the suite of the run, built on the first test that asks.
func setup(t *testing.T) *suite {
	t.Helper()
	sharedOnce.Do(func() { shared = newSuite(t) })
	if shared == nil { // the build failed a test before this one (t.Fatal inside the Once)
		t.Fatal("the suite was not built")
	}
	return shared
}

func (s *suite) close() {
	ctx := context.Background()
	_ = s.reg.Terminate(ctx)
	for _, tag := range s.tagged {
		_ = exec.Command("docker", "rmi", tag).Run()
	}
	for _, d := range s.tmp {
		_ = os.RemoveAll(d)
	}
}

func TestOpenBao(t *testing.T) {
	s := setup(t)

	t.Run("dev", func(t *testing.T) {
		r := s.release(t, devEnv, devValues)
		r.apply(t)
		r.wantServing(t, "inmem")
	})

	t.Run("raft", func(t *testing.T) {
		r := s.release(t, raftEnv, raftValues)
		r.apply(t)
		r.wantServing(t, "raft")
		if out := docker(t, "exec", r.container("openbao"), "ls", "/openbao/file"); !strings.Contains(out, "raft") {
			t.Errorf("no raft data in the volume of the pack: %q", out)
		}
		// the data outlives the container: what started initialized comes back initialized
		docker(t, "restart", r.container("openbao"))
		r.wantServing(t, "raft")
	})

	t.Run("postgres", func(t *testing.T) {
		r := s.release(t, postgresEnv, postgresValues)
		r.apply(t)
		r.wantServing(t, "postgresql")
		// the formula's connection_url reached the database the operator named
		out := docker(t, "exec", r.container("postgres"), "sh", "-c",
			`PGPASSWORD="$POSTGRES_PASSWORD" psql -U postgres -d openbao -tAc 'select count(*) from openbao_kv_store'`)
		if n, err := strconv.Atoi(strings.TrimSpace(out)); err != nil || n == 0 {
			t.Errorf("openbao keeps nothing in postgres: %q", out)
		}
	})
}

// newSuite starts the registry and publishes into it what the release will ask for: the images, the packs (the held
// one first: an item of a pack file is mounted from the registry being pushed to) and the docker target. The ansible
// target is built and pushed by withAnsible, when a test that needs it runs.
func newSuite(t *testing.T) *suite {
	ctx := context.Background()

	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	targets := os.Getenv("PACK_E2E_TARGETS")
	if len(targets) == 0 {
		targets = filepath.Join(root, "targets")
	}
	bin, err := os.MkdirTemp("", "pack-e2e-bin")
	if err != nil {
		t.Fatal(err)
	}
	pack := filepath.Join(bin, "pack")
	packer := filepath.Join(bin, "oci-packer")
	run(t, root, nil, "go", "build", "-o", pack, "./cmd/pack")
	// the version go.mod pins, whatever is on PATH
	run(t, root, nil, "go", "build", "-o", packer, "github.com/arenadata/oci-packer/cmd/oci-packer")

	c, err := testcontainers.Run(ctx, registryImage,
		testcontainers.WithExposedPorts("5000/tcp"),
		testcontainers.WithWaitStrategy(wait.ForHTTP("/v2/").WithPort("5000/tcp")),
	)
	if err != nil {
		t.Fatal(err)
	}
	port, err := c.MappedPort(ctx, "5000/tcp")
	if err != nil {
		t.Fatal(err)
	}
	// 127.0.0.1 the engine takes as an insecure registry by itself, on the machine and in Docker Desktop's VM alike
	reg := "127.0.0.1:" + port.Port()
	s := &suite{pack: pack, packer: packer, targets: targets, registry: reg, reg: c, tmp: []string{bin}}

	for from, to := range map[string]string{
		"openbao/openbao:2.6.5": "images/openbao:2.6.5",
		"postgres:16.4":         "images/postgres:16.4",
	} {
		if err := exec.CommandContext(ctx, "docker", "image", "inspect", from).Run(); err != nil {
			docker(t, "pull", from)
		}
		docker(t, "tag", from, reg+"/"+to)
		docker(t, "push", "-q", reg+"/"+to)
		s.tagged = append(s.tagged, reg+"/"+to)
	}

	// a pack is generated and published on its own, the pack it holds first; the pack file says where it goes
	generated, err := os.MkdirTemp("", "pack-e2e-gen")
	if err != nil {
		t.Fatal(err)
	}
	s.tmp = append(s.tmp, generated)
	for _, name := range []string{"docker", "postgres", "openbao"} {
		run(t, root, nil, "go", "run", "./examples/"+name+"/go/cmd", "generate", "-o", generated)
		run(t, filepath.Join(generated, name), nil, packer, "oci://"+reg, "-f", "pack.yaml", "--plain-http")
	}

	s.pushTarget(t, "docker")
	ip, err := c.ContainerIP(ctx)
	if err != nil {
		t.Fatal(err)
	}
	s.registryIP = ip
	return s
}

// pushTarget builds pack-target-<name> and pushes it to the registry, <name>:<targetTag>-<os>-<arch>.
func (s *suite) pushTarget(t *testing.T, name string) {
	t.Helper()
	if err := s.pushTargetE(name); err != nil {
		t.Fatal(err)
	}
}

func (s *suite) pushTargetE(name string) error {
	dir, err := os.MkdirTemp("", "pack-e2e-target-"+name)
	if err != nil {
		return err
	}
	s.tmp = append(s.tmp, dir)
	if out, err := runCmd(filepath.Join(s.targets, name), "go", "build", "-o", filepath.Join(dir, "pack-target-"+name), "."); err != nil {
		return fmt.Errorf("build the %s target: %v\n%s", name, err, out)
	}
	if err := os.WriteFile(filepath.Join(dir, "pack.yaml"), []byte("type: application/vnd.pack.target.v1\nitems:\n- from: file://pack-target-"+name+"\n"), 0o644); err != nil {
		return err
	}
	if out, err := runCmd(dir, s.packer,
		fmt.Sprintf("oci://%s/targets/%s:%s-%s-%s", s.registry, name, targetTag, runtime.GOOS, runtime.GOARCH), "-f", "pack.yaml", "--plain-http"); err != nil {
		return fmt.Errorf("push the %s target: %v\n%s", name, err, out)
	}
	return nil
}

// withAnsible builds and pushes the ansible target, once: the ansible target carries oci-proxy to the hosts.
func (s *suite) withAnsible(t *testing.T) {
	t.Helper()
	s.ansibleOnce.Do(func() {
		if out, err := runCmd(s.targets, "make", "oci-proxy"); err != nil {
			s.ansibleE = fmt.Errorf("make oci-proxy: %v\n%s", err, out)
			return
		}
		s.ansibleE = s.pushTargetE("ansible")
	})
	if s.ansibleE != nil {
		t.Fatal(s.ansibleE)
	}
}

// release is one deployment: its directory, and where the api of its openbao is published.
type release struct {
	suite *suite
	dir   string
	api   string
	id    string
}

// release makes a new release from the documents of a scenario and runs `pack init` in it.
func (s *suite) release(t *testing.T, env, values string) *release {
	t.Helper()
	dir := t.TempDir()
	api := freePort(t)
	placeholders := strings.NewReplacer("${REGISTRY}", s.registry, "${TARGET_TAG}", targetTag, "${API_PORT}", api)
	write(t, filepath.Join(dir, "env.yaml"), placeholders.Replace(env))
	write(t, filepath.Join(dir, "values.yaml"), values)
	write(t, filepath.Join(dir, "release.yaml"), placeholders.Replace(releaseYAML))
	r := &release{suite: s, dir: dir, api: api}
	t.Cleanup(func() { r.cleanup(t) })

	ref := "oci://" + s.registry + "/" + openbaoRef
	out := run(t, dir, nil, s.pack, "init", ref)
	if !strings.Contains(out, ref+" locked at sha256:") {
		t.Errorf("init locked nothing:\n%s", out)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "release.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "pack: "+ref) {
		t.Errorf("init did not name the pack in release.yaml:\n%s", raw)
	}
	var lock struct {
		Pack, Digest string
		Targets      map[string]struct{ SHA256 string }
	}
	raw, err = os.ReadFile(filepath.Join(dir, "release.lock.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &lock); err != nil {
		t.Fatal(err)
	}
	if lock.Pack != ref || len(lock.Digest) == 0 || len(lock.Targets["docker"].SHA256) == 0 {
		t.Errorf("lock = %s", raw)
	}
	if _, err := os.Stat(filepath.Join(dir, ".pack", "bin", "pack-target-docker")); err != nil {
		t.Errorf("init did not bring the target: %v", err)
	}
	return r
}

var installed = regexp.MustCompile(`installed openbao 2\.6\.5 on docker \(([0-9a-f-]{36})\)`)

func (r *release) apply(t *testing.T) {
	t.Helper()
	out := run(t, r.dir, r.dumpLogs, r.suite.pack, "apply")
	m := installed.FindStringSubmatch(out)
	if m == nil {
		t.Fatalf("apply did not say it installed:\n%s", out)
	}
	r.id = m[1]
}

func (r *release) container(key string) string { return r.id + "-" + key }

// wantServing asks the server itself: initialized, unsealed, on the storage the release chose.
func (r *release) wantServing(t *testing.T, storage string) {
	t.Helper()
	var last string
	deadline := time.Now().Add(healthTimeout)
	for time.Now().Before(deadline) {
		var status struct {
			Initialized bool   `json:"initialized"`
			Sealed      bool   `json:"sealed"`
			Version     string `json:"version"`
			Storage     string `json:"storage_type"`
		}
		resp, err := http.Get("http://127.0.0.1:" + r.api + "/v1/sys/seal-status")
		if err != nil {
			last = err.Error()
			time.Sleep(time.Second)
			continue
		}
		err = json.NewDecoder(resp.Body).Decode(&status)
		_ = resp.Body.Close()
		if err == nil && status.Initialized && !status.Sealed && status.Storage == storage && status.Version == "2.6.5" {
			return
		}
		last = fmt.Sprintf("%+v (%v)", status, err)
		time.Sleep(time.Second)
	}
	r.dumpLogs(t)
	t.Fatalf("openbao is not serving on %s: %s", storage, last)
}

func (r *release) dumpLogs(t *testing.T) {
	t.Helper()
	id := r.installationID()
	if len(id) == 0 {
		return
	}
	names, _ := exec.Command("docker", "ps", "-a", "-q", "--filter", "label=pack.horchestra.io/installation="+id).Output()
	for _, c := range strings.Fields(string(names)) {
		out, _ := exec.Command("docker", "logs", "--tail", "30", c).CombinedOutput()
		t.Logf("logs of %s:\n%s", c, out)
	}
}

// installationID reads the record: an apply that failed has said nothing, and what it started must still go.
func (r *release) installationID() string {
	if len(r.id) > 0 {
		return r.id
	}
	raw, err := os.ReadFile(filepath.Join(r.dir, ".pack", "state.json"))
	if err != nil {
		return ""
	}
	var state struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(raw, &state); err != nil {
		return ""
	}
	return state.ID
}

// cleanup removes what the installation left: `pack delete` keeps the data and the credentials by design, a test
// must not.
func (r *release) cleanup(t *testing.T) {
	t.Helper()
	id := r.installationID()
	if len(id) == 0 {
		return
	}
	if out, err := exec.Command("docker", "ps", "-a", "-q", "--filter", "label=pack.horchestra.io/installation="+id).Output(); err == nil {
		if cs := strings.Fields(string(out)); len(cs) > 0 {
			_ = exec.Command("docker", append([]string{"rm", "-f", "-v"}, cs...)...).Run()
		}
	}
	if out, err := exec.Command("docker", "volume", "ls", "-q", "--filter", "name="+id).Output(); err == nil {
		if vs := strings.Fields(string(out)); len(vs) > 0 {
			_ = exec.Command("docker", append([]string{"volume", "rm", "-f"}, vs...)...).Run()
		}
	}
	_ = exec.Command("docker", "network", "rm", id).Run()
}

// run runs a command to its end and fails the test if it fails; onFail says more about why. SIGINT first: pack stops
// the target it started, which a kill would leave behind.
func run(t *testing.T, dir string, onFail func(*testing.T), name string, args ...string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGINT) }
	cmd.WaitDelay = 30 * time.Second
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Run(); err != nil {
		if onFail != nil {
			onFail(t)
		}
		t.Fatalf("%s %s: %v\n%s", name, strings.Join(args, " "), err, out.String())
	}
	return out.String()
}

func docker(t *testing.T, args ...string) string {
	t.Helper()
	return run(t, "", nil, "docker", args...)
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// freePort names a port nothing listens on now; the engine publishes the api there.
func freePort(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = l.Close() }()
	return strconv.Itoa(l.Addr().(*net.TCPAddr).Port)
}
