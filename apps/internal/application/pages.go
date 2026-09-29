package application

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/Shubham04-oss/oryxa-go/apps/internal/authclient"
	"github.com/Shubham04-oss/oryxa-go/apps/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/apps/internal/integration"
	"github.com/Shubham04-oss/oryxa-go/apps/internal/signin"
)

// Knowledge indexes what a member's app holds by asking the app through the
// connection, one page at a time. For a Composio connection that is the read
// proxy, with Composio's key placing the credential; for an Oryxa one there
// is no such key, so the pages route is the way in and the connection's own
// integration's reader does the reading.
//
// Nothing here reaches an app. A reader is handed a session whose credential
// is placed below everything that can see it, and that session refuses every
// method but the reader's own GET, HEAD and read_posts, so a reader cannot
// become a way to write.

// readerSession is the connection's own reader, as its integration carries
// it, or why there is none.
func (s *Service) readerFor(toolkit string) (integration.Reader, bool) {
	if s.oryxa == nil {
		return nil, false
	}
	registered, ok := s.oryxa.Integrations.Lookup(toolkit)
	if !ok {
		return nil, false
	}
	reader, ok := registered.(integration.Reader)
	return reader, ok
}

// readerSpec is the manifest's declaration of a reader, so a name that is
// not declared is not served and a session is narrowed to its own reads.
func (s *Service) readerSpec(toolkit, name string) (integration.ReaderSpec, bool) {
	registered, ok := s.oryxa.Integrations.Lookup(toolkit)
	if !ok {
		return integration.ReaderSpec{}, false
	}
	for _, spec := range registered.Manifest().Readers {
		if spec.Name == name {
			return spec, true
		}
	}
	return integration.ReaderSpec{}, false
}

// ownConnection is the connection a member may act through, checked the same
// way for a tool, a target and a page: their own, active, and one the
// organization's workspace policy allows.
func (s *Service) ownConnection(ctx context.Context, credentials authclient.Credentials, organizationID, connectionID string) (domain.OrgMembership, domain.Connection, error) {
	caller, err := s.caller(ctx, credentials, organizationID)
	if err != nil {
		return domain.OrgMembership{}, domain.Connection{}, err
	}
	connection, err := s.store.Connection(ctx, organizationID, strings.TrimSpace(connectionID))
	if err != nil {
		return domain.OrgMembership{}, domain.Connection{}, err
	}
	// A page is read live through the connection's own credential, which is
	// acting through it rather than merely seeing that it exists: the same
	// line targets.go draws for a target. So only the connection's own member
	// may read it, asking by hand or through an agent acting for them.
	if connection.MemberID != caller.UserID {
		return domain.OrgMembership{}, domain.Connection{}, domain.ErrNotFound
	}
	if err := s.allowToolkit(ctx, organizationID, connection.Toolkit); err != nil {
		return domain.OrgMembership{}, domain.Connection{}, err
	}
	switch connection.Status {
	case domain.ConnectionActive:
	case domain.ConnectionExpired:
		return domain.OrgMembership{}, domain.Connection{}, fmt.Errorf("the %s connection expired: %w",
			connection.Toolkit, domain.ErrNeedsReconnect)
	default:
		return domain.OrgMembership{}, domain.Connection{}, fmt.Errorf("the %s connection is %s: %w",
			connection.Toolkit, connection.Status, domain.ErrNotConnected)
	}
	return caller, connection, nil
}

// Pages answers one page of one target, read through the connection's own
// integration.
//
// The request is a PageRequest and the answer a Page, one for one with
// knowledge's own sources.Page, and neither is more than MaxPageBytes: a
// reader that answers more than that has returned a page too large, and
// carries on from a smaller one rather than being trusted to stay under a
// limit it is told about nowhere.
func (s *Service) Pages(
	ctx context.Context,
	credentials authclient.Credentials,
	organizationID, connectionID string,
	request integration.PageRequest,
) (integration.Page, error) {
	_, connection, err := s.ownConnection(ctx, credentials, organizationID, connectionID)
	if err != nil {
		return integration.Page{}, err
	}
	reader, ok := s.readerFor(connection.Toolkit)
	if !ok {
		return integration.Page{}, noReader(connection.Toolkit)
	}
	name := strings.TrimSpace(request.Target[integration.TargetReader])
	if name == "" {
		return integration.Page{}, fmt.Errorf("a page request names its reader: %w", domain.ErrInvalid)
	}
	// A reader the manifest does not declare is not served: it would run
	// whatever the integration happens to answer for an unknown name.
	if _, declared := s.readerSpec(connection.Toolkit, name); !declared {
		return integration.Page{}, fmt.Errorf("%s declares no reader called %s: %w",
			connection.Toolkit, name, domain.ErrNotFound)
	}
	// A reader's own session: the credential placed below everything that can
	// see it, and nothing sent but the reads its manifest declares.
	session, err := s.oryxa.Signin.Session(ctx, connection.ID, signin.ReadOnly(s.readPOSTsOf(connection.Toolkit, name)...))
	if err != nil {
		return integration.Page{}, readerError(connection.Toolkit, err)
	}
	page, err := reader.Read(ctx, session, request)
	if err != nil {
		return integration.Page{}, readerError(connection.Toolkit, err)
	}
	if err := checkPageSize(page); err != nil {
		return integration.Page{}, err
	}
	return page, nil
}

