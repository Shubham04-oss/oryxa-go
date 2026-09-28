# Integrations plan

*Decided 2026-09-28. The owner approved building phase 1, which ships switched off. This supersedes the Nango plan and closes the discussion in [integrations-research.md](integrations-research.md).*

Composio stays the tool provider, behind apps' own MCP server. Nango, Activepieces and the archived `connections` service stay out. The archive tag is only a reference, for four ideas: per-organization OAuth apps with a sealed secret, a refresh lease, the SSRF-safe dialer, and health checks. AgentCore Identity was rejected for sign-ins; AgentCore Gateway is phase 3.

## Phases

| | Builds | By | Exit |
|---|---|---|---|
| 1 | One sign-in and token system for every credential kind; one webhook ingress | Main session | Conformance tests against a stub OAuth server (code + PKCE, rotation, `invalid_grant`, revoke). Live runs: GitHub on Oryxa's app, Google on an organization's own client, one API-token app, a signed Slack event with `url_verification`, a GitHub ping. Composio's tests still pass unchanged |
| 2 | End-to-end integrations on official SDKs (MCP tools, knowledge readers, triggers), using only phase-1 building blocks. Order: github, slack, gmail, googledrive, googlecalendar, notion | Cheap workers, one toolkit each | Gates G1–G4, per toolkit |
| 3 | AgentCore Gateway in front of the MCP server | A spike, then the main session | A written decision |

Phase 1 ships switched off. A toolkit uses the Oryxa route only after it is added to `APPS_ORYXA_TOOLKITS`, which happens at gate G4.

## Where phase 1 lives: `apps`

Recommendation: extend apps with a `broker` column (`composio` | `oryxa`). This is the "provider field" the brief asked about. It is named `broker` so nobody confuses it with knowledge's `provider`, which means the app. Don't build a new service, for three reasons:

- **Apps already owns everything a connection needs:**
  - `apps_connections`, and the rule that a call's account comes from auth's answer about the caller;
  - the index allowing one current connection per `(organization_id, member_id, toolkit)`, which then also means one broker per member per app;
  - return-origin checks, and removing connections on `auth.member.removed` and `auth.organization.deleted`;
  - the outbox, and the `oryxa.apps` events that knowledge and agents already consume;
  - the MCP server, and web-next's Tools screen.
- **Tokens stay in the process that uses them.** A separate service would need an API that hands out tokens, plus a second event stream. That was the archive's bloat.
- **A separate service would isolate nothing.** Production runs one task definition under one task role (`deploy/production/compute.tf`).

New code goes into new packages. Existing apps files change in exactly one work package, 1E.

## Reuse

- **KMS envelope: nothing is shared today.** `auth/internal/secrets` is private to auth and binds `service=oryxa-auth`. Promote it to `internal/secrets`, with the service name as a parameter. Auth's package becomes a thin wrapper with the same wire format, proven by a golden test.
- **Migrations follow the existing convention:**
  - files are `apps/internal/postgres/migrations/NNN_*.sql`, embedded, and run in name order under an advisory lock;
  - each file is recorded by checksum, and an edited file runs again, so every statement is `IF NOT EXISTS`;
  - there are no down migrations.
- **Events:** `internal/outbox` writes to `apps_outbox`. Event types are Go structs in `contracts/events/apps/v1`, and changes are additive only.
- **Patterns to copy:**
  - auth's agent-grant flow: PKCE S256, state bound to the member, a Lax cookie scoped to the callback, 10-minute flows, the return URL checked again at the callback;
  - teams' token-bucket rate limiter;
  - apps' `returnAllowed`;
  - `go-oidc` and `golang.org/x/oauth2`, which are already direct dependencies.
- **Dialer:** restore the archive's SSRF-safe dialer as `internal/safehttp`. Knowledge's `storedFiles` client has a weaker copy.

## Phase 1 work packages

