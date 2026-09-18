package knowledge

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/Shubham04-oss/oryxa-go/internal/membertoken"
	"github.com/Shubham04-oss/oryxa-go/knowledge/internal/domain"
)

type answeredTokens struct{ err error }

func (tokens answeredTokens) MemberToken(context.Context, string, string) (string, error) {
	if tokens.err != nil {
		return "", tokens.err
	}
	return "token", nil
}

// TestMemberGrantsTellsAMemberFromAnOutage keeps the three reasons a token
// cannot be had apart, since only one of them is worth asking again: a
// member who has to grant, a deployment auth will not mint for, and an auth
// that did not answer.
func TestMemberGrantsTellsAMemberFromAnOutage(t *testing.T) {
	for _, test := range []struct {
		name string
		err  error
		want error
	}{
		{"never granted", membertoken.ErrGrantRequired, domain.ErrGrantRequired},
		{"not a caller auth knows", fmt.Errorf("%w: status 403", membertoken.ErrNotConfigured), domain.ErrForbidden},
		{"auth down", fmt.Errorf("%w: status 503", membertoken.ErrUnavailable), domain.ErrProviderUnavailable},
		{"signing failed", errors.New("sign this service's token: kms throttled"), domain.ErrProviderUnavailable},
		{"going away", context.Canceled, context.Canceled},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := memberGrants{tokens: answeredTokens{err: test.err}}.MemberToken(context.Background(), "org_1", "user-1")
			if !errors.Is(err, test.want) {
				t.Fatalf("error = %v, want %v", err, test.want)
			}
		})
	}
	token, err := memberGrants{tokens: answeredTokens{}}.MemberToken(context.Background(), "org_1", "user-1")
	if err != nil || token != "token" {
		t.Fatalf("MemberToken() = %q, %v", token, err)
	}
}
