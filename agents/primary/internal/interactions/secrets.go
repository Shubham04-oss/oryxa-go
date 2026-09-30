package interactions

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/agents"
)

// A secret is never in Slack, in an event, in the ledger or in a model's
// context. When the answer to a request is a credential, its owner shares it as
// a one-time view in Oryxa: they paste the value in themselves, on the web,
// so it never passes through a model; the member who asked opens it once, in
// their own session, and it is deleted when they do or when it expires; and the
// owner can revoke it before either.
//
// Where the values are kept, sealed, is a store of its own (package
// secretshares, behind httpserver.SecretShares). What the ledger knows is
// who may do what to the secret of which request, and that is what the
// Authorize methods say: they read the request, and only when they allow a call
// is a SecretShare handed to the store, which trusts it. A secret is shared
// only for a request approved with access; with a summary alone, the answer may
// say only where it lives.

// MaxSecretTTL is the longest a secret lives before it is opened: an hour from
// when it is shared, or less where the organization expires consent sooner.
const MaxSecretTTL = time.Hour

// MinSecretTTL is the shortest.
const MinSecretTTL = time.Minute

// MaxSecretRunes bounds a secret's value.
const MaxSecretRunes = 16_000

// ErrSecretGone reports a secret that is not there to be opened or revoked:
// it was never shared, or it was opened, revoked or has expired. It says which
// of them it was to no one, so it tells no more than the secret's owner and
// the member who asked already know.
var ErrSecretGone = errors.New("the secret was already opened or revoked, or has expired, or was never shared")

// SecretShare names the one request whose secret a call is about, and the two
// members it is between. The ledger builds it after authorizing the call; the
// store keeps values under it and checks nothing more.
type SecretShare struct {
	RequestID      string
	OrganizationID string
	AskID          string
	// OwnerUserID shares the secret; AskerUserID opens it.
	OwnerUserID string
	AskerUserID string
}

// SecretMeta is what is told of a secret that was shared: when it stops being
// there to be opened.
type SecretMeta struct {
	ExpiresAt time.Time
}

// SecretValue is a secret as the member who asked opens it, once. It is the one
// place a value is held outside its store, and it takes care not to be
// printed: formatting it, or logging it, says nothing of what it holds.
type SecretValue struct {
	Value     string
	ExpiresAt time.Time
}

// String, GoString and LogValue keep a value out of anything that formats or
// logs the struct that holds it.
func (SecretValue) String() string   { return "[secret]" }
func (SecretValue) GoString() string { return "interactions.SecretValue{[secret]}" }
func (SecretValue) LogValue() slog.Value {
	return slog.StringValue("[secret]")
}

// AuthorizeSecretPut says whether the actor may share a secret for the request:
// they are the member it is addressed to, in their own signed-in session, it
// was approved with access, and it is neither closed nor expired. asked is the
// lifetime they asked for, or zero for the longest; the lifetime the secret
// gets is returned with the share.
func (service *Service) AuthorizeSecretPut(ctx context.Context, actor agents.InteractionActor, requestID string, asked time.Duration) (SecretShare, time.Duration, error) {
	request, err := service.secretRequest(ctx, actor, requestID)
	if err != nil {
		return SecretShare{}, 0, err
	}
	switch {
	case request.Target.UserID != actor.UserID:
		return SecretShare{}, 0, fmt.Errorf("%w: only the member this request is addressed to shares a secret for it", ErrForbidden)
	case !secretable(request):
		return SecretShare{}, 0, fmt.Errorf("%w: a secret is shared for a request that was approved and is not closed", ErrConflict)
	case consentOf(request) != ConsentAccess:
		return SecretShare{}, 0, fmt.Errorf("%w: a secret is shared only when the request was approved with access; with a summary alone, say where it lives", ErrInvalid)
	case !request.ExpiresAt.After(service.now()):
		return SecretShare{}, 0, ErrExpired
	}
	ttl, err := service.secretTTL(ctx, request.OrganizationID, asked)
	if err != nil {
		return SecretShare{}, 0, err
	}
	return shareOf(request), ttl, nil
}

