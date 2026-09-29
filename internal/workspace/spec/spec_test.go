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

func TestDirectoryMappingsValidateAndRoundTrip(t *testing.T) {
	input := example + "directory:\n  identities: [{source: 'google_workspace:acme.com', externalId: '100', member: raya@acme.com}]\n  groups: [{source: 'google_workspace:acme.com', externalId: 'grp-1', team: Engineering}]\n"
	document, err := Parse([]byte(input))
	if err != nil || document.Directory == nil || len(document.Directory.Identities) != 1 || len(document.Directory.Groups) != 1 {
		t.Fatalf("directory = %+v, %v", document.Directory, err)
	}
	encoded, err := document.YAML()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Parse(encoded); err != nil {
		t.Fatalf("directory round trip: %v", err)
	}
	for _, invalid := range []string{
		"directory: {identities: [{source: google_workspace, externalId: '100', member: raya@acme.com}]}\n",
		"directory: {identities: [{source: 'google_workspace:acme.com', externalId: '100', member: raya@acme.com}, {source: 'google_workspace:acme.com', externalId: '100', member: raya@acme.com}]}\n",
		"directory: {groups: [{source: 'google_workspace:acme.com', externalId: '', team: Engineering}]}\n",
	} {
		if _, err := Parse([]byte(example + invalid)); err == nil {
			t.Fatalf("accepted %s", invalid)
		}
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
		{"unsupported agent policy", example + "agents: {personal: {style: concise}}\n", "style"},
		{"unsupported external search", example + "search: {mode: external}\n", "external search is not yet supported"},
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

func TestParseNativeSearchMode(t *testing.T) {
	for _, mode := range []string{"native", "none"} {
		document, err := Parse([]byte(example + "search: {mode: " + mode + "}\n"))
		if err != nil || document.Search == nil || document.Search.Mode != mode {
			t.Fatalf("search %s = %+v, %v", mode, document.Search, err)
		}
		encoded, err := document.YAML()
		if err != nil {
			t.Fatal(err)
		}
		parsed, err := Parse(encoded)
		if err != nil || parsed.Search == nil || parsed.Search.Mode != mode {
			t.Fatalf("round trip %s = %+v, %v", mode, parsed.Search, err)
		}
	}
	for _, invalid := range []string{"search: {}", "search: {mode: other}", "search: {mode: native, source: glean}"} {
		if _, err := Parse([]byte(example + invalid + "\n")); err == nil {
			t.Fatalf("accepted %s", invalid)
		}
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

func TestParseOperationalConsentAndSafetyPolicy(t *testing.T) {
	file := example + "rules: {consent: {expireAfter: 6h}}\nsafety: {guard: {holdForReview: true}}\nagents: {personal: {proactive: off}}\n"
	document, err := Parse([]byte(file))
	if err != nil {
		t.Fatal(err)
	}
	if *document.Rules.Consent.ExpireAfter != "6h" || !*document.Safety.Guard.HoldForReview {
		t.Fatalf("policy = %+v %+v", document.Rules, document.Safety)
	}
	if document.Agents == nil || document.Agents.Personal == nil || *document.Agents.Personal.Proactive != "off" {
		t.Fatalf("personal default = %+v", document.Agents)
	}
	encoded, err := document.YAML()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Parse(encoded); err != nil {
		t.Fatalf("policy did not round trip: %v", err)
	}
	for _, invalid := range []string{
		example + "rules: {consent: {expireAfter: 2m}}\n",
		example + "rules: {consent: {expireAfter: 8d}}\n",
		example + "safety: {guard: {unknown: true}}\n",
		example + "agents: {personal: {proactive: digest}}\n",
	} {
		if _, err := Parse([]byte(invalid)); err == nil {
			t.Fatalf("accepted policy %q", invalid)
		}
	}
}

func TestParseToolsPolicyDistinguishesOmittedAndEmptyOffer(t *testing.T) {
	file := example + "tools: {provider: composio, offer: [], employeeConnections: false, autoSubscribe: false, wakeUpWithin: 30m}\n"
	document, err := Parse([]byte(file))
	if err != nil || document.Tools == nil || document.Tools.Offer == nil || len(*document.Tools.Offer) != 0 || document.Tools.EmployeeConnections == nil || *document.Tools.EmployeeConnections || document.Tools.AutoSubscribe == nil || *document.Tools.AutoSubscribe || document.Tools.WakeUpWithin == nil || *document.Tools.WakeUpWithin != "30m" {
		t.Fatalf("tools policy = %+v, %v", document.Tools, err)
	}
	encoded, err := document.YAML()
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := Parse(encoded)
	if err != nil || parsed.Tools == nil || parsed.Tools.Offer == nil || len(*parsed.Tools.Offer) != 0 {
		t.Fatalf("round trip = %+v, %v", parsed.Tools, err)
	}
	for _, invalid := range []string{
		example + "tools: {provider: other}\n",
		example + "tools: {offer: [github, github]}\n",
		example + "tools: {offer: [GitHub]}\n",
		example + "tools: {personal: {github: true}}\n",
		example + "tools: {wakeUpWithin: 91m}\n",
		example + "tools: {wakeUpWithin: 10s}\n",
	} {
		if _, err := Parse([]byte(invalid)); err == nil {
			t.Fatalf("accepted tools policy %q", invalid)
		}
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

func TestTimezoneUsesPortableIANAIdentifiers(t *testing.T) {
	t.Setenv("ZONEINFO", t.TempDir())
	for _, zone := range []string{"UTC", "Asia/Kolkata", "America/New_York"} {
		input := strings.Replace(example, "name: Acme", "name: Acme, timezone: "+zone, 1)
		if _, err := Parse([]byte(input)); err != nil {
			t.Fatalf("portable time zone %s rejected: %v", zone, err)
		}
	}
	for _, zone := range []string{"Local", "Not/AZone"} {
		input := strings.Replace(example, "name: Acme", "name: Acme, timezone: "+zone, 1)
		if _, err := Parse([]byte(input)); err == nil {
			t.Fatalf("host-specific or invalid time zone %s accepted", zone)
		}
	}
}

func TestMetadataDomainsPreservesExplicitEmptyOnRoundTrip(t *testing.T) {
	for _, domains := range []string{"", ", domains: []", ", domains: [acme.com]"} {
		document, err := Parse([]byte("apiVersion: oryxa.in/v1\nkind: Workspace\nmetadata: {organization: acme" + domains + "}\n"))
		if err != nil {
			t.Fatal(err)
		}
		encoded, err := document.YAML()
		if err != nil {
			t.Fatal(err)
		}
		parsed, err := Parse(encoded)
		if err != nil {
			t.Fatal(err)
		}
		if (parsed.Metadata.Domains == nil) != (document.Metadata.Domains == nil) || len(parsed.Metadata.Domains) != len(document.Metadata.Domains) {
			t.Fatalf("domain intent changed: %q -> %s", domains, encoded)
		}
	}
}

func TestNormalizeServiceTextBeforePlanning(t *testing.T) {
	for _, style := range []string{"|", ">"} {
		input := strings.Replace(example, "name: Acme", "name: '  Acme  '", 1) + "agents:\n  organization:\n    displayName: '  Acme  Helper  '\n    description: " + style + "\n      Helpful assistant\n    instructions: " + style + "\n      Coordinate teams.\n"
		document, err := Parse([]byte(input))
		if err != nil {
			t.Fatal(err)
		}
		agent := document.Agents.Organization
		if document.Organization.Name != "Acme" || *agent.DisplayName != "Acme Helper" || *agent.Description != "Helpful assistant" || *agent.Instructions != "Coordinate teams." {
			t.Fatalf("normalization did not mirror services: %+v %+v", document.Organization, agent)
		}
	}
	for _, name := range []string{"''", "'  '", "'" + strings.Repeat("a", 121) + "'"} {
		if _, err := Parse([]byte(strings.Replace(example, "name: Acme", "name: "+name, 1))); err == nil {
			t.Fatalf("invalid org name accepted: %s", name)
		}
	}
}

func TestTeamNamesMirrorServiceNormalization(t *testing.T) {
	for _, name := range []string{"  Engineering  ", strings.Repeat("界", 41)} {
		input := strings.Replace(example, "name: Engineering", "name: '"+name+"'", 1) + "directory: {groups: [{source: 'hr:acme', externalId: team1, team: '" + name + "'}]}\n"
		document, err := Parse([]byte(input))
		if err != nil {
			t.Fatal(err)
		}
		if document.Teams.Items[0].Name != strings.TrimSpace(name) || document.Directory.Groups[0].Team != strings.TrimSpace(name) {
			t.Fatal("team names not normalized")
		}
		encoded, err := document.YAML()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := Parse(encoded); err != nil {
			t.Fatal(err)
		}
	}
	for _, names := range []string{"[{name: Sales}, {name: sales}]", "[{name: \"Bad\\nName\"}]", "[{name: \"Bad\\u202eName\"}]"} {
		if _, err := Parse([]byte("apiVersion: oryxa.in/v1\nkind: Workspace\nmetadata: {organization: acme}\nteams: " + names)); err == nil {
			t.Fatalf("unsafe or duplicate names accepted: %s", names)
		}
	}
}

func TestDirectoryRejectsDuplicateReferencesBeforeResourcesExist(t *testing.T) {
	for _, input := range []string{
		"directory: {identities: [{source: 'hr:acme', externalId: first, member: new@acme.com}, {source: 'hr:acme', externalId: second, member: new@acme.com}]}",
		"directory: {groups: [{source: 'hr:acme', externalId: first, team: New}, {source: 'hr:acme', externalId: second, team: ' new '}]}",
	} {
		if _, err := Parse([]byte(example + input)); err == nil || !strings.Contains(err.Error(), "unique within its source") {
			t.Fatalf("duplicate reference accepted: %v", err)
		}
	}
	if _, err := Parse([]byte(example + "directory: {identities: [{source: 'hr:acme', externalId: first, member: new@acme.com}, {source: 'idp:acme', externalId: second, member: new@acme.com}]}")); err != nil {
		t.Fatalf("independent source mapping rejected: %v", err)
	}
}

func TestNullToolOfferNeverFailsOpen(t *testing.T) {
	for _, offer := range []string{"", "~", "null", "\n    # - gmail"} {
		if _, err := Parse([]byte(example + "tools:\n  offer: " + offer + "\n")); err == nil || !strings.Contains(err.Error(), "use [] for none") {
			t.Fatalf("null offer accepted: %q, %v", offer, err)
		}
	}
}

func TestAnswerGuardPoliciesValidateAndRoundTrip(t *testing.T) {
	input := example + "safety: {guard: {pii: {minimum: {owner: secrets, peer: strict}}, relevance: off}}\n"
	document, err := Parse([]byte(input))
	if err != nil || document.Safety == nil || document.Safety.Guard.PII == nil || *document.Safety.Guard.PII.Minimum.Peer != "strict" || *document.Safety.Guard.Relevance != "off" {
		t.Fatalf("guard = %+v, %v", document.Safety, err)
	}
	encoded, err := document.YAML()
	if err != nil {
		t.Fatal(err)
	}
	again, err := Parse(encoded)
	if err != nil || again.Safety == nil || *again.Safety.Guard.PII.Minimum.Owner != "secrets" || *again.Safety.Guard.Relevance != "off" {
		t.Fatalf("guard after a round trip = %+v, %v", again.Safety, err)
	}
	for _, invalid := range []string{
		"safety: {guard: {pii: {minimum: {peer: off}}}}\n",
		"safety: {guard: {pii: {minimum: {owner: Strict}}}}\n",
		"safety: {guard: {pii: {minimum: {owner: ''}}}}\n",
		"safety: {guard: {pii: {level: strict}}}\n",
		"safety: {guard: {relevance: sometimes}}\n",
		"safety: {guard: {relevance: ''}}\n",
	} {
		if _, err := Parse([]byte(example + invalid)); err == nil {
			t.Fatalf("accepted %s", invalid)
		}
	}
}

func TestWorkspaceRejectsTextDatabaseWouldRefuse(t *testing.T) {
	text := "hello" + string(rune(0)) + "world"
	document := Document{APIVersion: APIVersion, Kind: "Workspace", Metadata: Metadata{Organization: "acme"}, Agents: &Agents{Organization: &OrganizationAgent{Description: &text}}}
	if err := document.Validate(); err == nil {
		t.Fatal("NUL text passed workspace preflight")
	}
}
