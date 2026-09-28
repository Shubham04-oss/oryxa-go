// Package serviceconfig reads apps' configuration from the environment.
package serviceconfig

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/Shubham04-oss/oryxa-go/apps/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/internal/secrets"
	"github.com/Shubham04-oss/oryxa-go/internal/serviceauth"
)

// SigninCallbackPath is the path of Oryxa's OAuth callback, which
// APPS_SIGNIN_REDIRECT_URI must name.
const SigninCallbackPath = "/v1/apps/oauth/callback"

// Config is what apps reads for itself. The listener, the database, the AWS
// region and the event bus are the host's.
type Config struct {
	AuthURL string
	// ComposioAPIKey is the project API key. It reaches every user in the
	// project, so it lives only here.
	ComposioAPIKey  string
	ComposioBaseURL string
	// ComposioWebhookSecret verifies Composio's deliveries. Without it every
	// delivery is refused.
	ComposioWebhookSecret string
	// PublicBaseURL is where a member's browser and Composio reach this
	// process; the connect callback and the webhook URL are built on it.
	PublicBaseURL string
	// AllowedOrigins may send cookie-authenticated mutations, and are where a
	// member's browser may be sent back to after connecting an app.
	AllowedOrigins []string
	// EventBridgeEndpoint and SQSEndpoint override where those AWS clients
	// call, for a local emulator.
	EventBridgeEndpoint string
	SQSEndpoint         string
	// EventsQueueURL is the queue auth's events arrive on. Without it apps
	// does not learn that a member left or an organization was deleted.
	EventsQueueURL string
	// NoAutoSubscribe stops a newly connected app from being subscribed to
	// everything it can deliver. Connecting subscribes by default; set
	// APPS_AUTO_SUBSCRIBE=false for a deployment that would rather members
	// chose their own events. Parsed as a boolean (strconv.ParseBool, so
	// true/false/1/0/T/F and case-insensitive variants); unset means on, and
	// anything else refuses to start rather than silently keep the default.
	NoAutoSubscribe bool

	// OryxaToolkits are the toolkits Oryxa's own sign-in is switched on for:
	// APPS_ORYXA_TOOLKITS, a comma list. Empty means it is off, and every
	// Oryxa request is refused.
	OryxaToolkits []string
	// SigninKMSKeyID is the KMS key Oryxa's sign-in seals credentials and
	// webhook secrets with, bound to service oryxa-apps:
	// APPS_SIGNIN_KMS_KEY_ID. Required once a toolkit is switched on.
	SigninKMSKeyID string
	// SigninRedirectURI is the one OAuth callback registered with every app:
	// APPS_SIGNIN_REDIRECT_URI, {origin}/v1/apps/oauth/callback. The state
	// cookie comes back only to the host that set it, so it must be the host
	// the browser sends the connect request to: the web app's own origin
	// where the web app calls apps through it (app.oryxa.in/v1/…).
	SigninRedirectURI string
	// HookSecrets are the deployment's webhook secrets, by toolkit: the
	// current one, and during a rotation the previous one too.
	// APPS_HOOK_SECRETS, JSON {"slack": ["current", "previous"]}.
	HookSecrets map[string][]secrets.Secret
	// HookValues are the configuration a manifest names as {config:name}:
	// APPS_HOOK_VALUES, JSON {"pubsub_push_account": "…"}.
	HookValues map[string]string
	// ServiceCallers are the services that may call apps' internal routes:
	// APPS_SERVICE_CALLERS, JSON as serviceauth.ParseCallers reads it.
	// Without them there are no internal routes.
	ServiceCallers []serviceauth.Caller
	// DefaultOAuthClients are Oryxa's own OAuth apps, which members sign in
	// through unless their organization brings its own:
	// APPS_DEFAULT_OAUTH_CLIENTS, JSON {"github": {"client_id": "…",
	// "client_secret": "…"}}. apps saves them, sealed, once it starts.
	DefaultOAuthClients []DefaultOAuthClient
}

// DefaultOAuthClient is one of Oryxa's own OAuth apps.
type DefaultOAuthClient struct {
	// Client is the registration it serves: github, slack, notion, google.
	Client       string
	ClientID     string
	ClientSecret secrets.Secret
	// Fields are the registration's client-level fields, if it has any.
	Fields map[string]string
}

