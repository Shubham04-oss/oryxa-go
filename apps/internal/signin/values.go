package signin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/netip"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/Shubham04-oss/oryxa-go/apps/internal/integration"
	"github.com/Shubham04-oss/oryxa-go/internal/secrets"
)

// The resource types values are sealed for. A value's resource id is its
// row's id, a slash and the value's name, so a sealed value cannot be moved
// to another row or to another value of the same row.
const (
	resourceCredential  = "credential"
	resourceOAuthClient = "oauth-client"
)

// The names a credential's OAuth tokens are sealed under. A field's name
// never holds a dot, so they cannot collide with one.
const (
	sealedAccessToken  = "oauth.access_token"
	sealedRefreshToken = "oauth.refresh_token"
)

// SettingRefuseDefault, set to "true" in an organization's OAuth client
// settings, refuses Oryxa's default client for that registration. A field's
// name never holds a dot, so it cannot collide with one.
const SettingRefuseDefault = "oryxa.refuse_default"

// secretClientField is the name an OAuth client's secret is sealed under.
const secretClientField = "client_secret"

func credentialBinding(connectionID, name string) secrets.Binding {
	return secrets.Binding{ResourceType: resourceCredential, ResourceID: connectionID + "/" + name}
}

func clientBinding(clientRowID, name string) secrets.Binding {
	return secrets.Binding{ResourceType: resourceOAuthClient, ResourceID: clientRowID + "/" + name}
}

// sealEach seals every value on its own, bound by bind.
func (s *Service) sealEach(ctx context.Context, bind func(name string) secrets.Binding, values map[string]secrets.Secret) (map[string]secrets.SealedData, error) {
	sealed := make(map[string]secrets.SealedData, len(values))
	for _, name := range slices.Sorted(maps.Keys(values)) {
		plaintext := []byte(values[name].Reveal())
		envelope, err := s.sealer.Seal(ctx, bind(name), plaintext)
		secrets.Wipe(plaintext)
		if err != nil {
			return nil, fmt.Errorf("signin: seal %s: %w", name, err)
		}
		sealed[name] = envelope
	}
	return sealed, nil
}

// open opens one sealed value.
func (s *Service) open(ctx context.Context, binding secrets.Binding, sealed secrets.SealedData) (secrets.Secret, error) {
	plaintext, err := s.sealer.Open(ctx, binding, sealed)
	if err != nil {
		return "", fmt.Errorf("signin: open a sealed %s value: %w", binding.ResourceType, err)
	}
	value := secrets.Secret(plaintext)
	secrets.Wipe(plaintext)
	return value, nil
}

// standardField is one of a credential kind's own fields, which the core
// holds and a manifest never declares.
type standardField struct {
	name   string
	secret bool
}

var standardFields = map[string][]standardField{
	integration.KindAPIToken:                {{"token", true}},
	integration.KindBasic:                   {{"username", false}, {"password", true}},
	integration.KindOAuth2ClientCredentials: {{"client_id", false}, {"client_secret", true}},
}

// maxFieldLength bounds a value whose field sets no max_length.
const maxFieldLength = 4096

