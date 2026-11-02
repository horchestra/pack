package remote

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/backoff"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/horchestra/pack/pkg/render"
	"github.com/horchestra/pack/pkg/target"
	"github.com/horchestra/pack/pkg/target/remote/remotepb"
)

const (
	// EnvPath lists the directories searched for the programs of targets. Unset, it is .pack/bin of the release the
	// command runs in — where `pack init` downloads them.
	EnvPath = "PACK_TARGET_PATH"
	// DefaultDir is what EnvPath defaults to, relative to the directory pack runs in.
	DefaultDir = ".pack/bin"
	// Prefix names a target binary: pack-target-<target>.
	Prefix = "pack-target-"

	// EnvRuntime names the container runtime that runs a target image; unset — podman, else docker, whichever is on
	// PATH.
	EnvRuntime = "PACK_CONTAINER_RUNTIME"

	binaryStart = 10 * time.Second
	imageStart  = 3 * time.Minute // the runtime may pull the image first
	imagePort   = "7353"          // where a target listens inside its container
)

// Dirs are the directories the programs of targets are searched in: PACK_TARGET_PATH, else .pack/bin here.
func Dirs() []string {
	if p := os.Getenv(EnvPath); len(p) > 0 {
		return filepath.SplitList(p)
	}
	abs, err := filepath.Abs(DefaultDir)
	if err != nil {
		return []string{DefaultDir}
	}
	return []string{abs}
}

// Find locates pack-target-<name> in Dirs.
func Find(name string) (string, error) {
	bin := Prefix + name
	dirs := Dirs()
	for _, d := range dirs {
		p := filepath.Join(d, bin)
		if fi, err := os.Stat(p); err == nil && fi.Mode().IsRegular() && fi.Mode()&0o111 != 0 {
			return p, nil
		}
	}
	return "", fmt.Errorf("target %q: no %s in %s — `pack init` downloads it from the source release.yaml names under targets:", name, bin, strings.Join(dirs, string(filepath.ListSeparator)))
}

// Plugin is a provider running in its own process, as a target.Target.
// Close it when done: it stops the process.
type Plugin struct {
	name   string
	what   string // the binary or the image, for messages
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	exited chan error // the process's one Wait
	dir    string
	kill   func() // stops what Close could not
	conn   *grpc.ClientConn
	client remotepb.TargetClient
	caps   []string
}

// Launch finds and starts the provider of a target and shakes hands with it.
func Launch(name string) (*Plugin, error) { return Open(name, "") }

// Open starts the provider of a target from spec: a container image as
// oci://<host>/<repo>:<tag>, a binary by its path, or — empty — the binary
// pack-target-<name> wherever Find finds it.
func Open(name, spec string) (*Plugin, error) {
	switch {
	case strings.HasPrefix(spec, "oci://"):
		if rest := strings.TrimPrefix(spec, "oci://"); !strings.Contains(strings.SplitN(rest, "/", 2)[0], ".") && !strings.Contains(strings.SplitN(rest, "/", 2)[0], ":") && !strings.HasPrefix(rest, "localhost") {
			return nil, fmt.Errorf("target %s: %s names no registry — a container runtime runs an image from a registry (oci://host/repo:tag)", name, spec)
		}
		return StartImage(name, strings.TrimPrefix(spec, "oci://"))
	case len(spec) > 0:
		return Start(name, spec)
	}
	bin, err := Find(name)
	if err != nil {
		return nil, err
	}
	return Start(name, bin)
}

// Start runs the provider binary at bin for the target name; it listens on
// a unix socket in a private directory.
func Start(name, bin string) (*Plugin, error) {
	dir, err := os.MkdirTemp("", "pack-target-")
	if err != nil {
		return nil, err
	}
	sock := filepath.Join(dir, "provider.sock")
	token, err := newToken()
	if err != nil {
		_ = os.RemoveAll(dir)
		return nil, err
	}
	cmd := exec.Command(bin)
	cmd.Env = append(os.Environ(), EnvAddress+"=unix://"+sock, EnvToken+"="+token)
	p := &Plugin{name: name, what: bin, cmd: cmd, dir: dir}
	return p.start("unix://"+sock, token, binaryStart)
}

// startTCP runs a provider binary listening on TCP, as an image's does — for tests.
func startTCP(name, bin string) (*Plugin, error) {
	port, err := freePort()
	if err != nil {
		return nil, err
	}
	token, err := newToken()
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(bin)
	cmd.Env = append(os.Environ(), EnvAddress+"=tcp://127.0.0.1:"+port, EnvToken+"="+token)
	p := &Plugin{name: name, what: bin, cmd: cmd}
	return p.start("127.0.0.1:"+port, token, binaryStart)
}

