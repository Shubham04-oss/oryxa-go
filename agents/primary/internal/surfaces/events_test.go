package surfaces

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	agentsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/agents/v1"
)

func changeEvent(t *testing.T, mutate func(*agentsv1.InteractionChanged)) []byte {
	t.Helper()
	changed := agentsv1.InteractionChanged{
		RequestID: "irq_1", OrganizationID: "org_1", RequesterUserID: "user_asker", RequesterAgentID: "agt_asker",
		TargetUserID: "user_raya", TargetAgentID: "agt_raya", Status: "pending", Revision: 1,
		ChangedAt: time.Now(), AskID: "iak_1", Origin: agentsv1.InteractionOriginSlack,
	}
	if mutate != nil {
		mutate(&changed)
	}
	data, err := json.Marshal(changed)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestTheServiceConsumesTheThreeLifecycleEventsOfTheLedger(t *testing.T) {
	want := []string{agentsv1.InteractionRequestedDetailType, agentsv1.InteractionDecidedDetailType, agentsv1.InteractionCompletedDetailType}
	subscriptions := Subscriptions()
	if len(subscriptions) != 1 || len(subscriptions[agentsv1.Source]) != 3 {
		t.Fatalf("Subscriptions() = %v", subscriptions)
	}
	for _, detailType := range want {
		if !Handles(detailType) {
			t.Errorf("Handles(%q) = false", detailType)
		}
		found := false
		for _, subscribed := range subscriptions[agentsv1.Source] {
			found = found || subscribed == detailType
		}
		if !found {
			t.Errorf("%q is not subscribed", detailType)
		}
	}
	// A revoked lease and an opened secret say nothing in Slack.
	for _, detailType := range []string{
		agentsv1.InteractionLeaseRevokedDetailType, agentsv1.InteractionSecretViewedDetailType,
		agentsv1.RunCompletedDetailType, "apps.slack.message.received.v1", "",
	} {
		if Handles(detailType) {
			t.Errorf("Handles(%q) = true", detailType)
		}
	}
}

func TestOnlyAnEventAboutARequestAskedInSlackWakesAnything(t *testing.T) {
	for _, detailType := range []string{RequestedDetailType, DecidedDetailType, CompletedDetailType} {
		wake, ours, err := wakeFromEvent(detailType, "evt_1", changeEvent(t, nil))
		if err != nil || !ours {
			t.Fatalf("%s: wakeFromEvent() = %+v, %v, %v", detailType, wake, ours, err)
		}
		if want := (Wake{RequestID: "irq_1", OrganizationID: "org_1", EventID: "evt_1", AskerUserID: "user_asker", OwnerUserID: "user_raya"}); wake != want {
			t.Fatalf("%s: wake = %+v, want %+v", detailType, wake, want)
		}
	}
	for name, mutate := range map[string]func(*agentsv1.InteractionChanged){
		"an agent's request":              func(c *agentsv1.InteractionChanged) { c.Origin, c.AskID = agentsv1.InteractionOriginAgent, "" },
		"one from before origins existed": func(c *agentsv1.InteractionChanged) { c.Origin, c.AskID = "", "" },
		"an origin nobody knows":          func(c *agentsv1.InteractionChanged) { c.Origin = "email" },
		"a request in a teamspace": func(c *agentsv1.InteractionChanged) {
			c.Origin, c.TeamspaceID = agentsv1.InteractionOriginAgent, "ts_1"
		},
	} {
		if wake, ours, err := wakeFromEvent(CompletedDetailType, "evt_1", changeEvent(t, mutate)); err != nil || ours || wake != (Wake{}) {
			t.Errorf("%s: wakeFromEvent() = %+v, %v, %v, want an event that is not ours", name, wake, ours, err)
		}
	}
}

func TestAnEventThatCannotBeReadIsAnError(t *testing.T) {
	for name, test := range map[string]struct {
		detailType string
		data       []byte
	}{
		"not JSON":                  {RequestedDetailType, []byte("not json")},
		"a JSON list":               {RequestedDetailType, []byte("[]")},
		"no request id":             {RequestedDetailType, changeEvent(t, func(c *agentsv1.InteractionChanged) { c.RequestID = " " })},
		"no organization id":        {DecidedDetailType, changeEvent(t, func(c *agentsv1.InteractionChanged) { c.OrganizationID = "" })},
		"an event that is not ours": {agentsv1.InteractionSecretViewedDetailType, changeEvent(t, nil)},
	} {
		if _, _, err := wakeFromEvent(test.detailType, "evt_1", test.data); err == nil {
			t.Errorf("%s: wakeFromEvent() accepted it", name)
		} else if !strings.Contains(err.Error(), "evt_1") {
			t.Errorf("%s: error %q does not name the event", name, err)
		}
	}
}

func TestAnEventsErrorNeverSaysWhatItCarried(t *testing.T) {
	// Events carry ids only, but an error for one that cannot be read is logged,
	// and says nothing of the bytes it could not read.
	_, _, err := wakeFromEvent(RequestedDetailType, "evt_1", []byte(`{"request_id": "irq_1", "secret": nope-SECRET-VALUE`))
	if err == nil {
		t.Fatal("want an error")
	}
	if strings.Contains(err.Error(), "SECRET-VALUE") {
		t.Fatalf("the error says what the event carried: %v", err)
	}
}
