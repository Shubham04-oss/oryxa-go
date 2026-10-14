// Package edgelive runs live-layer Baseline A end to end against a running
// oryxa-edge Worker (A10): Go signs edge tokens with serviceauth, calls the
// edge through edgeclient, cfsandbox and cfbrowser, and checks what the room
// did, refused and logged.
//
// It runs only when EDGE_URL names an edge, and needs the private half of a
// key that edge trusts (its AGENTS_TOKEN_PUBLIC_KEYS) as PEM in
// EDGE_SIGNING_KEY_PEM; signing with it stands in for agents' KMS key.
//
//	EDGE_URL=http://127.0.0.1:8787 EDGE_SIGNING_KEY_PEM="$(cat key.pem)" go test ./agents/primary/internal/edgelive
//
// It lives here, not in test/live, because it drives the agents' internal
// packages.
//
// EDGE_EXPECT_SANDBOX=1 and EDGE_EXPECT_BROWSER=1 require the sandbox
// container and Browser Run to work, as on staging; without them, those
// calls must fail as unavailable and retryable, as under `wrangler dev
// --enable-containers=false`.
package edgelive

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/kms"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/cfbrowser"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/cfsandbox"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/edgeclient"
	edgev1 "github.com/Shubham04-oss/oryxa-go/contracts/edge/v1"
	"github.com/Shubham04-oss/oryxa-go/internal/serviceauth"
)

// localKMS signs the way KMS does for an ECC_NIST_P256 key: SHA-256, then
// an ASN.1 DER ECDSA signature.
type localKMS struct{ key *ecdsa.PrivateKey }

func (local localKMS) Sign(_ context.Context, input *kms.SignInput, _ ...func(*kms.Options)) (*kms.SignOutput, error) {
	digest := sha256.Sum256(input.Message)
	signature, err := ecdsa.SignASN1(rand.Reader, local.key, digest[:])
	return &kms.SignOutput{Signature: signature}, err
}

type live struct {
	url    string
	signer serviceauth.Signer
	client *edgeclient.Client
	run    string
}

