package sources

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	knowledgev1 "github.com/Shubham04-oss/oryxa-go/contracts/events/knowledge/v1"

	"github.com/Shubham04-oss/oryxa-go/knowledge/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/knowledge/internal/transform"
)

// driveAPI is where Drive's REST API v3 lives.
const driveAPI = "https://www.googleapis.com/drive/v3"

// driveMaxPage is what this extractor asks files.list and changes.list for at
// once. Drive allows up to 1000; a page is the unit of work a lease has to
// cover, and a smaller one resumes more precisely.
const driveMaxPage = 100

// driveFileFields is the metadata Fetch asks Drive for on every file, whether
// listing, walking changes, or resolving a folder's children. md5Checksum is
// read but not relied on for change detection — Fetch hashes the content it
// actually reads, the same as every other source — because Google Docs,
// Sheets and Slides carry none at all.
const driveFileFields = "id,name,mimeType,modifiedTime,webViewLink,size,md5Checksum,trashed,parents"

// driveFolderMIME is what Drive calls a folder. Fetch walks into one rather
// than indexing it as a document.
const driveFolderMIME = "application/vnd.google-apps.folder"

// driveExportTargets says which of Drive's own document types this reads, and
// the plain-text media type each is asked to export as. Slides and Docs both
// export as plain text; Sheets as CSV, since that is what a spreadsheet's
// rows actually are. Drive can also export a Doc as text/markdown, which
// would read better, but that is a newer capability nothing here has run
// against a live account to confirm, so this asks for the type the task is
// certain Drive has always supported — see the package README.
var driveExportTargets = map[string]string{
	"application/vnd.google-apps.document":     transform.MIMEPlain,
	"application/vnd.google-apps.presentation": transform.MIMEPlain,
	"application/vnd.google-apps.spreadsheet":  transform.MIMECSV,
}

// drivePassthroughTypes are the binary types Fetch downloads whole rather
// than exporting, for a caller with somewhere to keep such a file's bytes —
// see transform.IsPassthroughMIME, which this matches exactly.
var drivePassthroughTypes = map[string]bool{
	transform.MIMEPDF:  true,
	transform.MIMEDOC:  true,
	transform.MIMEDOCX: true,
	transform.MIMEXLS:  true,
	transform.MIMEXLSX: true,
	transform.MIMEPPT:  true,
	transform.MIMEPPTX: true,
}

// GoogleDrive reads a member's Drive: the whole of it by default, or one
// folder. There is no generated client here any more than for GitHub, Notion
// or Slack — Composio holds the credential, so this speaks the REST API
// directly, hand-rolled on the package's shared client.
type GoogleDrive struct {
	client *Client
	api    string
}

func NewGoogleDrive(client *Client) *GoogleDrive { return &GoogleDrive{client: client, api: driveAPI} }

func (source *GoogleDrive) Provider() string { return "googledrive" }

// Target accepts {"folder": "<id>"} to index one folder, recursively, or no
// target at all to index the whole of the member's Drive.
func (source *GoogleDrive) Target(target map[string]string) error {
	if folder, named := target["folder"]; named && strings.TrimSpace(folder) == "" {
		return MissingTarget("a folder id")
	}
	return nil
}

// Fetch reads one page of a member's Drive. A full pass without a folder
// target reads files.list once, unscoped: Drive already answers everything
// the account can see without this walking anything itself. A full pass
// scoped to a folder walks it breadth first, since Drive's query language has
// no operator for "under this folder, however deep" — only direct children —
// so nested folders are queued and visited in turn; see fullFolder. An
// incremental pass reads the changes feed instead of either.
func (source *GoogleDrive) Fetch(ctx context.Context, request Request) (Page, error) {
	folder := strings.TrimSpace(request.Source.Target["folder"])
	if !request.Full {
		return source.incremental(ctx, request, folder)
	}
	if folder == "" {
		return source.fullFlat(ctx, request)
	}
	return source.fullFolder(ctx, request, folder)
}

