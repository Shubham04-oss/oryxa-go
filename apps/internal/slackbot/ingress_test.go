package slackbot

import (
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	appsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/apps/v1"

	"github.com/Shubham04-oss/oryxa-go/apps/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/apps/internal/hooks"
	"github.com/Shubham04-oss/oryxa-go/apps/internal/integration"
	"github.com/Shubham04-oss/oryxa-go/apps/internal/integrations/slack"
	"github.com/Shubham04-oss/oryxa-go/internal/slackfake"
)

const askText = "<@U0FAKEBOT> what is the launch date? ask <@U0RAYA> and <@U0MAYA>, <@U0RAYA> knows"

// dataOf decodes an outbox event's data.
func dataOf(t *testing.T, raw json.RawMessage) map[string]any {
	t.Helper()
	var data map[string]any
	if err := json.Unmarshal(raw, &data); err != nil {
		t.Fatal(err)
	}
	return data
}

func keysOf(data map[string]any) []string {
	keys := make([]string, 0, len(data))
	for key := range data {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	return keys
}

func TestAMentionIsRecordedWithOneEventAndNoOutboundCall(t *testing.T) {
	h := newHarness(t)
	h.installFor(testOrg)

	response := h.mention(slackfake.Mention{Channel: "C0GENERAL", User: "U0ASKER", Text: askText})
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d %s", response.Code, response.Body)
	}

	rows := h.store.inboundRows()
	if len(rows) != 1 {
		t.Fatalf("records = %d, want 1", len(rows))
	}
	row := rows[0]
	if row.Kind != InboundMessage || row.OrganizationID != testOrg || row.TeamID != slackfake.DefaultTeamID ||
		row.ChannelID != "C0GENERAL" || row.UserID != "U0ASKER" || row.Text != askText || row.EventID == "" {
		t.Fatalf("record = %+v", row)
	}
	// A top-level message starts its own thread.
	if row.MessageTS == "" || row.ThreadTS != row.MessageTS {
		t.Fatalf("ts = %q, thread = %q", row.MessageTS, row.ThreadTS)
	}
	// The people named, once each, and not the bot the message is for.
	if want := []string{"U0RAYA", "U0MAYA"}; !slices.Equal(row.Mentions, want) {
		t.Fatalf("mentions = %v, want %v", row.Mentions, want)
	}
	if !row.ReceivedAt.Equal(testStart) {
		t.Fatalf("received at %v", row.ReceivedAt)
	}

	// One event, and it names the record and carries nothing else.
	events := h.store.published()
	if len(events) != 1 || events[0].DetailType != appsv1.SlackMessageReceivedDetailType {
		t.Fatalf("events = %+v", events)
	}
	data := dataOf(t, events[0].Data)
	if want := []string{"inbound_id", "organization_id", "received_at", "team_id"}; !reflect.DeepEqual(keysOf(data), want) {
		t.Fatalf("event fields = %v, want %v", keysOf(data), want)
	}
	if data["inbound_id"] != row.ID || data["organization_id"] != testOrg || data["team_id"] != slackfake.DefaultTeamID {
		t.Fatalf("event = %v", data)
	}
	if strings.Contains(string(events[0].Data), "launch") || strings.Contains(string(events[0].Data), "U0ASKER") {
		t.Fatalf("the event carries what was typed: %s", events[0].Data)
	}

	// Nothing before the answer called Slack, or opened a secret.
	if calls := h.fake.Calls(); len(calls) != 0 {
		t.Fatalf("the ack path called Slack: %+v", calls)
	}
	if h.keys.opened() != 0 {
		t.Fatal("the ack path opened a sealed secret")
	}
	h.assertLogsLack("launch", "U0RAYA", "U0ASKER", h.fake.SigningSecret(), slackfake.DefaultBotToken)
}

func TestAMentionInsideAThreadIsFiledUnderItsRoot(t *testing.T) {
	h := newHarness(t)
	h.installFor(testOrg)
	h.mention(slackfake.Mention{Text: askText, TS: "1780000100.000200", ThreadTS: "1780000000.000100", ExtShared: true})
	row := h.lastInbound()
	if row.MessageTS != "1780000100.000200" || row.ThreadTS != "1780000000.000100" || !row.ExtShared {
		t.Fatalf("record = %+v", row)
	}
}

