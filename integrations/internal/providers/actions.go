package providers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"

	integrationsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/integrations/v1"

	"github.com/Shubham04-oss/oryxa-go/integrations/internal/domain"
)

// ActionNamePattern is what an action may be called: lowercase words joined by
// underscores, such as create_issue. A caller names it after its provider, as
// github_create_issue, which is also the tool an agent sees.
var ActionNamePattern = regexp.MustCompile(`^[a-z][a-z0-9]*(_[a-z0-9]+)*$`)

// Action is one thing a caller may do with a connection: a tool an agent
// calls, or a step other work runs.
type Action struct {
	Name string
	// Summary is the one line that tells a model what the action does.
	Summary string
	// Input is the JSON Schema of the action's input: an object whose every
	// field carries a description. It describes the action to callers; Run
	// checks what it needs itself.
	Input json.RawMessage
	// Writes says the action changes something at the provider, so a caller
	// confirms it with the member first and repeats it only under the same
	// idempotency key.
	Writes bool
	// Run carries the action out with the connection's credential.
	Run func(ctx context.Context, client *http.Client, request ActionRequest) (ActionResult, error)
}

type ActionRequest struct {
	Credential domain.Credential
	// Input is what the caller sent, still to be checked by the action.
	Input json.RawMessage
	Now   time.Time
}

// ActionResult is what the provider did.
type ActionResult struct {
	// Resource is what the action created or changed, when it names one.
	Resource integrationsv1.Resource
	// Result is what the action reports back: what a caller needs to act
	// next, never a credential, and small enough to publish.
	Result json.RawMessage
}

// DecodeInput reads an action's input, refusing fields the action does not
// take so a caller learns about a mistake rather than having it ignored.
func DecodeInput(input json.RawMessage, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(input))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("%w: the action's input is not valid: %s", domain.ErrInvalid, err)
	}
	return nil
}

// Required refuses an empty field, naming it as the caller wrote it.
func Required(field, value string) error {
	if value == "" {
		return fmt.Errorf("%w: %s is required", domain.ErrInvalid, field)
	}
	return nil
}

// Answered is what an action reports from a provider's answer, keeping only
// the fields a caller needs.
func Answered(value any) json.RawMessage {
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil
	}
	return encoded
}

// ProviderBaseURL is where an action calls a provider reached through the
// organization's own address or account, rather than one fixed host.
//
// A bare host is the app's domain, which connections filled into the URLs the
// flow used: Zendesk's acme for acme.zendesk.com. Where the provider reported
// the address with the token it issued, that wins, because it is the one the
// provider itself named and it can differ per account: Salesforce's org comes
// back as instance_url and is not the login host the app named as its domain.
// A value that already carries a scheme is used as it is, so a provider whose
// domain is a whole origin needs no special case.
//
// preferred names the field to read first. An empty result means the
// connection did not carry what the provider needs, which an action must
// answer as domain.ErrInvalid rather than calling a guessed address.
func ProviderBaseURL(credential domain.Credential, preferred string) string {
	for _, name := range []string{preferred, "instance_url", "base_url", "host"} {
		if name == "" {
			continue
		}
		if value := withoutScheme(credential.Metadata[name]); value != "" {
			return value
		}
	}
	return withoutScheme(credential.Domain)
}

// withoutScheme trims surrounding space and a trailing slash, and leaves a
// value that already names an origin alone.
func withoutScheme(value string) string {
	return strings.TrimRight(strings.TrimSpace(value), "/")
}
