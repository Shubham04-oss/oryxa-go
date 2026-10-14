package application

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"sync"
	"time"

	appsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/apps/v1"

	"github.com/Shubham04-oss/oryxa-go/apps/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/internal/pipedream"
	"github.com/Shubham04-oss/oryxa-go/internal/secrets"
)

// PipedreamWebhookPrefix is where a deployed Pipedream trigger delivers,
// followed by apps' own id for the trigger, under the public base URL.
const PipedreamWebhookPrefix = "/v1/apps/webhooks/pipedream/"

// DefaultPipedreamMinPoll is the shortest interval a polling Pipedream
// trigger is deployed with unless APPS_PIPEDREAM_MIN_POLL_SECONDS says
// otherwise. Every poll costs credits whether it finds anything or not, so a
// trigger polls no more often than this, however often its own default is.
const DefaultPipedreamMinPoll = 15 * time.Minute

// MaxPipedreamDelivery bounds what one delivery's body is read to. A payload
// above appsv1.MaxTriggerDataBytes is published without its data anyway, so
// nothing larger than this needs reading whole.
const MaxPipedreamDelivery = 1 << 20

// ErrDeliveryTooLarge means a delivery's body is over MaxPipedreamDelivery,
// so none of it was used.
var ErrDeliveryTooLarge = errors.New("the delivery is too large")

// pipedreamKeyResource is what a trigger's sealed signing key is bound to,
// with apps' id for the trigger, so the key cannot be moved to another one.
const pipedreamKeyResource = "pipedream_trigger"

func pipedreamKeyBinding(triggerID string) secrets.Binding {
	return secrets.Binding{ResourceType: pipedreamKeyResource, ResourceID: triggerID}
}

// pipedreamTriggerViews lists a Pipedream app's triggers as the catalog
// shows them, a page at a time from an offset cursor. Their config schema is
// what CreateTrigger checks a config against, by the same rules as
// Composio's.
func (s *Service) pipedreamTriggerViews(ctx context.Context, toolkit, cursor string, limit int) ([]TriggerTypeView, string, error) {
	triggers, err := s.pipedreamTriggersFor(ctx, toolkit)
	if err != nil {
		return nil, "", err
	}
	offset := 0
	if cursor != "" {
		if offset, err = strconv.Atoi(cursor); err != nil || offset < 0 {
			return nil, "", fmt.Errorf("the cursor is not one the catalog answered: %w", domain.ErrInvalid)
		}
	}
	offset = min(offset, len(triggers))
	end := min(offset+limit, len(triggers))
	views := make([]TriggerTypeView, 0, end-offset)
	for _, trigger := range triggers[offset:end] {
		views = append(views, TriggerTypeView{
			Trigger: trigger.slug, Name: trigger.name, Description: trigger.description, Toolkit: toolkit,
			Kind: pipedreamKind(trigger), ConfigSchema: trigger.schema,
		})
	}
	next := ""
	if end < len(triggers) {
		next = strconv.Itoa(end)
	}
	return views, next, nil
}

func pipedreamKind(trigger pipedreamAction) string {
	if trigger.poll {
		return domain.TriggerKindPoll
	}
	return domain.TriggerKindWebhook
}

