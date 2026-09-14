package actionflow

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	integrationevents "github.com/Shubham04-oss/oryxa-go/contracts/events/integrations/v1"
)

func mapGoogle(request MapRequest) (MappedResult, error) {
	switch request.ActionName {
	case "send-email", "send-draft-email", "reply-email":
		return mapGoogleGmailSent(request)
	case "forward-email":
		return mapGoogleGmailForwarded(request)
	case "create-draft-email":
		return mapGoogleGmailDraftCreated(request)
	case "update-draft-email", "delete-draft-email":
		return mapGoogleGmailDraftMutated(request)
	case "add-label-to-email", "remove-label-from-email", "archive-email", "create-label":
		return mapGoogleGmailMutated(request)
	case "get-draft-email", "list-drafts", "get-thread", "list-threads", "list-labels", "get-label",
		"get-profile", "list-history", "get-email-attachment":
		return mapGoogleGmailLookup(request)
	case "get-email":
		return mapGoogleGmailMessage(request.Output)
	case "find-email":
		return mapGoogleGmailSearch(request.Output)
	case "gmail-custom-api-call", "calendar-custom-api-call", "drive-custom-api-call":
		return mapProviderHTTPCall(request)
	case "create-calendar-event", "create-quick-event", "import-calendar-event":
		return mapGoogleCalendarWrite(request, true)
	case "update-calendar-event", "add-calendar-attendees", "remove-calendar-attendee", "move-calendar-event":
		return mapGoogleCalendarWrite(request, false)
	case "delete-calendar-event":
		return mapGoogleCalendarDeleted(request)
	case "list-calendar-events", "search-calendar-events", "list-recurring-event-instances":
		return mapGoogleCalendarListed(request)
	case "get-calendar-event":
		return mapGoogleCalendarGet(request)
	case "find-busy-periods":
		return mapGoogleCalendarBusy(request)
	case "find-free-slots":
		return mapGoogleCalendarFreeSlots(request)
	case "list-calendars", "get-calendar", "get-calendar-colors", "list-calendar-settings":
		return mapGoogleCalendarLookup(request)
	case "list-drive-files", "search-drive-files":
		return mapGoogleDriveFiles(request)
	case "get-drive-file-metadata":
		return mapGoogleDriveFile(request, "")
	case "create-drive-folder", "create-shared-drive", "upload-drive-file":
		return mapGoogleDriveFile(request, "created")
	case "copy-drive-file":
		return mapGoogleDriveFile(request, "copied")
	case "create-file-from-text", "save-file-as-pdf", "upload-from-url":
		return mapGoogleDriveFile(request, "created")
	case "move-drive-file", "update-drive-file-metadata", "trash-drive-file", "untrash-drive-file",
		"delete-drive-file", "share-drive-file", "update-drive-permission", "remove-drive-permission",
		"update-shared-drive", "create-drive-comment", "create-drive-reply", "update-drive-reply",
		"delete-drive-reply", "replace-drive-file":
		return mapGoogleDriveFile(request, "mutated")
	case "set-drive-public-access":
		return mapGoogleDrivePublicAccess(request)
	case "empty-trash", "delete-shared-drive":
		return mapGoogleDriveDriveMutation(request)
	case "list-drive-permissions", "list-shared-drives", "get-shared-drive", "list-drive-comments",
		"get-drive-reply", "list-drive-replies", "get-drive-about":
		return mapGoogleDriveLookup(request)
	case "download-drive-file", "export-drive-file":
		return mapStoredFileLookup(request.Output)
	}
	if result, ok, err := mapGoogleDocsAction(request); ok {
		return result, err
	}
	if result, ok, err := mapGoogleSheetsAction(request); ok {
		return result, err
	}
	return MappedResult{}, fmt.Errorf("%w: unsupported Google action", ErrPieceOutputInvalid)
}

type gmailIdentity struct {
	ID       string `json:"id"`
	ThreadID string `json:"threadId"`
	DraftID  string `json:"draftId"`
	Message  struct {
		ID       string `json:"id"`
		ThreadID string `json:"threadId"`
	} `json:"message"`
	Data struct {
		ID       string `json:"id"`
		ThreadID string `json:"threadId"`
	} `json:"data"`
}

func decodeGmailIdentity(raw json.RawMessage) gmailIdentity {
	var value gmailIdentity
	_ = json.Unmarshal(raw, &value)
	if value.ID == "" {
		value.ID = value.Data.ID
	}
	if value.ThreadID == "" {
		value.ThreadID = value.Data.ThreadID
	}
	if value.Message.ID == "" {
		value.Message.ID = value.ID
	}
	if value.Message.ThreadID == "" {
		value.Message.ThreadID = value.ThreadID
	}
	return value
}

func mapGoogleGmailSent(request MapRequest) (MappedResult, error) {
	value := decodeGmailIdentity(request.Output)
	var props struct {
		MessageID string `json:"message_id"`
		DraftID   string `json:"draft_id"`
	}
	_ = json.Unmarshal(request.Props, &props)
	id := value.ID
	if id == "" {
		id = value.Message.ID
	}
	if id == "" {
		id = props.MessageID
	}
	if id == "" && props.DraftID != "" {
		id = props.DraftID
	}
	if id == "" {
		return MappedResult{}, ErrPieceOutputInvalid
	}
	result, err := json.Marshal(struct {
		MessageID string `json:"message_id"`
		ThreadID  string `json:"thread_id,omitempty"`
	}{MessageID: id, ThreadID: value.ThreadID})
	if err != nil {
		return MappedResult{}, ErrPieceOutputInvalid
	}
	return MappedResult{SafeResult: result, DetailType: integrationevents.GoogleGmailMessageSentDetailType,
		Data: integrationevents.GoogleGmailMessageSent{
			ExecutionID: request.Execution.ID, ConnectionID: request.Connection.ID,
			MessageID: id, ThreadID: value.ThreadID,
			RequestedBy: request.Execution.MemberID, SentAt: request.OccurredAt,
		}}, nil
}

