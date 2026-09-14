package catalog

import (
	"encoding/json"
	"errors"
	"strings"
)

const (
	microsoftSitesReadScope      = "Sites.Read.All"
	microsoftSitesReadWriteScope = "Sites.ReadWrite.All"
	microsoftSitesManageScope    = "Sites.Manage.All"
	microsoftFilesReadWriteAll   = "Files.ReadWrite.All"
)

var microsoftSharePointActions = map[string]microsoftActionSpec{
	"create-sharepoint-folder": {
		MicrosoftSharePointPieceName, MicrosoftSharePointPieceVersion, "microsoft_sharepoint_create_folder",
		[]string{microsoftSitesReadWriteScope}, validateSharePointCreateFolder, nil,
	},
	"create-list": {
		MicrosoftSharePointPieceName, MicrosoftSharePointPieceVersion, "microsoft_sharepoint_create_list",
		[]string{microsoftSitesReadWriteScope}, validateSharePointCreateList, nil,
	},
	"create-list-item": {
		MicrosoftSharePointPieceName, MicrosoftSharePointPieceVersion, "microsoft_sharepoint_create_list_item",
		[]string{microsoftSitesReadWriteScope}, validateSharePointListItemWrite, nil,
	},
	"update-list-item": {
		MicrosoftSharePointPieceName, MicrosoftSharePointPieceVersion, "microsoft_sharepoint_update_list_item",
		[]string{microsoftSitesReadWriteScope}, validateSharePointListItemUpdate, nil,
	},
	"delete-list-item": {
		MicrosoftSharePointPieceName, MicrosoftSharePointPieceVersion, "microsoft_sharepoint_delete_list_item",
		[]string{microsoftSitesReadWriteScope}, validateSharePointListItemID, nil,
	},
	"find-list-item": {
		MicrosoftSharePointPieceName, MicrosoftSharePointPieceVersion, "microsoft_sharepoint_search_list_item",
		[]string{microsoftSitesReadScope}, validateSharePointSearchListItem, nil,
	},
	"upload-sharepoint-file": {
		MicrosoftSharePointPieceName, MicrosoftSharePointPieceVersion, "microsoft_sharepoint_upload_file",
		[]string{microsoftFilesReadWriteAll}, validateSharePointUploadFile, nil,
	},
	"publish-page": {
		MicrosoftSharePointPieceName, MicrosoftSharePointPieceVersion, "microsoft_sharepoint_publish_page",
		[]string{microsoftSitesManageScope}, validateSharePointPublishPage, nil,
	},
	"copy-sharepoint-item": {
		MicrosoftSharePointPieceName, MicrosoftSharePointPieceVersion, "microsoft_sharepoint_copy_item",
		[]string{microsoftSitesReadWriteScope}, validateSharePointCopyAcross, nil,
	},
	"copy-item-within-site": {
		MicrosoftSharePointPieceName, MicrosoftSharePointPieceVersion, "microsoft_sharepoint_copy_item_within_site",
		[]string{microsoftSitesReadWriteScope}, validateSharePointCopyWithin, nil,
	},
	"move-sharepoint-file": {
		MicrosoftSharePointPieceName, MicrosoftSharePointPieceVersion, "microsoft_sharepoint_move_file",
		[]string{microsoftSitesReadWriteScope}, validateSharePointMoveFile, nil,
	},
	"find-sharepoint-file": {
		MicrosoftSharePointPieceName, MicrosoftSharePointPieceVersion, "microsoft_sharepoint_find_file",
		[]string{microsoftSitesReadScope}, validateSharePointFindFile, nil,
	},
	"get-folder-contents": {
		MicrosoftSharePointPieceName, MicrosoftSharePointPieceVersion, "microsoft_sharepoint_get_folder_contents",
		[]string{microsoftSitesReadScope}, validateSharePointFolderContents, nil,
	},
	"get-site-information": {
		MicrosoftSharePointPieceName, MicrosoftSharePointPieceVersion, "microsoft_sharepoint_get_site_information",
		[]string{microsoftSitesReadScope}, validateSharePointGetSite, nil,
	},
	"find-site": {
		MicrosoftSharePointPieceName, MicrosoftSharePointPieceVersion, "microsoft_sharepoint_find_site",
		[]string{microsoftSitesReadScope}, validateSharePointFindSite, nil,
	},
	"sharepoint-custom-api-call": {
		MicrosoftSharePointPieceName, MicrosoftSharePointPieceVersion, "custom_api_call",
		[]string{microsoftSitesReadScope}, validateCustomAPICall("graph.microsoft.com"), prepareCustomAPICall,
	},
}

var microsoftSharePointNeedsFiles = map[string]bool{"upload-sharepoint-file": true}

func sharePointPath(value string) bool {
	return value != "" && len(value) <= 1024 && strings.TrimSpace(value) == value &&
		!strings.Contains(value, "..") && !strings.ContainsAny(value, "\r\n\t")
}