// fullFlat reads the whole of a member's Drive, unscoped. Drive's own
// pageToken is the cursor as is: nothing here needs to remember more than
// that, since there is no folder tree to walk.
func (source *GoogleDrive) fullFlat(ctx context.Context, request Request) (Page, error) {
	size := Limit(request.Limit, driveMaxPage)
	list, err := source.list(ctx, request.Credential, "trashed = false", size, request.Cursor.Value)
	if err != nil {
		return Page{}, err
	}
	page, err := source.itemsFor(ctx, request.Credential, list.Files)
	if err != nil {
		return Page{}, err
	}
	if list.NextPageToken != "" {
		page.Cursor = list.NextPageToken
		return page, nil
	}
	token, err := source.startPageToken(ctx, request.Credential)
	if err != nil {
		return Page{}, err
	}
	page.Token = token
	return page, nil
}

// driveWalkCursor is Fetch's own cursor for a folder-scoped full pass: current
// is the folder whose children this page reads, page is Drive's own page
// token within it, and pending holds every subfolder discovered so far that
// still needs a visit of its own. Encoded as JSON so all three survive in the
// one string column a cursor is stored in.
type driveWalkCursor struct {
	Current string   `json:"current"`
	Page    string   `json:"page,omitempty"`
	Pending []string `json:"pending,omitempty"`
}

// fullFolder walks one folder and everything under it, breadth first. Every
// page reads one folder's direct children; a subfolder found along the way is
// queued rather than descended into immediately, so the walk's own place —
// which folder, and where in Drive's paging of it — always fits in one
// cursor.
func (source *GoogleDrive) fullFolder(ctx context.Context, request Request, folder string) (Page, error) {
	walk := driveWalkCursor{Current: folder}
	if request.Cursor.Value != "" {
		if err := json.Unmarshal([]byte(request.Cursor.Value), &walk); err != nil || walk.Current == "" {
			// Unreadable, or from before a folder target existed on this
			// source: safest is to start the walk over rather than fail the
			// run outright.
			walk = driveWalkCursor{Current: folder}
		}
	}

	size := Limit(request.Limit, driveMaxPage)
	q := "'" + escapeDriveQueryValue(walk.Current) + "' in parents and trashed = false"
	list, err := source.list(ctx, request.Credential, q, size, walk.Page)
	if err != nil {
		return Page{}, err
	}

	var files []*driveFile
	for _, file := range list.Files {
		if file.MimeType == driveFolderMIME {
			walk.Pending = append(walk.Pending, file.ID)
			continue
		}
		files = append(files, file)
	}
	page, err := source.itemsFor(ctx, request.Credential, files)
	if err != nil {
		return Page{}, err
	}

	if list.NextPageToken != "" {
		walk.Page = list.NextPageToken
		page.Cursor = mustEncodeJSON(walk)
		return page, nil
	}
	// This folder is drained. Move on to the next one queued, if any.
	if len(walk.Pending) > 0 {
		next := driveWalkCursor{Current: walk.Pending[0], Pending: walk.Pending[1:]}
		page.Cursor = mustEncodeJSON(next)
		return page, nil
	}
	// The whole tree is drained.
	token, err := source.startPageToken(ctx, request.Credential)
	if err != nil {
		return Page{}, err
	}
	page.Token = token
	return page, nil
}