func mapGoogleGmailForwarded(request MapRequest) (MappedResult, error) {
	mapped, err := mapGoogleGmailSent(request)
	if err != nil {
		return MappedResult{}, err
	}
	sent := mapped.Data.(integrationevents.GoogleGmailMessageSent)
	mapped.DetailType = integrationevents.GoogleGmailEmailForwardedDetailType
	mapped.Data = integrationevents.GoogleGmailEmailForwarded{
		ExecutionID: sent.ExecutionID, ConnectionID: sent.ConnectionID,
		MessageID: sent.MessageID, ThreadID: sent.ThreadID,
		RequestedBy: sent.RequestedBy, ForwardedAt: request.OccurredAt,
	}
	return mapped, nil
}

func mapGoogleGmailDraftCreated(request MapRequest) (MappedResult, error) {
	value := decodeGmailIdentity(request.Output)
	if value.ID == "" {
		return MappedResult{}, ErrPieceOutputInvalid
	}
	result, err := json.Marshal(struct {
		DraftID   string `json:"draft_id"`
		MessageID string `json:"message_id,omitempty"`
		ThreadID  string `json:"thread_id,omitempty"`
	}{DraftID: value.ID, MessageID: value.Message.ID, ThreadID: value.Message.ThreadID})
	if err != nil {
		return MappedResult{}, ErrPieceOutputInvalid
	}
	return MappedResult{SafeResult: result, DetailType: integrationevents.GoogleGmailDraftCreatedDetailType,
		Data: integrationevents.GoogleGmailDraftCreated{
			ExecutionID: request.Execution.ID, ConnectionID: request.Connection.ID,
			DraftID: value.ID, MessageID: value.Message.ID,
			RequestedBy: request.Execution.MemberID, CreatedAt: request.OccurredAt,
		}}, nil
}

func mapGoogleGmailDraftMutated(request MapRequest) (MappedResult, error) {
	value := decodeGmailIdentity(request.Output)
	var props struct {
		DraftID string `json:"draft_id"`
	}
	_ = json.Unmarshal(request.Props, &props)
	draftID := value.ID
	if draftID == "" {
		draftID = value.DraftID
	}
	if draftID == "" {
		draftID = props.DraftID
	}
	if draftID == "" {
		return MappedResult{}, ErrPieceOutputInvalid
	}
	result, err := json.Marshal(struct {
		DraftID   string `json:"draft_id"`
		MessageID string `json:"message_id,omitempty"`
	}{DraftID: draftID, MessageID: value.Message.ID})
	if err != nil {
		return MappedResult{}, ErrPieceOutputInvalid
	}
	return MappedResult{SafeResult: result, DetailType: integrationevents.GoogleGmailEmailMutatedDetailType,
		Data: integrationevents.GoogleGmailEmailMutated{
			ExecutionID: request.Execution.ID, ConnectionID: request.Connection.ID,
			DraftID: draftID, MessageID: value.Message.ID,
			RequestedBy: request.Execution.MemberID, MutatedAt: request.OccurredAt,
		}}, nil
}

func mapGoogleGmailMutated(request MapRequest) (MappedResult, error) {
	value := decodeGmailIdentity(request.Output)
	var props struct {
		MessageID string `json:"message_id"`
	}
	_ = json.Unmarshal(request.Props, &props)
	id := value.ID
	if id == "" {
		id = props.MessageID
	}
	if id == "" {
		return MappedResult{}, ErrPieceOutputInvalid
	}
	result, err := json.Marshal(struct {
		ID       string `json:"id"`
		ThreadID string `json:"thread_id,omitempty"`
	}{ID: id, ThreadID: value.ThreadID})
	if err != nil {
		return MappedResult{}, ErrPieceOutputInvalid
	}
	return MappedResult{SafeResult: result, DetailType: integrationevents.GoogleGmailEmailMutatedDetailType,
		Data: integrationevents.GoogleGmailEmailMutated{
			ExecutionID: request.Execution.ID, ConnectionID: request.Connection.ID, MessageID: id,
			RequestedBy: request.Execution.MemberID, MutatedAt: request.OccurredAt,
		}}, nil
}

func mapGoogleGmailMessage(raw json.RawMessage) (MappedResult, error) {
	safe, err := projectGmailMessage(raw)
	if err != nil {
		return MappedResult{}, err
	}
	return googleJSON(safe)
}

func mapGoogleGmailSearch(raw json.RawMessage) (MappedResult, error) {
	var output struct {
		Found   bool `json:"found"`
		Results struct {
			Messages []json.RawMessage `json:"messages"`
			Count    int               `json:"count"`
		} `json:"results"`
	}
	if json.Unmarshal(raw, &output) != nil {
		return MappedResult{}, ErrPieceOutputInvalid
	}
	if output.Results.Messages == nil {
		output.Results.Messages = []json.RawMessage{}
	}
	truncated := len(output.Results.Messages) > 100
	if truncated {
		output.Results.Messages = output.Results.Messages[:100]
	}
	messages := make([]any, 0, len(output.Results.Messages))
	for _, item := range output.Results.Messages {
		safe, err := projectGmailMessage(item)
		if err != nil {
			return MappedResult{}, err
		}
		messages = append(messages, safe)
	}
	count := output.Results.Count
	if count == 0 {
		count = len(messages)
	}
	return googleJSON(struct {
		Found     bool  `json:"found"`
		Messages  []any `json:"messages"`
		Count     int   `json:"count"`
		Truncated bool  `json:"truncated"`
	}{Found: output.Found, Messages: messages, Count: count, Truncated: truncated})
}

type gmailAddressField struct {
	Text  string `json:"text"`
	Value []struct {
		Address string `json:"address"`
		Name    string `json:"name"`
	} `json:"value"`
}