// StartImage runs the provider image for the target name in a container:
// podman or docker (PACK_CONTAINER_RUNTIME), the provider listening on TCP
// inside, published on a free port of 127.0.0.1 only. The runtime pulls the
// image when it does not have it.
func StartImage(name, image string) (*Plugin, error) {
	runtime, err := containerRuntime()
	if err != nil {
		return nil, err
	}
	port, err := freePort()
	if err != nil {
		return nil, err
	}
	token, err := newToken()
	if err != nil {
		return nil, err
	}
	cname := "pack-target-" + name + "-" + token[:12]
	cmd := exec.Command(runtime, "run", "-i", "--rm", "--name", cname,
		"-p", "127.0.0.1:"+port+":"+imagePort,
		"-e", EnvAddress+"=tcp://0.0.0.0:"+imagePort, "-e", EnvToken+"="+token,
		image)
	p := &Plugin{name: name, what: image, cmd: cmd,
		kill: func() { _ = exec.Command(runtime, "kill", cname).Run() }}
	return p.start("127.0.0.1:"+port, token, imageStart)
}

// start runs the process and shakes hands with it at addr, retrying until it
// answers, it exits, or the time is up.
func (p *Plugin) start(addr, token string, within time.Duration) (*Plugin, error) {
	p.cmd.Stdout = os.Stderr // a provider has nothing to say on pack's stdout
	detach(p.cmd)
	p.cmd.Stderr = os.Stderr
	var err error
	if p.stdin, err = p.cmd.StdinPipe(); err != nil {
		p.cleanup()
		return nil, err
	}
	if err := p.cmd.Start(); err != nil {
		p.cleanup()
		return nil, fmt.Errorf("target %s: %w", p.name, err)
	}
	p.exited = make(chan error, 1)
	go func() { p.exited <- p.cmd.Wait() }()
	auth := func(ctx context.Context, method string, req, reply any, cc *grpc.ClientConn, invoker grpc.UnaryInvoker, opts ...grpc.CallOption) error {
		return invoker(metadata.AppendToOutgoingContext(ctx, TokenKey, token), method, req, reply, cc, opts...)
	}
	authStream := func(ctx context.Context, desc *grpc.StreamDesc, cc *grpc.ClientConn, method string, streamer grpc.Streamer, opts ...grpc.CallOption) (grpc.ClientStream, error) {
		return streamer(metadata.AppendToOutgoingContext(ctx, TokenKey, token), desc, cc, method, opts...)
	}
	// the provider is starting: try often, not after gRPC's usual second
	connect := grpc.ConnectParams{Backoff: backoff.Config{BaseDelay: 20 * time.Millisecond, Multiplier: 1.5, MaxDelay: time.Second}, MinConnectTimeout: time.Second}
	if p.conn, err = grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithConnectParams(connect),
		grpc.WithUnaryInterceptor(auth), grpc.WithStreamInterceptor(authStream),
		grpc.WithDefaultCallOptions(grpc.MaxCallRecvMsgSize(MaxMessage), grpc.MaxCallSendMsgSize(MaxMessage))); err != nil {
		p.Close()
		return nil, err
	}
	p.client = remotepb.NewTargetClient(p.conn)
	deadline := time.Now().Add(within)
	var hs *remotepb.HandshakeResponse
	for {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		hs, err = p.client.Handshake(ctx, &remotepb.HandshakeRequest{Protocol: Protocol}, grpc.WaitForReady(true))
		cancel()
		if err == nil {
			break
		}
		if s, ok := status.FromError(err); ok && s.Code() != codes.Unavailable && s.Code() != codes.DeadlineExceeded {
			p.Close()
			return nil, fmt.Errorf("target %s: handshake: %w", p.name, unwrap(err))
		}
		select {
		case werr := <-p.exited:
			p.exited <- werr
			p.Close()
			return nil, fmt.Errorf("target %s: %s exited before answering: %v", p.name, p.what, werr)
		default:
		}
		if time.Now().After(deadline) {
			p.Close()
			return nil, fmt.Errorf("target %s: %s did not answer within %s: %v", p.name, p.what, within, unwrap(err))
		}
		time.Sleep(50 * time.Millisecond)
	}
	if hs.Protocol != Protocol || hs.Target != p.name {
		p.Close()
		return nil, fmt.Errorf("target %s: %s is target %q speaking %s, wanted target %q speaking %s", p.name, p.what, hs.Target, hs.Protocol, p.name, Protocol)
	}
	p.caps = hs.Capabilities
	return p, nil
}

func (p *Plugin) cleanup() {
	if len(p.dir) > 0 {
		_ = os.RemoveAll(p.dir)
	}
}

func containerRuntime() (string, error) {
	if r := os.Getenv(EnvRuntime); len(r) > 0 {
		return exec.LookPath(r)
	}
	for _, r := range []string{"podman", "docker"} {
		if p, err := exec.LookPath(r); err == nil {
			return p, nil
		}
	}
	return "", fmt.Errorf("a provider image needs a container runtime: no podman or docker on PATH (%s names another)", EnvRuntime)
}

func freePort() (string, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", err
	}
	defer func() { _ = l.Close() }()
	_, port, err := net.SplitHostPort(l.Addr().String())
	return port, err
}

