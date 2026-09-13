package mail

import (
	"strings"
	"testing"
)

func TestFormatSMTPMessageIncludesSubjectAndBodies(t *testing.T) {
	raw := string(formatSMTPMessage(`"Oryxa" <no-reply@example.com>`, "asha@example.com", Email{
		To: "asha@example.com",
		Message: Message{
			Template: "team-called",
			Subject:  "Engineering was called in Acme",
			HTML:     "<p>Engineering was called</p>",
			Text:     "Engineering was called\n",
		},
	}))
	for _, want := range []string{
		"From: \"Oryxa\" <no-reply@example.com>",
		"To: asha@example.com",
		"Subject: Engineering was called in Acme",
		"Engineering was called",
		"<p>Engineering was called</p>",
	} {
		if !strings.Contains(raw, want) {
			t.Fatalf("message missing %q:\n%s", want, raw)
		}
	}
}