func projectGmailMessage(raw json.RawMessage) (any, error) {
	var output struct {
		ID          string            `json:"id"`
		Subject     string            `json:"subject"`
		Date        string            `json:"date"`
		From        json.RawMessage   `json:"from"`
		To          json.RawMessage   `json:"to"`
		Attachments []json.RawMessage `json:"attachments"`
		Error       string            `json:"error"`
	}
	if json.Unmarshal(raw, &output) != nil || output.ID == "" {
		return nil, ErrPieceOutputInvalid
	}
	if output.Error != "" {
		return struct {
			ID    string `json:"id"`
			Error string `json:"error,omitempty"`
		}{ID: output.ID, Error: output.Error}, nil
	}
	attachments := make([]any, 0, len(output.Attachments))
	for _, item := range output.Attachments {
		filename, fileID := writtenOryxaFile(item)
		if fileID == "" {
			return nil, ErrPieceOutputInvalid
		}
		var leftover struct {
			Content string `json:"content"`
			Text    string `json:"text"`
			HTML    string `json:"html"`
		}
		_ = json.Unmarshal(item, &leftover)
		if leftover.Content != "" || leftover.Text != "" || leftover.HTML != "" {
			return nil, ErrPieceOutputInvalid
		}
		var nested struct {
			Data json.RawMessage `json:"data"`
		}
		_ = json.Unmarshal(item, &nested)
		if len(nested.Data) > 0 && nested.Data[0] != '{' {
			return nil, ErrPieceOutputInvalid
		}
		attachments = append(attachments, struct {
			FileID   string `json:"file_id"`
			Filename string `json:"filename,omitempty"`
		}{FileID: fileID, Filename: filename})
	}
	return struct {
		ID          string `json:"id"`
		Subject     string `json:"subject,omitempty"`
		From        string `json:"from,omitempty"`
		To          string `json:"to,omitempty"`
		Date        string `json:"date,omitempty"`
		Attachments []any  `json:"attachments,omitempty"`
	}{ID: output.ID, Subject: output.Subject, From: gmailAddressText(output.From),
		To: gmailAddressText(output.To), Date: output.Date, Attachments: attachments}, nil
}

func gmailAddressText(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var asString string
	if json.Unmarshal(raw, &asString) == nil {
		return asString
	}
	var field gmailAddressField
	if json.Unmarshal(raw, &field) != nil {
		return ""
	}
	if field.Text != "" {
		return field.Text
	}
	if len(field.Value) > 0 {
		return firstNonEmpty(field.Value[0].Address, field.Value[0].Name)
	}
	return ""
}