func newToken() (string, error) {
	var b [24]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

// Close ends the provider: its stdin closes, it stops serving and exits.
func (p *Plugin) Close() {
	if p.conn != nil {
		_ = p.conn.Close()
	}
	if p.stdin != nil {
		_ = p.stdin.Close()
	}
	if p.exited != nil {
		select {
		case <-p.exited:
		case <-time.After(5 * time.Second):
			if p.kill != nil {
				p.kill()
			}
			_ = p.cmd.Process.Kill()
			<-p.exited
		}
	}
	p.cleanup()
}

func (p *Plugin) Name() string { return p.name }

// Capabilities are what the provider declared in the handshake.
func (p *Plugin) Capabilities() []string { return p.caps }

// Configure binds the target to one installation.
func (p *Plugin) Configure(installation string, settings map[string]string) (target.Installation, error) {
	resp, err := p.client.Configure(context.Background(), &remotepb.ConfigureRequest{Installation: installation, Settings: settings})
	if err != nil {
		return nil, unwrap(err)
	}
	return &remote{p: p, namespace: resp.Namespace}, nil
}

// remote is the target bound to an installation, as a target.Installation. It also keeps the record and says
// what it holds when the target declared the capabilities; closing it stops the process.
type remote struct {
	p         *Plugin
	namespace string
}

func (r *remote) Namespace() string { return r.namespace }
func (r *remote) Close() error      { r.p.Close(); return nil }

func (r *remote) Resolve(key string, pl render.Placement) (*render.Resolved, error) {
	raw, err := placementTo(pl)
	if err != nil {
		return nil, err
	}
	resp, err := r.p.client.Resolve(context.Background(), &remotepb.ResolveRequest{Key: key, PlacementJson: raw})
	if err != nil {
		return nil, unwrap(err)
	}
	return resolvedFrom(resp), nil
}

func (r *remote) Execute(ctx context.Context, step target.Step, events func(string)) error {
	req, err := stepTo(step)
	if err != nil {
		return err
	}
	stream, err := r.p.client.Execute(ctx, req)
	if err != nil {
		return unwrap(err)
	}
	for {
		ev, err := stream.Recv()
		if err != nil {
			if errors.Is(err, io.EOF) {
				return fmt.Errorf("target %s: step %d ended without a result", r.p.name, step.N)
			}
			return unwrap(err)
		}
		if len(ev.Message) > 0 && events != nil {
			events(ev.Message)
		}
		if ev.Done {
			if len(ev.Error) > 0 {
				return errors.New(ev.Error)
			}
			return nil
		}
	}
}

func (r *remote) ReadInstalled() ([]byte, bool, error) {
	resp, err := r.p.client.ReadInstalled(context.Background(), &remotepb.ReadInstalledRequest{})
	if err != nil {
		return nil, false, unwrap(err)
	}
	return resp.Record, resp.Exists, nil
}

func (r *remote) WriteInstalled(record []byte) error {
	_, err := r.p.client.WriteInstalled(context.Background(), &remotepb.WriteInstalledRequest{Record: record})
	return unwrap(err)
}

func (r *remote) DeleteInstalled() error {
	_, err := r.p.client.DeleteInstalled(context.Background(), &remotepb.DeleteInstalledRequest{})
	return unwrap(err)
}

// Observe asks the target what it holds; a target that did not declare CapObserve cannot tell.
func (r *remote) Observe(ctx context.Context) (map[string]target.Observed, error) {
	if !slices.Contains(r.p.caps, CapObserve) {
		return nil, target.ErrNotObserved
	}
	resp, err := r.p.client.Observe(ctx, &remotepb.ObserveRequest{})
	if err != nil {
		if s, ok := status.FromError(err); ok && s.Code() == codes.Unimplemented {
			return nil, target.ErrNotObserved
		}
		return nil, unwrap(err)
	}
	out := make(map[string]target.Observed, len(resp.Held))
	for k, v := range resp.Held {
		out[k] = target.Observed{Image: v.Image, Replicas: int(v.Replicas)}
	}
	return out, nil
}

// unwrap turns a gRPC status into the target's own message.
func unwrap(err error) error {
	if err == nil {
		return nil
	}
	if s, ok := status.FromError(err); ok {
		return errors.New(strings.TrimSpace(s.Message()))
	}
	return err
}

func init() {
	// a target not compiled into pack is a program of its own: pack-target-<name>, or what the `provider` setting
	// names (a binary's path, or oci://host/repo:tag of an image)
	target.Fallback = func(name, installation string, settings map[string]string) (target.Installation, error) {
		p, err := Open(name, settings["provider"])
		if err != nil {
			return nil, err
		}
		in, err := p.Configure(installation, settings)
		if err != nil {
			p.Close()
			return nil, err
		}
		return in, nil
	}
}

var (
	_ target.Installation = (*remote)(nil)
	_ target.Recorder     = (*remote)(nil)
	_ target.Observer     = (*remote)(nil)
)
