package catalog

import (
	"encoding/json"
	"errors"
	"strings"
)

const (
	googleDocsWriteScope = "https://www.googleapis.com/auth/documents"
	googleDocsReadScope  = "https://www.googleapis.com/auth/documents.readonly"
)

var (
	googleDocsWrite = []string{googleDocsWriteScope}
	googleDocsRead  = []string{googleDocsReadScope}
)

var googleDocsActions = map[string]googleActionSpec{
	"create-document":               {GoogleDocsPieceName, GoogleDocsPieceVersion, "create_document", googleDocsWrite, validateDocsCreate, nil},
	"create-text-document":          {GoogleDocsPieceName, GoogleDocsPieceVersion, "create_text_document", googleDocsWrite, validateDocsCreate, nil},
	"create-document-from-markdown": {GoogleDocsPieceName, GoogleDocsPieceVersion, "create_document_from_markdown", googleDocsWrite, validateDocsMarkdownCreate, nil},
	"create-document-from-template": {GoogleDocsPieceName, GoogleDocsPieceVersion, "create_document_based_on_template", googleDocsWrite, validateDocsTemplate, nil},
	"copy-document":                 {GoogleDocsPieceName, GoogleDocsPieceVersion, "copy_document", googleDocsWrite, validateDocsCopy, nil},
	"search-documents":              {GoogleDocsPieceName, GoogleDocsPieceVersion, "search_documents", googleDocsRead, validateDocsSearch, nil},
	"get-document":                  {GoogleDocsPieceName, GoogleDocsPieceVersion, "get_document", googleDocsRead, validateDocsDocument, nil},
	"get-document-plaintext":        {GoogleDocsPieceName, GoogleDocsPieceVersion, "get_document_plaintext", googleDocsRead, validateDocsDocument, nil},
	"read-document":                 {GoogleDocsPieceName, GoogleDocsPieceVersion, "read_document", googleDocsRead, validateDocsDocument, nil},
	"get-document-end-index":        {GoogleDocsPieceName, GoogleDocsPieceVersion, "get_document_end_index", googleDocsRead, validateDocsDocument, nil},
	"append-text":                   {GoogleDocsPieceName, GoogleDocsPieceVersion, "append_text", googleDocsWrite, validateDocsAppend, nil},
	"insert-text":                   {GoogleDocsPieceName, GoogleDocsPieceVersion, "insert_text", googleDocsWrite, validateDocsInsertText, nil},
	"replace-all-text":              {GoogleDocsPieceName, GoogleDocsPieceVersion, "replace_all_text", googleDocsWrite, validateDocsReplaceAll, nil},
	"delete-content-range":          {GoogleDocsPieceName, GoogleDocsPieceVersion, "delete_content_range", googleDocsWrite, validateDocsRange, nil},
	"replace-body-with-markdown":    {GoogleDocsPieceName, GoogleDocsPieceVersion, "replace_body_with_markdown", googleDocsWrite, validateDocsMarkdownBody, nil},
	"replace-section-with-markdown": {GoogleDocsPieceName, GoogleDocsPieceVersion, "replace_section_with_markdown", googleDocsWrite, validateDocsMarkdownSection, nil},
	"insert-page-break":             {GoogleDocsPieceName, GoogleDocsPieceVersion, "insert_page_break", googleDocsWrite, validateDocsOptionalIndex, nil},
	"create-header":                 {GoogleDocsPieceName, GoogleDocsPieceVersion, "create_header", googleDocsWrite, validateDocsHeaderFooter, nil},
	"delete-header":                 {GoogleDocsPieceName, GoogleDocsPieceVersion, "delete_header", googleDocsWrite, validateDocsDeleteHeader, nil},
	"create-footer":                 {GoogleDocsPieceName, GoogleDocsPieceVersion, "create_footer", googleDocsWrite, validateDocsHeaderFooter, nil},
	"delete-footer":                 {GoogleDocsPieceName, GoogleDocsPieceVersion, "delete_footer", googleDocsWrite, validateDocsDeleteFooter, nil},
	"create-footnote":               {GoogleDocsPieceName, GoogleDocsPieceVersion, "create_footnote", googleDocsWrite, validateDocsOptionalIndex, nil},
	"create-named-range":            {GoogleDocsPieceName, GoogleDocsPieceVersion, "create_named_range", googleDocsWrite, validateDocsCreateNamedRange, nil},
	"delete-named-range":            {GoogleDocsPieceName, GoogleDocsPieceVersion, "delete_named_range", googleDocsWrite, validateDocsDeleteNamedRange, nil},
	"create-paragraph-bullets":      {GoogleDocsPieceName, GoogleDocsPieceVersion, "create_paragraph_bullets", googleDocsWrite, validateDocsBullets, nil},
	"delete-paragraph-bullets":      {GoogleDocsPieceName, GoogleDocsPieceVersion, "delete_paragraph_bullets", googleDocsWrite, validateDocsRange, nil},
	"insert-table":                  {GoogleDocsPieceName, GoogleDocsPieceVersion, "insert_table", googleDocsWrite, validateDocsInsertTable, nil},
	"insert-table-row":              {GoogleDocsPieceName, GoogleDocsPieceVersion, "insert_table_row", googleDocsWrite, validateDocsTableCell, nil},
	"insert-table-column":           {GoogleDocsPieceName, GoogleDocsPieceVersion, "insert_table_column", googleDocsWrite, validateDocsTableCell, nil},
	"delete-table-row":              {GoogleDocsPieceName, GoogleDocsPieceVersion, "delete_table_row", googleDocsWrite, validateDocsTableIndex, nil},
	"delete-table-column":           {GoogleDocsPieceName, GoogleDocsPieceVersion, "delete_table_column", googleDocsWrite, validateDocsTableIndex, nil},
	"insert-text-in-table-cell":     {GoogleDocsPieceName, GoogleDocsPieceVersion, "insert_text_in_table_cell", googleDocsWrite, validateDocsTableText, nil},
	"insert-image-in-table-cell":    {GoogleDocsPieceName, GoogleDocsPieceVersion, "insert_image_in_table_cell", googleDocsWrite, validateDocsTableImage, nil},
	"unmerge-table-cells":           {GoogleDocsPieceName, GoogleDocsPieceVersion, "unmerge_table_cells", googleDocsWrite, validateDocsUnmerge, nil},
	"update-table-row-style":        {GoogleDocsPieceName, GoogleDocsPieceVersion, "update_table_row_style", googleDocsWrite, validateDocsTableRowStyle, nil},
	"create-and-populate-table":     {GoogleDocsPieceName, GoogleDocsPieceVersion, "create_and_populate_table", googleDocsWrite, validateDocsPopulateTable, nil},
	"insert-inline-image":           {GoogleDocsPieceName, GoogleDocsPieceVersion, "insert_inline_image", googleDocsWrite, validateDocsInlineImage, nil},
	"replace-image":                 {GoogleDocsPieceName, GoogleDocsPieceVersion, "replace_image", googleDocsWrite, validateDocsReplaceImage, nil},
	"update-document-style":         {GoogleDocsPieceName, GoogleDocsPieceVersion, "update_document_style", googleDocsWrite, validateDocsStyle, nil},
	"export-as-pdf":                 {GoogleDocsPieceName, GoogleDocsPieceVersion, "export_as_pdf", googleDocsRead, validateDocsDocument, nil},
	"batch-update":                  {GoogleDocsPieceName, GoogleDocsPieceVersion, "batch_update", googleDocsWrite, validateDocsBatch, nil},
	"docs-custom-api-call":          {GoogleDocsPieceName, GoogleDocsPieceVersion, "custom_api_call", googleDocsRead, validateCustomAPICall("docs.googleapis.com"), prepareCustomAPICall},
}

