package events

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

// TestAnOutboxEnsuredAtTheSameMomentElsewhereIsEnsured: a statement another
// session won the race to is run once more, and then finds what it creates;
// a failure of any other kind, and a second failure, are the caller's.
func TestAnOutboxEnsuredAtTheSameMomentElsewhereIsEnsured(t *testing.T) {
	raced := func(code string) error {
		return fmt.Errorf("exec: %w", &pgconn.PgError{Code: code, Message: "already exists"})
	}
	for name, test := range map[string]struct {
		failures  []error
		wantCalls int
		wantErr   bool
	}{
		"nothing in the way":                     {nil, 1, false},
		"the table's type made at the same time": {[]error{raced("42710")}, 2, false},
		"the table made at the same time":        {[]error{raced("42P07")}, 2, false},
		"the schema made at the same time":       {[]error{raced("42P06")}, 2, false},
		"a column added at the same time":        {[]error{raced("42701")}, 2, false},
		"the catalog's own unique index":         {[]error{raced("23505")}, 2, false},
		"raced twice":                            {[]error{raced("42710"), raced("42710")}, 2, true},
		"no permission":                          {[]error{raced("42501")}, 1, true},
		"a database that did not answer":         {[]error{errors.New("connection refused")}, 1, true},
	} {
		t.Run(name, func(t *testing.T) {
			calls := 0
			exec := func(_ context.Context, statement string) error {
				calls++
				if statement != "CREATE TABLE IF NOT EXISTS x" {
					t.Fatalf("ran %q", statement)
				}
				if calls <= len(test.failures) {
					return test.failures[calls-1]
				}
				return nil
			}
			err := ensureStatement(context.Background(), exec, "CREATE TABLE IF NOT EXISTS x")
			if calls != test.wantCalls || (err != nil) != test.wantErr {
				t.Fatalf("ran %d times with %v, want %d times and an error: %v", calls, err, test.wantCalls, test.wantErr)
			}
		})
	}
	if createdConcurrently(nil) || createdConcurrently(errors.New("42710")) {
		t.Fatal("an error that is not PostgreSQL's was read as a race")
	}
}
