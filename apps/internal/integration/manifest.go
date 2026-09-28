package integration

import "regexp"

// Manifest is everything an integration declares about its app. The core
// signs members in, verifies webhooks, serves tools and registers readers
// and triggers from it alone; the integration's code only calls the app.
type Manifest struct {
	// Toolkit is Composio's slug for the app, such as github, so both
	// brokers name an app the same way.
	Toolkit string `json:"toolkit"`
	Version int    `json:"version"`
	Name    string `json:"name"`
	// APIBase is the app's API, which every tool, reader and test call
	// reaches under. It may hold {field} placeholders, filled by fields with
	// a url use.
	APIBase  string        `json:"api_base"`
	Auth     Auth          `json:"auth"`
	Webhook  *Webhook      `json:"webhook,omitempty"`
	Tools    []ToolSpec    `json:"tools,omitempty"`
	Readers  []ReaderSpec  `json:"readers,omitempty"`
	Triggers []TriggerSpec `json:"triggers,omitempty"`
}

// Auth is how a member signs in to the app.
type Auth struct {
	// Methods are alternatives; the first is the default. A connection picks
	// one by its kind, so no two share a kind.
	Methods []Method `json:"methods"`
	Test    Test     `json:"test"`
}

// Credential kinds. The core adds a kind here, once, with its conformance
// tests in signin; an integration that needs one it cannot find says so and
// stops.
const (
	KindOAuth2Code              = "oauth2_code"
	KindOAuth2ClientCredentials = "oauth2_client_credentials"
	KindAPIToken                = "api_token"
	KindBasic                   = "basic"
)

// Method is one way of signing in.
//
// A kind's standard fields are the core's own and are never declared:
// oauth2_code has none, since its client comes from apps_oauth_clients or an
// Oryxa default; oauth2_client_credentials has client_id and client_secret;
// api_token has token; basic has username and password. A personal token
// instead of an OAuth app is a second entry in Methods, not a field.
type Method struct {
	Kind   string  `json:"kind"`
	OAuth2 *OAuth2 `json:"oauth2,omitempty"` // the oauth2 kinds
	Place  *Place  `json:"place,omitempty"`  // api_token
	Fields []Field `json:"fields,omitempty"`
}

// How an OAuth client authenticates to the token endpoint.
const (
	// ClientAuthHeader is HTTP Basic in the Authorization header (RFC 6749
	// section 2.3.1).
	ClientAuthHeader = "header"
	// ClientAuthBody is client_id and client_secret as form parameters.
	ClientAuthBody = "body"
)

// Revoke styles: how a disconnect revokes the credential at the app. A
// failed revocation is retried later and never blocks the removal.
const (
	// RevokeRFC7009 posts token=<access token> as a form to revoke_url, with
	// the client authenticated as client_auth says; a 200 means revoked. It
	// is the default.
	RevokeRFC7009 = "rfc7009"
	// RevokeRFC7009JSON is RFC 7009 with a JSON body, {"token": "<access
	// token>"}, for an app that documents only that (Notion).
	RevokeRFC7009JSON = "rfc7009_json"
	// RevokeNone means the app cannot revoke; the core only forgets the
	// credential.
	RevokeNone = "none"
	// RevokeGitHubToken deletes the one token, not the whole grant: DELETE
	// {api_base}/applications/{client_id}/token with HTTP Basic
	// client_id:client_secret and {"access_token": "<token>"}. A 204 means
	// revoked; a 404 or 422 means it was already gone. Deleting the grant
	// instead would revoke every token the OAuth app holds for that GitHub
	// user, including the same person's connection in another organization.
	// It takes no revoke_url.
	RevokeGitHubToken = "github_token"
	// RevokeSlackAuthRevoke calls revoke_url (Slack's auth.revoke) with the
	// access token as its Bearer credential. Slack answers HTTP 200 either
	// way, so revoked means "ok": true, and the errors invalid_auth,
	// token_revoked and account_inactive mean it was already gone.
	RevokeSlackAuthRevoke = "slack_auth_revoke"
)

// ScopeSeparatorComma joins scopes for an app that wants them
// comma-separated (Slack). The default is a space, as RFC 6749 says.
const ScopeSeparatorComma = ","

