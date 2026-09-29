package primary

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/agents"
	agentinteractions "github.com/Shubham04-oss/oryxa-go/agents/primary/internal/interactions"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/managedagents"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/serviceconfig"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/slackrouter"
)

// slackOnSettings are the settings a deployment with the Slack consent loop on
// gives agents, as its environment would.
func slackOnSettings() map[string]string {
	return map[string]string{
		"GOOGLE_CLOUD_PROJECT":            "oryxa-test",
		"AGENTS_PRIMARY_SLACK_CONSENT":    "on",
		"AGENTS_PRIMARY_SLACK_TICKET_KEY": strings.Repeat("cd", 32),
		"AGENTS_PRIMARY_SERVICE_KEY_ID":   "alias/agents-service",
		"APPS_URL":                        "http://127.0.0.1:8080",
	}
}

// TestMountRefusesTheSlackLoopHalfSetUp: with the loop on, a service that lacks
// what it cannot run without does not start, whether or not it can run a model,
// and says which setting.
func TestMountRefusesTheSlackLoopHalfSetUp(t *testing.T) {
	for setting, want := range map[string]string{
		"AGENTS_PRIMARY_SLACK_TICKET_KEY": "AGENTS_PRIMARY_SLACK_TICKET_KEY",
		"AGENTS_PRIMARY_SERVICE_KEY_ID":   "AGENTS_PRIMARY_SERVICE_KEY_ID",
		"APPS_URL":                        "APPS_URL",
	} {
		values := slackOnSettings()
		delete(values, setting)
		if _, err := Mount(context.Background(), platform(values, nil)); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("Mount() without %s = %v, want it refused, naming it", setting, err)
		}
	}
	values := slackOnSettings()
	delete(values, "GOOGLE_CLOUD_PROJECT")
	delete(values, "AGENTS_PRIMARY_SLACK_TICKET_KEY")
	if _, err := Mount(context.Background(), platform(values, nil)); err == nil || !strings.Contains(err.Error(), "AGENTS_PRIMARY_SLACK_TICKET_KEY") {
		t.Errorf("Mount() with no model and no ticket key = %v, want it refused all the same", err)
	}
}

// TestMountRefusesHalfAConsentGuardrail: as the answer guard's, a guardrail
// without its version would ground no answer while looking set up.
func TestMountRefusesHalfAConsentGuardrail(t *testing.T) {
	for _, values := range []map[string]string{
		{"AGENTS_PRIMARY_CONSENT_GUARDRAIL_ID": "gr-consent"},
		{"AGENTS_PRIMARY_CONSENT_GUARDRAIL_VERSION": "1"},
	} {
		if _, err := Mount(context.Background(), platform(values, nil)); err == nil || !strings.Contains(err.Error(), "AGENTS_PRIMARY_CONSENT_GUARDRAIL") {
			t.Errorf("Mount() with %v = %v, want it refused, naming the settings", values, err)
		}
	}
}

// TestASlackLoopNeedsTheRequestLedger: the ledger is built only where Teams is,
// and the loop cannot ask anyone without it; newSlackLoop says so rather than
// panic on a nil ledger.
func TestASlackLoopNeedsTheRequestLedger(t *testing.T) {
	_, err := newSlackLoop(slackLoopConfig{Settings: serviceconfig.Config{SlackConsent: true}, Logger: slog.New(slog.DiscardHandler)})
	if err == nil || !strings.Contains(err.Error(), "TEAMS_URL") {
		t.Fatalf("newSlackLoop() without a ledger = %v, want it naming TEAMS_URL", err)
	}
}

// TestCredentialsAreTakenOutOfAnAsksWords: what the router is given to redact
// with masks a Slack token by its kind and leaves the rest of the words alone.
func TestCredentialsAreTakenOutOfAnAsksWords(t *testing.T) {
	got := redactCredentials("why did xoxb-123456789012-abcdefghijklmnopqrstuvwx stop working in #deploys?")
	if strings.Contains(got, "xoxb-") || !strings.Contains(got, "{SLACK_TOKEN}") || !strings.HasSuffix(got, "stop working in #deploys?") {
		t.Fatalf("redacted words = %q", got)
	}
	if plain := "who owns the Q3 forecast?"; redactCredentials(plain) != plain {
		t.Fatalf("redacted %q into %q", plain, redactCredentials(plain))
	}
}

