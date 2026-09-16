package providers

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/Shubham04-oss/oryxa-go/integrations/internal/domain"
)

// maxSkill is how long a provider's skill may be. It rides in an agent's
// prompt beside the provider's tools, so it stays guidance rather than
// becoming a copy of the provider's reference.
const maxSkill = 4 << 10

// TestBuiltinProvidersAreConsistent checks every provider against what the
// service assumes of it, so a new provider or action cannot be half-declared.
func TestBuiltinProvidersAreConsistent(t *testing.T) {
	for name, provider := range Builtin() {
		t.Run(name, func(t *testing.T) {
			if provider.Name != name {
				t.Fatalf("provider %q calls itself %q", name, provider.Name)
			}
			if provider.Target == nil || provider.Receive == nil {
				t.Fatal("every provider checks a target and receives deliveries")
			}
			switch provider.Delivery {
			case DeliveryApp:
				if provider.AccountKey == nil {
					t.Fatal("an app delivery routes by account key")
				}
				if provider.Register != nil || provider.Unregister != nil {
					t.Fatal("an app delivery registers nothing")
				}
			case DeliveryWebhook:
				if provider.Register == nil || provider.Unregister == nil {
					t.Fatal("a webhook delivery registers and removes what it registered")
				}
				if provider.AccountKey != nil {
					t.Fatal("a webhook delivery is not routed by account key")
				}
			default:
				t.Fatalf("delivery = %q", provider.Delivery)
			}
			checkSkill(t, provider)
			checkTargetDescription(t, provider)
			checkActions(t, provider)
		})
	}
}

func checkSkill(t *testing.T, provider Provider) {
	t.Helper()
	if !strings.HasPrefix(provider.Skill, "# ") {
		t.Fatalf("the skill must open with a heading, not %q", first(provider.Skill, 40))
	}
	if len(provider.Skill) > maxSkill {
		t.Fatalf("the skill is %d bytes, more than the %d an agent's prompt carries", len(provider.Skill), maxSkill)
	}
	// A skill that names a tool must name one the provider has, or it teaches
	// an agent to call something that does not exist.
	named := regexp.MustCompile(`\b` + provider.Name + `_[a-z_]+\b`)
	for _, mentioned := range named.FindAllString(provider.Skill, -1) {
		action := strings.TrimPrefix(mentioned, provider.Name+"_")
		if _, ok := provider.Action(action); !ok {
			t.Errorf("the skill names %s, which is not an action", mentioned)
		}
	}
}

// checkTargetDescription checks that what a provider says its target takes is
// what Target actually accepts, since a caller offers subscriptions from the
// description alone.
func checkTargetDescription(t *testing.T, provider Provider) {
	t.Helper()
	described := map[string]string{}
	for _, field := range provider.TargetFields {
		if field.Name == "" || field.Description == "" {
			t.Fatalf("target field %+v needs a name and a description", field)
		}
		value := field.Example
		if value == "" {
			value = "example"
		}
		described[field.Name] = value
	}
	if _, _, err := provider.Target(copyTarget(described), nil); err != nil {
		t.Fatalf("Target(%v) = %v, want the described target accepted", described, err)
	}
	for _, field := range provider.TargetFields {
		if !field.Required {
			continue
		}
		without := copyTarget(described)
		delete(without, field.Name)
		if _, _, err := provider.Target(without, nil); !errors.Is(err, domain.ErrInvalid) {
			t.Errorf("Target() without the required %s = %v, want domain.ErrInvalid", field.Name, err)
		}
	}
	// A field nothing describes is refused, so a caller cannot smuggle one in.
	extra := copyTarget(described)
	extra["something_else"] = "value"
	if _, _, err := provider.Target(extra, nil); !errors.Is(err, domain.ErrInvalid) {
		t.Errorf("Target() with an undescribed field = %v, want domain.ErrInvalid", err)
	}
	for _, event := range provider.Events.Names {
		if _, _, err := provider.Target(copyTarget(described), []string{event}); err != nil {
			t.Errorf("Target() with the described event %q = %v", event, err)
		}
	}
	if provider.Events.Default == "" {
		t.Error("a provider says what a subscription asking for no events receives")
	}
}

func checkActions(t *testing.T, provider Provider) {
	t.Helper()
	seen := map[string]bool{}
	for _, action := range provider.Actions {
		if !ActionNamePattern.MatchString(action.Name) {
			t.Errorf("action %q is not named in lowercase words", action.Name)
		}
		if seen[action.Name] {
			t.Errorf("action %q is declared twice", action.Name)
		}
		seen[action.Name] = true
		if action.Run == nil || action.Summary == "" || strings.Contains(action.Summary, "\n") {
			t.Errorf("action %s needs a one-line summary and something to run", action.Name)
		}
		checkSchema(t, action)
	}
}

// checkSchema checks an action's input schema, which is what a model reads
// before it calls the action.
func checkSchema(t *testing.T, action Action) {
	t.Helper()
	var schema struct {
		Type                 string                     `json:"type"`
		Properties           map[string]json.RawMessage `json:"properties"`
		Required             []string                   `json:"required"`
		AdditionalProperties *bool                      `json:"additionalProperties"`
	}
	if err := json.Unmarshal(action.Input, &schema); err != nil {
		t.Fatalf("action %s has an input schema that is not JSON: %v", action.Name, err)
	}
	if schema.Type != "object" || schema.AdditionalProperties == nil || *schema.AdditionalProperties {
		t.Errorf("action %s takes an object and refuses fields it does not name", action.Name)
	}
	for name, raw := range schema.Properties {
		var property struct {
			Type        string `json:"type"`
			Description string `json:"description"`
		}
		if err := json.Unmarshal(raw, &property); err != nil || property.Type == "" || property.Description == "" {
			t.Errorf("action %s field %s needs a type and a description a model can read: %s", action.Name, name, raw)
		}
	}
	for _, required := range schema.Required {
		if _, ok := schema.Properties[required]; !ok {
			t.Errorf("action %s requires %s, which it does not describe", action.Name, required)
		}
	}
}

// TestDescribeReadsTheRegistryAloud checks the catalogue a caller offering
// providers reads.
func TestDescribeReadsTheRegistryAloud(t *testing.T) {
	described := Builtin().Describe()
	if len(described) != 4 {
		t.Fatalf("Describe() = %d providers, want the registry's", len(described))
	}
	names := make([]string, 0, len(described))
	for _, provider := range described {
		names = append(names, provider.Name)
	}
	if !slices.IsSorted(names) {
		t.Fatalf("Describe() = %v, want them in name order", names)
	}
	encoded, err := json.Marshal(described)
	if err != nil {
		t.Fatal(err)
	}
	if !json.Valid(encoded) || strings.Contains(string(encoded), "null") {
		t.Fatalf("the catalogue does not encode cleanly: %s", first(string(encoded), 200))
	}
}

func copyTarget(from map[string]string) map[string]string {
	to := make(map[string]string, len(from))
	for key, value := range from {
		to[key] = value
	}
	return to
}

func first(text string, size int) string {
	if len(text) <= size {
		return text
	}
	return fmt.Sprintf("%s…", text[:size])
}
