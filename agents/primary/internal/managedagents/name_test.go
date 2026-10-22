package managedagents

import (
	"slices"
	"strings"
	"testing"
)

// A new primary agent is named plainly, one of the friendly names with no
// suffix, whenever one is free in its organization.
func TestGeneratedNameIsPlainWhileOneIsFree(t *testing.T) {
	for range 100 {
		name, err := generatedName(map[string]bool{})
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Contains(friendlyNames, name) {
			t.Fatalf("name = %q, want one of the friendly names as it is", name)
		}
	}
}

// A name already used, in any case, is never chosen again.
func TestGeneratedNameSkipsTakenNames(t *testing.T) {
	taken := map[string]bool{}
	for _, name := range friendlyNames {
		if name != "Wren" {
			taken[strings.ToLower(name)] = true
		}
	}
	for range 20 {
		name, err := generatedName(taken)
		if err != nil {
			t.Fatal(err)
		}
		if name != "Wren" {
			t.Fatalf("name = %q, want the one free name, Wren", name)
		}
	}
}

// Once every friendly name is taken, the next is numbered, from 2, and still
// readable.
func TestGeneratedNameIsNumberedWhenEveryNameIsTaken(t *testing.T) {
	taken := map[string]bool{}
	for _, name := range friendlyNames {
		taken[strings.ToLower(name)] = true
	}
	name, err := generatedName(taken)
	if err != nil {
		t.Fatal(err)
	}
	base, number, found := strings.Cut(name, " ")
	if !found || number != "2" || !slices.Contains(friendlyNames, base) {
		t.Fatalf("name = %q, want a friendly name numbered 2", name)
	}
	for _, other := range friendlyNames {
		taken[strings.ToLower(other)+" 2"] = true
	}
	if name, err = generatedName(taken); err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(name, " 3") {
		t.Fatalf("name = %q, want a friendly name numbered 3", name)
	}
}

// Only the names an earlier build generated count as suffixed; a name a member
// wrote, or one this build gives, does not.
func TestSuffixedName(t *testing.T) {
	for _, name := range []string{"Nia-WETP", "Nia--W_P", "Zara-A1B2", "Ada-____"} {
		if !suffixedName.MatchString(name) {
			t.Errorf("%q should read as generated with a suffix", name)
		}
	}
	for _, name := range []string{"Nia", "Nia 2", "nia-wetp", "Nia-WETPX", "Nia-WET", "Wren-WETP", "Sales-TEAM", "My Nia-WETP"} {
		if suffixedName.MatchString(name) {
			t.Errorf("%q should not read as generated with a suffix", name)
		}
	}
}

// A renamed agent takes its plain name when it is free, and the first number
// free after it when it is not.
func TestNumberedName(t *testing.T) {
	if name := numberedName("Nia", map[string]bool{}); name != "Nia" {
		t.Fatalf("name = %q, want Nia", name)
	}
	if name := numberedName("Nia", map[string]bool{"nia": true, "nia 2": true}); name != "Nia 3" {
		t.Fatalf("name = %q, want Nia 3", name)
	}
}
