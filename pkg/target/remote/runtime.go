package remote

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
)

// The runtimes a target's program runs in.
const (
	RuntimeNative = "native" // the program of .pack/bin, a process of the machine pack runs on
	RuntimeDocker = "docker" // the target's image, in a container of podman or docker
)

// The labels of a target's image and of the containers pack runs it in.
const (
	LabelTarget       = "io.horchestra.pack.target"       // the image: the target it is
	LabelProtocol     = "io.horchestra.pack.protocol"     // the image: the protocol it speaks
	LabelInstallation = "io.horchestra.pack.installation" // the container: the installation it serves
	LabelOwner        = "io.horchestra.pack.owner"        // the container: <host>/<pid> of the pack that runs it
)

// RuntimePrefix begins the settings of the runtime section of a target in env.yaml: pack's, cut out of the set
// before the target is configured.
const RuntimePrefix = "runtime_"

// SettingImage is the setting pack itself puts into a target's set: the image its lock pins, host/repo@sha256:….
const SettingImage = RuntimePrefix + "image"

// Runtime is where a target's program runs: the section runtime of the target's entry in env.yaml.
//
//	runtime:
//	  type: docker                 # native (the default) | docker
//	  network: host                # the container's network; host — the machine's
//	  mounts:                      # what the program reads of the machine: source:destination[:ro]
//	    - ~/.ssh/known_hosts:/home/pack/.ssh/known_hosts:ro
//	  ssh_agent: true              # the agent of the machine, in the container
//	  env:                         # variables passed in, by name — never their values in the file
//	    - ANSIBLE_VAULT_PASSWORD
type Runtime struct {
	Type     string
	Image    string // docker: what the lock pins (SettingImage)
	Network  string
	Mounts   []string
	SSHAgent bool
	Env      []string
}

// RuntimeOf reads the runtime out of a target's set and gives the rest of the set: what the target is configured
// with. An old provider setting is refused with the way it is said now.
func RuntimeOf(settings map[string]string) (Runtime, map[string]string, error) {
	rt := Runtime{Type: RuntimeNative}
	rest := map[string]string{}
	for k, v := range settings {
		name, ok := strings.CutPrefix(k, RuntimePrefix)
		if !ok {
			if k == "provider" {
				return rt, nil, errors.New("provider: a target's program is said by runtime: (type: native | docker) in env.yaml and image: in release.yaml targets:")
			}
			rest[k] = v
			continue
		}
		switch name {
		case "type":
			rt.Type = v
		case "image":
			rt.Image = v
		case "network":
			rt.Network = v
		case "mounts":
			rt.Mounts = list(v)
		case "ssh_agent":
			b, err := strconv.ParseBool(v)
			if err != nil {
				return rt, nil, fmt.Errorf("runtime.ssh_agent: %q is not true or false", v)
			}
			rt.SSHAgent = b
		case "env":
			rt.Env = list(v)
		default:
			return rt, nil, fmt.Errorf("runtime.%s: not a setting of the runtime (type, network, mounts, ssh_agent, env)", name)
		}
	}
	switch rt.Type {
	case RuntimeNative:
		if len(rt.Network) > 0 || len(rt.Mounts) > 0 || rt.SSHAgent || len(rt.Env) > 0 {
			return rt, nil, errors.New("runtime: network, mounts, ssh_agent and env are a container's: runtime.type: docker")
		}
	case RuntimeDocker:
	default:
		return rt, nil, fmt.Errorf("runtime.type: %q is not native or docker", rt.Type)
	}
	return rt, rest, nil
}

// list is a list setting of env.yaml as the set has it: its items joined by commas.
func list(v string) []string {
	var out []string
	for s := range strings.SplitSeq(v, ",") {
		if s = strings.TrimSpace(s); len(s) > 0 {
			out = append(out, s)
		}
	}
	return out
}

