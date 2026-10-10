// Package toolkit is the shape every one of Oryxa's own integrations takes.
// Copy this directory, rename the package to the toolkit's own name, and fill
// in what the provider's documentation says — nothing here is a starting
// point to be corrected, it is a list of what has to be answered.
//
// What is already in the toolkit a copy comes from — its Auth and Webhook — is
// frozen: connections are made against those two sections, so a change to
// either of them is a change every connection made so far has to live with.
// Everything else is this worker's to build.
//
// The work is:
//
//	1 Fill Manifest(): tools, readers, triggers. Need another auth kind or
//	  webhook scheme? Stop; say so in the PR. The core implements it once, in
//	  signin or hooks, with conformance tests.
//	2 Build the SDK client on Session.HTTP. go.mod already carries the SDK at
//	  its current stable version, so nothing outside this directory changes.
//	  Use Session.Token only if the SDK has no client hook.
//	3 Errors: 401 integration.ErrNeedsReconnect, 429 *integration.RateLimited,
//	  404 integration.ErrNotFound, any other 4xx *integration.AppError. Those
//	  four are the only shapes the core acts on, and an error that is none of
//	  them reaches a caller as a failure it cannot act on.
//	4 Readers: use the provider's own paging; a watermark or a token only on
//	  an incremental pass; under 6 MiB a page.
//	5 Triggers: the first Poll without a cursor returns no events and a
//	  cursor. Push: Subscribe registers hook.URL and hook.Secret; Parse only
//	  reads.
//	6 Cassettes in testdata/, built from the provider's documented responses.
//	  No real tokens, emails or ids.
//	7 Pass: go test ./apps/internal/integrations/<toolkit>/... and
//	  go run ./apps/internal/integration/cmd/check <toolkit>.
//
// Never: importing golang.org/x/oauth2; setting Authorization or Cookie;
// logging Session, arguments or results; reaching the network in a test.
//
// The directory is named with a leading underscore so the go tool ignores it:
// nothing here is compiled, and a copy is the first thing a worker does.
package toolkit

import (
	"context"
	"encoding/json"

	"github.com/Shubham04-oss/oryxa-go/apps/internal/integration"
)

// Toolkit is Composio's slug for the app, as apps and every other broker
// spells it. Every tool and event name begins with it in upper case, because
// an agent's skill reads tools by exactly those names.
const Toolkit = "acme"

// Integration is the app, as apps' registry holds it.
type Integration struct{}

// New answers the app's integration.
func New() Integration { return Integration{} }

// Manifest describes the app to the core. Everything below the Auth and
// Webhook is this worker's to fill in, and the manifest is what the core
// serves tools, readers and events from, so it is the first thing to write.
func (Integration) Manifest() integration.Manifest {
	return integration.Manifest{
		Toolkit: Toolkit, Version: 1, Name: "Acme", APIBase: "https://api.acme.example",
		// Auth and Webhook are frozen from the connection being made against
		// them; a copy keeps whatever the toolkit's own package already
		// declares there.
		Auth: auth(), Webhook: webhook(),
		Tools:    tools(),
		Readers:  readers(),
		Triggers: triggers(),
	}
}

// auth signs a member in, and is frozen.
func auth() integration.Auth {
	return integration.Auth{
		Methods: []integration.Method{
			{
				Kind: integration.KindOAuth2Code,
				OAuth2: &integration.OAuth2{
					Client: "acme", AuthURL: "https://acme.example/authorize", TokenURL: "https://acme.example/token",
					RevokeURL: "https://acme.example/revoke", Scopes: []string{"read", "write"},
					PKCE: true, ClientAuth: integration.ClientAuthBody,
				},
			},
			// A personal token instead of an OAuth app is a second method,
			// never a field.
			{Kind: integration.KindAPIToken, Place: &integration.Place{
				In: integration.InHeader, Name: "Authorization", Template: "Bearer {token}"}},
		},
		Test: integration.Test{Path: "/me", Label: "/login", AccountRef: "/id"},
	}
}

// webhook verifies the app's deliveries, and is frozen.
func webhook() *integration.Webhook {
	return &integration.Webhook{
		Scope: integration.HookScopeConnection,
		Verify: integration.Verify{
			Type:      integration.VerifyHMACSHA256,
			Signature: &integration.Locator{Header: "X-Acme-Signature", Prefix: "sha256="},
			Encoding:  integration.EncodingHex, Signed: "{body}",
		},
		DeliveryID: integration.Locator{Header: "X-Acme-Delivery"},
	}
}