func TestAClickIsAFormAndIsRecordedByReference(t *testing.T) {
	h := newHarness(t)
	h.installFor(testOrg)
	const ticket = "t1.eyJyIjoiciJ9.c2lnbmF0dXJl"

	response := h.serve(h.fake.Click("", slackfake.Click{
		User: "U0RAYA", Channel: "D0RAYA", MessageTS: "1780000200.000300", ActionID: ActionDeny, Value: ticket,
	}))
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d %s", response.Code, response.Body)
	}
	row := h.lastInbound()
	if row.Kind != InboundAction || row.OrganizationID != testOrg || row.TeamID != slackfake.DefaultTeamID || row.UserID != "U0RAYA" ||
		row.ChannelID != "D0RAYA" || row.MessageTS != "1780000200.000300" || row.ThreadTS != "1780000200.000300" ||
		row.ActionID != ActionDeny || row.ActionValue != ticket {
		t.Fatalf("record = %+v", row)
	}
	events := h.store.published()
	if len(events) != 1 || events[0].DetailType != appsv1.SlackActionReceivedDetailType {
		t.Fatalf("events = %+v", events)
	}
	data := dataOf(t, events[0].Data)
	if want := []string{"inbound_id", "organization_id", "received_at", "team_id"}; !reflect.DeepEqual(keysOf(data), want) {
		t.Fatalf("event fields = %v", keysOf(data))
	}
	if strings.Contains(string(events[0].Data), ticket) {
		t.Fatal("the event carries the ticket")
	}
	// The ticket is a capability: it is in the record and nowhere else.
	h.assertLogsLack(ticket, "U0RAYA")
	if len(h.fake.Calls()) != 0 || h.keys.opened() != 0 {
		t.Fatal("the ack path called out")
	}

	// The same delivery again is one click; another click is another.
	if again := h.serve(h.fake.Click("", slackfake.Click{
		User: "U0RAYA", Channel: "D0RAYA", MessageTS: "1780000200.000300", ActionID: ActionDeny, Value: ticket,
		TriggerID: "same", ActionTS: "1780000200.000400",
	})); again.Code != http.StatusOK {
		t.Fatal(again.Body)
	}
	if got := len(h.store.inboundRows()); got != 2 {
		t.Fatalf("records = %d, want a second for the second click", got)
	}
}

func TestSignatureVectorsAreVerifiedByTheV0SchemeEndToEnd(t *testing.T) {
	h := newHarness(t)
	h.installFor(testOrg)
	now := h.clock.Now()

	valid := h.fake.Mention("", slackfake.Mention{Text: askText, EventID: "EvVALID"})
	stale := h.fake.Mention("", slackfake.Mention{Text: askText, EventID: "EvSTALE"})
	h.fake.Sign(stale, now.Add(-6*time.Minute))
	future := h.fake.Mention("", slackfake.Mention{Text: askText, EventID: "EvFUTURE"})
	h.fake.Sign(future, now.Add(6*time.Minute))
	almost := h.fake.Mention("", slackfake.Mention{Text: askText, EventID: "EvALMOST"})
	h.fake.Sign(almost, now.Add(-299*time.Second))
	forged := h.fake.Mention("", slackfake.Mention{Text: askText, EventID: "EvFORGED"})
	forged.Header.Set("X-Slack-Signature", "v0="+strings.Repeat("a", 64))
	unsigned := h.fake.Mention("", slackfake.Mention{Text: askText, EventID: "EvUNSIGNED"})
	unsigned.Header.Del("X-Slack-Signature")
	other := slackfake.New(t, slackfake.WithSigningSecret("some-other-apps-signing-secret-0000"), slackfake.WithClock(h.clock.Now))
	foreign := other.Mention("", slackfake.Mention{Text: askText, EventID: "EvFOREIGN"})

	for name, test := range map[string]struct {
		request *http.Request
		status  int
	}{
		"a valid delivery":                     {valid, http.StatusOK},
		"a timestamp six minutes old":          {stale, http.StatusUnauthorized},
		"a timestamp six minutes ahead":        {future, http.StatusUnauthorized},
		"a timestamp just inside five minutes": {almost, http.StatusOK},
		"a forged signature":                   {forged, http.StatusUnauthorized},
		"no signature":                         {unsigned, http.StatusUnauthorized},
		"signed with another app's secret":     {foreign, http.StatusUnauthorized},
	} {
		t.Run(name, func(t *testing.T) {
			before := len(h.store.inboundRows())
			response := h.serve(test.request)
			if response.Code != test.status {
				t.Fatalf("status = %d, want %d: %s", response.Code, test.status, response.Body)
			}
			if got := len(h.store.inboundRows()) - before; (test.status == http.StatusOK) != (got == 1) {
				t.Fatalf("records added = %d", got)
			}
		})
	}
	// Refused deliveries left no mark and no trace.
	for _, id := range []string{"EvSTALE", "EvFUTURE", "EvFORGED", "EvUNSIGNED", "EvFOREIGN"} {
		for key := range h.store.handled {
			if strings.HasSuffix(key, ":"+id) {
				t.Errorf("a refused delivery %s left a mark %s", id, key)
			}
		}
	}
	h.assertLogsLack(askText, h.fake.SigningSecret())
}

