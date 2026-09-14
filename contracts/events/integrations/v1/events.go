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
	SlackMessageDeletedDetailType               = "integrations.slack.message-deleted.v1"
	SlackMessageScheduledDetailType             = "integrations.slack.message-scheduled.v1"
	SlackReactionAddedDetailType                = "integrations.slack.reaction-added.v1"
	SlackReactionRemovedDetailType              = "integrations.slack.reaction-removed.v1"
	SlackChannelMutatedDetailType               = "integrations.slack.channel-mutated.v1"
	SlackFileUploadedDetailType                 = "integrations.slack.file-uploaded.v1"
	SlackReactionReceivedDetailType             = "integrations.slack.reaction-received.v1"
	SlackChannelCreatedReceivedDetailType       = "integrations.slack.channel-created.v1"
	SlackUserJoinedDetailType                   = "integrations.slack.user-joined.v1"
	SlackItemStarredDetailType                  = "integrations.slack.item-starred.v1"
	SlackEmojiChangedDetailType                 = "integrations.slack.emoji-changed.v1"
	GitHubIssueCreatedDetailType                = "integrations.github.issue-created.v1"
	GitHubIssueCommentCreatedDetailType         = "integrations.github.issue-comment-created.v1"
	GitHubIssueMutatedDetailType                = "integrations.github.issue-mutated.v1"
	GitHubPullRequestCreatedDetailType          = "integrations.github.pull-request-created.v1"
	GitHubPullRequestMutatedDetailType          = "integrations.github.pull-request-mutated.v1"
	GitHubRepositoryCreatedDetailType           = "integrations.github.repository-created.v1"
	GitHubRepositoryMutatedDetailType           = "integrations.github.repository-mutated.v1"
	GitHubRefMutatedDetailType                  = "integrations.github.ref-mutated.v1"
	GitHubReleaseCreatedDetailType              = "integrations.github.release-created.v1"
	GitHubMilestoneMutatedDetailType            = "integrations.github.milestone-mutated.v1"
	GitHubLabelMutatedDetailType                = "integrations.github.label-mutated.v1"
	GitHubIssueOpenedDetailType                 = "integrations.github.issue-opened.v1"
	GitHubPullRequestOpenedDetailType           = "integrations.github.pull-request-opened.v1"
	GitHubFileMutatedDetailType                 = "integrations.github.file-mutated.v1"
	GitHubGistCreatedDetailType                 = "integrations.github.gist-created.v1"
	GitHubWorkflowDispatchedDetailType          = "integrations.github.workflow-dispatched.v1"
	GitHubStarReceivedDetailType                = "integrations.github.star-received.v1"
	GitHubPushReceivedDetailType                = "integrations.github.push-received.v1"
	GitHubReleasePublishedDetailType            = "integrations.github.release-published.v1"
	GitHubReviewSubmittedDetailType             = "integrations.github.review-submitted.v1"
	GoogleGmailMessageSentDetailType            = "integrations.google.gmail-message-sent.v1"
	GoogleGmailDraftCreatedDetailType           = "integrations.google.gmail-draft-created.v1"
	GoogleGmailEmailForwardedDetailType         = "integrations.google.gmail-email-forwarded.v1"
	GoogleGmailEmailMutatedDetailType           = "integrations.google.gmail-email-mutated.v1"
	GoogleGmailMessageReceivedDetailType        = "integrations.google.gmail-message-received.v1"
	GoogleCalendarEventCreatedDetailType        = "integrations.google.calendar-event-created.v1"
	GoogleCalendarEventUpdatedDetailType        = "integrations.google.calendar-event-updated.v1"
	GoogleCalendarEventDeletedDetailType        = "integrations.google.calendar-event-deleted.v1"
	GoogleCalendarEventChangedDetailType        = "integrations.google.calendar-event-changed.v1"
	GoogleDriveItemCreatedDetailType            = "integrations.google.drive-item-created.v1"
	GoogleDriveItemCopiedDetailType             = "integrations.google.drive-item-copied.v1"
	GoogleDriveItemMutatedDetailType            = "integrations.google.drive-item-mutated.v1"
	GoogleDriveFileChangedDetailType            = "integrations.google.drive-file-changed.v1"
	MicrosoftOutlookEmailAcceptedDetailType     = "integrations.microsoft.outlook-email-accepted.v1"
	MicrosoftOutlookDraftCreatedDetailType      = "integrations.microsoft.outlook-draft-created.v1"
	MicrosoftOutlookEmailForwardedDetailType    = "integrations.microsoft.outlook-email-forwarded.v1"
	MicrosoftOutlookEmailMutatedDetailType      = "integrations.microsoft.outlook-email-mutated.v1"
	MicrosoftCalendarEventCreatedDetailType     = "integrations.microsoft.calendar-event-created.v1"
	MicrosoftCalendarEventDeletedDetailType     = "integrations.microsoft.calendar-event-deleted.v1"
	MicrosoftOneDriveItemCopiedDetailType       = "integrations.microsoft.onedrive-item-copied.v1"
	MicrosoftOneDriveItemCreatedDetailType      = "integrations.microsoft.onedrive-item-created.v1"
	MicrosoftOutlookEmailReceivedDetailType     = "integrations.microsoft.outlook-email-received.v1"
	MicrosoftCalendarEventChangedDetailType     = "integrations.microsoft.calendar-event-changed.v1"
	GoogleDocsDocumentCreatedDetailType         = "integrations.google.docs-document-created.v1"
	GoogleDocsDocumentCopiedDetailType          = "integrations.google.docs-document-copied.v1"
	GoogleDocsDocumentMutatedDetailType         = "integrations.google.docs-document-mutated.v1"
	GoogleSheetsSpreadsheetCreatedDetailType    = "integrations.google.sheets-spreadsheet-created.v1"
	GoogleSheetsSpreadsheetMutatedDetailType    = "integrations.google.sheets-spreadsheet-mutated.v1"
	MicrosoftTeamsChannelCreatedDetailType      = "integrations.microsoft.teams-channel-created.v1"
	MicrosoftTeamsMessageSentDetailType         = "integrations.microsoft.teams-message-sent.v1"
	MicrosoftTeamsMessageDeletedDetailType      = "integrations.microsoft.teams-message-deleted.v1"
	MicrosoftSharePointItemCreatedDetailType    = "integrations.microsoft.sharepoint-item-created.v1"
	MicrosoftSharePointItemCopiedDetailType     = "integrations.microsoft.sharepoint-item-copied.v1"
	MicrosoftSharePointItemMutatedDetailType    = "integrations.microsoft.sharepoint-item-mutated.v1"
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

