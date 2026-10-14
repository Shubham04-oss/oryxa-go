package application

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/Shubham04-oss/oryxa-go/apps/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/internal/composio"
)

// catalogPipedream is a Pipedream whose catalog is apps; nothing else of it
// is called by routing, so the rest is left to the nil interface and would
// panic if it were.
type catalogPipedream struct {
	Pipedream
	apps  []string
	err   error
	asked int
}

func (fake *catalogPipedream) ListApps(_ context.Context, query string) ([]PipedreamApp, error) {
	fake.asked++
	var found []PipedreamApp
	for _, app := range fake.apps {
		found = append(found, PipedreamApp{Slug: app})
	}
	return found, fake.err
}

func TestBrokerForPrefersOryxaThenComposioThenPipedream(t *testing.T) {
	t.Parallel()
	managed := composio.Toolkit{Slug: "github", ComposioManagedAuthSchemes: []string{"OAUTH2"}}
	for name, testCase := range map[string]struct {
		oryxa     []string
		composio  map[string]composio.Toolkit
		down      error
		pipedream *catalogPipedream
		overrides map[string]string
		toolkit   string
		want      string
		wantErr   error
	}{
		"native first, though Composio has it too": {
			oryxa: []string{"github"}, composio: map[string]composio.Toolkit{"github": managed},
			pipedream: &catalogPipedream{apps: []string{"github"}}, toolkit: "github", want: domain.BrokerOryxa,
		},
		"Composio when it can connect the app": {
			composio: map[string]composio.Toolkit{"github": managed}, pipedream: &catalogPipedream{apps: []string{"github"}},
			toolkit: "github", want: domain.BrokerComposio,
		},
		"Pipedream when Composio has the app but cannot connect it": {
			composio:  map[string]composio.Toolkit{"github": {Slug: "github", AuthSchemes: []string{"OAUTH2"}}},
			pipedream: &catalogPipedream{apps: []string{"github"}}, toolkit: "github", want: domain.BrokerPipedream,
		},
		"Pipedream when Composio has no such app": {
			pipedream: &catalogPipedream{apps: []string{"linear"}}, toolkit: "linear", want: domain.BrokerPipedream,
		},
		"Composio, to refuse it, when neither has it": {
			pipedream: &catalogPipedream{apps: []string{"github_enterprise"}}, toolkit: "github", want: domain.BrokerComposio,
		},
		"Composio without a Pipedream, asking no catalog": {
			toolkit: "linear", want: domain.BrokerComposio,
		},
		"Composio being down is an error, not a reason to use Pipedream": {
			down: composio.ErrUnavailable, pipedream: &catalogPipedream{apps: []string{"linear"}}, toolkit: "linear", wantErr: domain.ErrUnavailable,
		},
		"an override beats native": {
			oryxa: []string{"github"}, overrides: map[string]string{"github": domain.BrokerComposio},
			toolkit: "github", want: domain.BrokerComposio,
		},
		"an override to Pipedream holds without a Pipedream": {
			composio: map[string]composio.Toolkit{"linear": managed}, overrides: map[string]string{"linear": domain.BrokerPipedream},
			toolkit: "linear", want: domain.BrokerPipedream,
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			h := newHarness(t)
			for slug, toolkit := range testCase.composio {
				h.composio.toolkits[slug] = toolkit
			}
			if testCase.down != nil {
				h.composio.toolkitsErr = testCase.down
			}
			if testCase.oryxa != nil {
				h.service.oryxa = &oryxaBroker{enabled: map[string]bool{}}
				for _, toolkit := range testCase.oryxa {
					h.service.oryxa.enabled[toolkit] = true
				}
			}
			if testCase.pipedream != nil {
				h.service.pipedream = testCase.pipedream
			}
			h.service.toolkitBrokers = testCase.overrides

			got, err := h.service.BrokerFor(context.Background(), testCase.toolkit)
			if testCase.wantErr != nil {
				if !errors.Is(err, testCase.wantErr) {
					t.Fatalf("BrokerFor() error = %v, want %v", err, testCase.wantErr)
				}
				if testCase.pipedream.asked != 0 {
					t.Fatal("Pipedream was asked while Composio was down")
				}
				return
			}
			if err != nil || got != testCase.want {
				t.Fatalf("BrokerFor() = %q, %v, want %q", got, err, testCase.want)
			}
		})
	}
}