var googleDocsNeedsFiles = map[string]bool{"export-as-pdf": true}

func validateDocsCreate(raw json.RawMessage) error {
	var props struct {
		Title string `json:"title"`
		Body  string `json:"body"`
	}
	if err := decodeStrict(raw, &props); err != nil || !boundedValue(props.Title, 200) ||
		len(props.Body) == 0 || len(props.Body) > 100*1024 {
		return errors.New("invalid Google Docs create-document properties")
	}
	return nil
}

func validateDocsMarkdownCreate(raw json.RawMessage) error {
	var props struct {
		Title    string `json:"title"`
		Markdown string `json:"markdown"`
	}
	if err := decodeStrict(raw, &props); err != nil || !boundedValue(props.Title, 200) ||
		len(props.Markdown) == 0 || len(props.Markdown) > 100*1024 {
		return errors.New("invalid Google Docs create-document-from-markdown properties")
	}
	return nil
}

func validateDocsTemplate(raw json.RawMessage) error {
	var props struct {
		Template          string            `json:"template"`
		Values            map[string]any    `json:"values"`
		Images            map[string]string `json:"images"`
		PlaceholderFormat string            `json:"placeholder_format"`
	}
	if err := decodeStrict(raw, &props); err != nil || !boundedIdentifier(props.Template, 128) ||
		len(props.Values) == 0 || len(props.Values) > 50 || !docsStringMap(props.Values, 8*1024) ||
		!docsImageMap(props.Images) || (props.PlaceholderFormat != "" &&
		!oneOf(props.PlaceholderFormat, "curly_braces", "square_brackets", "single_curly", "single_square")) {
		return errors.New("invalid Google Docs create-document-from-template properties")
	}
	return nil
}