func TestAReplayedDeliveryIsAcknowledgedAndRecordedOnce(t *testing.T) {
	h := newHarness(t)
	h.installFor(testOrg)
	request := func() *http.Request {
		return h.fake.Mention("", slackfake.Mention{Text: askText, EventID: "EvREPLAY", TS: "1780000300.000100"})
	}
	for range 3 {
		if response := h.serve(request()); response.Code != http.StatusOK {
			t.Fatalf("status = %d %s", response.Code, response.Body)
		}
	}
	// Slack retries with a fresh signature, and the same event id.
	h.clock.Advance(90 * time.Second)
	if response := h.serve(request()); response.Code != http.StatusOK {
		t.Fatalf("a retry = %d", response.Code)
	}
	if got := len(h.store.inboundRows()); got != 1 {
		t.Fatalf("records = %d, want 1", got)
	}
	if got := len(h.store.published()); got != 1 {
		t.Fatalf("events = %d, want 1", got)
	}
}

func TestTheDedupeRaceRecordsAMentionOnce(t *testing.T) {
	h := newHarness(t)
	h.installFor(testOrg)
	const copies = 24
	h.store.gate = make(chan struct{})
	h.store.arrivals = make(chan struct{}, copies)

	var group sync.WaitGroup
	statuses := make([]int, copies)
	for i := range copies {
		// Copies of one delivery: the same body and the same signature.
		request := h.fake.Mention("", slackfake.Mention{Text: askText, EventID: "EvRACE", TS: "1780000400.000100"})
		group.Add(1)
		go func() {
			defer group.Done()
			statuses[i] = h.serve(request).Code
		}()
	}
	// Every copy passed the check that the delivery was new and is inside the
	// transaction, before any of them writes.
	for range copies {
		select {
		case <-h.store.arrivals:
		case <-time.After(5 * time.Second):
			t.Fatal("the copies did not all reach the transaction")
		}
	}
	close(h.store.gate)
	group.Wait()

	for i, status := range statuses {
		if status != http.StatusOK {
			t.Fatalf("copy %d answered %d", i, status)
		}
	}
	if got := len(h.store.inboundRows()); got != 1 {
		t.Fatalf("records = %d, want the race to write one", got)
	}
	if got := len(h.store.published()); got != 1 {
		t.Fatalf("events = %d, want one", got)
	}
}

