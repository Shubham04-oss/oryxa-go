package host

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"maps"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"

	"github.com/Shubham04-oss/oryxa-go/internal/eventauth"
)

// mountOrder is cmd/oryxa's: the order services mount in, each taking its
// signer, unless it publishes nothing, and then a verifier for its consumer,
// as every real Mount does.
var mountOrder = []string{"auth", "teams", "billing", "files", "knowledge", "rag", "apps", "batching", "email", "agents", "realtime"}

// publishNothing are the services whose real Mount takes no signer, since
// they only consume.
var publishNothing = map[string]bool{"email": true, "realtime": true}

type identities struct {
	signers   map[string]*eventauth.Signer
	verifiers map[string]*eventauth.Verifier
}

// mountIdentities mounts services that do with the platform's registry what
// the real ones do, and returns what each was handed.
func mountIdentities(t *testing.T, registry *eventauth.Registry) identities {
	t.Helper()
	handed := identities{signers: map[string]*eventauth.Signer{}, verifiers: map[string]*eventauth.Verifier{}}
	definitions := make([]Definition, 0, len(mountOrder))
	for _, name := range mountOrder {
		definitions = append(definitions, Definition{Name: name, Mount: func(_ context.Context, platform Platform) (Service, error) {
			if !publishNothing[name] {
				signer, err := platform.EventAuth.Signer(name)
				if err != nil {
					return Service{}, err
				}
				handed.signers[name] = signer
			}
			handed.verifiers[name] = platform.EventAuth.Verifier()
			return Service{}, nil
		}})
	}
	platform := Platform{Logger: slog.New(slog.DiscardHandler), EventAuth: registry}
	if _, err := mount(context.Background(), platform, definitions); err != nil {
		t.Fatal(err)
	}
	return handed
}

func deliver(from *eventauth.Signer, service string, to *eventauth.Verifier) error {
	occurredAt := time.Now().UTC().Format(time.RFC3339Nano)
	data := []byte(`{"organization_id":"org_1"}`)
	source := "oryxa." + service
	sig, err := from.Sign(source, service+".something.happened.v1", "evt_1", 1, occurredAt, data)
	if err != nil {
		return err
	}
	return to.Verify(source, service+".something.happened.v1", "evt_1", 1, occurredAt, data, sig)
}

func namedDefinitions(names ...string) []Definition {
	selected := make([]Definition, 0, len(names))
	for _, name := range names {
		selected = append(selected, Definition{Name: name})
	}
	return selected
}

// In one process every consumer must take every service's events, whichever
// mounted first: auth, mounted first, hears billing; knowledge hears apps.
func TestEveryConsumerTrustsEveryServiceInItsProcess(t *testing.T) {
	registry := eventRegistry(aws.Config{}, true, nil, slog.New(slog.DiscardHandler))
	handed := mountIdentities(t, registry)
	for _, publisher := range mountOrder {
		if publishNothing[publisher] {
			continue
		}
		for _, consumer := range mountOrder {
			if err := deliver(handed.signers[publisher], publisher, handed.verifiers[consumer]); err != nil {
				t.Errorf("%s's event refused by %s's consumer: %v", publisher, consumer, err)
			}
		}
	}
}

// A key minted at boot dies with the process, so what the last one published
// never verifies in the next. Enforcing, that is a refusal; by default it is
// reported and the event is handled.
func TestReportingIsTheDefaultAcrossARestart(t *testing.T) {
	config, err := ConfigFromEnv(env(map[string]string{}))
	if err != nil {
		t.Fatal(err)
	}
	if config.EnforceEventSignatures {
		t.Fatal("event signatures are enforced by default")
	}
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	before := mountIdentities(t, eventRegistry(aws.Config{}, false, nil, logger))

	after := mountIdentities(t, eventRegistry(aws.Config{}, config.EnforceEventSignatures, nil, logger))
	if err := deliver(before.signers["billing"], "billing", after.verifiers["auth"]); err != nil {
		t.Fatalf("reporting, an event from before a restart was refused: %v", err)
	}
	if !strings.Contains(logs.String(), "event signature did not check out") || !strings.Contains(logs.String(), "source=oryxa.billing") {
		t.Fatalf("the failure was not logged:\n%s", logs.String())
	}

	enforcing := mountIdentities(t, eventRegistry(aws.Config{}, true, nil, logger))
	if err := deliver(before.signers["billing"], "billing", enforcing.verifiers["auth"]); !errors.Is(err, eventauth.ErrUnverified) {
		t.Fatalf("enforcing, an event from before a restart was not refused: %v", err)
	}
}

