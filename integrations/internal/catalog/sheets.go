package catalog

import (
	"encoding/json"
	"errors"
)

const (
	googleSheetsWriteScope = "https://www.googleapis.com/auth/spreadsheets"
	googleSheetsReadScope  = "https://www.googleapis.com/auth/spreadsheets.readonly"
)

var (
	googleSheetsWrite = []string{googleSheetsWriteScope}
	googleSheetsRead  = []string{googleSheetsReadScope}
)

var googleSheetsActions = map[string]googleActionSpec{
	"sheets-create-spreadsheet":          {GoogleSheetsPieceName, GoogleSheetsPieceVersion, "sheets_create_spreadsheet", googleSheetsWrite, validateSheetsCreateSpreadsheet, nil},
	"sheets-get-spreadsheet":             {GoogleSheetsPieceName, GoogleSheetsPieceVersion, "sheets_get_spreadsheet", googleSheetsRead, validateSheetsGetSpreadsheet, nil},
	"sheets-search-spreadsheets":         {GoogleSheetsPieceName, GoogleSheetsPieceVersion, "sheets_search_spreadsheets", googleSheetsRead, validateSheetsSearch, nil},
	"sheets-add-worksheet":               {GoogleSheetsPieceName, GoogleSheetsPieceVersion, "sheets_add_worksheet", googleSheetsWrite, validateSheetsAddWorksheet, nil},
	"sheets-find-worksheet":              {GoogleSheetsPieceName, GoogleSheetsPieceVersion, "sheets_find_worksheet", googleSheetsRead, validateSheetsFindWorksheet, nil},
	"sheets-find-or-create-worksheet":    {GoogleSheetsPieceName, GoogleSheetsPieceVersion, "sheets_find_or_create_worksheet", googleSheetsWrite, validateSheetsAddWorksheet, nil},
	"sheets-copy-worksheet":              {GoogleSheetsPieceName, GoogleSheetsPieceVersion, "sheets_copy_worksheet", googleSheetsWrite, validateSheetsCopyWorksheet, nil},
	"sheets-rename-worksheet":            {GoogleSheetsPieceName, GoogleSheetsPieceVersion, "sheets_rename_worksheet", googleSheetsWrite, validateSheetsRename, nil},
	"sheets-delete-worksheet":            {GoogleSheetsPieceName, GoogleSheetsPieceVersion, "sheets_delete_worksheet", googleSheetsWrite, validateSheetsSheet, nil},
	"sheets-add-row":                     {GoogleSheetsPieceName, GoogleSheetsPieceVersion, "sheets_add_row", googleSheetsWrite, validateSheetsAddRow, nil},
	"sheets-add-row-at-top":              {GoogleSheetsPieceName, GoogleSheetsPieceVersion, "sheets_add_row_at_top", googleSheetsWrite, validateSheetsAddRowTop, nil},
	"sheets-add-multiple-rows":           {GoogleSheetsPieceName, GoogleSheetsPieceVersion, "sheets_add_multiple_rows", googleSheetsWrite, validateSheetsAddMultipleRows, nil},
	"sheets-update-row":                  {GoogleSheetsPieceName, GoogleSheetsPieceVersion, "sheets_update_row", googleSheetsWrite, validateSheetsUpdateRow, nil},
	"sheets-update-multiple-rows":        {GoogleSheetsPieceName, GoogleSheetsPieceVersion, "sheets_update_multiple_rows", googleSheetsWrite, validateSheetsUpdateMultipleRows, nil},
	"sheets-delete-row":                  {GoogleSheetsPieceName, GoogleSheetsPieceVersion, "sheets_delete_row", googleSheetsWrite, validateSheetsDeleteRow, nil},
	"sheets-delete-multiple-rows":        {GoogleSheetsPieceName, GoogleSheetsPieceVersion, "sheets_delete_multiple_rows", googleSheetsWrite, validateSheetsDeleteMultipleRows, nil},
	"sheets-find-rows":                   {GoogleSheetsPieceName, GoogleSheetsPieceVersion, "sheets_find_rows", googleSheetsRead, validateSheetsFindRows, nil},
	"sheets-find-or-create-row":          {GoogleSheetsPieceName, GoogleSheetsPieceVersion, "sheets_find_or_create_row", googleSheetsWrite, validateSheetsFindOrCreateRow, nil},
	"sheets-get-row":                     {GoogleSheetsPieceName, GoogleSheetsPieceVersion, "sheets_get_row", googleSheetsRead, validateSheetsGetRow, nil},
	"sheets-get-all-rows":                {GoogleSheetsPieceName, GoogleSheetsPieceVersion, "sheets_get_all_rows", googleSheetsRead, validateSheetsGetAllRows, nil},
	"sheets-get-values":                  {GoogleSheetsPieceName, GoogleSheetsPieceVersion, "sheets_get_values", googleSheetsRead, validateSheetsGetValues, nil},
	"sheets-append-values":               {GoogleSheetsPieceName, GoogleSheetsPieceVersion, "sheets_append_values", googleSheetsWrite, validateSheetsAppendValues, nil},
	"sheets-update-values":               {GoogleSheetsPieceName, GoogleSheetsPieceVersion, "sheets_update_values", googleSheetsWrite, validateSheetsUpdateValues, nil},
	"sheets-clear-values":                {GoogleSheetsPieceName, GoogleSheetsPieceVersion, "sheets_clear_values", googleSheetsWrite, validateSheetsClearValues, nil},
	"sheets-add-column":                  {GoogleSheetsPieceName, GoogleSheetsPieceVersion, "sheets_add_column", googleSheetsWrite, validateSheetsAddColumn, nil},
	"sheets-insert-dimension":            {GoogleSheetsPieceName, GoogleSheetsPieceVersion, "sheets_insert_dimension", googleSheetsWrite, validateSheetsInsertDimension, nil},
	"sheets-append-dimension":            {GoogleSheetsPieceName, GoogleSheetsPieceVersion, "sheets_append_dimension", googleSheetsWrite, validateSheetsAppendDimension, nil},
	"sheets-delete-dimension":            {GoogleSheetsPieceName, GoogleSheetsPieceVersion, "sheets_delete_dimension", googleSheetsWrite, validateSheetsDeleteDimension, nil},
	"sheets-auto-resize-dimensions":      {GoogleSheetsPieceName, GoogleSheetsPieceVersion, "sheets_auto_resize_dimensions", googleSheetsWrite, validateSheetsAutoResize, nil},
	"sheets-update-dimension-properties": {GoogleSheetsPieceName, GoogleSheetsPieceVersion, "sheets_update_dimension_properties", googleSheetsWrite, validateSheetsDimensionProperties, nil},
	"sheets-update-sheet-properties":     {GoogleSheetsPieceName, GoogleSheetsPieceVersion, "sheets_update_sheet_properties", googleSheetsWrite, validateSheetsSheetProperties, nil},
	"sheets-format-cells":                {GoogleSheetsPieceName, GoogleSheetsPieceVersion, "sheets_format_cells", googleSheetsWrite, validateSheetsFormat, nil},
	"sheets-export-worksheet":            {GoogleSheetsPieceName, GoogleSheetsPieceVersion, "sheets_export_worksheet", googleSheetsRead, validateSheetsExport, nil},
	"sheets-custom-api-call":             {GoogleSheetsPieceName, GoogleSheetsPieceVersion, "custom_api_call", googleSheetsRead, validateCustomAPICall("sheets.googleapis.com"), prepareCustomAPICall},
}