// Start runs the program of the target name in the runtime, for the installation, and shakes hands with it.
func (rt Runtime) Start(name, installation string) (*Plugin, error) {
	if rt.Type == RuntimeDocker {
		if len(rt.Image) == 0 {
			return nil, fmt.Errorf("target %s: runtime docker, and no image for it: name it in release.yaml (targets: %s: {image: oci://…}) and run pack refresh", name, name)
		}
		return startContainer(name, installation, rt)
	}
	bin, err := Find(name)
	if err != nil {
		return nil, err
	}
	return Start(name, bin)
}

// container is what a run of a target's image needs to know besides the runtime section.
type container struct {
	name, installation string
	cname, port, token string
	owner              string            // <host>/<pid>
	sshSock            string            // the agent's socket as the container runtime reaches it; "" — none
	lookup             func(string) bool // whether a variable is set
}

// containerArgs is the command line of the container runtime that runs the target's image: where it listens and how
// pack reaches it, what it is given of the machine, labelled so that pack finds it again.
func containerArgs(c container, rt Runtime) (args []string, addr string, err error) {
	args = []string{"run", "-i", "--rm", "--name", c.cname,
		"--label", LabelInstallation + "=" + c.installation, "--label", LabelTarget + "=" + c.name, "--label", LabelOwner + "=" + c.owner}
	if rt.Network == "host" {
		// the machine's network: the program listens on the machine's loopback itself
		args = append(args, "--network", "host", "-e", EnvAddress+"=tcp://127.0.0.1:"+c.port)
	} else {
		if len(rt.Network) > 0 {
			args = append(args, "--network", rt.Network)
		}
		args = append(args, "-p", "127.0.0.1:"+c.port+":"+imagePort, "-e", EnvAddress+"=tcp://0.0.0.0:"+imagePort)
	}
	args = append(args, "-e", EnvToken+"="+c.token)
	for _, m := range rt.Mounts {
		v, err := mount(m)
		if err != nil {
			return nil, "", fmt.Errorf("runtime.mounts: %w", err)
		}
		args = append(args, "-v", v)
	}
	if rt.SSHAgent {
		if len(c.sshSock) == 0 {
			return nil, "", errors.New("runtime.ssh_agent: no agent — SSH_AUTH_SOCK is not set")
		}
		args = append(args, "-v", c.sshSock+":/run/pack/ssh-agent.sock", "-e", "SSH_AUTH_SOCK=/run/pack/ssh-agent.sock")
	}
	for _, n := range rt.Env {
		if !c.lookup(n) {
			return nil, "", fmt.Errorf("runtime.env: %s is not set", n)
		}
		args = append(args, "-e", n) // by name: the value comes from pack's environment, never the command line
	}
	return append(args, rt.Image), "127.0.0.1:" + c.port, nil
}

// mount is a mount of the machine's: source:destination[:ro], ~ the home, the source absolute.
func mount(m string) (string, error) {
	parts := strings.Split(m, ":")
	if len(parts) < 2 || len(parts) > 3 || len(parts) == 3 && parts[2] != "ro" && parts[2] != "rw" {
		return "", fmt.Errorf("%q: source:destination, :ro or :rw after", m)
	}
	if rest, ok := strings.CutPrefix(parts[0], "~/"); ok {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		parts[0] = filepath.Join(home, rest)
	}
	if !filepath.IsAbs(parts[0]) || !strings.HasPrefix(parts[1], "/") {
		return "", fmt.Errorf("%q: both paths absolute (~/ is the home)", m)
	}
	return strings.Join(parts, ":"), nil
}

// sshAgentSocket is the machine's agent as a container reaches it: Docker Desktop on a Mac has its own socket for it.
func sshAgentSocket(containerRuntime string) string {
	if runtime.GOOS == "darwin" && filepath.Base(containerRuntime) == "docker" {
		return "/run/host-services/ssh-auth.sock"
	}
	return os.Getenv("SSH_AUTH_SOCK")
}

