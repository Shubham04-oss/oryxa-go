package slackrouter

import (
	"encoding/json"
	"testing"
	"time"

	appsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/apps/v1"
)

// The router reads apps' events as apps' contract writes them: a message and a
// click each from their own type, the detail types from the contract's own
// constants, and a job made of what they say.
func TestTheRouterReadsAppsEventsAsTheContractWritesThem(t *testing.T) {
	receivedAt := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	message, err := json.Marshal(appsv1.SlackMessageReceived{InboundID: "asi_1", OrganizationID: "org_1", TeamID: "T1", ReceivedAt: receivedAt})
	if err != nil {
		t.Fatal(err)
	}
	action, err := json.Marshal(appsv1.SlackActionReceived{InboundID: "asi_2", OrganizationID: "org_1", TeamID: "T1", ReceivedAt: receivedAt})
	if err != nil {
		t.Fatal(err)
	}
	for name, test := range map[string]struct {
		detailType string
		data       []byte
		want       Job
	}{
		"a message": {appsv1.SlackMessageReceivedDetailType, message, Job{InboundID: "asi_1", Kind: KindMessage, OrganizationID: "org_1", TeamID: "T1", EventID: "evt_1", ReceivedAt: receivedAt}},
		"a click":   {appsv1.SlackActionReceivedDetailType, action, Job{InboundID: "asi_2", Kind: KindAction, OrganizationID: "org_1", TeamID: "T1", EventID: "evt_1", ReceivedAt: receivedAt}},
	} {
		t.Run(name, func(t *testing.T) {
			if !Handles(test.detailType) {
				t.Fatalf("Handles(%q) = false, want the router to take apps' own detail type", test.detailType)
			}
			got, err := jobFromEvent(test.detailType, "evt_1", test.data, time.Now())
			if err != nil || got != test.want {
				t.Fatalf("jobFromEvent() = %+v, %v; want %+v", got, err, test.want)
			}
		})
	}
	// The install's own event is the surfaces' and the consumer's to read, not a job.
	if Handles(appsv1.SlackInstallChangedDetailType) {
		t.Fatalf("Handles(%q) = true, but an install changing is no inbound delivery", appsv1.SlackInstallChangedDetailType)
	}
	subscribed := Subscriptions()[appsv1.Source]
	if len(subscribed) != 2 || subscribed[0] != appsv1.SlackMessageReceivedDetailType || subscribed[1] != appsv1.SlackActionReceivedDetailType {
		t.Fatalf("Subscriptions()[apps] = %v, want the contract's two inbound types", subscribed)
	}
}
