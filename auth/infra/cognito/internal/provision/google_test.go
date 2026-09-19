package provision

import "testing"

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