func validateDocsCopy(raw json.RawMessage) error {
	var props struct {
		FileID string `json:"fileId"`
		Title  string `json:"title"`
		Parent string `json:"parents"`
	}
	if err := decodeStrict(raw, &props); err != nil || !boundedIdentifier(props.FileID, 128) ||
		!boundedValue(props.Title, 200) || (props.Parent != "" && !boundedIdentifier(props.Parent, 128)) {
		return errors.New("invalid Google Docs copy-document properties")
	}
	return nil
}

func validateDocsSearch(raw json.RawMessage) error {
	var props struct {
		NameContains     string `json:"name_contains"`
		FullTextContains string `json:"full_text_contains"`
		MaxResults       int    `json:"max_results"`
		OrderBy          string `json:"order_by"`
		PageToken        string `json:"page_token"`
	}
	if err := decodeStrict(raw, &props); err != nil ||
		(props.NameContains == "" && props.FullTextContains == "") ||
		len(props.NameContains) > 200 || len(props.FullTextContains) > 500 ||
		!googleMaxResults(props.MaxResults) ||
		(props.OrderBy != "" && !oneOf(props.OrderBy, "modifiedTime desc", "modifiedTime", "createdTime desc", "createdTime", "name", "name desc")) ||
		(props.PageToken != "" && !boundedValue(props.PageToken, 1024)) {
		return errors.New("invalid Google Docs search-documents properties")
	}
	return nil
}

func validateDocsDocument(raw json.RawMessage) error {
	var props struct {
		DocumentID string `json:"documentId"`
	}
	if err := decodeStrict(raw, &props); err != nil || !boundedIdentifier(props.DocumentID, 128) {
		return errors.New("invalid Google Docs document properties")
	}
	return nil
}

func validateDocsAppend(raw json.RawMessage) error {
	var props struct {
		DocumentID string `json:"documentId"`
		Text       string `json:"text"`
	}
	if err := decodeStrict(raw, &props); err != nil || !boundedIdentifier(props.DocumentID, 128) ||
		len(props.Text) == 0 || len(props.Text) > 100*1024 {
		return errors.New("invalid Google Docs append-text properties")
	}
	return nil
}

func validateDocsInsertText(raw json.RawMessage) error {
	var props struct {
		DocumentID string `json:"documentId"`
		Text       string `json:"text"`
		Index      int    `json:"index"`
	}
	if err := decodeStrict(raw, &props); err != nil || !boundedIdentifier(props.DocumentID, 128) ||
		len(props.Text) == 0 || len(props.Text) > 100*1024 || props.Index < 0 {
		return errors.New("invalid Google Docs insert-text properties")
	}
	return nil
}

func validateDocsReplaceAll(raw json.RawMessage) error {
	var props struct {
		DocumentID string `json:"documentId"`
		FindText   string `json:"find_text"`
		Replace    string `json:"replace_text"`
		MatchCase  bool   `json:"match_case"`
	}
	if err := decodeStrict(raw, &props); err != nil || !boundedIdentifier(props.DocumentID, 128) ||
		len(props.FindText) == 0 || len(props.FindText) > 2000 || len(props.Replace) > 100*1024 {
		return errors.New("invalid Google Docs replace-all-text properties")
	}
	return nil
}

func validateDocsRange(raw json.RawMessage) error {
	var props struct {
		DocumentID string `json:"documentId"`
		StartIndex int    `json:"startIndex"`
		EndIndex   int    `json:"endIndex"`
	}
	if err := decodeStrict(raw, &props); err != nil || !boundedIdentifier(props.DocumentID, 128) ||
		props.StartIndex < 1 || props.EndIndex <= props.StartIndex {
		return errors.New("invalid Google Docs range properties")
	}
	return nil
}

