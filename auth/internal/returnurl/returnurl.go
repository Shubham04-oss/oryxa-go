// Package returnurl checks a return_to URL against the origins an operator
// allowed with AUTH_RETURN_URLS. oauthflow's Google sign-in and agentgrant's
// member-consent flow both send the browser back to a return_to after a
// redirect-based flow, and both must refuse the same tricks, so they share
// this one check instead of each keeping its own copy of it.
package returnurl

import (
	"fmt"
	"net/url"
	"strings"
)

// ParseOrigins parses each configured origin into an absolute URL for
// Allowed to compare against. It fails if any origin is not itself absolute
// (has no scheme or host), the same shape a flow's own return_to must have.
func ParseOrigins(origins []string) ([]*url.URL, error) {
	parsed := make([]*url.URL, 0, len(origins))
	for _, origin := range origins {
		value, err := url.Parse(origin)
		if err != nil || value.Scheme == "" || value.Host == "" {
			return nil, fmt.Errorf("return URL %q must be absolute", origin)
		}
		parsed = append(parsed, value)
	}
	return parsed, nil
}

// Allowed reports whether target is one of the allowed origins, or a page
// beneath one of them, so a flow can return to the page that sent the member
// away, such as the invitation they opened, rather than only the origin's
// root.
func Allowed(target string, origins []*url.URL) bool {
	parsed, err := url.Parse(target)
	if err != nil || parsed.User != nil || parsed.Host == "" || (parsed.Scheme != "https" && parsed.Scheme != "http") {
		return false
	}
	// A browser resolves dot segments, which could climb out of an allowed
	// path; the path is decoded, so encoded dots are caught too.
	for _, segment := range strings.Split(parsed.Path, "/") {
		if segment == "." || segment == ".." {
			return false
		}
	}
	for _, origin := range origins {
		base := strings.TrimSuffix(origin.Path, "/")
		if parsed.Scheme == origin.Scheme && strings.EqualFold(parsed.Host, origin.Host) &&
			(parsed.Path == base || strings.HasPrefix(parsed.Path, base+"/")) {
			return true
		}
	}
	return false
}
