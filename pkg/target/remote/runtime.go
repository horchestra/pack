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

	"github.com/horchestra/pack/pkg/target"
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

// The settings pack itself puts into a target's set: the image its lock pins (host/repo@sha256:…), and the directory
// of the release the container has for its home — what the program keeps in ~ (the ansible target's state) outlives
// the container.
const (
	SettingImage = RuntimePrefix + "image"
	SettingHome  = RuntimePrefix + "home"
)

// ImageHome is the home of a target's program in its image: HOME of the image, where pack mounts the release's
// directory for it.
const ImageHome = "/home/pack"

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
//	  user: host                   # host — pack's uid:gid (Linux's default), image — the image's (elsewhere)
type Runtime struct {
	Type     string
	Image    string // docker: what the lock pins (SettingImage)
	Home     string // docker: the release's directory for the container's home (SettingHome)
	Network  string
	Mounts   []string
	SSHAgent bool
	Env      []string
	User     string // UserHost | UserImage; "" — the machine's default (defaultUser)
}

// Who the program runs as in its container.
const (
	// UserHost is pack's own uid and gid, with the groups of the sockets mounted: where a mount is the user's files
	// as they are (an engine on Linux itself), the home pack made, a key of 0600 and the engine's socket are the
	// program's only so. A rootless runtime gets the same by its own means (rootlessOf).
	UserHost = "host"
	// UserImage is the image's user: Docker Desktop (a Mac, Windows) gives a mount to any user of the container, and
	// its engine's socket to root alone. On Linux the image's user cannot write the home pack made.
	UserImage = "image"
)

// defaultUser is UserHost on Linux, where the engine is usually the machine's own, and UserImage elsewhere, where it
// is Docker Desktop's VM. Docker Desktop for Linux is a VM too: runtime.user: image says so for it.
func defaultUser() string {
	if runtime.GOOS == "linux" {
		return UserHost
	}
	return UserImage
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
		case "home":
			rt.Home = v
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
		case "user":
			if v != UserHost && v != UserImage {
				return rt, nil, fmt.Errorf("runtime.user: %q is not host or image", v)
			}
			rt.User = v
		default:
			return rt, nil, fmt.Errorf("runtime.%s: not a setting of the runtime (type, network, mounts, ssh_agent, env, user)", name)
		}
	}
	switch rt.Type {
	case RuntimeNative:
		if len(rt.Network) > 0 || len(rt.Mounts) > 0 || rt.SSHAgent || len(rt.Env) > 0 || len(rt.User) > 0 {
			return rt, nil, errors.New("runtime: network, mounts, ssh_agent, env and user are a container's: runtime.type: docker")
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
			return nil, fmt.Errorf("target %s: runtime docker, and no image for it: name it in release.yaml (targets: %s: {image: oci://…}): %w", name, name, target.ErrNotBrought)
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
	owner              string                        // <host>/<pid>
	sshSock            string                        // the agent's socket as the container runtime reaches it; "" — none
	rootless           string                        // "podman" | "docker": the runtime is rootless (rootlessOf)
	lookup             func(string) bool             // whether a variable is set
	uid, gid           int                           // pack's, for UserHost
	group              func(path string) (int, bool) // the group of a unix socket of the machine; false — not one
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
	user := rt.User
	if len(user) == 0 {
		user = defaultUser()
	}
	switch {
	case user != UserHost:
	case c.rootless == "podman":
		// the user's own uid and groups, mapped as they are: in a rootless userns a --user of the machine's maps to a
		// subuid that owns nothing
		args = append(args, "--userns=keep-id", "--group-add", "keep-groups")
	case c.rootless == "docker":
		// rootless docker maps the container's root to the user who runs the engine: the machine's files are root's
		args = append(args, "--user", "0:0")
	default:
		args = append(args, "--user", strconv.Itoa(c.uid)+":"+strconv.Itoa(c.gid))
		// a socket mounted (the engine's, the agent's) is reached by its group
		var socks []string
		for _, m := range rt.Mounts {
			src, err := source(strings.SplitN(m, ":", 2)[0])
			if err != nil {
				return nil, "", fmt.Errorf("runtime.mounts: %w", err)
			}
			socks = append(socks, src)
		}
		if rt.SSHAgent && len(c.sshSock) > 0 {
			socks = append(socks, c.sshSock)
		}
		seen := map[int]bool{c.gid: true}
		for _, s := range socks {
			if g, ok := c.group(s); ok && !seen[g] {
				seen[g] = true
				args = append(args, "--group-add", strconv.Itoa(g))
			}
		}
	}
	if len(rt.Home) > 0 {
		args = append(args, "-v", rt.Home+":"+ImageHome, "-e", "HOME="+ImageHome)
	}
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
	src, err := source(parts[0])
	if err != nil {
		return "", err
	}
	parts[0] = src
	if !filepath.IsAbs(parts[0]) || !strings.HasPrefix(parts[1], "/") {
		return "", fmt.Errorf("%q: both paths absolute (~/ is the home)", m)
	}
	return strings.Join(parts, ":"), nil
}

// source is the source of a mount as the machine has it: ~/ the home.
func source(s string) (string, error) {
	rest, ok := strings.CutPrefix(s, "~/")
	if !ok {
		return s, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, rest), nil
}

// rootlessOf says whether the container runtime runs rootless — its root is the user who runs it — and which:
// "podman", "docker", or "" when it is not (or cannot tell).
func rootlessOf(containerRuntime string) string {
	name := filepath.Base(containerRuntime)
	switch {
	case strings.Contains(name, "podman"):
		out, err := exec.Command(containerRuntime, "info", "--format", "{{.Host.Security.Rootless}}").Output()
		if err == nil && strings.TrimSpace(string(out)) == "true" {
			return "podman"
		}
	case strings.Contains(name, "docker"):
		out, err := exec.Command(containerRuntime, "info", "--format", "{{json .SecurityOptions}}").Output()
		if err == nil && strings.Contains(string(out), "name=rootless") {
			return "docker"
		}
	}
	return ""
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
		uid:    os.Getuid(), gid: os.Getgid(), group: socketGroup, rootless: rootlessOf(cr),
	}
	args, addr, err := containerArgs(c, rt)
	if err != nil {
		return nil, fmt.Errorf("target %s: %w", name, err)
	}
	cmd := exec.Command(cr, args...)
	p := &Plugin{name: name, what: rt.Image, cmd: cmd, kill: func() { _ = exec.Command(cr, "kill", c.cname).Run() }}
	return p.start(addr, token, imageStart)
}
