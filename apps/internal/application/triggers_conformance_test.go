package application

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	appsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/apps/v1"

	"github.com/Shubham04-oss/oryxa-go/apps/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/internal/composio"
)

// A trigger in the catalog, as apps must handle it: what Composio publishes
// about it, a config a member would subscribe with, and the payload the app
// delivers.
type catalogTrigger struct {
	definition composio.TriggerType
	// config is what a member subscribes with, and must be accepted.
	config string
	// payload is what the app delivers, verbatim in the shape Composio
	// wraps it in.
	payload string
	// refuses, when set, is a config that must be refused instead of
	// subscribed - one that would be accepted and then deliver nothing.
	refuses string
}

// catalogTriggers is a trigger of each shape Composio's catalog holds,
// across apps that differ in every way a trigger can: pushed against polled,
// required config against none, an app whose webhook a member must register
// by hand, payloads from one field to deeply nested, and the awkward ones -
// an empty payload, a payload that is an array, one carrying characters that
// must survive being carried on the bus.
//
// The point is coverage of *shapes* rather than of apps: Composio has
// thousands of triggers and a test cannot name them, but every one of them
// is some combination of these, and the pipeline is blind to which app it
// came from.
func catalogTriggers() map[string]catalogTrigger {
	return map[string]catalogTrigger{
		"a pushed event needing a repository": {
			definition: composio.TriggerType{
				Slug: "GITHUB_COMMIT_EVENT", Name: "New commit", Toolkit: "github", Kind: "webhook",
				Config: json.RawMessage(`{"type":"object","properties":{"owner":{"type":"string"},
					"repo":{"type":"string"}},"required":["owner","repo"]}`),
			},
			config:  `{"owner":"oryxa-labs","repo":"oryxa"}`,
			payload: `{"id":"c1","sha":"abc123","message":"first","author":{"login":"ada"}}`,
			refuses: `{"repo":"oryxa"}`,
		},
		"a polled event needing nothing": {
			definition: composio.TriggerType{
				Slug: "GITHUB_STAR_ADDED_EVENT", Name: "Star added", Toolkit: "github", Kind: "poll",
				Config: json.RawMessage(`{"type":"object","properties":{}}`),
			},
			config:  `{}`,
			payload: `{"repository":"oryxa-labs/oryxa","starred_by":"ada"}`,
		},
		"an app whose webhook a member registers by hand": {
			definition: composio.TriggerType{
				Slug: "SLACK_RECEIVE_MESSAGE", Name: "New message", Toolkit: "slack", Kind: "webhook",
				Instructions:                 "Add the URL to your Slack app's Event Subscriptions.",
				RequiresWebhookEndpointSetup: true,
				Config: json.RawMessage(`{"type":"object","properties":{"channel":{"type":"string"}},
					"required":["channel"]}`),
			},
			config:  `{"channel":"C0123456789"}`,
			payload: `{"channel":"C0123456789","user":"U1","text":"ship it","ts":"1758110400.000100"}`,
			refuses: `{}`,
		},
		"a config with an enum": {
			definition: composio.TriggerType{
				Slug: "LINEAR_ISSUE_UPDATED", Name: "Issue updated", Toolkit: "linear", Kind: "webhook",
				Config: json.RawMessage(`{"type":"object","properties":{"state":{"type":"string",
					"enum":["backlog","started","completed"]}},"required":["state"]}`),
			},
			config:  `{"state":"completed"}`,
			payload: `{"issue":{"id":"LIN-1","title":"Ship triggers","state":"completed"}}`,
			refuses: `{"state":"archived"}`,
		},
		"a config with a number": {
			definition: composio.TriggerType{
				Slug: "GMAIL_NEW_GMAIL_MESSAGE", Name: "New mail", Toolkit: "gmail", Kind: "poll",
				Config: json.RawMessage(`{"type":"object","properties":{"interval":{"type":"integer"},
					"labelIds":{"type":"array","items":{"type":"string"}}},"required":["interval"]}`),
			},
			config:  `{"interval":60,"labelIds":["INBOX","IMPORTANT"]}`,
			payload: `{"messageId":"18f","subject":"Re: triggers","from":"ada@example.com"}`,
			refuses: `{"interval":"every minute"}`,
		},
		"a deeply nested payload": {
			definition: composio.TriggerType{
				Slug: "NOTION_PAGE_UPDATED", Name: "Page updated", Toolkit: "notion", Kind: "poll",
				Config: json.RawMessage(`{"type":"object","properties":{"database_id":{"type":"string"}}}`),
			},
			config: `{"database_id":"d1"}`,
			payload: `{"page":{"id":"p1","properties":{"Name":{"title":[{"text":{"content":"Triggers"},
				"annotations":{"bold":true}}]},"Tags":{"multi_select":[{"name":"apps"},{"name":"composio"}]}}}}`,
		},
		"a payload that is an array": {
			definition: composio.TriggerType{
				Slug: "GOOGLECALENDAR_EVENT_STARTED", Name: "Event started", Toolkit: "googlecalendar", Kind: "poll",
				Config: json.RawMessage(`{"type":"object","properties":{"calendarId":{"type":"string",
					"default":"primary"}}}`),
			},
			config:  `{}`,
			payload: `[{"id":"e1","summary":"Standup"},{"id":"e2","summary":"Review"}]`,
		},
		"an empty payload": {
			definition: composio.TriggerType{
				Slug: "STRIPE_PAYMENT_SUCCEEDED", Name: "Payment succeeded", Toolkit: "stripe", Kind: "webhook",
				Config: json.RawMessage(`{"type":"object","properties":{}}`),
			},
			config:  `{}`,
			payload: `{}`,
		},
		"a payload carrying characters the bus must not mangle": {
			definition: composio.TriggerType{
				Slug: "JIRA_ISSUE_CREATED", Name: "Issue created", Toolkit: "jira", Kind: "webhook",
				Config: json.RawMessage(`{"type":"object","properties":{"projectKey":{"type":"string"}},
					"required":["projectKey"]}`),
			},
			config: `{"projectKey":"ORY"}`,
			payload: `{"key":"ORY-1","summary":"Ünïcode, \"quotes\", <tags> & 中文 🎉",
				"description":"line one\nline two\ttabbed"}`,
			refuses: `{"projectKey":42}`,
		},
		"a config whose shape the catalog does not spell out": {
			definition: composio.TriggerType{
				Slug: "HUBSPOT_CONTACT_CREATED", Name: "Contact created", Toolkit: "hubspot", Kind: "poll",
				Config: json.RawMessage(`{"type":"object","properties":{"filter":{"$ref":"#/$defs/Filter"}},
					"required":["filter"]}`),
			},
			config:  `{"filter":{"propertyName":"email","operator":"HAS_PROPERTY"}}`,
			payload: `{"contactId":501,"properties":{"email":"ada@example.com"}}`,
		},
		"a trigger the catalog publishes no config schema for": {
			definition: composio.TriggerType{
				Slug: "DISCORD_MESSAGE_CREATE", Name: "Message created", Toolkit: "discord", Kind: "webhook",
			},
			config:  `{"guild_id":"g1","channel_id":"c1"}`,
			payload: `{"content":"hello","author":{"id":"u1"}}`,
		},
	}
}

