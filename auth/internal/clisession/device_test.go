package clisession

import (
	"strings"
	"testing"
)

func TestUserCodeAlphabetHasNoLookalikesOrVowels(t *testing.T) {
	if 256%len(UserCodeAlphabet) != 0 {
		t.Fatalf("alphabet of %d characters biases a byte modulo", len(UserCodeAlphabet))
	}
	seen := map[rune]bool{}
	for _, r := range UserCodeAlphabet {
		if seen[r] {
			t.Fatalf("%q repeats", r)
		}
		seen[r] = true
		if r < 'A' || r > 'Z' {
			t.Fatalf("%q is not an uppercase letter", r)
		}
	}
	// Vowels and Y spell words; the rest read as 0, 1, 2, 5 or 8.
	for _, r := range "AEIOUYLZSB" {
		if strings.ContainsRune(UserCodeAlphabet, r) {
			t.Errorf("alphabet contains %q", r)
		}
	}
}

func TestNewUserCodeIsShortGroupedAndInAlphabet(t *testing.T) {
	seen := map[string]bool{}
	used := map[rune]bool{}
	for range 2000 {
		code := NewUserCode()
		if len(code) != UserCodeLength+1 || code[UserCodeLength/2] != '-' {
			t.Fatalf("code %q is not XXXX-XXXX", code)
		}
		for i, r := range code {
			if i == UserCodeLength/2 {
				continue
			}
			if !strings.ContainsRune(UserCodeAlphabet, r) {
				t.Fatalf("code %q has %q", code, r)
			}
			used[r] = true
		}
		if NormalizeUserCode(code) != code {
			t.Fatalf("code %q does not normalize to itself", code)
		}
		seen[code] = true
	}
	if len(seen) < 1990 || len(used) != len(UserCodeAlphabet) {
		t.Fatalf("%d distinct codes, %d characters used", len(seen), len(used))
	}
}

func TestNormalizeUserCodeAcceptsWhatAMemberTypes(t *testing.T) {
	for input, want := range map[string]string{
		"HJKM-PQRT":   "HJKM-PQRT",
		"hjkmpqrt":    "HJKM-PQRT",
		" hjkm pqrt ": "HJKM-PQRT",
		"HJKM-PQR":    "",
		"HJKM-PQRTX":  "",
		"HJKM-PQR0":   "",
		"HJKM-PQRA":   "",
		"":            "",
		"HJKM_PQRT":   "",
	} {
		if got := NormalizeUserCode(input); got != want {
			t.Errorf("NormalizeUserCode(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestDeviceTokensAreOpaqueAndMarked(t *testing.T) {
	a, b := NewDeviceToken(), NewDeviceToken()
	if a == b || !IsDeviceToken(a) || IsDeviceToken("eyJhbGciOi.jwt.token") || len(a) < len(DeviceTokenPrefix)+40 {
		t.Fatal(a, b)
	}
	if NewDeviceCode() == NewDeviceCode() {
		t.Fatal("device codes repeat")
	}
}
