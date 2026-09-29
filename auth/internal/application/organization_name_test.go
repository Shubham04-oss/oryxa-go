package application

import (
	"errors"
	"strings"
	"testing"

	"github.com/Shubham04-oss/oryxa-go/auth/internal/domain"
)

func TestOrganizationNameLimitCountsUnicodeCharacters(t *testing.T) {
	for _, letter := range []string{"界", "é", "🦉"} {
		name := strings.Repeat(letter, 120)
		got, slug, err := organizationFields(" "+name+" ", "example-org")
		if err != nil || got != name || slug != "example-org" {
			t.Errorf("120 character name rejected: %v", err)
		}
		if _, _, err := organizationFields(name+letter, "example-org"); !errors.Is(err, domain.ErrInvalid) {
			t.Errorf("121 character name accepted: %v", err)
		}
	}
}
