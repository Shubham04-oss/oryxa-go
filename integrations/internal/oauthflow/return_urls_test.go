package oauthflow

import "testing"

func TestAllowedReturnURLsUseConfiguredOriginOnly(t *testing.T) {
	t.Parallel()
	policy := AllowedReturnURLs{"https://app.example.com/integrations", "http://localhost:3000"}
	for _, value := range []string{"https://app.example.com/settings/integrations?done=1", "http://localhost:3000/callback"} {
		if !policy.Allows(value) {
			t.Fatalf("expected %q to be allowed", value)
		}
	}
	for _, value := range []string{"https://app.example.com.evil.test/", "http://app.example.com/", "https://user@app.example.com/", "https://app.example.com/#secret", "/relative"} {
		if policy.Allows(value) {
			t.Fatalf("expected %q to be rejected", value)
		}
	}
}