| WP | Delivers | Owns, exclusively |
|---|---|---|
| 1A | `internal/secrets`, `internal/safehttp` | `internal/secrets/**`, `internal/safehttp/**`, `auth/internal/secrets/**` |
| 1B | Contract types, validation and registry; manifests with only the auth and webhook sections for github, slack, notion, and the shared Google method | `apps/internal/integration/**`, `apps/internal/integrations/**` |
| 1C | `signin`: credential kinds, fields, credential placement, test calls, sealing, refresh under a lease, revocation, health checks, organization OAuth clients | `apps/internal/signin/**`, `apps/internal/postgres/signin.go`, `…/migrations/006_signin.sql` |
| 1D | `hooks`: verification schemes, handshakes, dedupe, sealed endpoint secrets, endpoint renewals | `apps/internal/hooks/**`, `apps/internal/postgres/hooks.go`, `…/migrations/007_hooks.sql` |
| 1E | Wiring: routes, rate limits, broker-aware connect and remove, events, config. Composio paths (tools, read proxy, targets, auto-subscribe) skip `oryxa` connections | `apps/internal/{domain,application,httpserver,serviceconfig}/**`; `apps/internal/postgres/**` except 1C's and 1D's files; `apps/{service,mount}.go`; `apps/README.md`; `contracts/events/apps/v1/**`; `.env.example` |
| 1F | A KMS key for the vault; an IAM grant conditioned on `kms:EncryptionContext:service=oryxa-apps`; stored secrets for Oryxa's default apps and the deployment's webhook secrets | `deploy/production/{keys,compute,secrets}.tf`, `deploy/scripts/put-secrets.sh` |
| 1G | Knowledge refuses to register a source through an `oryxa` connection unless the extractor mounted for that toolkit reads through apps | `knowledge/internal/appsclient/client.go`, `knowledge/internal/application/sources.go` |
| 1W | The web-next screens below | `web-next/src/lib/apps/**`, `web-next/src/components/{workspace-tool-connection,workspace-settings}.tsx`, new `workspace-tool-credential-dialog.tsx` and `workspace-signin-apps-settings.tsx`, `web-next/PARITY.md` |

Order: 1A and 1B first, then 1C and 1D, then 1E, then 1W. 1F and 1G can run alongside.

**Screens (1W):**

- **Tools → Connect:** for OAuth, a redirect to the provider. The callback returns with the same `app_connection` and `status` parameters apps uses today, so `parseConnectReturn` is reused.
- **Credential dialog:** rendered from the manifest, covering standard and extra fields. Secret fields are write-only and masked. It also serves "Replace key".
- **Settings → Sign-in apps (admins):** an organization's own OAuth app, per client. It shows:
  - the client ID and a write-only secret, plus any client-level fields;
  - the redirect URI and webhook URL, to copy into the provider;
  - an organization webhook secret;
  - a "don't use Oryxa's app" switch.

**Routes (1E):**

- `POST …/apps/connections` also takes `broker`, `kind` and `fields`.
- `GET /v1/apps/oauth/callback`
- `PUT …/apps/connections/{id}/credentials`
- `GET /v1/apps/integrations/{toolkit}/manifest`
- `GET|PUT|DELETE …/apps/oauth-clients/{client}`
- `POST /v1/apps/hooks/{endpointID}`

There is one new error code, `invalid_credentials`. It names the failing field and never its value.

## Pinned contracts

### Tables

```text
006  apps_connections + broker text NOT NULL DEFAULT 'composio' CHECK (broker IN ('composio','oryxa'))
     apps_oauth_clients (id, organization_id ('' = Oryxa default), client, client_id, sealed_secret, settings,
       sealed_fields, status active|removed, created_by, created_at, updated_at)
       -- one live row per (organization_id, client); a replaced row stays while connections refresh/revoke through it
     apps_credentials (connection_id PK, kind, oauth_client_id, sealed, settings, scopes, expires_at, version,
       lease_token, lease_until, next_check_at, check_failures, updated_at)
     apps_signin_flows (state_hash PK, organization_id, member_id, connection_id, code_verifier, redirect_uri,
       return_to, expires_at, consumed_at)   -- as auth's login flows: 10 minutes, single use
007  apps_hook_endpoints (id PK = 120 random bits, the only thing in the URL; toolkit, organization_id, connection_id,
       scheme jsonb frozen at creation, sealed_secret, remote_id, expires_at, status, created_at)
```

Every `sealed*` column holds an `internal/secrets` envelope bound to `service=oryxa-apps`, the resource type and the resource id. Delivery dedupe reuses `apps_handled_events` with the key `hook:{endpoint}:{delivery}`, kept for 7 days.

### Manifest and credential schema (`apps/internal/integration`)

