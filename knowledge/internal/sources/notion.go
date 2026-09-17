package sources

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	knowledgev1 "github.com/Shubham04-oss/oryxa-go/contracts/events/knowledge/v1"
	"github.com/Shubham04-oss/oryxa-go/internal/providerapi/notion"

	"github.com/Shubham04-oss/oryxa-go/knowledge/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/knowledge/internal/transform"
)

// notionMaxPage is the most Notion answers with at once, for search and for
// a data source query alike.
const notionMaxPage = 100

// Notion reads the pages an integration can see: either every page search
// turns up, or the rows of one database. A database's schema and rows moved
// into a data source underneath it in the API version this client speaks
// (2025-09-03), so indexing "a database" means resolving it to that data
// source first and querying the data source, never the database, for rows.
type Notion struct {
	client *Client
	server string
}

func NewNotion(client *Client) *Notion {
	return &Notion{client: client, server: notion.Server}
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
func (source *Notion) Fetch(ctx context.Context, request Request) (Page, error) {
	client, err := notion.NewClientWithResponses(source.server,
		notion.WithHTTPClient(source.client.For(request.Credential)),
		notion.WithRequestEditorFn(notion.Versioned))
	if err != nil {
		return Page{}, fmt.Errorf("build the Notion client: %w", err)
	}

	size := Limit(request.Limit, notionMaxPage)
	var cursor *string
	if value := request.Cursor.Value; value != "" {
		cursor = &value
	}

	database := strings.TrimSpace(request.Source.Target["database"])
	var list notionPageList
	if database == "" {
		list, err = notionSearch(ctx, client, int32(size), cursor)
		if err != nil {
			return Page{}, err
		}
	} else {
		dataSourceID, resolveErr := notionDataSourceID(ctx, client, database)
		if resolveErr != nil {
			return Page{}, resolveErr
		}
		list, err = notionQuery(ctx, client, dataSourceID, size, cursor)
		if err != nil {
			return Page{}, err
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
	if list.HasMore && list.NextCursor != nil {
		page.Cursor = *list.NextCursor
	}
	return page, nil
}

// notionSearch walks every page the connection can see. Notion's search
// takes no since filter the way GitHub's issue list does, so a full pass and
// an incremental one read the same way here; the watermark Fetch computes
// records what was seen rather than bounding what was asked for.
func notionSearch(ctx context.Context, client *notion.ClientWithResponses, size int32, cursor *string) (notionPageList, error) {
	property := "object"
	onlyPages := notion.PostSearchJSONBodyFilterValue("page")
	answer, err := client.PostSearchWithResponse(ctx, &notion.PostSearchParams{}, notion.PostSearchJSONRequestBody{
		Filter:      &notion.PostSearchJSONBody_Filter{Property: &property, Value: &onlyPages},
		PageSize:    &size,
		StartCursor: cursor,
	})
	if err != nil {
		return notionPageList{}, providerError(err)
	}
	if answer.JSON200 == nil {
		return notionPageList{}, StatusError(answer.HTTPResponse.StatusCode, answer.HTTPResponse.Header)
	}
	return notionDecodePages(answer.Body)
}

func notionQuery(ctx context.Context, client *notion.ClientWithResponses, dataSourceID string, size int, cursor *string) (notionPageList, error) {
	answer, err := client.QueryDataSourceWithResponse(ctx, dataSourceID, &notion.QueryDataSourceParams{}, notion.QueryDataSourceJSONRequestBody{
		PageSize:    &size,
		StartCursor: cursor,
	})
	if err != nil {
		return notionPageList{}, providerError(err)
	}
	if answer.JSON200 == nil {
		return notionPageList{}, StatusError(answer.HTTPResponse.StatusCode, answer.HTTPResponse.Header)
	}
	return notionDecodePages(answer.Body)
}

// notionDataSourceID resolves a database to the data source Fetch actually
// queries. A database made before the 2025-09-03 split still has exactly
// one; this reads that one and does not try to choose among several.
func notionDataSourceID(ctx context.Context, client *notion.ClientWithResponses, databaseID string) (string, error) {
	answer, err := client.RetrieveADatabaseWithResponse(ctx, databaseID, &notion.RetrieveADatabaseParams{})
	if err != nil {
		return "", providerError(err)
	}
	if answer.JSON200 == nil {
		return "", StatusError(answer.HTTPResponse.StatusCode, answer.HTTPResponse.Header)
	}
	var database struct {
		DataSources []struct {
			ID string `json:"id"`
		} `json:"data_sources"`
	}
	if err := json.Unmarshal(answer.Body, &database); err != nil {
		return "", fmt.Errorf("read the database's answer: %w", err)
	}
	if len(database.DataSources) == 0 {
		return "", fmt.Errorf("database %q has no data source: %w", databaseID, domain.ErrInvalid)
	}
	return database.DataSources[0].ID, nil
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
