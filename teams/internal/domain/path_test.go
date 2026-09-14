package domain

import "testing"

func TestLtreeLabel(t *testing.T) {
	tests := map[string]string{
		"":             "_",
		"org_1":        "org_1",
		"team_abc-def": "team_abc_def",
		"team_+/=":     "team____",
		"Acme":         "Acme",
	}
	for in, want := range tests {
		if got := LtreeLabel(in); got != want {
			t.Fatalf("LtreeLabel(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestTeamPath(t *testing.T) {
	if got := TeamPath("org-1", "team_ab-c"); got != "org_1.team_ab_c" {
		t.Fatalf("TeamPath() = %q", got)
	}
}

func TestUserPath(t *testing.T) {
	if got := UserPath("org-1", "user-asha"); got != "org_1.user_asha" {
		t.Fatalf("UserPath() = %q", got)
	}
}

func TestMemberPath(t *testing.T) {
	if got := MemberPath("org_1.team_a", "user-1"); got != "org_1.team_a.user_1" {
		t.Fatalf("MemberPath() = %q", got)
	}
	if !PathContains("org_1.team_a", MemberPath("org_1.team_a", "user-1")) {
		t.Fatal("expected team path to contain its members")
	}
	if !PathContains("org_1.team_a", MemberPath("org_1.team_a.team_b", "user-1")) {
		t.Fatal("expected parent team to contain nested members")
	}
}

func TestChildPath(t *testing.T) {
	if got := ChildPath("org_1.team_a", "team_b-c"); got != "org_1.team_a.team_b_c" {
		t.Fatalf("ChildPath() = %q", got)
	}
	if got := ChildPath("", "team_1"); got != "team_1" {
		t.Fatalf("ChildPath(empty) = %q", got)
	}
}

func TestPathContains(t *testing.T) {
	if !PathContains("org_1.team_a", "org_1.team_a") || !PathContains("org_1.team_a", "org_1.team_a.team_b") {
		t.Fatal("expected ancestor and self to contain")
	}
	if PathContains("org_1.team_a", "org_1.team_ab") || PathContains("", "org_1") {
		t.Fatal("expected sibling prefix and empty path to miss")
	}
}
