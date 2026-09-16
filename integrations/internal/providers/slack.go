package providers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	integrationsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/integrations/v1"

	"github.com/Shubham04-oss/oryxa-go/integrations/internal/domain"
)

// slackTolerance is how far a request's timestamp may be from now, as Slack
// recommends, before it is refused as a replay.
const slackTolerance = 5 * time.Minute

var slackEventPattern = regexp.MustCompile(`^[a-z_]{1,64}$`)

// Slack delivers every event for a Slack app to the one Request URL set on the
// app, signed with its signing secret. Integrations routes an event to the
// subscriptions of the workspaces whose installation received it; the Slack
// app must subscribe to the event types subscriptions ask for.
func Slack() Provider {
	return slackProvider("https://slack.com/api")
}

func slackProvider(apiURL string) Provider {
	return Provider{
		Name:         "slack",
		Delivery:     DeliveryApp,
		Target:       slackTarget,
		AccountKey:   slackAccountKey,
		Receive:      slackReceive,
		TargetFields: []TargetField{},
		Events: EventsDescription{
			Names:   []string{"message", "app_mention", "reaction_added", "reaction_removed", "channel_created", "team_join", "file_shared"},
			Open:    true,
			Default: "every event the Slack app subscribes to",
		},
		Actions: slackActions(apiURL),
		Skill:   skill("slack"),
	}
}

// slackTarget takes no target, since events reach the whole workspace, and
// Slack event types, such as message or reaction_added.
func slackTarget(target map[string]string, events []string) (map[string]string, []string, error) {
	if len(target) > 0 {
		return nil, nil, fmt.Errorf("a Slack subscription takes no target: %w", domain.ErrInvalid)
	}
	for _, event := range events {
		if !slackEventPattern.MatchString(event) {
			return nil, nil, fmt.Errorf("%q is not a Slack event type: %w", event, domain.ErrInvalid)
		}
	}
	return map[string]string{}, events, nil
}

// slackAccountKey is the workspace of a Slack connection's account, which
// connections names team:user.
func slackAccountKey(account domain.ProviderAccount) (string, error) {
	team, _, _ := strings.Cut(account.ID, ":")
	if team == "" {
		return "", fmt.Errorf("Slack account %q names no workspace: %w", account.ID, domain.ErrInvalid)
	}
	return team, nil
}

type slackEnvelope struct {
	Type           string          `json:"type"`
	Challenge      string          `json:"challenge"`
	TeamID         string          `json:"team_id"`
	EventID        string          `json:"event_id"`
	EventTime      int64           `json:"event_time"`
	Event          json.RawMessage `json:"event"`
	Authorizations []struct {
		TeamID string `json:"team_id"`
	} `json:"authorizations"`
}

