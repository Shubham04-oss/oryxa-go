package membertoken

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Shubham04-oss/oryxa-go/internal/serviceauth"
)

type fakeSigner struct {
	token string
	err   error
	// audience and purpose record what was asked for, since auth refuses
	// anything else.
	audience, purpose string
}

func (signer *fakeSigner) Token(_ context.Context, audience, purpose string) (string, error) {
	signer.audience, signer.purpose = audience, purpose
	if signer.err != nil {
		return "", signer.err
	}
	return signer.token, nil
}

var testClock = time.Date(2026, time.September, 18, 12, 0, 0, 0, time.UTC)

// authStub answers auth's agent-token route, recording what it was asked.
func authStub(t *testing.T, handler func(http.ResponseWriter, *http.Request)) (*httptest.Server, *int64) {
	t.Helper()
	var calls int64
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		atomic.AddInt64(&calls, 1)
		if request.URL.Path != "/v1/internal/agent-tokens" || request.Method != http.MethodPost {
			t.Errorf("asked for %s %s", request.Method, request.URL.Path)
		}
		handler(writer, request)
	}))
	t.Cleanup(server.Close)
	return server, &calls
}

func TestMemberTokenExchangesTheGrant(t *testing.T) {
	t.Parallel()
	signer := &fakeSigner{token: "service-token"}
	server, calls := authStub(t, func(writer http.ResponseWriter, request *http.Request) {
		if got := request.Header.Get(serviceauth.Header); got != "service-token" {
			t.Errorf("service token header = %q", got)
		}
		writer.Header().Set("Content-Type", "application/json")
		fmt.Fprint(writer, `{"access_token":"member-token","expires_in":3600}`)
	})
	client := New(Config{AuthURL: server.URL, Signer: signer, Now: func() time.Time { return testClock }})

	token, err := client.MemberToken(context.Background(), "org_1", "user-1")
	if err != nil || token != "member-token" {
		t.Fatalf("MemberToken() = %q, %v", token, err)
	}
	// Auth accepts this purpose alone, whatever else the service may be
	// configured for.
	if signer.audience != "auth" || signer.purpose != "agent" {
		t.Fatalf("signed for %q/%q, want auth/agent", signer.audience, signer.purpose)
	}
	if *calls != 1 {
		t.Fatalf("asked auth %d times", *calls)
	}
}

// TestATokenIsHeldUntilItIsNearlyDone keeps a pass over many sources from
// asking auth once per source.
func TestATokenIsHeldUntilItIsNearlyDone(t *testing.T) {
	t.Parallel()
	server, calls := authStub(t, func(writer http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(writer, `{"access_token":"member-token","expires_in":3600}`)
	})
	now := testClock
	client := New(Config{AuthURL: server.URL, Signer: &fakeSigner{token: "s"}, Now: func() time.Time { return now }})
	ctx := context.Background()

	for range 5 {
		if _, err := client.MemberToken(ctx, "org_1", "user-1"); err != nil {
			t.Fatal(err)
		}
	}
	if *calls != 1 {
		t.Fatalf("asked auth %d times, want once", *calls)
	}

	// Another member is another token.
	if _, err := client.MemberToken(ctx, "org_1", "user-2"); err != nil {
		t.Fatal(err)
	}
	if *calls != 2 {
		t.Fatalf("asked auth %d times for two members, want twice", *calls)
	}

	// Inside the refresh window it is treated as already gone, so nothing is
	// handed a token the service it calls is about to refuse.
	now = testClock.Add(59 * time.Minute)
	if _, err := client.MemberToken(ctx, "org_1", "user-1"); err != nil {
		t.Fatal(err)
	}
	if *calls != 3 {
		t.Fatalf("asked auth %d times after the token aged, want a fresh one", *calls)
	}
}

// TestCallersForOneMemberShareOneRequest is what keeps a burst of sources
// from each asking auth for the same member.
func TestCallersForOneMemberShareOneRequest(t *testing.T) {
	t.Parallel()
	release := make(chan struct{})
	server, calls := authStub(t, func(writer http.ResponseWriter, _ *http.Request) {
		<-release
		fmt.Fprint(writer, `{"access_token":"member-token","expires_in":3600}`)
	})
	client := New(Config{AuthURL: server.URL, Signer: &fakeSigner{token: "s"}, Now: func() time.Time { return testClock }})

	var waiting sync.WaitGroup
	tokens := make([]string, 8)
	for index := range tokens {
		waiting.Add(1)
		go func() {
			defer waiting.Done()
			token, err := client.MemberToken(context.Background(), "org_1", "user-1")
			if err != nil {
				t.Errorf("MemberToken() error = %v", err)
			}
			tokens[index] = token
		}()
	}
	// Let them all arrive before auth answers.
	time.Sleep(20 * time.Millisecond)
	close(release)
	waiting.Wait()

	if *calls != 1 {
		t.Fatalf("asked auth %d times for one member at once, want once", *calls)
	}
	for index, token := range tokens {
		if token != "member-token" {
			t.Fatalf("caller %d got %q", index, token)
		}
	}
}