var (
	uuidPattern      = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
	hostLabelPattern = regexp.MustCompile(`^[a-zA-Z0-9]([a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?$`)
	fieldNamePattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,31}$`)
)

// memberFields checks what a member typed for a method: every standard field
// of its kind, and its connection-level fields. Discovered and client-level
// fields are not the member's to type, and any other name is refused. It
// answers the values that are not secret and the ones that are, apart.
func memberFields(method integration.Method, typed map[string]string) (map[string]string, map[string]secrets.Secret, error) {
	settings := map[string]string{}
	secretValues := map[string]secrets.Secret{}
	accepted := map[string]bool{}
	for _, standard := range standardFields[method.Kind] {
		accepted[standard.name] = true
		value := strings.TrimSpace(typed[standard.name])
		if value == "" {
			return nil, nil, invalidField(standard.name, "is required")
		}
		if rule := checkValue(integration.Validation{}, value); rule != "" {
			return nil, nil, invalidField(standard.name, rule)
		}
		if standard.secret {
			secretValues[standard.name] = secrets.Secret(value)
		} else {
			settings[standard.name] = value
		}
	}
	for _, field := range method.Fields {
		if field.Level != integration.LevelConnection {
			continue
		}
		accepted[field.Name] = true
		value := strings.TrimSpace(typed[field.Name])
		if value == "" {
			if field.Validate.Required {
				return nil, nil, invalidField(field.Name, "is required")
			}
			continue
		}
		if rule := checkValue(field.Validate, value); rule != "" {
			return nil, nil, invalidField(field.Name, rule)
		}
		if field.Secret {
			secretValues[field.Name] = secrets.Secret(value)
		} else {
			settings[field.Name] = value
		}
	}
	for _, name := range slices.Sorted(maps.Keys(typed)) {
		if accepted[name] {
			continue
		}
		if !fieldNamePattern.MatchString(name) {
			// A name that is not a field's could be anything, a pasted value
			// among them, so it is not repeated back.
			return nil, nil, invalidField("", "names a field this app does not take")
		}
		return nil, nil, invalidField(name, "is not a field this app takes")
	}
	return settings, secretValues, nil
}

// checkValue answers the rule a value breaks, or "". The rule never quotes
// the value.
func checkValue(validation integration.Validation, value string) string {
	limit := validation.MaxLen
	if limit <= 0 {
		limit = maxFieldLength
	}
	switch {
	case !utf8.ValidString(value) || strings.ContainsFunc(value, unicode.IsControl):
		return "must be one line of text"
	case utf8.RuneCountInString(value) > limit:
		return fmt.Sprintf("must be at most %d characters", limit)
	case len(validation.Enum) > 0 && !slices.Contains(validation.Enum, value):
		return "must be one of " + strings.Join(validation.Enum, ", ")
	}
	if validation.Format != "" && !validFormat(validation.Format, value) {
		return "must be " + formatName(validation.Format)
	}
	if pattern, err := validation.Regexp(); err != nil || (pattern != nil && !pattern.MatchString(value)) {
		return "is not in the form this app expects"
	}
	return ""
}

func formatName(format string) string {
	switch format {
	case integration.FormatHostname:
		return "a hostname"
	case integration.FormatHTTPSURL:
		return "an https URL"
	case integration.FormatUUID:
		return "a UUID"
	case integration.FormatEmail:
		return "an email address"
	}
	return "valid"
}

func validFormat(format, value string) bool {
	switch format {
	case integration.FormatHostname:
		return validHostname(value)
	case integration.FormatHTTPSURL:
		return validHTTPSURL(value)
	case integration.FormatUUID:
		return uuidPattern.MatchString(value)
	case integration.FormatEmail:
		local, domain, found := strings.Cut(value, "@")
		return found && local != "" && len(local) <= 64 && !strings.ContainsAny(local, " @<>\"") && validHostname(domain)
	}
	return false
}

// validHostname accepts a DNS name of two labels or more: never an address,
// and never a single label such as localhost.
func validHostname(value string) bool {
	if len(value) > 253 {
		return false
	}
	if _, err := netip.ParseAddr(value); err == nil {
		return false
	}
	labels := strings.Split(value, ".")
	if len(labels) < 2 {
		return false
	}
	for _, label := range labels {
		if !hostLabelPattern.MatchString(label) {
			return false
		}
	}
	// The last label is never all digits, or the name reads as an address.
	_, err := strconv.Atoi(labels[len(labels)-1])
	return err != nil
}

// validHTTPSURL accepts an absolute https URL on a hostname, with no
// credentials, query or fragment: what may lead one of a manifest's URLs.
func validHTTPSURL(value string) bool {
	parsed, err := url.Parse(value)
	return err == nil && parsed.Scheme == "https" && parsed.Opaque == "" && parsed.User == nil &&
		parsed.RawQuery == "" && !parsed.ForceQuery && !strings.Contains(value, "#") && validHostname(parsed.Hostname())
}

var errMissingValue = errors.New("signin: a URL names a field that has no value")

// fillURL fills one of a manifest's absolute URLs from values. A placeholder
// it begins with stands for a whole https URL, whose field's format checked
// it, and goes in as it is; any other is escaped for where it stands. The
// answer must still be an absolute https URL on a host.
func fillURL(template string, values map[string]string) (*url.URL, error) {
	filled, err := fill(template, values, true)
	if err != nil {
		return nil, err
	}
	parsed, err := url.Parse(filled)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil {
		return nil, errors.New("signin: a filled URL is not an absolute https URL")
	}
	return parsed, nil
}

// fill replaces each {name} in template with its value: escaped as a path
// segment before any ?, as a query value after one. With lead, a placeholder
// at the very start goes in as it is, less a trailing slash.
func fill(template string, values map[string]string, lead bool) (string, error) {
	var out strings.Builder
	inQuery := false
	rest := template
	for rest != "" {
		open := strings.IndexByte(rest, '{')
		if open < 0 {
			out.WriteString(rest)
			break
		}
		literal := rest[:open]
		inQuery = inQuery || strings.Contains(literal, "?")
		out.WriteString(literal)
		end := strings.IndexByte(rest[open:], '}')
		if end < 0 {
			return "", errors.New("signin: a URL has an unbalanced brace")
		}
		name := rest[open+1 : open+end]
		value := values[name]
		if value == "" {
			return "", fmt.Errorf("%w: {%s}", errMissingValue, name)
		}
		switch {
		case lead && out.Len() == 0:
			out.WriteString(strings.TrimRight(value, "/"))
		case inQuery:
			out.WriteString(url.QueryEscape(value))
		default:
			out.WriteString(url.PathEscape(value))
		}
		rest = rest[open+end+1:]
	}
	return out.String(), nil
}

// under answers the URL of path beneath base: base's own path, then path,
// which begins with a / and may carry a query.
func under(base *url.URL, path string) (*url.URL, error) {
	joined := strings.TrimRight(base.String(), "/") + path
	parsed, err := url.Parse(joined)
	if err != nil || parsed.Scheme != base.Scheme || parsed.Host != base.Host {
		return nil, errors.New("signin: a path does not stay under api_base")
	}
	return parsed, nil
}

// pointer resolves an RFC 6901 JSON pointer in a decoded JSON document.
func pointer(document any, jsonPointer string) (any, bool) {
	if !strings.HasPrefix(jsonPointer, "/") {
		return nil, false
	}
	current := document
	for _, token := range strings.Split(jsonPointer[1:], "/") {
		token = strings.ReplaceAll(strings.ReplaceAll(token, "~1", "/"), "~0", "~")
		switch node := current.(type) {
		case map[string]any:
			next, ok := node[token]
			if !ok {
				return nil, false
			}
			current = next
		case []any:
			index, err := strconv.Atoi(token)
			if err != nil || index < 0 || index >= len(node) || (len(token) > 1 && token[0] == '0') {
				return nil, false
			}
			current = node[index]
		default:
			return nil, false
		}
	}
	return current, true
}

// scalar answers a JSON value that is a string, a number or a boolean as a
// string, and false for anything else, or an empty string.
func scalar(value any, found bool) (string, bool) {
	if !found {
		return "", false
	}
	switch value := value.(type) {
	case string:
		return value, value != ""
	case json.Number:
		return value.String(), true
	case float64:
		return strconv.FormatFloat(value, 'f', -1, 64), true
	case int64:
		return strconv.FormatInt(value, 10), true
	case int:
		return strconv.Itoa(value), true
	case bool:
		return strconv.FormatBool(value), true
	}
	return "", false
}

// merged answers the union of maps, later ones winning.
func merged(sources ...map[string]string) map[string]string {
	out := map[string]string{}
	for _, source := range sources {
		maps.Copy(out, source)
	}
	return out
}

// hint is what may be shown of a secret: its last four characters, when it
// is long enough that they give nothing away.
func hint(secret secrets.Secret) string {
	value := []rune(secret.Reveal())
	if len(value) < 16 {
		return ""
	}
	return string(value[len(value)-4:])
}
