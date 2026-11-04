package remote

import (
	"context"
	"time"

	"github.com/rs/zerolog"
	"google.golang.org/grpc"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	"github.com/horchestra/pack/pkg/logging"
)

// The log of the protocol, both ends: a call — its method, how long, its code — at debug, the size of what it
// carried at trace. Never what it carried: a step holds a unit, a configuration a store's token.

func logCall(side, target, method string, start time.Time, req, reply any, err error) {
	l := logging.For("rpc")
	level := zerolog.DebugLevel
	if err != nil {
		level = zerolog.WarnLevel
	}
	e := l.WithLevel(level).Str("side", side).Str("method", method).Dur("took", time.Since(start)).
		Str("code", status.Code(err).String())
	if len(target) > 0 {
		e = e.Str("target", target)
	}
	if logging.Enabled(zerolog.TraceLevel) {
		e = e.Int("req_bytes", size(req)).Int("reply_bytes", size(reply))
	}
	if err != nil {
		e = e.Err(err)
	}
	e.Msg("call")
}

func size(m any) int {
	if pm, ok := m.(proto.Message); ok {
		return proto.Size(pm)
	}
	return 0
}

// clientLog are the interceptors of pack's end of the protocol with the target name.
func clientLog(name string) (grpc.UnaryClientInterceptor, grpc.StreamClientInterceptor) {
	unary := func(ctx context.Context, method string, req, reply any, cc *grpc.ClientConn, invoker grpc.UnaryInvoker, opts ...grpc.CallOption) error {
		start := time.Now()
		err := invoker(ctx, method, req, reply, cc, opts...)
		logCall("client", name, method, start, req, reply, err)
		return err
	}
	stream := func(ctx context.Context, desc *grpc.StreamDesc, cc *grpc.ClientConn, method string, streamer grpc.Streamer, opts ...grpc.CallOption) (grpc.ClientStream, error) {
		logging.For("rpc").Debug().Str("side", "client").Str("target", name).Str("method", method).Msg("stream opened")
		cs, err := streamer(ctx, desc, cc, method, opts...)
		if err != nil {
			logCall("client", name, method, time.Now(), nil, nil, err)
		}
		return cs, err
	}
	return unary, stream
}

// serverLog wraps a target's handlers: each call logged as it ends.
func serverLog(name string) (grpc.UnaryServerInterceptor, grpc.StreamServerInterceptor) {
	unary := func(ctx context.Context, req any, info *grpc.UnaryServerInfo, h grpc.UnaryHandler) (any, error) {
		start := time.Now()
		reply, err := h(ctx, req)
		logCall("server", name, info.FullMethod, start, req, reply, err)
		return reply, err
	}
	stream := func(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, h grpc.StreamHandler) error {
		start := time.Now()
		err := h(srv, ss)
		logCall("server", name, info.FullMethod, start, nil, nil, err)
		return err
	}
	return unary, stream
}
