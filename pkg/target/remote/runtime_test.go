package remote

import (
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/horchestra/pack/pkg/target"
)

func TestRuntimeOf(t *testing.T) {
	rt, rest, err := RuntimeOf(map[string]string{
		"namespace": "data", "runtime_type": "docker", "runtime_network": "host", "runtime_mounts": "/a:/b:ro, ~/c:/d",
		"runtime_ssh_agent": "true", "runtime_env": "X,Y", "runtime_image": "reg/t@sha256:1",
	})
	if err != nil {
		t.Fatal(err)
	}
	want := Runtime{Type: "docker", Image: "reg/t@sha256:1", Network: "host", Mounts: []string{"/a:/b:ro", "~/c:/d"}, SSHAgent: true, Env: []string{"X", "Y"}}
	if !reflect.DeepEqual(rt, want) {
		t.Errorf("runtime %+v, want %+v", rt, want)
	}
	if !reflect.DeepEqual(rest, map[string]string{"namespace": "data"}) {
		t.Errorf("the target's set keeps the runtime: %v", rest)
	}
	if rt, _, err := RuntimeOf(map[string]string{"namespace": "x"}); err != nil || rt.Type != RuntimeNative {
		t.Errorf("no runtime section: %+v %v", rt, err)
	}
	for name, set := range map[string]map[string]string{
		"provider":              {"provider": "oci://reg/x:1"},
		"a type":                {"runtime_type": "kubernetes"},
		"unknown":               {"runtime_cpus": "2"},
		"ssh_agent":             {"runtime_type": "docker", "runtime_ssh_agent": "yes please"},
		"a container's, native": {"runtime_mounts": "/a:/b"},
	} {
		if _, _, err := RuntimeOf(set); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestContainerArgs(t *testing.T) {
	c := container{name: "ansible", installation: "id1", cname: "pack-target-ansible-x", port: "4000", token: "tok", owner: "h/1",
		sshSock: "/run/agent", lookup: func(n string) bool { return n == "VAULT_PASS" }}
	args, addr, err := containerArgs(c, Runtime{Type: RuntimeDocker, Image: "reg/ansible@sha256:1", Mounts: []string{"/etc/hosts:/etc/hosts:ro"},
		SSHAgent: true, Env: []string{"VAULT_PASS"}})
	if err != nil {
		t.Fatal(err)
	}
	line := strings.Join(args, " ")
	for _, want := range []string{
		"run -i --rm --name pack-target-ansible-x",
		"--label " + LabelInstallation + "=id1", "--label " + LabelTarget + "=ansible", "--label " + LabelOwner + "=h/1",
		"-p 127.0.0.1:4000:7353 -e " + EnvAddress + "=tcp://0.0.0.0:7353", "-e " + EnvToken + "=tok",
		"-v /etc/hosts:/etc/hosts:ro", "-v /run/agent:/run/pack/ssh-agent.sock -e SSH_AUTH_SOCK=/run/pack/ssh-agent.sock",
		"-e VAULT_PASS reg/ansible@sha256:1",
	} {
		if !strings.Contains(line, want) {
			t.Errorf("lacks %q:\n%s", want, line)
		}
	}
	if addr != "127.0.0.1:4000" || args[len(args)-1] != "reg/ansible@sha256:1" {
		t.Errorf("addr %s, last %s", addr, args[len(args)-1])
	}
	if strings.Contains(line, "VAULT_PASS=") {
		t.Errorf("a variable's value on the command line:\n%s", line)
	}

	args, addr, err = containerArgs(c, Runtime{Type: RuntimeDocker, Image: "i", Network: "host"})
	if err != nil || !slices.Contains(args, "--network") || slices.Contains(args, "-p") || addr != "127.0.0.1:4000" ||
		!slices.Contains(args, EnvAddress+"=tcp://127.0.0.1:4000") {
		t.Errorf("host network: %v %s %v", args, addr, err)
	}
	home, _ := os.UserHomeDir()
	if args, _, err := containerArgs(c, Runtime{Type: RuntimeDocker, Image: "i", Mounts: []string{"~/.ssh/known_hosts:/k:ro"}}); err != nil ||
		!slices.Contains(args, filepath.Join(home, ".ssh/known_hosts")+":/k:ro") {
		t.Errorf("~ is the home: %v %v", args, err)
	}
	for name, rt := range map[string]Runtime{
		"a variable not set": {Env: []string{"NOPE"}},
		"a relative mount":   {Mounts: []string{"a:/b"}},
		"a bad mode":         {Mounts: []string{"/a:/b:x"}},
		"no agent":           {SSHAgent: true},
	} {
		cc := c
		if name == "no agent" {
			cc.sshSock = ""
		}
		rt.Type, rt.Image = RuntimeDocker, "i"
		if _, _, err := containerArgs(cc, rt); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestCheckLabels(t *testing.T) {
	ok := map[string]string{LabelTarget: "docker", LabelProtocol: Protocol}
	if err := checkLabels("i", "docker", ok); err != nil {
		t.Error(err)
	}
	for name, l := range map[string]map[string]string{
		"none":           {},
		"another target": {LabelTarget: "k8s", LabelProtocol: Protocol},
		"another proto":  {LabelTarget: "docker", LabelProtocol: "pack.target.v0"},
	} {
		if err := checkLabels("i", "docker", l); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestOrphans(t *testing.T) {
	ps := "c1 h/10\nc2 h/11\nc3 other/10\nc4\nc5 h/x\n"
	got := orphans(ps, "h", func(pid int) bool { return pid == 11 })
	if !reflect.DeepEqual(got, []string{"c1"}) {
		t.Errorf("orphans %v: only a container of this machine whose pack is gone", got)
	}
}

// A fake container runtime: inspect says the labels PACK_FAKE_LABELS has, run starts the fake target as the image
// would, listening where -p publishes it. Every call is written to PACK_FAKE_CALLS.
const fakeRuntime = `#!/bin/sh
echo "$@" >> "$PACK_FAKE_CALLS"
case "$1" in
image) echo "$PACK_FAKE_LABELS" ;;
run)
	shift
	while [ $# -gt 1 ]; do
		case "$1" in
		-e) shift; case "$1" in *=*) export "$1" ;; esac ;;
		-p) shift; port=$(echo "$1" | cut -d: -f2) ;;
		--name|--label|--network|-v) shift ;;
		esac
		shift
	done
	export PACK_TARGET_ADDRESS="tcp://127.0.0.1:$port"
	exec "$PACK_FAKE_BIN"
	;;
esac
`

func TestDockerRuntime(t *testing.T) {
	bin := filepath.Join(fakeBin(t), Prefix+"fake")
	t.Setenv(EnvPath, t.TempDir()) // not where the runtime looks: the image is what runs
	dir := t.TempDir()
	rt := filepath.Join(dir, "docker")
	if err := os.WriteFile(rt, []byte(fakeRuntime), 0o755); err != nil {
		t.Fatal(err)
	}
	calls := filepath.Join(dir, "calls")
	t.Setenv(EnvRuntime, rt)
	t.Setenv("PACK_FAKE_BIN", bin)
	t.Setenv("PACK_FAKE_CALLS", calls)
	t.Setenv("PACK_FAKE_DIR", t.TempDir())
	t.Setenv("PACK_FAKE_LABELS", `{"`+LabelTarget+`":"fake","`+LabelProtocol+`":"`+Protocol+`"}`)

	in, err := target.Open("fake", "shop", map[string]string{"target": "fake", "runtime_type": "docker", "runtime_image": "reg/fake@sha256:1"})
	if err != nil {
		t.Fatal(err)
	}
	if in.Namespace() != "fake-ns" {
		t.Errorf("namespace = %q", in.Namespace())
	}
	_ = in.(interface{ Close() error }).Close()
	log, _ := os.ReadFile(calls)
	for _, want := range []string{"image inspect", "ps -a --filter label=" + LabelInstallation + "=shop", "run -i --rm"} {
		if !strings.Contains(string(log), want) {
			t.Errorf("the runtime was not called with %q:\n%s", want, log)
		}
	}

	t.Setenv("PACK_FAKE_LABELS", `{"`+LabelTarget+`":"k8s","`+LabelProtocol+`":"`+Protocol+`"}`)
	if _, err := target.Open("fake", "shop", map[string]string{"runtime_type": "docker", "runtime_image": "reg/k8s@sha256:1"}); err == nil ||
		!strings.Contains(err.Error(), "image of target") {
		t.Errorf("another target's image: %v", err)
	}
	if _, err := target.Open("fake", "shop", map[string]string{"runtime_type": "docker"}); err == nil || !strings.Contains(err.Error(), "pack refresh") {
		t.Errorf("no image: %v", err)
	}
}