// tools is one read and one write, which is the smallest pair that shows both
// halves of the record: a read announces nothing and takes no key, a write
// takes an idempotency key, keeps what the app answered and announces
// apps.action.completed.
func tools() []integration.ToolSpec {
	return []integration.ToolSpec{
		{
			Name: "ACME_LIST_ISSUES", Description: "List the issues in a repository", ReadOnly: true,
			// A POST a read needs, for the apps whose read is a POST. A read
			// session refuses every other method before anything is sent.
			ReadPOSTs: []string{"/search"},
			Scopes:    []string{"read"},
			InputSchema: json.RawMessage(`{
			  "type": "object",
			  "required": ["repo"],
			  "properties": {
			    "repo":  {"type": "string"},
			    "state": {"type": "string", "enum": ["open", "closed"]},
			    "limit": {"type": "integer", "minimum": 1, "maximum": 100}
			  }
			}`),
		},
		{
			Name: "ACME_CLOSE_ISSUE", Description: "Close an issue", Destructive: true,
			Scopes: []string{"write"},
			InputSchema: json.RawMessage(`{
			  "type": "object",
			  "required": ["repo", "number"],
			  "properties": {
			    "repo":   {"type": "string"},
			    "number": {"type": "integer", "minimum": 1},
			    "reason": {"type": "string", "max_length": 200}
			  }
			}`),
		},
	}
}

// readers is one reader, with the kinds it reads by and the schema a target
// it answers is held to.
func readers() []integration.ReaderSpec {
	return []integration.ReaderSpec{{
		Name: "issues", Kinds: []string{"repo"},
		TargetSchema: json.RawMessage(`{
		  "type": "object",
		  "required": ["repo"],
		  "properties": {"repo": {"type": "string"}}
		}`),
		// A reader that reads through a POST says so here, which is what
		// narrows its session to those calls and refuses every other.
		ReadPOSTs: []string{"/search"},
		Scopes:    []string{"read"},
	}}
}

// triggers is one polled event and one pushed one, which are the two ways an
// event can arrive and the two shapes of code each needs.
func triggers() []integration.TriggerSpec {
	return []integration.TriggerSpec{
		{
			Slug: "ACME_ISSUE_EVENT", Name: "An issue changed", Kind: integration.TriggerPoll,
			ConfigSchema: json.RawMessage(`{"type": "object"}`),
			PayloadSchema: json.RawMessage(`{
			  "type": "object",
			  "required": ["number", "state"],
			  "properties": {"number": {"type": "integer"}, "state": {"type": "string"}}
			}`),
			// The sample is what a reviewer reads, and the harness checks it
			// against the schema beside it, so it is real and complete.
			Sample: json.RawMessage(`{"number":7,"state":"closed"}`),
			Scopes: []string{"read"},
		},
		{
			Slug: "ACME_REPO_EVENT", Name: "A repository was pushed to", Kind: integration.TriggerPush,
			ConfigSchema: json.RawMessage(`{"type": "object"}`),
			PayloadSchema: json.RawMessage(`{
			  "type": "object",
			  "required": ["ref"],
			  "properties": {"ref": {"type": "string"}}
			}`),
			Sample: json.RawMessage(`{"ref":"refs/heads/main"}`),
			Scopes: []string{"read"},
		},
	}
}

// Tools answers the function behind each declared tool, by the name the
// manifest gives it. The registry refuses an integration whose functions and
// tools do not match one for one, so a tool cannot be declared and not run.
func (Integration) Tools() map[string]integration.ToolFunc {
	return map[string]integration.ToolFunc{
		"ACME_LIST_ISSUES": listIssues,
		"ACME_CLOSE_ISSUE": closeIssue,
	}
}

// listIssues is a read. Its session has already been narrowed to the methods
// the tool may send, so it cannot change anything even by accident, and it
// never sets a header of its own.
func listIssues(ctx context.Context, session integration.Session, args json.RawMessage) (json.RawMessage, error) {
	var input struct {
		Repo  string `json:"repo"`
		State string `json:"state"`
		Limit int    `json:"limit"`
	}
	if json.Unmarshal(args, &input) != nil {
		return nil, &integration.AppError{Message: "the arguments are not one object"}
	}
	return call(ctx, session, "GET", session.BaseURL+"/repos/"+input.Repo+"/issues?state="+input.State, nil)
}

// closeIssue is a write. ctx carries the caller's idempotency key, for a
// provider that takes one, and the core has already checked the arguments
// against the input_schema above it.
func closeIssue(ctx context.Context, session integration.Session, args json.RawMessage) (json.RawMessage, error) {
	var input struct {
		Repo   string `json:"repo"`
		Number int    `json:"number"`
		Reason string `json:"reason"`
	}
	if json.Unmarshal(args, &input) != nil {
		return nil, &integration.AppError{Message: "the arguments are not one object"}
	}
	// Only the fields it was given: an app that documents a field as
	// optional is not helped by being sent an empty one, and a transcript of
	// the call reads better for it.
	change := map[string]string{"state": "closed"}
	if input.Reason != "" {
		change["reason"] = input.Reason
	}
	body, err := json.Marshal(change)
	if err != nil {
		return nil, &integration.AppError{Message: "the request could not be written"}
	}
	return call(ctx, session, "PATCH",
		session.BaseURL+"/repos/"+input.Repo+"/issues/"+itoa(input.Number), body)
}
