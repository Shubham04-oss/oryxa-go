package actionflow

import (
	"encoding/json"
	"fmt"

	integrationevents "github.com/Shubham04-oss/oryxa-go/contracts/events/integrations/v1"
)

type DomainMapper struct{}

func (DomainMapper) Map(request MapRequest) (MappedResult, error) {
	if request.Connection.Provider == "github" {
		return mapGitHub(request)
	}
	if request.Connection.Provider == "google" {
		return mapGoogle(request)
	}
	if request.Connection.Provider == "microsoft" {
		return mapMicrosoft(request)
	}
	if request.Connection.Provider != "slack" {
		return MappedResult{}, fmt.Errorf("%w: unsupported provider", ErrPieceOutputInvalid)
	}
	return mapSlack(request)
}

func mapSlack(request MapRequest) (MappedResult, error) {
	switch request.ActionName {
	case "post-message", "send-direct-message", "update-message", "send-ephemeral-message":
		return mapSlackMessage(request)
	case "schedule-message":
		return mapSlackScheduled(request)
	case "delete-message", "delete-scheduled-message", "mark-conversation-read", "close-dm":
		return mapSlackChannelMutation(request, "")
	case "add-reaction":
		return mapSlackReaction(request, false)
	case "remove-reaction":
		return mapSlackReaction(request, true)
	case "create-channel":
		return mapSlackCreatedChannel(request)
	case "archive-channel", "unarchive-channel", "rename-channel", "set-channel-topic",
		"set-channel-purpose", "join-channel", "leave-channel", "invite-users-to-channel",
		"remove-user-from-channel":
		return mapSlackChannelMutation(request, request.ActionName)
	case "set-user-status", "update-profile", "update-user-group-members":
		return mapSlackOKResult(request)
	case "find-channel", "get-channel-info":
		return mapSlackChannel(request)
	case "get-message":
		return mapSlackGetMessage(request)
	case "get-message-permalink":
		return mapSlackPermalink(request)
	case "get-channel-history", "get-thread-replies", "list-scheduled-messages",
		"get-reactions", "list-channels", "list-channel-members", "list-users",
		"list-user-conversations", "list-user-reactions", "list-user-groups",
		"list-custom-emoji", "list-files", "search-messages", "find-user-by-email",
		"find-user-by-handle", "find-user-by-id", "get-user", "get-user-group-by-handle":
		return mapSlackLookup(request)
	case "upload-file":
		return mapSlackUploadFile(request)
	case "get-file":
		return mapSlackGetFile(request)
	case "custom-api-call":
		return mapProviderHTTPCall(request)
	default:
		return MappedResult{}, fmt.Errorf("%w: unsupported action", ErrPieceOutputInvalid)
	}
}

func mapSlackMessage(request MapRequest) (MappedResult, error) {
	var output struct {
		OK        bool   `json:"ok"`
		Channel   string `json:"channel"`
		TS        string `json:"ts"`
		MessageTS string `json:"message_ts"`
		Message   struct {
			ThreadTS string `json:"thread_ts"`
		} `json:"message"`
	}
	var props struct {
		Channel string `json:"channel"`
		UserID  string `json:"userId"`
	}
	_ = json.Unmarshal(request.Props, &props)
	if err := json.Unmarshal(request.Output, &output); err != nil || !output.OK {
		return MappedResult{}, ErrPieceOutputInvalid
	}
	channel := firstNonEmpty(output.Channel, props.Channel, props.UserID)
	ts := firstNonEmpty(output.TS, output.MessageTS)
	if channel == "" || ts == "" {
		return MappedResult{}, ErrPieceOutputInvalid
	}
	result, _ := json.Marshal(struct {
		ChannelID string `json:"channel_id"`
		MessageTS string `json:"message_timestamp"`
		ThreadTS  string `json:"thread_timestamp,omitempty"`
	}{ChannelID: channel, MessageTS: ts, ThreadTS: output.Message.ThreadTS})
	data := integrationevents.SlackMessageSent{
		ExecutionID: request.Execution.ID, ConnectionID: request.Connection.ID,
		ChannelID: channel, MessageTS: ts, ThreadTS: output.Message.ThreadTS,
		RequestedBy: request.Execution.MemberID, ProviderSentAt: request.OccurredAt,
	}
	return MappedResult{SafeResult: result, DetailType: integrationevents.SlackMessageSentDetailType, Data: data}, nil
}

