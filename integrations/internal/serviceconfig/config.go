// Package serviceconfig reads the integrations service's configuration from
// the environment.
package serviceconfig

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/Shubham04-oss/oryxa-go/integrations/egress"
	"github.com/Shubham04-oss/oryxa-go/internal/secrets"
	"github.com/Shubham04-oss/oryxa-go/internal/serviceauth"
)

// Enabled reports whether INTEGRATIONS_ENABLED switches the service on. Only
// "true" does: the service ships off, and a typo must not turn it on.
func Enabled(getenv func(string) string) bool {
	return strings.EqualFold(strings.TrimSpace(getenv("INTEGRATIONS_ENABLED")), "true")
}

// Config is what the integrations service reads for itself. The database,
// the AWS region and the event bus are the host's.
type Config struct {
	// ServiceCallers are the services allowed to call the internal routes,
	// as serviceauth.ParseCallers reads them. None means every internal call
	// is refused.
	ServiceCallers []serviceauth.Caller
	// KMSKeyID wraps the data keys that seal app secrets and tokens. Without
	// it the service cannot seal, so the routes that store secrets are not
	// mounted.
	KMSKeyID string
	// PublicURL is where browsers reach this service (no trailing slash); the
	// OAuth callback is registered with each app under it. Without it sign-ins
	// are not mounted.
	PublicURL string
	// ReturnOrigins are the origins (scheme and host) a sign-in may send the
	// browser back to, comma separated in INTEGRATIONS_RETURN_ORIGINS.
	ReturnOrigins []string
	// OryxaApps are Oryxa's own apps, loaded into org_apps at start. ECS
	// injects the JSON from AWS Secrets Manager; it is never in the repo.
	OryxaApps []OryxaApp
	// ConnectorsDir holds the connector binaries the host starts, one per
	// file, named by the connector's manifest id (INTEGRATIONS_CONNECTORS_DIR).
	// Empty means no connector runs.
	ConnectorsDir string
	// ConnectorUID is the user and group connectors run as
	// (INTEGRATIONS_CONNECTOR_UID); the image's is 65533. Empty runs them as
	// the service's own user.
	ConnectorUID uint32
	// EgressKeys sign the handles connectors call apps with, as JSON
	// {"current": "<kid>", "keys": {"<kid>": "<at least 32 bytes>"}} in
	// INTEGRATIONS_EGRESS_KEYS. Without them tool calls are not mounted.
	EgressKeys egress.Keys
	// BrokerAddress is where the egress proxy listens for connectors; they run
	// in the same task, so it is loopback (INTEGRATIONS_BROKER_ADDRESS,
	// default 127.0.0.1:8091).
	BrokerAddress string
	// EventsQueueURL is the SQS queue auth's departures reach the service on
	// (INTEGRATIONS_EVENTS_QUEUE_URL); see integrations/infra/events. Without
	// it a member who leaves keeps their connections.
	EventsQueueURL string
}

// OryxaApp is one of Oryxa's own apps (design §9.3).
type OryxaApp struct {
	App           string            `json:"app"`
	ClientID      string            `json:"client_id"`
	Secret        secrets.Secret    `json:"secret"`
	WebhookSecret secrets.Secret    `json:"webhook_secret"`
	Extra         map[string]string `json:"extra"`
}

// FromEnv reads the configuration through getenv, which a host layers its own
// defaults under. It reads nothing while the service is off.
func FromEnv(getenv func(string) string) (Config, error) {
	var config Config
	if !Enabled(getenv) {
		return config, nil
	}
	var err error
	if config.ServiceCallers, err = serviceauth.ParseCallers(getenv("INTEGRATIONS_SERVICE_CALLERS")); err != nil {
		return Config{}, fmt.Errorf("INTEGRATIONS_SERVICE_CALLERS: %w", err)
	}
	config.KMSKeyID = strings.TrimSpace(getenv("INTEGRATIONS_KMS_KEY_ID"))
	config.PublicURL = strings.TrimRight(strings.TrimSpace(getenv("INTEGRATIONS_PUBLIC_URL")), "/")
	for _, origin := range strings.Split(getenv("INTEGRATIONS_RETURN_ORIGINS"), ",") {
		if origin = strings.TrimRight(strings.TrimSpace(origin), "/"); origin != "" {
			config.ReturnOrigins = append(config.ReturnOrigins, origin)
		}
	}
	if raw := strings.TrimSpace(getenv("INTEGRATIONS_ORYXA_APPS")); raw != "" {
		decoder := json.NewDecoder(bytes.NewReader([]byte(raw)))
		decoder.DisallowUnknownFields()
		// The decoder's error is dropped: it can quote the value, which holds secrets.
		if err := decoder.Decode(&config.OryxaApps); err != nil {
			return Config{}, fmt.Errorf("INTEGRATIONS_ORYXA_APPS is not a JSON list of apps")
		}
	}
	config.ConnectorsDir = strings.TrimSpace(getenv("INTEGRATIONS_CONNECTORS_DIR"))
	if raw := strings.TrimSpace(getenv("INTEGRATIONS_CONNECTOR_UID")); raw != "" {
		uid, err := strconv.ParseUint(raw, 10, 32)
		if err != nil || uid == 0 {
			return Config{}, fmt.Errorf("INTEGRATIONS_CONNECTOR_UID is not a user id other than 0")
		}
		config.ConnectorUID = uint32(uid)
	}
	if raw := strings.TrimSpace(getenv("INTEGRATIONS_EGRESS_KEYS")); raw != "" {
		var keys struct {
			Current string                    `json:"current"`
			Keys    map[string]secrets.Secret `json:"keys"`
		}
		decoder := json.NewDecoder(bytes.NewReader([]byte(raw)))
		decoder.DisallowUnknownFields()
		// As above, the decoder's error could quote a key.
		if err := decoder.Decode(&keys); err != nil {
			return Config{}, fmt.Errorf(`INTEGRATIONS_EGRESS_KEYS is not {"current": ..., "keys": {...}}`)
		}
		config.EgressKeys = egress.Keys{Current: keys.Current, Keys: keys.Keys}
		if err := config.EgressKeys.Validate(); err != nil {
			return Config{}, fmt.Errorf("INTEGRATIONS_EGRESS_KEYS: %w", err)
		}
	}
	config.EventsQueueURL = strings.TrimSpace(getenv("INTEGRATIONS_EVENTS_QUEUE_URL"))
	config.BrokerAddress = strings.TrimSpace(getenv("INTEGRATIONS_BROKER_ADDRESS"))
	if config.BrokerAddress == "" {
		config.BrokerAddress = "127.0.0.1:8091"
	}
	return config, nil
}