```go
type Manifest struct {
	Toolkit  string        `json:"toolkit"` // Composio's slug: "github"
	Version  int           `json:"version"`
	Name     string        `json:"name"`
	APIBase  string        `json:"api_base"` // may hold {field}
	Auth     Auth          `json:"auth"`
	Webhook  *Webhook      `json:"webhook,omitempty"`
	Tools    []ToolSpec    `json:"tools,omitempty"`
	Readers  []ReaderSpec  `json:"readers,omitempty"`
	Triggers []TriggerSpec `json:"triggers,omitempty"`
}

type Auth struct {
	Methods []Method `json:"methods"` // alternatives; the first is the default
	Test    Test     `json:"test"`
}

type Method struct {
	Kind   string  `json:"kind"` // oauth2_code | oauth2_client_credentials | api_token | basic | a core-added kind
	OAuth2 *OAuth2 `json:"oauth2,omitempty"`
	Place  *Place  `json:"place,omitempty"` // api_token
	Fields []Field `json:"fields,omitempty"`
}

type OAuth2 struct {
	Client      string            `json:"client"` // registration shared across toolkits: "google"
	AuthURL     string            `json:"auth_url"`
	TokenURL    string            `json:"token_url"`
	RevokeURL   string            `json:"revoke_url,omitempty"`
	RevokeStyle string            `json:"revoke_style,omitempty"` // rfc7009 (default) | none | a core-added style
	Scopes      []string          `json:"scopes"`
	ScopeSep    string            `json:"scope_separator,omitempty"`
	PKCE        bool              `json:"pkce"`
	AuthParams  map[string]string `json:"auth_params,omitempty"`
	ClientAuth  string            `json:"client_auth"` // header | body
	Rotating    bool              `json:"rotating_refresh,omitempty"`
}

type Place struct {
	In       string `json:"in"`       // header | query (last resort)
	Name     string `json:"name"`     // "Authorization"
	Template string `json:"template"` // "Bearer {token}"
}

type Field struct {
	Name     string     `json:"name"` // ^[a-z][a-z0-9_]{0,31}$
	Label    string     `json:"label"`
	Help     string     `json:"help,omitempty"`
	Secret   bool       `json:"secret"`
	Level    string     `json:"level"`              // connection (member) | client (admin) | discovered
	Discover string     `json:"discover,omitempty"` // JSON pointer into the token response
	Validate Validation `json:"validate"`
	Use      []Use      `json:"use"`
}

type Validation struct {
	Required bool     `json:"required"`
	Format   string   `json:"format,omitempty"`  // hostname | https_url | uuid | email
	Pattern  string   `json:"pattern,omitempty"` // RE2, anchored
	Enum     []string `json:"enum,omitempty"`
	MaxLen   int      `json:"max_length,omitempty"`
}

type Use struct {
	In       string `json:"in"`                 // header | query | url | token_request
	Name     string `json:"name,omitempty"`     // header/query/form name; url fills {name} in the manifest's URLs
	Template string `json:"template,omitempty"` // default "{value}"
}

type Test struct {
	Path       string `json:"path"`                  // a GET under api_base
	Label      string `json:"label"`                 // JSON pointer to the account's name
	AccountRef string `json:"account_ref,omitempty"` // JSON pointer to its stable id; push events route by it
}
```

Rules for fields:

- **Standard fields are never declared.** `oauth2_code` has none, because its client comes from `apps_oauth_clients` or an Oryxa default. `oauth2_client_credentials` has `client_id` and `client_secret`. `api_token` has `token`. `basic` has `username` and `password`.
- **Secret fields** are sealed, write-only and masked.
- **Non-secret fields** are stored in `settings`.
- **A PAT instead of a client ID** is a second entry in `methods`.

The values below are illustrative; workers confirm them against each provider's docs.

```json
{"toolkit": "github", "version": 1, "name": "GitHub", "api_base": "https://api.github.com",
 "auth": {"methods": [
   {"kind": "oauth2_code", "oauth2": {"client": "github", "auth_url": "https://github.com/login/oauth/authorize",
     "token_url": "https://github.com/login/oauth/access_token", "revoke_style": "github_grant",
     "scopes": ["repo", "read:org"], "pkce": true, "client_auth": "body"}},
   {"kind": "api_token", "place": {"in": "header", "name": "Authorization", "template": "Bearer {token}"}}],
   "test": {"path": "/user", "label": "/login", "account_ref": "/id"}},
 "webhook": {"scope": "connection", "verify": {"type": "hmac_sha256",
   "signature": {"header": "X-Hub-Signature-256", "prefix": "sha256="}, "encoding": "hex", "signed": "{body}"},
   "delivery_id": {"header": "X-GitHub-Delivery"}, "handshake": "github_ping"}}
```

```json
[{"name": "subdomain", "label": "Zendesk subdomain", "secret": false, "level": "connection",
  "validate": {"required": true, "pattern": "[a-z0-9][a-z0-9-]{0,62}"}, "use": [{"in": "url"}]},
 {"name": "region", "label": "Region", "secret": false, "level": "connection",
  "validate": {"required": true, "enum": ["us", "eu"]}, "use": [{"in": "url"}]},
 {"name": "audience", "label": "API audience", "secret": false, "level": "client",
  "validate": {"required": true, "format": "https_url"}, "use": [{"in": "token_request", "name": "audience"}]},
 {"name": "instance_url", "label": "Instance", "secret": false, "level": "discovered", "discover": "/instance_url",
  "validate": {"format": "https_url"}, "use": [{"in": "url"}]}]
```

### Webhook schemes

