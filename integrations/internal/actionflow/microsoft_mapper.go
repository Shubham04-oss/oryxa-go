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
	case "send-email", "send-draft-email":
		return mapMicrosoftSentEmail(request)
	case "reply-email":
		return mapMicrosoftReplyEmail(request)
	case "create-draft-email":
		return mapMicrosoftDraftCreated(request)
	case "forward-email":
		return mapMicrosoftForwardedEmail(request)
	case "add-label-to-email", "remove-label-from-email":
		return mapMicrosoftMutatedEmail(request, "")
	case "move-email-to-folder":
		return mapMicrosoftMovedEmail(request)
	case "find-email":
		return mapMicrosoftFoundEmail(request)
	case "create-calendar-event":
		return mapMicrosoftCreatedCalendarEvent(request)
	case "list-calendar-events":
		return mapMicrosoftCalendarEvents(request)
	case "delete-calendar-event":
		return mapMicrosoftDeletedCalendarEvent(request)
	case "list-files", "list-folders":
		return mapMicrosoftOneDriveFiles(request)
	case "copy-file":
		return mapMicrosoftCopiedFile(request)
	case "upload-onedrive-file":
		return mapMicrosoftCreatedFile(request)
	case "download-file":
		return mapMicrosoftDownloadedFile(request)
	case "download-attachment":
		return mapMicrosoftDownloadedAttachments(request)
	case "custom-api-call":
		return mapProviderHTTPCall(request)
	}
	if result, ok, err := mapMicrosoftTeamsAction(request); ok {
		return result, err
	}
	if result, ok, err := mapMicrosoftSharePointAction(request); ok {
		return result, err
	}
	return MappedResult{}, fmt.Errorf("%w: unsupported Microsoft action", ErrPieceOutputInvalid)
}

func mapMicrosoftSentEmail(request MapRequest) (MappedResult, error) {
	// Graph's sendMail endpoint returns 202 with an empty body. send-draft-email
	// wraps the same empty POST in {success, messageId}. Recipients, subject,
	// and body must not appear in either shape.
	if request.ActionName == "send-email" {
		if !microsoftEmptyOutput(request.Output) && strings.TrimSpace(string(request.Output)) != `{"accepted":true}` {
			return MappedResult{}, ErrPieceOutputInvalid
		}
		return MappedResult{SafeResult: json.RawMessage(`{"accepted":true}`),
			DetailType: integrationevents.MicrosoftOutlookEmailAcceptedDetailType,
			Data: integrationevents.MicrosoftOutlookEmailAccepted{
				ExecutionID: request.Execution.ID, ConnectionID: request.Connection.ID,
				RequestedBy: request.Execution.MemberID, AcceptedAt: request.OccurredAt,
			}}, nil
	}
	id, ok := microsoftOutputID(request.Output, request.Props, "messageId")
	if !ok {
		return MappedResult{}, ErrPieceOutputInvalid
	}
	result, err := json.Marshal(struct {
		Accepted bool   `json:"accepted"`
		ID       string `json:"id"`
	}{Accepted: true, ID: id})
	if err != nil {
		return MappedResult{}, ErrPieceOutputInvalid
	}
	return MappedResult{SafeResult: result,
		DetailType: integrationevents.MicrosoftOutlookEmailAcceptedDetailType,
		Data: integrationevents.MicrosoftOutlookEmailAccepted{
			ExecutionID: request.Execution.ID, ConnectionID: request.Connection.ID,
			RequestedBy: request.Execution.MemberID, AcceptedAt: request.OccurredAt,
		}}, nil
}

