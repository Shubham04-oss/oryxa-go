package httpserver

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"strings"
	"time"

	realtimev1 "github.com/Shubham04-oss/oryxa-go/contracts/events/realtime/v1"
	"github.com/Shubham04-oss/oryxa-go/realtime/internal/postgres"
)

// errTooMany marks a batch of more than MaxEvents events, which is answered
// 413, as a body too large is, rather than 400.
var errTooMany = fmt.Errorf("a call carries at most %d events", MaxEvents)

var (
	// eventID is an id the Worker gives an event: 1 to 200 of A-Z a-z 0-9 _ -.
	eventID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,200}$`)
	// nameID is one segment of a conversation's id: an organization's,
	// teamspace's or person's id as the web app lets a Durable Object's name
	// hold it, 1 to 128 of A-Z a-z 0-9 _ -.
	nameID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)
	// opaqueID is an id realtime only passes on, such as a message's or an
	// agent's: 1 to 200 printable ASCII characters, spaces not among them.
	opaqueID = regexp.MustCompile(`^[!-~]{1,200}$`)
	// utcTime is RFC 3339 in UTC, with up to nine digits of a fraction of a
	// second, or none. The Worker writes three.
	utcTime = regexp.MustCompile(`^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}(\.[0-9]{1,9})?Z$`)
)

// earliest and maxAhead bound when an event may say it happened: nothing
// Oryxa keeps is older than earliest, and no clock that stamped a message
// runs a day ahead of this one. Outside them, a time is not one the Worker's
// clock could have given, and is not put on the bus.
var earliest = time.Date(2020, time.January, 1, 0, 0, 0, 0, time.UTC)

const maxAhead = 24 * time.Hour

type wireBatch struct {
	Events []json.RawMessage `json:"events"`
}

type wireEvent struct {
	ID     string          `json:"id"`
	Type   string          `json:"type"`
	Detail json.RawMessage `json:"detail"`
}

type wireConversation struct {
	Kind        string `json:"kind"`
	ID          string `json:"id"`
	TeamspaceID string `json:"teamspace_id"`
}

type wireMessage struct {
	ID  string `json:"id"`
	Seq int64  `json:"seq"`
}

type wireAuthor struct {
	Kind    string `json:"kind"`
	UserID  string `json:"user_id"`
	AgentID string `json:"agent_id"`
}

type wireMessagePosted struct {
	OrganizationID     string            `json:"organization_id"`
	Conversation       *wireConversation `json:"conversation"`
	ParticipantUserIDs []string          `json:"participant_user_ids"`
	Message            *wireMessage      `json:"message"`
	Author             *wireAuthor       `json:"author"`
	PostedAt           string            `json:"posted_at"`
}

type wireConversationRead struct {
	OrganizationID string            `json:"organization_id"`
	Conversation   *wireConversation `json:"conversation"`
	ReaderUserID   string            `json:"reader_user_id"`
	ReadThroughSeq int64             `json:"read_through_seq"`
	ReadAt         string            `json:"read_at"`
}

// decode reads a batch and checks every event in it, as of now. It answers
// the events as realtime publishes them, rewritten from the contract's own
// types, so a field the Worker added and the contract does not name, which
// might be content, never reaches the bus. Any event realtime cannot publish
// fails the whole batch.
func decode(body []byte, now time.Time) ([]postgres.Received, error) {
	var batch wireBatch
	if err := json.Unmarshal(body, &batch); err != nil {
		return nil, fmt.Errorf("the body must be a JSON object holding events: %s", describe(err))
	}
	switch {
	case batch.Events == nil:
		return nil, errors.New("events is required")
	case len(batch.Events) == 0:
		return nil, errors.New("events holds no event")
	case len(batch.Events) > MaxEvents:
		return nil, fmt.Errorf("%w, and this one carries %d", errTooMany, len(batch.Events))
	}
	events := make([]postgres.Received, 0, len(batch.Events))
	for index, raw := range batch.Events {
		event, err := decodeEvent(raw, now)
		if err != nil {
			return nil, fmt.Errorf("events[%d]%s: %w", index, named(raw), err)
		}
		events = append(events, event)
	}
	return events, nil
}

// named is " (id)" for an event whose id can be read and shown, to find it
// by among the Worker's own.
func named(raw json.RawMessage) string {
	var event wireEvent
	if json.Unmarshal(raw, &event) != nil || !eventID.MatchString(event.ID) {
		return ""
	}
	return " (" + event.ID + ")"
}

func decodeEvent(raw json.RawMessage, now time.Time) (postgres.Received, error) {
	var event wireEvent
	if err := json.Unmarshal(raw, &event); err != nil {
		return postgres.Received{}, fmt.Errorf("an event must be an object with id, type and detail: %s", describe(err))
	}
	if !eventID.MatchString(event.ID) {
		return postgres.Received{}, errors.New("id must be 1 to 200 of A-Z a-z 0-9 _ -")
	}
	// Only what the Worker saw happen. A completed purge is realtime's own
	// to say, and the Worker cannot send one.
	if event.Type != realtimev1.MessagePostedDetailType && event.Type != realtimev1.ConversationReadDetailType {
		return postgres.Received{}, fmt.Errorf("type must be %s or %s", realtimev1.MessagePostedDetailType, realtimev1.ConversationReadDetailType)
	}
	if len(event.Detail) == 0 || string(event.Detail) == "null" {
		return postgres.Received{}, errors.New("detail is required")
	}
	var (
		detail     any
		occurredAt time.Time
		err        error
	)
	switch event.Type {
	case realtimev1.MessagePostedDetailType:
		var posted realtimev1.MessagePosted
		posted, err = messagePosted(event.Detail, now)
		detail, occurredAt = posted, posted.PostedAt
	default:
		var read realtimev1.ConversationRead
		read, err = conversationRead(event.Detail, now)
		detail, occurredAt = read, read.ReadAt
	}
	if err != nil {
		return postgres.Received{}, fmt.Errorf("detail: %w", err)
	}
	data, err := json.Marshal(detail)
	if err != nil {
		return postgres.Received{}, fmt.Errorf("detail: %w", err)
	}
	return postgres.Received{WorkerID: event.ID, DetailType: event.Type, Data: data, OccurredAt: occurredAt}, nil
}

func messagePosted(raw json.RawMessage, now time.Time) (realtimev1.MessagePosted, error) {
	var detail wireMessagePosted
	if err := json.Unmarshal(raw, &detail); err != nil {
		return realtimev1.MessagePosted{}, errors.New(describe(err))
	}
	conversation, participants, err := checkConversation(detail.OrganizationID, detail.Conversation)
	if err != nil {
		return realtimev1.MessagePosted{}, err
	}
	if len(detail.ParticipantUserIDs) != 2 || detail.ParticipantUserIDs[0] != participants[0] || detail.ParticipantUserIDs[1] != participants[1] {
		return realtimev1.MessagePosted{}, errors.New("participant_user_ids must be the conversation's two people, sorted, as its id names them")
	}
	if detail.Message == nil {
		return realtimev1.MessagePosted{}, errors.New("message is required")
	}
	if !opaqueID.MatchString(detail.Message.ID) {
		return realtimev1.MessagePosted{}, errors.New("message.id must be 1 to 200 printable ASCII characters, without spaces")
	}
	if detail.Message.Seq < 1 {
		return realtimev1.MessagePosted{}, errors.New("message.seq must be at least 1")
	}
	author, err := checkAuthor(detail.Author, participants)
	if err != nil {
		return realtimev1.MessagePosted{}, err
	}
	postedAt, err := checkTime("posted_at", detail.PostedAt, now)
	if err != nil {
		return realtimev1.MessagePosted{}, err
	}
	return realtimev1.MessagePosted{
		OrganizationID: detail.OrganizationID, Conversation: conversation, ParticipantUserIDs: participants[:],
		Message: realtimev1.Message{ID: detail.Message.ID, Seq: detail.Message.Seq}, Author: author, PostedAt: postedAt,
	}, nil
}

func conversationRead(raw json.RawMessage, now time.Time) (realtimev1.ConversationRead, error) {
	var detail wireConversationRead
	if err := json.Unmarshal(raw, &detail); err != nil {
		return realtimev1.ConversationRead{}, errors.New(describe(err))
	}
	conversation, participants, err := checkConversation(detail.OrganizationID, detail.Conversation)
	if err != nil {
		return realtimev1.ConversationRead{}, err
	}
	if detail.ReaderUserID != participants[0] && detail.ReaderUserID != participants[1] {
		return realtimev1.ConversationRead{}, errors.New("reader_user_id must be one of the conversation's two people")
	}
	if detail.ReadThroughSeq < 1 {
		return realtimev1.ConversationRead{}, errors.New("read_through_seq must be at least 1")
	}
	readAt, err := checkTime("read_at", detail.ReadAt, now)
	if err != nil {
		return realtimev1.ConversationRead{}, err
	}
	return realtimev1.ConversationRead{
		OrganizationID: detail.OrganizationID, Conversation: conversation,
		ReaderUserID: detail.ReaderUserID, ReadThroughSeq: detail.ReadThroughSeq, ReadAt: readAt,
	}, nil
}

// checkConversation reads a conversation's id as the web app builds it —
// "{organization}~{person}~{person}" for a direct conversation,
// "{organization}~{teamspace}~{person}~{person}" for a side one, the two
// people different and sorted — and checks that it is the organization's,
// and the teamspace's for a side conversation. It answers the conversation
// and its two people.
func checkConversation(organizationID string, conversation *wireConversation) (realtimev1.Conversation, [2]string, error) {
	var none [2]string
	if organizationID == "" {
		return realtimev1.Conversation{}, none, errors.New("organization_id is required")
	}
	if conversation == nil {
		return realtimev1.Conversation{}, none, errors.New("conversation is required")
	}
	segments := strings.Split(conversation.ID, "~")
	switch conversation.Kind {
	case realtimev1.ConversationDirect:
		if len(segments) != 3 {
			return realtimev1.Conversation{}, none, errors.New("conversation.id must be {organization}~{person}~{person} for a direct conversation")
		}
		if conversation.TeamspaceID != "" {
			return realtimev1.Conversation{}, none, errors.New("conversation.teamspace_id is only for a side conversation")
		}
	case realtimev1.ConversationSide:
		if len(segments) != 4 {
			return realtimev1.Conversation{}, none, errors.New("conversation.id must be {organization}~{teamspace}~{person}~{person} for a side conversation")
		}
		if conversation.TeamspaceID == "" {
			return realtimev1.Conversation{}, none, errors.New("conversation.teamspace_id is required for a side conversation")
		}
		if conversation.TeamspaceID != segments[1] {
			return realtimev1.Conversation{}, none, errors.New("conversation.teamspace_id must be the teamspace conversation.id names")
		}
	default:
		return realtimev1.Conversation{}, none, fmt.Errorf("conversation.kind must be %s or %s", realtimev1.ConversationDirect, realtimev1.ConversationSide)
	}
	for _, segment := range segments {
		if !nameID.MatchString(segment) {
			return realtimev1.Conversation{}, none, errors.New("conversation.id's ids must each be 1 to 128 of A-Z a-z 0-9 _ -")
		}
	}
	if segments[0] != organizationID {
		return realtimev1.Conversation{}, none, errors.New("conversation.id must start with organization_id")
	}
	people := [2]string{segments[len(segments)-2], segments[len(segments)-1]}
	if people[0] >= people[1] {
		return realtimev1.Conversation{}, none, errors.New("conversation.id must end with two different people, sorted")
	}
	return realtimev1.Conversation{Kind: conversation.Kind, ID: conversation.ID, TeamspaceID: conversation.TeamspaceID}, people, nil
}

// checkAuthor checks who wrote a message: one of the conversation's two
// people, or an agent of theirs, named by its id.
func checkAuthor(author *wireAuthor, people [2]string) (realtimev1.Author, error) {
	if author == nil {
		return realtimev1.Author{}, errors.New("author is required")
	}
	if author.UserID != people[0] && author.UserID != people[1] {
		return realtimev1.Author{}, errors.New("author.user_id must be one of the conversation's two people")
	}
	switch author.Kind {
	case realtimev1.AuthorMember:
		if author.AgentID != "" {
			return realtimev1.Author{}, errors.New("author.agent_id is only for an agent's message")
		}
	case realtimev1.AuthorAgent:
		if !opaqueID.MatchString(author.AgentID) {
			return realtimev1.Author{}, errors.New("author.agent_id is required for an agent's message, 1 to 200 printable ASCII characters, without spaces")
		}
	default:
		return realtimev1.Author{}, fmt.Errorf("author.kind must be %s or %s", realtimev1.AuthorMember, realtimev1.AuthorAgent)
	}
	return realtimev1.Author{Kind: author.Kind, UserID: author.UserID, AgentID: author.AgentID}, nil
}

// checkTime reads when something happened: RFC 3339 in UTC, from earliest
// on, and no more than maxAhead ahead of now.
func checkTime(field, value string, now time.Time) (time.Time, error) {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if !utcTime.MatchString(value) || err != nil {
		return time.Time{}, fmt.Errorf("%s must be an RFC 3339 time in UTC, such as 2026-09-24T10:00:00.000Z", field)
	}
	if parsed.Before(earliest) {
		return time.Time{}, fmt.Errorf("%s is before %d", field, earliest.Year())
	}
	if parsed.After(now.Add(maxAhead)) {
		return time.Time{}, fmt.Errorf("%s is more than a day ahead of this server's clock", field)
	}
	return parsed.UTC(), nil
}

// describe says why JSON would not decode, in the contract's terms rather
// than Go's.
func describe(err error) string {
	var typeError *json.UnmarshalTypeError
	if errors.As(err, &typeError) {
		want := "another type"
		switch typeError.Type.Kind() {
		case reflect.String:
			want = "a string"
		case reflect.Int64:
			want = "a whole number"
		case reflect.Struct, reflect.Pointer:
			want = "an object"
		case reflect.Slice:
			want = "an array"
		}
		value, _, _ := strings.Cut(typeError.Value, " ")
		if typeError.Field == "" {
			return fmt.Sprintf("it is a JSON %s, not %s", value, want)
		}
		return fmt.Sprintf("%s must be %s, not a JSON %s", typeError.Field, want, value)
	}
	var syntaxError *json.SyntaxError
	if errors.As(err, &syntaxError) {
		return "it is not JSON"
	}
	return err.Error()
}
