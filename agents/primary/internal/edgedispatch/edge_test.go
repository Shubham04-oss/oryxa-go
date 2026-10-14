package edgedispatch

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Shubham04-oss/oryxa-go/internal/serviceauth"
)

type fakeSigner struct{ audience, purpose string }

func (signer *fakeSigner) Token(_ context.Context, audience, purpose string) (string, error) {
	signer.audience, signer.purpose = audience, purpose
	return "signed-token", nil
}

func noSleep(context.Context, time.Duration) error { return nil }

func testResult() Result {
	return Result{DispatchID: "dsp_1", Organization: "org_1", Room: Room{Kind: RoomThread, ID: "thr 9"}, Call: "call_1",
		Member: "usr_1", ThreadID: "thr_1", RunID: "run_1", Status: StatusCompleted, Text: "hello"}
}

func TestDeliverPostsTheSignedResultToTheRoomsVoiceAgent(t *testing.T) {
	signer := &fakeSigner{}
	var got Result
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.EscapedPath() != "/v1/orgs/org_1/rooms/thread/thr%209/voice/result" {
			t.Errorf("request = %s %s", r.Method, r.URL.EscapedPath())
		}
		if r.Header.Get(serviceauth.Header) != "signed-token" || r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("headers = %v", r.Header)
		}
		body, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(body, &got); err != nil {
			t.Errorf("body: %v", err)
		}
		w.WriteHeader(http.StatusAccepted)
	}))
	defer server.Close()

	client, err := NewEdgeClient(EdgeConfig{BaseURL: server.URL, Signer: signer, Sleep: noSleep})
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Deliver(context.Background(), testResult()); err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	if got != testResult() {
		t.Fatalf("edge got %+v", got)
	}
	if signer.audience != EdgeAudience || signer.purpose != ResultPurpose {
		t.Fatalf("token for %q/%q", signer.audience, signer.purpose)
	}
}

func TestDeliverRetriesOnlyWhatMaySucceedLater(t *testing.T) {
	for name, test := range map[string]struct {
		statuses []int
		calls    int32
		ok       bool
	}{
		"5xx then ok":     {[]int{503, 502, 200}, 3, true},
		"429 then ok":     {[]int{429, 204}, 2, true},
		"refused":         {[]int{403}, 1, false},
		"bad request":     {[]int{400}, 1, false},
		"down every time": {[]int{500, 500, 500, 500, 500}, attempts, false},
	} {
		t.Run(name, func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				n := calls.Add(1)
				w.WriteHeader(test.statuses[n-1])
			}))
			defer server.Close()
			client, err := NewEdgeClient(EdgeConfig{BaseURL: server.URL, Signer: &fakeSigner{}, Sleep: noSleep})
			if err != nil {
				t.Fatal(err)
			}
			err = client.Deliver(context.Background(), testResult())
			if (err == nil) != test.ok || calls.Load() != test.calls {
				t.Fatalf("Deliver = %v after %d calls; want ok=%v after %d", err, calls.Load(), test.ok, test.calls)
			}
		})
	}
}

func TestDeliverNeverFollowsARedirect(t *testing.T) {
	var elsewhere atomic.Int32
	other := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { elsewhere.Add(1) }))
	defer other.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL, http.StatusTemporaryRedirect)
	}))
	defer server.Close()
	client, _ := NewEdgeClient(EdgeConfig{BaseURL: server.URL, Signer: &fakeSigner{}, Sleep: noSleep})
	if err := client.Deliver(context.Background(), testResult()); err == nil {
		t.Fatal("a redirect counted as delivered")
	}
	if elsewhere.Load() != 0 {
		t.Fatal("the service token followed a redirect")
	}
}

func TestNewEdgeClientRefusesUnsafeURLs(t *testing.T) {
	for _, base := range []string{"http://edge.oryxa.in", "https://edge.oryxa.in/v1", "https://user:pw@edge.oryxa.in", ""} {
		if _, err := NewEdgeClient(EdgeConfig{BaseURL: base, Signer: &fakeSigner{}}); err == nil {
			t.Errorf("NewEdgeClient(%q) accepted it", base)
		}
	}
	if _, err := NewEdgeClient(EdgeConfig{BaseURL: "https://edge.oryxa.in"}); err == nil || !strings.Contains(err.Error(), "signer") {
		t.Errorf("a client without a signer = %v", err)
	}
}

func TestDeliverStopsWhenItsContextEnds(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(503) }))
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	client, _ := NewEdgeClient(EdgeConfig{BaseURL: server.URL, Signer: &fakeSigner{}})
	if err := client.Deliver(ctx, testResult()); !errors.Is(err, context.Canceled) && err == nil {
		t.Fatalf("Deliver after cancel = %v", err)
	}
}
