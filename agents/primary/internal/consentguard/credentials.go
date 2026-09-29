package consentguard

import (
	"regexp"
	"slices"
	"strings"
)

// The kinds of credential that are masked, spelled as the answer guard spells
// the ones Bedrock masks (answerguard.MaskedKinds) where they are the same:
// AWS_ACCESS_KEY, AWS_SECRET_KEY and PASSWORD. The rest are named for what they
// are. A masked credential is replaced by its kind in braces, as {SLACK_TOKEN}.
const (
	KindSlackToken   = "SLACK_TOKEN"
	KindAWSAccessKey = "AWS_ACCESS_KEY"
	KindAWSSecretKey = "AWS_SECRET_KEY"
	KindAPIKey       = "API_KEY"
	KindAccessToken  = "ACCESS_TOKEN"
	KindPrivateKey   = "PRIVATE_KEY"
	KindPassword     = "PASSWORD"
)

// credential is one shape of credential: what it is called, how to find it, and
// which part of a match is the secret when the rest is only what gave it away.
type credential struct {
	kind    string
	pattern *regexp.Regexp
	// group is the submatch that is the secret; 0 is the whole match.
	group int
}

// credentials are fixed shapes: a key or token with a prefix its issuer gave
// it, and the assignment of a value to a name that says it is one. They stand
// beside Bedrock's detectors, which the answer guard has already run and which
// know nothing of Slack's tokens, and are read in this order. What each looks
// for is narrow on purpose: the value after "password is" in a sentence is
// Bedrock's to find, and a word after "token:" is no key.
var credentials = []credential{
	// Slack: bot, user, app-level and rotation tokens, session tokens, and the
	// incoming webhook addresses, which are secrets in their own right.
	{KindSlackToken, regexp.MustCompile(`\bxoxe\.xox[a-z]-[A-Za-z0-9-]{8,}`), 0},
	{KindSlackToken, regexp.MustCompile(`\bxox[a-z]-[A-Za-z0-9-]{8,}`), 0},
	{KindSlackToken, regexp.MustCompile(`\bxapp-[A-Za-z0-9-]{8,}`), 0},
	{KindSlackToken, regexp.MustCompile(`https?://hooks\.slack\.com/(?:services|workflows|triggers)/[A-Za-z0-9/_-]+`), 0},

	// AWS.
	{KindAWSAccessKey, regexp.MustCompile(`\b(?:AKIA|ASIA|AIDA|AROA|AGPA|AIPA|ANPA|ANVA|APKA)[0-9A-Z]{16}\b`), 0},
	{KindAWSSecretKey, regexp.MustCompile(`(?i)\baws[\w .-]{0,24}secret[\w .-]{0,24}["']?\s*[:=]\s*["']?([A-Za-z0-9/+=]{40})`), 1},

	// Keys of code hosts, payment providers, model providers, Google and package
	// registries.
	{KindAPIKey, regexp.MustCompile(`\bgh[pousr]_[A-Za-z0-9]{30,}`), 0},
	{KindAPIKey, regexp.MustCompile(`\bgithub_pat_[A-Za-z0-9_]{30,}`), 0},
	{KindAPIKey, regexp.MustCompile(`\b[sr]k_(?:live|test)_[A-Za-z0-9]{16,}`), 0},
	{KindAPIKey, regexp.MustCompile(`\bwhsec_[A-Za-z0-9]{16,}`), 0},
	{KindAPIKey, regexp.MustCompile(`\bsk-[A-Za-z0-9_-]{20,}`), 0},
	{KindAPIKey, regexp.MustCompile(`\bAIza[0-9A-Za-z_-]{35}`), 0},
	{KindAPIKey, regexp.MustCompile(`\bGOCSPX-[0-9A-Za-z_-]{20,}`), 0},
	{KindAPIKey, regexp.MustCompile(`\bnpm_[A-Za-z0-9]{36}`), 0},
	{KindAPIKey, regexp.MustCompile(`\bSG\.[A-Za-z0-9_-]{16,}\.[A-Za-z0-9_-]{16,}`), 0},

	// Tokens that carry access: JSON web tokens, Google's OAuth access tokens
	// and whatever follows "Bearer".
	{KindAccessToken, regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{8,}\.eyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}`), 0},
	{KindAccessToken, regexp.MustCompile(`\bya29\.[0-9A-Za-z_-]{20,}`), 0},
	{KindAccessToken, regexp.MustCompile(`(?i)\bbearer\s+([A-Za-z0-9._~+/-]{20,}=*)`), 1},

	// A private key, to the end of its block, or of the text when it is cut off.
	{KindPrivateKey, regexp.MustCompile(`-----BEGIN [A-Z0-9 ]*PRIVATE KEY(?: BLOCK)?-----[\s\S]*?(?:-----END [A-Z0-9 ]*PRIVATE KEY(?: BLOCK)?-----|$)`), 0},

	// A value assigned, with ":" or "=", to a name that says it is a secret: in
	// YAML, in JSON, or as an environment variable, where the name may have more
	// before it and after it (SLACK_SIGNING_SECRET, GITHUB_TOKEN, DB_PASSWORD).
	{KindAPIKey, regexp.MustCompile(`(?i)\b[\w.-]*(?:api[_-]?key|access[_-]?key|secret[_-]?key|client[_-]?secret|signing[_-]?secret|auth[_-]?token|access[_-]?token|refresh[_-]?token|private[_-]?key|secret|token)[\w-]*["']?\s*[:=]\s*["']?([A-Za-z0-9/+_.=~-]{16,})`), 1},
	{KindPassword, regexp.MustCompile(`(?i)\b[\w.-]*(?:password|passwd|passphrase)[\w-]*["']?\s*[:=]\s*["']?([^\s"'<>]{8,})`), 1},
	// The password in an address: scheme://user:password@host.
	{KindPassword, regexp.MustCompile(`(?i)\b[a-z][a-z0-9+.\-]*://[^\s/:@]+:([^\s/@]+)@`), 1},
}

// placeholder is a value that already is a masked kind, as {PASSWORD} is once
// Bedrock has masked it. Masking it again would only claim to have found it.
var placeholder = regexp.MustCompile(`^\{[A-Z_]+\}$`)

// Strip is texts with every credential masked, and the kinds that were found,
// sorted and never a value. With none found, the texts are as given. It is what
// Check begins with, and what every other version of an answer bound for Slack
// goes through, such as the fully masked one an answer held for review has beside
// the one Check read, so that none is sent with a credential in it.
func Strip(texts []string) ([]string, []string) {
	stripped := slices.Clone(texts)
	var kinds []string
	for index, text := range stripped {
		var found []string
		stripped[index], found = maskCredentials(text)
		kinds = append(kinds, found...)
	}
	slices.Sort(kinds)
	return stripped, slices.Compact(kinds)
}

// maskCredentials is text with every credential masked, and the kinds found.
func maskCredentials(text string) (string, []string) {
	var kinds []string
	for _, shape := range credentials {
		var masked bool
		if text, masked = shape.mask(text); masked {
			kinds = append(kinds, shape.kind)
		}
	}
	return text, kinds
}

// mask is text with the secrets of this shape replaced by its kind, and whether
// there were any.
func (shape credential) mask(text string) (string, bool) {
	matches := shape.pattern.FindAllStringSubmatchIndex(text, -1)
	if len(matches) == 0 {
		return text, false
	}
	var masked strings.Builder
	last, found := 0, false
	for _, match := range matches {
		start, end := match[0], match[1]
		if shape.group > 0 {
			start, end = match[2*shape.group], match[2*shape.group+1]
		}
		if start < 0 || placeholder.MatchString(text[start:end]) {
			continue
		}
		masked.WriteString(text[last:start])
		masked.WriteString("{" + shape.kind + "}")
		last, found = end, true
	}
	if !found {
		return text, false
	}
	masked.WriteString(text[last:])
	return masked.String(), true
}
