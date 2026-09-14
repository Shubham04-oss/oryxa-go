package catalog

import (
	"encoding/json"
	"errors"
)

const (
	microsoftChannelCreateScope     = "Channel.Create"
	microsoftChannelReadScope       = "Channel.ReadBasic.All"
	microsoftChannelMessageSend     = "ChannelMessage.Send"
	microsoftChannelMessageRead     = "ChannelMessage.Read.All"
	microsoftChatReadWriteScope     = "Chat.ReadWrite"
	microsoftTeamMemberReadScope    = "TeamMember.Read.All"
	microsoftMeetingTranscriptScope = "OnlineMeetingTranscript.Read.All"
	microsoftMeetingRecordingScope  = "OnlineMeetingRecording.Read.All"
)

var microsoftTeamsActions = map[string]microsoftActionSpec{
	"create-channel": {
		MicrosoftTeamsPieceName, MicrosoftTeamsPieceVersion, "microsoft_teams_create_channel",
		[]string{microsoftChannelCreateScope}, validateTeamsCreateChannel, nil,
	},
	"send-channel-message": {
		MicrosoftTeamsPieceName, MicrosoftTeamsPieceVersion, "microsoft_teams_send_channel_message",
		[]string{microsoftChannelMessageSend}, validateTeamsChannelMessage, nil,
	},
	"send-chat-message": {
		MicrosoftTeamsPieceName, MicrosoftTeamsPieceVersion, "microsoft_teams_send_chat_message",
		[]string{microsoftChatReadWriteScope}, validateTeamsChatMessage, nil,
	},
	"reply-to-channel-message": {
		MicrosoftTeamsPieceName, MicrosoftTeamsPieceVersion, "microsoft_teams_reply_to_channel_message",
		[]string{microsoftChannelMessageSend}, validateTeamsReply, nil,
	},
	"create-chat-and-send-message": {
		MicrosoftTeamsPieceName, MicrosoftTeamsPieceVersion, "microsoft_teams_create_chat_and_send_message",
		[]string{microsoftChatReadWriteScope}, validateTeamsCreateChat, nil,
	},
	"create-private-channel": {
		MicrosoftTeamsPieceName, MicrosoftTeamsPieceVersion, "microsoft_teams_create_private_channel",
		[]string{microsoftChannelCreateScope}, validateTeamsCreateChannel, nil,
	},
	"get-chat-message": {
		MicrosoftTeamsPieceName, MicrosoftTeamsPieceVersion, "microsoft_teams_get_chat_message",
		[]string{microsoftChatReadWriteScope}, validateTeamsGetChatMessage, nil,
	},
	"delete-chat-message": {
		MicrosoftTeamsPieceName, MicrosoftTeamsPieceVersion, "microsoft_teams_delete_chat_message",
		[]string{microsoftChatReadWriteScope}, validateTeamsGetChatMessage, nil,
	},
	"get-channel-message": {
		MicrosoftTeamsPieceName, MicrosoftTeamsPieceVersion, "microsoft_teams_get_channel_message",
		[]string{microsoftChannelMessageRead}, validateTeamsGetChannelMessage, nil,
	},
	"find-channel": {
		MicrosoftTeamsPieceName, MicrosoftTeamsPieceVersion, "microsoft_teams_find_channel",
		[]string{microsoftChannelReadScope}, validateTeamsFindChannel, nil,
	},
	"find-team-member": {
		MicrosoftTeamsPieceName, MicrosoftTeamsPieceVersion, "microsoft_teams_find_team_member",
		[]string{microsoftTeamMemberReadScope}, validateTeamsFindMember, nil,
	},
	"get-meeting-transcript": {
		MicrosoftTeamsPieceName, MicrosoftTeamsPieceVersion, "microsoft_teams_get_meeting_transcript",
		[]string{microsoftMeetingTranscriptScope}, validateTeamsMeeting, nil,
	},
	"get-meeting-recording": {
		MicrosoftTeamsPieceName, MicrosoftTeamsPieceVersion, "microsoft_teams_get_meeting_recording",
		[]string{microsoftMeetingRecordingScope}, validateTeamsMeeting, nil,
	},
	"teams-custom-api-call": {
		MicrosoftTeamsPieceName, MicrosoftTeamsPieceVersion, "custom_api_call",
		[]string{"User.Read"}, validateCustomAPICall("graph.microsoft.com"), prepareCustomAPICall,
	},
}

func graphID(value string) bool { return boundedValue(value, 1024) }

func teamsContent(value string) bool {
	return len(value) > 0 && len(value) <= 100*1024
}

func validateTeamsCreateChannel(raw json.RawMessage) error {
	var props struct {
		TeamID             string `json:"teamId"`
		ChannelDisplayName string `json:"channelDisplayName"`
		ChannelDescription string `json:"channelDescription"`
	}
	if err := decodeStrict(raw, &props); err != nil || !graphID(props.TeamID) ||
		!boundedValue(props.ChannelDisplayName, 200) || len(props.ChannelDescription) > 8*1024 {
		return errors.New("invalid Microsoft Teams create-channel properties")
	}
	return nil
}

