package consentguard

import (
	"slices"
	"strings"
	"testing"
)

// join builds a value from its parts. The fixtures below are shaped like the
// credentials they stand for, so they are never written whole in this file:
// nothing here is a secret, and nothing that scans source for secrets has
// reason to think so.
func join(parts ...string) string { return strings.Join(parts, "") }

func TestEveryCredentialIsMaskedByItsKind(t *testing.T) {
	slackBot := join("xo", "xb-", "123456789012-", "1234567890123-", "abcdefghijklmnopqrstuvwx")
	slackUser := join("xo", "xp-", "123456789012-", "123456789012-", "1234567890123-", "0123456789abcdef0123456789abcdef")
	slackApp := join("xa", "pp-", "1-A0123456789-", "1234567890123-", "abcdef0123456789abcdef0123456789")
	slackConfig := join("xo", "xe-", "1-", "abcdefghijklmnopqrstuvwxyz0123456789")
	slackRotated := join("xo", "xe.xo", "xp-", "1-", "abcdefghijklmnopqrstuvwxyz0123456789")
	slackSession := join("xo", "xd-", "abcdefghijklmnopqrstuvwxyz0123456789")
	webhook := join("https://hooks.", "slack.com/services/", "T00000000/", "B00000000/", strings.Repeat("Z", 24))
	awsKey := join("AK", "IA", "ABCDEFGHIJKLMNOP")
	awsSecret := strings.Repeat("aB3/", 10)
	githubToken := join("gh", "p_", strings.Repeat("aB3dE", 8))
	githubOAuth := join("gh", "o_", strings.Repeat("xY7zQ", 8))
	githubFineGrained := join("github", "_pat_", strings.Repeat("aB3dE_", 8))
	stripeLive := join("s", "k_live_", strings.Repeat("aB3dE", 5))
	stripeRestricted := join("r", "k_live_", strings.Repeat("aB3dE", 5))
	modelKey := join("s", "k-", "proj-", strings.Repeat("aB3dE", 8))
	googleKey := join("AI", "za", strings.Repeat("aB3dE", 7))
	npmToken := join("np", "m_", strings.Repeat("aB3dE", 7), "a")
	jwt := join("ey", "JhbGciOiJIUzI1NiJ9.", "ey", "JzdWIiOiIxMjM0NTY3ODkwIn0.", "abcdefghijklmnop")
	googleAccess := join("ya", "29.", strings.Repeat("aB3dE", 8))
	privateKey := join("-----BEGIN ", "RSA PRIVATE KEY-----\nMIIEowIBAAKCAQEA0123456789\nabcdefghijklmnop\n-----END ", "RSA PRIVATE KEY-----")

	for _, testCase := range []struct {
		name   string
		text   string
		secret string
		kind   string
	}{
		{"a bot token", "the token is " + slackBot + " for the app", slackBot, KindSlackToken},
		{"a user token", "use " + slackUser, slackUser, KindSlackToken},
		{"an app-level token", "socket mode needs " + slackApp, slackApp, KindSlackToken},
		{"a config token", "config: " + slackConfig, slackConfig, KindSlackToken},
		{"a rotated token, whole", "refreshed to " + slackRotated + ".", slackRotated, KindSlackToken},
		{"a session token", "cookie d=" + slackSession, slackSession, KindSlackToken},
		{"an incoming webhook", "post to " + webhook + " to notify", webhook, KindSlackToken},
		{"an AWS access key id", "AWS_ACCESS_KEY_ID=" + awsKey, awsKey, KindAWSAccessKey},
		{"an AWS secret key", "aws_secret_access_key = " + awsSecret, awsSecret, KindAWSSecretKey},
		{"a GitHub token", "clone with " + githubToken, githubToken, KindAPIKey},
		{"a GitHub OAuth token", "gh auth " + githubOAuth, githubOAuth, KindAPIKey},
		{"a fine-grained GitHub token", githubFineGrained, githubFineGrained, KindAPIKey},
		{"a Stripe key", "billing uses " + stripeLive, stripeLive, KindAPIKey},
		{"a restricted Stripe key", stripeRestricted, stripeRestricted, KindAPIKey},
		{"a model provider key", "OPENAI: " + modelKey, modelKey, KindAPIKey},
		{"a Google key", "maps key " + googleKey, googleKey, KindAPIKey},
		{"an npm token", "publish with " + npmToken, npmToken, KindAPIKey},
		{"a JSON web token", "session " + jwt + " expires", jwt, KindAccessToken},
		{"a Google access token", "Authorization uses " + googleAccess, googleAccess, KindAccessToken},
		{"a bearer token", "Authorization: Bearer " + strings.Repeat("aB3dE", 6), strings.Repeat("aB3dE", 6), KindAccessToken},
		{"a private key", "the key:\n" + privateKey + "\nkeep it safe", privateKey, KindPrivateKey},
		{"a key cut off", join("-----BEGIN ", "PRIVATE KEY-----\nMIIEvQIBADANBgkqhkiG9w0BAQEFAASC"), join("-----BEGIN ", "PRIVATE KEY-----\nMIIEvQIBADANBgkqhkiG9w0BAQEFAASC"), KindPrivateKey},
		{"an api key assigned", "api_key: abcdef0123456789abcdef", "abcdef0123456789abcdef", KindAPIKey},
		{"a client secret assigned in quotes", `"client_secret": "0123456789abcdef0123456789"`, "0123456789abcdef0123456789", KindAPIKey},
		{"a signing secret in an environment variable", "SLACK_SIGNING_SECRET=0123456789abcdef0123456789abcdef", "0123456789abcdef0123456789abcdef", KindAPIKey},
		{"a token in an environment variable", "GITHUB_TOKEN: abcdef0123456789abcdef0123", "abcdef0123456789abcdef0123", KindAPIKey},
		{"a password in an environment variable", "DB_PASSWORD=correcthorsebattery", "correcthorsebattery", KindPassword},
		{"a password assigned", "password = hunter2hunter2", "hunter2hunter2", KindPassword},
		{"a password after a colon", "Password: Winter2024!x", "Winter2024!x", KindPassword},
		{"a password in an address", "postgres://app:s3cretpassw0rd@db.internal:5432/app", "s3cretpassw0rd", KindPassword},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			masked, kinds := maskCredentials(testCase.text)
			if strings.Contains(masked, testCase.secret) {
				t.Fatalf("the secret is still in %q", masked)
			}
			if !strings.Contains(masked, "{"+testCase.kind+"}") {
				t.Fatalf("%q does not say what was masked, {%s}", masked, testCase.kind)
			}
			if !slices.Contains(kinds, testCase.kind) {
				t.Fatalf("kinds = %v, want %s among them", kinds, testCase.kind)
			}
			// Only the secret is replaced: what was around it is left as it was.
			if want := strings.Replace(testCase.text, testCase.secret, "{"+testCase.kind+"}", 1); masked != want {
				t.Fatalf("masked = %q, want %q", masked, want)
			}
		})
	}
}

