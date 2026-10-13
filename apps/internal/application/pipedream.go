package application

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	appsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/apps/v1"

	"github.com/Shubham04-oss/oryxa-go/apps/internal/authclient"
	"github.com/Shubham04-oss/oryxa-go/apps/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/internal/composio"
)

// pipedreamCallbackPath is where Pipedream's hosted connect sends a member's
// browser back to, whether they finished or not.
const pipedreamCallbackPath = "/v1/apps/connect/pipedream/callback"

// pipedreamTokenLifetime is how long a member has to finish connecting
// through Pipedream before its link stops working.
const pipedreamTokenLifetime = 900 * time.Second

// everyStatus lists a member's connections whatever happened to them, so an
// account recorded on any of them, even a removed one, is never claimed again.
var everyStatus = []domain.ConnectionStatus{
	domain.ConnectionPending, domain.ConnectionActive, domain.ConnectionFailed,
	domain.ConnectionExpired, domain.ConnectionRemoved,
}

// ConnectPipedream starts connecting an app through Pipedream Connect for the
// caller: it records the pending connection, as a Composio connect does, and
// answers the hosted page the member finishes at. Pipedream makes the
// account only once they do, so the connection holds no account until its
// callback reads it back.
func (s *Service) ConnectPipedream(ctx context.Context, credentials authclient.Credentials, organizationID string, input ConnectInput) (domain.Connection, string, error) {
	if s.pipedream == nil {
		return domain.Connection{}, "", errPipedreamOff(input.Toolkit)
	}
	start, err := s.startConnect(ctx, credentials, organizationID, input)
	if err != nil {
		return domain.Connection{}, "", err
	}
	state, stateHash, err := newState()
	if err != nil {
		return domain.Connection{}, "", err
	}
	connection := start.connection(organizationID, s.now().UTC())
	connection.Broker = domain.BrokerPipedream
	// The account is made under the user the tools run it as, by the one rule
	// both take it from.
	connection.ComposioUserID = pipedreamUserID(connection)
	callback := s.publicBaseURL + pipedreamCallbackPath + "?state=" + url.QueryEscape(state)
	token, err := s.pipedream.CreateConnectToken(ctx, PipedreamTokenRequest{
		ExternalUserID: connection.ComposioUserID, App: start.toolkit,
		// The error redirect says so itself: Pipedream adds nothing to either.
		SuccessRedirectURI: callback, ErrorRedirectURI: callback + "&status=failed",
		ExpiresIn: pipedreamTokenLifetime,
	})
	if err != nil {
		return domain.Connection{}, "", fmt.Errorf("start the connection at Pipedream: %w", err)
	}
	link, err := url.Parse(token.ConnectLinkURL)
	if err != nil || link.Host == "" {
		return domain.Connection{}, "", fmt.Errorf("Pipedream answered no usable connect link: %w", domain.ErrUnavailable)
	}
	// The link opens on Pipedream's app picker unless it names the app.
	query := link.Query()
	query.Set("app", start.toolkit)
	link.RawQuery = query.Encode()

	// Nothing exists at Pipedream yet if this fails: the token expires unused.
	connection, err = s.recordPending(ctx, connection, stateHash)
	if err != nil {
		return domain.Connection{}, "", err
	}
	return connection, link.String(), nil
}

// CompletePipedreamConnection finishes the Pipedream connection a callback's
// state names. The redirect proves nothing, since anyone holding the state
// could make it, so the account is read back: the newest of the member's
// accounts of the app made since the connect began that no connection
// already records. Pipedream may redirect a moment before it lists the
// account, so it is looked for a few times before the connection is left
// pending, for the sweep to fail if it never turns up.
func (s *Service) CompletePipedreamConnection(ctx context.Context, state, reportedStatus string) (Completion, error) {
	if state == "" {
		return Completion{}, fmt.Errorf("the callback carries no state: %w", domain.ErrInvalid)
	}
	connection, err := s.store.PendingConnection(ctx, hashState(state))
	if err != nil {
		return Completion{}, err
	}
	if !connection.Pipedream() || s.pipedream == nil {
		return Completion{}, domain.ErrNotFound
	}
	if reportedStatus == "failed" {
		if err := s.fail(ctx, connection, "the member did not finish connecting", appsv1.FailedDeniedAtProvider); err != nil {
			return Completion{}, err
		}
		connection.Status = domain.ConnectionFailed
		return Completion{Connection: connection, ReturnTo: connection.ReturnTo}, nil
	}
	for attempt := range connectPolls {
		account, found, err := s.newPipedreamAccount(ctx, connection)
		if err != nil {
			return Completion{}, err
		}
		if found {
			return s.activatePipedream(ctx, connection, account)
		}
		if attempt < connectPolls-1 {
			if err := s.wait(ctx, time.Second); err != nil {
				return Completion{}, err
			}
		}
	}
	return Completion{Connection: connection, ReturnTo: connection.ReturnTo}, nil
}

