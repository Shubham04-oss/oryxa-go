// Package adkmemory binds what Krude remembers to the organization the
// conversation belonged to.
//
// ADK addresses memory by app name and user alone: [memory.SearchRequest]
// carries nothing else, and the runner fills it from the session's own user.
// A member can belong to more than one organization, and threads are already
// kept apart by the organization in their state, so a memory service that
// took ADK's addressing at its word would answer a question asked in one
// organization with what it learned in another.
//
// So nothing here is addressed by user alone. A [Backend] is reached only
// through a [Scope], and the ADK-shaped view a run is given holds its scope
// from the moment it is built: the run cannot widen it, and a request for
// another user is refused rather than answered from the wrong place.
package adkmemory

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"google.golang.org/adk/v2/memory"
	"google.golang.org/adk/v2/session"
)

// scopeSeparator joins a scope's halves into the one key backends that
// address memory by a single string are given. Neither half may contain it,
// so two different scopes can never produce the same key.
const scopeSeparator = "/"

// Scope is whose memory a read or write addresses: one member, in one
// organization. Both halves are required, because either alone names more
// than one member's memory.
type Scope struct {
	OrganizationID string
	UserID         string
}

// Validate reports whether the scope names exactly one member's memory.
func (scope Scope) Validate() error {
	switch {
	case strings.TrimSpace(scope.OrganizationID) == "":
		return errors.New("adkmemory: a scope needs an organization")
	case strings.TrimSpace(scope.UserID) == "":
		return errors.New("adkmemory: a scope needs a member")
	case strings.Contains(scope.OrganizationID, scopeSeparator),
		strings.Contains(scope.UserID, scopeSeparator):
		return fmt.Errorf("adkmemory: a scope cannot contain %q", scopeSeparator)
	}
	return nil
}

// Key is the scope as one string, for a backend that addresses memory by a
// single identifier, as mem0 does. It is stable and distinct per scope: what
// one organization wrote is never what another reads. Callers validate the
// scope first; an invalid one has no key.
func (scope Scope) Key() string {
	return scope.OrganizationID + scopeSeparator + scope.UserID
}

// The keys [Scope.Fields] partitions memory by.
const (
	FieldOrganizationID = "organization_id"
	FieldUserID         = "user_id"
)

// Fields is the scope as the key-value map a backend that partitions along
// several dimensions of its own is given, as Vertex AI Memory Bank does.
// There the organization is a dimension the service itself matches on rather
// than a convention this package encodes into a string, which is the
// stronger of the two: Memory Bank returns a memory only when its scope has
// the same keys and values as the one asked for.
func (scope Scope) Fields() map[string]string {
	return map[string]string{
		FieldOrganizationID: scope.OrganizationID,
		FieldUserID:         scope.UserID,
	}
}

// Backend keeps what Krude remembers. Every call names the scope it acts in,
// so an implementation never has to work out whose memory it is holding, and
// cannot answer outside it.
//
// Implementations are safe for concurrent use: runs share one backend.
type Backend interface {
	// Write records a finished conversation as memory in scope.
	Write(ctx context.Context, scope Scope, conversation session.Session) error
	// Search returns what scope remembers about query, most relevant first,
	// or an empty slice when it remembers nothing.
	Search(ctx context.Context, scope Scope, query string) ([]memory.Entry, error)
	// Forget removes every memory in organizationID — only userID's, when
	// one is named. Forgetting what is already gone removes nothing more.
	Forget(ctx context.Context, organizationID, userID string) error
}

// Service is Krude's memory: the backend, and the scoped views runs are
// given.
type Service struct {
	backend Backend
}

// New returns the service reading and writing through backend.
func New(backend Backend) (*Service, error) {
	if backend == nil {
		return nil, errors.New("adkmemory: a backend is required")
	}
	return &Service{backend: backend}, nil
}

// For returns the [memory.Service] a run in scope is given. It answers only
// within scope, whatever ADK asks it for, so the organization a run belongs
// to is fixed before the run starts rather than read off each request.
func (service *Service) For(scope Scope) (memory.Service, error) {
	if err := scope.Validate(); err != nil {
		return nil, err
	}
	return &scoped{backend: service.backend, scope: scope}, nil
}

// Forget removes every memory in organizationID — only userID's, when one is
// named — for a member who left the organization or an organization that is
// gone. What a conversation taught Krude should not outlive the place it was
// taught in, any more than the conversation itself does.
func (service *Service) Forget(ctx context.Context, organizationID, userID string) error {
	if strings.TrimSpace(organizationID) == "" {
		return errors.New("adkmemory: forgetting needs an organization")
	}
	if err := service.backend.Forget(ctx, organizationID, userID); err != nil {
		return fmt.Errorf("forget memory: %w", err)
	}
	return nil
}

// scoped is one run's view of memory. It holds the scope it was built with
// and never reads it from what it is handed.
type scoped struct {
	backend Backend
	scope   Scope
}

var _ memory.Service = (*scoped)(nil)

// AddSessionToMemory implements [memory.Service].
func (s *scoped) AddSessionToMemory(ctx context.Context, conversation session.Session) error {
	if conversation == nil {
		return errors.New("adkmemory: no conversation to remember")
	}
	// The runner hands over whichever session it ran. Writing one member's
	// conversation into another's memory would be silent and permanent, so
	// refuse rather than record it in the wrong place.
	if user := conversation.UserID(); user != s.scope.UserID {
		return fmt.Errorf("adkmemory: refusing to remember user %q in the memory of %q", user, s.scope.UserID)
	}
	if err := s.backend.Write(ctx, s.scope, conversation); err != nil {
		return fmt.Errorf("remember conversation: %w", err)
	}
	return nil
}

// SearchMemory implements [memory.Service].
func (s *scoped) SearchMemory(ctx context.Context, request *memory.SearchRequest) (*memory.SearchResponse, error) {
	if request == nil {
		return nil, errors.New("adkmemory: no search request")
	}
	// ADK addresses the search by user; the scope is what actually answers
	// it. They agree for every request the runner builds, and a request for
	// anyone else is a bug somewhere above — one that would read another
	// member's memory, so it fails instead.
	if request.UserID != "" && request.UserID != s.scope.UserID {
		return nil, fmt.Errorf("adkmemory: refusing to search user %q in the memory of %q", request.UserID, s.scope.UserID)
	}
	entries, err := s.backend.Search(ctx, s.scope, request.Query)
	if err != nil {
		return nil, fmt.Errorf("search memory: %w", err)
	}
	return &memory.SearchResponse{Memories: entries}, nil
}
