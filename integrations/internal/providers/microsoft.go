package providers

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode"

	integrationsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/integrations/v1"

	"github.com/Shubham04-oss/oryxa-go/integrations/internal/domain"
)

// microsoftResource is a Graph resource a subscription may watch, the change
// types it may ask for, and how long Graph lets its subscription last.
type microsoftResource struct {
	pattern  *regexp.Regexp
	changes  []string
	lifetime time.Duration
}

// microsoftResources are what a Graph subscription may watch: the member's
// mail, one of their mail folders, their calendar, and their OneDrive.
var microsoftResources = []microsoftResource{
	{regexp.MustCompile(`^me/messages$`), []string{"created", "updated", "deleted"}, 10080 * time.Minute},
	{regexp.MustCompile(`^me/mailFolders\('[A-Za-z0-9=_.-]{1,200}'\)/messages$`), []string{"created", "updated", "deleted"}, 10080 * time.Minute},
	{regexp.MustCompile(`^me/events$`), []string{"created", "updated", "deleted"}, 10080 * time.Minute},
	{regexp.MustCompile(`^me/drive/root$`), []string{"updated"}, 42300 * time.Minute},
}

// microsoftMargin is how much shorter than Graph's longest lifetime a
// subscription is asked for, so clocks that differ a little still agree.
const microsoftMargin = time.Hour

// Microsoft delivers changes to a member's mail, calendar or OneDrive to a
// Graph subscription integrations creates, marked with the subscription's
// secret as its clientState. Graph subscriptions expire within days, so
// integrations renews them.
func Microsoft() Provider {
	return microsoftProvider("https://graph.microsoft.com/v1.0")
}

func microsoftProvider(apiURL string) Provider {
	return Provider{
		Name:     "microsoft",
		Delivery: DeliveryWebhook,
		Target:   microsoftTarget,
		Register: func(ctx context.Context, client *http.Client, request RegisterRequest) (Registration, error) {
			return microsoftRegister(ctx, client, apiURL, request)
		},
		Renew: func(ctx context.Context, client *http.Client, request RenewRequest) (Registration, error) {
			return microsoftRenew(ctx, client, apiURL, request)
		},
		Unregister: func(ctx context.Context, client *http.Client, request UnregisterRequest) error {
			return microsoftUnregister(ctx, client, apiURL, request)
		},
		Receive: microsoftReceive,
		TargetFields: []TargetField{
			{Name: "resource", Description: "What to watch: me/messages, me/mailFolders('<id>')/messages, me/events or me/drive/root.", Required: true, Example: "me/messages"},
		},
		Events: EventsDescription{
			Names:   []string{"created", "updated", "deleted"},
			Default: "every change the resource reports",
		},
		Actions: microsoftActions(apiURL),
		Skill:   skill("microsoft"),
	}
}

// microsoftTarget takes a Graph resource, such as me/messages, and change
// types; no events asks for every change the resource reports.
func microsoftTarget(target map[string]string, events []string) (map[string]string, []string, error) {
	resource, ok := microsoftResourceFor(target["resource"])
	if len(target) != 1 || !ok {
		return nil, nil, fmt.Errorf("a Microsoft subscription's target is {\"resource\": \"me/messages\"}, me/mailFolders('Inbox')/messages, me/events or me/drive/root: %w", domain.ErrInvalid)
	}
	for _, event := range events {
		if !slices.Contains(resource.changes, event) {
			return nil, nil, fmt.Errorf("%s reports %s changes, not %q: %w", target["resource"], strings.Join(resource.changes, ", "), event, domain.ErrInvalid)
		}
	}
	return map[string]string{"resource": target["resource"]}, events, nil
}

func microsoftResourceFor(resource string) (microsoftResource, bool) {
	for _, candidate := range microsoftResources {
		if candidate.pattern.MatchString(resource) {
			return candidate, true
		}
	}
	return microsoftResource{}, false
}

type microsoftSubscription struct {
	ID                 string    `json:"id"`
	ExpirationDateTime time.Time `json:"expirationDateTime"`
}

// microsoftExpiry is when to ask a subscription to expire: as late as Graph
// allows for its resource, less a margin.
func microsoftExpiry(target map[string]string, now time.Time) (time.Time, error) {
	resource, ok := microsoftResourceFor(target["resource"])
	if !ok {
		return time.Time{}, fmt.Errorf("%q is not a Graph resource integrations watches: %w", target["resource"], domain.ErrInvalid)
	}
	return now.UTC().Add(resource.lifetime - microsoftMargin).Truncate(time.Second), nil
}