// newPipedreamAccount finds the account a pending Pipedream connection was
// made as, if Pipedream lists it yet.
func (s *Service) newPipedreamAccount(ctx context.Context, connection domain.Connection) (PipedreamAccount, bool, error) {
	accounts, err := s.pipedream.ListAccounts(ctx, pipedreamUserID(connection), connection.Toolkit)
	if err != nil {
		return PipedreamAccount{}, false, fmt.Errorf("read the member's accounts at Pipedream: %w", err)
	}
	// A member who connected the app before, through a connection since
	// removed or failed, still has that account listed until Pipedream
	// forgets it, and it must not pass for the new one.
	recorded, err := s.store.Connections(ctx, connection.OrganizationID, domain.ConnectionQuery{
		MemberID: connection.MemberID, Toolkit: connection.Toolkit, Statuses: everyStatus,
	})
	if err != nil {
		return PipedreamAccount{}, false, err
	}
	taken := map[string]bool{}
	for _, other := range recorded {
		if other.ID != connection.ID && other.PipedreamAccountID != "" {
			taken[other.PipedreamAccountID] = true
		}
	}
	var newest PipedreamAccount
	found := false
	for _, account := range accounts {
		if (account.ExternalUserID != "" && account.ExternalUserID != pipedreamUserID(connection)) ||
			(account.App != "" && account.App != connection.Toolkit) ||
			!account.CreatedAt.After(connection.CreatedAt) || taken[account.ID] {
			continue
		}
		if !found || account.CreatedAt.After(newest.CreatedAt) {
			newest, found = account, true
		}
	}
	return newest, found, nil
}

func (s *Service) activatePipedream(ctx context.Context, connection domain.Connection, account PipedreamAccount) (Completion, error) {
	now := s.now().UTC()
	event, err := s.event(appsv1.ConnectionConnectedDetailType, appsv1.ConnectionConnected{
		ConnectionID: connection.ID, OrganizationID: connection.OrganizationID, MemberID: connection.MemberID,
		Toolkit: connection.Toolkit, Broker: appsv1.BrokerPipedream, ConnectedAt: now,
	})
	if err != nil {
		return Completion{}, err
	}
	activated, changed, err := s.store.ActivatePipedreamConnection(ctx, connection.ID, account.ID, truncate(account.Name, 200), now, event)
	if err != nil {
		return Completion{}, err
	}
	if !changed {
		// Another callback for the same state finished it first, or the
		// sweep failed it: answer it as it now is.
		current, err := s.store.Connection(ctx, connection.OrganizationID, connection.ID)
		if err != nil {
			return Completion{}, err
		}
		return Completion{Connection: current, ReturnTo: connection.ReturnTo}, nil
	}
	s.logger.InfoContext(ctx, "app connected", "connection_id", connection.ID,
		"organization_id", connection.OrganizationID, "toolkit", connection.Toolkit, "broker", domain.BrokerPipedream)
	return Completion{Connection: activated, ReturnTo: connection.ReturnTo}, nil
}