```go
type Webhook struct {
	Scope      string  `json:"scope"` // connection | organization | deployment: whose endpoint and secret
	Verify     Verify  `json:"verify"`
	DeliveryID Locator `json:"delivery_id"`
	Handshake  string  `json:"handshake,omitempty"` // slack_url_verification | github_ping | graph_validation | google_channel_sync
}

type Verify struct {
	Type      string   `json:"type"` // hmac_sha256 | static_token | jwt_push | a core-added scheme
	Signature *Locator `json:"signature,omitempty"`
	Encoding  string   `json:"encoding,omitempty"` // hex | base64
	Signed    string   `json:"signed,omitempty"`   // "{body}" | "v0:{timestamp}:{body}" | "{id}.{timestamp}.{body}"
	Timestamp *Locator `json:"timestamp,omitempty"`
	Tolerance int      `json:"tolerance_seconds,omitempty"` // default 300 when a timestamp is set
	Token     *Locator `json:"token,omitempty"`             // static_token, checked against the sealed secret
	Issuer    string   `json:"issuer,omitempty"`            // jwt_push, verified with go-oidc
	Audience  string   `json:"audience,omitempty"`          // may use {endpoint_url}
	Email     string   `json:"email,omitempty"`             // may use {config:name}
}

type Locator struct {
	Header string `json:"header,omitempty"`
	JSON   string `json:"json,omitempty"`   // RFC 6901; "*" = every element, all must match
	Hash   bool   `json:"hash,omitempty"`   // SHA-256 of the body, for payloads without an id
	Prefix string `json:"prefix,omitempty"` // "sha256=", "v0="
	Split  string `json:"split,omitempty"`  // several signatures during a rotation
	Unit   string `json:"unit,omitempty"`   // s | ms
}
```

```json
{"slack":       {"scope": "deployment", "verify": {"type": "hmac_sha256", "signature": {"header": "X-Slack-Signature", "prefix": "v0="}, "encoding": "hex", "signed": "v0:{timestamp}:{body}", "timestamp": {"header": "X-Slack-Request-Timestamp", "unit": "s"}, "tolerance_seconds": 300}, "delivery_id": {"json": "/event_id"}, "handshake": "slack_url_verification"},
 "linear":      {"scope": "connection", "verify": {"type": "hmac_sha256", "signature": {"header": "Linear-Signature"}, "encoding": "hex", "signed": "{body}", "timestamp": {"json": "/webhookTimestamp", "unit": "ms"}, "tolerance_seconds": 60}, "delivery_id": {"header": "Linear-Delivery"}},
 "gmail":       {"scope": "deployment", "verify": {"type": "jwt_push", "issuer": "https://accounts.google.com", "audience": "{endpoint_url}", "email": "{config:pubsub_push_account}"}, "delivery_id": {"json": "/message/messageId"}},
 "googledrive": {"scope": "connection", "verify": {"type": "static_token", "token": {"header": "X-Goog-Channel-Token"}}, "delivery_id": {"header": "X-Goog-Message-Number"}, "handshake": "google_channel_sync"},
 "outlook":     {"scope": "connection", "verify": {"type": "static_token", "token": {"json": "/value/*/clientState"}}, "delivery_id": {"hash": true}, "handshake": "graph_validation"}}
```

**Where webhook secrets come from:**

- **`connection` scope:** the core generates the secret and passes it to `Pusher.Subscribe`. Examples: GitHub's hook secret, Google's channel token, Graph's `clientState`.
- **`organization` scope:** an admin enters it, for example their own Slack app's signing secret.
- **`deployment` scope:** Secrets Manager.

Secrets stored in the database are sealed. During a rotation, both the old and new secret are accepted for 24 hours.

**What the ingress does, in order:**

1. Look up the endpoint by the id in the path. An unknown id gets 404 before the body is read.
2. Apply the endpoint's rate limit.
3. Read the body, capped at 1 MiB.
4. Answer Graph's unsigned `validationToken`, but only for an endpoint created in the last ten minutes that has not validated yet.
5. Verify the signature or token, with a constant-time comparison.
6. Check the timestamp tolerance.
7. Answer a signed handshake (Slack's `url_verification`, GitHub's ping).
8. Extract the delivery id and dedupe on it.
9. Call `Pusher.Parse`.
10. Route each event by connection or by `account_ref`.
11. Write `apps.trigger.received.v1` and the dedupe mark in one transaction.
12. Answer 2xx within the provider's deadline.

A delivery that fails verification gets 401, and nothing in it is used.

**New kinds and schemes.** If an integration needs a credential kind or webhook scheme that isn't listed, it says so in its PR and stops. The main session then implements it once, in `signin` or `hooks`, with conformance tests. The likely first ones are GitHub App installation tokens, OAuth 1.0a and SigV4. Integrations never import `golang.org/x/oauth2`, never set `Authorization` or `Cookie`, and never verify a signature themselves.

### Integration interface