var googleSheetsNeedsFiles = map[string]bool{"sheets-export-worksheet": true}

func sheetsSpreadsheet(id string) bool { return boundedIdentifier(id, 128) }
func sheetsSheetID(id int) bool        { return id >= 0 }

func validateSheetsCreateSpreadsheet(raw json.RawMessage) error {
	var props struct {
		Title          string `json:"spreadsheet_title"`
		ParentFolderID string `json:"parent_folder_id"`
	}
	if err := decodeStrict(raw, &props); err != nil || !boundedValue(props.Title, 200) ||
		(props.ParentFolderID != "" && !boundedIdentifier(props.ParentFolderID, 128)) {
		return errors.New("invalid Google Sheets create-spreadsheet properties")
	}
	return nil
}

func validateSheetsGetSpreadsheet(raw json.RawMessage) error {
	var props struct {
		SpreadsheetID   string `json:"spreadsheet_id"`
		IncludeGridData bool   `json:"include_grid_data"`
	}
	if err := decodeStrict(raw, &props); err != nil || !sheetsSpreadsheet(props.SpreadsheetID) || props.IncludeGridData {
		return errors.New("invalid Google Sheets get-spreadsheet properties")
	}
	return nil
}

func validateSheetsSearch(raw json.RawMessage) error {
	var props struct {
		Name       string `json:"spreadsheet_name"`
		ExactMatch bool   `json:"exact_match"`
	}
	if err := decodeStrict(raw, &props); err != nil || !boundedValue(props.Name, 200) {
		return errors.New("invalid Google Sheets search-spreadsheets properties")
	}
	return nil
}

