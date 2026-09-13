package catalog

import (
	"encoding/json"
	"errors"
	"net/mail"
	"strings"
	"time"

	"github.com/Shubham04-oss/oryxa-go/integrations/internal/actionflow"
)

const (
	googleGmailSendScope = "https://www.googleapis.com/auth/gmail.send"
	googleCalendarScope  = "https://www.googleapis.com/auth/calendar.events"
	googleDriveReadScope = "https://www.googleapis.com/auth/drive.metadata.readonly"
)

func googleActionDefinition(actionName string) (actionflow.Definition, bool) {
	definition := actionflow.Definition{Provider: "google", ActionName: actionName}
	switch actionName {
	case "send-email":
		definition.PieceName, definition.PieceVersion = GmailPieceName, GmailPieceVersion
		definition.RunnerActionName = "gmail_send_email"
		definition.RequiredScopes = []string{googleGmailSendScope}
		definition.ValidateProps = validateGmailSend
	case "create-calendar-event":
		definition.PieceName, definition.PieceVersion = GoogleCalendarPieceName, GoogleCalendarPieceVersion
		definition.RunnerActionName = "google_calendar_create_event"
		definition.RequiredScopes = []string{googleCalendarScope}
		definition.ValidateProps = validateGoogleCalendarCreate
	case "list-calendar-events":
		definition.PieceName, definition.PieceVersion = GoogleCalendarPieceName, GoogleCalendarPieceVersion
		definition.RunnerActionName = "google_calendar_list_events"
		definition.RequiredScopes = []string{googleCalendarScope}
		definition.ValidateProps = validateGoogleCalendarList
	case "list-drive-files":
		definition.PieceName, definition.PieceVersion = GoogleDrivePieceName, GoogleDrivePieceVersion
		definition.RunnerActionName = "drive_list_files"
		definition.RequiredScopes = []string{googleDriveReadScope}
		definition.ValidateProps = validateGoogleDriveList
	case "get-drive-file-metadata":
		definition.PieceName, definition.PieceVersion = GoogleDrivePieceName, GoogleDrivePieceVersion
		definition.RunnerActionName = "drive_get_file"
		definition.RequiredScopes = []string{googleDriveReadScope}
		definition.ValidateProps = validateGoogleDriveGet
	default:
		return actionflow.Definition{}, false
	}
	return definition, true
}

func validateGmailSend(raw json.RawMessage) error {
	var props struct {
		Receiver    []string `json:"receiver"`
		CC          []string `json:"cc"`
		BCC         []string `json:"bcc"`
		Subject     string   `json:"subject"`
		BodyType    string   `json:"body_type"`
		Body        string   `json:"body"`
		ReplyTo     []string `json:"reply_to"`
		SenderName  string   `json:"sender_name"`
		From        string   `json:"from"`
		Attachments []any    `json:"attachments"`
		InReplyTo   string   `json:"in_reply_to"`
		Draft       bool     `json:"draft"`
	}
	if err := decodeStrict(raw, &props); err != nil || !emails(props.Receiver, true) || !emails(props.CC, false) ||
		!emails(props.BCC, false) || !emails(props.ReplyTo, false) || strings.TrimSpace(props.Subject) == "" ||
		len(props.Subject) > 998 || len(props.Body) == 0 || len(props.Body) > 100*1024 ||
		!oneOf(props.BodyType, "plain_text", "html") || len(props.SenderName) > 200 || len(props.InReplyTo) > 998 ||
		len(props.Attachments) != 0 || props.Draft || (props.From != "" && !email(props.From)) {
		return errors.New("invalid Gmail send-email properties")
	}
	return nil
}

