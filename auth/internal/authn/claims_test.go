package authn

import (
	"encoding/json"
	"testing"
)

// Cognito issues email_verified as a boolean for its own users and, for an
// attribute mapped from Google, can issue it as a string.
func TestEmailVerifiedReadsBothSpellings(t *testing.T) {
	for document, want := range map[string]bool{
		`{"email_verified": true}`:    true,
		`{"email_verified": "true"}`:  true,
		`{"email_verified": false}`:   false,
		`{"email_verified": "false"}`: false,
		`{}`:                          false,
	} {
		var claims IdentityClaims
		if err := json.Unmarshal([]byte(document), &claims); err != nil {
			t.Fatalf("decode %s: %v", document, err)
		}
		if bool(claims.EmailVerified) != want {
			t.Errorf("%s: email_verified = %v, want %v", document, claims.EmailVerified, want)
		}
	}
	var claims IdentityClaims
	if err := json.Unmarshal([]byte(`{"email_verified": "yes please"}`), &claims); err == nil {
		t.Error("a claim that is not a boolean decoded as one")
	}
}