// FromEnv reads apps' configuration through getenv, which a host layers its
// own defaults under.
func FromEnv(getenv func(string) string) (Config, error) {
	genericEndpoint := endpoint(getenv, "AWS_ENDPOINT_URL")
	autoSubscribe, err := boolValue(getenv, "APPS_AUTO_SUBSCRIBE", true)
	if err != nil {
		return Config{}, err
	}
	config := Config{
		AuthURL:               endpoint(getenv, "AUTH_URL"),
		ComposioAPIKey:        strings.TrimSpace(getenv("COMPOSIO_API_KEY")),
		ComposioBaseURL:       endpoint(getenv, "COMPOSIO_BASE_URL"),
		ComposioWebhookSecret: strings.TrimSpace(getenv("COMPOSIO_WEBHOOK_SECRET")),
		PublicBaseURL:         endpoint(getenv, "APPS_PUBLIC_BASE_URL"),
		AllowedOrigins:        splitList(getenv("APPS_ALLOWED_ORIGINS")),
		EventBridgeEndpoint:   endpointOr(getenv, "EVENTBRIDGE_ENDPOINT", genericEndpoint),
		SQSEndpoint:           endpointOr(getenv, "AWS_ENDPOINT_URL_SQS", genericEndpoint),
		EventsQueueURL:        strings.TrimSpace(getenv("APPS_EVENTS_QUEUE_URL")),
		NoAutoSubscribe:       !autoSubscribe,
	}
	if config.AuthURL == "" || config.ComposioAPIKey == "" || config.PublicBaseURL == "" {
		return Config{}, errors.New("AUTH_URL, COMPOSIO_API_KEY and APPS_PUBLIC_BASE_URL are required")
	}
	for _, check := range []struct{ name, value string }{
		{"AUTH_URL", config.AuthURL},
		{"COMPOSIO_BASE_URL", config.ComposioBaseURL},
		{"APPS_PUBLIC_BASE_URL", config.PublicBaseURL},
		{"EVENTBRIDGE_ENDPOINT", config.EventBridgeEndpoint},
		{"AWS_ENDPOINT_URL_SQS", config.SQSEndpoint},
	} {
		if check.value != "" && !absoluteURL(check.value) {
			return Config{}, fmt.Errorf("%s must be an absolute URL", check.name)
		}
	}
	for _, origin := range config.AllowedOrigins {
		if !absoluteURL(origin) {
			return Config{}, errors.New("APPS_ALLOWED_ORIGINS must list absolute origins")
		}
	}
	if err := readOryxa(getenv, &config); err != nil {
		return Config{}, err
	}
	return config, nil
}

// readOryxa reads Oryxa's own sign-in's configuration. None of its messages
// ever quotes a secret.
func readOryxa(getenv func(string) string, config *Config) error {
	for _, toolkit := range splitList(getenv("APPS_ORYXA_TOOLKITS")) {
		checked, err := domain.CheckToolkit(toolkit)
		if err != nil {
			return fmt.Errorf("APPS_ORYXA_TOOLKITS must list toolkits, such as github: %q is not one", toolkit)
		}
		if !slices.Contains(config.OryxaToolkits, checked) {
			config.OryxaToolkits = append(config.OryxaToolkits, checked)
		}
	}
	config.SigninKMSKeyID = strings.TrimSpace(getenv("APPS_SIGNIN_KMS_KEY_ID"))
	config.SigninRedirectURI = strings.TrimSpace(getenv("APPS_SIGNIN_REDIRECT_URI"))
	switch {
	case len(config.OryxaToolkits) > 0 && config.SigninKMSKeyID == "":
		return errors.New("APPS_SIGNIN_KMS_KEY_ID is required once APPS_ORYXA_TOOLKITS names a toolkit")
	case config.SigninKMSKeyID != "" && config.SigninRedirectURI == "":
		return errors.New("APPS_SIGNIN_REDIRECT_URI is required with APPS_SIGNIN_KMS_KEY_ID")
	}
	if config.SigninRedirectURI != "" {
		if err := checkRedirectURI(config.SigninRedirectURI, config.PublicBaseURL, config.AllowedOrigins); err != nil {
			return err
		}
	}
	var err error
	if config.HookSecrets, err = hookSecrets(getenv("APPS_HOOK_SECRETS")); err != nil {
		return err
	}
	if config.HookValues, err = hookValues(getenv("APPS_HOOK_VALUES")); err != nil {
		return err
	}
	if config.ServiceCallers, err = serviceauth.ParseCallers(getenv("APPS_SERVICE_CALLERS")); err != nil {
		return fmt.Errorf("APPS_SERVICE_CALLERS: %w", err)
	}
	if config.DefaultOAuthClients, err = defaultClients(getenv("APPS_DEFAULT_OAUTH_CLIENTS")); err != nil {
		return err
	}
	if len(config.DefaultOAuthClients) > 0 && config.SigninKMSKeyID == "" {
		return errors.New("APPS_DEFAULT_OAUTH_CLIENTS needs APPS_SIGNIN_KMS_KEY_ID, which seals their secrets")
	}
	return nil
}

