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
	if request.Connection.Provider != "slack" {
		return MappedResult{}, fmt.Errorf("%w: unsupported provider", ErrPieceOutputInvalid)
	}
	switch request.ActionName {
	case "post-message":
		return mapSlackMessage(request)
	case "add-reaction":
		return mapSlackReaction(request)
	case "find-channel":
		return mapSlackChannel(request)
	default:
		return MappedResult{}, fmt.Errorf("%w: unsupported action", ErrPieceOutputInvalid)
	}
}

func mapSlackMessage(request MapRequest) (MappedResult, error) {
	var output struct {
		OK      bool   `json:"ok"`
		Channel string `json:"channel"`
		TS      string `json:"ts"`
		Message struct {
			ThreadTS string `json:"thread_ts"`
		} `json:"message"`
	}
	if err := json.Unmarshal(request.Output, &output); err != nil || !output.OK || output.Channel == "" || output.TS == "" {
		return MappedResult{}, ErrPieceOutputInvalid
	}
	result, _ := json.Marshal(struct {
		ChannelID string `json:"channel_id"`
		MessageTS string `json:"message_timestamp"`
		ThreadTS  string `json:"thread_timestamp,omitempty"`
	}{ChannelID: output.Channel, MessageTS: output.TS, ThreadTS: output.Message.ThreadTS})
	data := integrationevents.SlackMessageSent{
		ExecutionID: request.Execution.ID, ConnectionID: request.Connection.ID,
		ChannelID: output.Channel, MessageTS: output.TS, ThreadTS: output.Message.ThreadTS,
		RequestedBy: request.Execution.MemberID, ProviderSentAt: request.OccurredAt,
	}
	return MappedResult{SafeResult: result, DetailType: integrationevents.SlackMessageSentDetailType, Data: data}, nil
}

func mapSlackReaction(request MapRequest) (MappedResult, error) {
	var output struct {
		OK bool `json:"ok"`
	}
	var props struct {
		Channel  string `json:"channel"`
		TS       string `json:"ts"`
		Reaction string `json:"reaction"`
	}
	if json.Unmarshal(request.Output, &output) != nil || !output.OK || json.Unmarshal(request.Props, &props) != nil || props.Channel == "" || props.TS == "" || props.Reaction == "" {
		return MappedResult{}, ErrPieceOutputInvalid
	}
	result, _ := json.Marshal(struct {
		ChannelID        string `json:"channel_id"`
		MessageTimestamp string `json:"message_timestamp"`
		Reaction         string `json:"reaction"`
	}{ChannelID: props.Channel, MessageTimestamp: props.TS, Reaction: props.Reaction})
	data := integrationevents.SlackReactionAdded{
		ExecutionID: request.Execution.ID, ConnectionID: request.Connection.ID,
		ChannelID: props.Channel, MessageTimestamp: props.TS, Reaction: props.Reaction,
		RequestedBy: request.Execution.MemberID, AddedAt: request.OccurredAt,
	}
	return MappedResult{SafeResult: result, DetailType: integrationevents.SlackReactionAddedDetailType, Data: data}, nil
}

func mapSlackChannel(request MapRequest) (MappedResult, error) {
	var channel struct {
		ID         string `json:"id"`
		Name       string `json:"name"`
		IsPrivate  bool   `json:"is_private"`
		IsArchived bool   `json:"is_archived"`
		IsMember   bool   `json:"is_member"`
		NumMembers int    `json:"num_members"`
	}
	if err := json.Unmarshal(request.Output, &channel); err != nil || channel.ID == "" || channel.Name == "" {
		return MappedResult{}, ErrPieceOutputInvalid
	}
	result, err := json.Marshal(channel)
	if err != nil {
		return MappedResult{}, ErrPieceOutputInvalid
	}
	return MappedResult{SafeResult: result}, nil
}