func validateSheetsAddWorksheet(raw json.RawMessage) error {
	var props struct {
		SpreadsheetID string   `json:"spreadsheet_id"`
		Title         string   `json:"title"`
		Headers       []string `json:"headers"`
	}
	if err := decodeStrict(raw, &props); err != nil || !sheetsSpreadsheet(props.SpreadsheetID) ||
		!boundedValue(props.Title, 200) || len(props.Headers) > 50 {
		return errors.New("invalid Google Sheets worksheet properties")
	}
	for _, header := range props.Headers {
		if !boundedValue(header, 200) {
			return errors.New("invalid Google Sheets worksheet properties")
		}
	}
	return nil
}

func validateSheetsFindWorksheet(raw json.RawMessage) error {
	var props struct {
		SpreadsheetID string `json:"spreadsheet_id"`
		Title         string `json:"title"`
		ExactMatch    bool   `json:"exact_match"`
	}
	if err := decodeStrict(raw, &props); err != nil || !sheetsSpreadsheet(props.SpreadsheetID) ||
		!boundedValue(props.Title, 200) {
		return errors.New("invalid Google Sheets find-worksheet properties")
	}
	return nil
}

func validateSheetsCopyWorksheet(raw json.RawMessage) error {
	var props struct {
		SpreadsheetID            string `json:"spreadsheet_id"`
		SheetID                  int    `json:"sheet_id"`
		DestinationSpreadsheetID string `json:"destination_spreadsheet_id"`
	}
	if err := decodeStrict(raw, &props); err != nil || !sheetsSpreadsheet(props.SpreadsheetID) ||
		!sheetsSheetID(props.SheetID) || !sheetsSpreadsheet(props.DestinationSpreadsheetID) {
		return errors.New("invalid Google Sheets copy-worksheet properties")
	}
	return nil
}

func validateSheetsRename(raw json.RawMessage) error {
	var props struct {
		SpreadsheetID string `json:"spreadsheet_id"`
		SheetID       int    `json:"sheet_id"`
		NewName       string `json:"new_name"`
	}
	if err := decodeStrict(raw, &props); err != nil || !sheetsSpreadsheet(props.SpreadsheetID) ||
		!sheetsSheetID(props.SheetID) || !boundedValue(props.NewName, 200) {
		return errors.New("invalid Google Sheets rename-worksheet properties")
	}
	return nil
}

func validateSheetsSheet(raw json.RawMessage) error {
	var props struct {
		SpreadsheetID string `json:"spreadsheet_id"`
		SheetID       int    `json:"sheet_id"`
	}
	if err := decodeStrict(raw, &props); err != nil || !sheetsSpreadsheet(props.SpreadsheetID) || !sheetsSheetID(props.SheetID) {
		return errors.New("invalid Google Sheets sheet properties")
	}
	return nil
}

func validateSheetsAddRow(raw json.RawMessage) error {
	var props struct {
		SpreadsheetID   string          `json:"spreadsheet_id"`
		SheetID         int             `json:"sheet_id"`
		FirstRowHeaders bool            `json:"first_row_headers"`
		AsString        bool            `json:"as_string"`
		Values          json.RawMessage `json:"values"`
	}
	if err := decodeStrict(raw, &props); err != nil || !sheetsSpreadsheet(props.SpreadsheetID) ||
		!sheetsSheetID(props.SheetID) || !boundedJSON(props.Values, 64*1024) {
		return errors.New("invalid Google Sheets add-row properties")
	}
	return nil
}

func validateSheetsAddRowTop(raw json.RawMessage) error {
	var props struct {
		SpreadsheetID   string          `json:"spreadsheet_id"`
		SheetID         int             `json:"sheet_id"`
		FirstRowHeaders bool            `json:"first_row_headers"`
		AsString        bool            `json:"as_string"`
		InsertAfterRow  int             `json:"insert_after_row"`
		Values          json.RawMessage `json:"values"`
	}
	if err := decodeStrict(raw, &props); err != nil || !sheetsSpreadsheet(props.SpreadsheetID) ||
		!sheetsSheetID(props.SheetID) || props.InsertAfterRow < 0 || !boundedJSON(props.Values, 64*1024) {
		return errors.New("invalid Google Sheets add-row-at-top properties")
	}
	return nil
}