func boundedJSONObject(raw json.RawMessage, maximum int) bool {
	return boundedJSON(raw, maximum) && raw[0] == '{'
}

func validateSharePointCreateFolder(raw json.RawMessage) error {
	var props struct {
		SiteID       string `json:"siteId"`
		DriveID      string `json:"driveId"`
		ParentFolder string `json:"parentFolder"`
		FolderName   string `json:"folderName"`
	}
	if err := decodeStrict(raw, &props); err != nil || !graphID(props.SiteID) || !graphID(props.DriveID) ||
		!sharePointPath(props.ParentFolder) || !boundedValue(props.FolderName, 255) {
		return errors.New("invalid Microsoft SharePoint create-folder properties")
	}
	return nil
}

func validateSharePointCreateList(raw json.RawMessage) error {
	var props struct {
		SiteID      string `json:"siteId"`
		DisplayName string `json:"displayName"`
		Description string `json:"description"`
	}
	if err := decodeStrict(raw, &props); err != nil || !graphID(props.SiteID) ||
		!boundedValue(props.DisplayName, 200) || !boundedValue(props.Description, 2000) {
		return errors.New("invalid Microsoft SharePoint create-list properties")
	}
	return nil
}

func validateSharePointListItemWrite(raw json.RawMessage) error {
	var props struct {
		SiteID      string          `json:"siteId"`
		ListID      string          `json:"listId"`
		ListColumns json.RawMessage `json:"listColumns"`
	}
	if err := decodeStrict(raw, &props); err != nil || !graphID(props.SiteID) || !graphID(props.ListID) ||
		!boundedJSONObject(props.ListColumns, 16*1024) {
		return errors.New("invalid Microsoft SharePoint list-item properties")
	}
	return nil
}

func validateSharePointListItemUpdate(raw json.RawMessage) error {
	var props struct {
		SiteID      string          `json:"siteId"`
		ListID      string          `json:"listId"`
		ListItemID  string          `json:"listItemId"`
		ListColumns json.RawMessage `json:"listColumns"`
	}
	if err := decodeStrict(raw, &props); err != nil || !graphID(props.SiteID) || !graphID(props.ListID) ||
		!graphID(props.ListItemID) || !boundedJSONObject(props.ListColumns, 16*1024) {
		return errors.New("invalid Microsoft SharePoint update-list-item properties")
	}
	return nil
}

func validateSharePointListItemID(raw json.RawMessage) error {
	var props struct {
		SiteID     string `json:"siteId"`
		ListID     string `json:"listId"`
		ListItemID string `json:"listItemId"`
	}
	if err := decodeStrict(raw, &props); err != nil || !graphID(props.SiteID) || !graphID(props.ListID) ||
		!graphID(props.ListItemID) {
		return errors.New("invalid Microsoft SharePoint list-item identity properties")
	}
	return nil
}

func validateSharePointSearchListItem(raw json.RawMessage) error {
	var props struct {
		SiteID      string `json:"siteId"`
		ListID      string `json:"listId"`
		SearchValue string `json:"searchValue"`
	}
	if err := decodeStrict(raw, &props); err != nil || !graphID(props.SiteID) || !graphID(props.ListID) ||
		!boundedValue(props.SearchValue, 500) {
		return errors.New("invalid Microsoft SharePoint find-list-item properties")
	}
	return nil
}

func validateSharePointUploadFile(raw json.RawMessage) error {
	var props struct {
		SiteID       string  `json:"siteId"`
		DriveID      string  `json:"driveId"`
		File         fileRef `json:"file"`
		ParentFolder string  `json:"parentFolder"`
		FileName     string  `json:"fileName"`
	}
	if err := decodeStrict(raw, &props); err != nil || !graphID(props.SiteID) || !graphID(props.DriveID) ||
		!oryxaFileID(props.File.FileID) || !sharePointPath(props.ParentFolder) || !boundedFilename(props.FileName) {
		return errors.New("invalid Microsoft SharePoint upload-file properties")
	}
	return nil
}

func validateSharePointPublishPage(raw json.RawMessage) error {
	var props struct {
		SiteID string `json:"siteId"`
		PageID string `json:"pageId"`
	}
	if err := decodeStrict(raw, &props); err != nil || !graphID(props.SiteID) || !graphID(props.PageID) {
		return errors.New("invalid Microsoft SharePoint publish-page properties")
	}
	return nil
}

