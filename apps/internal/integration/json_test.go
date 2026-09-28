package integration

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"
)

// The JSON names are the contract other packages and the web app read, so
// every one is pinned here as the plan spells it, rather than as the code
// happens to.

var at = time.Date(2026, 9, 28, 10, 0, 0, 0, time.UTC)

// everyField sets every field of a manifest, so every name is encoded. It is
// not a valid manifest, and does not need to be.
func everyField() Manifest {
	return Manifest{
		Toolkit: "acme", Version: 2, Name: "Acme", APIBase: "https://{subdomain}.acme.example",
		Auth: Auth{
			Methods: []Method{{
				Kind: KindOAuth2Code,
				OAuth2: &OAuth2{
					Client: "acme", AuthURL: "https://a.example/auth", TokenURL: "https://a.example/token",
					RevokeURL: "https://a.example/revoke", RevokeStyle: RevokeRFC7009, Scopes: []string{"read"},
					ScopeSep: ",", PKCE: true, AuthParams: map[string]string{"prompt": "consent"},
					ClientAuth: ClientAuthBody, Rotating: true,
				},
				Place: &Place{In: InHeader, Name: "Authorization", Template: "Bearer {token}"},
				Fields: []Field{{
					Name: "subdomain", Label: "Subdomain", Help: "Before .acme.example", Secret: true,
					Level: LevelDiscovered, Discover: "/subdomain",
					Validate: Validation{Required: true, Format: FormatHostname, Pattern: "[a-z]+", Enum: []string{"a"}, MaxLen: 63},
					Use:      []Use{{In: InHeader, Name: "X-Sub", Template: "{value}"}},
				}},
			}},
			Test: Test{Path: "/me", Label: "/name", AccountRef: "/id", Headers: map[string]string{"Acme-Version": "1"}},
		},
		Webhook: &Webhook{
			Scope: HookScopeConnection,
			Verify: Verify{
				Type:      VerifyHMACSHA256,
				Signature: &Locator{Header: "X-Sig", JSON: "/sig", Hash: true, Prefix: "v1=", Split: ",", Unit: UnitSeconds},
				Encoding:  EncodingHex, Signed: "{body}",
				Timestamp: &Locator{Header: "X-Time", Unit: UnitMilliseconds}, Tolerance: 60,
				Token: &Locator{JSON: "/token"}, Issuer: "https://issuer.example",
				Audience: "{endpoint_url}", Email: "a@b.example",
			},
			DeliveryID: Locator{Header: "X-Id"}, Handshake: HandshakeGitHubPing,
		},
		Tools: []ToolSpec{{
			Name: "ACME_X", Description: "X.", ReadOnly: true, Destructive: true, ReadPOSTs: []string{"/search"},
			Scopes: []string{"read"}, InputSchema: json.RawMessage(`{"type":"object"}`),
		}},
		Readers: []ReaderSpec{{
			Name: "widgets", Kinds: []string{"widget"}, TargetSchema: json.RawMessage(`{}`), Scopes: []string{"read"},
		}},
		Triggers: []TriggerSpec{{
			Slug: "ACME_Y", Name: "Y", Kind: TriggerPush, ConfigSchema: json.RawMessage(`{}`),
			PayloadSchema: json.RawMessage(`{}`), Sample: json.RawMessage(`{"id":1}`), Scopes: []string{"read"},
		}},
	}
}

const everyFieldJSON = `{"toolkit":"acme","version":2,"name":"Acme","api_base":"https://{subdomain}.acme.example",` +
	`"auth":{"methods":[{"kind":"oauth2_code",` +
	`"oauth2":{"client":"acme","auth_url":"https://a.example/auth","token_url":"https://a.example/token",` +
	`"revoke_url":"https://a.example/revoke","revoke_style":"rfc7009","scopes":["read"],"scope_separator":",",` +
	`"pkce":true,"auth_params":{"prompt":"consent"},"client_auth":"body","rotating_refresh":true},` +
	`"place":{"in":"header","name":"Authorization","template":"Bearer {token}"},` +
	`"fields":[{"name":"subdomain","label":"Subdomain","help":"Before .acme.example","secret":true,` +
	`"level":"discovered","discover":"/subdomain",` +
	`"validate":{"required":true,"format":"hostname","pattern":"[a-z]+","enum":["a"],"max_length":63},` +
	`"use":[{"in":"header","name":"X-Sub","template":"{value}"}]}]}],` +
	`"test":{"path":"/me","label":"/name","account_ref":"/id","headers":{"Acme-Version":"1"}}},` +
	`"webhook":{"scope":"connection","verify":{"type":"hmac_sha256",` +
	`"signature":{"header":"X-Sig","json":"/sig","hash":true,"prefix":"v1=","split":",","unit":"s"},` +
	`"encoding":"hex","signed":"{body}","timestamp":{"header":"X-Time","unit":"ms"},"tolerance_seconds":60,` +
	`"token":{"json":"/token"},"issuer":"https://issuer.example","audience":"{endpoint_url}","email":"a@b.example"},` +
	`"delivery_id":{"header":"X-Id"},"handshake":"github_ping"},` +
	`"tools":[{"name":"ACME_X","description":"X.","read_only":true,"destructive":true,"read_posts":["/search"],` +
	`"scopes":["read"],"input_schema":{"type":"object"}}],` +
	`"readers":[{"name":"widgets","kinds":["widget"],"target_schema":{},"scopes":["read"]}],` +
	`"triggers":[{"slug":"ACME_Y","name":"Y","kind":"push","config_schema":{},"payload_schema":{},` +
	`"sample":{"id":1},"scopes":["read"]}]}`

