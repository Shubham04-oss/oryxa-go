package edgeclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	edgev1 "github.com/Shubham04-oss/oryxa-go/contracts/edge/v1"
)

// countingIssuer hands out a distinct token per call, as a real signer's
// fresh jti makes each one distinct.
type countingIssuer struct {
	mu     sync.Mutex
	issued []edgev1.TokenClaims
	err    error
}

func (issuer *countingIssuer) EdgeToken(_ context.Context, claims edgev1.TokenClaims) (string, error) {
	issuer.mu.Lock()
	defer issuer.mu.Unlock()
	if issuer.err != nil {
		return "", issuer.err
	}
	issuer.issued = append(issuer.issued, claims)
	return fmt.Sprintf("token-%d", len(issuer.issued)), nil
}

func newClient(t *testing.T, server *httptest.Server, issuer *countingIssuer) *Client {
	t.Helper()
	client, err := New(Config{BaseURL: server.URL, Tokens: issuer, Backoff: time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	return client
}

var claims = edgev1.TokenClaims{
	Org: "org_1", Room: edgev1.Room{Kind: edgev1.RoomThread, ID: "thr_1"},
	Member: "usr_1", Agent: "agt_1", Run: "run_1", Scopes: []edgev1.Scope{edgev1.ScopeSandboxExec},
}

func TestDoSendsTokenBodyAndDecodes(t *testing.T) {
	var seen *http.Request
	var seenBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r
		seenBody, _ = io.ReadAll(r.Body)
		_ = json.NewEncoder(w).Encode(edgev1.ExecResult{Stdout: "2\n", Results: []edgev1.ExecOutput{}})
	}))
	defer server.Close()
	issuer := &countingIssuer{}

	var result edgev1.ExecResult
	err := newClient(t, server, issuer).Do(context.Background(), Call{
		Claims: claims, Method: http.MethodPost, Path: "/v1/rooms/thread/thr_1/sandbox/shared/exec/code",
		Body: edgev1.ExecCodeRequest{Language: "python", Code: "print(2)"},
	}, &result)
	if err != nil {
		t.Fatal(err)
	}
	if result.Stdout != "2\n" {
		t.Errorf("stdout = %q", result.Stdout)
	}
	if got := seen.Header.Get("Authorization"); got != "Bearer token-1" {
		t.Errorf("Authorization = %q", got)
	}
	if seen.URL.Path != "/v1/rooms/thread/thr_1/sandbox/shared/exec/code" || seen.Header.Get("Content-Type") != "application/json" {
		t.Errorf("request = %s %s", seen.URL.Path, seen.Header.Get("Content-Type"))
	}
	if !strings.Contains(string(seenBody), `"language":"python"`) {
		t.Errorf("body = %s", seenBody)
	}
	if issuer.issued[0].Room.ID != "thr_1" || issuer.issued[0].Run != "run_1" {
		t.Errorf("token claims = %+v", issuer.issued[0])
	}
}

func TestDoRetriesWhatTheEdgeSaysNeverRan(t *testing.T) {
	var calls int
	var tokens []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		tokens = append(tokens, r.Header.Get("Authorization"))
		if calls < 3 {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"error":{"code":"unavailable","message":"container did not start","retryable":true}}`))
			return
		}
		_, _ = w.Write([]byte(`{"reset":true}`))
	}))
	defer server.Close()

	var reset edgev1.ResetResponse
	err := newClient(t, server, &countingIssuer{}).Do(context.Background(), Call{Claims: claims, Method: http.MethodPost, Path: "/v1/rooms/thread/thr_1/sandbox/shared/reset"}, &reset)
	if err != nil || !reset.Reset || calls != 3 {
		t.Fatalf("err %v reset %v calls %d", err, reset.Reset, calls)
	}
	if tokens[0] == tokens[1] || tokens[1] == tokens[2] {
		t.Errorf("a retry reused a token: %v", tokens)
	}
}

// An exec that failed after it may have run is not run again behind the
// agent's back.
func TestDoDoesNotRetryANonIdempotentServerError(t *testing.T) {
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":{"code":"internal","message":"boom"}}`))
	}))
	defer server.Close()

	err := newClient(t, server, &countingIssuer{}).Do(context.Background(), Call{Claims: claims, Method: http.MethodPost, Path: "/v1/rooms/thread/thr_1/sandbox/shared/exec/code"}, nil)
	var edgeErr *Error
	if !errors.As(err, &edgeErr) || edgeErr.Code != edgev1.CodeInternal || edgeErr.Message != "boom" || calls != 1 {
		t.Fatalf("err %v calls %d", err, calls)
	}
}

func TestDoRetriesAnIdempotentServerError(t *testing.T) {
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		if calls == 1 {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		_, _ = w.Write([]byte(`{"children":[]}`))
	}))
	defer server.Close()

	var children edgev1.ChildrenResponse
	err := newClient(t, server, &countingIssuer{}).Do(context.Background(), Call{Claims: claims, Method: http.MethodGet, Path: "/v1/rooms/thread/thr_1/children", Idempotent: true}, &children)
	if err != nil || calls != 2 {
		t.Fatalf("err %v calls %d", err, calls)
	}
}

func TestDoReportsRefusalsWithTheirCode(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":{"code":"forbidden","message":"token is for another room"}}`))
	}))
	defer server.Close()

	err := newClient(t, server, &countingIssuer{}).Do(context.Background(), Call{Claims: claims, Method: http.MethodGet, Path: "/v1/rooms/thread/thr_1/log", Idempotent: true}, nil)
	if !IsCode(err, edgev1.CodeForbidden) {
		t.Fatalf("err = %v", err)
	}
}

func TestDoGivesUpAtItsTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer server.Close()

	started := time.Now()
	err := newClient(t, server, &countingIssuer{}).Do(context.Background(), Call{Claims: claims, Method: http.MethodPost, Path: "/v1/rooms/thread/thr_1/sandbox/shared/exec/code", Timeout: 50 * time.Millisecond}, nil)
	if !errors.Is(err, context.DeadlineExceeded) || time.Since(started) > 2*time.Second {
		t.Fatalf("err %v after %s", err, time.Since(started))
	}
}

func TestDoRetriesWhenTheEdgeIsUnreachable(t *testing.T) {
	server := httptest.NewServer(http.NotFoundHandler())
	server.Close() // nothing listens: every dial fails
	issuer := &countingIssuer{}

	err := newClient(t, server, issuer).Do(context.Background(), Call{Claims: claims, Method: http.MethodPost, Path: "/v1/rooms/thread/thr_1/sandbox/shared/exec/code"}, nil)
	if err == nil || len(issuer.issued) != DefaultAttempts {
		t.Fatalf("err %v attempts %d", err, len(issuer.issued))
	}
}

func TestNewAndDoRefuseBadInput(t *testing.T) {
	if _, err := New(Config{BaseURL: "edge.oryxa.in", Tokens: &countingIssuer{}}); err == nil {
		t.Error("accepted a base URL without a scheme")
	}
	if _, err := New(Config{BaseURL: "https://edge.oryxa.in"}); err == nil {
		t.Error("accepted no token issuer")
	}
	client, _ := New(Config{BaseURL: "https://edge.oryxa.in", Tokens: &countingIssuer{}})
	if err := client.Do(context.Background(), Call{Path: "/internal/x"}, nil); err == nil {
		t.Error("sent a request outside /v1/")
	}
}