func validateDocsMarkdownBody(raw json.RawMessage) error {
	var props struct {
		DocumentID string `json:"documentId"`
		Markdown   string `json:"markdown"`
	}
	if err := decodeStrict(raw, &props); err != nil || !boundedIdentifier(props.DocumentID, 128) ||
		len(props.Markdown) == 0 || len(props.Markdown) > 100*1024 {
		return errors.New("invalid Google Docs replace-body-with-markdown properties")
	}
	return nil
}

func validateDocsMarkdownSection(raw json.RawMessage) error {
	var props struct {
		DocumentID string `json:"documentId"`
		StartIndex int    `json:"startIndex"`
		EndIndex   int    `json:"endIndex"`
		Markdown   string `json:"markdown"`
	}
	if err := decodeStrict(raw, &props); err != nil || !boundedIdentifier(props.DocumentID, 128) ||
		props.StartIndex < 1 || props.EndIndex <= props.StartIndex ||
		len(props.Markdown) == 0 || len(props.Markdown) > 100*1024 {
		return errors.New("invalid Google Docs replace-section-with-markdown properties")
	}
	return nil
}

func validateDocsOptionalIndex(raw json.RawMessage) error {
	var props struct {
		DocumentID string `json:"documentId"`
		Index      int    `json:"index"`
	}
	if err := decodeStrict(raw, &props); err != nil || !boundedIdentifier(props.DocumentID, 128) || props.Index < 0 {
		return errors.New("invalid Google Docs document index properties")
	}
	return nil
}

func validateDocsHeaderFooter(raw json.RawMessage) error {
	var props struct {
		DocumentID string `json:"documentId"`
		Type       string `json:"type"`
	}
	if err := decodeStrict(raw, &props); err != nil || !boundedIdentifier(props.DocumentID, 128) ||
		(props.Type != "" && props.Type != "DEFAULT") {
		return errors.New("invalid Google Docs header/footer properties")
	}
	return nil
}

func validateDocsDeleteHeader(raw json.RawMessage) error {
	var props struct {
		DocumentID string `json:"documentId"`
		HeaderID   string `json:"headerId"`
	}
	if err := decodeStrict(raw, &props); err != nil || !boundedIdentifier(props.DocumentID, 128) ||
		!boundedIdentifier(props.HeaderID, 128) {
		return errors.New("invalid Google Docs delete-header properties")
	}
	return nil
}

func validateDocsDeleteFooter(raw json.RawMessage) error {
	var props struct {
		DocumentID string `json:"documentId"`
		FooterID   string `json:"footerId"`
	}
	if err := decodeStrict(raw, &props); err != nil || !boundedIdentifier(props.DocumentID, 128) ||
		!boundedIdentifier(props.FooterID, 128) {
		return errors.New("invalid Google Docs delete-footer properties")
	}
	return nil
}

func validateDocsCreateNamedRange(raw json.RawMessage) error {
	var props struct {
		DocumentID string `json:"documentId"`
		Name       string `json:"name"`
		StartIndex int    `json:"startIndex"`
		EndIndex   int    `json:"endIndex"`
	}
	if err := decodeStrict(raw, &props); err != nil || !boundedIdentifier(props.DocumentID, 128) ||
		!boundedValue(props.Name, 100) || props.StartIndex < 1 || props.EndIndex <= props.StartIndex {
		return errors.New("invalid Google Docs create-named-range properties")
	}
	return nil
}

func validateDocsDeleteNamedRange(raw json.RawMessage) error {
	var props struct {
		DocumentID   string `json:"documentId"`
		NamedRangeID string `json:"namedRangeId"`
		Name         string `json:"name"`
	}
	if err := decodeStrict(raw, &props); err != nil || !boundedIdentifier(props.DocumentID, 128) ||
		(props.NamedRangeID == "" && props.Name == "") ||
		(props.NamedRangeID != "" && !boundedIdentifier(props.NamedRangeID, 128)) ||
		(props.Name != "" && !boundedValue(props.Name, 100)) {
		return errors.New("invalid Google Docs delete-named-range properties")
	}
	return nil
}