func mapSlackScheduled(request MapRequest) (MappedResult, error) {
	var output struct {
		OK                 bool   `json:"ok"`
		Channel            string `json:"channel"`
		ScheduledMessageID string `json:"scheduled_message_id"`
	}
	var props struct {
		Channel string `json:"channel"`
	}
	if json.Unmarshal(request.Output, &output) != nil || !output.OK || output.ScheduledMessageID == "" ||
		json.Unmarshal(request.Props, &props) != nil {
		return MappedResult{}, ErrPieceOutputInvalid
	}
	channel := firstNonEmpty(output.Channel, props.Channel)
	if channel == "" {
		return MappedResult{}, ErrPieceOutputInvalid
	}
	result, _ := json.Marshal(struct {
		ChannelID          string `json:"channel_id"`
		ScheduledMessageID string `json:"scheduled_message_id"`
	}{ChannelID: channel, ScheduledMessageID: output.ScheduledMessageID})
	data := integrationevents.SlackMessageScheduled{
		ExecutionID: request.Execution.ID, ConnectionID: request.Connection.ID,
		ChannelID: channel, ScheduledMessageID: output.ScheduledMessageID,
		RequestedBy: request.Execution.MemberID, ScheduledAt: request.OccurredAt,
	}
	return MappedResult{SafeResult: result, DetailType: integrationevents.SlackMessageScheduledDetailType, Data: data}, nil
}

func mapSlackReaction(request MapRequest, removed bool) (MappedResult, error) {
	var output struct {
		OK bool `json:"ok"`
	}
	var props struct {
		Channel  string `json:"channel"`
		TS       string `json:"ts"`
		Reaction string `json:"reaction"`
		File     string `json:"file"`
	}
	if json.Unmarshal(request.Output, &output) != nil || !output.OK || json.Unmarshal(request.Props, &props) != nil ||
		props.Reaction == "" || (props.Channel == "" && props.File == "") {
		return MappedResult{}, ErrPieceOutputInvalid
	}
	result, _ := json.Marshal(struct {
		ChannelID        string `json:"channel_id,omitempty"`
		MessageTimestamp string `json:"message_timestamp,omitempty"`
		FileID           string `json:"file_id,omitempty"`
		Reaction         string `json:"reaction"`
	}{ChannelID: props.Channel, MessageTimestamp: props.TS, FileID: props.File, Reaction: props.Reaction})
	if removed {
		data := integrationevents.SlackReactionRemoved{
			ExecutionID: request.Execution.ID, ConnectionID: request.Connection.ID,
			ChannelID: props.Channel, MessageTimestamp: props.TS, Reaction: props.Reaction,
			RequestedBy: request.Execution.MemberID, RemovedAt: request.OccurredAt,
		}
		return MappedResult{SafeResult: result, DetailType: integrationevents.SlackReactionRemovedDetailType, Data: data}, nil
	}
	data := integrationevents.SlackReactionAdded{
		ExecutionID: request.Execution.ID, ConnectionID: request.Connection.ID,
		ChannelID: props.Channel, MessageTimestamp: props.TS, Reaction: props.Reaction,
		RequestedBy: request.Execution.MemberID, AddedAt: request.OccurredAt,
	}
	return MappedResult{SafeResult: result, DetailType: integrationevents.SlackReactionAddedDetailType, Data: data}, nil
}

func mapSlackCreatedChannel(request MapRequest) (MappedResult, error) {
	var output struct {
		OK      bool `json:"ok"`
		Channel struct {
			ID        string `json:"id"`
			Name      string `json:"name"`
			IsPrivate bool   `json:"is_private"`
		} `json:"channel"`
		ID        string `json:"id"`
		Name      string `json:"name"`
		IsPrivate bool   `json:"is_private"`
	}
	if json.Unmarshal(request.Output, &output) != nil {
		return MappedResult{}, ErrPieceOutputInvalid
	}
	id := firstNonEmpty(output.Channel.ID, output.ID)
	name := firstNonEmpty(output.Channel.Name, output.Name)
	if id == "" || name == "" {
		return MappedResult{}, ErrPieceOutputInvalid
	}
	private := output.Channel.IsPrivate || output.IsPrivate
	result, _ := json.Marshal(struct {
		ID        string `json:"id"`
		Name      string `json:"name"`
		IsPrivate bool   `json:"is_private"`
	}{ID: id, Name: name, IsPrivate: private})
	data := integrationevents.SlackChannelMutated{
		ExecutionID: request.Execution.ID, ConnectionID: request.Connection.ID,
		ChannelID: id, Mutation: "create-channel", RequestedBy: request.Execution.MemberID,
		MutatedAt: request.OccurredAt,
	}
	return MappedResult{SafeResult: result, DetailType: integrationevents.SlackChannelMutatedDetailType, Data: data}, nil
}

