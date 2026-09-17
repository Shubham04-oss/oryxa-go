package sources

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	knowledgev1 "github.com/Shubham04-oss/oryxa-go/contracts/events/knowledge/v1"

	"github.com/Shubham04-oss/oryxa-go/knowledge/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/knowledge/internal/transform"
)

// notionAPI is where Notion's REST API lives.
const notionAPI = "https://api.notion.com"

// notionAPIVersion is the Notion-Version every call carries.
const notionAPIVersion = "2025-09-03"

// notionMaxPage is the most Notion answers with at once, for search and for
// a data source query alike.
const notionMaxPage = 100

// Notion reads the pages an integration can see: either every page search
// turns up, or the rows of one database. A database's schema and rows moved
// into one or more data sources underneath it in the API version this client
// speaks (2025-09-03), so indexing "a database" means resolving it to those
// data sources first and querying each of them, never the database, for
// rows. There is no generated client here: Composio holds the credential, so
// this speaks the REST API directly, hand-rolled on the package's shared
// client.
type Notion struct {
	client *Client
	api    string
}

func NewNotion(client *Client) *Notion {
	return &Notion{client: client, api: notionAPI}
}

func (source *Notion) Provider() string { return "notion" }

// Target accepts {"database": "<id>"} to index one database's rows, or no
// target at all to index every page the connection can see.
func (source *Notion) Target(target map[string]string) error {
	database, named := target["database"]
	if named && strings.TrimSpace(database) == "" {
		return MissingTarget("a database id")
	}
	return nil
}

// Fetch walks a page at a time behind Notion's own start_cursor/next_cursor,
// the cleanest pagination of any provider this package reads: the cursor
// Fetch hands back is exactly the one Notion handed it, empty once has_more
// says there is nothing left. A page's real content lives in its block
// children, a second call per page this version does not make; what it
// keeps as the body instead is a page's title and its properties, which
// still gives a retrieval pipeline something to work with until that gap is
// closed.
//
// A database can hold several data sources since the 2025-09-03 split, and
// every one of them holds rows worth indexing. Fetch walks them in turn: the
// cursor it hands back, once one data source is drained, names the next one
// to query rather than ending the walk, so the source is not drained until
// every data source is.
func (source *Notion) Fetch(ctx context.Context, request Request) (Page, error) {
	size := Limit(request.Limit, notionMaxPage)
	database := strings.TrimSpace(request.Source.Target["database"])

	var list notionPageList
	var nextCursor string
	var err error
	if database == "" {
		var cursor *string
		if value := request.Cursor.Value; value != "" {
			cursor = &value
		}
		list, err = source.search(ctx, request.Credential, size, cursor)
		if err != nil {
			return Page{}, err
		}
		if list.HasMore && list.NextCursor != nil {
			nextCursor = *list.NextCursor
		}
	} else {
		dataSourceIDs, resolveErr := source.dataSourceIDs(ctx, request.Credential, database)
		if resolveErr != nil {
			return Page{}, resolveErr
		}
		index, inner := decodeNotionCursor(request.Cursor.Value)
		if index < 0 || index >= len(dataSourceIDs) {
			index, inner = 0, "" // a cursor from before, or outside, this database starts over
		}
		var cursor *string
		if inner != "" {
			cursor = &inner
		}
		list, err = source.query(ctx, request.Credential, dataSourceIDs[index], size, cursor)
		if err != nil {
			return Page{}, err
		}
		switch {
		case list.HasMore && list.NextCursor != nil:
			nextCursor = encodeNotionCursor(index, *list.NextCursor)
		case index+1 < len(dataSourceIDs):
			nextCursor = encodeNotionCursor(index+1, "")
		}
	}

	page := Page{Items: make([]Item, 0, len(list.Results))}
	for _, entry := range list.Results {
		if entry.Object != "page" {
			continue
		}
		page.Items = append(page.Items, notionItem(entry))
		page.Watermark = watermarkAfter(page.Watermark, entry.LastEditedTime)
	}
	page.Cursor = nextCursor
	return page, nil
}

// search walks every page the connection can see. Notion's search takes no
// since filter the way GitHub's issue list does, so a full pass and an
// incremental one read the same way here; the watermark Fetch computes
// records what was seen rather than bounding what was asked for.
func (source *Notion) search(ctx context.Context, credential domain.Credential, size int, cursor *string) (notionPageList, error) {
	body, err := json.Marshal(notionSearchBody{
		Filter:      notionSearchFilter{Property: "object", Value: "page"},
		PageSize:    size,
		StartCursor: cursor,
	})
	if err != nil {
		return notionPageList{}, fmt.Errorf("encode the Notion search: %w", err)
	}
	payload, err := source.call(ctx, credential, http.MethodPost, "/v1/search", body)
	if err != nil {
		return notionPageList{}, err
	}
	return notionDecodePages(payload)
}

