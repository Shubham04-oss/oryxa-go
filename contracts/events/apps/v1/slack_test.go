package v1

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// The Slack events are spelled as the contract pins them: snake_case, times
// in UTC, ids only. Nothing a person typed or clicked is in them.
func TestSlackEventsAreWrittenAsTheContractSays(t *testing.T) {
	at := time.Date(2026, time.September, 29, 9, 15, 0, 0, time.UTC)
	for name, test := range map[string]struct {
		value any
		want  string
	}{
		"a message": {
			SlackMessageReceived{InboundID: "asi_1", OrganizationID: "org_1", TeamID: "T0123ABC", ReceivedAt: at},
			`{"inbound_id":"asi_1","organization_id":"org_1","team_id":"T0123ABC","received_at":"2026-09-29T09:15:00Z"}`,
		},
		"a click": {
			SlackActionReceived{InboundID: "asi_2", OrganizationID: "org_1", TeamID: "T0123ABC", ReceivedAt: at},
			`{"inbound_id":"asi_2","organization_id":"org_1","team_id":"T0123ABC","received_at":"2026-09-29T09:15:00Z"}`,
		},
		"an install": {
			SlackInstallChanged{OrganizationID: "org_1", TeamID: "T0123ABC", State: SlackInstallActive, Reason: SlackInstalled,
				ActorID: "user-1", ChangedAt: at},
			`{"organization_id":"org_1","team_id":"T0123ABC","state":"active","reason":"installed","actor_id":"user-1","changed_at":"2026-09-29T09:15:00Z"}`,
		},
		"an install ended by Slack": {
			SlackInstallChanged{OrganizationID: "org_1", TeamID: "T0123ABC", State: SlackInstallEnded, Reason: SlackAppUninstalled,
				ChangedAt: at},
			`{"organization_id":"org_1","team_id":"T0123ABC","state":"ended","reason":"app_uninstalled","changed_at":"2026-09-29T09:15:00Z"}`,
		},
	} {
		t.Run(name, func(t *testing.T) {
			encoded, err := json.Marshal(test.value)
			if err != nil {
				t.Fatal(err)
			}
			if string(encoded) != test.want {
				t.Fatalf("encoded\n got  %s\n want %s", encoded, test.want)
			}
		})
	}
}

func TestTheSlackDetailTypesAreVersionOne(t *testing.T) {
	for _, detailType := range []string{
		SlackMessageReceivedDetailType, SlackActionReceivedDetailType, SlackInstallChangedDetailType,
	} {
		if !strings.HasPrefix(detailType, "apps.slack.") || !strings.HasSuffix(detailType, ".v1") {
			t.Errorf("%q is not an apps.slack version-one detail type", detailType)
		}
	}
}

// A click carries a ticket and a message carries words, and neither belongs
// on the bus: the events are ids, so a field added to one has to be added to
// this list on purpose.
func TestSlackEventsCarryIdsOnly(t *testing.T) {
	for name, value := range map[string]any{
		"message": SlackMessageReceived{}, "action": SlackActionReceived{}, "install": SlackInstallChanged{},
	} {
		encoded, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(encoded, &fields); err != nil {
			t.Fatal(err)
		}
		for field := range fields {
			switch field {
			case "inbound_id", "organization_id", "team_id", "received_at", "state", "reason", "changed_at":
			default:
				t.Errorf("the %s event has a %q field: the Slack events carry ids only", name, field)
			}
		}
	}
}
