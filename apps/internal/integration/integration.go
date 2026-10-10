// Package integration is the contract between apps' core and each of
// Oryxa's own integrations: what an integration declares about its app (its
// Manifest), what it implements (Integration, and optionally Reader, Poller
// and Pusher), and the values that pass between the two.
//
// The core owns everything that touches a credential or a signature. It signs
// members in, keeps and refreshes their tokens, places them on each request,
// and verifies every webhook delivery before an integration sees it. An
// integration only describes its app and calls it through the Session it is
// handed, so none of that is written once per app, and none of it can be
// right in one integration and wrong in another.
//
// The shapes here are pinned by docs/design/integrations-plan.md. Changes to
// them are additive only.
package integration

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"
)

// Integration is one app, built on the app's own SDK. The registry files it
// under its manifest's toolkit.
type Integration interface {
	// Manifest describes the app: how a member signs in, how its webhooks
	// are verified, and the tools, readers and triggers it offers.
	Manifest() Manifest
	// Tools answers the function behind each of the manifest's tools, by
	// ToolSpec.Name.
	Tools() map[string]ToolFunc
}

// ToolFunc runs one tool as one connection. args has already been checked
// against the tool's input_schema.
//
// For a write, ctx carries IdempotencyKey(ctx), for providers that accept
// one.
type ToolFunc func(ctx context.Context, s Session, args json.RawMessage) (json.RawMessage, error)

// Reader is an integration knowledge can read documents through. It is
// optional.
type Reader interface {
	// Targets lists what the connection could index.
	Targets(ctx context.Context, s Session) ([]Target, error)
	// Read answers one page of one target.
	Read(ctx context.Context, s Session, r PageRequest) (Page, error)
}

// Target is one thing inside a connection that can be indexed: a repository,
// a channel, a database. It is apps' existing targets shape, so a reader's
// answer is served as it is.
type Target struct {
	// Kind is what the app calls this sort of thing: repository, channel.
	Kind string `json:"kind"`
	// ID is what the app knows it by.
	ID string `json:"id"`
	// Name is what a member reads, which is not always the id.
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	// Target is the payload knowledge registers a source with.
	Target json.RawMessage `json:"target"`
}

// Poller is an integration whose triggers are polled. It is optional.
//
// The first Poll with no cursor answers no events and a cursor: a new
// subscription starts from now, rather than delivering the app's history.
type Poller interface {
	Poll(ctx context.Context, s Session, trigger string, config json.RawMessage, cursor string) (Batch, error)
}

// Pusher is an integration whose triggers the app pushes. It is optional.
//
// Parse only ever sees deliveries the core has verified, so it never checks a
// signature itself, and it only reads: it makes no calls.
type Pusher interface {
	// Subscribe registers hook.URL and hook.Secret with the app, and answers
	// the app's id for the subscription.
	Subscribe(ctx context.Context, s Session, trigger string, config json.RawMessage, hook Hook) (remoteID string, err error)
	Unsubscribe(ctx context.Context, s Session, remoteID string) error
	Parse(ctx context.Context, header http.Header, body []byte) ([]Event, error)
}

// Session is what an integration calls its app with: one connection, for one
// call. The core builds it, and nothing in it outlives the call.
type Session struct {
	// HTTP places the credential, dials safely, and refuses writes for a
	// read tool. An integration builds its SDK client on it, and never sets
	// Authorization or Cookie itself.
	HTTP *http.Client
	// BaseURL is the manifest's api_base with the connection's fields filled
	// in.
	BaseURL string
	// Settings are the connection's non-secret fields, by name, and its
	// account label and ref, under SettingAccountLabel and SettingAccountRef.
	Settings map[string]string
	// Token answers the connection's access token. It is only for SDKs with
	// no client hook (slack-go); everything else goes through HTTP.
	Token func(context.Context) (string, error)
}