type SlackMessageDeleted struct {
	ExecutionID  string    `json:"execution_id"`
	ConnectionID string    `json:"connection_id"`
	ChannelID    string    `json:"channel_id"`
	MessageTS    string    `json:"message_timestamp"`
	RequestedBy  string    `json:"requested_by"`
	DeletedAt    time.Time `json:"deleted_at"`
}

type SlackMessageScheduled struct {
	ExecutionID        string    `json:"execution_id"`
	ConnectionID       string    `json:"connection_id"`
	ChannelID          string    `json:"channel_id"`
	ScheduledMessageID string    `json:"scheduled_message_id"`
	RequestedBy        string    `json:"requested_by"`
	ScheduledAt        time.Time `json:"scheduled_at"`
}

type SlackReactionRemoved struct {
	ExecutionID      string    `json:"execution_id"`
	ConnectionID     string    `json:"connection_id"`
	ChannelID        string    `json:"channel_id"`
	MessageTimestamp string    `json:"message_timestamp"`
	Reaction         string    `json:"reaction"`
	RequestedBy      string    `json:"requested_by"`
	RemovedAt        time.Time `json:"removed_at"`
}

type SlackChannelMutated struct {
	ExecutionID  string    `json:"execution_id"`
	ConnectionID string    `json:"connection_id"`
	ChannelID    string    `json:"channel_id"`
	Mutation     string    `json:"mutation"`
	RequestedBy  string    `json:"requested_by"`
	MutatedAt    time.Time `json:"mutated_at"`
}

