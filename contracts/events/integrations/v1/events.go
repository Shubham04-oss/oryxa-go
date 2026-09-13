// Package v1 contains the stable version-one events emitted by integrations.
package v1

import "time"

const (
	Source = "oryxa.integrations"

	ConnectionConnectedDetailType               = "integrations.connection.connected.v1"
	ConnectionReauthorizationRequiredDetailType = "integrations.connection.reauthorization-required.v1"
	ConnectionDisconnectedDetailType            = "integrations.connection.disconnected.v1"
	OAuthAppRegisteredDetailType                = "integrations.oauth-app.registered.v1"
	OAuthAppCredentialsRotatedDetailType        = "integrations.oauth-app.credentials-rotated.v1"
	OAuthAppDisabledDetailType                  = "integrations.oauth-app.disabled.v1"
	CatalogOperationEnabledDetailType           = "integrations.catalog.operation-enabled.v1"
	CatalogOperationDisabledDetailType          = "integrations.catalog.operation-disabled.v1"
	TriggerEnabledDetailType                    = "integrations.trigger.enabled.v1"
	TriggerDisabledDetailType                   = "integrations.trigger.disabled.v1"
	SlackMessageReceivedDetailType              = "integrations.slack.message-received.v1"
	SlackMessageSentDetailType                  = "integrations.slack.message-sent.v1"
	SlackReactionAddedDetailType                = "integrations.slack.reaction-added.v1"
	GitHubIssueCreatedDetailType                = "integrations.github.issue-created.v1"
	GitHubIssueCommentCreatedDetailType         = "integrations.github.issue-comment-created.v1"
	GoogleGmailMessageSentDetailType            = "integrations.google.gmail-message-sent.v1"
	GoogleCalendarEventCreatedDetailType        = "integrations.google.calendar-event-created.v1"
	MicrosoftOutlookEmailAcceptedDetailType     = "integrations.microsoft.outlook-email-accepted.v1"
	MicrosoftCalendarEventCreatedDetailType     = "integrations.microsoft.calendar-event-created.v1"
	MicrosoftOutlookEmailReceivedDetailType     = "integrations.microsoft.outlook-email-received.v1"
	MicrosoftCalendarEventChangedDetailType     = "integrations.microsoft.calendar-event-changed.v1"
)

type ProviderIdentity struct {
	SubjectID   string `json:"subject_id"`
	AccountID   string `json:"account_id,omitempty"`
	AccountName string `json:"account_name,omitempty"`
	TenantID    string `json:"tenant_id,omitempty"`
	TenantName  string `json:"tenant_name,omitempty"`
}

type ConnectionConnected struct {
	ConnectionID string           `json:"connection_id"`
	MemberID     string           `json:"member_id"`
	ProfileID    string           `json:"profile_id"`
	Provider     string           `json:"provider"`
	Identity     ProviderIdentity `json:"identity"`
	Scopes       []string         `json:"scopes"`
	ConnectedAt  time.Time        `json:"connected_at"`
}

type OAuthAppRegistered struct {
	ProfileID    string    `json:"profile_id"`
	Provider     string    `json:"provider"`
	Mode         string    `json:"mode"`
	Scopes       []string  `json:"scopes"`
	RegisteredBy string    `json:"registered_by"`
	RegisteredAt time.Time `json:"registered_at"`
}

type OAuthAppCredentialsRotated struct {
	ProfileID          string    `json:"profile_id"`
	Provider           string    `json:"provider"`
	CredentialRevision int64     `json:"credential_revision"`
	RotatedBy          string    `json:"rotated_by"`
	RotatedAt          time.Time `json:"rotated_at"`
}

type OAuthAppDisabled struct {
	ProfileID  string    `json:"profile_id"`
	Provider   string    `json:"provider"`
	DisabledBy string    `json:"disabled_by"`
	DisabledAt time.Time `json:"disabled_at"`
}

type ConnectionReauthorizationRequired struct {
	ConnectionID string    `json:"connection_id"`
	Provider     string    `json:"provider"`
	Reason       string    `json:"reason"`
	RequiredAt   time.Time `json:"required_at"`
}

type ConnectionDisconnected struct {
	ConnectionID   string    `json:"connection_id"`
	Provider       string    `json:"provider"`
	DisconnectedBy string    `json:"disconnected_by"`
	DisconnectedAt time.Time `json:"disconnected_at"`
}

type CatalogOperationEnabled struct {
	PieceName    string    `json:"piece_name"`
	PieceVersion string    `json:"piece_version"`
	Operation    string    `json:"operation"`
	EnabledAt    time.Time `json:"enabled_at"`
}

type CatalogOperationDisabled struct {
	PieceName    string    `json:"piece_name"`
	PieceVersion string    `json:"piece_version"`
	Operation    string    `json:"operation"`
	Reason       string    `json:"reason"`
	DisabledAt   time.Time `json:"disabled_at"`
}

type TriggerEnabled struct {
	TriggerID    string    `json:"trigger_id"`
	ConnectionID string    `json:"connection_id"`
	ProfileID    string    `json:"profile_id"`
	Provider     string    `json:"provider"`
	TriggerName  string    `json:"trigger_name"`
	EnabledBy    string    `json:"enabled_by"`
	EnabledAt    time.Time `json:"enabled_at"`
}

type TriggerDisabled struct {
	TriggerID    string    `json:"trigger_id"`
	ConnectionID string    `json:"connection_id"`
	Provider     string    `json:"provider"`
	DisabledBy   string    `json:"disabled_by"`
	DisabledAt   time.Time `json:"disabled_at"`
}

