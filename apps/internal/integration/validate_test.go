package integration

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

// subdomain is the plan's own example of a field placed in a URL. Every
// method of the made-up app below declares it, since api_base holds it.
func subdomain() Field {
	return Field{
		Name: "subdomain", Label: "Acme subdomain", Level: LevelConnection,
		Validate: Validation{Required: true, Pattern: "[a-z0-9][a-z0-9-]{0,62}"},
		Use:      []Use{{In: InURL}},
	}
}

// plain is a field that is only stored: it goes nowhere on a request.
func plain(name string) Field {
	return Field{Name: name, Label: "A value", Level: LevelConnection, Validate: Validation{Required: true}}
}

// goodManifest is a made-up app that uses every part of a manifest the right
// way. Its methods are, in order, oauth2_code, api_token, basic and
// oauth2_client_credentials.
func goodManifest() Manifest {
	return Manifest{
		Toolkit: "acme", Version: 1, Name: "Acme",
		APIBase: "https://{subdomain}.acme.example/api/v2",
		Auth: Auth{
			Methods: []Method{
				{
					Kind: KindOAuth2Code,
					OAuth2: &OAuth2{
						Client:     "acme",
						AuthURL:    "https://{subdomain}.acme.example/oauth/authorize",
						TokenURL:   "https://{subdomain}.acme.example/oauth/token",
						RevokeURL:  "https://{subdomain}.acme.example/oauth/revoke",
						Scopes:     []string{"read", "write"},
						PKCE:       true,
						AuthParams: map[string]string{"prompt": "consent"},
						ClientAuth: ClientAuthHeader,
						Rotating:   true,
					},
					Fields: []Field{
						subdomain(),
						{
							Name: "audience", Label: "API audience", Level: LevelClient,
							Validate: Validation{Required: true, Format: FormatHTTPSURL},
							Use:      []Use{{In: InTokenRequest, Name: "audience"}},
						},
						{
							Name: "account_id", Label: "Account", Level: LevelDiscovered, Discover: "/account/id",
							Validate: Validation{Format: FormatUUID},
							Use:      []Use{{In: InHeader, Name: "X-Acme-Account"}},
						},
					},
				},
				{
					Kind:  KindAPIToken,
					Place: &Place{In: InHeader, Name: "Authorization", Template: "Bearer {token}"},
					Fields: []Field{
						subdomain(),
						{
							Name: "region", Label: "Region", Level: LevelConnection,
							Validate: Validation{Required: true, Enum: []string{"us", "eu"}},
							Use:      []Use{{In: InQuery, Name: "region"}},
						},
						{
							Name: "signing_key", Label: "Signing key", Secret: true, Level: LevelConnection,
							Validate: Validation{Required: true, MaxLen: 128},
							Use:      []Use{{In: InHeader, Name: "X-Acme-Key", Template: "Key {value}"}},
						},
					},
				},
				{Kind: KindBasic, Fields: []Field{subdomain()}},
				{
					Kind: KindOAuth2ClientCredentials,
					OAuth2: &OAuth2{
						TokenURL:    "https://{subdomain}.acme.example/oauth/token",
						RevokeStyle: RevokeNone,
						Scopes:      []string{"read"},
						ClientAuth:  ClientAuthBody,
					},
					Fields: []Field{subdomain()},
				},
			},
			Test: Test{
				Path: "/users/me?fields=name", Label: "/name", AccountRef: "/id",
				Headers: map[string]string{"Acme-Version": "2026-09-01"},
			},
		},
		Webhook: &Webhook{
			Scope: HookScopeConnection,
			Verify: Verify{
				Type:      VerifyHMACSHA256,
				Signature: &Locator{Header: "Webhook-Signature", Prefix: "v1,", Split: " "},
				Encoding:  EncodingBase64,
				Signed:    "{id}.{timestamp}.{body}",
				Timestamp: &Locator{Header: "Webhook-Timestamp", Unit: UnitSeconds},
				Tolerance: 300,
			},
			DeliveryID: Locator{Header: "Webhook-Id"},
		},
		Tools: []ToolSpec{
			{
				Name: "ACME_SEARCH_WIDGETS", Description: "Searches widgets.", ReadOnly: true,
				ReadPOSTs: []string{"/widgets/search", "/widgets/{}/query"}, Scopes: []string{"read"},
				InputSchema: json.RawMessage(`{"type":"object"}`),
			},
			{
				Name: "ACME_DELETE_WIDGET", Description: "Deletes a widget.", Destructive: true,
				Scopes: []string{"write"}, InputSchema: json.RawMessage(`{"type":"object","required":["id"]}`),
			},
		},
		Readers: []ReaderSpec{{
			Name: "widgets", Kinds: []string{"widget", "note"},
			TargetSchema: json.RawMessage(`{"type":"object"}`), Scopes: []string{"read"},
		}},
		Triggers: []TriggerSpec{
			{
				Slug: "ACME_WIDGET_CREATED", Name: "Widget created", Kind: TriggerPush,
				ConfigSchema: json.RawMessage(`{"type":"object"}`), PayloadSchema: json.RawMessage(`{"type":"object"}`),
				Sample: json.RawMessage(`{"id":"w_1"}`), Scopes: []string{"read"},
			},
			{
				Slug: "ACME_WIDGET_CHANGED", Name: "Widget changed", Kind: TriggerPoll,
				ConfigSchema: json.RawMessage(`{}`), PayloadSchema: json.RawMessage(`{"type":"object"}`),
				Scopes: []string{"read"},
			},
		},
	}
}

func TestValidateAcceptsAManifestUsingEveryPart(t *testing.T) {
	t.Parallel()
	if err := goodManifest().Validate(); err != nil {
		t.Fatalf("refused a good manifest: %v", err)
	}
}