func validateSheetsAddMultipleRows(raw json.RawMessage) error {
	var props struct {
		SpreadsheetID   string          `json:"spreadsheet_id"`
		SheetID         int             `json:"sheet_id"`
		InputType       string          `json:"input_type"`
		Values          json.RawMessage `json:"values"`
		Overwrite       bool            `json:"overwrite"`
		CheckDuplicate  bool            `json:"check_for_duplicate"`
		DuplicateColumn string          `json:"duplicate_column"`
		AsString        bool            `json:"as_string"`
		HeaderRow       int             `json:"header_row"`
	}
	if err := decodeStrict(raw, &props); err != nil || !sheetsSpreadsheet(props.SpreadsheetID) ||
		!sheetsSheetID(props.SheetID) || !oneOf(props.InputType, "csv", "json", "column_names") ||
		!boundedJSON(props.Values, 64*1024) || (props.DuplicateColumn != "" && !boundedValue(props.DuplicateColumn, 100)) ||
		props.HeaderRow < 1 {
		return errors.New("invalid Google Sheets add-multiple-rows properties")
	}
	return nil
}

func validateSheetsUpdateRow(raw json.RawMessage) error {
	var props struct {
		SpreadsheetID   string          `json:"spreadsheet_id"`
		SheetID         int             `json:"sheet_id"`
		RowID           int             `json:"row_id"`
		FirstRowHeaders bool            `json:"first_row_headers"`
		Values          json.RawMessage `json:"values"`
	}
	if err := decodeStrict(raw, &props); err != nil || !sheetsSpreadsheet(props.SpreadsheetID) ||
		!sheetsSheetID(props.SheetID) || props.RowID < 1 || !boundedJSON(props.Values, 64*1024) {
		return errors.New("invalid Google Sheets update-row properties")
	}
	return nil
}

func validateSheetsUpdateMultipleRows(raw json.RawMessage) error {
	var props struct {
		SpreadsheetID string          `json:"spreadsheet_id"`
		SheetID       int             `json:"sheet_id"`
		HeaderRow     int             `json:"header_row"`
		Values        json.RawMessage `json:"values"`
		AsString      bool            `json:"as_string"`
	}
	if err := decodeStrict(raw, &props); err != nil || !sheetsSpreadsheet(props.SpreadsheetID) ||
		!sheetsSheetID(props.SheetID) || props.HeaderRow < 1 || !boundedJSON(props.Values, 64*1024) {
		return errors.New("invalid Google Sheets update-multiple-rows properties")
	}
	return nil
}

func validateSheetsDeleteRow(raw json.RawMessage) error {
	var props struct {
		SpreadsheetID string `json:"spreadsheet_id"`
		SheetID       int    `json:"sheet_id"`
		RowID         int    `json:"row_id"`
	}
	if err := decodeStrict(raw, &props); err != nil || !sheetsSpreadsheet(props.SpreadsheetID) ||
		!sheetsSheetID(props.SheetID) || props.RowID < 1 {
		return errors.New("invalid Google Sheets delete-row properties")
	}
	return nil
}

func validateSheetsDeleteMultipleRows(raw json.RawMessage) error {
	var props struct {
		SpreadsheetID string `json:"spreadsheet_id"`
		SheetID       int    `json:"sheet_id"`
		Mode          string `json:"mode"`
		StartingRow   int    `json:"starting_row"`
		EndingRow     int    `json:"ending_row"`
		RowNumbers    string `json:"row_numbers"`
	}
	if err := decodeStrict(raw, &props); err != nil || !sheetsSpreadsheet(props.SpreadsheetID) ||
		!sheetsSheetID(props.SheetID) || !oneOf(props.Mode, "range", "list") ||
		(props.Mode == "range" && (props.StartingRow < 1 || props.EndingRow < props.StartingRow)) ||
		(props.Mode == "list" && (props.RowNumbers == "" || len(props.RowNumbers) > 500)) {
		return errors.New("invalid Google Sheets delete-multiple-rows properties")
	}
	return nil
}

