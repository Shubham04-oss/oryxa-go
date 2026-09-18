package application

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/Shubham04-oss/oryxa-go/apps/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/internal/composio"
)

// answers makes the fake Composio proxy answer one body, and reports what it
// was asked for.
func (h harness) answers(status int, body string) {
	h.composio.proxyResult = composio.ProxyResponse{Status: status, Data: json.RawMessage(body)}
}

func TestGitHubTargetsAreTheRepositoriesTheConnectionCanSee(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	connection := h.activeConnection("apc_1", "user-1", "github", "ca_1")
	h.answers(200, `[
		{"full_name":"oryxa-labs/oryxa","description":"The product"},
		{"full_name":"oryxa-labs/old","description":"Retired","archived":true},
		{"full_name":"","description":"Nameless"},
		{"full_name":"oryxa-labs/web"}
	]`)

	targets, err := h.service.ConnectionTargets(context.Background(), memberToken, "org_1", connection.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(targets) != 2 {
		t.Fatalf("targets = %+v, want the two live repositories", targets)
	}
	if targets[0].Kind != "repository" || targets[0].Name != "oryxa-labs/oryxa" {
		t.Fatalf("first target = %+v", targets[0])
	}
	// The target is the payload knowledge registers a source with, so a
	// caller passes it on rather than assembling it.
	if string(targets[0].Target) != `{"repository":"oryxa-labs/oryxa"}` {
		t.Fatalf("target payload = %s", targets[0].Target)
	}
	if targets[0].Description != "The product" {
		t.Fatalf("description = %q", targets[0].Description)
	}
	// It read GitHub's own API through the connection, not Composio's tools.
	if len(h.composio.proxied) != 1 || h.composio.proxied[0].ConnectedAccountID != "ca_1" {
		t.Fatalf("proxied = %+v", h.composio.proxied)
	}
}

func TestSlackTargetsAreTheChannelsByNameNotById(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	connection := h.activeConnection("apc_1", "user-1", "slack", "ca_1")
	h.answers(200, `{"ok":true,"channels":[
		{"id":"C0123456789","name":"engineering","purpose":{"value":"Where the work happens"}},
		{"id":"C0000000000","name":"archive","is_archived":true}
	]}`)

	targets, err := h.service.ConnectionTargets(context.Background(), memberToken, "org_1", connection.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(targets) != 1 {
		t.Fatalf("targets = %+v, want the one live channel", targets)
	}
	// A member reads #engineering; the id is what the source is registered
	// with. Asking them to know the id was the whole problem.
	if targets[0].Name != "#engineering" || targets[0].ID != "C0123456789" {
		t.Fatalf("target = %+v", targets[0])
	}
	if string(targets[0].Target) != `{"channel":"C0123456789"}` {
		t.Fatalf("target payload = %s", targets[0].Target)
	}
}

// TestSlackSayingNoIsNotSilence covers Slack answering 200 with ok false,
// which is how it refuses.
func TestSlackSayingNoIsNotSilence(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	connection := h.activeConnection("apc_1", "user-1", "slack", "ca_1")
	h.answers(200, `{"ok":false,"error":"missing_scope"}`)

	_, err := h.service.ConnectionTargets(context.Background(), memberToken, "org_1", connection.ID)
	if !errors.Is(err, domain.ErrUnavailable) {
		t.Fatalf("error = %v, want unavailable", err)
	}
}

func TestNotionTargetsAreItsDataSources(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	connection := h.activeConnection("apc_1", "user-1", "notion", "ca_1")
	h.answers(200, `{"results":[
		{"id":"ds_1","title":[{"plain_text":"Engineering "},{"plain_text":"notes"}]},
		{"id":"ds_2","title":[]}
	]}`)

	targets, err := h.service.ConnectionTargets(context.Background(), memberToken, "org_1", connection.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(targets) != 2 {
		t.Fatalf("targets = %+v", targets)
	}
	// Notion writes a title in pieces, and one with none is still something
	// a member can pick.
	if targets[0].Name != "Engineering notes" || targets[1].Name != "Untitled" {
		t.Fatalf("names = %q, %q", targets[0].Name, targets[1].Name)
	}
	if string(targets[0].Target) != `{"database":"ds_1"}` {
		t.Fatalf("target payload = %s", targets[0].Target)
	}
	// Notion's search is a POST, which the read rules allow for exactly this.
	if len(h.composio.proxied) != 1 || h.composio.proxied[0].Method != "POST" {
		t.Fatalf("proxied = %+v", h.composio.proxied)
	}
}

// TestAnAppNothingCanBeIndexedForAnswersNothing keeps a picker from showing a
// problem where the honest answer is that there is nothing to index.
func TestAnAppNothingCanBeIndexedForAnswersNothing(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	connection := h.activeConnection("apc_1", "user-1", "linear", "ca_1")

	targets, err := h.service.ConnectionTargets(context.Background(), memberToken, "org_1", connection.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(targets) != 0 {
		t.Fatalf("targets = %+v, want none", targets)
	}
	if len(h.composio.proxied) != 0 {
		t.Fatalf("read an app it knows nothing about: %+v", h.composio.proxied)
	}
}

func TestConnectionTargetsChecksWhoIsAsking(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	theirs := h.activeConnection("apc_1", "user-2", "github", "ca_1")
	mine := h.activeConnection("apc_2", "user-1", "github", "ca_3")
	expired := h.activeConnection("apc_3", "user-1", "github", "ca_2")
	expired.Status = domain.ConnectionExpired
	h.store.connections[expired.ID] = expired
	h.answers(200, `[]`)

	if _, err := h.service.ConnectionTargets(context.Background(), memberToken, "org_1", theirs.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("another member's = %v, want not found", err)
	}
	if _, err := h.service.ConnectionTargets(context.Background(), memberToken, "org_1", expired.ID); !errors.Is(err, domain.ErrNeedsReconnect) {
		t.Fatalf("an expired connection = %v, want needs reconnect", err)
	}
	if _, err := h.service.ConnectionTargets(context.Background(), memberToken, "org_1", "apc_nope"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("a connection that does not exist = %v, want not found", err)
	}
	// A target is a live read through the connection's own credential: an
	// org admin may see that a member's connection exists and remove it, but
	// never act through it, so this is not found to them either.
	if _, err := h.service.ConnectionTargets(context.Background(), adminToken, "org_1", theirs.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("an admin's = %v, want not found", err)
	}
	// Nor may an agent acting for that admin: the caller's role does not
	// change what an agent may reach, only whose member it acts as.
	if _, err := h.service.ConnectionTargets(context.Background(), adminAgentToken, "org_1", theirs.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("an admin's agent = %v, want not found", err)
	}
	// An agent acting for the member themself still works: knowledge calls
	// this route with the member's own agent-grant token for the member's own
	// connection.
	if _, err := h.service.ConnectionTargets(context.Background(), agentToken, "org_1", mine.ID); err != nil {
		t.Fatalf("an agent acting for the connection's own member = %v", err)
	}
}

// TestAnAppRefusingTheReadIsReportedAsSuch keeps a 403 from the app looking
// like an empty workspace.
func TestAnAppRefusingTheReadIsReportedAsSuch(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	connection := h.activeConnection("apc_1", "user-1", "github", "ca_1")
	h.answers(403, `{"message":"Bad credentials"}`)

	_, err := h.service.ConnectionTargets(context.Background(), memberToken, "org_1", connection.ID)
	if !errors.Is(err, domain.ErrUnavailable) {
		t.Fatalf("error = %v, want unavailable", err)
	}
}

// TestAnAnswerThatIsNotWhatTheAppPromisesIsNotACrash covers an app answering
// something other than its own documented shape.
func TestAnAnswerThatIsNotWhatTheAppPromisesIsNotACrash(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	connection := h.activeConnection("apc_1", "user-1", "github", "ca_1")
	h.answers(200, `{"message":"not a list at all"}`)

	_, err := h.service.ConnectionTargets(context.Background(), memberToken, "org_1", connection.ID)
	if !errors.Is(err, domain.ErrUnavailable) {
		t.Fatalf("error = %v, want unavailable", err)
	}
}