// MicrosoftOutlookEmailReceived is a content-free receipt for an Outlook
// mailbox notification. Consumers must fetch mail content through a separate
// explicitly authorized operation.
type MicrosoftOutlookEmailReceived struct {
	TriggerID    string    `json:"trigger_id"`
	ConnectionID string    `json:"connection_id"`
	DeliveryID   string    `json:"delivery_id"`
	ReceivedAt   time.Time `json:"received_at"`
}

// MicrosoftCalendarEventChanged is a content-free receipt for an Outlook
// calendar notification. Event titles, attendees, location, and body never
// cross the integrations event boundary.
type MicrosoftCalendarEventChanged struct {
	TriggerID    string    `json:"trigger_id"`
	ConnectionID string    `json:"connection_id"`
	DeliveryID   string    `json:"delivery_id"`
	ReceivedAt   time.Time `json:"received_at"`
}

// SlackMessageReceived is the stable, credential-free domain projection of
// Slack's message event. Raw webhook headers and provider tokens never cross
// the integrations boundary.
type SlackMessageReceived struct {
	TriggerID    string         `json:"trigger_id"`
	ConnectionID string         `json:"connection_id"`
	WorkspaceID  string         `json:"workspace_id"`
	Messages     []SlackMessage `json:"messages"`
	ReceivedAt   time.Time      `json:"received_at"`
}

type SlackMessage struct {
	ChannelID       string `json:"channel_id"`
	UserID          string `json:"user_id,omitempty"`
	Text            string `json:"text,omitempty"`
	Timestamp       string `json:"timestamp"`
	ThreadTimestamp string `json:"thread_timestamp,omitempty"`
	Subtype         string `json:"subtype,omitempty"`
}

// SlackMessageSent is emitted only after the provider confirms the write and
// the durable action execution is completed. Message content is intentionally
// omitted from the cross-service event.
type SlackMessageSent struct {
	ExecutionID    string    `json:"execution_id"`
	ConnectionID   string    `json:"connection_id"`
	ChannelID      string    `json:"channel_id"`
	MessageTS      string    `json:"message_timestamp"`
	ThreadTS       string    `json:"thread_timestamp,omitempty"`
	RequestedBy    string    `json:"requested_by"`
	ProviderSentAt time.Time `json:"provider_sent_at"`
}

type SlackReactionAdded struct {
	ExecutionID      string    `json:"execution_id"`
	ConnectionID     string    `json:"connection_id"`
	ChannelID        string    `json:"channel_id"`
	MessageTimestamp string    `json:"message_timestamp"`
	Reaction         string    `json:"reaction"`
	RequestedBy      string    `json:"requested_by"`
	AddedAt          time.Time `json:"added_at"`
}

type GitHubIssueCreated struct {
	ExecutionID  string    `json:"execution_id"`
	ConnectionID string    `json:"connection_id"`
	Repository   string    `json:"repository"`
	IssueNumber  int64     `json:"issue_number"`
	URL          string    `json:"url"`
	RequestedBy  string    `json:"requested_by"`
	CreatedAt    time.Time `json:"created_at"`
}

type GitHubIssueCommentCreated struct {
	ExecutionID  string    `json:"execution_id"`
	ConnectionID string    `json:"connection_id"`
	Repository   string    `json:"repository"`
	IssueNumber  int64     `json:"issue_number"`
	CommentID    int64     `json:"comment_id"`
	URL          string    `json:"url"`
	RequestedBy  string    `json:"requested_by"`
	CreatedAt    time.Time `json:"created_at"`
}

// GoogleGmailMessageSent deliberately excludes recipients, subject, and body.
// Delivery services and other consumers only receive the domain fact that the
// member-authorized provider accepted a message.
type GoogleGmailMessageSent struct {
	ExecutionID  string    `json:"execution_id"`
	ConnectionID string    `json:"connection_id"`
	MessageID    string    `json:"message_id"`
	ThreadID     string    `json:"thread_id,omitempty"`
	RequestedBy  string    `json:"requested_by"`
	SentAt       time.Time `json:"sent_at"`
}

// GoogleCalendarEventCreated omits title, description, location, and attendee
// data so downstream services can react without receiving calendar content.
type GoogleCalendarEventCreated struct {
	ExecutionID  string    `json:"execution_id"`
	ConnectionID string    `json:"connection_id"`
	CalendarID   string    `json:"calendar_id"`
	EventID      string    `json:"event_id"`
	URL          string    `json:"url,omitempty"`
	RequestedBy  string    `json:"requested_by"`
	CreatedAt    time.Time `json:"created_at"`
}

// MicrosoftOutlookEmailAccepted records Graph's accepted sendMail write. The
// endpoint deliberately does not return a message ID, and recipients, subject,
// and body must never leave the integrations boundary.
type MicrosoftOutlookEmailAccepted struct {
	ExecutionID  string    `json:"execution_id"`
	ConnectionID string    `json:"connection_id"`
	RequestedBy  string    `json:"requested_by"`
	AcceptedAt   time.Time `json:"accepted_at"`
}

// MicrosoftCalendarEventCreated omits the event title, body, location, and
// attendees. Downstream consumers receive only the durable identity and, when
// Graph supplied one, its HTTPS web link.
type MicrosoftCalendarEventCreated struct {
	ExecutionID  string    `json:"execution_id"`
	ConnectionID string    `json:"connection_id"`
	CalendarID   string    `json:"calendar_id"`
	EventID      string    `json:"event_id"`
	URL          string    `json:"url,omitempty"`
	RequestedBy  string    `json:"requested_by"`
	CreatedAt    time.Time `json:"created_at"`
}