// harnessFor builds a service whose catalog holds exactly these triggers,
// with an active connection for each app they belong to.
func harnessFor(t *testing.T, triggers map[string]catalogTrigger) (harness, map[string]domain.Connection) {
	t.Helper()
	h := newHarness(t)
	h.composio.triggerTypes = map[string][]composio.TriggerType{}
	connections := map[string]domain.Connection{}
	for _, trigger := range triggers {
		toolkit := trigger.definition.Toolkit
		h.composio.triggerTypes[toolkit] = append(h.composio.triggerTypes[toolkit], trigger.definition)
		if _, made := connections[toolkit]; !made {
			connections[toolkit] = h.activeConnection("apc_"+toolkit, "user-1", toolkit, "ca_"+toolkit)
		}
	}
	return h, connections
}

// TestEveryTriggerShapeSubscribesAndDelivers walks one trigger of every
// shape the catalog holds through the whole pipeline a real one takes:
// subscribed against its own definition, delivered as a signed webhook,
// turned into the event the bus carries, with its payload intact and its
// delivery counted against the trigger it came from.
func TestEveryTriggerShapeSubscribesAndDelivers(t *testing.T) {
	t.Parallel()
	triggers := catalogTriggers()
	h, connections := harnessFor(t, triggers)
	ctx := context.Background()

	webhook := 0
	for name, want := range triggers {
		t.Run(name, func(t *testing.T) {
			connection := connections[want.definition.Toolkit]
			h.composio.upsertID = "ti_" + want.definition.Slug

			trigger, err := h.service.CreateTrigger(ctx, memberToken, "org_1", TriggerInput{
				ConnectionID: connection.ID, Trigger: want.definition.Slug, Config: json.RawMessage(want.config),
			})
			if err != nil {
				t.Fatalf("subscribing to %s: %v", want.definition.Slug, err)
			}
			// What the member is told about this subscription must match
			// what the catalog says, since it decides whether they wait for
			// an event or go and register a webhook.
			if trigger.Kind != want.definition.Kind || trigger.ManualSetup != want.definition.RequiresWebhookEndpointSetup {
				t.Fatalf("kind = %q, manual setup = %v; the catalog says %q and %v",
					trigger.Kind, trigger.ManualSetup, want.definition.Kind, want.definition.RequiresWebhookEndpointSetup)
			}
			if trigger.ManualSetup && !strings.Contains(TriggerSetup(trigger), "registered in the app itself") {
				t.Fatalf("a trigger needing a webhook registered says %q", TriggerSetup(trigger))
			}
			if trigger.Kind == domain.TriggerKindPoll && TriggerSetup(trigger) == "" {
				t.Fatal("a polled trigger tells the member nothing about when to expect an event")
			}

			// The app delivers. Everything from the signature onwards is the
			// path a real delivery takes.
			webhook++
			id := fmt.Sprintf("wh_%d", webhook)
			body := fmt.Sprintf(`{"id":%q,"type":%q,"timestamp":"2026-09-17T12:00:00Z",
				"metadata":{"trigger_id":%q,"trigger_slug":%q,"connected_account_id":%q},"data":%s}`,
				id, composio.EventTriggerMessage, trigger.ComposioTriggerID, want.definition.Slug,
				connection.ComposioAccountID, want.payload)
			header, signed := delivery(t, "whsec_test", id, testClock, body)
			if err := h.service.ReceiveWebhook(ctx, header, signed); err != nil {
				t.Fatalf("delivering %s: %v", want.definition.Slug, err)
			}

			received := receivedFor(t, h, trigger.ID)
			if received.Toolkit != want.definition.Toolkit || received.Trigger != want.definition.Slug {
				t.Fatalf("event named %s/%s, want %s/%s", received.Toolkit, received.Trigger,
					want.definition.Toolkit, want.definition.Slug)
			}
			if received.MemberID != "user-1" || received.OrganizationID != "org_1" {
				t.Fatalf("event is for %s in %s", received.MemberID, received.OrganizationID)
			}
			if received.DataOmitted {
				t.Fatal("a payload this size was left out of the event")
			}
			// The app's payload reaches the bus meaning exactly what it did
			// on the wire - every app's own shape, untouched.
			if !sameJSON(t, received.Data, json.RawMessage(want.payload)) {
				t.Fatalf("payload became %s, want %s", received.Data, want.payload)
			}
			// A trigger that delivered says so, which is the only way anyone
			// can tell a live subscription from a silent one.
			if delivered := h.store.triggers[trigger.ID]; delivered.DeliveryCount != 1 || delivered.LastDeliveryAt == nil {
				t.Fatalf("deliveries counted = %d, last = %v", delivered.DeliveryCount, delivered.LastDeliveryAt)
			}

			// Composio redelivers what it is not sure reached us. The same
			// delivery must not become a second event, nor count twice.
			if err := h.service.ReceiveWebhook(ctx, header, signed); err != nil {
				t.Fatalf("redelivering %s: %v", want.definition.Slug, err)
			}
			if again := h.store.triggers[trigger.ID]; again.DeliveryCount != 1 {
				t.Fatalf("a redelivery counted again: %d", again.DeliveryCount)
			}
			if events := receivedEvents(t, h, trigger.ID); len(events) != 1 {
				t.Fatalf("a redelivery published %d events", len(events))
			}
		})
	}
}