func mapSlackChannelMutation(request MapRequest, mutation string) (MappedResult, error) {
	var output struct {
		OK bool `json:"ok"`
	}
	var props struct {
		Channel            string `json:"channel"`
		TS                 string `json:"ts"`
		ScheduledMessageID string `json:"scheduledMessageId"`
	}
	if json.Unmarshal(request.Output, &output) != nil || !output.OK || json.Unmarshal(request.Props, &props) != nil || props.Channel == "" {
		return MappedResult{}, ErrPieceOutputInvalid
	}
	result, _ := json.Marshal(struct {
		ChannelID          string `json:"channel_id"`
		MessageTS          string `json:"message_timestamp,omitempty"`
		ScheduledMessageID string `json:"scheduled_message_id,omitempty"`
	}{ChannelID: props.Channel, MessageTS: props.TS, ScheduledMessageID: props.ScheduledMessageID})
	switch request.ActionName {
	case "delete-message":
		data := integrationevents.SlackMessageDeleted{
			ExecutionID: request.Execution.ID, ConnectionID: request.Connection.ID,
			ChannelID: props.Channel, MessageTS: props.TS, RequestedBy: request.Execution.MemberID,
			DeletedAt: request.OccurredAt,
		}
		return MappedResult{SafeResult: result, DetailType: integrationevents.SlackMessageDeletedDetailType, Data: data}, nil
	case "delete-scheduled-message", "mark-conversation-read", "close-dm":
		return MappedResult{SafeResult: result}, nil
	default:
		data := integrationevents.SlackChannelMutated{
			ExecutionID: request.Execution.ID, ConnectionID: request.Connection.ID,
			ChannelID: props.Channel, Mutation: mutation, RequestedBy: request.Execution.MemberID,
			MutatedAt: request.OccurredAt,
		}
		return MappedResult{SafeResult: result, DetailType: integrationevents.SlackChannelMutatedDetailType, Data: data}, nil
	}
}

func mapSlackOKResult(request MapRequest) (MappedResult, error) {
	var output struct {
		OK bool `json:"ok"`
	}
	if json.Unmarshal(request.Output, &output) != nil || !output.OK {
		return MappedResult{}, ErrPieceOutputInvalid
	}
	result, _ := json.Marshal(struct {
		OK bool `json:"ok"`
	}{OK: true})
	return MappedResult{SafeResult: result}, nil
}

func mapSlackGetMessage(request MapRequest) (MappedResult, error) {
	var output struct {
		OK       bool `json:"ok"`
		Messages []struct {
			TS     string `json:"ts"`
			User   string `json:"user"`
			Text   string `json:"text"`
			Thread string `json:"thread_ts"`
		} `json:"messages"`
	}
	var props struct {
		Channel string `json:"channel"`
		TS      string `json:"ts"`
	}
	if json.Unmarshal(request.Output, &output) != nil || json.Unmarshal(request.Props, &props) != nil ||
		props.Channel == "" || len(output.Messages) == 0 || output.Messages[0].TS == "" {
		return MappedResult{}, ErrPieceOutputInvalid
	}
	message := output.Messages[0]
	result, err := json.Marshal(struct {
		ChannelID string `json:"channel_id"`
		MessageTS string `json:"message_timestamp"`
		UserID    string `json:"user_id,omitempty"`
		Text      string `json:"text,omitempty"`
		ThreadTS  string `json:"thread_timestamp,omitempty"`
	}{ChannelID: props.Channel, MessageTS: message.TS, UserID: message.User, Text: message.Text, ThreadTS: message.Thread})
	if err != nil {
		return MappedResult{}, ErrPieceOutputInvalid
	}
	return MappedResult{SafeResult: result}, nil
}

func mapSlackPermalink(request MapRequest) (MappedResult, error) {
	var output struct {
		OK        bool   `json:"ok"`
		Permalink string `json:"permalink"`
		Channel   string `json:"channel"`
	}
	if json.Unmarshal(request.Output, &output) != nil || !output.OK || output.Permalink == "" {
		return MappedResult{}, ErrPieceOutputInvalid
	}
	result, err := json.Marshal(struct {
		ChannelID string `json:"channel_id,omitempty"`
		Permalink string `json:"permalink"`
	}{ChannelID: output.Channel, Permalink: output.Permalink})
	if err != nil {
		return MappedResult{}, ErrPieceOutputInvalid
	}
	return MappedResult{SafeResult: result}, nil
}

