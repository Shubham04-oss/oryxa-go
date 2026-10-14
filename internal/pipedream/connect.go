package pipedream

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// ConnectToken lets one external user connect an account in Pipedream's
// hosted flow, at ConnectLinkURL, until ExpiresAt.
type ConnectToken struct {
	Token          string    `json:"token"`
	ConnectLinkURL string    `json:"connect_link_url"`
	ExpiresAt      time.Time `json:"expires_at"`
}

// MaxConnectTokenLifetime is the longest a Connect token may live; it is
// also what Pipedream gives one when no lifetime is asked for.
const MaxConnectTokenLifetime = 4 * time.Hour

// CreateConnectToken makes a token for externalUserID to connect an account
// with. The redirects are where the hosted flow sends the browser afterwards,
// and are optional; so is expiresIn, which is rounded down to seconds
// (https://pipedream.com/docs/connect/api-reference/create-connect-token).
func (client *Client) CreateConnectToken(ctx context.Context, externalUserID, successRedirect, errorRedirect string, expiresIn time.Duration) (ConnectToken, error) {
	if expiresIn > MaxConnectTokenLifetime {
		return ConnectToken{}, fmt.Errorf("%w: a Connect token lives at most %s", ErrInvalid, MaxConnectTokenLifetime)
	}
	body := map[string]any{"external_user_id": externalUserID}
	if successRedirect != "" {
		body["success_redirect_uri"] = successRedirect
	}
	if errorRedirect != "" {
		body["error_redirect_uri"] = errorRedirect
	}
	if seconds := int(expiresIn / time.Second); seconds > 0 {
		body["expires_in"] = seconds
	}
	var token ConnectToken
	if err := client.call(ctx, http.MethodPost, client.project("/tokens"), nil, body, &token); err != nil {
		return ConnectToken{}, err
	}
	return token, nil
}

