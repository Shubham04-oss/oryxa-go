package catalog

import (
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/Shubham04-oss/oryxa-go/integrations/internal/actionflow"
	"github.com/Shubham04-oss/oryxa-go/integrations/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/integrations/internal/oauthflow"
	"github.com/Shubham04-oss/oryxa-go/integrations/internal/triggerflow"
)

// Slack user_scope values from the pinned piece OAuth2 auth URL. They are
// requested on every member connect and are not BYOC bot scopes.
var slackUserScopes = []string{
	"search:read", "users.profile:write", "reactions:read", "reactions:write",
	"im:history", "stars:read", "channels:write", "groups:write", "im:write",
	"mpim:write", "channels:write.invites", "groups:write.invites",
	"channels:history", "groups:history", "chat:write", "users:read",
	"usergroups:write",
}

var slackAllowedBotScopes = []string{
	"channels:history", "channels:join", "channels:manage", "channels:read",
	"channels:write.invites", "chat:write", "chat:write.customize",
	"emoji:read", "files:read", "files:write", "groups:history", "groups:read", "groups:write",
	"groups:write.invites", "im:history", "im:read", "im:write", "links:read",
	"links:write", "mpim:history", "mpim:read", "mpim:write", "reactions:read",
	"reactions:write", "users.profile:read", "users:read", "users:read.email",
	"usergroups:read", "usergroups:write",
}

type slackActionSpec struct {
	Runner   string
	Scopes   []string
	Validate func(json.RawMessage) error
}

type slackTriggerSpec struct {
	Runner   string
	Event    string
	Scopes   []string
	Validate func(json.RawMessage) error
}

var slackActions = map[string]slackActionSpec{
	"post-message":              {"slack_post_message", []string{"chat:write"}, validateSlackPostMessage},
	"send-direct-message":       {"slack_send_direct_message", []string{"chat:write", "im:write"}, validateSlackDirectMessage},
	"update-message":            {"slack_update_message", []string{"chat:write"}, validateSlackUpdateMessage},
	"delete-message":            {"slack_delete_message", []string{"chat:write"}, validateSlackChannelTS},
	"send-ephemeral-message":    {"slack_send_ephemeral_message", []string{"chat:write"}, validateSlackEphemeral},
	"schedule-message":          {"slack_schedule_message", []string{"chat:write"}, validateSlackScheduleMessage},
	"list-scheduled-messages":   {"slack_list_scheduled_messages", []string{"chat:write"}, validateSlackListScheduled},
	"delete-scheduled-message":  {"slack_delete_scheduled_message", []string{"chat:write"}, validateSlackDeleteScheduled},
	"get-message":               {"get-message", []string{"channels:history", "groups:history"}, validateSlackChannelTS},
	"get-channel-history":       {"slack_get_channel_history", []string{"channels:history", "groups:history"}, validateSlackHistory},
	"get-thread-replies":        {"slack_get_thread_replies", []string{"channels:history", "groups:history"}, validateSlackThread},
	"get-message-permalink":     {"slack_get_message_permalink", []string{"channels:history", "groups:history"}, validateSlackPermalink},
	"search-messages":           {"slack_search_messages", []string{"search:read"}, validateSlackSearch},
	"mark-conversation-read":    {"slack_mark_conversation_read", []string{"channels:history", "groups:history"}, validateSlackChannelTS},
	"close-dm":                  {"slack_close_dm", []string{"im:write"}, validateSlackChannelOnly},
	"add-reaction":              {"slack_add_reaction", []string{"reactions:write"}, validateSlackAddReaction},
	"remove-reaction":           {"slack_remove_reaction", []string{"reactions:write"}, validateSlackRemoveReaction},
	"get-reactions":             {"slack_get_reactions", []string{"reactions:read"}, validateSlackGetReactions},
	"find-channel":              {"slack_find_channel", []string{"channels:read", "groups:read"}, validateSlackFindChannel},
	"create-channel":            {"slack_create_channel", []string{"channels:manage"}, validateSlackCreateChannel},
	"archive-channel":           {"slack_archive_channel", []string{"channels:manage"}, validateSlackChannelOnly},
	"unarchive-channel":         {"slack_unarchive_channel", []string{"channels:manage"}, validateSlackChannelOnly},
	"rename-channel":            {"slack_rename_channel", []string{"channels:manage"}, validateSlackRenameChannel},
	"set-channel-topic":         {"slack_set_channel_topic", []string{"channels:manage"}, validateSlackTopic},
	"set-channel-purpose":       {"slack_set_channel_purpose", []string{"channels:manage"}, validateSlackPurpose},
	"join-channel":              {"slack_join_channel", []string{"channels:join"}, validateSlackChannelOnly},
	"leave-channel":             {"slack_leave_channel", []string{"channels:read"}, validateSlackChannelOnly},
	"invite-users-to-channel":   {"slack_invite_users_to_channel", []string{"channels:manage"}, validateSlackInviteUsers},
	"remove-user-from-channel":  {"slack_remove_user_from_channel", []string{"channels:manage"}, validateSlackRemoveUser},
	"list-channels":             {"slack_list_channels", []string{"channels:read", "groups:read"}, validateSlackListChannels},
	"get-channel-info":          {"slack_get_channel_info", []string{"channels:read", "groups:read"}, validateSlackChannelInfo},
	"list-channel-members":      {"slack_list_channel_members", []string{"channels:read", "groups:read"}, validateSlackChannelOnly},
	"find-user-by-email":        {"find_user_by_email", []string{"users:read.email"}, validateSlackEmail},
	"find-user-by-handle":       {"find_user_by_handle", []string{"users:read"}, validateSlackHandle},
	"find-user-by-id":           {"find-user-by-id", []string{"users:read"}, validateSlackUserID},
	"get-user":                  {"get_user", []string{"users:read"}, validateSlackUserID},
	"list-users":                {"list_users", []string{"users:read"}, validateSlackListUsers},
	"set-user-status":           {"set_user_status", []string{"users.profile:write"}, validateSlackStatus},
	"update-profile":            {"update_profile", []string{"users.profile:write"}, validateSlackProfile},
	"list-user-conversations":   {"slack_list_user_conversations", []string{"channels:read", "groups:read", "im:read", "mpim:read"}, validateSlackUserConversations},
	"list-user-reactions":       {"slack_list_user_reactions", []string{"reactions:read"}, validateSlackUserReactions},
	"list-user-groups":          {"list_user_groups", []string{"usergroups:read"}, validateSlackListUserGroups},
	"get-user-group-by-handle":  {"get_group_by_handle", []string{"usergroups:read"}, validateSlackHandle},
	"update-user-group-members": {"update_group_users", []string{"usergroups:write"}, validateSlackUpdateGroupUsers},
	"list-custom-emoji":         {"slack_list_custom_emoji", []string{"emoji:read"}, validateSlackEmpty},
	"list-files":                {"slack_list_files", []string{"files:read"}, validateSlackListFiles},
	"upload-file":               {"uploadFile", []string{"files:write"}, validateSlackUploadFile},
	"get-file":                  {"slack_get_file", []string{"files:read"}, validateSlackGetFile},
	"custom-api-call":           {"custom_api_call", []string{"users:read"}, validateSlackCustomAPICall},
}

