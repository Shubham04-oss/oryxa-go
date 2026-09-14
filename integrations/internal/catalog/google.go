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
	googleGmailSendScope     = "https://www.googleapis.com/auth/gmail.send"
	googleGmailMetadataScope = "https://www.googleapis.com/auth/gmail.metadata"
	googleGmailReadScope     = "https://www.googleapis.com/auth/gmail.readonly"
	googleGmailComposeScope  = "https://www.googleapis.com/auth/gmail.compose"
	googleGmailModifyScope   = "https://www.googleapis.com/auth/gmail.modify"
	googleCalendarScope      = "https://www.googleapis.com/auth/calendar.events"
	googleCalendarReadScope  = "https://www.googleapis.com/auth/calendar.readonly"
	googleDriveReadScope     = "https://www.googleapis.com/auth/drive.metadata.readonly"
	googleDriveFileReadScope = "https://www.googleapis.com/auth/drive.readonly"
	googleDriveWriteScope    = "https://www.googleapis.com/auth/drive"
)

type googleActionSpec struct {
	Piece    string
	Version  string
	Runner   string
	Scopes   []string
	Validate func(json.RawMessage) error
	Prepare  func(json.RawMessage) (json.RawMessage, error)
}

var (
	googleGmailSend     = []string{googleGmailSendScope}
	googleGmailRead     = []string{googleGmailReadScope}
	googleGmailCompose  = []string{googleGmailComposeScope}
	googleGmailModify   = []string{googleGmailModifyScope}
	googleCalWrite      = []string{googleCalendarScope}
	googleCalRead       = []string{googleCalendarReadScope}
	googleDriveRead     = []string{googleDriveReadScope}
	googleDriveFileRead = []string{googleDriveFileReadScope}
	googleDriveWrite    = []string{googleDriveWriteScope}
)

var googleDriveExportMIME = []string{
	"application/vnd.openxmlformats-officedocument.wordprocessingml.document",
	"application/pdf", "text/html", "text/plain", "application/rtf",
	"application/vnd.oasis.opendocument.text", "application/epub+zip", "text/markdown",
	"application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
	"text/csv", "text/tab-separated-values", "application/vnd.oasis.opendocument.spreadsheet",
	"application/vnd.openxmlformats-officedocument.presentationml.presentation",
	"application/vnd.oasis.opendocument.presentation", "image/png", "image/svg+xml",
}

