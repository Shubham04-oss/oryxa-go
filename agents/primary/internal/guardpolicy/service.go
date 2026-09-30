package guardpolicy

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"reflect"
	"sync"

	"github.com/Shubham04-oss/oryxa-go/internal/postgreskit"
)

// Service keeps each member's answer guard choices and says what applies to
// them, once their organization's policy and the deployment's Cap are taken
// into account.
type Service struct {
	store   store
	orgs    OrgPolicies
	ceiling Cap
	logger  *slog.Logger
	// unset makes a missing OrgPolicies be reported once.
	unset sync.Once
	// failing holds the organizations whose policy could not be read since it
	// last could, so that a policy that stays unreadable is reported when it
	// first fails, and not on every check after it.
	failing sync.Map
}

// New returns the service over database. orgs says what each organization's
// policy asks; nil, or a nil pointer in the interface, means there is no
// source for it: every organization gets the defaults, and that is logged the
// first time it matters. A deployment without the workspace service passes
// NoOrgPolicies to say so without the log. deployment is the ceiling on the
// relevance check.
//
// It contacts nothing until it is used, so a caller that only migrates may
// give it no organization policies and no Cap.
func New(database *postgreskit.Database, orgs OrgPolicies, deployment Cap) *Service {
	return newService(&postgresStore{database: database}, orgs, deployment)
}

func newService(store store, orgs OrgPolicies, deployment Cap) *Service {
	if isNil(orgs) {
		orgs = nil
	}
	deployment.HoldOrgs = maps.Clone(deployment.HoldOrgs)
	return &Service{store: store, orgs: orgs, ceiling: deployment}
}

// isNil says whether source is nothing, or a nil pointer, map, slice or func
// boxed in the interface: the way a source that was never built ends up
// non-nil, and panics when it is asked.
func isNil(source any) bool {
	if source == nil {
		return true
	}
	switch value := reflect.ValueOf(source); value.Kind() {
	case reflect.Pointer, reflect.Map, reflect.Slice, reflect.Func, reflect.Chan:
		return value.IsNil()
	}
	return false
}

// WithLogger sets where the service reports that an organization's policy
// could not be read; slog's default logger when it is never called. Call it
// before the service is used.
func (service *Service) WithLogger(logger *slog.Logger) *Service {
	service.logger = logger
	return service
}

func (service *Service) log() *slog.Logger {
	if service.logger != nil {
		return service.logger
	}
	return slog.Default()
}

// Migrate installs the tables. Running it twice is harmless.
func (service *Service) Migrate(ctx context.Context) error {
	return service.store.migrate(ctx)
}

// Effective is what applies to a member's answers now: for each origin the
// stricter of their organization's minimum and their own choice or default,
// and the organization's relevance mode under the deployment's ceiling.
//
// It fails only when the member's own choices cannot be read, and then the
// Effective it returns is the strictest there is, so a caller that cannot do
// better than use it fails closed. A missing organization policy means the
// defaults; a policy source that fails means strict floors and regulated
// handling, with relevance still under the deployment's ceiling.
func (service *Service) Effective(ctx context.Context, organizationID, userID string) (Effective, error) {
	if err := needMember(organizationID, userID); err != nil {
		return strictest(organizationID, OrgPolicy{}, service.ceiling), err
	}
	org := service.organization(ctx, organizationID)
	chosen, err := service.store.get(ctx, organizationID, userID)
	if err != nil {
		return strictest(organizationID, org, service.ceiling), err
	}
	return Resolve(organizationID, org, chosen, service.ceiling).Effective(), nil
}

// strictest is the Effective that loosens nothing, for when what a member
// chose cannot be read.
func strictest(organizationID string, org OrgPolicy, deployment Cap) Effective {
	return Resolve(organizationID, org, Choices{Owner: LevelStrict, Peer: LevelStrict}, deployment).Effective()
}

// Preferences is what a member is told about their settings: what they chose,
// what their organization's minimum is, and what applies.
func (service *Service) Preferences(ctx context.Context, organizationID, userID string) (View, error) {
	if err := needMember(organizationID, userID); err != nil {
		return View{}, err
	}
	org := service.organization(ctx, organizationID)
	chosen, err := service.store.get(ctx, organizationID, userID)
	if err != nil {
		return View{}, err
	}
	return Resolve(organizationID, org, chosen, service.ceiling), nil
}

// SetPreferences changes what a member chose, and answers with what they are
// told about it now. A nil level leaves that origin as it is; a level that is
// "" clears it, back to the default. A level that is not "secrets", "strict"
// or "" is refused with ErrInvalid before anything is changed.
//
// A choice below the organization's minimum is kept, and the answer shows the
// minimum winning: Chosen, Floor and Effective differ.
func (service *Service) SetPreferences(ctx context.Context, organizationID, userID string, owner, peer *Level) (View, error) {
	if err := needMember(organizationID, userID); err != nil {
		return View{}, err
	}
	for _, change := range []struct {
		name  string
		level *Level
	}{{"owner", owner}, {"peer", peer}} {
		if change.level != nil && *change.level != "" && !change.level.Valid() {
			return View{}, fmt.Errorf("%w: %s is %q or %q, or empty to clear it", ErrInvalid, change.name, LevelSecrets, LevelStrict)
		}
	}
	if owner == nil && peer == nil {
		return service.Preferences(ctx, organizationID, userID)
	}
	chosen, err := service.store.set(ctx, organizationID, userID, owner, peer)
	if err != nil {
		return View{}, err
	}
	return Resolve(organizationID, service.organization(ctx, organizationID), chosen, service.ceiling), nil
}

// Forget removes what is kept for a member of organizationID, choices and the
// log of their changes: for the whole organization when memberID is "", for a
// member who left or an organization that is gone. It has the shape of
// inbound.Forgetter.
func (service *Service) Forget(ctx context.Context, organizationID, memberID string) error {
	if organizationID == "" {
		return errors.New("forgetting guard preferences needs an organization")
	}
	if err := service.store.forget(ctx, organizationID, memberID); err != nil {
		return err
	}
	if memberID == "" {
		service.failing.Delete(organizationID)
	}
	return nil
}

func needMember(organizationID, userID string) error {
	if organizationID == "" || userID == "" {
		return fmt.Errorf("%w: guard preferences need an organization and a member", ErrInvalid)
	}
	return nil
}

// organization is what an organization's policy asks, or a conservative
// fallback when it cannot be read: a check never fails for want of the
// workspace service. A policy that fails is reported once, when it starts to,
// and again only after it has been read in between.
func (service *Service) organization(ctx context.Context, organizationID string) OrgPolicy {
	if service.orgs == nil {
		service.unset.Do(func() {
			service.log().WarnContext(ctx, "no organization policy source is set for the answer guard, so every organization gets the defaults")
		})
		return OrgPolicy{}
	}
	policy, err := service.orgs.Get(ctx, organizationID)
	if err != nil {
		// A caller that has gone away is not a workspace service that has.
		if ctx.Err() != nil {
			return unreadablePolicy()
		}
		if _, already := service.failing.LoadOrStore(organizationID, struct{}{}); !already {
			service.log().WarnContext(ctx, "an organization's workspace policy could not be read, so the answer guard uses strict floors and regulated handling until it can",
				"organization_id", organizationID, "error", err)
		}
		return unreadablePolicy()
	}
	service.failing.Delete(organizationID)
	return policy
}

func unreadablePolicy() OrgPolicy {
	return OrgPolicy{MinimumOwner: LevelStrict, MinimumPeer: LevelStrict, Relevance: RelevanceHold, Regulated: true}
}
