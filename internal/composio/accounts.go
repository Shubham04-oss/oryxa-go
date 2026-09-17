package composio

import (
	"context"
	"net/http"
	"net/url"
	"time"
)

// Connected account statuses. Only an ACTIVE account can run tools.
const (
	StatusInitializing = "INITIALIZING"
	StatusInitiated    = "INITIATED"
	StatusActive       = "ACTIVE"
	StatusFailed       = "FAILED"
	StatusExpired      = "EXPIRED"
	StatusInactive     = "INACTIVE"
	StatusRevoked      = "REVOKED"
)

// ConnectedAccount is one user's connection to one toolkit. Composio holds
// the credential behind it; nothing here carries one.
type ConnectedAccount struct {
	ID           string
	UserID       string
	Toolkit      string
	AuthConfigID string
	AuthScheme   string
	// IsComposioManaged says the connection went through Composio's own app.
	IsComposioManaged bool
	Status            string
	StatusReason      string
	// WordID is a short, readable label, such as github_red-castle, for
	// telling several accounts on one toolkit apart.
	WordID     string
	Alias      string
	IsDisabled bool
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

// connectedAccountWire decodes only what Oryxa keeps. The answer also carries
// the connection's state, which is left undecoded so no credential Composio
// might include ever reaches a struct, a log or a database.
type connectedAccountWire struct {
	ID      string `json:"id"`
	UserID  string `json:"user_id"`
	Toolkit struct {
		Slug string `json:"slug"`
	} `json:"toolkit"`
	AuthConfig struct {
		ID                string `json:"id"`
		AuthScheme        string `json:"auth_scheme"`
		IsComposioManaged bool   `json:"is_composio_managed"`
	} `json:"auth_config"`
	Status       string  `json:"status"`
	StatusReason *string `json:"status_reason"`
	WordID       *string `json:"word_id"`
	Alias        *string `json:"alias"`
	IsDisabled   bool    `json:"is_disabled"`
	CreatedAt    string  `json:"created_at"`
	UpdatedAt    string  `json:"updated_at"`
}

func (wire connectedAccountWire) account() ConnectedAccount {
	account := ConnectedAccount{
		ID: wire.ID, UserID: wire.UserID, Toolkit: wire.Toolkit.Slug,
		AuthConfigID: wire.AuthConfig.ID, AuthScheme: wire.AuthConfig.AuthScheme,
		IsComposioManaged: wire.AuthConfig.IsComposioManaged,
		Status:            wire.Status, IsDisabled: wire.IsDisabled,
		CreatedAt: timeOf(wire.CreatedAt), UpdatedAt: timeOf(wire.UpdatedAt),
	}
	if wire.StatusReason != nil {
		account.StatusReason = *wire.StatusReason
	}
	if wire.WordID != nil {
		account.WordID = *wire.WordID
	}
	if wire.Alias != nil {
		account.Alias = *wire.Alias
	}
	return account
}

// LinkRequest starts a connection a user completes in their browser.
type LinkRequest struct {
	AuthConfigID string
	UserID       string
	// CallbackURL is where Composio sends the browser afterwards. Composio
	// appends status and connected_account_id to it, keeping what it already
	// carries, and neither is to be trusted until the account is read back.
	CallbackURL string
	Alias       string
}

// ConnectLink is where to send the user, and the account it will become.
type ConnectLink struct {
	RedirectURL        string
	ConnectedAccountID string
	ExpiresAt          time.Time
}

// CreateConnectLink starts a connection with Composio's hosted flow, the
// current way to connect an account for every auth scheme.
func (client *Client) CreateConnectLink(ctx context.Context, request LinkRequest) (ConnectLink, error) {
	body := map[string]any{"auth_config_id": request.AuthConfigID, "user_id": request.UserID}
	if request.CallbackURL != "" {
		body["callback_url"] = request.CallbackURL
	}
	if request.Alias != "" {
		body["alias"] = request.Alias
	}
	var answer struct {
		RedirectURL        string `json:"redirect_url"`
		ConnectedAccountID string `json:"connected_account_id"`
		ExpiresAt          string `json:"expires_at"`
	}
	if err := client.call(ctx, http.MethodPost, "/connected_accounts/link", nil, body, &answer); err != nil {
		return ConnectLink{}, err
	}
	return ConnectLink{
		RedirectURL: answer.RedirectURL, ConnectedAccountID: answer.ConnectedAccountID, ExpiresAt: timeOf(answer.ExpiresAt),
	}, nil
}

// ConnectedAccount reads one account back.
func (client *Client) ConnectedAccount(ctx context.Context, id string) (ConnectedAccount, error) {
	var wire connectedAccountWire
	if err := client.call(ctx, http.MethodGet, "/connected_accounts/"+pathEscape(id), nil, nil, &wire); err != nil {
		return ConnectedAccount{}, err
	}
	return wire.account(), nil
}

// AccountQuery narrows an account listing.
type AccountQuery struct {
	UserIDs  []string
	Toolkits []string
	Statuses []string
	Cursor   string
	Limit    int
}

// ConnectedAccounts lists accounts, a page at a time.
func (client *Client) ConnectedAccounts(ctx context.Context, query AccountQuery) (Page[ConnectedAccount], error) {
	values := url.Values{}
	addAll(values, "user_ids", query.UserIDs)
	addAll(values, "toolkit_slugs", query.Toolkits)
	addAll(values, "statuses", query.Statuses)
	var answer struct {
		Items      []connectedAccountWire `json:"items"`
		NextCursor *string                `json:"next_cursor"`
	}
	if err := client.call(ctx, http.MethodGet, "/connected_accounts", pageQuery(values, query.Cursor, query.Limit), nil, &answer); err != nil {
		return Page[ConnectedAccount]{}, err
	}
	page := Page[ConnectedAccount]{NextCursor: cursorOf(answer.NextCursor)}
	for _, wire := range answer.Items {
		page.Items = append(page.Items, wire.account())
	}
	return page, nil
}

// DeleteConnectedAccount removes an account. With revoke, Composio also
// revokes the credential at the provider, asynchronously and for good;
// without it the provider still honours the token until it lapses.
func (client *Client) DeleteConnectedAccount(ctx context.Context, id string, revoke bool) error {
	var query url.Values
	if revoke {
		query = url.Values{"revoke_on_delete": {"true"}}
	}
	return client.call(ctx, http.MethodDelete, "/connected_accounts/"+pathEscape(id), query, nil, nil)
}
