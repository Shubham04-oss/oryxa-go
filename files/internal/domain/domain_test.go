package domain

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestObjectKeyFollowsPlacementAndDay(t *testing.T) {
	at := time.Date(2026, time.September, 14, 12, 0, 0, 0, time.UTC)
	member := Object{OrganizationID: "org_1", ID: "file_a", Scope: ScopeMember, MemberID: "user-1", CreatedAt: at}
	if got := member.Key(); got != "org/org_1/member/user-1/2026/09/14/obj/file_a" {
		t.Fatalf("member key = %q", got)
	}
	team := Object{OrganizationID: "org_1", ID: "file_b", Scope: ScopeTeam, TeamID: "team_1", CreatedAt: at}
	if got := team.Key(); got != "org/org_1/team/team_1/2026/09/14/obj/file_b" {
		t.Fatalf("team key = %q", got)
	}
	org := Object{OrganizationID: "org_1", ID: "file_c", Scope: ScopeOrganization, CreatedAt: at}
	if got := org.Key(); got != "org/org_1/organization/2026/09/14/obj/file_c" {
		t.Fatalf("org key = %q", got)
	}
	if got := OrganizationPrefix("org_1"); got != "org/org_1/" {
		t.Fatalf("OrganizationPrefix() = %q", got)
	}
}

func TestDateFilterRange(t *testing.T) {
	start, end, err := (DateFilter{Year: 2026}).Range()
	if err != nil || !start.Equal(time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)) || !end.Equal(time.Date(2027, time.January, 1, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("year range = %s %s %v", start, end, err)
	}
	start, end, err = (DateFilter{Year: 2026, Month: 9}).Range()
	if err != nil || !start.Equal(time.Date(2026, time.September, 1, 0, 0, 0, 0, time.UTC)) || !end.Equal(time.Date(2026, time.October, 1, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("month range = %s %s %v", start, end, err)
	}
	start, end, err = (DateFilter{Year: 2026, Month: 9, Day: 14}).Range()
	if err != nil || !start.Equal(time.Date(2026, time.September, 14, 0, 0, 0, 0, time.UTC)) || !end.Equal(time.Date(2026, time.September, 15, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("day range = %s %s %v", start, end, err)
	}
	if _, _, err := (DateFilter{Month: 9}).Range(); !errors.Is(err, ErrInvalid) {
		t.Fatalf("month without year error = %v", err)
	}
	if _, _, err := (DateFilter{Year: 2026, Day: 14}).Range(); !errors.Is(err, ErrInvalid) {
		t.Fatalf("day without month error = %v", err)
	}
	if _, _, err := (DateFilter{Year: 2026, Month: 2, Day: 31}).Range(); !errors.Is(err, ErrInvalid) {
		t.Fatalf("overflow day error = %v", err)
	}
	if _, _, err := (DateFilter{Year: 9999, Month: 12, Day: 31}).Range(); err != nil {
		t.Fatalf("the last day of year 9999 error = %v", err)
	}
	// Past four digits, and far past what a timestamp holds: the database used
	// to be the one to say so, with a 500.
	for _, year := range []int{10000, 2147483648, 9223372036854775807} {
		if _, _, err := (DateFilter{Year: year}).Range(); !errors.Is(err, ErrInvalid) {
			t.Fatalf("year %d error = %v, want invalid", year, err)
		}
	}
}

func TestPlace(t *testing.T) {
	teamID, memberID, err := Place(ScopeMember, "", "user-1")
	if err != nil || teamID != "" || memberID != "user-1" {
		t.Fatalf("member place = %q %q %v", teamID, memberID, err)
	}
	if _, _, err := Place(ScopeTeam, "", "user-1"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("team without id error = %v", err)
	}
	if _, _, err := Place(ScopeOrganization, "team_1", "user-1"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("org with team error = %v", err)
	}
}

func TestBillingBelongsOnTheOrganization(t *testing.T) {
	if err := PurposeBillingAttachment.Fits(ScopeMember); !errors.Is(err, ErrInvalid) {
		t.Fatalf("billing on member error = %v", err)
	}
	if err := PurposeBillingAttachment.Fits(ScopeOrganization); err != nil {
		t.Fatal(err)
	}
}

func TestFilenameRejectsPaths(t *testing.T) {
	got, err := Filename("  notes.txt  ")
	if err != nil || got != "notes.txt" {
		t.Fatalf("Filename() = %q, %v", got, err)
	}
	for _, raw := range []string{"", ".", "..", "a/b", "a\\b", strings.Repeat("n", 256)} {
		if _, err := Filename(raw); !errors.Is(err, ErrInvalid) {
			t.Fatalf("Filename(%q) error = %v, want invalid", raw, err)
		}
	}
}

func TestContentTypeAllowlist(t *testing.T) {
	got, err := ContentType("text/plain; charset=utf-8")
	if err != nil || got != "text/plain" {
		t.Fatalf("ContentType() = %q, %v", got, err)
	}
	if _, err := ContentType("application/x-msdownload"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("exe content type error = %v, want invalid", err)
	}
}

func TestPurposeAndRetention(t *testing.T) {
	if !PurposeUpload.Valid() || Purpose("other").Valid() {
		t.Fatal("purpose allowlist")
	}
	days, err := RetentionDays(PurposePipelineArtifact, 0)
	if err != nil || days != 7 {
		t.Fatalf("default retention = %d, %v", days, err)
	}
	if days, err := RetentionDays(PurposeBillingAttachment, 0); err != nil || days != 90 {
		t.Fatalf("billing retention = %d, %v", days, err)
	}
	if _, err := RetentionDays(PurposeUpload, 100); !errors.Is(err, ErrInvalid) {
		t.Fatalf("retention error = %v, want invalid", err)
	}
}

func TestSizeBounds(t *testing.T) {
	if err := Size(0); !errors.Is(err, ErrInvalid) {
		t.Fatalf("empty size error = %v", err)
	}
	if err := Size(MaxBytes + 1); !errors.Is(err, ErrInvalid) {
		t.Fatalf("oversize error = %v", err)
	}
	if err := Size(1); err != nil {
		t.Fatal(err)
	}
}
