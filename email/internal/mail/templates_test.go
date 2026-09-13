package mail

import (
	"strings"
	"testing"
)

// actionData has the fields every template renders with.
type actionData struct {
	Email            string
	ActionURL        string
	ActionLabel      string
	ExpiresIn        string
	OrganizationName string
	Role             string
}

var sampleAction = actionData{
	Email:            "asha@example.com",
	ActionURL:        "https://app.example.com/action?token=abc&source=email",
	ActionLabel:      "Open",
	ExpiresIn:        "24 hours",
	OrganizationName: "Acme & Sons",
	Role:             "admin",
}

// actionSubjects covers every template built around one of auth's links; the
// welcome email, which has none, is tested on its own.
var actionSubjects = map[string]string{
	"verify-email":   "Confirm your email address",
	"password-reset": "Reset your Oryxa password",
	"invitation":     "You're invited to join Acme & Sons on Oryxa",
}

func TestEveryTemplateIsTested(t *testing.T) {
	templates := parseTemplates(t)
	for name := range templates.byName {
		if _, ok := actionSubjects[name]; !ok && name != "welcome" {
			t.Fatalf("template %q has no test", name)
		}
	}
}

func TestRenderWelcome(t *testing.T) {
	templates := parseTemplates(t)

	message, err := templates.Render("welcome", struct{ Name string }{Name: "Asha"})
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	if message.Subject != "Welcome to Oryxa" || !strings.Contains(message.Text, "Hi Asha,") || !strings.Contains(message.HTML, "Hi Asha,") {
		t.Fatalf("unexpected welcome: %+v", message)
	}

	message, err = templates.Render("welcome", struct{ Name string }{})
	if err != nil {
		t.Fatalf("Render() without a name error = %v", err)
	}
	if !strings.HasPrefix(message.Text, "Hi,\n") {
		t.Fatalf("welcome without a name starts %q, want \"Hi,\"", message.Text)
	}
}

func TestRenderActionTemplates(t *testing.T) {
	templates := parseTemplates(t)

	for name, subject := range actionSubjects {
		t.Run(name, func(t *testing.T) {
			message, err := templates.Render(name, sampleAction)
			if err != nil {
				t.Fatalf("Render() error = %v", err)
			}
			if message.Template != name || message.Subject != subject {
				t.Fatalf("template %q with subject %q, want %q with %q", message.Template, message.Subject, name, subject)
			}
			if !strings.HasPrefix(message.HTML, "<!doctype html>") {
				t.Fatalf("HTML body is not wrapped in the layout:\n%s", message.HTML)
			}
			// The text body carries the link verbatim; HTML escapes it both in
			// the button and in the copyable fallback.
			if !strings.Contains(message.Text, sampleAction.ActionURL) {
				t.Fatalf("text body lacks the action link:\n%s", message.Text)
			}
			if strings.Count(message.HTML, `https://app.example.com/action?token=abc&amp;source=email`) != 3 {
				t.Fatalf("HTML body lacks the escaped link in the button, fallback href and fallback text:\n%s", message.HTML)
			}
			if !strings.Contains(message.Text, "expires in 24 hours") || !strings.Contains(message.HTML, "expires in 24 hours") {
				t.Fatalf("bodies do not say when the link expires:\n%s\n%s", message.Text, message.HTML)
			}
		})
	}
}

func TestRenderEscapesOnlyHTML(t *testing.T) {
	templates := parseTemplates(t)
	data := sampleAction
	data.OrganizationName = "<b>Acme</b>"

	message, err := templates.Render("invitation", data)
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	if strings.Contains(message.HTML, "<b>Acme</b>") || !strings.Contains(message.HTML, "&lt;b&gt;Acme&lt;/b&gt;") {
		t.Fatalf("HTML body did not escape the organization name:\n%s", message.HTML)
	}
	if !strings.Contains(message.Text, "join <b>Acme</b> on Oryxa") {
		t.Fatalf("text body changed the organization name:\n%s", message.Text)
	}
}

func TestRenderNeutralisesUnsafeLinks(t *testing.T) {
	templates := parseTemplates(t)
	data := sampleAction
	data.ActionURL = "javascript:alert(1)"

	message, err := templates.Render("verify-email", data)
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	if strings.Contains(message.HTML, `href="javascript:`) {
		t.Fatalf("HTML body links to a javascript: URL:\n%s", message.HTML)
	}
}

func TestRenderKeepsSubjectOnOneLine(t *testing.T) {
	templates := parseTemplates(t)
	data := sampleAction
	data.OrganizationName = "Acme\r\nBcc: someone@example.com"

	message, err := templates.Render("invitation", data)
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	if strings.ContainsAny(message.Subject, "\r\n") {
		t.Fatalf("subject %q spans lines", message.Subject)
	}
}

func TestRenderUnknownTemplate(t *testing.T) {
	templates := parseTemplates(t)

	if _, err := templates.Render("missing", nil); err == nil {
		t.Fatal("Render() error = nil, want unknown template error")
	}
}

func parseTemplates(t *testing.T) *Templates {
	t.Helper()
	templates, err := ParseTemplates()
	if err != nil {
		t.Fatalf("ParseTemplates() error = %v", err)
	}
	return templates
}