var slackNeedsFiles = map[string]bool{"upload-file": true, "get-file": true}

var slackPrepare = map[string]func(json.RawMessage) (json.RawMessage, error){
	"custom-api-call": prepareSlackCustomAPICall,
}

var slackTriggers = map[string]slackTriggerSpec{
	"new-message":                   {"new-message", "message", nil, validateSlackIgnoreBots},
	"new-message-in-channel":        {"new-message-in-channel", "message", nil, validateSlackChannelMessageTrigger},
	"new-direct-message":            {"new-direct-message", "message", []string{"im:history"}, validateSlackDirectMessageTrigger},
	"new-mention":                   {"new_mention", "message", nil, validateSlackMentionTrigger},
	"new-mention-in-direct-message": {"new-mention-in-direct-message", "message", []string{"im:history"}, validateSlackMentionDMTrigger},
	"new-reaction":                  {"new_reaction_added", "reaction_added", []string{"reactions:read"}, validateSlackReactionTrigger},
	"reaction-removed":              {"new_reaction_removed", "reaction_removed", []string{"reactions:read"}, validateSlackReactionTrigger},
	"channel-created":               {"channel_created", "channel_created", []string{"channels:read"}, validateSlackEmpty},
	"new-command":                   {"new_command", "message", nil, validateSlackCommandTrigger},
	"new-command-in-direct-message": {"new-command-in-direct-message", "message", []string{"im:history"}, validateSlackCommandDMTrigger},
	"new-user":                      {"new-user", "team_join", []string{"users:read"}, validateSlackEmpty},
	"new-saved-message":             {"new-saved-message", "star_added", []string{"stars:read"}, validateSlackEmpty},
	"new-team-custom-emoji":         {"new-team-custom-emoji", "emoji_changed", []string{"emoji:read"}, validateSlackEmpty},
}

func SlackPieceTriggerName(triggerName string) string {
	spec, ok := slackTriggers[triggerName]
	if !ok || spec.Runner == "" {
		return triggerName
	}
	return spec.Runner
}

func slackActionDefinition(actionName string) (actionflow.Definition, bool) {
	spec, ok := slackActions[actionName]
	if !ok {
		return actionflow.Definition{}, false
	}
	return actionflow.Definition{
		Provider: "slack", PieceName: SlackPieceName, PieceVersion: SlackPieceVersion,
		ActionName: actionName, RunnerActionName: spec.Runner, RequiredScopes: append([]string(nil), spec.Scopes...),
		ValidateProps: spec.Validate, PrepareProps: slackPrepare[actionName], NeedsFiles: slackNeedsFiles[actionName],
	}, true
}

