package triggercheck

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/Shubham04-oss/oryxa-go/internal/composio"
)

// fakeCatalog answers a catalog of apps and their triggers, in pages, so the
// walk over cursors is exercised rather than assumed.
type fakeCatalog struct {
	toolkits []composio.Toolkit
	triggers map[string][]composio.TriggerType
	pageSize int
	fails    map[string]error
	listed   []string
}

func (fake *fakeCatalog) Toolkits(_ context.Context, query composio.ToolkitQuery) (composio.Page[composio.Toolkit], error) {
	return pageOf(fake.toolkits, query.Cursor, fake.pageSize)
}

func (fake *fakeCatalog) TriggerTypes(_ context.Context, query composio.TriggerTypeQuery) (composio.Page[composio.TriggerType], error) {
	var items []composio.TriggerType
	for _, toolkit := range query.Toolkits {
		if err := fake.fails[toolkit]; err != nil {
			return composio.Page[composio.TriggerType]{}, err
		}
		fake.listed = append(fake.listed, toolkit)
		items = append(items, fake.triggers[toolkit]...)
	}
	return pageOf(items, query.Cursor, fake.pageSize)
}

func pageOf[T any](items []T, cursor string, size int) (composio.Page[T], error) {
	if size <= 0 {
		return composio.Page[T]{Items: items}, nil
	}
	start := 0
	if cursor != "" {
		if _, err := fmtSscan(cursor, &start); err != nil {
			return composio.Page[T]{}, err
		}
	}
	end := min(start+size, len(items))
	result := composio.Page[T]{Items: items[start:end]}
	if end < len(items) {
		result.NextCursor = itoa(end)
	}
	return result, nil
}

func TestRunReportsACatalogItCanHandle(t *testing.T) {
	t.Parallel()
	catalog := &fakeCatalog{
		pageSize: 1,
		toolkits: []composio.Toolkit{
			{Slug: "github", TriggersCount: 2}, {Slug: "slack", TriggersCount: 1},
			// An app that delivers nothing has nothing to check.
			{Slug: "calculator", TriggersCount: 0},
		},
		triggers: map[string][]composio.TriggerType{
			"github": {{
				Slug: "GITHUB_COMMIT_EVENT", Toolkit: "github", Kind: "webhook",
				Config: json.RawMessage(`{"type":"object","properties":{"owner":{"type":"string"},
					"repo":{"type":"string"}},"required":["owner","repo"]}`),
			}, {
				Slug: "GITHUB_STAR_ADDED_EVENT", Toolkit: "github", Kind: "poll",
				Config: json.RawMessage(`{"type":"object","properties":{}}`),
			}},
			"slack": {{
				Slug: "SLACK_RECEIVE_MESSAGE", Toolkit: "slack", Kind: "webhook",
				RequiresWebhookEndpointSetup: true,
				Config: json.RawMessage(`{"type":"object","properties":{"channel":{"type":"string"}},
					"required":["channel"]}`),
			}},
		},
	}

	report, err := Run(context.Background(), catalog, Config{})
	if err != nil {
		t.Fatal(err)
	}
	if !report.Sound() {
		t.Fatalf("findings against a catalog apps handles: %+v", report.Findings)
	}
	if report.Toolkits != 2 || report.Triggers != 3 || report.Checked != 3 {
		t.Fatalf("report = %+v", report)
	}
	if report.Pushed != 2 || report.Polled != 1 || report.ManualSetup != 1 || report.Configured != 2 {
		t.Fatalf("report = %+v", report)
	}
	// An app delivering nothing is never listed, however many pages the
	// others take.
	if walked := distinct(catalog.listed); strings.Join(walked, ",") != "github,slack" {
		t.Fatalf("apps walked = %v, want github and slack only", walked)
	}
}

func distinct(values []string) []string {
	var unique []string
	for _, value := range values {
		if !slices.Contains(unique, value) {
			unique = append(unique, value)
		}
	}
	slices.Sort(unique)
	return unique
}

func TestRunNamesEveryTriggerAMemberCouldNotSubscribeTo(t *testing.T) {
	t.Parallel()
	catalog := &fakeCatalog{
		toolkits: []composio.Toolkit{{Slug: "oddball", TriggersCount: 4}},
		triggers: map[string][]composio.TriggerType{
			"oddball": {{
				// A slug apps would refuse, so nothing could ever subscribe.
				Slug: "oddball trigger!", Toolkit: "oddball", Kind: "webhook",
			}, {
				// A slug apps reads but stores in another case.
				Slug: "oddball_lowercase_event", Toolkit: "oddball", Kind: "poll",
			}, {
				// A schema that is not JSON at all.
				Slug: "ODDBALL_BROKEN_SCHEMA", Toolkit: "oddball", Kind: "poll",
				Config: json.RawMessage(`{"type":"object",`),
			}, {
				// Sound, so the walk reports the others without stopping.
				Slug: "ODDBALL_FINE_EVENT", Toolkit: "oddball", Kind: "webhook",
				Config: json.RawMessage(`{"type":"object","properties":{}}`),
			}},
		},
	}

	report, err := Run(context.Background(), catalog, Config{})
	if err != nil {
		t.Fatal(err)
	}
	if report.Sound() {
		t.Fatal("a catalog apps cannot fully handle was reported sound")
	}
	if report.Triggers != 4 || report.Checked != 1 {
		t.Fatalf("checked %d of %d", report.Checked, report.Triggers)
	}
	problems := map[string]string{}
	for _, finding := range report.Findings {
		problems[finding.Trigger] = finding.Problem
	}
	for trigger, want := range map[string]string{
		"oddball trigger!":        "its slug is not one apps accepts",
		"oddball_lowercase_event": "its slug is not in the case apps stores",
		"ODDBALL_BROKEN_SCHEMA":   "its config schema is not valid JSON",
	} {
		if problems[trigger] != want {
			t.Fatalf("%s reported %q, want %q", trigger, problems[trigger], want)
		}
	}
	if _, reported := problems["ODDBALL_FINE_EVENT"]; reported {
		t.Fatal("a sound trigger was reported as a finding")
	}
}

