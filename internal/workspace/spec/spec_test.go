package spec

import (
	"strings"
	"testing"
)

const example = `apiVersion: oryxa.in/v1
kind: Workspace
metadata: {organization: acme, domains: [acme.com]}
preset: team
organization: {name: Acme}
people:
  members:
    - {email: raya@acme.com, role: admin}
teams:
  - {name: Engineering, members: [raya@acme.com]}
`

func TestParseRoundTrip(t *testing.T) {
	document, err := Parse([]byte(example))
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := document.YAML()
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := Parse(encoded)
	if err != nil {
		t.Fatalf("output cannot be read: %v", err)
	}
	if parsed.Metadata.Organization != "acme" || parsed.Teams.Items[0].Members[0] != "raya@acme.com" {
		t.Fatalf("round trip changed the workspace: %+v", parsed)
	}
}

func TestParseRejectsUnknownAndUnsupportedSettings(t *testing.T) {
	for _, test := range []struct {
		name, content, want string
	}{
		{"unknown nested", strings.Replace(example, "name: Acme", "name: Acme, typo: yes", 1), "typo"},
		{"unknown top level", example + "surprise: true\n", "surprise"},
		{"unknown team mapping", strings.Replace(example, "name: Engineering", "name: Engineering, mystery: yes", 1), "mystery"},
		{"unsupported section", example + "chat: {slack: {install: add_to_slack}}\n", "chat is not yet supported"},
		{"unsupported agent policy", example + "agents: {personal: {style: concise}}\n", "personal"},
		{"empty agent name", example + "agents: {organization: {displayName: ' '}}\n", "agents.organization.displayName"},
		{"secret value", example + "secrets: {apiKey: plaintext}\n", "secrets is not yet supported"},
		{"wrong domain", strings.Replace(example, "raya@acme.com", "raya@other.com", 1), "outside metadata.domains"},
		{"duplicate member", strings.Replace(example, "  members:\n", "  members:\n    - {email: raya@acme.com}\n", 1), "duplicate email"},
		{"alias", strings.Replace(example, "preset: team", "preset: &p team", 1), "aliases and anchors"},
		{"multiple documents", example + "---\nkind: Workspace\n", "one document"},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := Parse([]byte(test.content))
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestParseTeamsPruneMapping(t *testing.T) {
	input := `apiVersion: oryxa.in/v1
kind: Workspace
metadata: {organization: acme}
teams:
  prune: true
  items:
    - {name: Engineering, members: [owner@acme.com]}
`
	document, err := Parse([]byte(input))
	if err != nil || !document.Teams.Prune || len(document.Teams.Items) != 1 {
		t.Fatalf("teams = %+v, %v", document.Teams, err)
	}
	encoded, err := document.YAML()
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := Parse(encoded)
	if err != nil || !parsed.Teams.Prune || parsed.Teams.Items[0].Name != "Engineering" {
		t.Fatalf("round trip = %+v, %v", parsed.Teams, err)
	}
}

func TestParseOrganizationAgentSettings(t *testing.T) {
	document, err := Parse([]byte(example + "agents: {organization: {enabled: false, displayName: Atlas, description: Helper, instructions: Coordinate teams.}}\n"))
	if err != nil {
		t.Fatal(err)
	}
	agent := document.Agents.Organization
	if agent.Enabled == nil || *agent.Enabled || *agent.DisplayName != "Atlas" || *agent.Instructions != "Coordinate teams." {
		t.Fatalf("agent settings = %+v", agent)
	}
	encoded, err := document.YAML()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Parse(encoded); err != nil {
		t.Fatalf("agent settings did not round trip: %v", err)
	}
}

func TestGuestOutsideDomainRequiresExplicitMemberMarker(t *testing.T) {
	base := "apiVersion: oryxa.in/v1\nkind: Workspace\nmetadata: {organization: acme, domains: [acme.com]}\n"
	member := "people:\n  members: [{email: guest@example.com}]\n"
	if _, err := Parse([]byte(base + member)); err == nil || !strings.Contains(err.Error(), "guest: true") {
		t.Fatalf("guest without marker = %v", err)
	}
	valid := base + "people:\n  guests: true\n  members: [{email: guest@example.com, guest: true}]\nteams:\n  - {name: Partners, members: [guest@example.com]}\n"
	if _, err := Parse([]byte(valid)); err != nil {
		t.Fatalf("guest with policy = %v", err)
	}
}