func slackTriggerDefinition(triggerName string) (triggerflow.Definition, bool) {
	spec, ok := slackTriggers[triggerName]
	if !ok {
		return triggerflow.Definition{}, false
	}
	definition := triggerflow.Definition{
		Provider: "slack", PieceName: SlackPieceName, PieceVersion: SlackPieceVersion,
		TriggerName: triggerName, RunnerTriggerName: spec.Runner,
		RouteMode: domain.WebhookRouteAppLevel, Event: spec.Event,
		AppWebhookFamily: domain.SlackEventsRouteTrigger,
		TimestampHeader:  "x-slack-request-timestamp", TimestampWindow: 5 * time.Minute,
		MaxBodyBytes: 128 * 1024, RequiredScopes: append([]string(nil), spec.Scopes...),
		ValidateProps: spec.Validate,
	}
	return definition, true
}

func slackOAuthDefinition(publicBaseURL string) oauthflow.Definition {
	return oauthflow.Definition{
		Provider: "slack", PieceName: SlackPieceName, PieceVersion: SlackPieceVersion,
		RedirectURI:    publicBaseURL + "/v1/oauth/callback/slack",
		AllowedScopes:  append([]string(nil), slackAllowedBotScopes...),
		RequiredScopes: []string{"channels:history", "groups:history"},
		UserScopes:     append([]string(nil), slackUserScopes...),
		SupportsPKCE:   false,
	}
}

func validateSlackPostMessage(raw json.RawMessage) error {
	var props struct {
		Channel        string          `json:"channel"`
		Text           string          `json:"text"`
		ThreadTS       string          `json:"threadTs"`
		ReplyBroadcast bool            `json:"replyBroadcast"`
		SendAsUser     bool            `json:"sendAsUser"`
		UnfurlLinks    *bool           `json:"unfurlLinks"`
		Blocks         json.RawMessage `json:"blocks"`
	}
	if err := decodeStrict(raw, &props); err != nil || !boundedIdentifier(props.Channel, 100) ||
		len(props.Text) > 40_000 || len(props.ThreadTS) > 80 || (props.ThreadTS != "" && !boundedIdentifier(props.ThreadTS, 80)) {
		return errors.New("invalid Slack post-message properties")
	}
	if strings.TrimSpace(props.Text) == "" && len(props.Blocks) == 0 {
		return errors.New("message text or blocks are required")
	}
	if props.ReplyBroadcast && props.ThreadTS == "" {
		return errors.New("replyBroadcast requires threadTs")
	}
	if err := slackBlocks(props.Blocks); err != nil {
		return err
	}
	return nil
}

func validateSlackDirectMessage(raw json.RawMessage) error {
	var props struct {
		UserID      string          `json:"userId"`
		Text        string          `json:"text"`
		UnfurlLinks *bool           `json:"unfurlLinks"`
		Blocks      json.RawMessage `json:"blocks"`
	}
	if err := decodeStrict(raw, &props); err != nil || !boundedIdentifier(props.UserID, 100) || len(props.Text) > 40_000 {
		return errors.New("invalid Slack send-direct-message properties")
	}
	if strings.TrimSpace(props.Text) == "" && len(props.Blocks) == 0 {
		return errors.New("message text or blocks are required")
	}
	return slackBlocks(props.Blocks)
}

func validateSlackUpdateMessage(raw json.RawMessage) error {
	var props struct {
		Channel string          `json:"channel"`
		TS      string          `json:"ts"`
		Text    string          `json:"text"`
		Blocks  json.RawMessage `json:"blocks"`
	}
	if err := decodeStrict(raw, &props); err != nil || !boundedIdentifier(props.Channel, 100) ||
		!boundedIdentifier(props.TS, 80) || len(props.Text) > 40_000 {
		return errors.New("invalid Slack update-message properties")
	}
	if strings.TrimSpace(props.Text) == "" && len(props.Blocks) == 0 {
		return errors.New("message text or blocks are required")
	}
	return slackBlocks(props.Blocks)
}

func validateSlackEphemeral(raw json.RawMessage) error {
	var props struct {
		Channel string          `json:"channel"`
		User    string          `json:"user"`
		Text    string          `json:"text"`
		Blocks  json.RawMessage `json:"blocks"`
	}
	if err := decodeStrict(raw, &props); err != nil || !boundedIdentifier(props.Channel, 100) ||
		!boundedIdentifier(props.User, 100) || len(props.Text) > 40_000 {
		return errors.New("invalid Slack send-ephemeral-message properties")
	}
	if strings.TrimSpace(props.Text) == "" && len(props.Blocks) == 0 {
		return errors.New("message text or blocks are required")
	}
	return slackBlocks(props.Blocks)
}

