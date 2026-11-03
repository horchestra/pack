package e2e

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// The docker target from its image (runtime: docker): built from the targets checkout, pushed to the registry the
// release names it in, locked by pack refresh and run in a container that reaches the engine through its socket —
// openbao comes up as it does from the program of .pack/bin.
func TestTargetInAContainer(t *testing.T) {
	s := setup(t)
	image := strings.TrimPrefix(s.targetImage(t, "docker"), "oci://")

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
	if tl := lock.Targets["docker"]; !strings.HasPrefix(tl.Digest, "sha256:") || len(tl.SHA256) > 0 {
		t.Fatalf("lock = %s\n%s", raw, out)
	}
	r.apply(t)
	r.wantServing(t, "inmem")
	if _, err := os.Stat(filepath.Join(dir, ".pack", "home", "docker")); err != nil {
		t.Errorf("the container had no home of the release's: %v", err)
	}
}

const (
	containerRelease = `---
registry:
  plain_http: true
targets:
  docker:
    image: ${IMAGE}
    plain_http: true
`
	containerEnv = `---
target: main
targets:
  main:
    target: docker
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
	run(t, filepath.Join(s.targets, name), nil, "docker", "build", "-q", "-t", image, ".")
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

// ansibleInAContainerEnv is the ansible target from its image: the key a mount in its home, the host key check
// passed by name, the hosts as a container reaches them.
func ansibleInAContainerEnv(key, address, network string, ports map[string]string) string {
	var b strings.Builder
	b.WriteString("---\ntarget: main\ntargets:\n  main:\n    target: ansible\n    ssh_private_key: /home/pack/.ssh/id\n    runtime:\n      type: docker\n")
	if len(network) > 0 {
		b.WriteString("      network: " + network + "\n")
	}
	b.WriteString("      mounts:\n        - " + key + ":/home/pack/.ssh/id:ro\n      env:\n        - ANSIBLE_HOST_KEY_CHECKING\n")
	b.WriteString(strings.ReplaceAll(hostsBlock(ports), "host: 127.0.0.1", "host: "+address))
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
