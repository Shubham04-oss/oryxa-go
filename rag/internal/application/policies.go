package application

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/Shubham04-oss/oryxa-go/internal/authz"

	"github.com/Shubham04-oss/oryxa-go/rag/internal/authclient"
	"github.com/Shubham04-oss/oryxa-go/rag/internal/domain"
)

// This file is the admin surface onto one knowledge base's own stored Cedar
// policies: internal/authz already decides with them, so what is left here is
// letting an organization admin see, add and remove them, kept to admins
// because a knowledge base's policies are themselves privileged information —
// they say who else may or may not read it.

// maxPolicyNameLength bounds a stored policy's Name, the same length the web
// admin UI is expected to enforce on the field itself.
const maxPolicyNameLength = 100

// AddPolicyInput is one Cedar policy an organization admin asks to store on a
// knowledge base.
type AddPolicyInput struct {
	Name string
	Text string
}

// administerableCorpus loads a corpus exactly as a caller may see it — the
// same domain.ErrNotFound a caller who cannot see it at all would get — and
// then requires an org admin's reach over its own stored policies. Unlike a
// plain read or search denial, which must look identical to a corpus the
// caller cannot see, this one is upfront: readable succeeding already proved
// the corpus exists to this caller, so refusing to manage its policies says
// exactly that (domain.ErrForbidden), the same way mayWrite already refuses a
// non-admin's write to an organization corpus rather than hiding it.
func (service *Service) administerableCorpus(ctx context.Context, credentials authclient.Credentials, organizationID, corpusID string) (domain.Corpus, domain.OrgMembership, error) {
	corpus, member, teamMember, err := service.readable(ctx, credentials, organizationID, corpusID)
	if err != nil {
		return domain.Corpus{}, domain.OrgMembership{}, err
	}
	decision, err := service.authzEngine.Authorize(ctx, authz.Request{
		OrganizationID: organizationID,
		Principal:      authz.Principal{UserID: member.UserID, Agent: member.Agent},
		Action:         authz.Administer,
		Resource:       knowledgeBaseResource(corpus),
		Context:        authz.Context{ActsAsAdmin: member.ActsAsAdmin(), TeamMember: teamMember},
	})
	if err != nil {
		service.logger.ErrorContext(ctx, "authz: failed to evaluate a knowledge base's administer policy",
			"organization_id", organizationID, "corpus_id", corpus.ID, "error", err)
		return domain.Corpus{}, domain.OrgMembership{}, fmt.Errorf("evaluate knowledge base policy: %w", err)
	}
	if !decision.Allowed {
		return domain.Corpus{}, domain.OrgMembership{}, fmt.Errorf("only organization admins can manage a knowledge base's policies: %w", domain.ErrForbidden)
	}
	return corpus, member, nil
}

// ListPolicies is every Cedar policy an organization admin has stored on one
// knowledge base.
func (service *Service) ListPolicies(ctx context.Context, credentials authclient.Credentials, organizationID, corpusID string) ([]authz.Policy, error) {
	corpus, _, err := service.administerableCorpus(ctx, credentials, organizationID, corpusID)
	if err != nil {
		return nil, err
	}
	return service.authzStore.Policies(ctx, organizationID, authz.Scope{Type: knowledgeBaseScopeType, ID: corpus.ID})
}

// AddPolicy stores a new Cedar policy on a knowledge base and invalidates the
// engine's cache for the organization, so the next decision anywhere in it
// sees the change immediately rather than waiting out the cache TTL.
func (service *Service) AddPolicy(ctx context.Context, credentials authclient.Credentials, organizationID, corpusID string, input AddPolicyInput) (authz.Policy, error) {
	corpus, member, err := service.administerableCorpus(ctx, credentials, organizationID, corpusID)
	if err != nil {
		return authz.Policy{}, err
	}
	name := strings.TrimSpace(input.Name)
	if name == "" || utf8.RuneCountInString(name) > maxPolicyNameLength {
		return authz.Policy{}, fmt.Errorf("policy name must be 1 to %d characters: %w", maxPolicyNameLength, domain.ErrInvalid)
	}
	policy, err := service.authzStore.CreatePolicy(ctx, authz.Policy{
		OrganizationID: organizationID,
		Scope:          authz.Scope{Type: knowledgeBaseScopeType, ID: corpus.ID},
		Name:           name,
		Text:           input.Text,
		CreatedBy:      member.UserID,
	})
	if err != nil {
		switch {
		case errors.Is(err, authz.ErrInvalid):
			return authz.Policy{}, fmt.Errorf("%s: %w", authzErrorDetail(err, authz.ErrInvalid), domain.ErrInvalid)
		case errors.Is(err, authz.ErrConflict):
			return authz.Policy{}, fmt.Errorf("a policy with this name already exists: %w", domain.ErrConflict)
		default:
			return authz.Policy{}, err
		}
	}
	service.authzEngine.Invalidate(organizationID)
	// The policy's own Cedar text never goes to the log: it can quote a
	// caller-defined metadata key or value from an organization's documents,
	// which is exactly the kind of thing a policy might exist to keep out of
	// places like this one.
	service.logger.InfoContext(ctx, "knowledge base policy added",
		"organization_id", organizationID, "corpus_id", corpus.ID, "policy_id", policy.ID, "policy_name", policy.Name, "actor", member.UserID)
	return policy, nil
}

// RemovePolicy deletes one stored policy and invalidates the engine's cache
// for the organization, exactly as AddPolicy does.
func (service *Service) RemovePolicy(ctx context.Context, credentials authclient.Credentials, organizationID, corpusID, policyID string) error {
	corpus, member, err := service.administerableCorpus(ctx, credentials, organizationID, corpusID)
	if err != nil {
		return err
	}
	scope := authz.Scope{Type: knowledgeBaseScopeType, ID: corpus.ID}
	// Read before delete, purely so the removal log line below can name the
	// policy the way the addition one does; DeletePolicy itself only ever
	// hands back an error, never what it removed.
	name := policyNameFor(ctx, service.authzStore, organizationID, scope, policyID)
	if err := service.authzStore.DeletePolicy(ctx, organizationID, scope, policyID); err != nil {
		if errors.Is(err, authz.ErrNotFound) {
			return domain.ErrNotFound
		}
		return err
	}
	service.authzEngine.Invalidate(organizationID)
	service.logger.InfoContext(ctx, "knowledge base policy removed",
		"organization_id", organizationID, "corpus_id", corpus.ID, "policy_id", policyID, "policy_name", name, "actor", member.UserID)
	return nil
}

// policyNameFor is the name of one scope's policy id, best-effort: a failed
// lookup here must never turn into a failed delete over something that only
// ever affects the log line above, so it comes back empty rather than as an
// error.
func policyNameFor(ctx context.Context, store AuthzStore, organizationID string, scope authz.Scope, policyID string) string {
	policies, err := store.Policies(ctx, organizationID, scope)
	if err != nil {
		return ""
	}
	for _, policy := range policies {
		if policy.ID == policyID {
			return policy.Name
		}
	}
	return ""
}

// authzErrorDetail is err's own message with sentinel's text trimmed off its
// front, so translating an internal/authz sentinel into a domain one (see
// AddPolicy above) does not also repeat internal/authz's own "authz: ..."
// prefix in a message domain.ErrInvalid is about to prefix again with rag's.
func authzErrorDetail(err error, sentinel error) string {
	return strings.TrimPrefix(err.Error(), sentinel.Error()+": ")
}
