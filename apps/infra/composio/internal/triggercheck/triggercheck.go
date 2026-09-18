// Package triggercheck walks Composio's real catalog and reports, for every
// trigger in it, whether apps could subscribe a member to that trigger and
// handle what it delivers.
//
// The offline tests prove the pipeline against a trigger of every shape the
// catalog holds. They cannot prove the catalog holds only those shapes:
// Composio publishes thousands of triggers, written by as many teams, and a
// trigger whose slug or config schema is unlike the rest would be refused at
// the one moment a member tries to subscribe to it. This finds those ahead of
// any member, by checking each trigger through the very code the service
// runs - domain.CheckTool and domain.CheckTriggerConfig - rather than a
// second opinion of it.
//
// Nothing here subscribes to anything, runs a tool or touches a member's
// account. It only reads the catalog.
package triggercheck

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
	"sort"
	"strings"

	"github.com/Shubham04-oss/oryxa-go/apps/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/internal/composio"
)

// Client is the part of Composio this reads. *composio.Client satisfies it.
type Client interface {
	Toolkits(ctx context.Context, query composio.ToolkitQuery) (composio.Page[composio.Toolkit], error)
	TriggerTypes(ctx context.Context, query composio.TriggerTypeQuery) (composio.Page[composio.TriggerType], error)
}

type Config struct {
	APIKey  string
	BaseURL string
	// Toolkits narrows the walk to these apps. Empty walks every app in the
	// catalog that delivers anything, which is the point of the exercise and
	// takes a while.
	Toolkits []string
	// Limit stops after this many apps, for a quick pass. Zero walks them
	// all.
	Limit int
}

func FromEnv() Config {
	config := Config{
		APIKey:  strings.TrimSpace(os.Getenv("COMPOSIO_API_KEY")),
		BaseURL: strings.TrimSpace(os.Getenv("COMPOSIO_BASE_URL")),
	}
	for _, toolkit := range strings.Split(os.Getenv("TRIGGERCHECK_TOOLKITS"), ",") {
		if toolkit = strings.TrimSpace(toolkit); toolkit != "" {
			config.Toolkits = append(config.Toolkits, strings.ToLower(toolkit))
		}
	}
	return config
}

// Finding is one trigger apps would not handle, and why.
type Finding struct {
	Toolkit string `json:"toolkit"`
	Trigger string `json:"trigger"`
	// Problem is what is wrong, in one phrase, so findings group by it.
	Problem string `json:"problem"`
	Detail  string `json:"detail,omitempty"`
}

// Report is what the walk found.
type Report struct {
	Toolkits int `json:"toolkits"`
	Triggers int `json:"triggers"`
	// Pushed, Polled and Unstated count triggers by how they deliver.
	Pushed   int `json:"pushed"`
	Polled   int `json:"polled"`
	Unstated int `json:"kind_unstated"`
	// ManualSetup counts triggers a member must register a webhook for in
	// the app itself before anything arrives.
	ManualSetup int `json:"manual_setup"`
	// Configured counts triggers whose subscription takes required fields,
	// which are the ones a member can get silently wrong.
	Configured int `json:"with_required_config"`
	// Checked counts triggers proven subscribable: their slug is one apps
	// accepts and a config meeting their own schema passes its check.
	Checked  int       `json:"checked"`
	Findings []Finding `json:"findings,omitempty"`
	// Skipped counts apps whose triggers could not be read at all.
	Skipped []Finding `json:"skipped,omitempty"`
}

// Sound says every trigger in the catalog is one apps can subscribe a member
// to and handle.
func (report Report) Sound() bool { return len(report.Findings) == 0 }

const page = 50

// Run walks the catalog and checks every trigger in it.
func Run(ctx context.Context, client Client, config Config) (Report, error) {
	toolkits := config.Toolkits
	if len(toolkits) == 0 {
		found, err := everyToolkit(ctx, client, config.Limit)
		if err != nil {
			return Report{}, err
		}
		toolkits = found
	}

	report := Report{}
	for _, toolkit := range toolkits {
		triggers, err := triggersOf(ctx, client, toolkit)
		if err != nil {
			// One app's listing failing must not lose the rest of the walk;
			// it is reported as unwalked rather than as a sound app.
			report.Skipped = append(report.Skipped, Finding{
				Toolkit: toolkit, Problem: "its triggers could not be read", Detail: err.Error(),
			})
			continue
		}
		report.Toolkits++
		for _, trigger := range triggers {
			report.count(trigger)
			report.Findings = append(report.Findings, check(trigger)...)
		}
	}
	sort.Slice(report.Findings, func(i, j int) bool {
		if report.Findings[i].Problem != report.Findings[j].Problem {
			return report.Findings[i].Problem < report.Findings[j].Problem
		}
		return report.Findings[i].Trigger < report.Findings[j].Trigger
	})
	return report, nil
}

func (report *Report) count(trigger composio.TriggerType) {
	report.Triggers++
	switch trigger.Kind {
	case domain.TriggerKindWebhook:
		report.Pushed++
	case domain.TriggerKindPoll:
		report.Polled++
	default:
		report.Unstated++
	}
	if trigger.RequiresWebhookEndpointSetup {
		report.ManualSetup++
	}
	if len(required(trigger.Config)) > 0 {
		report.Configured++
	}
	if len(check(trigger)) == 0 {
		report.Checked++
	}
}