func validateSlackScheduleMessage(raw json.RawMessage) error {
	var props struct {
		Channel  string          `json:"channel"`
		PostAt   float64         `json:"postAt"`
		Text     string          `json:"text"`
		ThreadTS string          `json:"threadTs"`
		Blocks   json.RawMessage `json:"blocks"`
	}
	if err := decodeStrict(raw, &props); err != nil || !boundedIdentifier(props.Channel, 100) ||
		props.PostAt < 1 || len(props.Text) > 40_000 ||
		(props.ThreadTS != "" && !boundedIdentifier(props.ThreadTS, 80)) {
		return errors.New("invalid Slack schedule-message properties")
	}
	if strings.TrimSpace(props.Text) == "" && len(props.Blocks) == 0 {
		return errors.New("message text or blocks are required")
	}
	return slackBlocks(props.Blocks)
}

func validateSlackListScheduled(raw json.RawMessage) error {
	var props struct {
		Channel string   `json:"channel"`
		Oldest  *float64 `json:"oldest"`
		Latest  *float64 `json:"latest"`
	}
	if err := decodeStrict(raw, &props); err != nil ||
		(props.Channel != "" && !boundedIdentifier(props.Channel, 100)) ||
		(props.Oldest != nil && *props.Oldest < 0) || (props.Latest != nil && *props.Latest < 0) {
		return errors.New("invalid Slack list-scheduled-messages properties")
	}
	return nil
}

func validateSlackDeleteScheduled(raw json.RawMessage) error {
	var props struct {
		Channel            string `json:"channel"`
		ScheduledMessageID string `json:"scheduledMessageId"`
	}
	if err := decodeStrict(raw, &props); err != nil || !boundedIdentifier(props.Channel, 100) ||
		!boundedIdentifier(props.ScheduledMessageID, 80) {
		return errors.New("invalid Slack delete-scheduled-message properties")
	}
	return nil
}

func validateSlackChannelTS(raw json.RawMessage) error {
	var props struct {
		Channel string `json:"channel"`
		TS      string `json:"ts"`
	}
	if err := decodeStrict(raw, &props); err != nil || !boundedIdentifier(props.Channel, 100) || !boundedIdentifier(props.TS, 80) {
		return errors.New("invalid Slack channel and timestamp properties")
	}
	return nil
}

func validateSlackHistory(raw json.RawMessage) error {
	var props struct {
		Channel   string   `json:"channel"`
		Oldest    *float64 `json:"oldest"`
		Latest    *float64 `json:"latest"`
		Inclusive *bool    `json:"inclusive"`
	}
	if err := decodeStrict(raw, &props); err != nil || !boundedIdentifier(props.Channel, 100) ||
		(props.Oldest != nil && *props.Oldest < 0) || (props.Latest != nil && *props.Latest < 0) {
		return errors.New("invalid Slack get-channel-history properties")
	}
	return nil
}

func validateSlackThread(raw json.RawMessage) error {
	var props struct {
		Channel  string `json:"channel"`
		ThreadTS string `json:"threadTs"`
	}
	if err := decodeStrict(raw, &props); err != nil || !boundedIdentifier(props.Channel, 100) || !boundedIdentifier(props.ThreadTS, 80) {
		return errors.New("invalid Slack get-thread-replies properties")
	}
	return nil
}

func validateSlackPermalink(raw json.RawMessage) error {
	var props struct {
		Channel   string `json:"channel"`
		MessageTS string `json:"messageTs"`
	}
	if err := decodeStrict(raw, &props); err != nil || !boundedIdentifier(props.Channel, 100) || !boundedIdentifier(props.MessageTS, 80) {
		return errors.New("invalid Slack get-message-permalink properties")
	}
	return nil
}

func validateSlackSearch(raw json.RawMessage) error {
	var props struct {
		Query string `json:"query"`
	}
	if err := decodeStrict(raw, &props); err != nil || strings.TrimSpace(props.Query) == "" ||
		len(props.Query) > 1000 || strings.ContainsAny(props.Query, "\r\n") {
		return errors.New("invalid Slack search-messages properties")
	}
	return nil
}

func validateSlackChannelOnly(raw json.RawMessage) error {
	var props struct {
		Channel string `json:"channel"`
	}
	if err := decodeStrict(raw, &props); err != nil || !boundedIdentifier(props.Channel, 100) {
		return errors.New("invalid Slack channel properties")
	}
	return nil
}

func validateSlackAddReaction(raw json.RawMessage) error {
	var props struct {
		Channel     string `json:"channel"`
		TS          string `json:"ts"`
		Reaction    string `json:"reaction"`
		ReactAsUser bool   `json:"reactAsUser"`
	}
	if err := decodeStrict(raw, &props); err != nil || !boundedIdentifier(props.Channel, 100) ||
		!boundedIdentifier(props.TS, 80) || !slackReactionName(props.Reaction) {
		return errors.New("invalid Slack add-reaction properties")
	}
	return nil
}

