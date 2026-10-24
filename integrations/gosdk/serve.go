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
	"strconv"
	"syscall"

	"google.golang.org/grpc"

	pb "github.com/Shubham04-oss/oryxa-go/integrations/proto/connectorv1"
)

var manifestID = regexp.MustCompile(`^[a-z0-9-]{1,32}$`)

// Main serves srv until the process is interrupted: on the listener the host
// handed it (ListenerFDEnv), else on SocketPath(id).
func Main(srv pb.ConnectorServer, id string) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if fd := os.Getenv(ListenerFDEnv); fd != "" {
		n, err := strconv.Atoi(fd)
		if err != nil {
			return errors.New("invalid " + ListenerFDEnv)
		}
		f := os.NewFile(uintptr(n), "listener")
		lis, err := net.FileListener(f)
		f.Close()
		if err != nil {
			return err
		}
		return serve(ctx, srv, lis)
	}
	path, err := SocketPath(id)
	if err != nil {
		return err
	}
	return Serve(ctx, srv, path)
}

// ListenerFDEnv names the file descriptor of a Unix socket the host already
// listens on. A connector running as another user cannot reach the host's
// socket directory, so the host binds the socket and passes it down.
const ListenerFDEnv = "ORYXA_CONNECTOR_LISTENER_FD"

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
	return serve(ctx, srv, lis)
}

func serve(ctx context.Context, srv pb.ConnectorServer, lis net.Listener) error {
	// ponytail: no TLS; the socket's file mode is the boundary.
	s := grpc.NewServer()
	pb.RegisterConnectorServer(s, srv)
	go func() {
		<-ctx.Done()
		s.GracefulStop()
	}()
	return s.Serve(lis)
}
