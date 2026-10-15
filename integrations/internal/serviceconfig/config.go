// Package serviceconfig reads the integrations service's configuration from
// the environment.
package serviceconfig

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

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
	// OryxaApps are Oryxa's own apps, loaded into org_apps at start. ECS
	// injects the JSON from AWS Secrets Manager; it is never in the repo.
	OryxaApps []OryxaApp
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
	if raw := strings.TrimSpace(getenv("INTEGRATIONS_ORYXA_APPS")); raw != "" {
		decoder := json.NewDecoder(bytes.NewReader([]byte(raw)))
		decoder.DisallowUnknownFields()
		// The decoder's error is dropped: it can quote the value, which holds secrets.
		if err := decoder.Decode(&config.OryxaApps); err != nil {
			return Config{}, fmt.Errorf("INTEGRATIONS_ORYXA_APPS is not a JSON list of apps")
		}
	}
	return config, nil
}