// removePipedream deletes a Pipedream connection's triggers and then its
// account at Pipedream, which revokes what Pipedream holds for it, and
// records it removed. What Pipedream no longer has is not an error, so a
// retry after a failed record finishes the job.
func (s *Service) removePipedream(ctx context.Context, connection domain.Connection, reason string) error {
	triggers, err := s.store.Triggers(ctx, connection.OrganizationID, TriggerQuery{ConnectionID: connection.ID})
	if err != nil {
		return err
	}
	for _, trigger := range triggers {
		if trigger.PipedreamTriggerID == "" {
			continue
		}
		if s.pipedream == nil {
			return errPipedreamOff(connection.Toolkit)
		}
		if err := s.pipedream.DeleteTrigger(ctx, pipedreamUserID(connection), trigger.PipedreamTriggerID); err != nil && !errors.Is(err, domain.ErrNotFound) {
			return fmt.Errorf("remove the connection's trigger at Pipedream: %w", err)
		}
	}
	if connection.PipedreamAccountID != "" {
		if s.pipedream == nil {
			return errPipedreamOff(connection.Toolkit)
		}
		if err := s.pipedream.DeleteAccount(ctx, connection.PipedreamAccountID); err != nil && !errors.Is(err, domain.ErrNotFound) {
			return fmt.Errorf("remove the account at Pipedream: %w", err)
		}
	}
	return s.recordRemoved(ctx, connection, reason)
}

// forgetPipedreamUsers deletes, at Pipedream, the external user of each
// member whose connections were just removed, once none of theirs there is
// live, so nothing of a departed member stays behind at Pipedream: not an
// account a callback never read back, nor anything else filed under them.
func (s *Service) forgetPipedreamUsers(ctx context.Context, organizationID string, connections []domain.Connection) error {
	if s.pipedream == nil {
		return nil
	}
	users := map[string]string{}
	for _, connection := range connections {
		if connection.Pipedream() {
			users[connection.MemberID] = pipedreamUserID(connection)
		}
	}
	var failures []error
	for memberID, userID := range users {
		live, err := s.store.Connections(ctx, organizationID, domain.ConnectionQuery{
			MemberID: memberID,
			Statuses: []domain.ConnectionStatus{domain.ConnectionPending, domain.ConnectionActive, domain.ConnectionExpired},
		})
		if err != nil {
			failures = append(failures, err)
			continue
		}
		if containsPipedream(live) {
			continue
		}
		if err := s.pipedream.DeleteUser(ctx, userID); err != nil && !errors.Is(err, domain.ErrNotFound) {
			failures = append(failures, fmt.Errorf("remove the member at Pipedream: %w", err))
		}
	}
	return errors.Join(failures...)
}

func containsPipedream(connections []domain.Connection) bool {
	for _, connection := range connections {
		if connection.Pipedream() {
			return true
		}
	}
	return false
}

// Pipedream's health is read rather than told: apps sweeps every account once
// a day, at most pipedreamAccountCalls account reads per
// pipedreamAccountWindow, which is Pipedream's rate limit on them.
const (
	pipedreamHealthEvery   = 24 * time.Hour
	pipedreamAccountCalls  = 2000
	pipedreamAccountWindow = 5 * time.Minute
	pipedreamHealthPage    = 100
)

// checkPipedreamHealth marks every active Pipedream connection whose account
// Pipedream can no longer use as needing reconnecting, at most once a day.
func (s *Service) checkPipedreamHealth(ctx context.Context) {
	if s.pipedream == nil {
		return
	}
	if started := s.now(); s.pipedreamCheckedAt.IsZero() || started.Sub(s.pipedreamCheckedAt) >= pipedreamHealthEvery {
		s.pipedreamCheckedAt = started
	} else {
		return
	}
	expired := 0
	for after := ""; ; {
		page, err := s.store.PipedreamConnections(ctx, after, pipedreamHealthPage)
		if err != nil {
			if ctx.Err() == nil {
				s.logger.ErrorContext(ctx, "list Pipedream connections", "error", err)
			}
			return
		}
		for _, connection := range page {
			if err := s.wait(ctx, pipedreamAccountWindow/pipedreamAccountCalls); err != nil {
				return
			}
			reason, err := s.pipedreamAccountTrouble(ctx, connection)
			if err != nil {
				// One account that cannot be read now is checked again
				// tomorrow; the rest still are today.
				s.logger.WarnContext(ctx, "read a Pipedream account", "connection_id", connection.ID, "error", err)
				continue
			}
			if reason == "" {
				continue
			}
			if err := s.expirePipedream(ctx, connection, reason); err != nil {
				s.logger.ErrorContext(ctx, "expire a Pipedream connection", "connection_id", connection.ID, "error", err)
				continue
			}
			expired++
		}
		if len(page) < pipedreamHealthPage {
			break
		}
		after = page[len(page)-1].ID
	}
	if expired > 0 {
		s.logger.InfoContext(ctx, "Pipedream connections need reconnecting", "count", expired)
	}
}