func TestManifestJSONKeepsThePinnedNames(t *testing.T) {
	t.Parallel()
	roundTrip(t, everyField(), everyFieldJSON)
}

func TestRuntimeJSONKeepsThePinnedNames(t *testing.T) {
	t.Parallel()
	item := Item{
		ExternalID: "e1", Kind: "widget", Title: "T", URL: "https://acme.example/w/1", MIME: "text/plain",
		Content: []byte("hi"), Metadata: map[string]string{"author": "a"}, UpdatedAt: &at,
	}
	const itemJSON = `{"external_id":"e1","kind":"widget","title":"T","url":"https://acme.example/w/1",` +
		`"mime":"text/plain","content":"aGk=","metadata":{"author":"a"},"updated_at":"2026-09-28T10:00:00Z"}`
	event := Event{ID: "ev1", Trigger: "ACME_Y", AccountRef: "acct", OccurredAt: at, Data: json.RawMessage(`{"id":1}`)}
	const eventJSON = `{"id":"ev1","trigger":"ACME_Y","account_ref":"acct","occurred_at":"2026-09-28T10:00:00Z","data":{"id":1}}`

	roundTrip(t, item, itemJSON)
	roundTrip(t, event, eventJSON)
	roundTrip(t,
		PageRequest{Target: map[string]string{"reader": "widgets"}, Cursor: "c1", Watermark: &at, Token: "tok", Full: true, Limit: 50},
		`{"target":{"reader":"widgets"},"cursor":"c1","watermark":"2026-09-28T10:00:00Z","token":"tok","full":true,"limit":50}`)
	roundTrip(t,
		Page{Items: []Item{item}, Cursor: "c2", Watermark: &at, Token: "tok", Removed: []string{"e0"}},
		`{"items":[`+itemJSON+`],"cursor":"c2","watermark":"2026-09-28T10:00:00Z","token":"tok","removed":["e0"]}`)
	roundTrip(t, Batch{Events: []Event{event}, Cursor: "c3"}, `{"events":[`+eventJSON+`],"cursor":"c3"}`)
	roundTrip(t,
		Target{Kind: "repository", ID: "acme/api", Name: "acme/api", Description: "d", Target: json.RawMessage(`{"repository":"acme/api"}`)},
		`{"kind":"repository","id":"acme/api","name":"acme/api","description":"d","target":{"repository":"acme/api"}}`)
}

// Zero values show which names are always there and which are left out when
// empty.
func TestZeroValuesKeepOnlyThePinnedNames(t *testing.T) {
	t.Parallel()
	for name, testCase := range map[string]struct {
		value any
		want  string
	}{
		"manifest": {
			Manifest{}, `{"toolkit":"","version":0,"name":"","api_base":"","auth":{"methods":null,"test":{"path":"","label":""}}}`,
		},
		"method":      {Method{}, `{"kind":""}`},
		"oauth2":      {OAuth2{}, `{"client":"","auth_url":"","token_url":"","scopes":null,"pkce":false,"client_auth":""}`},
		"place":       {Place{}, `{"in":"","name":"","template":""}`},
		"field":       {Field{}, `{"name":"","label":"","secret":false,"level":"","validate":{"required":false},"use":null}`},
		"use":         {Use{}, `{"in":""}`},
		"webhook":     {Webhook{}, `{"scope":"","verify":{"type":""},"delivery_id":{}}`},
		"tool":        {ToolSpec{}, `{"name":"","description":"","read_only":false,"destructive":false,"scopes":null,"input_schema":null}`},
		"reader":      {ReaderSpec{}, `{"name":"","kinds":null,"target_schema":null,"scopes":null}`},
		"trigger":     {TriggerSpec{}, `{"slug":"","name":"","kind":"","config_schema":null,"payload_schema":null,"sample":null,"scopes":null}`},
		"pagerequest": {PageRequest{}, `{"target":null,"full":false,"limit":0}`},
		"page":        {Page{}, `{"items":null}`},
		"item":        {Item{}, `{"external_id":"","kind":""}`},
		"batch":       {Batch{}, `{"events":null,"cursor":""}`},
		"event":       {Event{}, `{"id":"","trigger":"","occurred_at":"0001-01-01T00:00:00Z","data":null}`},
		"target":      {Target{}, `{"kind":"","id":"","name":"","target":null}`},
	} {
		encoded, err := json.Marshal(testCase.value)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if string(encoded) != testCase.want {
			t.Errorf("%s encodes as\n%s\nwant\n%s", name, encoded, testCase.want)
		}
	}
}

// roundTrip checks that value encodes exactly as want, and decodes back to
// itself.
func roundTrip[T any](t *testing.T, value T, want string) {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if string(encoded) != want {
		t.Fatalf("%T encodes as\n%s\nwant\n%s", value, encoded, want)
	}
	var decoded T
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(decoded, value) {
		t.Fatalf("%T decodes as\n%#v\nwant\n%#v", value, decoded, value)
	}
}
