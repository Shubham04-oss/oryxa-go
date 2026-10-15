// Package gosdk serves a Go connector over the connector contract
// (integrations/proto/oryxa/connector/v1) on a private Unix socket for the
// connector host.
//
// Source: oryxa-platform, connectors/gosdk/serve.go, without its AWS Lambda
// adapter (the host runs connectors as local processes).
package gosdk

import (
	"context"
	"errors"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"syscall"

	"google.golang.org/grpc"

	pb "github.com/Shubham04-oss/oryxa-go/integrations/proto/connectorv1"
)

var manifestID = regexp.MustCompile(`^[a-z0-9-]{1,32}$`)

// Main serves srv on SocketPath(id) until the process is interrupted.
func Main(srv pb.ConnectorServer, id string) error {
	path, err := SocketPath(id)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return Serve(ctx, srv, path)
}

// SocketEnv is the variable through which the host tells a connector where to listen.
const SocketEnv = "ORYXA_CONNECTOR_SOCKET"

// SocketPath is where a connector listens: ORYXA_CONNECTOR_SOCKET, else /run/oryxa/<id>.sock.
// Requests carry users' egress handles, so they are never served on an open TCP port.
func SocketPath(id string) (string, error) {
	if p := os.Getenv(SocketEnv); p != "" {
		return p, nil
	}
	if !manifestID.MatchString(id) {
		return "", errors.New("invalid manifest id")
	}
	return "/run/oryxa/" + id + ".sock", nil
}

// Serve serves srv on a Unix socket only its own user can open (directory
// 0700, socket 0600) until ctx ends, then stops gracefully.
func Serve(ctx context.Context, srv pb.ConnectorServer, path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	_ = os.Remove(path) // a stale socket from a previous run
	lis, err := net.Listen("unix", path)
	if err != nil {
		return err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		lis.Close()
		return err
	}
	// ponytail: no TLS; the socket's file mode is the boundary.
	s := grpc.NewServer()
	pb.RegisterConnectorServer(s, srv)
	go func() {
		<-ctx.Done()
		s.GracefulStop()
	}()
	return s.Serve(lis)
}
