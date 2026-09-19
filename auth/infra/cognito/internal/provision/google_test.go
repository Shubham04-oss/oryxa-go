package provision

import (
	"slices"
	"testing"
)

// Auth refuses an identity token whose email is not verified, and Cognito
// sets email_verified on a Google user only from this mapping.
func TestGoogleAttributeMappingCarriesEmailVerified(t *testing.T) {
	mapping := googleAttributeMapping()
	for userPoolAttribute, googleClaim := range map[string]string{"email": "email", "email_verified": "email_verified"} {
		if mapping[userPoolAttribute] != googleClaim {
			t.Errorf("%s maps from %q, want Google's %q", userPoolAttribute, mapping[userPoolAttribute], googleClaim)
		}
	}
}

// Cognito puts in an identity token only what the app client may read, so
// email_verified has to be readable for auth to see it; no client may write it.
func TestTheWebClientReadsButCannotWriteEmailVerified(t *testing.T) {
	settings := appClientSettings(Config{})
	if !slices.Contains(settings.readAttributes, "email_verified") {
		t.Errorf("read attributes %v leave out email_verified", settings.readAttributes)
	}
	if slices.Contains(settings.writeAttributes, "email_verified") {
		t.Errorf("write attributes %v let a member mark their own email verified", settings.writeAttributes)
	}
}
