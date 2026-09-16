package providers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
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