func TestADeliveryForAnUnknownWorkspaceIsAcknowledgedAndDropped(t *testing.T) {
	h := newHarness(t)
	h.installFor(testOrg)
	for name, request := range map[string]*http.Request{
		"a mention":    h.fake.Mention("", slackfake.Mention{TeamID: "T0STRANGE", Text: askText}),
		"a click":      h.fake.Click("", slackfake.Click{TeamID: "T0STRANGE", ActionID: ActionDeny, Value: "t1.x"}),
		"an uninstall": h.fake.AppUninstalled("", "T0STRANGE"),
	} {
		if response := h.serve(request); response.Code != http.StatusOK {
			t.Fatalf("%s = %d %s", name, response.Code, response.Body)
		}
	}
	if len(h.store.inboundRows()) != 0 || len(h.store.published()) != 0 || len(h.store.handled) != 0 {
		t.Fatalf("something was kept: %d records, %d events, %d marks",
			len(h.store.inboundRows()), len(h.store.published()), len(h.store.handled))
	}
	if h.store.install(slackfake.DefaultTeamID).Status != appsv1.SlackInstallActive {
		t.Fatal("a stranger's uninstall ended the install")
	}

	// A workspace that installed and then ended is as unknown as one that never did.
	h.fake.AddWorkspace(slackfake.Workspace{TeamID: "T0GONE01"})
	gone := h.installTeam("org_gone", "T0GONE01")
	if _, ended, err := h.store.EndInstall(t.Context(), gone.TeamID, appsv1.SlackAppUninstalled, testStart, func(Install) (domain.Event, error) {
		return domain.Event{ID: "evt_end", DetailType: appsv1.SlackInstallChangedDetailType, OccurredAt: testStart, Data: json.RawMessage(`{}`)}, nil
	}); err != nil || !ended {
		t.Fatalf("EndInstall = %v, %v", ended, err)
	}
	before := len(h.store.published())
	if response := h.mention(slackfake.Mention{TeamID: "T0GONE01", Text: askText}); response.Code != http.StatusOK {
		t.Fatalf("a mention for an ended install = %d", response.Code)
	}
	if len(h.store.inboundRows()) != 0 || len(h.store.published()) != before {
		t.Fatal("a mention for an ended install was recorded")
	}
}

func TestEverythingTheSurfaceDidNotAskForIsAcknowledgedAndDropped(t *testing.T) {
	h := newHarness(t)
	install := h.installFor(testOrg)
	message := h.fake.Event("", map[string]any{
		"type": "event_callback", "team_id": slackfake.DefaultTeamID, "event_id": "EvMSG",
		"event": map[string]any{"type": "message", "user": "U0ASKER", "text": "hello", "ts": "1780000500.000100", "channel": "C0GENERAL"},
	})
	selfMention := h.fake.Mention("", slackfake.Mention{User: install.BotUserID, Text: "<@" + install.BotUserID + "> hi"})
	badChannel := h.fake.Mention("", slackfake.Mention{Channel: "general", Text: askText})
	badTS := h.fake.Mention("", slackfake.Mention{TS: "yesterday", Text: askText})
	modal := h.fake.Signed(testBaseURL+InteractivityPath, "application/x-www-form-urlencoded",
		[]byte("payload="+`%7B%22type%22%3A%22view_submission%22%7D`))
	empty := h.fake.Signed(testBaseURL+InteractivityPath, "application/x-www-form-urlencoded", []byte("nothing=here"))
	notJSON := h.fake.Signed(testBaseURL+EventsPath, "application/json", []byte(`not json`))
	rateLimited := h.fake.Event("", map[string]any{"type": "app_rate_limited", "team_id": slackfake.DefaultTeamID})

	for name, request := range map[string]*http.Request{
		"a message that does not mention the bot": message, "the bot mentioning itself": selfMention,
		"a mention in a channel with no id": badChannel, "a mention with a timestamp that is none": badTS,
		"a modal": modal, "a form with no payload": empty, "a body that is no JSON": notJSON, "an app_rate_limited": rateLimited,
	} {
		if response := h.serve(request); response.Code != http.StatusOK {
			t.Errorf("%s = %d %s, want it acknowledged", name, response.Code, response.Body)
		}
	}
	if got := len(h.store.inboundRows()); got != 0 {
		t.Fatalf("records = %d, want none", got)
	}
	if got := len(h.store.published()); got != 0 {
		t.Fatalf("events = %d, want none", got)
	}
}