// The plan's own examples, and the shapes beside them that are allowed on
// purpose, must all pass.
func TestValidateAcceptsThePlansExamples(t *testing.T) {
	t.Parallel()
	for name, mutate := range map[string]func(*Manifest){
		"slack's signed events with a timestamp": func(m *Manifest) {
			m.Webhook = &Webhook{
				Scope: HookScopeDeployment,
				Verify: Verify{
					Type: VerifyHMACSHA256, Signature: &Locator{Header: "X-Slack-Signature", Prefix: "v0="},
					Encoding: EncodingHex, Signed: "v0:{timestamp}:{body}",
					Timestamp: &Locator{Header: "X-Slack-Request-Timestamp", Unit: UnitSeconds}, Tolerance: 300,
				},
				DeliveryID: Locator{JSON: "/event_id"}, Handshake: HandshakeSlackURLVerification,
			}
		},
		"linear's timestamp in the signed body": func(m *Manifest) {
			m.Webhook = &Webhook{
				Scope: HookScopeConnection,
				Verify: Verify{
					Type: VerifyHMACSHA256, Signature: &Locator{Header: "Linear-Signature"},
					Encoding: EncodingHex, Signed: "{body}",
					Timestamp: &Locator{JSON: "/webhookTimestamp", Unit: UnitMilliseconds}, Tolerance: 60,
				},
				DeliveryID: Locator{Header: "Linear-Delivery"},
			}
		},
		"github's hook with no timestamp, relying on dedupe": func(m *Manifest) {
			m.Webhook = &Webhook{
				Scope: HookScopeConnection,
				Verify: Verify{
					Type: VerifyHMACSHA256, Signature: &Locator{Header: "X-Hub-Signature-256", Prefix: "sha256="},
					Encoding: EncodingHex, Signed: "{body}",
				},
				DeliveryID: Locator{Header: "X-GitHub-Delivery"}, Handshake: HandshakeGitHubPing,
			}
		},
		"gmail's pub/sub push": func(m *Manifest) {
			m.Webhook = &Webhook{
				Scope: HookScopeDeployment,
				Verify: Verify{
					Type: VerifyJWTPush, Issuer: "https://accounts.google.com",
					Audience: "{endpoint_url}", Email: "{config:pubsub_push_account}",
				},
				DeliveryID: Locator{JSON: "/message/messageId"},
			}
		},
		"google drive's channel token": func(m *Manifest) {
			m.Webhook = &Webhook{
				Scope:      HookScopeConnection,
				Verify:     Verify{Type: VerifyStaticToken, Token: &Locator{Header: "X-Goog-Channel-Token"}},
				DeliveryID: Locator{Header: "X-Goog-Message-Number"}, Handshake: HandshakeGoogleChannelSync,
			}
		},
		"outlook's clientState in every notification, and a hashed delivery id": func(m *Manifest) {
			m.Webhook = &Webhook{
				Scope:      HookScopeConnection,
				Verify:     Verify{Type: VerifyStaticToken, Token: &Locator{JSON: "/value/*/clientState"}},
				DeliveryID: Locator{Hash: true}, Handshake: HandshakeGraphValidation,
			}
		},
		"notion's signature, keyed by its verification token": func(m *Manifest) {
			m.Webhook = &Webhook{
				Scope: HookScopeDeployment,
				Verify: Verify{
					Type: VerifyHMACSHA256, Signature: &Locator{Header: "X-Notion-Signature", Prefix: "sha256="},
					Encoding: EncodingHex, Signed: "{body}",
				},
				DeliveryID: Locator{JSON: "/id"}, Handshake: HandshakeNotionVerification,
			}
		},
		"a jwt_push email written out": func(m *Manifest) {
			m.Webhook = &Webhook{
				Scope:      HookScopeDeployment,
				Verify:     Verify{Type: VerifyJWTPush, Issuer: "https://accounts.google.com", Audience: "https://api.oryxa.in/push", Email: "push@oryxa.iam.gserviceaccount.com"},
				DeliveryID: Locator{JSON: "/message/messageId"},
			}
		},
		"an instance URL discovered from the token response": func(m *Manifest) {
			m.APIBase = "{instance_url}/services/data"
			m.Auth.Methods = []Method{{
				Kind: KindOAuth2Code,
				OAuth2: &OAuth2{
					Client: "acme", AuthURL: "https://login.acme.example/authorize",
					TokenURL: "https://login.acme.example/token", RevokeURL: "{instance_url}/revoke",
					Scopes: []string{"api"}, PKCE: true, ClientAuth: ClientAuthBody,
				},
				Fields: []Field{{
					Name: "instance_url", Label: "Instance", Level: LevelDiscovered, Discover: "/instance_url",
					Validate: Validation{Format: FormatHTTPSURL}, Use: []Use{{In: InURL}},
				}},
			}}
		},
		"no webhook, and nothing to run yet": func(m *Manifest) {
			m.Webhook, m.Tools, m.Readers, m.Triggers = nil, nil, nil, nil
		},
		"an app with no scopes at all, as Notion's": func(m *Manifest) {
			m.Auth.Methods[0].OAuth2.Scopes = nil
		},
		"scopes joined with a comma, as Slack's": func(m *Manifest) {
			m.Auth.Methods[0].OAuth2.ScopeSep = ScopeSeparatorComma
		},
		"a style the core added, taking its own URL": func(m *Manifest) {
			m.Auth.Methods[0].OAuth2.RevokeStyle = RevokeSlackAuthRevoke
		},
		"a style the core added, building its own URL": func(m *Manifest) {
			m.Auth.Methods[0].OAuth2.RevokeStyle, m.Auth.Methods[0].OAuth2.RevokeURL = RevokeGitHubToken, ""
		},
		"a token in the query, as a last resort": func(m *Manifest) {
			m.Auth.Methods[1].Place = &Place{In: InQuery, Name: "api_key", Template: "{token}"}
		},
		"oauth2_code declaring a field named like a client's part, since it has no standard fields": func(m *Manifest) {
			m.Auth.Methods[0].Fields = append(m.Auth.Methods[0].Fields, plain("client_id"))
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			manifest := goodManifest()
			mutate(&manifest)
			if err := manifest.Validate(); err != nil {
				t.Fatalf("refused: %v", err)
			}
		})
	}
}