// defaultClients reads APPS_DEFAULT_OAUTH_CLIENTS. Its errors name the
// registration at fault, never a secret.
func defaultClients(raw string) ([]DefaultOAuthClient, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	var decoded map[string]struct {
		ClientID     string            `json:"client_id"`
		ClientSecret string            `json:"client_secret"`
		Fields       map[string]string `json:"fields"`
	}
	if err := json.Unmarshal([]byte(raw), &decoded); err != nil {
		return nil, errors.New(`APPS_DEFAULT_OAUTH_CLIENTS must be a JSON object of registrations to their clients, {"github": {"client_id": "…", "client_secret": "…"}}`)
	}
	clients := make([]DefaultOAuthClient, 0, len(decoded))
	for _, client := range slices.Sorted(maps.Keys(decoded)) {
		checked, err := domain.CheckToolkit(client)
		if err != nil || checked != client {
			return nil, fmt.Errorf("APPS_DEFAULT_OAUTH_CLIENTS must be keyed by registrations, such as github: %q is not one", client)
		}
		value := decoded[client]
		if strings.TrimSpace(value.ClientID) == "" || strings.TrimSpace(value.ClientSecret) == "" {
			return nil, fmt.Errorf("APPS_DEFAULT_OAUTH_CLIENTS gives %s no client_id or no client_secret", client)
		}
		clients = append(clients, DefaultOAuthClient{
			Client: client, ClientID: strings.TrimSpace(value.ClientID),
			ClientSecret: secrets.Secret(strings.TrimSpace(value.ClientSecret)), Fields: value.Fields,
		})
	}
	return clients, nil
}

// checkRedirectURI refuses a redirect URI that could not bring a sign-in
// back. It must be apps' own callback path, over https (or http on a
// loopback host, for development), and on a host a browser sends the
// connect request to: the web app's own origin, when the web app calls apps
// through it, or apps' public host. The state cookie is set on the connect
// request's answer and comes back only to that host, so a callback anywhere
// else could never read it.
func checkRedirectURI(raw, publicBaseURL string, allowedOrigins []string) error {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.ForceQuery ||
		parsed.Fragment != "" || strings.Contains(raw, "#") {
		return errors.New("APPS_SIGNIN_REDIRECT_URI must be an absolute URL with no credentials, query or fragment")
	}
	host := strings.ToLower(parsed.Hostname())
	switch parsed.Scheme {
	case "https":
	case "http":
		if ip := net.ParseIP(host); host != "localhost" && (ip == nil || !ip.IsLoopback()) {
			return errors.New("APPS_SIGNIN_REDIRECT_URI must be https, or http on a loopback host")
		}
	default:
		return errors.New("APPS_SIGNIN_REDIRECT_URI must be https, or http on a loopback host")
	}
	if parsed.Path != SigninCallbackPath {
		return fmt.Errorf("APPS_SIGNIN_REDIRECT_URI must end in apps' callback, %s", SigninCallbackPath)
	}
	hosts := []string{hostname(publicBaseURL)}
	for _, origin := range allowedOrigins {
		hosts = append(hosts, hostname(origin))
	}
	if !slices.Contains(hosts, host) {
		return fmt.Errorf("APPS_SIGNIN_REDIRECT_URI is on %s, where no connect request is sent: it must be on the web app's own origin "+
			"(APPS_ALLOWED_ORIGINS), which sets the state cookie when the web app calls apps through it, or on APPS_PUBLIC_BASE_URL's host", host)
	}
	return nil
}

