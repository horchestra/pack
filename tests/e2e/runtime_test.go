package e2e

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// The host target from its image (runtime: docker): built from the targets checkout, pushed to the registry the
// release names it in, locked by pack refresh and run in a container that reaches the engine through its socket —
// openbao comes up as it does from the program of .pack/bin. With no hosts the target works on its own container:
// the agent a process of it, the containers on the engine of the socket.
func TestTargetInAContainer(t *testing.T) {
	s := setup(t)
	image := strings.TrimPrefix(s.targetImage(t, "host"), "oci://")

	dir := t.TempDir()
	api := freePort(t)
	ref := "oci://" + s.registry + "/" + openbaoRef
	write(t, filepath.Join(dir, "env.yaml"), strings.ReplaceAll(containerEnv, "${API_PORT}", api))
	write(t, filepath.Join(dir, "values.yaml"), devValues)
	write(t, filepath.Join(dir, "release.yaml"), strings.NewReplacer("${IMAGE}", "oci://"+image).Replace(containerRelease)+"pack: "+ref+"\n")
	r := &release{suite: s, dir: dir, api: api}
	t.Cleanup(func() { r.cleanup(t) })

	out := run(t, dir, nil, s.pack, "refresh")
	var lock struct {
		Targets map[string]struct{ Image, Digest, SHA256 string }
	}
	raw, err := os.ReadFile(filepath.Join(dir, "release.lock.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &lock); err != nil {
		t.Fatal(err)
	}
	if tl := lock.Targets["host"]; !strings.HasPrefix(tl.Digest, "sha256:") || len(tl.SHA256) > 0 {
		t.Fatalf("lock = %s\n%s", raw, out)
	}
	r.apply(t)
	r.wantServing(t, "inmem")
	if _, err := os.Stat(filepath.Join(dir, ".pack", "home", "host")); err != nil {
		t.Errorf("the container had no home of the release's: %v", err)
	}
	// the facts of the engine, and the port openbao publishes on it among them
	wantFacts(t, stdoutOf(t, dir, s.pack, "discover", "--json"), "default", func(h hostFacts) bool {
		return h.CPUs > 0 && h.MemoryMB > 0 && slices.ContainsFunc(h.Ports, func(p struct{ Port int }) bool { return strconv.Itoa(p.Port) == api })
	})
}

// stdoutOf runs a command and gives its stdout alone: what --json writes, without what is said on stderr.
func stdoutOf(t *testing.T, dir, name string, args ...string) string {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("%s %v: %v\n%s%s", name, args, err, out, stderr.String())
	}
	return string(out)
}

// hostFacts is what the e2e tests look at of a host's facts.
type hostFacts struct {
	OS       string   `json:"os"`
	CPUs     int      `json:"cpus"`
	MemoryMB int64    `json:"memory_mb"`
	Runtimes []string `json:"runtimes"`
	Ports    []struct{ Port int }
}

// wantFacts: pack discover --json told the target's facts, and a host of them is as ok says.
func wantFacts(t *testing.T, out, target string, ok func(hostFacts) bool) {
	t.Helper()
	var got struct {
		Told map[string]struct {
			Hosts map[string]hostFacts `json:"hosts"`
		} `json:"told"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("discover --json: %v\n%s", err, out)
	}
	for _, h := range got.Told[target].Hosts {
		if ok(h) {
			return
		}
	}
	t.Errorf("no host of target %s as wanted:\n%s", target, out)
}

const (
	containerRelease = `---
registry:
  plain_http: true
targets:
  host:
    image: ${IMAGE}
    plain_http: true
`
	containerEnv = `---
target: main
targets:
  main:
    target: host
    runtime:
      type: docker
      mounts:
        - /var/run/docker.sock:/var/run/docker.sock
keys:
  openbao:
    publish:
      api: ${API_PORT}
`
)

// targetImage builds the image of a target from the targets checkout and pushes it to the registry: what release.yaml
// names in targets: {<name>: {image: oci://…}}.
func (s *suite) targetImage(t *testing.T, name string) string {
	t.Helper()
	image := s.registry + "/targets/" + name + ":" + targetTag + "-image"
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	// the checkout of pack beside the target's: what its go.mod's replace of pack means
	run(t, filepath.Join(s.targets, name), nil, "docker", "build", "-q", "--build-context", "pack="+root, "-t", image, ".")
	s.tagged = append(s.tagged, image)
	run(t, s.targets, nil, "docker", "push", "-q", image)
	return "oci://" + image
}

// hostFromAContainer is how a container reaches a host the machine reaches at 127.0.0.1:<port>: Docker Desktop's
// name of the machine, or — on Linux — the machine's own network.
func hostFromAContainer() (address, network string) {
	if runtime.GOOS == "linux" {
		return "127.0.0.1", "host"
	}
	return "host.docker.internal", ""
}

// hostInAContainerEnv is the host target from its image: the key a mount in its home, the hosts as a container
// reaches them.
func hostInAContainerEnv(key, address, network string, ports map[string]string) string {
	var b strings.Builder
	b.WriteString("---\ntarget: main\ntargets:\n  main:\n    target: host\n    runtime:\n      type: docker\n")
	if len(network) > 0 {
		b.WriteString("      network: " + network + "\n")
	}
	b.WriteString("      mounts:\n        - " + key + ":/home/pack/.ssh/id:ro\n")
	b.WriteString(strings.ReplaceAll(hostsBlock("/home/pack/.ssh/id", ports), "host: 127.0.0.1", "host: "+address))
	return b.String()
}

// imageRelease is a release of a pack whose target runs from its image.
func (s *suite) imageRelease(t *testing.T, pack, target, image, env, values string) *release {
	t.Helper()
	dir := t.TempDir()
	write(t, filepath.Join(dir, "env.yaml"), env)
	write(t, filepath.Join(dir, "values.yaml"), values)
	write(t, filepath.Join(dir, "release.yaml"), "---\nregistry:\n  plain_http: true\ntargets:\n  "+target+":\n    image: "+image+
		"\n    plain_http: true\npack: oci://"+s.registry+"/packs/"+pack+"\n")
	if out, err := runCmd(dir, s.pack, "refresh"); err != nil {
		t.Fatalf("refresh: %v\n%s", err, out)
	}
	return &release{suite: s, dir: dir}
}

// stat is os.Stat for the tests whose loop has a variable os.
var stat = os.Stat