// SlackFileUploaded records the Slack file identity and the Oryxa object used
// as the source. Bytes and private download URLs never leave the boundary.
type SlackFileUploaded struct {
	ExecutionID  string    `json:"execution_id"`
	ConnectionID string    `json:"connection_id"`
	SlackFileID  string    `json:"slack_file_id"`
	FileID       string    `json:"file_id,omitempty"`
	RequestedBy  string    `json:"requested_by"`
	UploadedAt   time.Time `json:"uploaded_at"`
}

type SlackReactionReceived struct {
	TriggerID    string    `json:"trigger_id"`
	ConnectionID string    `json:"connection_id"`
	WorkspaceID  string    `json:"workspace_id"`
	ChannelID    string    `json:"channel_id,omitempty"`
	UserID       string    `json:"user_id,omitempty"`
	Reaction     string    `json:"reaction,omitempty"`
	ItemTS       string    `json:"item_timestamp,omitempty"`
	Removed      bool      `json:"removed"`
	ReceivedAt   time.Time `json:"received_at"`
}

type SlackChannelCreatedReceived struct {
	TriggerID    string    `json:"trigger_id"`
	ConnectionID string    `json:"connection_id"`
	WorkspaceID  string    `json:"workspace_id"`
	ChannelID    string    `json:"channel_id"`
	ReceivedAt   time.Time `json:"received_at"`
}

type SlackUserJoined struct {
	TriggerID    string    `json:"trigger_id"`
	ConnectionID string    `json:"connection_id"`
	WorkspaceID  string    `json:"workspace_id"`
	UserID       string    `json:"user_id"`
	ReceivedAt   time.Time `json:"received_at"`
}

type SlackItemStarred struct {
	TriggerID    string    `json:"trigger_id"`
	ConnectionID string    `json:"connection_id"`
	WorkspaceID  string    `json:"workspace_id"`
	ChannelID    string    `json:"channel_id,omitempty"`
	MessageTS    string    `json:"message_timestamp,omitempty"`
	ReceivedAt   time.Time `json:"received_at"`
}