// TestRunCatchesACheckThatRefusesAValidSubscription is what this command is
// really for: a schema shape whose own required fields the validator would
// reject. That is the check being wrong, and it would turn a subscription a
// member is entitled to make into an error nobody can act on.
func TestRunCatchesACheckThatRefusesAValidSubscription(t *testing.T) {
	t.Parallel()
	// A schema whose required field is declared with a type no example can
	// satisfy, so the config built from it is refused.
	catalog := &fakeCatalog{
		toolkits: []composio.Toolkit{{Slug: "strict", TriggersCount: 1}},
		triggers: map[string][]composio.TriggerType{
			"strict": {{
				Slug: "STRICT_EVENT", Toolkit: "strict", Kind: "webhook",
				Config: json.RawMessage(`{"type":"object","properties":{"when":{"type":"impossible"}},
					"required":["when"]}`),
			}},
		},
	}

	report, err := Run(context.Background(), catalog, Config{})
	if err != nil {
		t.Fatal(err)
	}
	if report.Sound() {
		t.Fatal("a trigger whose own schema cannot be satisfied was reported sound")
	}
	if got := report.Findings[0].Problem; got != "a config meeting its own schema is refused" {
		t.Fatalf("problem = %q", got)
	}
}

func TestRunKeepsWalkingWhenOneAppCannotBeRead(t *testing.T) {
	t.Parallel()
	catalog := &fakeCatalog{
		toolkits: []composio.Toolkit{{Slug: "broken", TriggersCount: 1}, {Slug: "github", TriggersCount: 1}},
		fails:    map[string]error{"broken": errors.New("composio is unavailable")},
		triggers: map[string][]composio.TriggerType{
			"github": {{Slug: "GITHUB_COMMIT_EVENT", Toolkit: "github", Kind: "webhook"}},
		},
	}

	report, err := Run(context.Background(), catalog, Config{})
	if err != nil {
		t.Fatalf("one unreadable app ended the whole walk: %v", err)
	}
	if report.Toolkits != 1 || report.Triggers != 1 {
		t.Fatalf("report = %+v", report)
	}
	if len(report.Skipped) != 1 || report.Skipped[0].Toolkit != "broken" {
		t.Fatalf("skipped = %+v", report.Skipped)
	}
	// An app that could not be read is not a finding against apps, but it is
	// not silence either.
	if !report.Sound() {
		t.Fatalf("an unreadable app was counted as a trigger apps cannot handle: %+v", report.Findings)
	}
}

func TestRunNarrowsToTheAppsAskedFor(t *testing.T) {
	t.Parallel()
	catalog := &fakeCatalog{
		toolkits: []composio.Toolkit{{Slug: "github", TriggersCount: 1}, {Slug: "slack", TriggersCount: 1}},
		triggers: map[string][]composio.TriggerType{
			"github": {{Slug: "GITHUB_COMMIT_EVENT", Toolkit: "github", Kind: "webhook"}},
			"slack":  {{Slug: "SLACK_RECEIVE_MESSAGE", Toolkit: "slack", Kind: "webhook"}},
		},
	}

	report, err := Run(context.Background(), catalog, Config{Toolkits: []string{"slack"}})
	if err != nil {
		t.Fatal(err)
	}
	if report.Triggers != 1 || strings.Join(catalog.listed, ",") != "slack" {
		t.Fatalf("walked %v, %+v", catalog.listed, report)
	}
}

func TestSatisfyBuildsAConfigOfTheDeclaredTypes(t *testing.T) {
	t.Parallel()
	config, err := satisfy(json.RawMessage(`{"type":"object","properties":{
		"name":{"type":"string"},"count":{"type":"integer"},"ratio":{"type":"number"},
		"on":{"type":"boolean"},"tags":{"type":"array"},"opts":{"type":"object"},
		"state":{"type":"string","enum":["open","closed"]},"maybe":{"type":["string","null"]},
		"unstated":{}},
		"required":["name","count","ratio","on","tags","opts","state","maybe","unstated"]}`))
	if err != nil {
		t.Fatal(err)
	}
	var built map[string]any
	if err := json.Unmarshal(config, &built); err != nil {
		t.Fatal(err)
	}
	for field, want := range map[string]any{
		"name": "example", "count": float64(1), "ratio": 1.5, "on": true,
		"state": "open", "maybe": "example", "unstated": "example",
	} {
		if built[field] != want {
			t.Fatalf("%s = %#v, want %#v", field, built[field], want)
		}
	}
	if _, ok := built["tags"].([]any); !ok {
		t.Fatalf("tags = %#v, want an array", built["tags"])
	}
	if _, ok := built["opts"].(map[string]any); !ok {
		t.Fatalf("opts = %#v, want an object", built["opts"])
	}
}

// Small helpers so the fake needs no extra imports.
func itoa(value int) string {
	if value == 0 {
		return "0"
	}
	digits := ""
	for value > 0 {
		digits = string(rune('0'+value%10)) + digits
		value /= 10
	}
	return digits
}

func fmtSscan(value string, into *int) (int, error) {
	result := 0
	for _, digit := range value {
		if digit < '0' || digit > '9' {
			return 0, errors.New("bad cursor " + value)
		}
		result = result*10 + int(digit-'0')
	}
	*into = result
	return 1, nil
}
