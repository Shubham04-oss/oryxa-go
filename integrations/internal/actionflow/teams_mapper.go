package actionflow

import (
	"encoding/json"

	integrationevents "github.com/Shubham04-oss/oryxa-go/contracts/events/integrations/v1"
)

func mapMicrosoftTeamsAction(request MapRequest) (MappedResult, bool, error) {
	switch request.ActionName {
	case "create-channel", "create-private-channel":
		result, err := mapMicrosoftTeamsChannel(request, true)
		return result, true, err
	case "send-channel-message", "send-chat-message", "reply-to-channel-message", "create-chat-and-send-message":
		result, err := mapMicrosoftTeamsMessage(request, "sent")
		return result, true, err
	case "delete-chat-message":
		result, err := mapMicrosoftTeamsMessage(request, "deleted")
		return result, true, err
	case "get-chat-message", "get-channel-message":
		result, err := mapMicrosoftTeamsMessage(request, "")
		return result, true, err
	case "find-channel":
		result, err := mapMicrosoftTeamsChannel(request, false)
		return result, true, err
	case "find-team-member":
		result, err := mapMicrosoftTeamsMember(request)
		return result, true, err
	case "get-meeting-transcript":
		result, err := mapMicrosoftTeamsTranscript(request)
		return result, true, err
	case "get-meeting-recording":
		result, err := mapMicrosoftTeamsRecording(request)
		return result, true, err
	case "teams-custom-api-call":
		result, err := mapProviderHTTPCall(request)
		return result, true, err
	default:
		return MappedResult{}, false, nil
	}
}

func mapMicrosoftTeamsChannel(request MapRequest, created bool) (MappedResult, error) {
	payload := unwrapHTTPBody(request.Output)
	var output struct {
		ID          string `json:"id"`
		DisplayName string `json:"displayName"`
	}
	_ = json.Unmarshal(payload, &output)
	var props struct {
		TeamID    string `json:"teamId"`
		ChannelID string `json:"channelId"`
	}
	_ = json.Unmarshal(request.Props, &props)
	id := firstNonEmpty(output.ID, props.ChannelID)
	if created && id == "" {
		return MappedResult{}, ErrPieceOutputInvalid
	}
	if !created && output.ID == "" && output.DisplayName == "" {
		var list []struct {
			ID          string `json:"id"`
			DisplayName string `json:"displayName"`
		}
		if json.Unmarshal(payload, &list) == nil {
			items, truncated := googleCap(list, 100)
			safe := make([]any, 0, len(items))
			for _, item := range items {
				if item.ID == "" {
					return MappedResult{}, ErrPieceOutputInvalid
				}
				safe = append(safe, struct {
					ID   string `json:"id"`
					Name string `json:"name,omitempty"`
				}{ID: item.ID, Name: item.DisplayName})
			}
			return googleJSON(struct {
				Channels  []any `json:"channels"`
				Truncated bool  `json:"truncated"`
			}{Channels: safe, Truncated: truncated})
		}
	}
	if id == "" {
		return MappedResult{}, ErrPieceOutputInvalid
	}
	result, err := json.Marshal(struct {
		ID     string `json:"id"`
		Name   string `json:"name,omitempty"`
		TeamID string `json:"team_id,omitempty"`
	}{ID: id, Name: output.DisplayName, TeamID: props.TeamID})
	if err != nil {
		return MappedResult{}, ErrPieceOutputInvalid
	}
	if !created {
		return MappedResult{SafeResult: result}, nil
	}
	return MappedResult{SafeResult: result, DetailType: integrationevents.MicrosoftTeamsChannelCreatedDetailType,
		Data: integrationevents.MicrosoftTeamsChannelCreated{
			ExecutionID: request.Execution.ID, ConnectionID: request.Connection.ID,
			TeamID: props.TeamID, ChannelID: id, RequestedBy: request.Execution.MemberID,
			CreatedAt: request.OccurredAt,
		}}, nil
}