func validateSharePointCopyAcross(raw json.RawMessage) error {
	var props struct {
		SiteID              string `json:"siteId"`
		DriveID             string `json:"driveId"`
		ItemID              string `json:"itemId"`
		DestinationSiteID   string `json:"destination_site_id"`
		DestinationDriveID  string `json:"destination_drive_id"`
		DestinationFolderID string `json:"destination_folder_id"`
		NewName             string `json:"new_name"`
		ConflictBehavior    string `json:"conflict_behavior"`
	}
	if err := decodeStrict(raw, &props); err != nil || !graphID(props.SiteID) || !graphID(props.DriveID) ||
		!graphID(props.ItemID) || !graphID(props.DestinationSiteID) || !graphID(props.DestinationDriveID) ||
		(props.DestinationFolderID != "" && !graphID(props.DestinationFolderID)) ||
		(props.NewName != "" && !boundedValue(props.NewName, 255)) ||
		!oneOf(props.ConflictBehavior, "fail", "replace", "rename") {
		return errors.New("invalid Microsoft SharePoint copy-item properties")
	}
	return nil
}

func validateSharePointCopyWithin(raw json.RawMessage) error {
	var props struct {
		SiteID              string `json:"siteId"`
		DriveID             string `json:"driveId"`
		ItemID              string `json:"itemId"`
		DestinationFolderID string `json:"destinationFolderId"`
		NewName             string `json:"newName"`
		ConflictBehavior    string `json:"conflictBehavior"`
	}
	if err := decodeStrict(raw, &props); err != nil || !graphID(props.SiteID) || !graphID(props.DriveID) ||
		!graphID(props.ItemID) || !graphID(props.DestinationFolderID) ||
		(props.NewName != "" && !boundedValue(props.NewName, 255)) ||
		!oneOf(props.ConflictBehavior, "fail", "replace", "rename") {
		return errors.New("invalid Microsoft SharePoint copy-item-within-site properties")
	}
	return nil
}

func validateSharePointMoveFile(raw json.RawMessage) error {
	var props struct {
		SiteID              string `json:"siteId"`
		DriveID             string `json:"driveId"`
		FileID              string `json:"fileId"`
		DestinationFolderID string `json:"destinationFolderId"`
		NewName             string `json:"newName"`
	}
	if err := decodeStrict(raw, &props); err != nil || !graphID(props.SiteID) || !graphID(props.DriveID) ||
		!graphID(props.FileID) || !graphID(props.DestinationFolderID) ||
		(props.NewName != "" && !boundedValue(props.NewName, 255)) {
		return errors.New("invalid Microsoft SharePoint move-file properties")
	}
	return nil
}

func validateSharePointFindFile(raw json.RawMessage) error {
	var props struct {
		SiteID         string `json:"siteId"`
		DriveID        string `json:"driveId"`
		FindMethod     string `json:"findMethod"`
		FilePath       string `json:"filePath"`
		SearchQuery    string `json:"searchQuery"`
		SearchFolderID string `json:"searchFolderId"`
	}
	if err := decodeStrict(raw, &props); err != nil || !graphID(props.SiteID) || !graphID(props.DriveID) ||
		!oneOf(props.FindMethod, "path", "search") ||
		(props.SearchFolderID != "" && !graphID(props.SearchFolderID)) {
		return errors.New("invalid Microsoft SharePoint find-file properties")
	}
	switch props.FindMethod {
	case "path":
		if !sharePointPath(props.FilePath) {
			return errors.New("invalid Microsoft SharePoint find-file properties")
		}
	default:
		if !boundedValue(props.SearchQuery, 500) {
			return errors.New("invalid Microsoft SharePoint find-file properties")
		}
	}
	return nil
}

func validateSharePointFolderContents(raw json.RawMessage) error {
	var props struct {
		SiteID              string `json:"siteId"`
		DriveID             string `json:"driveId"`
		FolderID            string `json:"folderId"`
		IncludeCustomFields bool   `json:"includeCustomFields"`
		Top                 int    `json:"top"`
		Select              string `json:"select"`
		Orderby             string `json:"orderby"`
	}
	if err := decodeStrict(raw, &props); err != nil || !graphID(props.SiteID) || !graphID(props.DriveID) ||
		!graphID(props.FolderID) || props.IncludeCustomFields || props.Top < 0 || props.Top > 100 ||
		(props.Select != "" && !boundedValue(props.Select, 500)) ||
		(props.Orderby != "" && !boundedValue(props.Orderby, 200)) {
		return errors.New("invalid Microsoft SharePoint get-folder-contents properties")
	}
	return nil
}

func validateSharePointGetSite(raw json.RawMessage) error {
	var props struct {
		SiteID string `json:"siteId"`
		Select string `json:"select"`
	}
	if err := decodeStrict(raw, &props); err != nil || !graphID(props.SiteID) ||
		(props.Select != "" && !boundedValue(props.Select, 500)) {
		return errors.New("invalid Microsoft SharePoint get-site-information properties")
	}
	return nil
}

func validateSharePointFindSite(raw json.RawMessage) error {
	var props struct {
		SearchTerm string `json:"searchTerm"`
	}
	if err := decodeStrict(raw, &props); err != nil || !boundedValue(props.SearchTerm, 200) {
		return errors.New("invalid Microsoft SharePoint find-site properties")
	}
	return nil
}