var googleActions = map[string]googleActionSpec{
	"send-email": {
		GmailPieceName, GmailPieceVersion, "gmail_send_email", googleGmailSend, validateGmailSend, prepareFileAttachments,
	},
	"reply-email": {
		GmailPieceName, GmailPieceVersion, "gmail_reply_to_thread", googleGmailModify, validateGmailReply, nil,
	},
	"create-draft-email": {
		GmailPieceName, GmailPieceVersion, "gmail_create_draft", googleGmailCompose, validateGmailCreateDraft, prepareFileAttachments,
	},
	"update-draft-email": {
		GmailPieceName, GmailPieceVersion, "gmail_update_draft", googleGmailCompose, validateGmailUpdateDraft, prepareFileAttachments,
	},
	"send-draft-email": {
		GmailPieceName, GmailPieceVersion, "gmail_send_draft", googleGmailSend, validateGmailDraftID, nil,
	},
	"get-draft-email": {
		GmailPieceName, GmailPieceVersion, "gmail_get_draft", googleGmailRead, validateGmailDraftID, nil,
	},
	"list-drafts": {GmailPieceName, GmailPieceVersion, "gmail_list_drafts", googleGmailRead, validateGmailListDrafts, nil},
	"delete-draft-email": {
		GmailPieceName, GmailPieceVersion, "gmail_delete_draft", googleGmailCompose, validateGmailDraftID, nil,
	},
	"get-thread": {GmailPieceName, GmailPieceVersion, "gmail_get_thread", googleGmailRead, validateGmailGetThread, nil},
	"list-threads": {
		GmailPieceName, GmailPieceVersion, "gmail_list_threads", googleGmailRead, validateGmailListThreads, nil,
	},
	"forward-email": {
		GmailPieceName, GmailPieceVersion, "gmail_forward_message", googleGmailModify, validateGmailForward, nil,
	},
	"list-labels": {GmailPieceName, GmailPieceVersion, "gmail_list_labels", googleGmailRead, validateGoogleEmpty, nil},
	"get-label":   {GmailPieceName, GmailPieceVersion, "gmail_get_label", googleGmailRead, validateGmailLabelID, nil},
	"create-label": {
		GmailPieceName, GmailPieceVersion, "gmail_create_label", googleGmailModify, validateGmailCreateLabel, nil,
	},
	"add-label-to-email": {
		GmailPieceName, GmailPieceVersion, "gmail_add_label_to_email", googleGmailModify,
		validateGmailMessageLabel, prepareGmailLabelID,
	},
	"remove-label-from-email": {
		GmailPieceName, GmailPieceVersion, "gmail_remove_label_from_email", googleGmailModify,
		validateGmailMessageLabel, prepareGmailLabelID,
	},
	"archive-email": {
		GmailPieceName, GmailPieceVersion, "gmail_archive_email", googleGmailModify, validateGmailMessageID, nil,
	},
	"get-profile":  {GmailPieceName, GmailPieceVersion, "gmail_get_profile", googleGmailRead, validateGoogleEmpty, nil},
	"list-history": {GmailPieceName, GmailPieceVersion, "gmail_list_history", googleGmailRead, validateGmailHistory, nil},

	"create-calendar-event": {
		GoogleCalendarPieceName, GoogleCalendarPieceVersion, "google_calendar_create_event",
		googleCalWrite, validateGoogleCalendarCreate, nil,
	},
	"list-calendar-events": {
		GoogleCalendarPieceName, GoogleCalendarPieceVersion, "google_calendar_list_events",
		googleCalRead, validateGoogleCalendarList, nil,
	},
	"update-calendar-event": {
		GoogleCalendarPieceName, GoogleCalendarPieceVersion, "google_calendar_update_event",
		googleCalWrite, validateGoogleCalendarUpdate, nil,
	},
	"delete-calendar-event": {
		GoogleCalendarPieceName, GoogleCalendarPieceVersion, "google_calendar_delete_event",
		googleCalWrite, validateGoogleCalendarEventID, nil,
	},
	"get-calendar-event": {
		GoogleCalendarPieceName, GoogleCalendarPieceVersion, "google_calendar_get_event",
		googleCalRead, validateGoogleCalendarGetEvent, nil,
	},
	"add-calendar-attendees": {
		GoogleCalendarPieceName, GoogleCalendarPieceVersion, "google-calendar-add-attendees",
		googleCalWrite, validateGoogleCalendarAddAttendees, prepareGoogleCalendarEventID,
	},
	"remove-calendar-attendee": {
		GoogleCalendarPieceName, GoogleCalendarPieceVersion, "google_calendar_remove_attendee",
		googleCalWrite, validateGoogleCalendarRemoveAttendee, nil,
	},
	"create-quick-event": {
		GoogleCalendarPieceName, GoogleCalendarPieceVersion, "create_quick_event",
		googleCalWrite, validateGoogleCalendarQuick, nil,
	},
	"find-busy-periods": {
		GoogleCalendarPieceName, GoogleCalendarPieceVersion, "google_calendar_find_busy_periods",
		googleCalRead, validateGoogleCalendarBusy, nil,
	},
	"find-free-slots": {
		GoogleCalendarPieceName, GoogleCalendarPieceVersion, "google_calendar_find_free_slots",
		googleCalRead, validateGoogleCalendarFreeSlots, nil,
	},
	"search-calendar-events": {
		GoogleCalendarPieceName, GoogleCalendarPieceVersion, "google_calendar_search_events_all_calendars",
		googleCalRead, validateGoogleCalendarSearch, nil,
	},
	"list-recurring-event-instances": {
		GoogleCalendarPieceName, GoogleCalendarPieceVersion, "google_calendar_list_recurring_event_instances",
		googleCalRead, validateGoogleCalendarRecurring, nil,
	},
	"move-calendar-event": {
		GoogleCalendarPieceName, GoogleCalendarPieceVersion, "google_calendar_move_event",
		googleCalWrite, validateGoogleCalendarMove, nil,
	},
	"import-calendar-event": {
		GoogleCalendarPieceName, GoogleCalendarPieceVersion, "google_calendar_import_event",
		googleCalWrite, validateGoogleCalendarImport, nil,
	},
	"list-calendars": {
		GoogleCalendarPieceName, GoogleCalendarPieceVersion, "google_calendar_list_calendars",
		googleCalRead, validateGoogleEmpty, nil,
	},
	"get-calendar": {
		GoogleCalendarPieceName, GoogleCalendarPieceVersion, "google_calendar_get_calendar",
		googleCalRead, validateGoogleCalendarID, nil,
	},
	"get-calendar-colors": {
		GoogleCalendarPieceName, GoogleCalendarPieceVersion, "google_calendar_get_colors",
		googleCalRead, validateGoogleEmpty, nil,
	},
	"list-calendar-settings": {
		GoogleCalendarPieceName, GoogleCalendarPieceVersion, "google_calendar_list_settings",
		googleCalRead, validateGoogleEmpty, nil,
	},

	"list-drive-files": {
		GoogleDrivePieceName, GoogleDrivePieceVersion, "drive_list_files", googleDriveRead, validateGoogleDriveList, nil,
	},
	"get-drive-file-metadata": {
		GoogleDrivePieceName, GoogleDrivePieceVersion, "drive_get_file", googleDriveRead, validateGoogleDriveGet, nil,
	},
	"create-drive-folder": {
		GoogleDrivePieceName, GoogleDrivePieceVersion, "drive_create_folder", googleDriveWrite, validateGoogleDriveFolder, nil,
	},
	"search-drive-files": {
		GoogleDrivePieceName, GoogleDrivePieceVersion, "drive_search_files", googleDriveRead, validateGoogleDriveSearch, nil,
	},
	"copy-drive-file": {
		GoogleDrivePieceName, GoogleDrivePieceVersion, "drive_copy_file", googleDriveWrite, validateGoogleDriveCopy, nil,
	},
	"move-drive-file": {
		GoogleDrivePieceName, GoogleDrivePieceVersion, "drive_move_file", googleDriveWrite, validateGoogleDriveMove, nil,
	},
	"update-drive-file-metadata": {
		GoogleDrivePieceName, GoogleDrivePieceVersion, "drive_update_file_metadata",
		googleDriveWrite, validateGoogleDriveUpdateMetadata, nil,
	},
	"trash-drive-file": {
		GoogleDrivePieceName, GoogleDrivePieceVersion, "drive_trash_file", googleDriveWrite, validateGoogleDriveGet, nil,
	},
	"untrash-drive-file": {
		GoogleDrivePieceName, GoogleDrivePieceVersion, "drive_untrash_file", googleDriveWrite, validateGoogleDriveGet, nil,
	},
	"delete-drive-file": {
		GoogleDrivePieceName, GoogleDrivePieceVersion, "drive_delete_file", googleDriveWrite, validateGoogleDriveGet, nil,
	},
	"share-drive-file": {
		GoogleDrivePieceName, GoogleDrivePieceVersion, "drive_share_file", googleDriveWrite, validateGoogleDriveShare, nil,
	},
	"list-drive-permissions": {
		GoogleDrivePieceName, GoogleDrivePieceVersion, "drive_list_permissions", googleDriveWrite, validateGoogleDriveGet, nil,
	},
	"update-drive-permission": {
		GoogleDrivePieceName, GoogleDrivePieceVersion, "drive_update_permission",
		googleDriveWrite, validateGoogleDriveUpdatePermission, nil,
	},
	"remove-drive-permission": {
		GoogleDrivePieceName, GoogleDrivePieceVersion, "drive_remove_permission",
		googleDriveWrite, validateGoogleDriveRemovePermission, nil,
	},
	"list-shared-drives": {
		GoogleDrivePieceName, GoogleDrivePieceVersion, "drive_list_shared_drives",
		googleDriveWrite, validateGoogleDriveListShared, nil,
	},
	"get-shared-drive": {
		GoogleDrivePieceName, GoogleDrivePieceVersion, "drive_get_shared_drive",
		googleDriveWrite, validateGoogleDriveSharedID, nil,
	},
	"create-shared-drive": {
		GoogleDrivePieceName, GoogleDrivePieceVersion, "drive_create_shared_drive",
		googleDriveWrite, validateGoogleDriveSharedName, nil,
	},
	"update-shared-drive": {
		GoogleDrivePieceName, GoogleDrivePieceVersion, "drive_update_shared_drive",
		googleDriveWrite, validateGoogleDriveUpdateShared, nil,
	},
	"create-drive-comment": {
		GoogleDrivePieceName, GoogleDrivePieceVersion, "drive_create_comment",
		googleDriveWrite, validateGoogleDriveComment, nil,
	},
	"list-drive-comments": {
		GoogleDrivePieceName, GoogleDrivePieceVersion, "drive_list_comments", googleDriveWrite, validateGoogleDriveGet, nil,
	},
	"create-drive-reply": {
		GoogleDrivePieceName, GoogleDrivePieceVersion, "drive_create_reply", googleDriveWrite, validateGoogleDriveReply, nil,
	},
	"get-drive-reply": {
		GoogleDrivePieceName, GoogleDrivePieceVersion, "drive_get_reply", googleDriveWrite, validateGoogleDriveReplyID, nil,
	},
	"list-drive-replies": {
		GoogleDrivePieceName, GoogleDrivePieceVersion, "drive_list_replies",
		googleDriveWrite, validateGoogleDriveCommentID, nil,
	},
	"update-drive-reply": {
		GoogleDrivePieceName, GoogleDrivePieceVersion, "drive_update_reply",
		googleDriveWrite, validateGoogleDriveUpdateReply, nil,
	},
	"delete-drive-reply": {
		GoogleDrivePieceName, GoogleDrivePieceVersion, "drive_delete_reply",
		googleDriveWrite, validateGoogleDriveReplyID, nil,
	},
	"get-drive-about": {
		GoogleDrivePieceName, GoogleDrivePieceVersion, "drive_get_about", googleDriveRead, validateGoogleEmpty, nil,
	},
	"upload-drive-file": {
		GoogleDrivePieceName, GoogleDrivePieceVersion, "drive_upload_file", googleDriveWrite, validateGoogleDriveUpload, nil,
	},
	"replace-drive-file": {
		GoogleDrivePieceName, GoogleDrivePieceVersion, "drive_replace_file_content",
		googleDriveWrite, validateGoogleDriveReplace, nil,
	},
	"download-drive-file": {
		GoogleDrivePieceName, GoogleDrivePieceVersion, "drive_download_file", googleDriveFileRead, validateGoogleDriveDownload, nil,
	},
	"export-drive-file": {
		GoogleDrivePieceName, GoogleDrivePieceVersion, "drive_export_workspace_file",
		googleDriveFileRead, validateGoogleDriveExport, nil,
	},
	"create-file-from-text": {
		GoogleDrivePieceName, GoogleDrivePieceVersion, "drive_create_file_from_text",
		googleDriveWrite, validateGoogleDriveCreateFromText, nil,
	},
	"save-file-as-pdf": {
		GoogleDrivePieceName, GoogleDrivePieceVersion, "drive_save_file_as_pdf",
		googleDriveWrite, validateGoogleDriveSavePDF, nil,
	},
	"set-drive-public-access": {
		GoogleDrivePieceName, GoogleDrivePieceVersion, "drive_set_public_access",
		googleDriveWrite, validateGoogleDrivePublicAccess, nil,
	},
	"empty-trash": {
		GoogleDrivePieceName, GoogleDrivePieceVersion, "drive_empty_trash",
		googleDriveWrite, validateGoogleDriveEmptyTrash, nil,
	},
	"delete-shared-drive": {
		GoogleDrivePieceName, GoogleDrivePieceVersion, "drive_delete_shared_drive",
		googleDriveWrite, validateGoogleDriveSharedID, nil,
	},
	"get-email-attachment": {
		GmailPieceName, GmailPieceVersion, "gmail_get_attachment", googleGmailRead, validateGmailGetAttachment, nil,
	},
	"get-email": {
		GmailPieceName, GmailPieceVersion, "gmail_get_message", googleGmailRead, validateGmailGetEmail, nil,
	},
	"find-email": {
		GmailPieceName, GmailPieceVersion, "gmail_search_email", googleGmailRead, validateGmailFindEmail, prepareGmailSearchLabel,
	},
	"upload-from-url": {
		GoogleDrivePieceName, GoogleDrivePieceVersion, "drive_upload_from_url", googleDriveWrite, validateGoogleDriveUploadFromURL, nil,
	},
	"gmail-custom-api-call": {
		GmailPieceName, GmailPieceVersion, "custom_api_call", googleGmailRead, validateCustomAPICall("gmail.googleapis.com"), prepareCustomAPICall,
	},
	"calendar-custom-api-call": {
		GoogleCalendarPieceName, GoogleCalendarPieceVersion, "custom_api_call", googleCalRead, validateCustomAPICall("www.googleapis.com"), prepareCustomAPICall,
	},
	"drive-custom-api-call": {
		GoogleDrivePieceName, GoogleDrivePieceVersion, "custom_api_call", googleDriveFileRead, validateCustomAPICall("www.googleapis.com"), prepareCustomAPICall,
	},
}