func TestBrokerForRefusesANameThatIsNoToolkit(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	if _, err := h.service.BrokerFor(context.Background(), "NOT A SLUG"); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("BrokerFor() error = %v, want invalid", err)
	}
}

// Routing decides new connects only: a connection already made through
// Composio keeps running through Composio after the app moves to Pipedream.
func TestAnExistingConnectionKeepsItsBroker(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.service.toolkitBrokers = map[string]string{"github": domain.BrokerPipedream}
	h.service.pipedream = &catalogPipedream{apps: []string{"github"}}
	tool := h.readTool()
	h.activeConnection("apc_1", "user-1", "github", "ca_1")

	if broker, err := h.service.BrokerFor(context.Background(), "github"); err != nil || broker != domain.BrokerPipedream {
		t.Fatalf("BrokerFor() = %q, %v", broker, err)
	}
	run, err := h.service.RunTool(context.Background(), memberToken, "org_1", RunToolInput{
		Tool: tool.Slug, Arguments: json.RawMessage(`{}`), OnlyReads: true,
	})
	if err != nil || run.Action.Status != domain.ActionSucceeded || len(h.composio.executed) != 1 {
		t.Fatalf("run = %+v, %v, executed %d", run, err, len(h.composio.executed))
	}
}

func TestConnectPipedreamIsNotConnectableWithoutAPipedream(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	if _, _, err := h.service.ConnectPipedream(context.Background(), memberToken, "org_1", ConnectInput{Toolkit: "linear"}); !errors.Is(err, domain.ErrNotConnectable) {
		t.Fatalf("ConnectPipedream() = %v, want not connectable", err)
	}
	if len(h.store.connections) != 0 {
		t.Fatal("a connection was recorded")
	}
}

// A Pipedream connection must never reach a Composio path: Composio holds no
// account of it, and its key must not be spent on one.
func TestAPipedreamConnectionNeverReachesComposio(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	tool := h.readTool()
	connection := h.activeConnection("apc_1", "user-1", "github", "")
	connection.Broker = domain.BrokerPipedream
	h.store.connections[connection.ID] = connection
	ctx := context.Background()

	refused := map[string]error{}
	_, refused["RunTool"] = h.service.RunTool(ctx, memberToken, "org_1", RunToolInput{
		Tool: tool.Slug, Arguments: json.RawMessage(`{}`), OnlyReads: true,
	})
	_, refused["Proxy"] = h.service.Proxy(ctx, memberToken, "org_1", ProxyInput{Toolkit: "github", Endpoint: "/user"})
	_, refused["ConnectionTargets"] = h.service.ConnectionTargets(ctx, memberToken, "org_1", connection.ID)
	// Its triggers are Pipedream's own, which this deployment has none of.
	if _, err := h.service.CreateTrigger(ctx, memberToken, "org_1", TriggerInput{
		ConnectionID: connection.ID, Trigger: "GITHUB_STAR_ADDED_EVENT", Config: json.RawMessage(`{}`),
	}); !errors.Is(err, domain.ErrNotConnectable) || !strings.Contains(err.Error(), "through Pipedream") {
		t.Errorf("CreateTrigger = %v, want Pipedream's refusal", err)
	}
	for path, err := range refused {
		// The guard's own words, so no earlier refusal passes for it.
		if !errors.Is(err, domain.ErrNotConnectable) || !strings.Contains(err.Error(), "connected through pipedream") {
			t.Errorf("%s = %v, want the Composio guard's refusal", path, err)
		}
	}

	// Composio's callback never finishes another broker's connect.
	pending := connection
	pending.ID, pending.Toolkit, pending.Status = "apc_2", "slack", domain.ConnectionPending
	if err := h.store.CreateConnection(ctx, pending, hashState("state-1"), domain.Event{}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.service.CompleteConnection(ctx, "state-1", "success", ""); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("CompleteConnection = %v, want not found", err)
	}

	if len(h.composio.executed)+len(h.composio.proxied)+len(h.composio.upserts)+len(h.composio.deleted) != 0 {
		t.Fatalf("Composio was called: executed %d, proxied %d, upserts %d, deleted %d",
			len(h.composio.executed), len(h.composio.proxied), len(h.composio.upserts), len(h.composio.deleted))
	}
	if got := h.store.connections[connection.ID].Status; got != domain.ConnectionActive {
		t.Fatalf("the connection is %s", got)
	}
}