func (source *Notion) query(ctx context.Context, credential domain.Credential, dataSourceID string, size int, cursor *string) (notionPageList, error) {
	body, err := json.Marshal(notionQueryBody{PageSize: size, StartCursor: cursor})
	if err != nil {
		return notionPageList{}, fmt.Errorf("encode the Notion query: %w", err)
	}
	payload, err := source.call(ctx, credential, http.MethodPost,
		"/v1/data_sources/"+url.PathEscape(dataSourceID)+"/query", body)
	if err != nil {
		return notionPageList{}, err
	}
	return notionDecodePages(payload)
}

// dataSourceIDs resolves a database to every data source Fetch queries it
// through. A database made before the 2025-09-03 split still has exactly
// one; a database split since can have several, and rows live under all of
// them, not only the first.
func (source *Notion) dataSourceIDs(ctx context.Context, credential domain.Credential, databaseID string) ([]string, error) {
	payload, err := source.call(ctx, credential, http.MethodGet, "/v1/databases/"+url.PathEscape(databaseID), nil)
	if err != nil {
		return nil, err
	}
	var database struct {
		DataSources []struct {
			ID string `json:"id"`
		} `json:"data_sources"`
	}
	if err := json.Unmarshal(payload, &database); err != nil {
		return nil, fmt.Errorf("read the database's answer: %w", err)
	}
	if len(database.DataSources) == 0 {
		return nil, fmt.Errorf("database %q has no data source: %w", databaseID, domain.ErrInvalid)
	}
	ids := make([]string, len(database.DataSources))
	for i, dataSource := range database.DataSources {
		ids[i] = dataSource.ID
	}
	return ids, nil
}

// call sends one request against Notion's API and returns its raw body,
// already checked for a 2xx status by Client.Do.
func (source *Notion) call(ctx context.Context, credential domain.Credential, method, path string, body []byte) ([]byte, error) {
	var httpRequest *http.Request
	var err error
	if body != nil {
		httpRequest, err = JSONRequest(ctx, method, source.api+path, body)
	} else {
		httpRequest, err = http.NewRequestWithContext(ctx, method, source.api+path, nil)
		if err == nil {
			httpRequest.Header.Set("Accept", "application/json")
		}
	}
	if err != nil {
		return nil, fmt.Errorf("build the Notion request: %w", err)
	}
	httpRequest.Header.Set("Notion-Version", notionAPIVersion)

	response, err := source.client.Do(ctx, httpRequest, credential)
	if err != nil {
		return nil, err
	}
	return response.Body, nil
}

// notionSearchFilter narrows a search to pages, never a data source.
type notionSearchFilter struct {
	Property string `json:"property"`
	Value    string `json:"value"`
}

type notionSearchBody struct {
	Filter      notionSearchFilter `json:"filter"`
	PageSize    int                `json:"page_size"`
	StartCursor *string            `json:"start_cursor,omitempty"`
}

type notionQueryBody struct {
	PageSize    int     `json:"page_size"`
	StartCursor *string `json:"start_cursor,omitempty"`
}

// notionCursor is Fetch's own cursor for a database with several data
// sources: which one is being read, and Notion's own cursor within it.
// Encoded as JSON so it round-trips through the plain text column a cursor
// is stored in without needing a delimiter Notion's own cursor is promised
// never to contain.
type notionCursor struct {
	Index  int    `json:"i"`
	Cursor string `json:"c,omitempty"`
}

// decodeNotionCursor reads back what encodeNotionCursor wrote. Anything
// unreadable — including a plain Notion cursor a walk already under way
// saved before a database could hold more than one data source — starts
// over at the first data source rather than failing the run; Fetch treats a
// negative or out-of-range index the same way.
func decodeNotionCursor(value string) (index int, cursor string) {
	if value == "" {
		return 0, ""
	}
	var decoded notionCursor
	if err := json.Unmarshal([]byte(value), &decoded); err != nil {
		return 0, ""
	}
	return decoded.Index, decoded.Cursor
}

func encodeNotionCursor(index int, cursor string) string {
	encoded, err := json.Marshal(notionCursor{Index: index, Cursor: cursor})
	if err != nil {
		// Index is an int and Cursor a string: both always encode.
		return ""
	}
	return string(encoded)
}

func notionDecodePages(body []byte) (notionPageList, error) {
	var list notionPageList
	if err := json.Unmarshal(body, &list); err != nil {
		return notionPageList{}, fmt.Errorf("read the provider's answer: %w", err)
	}
	return list, nil
}

// notionPageList is the page of results both search and a data source query
// answer with, behind Notion's own cursor.
type notionPageList struct {
	Results    []notionPage `json:"results"`
	HasMore    bool         `json:"has_more"`
	NextCursor *string      `json:"next_cursor"`
}

