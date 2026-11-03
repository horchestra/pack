package remote

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
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
	// command runs in — where `pack refresh` downloads them.
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
	imagePort   = ImagePort
)

// ImagePort is where a target listens in its container, its image's EXPOSE.
const ImagePort = "7353"

// ImageStart is how long an image is given to answer: the runtime may pull it first.
const ImageStart = imageStart

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
	return "", fmt.Errorf("target %q: no %s in %s — `pack refresh` downloads it from the source release.yaml names under targets: %w", name, bin, strings.Join(dirs, string(filepath.ListSeparator)), target.ErrNotBrought)
}

// Plugin is a provider running in its own process, as a target.Target.
// Close it when done: it stops the process.
type Plugin struct {
	name    string
	what    string // the binary or the image, for messages
	cmd     *exec.Cmd
	stdin   io.WriteCloser
	exited  chan error // the process's one Wait
	dir     string
	kill    func() // stops what Close could not
	stop    func() // a runtime with no process of pack's (k8s): takes away what it made, on Close
	closing sync.Once
	conn    *grpc.ClientConn
	client  remotepb.TargetClient
	caps    []string
}

// Launch finds the program of a target in .pack/bin (Find), starts it and shakes hands with it.
func Launch(name string) (*Plugin, error) { return Runtime{Type: RuntimeNative}.Start(name, "") }

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
	return p.dial(addr, token, within)
}

// Connect shakes hands with a target that a runtime started where pack has no process of it — a Pod — at addr:
// what (the image) names it in messages; exited, when not nil, says it ended before answering; stop takes away
// what the runtime made, when the plugin is closed or fails to answer.
func Connect(name, what, addr, token string, within time.Duration, exited chan error, stop func()) (*Plugin, error) {
	p := &Plugin{name: name, what: what, exited: exited, stop: stop}
	return p.dial(addr, token, within)
}

// NewToken is a token a target is given to require of pack.
func NewToken() (string, error) { return newToken() }

// dial shakes hands with the target at addr, retrying until it answers, it exits, or the time is up.
func (p *Plugin) dial(addr, token string, within time.Duration) (*Plugin, error) {
	var err error
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

// Close ends the provider: its stdin closes, it stops serving and exits. A second Close does nothing.
func (p *Plugin) Close() { p.closing.Do(p.close) }

func (p *Plugin) close() {
	if p.conn != nil {
		_ = p.conn.Close()
	}
	if p.stdin != nil {
		_ = p.stdin.Close()
	}
	if p.stop != nil {
		p.stop()
		p.stop = nil
	}
	if p.exited != nil && p.cmd != nil {
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

// Discover asks the target the facts of where it installs; a target that did not declare CapDiscover cannot tell.
func (r *remote) Discover(ctx context.Context, hosts []string) (*target.Facts, error) {
	if !slices.Contains(r.p.caps, CapDiscover) {
		return nil, target.ErrNoFacts
	}
	resp, err := r.p.client.Discover(ctx, &remotepb.DiscoverRequest{Hosts: hosts})
	if err != nil {
		if s, ok := status.FromError(err); ok && s.Code() == codes.Unimplemented {
			return nil, target.ErrNoFacts
		}
		return nil, unwrap(err)
	}
	var f target.Facts
	if err := json.Unmarshal(resp.Facts, &f); err != nil {
		return nil, fmt.Errorf("the facts of target %s: %w", r.p.name, err)
	}
	return &f, nil
}

// Logs asks the target the tail of a key's logs; a target that did not declare CapLogs gives none.
func (r *remote) Logs(ctx context.Context, key string, lines int) (map[string][]string, error) {
	if !slices.Contains(r.p.caps, CapLogs) {
		return nil, target.ErrNoLogs
	}
	resp, err := r.p.client.Logs(ctx, &remotepb.LogsRequest{Key: key, Lines: int64(lines)})
	if err != nil {
		if s, ok := status.FromError(err); ok && s.Code() == codes.Unimplemented {
			return nil, target.ErrNoLogs
		}
		return nil, unwrap(err)
	}
	out := map[string][]string{}
	if err := json.Unmarshal(resp.Logs, &out); err != nil {
		return nil, fmt.Errorf("the logs of target %s: %w", r.p.name, err)
	}
	return out, nil
}

// Health asks the target how the keys are; a target that did not declare CapHealth cannot tell.
func (r *remote) Health(ctx context.Context) (map[string]target.Health, error) {
	if !slices.Contains(r.p.caps, CapHealth) {
		return nil, target.ErrNoHealth
	}
	resp, err := r.p.client.Health(ctx, &remotepb.HealthRequest{})
	if err != nil {
		if s, ok := status.FromError(err); ok && s.Code() == codes.Unimplemented {
			return nil, target.ErrNoHealth
		}
		return nil, unwrap(err)
	}
	out := map[string]target.Health{}
	if err := json.Unmarshal(resp.Health, &out); err != nil {
		return nil, fmt.Errorf("the health of target %s: %w", r.p.name, err)
	}
	return out, nil
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
		return &target.Error{Err: errors.New(strings.TrimSpace(s.Message()))}
	}
	return &target.Error{Err: err}
}

func init() {
	// a target not compiled into pack is a program of its own: pack-target-<name> of .pack/bin, or its image in a
	// container — the runtime section of the target's entry says which; the target is configured without it
	target.Fallback = func(name, installation string, settings map[string]string) (target.Installation, error) {
		rt, rest, err := RuntimeOf(settings)
		if err != nil {
			return nil, fmt.Errorf("target %s: %w", name, err)
		}
		p, err := rt.Start(name, installation)
		if err != nil {
			if errors.Is(err, target.ErrNotBrought) {
				return nil, err
			}
			return nil, &target.Error{Err: err} // it did not start, or did not answer
		}
		in, err := p.Configure(installation, rest)
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
	_ target.Discoverer   = (*remote)(nil)
	_ target.Logger       = (*remote)(nil)
	_ target.Checker      = (*remote)(nil)
)