// incremental reads the changes feed from the watermark a previous full pass
// left, or a previous incremental pass resumed from.
//
// Unscoped, every change in it belongs to this source: changes.list watches
// the whole of a Drive, which is exactly what an unscoped target means.
// Scoped to a folder, a change is kept only when the file's own parents name
// that folder directly — a change to a file nested inside a subfolder is not
// caught here, only at the source's next full sweep, which walks the whole
// tree again. Documented rather than solved: matching a deeply nested file
// would mean this pass knowing the whole current folder tree, which only a
// full walk actually has.
func (source *GoogleDrive) incremental(ctx context.Context, request Request, folder string) (Page, error) {
	startToken := request.Cursor.Value
	if startToken == "" {
		startToken = request.Cursor.Token
	}
	if startToken == "" {
		// Nothing to resume from at all: ask Drive where "now" is rather than
		// fail the run. Sync always gives a source its first pass as a full
		// one, so this is only reached if a source somehow lost its watermark.
		token, err := source.startPageToken(ctx, request.Credential)
		if err != nil {
			return Page{}, err
		}
		return Page{Token: token}, nil
	}

	list, err := source.changes(ctx, request.Credential, startToken, Limit(request.Limit, driveMaxPage))
	if err != nil {
		return Page{}, err
	}
	page := Page{}
	var files []*driveFile
	for _, change := range list.Changes {
		if change.FileID == "" {
			continue
		}
		if change.Removed || (change.File != nil && change.File.Trashed) {
			page.Removed = append(page.Removed, change.FileID)
			continue
		}
		if change.File == nil {
			continue
		}
		if folder != "" && !containsString(change.File.Parents, folder) {
			continue
		}
		files = append(files, change.File)
	}
	rest, err := source.itemsFor(ctx, request.Credential, files)
	if err != nil {
		return Page{}, err
	}
	page.Items = rest.Items

	if list.NextPageToken != "" {
		page.Cursor = list.NextPageToken
		return page, nil
	}
	page.Token = list.NewStartPageToken
	return page, nil
}

// itemsFor turns a page of Drive files into items, reading each one's content
// as its type says to. A file nothing here understands is left out, silently:
// an image, a video, an archive is not something whose absence is worth
// logging on every single pass over an ordinary Drive.
func (source *GoogleDrive) itemsFor(ctx context.Context, credential domain.Credential, files []*driveFile) (Page, error) {
	page := Page{Items: make([]Item, 0, len(files))}
	for _, file := range files {
		item, ok, err := source.toItem(ctx, credential, file)
		if err != nil {
			return Page{}, err
		}
		if !ok {
			continue
		}
		page.Items = append(page.Items, item)
		if updated, err := time.Parse(time.RFC3339, file.ModifiedTime); err == nil {
			page.Watermark = watermarkAfter(page.Watermark, updated)
		}
	}
	return page, nil
}

// toItem reads one file's content, or says to pass over it: a folder, a type
// nothing here reads, or a binary file too large to keep.
func (source *GoogleDrive) toItem(ctx context.Context, credential domain.Credential, file *driveFile) (Item, bool, error) {
	if file.MimeType == driveFolderMIME {
		return Item{}, false, nil
	}
	if exportMIME, ok := driveExportTargets[file.MimeType]; ok {
		content, err := source.export(ctx, credential, file.ID, exportMIME)
		if err != nil {
			if skippable(err) {
				return Item{}, false, nil
			}
			return Item{}, false, err
		}
		return driveItem(file, exportMIME, content), true, nil
	}
	if strings.HasPrefix(file.MimeType, "application/vnd.google-apps.") {
		// A Form, a Site, a Drawing, a Jam board and the rest: Google's own
		// types this does not know how to export as text.
		return Item{}, false, nil
	}
	if !drivePassthroughTypes[file.MimeType] {
		return Item{}, false, nil
	}
	if file.Size > 0 && file.Size > MaxOriginalBytes {
		// Too large to keep, counted as skipped like anything else this pass
		// leaves out. Checked before downloading anything: Drive already told
		// us the size in the same listing that gave us the file at all.
		return Item{}, false, nil
	}
	content, err := source.download(ctx, credential, file.ID)
	if err != nil {
		if skippable(err) {
			return Item{}, false, nil
		}
		return Item{}, false, err
	}
	if len(content) > MaxOriginalBytes {
		return Item{}, false, nil
	}
	return driveItem(file, file.MimeType, content), true, nil
}