func validateTeamsChannelMessage(raw json.RawMessage) error {
	var props struct {
		TeamID      string `json:"teamId"`
		ChannelID   string `json:"channelId"`
		ContentType string `json:"contentType"`
		Content     string `json:"content"`
	}
	if err := decodeStrict(raw, &props); err != nil || !graphID(props.TeamID) || !graphID(props.ChannelID) ||
		!oneOf(props.ContentType, "text", "html") || !teamsContent(props.Content) {
		return errors.New("invalid Microsoft Teams send-channel-message properties")
	}
	return nil
}

func validateTeamsChatMessage(raw json.RawMessage) error {
	var props struct {
		ChatID      string `json:"chatId"`
		ContentType string `json:"contentType"`
		Content     string `json:"content"`
	}
	if err := decodeStrict(raw, &props); err != nil || !graphID(props.ChatID) ||
		!oneOf(props.ContentType, "text", "html") || !teamsContent(props.Content) {
		return errors.New("invalid Microsoft Teams send-chat-message properties")
	}
	return nil
}

func validateTeamsReply(raw json.RawMessage) error {
	var props struct {
		TeamID      string `json:"teamId"`
		ChannelID   string `json:"channelId"`
		MessageID   string `json:"messageId"`
		ContentType string `json:"contentType"`
		Content     string `json:"content"`
	}
	if err := decodeStrict(raw, &props); err != nil || !graphID(props.TeamID) || !graphID(props.ChannelID) ||
		!graphID(props.MessageID) || !oneOf(props.ContentType, "text", "html") || !teamsContent(props.Content) {
		return errors.New("invalid Microsoft Teams reply-to-channel-message properties")
	}
	return nil
}

func validateTeamsCreateChat(raw json.RawMessage) error {
	var props struct {
		TeamID      string   `json:"teamId"`
		Members     []string `json:"members"`
		ContentType string   `json:"contentType"`
		Content     string   `json:"content"`
	}
	if err := decodeStrict(raw, &props); err != nil || !graphID(props.TeamID) ||
		len(props.Members) == 0 || len(props.Members) > 50 ||
		!oneOf(props.ContentType, "text", "html") || !teamsContent(props.Content) {
		return errors.New("invalid Microsoft Teams create-chat-and-send-message properties")
	}
	for _, member := range props.Members {
		if !graphID(member) {
			return errors.New("invalid Microsoft Teams create-chat-and-send-message properties")
		}
	}
	return nil
}

func validateTeamsGetChatMessage(raw json.RawMessage) error {
	var props struct {
		ChatID    string `json:"chatId"`
		MessageID string `json:"messageId"`
	}
	if err := decodeStrict(raw, &props); err != nil || !graphID(props.ChatID) || !graphID(props.MessageID) {
		return errors.New("invalid Microsoft Teams chat-message properties")
	}
	return nil
}

func validateTeamsGetChannelMessage(raw json.RawMessage) error {
	var props struct {
		TeamID    string `json:"teamId"`
		ChannelID string `json:"channelId"`
		MessageID string `json:"messageId"`
		ReplyID   string `json:"replyId"`
	}
	if err := decodeStrict(raw, &props); err != nil || !graphID(props.TeamID) || !graphID(props.ChannelID) ||
		!graphID(props.MessageID) || (props.ReplyID != "" && !graphID(props.ReplyID)) {
		return errors.New("invalid Microsoft Teams get-channel-message properties")
	}
	return nil
}

func validateTeamsFindChannel(raw json.RawMessage) error {
	var props struct {
		TeamID      string `json:"teamId"`
		ChannelName string `json:"channelName"`
	}
	if err := decodeStrict(raw, &props); err != nil || !graphID(props.TeamID) || !boundedValue(props.ChannelName, 200) {
		return errors.New("invalid Microsoft Teams find-channel properties")
	}
	return nil
}

func validateTeamsFindMember(raw json.RawMessage) error {
	var props struct {
		TeamID      string `json:"teamId"`
		SearchBy    string `json:"searchBy"`
		SearchValue string `json:"searchValue"`
	}
	if err := decodeStrict(raw, &props); err != nil || !graphID(props.TeamID) ||
		!oneOf(props.SearchBy, "email", "name") || !boundedValue(props.SearchValue, 320) {
		return errors.New("invalid Microsoft Teams find-team-member properties")
	}
	if props.SearchBy == "email" && !email(props.SearchValue) {
		return errors.New("invalid Microsoft Teams find-team-member properties")
	}
	return nil
}

func validateTeamsMeeting(raw json.RawMessage) error {
	var props struct {
		MeetingIdentifierType  string `json:"meetingIdentifierType"`
		MeetingIdentifierValue string `json:"meetingIdentifierValue"`
		TranscriptID           string `json:"transcriptId"`
		RecordingID            string `json:"recordingId"`
	}
	if err := decodeStrict(raw, &props); err != nil ||
		!oneOf(props.MeetingIdentifierType, "meetingId", "joinWebUrl", "joinMeetingId") ||
		(props.TranscriptID != "" && !graphID(props.TranscriptID)) ||
		(props.RecordingID != "" && !graphID(props.RecordingID)) {
		return errors.New("invalid Microsoft Teams meeting properties")
	}
	switch props.MeetingIdentifierType {
	case "joinWebUrl":
		if !publicHTTPSURL(props.MeetingIdentifierValue) {
			return errors.New("invalid Microsoft Teams meeting properties")
		}
	default:
		if !graphID(props.MeetingIdentifierValue) {
			return errors.New("invalid Microsoft Teams meeting properties")
		}
	}
	return nil
}
