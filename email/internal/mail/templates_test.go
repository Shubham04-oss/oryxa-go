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

// noticeSubjects covers the organization emails, which name the organization
// and roles but carry no link.
var noticeSubjects = map[string]string{
	"role-changed":         "Your role in Acme & Sons changed",
	"member-removed":       "You were removed from Acme & Sons",
	"organization-deleted": "Acme & Sons was deleted",
	"invitation-revoked":   "Your invitation to join Acme & Sons was withdrawn",
}

type noticeData struct {
	OrganizationName string
	Role             string
	PreviousRole     string
}

// billingSubjects covers the emails to an organization's billing contact, which
// link to the app's billing page.
var billingSubjects = map[string]string{
	"payment-failed":     "Payment failed for Acme & Sons's Oryxa subscription",
	"subscription-ended": "Acme & Sons's Oryxa subscription has ended",
	"renewal-upcoming":   "Acme & Sons's Oryxa subscription renews on 20 September 2026",
}

type billingData struct {
	OrganizationName string
	Plan             string
	GraceEndsAt      string
	RenewsOn         string
	Expired          bool
	ActionURL        string
	ActionLabel      string
}

func TestEveryTemplateIsTested(t *testing.T) {
	templates := parseTemplates(t)
	for name := range templates.byName {
		_, action := actionSubjects[name]
		_, notice := noticeSubjects[name]
		_, billing := billingSubjects[name]
		if !action && !notice && !billing && name != "welcome" {
			t.Fatalf("template %q has no test", name)
		}
	}
}

func TestRenderBillingTemplates(t *testing.T) {
	templates := parseTemplates(t)
	data := billingData{
		OrganizationName: "Acme & Sons",
		Plan:             "team",
		RenewsOn:         "20 September 2026",
		ActionURL:        "https://app.example.com/settings/billing?organization_id=org_1",
		ActionLabel:      "Manage billing",
	}

	for name, subject := range billingSubjects {
		t.Run(name, func(t *testing.T) {
			message, err := templates.Render(name, data)
			if err != nil {
				t.Fatalf("Render() error = %v", err)
			}
			if message.Subject != subject {
				t.Fatalf("subject = %q, want %q", message.Subject, subject)
			}
			if !strings.Contains(message.Text, data.ActionURL) || !strings.Contains(message.HTML, `href="https://app.example.com/settings/billing?organization_id=org_1"`) {
				t.Fatalf("bodies lack the billing link:\n%s\n%s", message.Text, message.HTML)
			}
		})
	}

	withGrace := data
	withGrace.GraceEndsAt = "16 September 2026, 12:00 UTC"
	for _, test := range []struct {
		name    string
		data    billingData
		want    string
		notWant string
	}{
		{name: "payment-failed", data: withGrace, want: "by 16 September 2026, 12:00 UTC to keep the plan", notWant: "has lost its plan"},
		{name: "payment-failed", data: data, want: "has lost its plan until the payment method is updated", notWant: "to keep the plan"},
		{name: "subscription-ended", data: data, want: "subscription was cancelled", notWant: "end of its term"},
		{name: "subscription-ended", data: billingData{OrganizationName: "Acme & Sons", Expired: true, ActionURL: data.ActionURL}, want: "reached the end of its term", notWant: "was cancelled"},
		{name: "renewal-upcoming", data: data, want: "to the team plan renews on 20 September 2026"},
		{name: "renewal-upcoming", data: billingData{OrganizationName: "Acme & Sons", RenewsOn: "20 September 2026", ActionURL: data.ActionURL}, want: "subscription renews on 20 September 2026", notWant: " plan renews"},
	} {
		message, err := templates.Render(test.name, test.data)
		if err != nil {
			t.Fatalf("Render(%s) error = %v", test.name, err)
		}
		if !strings.Contains(message.Text, test.want) || (test.notWant != "" && strings.Contains(message.Text, test.notWant)) {
			t.Fatalf("%s text = %q, want %q and not %q", test.name, message.Text, test.want, test.notWant)
		}
	}
}

func TestRenderNoticeTemplates(t *testing.T) {
	templates := parseTemplates(t)
	data := noticeData{OrganizationName: "Acme & Sons", Role: "admin", PreviousRole: "member"}

	for name, subject := range noticeSubjects {
		t.Run(name, func(t *testing.T) {
			message, err := templates.Render(name, data)
			if err != nil {
				t.Fatalf("Render() error = %v", err)
			}
			if message.Subject != subject {
				t.Fatalf("subject = %q, want %q", message.Subject, subject)
			}
			if !strings.Contains(message.Text, "Acme & Sons") || !strings.Contains(message.HTML, "Acme &amp; Sons") {
				t.Fatalf("bodies do not name the organization:\n%s\n%s", message.Text, message.HTML)
			}
			if strings.Contains(message.HTML, "https://") {
				t.Fatalf("notice links somewhere, but its event carries no link:\n%s", message.HTML)
			}
		})
	}

	message, err := templates.Render("role-changed", data)
	if err != nil {
		t.Fatalf("Render(role-changed) error = %v", err)
	}
	if !strings.Contains(message.Text, "from member to admin") {
		t.Fatalf("role change does not say what changed:\n%s", message.Text)
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
