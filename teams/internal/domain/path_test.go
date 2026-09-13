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
