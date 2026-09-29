package relevance

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"
)

// TestShouldHoldIsTheOwnersRule: an answer is held for an error, for any
// extra, and, in a regulated organization, for a request that asked for
// health, HR or compensation, and for nothing else.
func TestShouldHoldIsTheOwnersRule(t *testing.T) {
	boom := errors.New("boom")
	for _, test := range []struct {
		name      string
		regulated bool
		asked     []Category
		extras    []Category
		err       error
		hold      bool
	}{
		{name: "nothing private", hold: false},
		{name: "asked and answered at the breadth asked", asked: []Category{Health}, hold: false},
		{name: "asked, all of them, in an unregulated organization", asked: []Category{Health, HR, Compensation}, hold: false},
		{name: "one extra", extras: []Category{PersonalLife}, hold: true},
		{name: "every kind of extra", extras: Categories(), hold: true},
		{name: "an extra beside what was asked", asked: []Category{Health}, extras: []Category{Health}, hold: true},
		{name: "an error holds", err: boom, hold: true},
		{name: "an error holds a regulated organization's too", regulated: true, err: boom, hold: true},
		{name: "regulated, asked for health", regulated: true, asked: []Category{Health}, hold: true},
		{name: "regulated, asked for HR", regulated: true, asked: []Category{HR}, hold: true},
		{name: "regulated, asked for compensation", regulated: true, asked: []Category{Compensation}, hold: true},
		{name: "regulated, asked for health among others", regulated: true, asked: []Category{PersonalLife, Health}, hold: true},
		{name: "regulated, asked for personal life only", regulated: true, asked: []Category{PersonalLife}, hold: false},
		{name: "regulated, asked for credentials only", regulated: true, asked: []Category{Credentials}, hold: false},
		{name: "regulated, asked for others' private data only", regulated: true, asked: []Category{OthersPrivate}, hold: false},
		{name: "regulated, asked for nothing private", regulated: true, hold: false},
		{name: "regulated, extras hold as they do anywhere", regulated: true, extras: []Category{Credentials}, hold: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := ShouldHold(test.regulated, test.asked, test.extras, test.err); got != test.hold {
				t.Fatalf("ShouldHold(regulated=%v, asked=%v, extras=%v, err=%v) = %v, want %v", test.regulated, test.asked, test.extras, test.err, got, test.hold)
			}
		})
	}
}

func TestCategoriesAreTheSixAndValid(t *testing.T) {
	want := []Category{"personal_life", "health", "hr", "compensation", "others_private", "credentials"}
	if got := Categories(); !slices.Equal(got, want) {
		t.Fatalf("Categories() = %v, want %v", got, want)
	}
	for _, category := range want {
		if !category.Valid() {
			t.Fatalf("%q is not valid", category)
		}
	}
	for _, not := range []Category{"", "work", "Health", "salary", "none"} {
		if not.Valid() {
			t.Fatalf("%q is valid", not)
		}
	}
}

func TestErrKindNamesWhyAnAnswerWentUnchecked(t *testing.T) {
	for _, test := range []struct {
		err  error
		want string
	}{
		{nil, "none"},
		{ErrTimeout, "timeout"},
		{&failure{kind: ErrTimeout, cause: context.DeadlineExceeded}, "timeout"},
		{&failure{kind: ErrCanceled, cause: context.Canceled}, "canceled"},
		{&failure{kind: ErrUnavailable, status: 503}, "unavailable"},
		{&failure{kind: ErrBlocked}, "blocked"},
		{&failure{kind: ErrEmpty}, "empty"},
		{&failure{kind: ErrUnparseable}, "unparseable"},
		{&failure{kind: ErrUnknownCategory}, "category"},
		{&failure{kind: ErrNotConfigured}, "not_configured"},
		{fmt.Errorf("wrapped: %w", &failure{kind: ErrUnparseable}), "unparseable"},
		{errors.New("something else"), "other"},
	} {
		if got := ErrKind(test.err); got != test.want {
			t.Fatalf("ErrKind(%v) = %q, want %q", test.err, got, test.want)
		}
	}
}

// TestAFailureKeepsItsCauseButNeverPrintsIt: a model's own error message can
// quote what it was sent, so the message says the kind and the status alone,
// while errors.Is still finds both.
func TestAFailureKeepsItsCauseButNeverPrintsIt(t *testing.T) {
	cause := errors.New("the request said: Priya is on a performance plan")
	err := error(&failure{kind: ErrUnavailable, cause: cause, status: 400})
	if strings.Contains(err.Error(), "Priya") || !strings.Contains(err.Error(), "400") {
		t.Fatalf("message = %q, want the kind and the status and no text", err)
	}
	if !errors.Is(err, ErrUnavailable) || !errors.Is(err, cause) {
		t.Fatal("errors.Is does not find both the kind and the cause")
	}
}

func TestLogFieldsAreDecisionsNeverTexts(t *testing.T) {
	result := Result{
		Judged: true, Hold: true, Asked: []Category{Health}, Extras: []Category{HR, Credentials},
		Certainty: 0.75, Model: "gemini-test", Latency: 1234 * time.Millisecond, Truncated: true,
		Err: &failure{kind: ErrTimeout},
	}
	fields := LogFields(result)
	if len(fields)%2 != 0 {
		t.Fatalf("fields = %v, want key and value pairs", fields)
	}
	got := map[string]any{}
	for index := 0; index < len(fields); index += 2 {
		key, ok := fields[index].(string)
		if !ok {
			t.Fatalf("field %d = %v, want a key", index, fields[index])
		}
		got[key] = fields[index+1]
	}
	want := map[string]any{
		"judged": true, "hold": true, "asked": []string{"health"}, "extras": []string{"hr", "credentials"},
		"certainty": 0.75, "model": "gemini-test", "latency_ms": int64(1234), "truncated": true, "err_kind": "timeout",
	}
	if len(got) != len(want) {
		t.Fatalf("fields = %v, want exactly %v", got, want)
	}
	for key, value := range want {
		if fmt.Sprint(got[key]) != fmt.Sprint(value) {
			t.Fatalf("%s = %v, want %v", key, got[key], value)
		}
	}
	// A clean result has no errors and no categories, and still has every field.
	clean := LogFields(Result{Model: "m"})
	if len(clean) != len(fields) {
		t.Fatalf("a clean result has %d fields, want the same %d", len(clean), len(fields))
	}
}