// TestEveryTriggerShapeRefusesAConfigThatWouldNeverDeliver is the other half:
// a subscription Composio would accept and then deliver nothing for is
// refused here, where the member can still do something about it.
func TestEveryTriggerShapeRefusesAConfigThatWouldNeverDeliver(t *testing.T) {
	t.Parallel()
	triggers := catalogTriggers()
	h, connections := harnessFor(t, triggers)

	for name, want := range triggers {
		if want.refuses == "" {
			continue
		}
		t.Run(name, func(t *testing.T) {
			_, err := h.service.CreateTrigger(context.Background(), memberToken, "org_1", TriggerInput{
				ConnectionID: connections[want.definition.Toolkit].ID, Trigger: want.definition.Slug,
				Config: json.RawMessage(want.refuses),
			})
			if !errors.Is(err, domain.ErrInvalid) {
				t.Fatalf("subscribing with %s answered %v, want invalid", want.refuses, err)
			}
		})
	}
	if len(h.composio.upserts) != 0 {
		t.Fatalf("subscribed at Composio anyway: %v", h.composio.upserts)
	}
}

// TestSubscribingToAnotherAppsTriggerIsNotFound covers the mistake that is
// invisible afterwards: a trigger slug that exists, on a connection whose
// app does not deliver it.
func TestSubscribingToAnotherAppsTriggerIsNotFound(t *testing.T) {
	t.Parallel()
	triggers := catalogTriggers()
	h, connections := harnessFor(t, triggers)

	_, err := h.service.CreateTrigger(context.Background(), memberToken, "org_1", TriggerInput{
		// A real Slack trigger, asked for on a GitHub connection.
		ConnectionID: connections["github"].ID, Trigger: "SLACK_RECEIVE_MESSAGE",
		Config: json.RawMessage(`{"channel":"C1"}`),
	})
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("error = %v, want not found", err)
	}
	if !strings.Contains(err.Error(), "github") {
		t.Fatalf("the member is told %q, which does not say which app was asked", err)
	}
	if len(h.composio.upserts) != 0 {
		t.Fatalf("subscribed at Composio anyway: %v", h.composio.upserts)
	}
}

