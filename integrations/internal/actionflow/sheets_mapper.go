package actionflow

import (
	"bytes"
	"encoding/json"

	integrationevents "github.com/Shubham04-oss/oryxa-go/contracts/events/integrations/v1"
)

func mapGoogleSheetsAction(request MapRequest) (MappedResult, bool, error) {
	switch request.ActionName {
	case "sheets-create-spreadsheet":
		result, err := mapGoogleSheetsSpreadsheet(request, "created")
		return result, true, err
	case "sheets-get-spreadsheet":
		result, err := mapGoogleSheetsSpreadsheet(request, "")
		return result, true, err
	case "sheets-search-spreadsheets":
		result, err := mapGoogleSheetsSearch(request)
		return result, true, err
	case "sheets-find-worksheet":
		result, err := mapGoogleSheetsWorksheets(request)
		return result, true, err
	case "sheets-find-rows", "sheets-get-row", "sheets-get-all-rows", "sheets-get-values":
		result, err := mapGoogleSheetsRows(request)
		return result, true, err
	case "sheets-export-worksheet":
		result, err := mapStoredFileLookup(request.Output)
		return result, true, err
	case "sheets-custom-api-call":
		result, err := mapProviderHTTPCall(request)
		return result, true, err
	case "sheets-add-worksheet", "sheets-find-or-create-worksheet", "sheets-copy-worksheet",
		"sheets-rename-worksheet", "sheets-delete-worksheet", "sheets-add-row", "sheets-add-row-at-top",
		"sheets-add-multiple-rows", "sheets-update-row", "sheets-update-multiple-rows",
		"sheets-delete-row", "sheets-delete-multiple-rows", "sheets-find-or-create-row",
		"sheets-append-values", "sheets-update-values", "sheets-clear-values", "sheets-add-column",
		"sheets-insert-dimension", "sheets-append-dimension", "sheets-delete-dimension",
		"sheets-auto-resize-dimensions", "sheets-update-dimension-properties",
		"sheets-update-sheet-properties", "sheets-format-cells":
		result, err := mapGoogleSheetsSpreadsheet(request, "mutated")
		return result, true, err
	default:
		return MappedResult{}, false, nil
	}
}

func mapGoogleSheetsSpreadsheet(request MapRequest, kind string) (MappedResult, error) {
	payload := unwrapHTTPBody(request.Output)
	var output struct {
		SpreadsheetID string `json:"spreadsheetId"`
		ID            string `json:"id"`
		Properties    struct {
			Title string `json:"title"`
		} `json:"properties"`
		Sheets []struct {
			Properties struct {
				SheetID int    `json:"sheetId"`
				Title   string `json:"title"`
			} `json:"properties"`
		} `json:"sheets"`
	}
	_ = json.Unmarshal(payload, &output)
	var props struct {
		SpreadsheetID string `json:"spreadsheet_id"`
	}
	_ = json.Unmarshal(request.Props, &props)
	id := firstNonEmpty(output.SpreadsheetID, output.ID, props.SpreadsheetID)
	if id == "" {
		return MappedResult{}, ErrPieceOutputInvalid
	}
	sheets := make([]any, 0, len(output.Sheets))
	for _, sheet := range output.Sheets {
		sheets = append(sheets, struct {
			SheetID int    `json:"sheet_id"`
			Title   string `json:"title,omitempty"`
		}{SheetID: sheet.Properties.SheetID, Title: sheet.Properties.Title})
	}
	result, err := json.Marshal(struct {
		SpreadsheetID string `json:"spreadsheet_id"`
		Title         string `json:"title,omitempty"`
		Sheets        []any  `json:"sheets,omitempty"`
	}{SpreadsheetID: id, Title: output.Properties.Title, Sheets: sheets})
	if err != nil {
		return MappedResult{}, ErrPieceOutputInvalid
	}
	switch kind {
	case "created":
		return MappedResult{SafeResult: result, DetailType: integrationevents.GoogleSheetsSpreadsheetCreatedDetailType,
			Data: integrationevents.GoogleSheetsSpreadsheetCreated{
				ExecutionID: request.Execution.ID, ConnectionID: request.Connection.ID, SpreadsheetID: id,
				RequestedBy: request.Execution.MemberID, CreatedAt: request.OccurredAt,
			}}, nil
	case "mutated":
		return MappedResult{SafeResult: result, DetailType: integrationevents.GoogleSheetsSpreadsheetMutatedDetailType,
			Data: integrationevents.GoogleSheetsSpreadsheetMutated{
				ExecutionID: request.Execution.ID, ConnectionID: request.Connection.ID, SpreadsheetID: id,
				RequestedBy: request.Execution.MemberID, MutatedAt: request.OccurredAt,
			}}, nil
	default:
		return MappedResult{SafeResult: result}, nil
	}
}