func TestTextWithNoCredentialIsLeftAsItWas(t *testing.T) {
	for name, text := range map[string]string{
		"nothing":                    "",
		"plain words":                "Deployments run every Tuesday at nine.",
		"a prefix alone":             "Slack's bot tokens start with xoxb- and app tokens with xapp-.",
		"a word after token":         "the token: expired, so we asked again",
		"a policy":                   "The password policy: rotate every 90 days. Secrets are kept in the vault.",
		"a name without a value":     "set the api_key and the password before you deploy",
		"a short value":              "token = abc",
		"a library":                  "we use sk-learn's cousins and the ghp tool",
		"a hash":                     "commit 3f786850e387550fdab836ed7e6dc881de23001b fixed it",
		"an id":                      "channel C0123456789, user U0123456789, team T0123456789",
		"an address without a login": "https://example.com/docs",
	} {
		masked, kinds := maskCredentials(text)
		if masked != text || len(kinds) != 0 {
			t.Errorf("%s: %q became %q (%v)", name, text, masked, kinds)
		}
	}
}

// A value Bedrock already masked is no credential the second time.
func TestAMaskedCredentialIsNotMaskedAgain(t *testing.T) {
	for _, text := range []string{"password: {PASSWORD}", "aws_secret_access_key = {AWS_SECRET_KEY}", "the key is {AWS_ACCESS_KEY} and {SLACK_TOKEN}"} {
		if masked, kinds := maskCredentials(text); masked != text || len(kinds) != 0 {
			t.Errorf("%q became %q (%v)", text, masked, kinds)
		}
	}
	once, _ := maskCredentials("password = hunter2hunter2 and " + join("gh", "p_", strings.Repeat("aB3dE", 8)))
	if twice, kinds := maskCredentials(once); twice != once || len(kinds) != 0 {
		t.Errorf("masking again changed %q into %q (%v)", once, twice, kinds)
	}
}

func TestEveryTextIsStrippedAndTheKindsAreSortedOnceEach(t *testing.T) {
	token := join("xo", "xb-", "123456789012-", "1234567890123-", "abcdefghijklmnopqrstuvwx")
	given := []string{"the token " + token, "fine", "a password = hunter2hunter2 and another " + token}
	stripped, kinds := Strip(given)
	if !slices.Equal(kinds, []string{KindPassword, KindSlackToken}) {
		t.Errorf("kinds = %v, want PASSWORD and SLACK_TOKEN once each, sorted", kinds)
	}
	if len(stripped) != 3 || stripped[1] != "fine" || strings.Contains(strings.Join(stripped, " "), token) || strings.Contains(stripped[2], "hunter2") {
		t.Errorf("stripped = %q", stripped)
	}
	if !strings.Contains(given[0], token) {
		t.Errorf("the texts given were changed: %q", given)
	}
	if untouched, none := Strip([]string{"a", "b"}); len(none) != 0 || !slices.Equal(untouched, []string{"a", "b"}) {
		t.Errorf("Strip of clean texts = %q, %v", untouched, none)
	}
}
