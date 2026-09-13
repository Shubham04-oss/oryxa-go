package actionflow

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"

	integrationevents "github.com/Shubham04-oss/oryxa-go/contracts/events/integrations/v1"
)

// Microsoft pieces expose Microsoft Graph payloads. These mappers deliberately
// retain only stable resource metadata and never persist mail bodies, file
// content, attendees, authentication data, or Graph transport headers.
func mapMicrosoft(request MapRequest) (MappedResult, error) {
	switch request.ActionName {
	case "send-email":
		return mapMicrosoftSentEmail(request)
	case "create-calendar-event":
		return mapMicrosoftCreatedCalendarEvent(request)
	case "list-calendar-events":
		return mapMicrosoftCalendarEvents(request)
	case "list-files":
		return mapMicrosoftOneDriveFiles(request)
	default:
		return MappedResult{}, fmt.Errorf("%w: unsupported Microsoft action", ErrPieceOutputInvalid)
	}
}

func mapMicrosoftSentEmail(request MapRequest) (MappedResult, error) {
	// Graph's sendMail endpoint returns 202 with an empty body. The runtime
	// serializes that as null, {} or an omitted output; all represent an accepted
	// request and reveal no submitted email content.
	trimmed := strings.TrimSpace(string(request.Output))
	if trimmed != "" && trimmed != "null" && trimmed != "{}" && trimmed != `{"accepted":true}` {
		return MappedResult{}, ErrPieceOutputInvalid
	}
	return MappedResult{SafeResult: json.RawMessage(`{"accepted":true}`),
		DetailType: integrationevents.MicrosoftOutlookEmailAcceptedDetailType,
		Data: integrationevents.MicrosoftOutlookEmailAccepted{
			ExecutionID: request.Execution.ID, ConnectionID: request.Connection.ID,
			RequestedBy: request.Execution.MemberID, AcceptedAt: request.OccurredAt,
		}}, nil
}

type microsoftCalendarEvent struct {
	ID      string `json:"id"`
	Subject string `json:"subject"`
	WebLink string `json:"webLink"`
	Start   struct {
		DateTime string `json:"dateTime"`
		TimeZone string `json:"timeZone"`
	} `json:"start"`
	End struct {
		DateTime string `json:"dateTime"`
		TimeZone string `json:"timeZone"`
	} `json:"end"`
}

func mapMicrosoftCreatedCalendarEvent(request MapRequest) (MappedResult, error) {
	var output microsoftCalendarEvent
	var props struct {
		CalendarID string `json:"calendarId"`
	}
	if json.Unmarshal(request.Output, &output) != nil || json.Unmarshal(request.Props, &props) != nil ||
		output.ID == "" || props.CalendarID == "" {
		return MappedResult{}, ErrPieceOutputInvalid
	}
	result, err := json.Marshal(safeMicrosoftCalendarEvent(output))
	if err != nil {
		return MappedResult{}, ErrPieceOutputInvalid
	}
	return MappedResult{SafeResult: result,
		DetailType: integrationevents.MicrosoftCalendarEventCreatedDetailType,
		Data: integrationevents.MicrosoftCalendarEventCreated{
			ExecutionID: request.Execution.ID, ConnectionID: request.Connection.ID,
			CalendarID: props.CalendarID, EventID: output.ID, URL: safeMicrosoftURL(output.WebLink),
			RequestedBy: request.Execution.MemberID, CreatedAt: request.OccurredAt,
		}}, nil
}

func mapMicrosoftCalendarEvents(request MapRequest) (MappedResult, error) {
	var output struct {
		Value []microsoftCalendarEvent `json:"value"`
		Items []microsoftCalendarEvent `json:"items"`
	}
	if json.Unmarshal(request.Output, &output) != nil {
		return MappedResult{}, ErrPieceOutputInvalid
	}
	items := output.Value
	if items == nil {
		items = output.Items
	}
	if items == nil {
		return MappedResult{}, ErrPieceOutputInvalid
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
		safe = append(safe, safeMicrosoftCalendarEvent(item))
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

func safeMicrosoftCalendarEvent(value microsoftCalendarEvent) any {
	return struct {
		ID       string `json:"id"`
		URL      string `json:"url,omitempty"`
		Start    string `json:"start,omitempty"`
		End      string `json:"end,omitempty"`
		TimeZone string `json:"time_zone,omitempty"`
	}{ID: value.ID, URL: safeMicrosoftURL(value.WebLink), Start: value.Start.DateTime,
		End: value.End.DateTime, TimeZone: value.Start.TimeZone}
}

func safeMicrosoftURL(value string) string {
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.Fragment != "" {
		return ""
	}
	return parsed.String()
}

type microsoftOneDriveFile struct {
	ID               string `json:"id"`
	Name             string `json:"name"`
	Size             int64  `json:"size"`
	WebURL           string `json:"webUrl"`
	CreatedDateTime  string `json:"createdDateTime"`
	ModifiedDateTime string `json:"lastModifiedDateTime"`
	File             *struct {
		MimeType string `json:"mimeType"`
	} `json:"file"`
}

func mapMicrosoftOneDriveFiles(request MapRequest) (MappedResult, error) {
	var files []microsoftOneDriveFile
	if json.Unmarshal(request.Output, &files) != nil || files == nil {
		var wrapped struct {
			Value []microsoftOneDriveFile `json:"value"`
			Files []microsoftOneDriveFile `json:"files"`
		}
		if json.Unmarshal(request.Output, &wrapped) != nil {
			return MappedResult{}, ErrPieceOutputInvalid
		}
		files = wrapped.Value
		if files == nil {
			files = wrapped.Files
		}
	}
	if files == nil {
		return MappedResult{}, ErrPieceOutputInvalid
	}
	const maximum = 100
	truncated := len(files) > maximum
	if truncated {
		files = files[:maximum]
	}
	safe := make([]any, 0, len(files))
	for _, file := range files {
		if file.ID == "" || file.Name == "" {
			return MappedResult{}, ErrPieceOutputInvalid
		}
		safe = append(safe, safeMicrosoftOneDriveFile(file))
	}
	result, err := json.Marshal(struct {
		Files     []any `json:"files"`
		Truncated bool  `json:"truncated"`
	}{Files: safe, Truncated: truncated})
	if err != nil {
		return MappedResult{}, ErrPieceOutputInvalid
	}
	return MappedResult{SafeResult: result}, nil
}

func safeMicrosoftOneDriveFile(value microsoftOneDriveFile) any {
	mimeType := ""
	if value.File != nil {
		mimeType = value.File.MimeType
	}
	return struct {
		ID         string `json:"id"`
		Name       string `json:"name"`
		Size       int64  `json:"size,omitempty"`
		MimeType   string `json:"mime_type,omitempty"`
		URL        string `json:"url,omitempty"`
		CreatedAt  string `json:"created_at,omitempty"`
		ModifiedAt string `json:"modified_at,omitempty"`
	}{ID: value.ID, Name: value.Name, Size: value.Size, MimeType: mimeType, URL: safeMicrosoftURL(value.WebURL),
		CreatedAt: value.CreatedDateTime, ModifiedAt: value.ModifiedDateTime}
}