func validateSlackRemoveReaction(raw json.RawMessage) error {
	var props struct {
		Reaction    string `json:"reaction"`
		Channel     string `json:"channel"`
		TS          string `json:"ts"`
		File        string `json:"file"`
		ReactAsUser *bool  `json:"reactAsUser"`
	}
	if err := decodeStrict(raw, &props); err != nil || !slackReactionName(props.Reaction) {
		return errors.New("invalid Slack remove-reaction properties")
	}
	if (props.Channel == "") != (props.TS == "") {
		return errors.New("channel and ts must be supplied together")
	}
	if props.Channel != "" && !boundedIdentifier(props.Channel, 100) {
		return errors.New("invalid Slack remove-reaction properties")
	}
	if props.TS != "" && !boundedIdentifier(props.TS, 80) {
		return errors.New("invalid Slack remove-reaction properties")
	}
	if props.File != "" && !boundedIdentifier(props.File, 100) {
		return errors.New("invalid Slack remove-reaction properties")
	}
	if props.Channel == "" && props.File == "" {
		return errors.New("channel or file is required")
	}
	return nil
}

func validateSlackGetReactions(raw json.RawMessage) error {
	var props struct {
		Channel string `json:"channel"`
		TS      string `json:"ts"`
		File    string `json:"file"`
		Full    *bool  `json:"full"`
	}
	if err := decodeStrict(raw, &props); err != nil {
		return errors.New("invalid Slack get-reactions properties")
	}
	if (props.Channel == "") != (props.TS == "") {
		return errors.New("channel and ts must be supplied together")
	}
	if props.Channel != "" && (!boundedIdentifier(props.Channel, 100) || !boundedIdentifier(props.TS, 80)) {
		return errors.New("invalid Slack get-reactions properties")
	}
	if props.File != "" && !boundedIdentifier(props.File, 100) {
		return errors.New("invalid Slack get-reactions properties")
	}
	if props.Channel == "" && props.File == "" {
		return errors.New("channel or file is required")
	}
	return nil
}

func validateSlackFindChannel(raw json.RawMessage) error {
	var props struct {
		Name            string `json:"name"`
		ExcludeArchived *bool  `json:"excludeArchived"`
	}
	if err := decodeStrict(raw, &props); err != nil {
		return err
	}
	name := strings.TrimPrefix(strings.TrimSpace(props.Name), "#")
	if name == "" || len(name) > 80 || strings.ContainsAny(name, "\r\n\t") {
		return errors.New("invalid Slack channel name")
	}
	return nil
}

func validateSlackCreateChannel(raw json.RawMessage) error {
	var props struct {
		ChannelName string `json:"channelName"`
		IsPrivate   *bool  `json:"isPrivate"`
	}
	if err := decodeStrict(raw, &props); err != nil {
		return errors.New("invalid Slack create-channel properties")
	}
	name := strings.TrimSpace(props.ChannelName)
	if name == "" || len(name) > 80 || strings.ContainsAny(name, " \t\r\n#") {
		return errors.New("invalid Slack create-channel properties")
	}
	return nil
}

func validateSlackRenameChannel(raw json.RawMessage) error {
	var props struct {
		Channel string `json:"channel"`
		Name    string `json:"name"`
	}
	if err := decodeStrict(raw, &props); err != nil || !boundedIdentifier(props.Channel, 100) {
		return errors.New("invalid Slack rename-channel properties")
	}
	name := strings.TrimSpace(props.Name)
	if name == "" || len(name) > 80 || strings.ContainsAny(name, " \t\r\n#") {
		return errors.New("invalid Slack rename-channel properties")
	}
	return nil
}

func validateSlackTopic(raw json.RawMessage) error {
	var props struct {
		Channel string `json:"channel"`
		Topic   string `json:"topic"`
	}
	if err := decodeStrict(raw, &props); err != nil || !boundedIdentifier(props.Channel, 100) ||
		strings.TrimSpace(props.Topic) == "" || len(props.Topic) > 250 {
		return errors.New("invalid Slack set-channel-topic properties")
	}
	return nil
}

func validateSlackPurpose(raw json.RawMessage) error {
	var props struct {
		Channel string `json:"channel"`
		Purpose string `json:"purpose"`
	}
	if err := decodeStrict(raw, &props); err != nil || !boundedIdentifier(props.Channel, 100) ||
		strings.TrimSpace(props.Purpose) == "" || len(props.Purpose) > 250 {
		return errors.New("invalid Slack set-channel-purpose properties")
	}
	return nil
}

func validateSlackInviteUsers(raw json.RawMessage) error {
	var props struct {
		Channel string   `json:"channel"`
		UserIDs []string `json:"userIds"`
	}
	if err := decodeStrict(raw, &props); err != nil || !boundedIdentifier(props.Channel, 100) ||
		!slackIdentifiers(props.UserIDs, 1, 100) {
		return errors.New("invalid Slack invite-users-to-channel properties")
	}
	return nil
}

func validateSlackRemoveUser(raw json.RawMessage) error {
	var props struct {
		Channel string `json:"channel"`
		User    string `json:"user"`
	}
	if err := decodeStrict(raw, &props); err != nil || !boundedIdentifier(props.Channel, 100) || !boundedIdentifier(props.User, 100) {
		return errors.New("invalid Slack remove-user-from-channel properties")
	}
	return nil
}

