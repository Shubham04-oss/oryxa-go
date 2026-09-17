package providers

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Shubham04-oss/oryxa-go/integrations/internal/domain"
)

// hubspotActionCredential is a bearer credential as connections places it for
// a HubSpot connection.
var hubspotActionCredential = domain.Credential{
	AccessToken: "pat-na1-abc", In: "header", Name: "Authorization", Prefix: "Bearer ",
}

// hubspotCall is one request the fake HubSpot captured.
type hubspotCall struct {
	Method string
	Path   string
	Query  string
	Auth   string
	Body   map[string]any
}

// hubspotServer answers with body and reports what each request sent.
func hubspotServer(t *testing.T, status int, body string) (*httptest.Server, *[]hubspotCall) {
	t.Helper()
	var calls []hubspotCall
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		raw, _ := io.ReadAll(request.Body)
		call := hubspotCall{
			Method: request.Method, Path: request.URL.Path, Query: request.URL.RawQuery,
			Auth: request.Header.Get("Authorization"),
		}
		if len(raw) > 0 {
			if err := json.Unmarshal(raw, &call.Body); err != nil {
				t.Errorf("request body is not JSON: %s", raw)
			}
		}
		calls = append(calls, call)
		writer.Header().Set("Content-Type", "application/json")
		writer.WriteHeader(status)
		_, _ = io.WriteString(writer, body)
	}))
	t.Cleanup(server.Close)
	return server, &calls
}

// runHubspotAction runs a named action against a provider pointed at server.
func runHubspotAction(t *testing.T, server *httptest.Server, name string, input map[string]any) (ActionResult, error) {
	t.Helper()
	provider := hubspotProviderAt(server.URL)
	action, ok := provider.Action(name)
	if !ok {
		t.Fatalf("provider has no action %q", name)
	}
	raw, err := json.Marshal(input)
	if err != nil {
		t.Fatalf("marshal input: %v", err)
	}
	return action.Run(context.Background(), server.Client(), ActionRequest{
		Credential: hubspotActionCredential, Input: raw, Now: fixedNow,
	})
}

// hubspotProviderAt is HubSpot pointed at a test server.
func hubspotProviderAt(apiURL string) Provider {
	provider := HubSpot()
	provider.Actions = hubspotActionsAt(apiURL)
	return provider
}

func TestHubspotCreateContact(t *testing.T) {
	server, calls := hubspotServer(t, http.StatusCreated, `{"id":"501","properties":{"email":"ada@example.com","firstname":"Ada"}}`)
	result, err := runHubspotAction(t, server, "create_contact", map[string]any{
		"email": "ada@example.com", "firstname": "Ada", "jobtitle": "Engineer",
	})
	if err != nil {
		t.Fatalf("create_contact error = %v", err)
	}
	if len(*calls) != 1 {
		t.Fatalf("calls = %d, want 1", len(*calls))
	}
	call := (*calls)[0]
	// The current generation is dated; the undated v3 paths are the legacy
	// ones, so this is what pins the action to the right API.
	if call.Method != http.MethodPost || call.Path != "/crm/objects/2026-09/contacts" {
		t.Fatalf("call = %s %s, want POST /crm/objects/2026-09/contacts", call.Method, call.Path)
	}
	if call.Auth != "Bearer pat-na1-abc" {
		t.Errorf("Authorization = %q, want the connection's credential", call.Auth)
	}
	properties, _ := call.Body["properties"].(map[string]any)
	if properties["email"] != "ada@example.com" || properties["jobtitle"] != "Engineer" {
		t.Errorf("properties = %v", properties)
	}
	// A property the caller did not name is not sent as an empty string.
	if _, ok := properties["lastname"]; ok {
		t.Errorf("properties carry an unnamed field: %v", properties)
	}
	if result.Resource.ID != "501" || result.Resource.Name != "ada@example.com" {
		t.Errorf("Resource = %+v", result.Resource)
	}
}

func TestHubspotCreateContactNeedsAnEmail(t *testing.T) {
	server, calls := hubspotServer(t, http.StatusCreated, `{}`)
	if _, err := runHubspotAction(t, server, "create_contact", map[string]any{"firstname": "Ada"}); err == nil {
		t.Fatal("create_contact without an email error = nil, want a refusal")
	}
	if len(*calls) != 0 {
		t.Fatalf("a refused input still called HubSpot %d times", len(*calls))
	}
}