// Each case breaks one rule, and the manifest is otherwise good.
func TestValidateRefusesEachBrokenRule(t *testing.T) {
	t.Parallel()
	for name, testCase := range map[string]struct {
		mutate func(*Manifest)
		// path is where the rule is broken, and says a phrase of the rule.
		path, says string
		// problems is how many problems are found in all; 0 means one.
		problems int
		// leak is the offending value, which the error must never quote.
		leak string
	}{
		// The manifest itself.
		"a toolkit that is not a slug": {
			mutate: func(m *Manifest) { m.Toolkit, m.Tools, m.Triggers = "Acme Corp", nil, nil },
			path:   "toolkit", says: "lowercase slug", leak: "Acme Corp",
		},
		"version zero": {
			mutate: func(m *Manifest) { m.Version = 0 },
			path:   "version", says: "1 or more",
		},
		"no name": {
			mutate: func(m *Manifest) { m.Name = " " },
			path:   "name", says: "required",
		},
		"no api_base": {
			// Nor are the url fields blamed for filling nothing.
			mutate: func(m *Manifest) { m.APIBase = "" },
			path:   "api_base", says: "required",
		},
		"api_base over plain http": {
			mutate: func(m *Manifest) { m.APIBase = "http://{subdomain}.acme.example/api/v2" },
			path:   "api_base", says: "https", leak: "http://",
		},
		"api_base with credentials in it": {
			mutate: func(m *Manifest) { m.APIBase = "https://robot:hunter2@{subdomain}.acme.example/api/v2" },
			path:   "api_base", says: "credentials", leak: "hunter2",
		},
		"api_base with a query": {
			mutate: func(m *Manifest) { m.APIBase = "https://{subdomain}.acme.example/api/v2?key=abc" },
			path:   "api_base", says: "query", leak: "key=abc",
		},
		"api_base with a fragment": {
			mutate: func(m *Manifest) { m.APIBase = "https://{subdomain}.acme.example/api/v2#top" },
			path:   "api_base", says: "fragment",
		},
		"api_base with an unclosed placeholder": {
			// Which placeholders api_base holds is then unknown, so the url
			// fields are not blamed for filling none.
			mutate: func(m *Manifest) { m.APIBase = "https://{subdomain.acme.example/api/v2" },
			path:   "api_base", says: "unbalanced",
		},
		"api_base holding a placeholder no method fills": {
			mutate: func(m *Manifest) { m.APIBase = "https://{subdomain}.{tenant}.acme.example/api/v2" },
			path:   "api_base", says: "names no url field", problems: 4, leak: "tenant",
		},
		"api_base beginning with a field that is not a URL": {
			mutate: func(m *Manifest) { m.APIBase = "{subdomain}/api/v2" },
			path:   "api_base", says: "https_url", problems: 4,
		},

		// Methods.
		"no methods": {
			mutate: func(m *Manifest) { m.Auth.Methods = nil },
			path:   "auth.methods", says: "at least one",
		},
		"two methods of one kind": {
			mutate: func(m *Manifest) {
				m.Auth.Methods = append(m.Auth.Methods, Method{Kind: KindBasic, Fields: []Field{subdomain()}})
			},
			path: "auth.methods[4].kind", says: "repeats",
		},
		"a kind the core does not know": {
			mutate: func(m *Manifest) { m.Auth.Methods[2].Kind = "magic_link" },
			path:   "auth.methods[2].kind", says: "credential kind", leak: "magic_link",
		},
		"api_token declaring its token": {
			mutate: func(m *Manifest) { m.Auth.Methods[1].Fields = append(m.Auth.Methods[1].Fields, plain("token")) },
			path:   "auth.methods[1].fields[3].name", says: "standard fields",
		},
		"basic declaring its password": {
			mutate: func(m *Manifest) { m.Auth.Methods[2].Fields = append(m.Auth.Methods[2].Fields, plain("password")) },
			path:   "auth.methods[2].fields[1].name", says: "standard fields",
		},
		"client credentials declaring the client secret": {
			mutate: func(m *Manifest) {
				m.Auth.Methods[3].Fields = append(m.Auth.Methods[3].Fields, plain("client_secret"))
			},
			path: "auth.methods[3].fields[1].name", says: "standard fields",
		},
		"oauth2_code without its oauth2 block": {
			mutate: func(m *Manifest) { m.Auth.Methods[0].OAuth2 = nil },
			path:   "auth.methods[0].oauth2", says: "required",
		},
		"an oauth2 block on api_token": {
			mutate: func(m *Manifest) {
				m.Auth.Methods[1].OAuth2 = &OAuth2{
					TokenURL: "https://acme.example/token", RevokeStyle: RevokeNone, ClientAuth: ClientAuthBody,
				}
			},
			path: "auth.methods[1].oauth2", says: "only for the oauth2 kinds",
		},
		"api_token with nowhere to place its token": {
			mutate: func(m *Manifest) { m.Auth.Methods[1].Place = nil },
			path:   "auth.methods[1].place", says: "required",
		},
		"a place on basic": {
			mutate: func(m *Manifest) {
				m.Auth.Methods[2].Place = &Place{In: InHeader, Name: "X-Key", Template: "{token}"}
			},
			path: "auth.methods[2].place", says: "only for api_token",
		},

		// OAuth2.
		"oauth2_code without a client": {
			mutate: func(m *Manifest) { m.Auth.Methods[0].OAuth2.Client = "" },
			path:   "auth.methods[0].oauth2.client", says: "lowercase slug",
		},
		"a client that is not a slug": {
			mutate: func(m *Manifest) { m.Auth.Methods[0].OAuth2.Client = "Acme Client" },
			path:   "auth.methods[0].oauth2.client", says: "lowercase slug", leak: "Acme Client",
		},
		"oauth2_code without an auth_url": {
			mutate: func(m *Manifest) { m.Auth.Methods[0].OAuth2.AuthURL = "" },
			path:   "auth.methods[0].oauth2.auth_url", says: "required",
		},
		"no token_url": {
			mutate: func(m *Manifest) { m.Auth.Methods[0].OAuth2.TokenURL = "" },
			path:   "auth.methods[0].oauth2.token_url", says: "required",
		},
		"an auth_url with a fragment": {
			mutate: func(m *Manifest) {
				m.Auth.Methods[0].OAuth2.AuthURL = "https://{subdomain}.acme.example/oauth/authorize#consent"
			},
			path: "auth.methods[0].oauth2.auth_url", says: "fragment",
		},
		"a token_url over plain http": {
			mutate: func(m *Manifest) {
				m.Auth.Methods[0].OAuth2.TokenURL = "http://{subdomain}.acme.example/oauth/token"
			},
			path: "auth.methods[0].oauth2.token_url", says: "https",
		},
		"an OAuth URL holding a field the method lacks": {
			mutate: func(m *Manifest) {
				m.Auth.Methods[0].OAuth2.RevokeURL = "https://{tenant}.acme.example/oauth/revoke"
			},
			path: "auth.methods[0].oauth2.revoke_url", says: "names no url field", leak: "tenant",
		},
		"a token_url needing a field only the token response discovers": {
			mutate: func(m *Manifest) {
				m.Auth.Methods[0].Fields[2].Use = []Use{{In: InURL}}
				m.Auth.Methods[0].OAuth2.TokenURL = "https://{subdomain}.acme.example/oauth/token/{account_id}"
			},
			path: "auth.methods[0].oauth2.token_url", says: "discovered",
		},
		"a revoke style the core does not know": {
			mutate: func(m *Manifest) { m.Auth.Methods[0].OAuth2.RevokeStyle = "github_grant" },
			path:   "auth.methods[0].oauth2.revoke_style", says: "revoke style", leak: "github_grant",
		},
		"rfc7009, the default, with nowhere to revoke": {
			mutate: func(m *Manifest) { m.Auth.Methods[0].OAuth2.RevokeURL = "" },
			path:   "auth.methods[0].oauth2.revoke_url", says: "required by this revoke style",
		},
		"none with a revoke_url anyway": {
			mutate: func(m *Manifest) { m.Auth.Methods[0].OAuth2.RevokeStyle = RevokeNone },
			path:   "auth.methods[0].oauth2.revoke_url", says: "must be empty",
		},
		"github_token with a revoke_url it would not use": {
			mutate: func(m *Manifest) { m.Auth.Methods[0].OAuth2.RevokeStyle = RevokeGitHubToken },
			path:   "auth.methods[0].oauth2.revoke_url", says: "must be empty",
		},
		"an empty scope": {
			mutate: func(m *Manifest) { m.Auth.Methods[0].OAuth2.Scopes = []string{"read", ""} },
			path:   "auth.methods[0].oauth2.scopes[1]", says: "one scope",
		},
		"two scopes in one": {
			mutate: func(m *Manifest) { m.Auth.Methods[0].OAuth2.Scopes = []string{"read write"} },
			path:   "auth.methods[0].oauth2.scopes[0]", says: "one scope", leak: "read write",
		},
		"a scope listed twice": {
			mutate: func(m *Manifest) { m.Auth.Methods[0].OAuth2.Scopes = []string{"read", "read"} },
			path:   "auth.methods[0].oauth2.scopes[1]", says: "repeats",
		},
		"a scope separator the core does not know": {
			mutate: func(m *Manifest) { m.Auth.Methods[0].OAuth2.ScopeSep = ";" },
			path:   "auth.methods[0].oauth2.scope_separator", says: "space or a comma",
		},
		"no client_auth": {
			mutate: func(m *Manifest) { m.Auth.Methods[0].OAuth2.ClientAuth = "" },
			path:   "auth.methods[0].oauth2.client_auth", says: "header or body",
		},
		"auth_params fixing the state": {
			mutate: func(m *Manifest) { m.Auth.Methods[0].OAuth2.AuthParams["state"] = "fixed-state-value" },
			path:   "auth.methods[0].oauth2.auth_params", says: "own parameters", leak: "fixed-state-value",
		},
		"auth_params redirecting elsewhere": {
			mutate: func(m *Manifest) { m.Auth.Methods[0].OAuth2.AuthParams["redirect_uri"] = "https://evil.example/cb" },
			path:   "auth.methods[0].oauth2.auth_params", says: "own parameters", leak: "evil.example",
		},

		// Place.
		"a token placed in a cookie": {
			mutate: func(m *Manifest) { m.Auth.Methods[1].Place.In = "cookie" },
			path:   "auth.methods[1].place.in", says: "header or query", leak: "cookie",
		},
		"a token placed under no name": {
			mutate: func(m *Manifest) { m.Auth.Methods[1].Place.Name = "" },
			path:   "auth.methods[1].place.name", says: "header name",
		},
		"a place template without the token": {
			mutate: func(m *Manifest) { m.Auth.Methods[1].Place.Template = "Bearer" },
			path:   "auth.methods[1].place.template", says: "{token}",
		},
		"a place template with another placeholder": {
			mutate: func(m *Manifest) { m.Auth.Methods[1].Place.Template = "Bearer {token} {value}" },
			path:   "auth.methods[1].place.template", says: "no other placeholder",
		},

		// Fields.
		"a field name with a capital": {
			mutate: func(m *Manifest) { m.Auth.Methods[1].Fields = append(m.Auth.Methods[1].Fields, plain("Region")) },
			path:   "auth.methods[1].fields[3].name", says: "must match", leak: "Region",
		},
		"a field name used twice": {
			mutate: func(m *Manifest) { m.Auth.Methods[1].Fields = append(m.Auth.Methods[1].Fields, plain("region")) },
			path:   "auth.methods[1].fields[3].name", says: "repeats",
		},
		"a field with no label": {
			mutate: func(m *Manifest) {
				field := plain("note")
				field.Label = ""
				m.Auth.Methods[1].Fields = append(m.Auth.Methods[1].Fields, field)
			},
			path: "auth.methods[1].fields[3].label", says: "required",
		},
		"a level the core does not know": {
			mutate: func(m *Manifest) { m.Auth.Methods[1].Fields[1].Level = "member" },
			path:   "auth.methods[1].fields[1].level", says: "connection, client or discovered", leak: "member",
		},
		"discover on a field someone types": {
			mutate: func(m *Manifest) { m.Auth.Methods[1].Fields[1].Discover = "/region" },
			path:   "auth.methods[1].fields[1].discover", says: "only for a discovered field",
		},
		"a discovered field with nowhere to discover it": {
			mutate: func(m *Manifest) {
				m.Auth.Methods[0].Fields = append(m.Auth.Methods[0].Fields,
					Field{Name: "tenant", Label: "Tenant", Level: LevelDiscovered})
			},
			path: "auth.methods[0].fields[3].discover", says: "JSON pointer",
		},
		"discover that is not a JSON pointer": {
			mutate: func(m *Manifest) { m.Auth.Methods[0].Fields[2].Discover = "account_id" },
			path:   "auth.methods[0].fields[2].discover", says: "JSON pointer",
		},
		"a discovered field on a kind with no token response": {
			mutate: func(m *Manifest) {
				m.Auth.Methods[1].Fields = append(m.Auth.Methods[1].Fields,
					Field{Name: "tenant", Label: "Tenant", Level: LevelDiscovered, Discover: "/tenant"})
			},
			path: "auth.methods[1].fields[3].level", says: "no token response",
		},
		"a format the core does not know": {
			mutate: func(m *Manifest) { m.Auth.Methods[1].Fields[1].Validate.Format = "ipv4" },
			path:   "auth.methods[1].fields[1].validate.format", says: "hostname", leak: "ipv4",
		},
		"a pattern RE2 cannot compile": {
			mutate: func(m *Manifest) { m.Auth.Methods[1].Fields[1].Validate.Pattern = "(unclosed[" },
			path:   "auth.methods[1].fields[1].validate.pattern", says: "RE2", leak: "(unclosed[",
		},
		"a pattern that would escape its anchors": {
			// ^(?:a)|(b)$ compiles, and matches anything beginning with a.
			mutate: func(m *Manifest) { m.Auth.Methods[1].Fields[1].Validate.Pattern = "us)|(.*" },
			path:   "auth.methods[1].fields[1].validate.pattern", says: "RE2",
		},
		"a negative max_length": {
			mutate: func(m *Manifest) { m.Auth.Methods[1].Fields[2].Validate.MaxLen = -1 },
			path:   "auth.methods[1].fields[2].validate.max_length", says: "negative",
		},
		"an empty enum value": {
			mutate: func(m *Manifest) { m.Auth.Methods[1].Fields[1].Validate.Enum = []string{"us", ""} },
			path:   "auth.methods[1].fields[1].validate.enum[1]", says: "empty",
		},
		"an enum value listed twice": {
			mutate: func(m *Manifest) { m.Auth.Methods[1].Fields[1].Validate.Enum = []string{"us", "us"} },
			path:   "auth.methods[1].fields[1].validate.enum[1]", says: "repeats",
		},
		"a use the core does not know": {
			mutate: func(m *Manifest) { m.Auth.Methods[1].Fields[1].Use = []Use{{In: "cookie", Name: "session"}} },
			path:   "auth.methods[1].fields[1].use[0].in", says: "header, query, url or token_request",
		},
		"a field placed in Authorization": {
			mutate: func(m *Manifest) { m.Auth.Methods[1].Fields[2].Use[0].Name = "authorization" },
			path:   "auth.methods[1].fields[2].use[0].name", says: "only the core sets",
		},
		"a header name with a space": {
			mutate: func(m *Manifest) { m.Auth.Methods[1].Fields[2].Use[0].Name = "X Acme Key" },
			path:   "auth.methods[1].fields[2].use[0].name", says: "header name", leak: "X Acme Key",
		},
		"a query use with no name": {
			mutate: func(m *Manifest) { m.Auth.Methods[1].Fields[1].Use[0].Name = "" },
			path:   "auth.methods[1].fields[1].use[0].name", says: "query parameter",
		},
		"a token_request use setting the flow's own parameter": {
			mutate: func(m *Manifest) { m.Auth.Methods[0].Fields[1].Use[0].Name = "redirect_uri" },
			path:   "auth.methods[0].fields[1].use[0].name", says: "token request's own",
		},
		"a token_request use on a kind with no token request": {
			mutate: func(m *Manifest) {
				m.Auth.Methods[1].Fields = append(m.Auth.Methods[1].Fields, Field{
					Name: "audience", Label: "Audience", Level: LevelConnection,
					Use: []Use{{In: InTokenRequest, Name: "audience"}},
				})
			},
			path: "auth.methods[1].fields[3].use[0].in", says: "no token request",
		},
		"a url use with a name of its own": {
			mutate: func(m *Manifest) { m.Auth.Methods[2].Fields[0].Use[0].Name = "host" },
			path:   "auth.methods[2].fields[0].use[0].name", says: "must be empty",
		},
		"a secret placed in a URL": {
			mutate: func(m *Manifest) { m.Auth.Methods[2].Fields[0].Secret = true },
			path:   "auth.methods[2].fields[0].use[0].in", says: "secret",
		},
		"a URL filled by a field nothing checks": {
			mutate: func(m *Manifest) { m.Auth.Methods[2].Fields[0].Validate = Validation{Required: true} },
			path:   "auth.methods[2].fields[0].validate", says: "format, a pattern or an enum",
		},
		"a url field no URL holds": {
			mutate: func(m *Manifest) {
				m.Auth.Methods[1].Fields = append(m.Auth.Methods[1].Fields, Field{
					Name: "tenant", Label: "Tenant", Level: LevelConnection,
					Validate: Validation{Pattern: "[a-z]+"}, Use: []Use{{In: InURL}},
				})
			},
			path: "auth.methods[1].fields[3].use[0]", says: "none of the manifest's URLs",
		},
		"a use template without its value": {
			mutate: func(m *Manifest) { m.Auth.Methods[1].Fields[2].Use[0].Template = "Key" },
			path:   "auth.methods[1].fields[2].use[0].template", says: "{value}",
		},

		// The test call.
		"no test path": {
			mutate: func(m *Manifest) { m.Auth.Test.Path = "" },
			path:   "auth.test.path", says: "required",
		},
		"a test path to another host": {
			mutate: func(m *Manifest) { m.Auth.Test.Path = "//evil.example/users/me" },
			path:   "auth.test.path", says: "single /", leak: "evil.example",
		},
		"a test path climbing out of api_base": {
			mutate: func(m *Manifest) { m.Auth.Test.Path = "/../admin" },
			path:   "auth.test.path", says: "dot segments",
		},
		"a test path climbing out in disguise": {
			mutate: func(m *Manifest) { m.Auth.Test.Path = "/%2e%2e/admin" },
			path:   "auth.test.path", says: "dot segments",
		},
		"a relative test path": {
			mutate: func(m *Manifest) { m.Auth.Test.Path = "users/me" },
			path:   "auth.test.path", says: "single /",
		},
		"a test path holding a field no method fills from a URL": {
			mutate: func(m *Manifest) { m.Auth.Test.Path = "/users/{account_id}" },
			path:   "auth.test.path", says: "names no url field", problems: 4,
		},
		"no label": {
			mutate: func(m *Manifest) { m.Auth.Test.Label = "" },
			path:   "auth.test.label", says: "required",
		},
		"a label that is not a JSON pointer": {
			mutate: func(m *Manifest) { m.Auth.Test.Label = "name" },
			path:   "auth.test.label", says: "JSON pointer",
		},
		"an account_ref that is not a JSON pointer": {
			mutate: func(m *Manifest) { m.Auth.Test.AccountRef = "id" },
			path:   "auth.test.account_ref", says: "JSON pointer",
		},
		"a test header carrying a credential": {
			mutate: func(m *Manifest) { m.Auth.Test.Headers["Authorization"] = "Bearer abc123" },
			path:   "auth.test.headers", says: "other than Authorization", leak: "abc123",
		},
		"a test header holding a placeholder": {
			mutate: func(m *Manifest) { m.Auth.Test.Headers["Acme-Version"] = "{version}" },
			path:   "auth.test.headers", says: "fixed",
		},

		// The webhook.
		"a webhook scope the core does not know": {
			mutate: func(m *Manifest) { m.Webhook.Scope = "member" },
			path:   "webhook.scope", says: "connection, organization or deployment",
		},
		"a scheme the core does not know": {
			mutate: func(m *Manifest) { m.Webhook.Verify.Type = "hmac_sha1" },
			path:   "webhook.verify.type", says: "verification scheme", leak: "hmac_sha1",
		},
		"hmac with no signature to check": {
			mutate: func(m *Manifest) { m.Webhook.Verify.Signature = nil },
			path:   "webhook.verify.signature", says: "required",
		},
		"an encoding the core does not know": {
			mutate: func(m *Manifest) { m.Webhook.Verify.Encoding = "base32" },
			path:   "webhook.verify.encoding", says: "hex or base64", leak: "base32",
		},
		"hmac with nothing signed": {
			mutate: func(m *Manifest) { m.Webhook.Verify.Signed = "" },
			path:   "webhook.verify.signed", says: "required",
		},
		"a signed template with a placeholder the core does not fill": {
			mutate: func(m *Manifest) { m.Webhook.Verify.Signed = "{id}.{timestamp}.{nonce}.{body}" },
			path:   "webhook.verify.signed", says: "only hold", leak: "nonce",
		},
		"a signature that leaves the body out": {
			mutate: func(m *Manifest) { m.Webhook.Verify.Signed = "{id}.{timestamp}" },
			path:   "webhook.verify.signed", says: "{body}",
		},
		"a signed timestamp with nowhere to read it": {
			mutate: func(m *Manifest) { m.Webhook.Verify.Timestamp, m.Webhook.Verify.Tolerance = nil, 0 },
			path:   "webhook.verify.signed", says: "timestamp locator",
		},
		"a header timestamp the signature leaves out": {
			mutate: func(m *Manifest) { m.Webhook.Verify.Signed = "{id}.{body}" },
			path:   "webhook.verify.timestamp", says: "must cover it",
		},
		"a signed id with no delivery id to read it from": {
			mutate: func(m *Manifest) { m.Webhook.DeliveryID = Locator{Hash: true} },
			path:   "webhook.verify.signed", says: "delivery_id read from a header or the body",
		},
		"a negative tolerance": {
			mutate: func(m *Manifest) { m.Webhook.Verify.Tolerance = -5 },
			path:   "webhook.verify.tolerance_seconds", says: "negative",
		},
		"a tolerance with no timestamp": {
			mutate: func(m *Manifest) { m.Webhook.Verify.Signed, m.Webhook.Verify.Timestamp = "{id}.{body}", nil },
			path:   "webhook.verify.tolerance_seconds", says: "reads a timestamp",
		},
		"a timestamp with no unit": {
			mutate: func(m *Manifest) { m.Webhook.Verify.Timestamp.Unit = "" },
			path:   "webhook.verify.timestamp.unit", says: "s or ms",
		},
		"a field the scheme does not read": {
			mutate: func(m *Manifest) { m.Webhook.Verify.Token = &Locator{Header: "X-Token"} },
			path:   "webhook.verify.token", says: "not read by this scheme",
		},
		"static_token with no token to check": {
			mutate: func(m *Manifest) {
				m.Webhook = &Webhook{
					Scope: HookScopeConnection, Verify: Verify{Type: VerifyStaticToken},
					DeliveryID: Locator{Header: "X-Goog-Message-Number"},
				}
			},
			path: "webhook.verify.token", says: "required",
		},
		"jwt_push with no issuer": {
			mutate: func(m *Manifest) {
				m.Webhook = &Webhook{
					Scope: HookScopeDeployment, Verify: Verify{Type: VerifyJWTPush, Audience: "{endpoint_url}"},
					DeliveryID: Locator{JSON: "/message/messageId"},
				}
			},
			path: "webhook.verify.issuer", says: "required",
		},
		"jwt_push trusting an issuer over plain http": {
			mutate: func(m *Manifest) {
				m.Webhook = &Webhook{
					Scope:      HookScopeDeployment,
					Verify:     Verify{Type: VerifyJWTPush, Issuer: "http://accounts.google.com", Audience: "{endpoint_url}"},
					DeliveryID: Locator{JSON: "/message/messageId"},
				}
			},
			path: "webhook.verify.issuer", says: "https", leak: "accounts.google.com",
		},
		"jwt_push with no audience": {
			mutate: func(m *Manifest) {
				m.Webhook = &Webhook{
					Scope: HookScopeDeployment, Verify: Verify{Type: VerifyJWTPush, Issuer: "https://accounts.google.com"},
					DeliveryID: Locator{JSON: "/message/messageId"},
				}
			},
			path: "webhook.verify.audience", says: "required",
		},
		"an audience with a placeholder the core does not fill": {
			mutate: func(m *Manifest) {
				m.Webhook = &Webhook{
					Scope: HookScopeDeployment,
					Verify: Verify{
						Type: VerifyJWTPush, Issuer: "https://accounts.google.com", Audience: "{endpoint_url}/{tenant}",
					},
					DeliveryID: Locator{JSON: "/message/messageId"},
				}
			},
			path: "webhook.verify.audience", says: "{endpoint_url}", leak: "tenant",
		},
		"an email that is neither an address nor configuration": {
			mutate: func(m *Manifest) {
				m.Webhook = &Webhook{
					Scope: HookScopeDeployment,
					Verify: Verify{
						Type: VerifyJWTPush, Issuer: "https://accounts.google.com", Audience: "{endpoint_url}",
						Email: "pubsub-push",
					},
					DeliveryID: Locator{JSON: "/message/messageId"},
				}
			},
			path: "webhook.verify.email", says: "email address", leak: "pubsub-push",
		},
		"an email from configuration, spelt wrong": {
			mutate: func(m *Manifest) {
				m.Webhook = &Webhook{
					Scope: HookScopeDeployment,
					Verify: Verify{
						Type: VerifyJWTPush, Issuer: "https://accounts.google.com", Audience: "{endpoint_url}",
						Email: "{env:PUSH_ACCOUNT}",
					},
					DeliveryID: Locator{JSON: "/message/messageId"},
				}
			},
			path: "webhook.verify.email", says: "{config:name}", leak: "PUSH_ACCOUNT",
		},
		"a handshake the core does not know": {
			mutate: func(m *Manifest) { m.Webhook.Handshake = "stripe_ping" },
			path:   "webhook.handshake", says: "handshake the core knows", leak: "stripe_ping",
		},
		"a handshake of another scheme": {
			mutate: func(m *Manifest) { m.Webhook.Handshake = HandshakeGoogleChannelSync },
			path:   "webhook.handshake", says: "another verification scheme",
		},
		"a delivery id with nothing to read": {
			mutate: func(m *Manifest) {
				m.Webhook.DeliveryID, m.Webhook.Verify.Signed = Locator{}, "{timestamp}.{body}"
			},
			path: "webhook.delivery_id", says: "exactly one",
		},
		"a delivery id read two ways": {
			mutate: func(m *Manifest) { m.Webhook.DeliveryID = Locator{Header: "Webhook-Id", JSON: "/id"} },
			path:   "webhook.delivery_id", says: "exactly one",
		},
		"a delivery id pointer that is not one": {
			mutate: func(m *Manifest) { m.Webhook.DeliveryID = Locator{JSON: "id"} },
			path:   "webhook.delivery_id.json", says: "JSON pointer",
		},
		"a wildcard outside a token": {
			mutate: func(m *Manifest) { m.Webhook.DeliveryID = Locator{JSON: "/items/*/id"} },
			path:   "webhook.delivery_id.json", says: "only a token locator",
		},
		"a pointer with a broken escape": {
			mutate: func(m *Manifest) { m.Webhook.DeliveryID = Locator{JSON: "/a~2b"} },
			path:   "webhook.delivery_id.json", says: "JSON pointer",
		},
		"a hash standing for a signature": {
			mutate: func(m *Manifest) { m.Webhook.Verify.Signature = &Locator{Hash: true} },
			path:   "webhook.verify.signature.hash", says: "only for a delivery id",
		},
		"a split outside a signature": {
			mutate: func(m *Manifest) { m.Webhook.DeliveryID = Locator{Header: "Webhook-Id", Split: ","} },
			path:   "webhook.delivery_id.split", says: "only for a signature",
		},
		"a unit outside a timestamp": {
			mutate: func(m *Manifest) { m.Webhook.Verify.Signature.Unit = UnitSeconds },
			path:   "webhook.verify.signature.unit", says: "only for a timestamp",
		},
		"a locator header with a space": {
			mutate: func(m *Manifest) { m.Webhook.DeliveryID = Locator{Header: "Webhook Id"} },
			path:   "webhook.delivery_id.header", says: "header name",
		},
		"a prefix on a hash": {
			mutate: func(m *Manifest) {
				m.Webhook.DeliveryID, m.Webhook.Verify.Signed = Locator{Hash: true, Prefix: "x"}, "{timestamp}.{body}"
			},
			path: "webhook.delivery_id.prefix", says: "hash",
		},

		// Tools.
		"a tool without the toolkit's prefix": {
			mutate: func(m *Manifest) { m.Tools[0].Name = "WIDGETS_SEARCH" },
			path:   "tools[0].name", says: "prefix", leak: "WIDGETS_SEARCH",
		},
		"a tool in lowercase": {
			mutate: func(m *Manifest) { m.Tools[0].Name = "acme_search_widgets" },
			path:   "tools[0].name", says: "uppercase slug",
		},
		"a tool that is only the prefix": {
			mutate: func(m *Manifest) { m.Tools[0].Name = "ACME_" },
			path:   "tools[0].name", says: "prefix",
		},
		"two tools of one name": {
			mutate: func(m *Manifest) { m.Tools[1].Name = m.Tools[0].Name },
			path:   "tools[1].name", says: "repeats",
		},
		"a tool with no description": {
			mutate: func(m *Manifest) { m.Tools[0].Description = "" },
			path:   "tools[0].description", says: "required",
		},
		"a read tool marked destructive": {
			mutate: func(m *Manifest) { m.Tools[0].Destructive = true },
			path:   "tools[0].destructive", says: "read tool",
		},
		"read_posts on a write tool": {
			mutate: func(m *Manifest) { m.Tools[1].ReadPOSTs = []string{"/widgets/search"} },
			path:   "tools[1].read_posts", says: "only for read tools",
		},
		"a read_post that is not a path": {
			mutate: func(m *Manifest) { m.Tools[0].ReadPOSTs[0] = "widgets/search" },
			path:   "tools[0].read_posts[0]", says: "single /",
		},
		"a read_post on another host": {
			mutate: func(m *Manifest) { m.Tools[0].ReadPOSTs[0] = "//evil.example/search" },
			path:   "tools[0].read_posts[0]", says: "single /", leak: "evil.example",
		},
		"a read_post with a query": {
			mutate: func(m *Manifest) { m.Tools[0].ReadPOSTs[0] = "/widgets/search?q=all" },
			path:   "tools[0].read_posts[0]", says: "query",
		},
		"a read_post naming its placeholder": {
			mutate: func(m *Manifest) { m.Tools[0].ReadPOSTs[1] = "/widgets/{id}/query" },
			path:   "tools[0].read_posts[1]", says: "whole segment",
		},
		"a read_post climbing out of api_base": {
			mutate: func(m *Manifest) { m.Tools[0].ReadPOSTs[0] = "/widgets/../admin" },
			path:   "tools[0].read_posts[0]", says: "dot segments",
		},
		"an empty tool scope": {
			mutate: func(m *Manifest) { m.Tools[0].Scopes = []string{""} },
			path:   "tools[0].scopes[0]", says: "one scope",
		},
		"a tool with no input schema": {
			mutate: func(m *Manifest) { m.Tools[0].InputSchema = nil },
			path:   "tools[0].input_schema", says: "required",
		},
		"an input schema that is not JSON": {
			mutate: func(m *Manifest) { m.Tools[0].InputSchema = json.RawMessage(`{"type":`) },
			path:   "tools[0].input_schema", says: "valid JSON",
		},
		"an input schema that is not an object": {
			mutate: func(m *Manifest) { m.Tools[0].InputSchema = json.RawMessage(`["object"]`) },
			path:   "tools[0].input_schema", says: "an object",
		},

		// Readers.
		"a reader name with a capital": {
			mutate: func(m *Manifest) { m.Readers[0].Name = "Widgets" },
			path:   "readers[0].name", says: "must match",
		},
		"two readers of one name": {
			mutate: func(m *Manifest) { m.Readers = append(m.Readers, m.Readers[0]) },
			path:   "readers[1].name", says: "repeats",
		},
		"a reader of no kind": {
			mutate: func(m *Manifest) { m.Readers[0].Kinds = nil },
			path:   "readers[0].kinds", says: "at least one",
		},
		"a kind listed twice": {
			mutate: func(m *Manifest) { m.Readers[0].Kinds = []string{"widget", "widget"} },
			path:   "readers[0].kinds[1]", says: "repeats",
		},
		"a reader with no target schema": {
			mutate: func(m *Manifest) { m.Readers[0].TargetSchema = nil },
			path:   "readers[0].target_schema", says: "required",
		},

		// Triggers.
		"a trigger without the toolkit's prefix": {
			mutate: func(m *Manifest) { m.Triggers[0].Slug = "WIDGET_CREATED" },
			path:   "triggers[0].slug", says: "prefix", leak: "WIDGET_CREATED",
		},
		"two triggers of one slug": {
			mutate: func(m *Manifest) { m.Triggers[1].Slug = m.Triggers[0].Slug },
			path:   "triggers[1].slug", says: "repeats",
		},
		"a trigger with no name": {
			mutate: func(m *Manifest) { m.Triggers[0].Name = "" },
			path:   "triggers[0].name", says: "required",
		},
		"a trigger kind the core does not know": {
			mutate: func(m *Manifest) { m.Triggers[0].Kind = "webhook" },
			path:   "triggers[0].kind", says: "poll or push",
		},
		"a push trigger with no webhook to arrive on": {
			mutate: func(m *Manifest) { m.Webhook = nil },
			path:   "triggers[0].kind", says: "needs the manifest's webhook",
		},
		"a trigger config schema that is not JSON": {
			mutate: func(m *Manifest) { m.Triggers[0].ConfigSchema = json.RawMessage(`{"type"`) },
			path:   "triggers[0].config_schema", says: "valid JSON",
		},
		"a trigger with no payload schema": {
			mutate: func(m *Manifest) { m.Triggers[0].PayloadSchema = nil },
			path:   "triggers[0].payload_schema", says: "required",
		},
		"a sample that is not JSON": {
			mutate: func(m *Manifest) { m.Triggers[0].Sample = json.RawMessage(`{"id":`) },
			path:   "triggers[0].sample", says: "valid JSON",
		},
		"a trigger scope with a space in it": {
			mutate: func(m *Manifest) { m.Triggers[0].Scopes = []string{"read all"} },
			path:   "triggers[0].scopes[0]", says: "one scope",
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			manifest := goodManifest()
			testCase.mutate(&manifest)
			err := manifest.Validate()
			var invalid *ValidationError
			if !errors.As(err, &invalid) || !errors.Is(err, ErrInvalidManifest) {
				t.Fatalf("error = %v, want a ValidationError", err)
			}
			want := max(testCase.problems, 1)
			if len(invalid.Problems) != want {
				t.Errorf("found %d problems, want %d: %v", len(invalid.Problems), want, err)
			}
			found := false
			for _, problem := range invalid.Problems {
				found = found || problem.Path == testCase.path && strings.Contains(problem.Rule, testCase.says)
			}
			if !found {
				t.Errorf("no problem at %s saying %q: %v", testCase.path, testCase.says, err)
			}
			if testCase.leak != "" && strings.Contains(err.Error(), testCase.leak) {
				t.Errorf("the error quotes the value: %v", err)
			}
		})
	}
}