// check runs one trigger through the same two gates a subscription passes,
// and reports whatever would stop a member using it.
func check(trigger composio.TriggerType) []Finding {
	var findings []Finding
	at := func(problem, detail string) Finding {
		return Finding{Toolkit: trigger.Toolkit, Trigger: trigger.Slug, Problem: problem, Detail: detail}
	}

	slug, err := domain.CheckTool(trigger.Slug)
	if err != nil {
		// Nothing can be subscribed under a slug apps refuses to read, so
		// this trigger is unreachable however well the rest works.
		return []Finding{at("its slug is not one apps accepts", trigger.Slug)}
	}
	if slug != trigger.Slug {
		findings = append(findings, at("its slug is not in the case apps stores",
			fmt.Sprintf("%s becomes %s", trigger.Slug, slug)))
	}

	if len(trigger.Config) > 0 && !json.Valid(trigger.Config) {
		return append(findings, at("its config schema is not valid JSON", truncate(string(trigger.Config))))
	}

	// A config meeting the trigger's own schema must pass. A refusal here is
	// the check being wrong, not the member: it would turn a valid
	// subscription into an error nobody could act on.
	satisfying, err := satisfy(trigger.Config)
	if err != nil {
		return append(findings, at("no config can be built from its schema", err.Error()))
	}
	if err := domain.CheckTriggerConfig(trigger.Config, satisfying); err != nil {
		findings = append(findings, at("a config meeting its own schema is refused",
			fmt.Sprintf("%s: %v", satisfying, err)))
	}

	// And where the trigger declares required fields, leaving them out must
	// be caught, since that is the subscription that is accepted and then
	// delivers nothing.
	if names := required(trigger.Config); len(names) > 0 {
		if err := domain.CheckTriggerConfig(trigger.Config, json.RawMessage(`{}`)); !errors.Is(err, domain.ErrInvalid) {
			findings = append(findings, at("an empty config is accepted although the trigger requires fields",
				strings.Join(names, ", ")))
		}
	}
	return findings
}

// satisfy builds the config a member would send for a trigger: a value of
// the declared type for each required field. It is what the check is run
// against, so a schema this cannot satisfy is itself the finding.
func satisfy(schema json.RawMessage) (json.RawMessage, error) {
	names := required(schema)
	if len(names) == 0 {
		return json.RawMessage(`{}`), nil
	}
	var parsed struct {
		Properties map[string]json.RawMessage `json:"properties"`
	}
	if len(schema) > 0 {
		if err := json.Unmarshal(schema, &parsed); err != nil {
			return nil, fmt.Errorf("read its properties: %w", err)
		}
	}
	config := map[string]any{}
	for _, name := range names {
		config[name] = exampleOf(parsed.Properties[name])
	}
	encoded, err := json.Marshal(config)
	if err != nil {
		return nil, err
	}
	return encoded, nil
}

// exampleOf is a value of the type a property declares. A property whose
// type is not stated plainly gets a string, which nothing judges.
func exampleOf(property json.RawMessage) any {
	if len(property) == 0 {
		return "example"
	}
	var described struct {
		Type json.RawMessage   `json:"type"`
		Enum []json.RawMessage `json:"enum"`
	}
	if json.Unmarshal(property, &described) != nil {
		return "example"
	}
	// An enum's own first value is the surest thing to send.
	if len(described.Enum) > 0 {
		var value any
		if json.Unmarshal(described.Enum[0], &value) == nil {
			return value
		}
	}
	for _, name := range typeNames(described.Type) {
		switch name {
		case "string":
			return "example"
		case "integer":
			return 1
		case "number":
			return 1.5
		case "boolean":
			return true
		case "array":
			return []any{"example"}
		case "object":
			return map[string]any{}
		}
	}
	return "example"
}

func typeNames(declared json.RawMessage) []string {
	if len(declared) == 0 {
		return nil
	}
	var one string
	if json.Unmarshal(declared, &one) == nil {
		return []string{one}
	}
	var many []string
	if json.Unmarshal(declared, &many) == nil {
		// "null" says nothing about what to send, so it is tried last.
		return slices.DeleteFunc(many, func(name string) bool { return name == "null" })
	}
	return nil
}

func required(schema json.RawMessage) []string {
	if len(schema) == 0 {
		return nil
	}
	var parsed struct {
		Required []string `json:"required"`
	}
	if json.Unmarshal(schema, &parsed) != nil {
		return nil
	}
	return parsed.Required
}

// everyToolkit names the apps in the catalog that deliver at least one
// trigger, since an app with none has nothing to check.
func everyToolkit(ctx context.Context, client Client, limit int) ([]string, error) {
	var toolkits []string
	cursor := ""
	for {
		answer, err := client.Toolkits(ctx, composio.ToolkitQuery{Cursor: cursor, Limit: page})
		if err != nil {
			return nil, fmt.Errorf("list the catalog: %w", err)
		}
		for _, toolkit := range answer.Items {
			if toolkit.TriggersCount > 0 {
				toolkits = append(toolkits, toolkit.Slug)
			}
			if limit > 0 && len(toolkits) >= limit {
				return toolkits, nil
			}
		}
		if cursor = answer.NextCursor; cursor == "" {
			return toolkits, nil
		}
	}
}

func triggersOf(ctx context.Context, client Client, toolkit string) ([]composio.TriggerType, error) {
	var triggers []composio.TriggerType
	cursor := ""
	for {
		answer, err := client.TriggerTypes(ctx, composio.TriggerTypeQuery{
			Toolkits: []string{toolkit}, Cursor: cursor, Limit: page,
		})
		if err != nil {
			return nil, err
		}
		triggers = append(triggers, answer.Items...)
		if cursor = answer.NextCursor; cursor == "" {
			return triggers, nil
		}
	}
}

func truncate(value string) string {
	const most = 200
	if len(value) <= most {
		return value
	}
	return value[:most] + "…"
}
