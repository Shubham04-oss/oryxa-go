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
// Or sign with agents' KMS service key, as on a1 against staging:
//
//	EDGE_URL=https://edge-staging.oryxa.in EDGE_SIGNING_KMS_KEY_ID=alias/oryxa-staging-agents-primary AWS_REGION=ap-south-1 …
//
// It lives here, not in test/live, because it drives the agents' internal
// packages.
//
// EDGE_EXPECT_SANDBOX=1 and EDGE_EXPECT_BROWSER=1 require the sandbox
// container and Browser Run to work, as on staging; without them, those
// calls must fail as unavailable and retryable, as under `wrangler dev
// --enable-containers=false --local` (--local because the AI binding is remote-only).
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
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/kms"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/cfbrowser"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/cfsandbox"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/edgeclient"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/edgemembers"
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
	var signer serviceauth.Signer
	if keyID := os.Getenv("EDGE_SIGNING_KMS_KEY_ID"); keyID != "" {
		// Sign with agents' own KMS key, as the agents the edge serves do:
		// the staging service key, with whatever AWS credentials are here.
		awsConfig, err := config.LoadDefaultConfig(context.Background())
		if err != nil {
			t.Fatalf("load AWS config: %v", err)
		}
		signer = serviceauth.Signer{Client: kms.NewFromConfig(awsConfig), KeyID: keyID, Service: "agents"}
	} else {
		signer = pemSigner(t)
	}
	client, err := edgeclient.New(edgeclient.Config{BaseURL: url, Tokens: signer, Backoff: 100 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	return live{url: url, signer: signer, client: client, run: fmt.Sprintf("run_live_%d", time.Now().UnixNano())}
}

// pemSigner signs with EDGE_SIGNING_KEY_PEM, a test key whose public half
// the edge trusts, in KMS's place.
func pemSigner(t *testing.T) serviceauth.Signer {
	t.Helper()
	block, _ := pem.Decode([]byte(os.Getenv("EDGE_SIGNING_KEY_PEM")))
	if block == nil {
		t.Fatal("set EDGE_SIGNING_KMS_KEY_ID, or EDGE_SIGNING_KEY_PEM to the PEM private key the edge trusts")
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		key, ecErr := x509.ParseECPrivateKey(block.Bytes)
		if ecErr != nil {
			t.Fatalf("parse signing key: %v", err)
		}
		parsed = key
	}
	return serviceauth.Signer{Client: localKMS{key: parsed.(*ecdsa.PrivateKey)}, KeyID: "live-test", Service: "agents"}
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

// memoryMemberships is edgemembers.Memberships in memory: this checks the
// edge, not PostgreSQL.
type memoryMemberships struct{ rows []edgemembers.Membership }

func (m *memoryMemberships) Record(_ context.Context, row edgemembers.Membership) error {
	m.rows = append(m.rows, row)
	return nil
}
func (m *memoryMemberships) Forget(_ context.Context, row edgemembers.Membership) error {
	kept := m.rows[:0]
	for _, each := range m.rows {
		if each != row {
			kept = append(kept, each)
		}
	}
	m.rows = kept
	return nil
}
func (m *memoryMemberships) Rooms(_ context.Context, org, member string, _ []string) ([]edgemembers.Membership, error) {
	var found []edgemembers.Membership
	for _, each := range m.rows {
		if each.Org == org && each.Member == member {
			found = append(found, each)
		}
	}
	return found, nil
}
func (m *memoryMemberships) ForgetOrganization(context.Context, string) error { return nil }
func (m *memoryMemberships) Exists(_ context.Context, row edgemembers.Membership) (bool, error) {
	for _, each := range m.rows {
		if each == row {
			return true, nil
		}
	}
	return false, nil
}
func (m *memoryMemberships) InTeamspace(context.Context, string, string) ([]edgemembers.Membership, error) {
	return nil, nil
}

// Go's pusher sets a room's members; a removed member's own agent token is
// then refused (A18).
func TestGoSetsARoomsMembers(t *testing.T) {
	l := setup(t)
	target := l.target(t, "org_live_m")
	target.Room.Kind = edgev1.RoomTeamspace
	pusher, err := edgemembers.New(l.client, &memoryMemberships{}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for _, member := range []string{"usr_m1", "usr_m2"} {
		if err := pusher.Admit(ctx, edgemembers.Membership{Org: target.Org, Room: target.Room, Member: member}, nil); err != nil {
			t.Fatal(err)
		}
	}
	path, _ := edgev1.RoomPath(target.Room)
	read := func() []string {
		var members edgev1.MembersResponse
		if err := l.client.Do(ctx, edgeclient.Call{
			Claims: edgev1.TokenClaims{Org: target.Org, Room: target.Room, Agent: "edgemembers", Scopes: []edgev1.Scope{edgev1.ScopeRoomMembers}},
			Method: http.MethodGet, Path: path + "/members", Idempotent: true,
		}, &members); err != nil {
			t.Fatal(err)
		}
		return members.Members
	}
	if got := read(); strings.Join(got, ",") != "usr_m1,usr_m2" {
		t.Fatalf("members = %v", got)
	}
	if err := pusher.MemberLeftOrganization(ctx, target.Org, "usr_m1"); err != nil {
		t.Fatal(err)
	}
	if got := read(); strings.Join(got, ",") != "usr_m2" {
		t.Fatalf("after removal, members = %v", got)
	}
	removed := target
	removed.Member = "usr_m1"
	var log edgev1.LogResponse
	err = l.client.Do(ctx, edgeclient.Call{Claims: removed.Claims(edgev1.ScopeLogRead), Method: http.MethodGet, Path: path + "/log", Idempotent: true}, &log)
	if !edgeclient.IsCode(err, edgev1.CodeForbidden) {
		t.Fatalf("a removed member's agent token: %v", err)
	}
}

func requireSandbox(t *testing.T) {
	t.Helper()
	if os.Getenv("EDGE_EXPECT_SANDBOX") != "1" {
		t.Skip("EDGE_EXPECT_SANDBOX is not 1: no real sandbox to run against")
	}
}

// What one cell defines is there in the next, per language, until
// clear_context (A12).
func TestCodeStateLastsAcrossCalls(t *testing.T) {
	requireSandbox(t)
	l := setup(t)
	target := l.target(t, "org_live_a")
	executor, _ := cfsandbox.New(l.client)
	ctx := context.Background()
	run := func(t *testing.T, language, code string, clear bool) cfsandbox.Result {
		t.Helper()
		result, err := executor.Invoke(ctx, target, cfsandbox.Request{Action: cfsandbox.ExecuteCode, Language: language, Code: code, ClearContext: clear})
		if err != nil {
			t.Fatalf("%s %q: %v", language, code, err)
		}
		return result
	}
	t.Run("python", func(t *testing.T) {
		run(t, "python", "import statistics\nrows = [40, 42, 44]", false)
		if got := strings.TrimSpace(run(t, "python", "statistics.mean(rows)", false).Stdout); got != "42" {
			t.Errorf("python kept state: %q", got)
		}
		cleared := run(t, "python", "rows", true)
		if !cleared.IsError || !strings.Contains(cleared.Stderr, "NameError") {
			t.Errorf("clear_context kept state: %+v", cleared)
		}
	})
	t.Run("typescript", func(t *testing.T) {
		run(t, "typescript", "const base: number = 40", false)
		if got := strings.TrimSpace(run(t, "typescript", "base + 2", false).Stdout); got != "42" {
			t.Errorf("typescript kept state: %q", got)
		}
	})
}

// A server in the sandbox is reachable through the preview's signed URL.
func TestPreviewServesFromTheSandbox(t *testing.T) {
	requireSandbox(t)
	l := setup(t)
	target := l.target(t, "org_live_a")
	executor, _ := cfsandbox.New(l.client)
	ctx := context.Background()
	start, err := executor.Invoke(ctx, target, cfsandbox.Request{Action: cfsandbox.ExecuteCommand,
		Command: "mkdir -p /workspace/site && echo hello-from-the-sandbox > /workspace/site/index.html && " +
			// socketserver, not http.server, which dies on Cloudflare's 64-character hostname.
			"cd /workspace/site && (nohup python3 -c 'import http.server, socketserver; socketserver.ThreadingTCPServer((\"\", 8000), http.server.SimpleHTTPRequestHandler).serve_forever()' >/tmp/http.log 2>&1 &) && sleep 2"})
	if err != nil || start.IsError {
		t.Fatalf("start the server: %+v, %v", start, err)
	}
	path, _ := edgev1.SandboxPath(target.Room, edgev1.SharedSandbox)
	var signed edgev1.SignedURL
	if err := l.client.Do(ctx, edgeclient.Call{Claims: target.Claims(edgev1.ScopeSandboxExec), Method: http.MethodPost, Path: path + "/preview", Body: edgev1.PreviewRequest{Port: 8000}}, &signed); err != nil {
		t.Fatal(err)
	}
	response, err := http.Get(signed.URL + "index.html")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(response.Body)
	if response.StatusCode != http.StatusOK || !strings.Contains(string(body), "hello-from-the-sandbox") {
		t.Errorf("preview answered %d: %q", response.StatusCode, body)
	}
	tampered := signed.URL[:strings.LastIndex(signed.URL, ".")+1] + "AAAA/index.html"
	if refused, err := http.Get(tampered); err == nil {
		refused.Body.Close()
		if refused.StatusCode != http.StatusForbidden {
			t.Errorf("a tampered preview link answered %d", refused.StatusCode)
		}
	}
}

// Browse, read the page, and keep a screenshot, which lands in R2 under the
// run.
func TestTheBrowserSnapshotsAndScreenshots(t *testing.T) {
	if os.Getenv("EDGE_EXPECT_BROWSER") != "1" {
		t.Skip("EDGE_EXPECT_BROWSER is not 1: no Browser Run to run against")
	}
	l := setup(t)
	target := l.target(t, "org_live_a")
	controller, _ := cfbrowser.New(l.client)
	ctx := context.Background()
	if _, err := controller.Invoke(ctx, target, cfbrowser.Request{Action: cfbrowser.Navigate, URL: "https://example.com/"}); err != nil {
		t.Fatal(err)
	}
	snapshot, err := controller.Invoke(ctx, target, cfbrowser.Request{Action: cfbrowser.Snapshot})
	if err != nil || snapshot.Page.Title != "Example Domain" || !strings.Contains(snapshot.Page.Text, "documentation examples") || len(snapshot.Page.Elements) == 0 {
		t.Fatalf("snapshot = %+v, %v", snapshot.Page, err)
	}
	shot, err := controller.Invoke(ctx, target, cfbrowser.Request{Action: cfbrowser.Screenshot})
	if err != nil || !bytes.HasPrefix(shot.ScreenshotPNG, []byte("\x89PNG")) {
		t.Fatalf("screenshot: %d bytes, %v", len(shot.ScreenshotPNG), err)
	}
	prefix, _ := edgev1.RunPrefix(target.Org, target.Room, target.Run)
	var key string
	for _, event := range l.readLog(t, target) {
		if event.Type == edgev1.EventBrowserScreenshot {
			key, _ = event.Data["key"].(string)
		}
	}
	if !strings.HasPrefix(key, prefix+"media/screenshot-") {
		t.Errorf("screenshot stored at %q, want under %smedia/", key, prefix)
	}
}