func mapMicrosoftReplyEmail(request MapRequest) (MappedResult, error) {
	var output struct {
		DraftID   string `json:"draftId"`
		DraftLink string `json:"draftLink"`
	}
	var props struct {
		Draft     bool   `json:"draft"`
		MessageID string `json:"messageId"`
	}
	if json.Unmarshal(request.Output, &output) != nil || json.Unmarshal(request.Props, &props) != nil {
		return MappedResult{}, ErrPieceOutputInvalid
	}
	id := output.DraftID
	if id == "" {
		id = props.MessageID
	}
	if id == "" {
		return MappedResult{}, ErrPieceOutputInvalid
	}
	result, err := json.Marshal(struct {
		ID    string `json:"id"`
		URL   string `json:"url,omitempty"`
		Draft bool   `json:"draft"`
	}{ID: id, URL: safeMicrosoftURL(output.DraftLink), Draft: props.Draft})
	if err != nil {
		return MappedResult{}, ErrPieceOutputInvalid
	}
	if props.Draft {
		return MappedResult{SafeResult: result,
			DetailType: integrationevents.MicrosoftOutlookDraftCreatedDetailType,
			Data: integrationevents.MicrosoftOutlookDraftCreated{
				ExecutionID: request.Execution.ID, ConnectionID: request.Connection.ID, MessageID: id,
				RequestedBy: request.Execution.MemberID, CreatedAt: request.OccurredAt,
			}}, nil
	}
	return MappedResult{SafeResult: result,
		DetailType: integrationevents.MicrosoftOutlookEmailAcceptedDetailType,
		Data: integrationevents.MicrosoftOutlookEmailAccepted{
			ExecutionID: request.Execution.ID, ConnectionID: request.Connection.ID,
			RequestedBy: request.Execution.MemberID, AcceptedAt: request.OccurredAt,
		}}, nil
}

func mapMicrosoftDraftCreated(request MapRequest) (MappedResult, error) {
	id, ok := microsoftOutputID(request.Output, nil, "id")
	if !ok {
		return MappedResult{}, ErrPieceOutputInvalid
	}
	result, err := json.Marshal(struct {
		ID string `json:"id"`
	}{ID: id})
	if err != nil {
		return MappedResult{}, ErrPieceOutputInvalid
	}
	return MappedResult{SafeResult: result,
		DetailType: integrationevents.MicrosoftOutlookDraftCreatedDetailType,
		Data: integrationevents.MicrosoftOutlookDraftCreated{
			ExecutionID: request.Execution.ID, ConnectionID: request.Connection.ID, MessageID: id,
			RequestedBy: request.Execution.MemberID, CreatedAt: request.OccurredAt,
		}}, nil
}

func mapMicrosoftForwardedEmail(request MapRequest) (MappedResult, error) {
	id, ok := microsoftOutputID(request.Output, request.Props, "messageId")
	if !ok {
		return MappedResult{}, ErrPieceOutputInvalid
	}
	result, err := json.Marshal(struct {
		ID string `json:"id"`
	}{ID: id})
	if err != nil {
		return MappedResult{}, ErrPieceOutputInvalid
	}
	return MappedResult{SafeResult: result,
		DetailType: integrationevents.MicrosoftOutlookEmailForwardedDetailType,
		Data: integrationevents.MicrosoftOutlookEmailForwarded{
			ExecutionID: request.Execution.ID, ConnectionID: request.Connection.ID, MessageID: id,
			RequestedBy: request.Execution.MemberID, ForwardedAt: request.OccurredAt,
		}}, nil
}

func mapMicrosoftMutatedEmail(request MapRequest, folderID string) (MappedResult, error) {
	id, ok := microsoftOutputID(request.Output, request.Props, "id")
	if !ok {
		id, ok = microsoftOutputID(nil, request.Props, "messageId")
	}
	if !ok {
		return MappedResult{}, ErrPieceOutputInvalid
	}
	result, err := json.Marshal(struct {
		ID       string `json:"id"`
		FolderID string `json:"folder_id,omitempty"`
	}{ID: id, FolderID: folderID})
	if err != nil {
		return MappedResult{}, ErrPieceOutputInvalid
	}
	return MappedResult{SafeResult: result,
		DetailType: integrationevents.MicrosoftOutlookEmailMutatedDetailType,
		Data: integrationevents.MicrosoftOutlookEmailMutated{
			ExecutionID: request.Execution.ID, ConnectionID: request.Connection.ID,
			MessageID: id, FolderID: folderID, RequestedBy: request.Execution.MemberID,
			MutatedAt: request.OccurredAt,
		}}, nil
}

