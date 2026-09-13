package postgres

import (
	"errors"
	"testing"

	"github.com/Shubham04-oss/oryxa-go/teams/internal/domain"
)

func TestSearchArgs(t *testing.T) {
	if _, _, err := searchArgs("", 5); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("empty query error = %v, want invalid", err)
	}
	query, limit, err := searchArgs("eng", 0)
	if err != nil || query != "eng" || limit != defaultSearchLimit {
		t.Fatalf("searchArgs() = %q %d %v", query, limit, err)
	}
}