// subscribePipedream deploys one of a Pipedream app's triggers for the
// connection's member, delivering to apps' own webhook for it, and records
// it with its signing key sealed. It is only ever asked for: auto-subscribe
// leaves Pipedream connections alone, since every deployed trigger costs
// credits for as long as it runs.
func (s *Service) subscribePipedream(ctx context.Context, connection domain.Connection, slug string, config json.RawMessage) (domain.Trigger, error) {
	if s.pipedream == nil {
		return domain.Trigger{}, errPipedreamOff(connection.Toolkit)
	}
	if s.sealer == nil {
		return domain.Trigger{}, fmt.Errorf("events from apps connected through Pipedream are not available here yet: %w", domain.ErrNotConnectable)
	}
	if connection.PipedreamAccountID == "" {
		return domain.Trigger{}, fmt.Errorf("the %s connection has no account yet: %w", connection.Toolkit, domain.ErrNotConnected)
	}
	triggers, err := s.pipedreamTriggersFor(ctx, connection.Toolkit)
	if err != nil {
		return domain.Trigger{}, err
	}
	var definition pipedreamAction
	for _, trigger := range triggers {
		if trigger.slug == slug {
			definition = trigger
		}
	}
	if definition.slug == "" {
		return domain.Trigger{}, fmt.Errorf("%s delivers no event called %s: %w", connection.Toolkit, slug, domain.ErrNotFound)
	}
	if err := domain.CheckTriggerConfig(definition.schema, config); err != nil {
		return domain.Trigger{}, err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(config, &fields); err != nil {
		return domain.Trigger{}, fmt.Errorf("a trigger's config is a JSON object: %w", domain.ErrInvalid)
	}
	props := make(map[string]any, len(fields)+1)
	for name, value := range fields {
		// The app prop is refused like any unknown one: a trigger always
		// watches the member's own account, never one a config names.
		if !definition.props[name] {
			return domain.Trigger{}, fmt.Errorf("%s takes no config %q: %w", slug, name, domain.ErrInvalid)
		}
		props[name] = value
	}
	if definition.appProp != "" {
		props[definition.appProp] = map[string]any{"authProvisionId": connection.PipedreamAccountID}
	}

	// Asking again answers the subscription that exists rather than
	// deploying, and paying for, a second one.
	key := OwnKey(connection.ID, slug, config)
	live, err := s.store.Triggers(ctx, connection.OrganizationID, TriggerQuery{ConnectionID: connection.ID})
	if err != nil {
		return domain.Trigger{}, err
	}
	for _, trigger := range live {
		if trigger.PipedreamTriggerID != "" && OwnKey(trigger.ConnectionID, trigger.Slug, trigger.Config) == key {
			return trigger, nil
		}
	}

	id, err := newID("apt")
	if err != nil {
		return domain.Trigger{}, err
	}
	var timer time.Duration
	if definition.poll {
		timer = max(time.Duration(definition.pollSeconds)*time.Second, s.pipedreamMinPoll)
	}
	userID := pipedreamUserID(connection)
	deployed, err := s.pipedream.DeployTrigger(ctx, PipedreamDeployRequest{
		ExternalUserID: userID, ComponentKey: definition.key, ConfiguredProps: props,
		WebhookURL: s.publicBaseURL + PipedreamWebhookPrefix + id, Timer: timer,
	})
	if errors.Is(err, domain.ErrInvalid) {
		return domain.Trigger{}, fmt.Errorf("Pipedream refused the trigger's config: %w", err)
	}
	if err != nil {
		return domain.Trigger{}, pipedreamError("subscribe to "+slug, err)
	}
	// From here a failure leaves a deployed trigger nothing records, which
	// would cost credits for ever, so it is taken down again.
	undeploy := func() {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		if err := s.pipedream.DeleteTrigger(cleanup, userID, deployed.ID); err != nil && !errors.Is(err, domain.ErrNotFound) {
			s.logger.ErrorContext(ctx, "take down a Pipedream trigger nothing records", "pipedream_trigger_id", deployed.ID, "error", err)
		}
	}
	if len(deployed.SigningKey) == 0 {
		undeploy()
		return domain.Trigger{}, fmt.Errorf("Pipedream deployed the trigger without a signing key: %w", domain.ErrUnavailable)
	}
	sealed, err := s.sealer.Seal(ctx, pipedreamKeyBinding(id), deployed.SigningKey)
	secrets.Wipe(deployed.SigningKey)
	if err != nil {
		undeploy()
		return domain.Trigger{}, fmt.Errorf("seal the trigger's signing key: %w: %v", domain.ErrUnavailable, err)
	}
	sealedJSON, err := json.Marshal(sealed)
	if err != nil {
		undeploy()
		return domain.Trigger{}, fmt.Errorf("encode the trigger's signing key: %w", err)
	}
	now := s.now().UTC()
	kind := pipedreamKind(definition)
	event, err := s.event(appsv1.TriggerSubscribedDetailType, appsv1.TriggerSubscribed{
		TriggerID: id, ConnectionID: connection.ID, OrganizationID: connection.OrganizationID,
		MemberID: connection.MemberID, Toolkit: connection.Toolkit, Trigger: slug, Kind: kind, SubscribedAt: now,
	})
	if err != nil {
		undeploy()
		return domain.Trigger{}, err
	}
	trigger, created, err := s.store.CreatePipedreamTrigger(ctx, domain.Trigger{
		ID: id, OrganizationID: connection.OrganizationID, MemberID: connection.MemberID,
		ConnectionID: connection.ID, Toolkit: connection.Toolkit, Slug: slug, Config: config,
		PipedreamTriggerID: deployed.ID, Status: domain.TriggerActive, Kind: kind, CreatedAt: now, UpdatedAt: now,
	}, sealedJSON, event)
	if err != nil || !created {
		// A concurrent request recorded the same subscription first, or the
		// connection went: either way this deployment is no one's.
		undeploy()
		return trigger, err
	}
	s.logger.InfoContext(ctx, "trigger subscribed", "trigger_id", trigger.ID, "organization_id", connection.OrganizationID,
		"trigger", slug, "kind", kind, "broker", domain.BrokerPipedream)
	return trigger, nil
}

// unsubscribePipedream deletes a trigger's deployment at Pipedream. One
// Pipedream no longer has is already what was asked for.
func (s *Service) unsubscribePipedream(ctx context.Context, trigger domain.Trigger) error {
	if s.pipedream == nil {
		return errPipedreamOff(trigger.Toolkit)
	}
	connection, err := s.store.Connection(ctx, trigger.OrganizationID, trigger.ConnectionID)
	if err != nil {
		return err
	}
	if err := s.pipedream.DeleteTrigger(ctx, pipedreamUserID(connection), trigger.PipedreamTriggerID); err != nil && !errors.Is(err, domain.ErrNotFound) {
		return fmt.Errorf("remove the trigger at Pipedream: %w", err)
	}
	return nil
}

// ReceivePipedreamWebhook acts on one delivery from a deployed Pipedream
// trigger, once. The trigger is looked up before the body is read, so a
// delivery to an id apps never issued costs no more than that lookup; and
// nothing in the body is used until its signature is checked with the
// trigger's own key.
//
// A trigger that is removed or disabled is answered as not found rather than
// acknowledged: Pipedream should stop delivering to it, and one apps has
// removed has been, or is being, deleted there.
func (s *Service) ReceivePipedreamWebhook(ctx context.Context, triggerID string, header http.Header, body io.Reader) error {
	trigger, sealed, err := s.store.PipedreamTrigger(ctx, triggerID)
	if err != nil {
		return err
	}
	if trigger.Status != domain.TriggerActive {
		return fmt.Errorf("the trigger is %s: %w", trigger.Status, domain.ErrNotFound)
	}
	raw, err := io.ReadAll(io.LimitReader(body, MaxPipedreamDelivery+1))
	if err != nil {
		return fmt.Errorf("read the delivery: %w", domain.ErrInvalid)
	}
	if len(raw) > MaxPipedreamDelivery {
		return ErrDeliveryTooLarge
	}
	key, err := s.pipedreamSigningKey(ctx, trigger.ID, sealed)
	if err != nil {
		return err
	}
	if err := pipedream.VerifyWebhook(string(key), header, raw, s.now()); err != nil {
		return fmt.Errorf("%w: %w", ErrUnverified, err)
	}
	var delivery struct {
		ID json.RawMessage `json:"id"`
	}
	if err := json.Unmarshal(raw, &delivery); err != nil {
		return fmt.Errorf("a delivery is a JSON object: %w", domain.ErrInvalid)
	}
	if err := s.allowToolkit(ctx, trigger.OrganizationID, trigger.Toolkit); err != nil {
		if errors.Is(err, domain.ErrForbidden) {
			s.logger.InfoContext(ctx, "pipedream delivery dropped", "trigger_id", trigger.ID, "reason", "app no longer offered")
			return nil
		}
		return err
	}

	// A redelivery carries the same id, when the event has one, or else is
	// the same bytes. Either is hashed under the trigger, so one trigger's
	// ids never collide with another's or with Composio's.
	identity := raw
	if len(delivery.ID) > 0 && string(delivery.ID) != "null" {
		identity = delivery.ID
	}
	sum := sha256.Sum256(identity)
	deliveryID := "pd_" + trigger.ID + "_" + hex.EncodeToString(sum[:16])

	now := s.now().UTC()
	received := appsv1.TriggerReceived{
		TriggerID: trigger.ID, ConnectionID: trigger.ConnectionID, OrganizationID: trigger.OrganizationID,
		MemberID: trigger.MemberID, Toolkit: trigger.Toolkit, Trigger: trigger.Slug, ReceivedAt: now,
	}
	if len(raw) > appsv1.MaxTriggerDataBytes {
		received.DataOmitted = true
	} else {
		received.Data = raw
	}
	event, err := s.event(appsv1.TriggerReceivedDetailType, received)
	if err != nil {
		return err
	}
	recorded, err := s.store.RecordTriggerDelivery(ctx, deliveryID, "pipedream.trigger", trigger.ID, now, event)
	if err != nil {
		return err
	}
	if !recorded {
		s.logger.InfoContext(ctx, "pipedream delivery already handled", "trigger_id", trigger.ID)
		return nil
	}
	s.logger.InfoContext(ctx, "app delivered an event", "trigger_id", trigger.ID, "toolkit", trigger.Toolkit,
		"trigger", trigger.Slug, "broker", domain.BrokerPipedream, "data_omitted", received.DataOmitted)
	return nil
}

// pipedreamSigningKey opens a trigger's sealed signing key, or answers the
// one opened a moment ago.
func (s *Service) pipedreamSigningKey(ctx context.Context, triggerID string, sealedJSON []byte) ([]byte, error) {
	now := s.now()
	if key, ok := s.pipedreamKeys.get(triggerID, now); ok {
		return key, nil
	}
	if s.sealer == nil {
		return nil, fmt.Errorf("no sealer to open the trigger's signing key: %w", domain.ErrUnavailable)
	}
	var sealed secrets.SealedData
	if err := json.Unmarshal(sealedJSON, &sealed); err != nil {
		return nil, fmt.Errorf("read the trigger's signing key: %w", err)
	}
	key, err := s.sealer.Open(ctx, pipedreamKeyBinding(triggerID), sealed)
	if err != nil {
		return nil, fmt.Errorf("open the trigger's signing key: %w: %v", domain.ErrUnavailable, err)
	}
	s.pipedreamKeys.put(triggerID, key, now)
	return key, nil
}

// How long, and for how many triggers, opened signing keys are kept. Opening
// one is a KMS call, and a trigger's key never changes while it lives.
const (
	openedKeyLifetime = 5 * time.Minute
	maxOpenedKeys     = 10_000
)

type openedKey struct {
	key   []byte
	until time.Time
}

// openedKeys keeps triggers' opened signing keys for a few minutes.
type openedKeys struct {
	mu      sync.Mutex
	entries map[string]openedKey
}

func (cache *openedKeys) get(triggerID string, now time.Time) ([]byte, bool) {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	entry, ok := cache.entries[triggerID]
	if !ok || !now.Before(entry.until) {
		return nil, false
	}
	return entry.key, true
}

func (cache *openedKeys) put(triggerID string, key []byte, now time.Time) {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	if cache.entries == nil {
		cache.entries = map[string]openedKey{}
	}
	if len(cache.entries) >= maxOpenedKeys {
		for id, entry := range cache.entries {
			if !now.Before(entry.until) {
				delete(cache.entries, id)
			}
		}
		if len(cache.entries) >= maxOpenedKeys {
			return
		}
	}
	cache.entries[triggerID] = openedKey{key: key, until: now.Add(openedKeyLifetime)}
}