func mapGoogleGmailLookup(request MapRequest) (MappedResult, error) {
	switch request.ActionName {
	case "get-draft-email":
		value := decodeGmailIdentity(request.Output)
		if value.ID == "" {
			return MappedResult{}, ErrPieceOutputInvalid
		}
		return googleJSON(struct {
			DraftID   string `json:"draft_id"`
			MessageID string `json:"message_id,omitempty"`
			ThreadID  string `json:"thread_id,omitempty"`
		}{DraftID: value.ID, MessageID: value.Message.ID, ThreadID: value.Message.ThreadID})
	case "list-drafts":
		var output struct {
			Drafts []gmailIdentity `json:"drafts"`
			Count  int             `json:"count"`
		}
		if json.Unmarshal(request.Output, &output) != nil {
			return MappedResult{}, ErrPieceOutputInvalid
		}
		if output.Drafts == nil {
			output.Drafts = []gmailIdentity{}
		}
		items, truncated := googleCap(output.Drafts, 100)
		safe := make([]any, 0, len(items))
		for _, item := range items {
			if item.ID == "" {
				return MappedResult{}, ErrPieceOutputInvalid
			}
			safe = append(safe, struct {
				DraftID   string `json:"draft_id"`
				MessageID string `json:"message_id,omitempty"`
				ThreadID  string `json:"thread_id,omitempty"`
			}{DraftID: item.ID, MessageID: item.Message.ID, ThreadID: item.Message.ThreadID})
		}
		return googleJSON(struct {
			Drafts    []any `json:"drafts"`
			Truncated bool  `json:"truncated"`
		}{Drafts: safe, Truncated: truncated})
	case "get-thread":
		var output struct {
			ID       string `json:"id"`
			Messages []struct {
				ID       string   `json:"id"`
				ThreadID string   `json:"threadId"`
				LabelIDs []string `json:"labelIds"`
			} `json:"messages"`
		}
		if json.Unmarshal(unwrapGoogleHTTP(request.Output), &output) != nil || output.ID == "" {
			return MappedResult{}, ErrPieceOutputInvalid
		}
		items, truncated := googleCap(output.Messages, 100)
		safe := make([]any, 0, len(items))
		for _, item := range items {
			if item.ID == "" {
				return MappedResult{}, ErrPieceOutputInvalid
			}
			safe = append(safe, struct {
				ID       string   `json:"id"`
				ThreadID string   `json:"thread_id,omitempty"`
				LabelIDs []string `json:"label_ids,omitempty"`
			}{ID: item.ID, ThreadID: item.ThreadID, LabelIDs: item.LabelIDs})
		}
		return googleJSON(struct {
			ID        string `json:"id"`
			Messages  []any  `json:"messages"`
			Truncated bool   `json:"truncated"`
		}{ID: output.ID, Messages: safe, Truncated: truncated})
	case "list-threads":
		var output struct {
			Threads []struct {
				ID        string `json:"id"`
				HistoryID string `json:"historyId"`
			} `json:"threads"`
		}
		if json.Unmarshal(request.Output, &output) != nil {
			return MappedResult{}, ErrPieceOutputInvalid
		}
		if output.Threads == nil {
			output.Threads = []struct {
				ID        string `json:"id"`
				HistoryID string `json:"historyId"`
			}{}
		}
		items, truncated := googleCap(output.Threads, 100)
		safe := make([]any, 0, len(items))
		for _, item := range items {
			if item.ID == "" {
				return MappedResult{}, ErrPieceOutputInvalid
			}
			safe = append(safe, struct {
				ID        string `json:"id"`
				HistoryID string `json:"history_id,omitempty"`
			}{ID: item.ID, HistoryID: item.HistoryID})
		}
		return googleJSON(struct {
			Threads   []any `json:"threads"`
			Truncated bool  `json:"truncated"`
		}{Threads: safe, Truncated: truncated})
	case "list-labels":
		var output struct {
			Labels []struct {
				ID   string `json:"id"`
				Name string `json:"name"`
				Type string `json:"type"`
			} `json:"labels"`
		}
		if json.Unmarshal(request.Output, &output) != nil {
			return MappedResult{}, ErrPieceOutputInvalid
		}
		if output.Labels == nil {
			output.Labels = []struct {
				ID   string `json:"id"`
				Name string `json:"name"`
				Type string `json:"type"`
			}{}
		}
		items, truncated := googleCap(output.Labels, 100)
		for _, item := range items {
			if item.ID == "" {
				return MappedResult{}, ErrPieceOutputInvalid
			}
		}
		return googleJSON(struct {
			Labels    any  `json:"labels"`
			Truncated bool `json:"truncated"`
		}{Labels: items, Truncated: truncated})
	case "get-label":
		var output struct {
			ID   string `json:"id"`
			Name string `json:"name"`
			Type string `json:"type"`
		}
		if json.Unmarshal(request.Output, &output) != nil || output.ID == "" {
			return MappedResult{}, ErrPieceOutputInvalid
		}
		return googleJSON(output)
	case "get-profile":
		var output struct {
			EmailAddress  string `json:"emailAddress"`
			MessagesTotal int64  `json:"messagesTotal"`
			ThreadsTotal  int64  `json:"threadsTotal"`
			HistoryID     string `json:"historyId"`
		}
		if json.Unmarshal(request.Output, &output) != nil || output.EmailAddress == "" {
			return MappedResult{}, ErrPieceOutputInvalid
		}
		return googleJSON(struct {
			EmailAddress  string `json:"email_address"`
			MessagesTotal int64  `json:"messages_total"`
			ThreadsTotal  int64  `json:"threads_total"`
			HistoryID     string `json:"history_id,omitempty"`
		}{EmailAddress: output.EmailAddress, MessagesTotal: output.MessagesTotal,
			ThreadsTotal: output.ThreadsTotal, HistoryID: output.HistoryID})
	case "list-history":
		var output struct {
			History []struct {
				ID       string `json:"id"`
				Messages []struct {
					ID       string `json:"id"`
					ThreadID string `json:"threadId"`
				} `json:"messages"`
			} `json:"history"`
			HistoryID string `json:"historyId"`
		}
		if json.Unmarshal(request.Output, &output) != nil {
			return MappedResult{}, ErrPieceOutputInvalid
		}
		if output.History == nil {
			output.History = []struct {
				ID       string `json:"id"`
				Messages []struct {
					ID       string `json:"id"`
					ThreadID string `json:"threadId"`
				} `json:"messages"`
			}{}
		}
		items, truncated := googleCap(output.History, 100)
		safe := make([]any, 0, len(items))
		for _, item := range items {
			if item.ID == "" {
				return MappedResult{}, ErrPieceOutputInvalid
			}
			messages := make([]any, 0, len(item.Messages))
			for _, message := range item.Messages {
				if message.ID == "" {
					continue
				}
				messages = append(messages, struct {
					ID       string `json:"id"`
					ThreadID string `json:"thread_id,omitempty"`
				}{ID: message.ID, ThreadID: message.ThreadID})
			}
			safe = append(safe, struct {
				ID       string `json:"id"`
				Messages []any  `json:"messages,omitempty"`
			}{ID: item.ID, Messages: messages})
		}
		return googleJSON(struct {
			History   []any  `json:"history"`
			HistoryID string `json:"history_id,omitempty"`
			Truncated bool   `json:"truncated"`
		}{History: safe, HistoryID: output.HistoryID, Truncated: truncated})
	case "get-email-attachment":
		return mapStoredFileLookup(request.Output)
	default:
		return MappedResult{}, fmt.Errorf("%w: unsupported Google action", ErrPieceOutputInvalid)
	}
}

type googleCalendarEvent struct {
	ID         string `json:"id"`
	Status     string `json:"status"`
	Summary    string `json:"summary"`
	HTMLLink   string `json:"htmlLink"`
	Created    string `json:"created"`
	Updated    string `json:"updated"`
	CalendarID string `json:"calendarId"`
	Start      struct {
		Date     string `json:"date"`
		DateTime string `json:"dateTime"`
		TimeZone string `json:"timeZone"`
	} `json:"start"`
	End struct {
		Date     string `json:"date"`
		DateTime string `json:"dateTime"`
		TimeZone string `json:"timeZone"`
	} `json:"end"`
}

func decodeGoogleCalendarEvent(raw json.RawMessage) googleCalendarEvent {
	payload := unwrapGoogleHTTP(raw)
	var event googleCalendarEvent
	_ = json.Unmarshal(payload, &event)
	if event.ID != "" {
		return event
	}
	var wrapped struct {
		Event googleCalendarEvent `json:"event"`
		Body  googleCalendarEvent `json:"body"`
		Data  googleCalendarEvent `json:"data"`
	}
	_ = json.Unmarshal(payload, &wrapped)
	if wrapped.Event.ID != "" {
		return wrapped.Event
	}
	if wrapped.Body.ID != "" {
		return wrapped.Body
	}
	return wrapped.Data
}