var googleNeedsFiles = map[string]bool{
	"send-email": true, "reply-email": true, "create-draft-email": true, "update-draft-email": true,
	"get-email-attachment": true, "get-email": true, "find-email": true,
	"upload-drive-file": true, "replace-drive-file": true,
	"download-drive-file": true, "export-drive-file": true, "set-drive-public-access": true,
}

func googleActionDefinition(actionName string) (actionflow.Definition, bool) {
	spec, ok := googleActions[actionName]
	if !ok {
		spec, ok = googleDocsActions[actionName]
	}
	if !ok {
		spec, ok = googleSheetsActions[actionName]
	}
	if !ok {
		return actionflow.Definition{}, false
	}
	return actionflow.Definition{
		Provider: "google", ActionName: actionName, PieceName: spec.Piece, PieceVersion: spec.Version,
		RunnerActionName: spec.Runner, RequiredScopes: append([]string(nil), spec.Scopes...),
		ValidateProps: spec.Validate, PrepareProps: spec.Prepare,
		NeedsFiles: googleNeedsFiles[actionName] || googleDocsNeedsFiles[actionName] || googleSheetsNeedsFiles[actionName],
	}, true
}

func validateGmailSend(raw json.RawMessage) error {
	var props struct {
		Receiver    []string  `json:"receiver"`
		CC          []string  `json:"cc"`
		BCC         []string  `json:"bcc"`
		Subject     string    `json:"subject"`
		BodyType    string    `json:"body_type"`
		Body        string    `json:"body"`
		ReplyTo     []string  `json:"reply_to"`
		SenderName  string    `json:"sender_name"`
		From        string    `json:"from"`
		Attachments []fileRef `json:"attachments"`
		InReplyTo   string    `json:"in_reply_to"`
		Draft       bool      `json:"draft"`
	}
	if err := decodeStrict(raw, &props); err != nil || !emails(props.Receiver, true) || !emails(props.CC, false) ||
		!emails(props.BCC, false) || !emails(props.ReplyTo, false) || strings.TrimSpace(props.Subject) == "" ||
		len(props.Subject) > 998 || len(props.Body) == 0 || len(props.Body) > 100*1024 ||
		!oneOf(props.BodyType, "plain_text", "html") || len(props.SenderName) > 200 || len(props.InReplyTo) > 998 ||
		!optionalFileRefs(props.Attachments) || props.Draft || (props.From != "" && !email(props.From)) {
		return errors.New("invalid Gmail send-email properties")
	}
	return nil
}

