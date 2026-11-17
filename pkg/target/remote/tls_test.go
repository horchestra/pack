package remote

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"net"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/horchestra/pack/pkg/target/remote/remotepb"
)

type tlsTarget struct {
	remotepb.UnimplementedTargetServer
}

func (tlsTarget) Handshake(context.Context, *remotepb.HandshakeRequest) (*remotepb.HandshakeResponse, error) {
	return &remotepb.HandshakeResponse{Protocol: Protocol}, nil
}

func serveTLS(t *testing.T, material *TLSMaterial) string {
	t.Helper()
	creds, err := material.ServerCredentials()
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := grpc.NewServer(grpc.Creds(creds))
	remotepb.RegisterTargetServer(server, tlsTarget{})
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(server.Stop)
	return listener.Addr().String()
}

func callTLS(t *testing.T, addr string, creds credentials.TransportCredentials) error {
	t.Helper()
	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(creds))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, err = remotepb.NewTargetClient(conn).Handshake(ctx, &remotepb.HandshakeRequest{Protocol: Protocol}, grpc.WaitForReady(true))
	return err
}

func TestTLS(t *testing.T) {
	material, err := NewTLSMaterial([]string{"127.0.0.1"})
	if err != nil {
		t.Fatal(err)
	}
	addr := serveTLS(t, material)
	creds, err := material.ClientCredentials()
	if err != nil {
		t.Fatal(err)
	}
	if err := callTLS(t, addr, creds); err != nil {
		t.Fatalf("the run's client and server must connect: %v", err)
	}

	other, err := NewTLSMaterial([]string{"127.0.0.1"})
	if err != nil {
		t.Fatal(err)
	}
	wrong, err := other.ClientCredentials()
	if err != nil {
		t.Fatal(err)
	}
	if err := callTLS(t, addr, wrong); err == nil {
		t.Fatal("a client of another CA connected")
	}

	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(material.CACert) {
		t.Fatal("the CA does not parse")
	}
	withoutClient := credentials.NewTLS(&tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS13})
	if err := callTLS(t, addr, withoutClient); err == nil {
		t.Fatal("a client without a certificate connected")
	}
	if err := callTLS(t, addr, insecure.NewCredentials()); err == nil {
		t.Fatal("a plaintext client connected")
	}
}