// checkPageSize refuses a page larger than the route can carry. A reader is
// told to keep a page under the limit, but a page is a document payload and
// one item is often one whole file, so the core checks rather than trusts.
func checkPageSize(page integration.Page) error {
	encoded, err := json.Marshal(page)
	if err != nil {
		return fmt.Errorf("the page could not be encoded: %w", domain.ErrUnavailable)
	}
	if len(encoded) > integration.MaxPageBytes {
		return fmt.Errorf("the page is %d bytes, over the %d this route carries; fewer items per page would fit: %w",
			len(encoded), integration.MaxPageBytes, domain.ErrInvalid)
	}
	return nil
}

// ownTargets lists what a member could index through their own connection,
// read from the app by its integration's reader. An app that declares no
// reader answers an empty list rather than an error: nothing to index there
// is a fact about the app, and a picker wants to show that rather than a
// problem.
func (s *Service) ownTargets(ctx context.Context, connection domain.Connection) ([]Target, error) {
	reader, ok := s.readerFor(connection.Toolkit)
	if !ok {
		return []Target{}, nil
	}
	// A listing reads the app's own collections, which are GETs unless the app
	// reads them through a POST; a reader declares those as its read_posts and
	// this narrows the session to them.
	session, err := s.oryxa.Signin.Session(ctx, connection.ID, signin.ReadOnly(s.readPOSTsOf(connection.Toolkit, "")...))
	if err != nil {
		return nil, readerError(connection.Toolkit, err)
	}
	targets, err := reader.Targets(ctx, session)
	if err != nil {
		return nil, readerError(connection.Toolkit, err)
	}
	// A member with a thousand repositories is choosing from a search box, not
	// scrolling, and the app's own paging is not worth carrying through.
	if len(targets) > maxTargets {
		targets = targets[:maxTargets]
	}
	views := make([]Target, 0, len(targets))
	for _, target := range targets {
		views = append(views, Target{
			Kind: target.Kind, ID: target.ID, Name: target.Name,
			Description: target.Description, Target: target.Target,
		})
	}
	return views, nil
}

// readPOSTsOf is the POST paths a reader may call: its own when one is named,
// and every reader's for a listing, which reads collections rather than one
// target. Nothing else may be sent, and the session refuses it.
func (s *Service) readPOSTsOf(toolkit, reader string) []string {
	if s.oryxa == nil {
		return nil
	}
	registered, ok := s.oryxa.Integrations.Lookup(toolkit)
	if !ok {
		return nil
	}
	var posts []string
	for _, spec := range registered.Manifest().Readers {
		if reader == "" || spec.Name == reader {
			posts = append(posts, spec.ReadPOSTs...)
		}
	}
	return posts
}

// noReader says an app has nothing knowledge can read through this build.
func noReader(toolkit string) error {
	return fmt.Errorf("%s is connected through Oryxa's own sign-in, whose readers are not available yet: %w",
		toolkit, domain.ErrNotConnectable)
}

// readerError is what a reader's own failure becomes in apps' terms, so
// knowledge reads one set of words whichever broker answered.
func readerError(toolkit string, err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	var refused *integration.AppError
	switch {
	case errors.As(err, &refused):
		return fmt.Errorf("read %s: %s: %w", toolkit, truncate(refused.Error(), maxComposioMessage), domain.ErrInvalid)
	case errors.Is(err, integration.ErrNeedsReconnect):
		return fmt.Errorf("read %s: the app no longer accepts this connection: %w", toolkit, domain.ErrNeedsReconnect)
	case errors.Is(err, integration.ErrNotFound):
		return fmt.Errorf("read %s: the app has no such thing: %w", toolkit, domain.ErrNotFound)
	case isRateLimited(err):
		return fmt.Errorf("read %s: the app is rate limiting: %w", toolkit, domain.ErrRateLimited)
	default:
		return fmt.Errorf("read %s: the app did not answer: %w", toolkit, domain.ErrUnavailable)
	}
}