type SlackEmojiChanged struct {
	TriggerID    string    `json:"trigger_id"`
	ConnectionID string    `json:"connection_id"`
	WorkspaceID  string    `json:"workspace_id"`
	Name         string    `json:"name"`
	ReceivedAt   time.Time `json:"received_at"`
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

// GitHubIssueOpened is an identity-only receipt for a newly opened GitHub
// issue. Title, body, labels, and assignees never cross the integrations
// event boundary.
type GitHubIssueOpened struct {
	TriggerID    string    `json:"trigger_id"`
	ConnectionID string    `json:"connection_id"`
	Repository   string    `json:"repository"`
	IssueNumber  int64     `json:"issue_number"`
	URL          string    `json:"url"`
	ReceivedAt   time.Time `json:"received_at"`
}

// GitHubPullRequestOpened is an identity-only receipt for a newly opened
// GitHub pull request. Title, body, and diffs never cross the integrations
// event boundary.
type GitHubPullRequestOpened struct {
	TriggerID         string    `json:"trigger_id"`
	ConnectionID      string    `json:"connection_id"`
	Repository        string    `json:"repository"`
	PullRequestNumber int64     `json:"pull_request_number"`
	URL               string    `json:"url"`
	ReceivedAt        time.Time `json:"received_at"`
}

// GitHubFileMutated records a contents create, update, or delete. File bytes
// never leave the integrations boundary.
type GitHubFileMutated struct {
	ExecutionID  string    `json:"execution_id"`
	ConnectionID string    `json:"connection_id"`
	Repository   string    `json:"repository"`
	Path         string    `json:"path"`
	SHA          string    `json:"sha,omitempty"`
	URL          string    `json:"url,omitempty"`
	RequestedBy  string    `json:"requested_by"`
	MutatedAt    time.Time `json:"mutated_at"`
}

// GitHubGistCreated records gist identity after create. File bytes never
// leave the integrations boundary.
type GitHubGistCreated struct {
	ExecutionID  string    `json:"execution_id"`
	ConnectionID string    `json:"connection_id"`
	GistID       string    `json:"gist_id"`
	URL          string    `json:"url,omitempty"`
	RequestedBy  string    `json:"requested_by"`
	CreatedAt    time.Time `json:"created_at"`
}

// GitHubWorkflowDispatched records that a workflow_dispatch was accepted.
// Logs and job output never leave the integrations boundary.
type GitHubWorkflowDispatched struct {
	ExecutionID  string    `json:"execution_id"`
	ConnectionID string    `json:"connection_id"`
	Repository   string    `json:"repository"`
	WorkflowID   string    `json:"workflow_id"`
	Ref          string    `json:"ref"`
	RunID        int64     `json:"run_id,omitempty"`
	URL          string    `json:"url,omitempty"`
	RequestedBy  string    `json:"requested_by"`
	DispatchedAt time.Time `json:"dispatched_at"`
}

// GitHubStarReceived is an identity-only receipt for a repository star.
type GitHubStarReceived struct {
	TriggerID    string    `json:"trigger_id"`
	ConnectionID string    `json:"connection_id"`
	Repository   string    `json:"repository"`
	URL          string    `json:"url"`
	ReceivedAt   time.Time `json:"received_at"`
}

// GitHubPushReceived is an identity-only receipt for a git push. Commit
// messages and diffs never leave the integrations boundary.
type GitHubPushReceived struct {
	TriggerID    string    `json:"trigger_id"`
	ConnectionID string    `json:"connection_id"`
	Repository   string    `json:"repository"`
	Ref          string    `json:"ref"`
	SHA          string    `json:"sha,omitempty"`
	URL          string    `json:"url,omitempty"`
	ReceivedAt   time.Time `json:"received_at"`
}

// GitHubReleasePublished is an identity-only receipt for a published release.
// Notes never leave the integrations boundary.
type GitHubReleasePublished struct {
	TriggerID    string    `json:"trigger_id"`
	ConnectionID string    `json:"connection_id"`
	Repository   string    `json:"repository"`
	ReleaseID    int64     `json:"release_id"`
	URL          string    `json:"url"`
	ReceivedAt   time.Time `json:"received_at"`
}

// GitHubReviewSubmitted is an identity-only receipt for a pull-request review.
// Review bodies never leave the integrations boundary.
type GitHubReviewSubmitted struct {
	TriggerID         string    `json:"trigger_id"`
	ConnectionID      string    `json:"connection_id"`
	Repository        string    `json:"repository"`
	PullRequestNumber int64     `json:"pull_request_number"`
	ReviewID          int64     `json:"review_id,omitempty"`
	URL               string    `json:"url"`
	ReceivedAt        time.Time `json:"received_at"`
}

// GitHubIssueMutated records lock, label, assignee, or field updates. Title
// and body never leave the integrations boundary.
type GitHubIssueMutated struct {
	ExecutionID  string    `json:"execution_id"`
	ConnectionID string    `json:"connection_id"`
	Repository   string    `json:"repository"`
	IssueNumber  int64     `json:"issue_number"`
	URL          string    `json:"url,omitempty"`
	RequestedBy  string    `json:"requested_by"`
	MutatedAt    time.Time `json:"mutated_at"`
}

// GitHubPullRequestCreated omits title, body, and diffs.
type GitHubPullRequestCreated struct {
	ExecutionID       string    `json:"execution_id"`
	ConnectionID      string    `json:"connection_id"`
	Repository        string    `json:"repository"`
	PullRequestNumber int64     `json:"pull_request_number"`
	URL               string    `json:"url"`
	RequestedBy       string    `json:"requested_by"`
	CreatedAt         time.Time `json:"created_at"`
}

// GitHubPullRequestMutated records update, merge, review, or reviewer changes.
// Title, body, and patches never leave the integrations boundary.
type GitHubPullRequestMutated struct {
	ExecutionID       string    `json:"execution_id"`
	ConnectionID      string    `json:"connection_id"`
	Repository        string    `json:"repository"`
	PullRequestNumber int64     `json:"pull_request_number"`
	URL               string    `json:"url,omitempty"`
	RequestedBy       string    `json:"requested_by"`
	MutatedAt         time.Time `json:"mutated_at"`
}

// GitHubRepositoryCreated omits description and clone credentials.
type GitHubRepositoryCreated struct {
	ExecutionID  string    `json:"execution_id"`
	ConnectionID string    `json:"connection_id"`
	Repository   string    `json:"repository"`
	URL          string    `json:"url,omitempty"`
	RequestedBy  string    `json:"requested_by"`
	CreatedAt    time.Time `json:"created_at"`
}

// GitHubRepositoryMutated records settings, collaborator, or star changes.
type GitHubRepositoryMutated struct {
	ExecutionID  string    `json:"execution_id"`
	ConnectionID string    `json:"connection_id"`
	Repository   string    `json:"repository"`
	URL          string    `json:"url,omitempty"`
	RequestedBy  string    `json:"requested_by"`
	MutatedAt    time.Time `json:"mutated_at"`
}

// GitHubRefMutated records branch create or delete. Commit patches never leave
// the integrations boundary.
type GitHubRefMutated struct {
	ExecutionID  string    `json:"execution_id"`
	ConnectionID string    `json:"connection_id"`
	Repository   string    `json:"repository"`
	Ref          string    `json:"ref"`
	SHA          string    `json:"sha,omitempty"`
	RequestedBy  string    `json:"requested_by"`
	MutatedAt    time.Time `json:"mutated_at"`
}

// GitHubReleaseCreated omits release notes.
type GitHubReleaseCreated struct {
	ExecutionID  string    `json:"execution_id"`
	ConnectionID string    `json:"connection_id"`
	Repository   string    `json:"repository"`
	ReleaseID    int64     `json:"release_id"`
	Tag          string    `json:"tag,omitempty"`
	URL          string    `json:"url,omitempty"`
	RequestedBy  string    `json:"requested_by"`
	CreatedAt    time.Time `json:"created_at"`
}

// GitHubMilestoneMutated omits milestone description.
type GitHubMilestoneMutated struct {
	ExecutionID     string    `json:"execution_id"`
	ConnectionID    string    `json:"connection_id"`
	Repository      string    `json:"repository"`
	MilestoneNumber int64     `json:"milestone_number"`
	URL             string    `json:"url,omitempty"`
	RequestedBy     string    `json:"requested_by"`
	MutatedAt       time.Time `json:"mutated_at"`
}

// GitHubLabelMutated records label create or rename. Description never leaves
// the integrations boundary.
type GitHubLabelMutated struct {
	ExecutionID  string    `json:"execution_id"`
	ConnectionID string    `json:"connection_id"`
	Repository   string    `json:"repository"`
	Name         string    `json:"name"`
	RequestedBy  string    `json:"requested_by"`
	MutatedAt    time.Time `json:"mutated_at"`
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

// GoogleGmailDraftCreated records a saved Gmail draft identity. Recipients,
// subject, and body never leave the integrations boundary.
type GoogleGmailDraftCreated struct {
	ExecutionID  string    `json:"execution_id"`
	ConnectionID string    `json:"connection_id"`
	DraftID      string    `json:"draft_id"`
	MessageID    string    `json:"message_id,omitempty"`
	RequestedBy  string    `json:"requested_by"`
	CreatedAt    time.Time `json:"created_at"`
}

// GoogleGmailEmailForwarded records that Gmail accepted a forward. Original
// content and destination addresses never leave the integrations boundary.
type GoogleGmailEmailForwarded struct {
	ExecutionID  string    `json:"execution_id"`
	ConnectionID string    `json:"connection_id"`
	MessageID    string    `json:"message_id"`
	ThreadID     string    `json:"thread_id,omitempty"`
	RequestedBy  string    `json:"requested_by"`
	ForwardedAt  time.Time `json:"forwarded_at"`
}

// GoogleGmailEmailMutated records a draft, label, or archive change. Subjects,
// bodies, and label names never leave the integrations boundary.
type GoogleGmailEmailMutated struct {
	ExecutionID  string    `json:"execution_id"`
	ConnectionID string    `json:"connection_id"`
	MessageID    string    `json:"message_id,omitempty"`
	DraftID      string    `json:"draft_id,omitempty"`
	RequestedBy  string    `json:"requested_by"`
	MutatedAt    time.Time `json:"mutated_at"`
}

// GoogleCalendarEventUpdated omits title, description, location, and attendee
// data so downstream services can react without receiving calendar content.
type GoogleCalendarEventUpdated struct {
	ExecutionID  string    `json:"execution_id"`
	ConnectionID string    `json:"connection_id"`
	CalendarID   string    `json:"calendar_id"`
	EventID      string    `json:"event_id"`
	URL          string    `json:"url,omitempty"`
	RequestedBy  string    `json:"requested_by"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// GoogleCalendarEventDeleted omits title, description, location, and attendee
// data. Downstream consumers receive only the durable calendar and event identity.
type GoogleCalendarEventDeleted struct {
	ExecutionID  string    `json:"execution_id"`
	ConnectionID string    `json:"connection_id"`
	CalendarID   string    `json:"calendar_id"`
	EventID      string    `json:"event_id"`
	RequestedBy  string    `json:"requested_by"`
	DeletedAt    time.Time `json:"deleted_at"`
}

// GoogleDriveItemCreated records a created Drive folder, shared drive, or
// similar identity. File bytes and download URLs never leave the boundary.
type GoogleDriveItemCreated struct {
	ExecutionID  string    `json:"execution_id"`
	ConnectionID string    `json:"connection_id"`
	ItemID       string    `json:"item_id"`
	RequestedBy  string    `json:"requested_by"`
	CreatedAt    time.Time `json:"created_at"`
}

// GoogleDriveItemCopied records a copied Drive item identity. File bytes,
// download URLs, and sharing emails never leave the integrations boundary.
type GoogleDriveItemCopied struct {
	ExecutionID  string    `json:"execution_id"`
	ConnectionID string    `json:"connection_id"`
	ItemID       string    `json:"item_id"`
	RequestedBy  string    `json:"requested_by"`
	CopiedAt     time.Time `json:"copied_at"`
}

// GoogleDriveItemMutated records a metadata, trash, share, or comment change.
// File bytes, download URLs, and emails never leave the integrations boundary.
type GoogleDriveItemMutated struct {
	ExecutionID  string    `json:"execution_id"`
	ConnectionID string    `json:"connection_id"`
	ItemID       string    `json:"item_id"`
	RequestedBy  string    `json:"requested_by"`
	MutatedAt    time.Time `json:"mutated_at"`
}

// GoogleGmailMessageReceived is a content-free receipt for a Gmail Inbox
// watch notification. Subjects, bodies, and headers never cross the
// integrations event boundary.
type GoogleGmailMessageReceived struct {
	TriggerID    string    `json:"trigger_id"`
	ConnectionID string    `json:"connection_id"`
	DeliveryID   string    `json:"delivery_id"`
	ReceivedAt   time.Time `json:"received_at"`
}

// GoogleCalendarEventChanged is a content-free receipt for a Google Calendar
// channel notification. Titles, attendees, location, and body never cross the
// integrations event boundary.
type GoogleCalendarEventChanged struct {
	TriggerID    string    `json:"trigger_id"`
	ConnectionID string    `json:"connection_id"`
	DeliveryID   string    `json:"delivery_id"`
	ReceivedAt   time.Time `json:"received_at"`
}

// GoogleDriveFileChanged is an identity-only receipt that Drive reported a
// change on the watched corpus. File names and bytes never leave the boundary.
type GoogleDriveFileChanged struct {
	TriggerID    string    `json:"trigger_id"`
	ConnectionID string    `json:"connection_id"`
	DeliveryID   string    `json:"delivery_id"`
	ReceivedAt   time.Time `json:"received_at"`
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

// MicrosoftOutlookDraftCreated records a saved Outlook draft identity. Subject,
// recipients, and body never leave the integrations boundary.
type MicrosoftOutlookDraftCreated struct {
	ExecutionID  string    `json:"execution_id"`
	ConnectionID string    `json:"connection_id"`
	MessageID    string    `json:"message_id"`
	RequestedBy  string    `json:"requested_by"`
	CreatedAt    time.Time `json:"created_at"`
}

// MicrosoftOutlookEmailForwarded records that Graph accepted a forward. The
// original message content and destination addresses never leave the
// integrations boundary.
type MicrosoftOutlookEmailForwarded struct {
	ExecutionID  string    `json:"execution_id"`
	ConnectionID string    `json:"connection_id"`
	MessageID    string    `json:"message_id"`
	RequestedBy  string    `json:"requested_by"`
	ForwardedAt  time.Time `json:"forwarded_at"`
}

// MicrosoftOutlookEmailMutated records a category or folder change. Subjects,
// bodies, and category names never leave the integrations boundary.
type MicrosoftOutlookEmailMutated struct {
	ExecutionID  string    `json:"execution_id"`
	ConnectionID string    `json:"connection_id"`
	MessageID    string    `json:"message_id"`
	FolderID     string    `json:"folder_id,omitempty"`
	RequestedBy  string    `json:"requested_by"`
	MutatedAt    time.Time `json:"mutated_at"`
}

// MicrosoftCalendarEventDeleted omits the event title, body, location, and
// attendees. Downstream consumers receive only the durable calendar and event
// identity.
type MicrosoftCalendarEventDeleted struct {
	ExecutionID  string    `json:"execution_id"`
	ConnectionID string    `json:"connection_id"`
	CalendarID   string    `json:"calendar_id"`
	EventID      string    `json:"event_id"`
	RequestedBy  string    `json:"requested_by"`
	DeletedAt    time.Time `json:"deleted_at"`
}

// MicrosoftOneDriveItemCopied records the copied item identity. File bytes,
// download URLs, and sharing permissions never leave the integrations boundary.
type MicrosoftOneDriveItemCopied struct {
	ExecutionID  string    `json:"execution_id"`
	ConnectionID string    `json:"connection_id"`
	ItemID       string    `json:"item_id"`
	RequestedBy  string    `json:"requested_by"`
	CopiedAt     time.Time `json:"copied_at"`
}

// MicrosoftOneDriveItemCreated records the uploaded item identity. File bytes
// and download URLs never leave the integrations boundary.
type MicrosoftOneDriveItemCreated struct {
	ExecutionID  string    `json:"execution_id"`
	ConnectionID string    `json:"connection_id"`
	ItemID       string    `json:"item_id"`
	RequestedBy  string    `json:"requested_by"`
	CreatedAt    time.Time `json:"created_at"`
}

// GoogleDocsDocumentCreated records a created document identity. Body text,
// markdown, and template values never leave the integrations boundary.
type GoogleDocsDocumentCreated struct {
	ExecutionID  string    `json:"execution_id"`
	ConnectionID string    `json:"connection_id"`
	DocumentID   string    `json:"document_id"`
	RequestedBy  string    `json:"requested_by"`
	CreatedAt    time.Time `json:"created_at"`
}

// GoogleDocsDocumentCopied records a copied document identity. Body text never
// leaves the integrations boundary.
type GoogleDocsDocumentCopied struct {
	ExecutionID  string    `json:"execution_id"`
	ConnectionID string    `json:"connection_id"`
	DocumentID   string    `json:"document_id"`
	RequestedBy  string    `json:"requested_by"`
	CopiedAt     time.Time `json:"copied_at"`
}

// GoogleDocsDocumentMutated records a Docs write. Body text, markdown, and
// image URIs never leave the integrations boundary.
type GoogleDocsDocumentMutated struct {
	ExecutionID  string    `json:"execution_id"`
	ConnectionID string    `json:"connection_id"`
	DocumentID   string    `json:"document_id"`
	RequestedBy  string    `json:"requested_by"`
	MutatedAt    time.Time `json:"mutated_at"`
}

// GoogleSheetsSpreadsheetCreated records a created spreadsheet identity. Cell
// values never leave the integrations event boundary.
type GoogleSheetsSpreadsheetCreated struct {
	ExecutionID   string    `json:"execution_id"`
	ConnectionID  string    `json:"connection_id"`
	SpreadsheetID string    `json:"spreadsheet_id"`
	RequestedBy   string    `json:"requested_by"`
	CreatedAt     time.Time `json:"created_at"`
}

// GoogleSheetsSpreadsheetMutated records a Sheets write. Cell values never
// leave the integrations event boundary.
type GoogleSheetsSpreadsheetMutated struct {
	ExecutionID   string    `json:"execution_id"`
	ConnectionID  string    `json:"connection_id"`
	SpreadsheetID string    `json:"spreadsheet_id"`
	RequestedBy   string    `json:"requested_by"`
	MutatedAt     time.Time `json:"mutated_at"`
}

// MicrosoftTeamsChannelCreated records a created channel identity. Channel
// descriptions never leave the integrations boundary.
type MicrosoftTeamsChannelCreated struct {
	ExecutionID  string    `json:"execution_id"`
	ConnectionID string    `json:"connection_id"`
	TeamID       string    `json:"team_id"`
	ChannelID    string    `json:"channel_id"`
	RequestedBy  string    `json:"requested_by"`
	CreatedAt    time.Time `json:"created_at"`
}

// MicrosoftTeamsMessageSent records that a Teams message was accepted. Message
// bodies never leave the integrations boundary.
type MicrosoftTeamsMessageSent struct {
	ExecutionID  string    `json:"execution_id"`
	ConnectionID string    `json:"connection_id"`
	MessageID    string    `json:"message_id"`
	RequestedBy  string    `json:"requested_by"`
	SentAt       time.Time `json:"sent_at"`
}

// MicrosoftTeamsMessageDeleted records a deleted chat message identity. Bodies
// never leave the integrations boundary.
type MicrosoftTeamsMessageDeleted struct {
	ExecutionID  string    `json:"execution_id"`
	ConnectionID string    `json:"connection_id"`
	MessageID    string    `json:"message_id"`
	RequestedBy  string    `json:"requested_by"`
	DeletedAt    time.Time `json:"deleted_at"`
}

// MicrosoftSharePointItemCreated records a created folder, list, list item, or
// uploaded file identity. File bytes and list field values never leave the
// integrations boundary.
type MicrosoftSharePointItemCreated struct {
	ExecutionID  string    `json:"execution_id"`
	ConnectionID string    `json:"connection_id"`
	ItemID       string    `json:"item_id"`
	RequestedBy  string    `json:"requested_by"`
	CreatedAt    time.Time `json:"created_at"`
}

// MicrosoftSharePointItemCopied records a copied SharePoint item identity.
// File bytes never leave the integrations boundary.
type MicrosoftSharePointItemCopied struct {
	ExecutionID  string    `json:"execution_id"`
	ConnectionID string    `json:"connection_id"`
	ItemID       string    `json:"item_id"`
	RequestedBy  string    `json:"requested_by"`
	CopiedAt     time.Time `json:"copied_at"`
}

// MicrosoftSharePointItemMutated records a SharePoint write. File bytes, list
// field values, and next-page URLs never leave the integrations boundary.
type MicrosoftSharePointItemMutated struct {
	ExecutionID  string    `json:"execution_id"`
	ConnectionID string    `json:"connection_id"`
	ItemID       string    `json:"item_id"`
	RequestedBy  string    `json:"requested_by"`
	MutatedAt    time.Time `json:"mutated_at"`
}