func microsoftRegister(ctx context.Context, client *http.Client, apiURL string, request RegisterRequest) (Registration, error) {
	expiry, err := microsoftExpiry(request.Target, request.Now)
	if err != nil {
		return Registration{}, err
	}
	changes := request.Events
	if len(changes) == 0 {
		resource, _ := microsoftResourceFor(request.Target["resource"])
		changes = resource.changes
	}
	var created microsoftSubscription
	if err := sendJSON(ctx, client, http.MethodPost, apiURL+"/subscriptions", request.Credential, map[string]any{
		"changeType":                strings.Join(changes, ","),
		"notificationUrl":           request.CallbackURL,
		"lifecycleNotificationUrl":  request.CallbackURL,
		"resource":                  request.Target["resource"],
		"expirationDateTime":        expiry.Format(time.RFC3339),
		"clientState":               request.Secret,
		"latestSupportedTlsVersion": "v1_2",
	}, &created); err != nil {
		return Registration{}, err
	}
	if created.ID == "" {
		return Registration{}, fmt.Errorf("%w: Graph answered without a subscription", domain.ErrProviderUnavailable)
	}
	return Registration{ID: created.ID, ExpiresAt: firstTime(created.ExpirationDateTime, expiry)}, nil
}

func microsoftRenew(ctx context.Context, client *http.Client, apiURL string, request RenewRequest) (Registration, error) {
	expiry, err := microsoftExpiry(request.Target, request.Now)
	if err != nil {
		return Registration{}, err
	}
	var renewed microsoftSubscription
	if err := sendJSON(ctx, client, http.MethodPatch, apiURL+"/subscriptions/"+url.PathEscape(request.RegistrationID), request.Credential,
		map[string]any{"expirationDateTime": expiry.Format(time.RFC3339)}, &renewed); err != nil {
		return Registration{}, err
	}
	return Registration{ID: request.RegistrationID, ExpiresAt: firstTime(renewed.ExpirationDateTime, expiry)}, nil
}

func microsoftUnregister(ctx context.Context, client *http.Client, apiURL string, request UnregisterRequest) error {
	err := sendJSON(ctx, client, http.MethodDelete, apiURL+"/subscriptions/"+url.PathEscape(request.RegistrationID), request.Credential, nil, nil)
	if errors.Is(err, domain.ErrNotFound) {
		return nil
	}
	return err
}

type microsoftNotification struct {
	SubscriptionID string `json:"subscriptionId"`
	ChangeType     string `json:"changeType"`
	Resource       string `json:"resource"`
	ClientState    string `json:"clientState"`
	LifecycleEvent string `json:"lifecycleEvent"`
	ResourceData   struct {
		Type string `json:"@odata.type"`
		ETag string `json:"@odata.etag"`
		ID   string `json:"id"`
	} `json:"resourceData"`
}

// microsoftReceive answers Graph's validation request by echoing its token,
// and otherwise reads a batch of notifications, every one of which must carry
// the subscription's secret as its clientState. Graph expects 202 Accepted.
func microsoftReceive(delivery Delivered, secret []byte, _ time.Time) (Reception, error) {
	if token := delivery.Query.Get("validationToken"); token != "" {
		return Reception{Reply: &Reply{Status: http.StatusOK, ContentType: "text/plain", Body: []byte(token)}}, nil
	}
	var batch struct {
		Value []json.RawMessage `json:"value"`
	}
	if err := json.Unmarshal(delivery.Body, &batch); err != nil || len(batch.Value) == 0 {
		return Reception{}, fmt.Errorf("%w: Graph notifications are not valid", domain.ErrInvalid)
	}
	reception := Reception{Reply: &Reply{Status: http.StatusAccepted}}
	for _, raw := range batch.Value {
		var notification microsoftNotification
		if err := json.Unmarshal(raw, &notification); err != nil {
			return Reception{}, fmt.Errorf("%w: Graph notification is not valid", domain.ErrInvalid)
		}
		if subtle.ConstantTimeCompare([]byte(notification.ClientState), secret) != 1 {
			return Reception{}, fmt.Errorf("%w: Graph clientState", ErrUnverified)
		}
		switch notification.LifecycleEvent {
		case "":
		case "subscriptionRemoved":
			reception.Lifecycle = LifecycleRemoved
			continue
		case "reauthorizationRequired":
			if reception.Lifecycle != LifecycleRemoved {
				reception.Lifecycle = LifecycleRenew
			}
			continue
		default:
			// missed means some notifications were dropped; there is nothing to
			// redeliver.
			continue
		}
		kind := microsoftKind(notification.ResourceData.Type)
		reception.Events = append(reception.Events, Event{
			Name:           kind + "." + notification.ChangeType,
			Key:            Digest([]byte(notification.SubscriptionID), []byte(notification.ChangeType), []byte(notification.Resource), []byte(notification.ResourceData.ETag)),
			RegistrationID: notification.SubscriptionID,
			Resource:       integrationsv1.Resource{Type: kind, ID: notification.ResourceData.ID},
			Payload:        withoutFields(raw, "clientState"),
		})
	}
	return reception, nil
}

// microsoftKind names a notification's resource by its OData type:
// #Microsoft.Graph.Message is message, and #microsoft.graph.driveItem is
// drive_item.
func microsoftKind(odataType string) string {
	name := odataType[strings.LastIndex(odataType, ".")+1:]
	if name == "" {
		return "resource"
	}
	runes := []rune(name)
	runes[0] = unicode.ToUpper(runes[0])
	return snakeCase(string(runes))
}

func firstTime(values ...time.Time) time.Time {
	for _, value := range values {
		if !value.IsZero() {
			return value.UTC()
		}
	}
	return time.Time{}
}