func mapMicrosoftTeamsMessage(request MapRequest, kind string) (MappedResult, error) {
	payload := unwrapHTTPBody(request.Output)
	var output struct {
		ID              string `json:"id"`
		CreatedDateTime string `json:"createdDateTime"`
		ChatID          string `json:"chatId"`
	}
	_ = json.Unmarshal(payload, &output)
	var props struct {
		MessageID string `json:"messageId"`
		ChatID    string `json:"chatId"`
		ChannelID string `json:"channelId"`
	}
	_ = json.Unmarshal(request.Props, &props)
	id := firstNonEmpty(output.ID, props.MessageID)
	if id == "" && kind != "deleted" {
		return MappedResult{}, ErrPieceOutputInvalid
	}
	if id == "" {
		id = props.MessageID
	}
	if id == "" {
		return MappedResult{}, ErrPieceOutputInvalid
	}
	result, err := json.Marshal(struct {
		ID        string `json:"id"`
		ChatID    string `json:"chat_id,omitempty"`
		ChannelID string `json:"channel_id,omitempty"`
		CreatedAt string `json:"created_at,omitempty"`
	}{ID: id, ChatID: firstNonEmpty(output.ChatID, props.ChatID), ChannelID: props.ChannelID,
		CreatedAt: output.CreatedDateTime})
	if err != nil {
		return MappedResult{}, ErrPieceOutputInvalid
	}
	switch kind {
	case "sent":
		return MappedResult{SafeResult: result, DetailType: integrationevents.MicrosoftTeamsMessageSentDetailType,
			Data: integrationevents.MicrosoftTeamsMessageSent{
				ExecutionID: request.Execution.ID, ConnectionID: request.Connection.ID, MessageID: id,
				RequestedBy: request.Execution.MemberID, SentAt: request.OccurredAt,
			}}, nil
	case "deleted":
		return MappedResult{SafeResult: result, DetailType: integrationevents.MicrosoftTeamsMessageDeletedDetailType,
			Data: integrationevents.MicrosoftTeamsMessageDeleted{
				ExecutionID: request.Execution.ID, ConnectionID: request.Connection.ID, MessageID: id,
				RequestedBy: request.Execution.MemberID, DeletedAt: request.OccurredAt,
			}}, nil
	default:
		return MappedResult{SafeResult: result}, nil
	}
}

func mapMicrosoftTeamsMember(request MapRequest) (MappedResult, error) {
	payload := unwrapHTTPBody(request.Output)
	var members []struct {
		ID string `json:"id"`
	}
	if json.Unmarshal(payload, &members) != nil {
		var single struct {
			ID string `json:"id"`
		}
		if json.Unmarshal(payload, &single) != nil || single.ID == "" {
			return MappedResult{}, ErrPieceOutputInvalid
		}
		members = []struct {
			ID string `json:"id"`
		}{single}
	}
	items, truncated := googleCap(members, 100)
	safe := make([]any, 0, len(items))
	for _, item := range items {
		if item.ID == "" {
			return MappedResult{}, ErrPieceOutputInvalid
		}
		safe = append(safe, struct {
			ID string `json:"id"`
		}{ID: item.ID})
	}
	return googleJSON(struct {
		Members   []any `json:"members"`
		Truncated bool  `json:"truncated"`
	}{Members: safe, Truncated: truncated})
}

func mapMicrosoftTeamsTranscript(request MapRequest) (MappedResult, error) {
	payload := unwrapHTTPBody(request.Output)
	text := docsPlaintext(payload)
	var output struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(payload, &output)
	capped, truncated := capText(text, 32*1024)
	return googleJSON(struct {
		ID        string `json:"id,omitempty"`
		Text      string `json:"text,omitempty"`
		Truncated bool   `json:"truncated"`
	}{ID: output.ID, Text: capped, Truncated: truncated})
}

func mapMicrosoftTeamsRecording(request MapRequest) (MappedResult, error) {
	payload := unwrapHTTPBody(request.Output)
	var output struct {
		ID              string `json:"id"`
		CreatedDateTime string `json:"createdDateTime"`
		MeetingID       string `json:"meetingId"`
	}
	_ = json.Unmarshal(payload, &output)
	if output.ID == "" {
		return MappedResult{}, ErrPieceOutputInvalid
	}
	return googleJSON(struct {
		ID        string `json:"id"`
		MeetingID string `json:"meeting_id,omitempty"`
		CreatedAt string `json:"created_at,omitempty"`
	}{ID: output.ID, MeetingID: output.MeetingID, CreatedAt: output.CreatedDateTime})
}