func mapGoogleCalendarWrite(request MapRequest, created bool) (MappedResult, error) {
	event := decodeGoogleCalendarEvent(request.Output)
	var props struct {
		CalendarID            string `json:"calendar_id"`
		DestinationCalendarID string `json:"destination_calendar_id"`
	}
	_ = json.Unmarshal(request.Props, &props)
	calendarID := props.CalendarID
	if props.DestinationCalendarID != "" {
		calendarID = props.DestinationCalendarID
	}
	if event.CalendarID != "" {
		calendarID = event.CalendarID
	}
	if event.ID == "" || calendarID == "" {
		return MappedResult{}, ErrPieceOutputInvalid
	}
	result, err := json.Marshal(safeGoogleCalendarEvent(event))
	if err != nil {
		return MappedResult{}, ErrPieceOutputInvalid
	}
	if created {
		return MappedResult{SafeResult: result, DetailType: integrationevents.GoogleCalendarEventCreatedDetailType,
			Data: integrationevents.GoogleCalendarEventCreated{
				ExecutionID: request.Execution.ID, ConnectionID: request.Connection.ID,
				CalendarID: calendarID, EventID: event.ID, URL: event.HTMLLink,
				RequestedBy: request.Execution.MemberID, CreatedAt: request.OccurredAt,
			}}, nil
	}
	return MappedResult{SafeResult: result, DetailType: integrationevents.GoogleCalendarEventUpdatedDetailType,
		Data: integrationevents.GoogleCalendarEventUpdated{
			ExecutionID: request.Execution.ID, ConnectionID: request.Connection.ID,
			CalendarID: calendarID, EventID: event.ID, URL: event.HTMLLink,
			RequestedBy: request.Execution.MemberID, UpdatedAt: request.OccurredAt,
		}}, nil
}

func mapGoogleCalendarDeleted(request MapRequest) (MappedResult, error) {
	if !googleEmptyOutput(request.Output) {
		event := decodeGoogleCalendarEvent(request.Output)
		if event.ID == "" {
			return MappedResult{}, ErrPieceOutputInvalid
		}
	}
	var props struct {
		CalendarID string `json:"calendar_id"`
		EventID    string `json:"event_id"`
	}
	if json.Unmarshal(request.Props, &props) != nil || props.CalendarID == "" || props.EventID == "" {
		return MappedResult{}, ErrPieceOutputInvalid
	}
	result, err := json.Marshal(struct {
		ID         string `json:"id"`
		CalendarID string `json:"calendar_id"`
	}{ID: props.EventID, CalendarID: props.CalendarID})
	if err != nil {
		return MappedResult{}, ErrPieceOutputInvalid
	}
	return MappedResult{SafeResult: result, DetailType: integrationevents.GoogleCalendarEventDeletedDetailType,
		Data: integrationevents.GoogleCalendarEventDeleted{
			ExecutionID: request.Execution.ID, ConnectionID: request.Connection.ID,
			CalendarID: props.CalendarID, EventID: props.EventID,
			RequestedBy: request.Execution.MemberID, DeletedAt: request.OccurredAt,
		}}, nil
}

func mapGoogleCalendarListed(request MapRequest) (MappedResult, error) {
	payload := unwrapGoogleHTTP(request.Output)
	var output struct {
		Items     []googleCalendarEvent `json:"items"`
		Events    []googleCalendarEvent `json:"events"`
		Instances []googleCalendarEvent `json:"instances"`
		Body      struct {
			Items []googleCalendarEvent `json:"items"`
		} `json:"body"`
	}
	if json.Unmarshal(payload, &output) != nil {
		return MappedResult{}, ErrPieceOutputInvalid
	}
	items := output.Items
	if items == nil {
		items = output.Events
	}
	if items == nil {
		items = output.Instances
	}
	if items == nil {
		items = output.Body.Items
	}
	if items == nil {
		items = []googleCalendarEvent{}
	}
	truncated := false
	items, truncated = googleCap(items, 100)
	safe := make([]any, 0, len(items))
	for _, item := range items {
		if item.ID == "" {
			return MappedResult{}, ErrPieceOutputInvalid
		}
		safe = append(safe, safeGoogleCalendarEvent(item))
	}
	return googleJSON(struct {
		Items     []any `json:"items"`
		Truncated bool  `json:"truncated"`
	}{Items: safe, Truncated: truncated})
}

func mapGoogleCalendarGet(request MapRequest) (MappedResult, error) {
	event := decodeGoogleCalendarEvent(request.Output)
	if event.ID == "" {
		return MappedResult{}, ErrPieceOutputInvalid
	}
	return googleJSON(safeGoogleCalendarEvent(event))
}

func mapGoogleCalendarBusy(request MapRequest) (MappedResult, error) {
	var output struct {
		Calendars map[string]struct {
			Busy []struct {
				Start string `json:"start"`
				End   string `json:"end"`
			} `json:"busy"`
		} `json:"calendars"`
	}
	if json.Unmarshal(unwrapGoogleHTTP(request.Output), &output) != nil || output.Calendars == nil {
		return MappedResult{}, ErrPieceOutputInvalid
	}
	safe := make(map[string]any, len(output.Calendars))
	for id, calendar := range output.Calendars {
		busy := calendar.Busy
		if busy == nil {
			busy = []struct {
				Start string `json:"start"`
				End   string `json:"end"`
			}{}
		}
		safe[id] = struct {
			Busy any `json:"busy"`
		}{Busy: busy}
	}
	return googleJSON(struct {
		Calendars map[string]any `json:"calendars"`
	}{Calendars: safe})
}

func mapGoogleCalendarFreeSlots(request MapRequest) (MappedResult, error) {
	var output struct {
		FreeSlots []struct {
			Start           string `json:"start"`
			End             string `json:"end"`
			DurationMinutes int    `json:"duration_minutes"`
		} `json:"free_slots"`
	}
	if json.Unmarshal(request.Output, &output) != nil {
		return MappedResult{}, ErrPieceOutputInvalid
	}
	if output.FreeSlots == nil {
		output.FreeSlots = []struct {
			Start           string `json:"start"`
			End             string `json:"end"`
			DurationMinutes int    `json:"duration_minutes"`
		}{}
	}
	items, truncated := googleCap(output.FreeSlots, 100)
	return googleJSON(struct {
		FreeSlots []any `json:"free_slots"`
		Truncated bool  `json:"truncated"`
	}{FreeSlots: toAny(items), Truncated: truncated})
}