// hostname answers a URL's host, lower case and without its port: what a
// cookie is scoped to.
func hostname(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return strings.ToLower(parsed.Hostname())
}

// maxHookSecrets is how many secrets a toolkit's webhook verifies with at
// once: the current one, and during a rotation the previous one.
const maxHookSecrets = 2

// hookSecrets reads APPS_HOOK_SECRETS. Its error names the toolkit at fault,
// never a secret.
func hookSecrets(raw string) (map[string][]secrets.Secret, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	var decoded map[string][]string
	if err := json.Unmarshal([]byte(raw), &decoded); err != nil {
		return nil, errors.New(`APPS_HOOK_SECRETS must be a JSON object of toolkits to their secrets, {"slack": ["current", "previous"]}`)
	}
	parsed := make(map[string][]secrets.Secret, len(decoded))
	for toolkit, values := range decoded {
		checked, err := domain.CheckToolkit(toolkit)
		if err != nil || checked != toolkit {
			return nil, fmt.Errorf("APPS_HOOK_SECRETS must be keyed by toolkits, such as slack: %q is not one", toolkit)
		}
		if len(values) == 0 || len(values) > maxHookSecrets {
			return nil, fmt.Errorf("APPS_HOOK_SECRETS must give %s its current secret, and during a rotation its previous one", toolkit)
		}
		for _, value := range values {
			if strings.TrimSpace(value) == "" {
				return nil, fmt.Errorf("APPS_HOOK_SECRETS gives %s an empty secret", toolkit)
			}
			parsed[toolkit] = append(parsed[toolkit], secrets.Secret(strings.TrimSpace(value)))
		}
	}
	return parsed, nil
}

var hookValueName = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)

// hookValues reads APPS_HOOK_VALUES.
func hookValues(raw string) (map[string]string, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	var decoded map[string]string
	if err := json.Unmarshal([]byte(raw), &decoded); err != nil {
		return nil, errors.New(`APPS_HOOK_VALUES must be a JSON object of names to values, {"pubsub_push_account": "…"}`)
	}
	parsed := make(map[string]string, len(decoded))
	for name, value := range decoded {
		if !hookValueName.MatchString(name) {
			return nil, fmt.Errorf("APPS_HOOK_VALUES must be keyed by names such as pubsub_push_account: %q is not one", name)
		}
		if strings.TrimSpace(value) == "" {
			return nil, fmt.Errorf("APPS_HOOK_VALUES gives %s no value", name)
		}
		parsed[name] = strings.TrimSpace(value)
	}
	return parsed, nil
}

func endpoint(getenv func(string) string, name string) string {
	return strings.TrimRight(strings.TrimSpace(getenv(name)), "/")
}

func endpointOr(getenv func(string) string, name, fallback string) string {
	if value := endpoint(getenv, name); value != "" {
		return value
	}
	return fallback
}

func splitList(value string) []string {
	var result []string
	for _, item := range strings.Split(value, ",") {
		if item = strings.TrimSpace(item); item != "" {
			result = append(result, item)
		}
	}
	return result
}

// boolValue reads an environment variable as a boolean (strconv.ParseBool:
// true/false/1/0/T/F, case-insensitive). Unset answers fallback; anything
// else is a configuration error naming the variable, since silently keeping
// the default on a typo is worse than refusing to start.
func boolValue(getenv func(string) string, name string, fallback bool) (bool, error) {
	value := strings.TrimSpace(getenv(name))
	if value == "" {
		return fallback, nil
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return false, fmt.Errorf("%s must be true or false", name)
	}
	return parsed, nil
}

func absoluteURL(value string) bool {
	parsed, err := url.Parse(value)
	return err == nil && parsed.Scheme != "" && parsed.Host != ""
}