// fakeAskReader is the ledger as far as an ask's link reads it.
type fakeAskReader struct {
	ask      agentinteractions.Ask
	askErr   error
	sent     []agents.Interaction
	listErr  error
	listed   []agentinteractions.Query
	listedBy []string
}

func (reader *fakeAskReader) Ask(context.Context, string) (agentinteractions.Ask, error) {
	return reader.ask, reader.askErr
}

func (reader *fakeAskReader) List(_ context.Context, organizationID, userID string, query agentinteractions.Query) ([]agents.Interaction, error) {
	reader.listed = append(reader.listed, query)
	reader.listedBy = append(reader.listedBy, organizationID+"/"+userID)
	return reader.sent, reader.listErr
}

// The link in an acknowledgement is the web app's own address for Requests: on
// the organization the ask was made in and, where the ask went to one person,
// on the request its asker sent them.
func TestAnAsksLinkOpensRequestsOnTheAskersRequest(t *testing.T) {
	reader := &fakeAskReader{
		ask:  agentinteractions.Ask{ID: "iak_1", OrganizationID: "org_1", AskerUserID: "usr_ada"},
		sent: []agents.Interaction{{ID: "irq_1"}},
	}
	link := askLink("https://app.oryxa.test/", reader)
	if got, want := link("iak_1"), "https://app.oryxa.test/?org=org_1&view=requests&request=irq_1"; got != want {
		t.Fatalf("link = %q, want %q", got, want)
	}
	// What is read is the asker's own sent box, for this ask alone.
	if len(reader.listed) != 1 || reader.listedBy[0] != "org_1/usr_ada" || reader.listed[0].Box != agentinteractions.BoxSent || reader.listed[0].AskID != "iak_1" {
		t.Fatalf("listed %+v by %v", reader.listed, reader.listedBy)
	}

	// Two owners, two requests: no one card to open, so the asker's Sent tab.
	reader.sent = []agents.Interaction{{ID: "irq_1"}, {ID: "irq_2"}}
	if got, want := link("iak_1"), "https://app.oryxa.test/?org=org_1&view=requests"; got != want {
		t.Fatalf("link for two requests = %q, want %q", got, want)
	}

	// A ledger that cannot be read leaves a link to Requests, which still opens.
	reader.listErr = errors.New("database is slow")
	if got, want := link("iak_1"), "https://app.oryxa.test/?org=org_1&view=requests"; got != want {
		t.Fatalf("link with the requests unread = %q, want %q", got, want)
	}
	reader.askErr = errors.New("database is slow")
	if got, want := link("iak_1"), "https://app.oryxa.test/?view=requests"; got != want {
		t.Fatalf("link with the ask unread = %q, want %q", got, want)
	}

	// Ids are escaped: the link never carries a character it did not mean to.
	reader.askErr, reader.listErr = nil, nil
	reader.ask.OrganizationID, reader.sent = "org 1&x=y", []agents.Interaction{{ID: "irq/1"}}
	if got, want := link("iak_1"), "https://app.oryxa.test/?org=org+1%26x%3Dy&view=requests&request=irq%2F1"; got != want {
		t.Fatalf("link with awkward ids = %q, want %q", got, want)
	}
}

// With no web origin there is nothing to link to, and the acknowledgement has
// no link, which the router allows.
func TestAnAsksLinkNeedsTheWebOrigin(t *testing.T) {
	if link := askLink("", &fakeAskReader{}); link != nil {
		t.Fatal("a link was made with no origin")
	}
	if firstOrigin(nil) != "" || firstOrigin([]string{"https://app.oryxa.test/", "https://other.test"}) != "https://app.oryxa.test" {
		t.Fatalf("firstOrigin() = %q and %q", firstOrigin(nil), firstOrigin([]string{"https://app.oryxa.test/", "https://other.test"}))
	}
}

