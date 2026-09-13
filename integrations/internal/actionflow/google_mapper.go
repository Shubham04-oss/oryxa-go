package actionflow

import (
	"encoding/json"
	"fmt"
	"time"

	integrationevents "github.com/Shubham04-oss/oryxa-go/contracts/events/integrations/v1"
)

func mapGoogle(request MapRequest) (MappedResult, error) {
	switch request.ActionName {
	case "send-email":
		return mapGoogleGmailMessage(request)
	case "create-calendar-event":
		return mapGoogleCalendarCreatedEvent(request)
	case "list-calendar-events":
		return mapGoogleCalendarEvents(request)
	case "list-drive-files":
		return mapGoogleDriveFiles(request)
	case "get-drive-file-metadata":
		return mapGoogleDriveFile(request)
	default:
		return MappedResult{}, fmt.Errorf("%w: unsupported Google action", ErrPieceOutputInvalid)
	}
}

func mapGoogleGmailMessage(request MapRequest) (MappedResult, error) {
	type message struct {
		ID       string   `json:"id"`
		ThreadID string   `json:"threadId"`
		LabelIDs []string `json:"labelIds"`
	}
	var output struct {
		message
		Data message `json:"data"`
	}
	if json.Unmarshal(request.Output, &output) != nil {
		return MappedResult{}, ErrPieceOutputInvalid
	}
	value := output.message
	if value.ID == "" {
		value = output.Data
	}
	if value.ID == "" {
		return MappedResult{}, ErrPieceOutputInvalid
	}
	result, _ := json.Marshal(struct {
		MessageID string `json:"message_id"`
		ThreadID  string `json:"thread_id,omitempty"`
	}{MessageID: value.ID, ThreadID: value.ThreadID})
	data := integrationevents.GoogleGmailMessageSent{
		ExecutionID: request.Execution.ID, ConnectionID: request.Connection.ID,
		MessageID: value.ID, ThreadID: value.ThreadID,
		RequestedBy: request.Execution.MemberID, SentAt: request.OccurredAt,
	}
	return MappedResult{SafeResult: result, DetailType: integrationevents.GoogleGmailMessageSentDetailType, Data: data}, nil
}

type googleCalendarEvent struct {
	ID       string `json:"id"`
	Status   string `json:"status"`
	Summary  string `json:"summary"`
	HTMLLink string `json:"htmlLink"`
	Created  string `json:"created"`
	Updated  string `json:"updated"`
	Start    struct {
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

func mapGoogleCalendarCreatedEvent(request MapRequest) (MappedResult, error) {
	var output googleCalendarEvent
	var props struct {
		CalendarID string `json:"calendar_id"`
	}
	if json.Unmarshal(request.Output, &output) != nil || json.Unmarshal(request.Props, &props) != nil ||
		output.ID == "" || props.CalendarID == "" {
		return MappedResult{}, ErrPieceOutputInvalid
	}
	result, err := json.Marshal(safeGoogleCalendarEvent(output))
	if err != nil {
		return MappedResult{}, ErrPieceOutputInvalid
	}
	data := integrationevents.GoogleCalendarEventCreated{
		ExecutionID: request.Execution.ID, ConnectionID: request.Connection.ID,
		CalendarID: props.CalendarID, EventID: output.ID, URL: output.HTMLLink,
		RequestedBy: request.Execution.MemberID, CreatedAt: request.OccurredAt,
	}
	return MappedResult{SafeResult: result, DetailType: integrationevents.GoogleCalendarEventCreatedDetailType, Data: data}, nil
}

func mapGoogleCalendarEvents(request MapRequest) (MappedResult, error) {
	var output struct {
		Items []googleCalendarEvent `json:"items"`
		Body  struct {
			Items []googleCalendarEvent `json:"items"`
		} `json:"body"`
	}
	if json.Unmarshal(request.Output, &output) != nil {
		return MappedResult{}, ErrPieceOutputInvalid
	}
	items := output.Items
	if items == nil {
		items = output.Body.Items
	}
	const maximum = 100
	truncated := len(items) > maximum
	if truncated {
		items = items[:maximum]
	}
	safe := make([]any, 0, len(items))
	for _, item := range items {
		if item.ID == "" {
			return MappedResult{}, ErrPieceOutputInvalid
		}
		safe = append(safe, safeGoogleCalendarEvent(item))
	}
	result, err := json.Marshal(struct {
		Items     []any `json:"items"`
		Truncated bool  `json:"truncated"`
	}{Items: safe, Truncated: truncated})
	if err != nil {
		return MappedResult{}, ErrPieceOutputInvalid
	}
	return MappedResult{SafeResult: result}, nil
}

func safeGoogleCalendarEvent(value googleCalendarEvent) any {
	return struct {
		ID       string `json:"id"`
		Status   string `json:"status,omitempty"`
		Summary  string `json:"summary,omitempty"`
		URL      string `json:"url,omitempty"`
		Start    string `json:"start,omitempty"`
		End      string `json:"end,omitempty"`
		TimeZone string `json:"time_zone,omitempty"`
		Updated  string `json:"updated,omitempty"`
	}{ID: value.ID, Status: value.Status, Summary: value.Summary, URL: value.HTMLLink,
		Start: dateOrTime(value.Start.DateTime, value.Start.Date),
		End:   dateOrTime(value.End.DateTime, value.End.Date), TimeZone: value.Start.TimeZone,
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

func mapGoogleDriveFiles(request MapRequest) (MappedResult, error) {
	var output struct {
		Files []googleDriveFile `json:"files"`
	}
	if json.Unmarshal(request.Output, &output) != nil || output.Files == nil {
		return MappedResult{}, ErrPieceOutputInvalid
	}
	const maximum = 100
	truncated := len(output.Files) > maximum
	files := output.Files
	if truncated {
		files = files[:maximum]
	}
	for _, file := range files {
		if file.ID == "" || file.Name == "" {
			return MappedResult{}, ErrPieceOutputInvalid
		}
	}
	result, err := json.Marshal(struct {
		Files     []googleDriveFile `json:"files"`
		Truncated bool              `json:"truncated"`
	}{Files: files, Truncated: truncated})
	if err != nil {
		return MappedResult{}, ErrPieceOutputInvalid
	}
	return MappedResult{SafeResult: result}, nil
}

func mapGoogleDriveFile(request MapRequest) (MappedResult, error) {
	var output googleDriveFile
	if json.Unmarshal(request.Output, &output) != nil || output.ID == "" || output.Name == "" {
		return MappedResult{}, ErrPieceOutputInvalid
	}
	result, err := json.Marshal(output)
	if err != nil {
		return MappedResult{}, ErrPieceOutputInvalid
	}
	return MappedResult{SafeResult: result}, nil
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