// pipedreamAccountTrouble says why a connection's account can no longer be
// used, or "" when it still can.
func (s *Service) pipedreamAccountTrouble(ctx context.Context, connection domain.Connection) (string, error) {
	account, err := s.pipedream.GetAccount(ctx, connection.PipedreamAccountID)
	switch {
	case errors.Is(err, domain.ErrNotFound):
		return "Pipedream no longer has the account", nil
	case err != nil:
		return "", err
	case account.Dead:
		return "Pipedream gave up on the account", nil
	case !account.Healthy:
		return "Pipedream cannot renew the account's credential", nil
	}
	return "", nil
}

// expirePipedream marks an active Pipedream connection expired, as a Composio
// connection is when Composio says its account expired.
func (s *Service) expirePipedream(ctx context.Context, connection domain.Connection, reason string) error {
	now := s.now().UTC()
	event, err := s.event(appsv1.ConnectionExpiredDetailType, appsv1.ConnectionExpired{
		ConnectionID: connection.ID, OrganizationID: connection.OrganizationID, MemberID: connection.MemberID,
		Toolkit: connection.Toolkit, Broker: appsv1.BrokerPipedream, Reason: appsv1.ExpiredCredentialRejected, ExpiredAt: now,
	})
	if err != nil {
		return err
	}
	_, err = s.store.ExpireConnection(ctx, connection.ID, reason, now, event)
	return err
}

// The catalog of Pipedream's apps is held for a day: it changes slowly, and
// listing it is many pages.
const (
	pipedreamCatalogEvery = 24 * time.Hour
	// pipedreamCatalogRetry is how soon a catalog that could not be refreshed
	// is tried again, while the one already held keeps being answered.
	pipedreamCatalogRetry = 5 * time.Minute
	// maxComposioCatalogPages bounds the walk through Composio's catalog that
	// decides which of Pipedream's apps Composio already serves.
	maxComposioCatalogPages = 100
)

// pipedreamCatalog holds Pipedream's apps as apps offers them.
type pipedreamCatalog struct {
	mu        sync.Mutex
	list      *pipedreamApps
	fetchedAt time.Time
}

type pipedreamApps struct {
	// bySlug is every Pipedream app whose slug is a toolkit, which is what
	// routing asks of it.
	bySlug map[string]PipedreamApp
	// only is the ones Composio has no match for, in Pipedream's order: what
	// the catalog lists after Composio's.
	only []PipedreamApp
}

// pipedreamAliases match a Pipedream app to the Composio app it is, where
// their names differ by more than case, underscores and hyphens. Both sides
// are catalog keys.
var pipedreamAliases = map[string]string{
	"microsoftoutlook":  "outlook",
	"microsoftonedrive": "onedrive",
	"microsoftexcel":    "excel",
}

// catalogKey is what two catalogs' slugs are compared by.
func catalogKey(slug string) string {
	return strings.NewReplacer("_", "", "-", "").Replace(strings.ToLower(slug))
}

// pipedreamCatalog answers Pipedream's catalog, refreshing it once a day. A
// refresh that fails keeps answering the catalog already held.
func (s *Service) pipedreamCatalog(ctx context.Context) (*pipedreamApps, error) {
	held := &s.pipedreamAppCatalog
	held.mu.Lock()
	defer held.mu.Unlock()
	now := s.now()
	if held.list != nil && now.Sub(held.fetchedAt) < pipedreamCatalogEvery {
		return held.list, nil
	}
	list, err := s.loadPipedreamCatalog(ctx)
	if err != nil {
		if held.list == nil {
			return nil, err
		}
		s.logger.WarnContext(ctx, "refresh Pipedream's catalog", "error", err)
		held.fetchedAt = now.Add(pipedreamCatalogRetry - pipedreamCatalogEvery)
		return held.list, nil
	}
	held.list, held.fetchedAt = list, now
	return list, nil
}

