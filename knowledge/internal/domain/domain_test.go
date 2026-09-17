package domain

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestCheckTargetKeepsWhatAProviderCanRead(t *testing.T) {
	checked, err := CheckTarget(map[string]string{"repository": "  acme/api  ", "branch": "main"})
	if err != nil {
		t.Fatalf("CheckTarget() error = %v", err)
	}
	if checked["repository"] != "acme/api" {
		t.Fatalf("CheckTarget() repository = %q, want %q", checked["repository"], "acme/api")
	}
	if checked["branch"] != "main" {
		t.Fatalf("CheckTarget() branch = %q, want %q", checked["branch"], "main")
	}
}

func TestCheckTargetNilTargetComesBackEmptyNotNil(t *testing.T) {
	checked, err := CheckTarget(nil)
	if err != nil {
		t.Fatalf("CheckTarget() error = %v", err)
	}
	if checked == nil {
		t.Fatal("CheckTarget() = nil, want an empty map")
	}
}

func TestCheckTargetRejectsTooManyFields(t *testing.T) {
	target := map[string]string{}
	for i := range MaxTargetFields + 1 {
		target[string(rune('a'+i))+"field"] = "value"
	}
	if _, err := CheckTarget(target); !errors.Is(err, ErrInvalid) {
		t.Fatalf("CheckTarget() error = %v, want ErrInvalid", err)
	}
}

func TestCheckTargetRejectsWhatAProviderCouldNotName(t *testing.T) {
	for _, target := range []map[string]string{
		{"Repository": "acme/api"},
		{"repo-name": "acme/api"},
		{"1repo": "acme/api"},
		{"repository": "   "},
		{"repository": strings.Repeat("x", maxTargetValue+1)},
	} {
		if _, err := CheckTarget(target); !errors.Is(err, ErrInvalid) {
			t.Fatalf("CheckTarget(%v) error = %v, want ErrInvalid", target, err)
		}
	}
}

func TestCheckIntervalDefaultsWhenUnset(t *testing.T) {
	interval, err := CheckInterval(0)
	if err != nil {
		t.Fatalf("CheckInterval() error = %v", err)
	}
	if interval != DefaultInterval {
		t.Fatalf("CheckInterval(0) = %s, want %s", interval, DefaultInterval)
	}
}

func TestCheckIntervalRejectsWhatWouldHammerOrStall(t *testing.T) {
	for _, interval := range []time.Duration{MinInterval - time.Second, MaxInterval + time.Second} {
		if _, err := CheckInterval(interval); !errors.Is(err, ErrInvalid) {
			t.Fatalf("CheckInterval(%s) error = %v, want ErrInvalid", interval, err)
		}
	}
}

func TestPlaceFollowsTheConnectionsScope(t *testing.T) {
	teamID, memberID, err := Place(ScopeMember, "", "user-1")
	if err != nil || teamID != "" || memberID != "user-1" {
		t.Fatalf("Place(member) = %q, %q, %v", teamID, memberID, err)
	}
	teamID, memberID, err = Place(ScopeTeam, "team_1", "")
	if err != nil || teamID != "team_1" || memberID != "" {
		t.Fatalf("Place(team) = %q, %q, %v", teamID, memberID, err)
	}
	teamID, memberID, err = Place(ScopeOrganization, "", "user-1")
	if err != nil || teamID != "" || memberID != "" {
		t.Fatalf("Place(organization) = %q, %q, %v", teamID, memberID, err)
	}
}

func TestPlaceRejectsAScopeWithoutItsOwner(t *testing.T) {
	if _, _, err := Place(ScopeMember, "", ""); !errors.Is(err, ErrInvalid) {
		t.Fatalf("Place(member, no member) error = %v, want ErrInvalid", err)
	}
	if _, _, err := Place(ScopeTeam, "", ""); !errors.Is(err, ErrInvalid) {
		t.Fatalf("Place(team, no team) error = %v, want ErrInvalid", err)
	}
	if _, _, err := Place(Scope("everyone"), "", ""); !errors.Is(err, ErrInvalid) {
		t.Fatalf("Place(unknown scope) error = %v, want ErrInvalid", err)
	}
}

func TestExtractableRefusesASourceThatIsNotActive(t *testing.T) {
	if err := (Source{Status: StatusActive}).Extractable(); err != nil {
		t.Fatalf("Extractable(active) error = %v", err)
	}
	if err := (Source{Status: StatusPaused}).Extractable(); !errors.Is(err, ErrConflict) {
		t.Fatalf("Extractable(paused) error = %v, want ErrConflict", err)
	}
	if err := (Source{Status: StatusRemoved}).Extractable(); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Extractable(removed) error = %v, want ErrNotFound", err)
	}
}

func TestRetryAfterDoublesUntilItIsCapped(t *testing.T) {
	if got := RetryAfter(0); got != MinInterval {
		t.Fatalf("RetryAfter(0) = %s, want %s", got, MinInterval)
	}
	if got := RetryAfter(2); got != 4*MinInterval {
		t.Fatalf("RetryAfter(2) = %s, want %s", got, 4*MinInterval)
	}
	capped := MinInterval * (1 << retryCeiling)
	if got := RetryAfter(retryCeiling + 5); got != capped {
		t.Fatalf("RetryAfter(past the ceiling) = %s, want %s", got, capped)
	}
	if got := RetryAfter(-1); got != MinInterval {
		t.Fatalf("RetryAfter(-1) = %s, want %s", got, MinInterval)
	}
}