// notionPage is the slice of a Notion page object Fetch reads. Properties
// stays raw because its shape depends on the schema of whatever database the
// page belongs to, which is not known ahead of time.
type notionPage struct {
	ID             string                     `json:"id"`
	Object         string                     `json:"object"`
	URL            string                     `json:"url"`
	LastEditedTime time.Time                  `json:"last_edited_time"`
	Properties     map[string]json.RawMessage `json:"properties"`
	Parent         struct {
		Type string `json:"type"`
	} `json:"parent"`
}

func notionItem(page notionPage) Item {
	updated := page.LastEditedTime.UTC()
	title, body := notionPageText(page.Properties)
	var metadata map[string]string
	if page.Parent.Type != "" {
		metadata = map[string]string{"parent_type": page.Parent.Type}
	}
	return Item{
		ExternalID: page.ID,
		Kind:       knowledgev1.KindPage,
		Title:      title,
		URL:        page.URL,
		MIME:       transform.MIMEMarkdown,
		Content:    []byte(body),
		Metadata:   metadata,
		UpdatedAt:  &updated,
	}
}

// notionPageText turns a page's properties into the text Fetch keeps: a
// title heading, then one line per other property whose value
// notionPropertyText knows how to render.
func notionPageText(properties map[string]json.RawMessage) (title, body string) {
	names := make([]string, 0, len(properties))
	for name := range properties {
		names = append(names, name)
	}
	sort.Strings(names)

	var lines []string
	for _, name := range names {
		var property notionProperty
		if err := json.Unmarshal(properties[name], &property); err != nil {
			continue
		}
		value := notionPropertyText(property)
		if property.Type == "title" {
			if title == "" {
				title = value
			}
			continue
		}
		if value != "" {
			lines = append(lines, fmt.Sprintf("- %s: %s", name, value))
		}
	}

	var builder strings.Builder
	if title != "" {
		builder.WriteString("# " + title)
		if len(lines) > 0 {
			builder.WriteString("\n\n")
		}
	}
	builder.WriteString(strings.Join(lines, "\n"))
	return title, builder.String()
}

// notionProperty is a page property's value, for the column types
// notionPropertyText knows how to render.
type notionProperty struct {
	Type        string         `json:"type"`
	Title       []notionText   `json:"title"`
	RichText    []notionText   `json:"rich_text"`
	Select      *notionOption  `json:"select"`
	Status      *notionOption  `json:"status"`
	MultiSelect []notionOption `json:"multi_select"`
	People      []notionOption `json:"people"`
	Number      *float64       `json:"number"`
	Checkbox    *bool          `json:"checkbox"`
	URL         *string        `json:"url"`
	Email       *string        `json:"email"`
	PhoneNumber *string        `json:"phone_number"`
	Date        *notionDate    `json:"date"`
}

// notionText is a fragment of Notion's rich text, reduced to the plain
// string a caller reads.
type notionText struct {
	PlainText string `json:"plain_text"`
}

// notionOption is a select, status, multi-select or person value's shared
// shape: something with a name.
type notionOption struct {
	Name string `json:"name"`
}

// notionDate is a date property's value, a range when End is set.
type notionDate struct {
	Start string `json:"start"`
	End   string `json:"end"`
}

// notionPropertyText renders one property's value for the column types a
// database schema commonly holds. A type not listed here is left out rather
// than guessed at.
func notionPropertyText(property notionProperty) string {
	switch property.Type {
	case "title":
		return notionPlainText(property.Title)
	case "rich_text":
		return notionPlainText(property.RichText)
	case "select":
		return notionOptionName(property.Select)
	case "status":
		return notionOptionName(property.Status)
	case "multi_select":
		return notionOptionNames(property.MultiSelect)
	case "people":
		return notionOptionNames(property.People)
	case "number":
		if property.Number == nil {
			return ""
		}
		return strconv.FormatFloat(*property.Number, 'f', -1, 64)
	case "checkbox":
		if property.Checkbox == nil {
			return ""
		}
		return strconv.FormatBool(*property.Checkbox)
	case "url":
		return notionString(property.URL)
	case "email":
		return notionString(property.Email)
	case "phone_number":
		return notionString(property.PhoneNumber)
	case "date":
		return notionDateText(property.Date)
	default:
		return ""
	}
}

func notionPlainText(fragments []notionText) string {
	parts := make([]string, len(fragments))
	for i, fragment := range fragments {
		parts[i] = fragment.PlainText
	}
	return strings.Join(parts, "")
}

func notionOptionName(option *notionOption) string {
	if option == nil {
		return ""
	}
	return option.Name
}

func notionOptionNames(options []notionOption) string {
	names := make([]string, len(options))
	for i, option := range options {
		names[i] = option.Name
	}
	return strings.Join(names, ", ")
}

func notionString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func notionDateText(date *notionDate) string {
	if date == nil {
		return ""
	}
	if date.End != "" {
		return date.Start + " to " + date.End
	}
	return date.Start
}
