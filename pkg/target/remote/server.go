// Package remote runs a target in its own process — a binary, or a container image when the target is written in a
// language whose runtime the machine running pack does not have. A target written in Go calls Serve from its main
// with its target.Factory; pack asks Launch (the fallback of target.Open for a target not compiled in), which starts
// the binary or the image and gives back a target.Installation that asks it over gRPC (proto/target.proto). Neither
// side links the other: pack knows no target, a target knows the render's types and nothing of the CLI.
//
// What a target in any language is told, in its environment:
//
//	PACK_TARGET_ADDRESS  where to listen: unix:///<path> (a binary) or tcp://<host>:<port> (an image)
//	PACK_TARGET_TOKEN    a token every call carries as the metadata pack-target-token; refuse a call without it
//
// and EOF on stdin means pack is done with it: exit.
package remote

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/horchestra/pack/pkg/logging"
	"github.com/horchestra/pack/pkg/render"
	"github.com/horchestra/pack/pkg/target"
	"github.com/horchestra/pack/pkg/target/remote/remotepb"
)

const (
	// Protocol is the version of the target protocol this package speaks.
	Protocol = "pack.target.v1"
	// EnvAddress is where pack tells the target to listen.
	EnvAddress = "PACK_TARGET_ADDRESS"
	// EnvToken is the token every call of pack carries.
	EnvToken = "PACK_TARGET_TOKEN"
	// TokenKey is the metadata key the token travels under.
	TokenKey = "pack-target-token"

	// CapRecord: the target keeps the installation's record in its own system (target.Recorder).
	CapRecord = "record"
	// CapObserve: the target says what it holds (target.Observer).
	CapObserve = "observe"
	// CapDiscover: the target tells the facts of where it installs (target.Discoverer).
	CapDiscover = "discover"
	// CapLogs: the target gives the tail of a key's logs (target.Logger).
	CapLogs = "logs"
	// CapHealth: the target says how the keys are (target.Checker).
	CapHealth = "health"
)

// MaxMessage bounds one message between pack and a target: a unit carries the files of the pack — an archive of
// data among them — far beyond gRPC's default 4 MiB.
const MaxMessage = 2<<30 - 1

// Serve is the whole main of a target binary: it listens where pack told it, serves the target made by f to whoever
// has the token, and returns when pack closes its stdin. caps are what the installation f makes can do beyond
// performing steps: CapRecord if it is a target.Recorder, CapObserve if it is a target.Observer.
func Serve(name string, f target.Factory, caps ...string) error {
	addr := os.Getenv(EnvAddress)
	if len(addr) == 0 {
		return fmt.Errorf("%s is not set: this is a pack target (%s), pack starts it", EnvAddress, name)
	}
	token := os.Getenv(EnvToken)
	if len(token) == 0 {
		return fmt.Errorf("%s is not set: pack gives every target a token", EnvToken)
	}
	var l net.Listener
	var err error
	switch {
	case strings.HasPrefix(addr, "unix://"):
		l, err = net.Listen("unix", strings.TrimPrefix(addr, "unix://"))
	case strings.HasPrefix(addr, "tcp://"):
		l, err = net.Listen("tcp", strings.TrimPrefix(addr, "tcp://"))
	default:
		return fmt.Errorf("%s %q: unix:///<path> or tcp://<host>:<port>", EnvAddress, addr)
	}
	if err != nil {
		return err
	}
	// a target logs as pack tells it (PACK_LOG_LEVEL, PACK_LOG_FORMAT): JSON on stderr, which pack reads back
	if err := logging.Setup(logging.FromEnv(), os.Stderr); err != nil {
		return err
	}
	log := logging.For("serve")
	logUnary, logStream := serverLog("")
	s := grpc.NewServer(grpc.MaxRecvMsgSize(MaxMessage), grpc.MaxSendMsgSize(MaxMessage), grpc.ChainUnaryInterceptor(func(ctx context.Context, req any, _ *grpc.UnaryServerInfo, h grpc.UnaryHandler) (any, error) {
		if err := authorized(ctx, token); err != nil {
			return nil, err
		}
		return h(ctx, req)
	}, logUnary), grpc.ChainStreamInterceptor(func(srv any, ss grpc.ServerStream, _ *grpc.StreamServerInfo, h grpc.StreamHandler) error {
		if err := authorized(ss.Context(), token); err != nil {
			return err
		}
		return h(srv, ss)
	}, logStream))
	log.Debug().Str("target", name).Str("network", l.Addr().Network()).Strs("caps", caps).Msg("listening")
	srv := &server{name: name, f: f, caps: caps}
	defer srv.close()
	remotepb.RegisterTargetServer(s, srv)
	done := make(chan error, 1)
	go func() { done <- s.Serve(l) }()
	go func() {
		// pack holds our stdin; EOF means pack is gone or done with us.
		_, _ = io.Copy(io.Discard, os.Stdin)
		log.Debug().Str("target", name).Msg("stdin closed: stopping")
		s.GracefulStop()
	}()
	if err := <-done; err != nil && !errors.Is(err, grpc.ErrServerStopped) {
		return err
	}
	return nil
}

