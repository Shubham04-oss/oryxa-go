package hooks

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/Shubham04-oss/oryxa-go/apps/internal/integration"
)

// A surface is a first-party face of Oryxa that an app delivers to, and that
// is no integration: Oryxa's own Slack bot, whose people talk to it by
// mentioning it and clicking its buttons. It shares everything the ingress
// does for an integration's deliveries (the endpoint, the rate limit, the
// signature, the timestamp, the handshake, the dedupe key) and differs in
// what happens after: its verified deliveries go to the surface, never to
// Pusher.Parse, and nothing about it is in the registry, so the shipped
// manifests, Oryxa's sign-in and their tests never see it.
//
// It has a deployment endpoint like any deployment app's, made the first time
// it is asked for, and it is served at static URLs of its own, through Alias,
// because those are typed into the app's settings once and must not change.
// Its secret is configured, as a deployment endpoint's is.

// Surface takes the verified deliveries of one toolkit that is no
// integration.
type Surface interface {
	// Webhook is how the surface's deliveries are verified: the same scheme
	// vocabulary a manifest's webhook section uses, so the one verification
	// in this package serves it.
	Webhook() integration.Webhook
	// Deliver takes a delivery that passed verification and was not seen
	// before. kind is the alias it arrived on, and key its dedupe key. The
	// header is the delivery's without its Authorization and Cookie.
	//
	// Deliver records the key, in the transaction that records whatever the
	// delivery became, so two copies of it racing write one record between
	// them; the second is not an error. It must not call anything beyond its
	// own store, but for what the app allows only while a delivery is fresh,
	// such as Slack opening a modal for a click: the app is waiting for the
	// answer, and the ingress has given it a deadline, which bounds that too.
	// An error is answered 5xx, so the app delivers again; a delivery that is
	// safe to drop is nil.
	Deliver(ctx context.Context, kind, key string, header http.Header, body []byte, receivedAt time.Time) error
}

// AnsweringSurface is a surface whose deliveries can need an answer in the
// response's body, as the submission of a Slack modal does, to say in the modal
// what is wrong with what was typed. The ingress asks it rather than Deliver.
type AnsweringSurface interface {
	Surface
	// DeliverAndAnswer is Deliver, and the JSON the 200 that answers the
	// delivery carries: nil for none, as Deliver's success is answered.
	DeliverAndAnswer(ctx context.Context, kind, key string, header http.Header, body []byte, receivedAt time.Time) (json.RawMessage, error)
}

// Surfaces finds a surface by its toolkit.
type Surfaces interface {
	Surface(toolkit string) (Surface, bool)
}

// DefaultSurfaceDeadline is how long a surface's delivery may use the
// database before the ingress gives up on it and answers 5xx. Slack waits
// three seconds for its answer.
const DefaultSurfaceDeadline = 2 * time.Second

// alias is a static URL that serves the ingress for one surface's deployment
// endpoint, whichever id that endpoint was given.
type alias struct{ toolkit, kind string }

// Alias is the ingress for a surface's static URL: what a deployment
// endpoint's own URL would be if its id were not chosen at random. It is for
// apps to mount at the surface's paths, one per kind of delivery the app
// sends. The endpoint is made the first time a request reaches it. A toolkit
// that is no surface is answered as an unknown endpoint is.
func (s *Service) Alias(toolkit, kind string) http.Handler {
	if _, ok := s.surface(toolkit); !ok {
		return ingress{service: s, alias: &alias{}}
	}
	return ingress{service: s, alias: &alias{toolkit: toolkit, kind: kind}}
}

func (s *Service) surface(toolkit string) (Surface, bool) {
	if s.surfaces == nil {
		return nil, false
	}
	return s.surfaces.Surface(toolkit)
}

// copyScheme is a webhook section copied, so an endpoint's frozen scheme
// shares nothing with the registry or the surface it came from.
func copyScheme(toolkit string, webhook integration.Webhook) (integration.Webhook, error) {
	encoded, err := json.Marshal(webhook)
	if err != nil {
		return integration.Webhook{}, fmt.Errorf("copy %s's webhook: %w", toolkit, err)
	}
	var scheme integration.Webhook
	if err := json.Unmarshal(encoded, &scheme); err != nil {
		return integration.Webhook{}, fmt.Errorf("copy %s's webhook: %w", toolkit, err)
	}
	return scheme, nil
}