func TestHubspotGetContact(t *testing.T) {
	server, calls := hubspotServer(t, http.StatusOK, `{"id":"501","properties":{"email":"ada@example.com"}}`)
	result, err := runHubspotAction(t, server, "get_contact", map[string]any{"contact_id": "501"})
	if err != nil {
		t.Fatalf("get_contact error = %v", err)
	}
	call := (*calls)[0]
	if call.Method != http.MethodGet || call.Path != "/crm/objects/2026-09/contacts/501" {
		t.Fatalf("call = %s %s", call.Method, call.Path)
	}
	// Reading without naming properties asks for the useful defaults, since
	// HubSpot returns almost nothing otherwise.
	if !strings.Contains(call.Query, "properties=") || !strings.Contains(call.Query, "email") {
		t.Errorf("query = %q, want the default properties", call.Query)
	}
	if result.Resource.ID != "501" {
		t.Errorf("Resource = %+v", result.Resource)
	}
}

func TestHubspotUpdateContact(t *testing.T) {
	t.Run("by id", func(t *testing.T) {
		server, calls := hubspotServer(t, http.StatusOK, `{"id":"501","properties":{"jobtitle":"Staff Engineer"}}`)
		if _, err := runHubspotAction(t, server, "update_contact", map[string]any{"contact_id": "501", "jobtitle": "Staff Engineer"}); err != nil {
			t.Fatalf("update_contact error = %v", err)
		}
		call := (*calls)[0]
		if call.Method != http.MethodPatch || call.Path != "/crm/objects/2026-09/contacts/501" {
			t.Fatalf("call = %s %s", call.Method, call.Path)
		}
		if call.Query != "" {
			t.Errorf("query = %q, want none", call.Query)
		}
	})

	// Updating by email is what lets a caller act on a search result without
	// resolving an ID first.
	t.Run("by email", func(t *testing.T) {
		server, calls := hubspotServer(t, http.StatusOK, `{"id":"501","properties":{}}`)
		if _, err := runHubspotAction(t, server, "update_contact", map[string]any{
			"contact_id": "ada@example.com", "id_property": "email", "phone": "+1 555 0100",
		}); err != nil {
			t.Fatalf("update_contact error = %v", err)
		}
		if got := (*calls)[0].Query; got != "idProperty=email" {
			t.Fatalf("query = %q, want idProperty=email", got)
		}
	})
}

func TestHubspotUpdateContactNeedsSomethingToChange(t *testing.T) {
	server, calls := hubspotServer(t, http.StatusOK, `{}`)
	if _, err := runHubspotAction(t, server, "update_contact", map[string]any{"contact_id": "501"}); err == nil {
		t.Fatal("update_contact with no properties error = nil, want a refusal")
	}
	if len(*calls) != 0 {
		t.Fatalf("a refused input still called HubSpot %d times", len(*calls))
	}
}