// OAuth2 is an OAuth 2.0 sign-in.
type OAuth2 struct {
	// Client names the registration the app's OAuth client belongs to, shared
	// across toolkits: every Google toolkit signs in through "google". The
	// client's id and secret come from apps_oauth_clients, or Oryxa's
	// default for it.
	Client   string `json:"client"`
	AuthURL  string `json:"auth_url"`
	TokenURL string `json:"token_url"`
	// RevokeURL is where RevokeStyle revokes, for the styles that take one.
	RevokeURL string `json:"revoke_url,omitempty"`
	// RevokeStyle is rfc7009 (the default), none, or a style the core added.
	RevokeStyle string   `json:"revoke_style,omitempty"`
	Scopes      []string `json:"scopes"`
	ScopeSep    string   `json:"scope_separator,omitempty"`
	// PKCE is S256 whenever it is true, which it always is unless the app
	// refuses it; a manifest that turns it off is reviewed at G2.
	PKCE bool `json:"pkce"`
	// AuthParams are fixed parameters the authorization URL carries beside
	// the flow's own, such as Google's access_type=offline. They never set
	// the flow's own.
	AuthParams map[string]string `json:"auth_params,omitempty"`
	ClientAuth string            `json:"client_auth"`
	// Rotating says a refresh answers a new refresh token and retires the old
	// one, so only one holder may ever refresh the grant.
	Rotating bool `json:"rotating_refresh,omitempty"`
}

// Where a value goes on a request: a Place's In, and a Use's In.
const (
	InHeader = "header"
	// InQuery is a last resort for a credential: a URL is logged far more
	// often than a header.
	InQuery = "query"
	// InURL fills the field's own {name} placeholder in the manifest's URLs.
	InURL = "url"
	// InTokenRequest adds a form parameter to the OAuth token request.
	InTokenRequest = "token_request"
)

// Place is where an api_token's token goes on every request.
type Place struct {
	In       string `json:"in"`       // header, or query as a last resort
	Name     string `json:"name"`     // "Authorization"
	Template string `json:"template"` // "Bearer {token}"
}

// Field levels: who supplies a field's value.
const (
	// LevelConnection is typed by the member connecting.
	LevelConnection = "connection"
	// LevelClient is typed by an organization admin, once, for the
	// organization's OAuth client.
	LevelClient = "client"
	// LevelDiscovered is read from the token response, at Discover.
	LevelDiscovered = "discovered"
)

// Field is a value a method needs beyond its kind's standard ones: a
// subdomain, a region, an instance URL. A secret field is sealed, write-only
// and masked; any other is stored in the connection's settings.
type Field struct {
	Name   string `json:"name"` // ^[a-z][a-z0-9_]{0,31}$
	Label  string `json:"label"`
	Help   string `json:"help,omitempty"`
	Secret bool   `json:"secret"`
	Level  string `json:"level"`
	// Discover is a JSON pointer into the token response, for a discovered
	// field.
	Discover string     `json:"discover,omitempty"`
	Validate Validation `json:"validate"`
	Use      []Use      `json:"use"`
}

// Value formats a field may be checked against.
const (
	FormatHostname = "hostname"
	FormatHTTPSURL = "https_url"
	FormatUUID     = "uuid"
	FormatEmail    = "email"
)

// Validation is what a field's value must be before anything uses it. A
// field placed in a URL always has a format, a pattern or an enum, so what
// fills a URL has been checked first.
type Validation struct {
	Required bool   `json:"required"`
	Format   string `json:"format,omitempty"`
	// Pattern is RE2, and anchored: it must match the whole value.
	Pattern string   `json:"pattern,omitempty"`
	Enum    []string `json:"enum,omitempty"`
	MaxLen  int      `json:"max_length,omitempty"`
}

// Regexp compiles Pattern anchored, as it is always matched: against the
// whole value. It answers nil when there is no pattern.
//
// The pattern must compile on its own first. Otherwise one such as a)|(b,
// which only compiles once wrapped, would escape the anchors with a
// top-level |.
func (v Validation) Regexp() (*regexp.Regexp, error) {
	if v.Pattern == "" {
		return nil, nil
	}
	if _, err := regexp.Compile(v.Pattern); err != nil {
		return nil, err
	}
	return regexp.Compile(`^(?:` + v.Pattern + `)$`)
}

// Use is one place a field's value goes.
type Use struct {
	In string `json:"in"`
	// Name is the header, query or form parameter's name. A url use has
	// none: it fills the field's own {name} in the manifest's URLs.
	Name string `json:"name,omitempty"`
	// Template shapes the value, holding {value}; the default is "{value}".
	Template string `json:"template,omitempty"`
}

// Test is the call that proves a credential works, and names the account it
// belongs to. Keys and passwords must pass it before they are saved, an OAuth
// connection runs it after the code exchange, and idle connections run it as
// their health check.
//
// It passes when the app answers 2xx and Label resolves to a non-empty value.
// The second half matters: some apps answer 200 to refuse (Slack answers
// {"ok": false}).
type Test struct {
	// Path is a GET under api_base.
	Path string `json:"path"`
	// Label is a JSON pointer to the account's name in the answer.
	Label string `json:"label"`
	// AccountRef is a JSON pointer to the account's stable id. Pushed events
	// are routed by it.
	AccountRef string `json:"account_ref,omitempty"`
	// Headers are fixed headers the call sends, for an app that refuses any
	// request without one (Notion-Version). Never a credential.
	Headers map[string]string `json:"headers,omitempty"`
}
