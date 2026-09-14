package domain

import "testing"

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