func validateSheetsFindRows(raw json.RawMessage) error {
	var props struct {
		SpreadsheetID  string `json:"spreadsheet_id"`
		SheetID        int    `json:"sheet_id"`
		ColumnName     string `json:"column_name"`
		SearchValue    string `json:"search_value"`
		MatchCase      bool   `json:"match_case"`
		StartingRow    int    `json:"starting_row"`
		NumberOfRows   int    `json:"number_of_rows"`
		HeaderRow      int    `json:"header_row"`
		UseHeaderNames bool   `json:"use_header_names"`
	}
	if err := decodeStrict(raw, &props); err != nil || !sheetsSpreadsheet(props.SpreadsheetID) ||
		!sheetsSheetID(props.SheetID) || (props.ColumnName != "" && !boundedValue(props.ColumnName, 100)) ||
		len(props.SearchValue) > 2000 || props.StartingRow < 0 || props.NumberOfRows < 0 ||
		props.NumberOfRows > 100 || props.HeaderRow < 1 {
		return errors.New("invalid Google Sheets find-rows properties")
	}
	return nil
}

func validateSheetsFindOrCreateRow(raw json.RawMessage) error {
	var props struct {
		SpreadsheetID   string          `json:"spreadsheet_id"`
		SheetID         int             `json:"sheet_id"`
		ColumnName      string          `json:"column_name"`
		SearchValue     string          `json:"search_value"`
		MatchCase       bool            `json:"match_case"`
		HeaderRow       int             `json:"header_row"`
		UseHeaderNames  bool            `json:"use_header_names"`
		FirstRowHeaders bool            `json:"first_row_headers"`
		AsString        bool            `json:"as_string"`
		Values          json.RawMessage `json:"values"`
	}
	if err := decodeStrict(raw, &props); err != nil || !sheetsSpreadsheet(props.SpreadsheetID) ||
		!sheetsSheetID(props.SheetID) || !boundedValue(props.SearchValue, 2000) || props.HeaderRow < 1 ||
		(len(props.Values) > 0 && !boundedJSON(props.Values, 64*1024)) {
		return errors.New("invalid Google Sheets find-or-create-row properties")
	}
	return nil
}

func validateSheetsGetRow(raw json.RawMessage) error {
	var props struct {
		SpreadsheetID  string `json:"spreadsheet_id"`
		SheetID        int    `json:"sheet_id"`
		RowNumber      int    `json:"row_number"`
		HeaderRow      int    `json:"header_row"`
		UseHeaderNames bool   `json:"use_header_names"`
	}
	if err := decodeStrict(raw, &props); err != nil || !sheetsSpreadsheet(props.SpreadsheetID) ||
		!sheetsSheetID(props.SheetID) || props.RowNumber < 1 || props.HeaderRow < 1 {
		return errors.New("invalid Google Sheets get-row properties")
	}
	return nil
}

func validateSheetsGetAllRows(raw json.RawMessage) error {
	var props struct {
		SpreadsheetID   string `json:"spreadsheet_id"`
		SheetID         int    `json:"sheet_id"`
		FirstRowHeaders bool   `json:"first_row_headers"`
	}
	if err := decodeStrict(raw, &props); err != nil || !sheetsSpreadsheet(props.SpreadsheetID) || !sheetsSheetID(props.SheetID) {
		return errors.New("invalid Google Sheets get-all-rows properties")
	}
	return nil
}

func validateSheetsGetValues(raw json.RawMessage) error {
	var props struct {
		SpreadsheetID     string `json:"spreadsheet_id"`
		WorksheetTitle    string `json:"worksheet_title"`
		Range             string `json:"range"`
		MajorDimension    string `json:"major_dimension"`
		ValueRenderOption string `json:"value_render_option"`
	}
	if err := decodeStrict(raw, &props); err != nil || !sheetsSpreadsheet(props.SpreadsheetID) ||
		!boundedValue(props.WorksheetTitle, 200) || (props.Range != "" && !boundedValue(props.Range, 100)) ||
		(props.MajorDimension != "" && !oneOf(props.MajorDimension, "ROWS", "COLUMNS")) ||
		(props.ValueRenderOption != "" && !oneOf(props.ValueRenderOption, "FORMATTED_VALUE", "UNFORMATTED_VALUE", "FORMULA")) {
		return errors.New("invalid Google Sheets get-values properties")
	}
	return nil
}