```go
type Integration interface {
	Manifest() Manifest
	Tools() map[string]ToolFunc // by ToolSpec.Name
}

// For a write, ctx carries IdempotencyKey(ctx), for providers that accept one.
type ToolFunc func(ctx context.Context, s Session, args json.RawMessage) (json.RawMessage, error)

type Reader interface { // optional
	Targets(ctx context.Context, s Session) ([]Target, error) // Target: apps' existing targets shape
	Read(ctx context.Context, s Session, r PageRequest) (Page, error)
}

type Poller interface { // optional; the first Poll with no cursor answers no events and a cursor
	Poll(ctx context.Context, s Session, trigger string, config json.RawMessage, cursor string) (Batch, error)
}

type Pusher interface { // optional; Parse only ever sees deliveries the core verified
	Subscribe(ctx context.Context, s Session, trigger string, config json.RawMessage, hook Hook) (remoteID string, err error)
	Unsubscribe(ctx context.Context, s Session, remoteID string) error
	Parse(ctx context.Context, header http.Header, body []byte) ([]Event, error)
}

type Session struct {
	HTTP     *http.Client                          // places the credential, dials safely, refuses writes for a read tool
	BaseURL  string                                // api_base with fields filled in
	Settings map[string]string                     // non-secret fields, account label and ref
	Token    func(context.Context) (string, error) // only for SDKs with no client hook (slack-go)
}

type Hook struct{ URL, Secret string }

var (
	ErrNeedsReconnect = errors.New("needs_reconnect") // 401 or invalid_grant: the connection expires
	ErrNotFound       = errors.New("not_found")
)

type RateLimited struct{ RetryAfter time.Duration } // an error: 429
type AppError struct{ Message string }              // an error: the app refused (successful:false, app_error)

type ToolSpec struct {
	Name        string          `json:"name"` // GITHUB_LIST_ISSUES: Composio's shape, toolkit prefix
	Description string          `json:"description"`
	ReadOnly    bool            `json:"read_only"` // false: idempotency key, and the member confirms
	Destructive bool            `json:"destructive"`
	ReadPOSTs   []string        `json:"read_posts,omitempty"` // POST paths a read tool may call (search APIs)
	Scopes      []string        `json:"scopes"`
	InputSchema json.RawMessage `json:"input_schema"`
}

type ReaderSpec struct {
	Name         string          `json:"name"`
	Kinds        []string        `json:"kinds"`
	TargetSchema json.RawMessage `json:"target_schema"`
	Scopes       []string        `json:"scopes"`
}

type TriggerSpec struct {
	Slug          string          `json:"slug"`
	Name          string          `json:"name"`
	Kind          string          `json:"kind"` // poll | push
	ConfigSchema  json.RawMessage `json:"config_schema"`
	PayloadSchema json.RawMessage `json:"payload_schema"` // the event contract
	Sample        json.RawMessage `json:"sample"`
	Scopes        []string        `json:"scopes"`
}

type PageRequest struct {
	Target    map[string]string `json:"target"` // always carries "reader"
	Cursor    string            `json:"cursor,omitempty"`
	Watermark *time.Time        `json:"watermark,omitempty"`
	Token     string            `json:"token,omitempty"`
	Full      bool              `json:"full"`
	Limit     int               `json:"limit"`
}

type Page struct { // one-to-one with knowledge's sources.Page
	Items     []Item     `json:"items"`
	Cursor    string     `json:"cursor,omitempty"` // "" = drained
	Watermark *time.Time `json:"watermark,omitempty"`
	Token     string     `json:"token,omitempty"`
	Removed   []string   `json:"removed,omitempty"`
}

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

type Batch struct {
	Events []Event `json:"events"`
	Cursor string  `json:"cursor"`
}

type Event struct {
	ID         string          `json:"id"` // the provider's id; deduped per trigger
	Trigger    string          `json:"trigger"`
	AccountRef string          `json:"account_ref,omitempty"` // push: routes the event to connections
	OccurredAt time.Time       `json:"occurred_at"`
	Data       json.RawMessage `json:"data"` // valid against payload_schema
}
```

### Tools beside Composio

`apps/internal/mcpserver` and `agents/primary` don't change. The four meta tools and their schemas stay as they are, and the member's current connection for a toolkit decides the route.

