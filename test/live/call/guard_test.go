package call

import (
	"strings"
	"testing"
)

// The guard against production runs everywhere, CI included: the rest of the
// package skips without CALL_LIVE_ROOM_URL.
func TestProductionIsRefused(t *testing.T) {
	for raw, want := range map[string]string{
		"https://app.oryxa.in/live/{org}/rooms/{kind}/{id}":         "app.oryxa.in",
		"https://APP.oryxa.in./live/{org}/rooms/{kind}/{id}":        "app.oryxa.in",
		"https://edge.oryxa.in":                                     "edge.oryxa.in",
		"https://api.oryxa.in:443/":                                 "api.oryxa.in",
		"https://app-staging.oryxa.in/live/{org}/rooms/{kind}/{id}": "",
		"https://edge-staging.oryxa.in":                             "",
		"http://127.0.0.1:8788/live/{org}/rooms/{kind}/{id}":        "",
		"": "",
	} {
		got, err := productionHost(raw)
		if err != nil || got != want {
			t.Errorf("productionHost(%q) = %q, %v; want %q", raw, got, err, want)
		}
	}
	if s := (joined{CallID: "call_1", Token: "a.b.c"}).String(); s == "" || strings.Contains(s, "a.b.c") {
		t.Errorf("joined.String() = %q: the token must not show", s)
	}
}
