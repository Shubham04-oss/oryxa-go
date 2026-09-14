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
	microsoftMailSendScope       = "Mail.Send"
	microsoftMailReadScope       = "Mail.Read"
	microsoftMailReadWriteScope  = "Mail.ReadWrite"
	microsoftCalendarReadScope   = "Calendars.Read"
	microsoftCalendarWriteScope  = "Calendars.ReadWrite"
	microsoftFilesReadScope      = "Files.Read"
	microsoftFilesReadWriteScope = "Files.ReadWrite"
)

type microsoftActionSpec struct {
	Piece    string
	Version  string
	Runner   string
	Scopes   []string
	Validate func(json.RawMessage) error
	Prepare  func(json.RawMessage) (json.RawMessage, error)
}

var microsoftActions = map[string]microsoftActionSpec{
	"send-email": {
		MicrosoftOutlookPieceName, MicrosoftOutlookPieceVersion, "send-email",
		[]string{microsoftMailSendScope}, validateMicrosoftSendEmail, prepareFileAttachments,
	},
	"reply-email": {
		MicrosoftOutlookPieceName, MicrosoftOutlookPieceVersion, "reply-email",
		[]string{microsoftMailReadWriteScope, microsoftMailSendScope}, validateMicrosoftReplyEmail, prepareFileAttachments,
	},
	"create-draft-email": {
		MicrosoftOutlookPieceName, MicrosoftOutlookPieceVersion, "createDraftEmail",
		[]string{microsoftMailReadWriteScope}, validateMicrosoftSendEmail, prepareFileAttachments,
	},
	"send-draft-email": {
		MicrosoftOutlookPieceName, MicrosoftOutlookPieceVersion, "sendDraftEmail",
		[]string{microsoftMailSendScope}, validateMicrosoftMessageID, nil,
	},
	"forward-email": {
		MicrosoftOutlookPieceName, MicrosoftOutlookPieceVersion, "forwardEmail",
		[]string{microsoftMailReadWriteScope, microsoftMailSendScope}, validateMicrosoftForwardEmail, nil,
	},
	"add-label-to-email": {
		MicrosoftOutlookPieceName, MicrosoftOutlookPieceVersion, "addLabelToEmail",
		[]string{microsoftMailReadWriteScope}, validateMicrosoftEmailLabels, nil,
	},
	"remove-label-from-email": {
		MicrosoftOutlookPieceName, MicrosoftOutlookPieceVersion, "removeLabelFromEmail",
		[]string{microsoftMailReadWriteScope}, validateMicrosoftEmailLabels, nil,
	},
	"move-email-to-folder": {
		MicrosoftOutlookPieceName, MicrosoftOutlookPieceVersion, "moveEmailToFolder",
		[]string{microsoftMailReadWriteScope}, validateMicrosoftMoveEmail, nil,
	},
	"find-email": {
		MicrosoftOutlookPieceName, MicrosoftOutlookPieceVersion, "findEmail",
		[]string{microsoftMailReadScope}, validateMicrosoftFindEmail, nil,
	},
	"download-attachment": {
		MicrosoftOutlookPieceName, MicrosoftOutlookPieceVersion, "downloadAttachment",
		[]string{microsoftMailReadScope}, validateMicrosoftMessageID, nil,
	},
	"create-calendar-event": {
		MicrosoftCalendarPieceName, MicrosoftCalendarPieceVersion, "create_event",
		[]string{microsoftCalendarWriteScope}, validateMicrosoftCalendarCreate, nil,
	},
	"list-calendar-events": {
		MicrosoftCalendarPieceName, MicrosoftCalendarPieceVersion, "list_events",
		[]string{microsoftCalendarReadScope}, validateMicrosoftCalendarList, nil,
	},
	"delete-calendar-event": {
		MicrosoftCalendarPieceName, MicrosoftCalendarPieceVersion, "delete_event",
		[]string{microsoftCalendarWriteScope}, validateMicrosoftCalendarDelete, nil,
	},
	"list-files": {
		MicrosoftOneDrivePieceName, MicrosoftOneDrivePieceVersion, "list_files",
		[]string{microsoftFilesReadScope}, validateMicrosoftOneDriveList, nil,
	},
	"list-folders": {
		MicrosoftOneDrivePieceName, MicrosoftOneDrivePieceVersion, "list_folders",
		[]string{microsoftFilesReadScope}, validateMicrosoftOneDriveList, nil,
	},
	"copy-file": {
		MicrosoftOneDrivePieceName, MicrosoftOneDrivePieceVersion, "copy_file",
		[]string{microsoftFilesReadWriteScope}, validateMicrosoftCopyFile, nil,
	},
	"upload-onedrive-file": {
		MicrosoftOneDrivePieceName, MicrosoftOneDrivePieceVersion, "upload_onedrive_file",
		[]string{microsoftFilesReadWriteScope}, validateMicrosoftUploadFile, nil,
	},
	"download-file": {
		MicrosoftOneDrivePieceName, MicrosoftOneDrivePieceVersion, "download_file",
		[]string{microsoftFilesReadScope}, validateMicrosoftDownloadFile, nil,
	},
	"custom-api-call": {
		MicrosoftOutlookPieceName, MicrosoftOutlookPieceVersion, "custom_api_call",
		[]string{"User.Read"}, validateCustomAPICall("graph.microsoft.com"), prepareCustomAPICall,
	},
}