func TestHubspotSearchContacts(t *testing.T) {
	t.Run("free text", func(t *testing.T) {
		server, calls := hubspotServer(t, http.StatusOK, `{"total":1,"results":[{"id":"501","properties":{"email":"ada@example.com"}}]}`)
		result, err := runHubspotAction(t, server, "search_contacts", map[string]any{"query": "ada"})
		if err != nil {
			t.Fatalf("search_contacts error = %v", err)
		}
		call := (*calls)[0]
		if call.Method != http.MethodPost || call.Path != "/crm/objects/2026-09/contacts/search" {
			t.Fatalf("call = %s %s", call.Method, call.Path)
		}
		if call.Body["query"] != "ada" {
			t.Errorf("body = %v, want the query", call.Body)
		}
		if _, ok := call.Body["filterGroups"]; ok {
			t.Errorf("a text search sent a filter: %v", call.Body)
		}
		var answer struct {
			Contacts []map[string]string `json:"contacts"`
			Total    int                 `json:"total"`
		}
		if err := json.Unmarshal(result.Result, &answer); err != nil || len(answer.Contacts) != 1 || answer.Contacts[0]["email"] != "ada@example.com" {
			t.Fatalf("Result = %s, %v", result.Result, err)
		}
	})

	t.Run("an exact email becomes a filter", func(t *testing.T) {
		server, calls := hubspotServer(t, http.StatusOK, `{"total":0,"results":[]}`)
		if _, err := runHubspotAction(t, server, "search_contacts", map[string]any{"email": "ada@example.com"}); err != nil {
			t.Fatalf("search_contacts error = %v", err)
		}
		encoded, _ := json.Marshal((*calls)[0].Body)
		if !strings.Contains(string(encoded), `"propertyName":"email"`) || !strings.Contains(string(encoded), `"operator":"EQ"`) {
			t.Fatalf("body = %s, want an email filter", encoded)
		}
	})

	t.Run("neither a query nor an email is refused", func(t *testing.T) {
		server, calls := hubspotServer(t, http.StatusOK, `{}`)
		if _, err := runHubspotAction(t, server, "search_contacts", map[string]any{}); err == nil {
			t.Fatal("search_contacts with nothing to look for error = nil, want a refusal")
		}
		if len(*calls) != 0 {
			t.Fatalf("a refused input still called HubSpot %d times", len(*calls))
		}
	})

	t.Run("a limit beyond what HubSpot takes is refused", func(t *testing.T) {
		server, _ := hubspotServer(t, http.StatusOK, `{}`)
		if _, err := runHubspotAction(t, server, "search_contacts", map[string]any{"query": "ada", "limit": 500}); err == nil {
			t.Fatal("search_contacts with limit 500 error = nil, want a refusal")
		}
	})
}

func TestHubspotCreateCompany(t *testing.T) {
	server, calls := hubspotServer(t, http.StatusCreated, `{"id":"900","properties":{"name":"Acme","domain":"acme.com"}}`)
	result, err := runHubspotAction(t, server, "create_company", map[string]any{"name": "Acme", "domain": "acme.com"})
	if err != nil {
		t.Fatalf("create_company error = %v", err)
	}
	call := (*calls)[0]
	if call.Method != http.MethodPost || call.Path != "/crm/objects/2026-09/companies" {
		t.Fatalf("call = %s %s", call.Method, call.Path)
	}
	if result.Resource.Name != "Acme" {
		t.Errorf("Resource = %+v", result.Resource)
	}
}

func TestHubspotCreateNote(t *testing.T) {
	server, calls := hubspotServer(t, http.StatusCreated, `{"id":"77","properties":{}}`)
	if _, err := runHubspotAction(t, server, "create_note", map[string]any{"body": "Called about renewal", "contact_id": "501"}); err != nil {
		t.Fatalf("create_note error = %v", err)
	}
	call := (*calls)[0]
	if call.Method != http.MethodPost || call.Path != "/crm/objects/2026-09/notes" {
		t.Fatalf("call = %s %s", call.Method, call.Path)
	}
	properties, _ := call.Body["properties"].(map[string]any)
	if properties["hs_note_body"] != "Called about renewal" {
		t.Errorf("properties = %v, want the note body", properties)
	}
	// HubSpot requires a timestamp, so one is always sent even when the
	// caller names none.
	if timestamp, _ := properties["hs_timestamp"].(string); timestamp == "" {
		t.Errorf("properties = %v, want a timestamp", properties)
	}
	// A note that is not associated with a contact is invisible in the CRM.
	encoded, _ := json.Marshal(call.Body["associations"])
	if !strings.Contains(string(encoded), `"associationTypeId":202`) {
		t.Fatalf("associations = %s, want the note-to-contact association", encoded)
	}
}

func TestHubspotCreateNoteNeedsAContact(t *testing.T) {
	server, calls := hubspotServer(t, http.StatusCreated, `{}`)
	if _, err := runHubspotAction(t, server, "create_note", map[string]any{"body": "Called"}); err == nil {
		t.Fatal("create_note without a contact error = nil, want a refusal")
	}
	if len(*calls) != 0 {
		t.Fatalf("a refused input still called HubSpot %d times", len(*calls))
	}
}