// Only a service that publishes takes a signer, so the boot report names
// those that sign with a key minted at boot, and neither the services that
// only consume, such as realtime and email, nor one with a KMS key.
func TestTheBootReportNamesOnlyTheKeysMinted(t *testing.T) {
	keys := map[string]string{"agents": "alias/agents-events"}
	var want []string
	for _, name := range mountOrder {
		if !publishNothing[name] && keys[name] == "" {
			want = append(want, name)
		}
	}
	slices.Sort(want)
	for _, enforce := range []bool{false, true} {
		var logs bytes.Buffer
		logger := slog.New(slog.NewJSONHandler(&logs, nil))
		registry := eventRegistry(aws.Config{Region: "us-west-2"}, enforce, keys, logger)
		mountIdentities(t, registry)
		reportEventSigning(registry, enforce, logger)
		var report struct {
			Reporting []string `json:"keys_minted_at_boot"`
			Enforcing []string `json:"services"`
		}
		if err := json.Unmarshal(logs.Bytes(), &report); err != nil {
			t.Fatalf("enforce=%v: %v in %s", enforce, err, logs.String())
		}
		got := report.Reporting
		if enforce {
			got = report.Enforcing
		}
		if !slices.Equal(got, want) {
			t.Errorf("enforce=%v: the boot report names %v as minting keys, want %v", enforce, got, want)
		}
	}
}

func TestConfigReadsEventSignatures(t *testing.T) {
	for value, want := range map[string]bool{"": false, "report": false, " Enforce ": true, "enforce": true} {
		config, err := ConfigFromEnv(env(map[string]string{"ORYXA_EVENT_SIGNATURES": value}))
		if err != nil || config.EnforceEventSignatures != want {
			t.Errorf("ORYXA_EVENT_SIGNATURES=%q: enforce = %v, %v; want %v", value, config.EnforceEventSignatures, err, want)
		}
	}
	if _, err := ConfigFromEnv(env(map[string]string{"ORYXA_EVENT_SIGNATURES": "strict"})); err == nil || !strings.Contains(err.Error(), "ORYXA_EVENT_SIGNATURES") {
		t.Fatalf("ORYXA_EVENT_SIGNATURES=strict: error = %v", err)
	}
}

// Event signing keys have settings of their own. The service identity keys
// knowledge, rag and agents already hold are for proving who calls whom, and
// are not taken up for events.
func TestEventSigningKeysHaveTheirOwnSettings(t *testing.T) {
	keys := eventSigningKeys(env(map[string]string{
		"KNOWLEDGE_SERVICE_KEY_ID":      "alias/knowledge",
		"AGENTS_PRIMARY_SERVICE_KEY_ID": "alias/agents-primary",
		"AGENTS_EVENT_SIGNING_KEY_ID":   " alias/agents-events ",
		"RAG_EVENT_SIGNING_KEY_ID":      "alias/rag-events",
		"TEAMS_EVENT_SIGNING_KEY_ID":    "alias/teams-events",
	}), namedDefinitions("knowledge", "rag", "agents"))
	if want := map[string]string{"agents": "alias/agents-events", "rag": "alias/rag-events"}; !maps.Equal(keys, want) {
		t.Fatalf("keys = %v, want %v", keys, want)
	}
	if got := eventSigningKeyVariable("agents-primary"); got != "AGENTS_PRIMARY_EVENT_SIGNING_KEY_ID" {
		t.Fatalf("variable = %q", got)
	}
	if newKMSResolver(aws.Config{}, map[string]string{}) != nil {
		t.Fatal("no keys still built a KMS resolver")
	}
}