func setup(t *testing.T) live {
	t.Helper()
	url := strings.TrimRight(os.Getenv("EDGE_URL"), "/")
	if url == "" {
		t.Skip("EDGE_URL is not set: no edge to run against")
	}
	block, _ := pem.Decode([]byte(os.Getenv("EDGE_SIGNING_KEY_PEM")))
	if block == nil {
		t.Fatal("EDGE_SIGNING_KEY_PEM must hold the PEM private key the edge trusts")
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		key, ecErr := x509.ParseECPrivateKey(block.Bytes)
		if ecErr != nil {
			t.Fatalf("parse signing key: %v", err)
		}
		parsed = key
	}
	signer := serviceauth.Signer{Client: localKMS{key: parsed.(*ecdsa.PrivateKey)}, KeyID: "live-test", Service: "agents"}
	client, err := edgeclient.New(edgeclient.Config{BaseURL: url, Tokens: signer, Backoff: 100 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	return live{url: url, signer: signer, client: client, run: fmt.Sprintf("run_live_%d", time.Now().UnixNano())}
}

// target is a fresh room for each test, so runs never see each other.
func (l live) target(t *testing.T, org string) edgeclient.Target {
	return edgeclient.Target{
		Org: org, Room: edgev1.Room{Kind: edgev1.RoomThread, ID: fmt.Sprintf("thr_%s_%d", strings.ToLower(t.Name()[4:min(len(t.Name()), 20)]), time.Now().UnixNano())},
		Member: "usr_live", Agent: "agt_live", Run: l.run,
	}
}

func (l live) readLog(t *testing.T, target edgeclient.Target) []edgev1.Event {
	t.Helper()
	path, _ := edgev1.RoomPath(target.Room)
	var log edgev1.LogResponse
	if err := l.client.Do(context.Background(), edgeclient.Call{
		Claims: target.Claims(edgev1.ScopeLogRead), Method: http.MethodGet, Path: path + "/log", Idempotent: true,
	}, &log); err != nil {
		t.Fatalf("read log: %v", err)
	}
	return log.Events
}

func types(events []edgev1.Event) []string {
	names := make([]string, 0, len(events))
	for _, event := range events {
		names = append(names, event.Type)
	}
	return names
}

// Go's token, Go's client, the edge's verifier and room: a run's event goes
// in and comes back with the room, actor and run the token named.
func TestGoAndTheEdgeAgreeOnTokensAndTheLog(t *testing.T) {
	l := setup(t)
	target := l.target(t, "org_live_a")
	path, _ := edgev1.RoomPath(target.Room)
	var appended struct{ Seq int64 }
	err := l.client.Do(context.Background(), edgeclient.Call{
		Claims: target.Claims(edgev1.ScopeLogWrite), Method: http.MethodPost, Path: path + "/log",
		Body: edgev1.AppendEventRequest{Type: "run.started", Summary: "live test", Data: map[string]any{"from": "go"}},
	}, &appended)
	if err != nil {
		t.Fatal(err)
	}
	events := l.readLog(t, target)
	if got := types(events); len(got) < 2 || got[0] != edgev1.EventRoomCreated || got[1] != "run.started" {
		t.Fatalf("log = %v", got)
	}
	event := events[1]
	if event.Org != target.Org || event.Room != target.Room || event.Actor.ID != "agt_live" || event.Actor.OnBehalfOf != "usr_live" || event.Run == nil || *event.Run != l.run {
		t.Errorf("event = %+v", event)
	}
}

// The isolation attacks a token can make (05-testing-calibration.md), each
// refused.
func TestTheEdgeRefusesWhatATokenDoesNotCover(t *testing.T) {
	l := setup(t)
	target := l.target(t, "org_live_b")
	path, _ := edgev1.RoomPath(target.Room)

	send := func(token, method, path string, body any) int {
		var reader *bytes.Reader
		if body != nil {
			encoded, _ := json.Marshal(body)
			reader = bytes.NewReader(encoded)
		} else {
			reader = bytes.NewReader(nil)
		}
		request, _ := http.NewRequest(method, l.url+path, reader)
		if token != "" {
			request.Header.Set("Authorization", "Bearer "+token)
		}
		request.Header.Set("Content-Type", "application/json")
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		return response.StatusCode
	}
	sign := func(claims edgev1.TokenClaims) string {
		token, err := l.signer.EdgeToken(context.Background(), claims)
		if err != nil {
			t.Fatal(err)
		}
		return token
	}

	if status := send("", http.MethodGet, path+"/log", nil); status != http.StatusUnauthorized {
		t.Errorf("no token: %d", status)
	}
	once := sign(target.Claims(edgev1.ScopeLogRead))
	if status := send(once, http.MethodGet, path+"/log", nil); status != http.StatusOK {
		t.Fatalf("first use: %d", status)
	}
	if status := send(once, http.MethodGet, path+"/log", nil); status != http.StatusForbidden {
		t.Errorf("replayed jti: %d", status)
	}
	other := target
	other.Room.ID += "_other"
	if status := send(sign(other.Claims(edgev1.ScopeLogRead)), http.MethodGet, path+"/log", nil); status != http.StatusForbidden {
		t.Errorf("token for another room: %d", status)
	}
	if status := send(sign(target.Claims(edgev1.ScopeSandboxExec)), http.MethodGet, path+"/log", nil); status != http.StatusForbidden {
		t.Errorf("missing scope: %d", status)
	}
	private := target.Claims(edgev1.ScopeSandboxExec)
	private.Member = "usr_intruder"
	private.Private = true
	sandbox, _ := edgev1.SandboxPath(target.Room, edgev1.PrivateSandbox("usr_live"))
	if status := send(sign(private), http.MethodPost, sandbox+"/exec/command", edgev1.ExecCommandRequest{Command: "id"}); status != http.StatusForbidden {
		t.Errorf("another member's private sandbox: %d", status)
	}
	tampered := sign(target.Claims(edgev1.ScopeLogRead))
	tampered = tampered[:len(tampered)-4] + "AAAA"
	if status := send(tampered, http.MethodGet, path+"/log", nil); status != http.StatusUnauthorized {
		t.Errorf("tampered signature: %d", status)
	}

	// The refusals the room could attribute are in its log.
	if got := types(l.readLog(t, target)); !contains(got, edgev1.EventAccessDenied) {
		t.Errorf("no access.denied in %v", got)
	}

	// Another organization's room of the same id is another room.
	theirs := target
	theirs.Org = "org_live_c"
	for _, event := range l.readLog(t, theirs) {
		if event.Org != "org_live_c" {
			t.Fatalf("org_live_c read %s's event", event.Org)
		}
	}
}

// The sandbox tools' path: cfsandbox → edge → RoomDO → SandboxDO.
func TestTheSandboxRunsThroughTheRoom(t *testing.T) {
	l := setup(t)
	target := l.target(t, "org_live_a")
	executor, err := cfsandbox.New(l.client)
	if err != nil {
		t.Fatal(err)
	}
	result, err := executor.Invoke(context.Background(), target, cfsandbox.Request{Action: cfsandbox.ExecuteCode, Language: "python", Code: "print(6 * 7)"})
	if os.Getenv("EDGE_EXPECT_SANDBOX") != "1" {
		if err == nil || !strings.Contains(err.Error(), "unavailable") {
			t.Fatalf("without containers the sandbox must be unavailable, got %+v, %v", result, err)
		}
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(result.Stdout) != "42" || result.IsError {
		t.Fatalf("result = %+v", result)
	}
	if got := types(l.readLog(t, target)); !contains(got, edgev1.EventSandboxExec) {
		t.Errorf("no sandbox.exec in %v", got)
	}
	written, err := executor.Invoke(context.Background(), target, cfsandbox.Request{Action: cfsandbox.WriteFiles, Files: []cfsandbox.File{{Path: "note.txt", Text: "kept"}}})
	if err != nil || written.IsError {
		t.Fatalf("write: %+v, %v", written, err)
	}
	read, err := executor.Invoke(context.Background(), target, cfsandbox.Request{Action: cfsandbox.ReadFiles, Paths: []string{"note.txt"}})
	if err != nil || len(read.Content) != 1 || read.Content[0].ResourceText != "kept" {
		t.Fatalf("read: %+v, %v", read, err)
	}
	probe, err := executor.Invoke(context.Background(), target, cfsandbox.Request{Action: cfsandbox.ExecuteCommand, Command: "curl -s -m 5 -o /dev/null -w '%{http_code}' http://169.254.169.254/latest/meta-data/ || true"})
	if err != nil || strings.TrimSpace(probe.Stdout) == "200" {
		t.Errorf("the sandbox reached the metadata address: %+v, %v", probe, err)
	}
}

// The browser tools' path: cfbrowser → edge → RoomDO → BrowserDO.
func TestTheBrowserRunsThroughTheRoom(t *testing.T) {
	l := setup(t)
	target := l.target(t, "org_live_a")
	controller, err := cfbrowser.New(l.client)
	if err != nil {
		t.Fatal(err)
	}
	result, err := controller.Invoke(context.Background(), target, cfbrowser.Request{Action: cfbrowser.Navigate, URL: "https://example.com/"})
	if os.Getenv("EDGE_EXPECT_BROWSER") != "1" {
		if err == nil {
			t.Fatalf("without Browser Run the browser must fail, got %+v", result)
		}
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Page.Title, "Example") {
		t.Fatalf("page = %+v", result.Page)
	}
	if got := types(l.readLog(t, target)); !contains(got, edgev1.EventBrowserNavigate) {
		t.Errorf("no browser.navigate in %v", got)
	}
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