func validateSheetsAppendValues(raw json.RawMessage) error {
	var props struct {
		SpreadsheetID    string          `json:"spreadsheet_id"`
		WorksheetTitle   string          `json:"worksheet_title"`
		Range            string          `json:"range"`
		Values           json.RawMessage `json:"values"`
		ValueInputOption string          `json:"value_input_option"`
	}
	if err := decodeStrict(raw, &props); err != nil || !sheetsSpreadsheet(props.SpreadsheetID) ||
		!boundedValue(props.WorksheetTitle, 200) || !boundedValue(props.Range, 100) ||
		!boundedJSON(props.Values, 64*1024) ||
		(props.ValueInputOption != "" && !oneOf(props.ValueInputOption, "USER_ENTERED", "RAW")) {
		return errors.New("invalid Google Sheets append-values properties")
	}
	return nil
}

func validateSheetsUpdateValues(raw json.RawMessage) error {
	var props struct {
		SpreadsheetID    string          `json:"spreadsheet_id"`
		WorksheetTitle   string          `json:"worksheet_title"`
		Range            string          `json:"range"`
		Values           json.RawMessage `json:"values"`
		ValueInputOption string          `json:"value_input_option"`
		MajorDimension   string          `json:"major_dimension"`
	}
	if err := decodeStrict(raw, &props); err != nil || !sheetsSpreadsheet(props.SpreadsheetID) ||
		!boundedValue(props.WorksheetTitle, 200) || !boundedValue(props.Range, 100) ||
		!boundedJSON(props.Values, 64*1024) ||
		(props.ValueInputOption != "" && !oneOf(props.ValueInputOption, "USER_ENTERED", "RAW")) ||
		(props.MajorDimension != "" && !oneOf(props.MajorDimension, "ROWS", "COLUMNS")) {
		return errors.New("invalid Google Sheets update-values properties")
	}
	return nil
}

func validateSheetsClearValues(raw json.RawMessage) error {
	var props struct {
		SpreadsheetID string `json:"spreadsheet_id"`
		SheetID       int    `json:"sheet_id"`
		StartingRow   int    `json:"starting_row"`
		EndingRow     int    `json:"ending_row"`
	}
	if err := decodeStrict(raw, &props); err != nil || !sheetsSpreadsheet(props.SpreadsheetID) ||
		!sheetsSheetID(props.SheetID) || props.StartingRow < 1 ||
		(props.EndingRow != 0 && props.EndingRow < props.StartingRow) {
		return errors.New("invalid Google Sheets clear-values properties")
	}
	return nil
}

func validateSheetsAddColumn(raw json.RawMessage) error {
	var props struct {
		SpreadsheetID string `json:"spreadsheet_id"`
		SheetID       int    `json:"sheet_id"`
		ColumnName    string `json:"column_name"`
		ColumnIndex   int    `json:"column_index"`
	}
	if err := decodeStrict(raw, &props); err != nil || !sheetsSpreadsheet(props.SpreadsheetID) ||
		!sheetsSheetID(props.SheetID) || !boundedValue(props.ColumnName, 100) || props.ColumnIndex < 0 {
		return errors.New("invalid Google Sheets add-column properties")
	}
	return nil
}

func validateSheetsInsertDimension(raw json.RawMessage) error {
	var props struct {
		SpreadsheetID     string `json:"spreadsheet_id"`
		SheetID           int    `json:"sheet_id"`
		Dimension         string `json:"dimension"`
		StartIndex        int    `json:"start_index"`
		EndIndex          int    `json:"end_index"`
		InheritFromBefore bool   `json:"inherit_from_before"`
	}
	if err := decodeStrict(raw, &props); err != nil || !sheetsSpreadsheet(props.SpreadsheetID) ||
		!sheetsSheetID(props.SheetID) || !oneOf(props.Dimension, "ROWS", "COLUMNS") ||
		props.StartIndex < 0 || props.EndIndex <= props.StartIndex {
		return errors.New("invalid Google Sheets insert-dimension properties")
	}
	return nil
}

func validateSheetsAppendDimension(raw json.RawMessage) error {
	var props struct {
		SpreadsheetID string `json:"spreadsheet_id"`
		SheetID       int    `json:"sheet_id"`
		Dimension     string `json:"dimension"`
		Length        int    `json:"length"`
	}
	if err := decodeStrict(raw, &props); err != nil || !sheetsSpreadsheet(props.SpreadsheetID) ||
		!sheetsSheetID(props.SheetID) || !oneOf(props.Dimension, "ROWS", "COLUMNS") ||
		props.Length < 1 || props.Length > 1000 {
		return errors.New("invalid Google Sheets append-dimension properties")
	}
	return nil
}

