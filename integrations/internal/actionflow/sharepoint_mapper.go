package actionflow

import (
	"encoding/json"

	integrationevents "github.com/Shubham04-oss/oryxa-go/contracts/events/integrations/v1"
)

func mapMicrosoftSharePointAction(request MapRequest) (MappedResult, bool, error) {
	switch request.ActionName {
	case "create-sharepoint-folder", "create-list", "create-list-item", "upload-sharepoint-file":
		result, err := mapMicrosoftSharePointItem(request, "created")
		return result, true, err
	case "copy-sharepoint-item", "copy-item-within-site":
		result, err := mapMicrosoftSharePointItem(request, "copied")
		return result, true, err
	case "update-list-item", "delete-list-item", "publish-page", "move-sharepoint-file":
		result, err := mapMicrosoftSharePointItem(request, "mutated")
		return result, true, err
	case "find-list-item", "find-sharepoint-file", "get-folder-contents", "get-site-information", "find-site":
		result, err := mapMicrosoftSharePointLookup(request)
		return result, true, err
	case "sharepoint-custom-api-call":
		result, err := mapProviderHTTPCall(request)
		return result, true, err
	default:
		return MappedResult{}, false, nil
	}
}

func mapMicrosoftSharePointItem(request MapRequest, kind string) (MappedResult, error) {
	file := decodeMicrosoftSharePointItem(request.Output)
	var props struct {
		ItemID     string `json:"itemId"`
		FileID     string `json:"fileId"`
		ListItemID string `json:"listItemId"`
		PageID     string `json:"pageId"`
	}
	_ = json.Unmarshal(request.Props, &props)
	id := firstNonEmpty(file.ID, props.ItemID, props.FileID, props.ListItemID, props.PageID)
	if id == "" {
		return MappedResult{}, ErrPieceOutputInvalid
	}
	if file.ID == "" {
		file.ID = id
	}
	if file.Name == "" {
		file.Name = id
	}
	result, err := json.Marshal(safeMicrosoftSharePointItem(file))
	if err != nil {
		return MappedResult{}, ErrPieceOutputInvalid
	}
	switch kind {
	case "created":
		return MappedResult{SafeResult: result, DetailType: integrationevents.MicrosoftSharePointItemCreatedDetailType,
			Data: integrationevents.MicrosoftSharePointItemCreated{
				ExecutionID: request.Execution.ID, ConnectionID: request.Connection.ID, ItemID: id,
				RequestedBy: request.Execution.MemberID, CreatedAt: request.OccurredAt,
			}}, nil
	case "copied":
		return MappedResult{SafeResult: result, DetailType: integrationevents.MicrosoftSharePointItemCopiedDetailType,
			Data: integrationevents.MicrosoftSharePointItemCopied{
				ExecutionID: request.Execution.ID, ConnectionID: request.Connection.ID, ItemID: id,
				RequestedBy: request.Execution.MemberID, CopiedAt: request.OccurredAt,
			}}, nil
	default:
		return MappedResult{SafeResult: result, DetailType: integrationevents.MicrosoftSharePointItemMutatedDetailType,
			Data: integrationevents.MicrosoftSharePointItemMutated{
				ExecutionID: request.Execution.ID, ConnectionID: request.Connection.ID, ItemID: id,
				RequestedBy: request.Execution.MemberID, MutatedAt: request.OccurredAt,
			}}, nil
	}
}

func mapMicrosoftSharePointLookup(request MapRequest) (MappedResult, error) {
	payload := unwrapHTTPBody(request.Output)
	var items []microsoftOneDriveFile
	if json.Unmarshal(payload, &items) != nil {
		var wrapped struct {
			Value  []microsoftOneDriveFile `json:"value"`
			ID     string                  `json:"id"`
			Name   string                  `json:"name"`
			WebURL string                  `json:"webUrl"`
		}
		if json.Unmarshal(payload, &wrapped) != nil {
			return MappedResult{}, ErrPieceOutputInvalid
		}
		if wrapped.Value != nil {
			items = wrapped.Value
		} else if wrapped.ID != "" {
			return googleJSON(safeMicrosoftSharePointItem(microsoftOneDriveFile{
				ID: wrapped.ID, Name: wrapped.Name, WebURL: wrapped.WebURL,
			}))
		} else {
			items = []microsoftOneDriveFile{}
		}
	}
	capped, truncated := googleCap(items, 100)
	safe := make([]any, 0, len(capped))
	for _, item := range capped {
		if item.ID == "" {
			return MappedResult{}, ErrPieceOutputInvalid
		}
		safe = append(safe, safeMicrosoftSharePointItem(item))
	}
	return googleJSON(struct {
		Items     []any `json:"items"`
		Truncated bool  `json:"truncated"`
	}{Items: safe, Truncated: truncated})
}

func decodeMicrosoftSharePointItem(raw json.RawMessage) microsoftOneDriveFile {
	payload := unwrapHTTPBody(raw)
	var file microsoftOneDriveFile
	_ = json.Unmarshal(payload, &file)
	if file.Name == "" {
		var named struct {
			DisplayName string `json:"displayName"`
		}
		_ = json.Unmarshal(payload, &named)
		file.Name = named.DisplayName
	}
	if file.ID != "" {
		return file
	}
	var wrapped struct {
		ID         string                `json:"id"`
		ResourceID string                `json:"resourceId"`
		File       microsoftOneDriveFile `json:"file"`
		Item       microsoftOneDriveFile `json:"item"`
	}
	_ = json.Unmarshal(payload, &wrapped)
	file = wrapped.File
	if file.ID == "" {
		file = wrapped.Item
	}
	if file.ID == "" {
		file.ID = firstNonEmpty(wrapped.ID, wrapped.ResourceID)
	}
	return file
}

func safeMicrosoftSharePointItem(file microsoftOneDriveFile) any {
	name := file.Name
	if name == "" {
		name = file.ID
	}
	return struct {
		ID        string `json:"id"`
		Name      string `json:"name,omitempty"`
		Size      int64  `json:"size,omitempty"`
		URL       string `json:"url,omitempty"`
		CreatedAt string `json:"created_at,omitempty"`
		UpdatedAt string `json:"updated_at,omitempty"`
	}{ID: file.ID, Name: name, Size: file.Size, URL: safeMicrosoftURL(file.WebURL),
		CreatedAt: file.CreatedDateTime, UpdatedAt: file.ModifiedDateTime}
}
