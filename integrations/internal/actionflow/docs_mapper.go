package actionflow

import (
	"encoding/json"

	integrationevents "github.com/Shubham04-oss/oryxa-go/contracts/events/integrations/v1"
)

func mapGoogleDocsAction(request MapRequest) (MappedResult, bool, error) {
	switch request.ActionName {
	case "create-document", "create-text-document", "create-document-from-markdown", "create-document-from-template":
		result, err := mapGoogleDocsDocument(request, "created")
		return result, true, err
	case "copy-document":
		result, err := mapGoogleDocsDocument(request, "copied")
		return result, true, err
	case "get-document", "read-document":
		result, err := mapGoogleDocsDocument(request, "")
		return result, true, err
	case "get-document-plaintext":
		result, err := mapGoogleDocsPlaintext(request)
		return result, true, err
	case "get-document-end-index":
		result, err := mapGoogleDocsEndIndex(request)
		return result, true, err
	case "search-documents":
		result, err := mapGoogleDocsSearch(request)
		return result, true, err
	case "export-as-pdf":
		result, err := mapStoredFileLookup(request.Output)
		return result, true, err
	case "docs-custom-api-call":
		result, err := mapProviderHTTPCall(request)
		return result, true, err
	case "append-text", "insert-text", "replace-all-text", "delete-content-range",
		"replace-body-with-markdown", "replace-section-with-markdown", "insert-page-break",
		"create-header", "delete-header", "create-footer", "delete-footer", "create-footnote",
		"create-named-range", "delete-named-range", "create-paragraph-bullets", "delete-paragraph-bullets",
		"insert-table", "insert-table-row", "insert-table-column", "delete-table-row", "delete-table-column",
		"insert-text-in-table-cell", "insert-image-in-table-cell", "unmerge-table-cells",
		"update-table-row-style", "create-and-populate-table", "insert-inline-image",
		"replace-image", "update-document-style", "batch-update":
		result, err := mapGoogleDocsDocument(request, "mutated")
		return result, true, err
	default:
		return MappedResult{}, false, nil
	}
}

func mapGoogleDocsDocument(request MapRequest, kind string) (MappedResult, error) {
	payload := unwrapHTTPBody(request.Output)
	var output struct {
		DocumentID string `json:"documentId"`
		ID         string `json:"id"`
		Title      string `json:"title"`
		RevisionID string `json:"revisionId"`
	}
	_ = json.Unmarshal(payload, &output)
	var props struct {
		DocumentID string `json:"documentId"`
		FileID     string `json:"fileId"`
		Template   string `json:"template"`
	}
	_ = json.Unmarshal(request.Props, &props)
	id := firstNonEmpty(output.DocumentID, output.ID, props.DocumentID, props.FileID, props.Template)
	if id == "" {
		return MappedResult{}, ErrPieceOutputInvalid
	}
	result, err := json.Marshal(struct {
		DocumentID string `json:"document_id"`
		Title      string `json:"title,omitempty"`
		RevisionID string `json:"revision_id,omitempty"`
	}{DocumentID: id, Title: output.Title, RevisionID: output.RevisionID})
	if err != nil {
		return MappedResult{}, ErrPieceOutputInvalid
	}
	switch kind {
	case "created":
		return MappedResult{SafeResult: result, DetailType: integrationevents.GoogleDocsDocumentCreatedDetailType,
			Data: integrationevents.GoogleDocsDocumentCreated{
				ExecutionID: request.Execution.ID, ConnectionID: request.Connection.ID, DocumentID: id,
				RequestedBy: request.Execution.MemberID, CreatedAt: request.OccurredAt,
			}}, nil
	case "copied":
		return MappedResult{SafeResult: result, DetailType: integrationevents.GoogleDocsDocumentCopiedDetailType,
			Data: integrationevents.GoogleDocsDocumentCopied{
				ExecutionID: request.Execution.ID, ConnectionID: request.Connection.ID, DocumentID: id,
				RequestedBy: request.Execution.MemberID, CopiedAt: request.OccurredAt,
			}}, nil
	case "mutated":
		return MappedResult{SafeResult: result, DetailType: integrationevents.GoogleDocsDocumentMutatedDetailType,
			Data: integrationevents.GoogleDocsDocumentMutated{
				ExecutionID: request.Execution.ID, ConnectionID: request.Connection.ID, DocumentID: id,
				RequestedBy: request.Execution.MemberID, MutatedAt: request.OccurredAt,
			}}, nil
	default:
		return MappedResult{SafeResult: result}, nil
	}
}

func mapGoogleDocsPlaintext(request MapRequest) (MappedResult, error) {
	payload := unwrapHTTPBody(request.Output)
	text := docsPlaintext(payload)
	var props struct {
		DocumentID string `json:"documentId"`
	}
	_ = json.Unmarshal(request.Props, &props)
	if props.DocumentID == "" {
		return MappedResult{}, ErrPieceOutputInvalid
	}
	capped, truncated := capText(text, 32*1024)
	return googleJSON(struct {
		DocumentID string `json:"document_id"`
		Text       string `json:"text,omitempty"`
		Truncated  bool   `json:"truncated"`
	}{DocumentID: props.DocumentID, Text: capped, Truncated: truncated})
}

func mapGoogleDocsEndIndex(request MapRequest) (MappedResult, error) {
	payload := unwrapHTTPBody(request.Output)
	var output struct {
		EndIndex int `json:"endIndex"`
		Index    int `json:"index"`
	}
	_ = json.Unmarshal(payload, &output)
	index := output.EndIndex
	if index == 0 {
		index = output.Index
	}
	var props struct {
		DocumentID string `json:"documentId"`
	}
	_ = json.Unmarshal(request.Props, &props)
	if props.DocumentID == "" {
		return MappedResult{}, ErrPieceOutputInvalid
	}
	return googleJSON(struct {
		DocumentID string `json:"document_id"`
		EndIndex   int    `json:"end_index"`
	}{DocumentID: props.DocumentID, EndIndex: index})
}

func mapGoogleDocsSearch(request MapRequest) (MappedResult, error) {
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

func docsPlaintext(raw json.RawMessage) string {
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text
	}
	var wrapped struct {
		Text    string `json:"text"`
		Content string `json:"content"`
		Body    string `json:"body"`
	}
	_ = json.Unmarshal(raw, &wrapped)
	return firstNonEmpty(wrapped.Text, wrapped.Content, wrapped.Body)
}

func capText(value string, maximum int) (string, bool) {
	if len(value) <= maximum {
		return value, false
	}
	return value[:maximum], true
}