func validateGmailReply(raw json.RawMessage) error {
	var props struct {
		MessageID      string   `json:"message_id"`
		ReplyType      string   `json:"reply_type"`
		BodyType       string   `json:"body_type"`
		Body           string   `json:"body"`
		SenderName     string   `json:"sender_name"`
		Attachment     *fileRef `json:"attachment"`
		AttachmentName string   `json:"attachment_name"`
	}
	if err := decodeStrict(raw, &props); err != nil || !boundedValue(props.MessageID, 1024) ||
		!oneOf(props.ReplyType, "reply", "reply_all") || !oneOf(props.BodyType, "plain_text", "html") ||
		len(props.Body) == 0 || len(props.Body) > 100*1024 || len(props.SenderName) > 200 ||
		!optionalFileRef(props.Attachment) || (props.AttachmentName != "" && (len(props.AttachmentName) > 255 ||
		strings.ContainsAny(props.AttachmentName, "\r\n/\\"))) {
		return errors.New("invalid Gmail reply-email properties")
	}
	return nil
}

func validateGmailCreateDraft(raw json.RawMessage) error {
	return validateGmailDraftFields(raw, false)
}

func validateGmailUpdateDraft(raw json.RawMessage) error {
	return validateGmailDraftFields(raw, true)
}

func validateGmailDraftFields(raw json.RawMessage, requireID bool) error {
	var props struct {
		DraftID     string    `json:"draft_id"`
		Receiver    []string  `json:"receiver"`
		CC          []string  `json:"cc"`
		BCC         []string  `json:"bcc"`
		Subject     string    `json:"subject"`
		BodyType    string    `json:"body_type"`
		Body        string    `json:"body"`
		SenderName  string    `json:"sender_name"`
		From        string    `json:"from"`
		ThreadID    string    `json:"thread_id"`
		Attachments []fileRef `json:"attachments"`
	}
	if err := decodeStrict(raw, &props); err != nil || (requireID && !boundedValue(props.DraftID, 1024)) ||
		(!requireID && props.DraftID != "") || !emails(props.Receiver, false) || !emails(props.CC, false) ||
		!emails(props.BCC, false) || strings.TrimSpace(props.Subject) == "" || len(props.Subject) > 998 ||
		len(props.Body) == 0 || len(props.Body) > 100*1024 || !oneOf(props.BodyType, "plain_text", "html") ||
		len(props.SenderName) > 200 || (props.ThreadID != "" && !boundedValue(props.ThreadID, 1024)) ||
		!optionalFileRefs(props.Attachments) || (props.From != "" && !email(props.From)) {
		return errors.New("invalid Gmail draft properties")
	}
	return nil
}

func validateGmailDraftID(raw json.RawMessage) error {
	var props struct {
		DraftID string `json:"draft_id"`
	}
	if err := decodeStrict(raw, &props); err != nil || !boundedValue(props.DraftID, 1024) {
		return errors.New("invalid Gmail draft identifier")
	}
	return nil
}

func validateGmailListDrafts(raw json.RawMessage) error {
	var props struct {
		Query      string `json:"query"`
		MaxResults int    `json:"max_results"`
	}
	if err := decodeStrict(raw, &props); err != nil || len(props.Query) > 2048 ||
		strings.ContainsAny(props.Query, "\r\n") || !googleMaxResults(props.MaxResults) {
		return errors.New("invalid Gmail list-drafts properties")
	}
	return nil
}

func validateGmailGetThread(raw json.RawMessage) error {
	var props struct {
		ThreadID string `json:"thread_id"`
		Format   string `json:"format"`
	}
	if err := decodeStrict(raw, &props); err != nil || !boundedValue(props.ThreadID, 1024) ||
		!oneOf(props.Format, "metadata", "minimal") {
		return errors.New("invalid Gmail get-thread properties")
	}
	return nil
}

func validateGmailListThreads(raw json.RawMessage) error {
	var props struct {
		Query            string   `json:"query"`
		LabelIDs         []string `json:"label_ids"`
		IncludeSpamTrash bool     `json:"include_spam_trash"`
		MaxResults       int      `json:"max_results"`
	}
	if err := decodeStrict(raw, &props); err != nil || len(props.Query) > 2048 ||
		strings.ContainsAny(props.Query, "\r\n") || !boundedNames(props.LabelIDs, 20, 256) ||
		!googleMaxResults(props.MaxResults) {
		return errors.New("invalid Gmail list-threads properties")
	}
	return nil
}

func validateGmailForward(raw json.RawMessage) error {
	var props struct {
		MessageID  string   `json:"message_id"`
		Receiver   []string `json:"receiver"`
		CC         []string `json:"cc"`
		BCC        []string `json:"bcc"`
		Note       string   `json:"note"`
		SenderName string   `json:"sender_name"`
	}
	if err := decodeStrict(raw, &props); err != nil || !boundedValue(props.MessageID, 1024) ||
		!emails(props.Receiver, true) || !emails(props.CC, false) || !emails(props.BCC, false) ||
		len(props.Note) > 10*1024 || len(props.SenderName) > 200 {
		return errors.New("invalid Gmail forward-email properties")
	}
	return nil
}

func validateGmailCreateLabel(raw json.RawMessage) error {
	var props struct {
		Name string `json:"name"`
	}
	if err := decodeStrict(raw, &props); err != nil || strings.TrimSpace(props.Name) == "" || len(props.Name) > 225 {
		return errors.New("invalid Gmail create-label properties")
	}
	return nil
}

func validateGmailLabelID(raw json.RawMessage) error {
	var props struct {
		LabelID string `json:"label_id"`
	}
	if err := decodeStrict(raw, &props); err != nil || !boundedValue(props.LabelID, 256) {
		return errors.New("invalid Gmail label identifier")
	}
	return nil
}

func validateGmailMessageLabel(raw json.RawMessage) error {
	var props struct {
		MessageID string `json:"message_id"`
		Label     string `json:"label"`
	}
	if err := decodeStrict(raw, &props); err != nil || !boundedValue(props.MessageID, 1024) ||
		!boundedValue(props.Label, 256) {
		return errors.New("invalid Gmail email label properties")
	}
	return nil
}

func validateGmailMessageID(raw json.RawMessage) error {
	var props struct {
		MessageID string `json:"message_id"`
	}
	if err := decodeStrict(raw, &props); err != nil || !boundedValue(props.MessageID, 1024) {
		return errors.New("invalid Gmail message identifier")
	}
	return nil
}