func validateDocsBullets(raw json.RawMessage) error {
	var props struct {
		DocumentID   string `json:"documentId"`
		StartIndex   int    `json:"startIndex"`
		EndIndex     int    `json:"endIndex"`
		BulletPreset string `json:"bulletPreset"`
	}
	if err := decodeStrict(raw, &props); err != nil || !boundedIdentifier(props.DocumentID, 128) ||
		props.StartIndex < 1 || props.EndIndex <= props.StartIndex ||
		(props.BulletPreset != "" && !strings.HasPrefix(props.BulletPreset, "BULLET_")) ||
		len(props.BulletPreset) > 80 {
		return errors.New("invalid Google Docs create-paragraph-bullets properties")
	}
	return nil
}

func validateDocsInsertTable(raw json.RawMessage) error {
	var props struct {
		DocumentID string `json:"documentId"`
		Rows       int    `json:"rows"`
		Columns    int    `json:"columns"`
		Index      int    `json:"index"`
	}
	if err := decodeStrict(raw, &props); err != nil || !boundedIdentifier(props.DocumentID, 128) ||
		props.Rows < 1 || props.Rows > 20 || props.Columns < 1 || props.Columns > 20 || props.Index < 0 {
		return errors.New("invalid Google Docs insert-table properties")
	}
	return nil
}

func validateDocsTableIndex(raw json.RawMessage) error {
	var props struct {
		DocumentID      string `json:"documentId"`
		TableStartIndex int    `json:"tableStartIndex"`
		RowIndex        int    `json:"rowIndex"`
		ColumnIndex     int    `json:"columnIndex"`
	}
	if err := decodeStrict(raw, &props); err != nil || !boundedIdentifier(props.DocumentID, 128) ||
		props.TableStartIndex < 1 || props.RowIndex < 0 || props.ColumnIndex < 0 {
		return errors.New("invalid Google Docs table index properties")
	}
	return nil
}

func validateDocsTableCell(raw json.RawMessage) error {
	var props struct {
		DocumentID      string `json:"documentId"`
		TableStartIndex int    `json:"tableStartIndex"`
		RowIndex        int    `json:"rowIndex"`
		ColumnIndex     int    `json:"columnIndex"`
		InsertRight     bool   `json:"insertRight"`
		InsertBelow     bool   `json:"insertBelow"`
	}
	if err := decodeStrict(raw, &props); err != nil || !boundedIdentifier(props.DocumentID, 128) ||
		props.TableStartIndex < 1 || props.RowIndex < 0 || props.ColumnIndex < 0 {
		return errors.New("invalid Google Docs table cell properties")
	}
	return nil
}

func validateDocsTableText(raw json.RawMessage) error {
	var props struct {
		DocumentID      string `json:"documentId"`
		TableStartIndex int    `json:"tableStartIndex"`
		RowIndex        int    `json:"rowIndex"`
		ColumnIndex     int    `json:"columnIndex"`
		Text            string `json:"text"`
	}
	if err := decodeStrict(raw, &props); err != nil || !boundedIdentifier(props.DocumentID, 128) ||
		props.TableStartIndex < 1 || props.RowIndex < 0 || props.ColumnIndex < 0 ||
		len(props.Text) == 0 || len(props.Text) > 32*1024 {
		return errors.New("invalid Google Docs insert-text-in-table-cell properties")
	}
	return nil
}

func validateDocsTableImage(raw json.RawMessage) error {
	var props struct {
		DocumentID      string `json:"documentId"`
		TableStartIndex int    `json:"tableStartIndex"`
		RowIndex        int    `json:"rowIndex"`
		ColumnIndex     int    `json:"columnIndex"`
		URI             string `json:"uri"`
		Width           int    `json:"width"`
		Height          int    `json:"height"`
	}
	if err := decodeStrict(raw, &props); err != nil || !boundedIdentifier(props.DocumentID, 128) ||
		props.TableStartIndex < 1 || props.RowIndex < 0 || props.ColumnIndex < 0 ||
		!publicHTTPSURL(props.URI) || props.Width < 0 || props.Height < 0 {
		return errors.New("invalid Google Docs insert-image-in-table-cell properties")
	}
	return nil
}

func validateDocsUnmerge(raw json.RawMessage) error {
	var props struct {
		DocumentID      string `json:"documentId"`
		TableStartIndex int    `json:"tableStartIndex"`
		RowIndex        int    `json:"rowIndex"`
		ColumnIndex     int    `json:"columnIndex"`
		RowSpan         int    `json:"rowSpan"`
		ColumnSpan      int    `json:"columnSpan"`
	}
	if err := decodeStrict(raw, &props); err != nil || !boundedIdentifier(props.DocumentID, 128) ||
		props.TableStartIndex < 1 || props.RowIndex < 0 || props.ColumnIndex < 0 ||
		props.RowSpan < 1 || props.ColumnSpan < 1 {
		return errors.New("invalid Google Docs unmerge-table-cells properties")
	}
	return nil
}