var microsoftNeedsFiles = map[string]bool{
	"send-email": true, "reply-email": true, "create-draft-email": true,
	"download-attachment": true, "upload-onedrive-file": true, "download-file": true,
}

func microsoftActionDefinition(actionName string) (actionflow.Definition, bool) {
	spec, ok := microsoftActions[actionName]
	if !ok {
		spec, ok = microsoftTeamsActions[actionName]
	}
	if !ok {
		spec, ok = microsoftSharePointActions[actionName]
	}
	if !ok {
		return actionflow.Definition{}, false
	}
	return actionflow.Definition{
		Provider: "microsoft", ActionName: actionName, PieceName: spec.Piece, PieceVersion: spec.Version,
		RunnerActionName: spec.Runner, RequiredScopes: append([]string(nil), spec.Scopes...),
		ValidateProps: spec.Validate, PrepareProps: spec.Prepare,
		NeedsFiles: microsoftNeedsFiles[actionName] || microsoftSharePointNeedsFiles[actionName],
	}, true
}

func validateMicrosoftSendEmail(raw json.RawMessage) error {
	var props struct {
		Recipients    []string  `json:"recipients"`
		CCRecipients  []string  `json:"ccRecipients"`
		BCCRecipients []string  `json:"bccRecipients"`
		Subject       string    `json:"subject"`
		BodyFormat    string    `json:"bodyFormat"`
		Body          string    `json:"body"`
		Attachments   []fileRef `json:"attachments"`
	}
	if err := decodeStrict(raw, &props); err != nil || !emails(props.Recipients, true) ||
		!emails(props.CCRecipients, false) || !emails(props.BCCRecipients, false) ||
		strings.TrimSpace(props.Subject) == "" || len(props.Subject) > 998 || len(props.Body) == 0 ||
		len(props.Body) > 100*1024 || !oneOf(props.BodyFormat, "text", "html") || !optionalFileRefs(props.Attachments) {
		return errors.New("invalid Microsoft Outlook send-email properties")
	}
	return nil
}

func validateMicrosoftReplyEmail(raw json.RawMessage) error {
	var props struct {
		MessageID     string    `json:"messageId"`
		ReplyBody     string    `json:"replyBody"`
		BodyFormat    string    `json:"bodyFormat"`
		Draft         bool      `json:"draft"`
		CCRecipients  []string  `json:"ccRecipients"`
		BCCRecipients []string  `json:"bccRecipients"`
		Attachments   []fileRef `json:"attachments"`
	}
	if err := decodeStrict(raw, &props); err != nil || !boundedValue(props.MessageID, 1024) ||
		len(props.ReplyBody) == 0 || len(props.ReplyBody) > 100*1024 || !oneOf(props.BodyFormat, "text", "html") ||
		!emails(props.CCRecipients, false) || !emails(props.BCCRecipients, false) || !optionalFileRefs(props.Attachments) {
		return errors.New("invalid Microsoft Outlook reply-email properties")
	}
	return nil
}

func validateMicrosoftMessageID(raw json.RawMessage) error {
	var props struct {
		MessageID string `json:"messageId"`
	}
	if err := decodeStrict(raw, &props); err != nil || !boundedValue(props.MessageID, 1024) {
		return errors.New("invalid Microsoft Outlook message identifier")
	}
	return nil
}

func validateMicrosoftForwardEmail(raw json.RawMessage) error {
	var props struct {
		MessageID  string   `json:"messageId"`
		Recipients []string `json:"recipients"`
		Comment    string   `json:"comment"`
	}
	if err := decodeStrict(raw, &props); err != nil || !boundedValue(props.MessageID, 1024) ||
		!emails(props.Recipients, true) || len(props.Comment) > 10*1024 {
		return errors.New("invalid Microsoft Outlook forward-email properties")
	}
	return nil
}

