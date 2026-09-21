package domain

import (
	"errors"
	"strings"
	"testing"
)

func TestFuzzyMatch(t *testing.T) {
	if !FuzzyMatch("eng", "Engineering") || !FuzzyMatch("asha", "asha@example.com") {
		t.Fatal("expected substring matches")
	}
	if !FuzzyMatch("Engineerin", "Engineering") {
		t.Fatal("expected a close misspelling to match")
	}
	if FuzzyMatch("design", "Engineering") || FuzzyMatch("", "Engineering") {
		t.Fatal("expected unrelated and empty queries to miss")
	}
}

func TestClampSearchLimit(t *testing.T) {
	if ClampSearchLimit(0) != DefaultSearchLimit || ClampSearchLimit(500) != MaxSearchLimit || ClampSearchLimit(7) != 7 {
		t.Fatal("unexpected search limit clamping")
	}
}

func TestSearchQuery(t *testing.T) {
	tests := []struct {
		name    string
		query   string
		want    string
		wantErr bool
	}{
		{"trimmed", "  Engineering \n", "Engineering", false},
		{"empty", "", "", true},
		{"blank", " \t ", "", true},
		{"at the limit", strings.Repeat("a", MaxSearchQueryLength), strings.Repeat("a", MaxSearchQueryLength), false},
		{"one past it", strings.Repeat("a", MaxSearchQueryLength+1), "", true},
		// Characters, not bytes: a name in another script is no longer than one in Latin.
		{"multibyte at the limit", strings.Repeat("é", MaxSearchQueryLength), strings.Repeat("é", MaxSearchQueryLength), false},
		{"multibyte past it", strings.Repeat("é", MaxSearchQueryLength+1), "", true},
		{"whitespace does not count", strings.Repeat(" ", 1000) + "a" + strings.Repeat(" ", 1000), "a", false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := SearchQuery(test.query)
			if test.wantErr != errors.Is(err, ErrInvalid) {
				t.Fatalf("error = %v, want invalid = %v", err, test.wantErr)
			}
			if !test.wantErr && got != test.want {
				t.Fatalf("query = %q, want %q", got, test.want)
			}
		})
	}
}