func validateDocsTableRowStyle(raw json.RawMessage) error {
	var props struct {
		DocumentID      string `json:"documentId"`
		TableStartIndex int    `json:"tableStartIndex"`
		RowIndices      []int  `json:"rowIndices"`
		MinRowHeightPt  int    `json:"minRowHeightPt"`
	}
	if err := decodeStrict(raw, &props); err != nil || !boundedIdentifier(props.DocumentID, 128) ||
		props.TableStartIndex < 1 || len(props.RowIndices) == 0 || len(props.RowIndices) > 50 {
		return errors.New("invalid Google Docs update-table-row-style properties")
	}
	for _, index := range props.RowIndices {
		if index < 0 {
			return errors.New("invalid Google Docs update-table-row-style properties")
		}
	}
	return nil
}

func validateDocsPopulateTable(raw json.RawMessage) error {
	var props struct {
		DocumentID string          `json:"documentId"`
		Data       json.RawMessage `json:"data"`
	}
	if err := decodeStrict(raw, &props); err != nil || !boundedIdentifier(props.DocumentID, 128) ||
		!boundedJSON(props.Data, 64*1024) {
		return errors.New("invalid Google Docs create-and-populate-table properties")
	}
	return nil
}

func validateDocsInlineImage(raw json.RawMessage) error {
	var props struct {
		DocumentID string `json:"documentId"`
		URI        string `json:"uri"`
		Index      int    `json:"index"`
		WidthPt    int    `json:"widthPt"`
		HeightPt   int    `json:"heightPt"`
	}
	if err := decodeStrict(raw, &props); err != nil || !boundedIdentifier(props.DocumentID, 128) ||
		!publicHTTPSURL(props.URI) || props.Index < 0 {
		return errors.New("invalid Google Docs insert-inline-image properties")
	}
	return nil
}

func validateDocsReplaceImage(raw json.RawMessage) error {
	var props struct {
		DocumentID    string `json:"documentId"`
		ImageObjectID string `json:"imageObjectId"`
		URI           string `json:"uri"`
	}
	if err := decodeStrict(raw, &props); err != nil || !boundedIdentifier(props.DocumentID, 128) ||
		!boundedIdentifier(props.ImageObjectID, 128) || !publicHTTPSURL(props.URI) {
		return errors.New("invalid Google Docs replace-image properties")
	}
	return nil
}

func validateDocsStyle(raw json.RawMessage) error {
	var props struct {
		DocumentID     string `json:"documentId"`
		MarginTopPt    int    `json:"marginTopPt"`
		MarginBottomPt int    `json:"marginBottomPt"`
		MarginLeftPt   int    `json:"marginLeftPt"`
		MarginRightPt  int    `json:"marginRightPt"`
	}
	if err := decodeStrict(raw, &props); err != nil || !boundedIdentifier(props.DocumentID, 128) {
		return errors.New("invalid Google Docs update-document-style properties")
	}
	return nil
}

func validateDocsBatch(raw json.RawMessage) error {
	var props struct {
		DocumentID string          `json:"documentId"`
		Requests   json.RawMessage `json:"requests"`
	}
	if err := decodeStrict(raw, &props); err != nil || !boundedIdentifier(props.DocumentID, 128) ||
		!boundedJSON(props.Requests, 64*1024) || len(props.Requests) == 0 || props.Requests[0] != '[' {
		return errors.New("invalid Google Docs batch-update properties")
	}
	return nil
}

func docsStringMap(values map[string]any, max int) bool {
	for key, value := range values {
		if !boundedValue(key, 100) {
			return false
		}
		text, ok := value.(string)
		if !ok || len(text) > max {
			return false
		}
	}
	return true
}

func docsImageMap(values map[string]string) bool {
	if len(values) > 20 {
		return false
	}
	for key, uri := range values {
		if !boundedValue(key, 100) || !publicHTTPSURL(uri) {
			return false
		}
	}
	return true
}

func boundedJSON(raw json.RawMessage, maximum int) bool {
	return len(raw) > 0 && len(raw) <= maximum && json.Valid(raw) && string(raw) != "null"
}
