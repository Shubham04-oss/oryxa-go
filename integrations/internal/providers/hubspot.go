package providers

// HubSpot delivers every subscription's events to one target URL set on the
// app, and distinguishes the accounts they belong to by the portal a delivery
// names. Its subscriptions are created through the app's own API rather than
// per subscription, which is why it is an app delivery and registers nothing
// here: connections already stores the app's client secret, and that secret —
// not a per-subscription one — is what signs every delivery.

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	integrationsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/integrations/v1"

	"github.com/Shubham04-oss/oryxa-go/integrations/internal/domain"
)

// hubspotTolerance is how far a delivery's timestamp may be from now. HubSpot
// documents a five-minute window, and it is the only replay protection the v3
// signature has.
const hubspotTolerance = 5 * time.Minute

// hubspotAppIDPattern is a HubSpot app ID, which names the app a subscription
// belongs to in the target.
var hubspotAppIDPattern = regexp.MustCompile(`^[0-9]{1,20}$`)

// hubspotEventTypes are the subscription types a HubSpot app can ask for.
var hubspotEventTypes = []string{
	"contact.creation", "contact.deletion", "contact.merge", "contact.privacyDeletion", "contact.propertyChange",
	"company.creation", "company.deletion", "company.merge", "company.propertyChange",
	"deal.creation", "deal.deletion", "deal.merge", "deal.propertyChange",
	"ticket.creation", "ticket.deletion", "ticket.propertyChange",
	"product.creation", "product.deletion", "product.propertyChange",
	"lineItem.creation", "lineItem.deletion", "lineItem.propertyChange",
	"conversation.creation", "conversation.deletion", "conversation.newMessage", "conversation.privacyDeletion", "conversation.propertyChange",
}

// hubspotEventPattern is what an event a subscription asks for looks like:
// object.event, such as contact.creation.
var hubspotEventPattern = regexp.MustCompile(`^[a-zA-Z]{1,32}\.[a-zA-Z]{1,32}$`)

// HubSpot delivers an app's events, signed with the app's client secret.
func HubSpot() Provider {
	return Provider{
		Name:     "hubspot",
		Delivery: DeliveryApp,
		Target:   hubspotTarget,
		// A HubSpot app's webhook target is one URL for the whole app, so a
		// subscription names only the app whose events it wants.
		AccountKey:   hubspotAccountKey,
		Receive:      hubspotReceive,
		TargetFields: []TargetField{},
		Events: EventsDescription{
			Names:   hubspotEventTypes,
			Open:    true,
			Default: "every event the HubSpot app subscribes to",
		},
		Actions: hubspotActions(),
		Skill:   skill("hubspot"),
	}
}

// hubspotTarget takes no target: a HubSpot app delivers its accounts' events
// to one URL, and portalId in each delivery says which account it belongs to.
func hubspotTarget(target map[string]string, events []string) (map[string]string, []string, error) {
	if len(target) > 0 {
		return nil, nil, fmt.Errorf("a HubSpot subscription takes no target: %w", domain.ErrInvalid)
	}
	for _, event := range events {
		if !hubspotEventPattern.MatchString(event) {
			return nil, nil, fmt.Errorf("%q is not a HubSpot event type, such as contact.creation: %w", event, domain.ErrInvalid)
		}
	}
	return map[string]string{}, events, nil
}

// hubspotAccountKey is the portal a connection's account belongs to.
// Connections names a HubSpot connection by its portal, so the provider
// account's ID is the account key as it stands.
func hubspotAccountKey(account domain.ProviderAccount) (string, error) {
	key := strings.TrimSpace(account.ID)
	if key == "" {
		return "", fmt.Errorf("HubSpot account names no portal: %w", domain.ErrInvalid)
	}
	return key, nil
}

// hubspotDelivery is one event in a delivery. HubSpot posts an array of them,
// up to a hundred at a time.
type hubspotDelivery struct {
	PortalID       json.Number `json:"portalId"`
	ObjectID       json.Number `json:"objectId"`
	EventID        json.Number `json:"eventId"`
	OccurredAt     json.Number `json:"occurredAt"`
	EventType      string      `json:"eventType"`
	SubscriptionID json.Number `json:"subscriptionId"`
	AttemptNumber  json.Number `json:"attemptNumber"`
	PropertyName   string      `json:"propertyName"`
	PropertyValue  string      `json:"propertyValue"`
	ChangeSource   string      `json:"changeSource"`
}