// checkLabels: the image is the target's, speaking pack's protocol — said before it runs, not after a handshake.
func checkLabels(image, name string, labels map[string]string) error {
	if got := labels[LabelTarget]; got != name {
		if len(got) == 0 {
			return fmt.Errorf("target %s: %s is no target's image (no label %s)", name, image, LabelTarget)
		}
		return fmt.Errorf("target %s: %s is the image of target %q", name, image, got)
	}
	if got := labels[LabelProtocol]; got != Protocol {
		return fmt.Errorf("target %s: %s speaks %q, pack speaks %s (label %s)", name, image, got, Protocol, LabelProtocol)
	}
	return nil
}

func imageLabels(containerRuntime, image string) (map[string]string, error) {
	out, err := exec.Command(containerRuntime, "image", "inspect", "--format", "{{json .Config.Labels}}", image).Output()
	if err != nil {
		if pull, perr := exec.Command(containerRuntime, "pull", image).CombinedOutput(); perr != nil {
			return nil, fmt.Errorf("%s pull %s: %v: %s", filepath.Base(containerRuntime), image, perr, strings.TrimSpace(string(pull)))
		}
		if out, err = exec.Command(containerRuntime, "image", "inspect", "--format", "{{json .Config.Labels}}", image).Output(); err != nil {
			return nil, fmt.Errorf("%s image inspect %s: %w", filepath.Base(containerRuntime), image, err)
		}
	}
	labels := map[string]string{}
	if s := strings.TrimSpace(string(out)); s != "null" && len(s) > 0 {
		if err := json.Unmarshal([]byte(s), &labels); err != nil {
			return nil, fmt.Errorf("the labels of %s: %w", image, err)
		}
	}
	return labels, nil
}

// orphans are the containers of the target for the installation that a pack of this machine left running when it
// ended without stopping them: their owner is gone. Another pack's that runs is none of them.
func orphans(ps string, host string, alive func(int) bool) []string {
	var out []string
	for l := range strings.SplitSeq(strings.TrimSpace(ps), "\n") {
		id, owner, ok := strings.Cut(strings.TrimSpace(l), " ")
		if !ok {
			continue
		}
		h, p, ok := strings.Cut(owner, "/")
		pid, err := strconv.Atoi(p)
		if !ok || err != nil || h != host || alive(pid) {
			continue
		}
		out = append(out, id)
	}
	return out
}

func removeOrphans(containerRuntime, installation, name string) {
	host, _ := os.Hostname()
	out, err := exec.Command(containerRuntime, "ps", "-a",
		"--filter", "label="+LabelInstallation+"="+installation, "--filter", "label="+LabelTarget+"="+name,
		"--format", `{{.ID}} {{.Label "`+LabelOwner+`"}}`).Output()
	if err != nil {
		return // nothing to tell: the run says what is wrong with the runtime
	}
	if ids := orphans(string(out), host, alive); len(ids) > 0 {
		_ = exec.Command(containerRuntime, append([]string{"rm", "-f"}, ids...)...).Run()
	}
}

func startContainer(name, installation string, rt Runtime) (*Plugin, error) {
	cr, err := containerRuntime()
	if err != nil {
		return nil, err
	}
	labels, err := imageLabels(cr, rt.Image)
	if err != nil {
		return nil, fmt.Errorf("target %s: %w", name, err)
	}
	if err := checkLabels(rt.Image, name, labels); err != nil {
		return nil, err
	}
	removeOrphans(cr, installation, name)
	port, err := freePort()
	if err != nil {
		return nil, err
	}
	token, err := newToken()
	if err != nil {
		return nil, err
	}
	host, _ := os.Hostname()
	c := container{
		name: name, installation: installation, cname: Prefix + name + "-" + token[:12], port: port, token: token,
		owner: host + "/" + strconv.Itoa(os.Getpid()), sshSock: sshAgentSocket(cr),
		lookup: func(n string) bool { _, ok := os.LookupEnv(n); return ok },
	}
	args, addr, err := containerArgs(c, rt)
	if err != nil {
		return nil, fmt.Errorf("target %s: %w", name, err)
	}
	cmd := exec.Command(cr, args...)
	p := &Plugin{name: name, what: rt.Image, cmd: cmd, kill: func() { _ = exec.Command(cr, "kill", c.cname).Run() }}
	return p.start(addr, token, imageStart)
}