// The keys Session.Settings carries beside a connection's fields. A field's
// name never holds a dot, so they cannot collide with one.
const (
	SettingAccountLabel = "account.label"
	SettingAccountRef   = "account.ref"
)

// Hook is where the core receives a push subscription's deliveries, and the
// secret it checks them with.
//
// However a Hook is printed, logged or encoded, the secret is left out.
type Hook struct{ URL, Secret string }

// String prints the hook without its secret.
func (h Hook) String() string {
	return fmt.Sprintf("{URL:%s Secret:%s}", h.URL, redact(h.Secret))
}

// GoString prints the hook without its secret, for %#v.
func (h Hook) GoString() string {
	return fmt.Sprintf("integration.Hook{URL:%q, Secret:%q}", h.URL, redact(h.Secret))
}

// LogValue logs the hook without its secret.
func (h Hook) LogValue() slog.Value {
	return slog.GroupValue(slog.String("url", h.URL), slog.String("secret", redact(h.Secret)))
}

// MarshalJSON encodes the hook without its secret.
func (h Hook) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		URL    string `json:"url"`
		Secret string `json:"secret"`
	}{h.URL, redact(h.Secret)})
}

func redact(secret string) string {
	if secret == "" {
		return ""
	}
	return "[redacted]"
}

var (
	// ErrNeedsReconnect means the app refused the credential: a 401, or
	// invalid_grant on a refresh. The connection expires, and the member
	// connects again.
	ErrNeedsReconnect = errors.New("needs_reconnect")
	// ErrNotFound means what a call named is not there: a 404.
	ErrNotFound = errors.New("not_found")
)

// RateLimited is an error: the app answered 429. RetryAfter is how long it
// asked for, or zero when it did not say.
type RateLimited struct{ RetryAfter time.Duration }

func (e *RateLimited) Error() string {
	if e.RetryAfter > 0 {
		return fmt.Sprintf("rate_limited: retry after %s", e.RetryAfter)
	}
	return "rate_limited"
}

// AppError is an error: the app refused what was asked, for a reason of its
// own. The core answers it as successful: false with the code app_error, and
// Message is what the caller, often a model, reads to try again differently.
// It never carries a credential.
type AppError struct{ Message string }

func (e *AppError) Error() string {
	if e.Message == "" {
		return "app_error"
	}
	return e.Message
}

type idempotencyKeyKey struct{}

// WithIdempotencyKey is how the core hands a write's idempotency key to its
// ToolFunc.
func WithIdempotencyKey(ctx context.Context, key string) context.Context {
	return context.WithValue(ctx, idempotencyKeyKey{}, key)
}

// IdempotencyKey answers the idempotency key of the write ctx runs, for a
// provider that accepts one, or "" when there is none, as for a read.
func IdempotencyKey(ctx context.Context) string {
	key, _ := ctx.Value(idempotencyKeyKey{}).(string)
	return key
}

// ToolSpec is one tool, as a caller chooses between them.
type ToolSpec struct {
	// Name keeps Composio's shape, toolkit prefix and all:
	// GITHUB_LIST_ISSUES. Krude's skill reads tools by these names.
	Name        string `json:"name"`
	Description string `json:"description"`
	// ReadOnly false means a write: it takes an idempotency key, and the
	// member confirms it.
	ReadOnly    bool `json:"read_only"`
	Destructive bool `json:"destructive"`
	// ReadPOSTs are the POST paths a read tool may call, under api_base, for
	// the APIs that read through a POST (search). A segment written {}
	// stands for any one segment, as apps' read proxy matches them. A read
	// tool's Session refuses every other POST.
	ReadPOSTs   []string        `json:"read_posts,omitempty"`
	Scopes      []string        `json:"scopes"`
	InputSchema json.RawMessage `json:"input_schema"`
}