func TestSlackEndingAnInstallEndsIt(t *testing.T) {
	t.Run("app_uninstalled", func(t *testing.T) {
		h := newHarness(t)
		h.installFor(testOrg)
		if response := h.serve(h.fake.AppUninstalled("", slackfake.DefaultTeamID)); response.Code != http.StatusOK {
			t.Fatalf("status = %d %s", response.Code, response.Body)
		}
		install := h.store.install(slackfake.DefaultTeamID)
		if install.Active() || install.Sealed != nil || install.EndReason != appsv1.SlackAppUninstalled || install.EndedAt == nil {
			t.Fatalf("install = %+v", install)
		}
		events := h.store.publishedOf(appsv1.SlackInstallChangedDetailType)
		if len(events) != 1 {
			t.Fatalf("events = %+v", h.store.published())
		}
		data := dataOf(t, events[0].Data)
		if data["state"] != "ended" || data["reason"] != "app_uninstalled" || data["organization_id"] != testOrg || data["team_id"] != slackfake.DefaultTeamID {
			t.Fatalf("event = %v", data)
		}
		if _, hasActor := data["actor_id"]; hasActor {
			t.Fatalf("Slack ending an install has no actor: %v", data)
		}
		// Slack may send tokens_revoked as well, in either order: nothing more happens.
		h.serve(h.fake.TokensRevoked("", slackfake.DefaultTeamID, slackfake.DefaultBotUserID))
		// And a mention after the end is a stranger's.
		h.mention(slackfake.Mention{Text: askText})
		if len(h.store.published()) != 1 || len(h.store.inboundRows()) != 0 {
			t.Fatalf("events = %d, records = %d after the end", len(h.store.published()), len(h.store.inboundRows()))
		}
	})
	t.Run("tokens_revoked for the bot", func(t *testing.T) {
		h := newHarness(t)
		h.installFor(testOrg)
		if response := h.serve(h.fake.TokensRevoked("", slackfake.DefaultTeamID, slackfake.DefaultBotUserID)); response.Code != http.StatusOK {
			t.Fatalf("status = %d", response.Code)
		}
		install := h.store.install(slackfake.DefaultTeamID)
		if install.Active() || install.EndReason != appsv1.SlackTokensRevoked {
			t.Fatalf("install = %+v", install)
		}
	})
	t.Run("tokens_revoked for users only", func(t *testing.T) {
		h := newHarness(t)
		h.installFor(testOrg)
		h.serve(h.fake.TokensRevoked("", slackfake.DefaultTeamID))
		h.serve(h.fake.TokensRevoked("", slackfake.DefaultTeamID, "U0SOMEBOT"))
		if !h.store.install(slackfake.DefaultTeamID).Active() || len(h.store.published()) != 0 {
			t.Fatal("a token that is not the bot's ended the install")
		}
	})
}

