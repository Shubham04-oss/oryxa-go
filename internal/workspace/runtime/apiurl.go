package runtime

import (
	"fmt"
	"net"
	"net/url"
	"strings"
)

// ValidateAPIURL is shared by unauthenticated discovery and authenticated
// workspace calls, so token transport cannot accept a less safe origin.
func ValidateAPIURL(value string) (string, error) {
	parsed, err := url.Parse(strings.TrimRight(value, "/"))
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.RawPath != "" || (parsed.Path != "" && parsed.Path != "/") {
		return "", fmt.Errorf("API URL must be HTTPS or loopback HTTP without credentials, path, query, or fragment")
	}
	ip := net.ParseIP(parsed.Hostname())
	loopback := parsed.Hostname() == "localhost" || ip != nil && ip.IsLoopback()
	if parsed.Scheme != "https" && !(parsed.Scheme == "http" && loopback) {
		return "", fmt.Errorf("API URL must be HTTPS or loopback HTTP")
	}
	return strings.TrimRight(parsed.String(), "/"), nil
}