// TestSubscribingToATriggerNoAppDeliversIsNotFound covers a slug that is
// well formed and simply does not exist.
func TestSubscribingToATriggerNoAppDeliversIsNotFound(t *testing.T) {
	t.Parallel()
	h, connections := harnessFor(t, catalogTriggers())

	_, err := h.service.CreateTrigger(context.Background(), memberToken, "org_1", TriggerInput{
		ConnectionID: connections["github"].ID, Trigger: "GITHUB_INVENTED_EVENT",
	})
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("error = %v, want not found", err)
	}
}

// TestTheCatalogIsReadOncePerAppAndWalkedInFull keeps the check on the
// subscribe path from becoming a catalog walk per subscription, and proves
// a trigger on the last page is found as readily as one on the first.
func TestTheCatalogIsReadOncePerAppAndWalkedInFull(t *testing.T) {
	t.Parallel()
	triggers := catalogTriggers()
	h, connections := harnessFor(t, triggers)
	// One definition per page, so finding any of GitHub's needs the walk.
	h.composio.triggerPages = 1
	ctx := context.Background()

	for _, slug := range []string{"GITHUB_COMMIT_EVENT", "GITHUB_STAR_ADDED_EVENT"} {
		config := json.RawMessage(`{}`)
		if slug == "GITHUB_COMMIT_EVENT" {
			config = json.RawMessage(`{"owner":"oryxa-labs","repo":"oryxa"}`)
		}
		h.composio.upsertID = "ti_" + slug
		if _, err := h.service.CreateTrigger(ctx, memberToken, "org_1", TriggerInput{
			ConnectionID: connections["github"].ID, Trigger: slug, Config: config,
		}); err != nil {
			t.Fatalf("subscribing to %s: %v", slug, err)
		}
	}
	// GitHub holds two definitions over two pages: one walk, not one per
	// subscription.
	if h.composio.triggerLists != 2 {
		t.Fatalf("read the catalog %d times, want the one walk of two pages", h.composio.triggerLists)
	}
}