func mapGoogleCalendarLookup(request MapRequest) (MappedResult, error) {
	payload := unwrapGoogleHTTP(request.Output)
	switch request.ActionName {
	case "list-calendars":
		var output struct {
			Calendars []struct {
				ID         string `json:"id"`
				Summary    string `json:"summary"`
				TimeZone   string `json:"timeZone"`
				AccessRole string `json:"accessRole"`
				Primary    bool   `json:"primary"`
			} `json:"calendars"`
		}
		if json.Unmarshal(payload, &output) != nil {
			return MappedResult{}, ErrPieceOutputInvalid
		}
		if output.Calendars == nil {
			output.Calendars = []struct {
				ID         string `json:"id"`
				Summary    string `json:"summary"`
				TimeZone   string `json:"timeZone"`
				AccessRole string `json:"accessRole"`
				Primary    bool   `json:"primary"`
			}{}
		}
		items, truncated := googleCap(output.Calendars, 100)
		for _, item := range items {
			if item.ID == "" {
				return MappedResult{}, ErrPieceOutputInvalid
			}
		}
		return googleJSON(struct {
			Calendars any  `json:"calendars"`
			Truncated bool `json:"truncated"`
		}{Calendars: items, Truncated: truncated})
	case "get-calendar":
		var output struct {
			ID         string `json:"id"`
			Summary    string `json:"summary"`
			TimeZone   string `json:"timeZone"`
			AccessRole string `json:"accessRole"`
			Primary    bool   `json:"primary"`
		}
		if json.Unmarshal(payload, &output) != nil || output.ID == "" {
			return MappedResult{}, ErrPieceOutputInvalid
		}
		return googleJSON(struct {
			ID         string `json:"id"`
			Summary    string `json:"summary,omitempty"`
			TimeZone   string `json:"time_zone,omitempty"`
			AccessRole string `json:"access_role,omitempty"`
			Primary    bool   `json:"primary"`
		}{ID: output.ID, Summary: output.Summary, TimeZone: output.TimeZone,
			AccessRole: output.AccessRole, Primary: output.Primary})
	case "get-calendar-colors":
		var output map[string]any
		if json.Unmarshal(payload, &output) != nil || output == nil {
			return MappedResult{}, ErrPieceOutputInvalid
		}
		delete(output, "kind")
		delete(output, "etag")
		return googleJSON(output)
	case "list-calendar-settings":
		var output struct {
			Settings map[string]string `json:"settings"`
			Items    []struct {
				ID    string `json:"id"`
				Value string `json:"value"`
			} `json:"items"`
		}
		if json.Unmarshal(payload, &output) != nil {
			return MappedResult{}, ErrPieceOutputInvalid
		}
		if output.Settings == nil {
			output.Settings = map[string]string{}
			for _, item := range output.Items {
				if item.ID != "" {
					output.Settings[item.ID] = item.Value
				}
			}
		}
		return googleJSON(struct {
			Settings map[string]string `json:"settings"`
		}{Settings: output.Settings})
	default:
		return MappedResult{}, fmt.Errorf("%w: unsupported Google action", ErrPieceOutputInvalid)
	}
}

func safeGoogleCalendarEvent(value googleCalendarEvent) any {
	return struct {
		ID         string `json:"id"`
		Status     string `json:"status,omitempty"`
		Summary    string `json:"summary,omitempty"`
		URL        string `json:"url,omitempty"`
		CalendarID string `json:"calendar_id,omitempty"`
		Start      string `json:"start,omitempty"`
		End        string `json:"end,omitempty"`
		TimeZone   string `json:"time_zone,omitempty"`
		Updated    string `json:"updated,omitempty"`
	}{ID: value.ID, Status: value.Status, Summary: value.Summary, URL: value.HTMLLink,
		CalendarID: value.CalendarID, Start: dateOrTime(value.Start.DateTime, value.Start.Date),
		End: dateOrTime(value.End.DateTime, value.End.Date), TimeZone: value.Start.TimeZone,
		Updated: validProviderTime(value.Updated)}
}

type googleDriveFile struct {
	ID           string   `json:"id"`
	Name         string   `json:"name"`
	MimeType     string   `json:"mimeType"`
	Trashed      bool     `json:"trashed"`
	Parents      []string `json:"parents"`
	WebViewLink  string   `json:"webViewLink"`
	ModifiedTime string   `json:"modifiedTime"`
}

func safeGoogleDriveFile(value googleDriveFile) googleDriveFile {
	value.ModifiedTime = validProviderTime(value.ModifiedTime)
	return value
}

func decodeGoogleDriveFile(raw json.RawMessage) googleDriveFile {
	var file googleDriveFile
	_ = json.Unmarshal(unwrapGoogleHTTP(raw), &file)
	return file
}

func mapGoogleDriveFiles(request MapRequest) (MappedResult, error) {
	payload := unwrapGoogleHTTP(request.Output)
	var files []googleDriveFile
	if len(bytes.TrimSpace(payload)) > 0 && bytes.TrimSpace(payload)[0] == '[' {
		if json.Unmarshal(payload, &files) != nil {
			return MappedResult{}, ErrPieceOutputInvalid
		}
	} else {
		var output struct {
			Files []googleDriveFile `json:"files"`
		}
		if json.Unmarshal(payload, &output) != nil {
			return MappedResult{}, ErrPieceOutputInvalid
		}
		files = output.Files
	}
	if files == nil {
		files = []googleDriveFile{}
	}
	files, truncated := googleCap(files, 100)
	safe := make([]any, 0, len(files))
	for _, file := range files {
		if file.ID == "" {
			return MappedResult{}, ErrPieceOutputInvalid
		}
		safe = append(safe, safeGoogleDriveFile(file))
	}
	return googleJSON(struct {
		Files     []any `json:"files"`
		Truncated bool  `json:"truncated"`
	}{Files: safe, Truncated: truncated})
}