// ReaderSpec is one reader knowledge can register sources on.
type ReaderSpec struct {
	Name         string          `json:"name"`
	Kinds        []string        `json:"kinds"`
	TargetSchema json.RawMessage `json:"target_schema"`
	Scopes       []string        `json:"scopes"`
	// ReadPOSTs are the POST paths under api_base this reader may call, for
	// the APIs that read through a POST (Notion's query, a search endpoint).
	// Its session refuses every other method before anything is sent, as a
	// read tool's does. It is additive to the pinned shape: a reader that
	// declares none reads with GET and HEAD alone.
	ReadPOSTs []string `json:"read_posts,omitempty"`
}

// Trigger kinds: how a trigger's events arrive.
const (
	TriggerPoll = "poll"
	TriggerPush = "push"
)

// TriggerSpec is one event an app can deliver.
type TriggerSpec struct {
	// Slug keeps Composio's shape, toolkit prefix and all. A breaking change
	// to the payload gets a new slug (…_V2).
	Slug string `json:"slug"`
	Name string `json:"name"`
	// Kind is poll or push. A push trigger needs the manifest's webhook.
	Kind         string          `json:"kind"`
	ConfigSchema json.RawMessage `json:"config_schema"`
	// PayloadSchema is the event contract: every event's Data validates
	// against it.
	PayloadSchema json.RawMessage `json:"payload_schema"`
	Sample        json.RawMessage `json:"sample"`
	Scopes        []string        `json:"scopes"`
}

// TargetReader is the PageRequest.Target key that names the reader a page is
// for, as ReaderSpec.Name.
const TargetReader = "reader"

// MaxPageBytes bounds one page, in either direction: the request that asks
// for it and the answer that carries it. A page is a document payload, so it
// is measured in mebibytes rather than in kilobytes as an event's is; an
// item that does not fit is one page too many, and the reader carries on
// from the cursor it answered.
const MaxPageBytes = 6 << 20

// PageRequest asks a reader for one page of one target.
type PageRequest struct {
	// Target always carries TargetReader, the ReaderSpec it is for.
	Target    map[string]string `json:"target"`
	Cursor    string            `json:"cursor,omitempty"`
	Watermark *time.Time        `json:"watermark,omitempty"`
	Token     string            `json:"token,omitempty"`
	Full      bool              `json:"full"`
	Limit     int               `json:"limit"`
}

// Page is one page a reader answered. It maps one-to-one onto knowledge's
// sources.Page.
type Page struct {
	Items []Item `json:"items"`
	// Cursor is where to carry on; "" means the target is drained.
	Cursor    string     `json:"cursor,omitempty"`
	Watermark *time.Time `json:"watermark,omitempty"`
	Token     string     `json:"token,omitempty"`
	Removed   []string   `json:"removed,omitempty"`
}

// Item is one document a reader yielded, as the app gave it.
type Item struct {
	ExternalID string            `json:"external_id"`
	Kind       string            `json:"kind"`
	Title      string            `json:"title,omitempty"`
	URL        string            `json:"url,omitempty"`
	MIME       string            `json:"mime,omitempty"`
	Content    []byte            `json:"content,omitempty"`
	Metadata   map[string]string `json:"metadata,omitempty"`
	UpdatedAt  *time.Time        `json:"updated_at,omitempty"`
}

// Batch is what one poll answered.
type Batch struct {
	Events []Event `json:"events"`
	Cursor string  `json:"cursor"`
}

// Event is one thing that happened in an app, as a trigger delivers it.
type Event struct {
	// ID is the app's own id for it; events are deduped on it per trigger.
	ID      string `json:"id"`
	Trigger string `json:"trigger"`
	// AccountRef routes a pushed event to the connections whose account it
	// is, matched against the manifest's test.account_ref.
	AccountRef string    `json:"account_ref,omitempty"`
	OccurredAt time.Time `json:"occurred_at"`
	// Data is valid against the trigger's payload_schema.
	Data json.RawMessage `json:"data"`
}