// authorized: the call carries the token pack gave this target. A target on TCP is reachable by anyone on the
// machine; the token is what makes it pack's.
func authorized(ctx context.Context, token string) error {
	md, _ := metadata.FromIncomingContext(ctx)
	for _, v := range md.Get(TokenKey) {
		if subtle.ConstantTimeCompare([]byte(v), []byte(token)) == 1 {
			return nil
		}
	}
	return status.Error(codes.Unauthenticated, "no or wrong "+TokenKey)
}

// server answers for one target, bound to one installation by Configure.
type server struct {
	remotepb.UnimplementedTargetServer
	name string
	f    target.Factory
	caps []string

	mu   sync.Mutex
	inst target.Installation
}

// close lets the installation go, if it holds anything — a tunnel, a connection — that must not outlive the target.
func (s *server) close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if c, ok := s.inst.(io.Closer); ok {
		_ = c.Close()
	}
}

func (s *server) installation() (target.Installation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.inst == nil {
		return nil, status.Error(codes.FailedPrecondition, "Configure first")
	}
	return s.inst, nil
}

func (s *server) Handshake(_ context.Context, req *remotepb.HandshakeRequest) (*remotepb.HandshakeResponse, error) {
	if req.Protocol != Protocol {
		return nil, status.Errorf(codes.FailedPrecondition, "protocol %q: this target speaks %s", req.Protocol, Protocol)
	}
	return &remotepb.HandshakeResponse{Protocol: Protocol, Target: s.name, Capabilities: s.caps}, nil
}

func (s *server) Configure(_ context.Context, req *remotepb.ConfigureRequest) (*remotepb.ConfigureResponse, error) {
	inst, err := s.f(req.Installation, req.Settings)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	s.mu.Lock()
	s.inst = inst
	s.mu.Unlock()
	return &remotepb.ConfigureResponse{Namespace: inst.Namespace()}, nil
}

func (s *server) Resolve(_ context.Context, req *remotepb.ResolveRequest) (*remotepb.ResolveResponse, error) {
	inst, err := s.installation()
	if err != nil {
		return nil, err
	}
	p, err := placementFrom(req.PlacementJson)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	res, err := inst.Resolve(req.Key, p)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	return resolvedTo(res), nil
}

func resolvedTo(res *render.Resolved) *remotepb.ResolveResponse {
	out := &remotepb.ResolveResponse{Service: res.Service, Address: res.Address}
	for _, in := range res.Instances {
		out.Instances = append(out.Instances, &remotepb.Instance{Name: in.Name, Address: in.Address})
	}
	if rc := res.Reach; rc != nil {
		out.Reach = &remotepb.Reach{Instances: rc.Instances, SamePorts: rc.SamePorts}
		if len(rc.Ports) > 0 {
			out.Reach.Ports = map[string]int32{}
			for name, p := range rc.Ports {
				out.Reach.Ports[name] = int32(p)
			}
		}
	}
	return out
}

func resolvedFrom(resp *remotepb.ResolveResponse) *render.Resolved {
	res := &render.Resolved{Service: resp.Service, Address: resp.Address}
	for _, in := range resp.Instances {
		res.Instances = append(res.Instances, render.Instance{Name: in.Name, Address: in.Address})
	}
	if rc := resp.Reach; rc != nil {
		res.Reach = &render.Reach{Instances: rc.Instances, SamePorts: rc.SamePorts}
		if len(rc.Ports) > 0 {
			res.Reach.Ports = map[string]int{}
			for name, p := range rc.Ports {
				res.Reach.Ports[name] = int(p)
			}
		}
	}
	return res
}

func (s *server) recorder() (target.Recorder, error) {
	inst, err := s.installation()
	if err != nil {
		return nil, err
	}
	r, ok := inst.(target.Recorder)
	if !ok {
		return nil, status.Errorf(codes.Unimplemented, "target %s does not keep the record of an installation", s.name)
	}
	return r, nil
}

func (s *server) ReadInstalled(context.Context, *remotepb.ReadInstalledRequest) (*remotepb.ReadInstalledResponse, error) {
	r, err := s.recorder()
	if err != nil {
		return nil, err
	}
	rec, exists, err := r.ReadInstalled()
	if err != nil {
		return nil, status.Error(codes.Unavailable, err.Error())
	}
	return &remotepb.ReadInstalledResponse{Exists: exists, Record: rec}, nil
}

func (s *server) WriteInstalled(_ context.Context, req *remotepb.WriteInstalledRequest) (*remotepb.WriteInstalledResponse, error) {
	r, err := s.recorder()
	if err != nil {
		return nil, err
	}
	if err := r.WriteInstalled(req.Record); err != nil {
		return nil, status.Error(codes.Unavailable, err.Error())
	}
	return &remotepb.WriteInstalledResponse{}, nil
}