func mapGoogleDriveFile(request MapRequest, kind string) (MappedResult, error) {
	file := decodeGoogleDriveFile(request.Output)
	var props struct {
		FileID  string `json:"file_id"`
		DriveID string `json:"drive_id"`
		ReplyID string `json:"reply_id"`
	}
	_ = json.Unmarshal(request.Props, &props)
	id := file.ID
	if kind == "mutated" && props.FileID != "" {
		id = props.FileID
	}
	if id == "" {
		id = props.FileID
	}
	if id == "" {
		id = props.DriveID
	}
	if id == "" {
		id = props.ReplyID
	}
	if id == "" {
		return MappedResult{}, ErrPieceOutputInvalid
	}
	if kind == "" && file.Name == "" {
		return MappedResult{}, ErrPieceOutputInvalid
	}
	if file.ID == "" {
		file.ID = id
	}
	result, err := json.Marshal(safeGoogleDriveFile(file))
	if err != nil {
		return MappedResult{}, ErrPieceOutputInvalid
	}
	switch kind {
	case "created":
		return MappedResult{SafeResult: result, DetailType: integrationevents.GoogleDriveItemCreatedDetailType,
			Data: integrationevents.GoogleDriveItemCreated{
				ExecutionID: request.Execution.ID, ConnectionID: request.Connection.ID, ItemID: id,
				RequestedBy: request.Execution.MemberID, CreatedAt: request.OccurredAt,
			}}, nil
	case "copied":
		return MappedResult{SafeResult: result, DetailType: integrationevents.GoogleDriveItemCopiedDetailType,
			Data: integrationevents.GoogleDriveItemCopied{
				ExecutionID: request.Execution.ID, ConnectionID: request.Connection.ID, ItemID: id,
				RequestedBy: request.Execution.MemberID, CopiedAt: request.OccurredAt,
			}}, nil
	case "mutated":
		return MappedResult{SafeResult: result, DetailType: integrationevents.GoogleDriveItemMutatedDetailType,
			Data: integrationevents.GoogleDriveItemMutated{
				ExecutionID: request.Execution.ID, ConnectionID: request.Connection.ID, ItemID: id,
				RequestedBy: request.Execution.MemberID, MutatedAt: request.OccurredAt,
			}}, nil
	default:
		return MappedResult{SafeResult: result}, nil
	}
}

func mapGoogleDrivePublicAccess(request MapRequest) (MappedResult, error) {
	var output struct {
		ID          string `json:"id"`
		Type        string `json:"type"`
		Role        string `json:"role"`
		WebViewLink string `json:"webViewLink"`
	}
	_ = json.Unmarshal(unwrapGoogleHTTP(request.Output), &output)
	var props struct {
		FileID string `json:"file_id"`
		Role   string `json:"role"`
	}
	_ = json.Unmarshal(request.Props, &props)
	if props.FileID == "" {
		return MappedResult{}, ErrPieceOutputInvalid
	}
	role := output.Role
	if role == "" {
		role = props.Role
	}
	result, err := json.Marshal(struct {
		FileID       string `json:"file_id"`
		PermissionID string `json:"permission_id,omitempty"`
		Type         string `json:"type,omitempty"`
		Role         string `json:"role,omitempty"`
		URL          string `json:"url,omitempty"`
	}{FileID: props.FileID, PermissionID: output.ID, Type: output.Type, Role: role, URL: output.WebViewLink})
	if err != nil {
		return MappedResult{}, ErrPieceOutputInvalid
	}
	return MappedResult{SafeResult: result, DetailType: integrationevents.GoogleDriveItemMutatedDetailType,
		Data: integrationevents.GoogleDriveItemMutated{
			ExecutionID: request.Execution.ID, ConnectionID: request.Connection.ID, ItemID: props.FileID,
			RequestedBy: request.Execution.MemberID, MutatedAt: request.OccurredAt,
		}}, nil
}

func mapGoogleDriveDriveMutation(request MapRequest) (MappedResult, error) {
	var props struct {
		DriveID string `json:"drive_id"`
	}
	_ = json.Unmarshal(request.Props, &props)
	var output struct {
		Success bool   `json:"success"`
		Deleted bool   `json:"deleted"`
		DriveID string `json:"driveId"`
	}
	_ = json.Unmarshal(unwrapGoogleHTTP(request.Output), &output)
	id := firstNonEmpty(output.DriveID, props.DriveID)
	if id == "" {
		id = "me"
	}
	success := output.Success || output.Deleted || request.ActionName == "empty-trash" ||
		request.ActionName == "delete-shared-drive"
	result, err := json.Marshal(struct {
		DriveID string `json:"drive_id"`
		Success bool   `json:"success"`
	}{DriveID: id, Success: success})
	if err != nil {
		return MappedResult{}, ErrPieceOutputInvalid
	}
	return MappedResult{SafeResult: result, DetailType: integrationevents.GoogleDriveItemMutatedDetailType,
		Data: integrationevents.GoogleDriveItemMutated{
			ExecutionID: request.Execution.ID, ConnectionID: request.Connection.ID, ItemID: id,
			RequestedBy: request.Execution.MemberID, MutatedAt: request.OccurredAt,
		}}, nil
}

