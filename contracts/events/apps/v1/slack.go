package v1

import "time"

// The Slack surface: Oryxa's one Slack app, and what apps tells the rest of
// Oryxa about it. A workspace installs the app for one organization, and
// people in it then talk to Oryxa by @-mentioning the bot and by clicking the
// buttons on the notices it posts.
//
// These events carry ids and nothing else. What a person typed or clicked
// stays in apps, in a record the services that act on it read with a service
// token, and is forgotten after seven days: a message's text, a button's
// ticket and a person's Slack id are never put on the bus. Anything that
// hears one of them reads the record it names, so a forged or replayed event
// can point at a record but never invent one.
const (
	// SlackMessageReceivedDetailType is a message that reached the bot and
	// passed Slack's signature: an @-mention of it. InboundID names the record
	// that holds what was said.
	SlackMessageReceivedDetailType = "apps.slack.message.received.v1"
	// SlackActionReceivedDetailType is a click on a button of a message the
	// bot posted, verified the same way. InboundID names the record that holds
	// which button, and its ticket.
	SlackActionReceivedDetailType = "apps.slack.action.received.v1"
	// SlackInstallChangedDetailType is a workspace's install coming or going:
	// installed, installed again, or ended by Slack, by an admin, or by Slack
	// refusing the bot's token.
	SlackInstallChangedDetailType = "apps.slack.install.changed.v1"
)

// SlackMessageReceived is a verified message to the bot, by reference.
type SlackMessageReceived struct {
	InboundID      string `json:"inbound_id"`
	OrganizationID string `json:"organization_id"`
	// TeamID is the Slack workspace the message came from.
	TeamID     string    `json:"team_id"`
	ReceivedAt time.Time `json:"received_at"`
}

// SlackActionReceived is a verified click, by reference. It has the same
// shape as SlackMessageReceived, and is its own event because the two go to
// different handlers: a message is routed, a click is a decision.
type SlackActionReceived struct {
	InboundID      string    `json:"inbound_id"`
	OrganizationID string    `json:"organization_id"`
	TeamID         string    `json:"team_id"`
	ReceivedAt     time.Time `json:"received_at"`
}

// States of a workspace's install, as SlackInstallChanged.State says.
const (
	// SlackInstallActive is an install the bot can post through.
	SlackInstallActive = "active"
	// SlackInstallEnded is an install that no longer works. The workspace can
	// install the app again, for the same organization or another.
	SlackInstallEnded = "ended"
)

// Why an install changed: SlackInstallChanged.Reason.
const (
	// SlackInstalled is a workspace installing the app for the first time, or
	// again after it ended.
	SlackInstalled = "installed"
	// SlackReinstalled is an install that was already active being installed
	// over, such as to pick up new scopes. Its bot token was replaced.
	SlackReinstalled = "reinstalled"
	// SlackAppUninstalled is Slack saying an admin removed the app from the
	// workspace.
	SlackAppUninstalled = "app_uninstalled"
	// SlackTokensRevoked is Slack saying the bot's token was revoked.
	SlackTokensRevoked = "tokens_revoked"
	// SlackRemovedByAdmin is an Oryxa organization admin disconnecting Slack.
	SlackRemovedByAdmin = "removed_by_admin"
	// SlackTokenInvalid is Slack answering a call with the bot's token that
	// the token is no longer any good, with no event to say so.
	SlackTokenInvalid = "token_invalid"
)

// SlackInstallChanged is a workspace's install becoming active or ending. It
// never carries the bot's token.
type SlackInstallChanged struct {
	OrganizationID string `json:"organization_id"`
	TeamID         string `json:"team_id"`
	// State is SlackInstallActive or SlackInstallEnded, and Reason one of the
	// Slack constants above.
	State  string `json:"state"`
	Reason string `json:"reason"`
	// ActorID is the member who installed or disconnected it, when a member
	// did; it is left out when Slack ended the install.
	ActorID   string    `json:"actor_id,omitempty"`
	ChangedAt time.Time `json:"changed_at"`
}