// TestSubscribingSurfacesACatalogThatCannotBeRead keeps a Composio outage
// from looking like a trigger that does not exist.
func TestSubscribingSurfacesACatalogThatCannotBeRead(t *testing.T) {
	t.Parallel()
	h, connections := harnessFor(t, catalogTriggers())
	h.composio.triggerTypeErr = composio.ErrUnavailable

	_, err := h.service.CreateTrigger(context.Background(), memberToken, "org_1", TriggerInput{
		ConnectionID: connections["github"].ID, Trigger: "GITHUB_COMMIT_EVENT",
		Config: json.RawMessage(`{"owner":"oryxa-labs","repo":"oryxa"}`),
	})
	if !errors.Is(err, domain.ErrUnavailable) {
		t.Fatalf("error = %v, want unavailable", err)
	}
	if errors.Is(err, domain.ErrNotFound) {
		t.Fatal("an unreadable catalog reads as a trigger that does not exist")
	}
}

// receivedFor answers the one trigger.received event published for a
// trigger.
func receivedFor(t *testing.T, h harness, triggerID string) appsv1.TriggerReceived {
	t.Helper()
	events := receivedEvents(t, h, triggerID)
	if len(events) != 1 {
		t.Fatalf("events published for %s = %d, want 1", triggerID, len(events))
	}
	return events[0]
}

func receivedEvents(t *testing.T, h harness, triggerID string) []appsv1.TriggerReceived {
	t.Helper()
	var matched []appsv1.TriggerReceived
	for _, event := range h.store.published(appsv1.TriggerReceivedDetailType) {
		var received appsv1.TriggerReceived
		if err := json.Unmarshal(event.Data, &received); err != nil {
			t.Fatalf("the event apps published is not %s: %v", appsv1.TriggerReceivedDetailType, err)
		}
		if received.TriggerID == triggerID {
			matched = append(matched, received)
		}
	}
	return matched
}

// sameJSON compares two payloads by what they mean rather than how they are
// written, so whitespace and key order do not decide the test.
func sameJSON(t *testing.T, left, right json.RawMessage) bool {
	t.Helper()
	canonical := func(value json.RawMessage) string {
		var decoded any
		if err := json.Unmarshal(value, &decoded); err != nil {
			t.Fatalf("payload is not JSON: %v", err)
		}
		encoded, err := json.Marshal(decoded)
		if err != nil {
			t.Fatalf("payload cannot be re-encoded: %v", err)
		}
		return string(encoded)
	}
	return canonical(left) == canonical(right)
}
