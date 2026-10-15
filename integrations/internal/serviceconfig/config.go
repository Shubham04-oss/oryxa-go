// Package serviceconfig reads the integrations service's configuration from
// the environment.
package serviceconfig

import (
	"fmt"
	"strings"

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
	return config, nil
}