func mapGoogleDriveLookup(request MapRequest) (MappedResult, error) {
	payload := unwrapGoogleHTTP(request.Output)
	switch request.ActionName {
	case "list-drive-permissions":
		var output struct {
			Permissions []struct {
				ID           string `json:"id"`
				Type         string `json:"type"`
				Role         string `json:"role"`
				EmailAddress string `json:"emailAddress"`
				Domain       string `json:"domain"`
			} `json:"permissions"`
		}
		if json.Unmarshal(payload, &output) != nil {
			return MappedResult{}, ErrPieceOutputInvalid
		}
		if output.Permissions == nil {
			output.Permissions = []struct {
				ID           string `json:"id"`
				Type         string `json:"type"`
				Role         string `json:"role"`
				EmailAddress string `json:"emailAddress"`
				Domain       string `json:"domain"`
			}{}
		}
		items, truncated := googleCap(output.Permissions, 100)
		safe := make([]any, 0, len(items))
		for _, item := range items {
			if item.ID == "" {
				return MappedResult{}, ErrPieceOutputInvalid
			}
			safe = append(safe, struct {
				ID     string `json:"id"`
				Type   string `json:"type,omitempty"`
				Role   string `json:"role,omitempty"`
				Email  string `json:"email,omitempty"`
				Domain string `json:"domain,omitempty"`
			}{ID: item.ID, Type: item.Type, Role: item.Role, Email: item.EmailAddress, Domain: item.Domain})
		}
		return googleJSON(struct {
			Permissions []any `json:"permissions"`
			Truncated   bool  `json:"truncated"`
		}{Permissions: safe, Truncated: truncated})
	case "list-shared-drives":
		var output struct {
			Drives []struct {
				ID   string `json:"id"`
				Name string `json:"name"`
			} `json:"drives"`
		}
		if json.Unmarshal(payload, &output) != nil {
			return MappedResult{}, ErrPieceOutputInvalid
		}
		if output.Drives == nil {
			output.Drives = []struct {
				ID   string `json:"id"`
				Name string `json:"name"`
			}{}
		}
		items, truncated := googleCap(output.Drives, 100)
		for _, item := range items {
			if item.ID == "" {
				return MappedResult{}, ErrPieceOutputInvalid
			}
		}
		return googleJSON(struct {
			Drives    any  `json:"drives"`
			Truncated bool `json:"truncated"`
		}{Drives: items, Truncated: truncated})
	case "get-shared-drive":
		var output struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		}
		if json.Unmarshal(payload, &output) != nil || output.ID == "" {
			return MappedResult{}, ErrPieceOutputInvalid
		}
		return googleJSON(output)
	case "list-drive-comments":
		return mapGoogleDriveComments(payload, "comments")
	case "list-drive-replies":
		return mapGoogleDriveComments(payload, "replies")
	case "get-drive-reply":
		var output struct {
			ID      string `json:"id"`
			Content string `json:"content"`
		}
		if json.Unmarshal(payload, &output) != nil || output.ID == "" {
			return MappedResult{}, ErrPieceOutputInvalid
		}
		return googleJSON(output)
	case "get-drive-about":
		var output struct {
			User struct {
				EmailAddress string `json:"emailAddress"`
			} `json:"user"`
			StorageQuota struct {
				Limit        string `json:"limit"`
				Usage        string `json:"usage"`
				UsageInDrive string `json:"usageInDrive"`
			} `json:"storageQuota"`
		}
		if json.Unmarshal(payload, &output) != nil {
			return MappedResult{}, ErrPieceOutputInvalid
		}
		return googleJSON(struct {
			Email        string `json:"email,omitempty"`
			Limit        string `json:"limit,omitempty"`
			Usage        string `json:"usage,omitempty"`
			UsageInDrive string `json:"usage_in_drive,omitempty"`
		}{Email: output.User.EmailAddress, Limit: output.StorageQuota.Limit,
			Usage: output.StorageQuota.Usage, UsageInDrive: output.StorageQuota.UsageInDrive})
	default:
		return MappedResult{}, fmt.Errorf("%w: unsupported Google action", ErrPieceOutputInvalid)
	}
}

type googleDriveComment struct {
	ID      string `json:"id"`
	Content string `json:"content,omitempty"`
}

func mapGoogleDriveComments(raw json.RawMessage, field string) (MappedResult, error) {
	var output struct {
		Comments []googleDriveComment `json:"comments"`
		Replies  []googleDriveComment `json:"replies"`
	}
	if json.Unmarshal(raw, &output) != nil {
		return MappedResult{}, ErrPieceOutputInvalid
	}
	items := output.Comments
	if field == "replies" {
		items = output.Replies
	}
	if items == nil {
		items = []googleDriveComment{}
	}
	capped, truncated := googleCap(items, 100)
	safe := make([]any, 0, len(capped))
	for _, item := range capped {
		if item.ID == "" {
			return MappedResult{}, ErrPieceOutputInvalid
		}
		safe = append(safe, item)
	}
	if field == "replies" {
		return googleJSON(struct {
			Replies   []any `json:"replies"`
			Truncated bool  `json:"truncated"`
		}{Replies: safe, Truncated: truncated})
	}
	return googleJSON(struct {
		Comments  []any `json:"comments"`
		Truncated bool  `json:"truncated"`
	}{Comments: safe, Truncated: truncated})
}

func unwrapGoogleHTTP(raw json.RawMessage) json.RawMessage {
	var wrapped struct {
		Body json.RawMessage `json:"body"`
	}
	if json.Unmarshal(raw, &wrapped) == nil && len(wrapped.Body) > 0 && json.Valid(wrapped.Body) &&
		(wrapped.Body[0] == '{' || wrapped.Body[0] == '[') {
		return wrapped.Body
	}
	return raw
}

func googleEmptyOutput(raw json.RawMessage) bool {
	trimmed := strings.TrimSpace(string(raw))
	return trimmed == "" || trimmed == "null" || trimmed == "{}" || trimmed == "[]" || trimmed == `""`
}

func googleJSON(value any) (MappedResult, error) {
	result, err := json.Marshal(value)
	if err != nil {
		return MappedResult{}, ErrPieceOutputInvalid
	}
	return MappedResult{SafeResult: result}, nil
}

func googleCap[T any](items []T, maximum int) ([]T, bool) {
	if len(items) <= maximum {
		return items, false
	}
	return items[:maximum], true
}

func toAny[T any](items []T) []any {
	safe := make([]any, 0, len(items))
	for _, item := range items {
		safe = append(safe, item)
	}
	return safe
}

func dateOrTime(dateTime, date string) string {
	if dateTime != "" {
		return dateTime
	}
	return date
}

func validProviderTime(value string) string {
	if value == "" {
		return ""
	}
	if _, err := time.Parse(time.RFC3339, value); err != nil {
		return ""
	}
	return value
}