func mapSlackLookup(request MapRequest) (MappedResult, error) {
	if !json.Valid(request.Output) || len(request.Output) == 0 {
		return MappedResult{}, ErrPieceOutputInvalid
	}
	var asObject map[string]any
	if json.Unmarshal(request.Output, &asObject) == nil {
		delete(asObject, "token")
		delete(asObject, "access_token")
		delete(asObject, "response_metadata")
		if files, ok := asObject["files"].([]any); ok {
			if len(files) > 100 {
				return MappedResult{}, ErrPieceOutputInvalid
			}
			cleaned := make([]any, 0, len(files))
			for _, file := range files {
				item, _ := file.(map[string]any)
				if item == nil {
					continue
				}
				cleaned = append(cleaned, map[string]any{
					"id": item["id"], "name": item["name"], "mimetype": item["mimetype"],
					"size": item["size"], "created": item["created"], "user": item["user"],
					"channels": item["channels"],
				})
			}
			asObject["files"] = cleaned
		}
		result, err := json.Marshal(asObject)
		if err != nil {
			return MappedResult{}, ErrPieceOutputInvalid
		}
		return MappedResult{SafeResult: result}, nil
	}
	var asArray []any
	if json.Unmarshal(request.Output, &asArray) != nil {
		return MappedResult{}, ErrPieceOutputInvalid
	}
	if len(asArray) > 100 {
		asArray = asArray[:100]
	}
	result, err := json.Marshal(asArray)
	if err != nil {
		return MappedResult{}, ErrPieceOutputInvalid
	}
	return MappedResult{SafeResult: result}, nil
}

func mapSlackChannel(request MapRequest) (MappedResult, error) {
	var wrapped struct {
		OK      bool `json:"ok"`
		Channel struct {
			ID         string `json:"id"`
			Name       string `json:"name"`
			IsPrivate  bool   `json:"is_private"`
			IsArchived bool   `json:"is_archived"`
			IsMember   bool   `json:"is_member"`
			NumMembers int    `json:"num_members"`
		} `json:"channel"`
	}
	var channel struct {
		ID         string `json:"id"`
		Name       string `json:"name"`
		IsPrivate  bool   `json:"is_private"`
		IsArchived bool   `json:"is_archived"`
		IsMember   bool   `json:"is_member"`
		NumMembers int    `json:"num_members"`
	}
	if json.Unmarshal(request.Output, &wrapped) == nil && wrapped.Channel.ID != "" {
		channel = wrapped.Channel
	} else if json.Unmarshal(request.Output, &channel) != nil || channel.ID == "" || channel.Name == "" {
		return MappedResult{}, ErrPieceOutputInvalid
	}
	result, err := json.Marshal(channel)
	if err != nil {
		return MappedResult{}, ErrPieceOutputInvalid
	}
	return MappedResult{SafeResult: result}, nil
}

func mapSlackUploadFile(request MapRequest) (MappedResult, error) {
	var output struct {
		OK    bool `json:"ok"`
		Files []struct {
			ID string `json:"id"`
		} `json:"files"`
		File struct {
			ID string `json:"id"`
		} `json:"file"`
	}
	var props struct {
		File struct {
			FileID string `json:"file_id"`
		} `json:"file"`
	}
	if json.Unmarshal(request.Output, &output) != nil || json.Unmarshal(request.Props, &props) != nil {
		return MappedResult{}, ErrPieceOutputInvalid
	}
	slackID := output.File.ID
	if slackID == "" && len(output.Files) > 0 {
		slackID = output.Files[0].ID
	}
	if slackID == "" {
		return MappedResult{}, ErrPieceOutputInvalid
	}
	result, err := json.Marshal(struct {
		SlackFileID string `json:"slack_file_id"`
		FileID      string `json:"file_id,omitempty"`
	}{SlackFileID: slackID, FileID: props.File.FileID})
	if err != nil {
		return MappedResult{}, ErrPieceOutputInvalid
	}
	return MappedResult{SafeResult: result, DetailType: integrationevents.SlackFileUploadedDetailType,
		Data: integrationevents.SlackFileUploaded{
			ExecutionID: request.Execution.ID, ConnectionID: request.Connection.ID,
			SlackFileID: slackID, FileID: props.File.FileID, RequestedBy: request.Execution.MemberID,
			UploadedAt: request.OccurredAt,
		}}, nil
}

func mapSlackGetFile(request MapRequest) (MappedResult, error) {
	var output struct {
		ID       string `json:"id"`
		Name     string `json:"name"`
		Mimetype string `json:"mimetype"`
		Size     int64  `json:"size"`
		Data     storedFile
	}
	if json.Unmarshal(request.Output, &output) != nil || output.ID == "" || output.Data.FileID == "" {
		return MappedResult{}, ErrPieceOutputInvalid
	}
	result, err := json.Marshal(struct {
		ID       string `json:"id"`
		Name     string `json:"name,omitempty"`
		MimeType string `json:"mimetype,omitempty"`
		Size     int64  `json:"size,omitempty"`
		FileID   string `json:"file_id"`
		Filename string `json:"filename,omitempty"`
	}{ID: output.ID, Name: output.Name, MimeType: output.Mimetype, Size: output.Size,
		FileID: output.Data.FileID, Filename: firstNonEmpty(output.Data.FileName, output.Name)})
	if err != nil {
		return MappedResult{}, ErrPieceOutputInvalid
	}
	return MappedResult{SafeResult: result}, nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
