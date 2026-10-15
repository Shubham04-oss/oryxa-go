package application

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Shubham04-oss/oryxa-go/apps/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/apps/internal/nativeclient"
	"github.com/Shubham04-oss/oryxa-go/internal/composio"
)

type nativeSigner struct{}

func (nativeSigner) Token(context.Context, string, string) (string, error) {
	return "service-token", nil
}

// nativeHarness routes github to a fake integrations service, which answers
// every connect with answer and records what it was sent.
func nativeHarness(t *testing.T, status int, answer string) (harness, *[]nativeclient.ConnectRequest) {
	t.Helper()
	var received []nativeclient.ConnectRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request nativeclient.ConnectRequest
		_ = json.NewDecoder(r.Body).Decode(&request)
		received = append(received, request)
		w.WriteHeader(status)
		_, _ = w.Write([]byte(answer))
	}))
	t.Cleanup(server.Close)
	client, err := nativeclient.New(nativeclient.Config{BaseURL: server.URL, Signer: nativeSigner{}})
	if err != nil {
		t.Fatal(err)
	}
	h := newHarness(t)
	h.service.native, h.service.nativeToolkits = client, []string{"github"}
	return h, &received
}

func TestANativeToolkitConnectsThroughTheIntegrationsService(t *testing.T) {
	t.Parallel()
	h, received := nativeHarness(t, http.StatusOK, `{"url":"https://github.com/login/oauth/authorize?state=s"}`)

	if broker, err := h.service.BrokerFor(context.Background(), "github"); err != nil || broker != brokerNative {
		t.Fatalf("BrokerFor() = %q, %v", broker, err)
	}
	connection, redirect, err := h.service.Connect(context.Background(), memberToken, "org_1",
		ConnectInput{Toolkit: "github", ReturnTo: "https://app.oryxa.test/settings"})
	if err != nil || redirect != "https://github.com/login/oauth/authorize?state=s" || connection.ID != "" {
		t.Fatalf("Connect() = %+v, %q, %v", connection, redirect, err)
	}
	want := nativeclient.ConnectRequest{Org: "org_1", Member: "user-1", App: "github", Level: "personal", ReturnTo: "https://app.oryxa.test/settings"}
	if len(*received) != 1 || (*received)[0] != want {
		t.Fatalf("integrations received %+v, want %+v", *received, want)
	}
	if len(h.composio.links) != 0 || len(h.store.connections) != 0 {
		t.Fatal("a native connect reached Composio or recorded a connection")
	}
}

func TestANativeConnectKeepsEveryConnectCheck(t *testing.T) {
	t.Parallel()
	h, received := nativeHarness(t, http.StatusOK, `{"url":"https://github.com/x"}`)
	if _, _, err := h.service.Connect(context.Background(), agentToken, "org_1", ConnectInput{Toolkit: "github"}); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("an agent's connect = %v, want forbidden", err)
	}
	if _, _, err := h.service.Connect(context.Background(), memberToken, "org_1",
		ConnectInput{Toolkit: "github", ReturnTo: "https://evil.example/"}); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("a return page off the allowed origins = %v, want invalid", err)
	}
	if len(*received) != 0 {
		t.Fatalf("integrations was called for refused connects: %+v", *received)
	}
}

func TestANativeConnectSaysWhyTheServiceRefused(t *testing.T) {
	t.Parallel()
	for name, testCase := range map[string]struct {
		status  int
		body    string
		want    error
		wantNot error
	}{
		"not set up":        {http.StatusConflict, `{"error":{"code":"not_set_up"}}`, domain.ErrNotConnectable, nil},
		"a broken org app":  {http.StatusConflict, `{"error":{"code":"app_broken"}}`, domain.ErrNotConnectable, nil},
		"no such connector": {http.StatusNotFound, `{"error":{"code":"not_found"}}`, domain.ErrNotConnectable, nil},
		"the service fails": {http.StatusInternalServerError, `{"error":{"code":"internal"}}`, domain.ErrUnavailable, nil},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			h, _ := nativeHarness(t, testCase.status, testCase.body)
			if _, _, err := h.service.Connect(context.Background(), memberToken, "org_1", ConnectInput{Toolkit: "github"}); !errors.Is(err, testCase.want) {
				t.Fatalf("Connect() error = %v, want %v", err, testCase.want)
			}
		})
	}
}

func TestOnlyNamedAndUnpinnedToolkitsGoNative(t *testing.T) {
	t.Parallel()
	h, received := nativeHarness(t, http.StatusOK, `{"url":"https://github.com/x"}`)
	if broker, err := h.service.BrokerFor(context.Background(), "linear"); err != nil || broker != domain.BrokerComposio {
		t.Fatalf("an unnamed toolkit = %q, %v, want composio", broker, err)
	}
	h.service.toolkitBrokers = map[string]string{"github": domain.BrokerComposio}
	h.composio.toolkits["github"] = composio.Toolkit{
		Slug: "github", AuthSchemes: []string{"OAUTH2"}, ComposioManagedAuthSchemes: []string{"OAUTH2"},
	}
	if broker, err := h.service.BrokerFor(context.Background(), "github"); err != nil || broker != domain.BrokerComposio {
		t.Fatalf("a pinned toolkit = %q, %v, want composio", broker, err)
	}
	if _, _, err := h.service.Connect(context.Background(), memberToken, "org_1", ConnectInput{Toolkit: "github"}); err != nil {
		t.Fatalf("a pinned toolkit's connect = %v", err)
	}
	if len(*received) != 0 {
		t.Fatalf("integrations was called for a pinned toolkit: %+v", *received)
	}
}

func TestNewNeedsTheServiceForNativeToolkits(t *testing.T) {
	t.Parallel()
	if _, err := checkNativeToolkits([]string{"github"}, nil); err == nil {
		t.Error("native toolkits without a service were accepted")
	}
	if _, err := checkNativeToolkits([]string{"NOT A SLUG"}, nativeStub{}); !errors.Is(err, domain.ErrInvalid) {
		t.Errorf("a bad slug = %v, want invalid", err)
	}
	if got, err := checkNativeToolkits(nil, nil); err != nil || len(got) != 0 {
		t.Errorf("none = %v, %v", got, err)
	}
}

type nativeStub struct{}

func (nativeStub) Connect(context.Context, nativeclient.ConnectRequest) (string, error) {
	return "", nil
}