func (s *Service) loadPipedreamCatalog(ctx context.Context) (*pipedreamApps, error) {
	apps, err := s.pipedream.ListApps(ctx, "")
	if err != nil {
		return nil, fmt.Errorf("read Pipedream's apps: %w", err)
	}
	composioKeys := map[string]bool{}
	cursor := ""
	for range maxComposioCatalogPages {
		page, err := s.composio.Toolkits(ctx, composio.ToolkitQuery{Cursor: cursor, Limit: maxCatalogPage})
		if err != nil {
			return nil, composioError("list the catalog", err)
		}
		for _, toolkit := range page.Items {
			composioKeys[catalogKey(toolkit.Slug)] = true
		}
		if cursor = page.NextCursor; cursor == "" {
			break
		}
	}
	list := &pipedreamApps{bySlug: map[string]PipedreamApp{}}
	for _, app := range apps {
		// A Pipedream slug that is not a toolkit could not be connected,
		// routed or offered, so it is left out.
		slug, err := domain.CheckToolkit(app.Slug)
		if err != nil || slug != app.Slug {
			continue
		}
		if _, seen := list.bySlug[slug]; seen {
			continue
		}
		list.bySlug[slug] = app
		key := catalogKey(slug)
		if alias, ok := pipedreamAliases[key]; ok {
			key = alias
		}
		if !composioKeys[key] {
			list.only = append(list.only, app)
		}
	}
	return list, nil
}

// pipedreamCursor starts a catalog cursor that pages through Pipedream's own
// apps, which follow Composio's: the rest of it is an offset.
const pipedreamCursor = "pd:"

// pipedreamPage is a page of the Pipedream apps Composio does not have,
// narrowed as query asks, from offset.
func (s *Service) pipedreamPage(ctx context.Context, query CatalogQuery, offset, limit int) ([]ToolkitView, string, error) {
	catalog, err := s.pipedreamCatalog(ctx)
	if err != nil {
		return nil, "", err
	}
	search := strings.ToLower(strings.TrimSpace(query.Search))
	category := strings.TrimSpace(query.Category)
	var matched []PipedreamApp
	for _, app := range catalog.only {
		if search != "" && !strings.Contains(strings.ToLower(app.Name), search) && !strings.Contains(app.Slug, search) {
			continue
		}
		if category != "" && !hasCategory(app, category) {
			continue
		}
		matched = append(matched, app)
	}
	if offset < 0 || offset > len(matched) {
		offset = len(matched)
	}
	end := min(offset+limit, len(matched))
	views := make([]ToolkitView, 0, end-offset)
	for _, app := range matched[offset:end] {
		views = append(views, s.pipedreamView(app))
	}
	next := ""
	if end < len(matched) {
		next = pipedreamCursor + strconv.Itoa(end)
	}
	return views, next, nil
}

func hasCategory(app PipedreamApp, category string) bool {
	for _, name := range app.Categories {
		if categoryID(name) == category {
			return true
		}
	}
	return false
}

// categoryID makes a Pipedream category, which is a name, look like
// Composio's ids, such as developer-tools.
func categoryID(name string) string {
	return strings.ReplaceAll(strings.ToLower(strings.TrimSpace(name)), " ", "-")
}

func (s *Service) pipedreamView(app PipedreamApp) ToolkitView {
	view := ToolkitView{
		Toolkit: app.Slug, Name: app.Name, Description: app.Description, Logo: app.Logo,
		Categories: []CategoryView{}, AuthSchemes: []string{}, Broker: s.catalogBroker(app.Slug, domain.BrokerPipedream),
	}
	for _, name := range app.Categories {
		view.Categories = append(view.Categories, CategoryView{ID: categoryID(name), Name: name})
	}
	if app.AuthType == "none" {
		view.NoAuth = true
	} else {
		view.Connectable = true
		if app.AuthType != "" {
			view.AuthSchemes = []string{strings.ToUpper(app.AuthType)}
		}
	}
	return view
}