// skippable says whether a file disappearing or becoming unreadable between
// being listed and being read is something this pass steps over, the same
// way files.go treats a file whose retention lapsed. A full pass that no
// longer sees it retracts it in due course; an incremental one saw it
// removed through the very same changes feed, likely in this same page.
func skippable(err error) bool {
	return errors.Is(err, domain.ErrNotFound) || errors.Is(err, domain.ErrForbidden)
}

func driveItem(file *driveFile, mimeType string, content []byte) Item {
	metadata := map[string]string{}
	if file.MD5Checksum != "" {
		metadata["md5_checksum"] = file.MD5Checksum
	}
	var updated *time.Time
	if parsed, err := time.Parse(time.RFC3339, file.ModifiedTime); err == nil {
		parsed = parsed.UTC()
		updated = &parsed
	}
	return Item{
		ExternalID: file.ID,
		Kind:       knowledgev1.KindFile,
		Title:      file.Name,
		URL:        file.WebViewLink,
		MIME:       mimeType,
		Content:    content,
		Metadata:   metadata,
		UpdatedAt:  updated,
	}
}

// export asks Drive to render one of its own document types as plain text,
// CSV or the like. Composio's proxy never returns this inline — Drive's own
// answer is not JSON — so the request carries AllowBinary, and it is read
// through DoWithLimit rather than Do since an exported document can
// legitimately be larger than the page of JSON Do is sized for.
func (source *GoogleDrive) export(ctx context.Context, credential domain.Credential, fileID, mimeType string) ([]byte, error) {
	endpoint := source.api + "/files/" + url.PathEscape(fileID) + "/export?mimeType=" + url.QueryEscape(mimeType)
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("build the Drive export request: %w", err)
	}
	response, err := source.client.DoWithLimit(AllowBinary(ctx), httpRequest, credential, MaxOriginalBytes)
	if err != nil {
		return nil, err
	}
	return response.Body, nil
}

// download reads a binary file's bytes whole, through alt=media.
func (source *GoogleDrive) download(ctx context.Context, credential domain.Credential, fileID string) ([]byte, error) {
	endpoint := source.api + "/files/" + url.PathEscape(fileID) + "?alt=media"
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("build the Drive download request: %w", err)
	}
	response, err := source.client.DoWithLimit(AllowBinary(ctx), httpRequest, credential, MaxOriginalBytes)
	if err != nil {
		return nil, err
	}
	return response.Body, nil
}

// driveFileList is what files.list answers: a page of files behind Drive's
// own pageToken.
type driveFileList struct {
	Files         []*driveFile `json:"files"`
	NextPageToken string       `json:"nextPageToken"`
}

// driveFile is the slice of Drive's file resource Fetch reads, named by
// driveFileFields.
type driveFile struct {
	ID           string   `json:"id"`
	Name         string   `json:"name"`
	MimeType     string   `json:"mimeType"`
	ModifiedTime string   `json:"modifiedTime"`
	WebViewLink  string   `json:"webViewLink"`
	Size         int64    `json:"size,string"`
	MD5Checksum  string   `json:"md5Checksum"`
	Trashed      bool     `json:"trashed"`
	Parents      []string `json:"parents"`
}

// list calls files.list once, already scoped by q and paged by pageToken.
func (source *GoogleDrive) list(ctx context.Context, credential domain.Credential, q string, pageSize int, pageToken string) (driveFileList, error) {
	query := url.Values{
		"q":        {q},
		"pageSize": {strconv.Itoa(pageSize)},
		"fields":   {"nextPageToken,files(" + driveFileFields + ")"},
		"spaces":   {"drive"},
	}
	if pageToken != "" {
		query.Set("pageToken", pageToken)
	}
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodGet, source.api+"/files?"+query.Encode(), nil)
	if err != nil {
		return driveFileList{}, fmt.Errorf("build the Drive files.list request: %w", err)
	}
	response, err := source.client.Do(ctx, httpRequest, credential)
	if err != nil {
		return driveFileList{}, err
	}
	var list driveFileList
	if err := json.Unmarshal(response.Body, &list); err != nil {
		return driveFileList{}, fmt.Errorf("%w: Drive's file list could not be read", domain.ErrProviderUnavailable)
	}
	return list, nil
}

