package oauthflow

import (
	"net/url"
	"strings"
)

// AllowedReturnURLs accepts an exact configured URL or another path on the
// same configured origin. Fragments and credentials are rejected.
type AllowedReturnURLs []string

func (allowed AllowedReturnURLs) Allows(value string) bool {
	target, err := url.Parse(value)
	if err != nil || !target.IsAbs() || target.Host == "" || target.User != nil || target.Fragment != "" {
		return false
	}
	if target.Scheme != "https" && !(target.Scheme == "http" && isLocalhost(target.Hostname())) {
		return false
	}
	for _, entry := range allowed {
		configured, err := url.Parse(entry)
		if err == nil && strings.EqualFold(configured.Scheme, target.Scheme) && strings.EqualFold(configured.Host, target.Host) {
			return true
		}
	}
	return false
}

func isLocalhost(host string) bool {
	return host == "localhost" || host == "127.0.0.1" || host == "::1"
}