// TestWhatAMemberHasToFixIsToldApartFromWhatARetryMight is the distinction
// the caller acts on: a missing grant stops the work, an outage waits.
func TestWhatAMemberHasToFixIsToldApartFromWhatARetryMight(t *testing.T) {
	t.Parallel()
	for name, testCase := range map[string]struct {
		status int
		want   error
	}{
		"the member never granted":       {status: http.StatusNotFound, want: ErrGrantRequired},
		"the grant was dropped":          {status: http.StatusConflict, want: ErrGrantRequired},
		"this service may not mint":      {status: http.StatusForbidden, want: ErrNotConfigured},
		"the service token was refused":  {status: http.StatusUnauthorized, want: ErrNotConfigured},
		"auth is having a bad time":      {status: http.StatusInternalServerError, want: ErrUnavailable},
		"auth is temporarily unreadable": {status: http.StatusBadGateway, want: ErrUnavailable},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			server, _ := authStub(t, func(writer http.ResponseWriter, _ *http.Request) {
				writer.WriteHeader(testCase.status)
			})
			client := New(Config{AuthURL: server.URL, Signer: &fakeSigner{token: "s"}, Now: func() time.Time { return testClock }})

			_, err := client.MemberToken(context.Background(), "org_1", "user-1")
			if !errors.Is(err, testCase.want) {
				t.Fatalf("error = %v, want %v", err, testCase.want)
			}
		})
	}
}

// TestAFailedTokenIsNotHeld keeps one refusal from being answered for the
// rest of the process's life.
func TestAFailedTokenIsNotHeld(t *testing.T) {
	t.Parallel()
	failing := true
	server, calls := authStub(t, func(writer http.ResponseWriter, _ *http.Request) {
		if failing {
			writer.WriteHeader(http.StatusInternalServerError)
			return
		}
		fmt.Fprint(writer, `{"access_token":"member-token","expires_in":3600}`)
	})
	client := New(Config{AuthURL: server.URL, Signer: &fakeSigner{token: "s"}, Now: func() time.Time { return testClock }})
	ctx := context.Background()

	if _, err := client.MemberToken(ctx, "org_1", "user-1"); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("error = %v, want unavailable", err)
	}
	failing = false
	token, err := client.MemberToken(ctx, "org_1", "user-1")
	if err != nil || token != "member-token" {
		t.Fatalf("MemberToken() after auth recovered = %q, %v", token, err)
	}
	if *calls != 2 {
		t.Fatalf("asked auth %d times", *calls)
	}
}

// TestADeploymentWithNoWayToAuthSaysSo keeps an unconfigured deployment from
// failing somewhere less obvious, such as a source that simply never runs.
func TestADeploymentWithNoWayToAuthSaysSo(t *testing.T) {
	t.Parallel()
	for name, client := range map[string]*Client{
		"no auth URL": New(Config{Signer: &fakeSigner{token: "s"}}),
		"no signer":   New(Config{AuthURL: "https://auth.oryxa.test"}),
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if _, err := client.MemberToken(context.Background(), "org_1", "user-1"); !errors.Is(err, ErrNotConfigured) {
				t.Fatalf("error = %v, want not configured", err)
			}
		})
	}
}

func TestAMemberlessRequestIsRefusedWithoutAskingAuth(t *testing.T) {
	t.Parallel()
	server, calls := authStub(t, func(http.ResponseWriter, *http.Request) {})
	client := New(Config{AuthURL: server.URL, Signer: &fakeSigner{token: "s"}})

	if _, err := client.MemberToken(context.Background(), "org_1", "  "); !errors.Is(err, ErrGrantRequired) {
		t.Fatalf("error = %v, want grant required", err)
	}
	if *calls != 0 {
		t.Fatalf("asked auth %d times for no member", *calls)
	}
}