func mapMicrosoftMovedEmail(request MapRequest) (MappedResult, error) {
	var props struct {
		DestinationFolderID string `json:"destinationFolderId"`
	}
	_ = json.Unmarshal(request.Props, &props)
	folderID := props.DestinationFolderID
	var output struct {
		ParentFolderID string `json:"parentFolderId"`
	}
	_ = json.Unmarshal(request.Output, &output)
	if output.ParentFolderID != "" {
		folderID = output.ParentFolderID
	}
	return mapMicrosoftMutatedEmail(request, folderID)
}

func mapMicrosoftFoundEmail(request MapRequest) (MappedResult, error) {
	var output struct {
		Found   bool `json:"found"`
		HasMore bool `json:"hasMore"`
		Result  []struct {
			ID               string `json:"id"`
			Subject          string `json:"subject"`
			ReceivedDateTime string `json:"receivedDateTime"`
		} `json:"result"`
	}
	if json.Unmarshal(request.Output, &output) != nil {
		return MappedResult{}, ErrPieceOutputInvalid
	}
	items := output.Result
	if items == nil {
		items = []struct {
			ID               string `json:"id"`
			Subject          string `json:"subject"`
			ReceivedDateTime string `json:"receivedDateTime"`
		}{}
	}
	const maximum = 100
	truncated := output.HasMore || len(items) > maximum
	if len(items) > maximum {
		items = items[:maximum]
	}
	safe := make([]any, 0, len(items))
	for _, item := range items {
		if item.ID == "" {
			return MappedResult{}, ErrPieceOutputInvalid
		}
		safe = append(safe, struct {
			ID         string `json:"id"`
			Subject    string `json:"subject,omitempty"`
			ReceivedAt string `json:"received_at,omitempty"`
		}{ID: item.ID, Subject: item.Subject, ReceivedAt: item.ReceivedDateTime})
	}
	result, err := json.Marshal(struct {
		Found     bool  `json:"found"`
		Items     []any `json:"items"`
		Truncated bool  `json:"truncated"`
	}{Found: output.Found, Items: safe, Truncated: truncated})
	if err != nil {
		return MappedResult{}, ErrPieceOutputInvalid
	}
	return MappedResult{SafeResult: result}, nil
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

func mapMicrosoftDeletedCalendarEvent(request MapRequest) (MappedResult, error) {
	if !microsoftEmptyOutput(request.Output) {
		return MappedResult{}, ErrPieceOutputInvalid
	}
	var props struct {
		CalendarID string `json:"calendarId"`
		EventID    string `json:"eventId"`
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
	return MappedResult{SafeResult: result,
		DetailType: integrationevents.MicrosoftCalendarEventDeletedDetailType,
		Data: integrationevents.MicrosoftCalendarEventDeleted{
			ExecutionID: request.Execution.ID, ConnectionID: request.Connection.ID,
			CalendarID: props.CalendarID, EventID: props.EventID,
			RequestedBy: request.Execution.MemberID, DeletedAt: request.OccurredAt,
		}}, nil
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

func mapMicrosoftCopiedFile(request MapRequest) (MappedResult, error) {
	var output struct {
		ResourceID string                `json:"resourceId"`
		File       microsoftOneDriveFile `json:"file"`
	}
	if json.Unmarshal(request.Output, &output) != nil {
		return MappedResult{}, ErrPieceOutputInvalid
	}
	file := output.File
	if file.ID == "" {
		file.ID = output.ResourceID
	}
	if file.ID == "" {
		return MappedResult{}, ErrPieceOutputInvalid
	}
	if file.Name == "" {
		file.Name = file.ID
	}
	result, err := json.Marshal(safeMicrosoftOneDriveFile(file))
	if err != nil {
		return MappedResult{}, ErrPieceOutputInvalid
	}
	return MappedResult{SafeResult: result,
		DetailType: integrationevents.MicrosoftOneDriveItemCopiedDetailType,
		Data: integrationevents.MicrosoftOneDriveItemCopied{
			ExecutionID: request.Execution.ID, ConnectionID: request.Connection.ID, ItemID: file.ID,
			RequestedBy: request.Execution.MemberID, CopiedAt: request.OccurredAt,
		}}, nil
}

func mapMicrosoftCreatedFile(request MapRequest) (MappedResult, error) {
	file := decodeMicrosoftOneDriveFile(request.Output)
	if file.ID == "" {
		return MappedResult{}, ErrPieceOutputInvalid
	}
	if file.Name == "" {
		file.Name = file.ID
	}
	result, err := json.Marshal(safeMicrosoftOneDriveFile(file))
	if err != nil {
		return MappedResult{}, ErrPieceOutputInvalid
	}
	return MappedResult{SafeResult: result,
		DetailType: integrationevents.MicrosoftOneDriveItemCreatedDetailType,
		Data: integrationevents.MicrosoftOneDriveItemCreated{
			ExecutionID: request.Execution.ID, ConnectionID: request.Connection.ID, ItemID: file.ID,
			RequestedBy: request.Execution.MemberID, CreatedAt: request.OccurredAt,
		}}, nil
}

func mapMicrosoftDownloadedFile(request MapRequest) (MappedResult, error) {
	return mapStoredFileLookup(request.Output)
}

func mapMicrosoftDownloadedAttachments(request MapRequest) (MappedResult, error) {
	var items []json.RawMessage
	if json.Unmarshal(request.Output, &items) != nil {
		var wrapped struct {
			Value []json.RawMessage `json:"value"`
		}
		if json.Unmarshal(request.Output, &wrapped) != nil || wrapped.Value == nil {
			return MappedResult{}, ErrPieceOutputInvalid
		}
		items = wrapped.Value
	}
	if items == nil {
		items = []json.RawMessage{}
	}
	const maximum = 5
	truncated := len(items) > maximum
	if truncated {
		items = items[:maximum]
	}
	safe := make([]any, 0, len(items))
	for _, item := range items {
		filename, fileID := writtenOryxaFile(item)
		var named struct {
			Name string `json:"name"`
		}
		_ = json.Unmarshal(item, &named)
		if fileID == "" {
			return MappedResult{}, ErrPieceOutputInvalid
		}
		safe = append(safe, struct {
			Name     string `json:"name,omitempty"`
			FileID   string `json:"file_id"`
			Filename string `json:"filename,omitempty"`
		}{Name: named.Name, FileID: fileID, Filename: filename})
	}
	result, err := json.Marshal(struct {
		Attachments []any `json:"attachments"`
		Truncated   bool  `json:"truncated"`
	}{Attachments: safe, Truncated: truncated})
	if err != nil {
		return MappedResult{}, ErrPieceOutputInvalid
	}
	return MappedResult{SafeResult: result}, nil
}

func decodeMicrosoftOneDriveFile(raw json.RawMessage) microsoftOneDriveFile {
	var file microsoftOneDriveFile
	if json.Unmarshal(raw, &file) == nil && file.ID != "" {
		return file
	}
	var wrapped struct {
		File microsoftOneDriveFile `json:"file"`
	}
	_ = json.Unmarshal(raw, &wrapped)
	return wrapped.File
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

func microsoftEmptyOutput(raw json.RawMessage) bool {
	trimmed := strings.TrimSpace(string(raw))
	return trimmed == "" || trimmed == "null" || trimmed == "{}" || trimmed == "[]"
}

func microsoftOutputID(output, props json.RawMessage, propField string) (string, bool) {
	var decoded struct {
		ID         string `json:"id"`
		DraftID    string `json:"draftId"`
		MessageID  string `json:"messageId"`
		ResourceID string `json:"resourceId"`
	}
	if len(output) > 0 && json.Unmarshal(output, &decoded) == nil {
		for _, candidate := range []string{decoded.ID, decoded.DraftID, decoded.MessageID, decoded.ResourceID} {
			if candidate != "" {
				return candidate, true
			}
		}
	}
	if len(props) == 0 {
		return "", false
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(props, &fields) != nil {
		return "", false
	}
	for _, key := range []string{propField, "messageId", "id"} {
		raw, ok := fields[key]
		if !ok {
			continue
		}
		var value string
		if json.Unmarshal(raw, &value) == nil && value != "" {
			return value, true
		}
	}
	return "", false
}