func mapGoogleSheetsSearch(request MapRequest) (MappedResult, error) {
	payload := unwrapHTTPBody(request.Output)
	var output struct {
		Files []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"files"`
	}
	if json.Unmarshal(payload, &output) != nil {
		return MappedResult{}, ErrPieceOutputInvalid
	}
	if output.Files == nil {
		output.Files = []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		}{}
	}
	items, truncated := googleCap(output.Files, 100)
	safe := make([]any, 0, len(items))
	for _, item := range items {
		if item.ID == "" {
			return MappedResult{}, ErrPieceOutputInvalid
		}
		safe = append(safe, struct {
			ID   string `json:"id"`
			Name string `json:"name,omitempty"`
		}{ID: item.ID, Name: item.Name})
	}
	return googleJSON(struct {
		Files     []any `json:"files"`
		Truncated bool  `json:"truncated"`
	}{Files: safe, Truncated: truncated})
}

func mapGoogleSheetsWorksheets(request MapRequest) (MappedResult, error) {
	payload := unwrapHTTPBody(request.Output)
	var sheets []struct {
		SheetID int    `json:"sheetId"`
		Title   string `json:"title"`
	}
	if json.Unmarshal(payload, &sheets) != nil {
		var wrapped struct {
			Sheets []struct {
				SheetID int    `json:"sheetId"`
				Title   string `json:"title"`
			} `json:"sheets"`
		}
		if json.Unmarshal(payload, &wrapped) != nil {
			return MappedResult{}, ErrPieceOutputInvalid
		}
		sheets = wrapped.Sheets
	}
	if sheets == nil {
		sheets = []struct {
			SheetID int    `json:"sheetId"`
			Title   string `json:"title"`
		}{}
	}
	items, truncated := googleCap(sheets, 100)
	safe := make([]any, 0, len(items))
	for _, item := range items {
		safe = append(safe, struct {
			SheetID int    `json:"sheet_id"`
			Title   string `json:"title,omitempty"`
		}{SheetID: item.SheetID, Title: item.Title})
	}
	return googleJSON(struct {
		Sheets    []any `json:"sheets"`
		Truncated bool  `json:"truncated"`
	}{Sheets: safe, Truncated: truncated})
}

func mapGoogleSheetsRows(request MapRequest) (MappedResult, error) {
	payload := unwrapHTTPBody(request.Output)
	rows := sheetsRows(payload)
	if rows == nil {
		return MappedResult{}, ErrPieceOutputInvalid
	}
	items, truncated := googleCap(rows, 100)
	return googleJSON(struct {
		Rows      []json.RawMessage `json:"rows"`
		Truncated bool              `json:"truncated"`
	}{Rows: items, Truncated: truncated})
}

func sheetsRows(raw json.RawMessage) []json.RawMessage {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || string(trimmed) == "null" {
		return []json.RawMessage{}
	}
	if trimmed[0] == '[' {
		var values []json.RawMessage
		if json.Unmarshal(trimmed, &values) != nil {
			return nil
		}
		return values
	}
	var wrapped struct {
		Values []json.RawMessage `json:"values"`
		Rows   []json.RawMessage `json:"rows"`
	}
	if json.Unmarshal(trimmed, &wrapped) != nil {
		return nil
	}
	if wrapped.Values != nil {
		return wrapped.Values
	}
	if wrapped.Rows != nil {
		return wrapped.Rows
	}
	return []json.RawMessage{trimmed}
}