- **Search:** over an `oryxa` connection, `apps_search_tools` answers from the registry. It does a case-insensitive match on name and description (all tools when there's no query) and answers in today's `ToolView` JSON.
- **Run:** the slug is looked up in the registry first. If the member's connection for that toolkit is `oryxa`, apps:
  - checks the arguments against `input_schema`;
  - runs the `ToolFunc`;
  - records `apps_actions`, the idempotency key, replay and `apps.action.*` events exactly as it does for Composio;
  - answers an `AppError` as `successful: false` / `app_error`.
- **Otherwise** the Composio path runs, and it now skips `oryxa` connections.

Native tool names keep Composio's shape, so Krude's skill doesn't change. A read tool's `Session.HTTP` refuses every method except GET, HEAD and that tool's `read_posts`.

### Readers into knowledge

- **Apps** serves `POST /v1/organizations/{org}/apps/connections/{id}/pages`:
  - member-authenticated, and only for the connection's own member;
  - the body is a `PageRequest` and the answer a `Page`, at most 6 MiB.
  
  Apps also answers `…/targets` from the reader.
- **Knowledge** adds one extractor, `apps`, registered under each toolkit whose integration has readers:
  - it calls the pages route with a member token from `internal/membertoken`, as the files source already does;
  - a `limit: 1` call serves as its `Checker`.
- **Composio-backed sources** keep their existing extractors.
- **There is no `Held`.** Incremental passes read by watermark or token, and knowledge's content hash keeps unchanged documents from being published again.

### Events (additive, `contracts/events/apps/v1`)

```go
const (
	BrokerOryxa                  = "oryxa" // Broker on connection events, omitempty: Composio's stay byte-identical
	CredentialReplacedDetailType = "apps.credential.replaced.v1"
	OAuthClientSavedDetailType   = "apps.oauth_client.saved.v1"
	OAuthClientRemovedDetailType = "apps.oauth_client.removed.v1"
	FailedInvalidCredentials     = "invalid_credentials" // ConnectionFailed.Reason
	ExpiredRefreshRejected       = "refresh_rejected"    // ConnectionExpired.Reason
	ExpiredCredentialRejected    = "credential_rejected"
)

type CredentialReplaced struct {
	ConnectionID   string    `json:"connection_id"`
	OrganizationID string    `json:"organization_id"`
	MemberID       string    `json:"member_id"`
	Toolkit        string    `json:"toolkit"`
	Kind           string    `json:"kind"`
	ReplacedAt     time.Time `json:"replaced_at"`
}

type OAuthClientChanged struct { // the detail of both oauth_client types; never carries the secret
	OrganizationID string    `json:"organization_id"`
	Client         string    `json:"client"`
	ClientID       string    `json:"client_id"`
	ActorID        string    `json:"actor_id"`
	ChangedAt      time.Time `json:"changed_at"`
}
```

`ConnectionRemoved` gains `ProviderRevoked *bool` (`json:"provider_revoked,omitempty"`).

Integrations add no new detail types. A trigger delivers as `apps.trigger.received.v1`: `trigger` is the slug, and `data` must validate against the trigger's `payload_schema`. As today, a payload over 128 KiB is left out and flagged `data_omitted`. A breaking payload change gets a new slug (`…_V2`). Batching, and agents through it, consume these events unchanged.

### Recorded-response harness (`apps/internal/integration/integrationtest`)

```go
func Session(t *testing.T, cassette string, settings map[string]string) integration.Session
func Conformance(t *testing.T, i integration.Integration, testdata string)
```

```json
{"interactions": [{
  "request":  {"method": "GET", "url": "https://api.github.com/repos/acme/api/issues?per_page=50&state=open"},
  "response": {"status": 200, "headers": {"Link": ["<https://api.github.com/repositories/1/issues?page=2>; rel=\"next\""]},
               "body": [{"number": 7, "title": "Flaky test"}]}}]}
```

**How cassettes work:**

- Requests match on method, URL (with the query sorted) and a JSON-equal body. An unmatched request fails the test, and so does an interaction that is never used.
- `Authorization` and `Cookie` headers are never recorded.
- The session carries a fake token, and that token must not appear in any result, error or captured log line.
- Cassettes are built from the provider's documented example responses, so no worker needs a live account.

**What `Conformance` checks:**

- The manifest validates.
- Every tool has an `ok` cassette and an `app_error` cassette, and 401 and 429 map to `ErrNeedsReconnect` and `RateLimited`.
- A read tool's cassettes contain only GET, HEAD and its declared `read_posts`.
- Every reader drains two pages and can resume from the middle.
- The first poll of every poll trigger answers no events and a cursor.
- For every push trigger, `Parse` over `testdata/hooks/` yields events that validate against `payload_schema`.
- Every `sample` payload validates.

## Phase 2 work packages

| WP | Delivers | Owns, exclusively |
|---|---|---|
| 2A | The runtime: registry-first tool routing, the pages route, the poll worker, push subscriptions through `hooks`, the harness, `cmd/check`, `_template`, and the SDK modules (go-github, slack-go) added up front | `apps/internal/integration/**`; `apps/internal/integrations/{registry.go,_template/**,google/**}`; `apps/internal/{application,httpserver}/**`; `apps/internal/postgres/triggers.go`; `…/migrations/008_native_triggers.sql` (triggers gain `broker`, `cursor`, `next_poll_at` and a lease; the Composio instance index skips `''`); `go.mod`; `go.sum` |
| 2B | Knowledge's `apps` extractor | `knowledge/internal/sources/apps.go`, `knowledge/internal/appsclient/**`, `knowledge/internal/application/{connected,sources}.go`, `knowledge/{mount,service}.go` |
| 2C… | One integration per toolkit, built in parallel | `apps/internal/integrations/<toolkit>/**` |

### Work-package template

```text
WORK PACKAGE: integration <toolkit>          worker: <model>    reviewer: main session
Owns:    apps/internal/integrations/<toolkit>/** and nothing else (go.mod already has the SDK)
Frozen:  the package's Auth and Webhook; SDK <module@version>
Build:   tools    <NAME> · read|write · scopes · what it does
         readers  <name> · kinds · target fields
         triggers <SLUG> · poll|push · config · payload fields
Steps:   1 Copy apps/internal/integrations/_template.
         2 Fill Manifest(): tools, readers, triggers. Need another auth kind or webhook scheme? Stop; say so in the PR.
         3 Build the SDK client on Session.HTTP; use Session.Token only if the SDK has no client hook.
         4 Errors: 401 ErrNeedsReconnect, 429 RateLimited, 404 ErrNotFound, any other 4xx AppError.
         5 Readers: use the provider's own paging; watermark or token only on incremental passes; under 6 MiB a page.
         6 Triggers: the first Poll without a cursor returns no events and a cursor. Push: Subscribe registers
           hook.URL and hook.Secret; Parse only reads.
         7 Cassettes in testdata/, built from the provider's documented responses; no real tokens, emails or ids.
         8 Pass: go test ./apps/internal/integrations/<toolkit>/... and go run ./apps/internal/integration/cmd/check <toolkit>
Never:   oauth2 imports; Authorization or Cookie headers; logging Session, arguments or results;
         network in tests; files outside the package
PR:      each tool with read|write and the scope it needs; readers; triggers; the check output
```

### Review gates

- **G1, CI:** `Conformance` and `cmd/check` pass. `cmd/check` confirms:
  - the manifest is valid, and every name carries the toolkit prefix;
  - every declared scope is used, and every tool's scopes are within its method's;
  - there are no forbidden imports;
  - a secret scan of `testdata/` is clean;
  - the diff stays inside the owned directory.
- **G2, main-session review:** read/write and destructive marks, pagination and resumption, error mapping, payload schemas.
- **G3, live smoke test:** once per manifest version, by a person with a real account. It covers the test call, every read tool, one confirmed write, one reader pass into knowledge, and one trigger delivery.
- **G4, enable:** the reviewer adds the registry line and adds the toolkit to `APPS_ORYXA_TOOLKITS`.

## Phase 1 hardening checklist

**State and PKCE**

- [ ] **State:** 32 random bytes. Only its SHA-256 is stored; it expires after 10 minutes and is consumed atomically before the code exchange. It is also set as an HttpOnly, Lax cookie scoped to `/v1/apps/oauth/callback`.
- [ ] **Callback binding:** the callback requires the cookie to equal the state. If an access cookie is present, it must belong to the flow's member and organization.
- [ ] **PKCE S256** always, unless the manifest says the provider refuses it (G2 reviews any such case). There is one fixed `redirect_uri`, stored with the flow and sent back unchanged.
- [ ] **Return page:** `return_to` goes through `returnAllowed` at the start and again at the callback.

**Tokens and credentials**

- [ ] **Refresh:** only under the credential's lease. This is a compare-and-swap on `version`, using the archive's `AcquireLease`/`SaveRenewedCredentials` semantics. The save runs on a detached context. `invalid_grant` marks the connection expired with reason `refresh_rejected`.
- [ ] **Disconnect:**
  - revokes at the provider (per `revoke_style`);
  - deletes the sealed credential and the push endpoints;
  - records `provider_revoked`.
  
  A failed revocation is retried later and never blocks the removal.
- [ ] **Validation:**
  - keys and passwords must pass the manifest's test call before they are saved;
  - OAuth connections run the test call after the code exchange;
  - idle connections get a daily health check, skipped if the connection was used in the last 12 hours, with the archive's backoff.
- [ ] **Secrets:**
  - each value is sealed separately;
  - a `Secret` type redacts itself in `String`, `GoString`, `LogValue` and `MarshalJSON`;
  - credentials are placed by the innermost transport, below `observability.Transport`, so traces never see them;
  - no API ever returns a secret; it returns a hint instead, the last four characters of a secret that is 16 or more characters long.

**Access and abuse**

- [ ] **Rate limits:** teams' token bucket, per replica:
  - 10 per minute per member for connect and key replacement;
  - 30 per minute per IP for the callback;
  - 50 per second per hook endpoint;
  - 429 `rate_limited` when exceeded, plus a Cloudflare rule on the callback and hook paths.
- [ ] **Audit:** connect, fail, replace, expire, remove, and every OAuth-client change are published as `oryxa.apps` events through the outbox.
- [ ] **SSRF:** any URL filled from a field a member or admin typed is format-checked, then dialed only through `internal/safehttp`, over HTTPS only.
- [ ] **Agents:** agents may not connect an app, replace a key or change an OAuth client.

## Phase 3 spike

A one-week spike. It uses a dev gateway in us-west-2 with the MCP endpoint as its only target, driven by a go-sdk client in `apps/infra/gateway/cmd/spike`. `agents/primary` is not touched.

1. **Identity.** Two options:
   - **(a)** An http/passthrough target that forwards the member's Cognito JWT, so the server keeps asking auth as it does today.
   - **(b)** An `mcpServer` target with a Lambda interceptor that verifies the inbound JWT and forwards it. The server still accepts only a member token, whatever credential the gateway adds.
   
   Recommend (a) unless step 3 turns out to need (b).
2. **Compatibility.** The endpoint only speaks the stateless 2026-07-28 protocol and requires its `Mcp-*` headers. Confirm what the gateway sends to a target. Also confirm whether it renames tools to `<target>___<tool>`. Krude's read list in `agents/primary/internal/agents/apps.go` matches exact names, so renaming would make every call ask for confirmation.
3. **Policy.** Run Cedar in LOG_ONLY mode on tool calls, as generic attribute-based policies. An engine allows 1,000 policies and that limit is not adjustable, so never write one policy per organization. Record which principal and tool attributes reach the policies under (a) and under (b).
4. **Limits.** Measure the p95 latency the gateway adds, and compare the 200 tool calls per second ceiling with projected load.

**What stays in our server** whatever the outcome:

- mapping identity to a connection;
- enforcing read versus write;
- idempotency and replay;
- the credential vault and refresh;
- the webhook ingress;
- audit events.

**What the gateway adds:**

- a front door, throttling, and tool discovery across targets;
- Cedar as a second layer of defence, for example the per-organization tool allow-list that apps' README defers.

## Risks

- **Google verification.** Gmail's restricted scopes need verification and a yearly security assessment. An unverified app is capped at 100 users, and in testing mode its refresh tokens stop working after seven days.
- **Rotating refresh tokens.** A crash between the provider's answer and our save loses the connection. The lease, the detached save and a fault-injection test shrink that window. Whatever is left ends as `expired`, and the member reconnects.
- **A write tool marked as read** would run without the member confirming. Three layers guard against it: a read tool's `Session` refuses writes, G1's cassette check, and G2 review.
- **The two brokers drifting apart.** Both use the same events, meta tools, error codes and screens, and share conformance tests.
- **A public webhook ingress.** Endpoint ids are unguessable, each endpoint is rate-limited, bodies are capped at 1 MiB, and deliveries are deduped. Schemes without a timestamp (GitHub) rely on 7 days of dedupe alone.
- **One task role.** Any code in the binary can use the vault key. The encryption context confines the ciphertext to apps' resources, not the key to apps' code.
- **KMS latency.** Each process caches unsealed access tokens until two minutes before they expire, keyed by credential version. Refresh tokens and client secrets are never cached.
- **Stale instructions.** A local, untracked `CLAUDE.md` in the owner's checkout still describes the archived `connections/` service. Every worker's brief says this plan overrides it. `docs/REMAINING-WORK.md` is updated in the same change as this plan.

## Owner decisions

| Decision | Recommended default |
|---|---|
| When a toolkit switches to Oryxa, and what happens to members' Composio connections | Switch per toolkit at G4. Never migrate silently: a member keeps Composio until they reconnect, and Tools offers "Reconnect with Oryxa" once |
| Which Oryxa default OAuth apps to register | GitHub, Slack and Notion now. Google after verification; until then, Google goes through Composio or an organization's own client |
| Whether an organization may refuse Oryxa's default apps | Yes, with a per-client switch that is off by default |
| Push or poll for triggers | Push wherever a manifest can declare one of the schemes above; otherwise poll every 5 minutes |
| Whether knowledge reading Oryxa connections needs the member's standing agent grant | Yes, as the files source already does |
| Who may add a credential kind or webhook scheme | The main session, with owner sign-off for anything that signs requests |
| AgentCore Policy or in-process `cedar-go`; when Krude's read list learns gateway-prefixed names (`agents/primary`) | Decide after the spike. Enforcement stays in apps either way |

---

## Files to read first
- `apps/internal/application/connections.go`
- `apps/internal/application/tools.go`
- `apps/internal/postgres/migrations/005_single_connection_per_member_app.sql`
- `auth/internal/secrets/envelope.go`
- `knowledge/internal/sources/sources.go`
