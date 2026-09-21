package domain

import (
	"errors"
	"strings"
	"testing"
)

func TestCleanTeamName(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		want    string
		wantErr bool
	}{
		{"trimmed", "  Engineering  ", "Engineering", false},
		{"empty", "", "", true},
		{"blank", " \t ", "", true},
		{"at the limit", strings.Repeat("a", MaxTeamNameLength), strings.Repeat("a", MaxTeamNameLength), false},
		{"one past it", strings.Repeat("a", MaxTeamNameLength+1), "", true},
		// A newline in a name is header injection wherever the name is spoken
		// aloud — a mail header, a log line — and never a name anyone meant.
		{"crlf", "x\r\nX-Injected: yes", "", true},
		{"bare newline", "x\ny", "", true},
		{"bare carriage return", "x\ry", "", true},
		{"nul", "x\x00y", "", true},
		{"other control", "x\x01y", "", true},
		{"delete", "x\x7fy", "", true},
		// The bidi marks that reorder text are invisible, and a name that
		// reorders itself lies about what it says.
		{"rtl override", "x\u202ey", "", true},
		{"bidi isolate", "x\u2068y", "", true},
		// A combining mark that is not a format character is writing, not
		// control, and stays.
		{"combining accent", "e\u0301cole", "e\u0301cole", false},
		{"multibyte", "Engineering · Platform", "Engineering · Platform", false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := CleanTeamName(test.raw)
			if test.wantErr != errors.Is(err, ErrInvalid) {
				t.Fatalf("error = %v, want invalid = %v", err, test.wantErr)
			}
			if !test.wantErr && got != test.want {
				t.Fatalf("name = %q, want %q", got, test.want)
			}
		})
	}
}