func validateSheetsDeleteDimension(raw json.RawMessage) error {
	var props struct {
		SpreadsheetID string `json:"spreadsheet_id"`
		SheetID       int    `json:"sheet_id"`
		Dimension     string `json:"dimension"`
		StartIndex    int    `json:"start_index"`
		EndIndex      int    `json:"end_index"`
	}
	if err := decodeStrict(raw, &props); err != nil || !sheetsSpreadsheet(props.SpreadsheetID) ||
		!sheetsSheetID(props.SheetID) || !oneOf(props.Dimension, "ROWS", "COLUMNS") ||
		props.StartIndex < 0 || props.EndIndex <= props.StartIndex {
		return errors.New("invalid Google Sheets delete-dimension properties")
	}
	return nil
}

func validateSheetsAutoResize(raw json.RawMessage) error {
	return validateSheetsDeleteDimension(raw)
}

func validateSheetsDimensionProperties(raw json.RawMessage) error {
	var props struct {
		SpreadsheetID string `json:"spreadsheet_id"`
		SheetID       int    `json:"sheet_id"`
		Dimension     string `json:"dimension"`
		StartIndex    int    `json:"start_index"`
		EndIndex      int    `json:"end_index"`
		PixelSize     int    `json:"pixel_size"`
		Hidden        bool   `json:"hidden"`
	}
	if err := decodeStrict(raw, &props); err != nil || !sheetsSpreadsheet(props.SpreadsheetID) ||
		!sheetsSheetID(props.SheetID) || !oneOf(props.Dimension, "ROWS", "COLUMNS") ||
		props.StartIndex < 0 || props.EndIndex <= props.StartIndex {
		return errors.New("invalid Google Sheets update-dimension-properties properties")
	}
	return nil
}

func validateSheetsSheetProperties(raw json.RawMessage) error {
	var props struct {
		SpreadsheetID     string `json:"spreadsheet_id"`
		SheetID           int    `json:"sheet_id"`
		FrozenRowCount    int    `json:"frozen_row_count"`
		FrozenColumnCount int    `json:"frozen_column_count"`
		TabColor          string `json:"tab_color"`
		Hidden            bool   `json:"hidden"`
	}
	if err := decodeStrict(raw, &props); err != nil || !sheetsSpreadsheet(props.SpreadsheetID) ||
		!sheetsSheetID(props.SheetID) || props.FrozenRowCount < 0 || props.FrozenColumnCount < 0 ||
		(props.TabColor != "" && !boundedValue(props.TabColor, 32)) {
		return errors.New("invalid Google Sheets update-sheet-properties properties")
	}
	return nil
}

func validateSheetsFormat(raw json.RawMessage) error {
	var props struct {
		SpreadsheetID string `json:"spreadsheet_id"`
		SheetID       int    `json:"sheet_id"`
		StartingRow   int    `json:"starting_row"`
		EndingRow     int    `json:"ending_row"`
		BgColor       string `json:"bg_color"`
		TextColor     string `json:"text_color"`
		Bold          bool   `json:"bold"`
		Italic        bool   `json:"italic"`
		Strikethrough bool   `json:"strikethrough"`
	}
	if err := decodeStrict(raw, &props); err != nil || !sheetsSpreadsheet(props.SpreadsheetID) ||
		!sheetsSheetID(props.SheetID) || props.StartingRow < 1 ||
		(props.EndingRow != 0 && props.EndingRow < props.StartingRow) ||
		(props.BgColor != "" && !boundedValue(props.BgColor, 32)) ||
		(props.TextColor != "" && !boundedValue(props.TextColor, 32)) {
		return errors.New("invalid Google Sheets format-cells properties")
	}
	return nil
}

func validateSheetsExport(raw json.RawMessage) error {
	var props struct {
		SpreadsheetID string `json:"spreadsheet_id"`
		SheetID       int    `json:"sheet_id"`
		Format        string `json:"format"`
		ReturnAsText  bool   `json:"return_as_text"`
	}
	if err := decodeStrict(raw, &props); err != nil || !sheetsSpreadsheet(props.SpreadsheetID) ||
		!sheetsSheetID(props.SheetID) || !oneOf(props.Format, "csv", "tsv") || props.ReturnAsText {
		return errors.New("invalid Google Sheets export-worksheet properties")
	}
	return nil
}