func validateMicrosoftEmailLabels(raw json.RawMessage) error {
	var props struct {
		MessageID  string   `json:"messageId"`
		Categories []string `json:"categories"`
	}
	if err := decodeStrict(raw, &props); err != nil || !boundedValue(props.MessageID, 1024) ||
		len(props.Categories) == 0 || !boundedNames(props.Categories, 25, 255) {
		return errors.New("invalid Microsoft Outlook email label properties")
	}
	return nil
}

func validateMicrosoftMoveEmail(raw json.RawMessage) error {
	var props struct {
		MessageID           string `json:"messageId"`
		DestinationFolderID string `json:"destinationFolderId"`
	}
	if err := decodeStrict(raw, &props); err != nil || !boundedValue(props.MessageID, 1024) ||
		!boundedValue(props.DestinationFolderID, 1024) {
		return errors.New("invalid Microsoft Outlook move-email properties")
	}
	return nil
}

func validateMicrosoftFindEmail(raw json.RawMessage) error {
	var props struct {
		SearchQuery string `json:"searchQuery"`
		FolderID    string `json:"folderId"`
		Top         int    `json:"top"`
	}
	if err := decodeStrict(raw, &props); err != nil || !microsoftSearchQuery(props.SearchQuery) ||
		(props.FolderID != "" && !boundedValue(props.FolderID, 1024)) ||
		props.Top < 0 || props.Top > 100 {
		return errors.New("invalid Microsoft Outlook find-email properties")
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

func validateMicrosoftCalendarDelete(raw json.RawMessage) error {
	var props struct {
		CalendarID string `json:"calendarId"`
		EventID    string `json:"eventId"`
	}
	if err := decodeStrict(raw, &props); err != nil || !boundedValue(props.CalendarID, 1024) ||
		!boundedValue(props.EventID, 1024) {
		return errors.New("invalid Microsoft Outlook Calendar delete-event properties")
	}
	return nil
}

func validateMicrosoftOneDriveList(raw json.RawMessage) error {
	var props struct {
		ParentFolder string `json:"parentFolder"`
	}
	if err := decodeStrict(raw, &props); err != nil || (props.ParentFolder != "" && !boundedValue(props.ParentFolder, 1024)) {
		return errors.New("invalid Microsoft OneDrive list properties")
	}
	return nil
}

func validateMicrosoftCopyFile(raw json.RawMessage) error {
	var props struct {
		FileID              string `json:"fileId"`
		DestinationFolderID string `json:"destinationFolderId"`
		NewName             string `json:"newName"`
		ConflictBehavior    string `json:"conflictBehavior"`
	}
	if err := decodeStrict(raw, &props); err != nil || !boundedValue(props.FileID, 1024) ||
		(props.DestinationFolderID != "" && !boundedValue(props.DestinationFolderID, 1024)) ||
		(props.NewName != "" && !boundedValue(props.NewName, 255)) {
		return errors.New("invalid Microsoft OneDrive copy-file properties")
	}
	if props.ConflictBehavior == "" {
		props.ConflictBehavior = "rename"
	}
	if !oneOf(props.ConflictBehavior, "rename", "replace", "fail") {
		return errors.New("invalid Microsoft OneDrive copy-file properties")
	}
	return nil
}

func validateMicrosoftUploadFile(raw json.RawMessage) error {
	var props struct {
		FileName string  `json:"fileName"`
		File     fileRef `json:"file"`
		ParentID string  `json:"parentId"`
	}
	if err := decodeStrict(raw, &props); err != nil || !boundedFilename(props.FileName) ||
		!oryxaFileID(props.File.FileID) || (props.ParentID != "" && !boundedValue(props.ParentID, 1024)) {
		return errors.New("invalid Microsoft OneDrive upload-onedrive-file properties")
	}
	return nil
}

func validateMicrosoftDownloadFile(raw json.RawMessage) error {
	var props struct {
		LookupBy       string `json:"lookupBy"`
		FileIdentifier string `json:"fileIdentifier"`
	}
	if err := decodeStrict(raw, &props); err != nil || !oneOf(props.LookupBy, "id", "name") ||
		!boundedValue(props.FileIdentifier, 1024) {
		return errors.New("invalid Microsoft OneDrive download-file properties")
	}
	return nil
}

func microsoftSearchQuery(value string) bool {
	return boundedValue(value, 512) && !strings.Contains(value, `"`)
}

func boundedTimezone(value string) bool {
	return value != "" && len(value) <= 128 && strings.TrimSpace(value) == value && !strings.ContainsAny(value, "\r\n\t")
}