func validateGoogleCalendarCreate(raw json.RawMessage) error {
	var props struct {
		CalendarID              string   `json:"calendar_id"`
		Title                   string   `json:"title"`
		Start                   string   `json:"start_date_time"`
		End                     string   `json:"end_date_time"`
		Location                string   `json:"location"`
		Description             string   `json:"description"`
		ColorID                 string   `json:"colorId"`
		Attendees               []string `json:"attendees"`
		GuestsCanModify         bool     `json:"guests_can_modify"`
		GuestsCanInviteOthers   bool     `json:"guests_can_invite_others"`
		GuestsCanSeeOtherGuests bool     `json:"guests_can_see_other_guests"`
		SendNotifications       string   `json:"send_notifications"`
		CreateMeetLink          bool     `json:"create_meet_link"`
	}
	if err := decodeStrict(raw, &props); err != nil {
		return errors.New("invalid Google Calendar create-event properties")
	}
	start, startErr := time.Parse(time.RFC3339, props.Start)
	var end time.Time
	var endErr error
	if props.End != "" {
		end, endErr = time.Parse(time.RFC3339, props.End)
	}
	if !boundedValue(props.CalendarID, 1024) || strings.TrimSpace(props.Title) == "" || len(props.Title) > 1024 ||
		startErr != nil || endErr != nil || (!end.IsZero() && !end.After(start)) || len(props.Location) > 1024 ||
		len(props.Description) > 32*1024 || !emails(props.Attendees, false) || len(props.Attendees) > 200 ||
		!oneOf(props.SendNotifications, "all", "externalOnly", "none") || len(props.ColorID) > 20 {
		return errors.New("invalid Google Calendar create-event properties")
	}
	return nil
}

func validateGoogleCalendarList(raw json.RawMessage) error {
	var props struct {
		CalendarID   string   `json:"calendar_id"`
		EventTypes   []string `json:"event_types"`
		Search       string   `json:"search"`
		Start        string   `json:"start_date"`
		End          string   `json:"end_date"`
		SingleEvents bool     `json:"singleEvents"`
	}
	if err := decodeStrict(raw, &props); err != nil || !boundedValue(props.CalendarID, 1024) || len(props.Search) > 1024 ||
		!optionalRFC3339(props.Start) || !optionalRFC3339(props.End) || len(props.EventTypes) == 0 || len(props.EventTypes) > 4 {
		return errors.New("invalid Google Calendar list-events properties")
	}
	for _, eventType := range props.EventTypes {
		if !oneOf(eventType, "default", "focusTime", "outOfOffice", "workingLocation") {
			return errors.New("invalid Google Calendar event type")
		}
	}
	return nil
}

func validateGoogleDriveList(raw json.RawMessage) error {
	var props struct {
		FolderID          string `json:"folder_id"`
		IncludeTrashed    bool   `json:"include_trashed"`
		DepthLevel        int    `json:"depth_level"`
		DownloadFiles     bool   `json:"download_files"`
		IncludeTeamDrives bool   `json:"include_team_drives"`
	}
	if err := decodeStrict(raw, &props); err != nil || !boundedValue(props.FolderID, 1024) ||
		props.DepthLevel < 0 || props.DepthLevel > 3 || props.DownloadFiles {
		return errors.New("invalid Google Drive list-files properties")
	}
	return nil
}

func validateGoogleDriveGet(raw json.RawMessage) error {
	var props struct {
		FileID string `json:"file_id"`
	}
	if err := decodeStrict(raw, &props); err != nil || !boundedValue(props.FileID, 1024) {
		return errors.New("invalid Google Drive get-file properties")
	}
	return nil
}

func emails(values []string, required bool) bool {
	if (required && len(values) == 0) || len(values) > 50 {
		return false
	}
	for _, value := range values {
		if !email(value) {
			return false
		}
	}
	return true
}

func email(value string) bool {
	address, err := mail.ParseAddress(value)
	return err == nil && address.Address == value && len(value) <= 320
}

func optionalRFC3339(value string) bool {
	if value == "" {
		return true
	}
	_, err := time.Parse(time.RFC3339, value)
	return err == nil
}

func boundedValue(value string, maximum int) bool {
	return value != "" && len(value) <= maximum && strings.TrimSpace(value) == value && !strings.ContainsAny(value, "\r\n\t")
}
