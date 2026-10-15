package gosdk

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	pb "github.com/Shubham04-oss/oryxa-go/integrations/proto/connectorv1"
)

type described struct {
	pb.UnimplementedConnectorServer
}

func (described) Describe(context.Context, *pb.DescribeRequest) (*pb.DescribeResponse, error) {
	return &pb.DescribeResponse{ManifestJson: `{"id":"x"}`}, nil
}

func TestServesOnAPrivateUnixSocket(t *testing.T) {
	// Unix socket paths are short (104 bytes on macOS); t.TempDir can be longer.
	dir, err := os.MkdirTemp("", "gosdk")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	path := filepath.Join(dir, "run", "x.sock")
	ctx, stop := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Serve(ctx, described{}, path) }()

	var info os.FileInfo
	for range 100 {
		if info, err = os.Stat(path); err == nil && info.Mode().Perm() == 0o600 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if info == nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("socket %v", info)
	}
	if d, _ := os.Stat(filepath.Dir(path)); d.Mode().Perm() != 0o700 {
		t.Fatalf("dir mode %v", d.Mode())
	}
	conn, err := grpc.NewClient("unix://"+path, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	r, err := pb.NewConnectorClient(conn).Describe(context.Background(), &pb.DescribeRequest{})
	if err != nil || r.ManifestJson != `{"id":"x"}` {
		t.Fatalf("%v %v", r, err)
	}
	stop()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestSocketPath(t *testing.T) {
	t.Setenv(SocketEnv, "")
	if p, err := SocketPath("gmail"); err != nil || p != "/run/oryxa/gmail.sock" {
		t.Fatal(p, err)
	}
	if _, err := SocketPath("../etc"); err == nil {
		t.Fatal("bad id accepted")
	}
	t.Setenv(SocketEnv, "/tmp/x.sock")
	if p, _ := SocketPath("gmail"); p != "/tmp/x.sock" {
		t.Fatal(p)
	}
}