// hubspotReceive verifies a delivery's v3 signature and reads it. One batch
// can carry events from several portals, so the account keys are every portal
// it names and each event keeps its own: an event reaches only the
// subscriptions of connections to the portal it happened in.
func hubspotReceive(delivery Delivered, secret []byte, now time.Time) (Reception, error) {
	if err := hubspotVerify(delivery, secret, now); err != nil {
		return Reception{}, err
	}
	var delivered []hubspotDelivery
	if err := json.Unmarshal(delivery.Body, &delivered); err != nil {
		return Reception{}, fmt.Errorf("%w: HubSpot payload is not a JSON array", domain.ErrInvalid)
	}
	var portals []string
	seen := map[string]bool{}
	events := make([]Event, 0, len(delivered))
	for _, item := range delivered {
		eventType := strings.TrimSpace(item.EventType)
		if !hubspotEventPattern.MatchString(eventType) {
			return Reception{}, fmt.Errorf("%w: HubSpot event has no type", domain.ErrInvalid)
		}
		portal := item.PortalID.String()
		if portal == "" {
			return Reception{}, fmt.Errorf("%w: HubSpot event names no portal", domain.ErrInvalid)
		}
		if !seen[portal] {
			seen[portal] = true
			portals = append(portals, portal)
		}

		// HubSpot says eventId is not guaranteed unique, and a retry repeats
		// it, so the attempt number is what tells a redelivery from a new
		// event. Without one, the payload's own digest has to do.
		key := strings.Join([]string{portal, item.EventID.String(), item.AttemptNumber.String()}, ":")
		if item.EventID.String() == "" {
			key = Digest([]byte(eventType), []byte(portal), delivery.Body)
		}
		var occurredAt time.Time
		if milliseconds, err := item.OccurredAt.Int64(); err == nil && milliseconds > 0 {
			occurredAt = time.UnixMilli(milliseconds).UTC()
		}
		event := Event{
			Name:       eventType,
			Type:       eventType,
			DeliveryID: item.EventID.String(),
			Key:        key,
			AccountKey: portal,
			Resource:   hubspotResource(eventType, item),
			OccurredAt: occurredAt,
			Payload:    hubspotEventPayload(item, delivery.Body),
		}
		events = append(events, event)
	}
	return Reception{AccountKeys: portals, Events: events}, nil
}

// hubspotResource is what a delivery is about: the object it names.
func hubspotResource(eventType string, item hubspotDelivery) integrationsv1.Resource {
	name := item.PropertyValue
	if name == "" {
		name = item.PropertyName
	}
	return integrationsv1.Resource{Type: objectOf(eventType), ID: item.ObjectID.String(), Name: name}
}

// objectOf is the object an event type is about: contact for contact.creation.
func objectOf(eventType string) string {
	object, _, found := strings.Cut(eventType, ".")
	if !found {
		return eventType
	}
	return object
}

// hubspotEventPayload is what a caller reads about an event: the event as the
// provider wrote it, so nothing is lost that an agent might need.
func hubspotEventPayload(item hubspotDelivery, body []byte) json.RawMessage {
	encoded, err := json.Marshal(item)
	if err != nil {
		return body
	}
	return encoded
}

// hubspotVerify checks the v3 signature. HubSpot signs the method, the URI it
// called, the raw body and the timestamp with the app's client secret, and
// sends every signature version for compatibility, so the version it names is
// the one that decides: an older scheme has no replay protection and is
// refused rather than quietly accepted.
func hubspotVerify(delivery Delivered, secret []byte, now time.Time) error {
	version := strings.ToLower(strings.TrimSpace(delivery.Header.Get("X-HubSpot-Signature-Version")))
	if version != "" && version != "v3" {
		return fmt.Errorf("%w: HubSpot %s signature", ErrUnverified, version)
	}
	signature := strings.TrimSpace(delivery.Header.Get("X-HubSpot-Signature-V3"))
	if signature == "" {
		return fmt.Errorf("%w: HubSpot signature", ErrUnverified)
	}
	timestamp := strings.TrimSpace(delivery.Header.Get("X-HubSpot-Request-Timestamp"))
	milliseconds, err := strconv.ParseInt(timestamp, 10, 64)
	if err != nil {
		return fmt.Errorf("%w: HubSpot timestamp", ErrUnverified)
	}
	signedAt := time.UnixMilli(milliseconds)
	// A timestamp far in the future is as suspect as an old one.
	if age := now.Sub(signedAt); age > hubspotTolerance || age < -hubspotTolerance {
		return fmt.Errorf("%w: HubSpot request is too old", ErrUnverified)
	}
	if delivery.URL == "" {
		return fmt.Errorf("%w: HubSpot delivery has no URL to verify against", ErrUnverified)
	}
	message := delivery.Method + hubspotSignedURI(delivery) + string(delivery.Body) + timestamp
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(message))
	expected := base64.StdEncoding.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(expected), []byte(signature)) {
		return fmt.Errorf("%w: HubSpot signature", ErrUnverified)
	}
	return nil
}

// hubspotSignedURI is the URI HubSpot signed: the whole URL it called, the
// webhook URL integrations registered. The query is decoded as HubSpot's own
// samples decode it — only the query, and only these sequences — so an encoded
// slash in the path stays encoded, which is the part implementations get
// wrong.
func hubspotSignedURI(delivery Delivered) string {
	return hubspotDecodedQuery(delivery.URL)
}

// hubspotDecodedQuery decodes the sequences HubSpot's own examples decode, in
// the query only. The `?` that opens the query is left as it is.
func hubspotDecodedQuery(uri string) string {
	path, query, found := strings.Cut(uri, "?")
	if !found {
		return uri
	}
	return path + "?" + hubspotQueryDecoder.Replace(query)
}

var hubspotQueryDecoder = strings.NewReplacer(
	"%3A", ":", "%3a", ":",
	"%2F", "/", "%2f", "/",
	"%3F", "?", "%3f", "?",
	"%40", "@",
	"%21", "!",
	"%24", "$",
	"%27", "'",
	"%28", "(",
	"%29", ")",
	"%2A", "*", "%2a", "*",
	"%2C", ",", "%2c", ",",
	"%3B", ";", "%3b", ";",
)