func validateSlackListChannels(raw json.RawMessage) error {
	var props struct {
		Types           string `json:"types"`
		ExcludeArchived *bool  `json:"excludeArchived"`
	}
	if err := decodeStrict(raw, &props); err != nil || len(props.Types) > 80 || strings.ContainsAny(props.Types, "\r\n\t") {
		return errors.New("invalid Slack list-channels properties")
	}
	return nil
}

func validateSlackChannelInfo(raw json.RawMessage) error {
	var props struct {
		Channel           string `json:"channel"`
		IncludeNumMembers *bool  `json:"includeNumMembers"`
	}
	if err := decodeStrict(raw, &props); err != nil || !boundedIdentifier(props.Channel, 100) {
		return errors.New("invalid Slack get-channel-info properties")
	}
	return nil
}

func validateSlackEmail(raw json.RawMessage) error {
	var props struct {
		Email string `json:"email"`
	}
	if err := decodeStrict(raw, &props); err != nil || !email(props.Email) {
		return errors.New("invalid Slack find-user-by-email properties")
	}
	return nil
}

func validateSlackHandle(raw json.RawMessage) error {
	var props struct {
		Handle string `json:"handle"`
	}
	if err := decodeStrict(raw, &props); err != nil {
		return errors.New("invalid Slack handle properties")
	}
	handle := strings.TrimPrefix(strings.TrimSpace(props.Handle), "@")
	if handle == "" || len(handle) > 80 || strings.ContainsAny(handle, " \t\r\n") {
		return errors.New("invalid Slack handle properties")
	}
	return nil
}

func validateSlackUserID(raw json.RawMessage) error {
	var props struct {
		ID string `json:"id"`
	}
	if err := decodeStrict(raw, &props); err != nil || !boundedIdentifier(props.ID, 100) {
		return errors.New("invalid Slack user id properties")
	}
	return nil
}

func validateSlackListUsers(raw json.RawMessage) error {
	var props struct {
		IncludeBots     bool `json:"includeBots"`
		IncludeDisabled bool `json:"includeDisabled"`
	}
	if err := decodeStrict(raw, &props); err != nil {
		return errors.New("invalid Slack list-users properties")
	}
	return nil
}

func validateSlackStatus(raw json.RawMessage) error {
	var props struct {
		Text       string   `json:"text"`
		Emoji      string   `json:"emoji"`
		Expiration *float64 `json:"expiration"`
	}
	if err := decodeStrict(raw, &props); err != nil || strings.TrimSpace(props.Text) == "" || len(props.Text) > 100 ||
		(props.Emoji != "" && !slackReactionName(strings.Trim(props.Emoji, ":"))) ||
		(props.Expiration != nil && *props.Expiration < 0) {
		return errors.New("invalid Slack set-user-status properties")
	}
	return nil
}

func validateSlackProfile(raw json.RawMessage) error {
	var props struct {
		FirstName string `json:"firstName"`
		LastName  string `json:"lastName"`
		Email     string `json:"email"`
		UserID    string `json:"userId"`
	}
	if err := decodeStrict(raw, &props); err != nil ||
		len(props.FirstName) > 80 || len(props.LastName) > 80 ||
		strings.ContainsAny(props.FirstName+props.LastName, "\r\n") ||
		(props.Email != "" && !email(props.Email)) ||
		(props.UserID != "" && !boundedIdentifier(props.UserID, 100)) ||
		(props.FirstName == "" && props.LastName == "" && props.Email == "") {
		return errors.New("invalid Slack update-profile properties")
	}
	return nil
}

func validateSlackUserConversations(raw json.RawMessage) error {
	var props struct {
		User            string `json:"user"`
		Types           string `json:"types"`
		ExcludeArchived *bool  `json:"excludeArchived"`
	}
	if err := decodeStrict(raw, &props); err != nil ||
		(props.User != "" && !boundedIdentifier(props.User, 100)) ||
		len(props.Types) > 80 || strings.ContainsAny(props.Types, "\r\n\t") {
		return errors.New("invalid Slack list-user-conversations properties")
	}
	return nil
}

func validateSlackUserReactions(raw json.RawMessage) error {
	var props struct {
		User  string   `json:"user"`
		Limit *float64 `json:"limit"`
	}
	if err := decodeStrict(raw, &props); err != nil ||
		(props.User != "" && !boundedIdentifier(props.User, 100)) ||
		(props.Limit != nil && (*props.Limit < 1 || *props.Limit > 1000)) {
		return errors.New("invalid Slack list-user-reactions properties")
	}
	return nil
}

func validateSlackListUserGroups(raw json.RawMessage) error {
	var props struct {
		IncludeDisabled *bool `json:"includeDisabled"`
		IncludeUsers    *bool `json:"includeUsers"`
	}
	if err := decodeStrict(raw, &props); err != nil {
		return errors.New("invalid Slack list-user-groups properties")
	}
	return nil
}