// The ledger's refusals of an ask or a click are ones the same call would meet
// again, and the router drops the job on them; anything else is a failure to
// try again.
func TestTheLedgersRefusalsArePermanentAndNothingElseIs(t *testing.T) {
	for name, err := range map[string]error{
		"a forged ticket":            agentinteractions.ErrTicketInvalid,
		"an old ticket":              agentinteractions.ErrTicketExpired,
		"another member's ticket":    fmt.Errorf("%w: this ticket is for another member", agentinteractions.ErrForbidden),
		"a request decided first":    agentinteractions.ErrAlreadyDecided,
		"a request past its expiry":  agentinteractions.ErrExpired,
		"a request that is gone":     agentinteractions.ErrNotFound,
		"an ask it cannot take":      fmt.Errorf("%w: an ask names its organization", agentinteractions.ErrInvalid),
		"a retried ask that differs": agentinteractions.ErrIdempotencyConflict,
		"a ledger with no tickets":   fmt.Errorf("%w: no ticket key is set", agentinteractions.ErrConsentUnavailable),
	} {
		if !isRefusal(err) {
			t.Errorf("%s is not taken for a refusal", name)
		}
	}
	for name, err := range map[string]error{
		"a database that did not answer": errors.New("dial tcp 127.0.0.1:5432: connection refused"),
		"a deadline":                     context.DeadlineExceeded,
	} {
		if isRefusal(err) {
			t.Errorf("%s is taken for a refusal", name)
		}
	}
}

// A click the ledger refuses reaches the router as a permanent failure, with the
// ledger's own error inside it; one it does not is left for a retry.
func TestAClickTheLedgerRefusesIsPermanentForTheRouter(t *testing.T) {
	service, err := agentinteractions.New(agentinteractions.Config{
		Repository: unusedRepository{}, Directory: unusedDirectory{}, TicketKey: []byte(strings.Repeat("k", 32)),
	})
	if err != nil {
		t.Fatal(err)
	}
	err = slackAsks{service: service}.Consent(context.Background(), "t1.not.aticket", "usr_raya", slackrouter.SurfaceSlack)
	if !slackrouter.IsPermanent(err) || !errors.Is(err, agentinteractions.ErrTicketInvalid) {
		t.Fatalf("Consent() with a forged ticket = %v, want it permanent, and the ledger's", err)
	}
	// A ledger with no ticket key cannot take any click, for good.
	bare, err := agentinteractions.New(agentinteractions.Config{Repository: unusedRepository{}, Directory: unusedDirectory{}})
	if err != nil {
		t.Fatal(err)
	}
	if err := (slackAsks{service: bare}).Consent(context.Background(), "t1.x.y", "usr_raya", slackrouter.SurfaceSlack); !slackrouter.IsPermanent(err) {
		t.Fatalf("Consent() with no ticket key = %v, want it permanent", err)
	}
	// An ask nobody set the ledger up for is refused for good as well.
	_, err = slackAsks{service: bare}.CreateAsk(context.Background(), slackrouter.AskInput{OrganizationID: "org_1"})
	if !slackrouter.IsPermanent(err) || !errors.Is(err, agentinteractions.ErrConsentUnavailable) {
		t.Fatalf("CreateAsk() with no ledger for asks = %v, want it permanent", err)
	}
}

// unusedRepository and unusedDirectory are what a ledger is built from where the
// call under test never reaches them.
type unusedRepository struct{ agentinteractions.Repository }

type unusedDirectory struct{ agents.Directory }

// Until the registry is bound the primary agents cannot be found, and say so
// rather than dereference nothing.
func TestThePrimaryAgentsAreFoundThroughTheRegistryOnceItIsBound(t *testing.T) {
	primaries := &primaryAgents{}
	if _, err := primaries.Primary(context.Background(), managedagents.Scope{OrganizationID: "org_1", UserID: "usr_ada"}); err == nil {
		t.Fatal("an unbound registry found a primary agent")
	}
}

// Without a guardrail the consent guard holds every answer as unchecked, and
// says so once; it never delivers what nothing read.
func TestTheConsentGuardWithoutAGuardrailHoldsWhatItCannotGround(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	guard, err := newConsentGuard(serviceconfig.Config{}, nil, logger)
	if err != nil || guard == nil {
		t.Fatalf("newConsentGuard() = %v, %v, want a guard that fails closed", guard, err)
	}
	// A guardrail needs a client to ask.
	if _, err := newConsentGuard(serviceconfig.Config{ConsentGuardrailIdentifier: "gr", ConsentGuardrailVersion: "1"}, nil, logger); err == nil {
		t.Fatal("a guardrail with no Bedrock client was accepted")
	}
}