func TestUrlVerificationIsAnsweredAndMakesTheEndpoint(t *testing.T) {
	h := newHarness(t)
	response := h.serve(h.fake.URLVerification("", "3eZbrw1aBm2rZgRNFdxV2595E9CY3gmdALWMmHkvFXO7tYXAYM8P"))
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"challenge":"3eZbrw1aBm2rZgRNFdxV2595E9CY3gmdALWMmHkvFXO7tYXAYM8P"`) {
		t.Fatalf("url_verification = %d %s", response.Code, response.Body)
	}
	endpoint, err := h.store.ActiveHookEndpoint(t.Context(), integration.HookScopeDeployment, "", Toolkit)
	if err != nil || endpoint.ValidatedAt == nil {
		t.Fatalf("endpoint = %+v, %v", endpoint, err)
	}
	// Nothing was recorded, and Slack has no reason to be told anything else.
	if len(h.store.inboundRows()) != 0 || len(h.store.published()) != 0 {
		t.Fatal("a handshake was recorded")
	}
	// The interactivity URL is served by the same endpoint.
	if again := h.serve(h.fake.Click("", slackfake.Click{ActionID: ActionDeny, Value: "t1.x"})); again.Code != http.StatusOK {
		t.Fatalf("interactivity = %d", again.Code)
	}
	if count := len(h.store.endpoints); count != 1 {
		t.Fatalf("endpoints = %d, want the one deployment endpoint", count)
	}
}

// Slack waits three seconds. The database gets two of them, and a delivery
// that cannot be recorded in that time is answered 5xx so Slack sends it
// again, rather than left to hang until Slack gives up on us.
func TestTheAckPathHasATwoSecondDatabaseDeadline(t *testing.T) {
	if hooks.DefaultSurfaceDeadline != 2*time.Second {
		t.Fatalf("the shipped deadline is %v, want 2s", hooks.DefaultSurfaceDeadline)
	}
	h := newHarness(t)
	h.installFor(testOrg)
	if response := h.mention(slackfake.Mention{Text: askText}); response.Code != http.StatusOK {
		t.Fatal(response.Body)
	}
	h.store.mu.Lock()
	limited, deadline := h.store.limited, h.store.deadline
	h.store.mu.Unlock()
	if !limited || time.Until(deadline) > 2*time.Second {
		t.Fatalf("the transaction ran with deadline %v (limited %v)", deadline, limited)
	}

	slow := newHarness(t, func(o *harnessOptions) { o.surfaceDeadline = 50 * time.Millisecond })
	slow.installFor(testOrg)
	slow.store.gate = make(chan struct{}) // the database never answers
	start := time.Now()
	response := slow.mention(slackfake.Mention{Text: askText})
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("a delivery that could not be recorded in time = %d %s, want 503", response.Code, response.Body)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("it took %v to give up", elapsed)
	}
	if len(slow.store.inboundRows()) != 0 || len(slow.store.handled) != 0 {
		t.Fatal("something was kept of a delivery that failed: Slack's retry would be refused as a repeat")
	}
}

// The signing scheme is the one Slack's requests to any app use, and the same
// one members' own Slack integration declares.
func TestTheSurfacesSchemeIsTheSharedV0Scheme(t *testing.T) {
	h := newHarness(t)
	scheme := h.service.Webhook()
	shared := slack.New().Manifest().Webhook
	if !reflect.DeepEqual(scheme, *shared) {
		t.Fatalf("the surface's scheme differs from the members' Slack integration's:\n got  %+v\n want %+v", scheme, *shared)
	}
	manifest := integration.Manifest{
		Toolkit: Toolkit, Version: 1, Name: "Oryxa's Slack bot", APIBase: "https://slack.com/api",
		Auth: integration.Auth{
			Methods: []integration.Method{{
				Kind:  integration.KindAPIToken,
				Place: &integration.Place{In: integration.InHeader, Name: "Authorization", Template: "Bearer {token}"},
			}},
			Test: integration.Test{Path: "/auth.test", Label: "/team", AccountRef: "/user_id"},
		},
		Webhook: &scheme,
	}
	if err := manifest.Validate(); err != nil {
		t.Fatalf("the surface's webhook breaks the core's rules: %v", err)
	}
}

func TestTheInboundViewNamesTheBotAndTheThreadToReplyIn(t *testing.T) {
	h := newHarness(t)
	h.installFor(testOrg)
	h.mention(slackfake.Mention{Text: askText, TS: "1780000600.000200", ThreadTS: "1780000000.000100"})
	row := h.lastInbound()
	view, err := h.service.Inbound(t.Context(), row.ID)
	if err != nil {
		t.Fatal(err)
	}
	if view.BotUserID != slackfake.DefaultBotUserID || view.Kind != InboundMessage || view.ThreadTS != "1780000000.000100" ||
		view.MessageTS != "1780000600.000200" || view.Text != askText || !slices.Equal(view.Mentions, []string{"U0RAYA", "U0MAYA"}) {
		t.Fatalf("view = %+v", view)
	}
	if _, err := h.service.Inbound(t.Context(), "asi_unknown"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("an unknown record = %v", err)
	}
}
