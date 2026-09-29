package spec

import (
	"strings"
	"testing"
)

func TestSecretScannerRejectsCredentialsWithoutEchoingThem(t *testing.T) {
	for _, secret := range []string{"xoxb-1234567890-secret-token", "ghp_0123456789abcdefghijklmnop", "sk_live_0123456789abcdefghijklmnop", "AKIA1234567890ABCDEFGH", "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiJzZWNyZXQifQ.QwErTyUiOpAsDfGhJkLzXcVb", "A7pQ9eR4tY1uI8oP2aS6dF3gH5jK0lZxCvBnMqWsEdRt"} {
		input := example + "agents: {organization: {instructions: 'Use " + secret + " for access'}}\n"
		_, err := Parse([]byte(input))
		if err == nil || !strings.Contains(err.Error(), "secret_in_manifest") || !strings.Contains(err.Error(), "agents.organization.instructions") || strings.Contains(err.Error(), secret) {
			t.Fatalf("unsafe secret diagnostic: %v", err)
		}
	}
}
func TestSecretScannerAcceptsOpaqueDirectoryIDsAndOrdinaryText(t *testing.T) {
	input := example + "directory: {identities: [{source: 'hr:acme', externalId: 'A7pQ9eR4tY1uI8oP2aS6dF3gH5jK0lZxCvBnMqWsEdRt', member: raya@acme.com}]}\nagents: {organization: {instructions: 'Coordinate the Engineering and Finance teams. Summarize blockers and ask for approval.'}}"
	if _, err := Parse([]byte(input)); err != nil {
		t.Fatal(err)
	}
}

func TestSecretInUnknownFieldNameIsNotEchoedByDecoder(t *testing.T) {
	secret := "ghp_0123456789abcdefghijklmnop"
	_, err := Parse([]byte(example + secret + ": enabled\n"))
	if err == nil || !strings.Contains(err.Error(), "secret_in_manifest") || strings.Contains(err.Error(), secret) {
		t.Fatalf("field name leaked credential: %v", err)
	}
}