// Account is one external user's connection to one app. Pipedream holds the
// credential behind it; this never asks for it (include_credentials), and
// has no field one could land in.
type Account struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	ExternalID string `json:"external_id"`
	// Healthy is false while Pipedream retries an account whose token
	// refresh or test request failed; Dead means it gave up.
	Healthy   bool      `json:"healthy"`
	Dead      bool      `json:"dead"`
	Error     string    `json:"error"`
	App       App       `json:"app"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// ListAccounts lists externalUserID's accounts, narrowed to one app when app
// is not empty. The endpoint is not paged
// (https://pipedream.com/docs/connect/api-reference/list-accounts-for-external-user).
func (client *Client) ListAccounts(ctx context.Context, externalUserID, app string) ([]Account, error) {
	var query url.Values
	if app != "" {
		query = url.Values{"app": {app}}
	}
	var accounts []Account
	err := client.call(ctx, http.MethodGet, client.project("/users/"+url.PathEscape(externalUserID)+"/accounts"), query, nil, &accounts)
	// Pipedream knows an external user only once they have connected
	// something, and answers 404 for anyone else (seen live 2026-10-13; the
	// reference does not say), which means they have no accounts.
	if errors.Is(err, ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return accounts, nil
}

// GetAccount reads one account back.
func (client *Client) GetAccount(ctx context.Context, id string) (Account, error) {
	var account Account
	if err := client.call(ctx, http.MethodGet, client.project("/accounts/"+url.PathEscape(id)), nil, nil, &account); err != nil {
		return Account{}, err
	}
	return account, nil
}

// DeleteAccount removes an account and the credential Pipedream holds for it.
func (client *Client) DeleteAccount(ctx context.Context, id string) error {
	return client.call(ctx, http.MethodDelete, client.project("/accounts/"+url.PathEscape(id)), nil, nil, nil)
}

// DeleteUser removes an external user with every account and resource
// Pipedream holds for them, the call to make when a member leaves.
func (client *Client) DeleteUser(ctx context.Context, externalUserID string) error {
	return client.call(ctx, http.MethodDelete, client.project("/users/"+url.PathEscape(externalUserID)), nil, nil, nil)
}

// App is one app in Pipedream's catalog.
type App struct {
	ID          string   `json:"id"`
	NameSlug    string   `json:"name_slug"`
	Name        string   `json:"name"`
	AuthType    string   `json:"auth_type"`
	Description string   `json:"description"`
	ImgSrc      string   `json:"img_src"`
	Categories  []string `json:"categories"`
	// FeaturedWeight orders apps roughly by popularity.
	FeaturedWeight float64 `json:"featured_weight"`
}

// AppQuery narrows the catalog.
type AppQuery struct {
	Search string
	After  string
	Limit  int
	// HasActions keeps only apps with at least one action.
	HasActions bool
}

// ListApps lists the catalog, a page at a time. It is not scoped to a
// project (https://pipedream.com/docs/connect/api-reference/list-apps).
func (client *Client) ListApps(ctx context.Context, query AppQuery) (Page[App], error) {
	values := pageQuery(query.After, query.Limit)
	if query.Search != "" {
		values.Set("q", query.Search)
	}
	if query.HasActions {
		values.Set("has_actions", "true")
	}
	var answer struct {
		Data     []App    `json:"data"`
		PageInfo pageInfo `json:"page_info"`
	}
	if err := client.call(ctx, http.MethodGet, "/connect/apps", values, nil, &answer); err != nil {
		return Page[App]{}, err
	}
	return Page[App]{Items: answer.Data, NextCursor: answer.PageInfo.next(len(answer.Data))}, nil
}

// Component is an action or a trigger.
type Component struct {
	// Key is what RunAction and DeployTrigger name it by, such as
	// gitlab-list-commits.
	Key         string `json:"key"`
	Name        string `json:"name"`
	Version     string `json:"version"`
	Description string `json:"description"`
	// Type is action, or source for a trigger: the reference says trigger,
	// but the live API answers source (seen 2026-10-13).
	Type string `json:"component_type"`
	// ConfigurableProps is left raw: it is a list of many shapes, read by
	// whoever builds the props.
	ConfigurableProps json.RawMessage `json:"configurable_props"`
	// Annotations is nil when the component declares none.
	Annotations *Annotations `json:"annotations"`
}

// Annotations are a component's MCP-style hints about what it does. Each is
// nil when not declared, which says nothing either way.
type Annotations struct {
	Title           string `json:"title"`
	ReadOnlyHint    *bool  `json:"readOnlyHint"`
	DestructiveHint *bool  `json:"destructiveHint"`
	IdempotentHint  *bool  `json:"idempotentHint"`
	OpenWorldHint   *bool  `json:"openWorldHint"`
}

// ReadOnly says whether a component declares that it changes nothing. One
// with no annotation is not assumed to be.
func (component Component) ReadOnly() bool {
	return component.Annotations != nil && component.Annotations.ReadOnlyHint != nil && *component.Annotations.ReadOnlyHint
}

// ComponentQuery narrows a listing of actions or triggers.
type ComponentQuery struct {
	// App is the app's ID or name slug.
	App    string
	Search string
	After  string
	Limit  int
}

// ListActions lists actions, a page at a time
// (https://pipedream.com/docs/connect/api-reference/list-actions).
func (client *Client) ListActions(ctx context.Context, query ComponentQuery) (Page[Component], error) {
	return client.components(ctx, "/actions", query)
}

// ListTriggers lists triggers, a page at a time
// (https://pipedream.com/docs/connect/api-reference/list-triggers).
func (client *Client) ListTriggers(ctx context.Context, query ComponentQuery) (Page[Component], error) {
	return client.components(ctx, "/triggers", query)
}

func (client *Client) components(ctx context.Context, path string, query ComponentQuery) (Page[Component], error) {
	values := pageQuery(query.After, query.Limit)
	if query.App != "" {
		values.Set("app", query.App)
	}
	if query.Search != "" {
		values.Set("q", query.Search)
	}
	var answer struct {
		Data     []Component `json:"data"`
		PageInfo pageInfo    `json:"page_info"`
	}
	if err := client.call(ctx, http.MethodGet, client.project(path), values, nil, &answer); err != nil {
		return Page[Component]{}, err
	}
	return Page[Component]{Items: answer.Data, NextCursor: answer.PageInfo.next(len(answer.Data))}, nil
}

// ActionResult is what a run returned: Ret is the action's return value and
// Exports what it exported with $.export, both left raw.
type ActionResult struct {
	Ret     json.RawMessage            `json:"ret"`
	Exports map[string]json.RawMessage `json:"exports"`
}

// RunAction runs action id as externalUserID. An app prop in configuredProps
// names the account to use, {"authProvisionId": "apn_…"}. When the action
// fails the error is an *Error that errors.Is ErrApp, with the Origin
// Pipedream attributed it to
// (https://pipedream.com/docs/connect/api-reference/run-action).
func (client *Client) RunAction(ctx context.Context, id, externalUserID string, configuredProps map[string]any) (ActionResult, error) {
	body := map[string]any{"id": id, "external_user_id": externalUserID, "configured_props": configuredProps}
	if configuredProps == nil {
		body["configured_props"] = map[string]any{}
	}
	var answer struct {
		ActionResult
		Error json.RawMessage `json:"error"`
	}
	if err := client.call(ctx, http.MethodPost, client.project("/actions/run"), nil, body, &answer); err != nil {
		return ActionResult{}, err
	}
	// ponytail: the reference does not say whether a failed run answers 200
	// or an error status, so an error object is honoured on either.
	failure := &Error{Status: http.StatusOK}
	if attribute(failure, answer.Error) {
		return answer.ActionResult, failure
	}
	return answer.ActionResult, nil
}

// DeployedTrigger is a trigger deployed for one external user.
type DeployedTrigger struct {
	ID           string `json:"id"`
	ComponentID  string `json:"component_id"`
	ComponentKey string `json:"component_key"`
	Name         string `json:"name"`
	Active       bool   `json:"active"`
	// SigningKey signs every delivery to the trigger's webhook URL; see
	// VerifyWebhook. Pipedream answers it only on deploy, and only when a
	// webhook URL was given.
	SigningKey string `json:"webhook_signing_key"`
}

// DeployTrigger deploys trigger id for externalUserID, delivering what it
// emits to webhookURL. A polling trigger checks every timer when timer is not
// zero, which sets its timer prop, the documented way to choose the interval
// (https://pipedream.com/docs/connect/components/triggers#setting-the-polling-interval).
// Historical events are not emitted on deploy, so connecting an app does not
// replay its past.
func (client *Client) DeployTrigger(ctx context.Context, id, externalUserID string, props map[string]any, webhookURL string, timer time.Duration) (DeployedTrigger, error) {
	configured := make(map[string]any, len(props)+1)
	for name, value := range props {
		configured[name] = value
	}
	if seconds := int(timer / time.Second); seconds > 0 {
		configured["timer"] = map[string]any{"intervalSeconds": seconds}
	}
	body := map[string]any{
		"id": id, "external_user_id": externalUserID, "configured_props": configured,
		"webhook_url": webhookURL, "emit_on_deploy": false,
	}
	var answer struct {
		Data DeployedTrigger `json:"data"`
	}
	if err := client.call(ctx, http.MethodPost, client.project("/triggers/deploy"), nil, body, &answer); err != nil {
		return DeployedTrigger{}, err
	}
	if answer.Data.ID == "" {
		return DeployedTrigger{}, fmt.Errorf("%w: the deploy answered no trigger", ErrUnavailable)
	}
	return answer.Data, nil
}

// DeleteDeployedTrigger stops a deployed trigger and deletes it.
func (client *Client) DeleteDeployedTrigger(ctx context.Context, id, externalUserID string) error {
	return client.call(ctx, http.MethodDelete, client.project("/deployed-triggers/"+url.PathEscape(id)),
		url.Values{"external_user_id": {externalUserID}}, nil, nil)
}

// UsageRecord is the Connect credits the workspace used in one window.
type UsageRecord struct {
	Start, End time.Time
	Credits    int
	// The parts of Credits, each zero when Pipedream did not say.
	ActionRunCredits, ProxyCredits, TriggerEmitCredits int
}

// Usage lists the credits used between start and end
// (https://pipedream.com/docs/connect/api-reference/list-usage-records).
func (client *Client) Usage(ctx context.Context, start, end time.Time) ([]UsageRecord, error) {
	query := url.Values{
		"start_ts": {strconv.FormatInt(start.Unix(), 10)}, "end_ts": {strconv.FormatInt(end.Unix(), 10)},
	}
	var answer struct {
		Data []struct {
			CreditsUsed       int   `json:"credits_used"`
			ActionRunCredits  int   `json:"action_run_credits_used"`
			ProxyCredits      int   `json:"proxy_credits_used"`
			SourceEmitCredits int   `json:"source_emit_credits_used"`
			Start             int64 `json:"usage_start_ts"`
			End               int64 `json:"usage_end_ts"`
		} `json:"data"`
	}
	if err := client.call(ctx, http.MethodGet, "/connect/usage", query, nil, &answer); err != nil {
		return nil, err
	}
	records := make([]UsageRecord, 0, len(answer.Data))
	for _, wire := range answer.Data {
		records = append(records, UsageRecord{
			Start: time.Unix(wire.Start, 0), End: time.Unix(wire.End, 0), Credits: wire.CreditsUsed,
			ActionRunCredits: wire.ActionRunCredits, ProxyCredits: wire.ProxyCredits, TriggerEmitCredits: wire.SourceEmitCredits,
		})
	}
	return records, nil
}

// pageQuery starts a listing's query with its cursor and limit.
func pageQuery(after string, limit int) url.Values {
	query := url.Values{}
	if after != "" {
		query.Set("after", after)
	}
	if limit > 0 {
		query.Set("limit", strconv.Itoa(limit))
	}
	return query
}