// AuthorizeSecretView says whether the actor may open the request's secret:
// they are the member who asked, in their own signed-in session, and the request
// is approved and not closed.
func (service *Service) AuthorizeSecretView(ctx context.Context, actor agents.InteractionActor, requestID string) (SecretShare, error) {
	request, err := service.secretRequest(ctx, actor, requestID)
	if err != nil {
		return SecretShare{}, err
	}
	switch {
	case request.Requester.UserID != actor.UserID:
		return SecretShare{}, fmt.Errorf("%w: only the member who asked opens the secret shared for a request", ErrForbidden)
	case !secretable(request):
		return SecretShare{}, ErrSecretGone
	}
	return shareOf(request), nil
}

// AuthorizeSecretRevoke says whether the actor may revoke the request's secret:
// they are the member who shared it, in their own signed-in session. A request
// that has since closed can still have its secret revoked, since revoking is
// what its owner does when they no longer want it opened.
func (service *Service) AuthorizeSecretRevoke(ctx context.Context, actor agents.InteractionActor, requestID string) (SecretShare, error) {
	request, err := service.secretRequest(ctx, actor, requestID)
	if err != nil {
		return SecretShare{}, err
	}
	if request.Target.UserID != actor.UserID {
		return SecretShare{}, fmt.Errorf("%w: only the member who shared a secret revokes it", ErrForbidden)
	}
	return shareOf(request), nil
}

// secretRequest reads the request a secret call is about, as one of its two
// members in their own signed-in session, or ErrNotFound for anyone else.
func (service *Service) secretRequest(ctx context.Context, actor agents.InteractionActor, requestID string) (agents.Interaction, error) {
	if actor.Session != agents.SessionMember {
		return agents.Interaction{}, fmt.Errorf("%w: a secret is shared and opened by a member in their own signed-in session", ErrForbidden)
	}
	if err := validateActor(actor); err != nil {
		return agents.Interaction{}, err
	}
	request, err := service.repository.Get(ctx, strings.TrimSpace(requestID))
	if err != nil {
		return agents.Interaction{}, err
	}
	if request.OrganizationID != actor.OrganizationID || !canRead(actor, request) {
		return agents.Interaction{}, ErrNotFound
	}
	return request, nil
}

// secretable is whether a request is in a state a secret can be shared and
// opened in: approved, or answered, or its answer held for review, and not
// closed, rejected, withheld or expired.
func secretable(request agents.Interaction) bool {
	switch request.Status {
	case StatusApproved, StatusReview, StatusCompleted:
		return true
	}
	return false
}

func shareOf(request agents.Interaction) SecretShare {
	return SecretShare{
		RequestID: request.ID, OrganizationID: request.OrganizationID, AskID: request.AskID,
		OwnerUserID: request.Target.UserID, AskerUserID: request.Requester.UserID,
	}
}

// secretTTL is how long a secret shared now lives: what was asked, at most
// MaxSecretTTL and at least MinSecretTTL, or MaxSecretTTL when nothing was; and
// no longer than the organization lets consent last.
func (service *Service) secretTTL(ctx context.Context, organizationID string, asked time.Duration) (time.Duration, error) {
	ttl := asked
	switch {
	case ttl == 0:
		ttl = MaxSecretTTL
	case ttl < MinSecretTTL || ttl > MaxSecretTTL:
		return 0, fmt.Errorf("%w: ttl_seconds is from %d to %d", ErrInvalid, int(MinSecretTTL/time.Second), int(MaxSecretTTL/time.Second))
	}
	settings, err := service.organizationPolicy(ctx, organizationID)
	if err != nil {
		return 0, err
	}
	if settings.ExpireAfter > 0 && ttl > settings.ExpireAfter {
		ttl = settings.ExpireAfter
	}
	return ttl, nil
}