func validateGmailHistory(raw json.RawMessage) error {
	var props struct {
		StartHistoryID string   `json:"start_history_id"`
		HistoryTypes   []string `json:"history_types"`
		MaxResults     int      `json:"max_results"`
	}
	if err := decodeStrict(raw, &props); err != nil || !boundedValue(props.StartHistoryID, 256) ||
		len(props.HistoryTypes) > 4 || !googleMaxResults(props.MaxResults) {
		return errors.New("invalid Gmail list-history properties")
	}
	for _, historyType := range props.HistoryTypes {
		if !oneOf(historyType, "messageAdded", "messageDeleted", "labelAdded", "labelRemoved") {
			return errors.New("invalid Gmail list-history properties")
		}
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

func validateGoogleCalendarUpdate(raw json.RawMessage) error {
	var props struct {
		CalendarID              string   `json:"calendar_id"`
		EventID                 string   `json:"event_id"`
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
	}
	if err := decodeStrict(raw, &props); err != nil || !boundedValue(props.CalendarID, 1024) ||
		!boundedValue(props.EventID, 1024) || len(props.Title) > 1024 || !optionalRFC3339(props.Start) ||
		!optionalRFC3339(props.End) || len(props.Location) > 1024 || len(props.Description) > 32*1024 ||
		!emails(props.Attendees, false) || len(props.Attendees) > 200 || len(props.ColorID) > 20 {
		return errors.New("invalid Google Calendar update-event properties")
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

func validateGoogleCalendarEventID(raw json.RawMessage) error {
	var props struct {
		CalendarID string `json:"calendar_id"`
		EventID    string `json:"event_id"`
	}
	if err := decodeStrict(raw, &props); err != nil || !boundedValue(props.CalendarID, 1024) ||
		!boundedValue(props.EventID, 1024) {
		return errors.New("invalid Google Calendar event identifier")
	}
	return nil
}

func validateGoogleCalendarGetEvent(raw json.RawMessage) error {
	var props struct {
		CalendarID   string `json:"calendar_id"`
		EventID      string `json:"event_id"`
		MaxAttendees int    `json:"max_attendees"`
		TimeZone     string `json:"time_zone"`
	}
	if err := decodeStrict(raw, &props); err != nil || !boundedValue(props.CalendarID, 1024) ||
		!boundedValue(props.EventID, 1024) || props.MaxAttendees < 0 || props.MaxAttendees > 100 ||
		(props.TimeZone != "" && (len(props.TimeZone) > 128 || strings.ContainsAny(props.TimeZone, "\r\n\t"))) {
		return errors.New("invalid Google Calendar get-event properties")
	}
	return nil
}

func validateGoogleCalendarAddAttendees(raw json.RawMessage) error {
	var props struct {
		CalendarID string   `json:"calendar_id"`
		EventID    string   `json:"event_id"`
		Attendees  []string `json:"attendees"`
	}
	if err := decodeStrict(raw, &props); err != nil || !boundedValue(props.CalendarID, 1024) ||
		!boundedValue(props.EventID, 1024) || !emails(props.Attendees, true) || len(props.Attendees) > 200 {
		return errors.New("invalid Google Calendar add-attendees properties")
	}
	return nil
}

func validateGoogleCalendarRemoveAttendee(raw json.RawMessage) error {
	var props struct {
		CalendarID    string `json:"calendar_id"`
		EventID       string `json:"event_id"`
		AttendeeEmail string `json:"attendee_email"`
		SendUpdates   string `json:"send_updates"`
	}
	if err := decodeStrict(raw, &props); err != nil || !boundedValue(props.CalendarID, 1024) ||
		!boundedValue(props.EventID, 1024) || !email(props.AttendeeEmail) ||
		(props.SendUpdates != "" && !oneOf(props.SendUpdates, "all", "externalOnly", "none")) {
		return errors.New("invalid Google Calendar remove-attendee properties")
	}
	return nil
}

func validateGoogleCalendarQuick(raw json.RawMessage) error {
	var props struct {
		CalendarID  string `json:"calendar_id"`
		Text        string `json:"text"`
		SendUpdates string `json:"send_updates"`
	}
	if err := decodeStrict(raw, &props); err != nil || !boundedValue(props.CalendarID, 1024) ||
		strings.TrimSpace(props.Text) == "" || len(props.Text) > 8*1024 ||
		(props.SendUpdates != "" && !oneOf(props.SendUpdates, "all", "externalOnly", "none")) {
		return errors.New("invalid Google Calendar create-quick-event properties")
	}
	return nil
}

func validateGoogleCalendarBusy(raw json.RawMessage) error {
	var props struct {
		CalendarIDs []string `json:"calendar_ids"`
		Start       string   `json:"start_date"`
		End         string   `json:"end_date"`
	}
	if err := decodeStrict(raw, &props); err != nil || !googleCalendarIDs(props.CalendarIDs) ||
		!googleTimeWindow(props.Start, props.End) {
		return errors.New("invalid Google Calendar find-busy-periods properties")
	}
	return nil
}

func validateGoogleCalendarFreeSlots(raw json.RawMessage) error {
	var props struct {
		CalendarIDs        []string `json:"calendar_ids"`
		Start              string   `json:"start_date"`
		End                string   `json:"end_date"`
		MinDurationMinutes int      `json:"min_duration_minutes"`
		WorkingHoursStart  int      `json:"working_hours_start"`
		WorkingHoursEnd    int      `json:"working_hours_end"`
	}
	if err := decodeStrict(raw, &props); err != nil || !googleCalendarIDs(props.CalendarIDs) ||
		!googleTimeWindow(props.Start, props.End) || props.MinDurationMinutes < 0 || props.MinDurationMinutes > 24*60 ||
		props.WorkingHoursStart < 0 || props.WorkingHoursStart > 24 ||
		props.WorkingHoursEnd < 0 || props.WorkingHoursEnd > 24 {
		return errors.New("invalid Google Calendar find-free-slots properties")
	}
	return nil
}

func validateGoogleCalendarSearch(raw json.RawMessage) error {
	var props struct {
		Query        string `json:"query"`
		Start        string `json:"start_date"`
		End          string `json:"end_date"`
		SingleEvents bool   `json:"single_events"`
	}
	if err := decodeStrict(raw, &props); err != nil || strings.TrimSpace(props.Query) == "" ||
		len(props.Query) > 1024 || !googleTimeWindow(props.Start, props.End) {
		return errors.New("invalid Google Calendar search-events properties")
	}
	return nil
}

func validateGoogleCalendarRecurring(raw json.RawMessage) error {
	var props struct {
		CalendarID  string `json:"calendar_id"`
		EventID     string `json:"event_id"`
		TimeMin     string `json:"time_min"`
		TimeMax     string `json:"time_max"`
		ShowDeleted bool   `json:"show_deleted"`
		MaxResults  int    `json:"max_results"`
	}
	if err := decodeStrict(raw, &props); err != nil || !boundedValue(props.CalendarID, 1024) ||
		!boundedValue(props.EventID, 1024) || !optionalRFC3339(props.TimeMin) || !optionalRFC3339(props.TimeMax) ||
		!googleMaxResults(props.MaxResults) {
		return errors.New("invalid Google Calendar list-recurring-event-instances properties")
	}
	return nil
}

func validateGoogleCalendarMove(raw json.RawMessage) error {
	var props struct {
		CalendarID            string `json:"calendar_id"`
		EventID               string `json:"event_id"`
		DestinationCalendarID string `json:"destination_calendar_id"`
		SendUpdates           string `json:"send_updates"`
	}
	if err := decodeStrict(raw, &props); err != nil || !boundedValue(props.CalendarID, 1024) ||
		!boundedValue(props.EventID, 1024) || !boundedValue(props.DestinationCalendarID, 1024) ||
		(props.SendUpdates != "" && !oneOf(props.SendUpdates, "all", "externalOnly", "none")) {
		return errors.New("invalid Google Calendar move-event properties")
	}
	return nil
}

func validateGoogleCalendarImport(raw json.RawMessage) error {
	var props struct {
		CalendarID  string `json:"calendar_id"`
		ICalUID     string `json:"ical_uid"`
		Summary     string `json:"summary"`
		Start       string `json:"start_date_time"`
		End         string `json:"end_date_time"`
		Location    string `json:"location"`
		Description string `json:"description"`
	}
	if err := decodeStrict(raw, &props); err != nil {
		return errors.New("invalid Google Calendar import-event properties")
	}
	start, startErr := time.Parse(time.RFC3339, props.Start)
	end, endErr := time.Parse(time.RFC3339, props.End)
	if !boundedValue(props.CalendarID, 1024) || !boundedValue(props.ICalUID, 1024) ||
		strings.TrimSpace(props.Summary) == "" || len(props.Summary) > 1024 || startErr != nil || endErr != nil ||
		!end.After(start) || len(props.Location) > 1024 || len(props.Description) > 32*1024 {
		return errors.New("invalid Google Calendar import-event properties")
	}
	return nil
}

func validateGoogleCalendarID(raw json.RawMessage) error {
	var props struct {
		CalendarID string `json:"calendar_id"`
	}
	if err := decodeStrict(raw, &props); err != nil || !boundedValue(props.CalendarID, 1024) {
		return errors.New("invalid Google Calendar identifier")
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
		return errors.New("invalid Google Drive file identifier")
	}
	return nil
}

func validateGoogleDriveFolder(raw json.RawMessage) error {
	var props struct {
		FolderName     string `json:"folder_name"`
		ParentFolderID string `json:"parent_folder_id"`
	}
	if err := decodeStrict(raw, &props); err != nil || strings.TrimSpace(props.FolderName) == "" ||
		len(props.FolderName) > 255 || (props.ParentFolderID != "" && !boundedValue(props.ParentFolderID, 1024)) {
		return errors.New("invalid Google Drive create-folder properties")
	}
	return nil
}

func validateGoogleDriveSearch(raw json.RawMessage) error {
	var props struct {
		QueryTerm         string `json:"query_term"`
		Operator          string `json:"operator"`
		Value             string `json:"value"`
		Type              string `json:"type"`
		ParentFolderID    string `json:"parent_folder_id"`
		IncludeTeamDrives bool   `json:"include_team_drives"`
	}
	if err := decodeStrict(raw, &props); err != nil || !oneOf(props.QueryTerm, "name", "mimeType", "fullText") ||
		!oneOf(props.Operator, "contains", "=") || strings.TrimSpace(props.Value) == "" || len(props.Value) > 1024 ||
		strings.ContainsAny(props.Value, "'\\") || (props.Type != "" && !oneOf(props.Type, "all", "file", "folder")) ||
		(props.ParentFolderID != "" && !boundedValue(props.ParentFolderID, 1024)) {
		return errors.New("invalid Google Drive search-files properties")
	}
	return nil
}

func validateGoogleDriveCopy(raw json.RawMessage) error {
	var props struct {
		FileID    string `json:"file_id"`
		Name      string `json:"name"`
		FolderID  string `json:"folder_id"`
		ConvertTo string `json:"convert_to"`
	}
	if err := decodeStrict(raw, &props); err != nil || !boundedValue(props.FileID, 1024) ||
		strings.TrimSpace(props.Name) == "" || len(props.Name) > 255 || !boundedValue(props.FolderID, 1024) ||
		(props.ConvertTo != "" && !oneOf(props.ConvertTo, "application/vnd.google-apps.document",
			"application/vnd.google-apps.spreadsheet")) {
		return errors.New("invalid Google Drive copy-file properties")
	}
	return nil
}

func validateGoogleDriveMove(raw json.RawMessage) error {
	var props struct {
		FileID   string `json:"file_id"`
		FolderID string `json:"folder_id"`
	}
	if err := decodeStrict(raw, &props); err != nil || !boundedValue(props.FileID, 1024) ||
		!boundedValue(props.FolderID, 1024) {
		return errors.New("invalid Google Drive move-file properties")
	}
	return nil
}

func validateGoogleDriveUpdateMetadata(raw json.RawMessage) error {
	var props struct {
		FileID      string `json:"file_id"`
		Name        string `json:"name"`
		Description string `json:"description"`
		Starred     *bool  `json:"starred"`
	}
	if err := decodeStrict(raw, &props); err != nil || !boundedValue(props.FileID, 1024) ||
		(props.Name != "" && len(props.Name) > 255) || len(props.Description) > 32*1024 {
		return errors.New("invalid Google Drive update-metadata properties")
	}
	return nil
}

func validateGoogleDriveShare(raw json.RawMessage) error {
	var props struct {
		FileID              string `json:"file_id"`
		UserEmail           string `json:"user_email"`
		Role                string `json:"role"`
		SendInvitationEmail bool   `json:"send_invitation_email"`
	}
	if err := decodeStrict(raw, &props); err != nil || !boundedValue(props.FileID, 1024) || !email(props.UserEmail) ||
		!googleDriveRole(props.Role) {
		return errors.New("invalid Google Drive share-file properties")
	}
	return nil
}

func validateGoogleDriveUpdatePermission(raw json.RawMessage) error {
	var props struct {
		FileID    string `json:"file_id"`
		UserEmail string `json:"user_email"`
		NewRole   string `json:"new_role"`
	}
	if err := decodeStrict(raw, &props); err != nil || !boundedValue(props.FileID, 1024) || !email(props.UserEmail) ||
		!googleDriveRole(props.NewRole) {
		return errors.New("invalid Google Drive update-permission properties")
	}
	return nil
}

func validateGoogleDriveRemovePermission(raw json.RawMessage) error {
	var props struct {
		FileID    string `json:"file_id"`
		UserEmail string `json:"user_email"`
		Role      string `json:"role"`
	}
	if err := decodeStrict(raw, &props); err != nil || !boundedValue(props.FileID, 1024) || !email(props.UserEmail) ||
		!googleDriveRole(props.Role) {
		return errors.New("invalid Google Drive remove-permission properties")
	}
	return nil
}

func validateGoogleDriveListShared(raw json.RawMessage) error {
	var props struct {
		Query    string `json:"query"`
		PageSize int    `json:"page_size"`
	}
	if err := decodeStrict(raw, &props); err != nil || len(props.Query) > 1024 ||
		strings.ContainsAny(props.Query, "\r\n") || props.PageSize < 0 || props.PageSize > 100 {
		return errors.New("invalid Google Drive list-shared-drives properties")
	}
	return nil
}

func validateGoogleDriveSharedID(raw json.RawMessage) error {
	var props struct {
		DriveID string `json:"drive_id"`
	}
	if err := decodeStrict(raw, &props); err != nil || !boundedValue(props.DriveID, 1024) {
		return errors.New("invalid Google Drive shared-drive identifier")
	}
	return nil
}

func validateGoogleDriveSharedName(raw json.RawMessage) error {
	var props struct {
		Name string `json:"name"`
	}
	if err := decodeStrict(raw, &props); err != nil || strings.TrimSpace(props.Name) == "" || len(props.Name) > 255 {
		return errors.New("invalid Google Drive create-shared-drive properties")
	}
	return nil
}

func validateGoogleDriveUpdateShared(raw json.RawMessage) error {
	var props struct {
		DriveID string `json:"drive_id"`
		Name    string `json:"name"`
	}
	if err := decodeStrict(raw, &props); err != nil || !boundedValue(props.DriveID, 1024) ||
		(props.Name != "" && len(props.Name) > 255) {
		return errors.New("invalid Google Drive update-shared-drive properties")
	}
	return nil
}

func validateGoogleDriveComment(raw json.RawMessage) error {
	var props struct {
		FileID  string `json:"file_id"`
		Content string `json:"content"`
	}
	if err := decodeStrict(raw, &props); err != nil || !boundedValue(props.FileID, 1024) ||
		strings.TrimSpace(props.Content) == "" || len(props.Content) > 32*1024 {
		return errors.New("invalid Google Drive create-comment properties")
	}
	return nil
}

func validateGoogleDriveReply(raw json.RawMessage) error {
	var props struct {
		FileID    string `json:"file_id"`
		CommentID string `json:"comment_id"`
		Content   string `json:"content"`
		Action    string `json:"action"`
	}
	if err := decodeStrict(raw, &props); err != nil || !boundedValue(props.FileID, 1024) ||
		!boundedValue(props.CommentID, 1024) || strings.TrimSpace(props.Content) == "" ||
		len(props.Content) > 32*1024 || (props.Action != "" && !oneOf(props.Action, "resolve", "reopen")) {
		return errors.New("invalid Google Drive create-reply properties")
	}
	return nil
}

func validateGoogleDriveCommentID(raw json.RawMessage) error {
	var props struct {
		FileID    string `json:"file_id"`
		CommentID string `json:"comment_id"`
	}
	if err := decodeStrict(raw, &props); err != nil || !boundedValue(props.FileID, 1024) ||
		!boundedValue(props.CommentID, 1024) {
		return errors.New("invalid Google Drive comment identifier")
	}
	return nil
}

func validateGoogleDriveReplyID(raw json.RawMessage) error {
	var props struct {
		FileID    string `json:"file_id"`
		CommentID string `json:"comment_id"`
		ReplyID   string `json:"reply_id"`
	}
	if err := decodeStrict(raw, &props); err != nil || !boundedValue(props.FileID, 1024) ||
		!boundedValue(props.CommentID, 1024) || !boundedValue(props.ReplyID, 1024) {
		return errors.New("invalid Google Drive reply identifier")
	}
	return nil
}

func validateGoogleDriveUpdateReply(raw json.RawMessage) error {
	var props struct {
		FileID    string `json:"file_id"`
		CommentID string `json:"comment_id"`
		ReplyID   string `json:"reply_id"`
		Content   string `json:"content"`
	}
	if err := decodeStrict(raw, &props); err != nil || !boundedValue(props.FileID, 1024) ||
		!boundedValue(props.CommentID, 1024) || !boundedValue(props.ReplyID, 1024) ||
		strings.TrimSpace(props.Content) == "" || len(props.Content) > 32*1024 {
		return errors.New("invalid Google Drive update-reply properties")
	}
	return nil
}

func validateGoogleDriveCreateFromText(raw json.RawMessage) error {
	var props struct {
		FileName       string `json:"file_name"`
		Text           string `json:"text"`
		ContentType    string `json:"content_type"`
		ParentFolderID string `json:"parent_folder_id"`
	}
	if err := decodeStrict(raw, &props); err != nil || !boundedFilename(props.FileName) ||
		strings.TrimSpace(props.Text) == "" || len(props.Text) > 1024*1024 ||
		!oneOf(props.ContentType, "plain/text", "text/csv", "text/xml") ||
		(props.ParentFolderID != "" && !boundedValue(props.ParentFolderID, 1024)) {
		return errors.New("invalid Google Drive create-file-from-text properties")
	}
	return nil
}

func validateGoogleDriveSavePDF(raw json.RawMessage) error {
	var props struct {
		DocumentID string `json:"document_id"`
		FolderID   string `json:"folder_id"`
		Name       string `json:"name"`
	}
	if err := decodeStrict(raw, &props); err != nil || !boundedValue(props.DocumentID, 1024) ||
		!boundedValue(props.FolderID, 1024) || strings.TrimSpace(props.Name) == "" || len(props.Name) > 255 ||
		strings.ContainsAny(props.Name, "\r\n/\\") {
		return errors.New("invalid Google Drive save-file-as-pdf properties")
	}
	return nil
}

func validateGoogleDrivePublicAccess(raw json.RawMessage) error {
	var props struct {
		FileID string `json:"file_id"`
		Role   string `json:"role"`
	}
	if err := decodeStrict(raw, &props); err != nil || !boundedValue(props.FileID, 1024) ||
		!oneOf(props.Role, "reader", "commenter", "writer") {
		return errors.New("invalid Google Drive set-drive-public-access properties")
	}
	return nil
}

func validateGoogleDriveEmptyTrash(raw json.RawMessage) error {
	var props struct {
		DriveID string `json:"drive_id"`
	}
	if err := decodeStrict(raw, &props); err != nil ||
		(props.DriveID != "" && !boundedValue(props.DriveID, 1024)) {
		return errors.New("invalid Google Drive empty-trash properties")
	}
	return nil
}

func validateGmailGetEmail(raw json.RawMessage) error {
	var props struct {
		MessageID string `json:"message_id"`
	}
	if err := decodeStrict(raw, &props); err != nil || !boundedValue(props.MessageID, 1024) {
		return errors.New("invalid Gmail get-email properties")
	}
	return nil
}

func validateGmailFindEmail(raw json.RawMessage) error {
	var props struct {
		From             string `json:"from"`
		To               string `json:"to"`
		Subject          string `json:"subject"`
		Content          string `json:"content"`
		HasAttachment    bool   `json:"has_attachment"`
		AttachmentName   string `json:"attachment_name"`
		Label            string `json:"label"`
		Category         string `json:"category"`
		AfterDate        string `json:"after_date"`
		BeforeDate       string `json:"before_date"`
		IncludeSpamTrash bool   `json:"include_spam_trash"`
		MaxResults       int    `json:"max_results"`
	}
	if err := decodeStrict(raw, &props); err != nil ||
		(props.From != "" && !gmailSearchTerm(props.From, 320)) ||
		(props.To != "" && !gmailSearchTerm(props.To, 320)) ||
		(props.Subject != "" && !gmailSearchTerm(props.Subject, 512)) ||
		(props.Content != "" && !gmailSearchTerm(props.Content, 512)) ||
		(props.AttachmentName != "" && !gmailSearchTerm(props.AttachmentName, 255)) ||
		(props.Label != "" && !boundedValue(props.Label, 256)) ||
		!oneOf(props.Category, "", "primary", "social", "promotions", "updates", "forums", "reservations", "purchases") ||
		!optionalRFC3339(props.AfterDate) || !optionalRFC3339(props.BeforeDate) ||
		!googleMaxResults(props.MaxResults) {
		return errors.New("invalid Gmail find-email properties")
	}
	return nil
}

func validateGmailGetAttachment(raw json.RawMessage) error {
	var props struct {
		MessageID    string `json:"message_id"`
		AttachmentID string `json:"attachment_id"`
		FileName     string `json:"file_name"`
	}
	if err := decodeStrict(raw, &props); err != nil || !boundedValue(props.MessageID, 1024) ||
		!boundedValue(props.AttachmentID, 1024) || !optionalFilename(props.FileName) {
		return errors.New("invalid Gmail get-email-attachment properties")
	}
	return nil
}

func validateGoogleDriveUpload(raw json.RawMessage) error {
	var props struct {
		FileName       string  `json:"file_name"`
		File           fileRef `json:"file"`
		ParentFolderID string  `json:"parent_folder_id"`
	}
	if err := decodeStrict(raw, &props); err != nil || !boundedFilename(props.FileName) ||
		!oryxaFileID(props.File.FileID) || (props.ParentFolderID != "" && !boundedValue(props.ParentFolderID, 1024)) {
		return errors.New("invalid Google Drive upload-drive-file properties")
	}
	return nil
}

func validateGoogleDriveReplace(raw json.RawMessage) error {
	var props struct {
		FileID   string  `json:"file_id"`
		File     fileRef `json:"file"`
		MimeType string  `json:"mime_type"`
	}
	if err := decodeStrict(raw, &props); err != nil || !boundedValue(props.FileID, 1024) ||
		!oryxaFileID(props.File.FileID) || (props.MimeType != "" && (len(props.MimeType) > 200 ||
		strings.ContainsAny(props.MimeType, "\r\n\t"))) {
		return errors.New("invalid Google Drive replace-drive-file properties")
	}
	return nil
}

func validateGoogleDriveDownload(raw json.RawMessage) error {
	var props struct {
		FileID              string `json:"file_id"`
		DestinationFileName string `json:"destination_file_name"`
	}
	if err := decodeStrict(raw, &props); err != nil || !boundedValue(props.FileID, 1024) ||
		!optionalFilename(props.DestinationFileName) {
		return errors.New("invalid Google Drive download-drive-file properties")
	}
	return nil
}

func validateGoogleDriveExport(raw json.RawMessage) error {
	var props struct {
		FileID   string `json:"file_id"`
		MimeType string `json:"mime_type"`
	}
	if err := decodeStrict(raw, &props); err != nil || !boundedValue(props.FileID, 1024) ||
		!oneOf(props.MimeType, googleDriveExportMIME...) {
		return errors.New("invalid Google Drive export-drive-file properties")
	}
	return nil
}

func validateGoogleEmpty(raw json.RawMessage) error {
	var props struct{}
	if err := decodeStrict(raw, &props); err != nil {
		return errors.New("Google properties must be an empty object")
	}
	return nil
}

func validateGoogleDriveUploadFromURL(raw json.RawMessage) error {
	var props struct {
		SourceURL      string `json:"source_url"`
		FileName       string `json:"file_name"`
		MimeType       string `json:"mime_type"`
		ParentFolderID string `json:"parent_folder_id"`
	}
	if err := decodeStrict(raw, &props); err != nil || !publicHTTPSURL(props.SourceURL) ||
		!boundedFilename(props.FileName) || !optionalDriveMIME(props.MimeType) ||
		(props.ParentFolderID != "" && !boundedValue(props.ParentFolderID, 1024)) {
		return errors.New("invalid Google Drive upload-from-url properties")
	}
	return nil
}

func prepareGmailSearchLabel(raw json.RawMessage) (json.RawMessage, error) {
	var props map[string]any
	if err := json.Unmarshal(raw, &props); err != nil {
		return nil, err
	}
	if label, ok := props["label"].(string); ok && label != "" {
		props["label"] = map[string]string{"name": label}
	}
	return json.Marshal(props)
}

func gmailSearchTerm(value string, maximum int) bool {
	return value != "" && len(value) <= maximum && strings.TrimSpace(value) == value && !strings.ContainsAny(value, "\r\n")
}

func optionalDriveMIME(value string) bool {
	if value == "" {
		return true
	}
	return boundedValue(value, 128) && strings.Count(value, "/") == 1 && !strings.Contains(value, " ")
}

func prepareGmailLabelID(raw json.RawMessage) (json.RawMessage, error) {
	var props map[string]any
	if err := json.Unmarshal(raw, &props); err != nil {
		return nil, err
	}
	if label, ok := props["label"].(string); ok {
		props["label"] = map[string]string{"id": label}
	}
	return json.Marshal(props)
}

func prepareGoogleCalendarEventID(raw json.RawMessage) (json.RawMessage, error) {
	var props map[string]any
	if err := json.Unmarshal(raw, &props); err != nil {
		return nil, err
	}
	if eventID, ok := props["event_id"].(string); ok {
		props["eventId"] = eventID
		delete(props, "event_id")
	}
	return json.Marshal(props)
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

func googleMaxResults(value int) bool {
	return value >= 0 && value <= 100
}

func googleCalendarIDs(values []string) bool {
	if len(values) == 0 || len(values) > 20 {
		return false
	}
	for _, value := range values {
		if !boundedValue(value, 1024) {
			return false
		}
	}
	return true
}

func googleTimeWindow(start, end string) bool {
	started, startErr := time.Parse(time.RFC3339, start)
	finished, endErr := time.Parse(time.RFC3339, end)
	return startErr == nil && endErr == nil && finished.After(started)
}

func googleDriveRole(value string) bool {
	return oneOf(value, "organizer", "fileOrganizer", "writer", "commenter", "reader")
}

func validateGoogleTrigger(raw json.RawMessage) error {
	var props struct{}
	if err := decodeStrict(raw, &props); err != nil {
		return errors.New("Google trigger properties must be an empty object")
	}
	return nil
}
