package host

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Shubham04-oss/oryxa-go/integrations/gosdk"
	pb "github.com/Shubham04-oss/oryxa-go/integrations/proto/connectorv1"
)

type spyReport struct {
	Env         []string
	UID, GID    int
	Groups      []int
	ParentEnv   string
	ParentError string
}

func spyOn(t *testing.T, cfg Config) spyReport {
	t.Helper()
	cfg.MinBackoff, cfg.MaxBackoff, cfg.StartTimeout = 10*time.Millisecond, 50*time.Millisecond, 10*time.Second
	h, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { h.Close() })
	if err := h.Start(context.Background(), Spec{ID: "fakeapp", Path: spyBin}); err != nil {
		t.Fatal(err)
	}
	// An ordinary call still works.
	if resp, err := h.Invoke(context.Background(), "fakeapp", &pb.InvokeRequest{Tool: "fakeapp.echo", InputJson: `{"text":"hi"}`}); err != nil || resp.GetOutputJson() != `{"text":"hi"}` {
		t.Fatalf("invoke %v %v", resp, err)
	}
	resp, err := h.Invoke(context.Background(), "fakeapp", &pb.InvokeRequest{Tool: "fakeapp.echo", InputJson: `{"text":"spy"}`})
	if err != nil {
		t.Fatal(err)
	}
	var echo struct{ Text string }
	var r spyReport
	if err := json.Unmarshal([]byte(resp.GetOutputJson()), &echo); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(echo.Text), &r); err != nil {
		t.Fatal(err)
	}
	return r
}

func TestConnectorsSeeNoneOfTheHostsSecrets(t *testing.T) {
	secrets := map[string]string{
		"INTEGRATIONS_EGRESS_KEYS":               `{"current":"k1"}`,
		"AWS_CONTAINER_CREDENTIALS_RELATIVE_URI": "/v2/credentials/spy",
		"AWS_SECRET_ACCESS_KEY":                  "spy-secret",
		"SLACK_BOT_TOKEN":                        "spy-token",
		"DATABASE_URL":                           "postgres://spy",
	}
	for k, v := range secrets {
		t.Setenv(k, v)
	}
	r := spyOn(t, Config{})
	if want := []string{gosdk.ListenerFDEnv + "=3"}; !slices.Equal(r.Env, want) {
		t.Errorf("connector environment %q, want %q", r.Env, want)
	}
	// The host is not dumpable, so even a connector of its own user cannot
	// read its environment through /proc.
	if r.ParentError == "" {
		t.Errorf("connector read the host's environ: %q", r.ParentEnv)
	}
	for _, v := range secrets {
		if strings.Contains(r.ParentEnv, v) {
			t.Errorf("connector saw %q", v)
		}
	}
}

func TestConnectorsRunAsTheirOwnUser(t *testing.T) {
	if runtime.GOOS != "linux" || os.Geteuid() != 0 {
		t.Skip("switching users needs Linux and CAP_SETUID; run as root to cover it")
	}
	// The connector user must be able to run the binary from its temp directory.
	if err := os.Chmod(filepath.Dir(spyBin), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("INTEGRATIONS_EGRESS_KEYS", "spy-keys")
	const uid = 65533
	r := spyOn(t, Config{ConnectorUID: uid})
	if r.UID != uid || r.GID != uid || len(r.Groups) != 0 {
		t.Errorf("connector runs as uid %d gid %d groups %v, want %d with no groups", r.UID, r.GID, r.Groups, uid)
	}
	if r.ParentError == "" || strings.Contains(r.ParentEnv, "spy-keys") {
		t.Errorf("connector read the host's environ: %q", r.ParentEnv)
	}
}

func TestAConnectorUserTheHostCannotSwitchToStopsTheStart(t *testing.T) {
	if runtime.GOOS != "linux" || os.Geteuid() == 0 {
		t.Skip("needs Linux without CAP_SETUID")
	}
	h, err := New(Config{ConnectorUID: 65533, StartTimeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { h.Close() })
	if err := h.Start(context.Background(), Spec{ID: "fakeapp", Path: spyBin}); err == nil {
		t.Fatal("the connector started as the host's own user")
	}
}