func (s *server) DeleteInstalled(context.Context, *remotepb.DeleteInstalledRequest) (*remotepb.DeleteInstalledResponse, error) {
	r, err := s.recorder()
	if err != nil {
		return nil, err
	}
	if err := r.DeleteInstalled(); err != nil {
		return nil, status.Error(codes.Unavailable, err.Error())
	}
	return &remotepb.DeleteInstalledResponse{}, nil
}

func (s *server) Discover(ctx context.Context, req *remotepb.DiscoverRequest) (*remotepb.DiscoverResponse, error) {
	inst, err := s.installation()
	if err != nil {
		return nil, err
	}
	d, ok := inst.(target.Discoverer)
	if !ok {
		return nil, status.Errorf(codes.Unimplemented, "target %s does not tell facts", s.name)
	}
	f, err := d.Discover(ctx, req.Hosts)
	if errors.Is(err, target.ErrNoFacts) {
		return nil, status.Error(codes.Unimplemented, err.Error())
	}
	if err != nil {
		return nil, status.Error(codes.Unavailable, err.Error())
	}
	b, err := json.Marshal(f)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	return &remotepb.DiscoverResponse{Facts: b}, nil
}

func (s *server) Logs(ctx context.Context, req *remotepb.LogsRequest) (*remotepb.LogsResponse, error) {
	inst, err := s.installation()
	if err != nil {
		return nil, err
	}
	l, ok := inst.(target.Logger)
	if !ok {
		return nil, status.Errorf(codes.Unimplemented, "target %s gives no logs", s.name)
	}
	logs, err := l.Logs(ctx, req.Key, int(req.Lines))
	if errors.Is(err, target.ErrNoLogs) {
		return nil, status.Error(codes.Unimplemented, err.Error())
	}
	if err != nil {
		return nil, status.Error(codes.Unavailable, err.Error())
	}
	b, err := json.Marshal(logs)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	return &remotepb.LogsResponse{Logs: b}, nil
}

func (s *server) Health(ctx context.Context, _ *remotepb.HealthRequest) (*remotepb.HealthResponse, error) {
	inst, err := s.installation()
	if err != nil {
		return nil, err
	}
	c, ok := inst.(target.Checker)
	if !ok {
		return nil, status.Errorf(codes.Unimplemented, "target %s does not say how the keys are", s.name)
	}
	h, err := c.Health(ctx)
	if errors.Is(err, target.ErrNoHealth) {
		return nil, status.Error(codes.Unimplemented, err.Error())
	}
	if err != nil {
		return nil, status.Error(codes.Unavailable, err.Error())
	}
	b, err := json.Marshal(h)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	return &remotepb.HealthResponse{Health: b}, nil
}

func (s *server) Observe(ctx context.Context, _ *remotepb.ObserveRequest) (*remotepb.ObserveResponse, error) {
	inst, err := s.installation()
	if err != nil {
		return nil, err
	}
	o, ok := inst.(target.Observer)
	if !ok {
		return nil, status.Errorf(codes.Unimplemented, "target %s does not say what it holds", s.name)
	}
	held, err := o.Observe(ctx)
	if errors.Is(err, target.ErrNotObserved) {
		return nil, status.Error(codes.Unimplemented, err.Error())
	}
	if err != nil {
		return nil, status.Error(codes.Unavailable, err.Error())
	}
	out := &remotepb.ObserveResponse{Held: map[string]*remotepb.Observed{}}
	for k, v := range held {
		out.Held[k] = &remotepb.Observed{Image: v.Image, Replicas: int64(v.Replicas)}
	}
	return out, nil
}

func (s *server) Execute(req *remotepb.ExecuteRequest, stream remotepb.Target_ExecuteServer) error {
	inst, err := s.installation()
	if err != nil {
		return err
	}
	step, err := stepFrom(req)
	if err != nil {
		return status.Error(codes.InvalidArgument, err.Error())
	}
	log := logging.For("step").With().Str("target", s.name).Int("n", step.N).Str("key", step.Key).Str("kind", step.Kind).
		Str("name", step.Name).Bool("rollback", step.Rollback).Logger()
	log.Debug().Msg("step")
	var sendErr error
	events := func(msg string) {
		log.Trace().Msg(msg) // what the step says pack hears too: here, as the target said it
		if sendErr == nil {
			sendErr = stream.Send(&remotepb.ExecuteEvent{Message: msg})
		}
	}
	last := &remotepb.ExecuteEvent{Done: true}
	start := time.Now()
	if err := inst.Execute(stream.Context(), step, events); err != nil {
		last.Error = err.Error()
		if len(last.Error) == 0 {
			last.Error = "failed"
		}
		log.Warn().Err(err).Dur("took", time.Since(start)).Msg("step failed")
	} else {
		log.Debug().Dur("took", time.Since(start)).Msg("step done")
	}
	if sendErr != nil {
		return sendErr
	}
	return stream.Send(last)
}