func validateSlackUpdateGroupUsers(raw json.RawMessage) error {
	var props struct {
		Handle      string   `json:"handle"`
		UserIDs     []string `json:"userIds"`
		AppendUsers bool     `json:"appendUsers"`
	}
	if err := decodeStrict(raw, &props); err != nil {
		return errors.New("invalid Slack update-user-group-members properties")
	}
	handle := strings.TrimPrefix(strings.TrimSpace(props.Handle), "@")
	if handle == "" || len(handle) > 80 || strings.ContainsAny(handle, " \t\r\n") ||
		(len(props.UserIDs) > 0 && !slackIdentifiers(props.UserIDs, 1, 100)) {
		return errors.New("invalid Slack update-user-group-members properties")
	}
	return nil
}

func validateSlackEmpty(raw json.RawMessage) error {
	var props struct{}
	if err := decodeStrict(raw, &props); err != nil {
		return errors.New("Slack properties must be an empty object")
	}
	return nil
}

func validateSlackListFiles(raw json.RawMessage) error {
	var props struct {
		Channel string   `json:"channel"`
		User    string   `json:"user"`
		Types   string   `json:"types"`
		TSFrom  string   `json:"ts_from"`
		TSTo    string   `json:"ts_to"`
		Limit   *float64 `json:"limit"`
	}
	if err := decodeStrict(raw, &props); err != nil ||
		(props.Channel != "" && !boundedIdentifier(props.Channel, 100)) ||
		(props.User != "" && !boundedIdentifier(props.User, 100)) ||
		len(props.Types) > 80 || strings.ContainsAny(props.Types, "\r\n\t") ||
		(props.TSFrom != "" && !boundedIdentifier(props.TSFrom, 80)) ||
		(props.TSTo != "" && !boundedIdentifier(props.TSTo, 80)) ||
		(props.Limit != nil && (*props.Limit < 1 || *props.Limit > 1000)) {
		return errors.New("invalid Slack list-files properties")
	}
	return nil
}

func validateSlackUploadFile(raw json.RawMessage) error {
	var props struct {
		File     fileRef `json:"file"`
		Title    string  `json:"title"`
		Filename string  `json:"filename"`
		Channel  string  `json:"channel"`
	}
	if err := decodeStrict(raw, &props); err != nil || !oryxaFileID(props.File.FileID) ||
		len(props.Title) > 255 || strings.ContainsAny(props.Title, "\r\n") ||
		len(props.Filename) > 255 || strings.ContainsAny(props.Filename, "\r\n/\\") ||
		(props.Channel != "" && !boundedIdentifier(props.Channel, 100)) {
		return errors.New("invalid Slack upload-file properties")
	}
	return nil
}

func validateSlackGetFile(raw json.RawMessage) error {
	var props struct {
		FileID string `json:"fileId"`
	}
	if err := decodeStrict(raw, &props); err != nil || !boundedIdentifier(props.FileID, 100) {
		return errors.New("invalid Slack get-file properties")
	}
	return nil
}

func validateSlackCustomAPICall(raw json.RawMessage) error {
	var props struct {
		URL          string          `json:"url"`
		Method       string          `json:"method"`
		Headers      map[string]any  `json:"headers"`
		Query        map[string]any  `json:"query"`
		BodyType     string          `json:"body_type"`
		Body         json.RawMessage `json:"body"`
		UseUserToken bool            `json:"use_user_token"`
	}
	if err := decodeStrict(raw, &props); err != nil || !customAPIURL(props.URL, "slack.com", "www.slack.com", "api.slack.com") ||
		!oneOf(strings.ToUpper(props.Method), customAPIMethods...) ||
		!customAPIHeaders(props.Headers) || !customAPIObject(props.Query) ||
		(props.BodyType != "" && !oneOf(props.BodyType, "none", "json", "form_data", "raw")) ||
		(len(props.Body) > 64*1024) {
		return errors.New("invalid Slack custom API call properties")
	}
	if (props.BodyType == "" || props.BodyType == "none") && len(props.Body) > 0 && string(props.Body) != "null" {
		return errors.New("invalid Slack custom API call properties")
	}
	return nil
}

func prepareSlackCustomAPICall(raw json.RawMessage) (json.RawMessage, error) {
	prepared, err := prepareCustomAPICall(raw)
	if err != nil {
		return nil, err
	}
	var extra struct {
		UseUserToken bool `json:"use_user_token"`
	}
	_ = json.Unmarshal(raw, &extra)
	var object map[string]any
	if err := json.Unmarshal(prepared, &object); err != nil {
		return nil, err
	}
	object["useUserToken"] = extra.UseUserToken
	return json.Marshal(object)
}

func validateSlackIgnoreBots(raw json.RawMessage) error {
	var props struct {
		IgnoreBots *bool `json:"ignoreBots"`
	}
	if err := json.Unmarshal(raw, &props); err != nil {
		return errors.New("trigger properties must be a JSON object")
	}
	return decodeStrict(raw, &props)
}

