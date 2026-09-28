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
	if parsed.Metadata.Organization != "acme" || parsed.Teams[0].Members[0] != "raya@acme.com" {
		t.Fatalf("round trip changed the workspace: %+v", parsed)
	}
}

func TestParseRejectsUnknownAndUnsupportedSettings(t *testing.T) {
	for _, test := range []struct {
		name, content, want string
	}{
		{"unknown nested", strings.Replace(example, "name: Acme", "name: Acme, typo: yes", 1), "typo"},
		{"unknown top level", example + "surprise: true\n", "surprise"},
		{"unsupported section", example + "chat: {slack: {install: add_to_slack}}\n", "chat is not yet supported"},
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