// slackReceive verifies a request's signature over its timestamp and body and
// reads it: the URL verification challenge Slack sends when the Request URL
// is set, or an event.
func slackReceive(delivery Delivered, secret []byte, now time.Time) (Reception, error) {
	timestamp := delivery.Header.Get("X-Slack-Request-Timestamp")
	seconds, err := strconv.ParseInt(timestamp, 10, 64)
	if err != nil {
		return Reception{}, fmt.Errorf("%w: Slack timestamp", ErrUnverified)
	}
	if age := now.Sub(time.Unix(seconds, 0)); age > slackTolerance || age < -slackTolerance {
		return Reception{}, fmt.Errorf("%w: Slack request is too old", ErrUnverified)
	}
	signature, ok := strings.CutPrefix(delivery.Header.Get("X-Slack-Signature"), "v0=")
	message := append([]byte("v0:"+timestamp+":"), delivery.Body...)
	if !ok || !ValidHMACSHA256(secret, message, signature) {
		return Reception{}, fmt.Errorf("%w: Slack signature", ErrUnverified)
	}
	var envelope slackEnvelope
	if err := json.Unmarshal(delivery.Body, &envelope); err != nil {
		return Reception{}, fmt.Errorf("%w: Slack payload is not a JSON object", domain.ErrInvalid)
	}
	switch envelope.Type {
	case "url_verification":
		return Reception{Reply: &Reply{Status: http.StatusOK, ContentType: "text/plain", Body: []byte(envelope.Challenge)}}, nil
	case "event_callback":
	default:
		// app_rate_limited and anything newer carry no event.
		return Reception{}, nil
	}
	var teams []string
	for _, authorization := range envelope.Authorizations {
		teams = append(teams, authorization.TeamID)
	}
	teams = append(teams, envelope.TeamID)
	teams = slices.DeleteFunc(teams, func(team string) bool { return team == "" })
	slices.Sort(teams)
	teams = slices.Compact(teams)

	var event map[string]json.RawMessage
	if err := json.Unmarshal(envelope.Event, &event); err != nil {
		return Reception{}, fmt.Errorf("%w: Slack event is not a JSON object", domain.ErrInvalid)
	}
	var eventType string
	if json.Unmarshal(event["type"], &eventType) != nil || !slackEventPattern.MatchString(eventType) {
		return Reception{}, fmt.Errorf("%w: Slack event has no type", domain.ErrInvalid)
	}
	key := envelope.EventID
	if key == "" {
		key = Digest(delivery.Body)
	}
	var occurredAt time.Time
	if envelope.EventTime > 0 {
		occurredAt = time.Unix(envelope.EventTime, 0).UTC()
	}
	return Reception{AccountKeys: teams, Events: []Event{{
		Name:       eventType,
		Type:       eventType,
		DeliveryID: envelope.EventID,
		Key:        key,
		Resource:   slackResource(eventType, event),
		Actor:      slackActor(event["user"]),
		OccurredAt: occurredAt,
		Payload:    withoutFields(delivery.Body, "token"),
	}}}, nil
}

// slackResource is what a Slack event is about: a message, the item a
// reaction is on, a channel, a user, a file, or else just its type.
func slackResource(eventType string, event map[string]json.RawMessage) integrationsv1.Resource {
	text := func(field string) string {
		var value string
		_ = json.Unmarshal(event[field], &value)
		return value
	}
	switch {
	case eventType == "message" || eventType == "app_mention":
		return integrationsv1.Resource{Type: "message", ID: text("ts"), Name: text("channel")}
	case strings.HasPrefix(eventType, "reaction_"):
		var item struct {
			TS      string `json:"ts"`
			Channel string `json:"channel"`
		}
		_ = json.Unmarshal(event["item"], &item)
		return integrationsv1.Resource{Type: "reaction", ID: item.TS, Name: text("reaction")}
	case strings.HasPrefix(eventType, "channel_") || strings.HasPrefix(eventType, "group_"):
		var channel struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		}
		if json.Unmarshal(event["channel"], &channel) != nil {
			channel.ID = text("channel")
		}
		return integrationsv1.Resource{Type: "channel", ID: channel.ID, Name: channel.Name}
	case eventType == "team_join" || eventType == "user_change":
		var user struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		}
		_ = json.Unmarshal(event["user"], &user)
		return integrationsv1.Resource{Type: "user", ID: user.ID, Name: user.Name}
	case strings.HasPrefix(eventType, "file_"):
		return integrationsv1.Resource{Type: "file", ID: text("file_id")}
	}
	return integrationsv1.Resource{Type: eventType}
}

// slackActor is the user an event names, whether by ID or as an object.
func slackActor(raw json.RawMessage) *integrationsv1.Actor {
	var id string
	if json.Unmarshal(raw, &id) == nil && id != "" {
		return &integrationsv1.Actor{ID: id}
	}
	var user struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	if json.Unmarshal(raw, &user) == nil && user.ID != "" {
		return &integrationsv1.Actor{ID: user.ID, Name: user.Name}
	}
	return nil
}