func TestValidateNamesEveryProblemAtOnce(t *testing.T) {
	t.Parallel()
	manifest := goodManifest()
	manifest.Version = 0
	manifest.Auth.Methods[1].Place = nil
	manifest.Webhook.Verify.Encoding = "base32"
	manifest.Tools[0].Description = ""
	err := manifest.Validate()
	for _, path := range []string{"version", "auth.methods[1].place", "webhook.verify.encoding", "tools[0].description"} {
		if err == nil || !strings.Contains(err.Error(), path+": ") {
			t.Errorf("the error does not name %s: %v", path, err)
		}
	}
}

func TestValidationPatternMatchesTheWholeValue(t *testing.T) {
	t.Parallel()
	pattern, err := Validation{Pattern: "[a-z0-9][a-z0-9-]{0,62}|admin"}.Regexp()
	if err != nil {
		t.Fatal(err)
	}
	for value, want := range map[string]bool{
		"acme": true, "acme-eu": true, "admin": true,
		"acme.evil.example": false, "-acme": false, "acme/../x": false, "not-admin!": false,
	} {
		if got := pattern.MatchString(value); got != want {
			t.Errorf("match(%q) = %v, want %v", value, got, want)
		}
	}
	if pattern, err := (Validation{}).Regexp(); pattern != nil || err != nil {
		t.Fatalf("no pattern compiled to %v, %v", pattern, err)
	}
}

func TestEffectiveToleranceDefaultsOnlyWithATimestamp(t *testing.T) {
	t.Parallel()
	for name, testCase := range map[string]struct {
		verify Verify
		want   time.Duration
	}{
		"no timestamp, nothing checked": {Verify{Type: VerifyHMACSHA256}, 0},
		"a timestamp and no tolerance":  {Verify{Timestamp: &Locator{Header: "X-Time", Unit: UnitSeconds}}, 300 * time.Second},
		"a timestamp and a tolerance":   {Verify{Timestamp: &Locator{JSON: "/at", Unit: UnitMilliseconds}, Tolerance: 60}, time.Minute},
	} {
		if got := testCase.verify.EffectiveTolerance(); got != testCase.want {
			t.Errorf("%s: tolerance = %s, want %s", name, got, testCase.want)
		}
	}
}