func validateSlackChannelMessageTrigger(raw json.RawMessage) error {
	var props struct {
		Channel    string `json:"channel"`
		IgnoreBots *bool  `json:"ignoreBots"`
	}
	if err := decodeStrict(raw, &props); err != nil || !boundedIdentifier(props.Channel, 100) {
		return errors.New("invalid Slack new-message-in-channel properties")
	}
	return nil
}

func validateSlackDirectMessageTrigger(raw json.RawMessage) error {
	var props struct {
		IgnoreBots         *bool `json:"ignoreBots"`
		IgnoreSelfMessages *bool `json:"ignoreSelfMessages"`
	}
	if err := decodeStrict(raw, &props); err != nil {
		return errors.New("invalid Slack new-direct-message properties")
	}
	return nil
}

func validateSlackMentionTrigger(raw json.RawMessage) error {
	var props struct {
		Users         []string `json:"users"`
		Usergroups    []string `json:"usergroups"`
		Channels      []string `json:"channels"`
		IgnoreBots    *bool    `json:"ignoreBots"`
		RemoveMention *bool    `json:"removeMention"`
	}
	if err := decodeStrict(raw, &props); err != nil ||
		(len(props.Users) > 0 && !slackIdentifiers(props.Users, 1, 50)) ||
		(len(props.Usergroups) > 0 && !slackIdentifiers(props.Usergroups, 1, 50)) ||
		(len(props.Channels) > 0 && !slackIdentifiers(props.Channels, 1, 50)) ||
		(len(props.Users) == 0 && len(props.Usergroups) == 0) {
		return errors.New("invalid Slack new-mention properties")
	}
	return nil
}

func validateSlackMentionDMTrigger(raw json.RawMessage) error {
	var props struct {
		User               string `json:"user"`
		IgnoreBots         *bool  `json:"ignoreBots"`
		IgnoreSelfMessages *bool  `json:"ignoreSelfMessages"`
	}
	if err := decodeStrict(raw, &props); err != nil || !boundedIdentifier(props.User, 100) {
		return errors.New("invalid Slack new-mention-in-direct-message properties")
	}
	return nil
}

func validateSlackReactionTrigger(raw json.RawMessage) error {
	var props struct {
		Emojis   []string `json:"emojis"`
		User     string   `json:"user"`
		Channels []string `json:"channels"`
	}
	if err := decodeStrict(raw, &props); err != nil ||
		(props.User != "" && !boundedIdentifier(props.User, 100)) ||
		(len(props.Channels) > 0 && !slackIdentifiers(props.Channels, 1, 50)) {
		return errors.New("invalid Slack reaction trigger properties")
	}
	for _, emoji := range props.Emojis {
		if !slackReactionName(emoji) {
			return errors.New("invalid Slack reaction trigger properties")
		}
	}
	if len(props.Emojis) > 50 {
		return errors.New("invalid Slack reaction trigger properties")
	}
	return nil
}

func validateSlackCommandTrigger(raw json.RawMessage) error {
	var props struct {
		User       string   `json:"user"`
		Commands   []string `json:"commands"`
		Channels   []string `json:"channels"`
		IgnoreBots *bool    `json:"ignoreBots"`
	}
	if err := decodeStrict(raw, &props); err != nil || !boundedIdentifier(props.User, 100) ||
		!slackCommands(props.Commands) ||
		(len(props.Channels) > 0 && !slackIdentifiers(props.Channels, 1, 50)) {
		return errors.New("invalid Slack new-command properties")
	}
	return nil
}

func validateSlackCommandDMTrigger(raw json.RawMessage) error {
	var props struct {
		User               string   `json:"user"`
		Commands           []string `json:"commands"`
		IgnoreBots         *bool    `json:"ignoreBots"`
		IgnoreSelfMessages *bool    `json:"ignoreSelfMessages"`
	}
	if err := decodeStrict(raw, &props); err != nil || !boundedIdentifier(props.User, 100) || !slackCommands(props.Commands) {
		return errors.New("invalid Slack new-command-in-direct-message properties")
	}
	return nil
}

func slackBlocks(raw json.RawMessage) error {
	if len(raw) == 0 {
		return nil
	}
	var blocks []any
	if len(raw) > 100*1024 || json.Unmarshal(raw, &blocks) != nil || len(blocks) == 0 || len(blocks) > 50 {
		return errors.New("invalid Slack blocks")
	}
	return nil
}

func slackReactionName(value string) bool {
	return len(value) > 0 && len(value) <= 100 && !strings.ContainsAny(value, ": \t\r\n")
}

func slackIdentifiers(values []string, minimum, maximum int) bool {
	if len(values) < minimum || len(values) > maximum {
		return false
	}
	for _, value := range values {
		if !boundedIdentifier(value, 100) {
			return false
		}
	}
	return true
}

func slackCommands(values []string) bool {
	if len(values) < 1 || len(values) > 20 {
		return false
	}
	for _, value := range values {
		if strings.TrimSpace(value) == "" || len(value) > 80 || strings.ContainsAny(value, " \t\r\n") {
			return false
		}
	}
	return true
}
