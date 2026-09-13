package catalog

import (
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/Shubham04-oss/oryxa-go/integrations/internal/actionflow"
)

// These are delegated Microsoft Graph permissions. The provider uses a single
// Microsoft grant while the catalog pins each operation to its exact reviewed
// Activepieces piece and action.
const (
	microsoftMailSendScope      = "Mail.Send"
	microsoftCalendarReadScope  = "Calendars.Read"
	microsoftCalendarWriteScope = "Calendars.ReadWrite"
	microsoftFilesReadScope     = "Files.Read"
)

func microsoftActionDefinition(actionName string) (actionflow.Definition, bool) {
	definition := actionflow.Definition{Provider: "microsoft", ActionName: actionName}
	switch actionName {
	case "send-email":
		definition.PieceName, definition.PieceVersion = MicrosoftOutlookPieceName, MicrosoftOutlookPieceVersion
		definition.RunnerActionName = "send-email"
		definition.RequiredScopes = []string{microsoftMailSendScope}
		definition.ValidateProps = validateMicrosoftSendEmail
	case "create-calendar-event":
		definition.PieceName, definition.PieceVersion = MicrosoftCalendarPieceName, MicrosoftCalendarPieceVersion
		definition.RunnerActionName = "create_event"
		definition.RequiredScopes = []string{microsoftCalendarWriteScope}
		definition.ValidateProps = validateMicrosoftCalendarCreate
	case "list-calendar-events":
		definition.PieceName, definition.PieceVersion = MicrosoftCalendarPieceName, MicrosoftCalendarPieceVersion
		definition.RunnerActionName = "list_events"
		definition.RequiredScopes = []string{microsoftCalendarReadScope}
		definition.ValidateProps = validateMicrosoftCalendarList
	case "list-files":
		definition.PieceName, definition.PieceVersion = MicrosoftOneDrivePieceName, MicrosoftOneDrivePieceVersion
		definition.RunnerActionName = "list_files"
		definition.RequiredScopes = []string{microsoftFilesReadScope}
		definition.ValidateProps = validateMicrosoftOneDriveList
	default:
		return actionflow.Definition{}, false
	}
	return definition, true
}

func validateMicrosoftSendEmail(raw json.RawMessage) error {
	var props struct {
		Recipients    []string `json:"recipients"`
		CCRecipients  []string `json:"ccRecipients"`
		BCCRecipients []string `json:"bccRecipients"`
		Subject       string   `json:"subject"`
		BodyFormat    string   `json:"bodyFormat"`
		Body          string   `json:"body"`
		Attachments   []any    `json:"attachments"`
	}
	if err := decodeStrict(raw, &props); err != nil || !emails(props.Recipients, true) ||
		!emails(props.CCRecipients, false) || !emails(props.BCCRecipients, false) ||
		strings.TrimSpace(props.Subject) == "" || len(props.Subject) > 998 || len(props.Body) == 0 ||
		len(props.Body) > 100*1024 || !oneOf(props.BodyFormat, "text", "html") || len(props.Attachments) != 0 {
		return errors.New("invalid Microsoft Outlook send-email properties")
	}
	return nil
}

func validateMicrosoftCalendarCreate(raw json.RawMessage) error {
	var props struct {
		CalendarID string `json:"calendarId"`
		Title      string `json:"title"`
		Start      string `json:"start"`
		End        string `json:"end"`
		Timezone   string `json:"timezone"`
		Location   string `json:"location"`
	}
	if err := decodeStrict(raw, &props); err != nil {
		return errors.New("invalid Microsoft Outlook Calendar create-event properties")
	}
	start, startErr := time.Parse(time.RFC3339, props.Start)
	var end time.Time
	var endErr error
	if props.End != "" {
		end, endErr = time.Parse(time.RFC3339, props.End)
	}
	if !boundedValue(props.CalendarID, 1024) || strings.TrimSpace(props.Title) == "" || len(props.Title) > 1024 ||
		startErr != nil || endErr != nil || (!end.IsZero() && !end.After(start)) ||
		!boundedTimezone(props.Timezone) || len(props.Location) > 1024 {
		return errors.New("invalid Microsoft Outlook Calendar create-event properties")
	}
	return nil
}

func validateMicrosoftCalendarList(raw json.RawMessage) error {
	var props struct {
		CalendarID string `json:"calendarId"`
		Filter     string `json:"filter"`
	}
	if err := decodeStrict(raw, &props); err != nil || !boundedValue(props.CalendarID, 1024) ||
		len(props.Filter) > 2048 || strings.ContainsAny(props.Filter, "\r\n") {
		return errors.New("invalid Microsoft Outlook Calendar list-events properties")
	}
	return nil
}

func validateMicrosoftOneDriveList(raw json.RawMessage) error {
	var props struct {
		ParentFolder string `json:"parentFolder"`
	}
	if err := decodeStrict(raw, &props); err != nil || (props.ParentFolder != "" && !boundedValue(props.ParentFolder, 1024)) {
		return errors.New("invalid Microsoft OneDrive list-files properties")
	}
	return nil
}

func boundedTimezone(value string) bool {
	return value != "" && len(value) <= 128 && strings.TrimSpace(value) == value && !strings.ContainsAny(value, "\r\n\t")
}
