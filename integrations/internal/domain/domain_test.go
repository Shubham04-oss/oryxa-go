package domain

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
)

func TestCheckTargetAcceptsAndTrims(t *testing.T) {
	target := map[string]string{"channel": "  #general  ", "repo": "acme/widgets"}
	checked, events, err := CheckTarget(target, []string{" issues.opened ", "issues.opened", " pulls.closed "})
	if err != nil {
		t.Fatalf("CheckTarget() error = %v", err)
	}
	if checked["channel"] != "#general" || checked["repo"] != "acme/widgets" {
		t.Fatalf("CheckTarget() target = %+v", checked)
	}
	if want := []string{"issues.opened", "pulls.closed"}; !slices.Equal(events, want) {
		t.Fatalf("CheckTarget() events = %v, want %v (dedup in first-seen order)", events, want)
	}
}

func TestCheckTargetNilEventsComeBackEmptyNotNil(t *testing.T) {
	_, events, err := CheckTarget(nil, nil)
	if err != nil {
		t.Fatalf("CheckTarget() error = %v", err)
	}
	if events == nil || len(events) != 0 {
		t.Fatalf("CheckTarget() events = %#v, want an empty non-nil slice", events)
	}
}

func TestCheckTargetRejectsTooManyFields(t *testing.T) {
	target := map[string]string{}
	for i := 0; i < MaxTargetFields+1; i++ {
		target[fmt.Sprintf("field%d", i)] = "value"
	}
	if _, _, err := CheckTarget(target, nil); !errors.Is(err, ErrInvalid) {
		t.Fatalf("CheckTarget() with %d fields error = %v, want ErrInvalid", len(target), err)
	}
	// MaxTargetFields itself is fine.
	delete(target, fmt.Sprintf("field%d", MaxTargetFields))
	if _, _, err := CheckTarget(target, nil); err != nil {
		t.Fatalf("CheckTarget() with %d fields error = %v, want nil", len(target), err)
	}
}

func TestCheckTargetRejectsInvalidKeys(t *testing.T) {
	for _, key := range []string{"Channel", "1key", strings.Repeat("a", 41), "my-key", "has space"} {
		if _, _, err := CheckTarget(map[string]string{key: "value"}, nil); !errors.Is(err, ErrInvalid) {
			t.Fatalf("CheckTarget() key %q error = %v, want ErrInvalid", key, err)
		}
	}
	// Exactly 40 characters, the longest allowed, is fine.
	key := strings.Repeat("a", 40)
	if _, _, err := CheckTarget(map[string]string{key: "value"}, nil); err != nil {
		t.Fatalf("CheckTarget() with a 40-byte key error = %v, want nil", err)
	}
}

func TestCheckTargetRejectsInvalidValues(t *testing.T) {
	for name, value := range map[string]string{
		"empty":             "",
		"blank":             "   ",
		"too long":          strings.Repeat("a", MaxTargetValue+1),
		"control character": "bad\x00value",
	} {
		if _, _, err := CheckTarget(map[string]string{"key": value}, nil); !errors.Is(err, ErrInvalid) {
			t.Fatalf("%s value: CheckTarget() error = %v, want ErrInvalid", name, err)
		}
	}
	// Exactly MaxTargetValue bytes, the longest allowed, is fine.
	if _, _, err := CheckTarget(map[string]string{"key": strings.Repeat("a", MaxTargetValue)}, nil); err != nil {
		t.Fatalf("CheckTarget() with a %d-byte value error = %v, want nil", MaxTargetValue, err)
	}
}

func TestCheckTargetRejectsInvalidEvents(t *testing.T) {
	if _, _, err := CheckTarget(nil, []string{"bad event"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("CheckTarget() event with a space error = %v, want ErrInvalid", err)
	}
	if _, _, err := CheckTarget(nil, []string{strings.Repeat("a", 101)}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("CheckTarget() 101-byte event error = %v, want ErrInvalid", err)
	}
	events := make([]string, MaxEvents+1)
	for i := range events {
		events[i] = fmt.Sprintf("event.%d", i)
	}
	if _, _, err := CheckTarget(nil, events); !errors.Is(err, ErrInvalid) {
		t.Fatalf("CheckTarget() with %d events error = %v, want ErrInvalid", len(events), err)
	}
	// Exactly MaxEvents is fine.
	if _, _, err := CheckTarget(nil, events[:MaxEvents]); err != nil {
		t.Fatalf("CheckTarget() with %d events error = %v, want nil", MaxEvents, err)
	}
}

func TestCheckWebhookSecretTrimsAndAccepts(t *testing.T) {
	raw := strings.Repeat("a", MinWebhookSecret)
	secret, err := CheckWebhookSecret("  " + raw + "  ")
	if err != nil || secret != raw {
		t.Fatalf("CheckWebhookSecret() = %q, %v, want %q, nil", secret, err, raw)
	}
	if _, err := CheckWebhookSecret(strings.Repeat("a", MaxWebhookSecret)); err != nil {
		t.Fatalf("CheckWebhookSecret() at the maximum length error = %v, want nil", err)
	}
}

func TestCheckWebhookSecretRejects(t *testing.T) {
	for name, secret := range map[string]string{
		"too short":         strings.Repeat("a", MinWebhookSecret-1),
		"too long":          strings.Repeat("a", MaxWebhookSecret+1),
		"control character": strings.Repeat("a", MinWebhookSecret-1) + "\x00",
	} {
		if _, err := CheckWebhookSecret(secret); !errors.Is(err, ErrInvalid) {
			t.Fatalf("%s: CheckWebhookSecret() error = %v, want ErrInvalid", name, err)
		}
	}
}

func TestSubscriptionAccepts(t *testing.T) {
	open := Subscription{}
	if !open.Accepts("issues.opened") || !open.Accepts("anything.at.all") {
		t.Fatal("Subscription with no events should accept any event")
	}
	narrow := Subscription{Events: []string{"issues.opened", "pulls.closed"}}
	if !narrow.Accepts("issues.opened") || !narrow.Accepts("pulls.closed") || narrow.Accepts("issues.closed") {
		t.Fatalf("Subscription.Accepts() with events = %v behaved unexpectedly", narrow.Events)
	}
}

func TestSubscriptionStatusReceives(t *testing.T) {
	for status, want := range map[SubscriptionStatus]bool{
		SubscriptionPending: true,
		SubscriptionActive:  true,
		SubscriptionFailed:  false,
		SubscriptionStopped: false,
	} {
		if got := status.Receives(); got != want {
			t.Errorf("%s.Receives() = %v, want %v", status, got, want)
		}
	}
}

func TestOrgRoleIsOrgAdmin(t *testing.T) {
	for role, want := range map[OrgRole]bool{
		OrgRoleOwner:       true,
		OrgRoleAdmin:       true,
		OrgRoleMember:      false,
		OrgRole("unknown"): false,
	} {
		if got := role.IsOrgAdmin(); got != want {
			t.Errorf("%s.IsOrgAdmin() = %v, want %v", role, got, want)
		}
	}
}