// driveChangeList is what changes.list answers: a page of changes behind
// Drive's own pageToken, and, once drained, newStartPageToken — the watermark
// for the next incremental pass.
type driveChangeList struct {
	Changes           []driveChange `json:"changes"`
	NextPageToken     string        `json:"nextPageToken"`
	NewStartPageToken string        `json:"newStartPageToken"`
}

// driveChange is one entry of the changes feed: a file that changed, or
// removed true when it — or the whole Drive it lived on — is gone from the
// caller's view entirely, in which case File is absent.
type driveChange struct {
	FileID  string     `json:"fileId"`
	Removed bool       `json:"removed"`
	File    *driveFile `json:"file"`
}

// changes calls changes.list once, from pageToken, asking for enough of a
// changed file to decide whether it is still in scope and what to read.
func (source *GoogleDrive) changes(ctx context.Context, credential domain.Credential, pageToken string, pageSize int) (driveChangeList, error) {
	query := url.Values{
		"pageToken":         {pageToken},
		"pageSize":          {strconv.Itoa(pageSize)},
		"fields":            {"nextPageToken,newStartPageToken,changes(fileId,removed,file(" + driveFileFields + "))"},
		"includeRemoved":    {"true"},
		"restrictToMyDrive": {"false"},
	}
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodGet, source.api+"/changes?"+query.Encode(), nil)
	if err != nil {
		return driveChangeList{}, fmt.Errorf("build the Drive changes.list request: %w", err)
	}
	response, err := source.client.Do(ctx, httpRequest, credential)
	if err != nil {
		return driveChangeList{}, err
	}
	var list driveChangeList
	if err := json.Unmarshal(response.Body, &list); err != nil {
		return driveChangeList{}, fmt.Errorf("%w: Drive's changes could not be read", domain.ErrProviderUnavailable)
	}
	return list, nil
}

// startPageToken asks Drive where "now" is, so the pass that just drained —
// full or incremental — knows where the next incremental one should resume
// from.
func (source *GoogleDrive) startPageToken(ctx context.Context, credential domain.Credential) (string, error) {
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodGet, source.api+"/changes/startPageToken", nil)
	if err != nil {
		return "", fmt.Errorf("build the Drive startPageToken request: %w", err)
	}
	response, err := source.client.Do(ctx, httpRequest, credential)
	if err != nil {
		return "", err
	}
	var answer struct {
		StartPageToken string `json:"startPageToken"`
	}
	if err := json.Unmarshal(response.Body, &answer); err != nil {
		return "", fmt.Errorf("%w: Drive's start page token could not be read", domain.ErrProviderUnavailable)
	}
	return answer.StartPageToken, nil
}

// escapeDriveQueryValue escapes a value placed inside a single-quoted string
// in a Drive query. A backslash or a single quote in the value must itself be
// backslash-escaped, or Drive reads the query as malformed rather than as
// naming a literal id.
func escapeDriveQueryValue(value string) string {
	replacer := strings.NewReplacer(`\`, `\\`, `'`, `\'`)
	return replacer.Replace(value)
}

// containsString says whether value is one of list — a file's own direct
// parents, or a message's label ids.
func containsString(list []string, value string) bool {
	for _, entry := range list {
		if entry == value {
			return true
		}
	}
	return false
}

// mustEncodeJSON encodes a cursor this package built itself, whose fields are
// always plain strings and a slice of them — always representable — so a
// failure here would be a bug in this file, not bad input from anywhere else.
func mustEncodeJSON(value any) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		return ""
	}
	return string(encoded)
}
