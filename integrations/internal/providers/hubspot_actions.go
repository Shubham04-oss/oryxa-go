package providers

// HubSpot's CRM objects, at the dated generation its current API uses. Each
// action reports what a caller needs next rather than the whole record, and
// never a credential.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	integrationsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/integrations/v1"

	"github.com/Shubham04-oss/oryxa-go/integrations/internal/domain"
)

// hubspotAPI is where HubSpot's CRM objects are reached.
const hubspotAPI = "https://api.hubapi.com"

// hubspotActionsAt is hubspotActions pointed at a different host, so a test
// can drive every action against a fake HubSpot.
func hubspotActionsAt(apiURL string) []Action {
	actions := hubspotActions()
	for index := range actions {
		name := actions[index].Name
		actions[index].Run = func(ctx context.Context, client *http.Client, request ActionRequest) (ActionResult, error) {
			return hubspotAt(ctx, client, apiURL, name, request)
		}
	}
	return actions
}

// hubspotAt carries out a named action against apiURL.
func hubspotAt(ctx context.Context, client *http.Client, apiURL, name string, request ActionRequest) (ActionResult, error) {
	switch name {
	case "create_contact":
		return hubspotCreateContact(ctx, client, apiURL, request)
	case "get_contact":
		return hubspotGetContact(ctx, client, apiURL, request)
	case "update_contact":
		return hubspotUpdateContact(ctx, client, apiURL, request)
	case "search_contacts":
		return hubspotSearchContacts(ctx, client, apiURL, request)
	case "create_company":
		return hubspotCreateCompany(ctx, client, apiURL, request)
	case "create_note":
		return hubspotCreateNote(ctx, client, apiURL, request)
	default:
		return ActionResult{}, fmt.Errorf("%w: HubSpot has no action %s", domain.ErrInvalid, name)
	}
}

// hubspotGeneration is the dated API generation. HubSpot's undated v3 paths
// are the legacy generation; these are the current ones.
const hubspotGeneration = "2026-09"

// hubspotNoteToContact is HubSpot's association type for a note about a
// contact: HUBSPOT_DEFINED 202. The others are 190 for a company, 214 for a
// deal and 228 for a ticket.
const hubspotNoteToContact = 202

var (
	hubspotCreateContactSchema = json.RawMessage(`{
		"type": "object",
		"properties": {
			"email": {"type": "string", "description": "The contact's email address. HubSpot uses it as the contact's unique identifier."},
			"firstname": {"type": "string", "description": "The contact's first name."},
			"lastname": {"type": "string", "description": "The contact's last name."},
			"phone": {"type": "string", "description": "The contact's phone number."},
			"company": {"type": "string", "description": "The name of the company the contact works for."},
			"jobtitle": {"type": "string", "description": "The contact's job title."}
		},
		"required": ["email"],
		"additionalProperties": false
	}`)

	hubspotGetContactSchema = json.RawMessage(`{
		"type": "object",
		"properties": {
			"contact_id": {"type": "string", "description": "The HubSpot contact's ID."},
			"properties": {"type": "array", "items": {"type": "string"}, "description": "The property names to return. Defaults to a small set: email, firstname, lastname, company, jobtitle, phone."}
		},
		"required": ["contact_id"],
		"additionalProperties": false
	}`)

	hubspotUpdateContactSchema = json.RawMessage(`{
		"type": "object",
		"properties": {
			"contact_id": {"type": "string", "description": "The HubSpot contact's ID, or its email address when id_property is email."},
			"id_property": {"type": "string", "description": "Which property contact_id is: id (the default) or email."},
			"email": {"type": "string", "description": "A new email address for the contact."},
			"firstname": {"type": "string", "description": "The contact's first name."},
			"lastname": {"type": "string", "description": "The contact's last name."},
			"phone": {"type": "string", "description": "The contact's phone number."},
			"company": {"type": "string", "description": "The name of the company the contact works for."},
			"jobtitle": {"type": "string", "description": "The contact's job title."}
		},
		"required": ["contact_id"],
		"additionalProperties": false
	}`)

	hubspotSearchContactsSchema = json.RawMessage(`{
		"type": "object",
		"properties": {
			"query": {"type": "string", "description": "Text to look for across the contact's default searchable properties."},
			"email": {"type": "string", "description": "Match contacts whose email address is exactly this."},
			"limit": {"type": "integer", "description": "Most contacts to return, from 1 to 200. Defaults to 10."},
			"properties": {"type": "array", "items": {"type": "string"}, "description": "The property names to return for each contact."}
		},
		"additionalProperties": false
	}`)

	hubspotCreateCompanySchema = json.RawMessage(`{
		"type": "object",
		"properties": {
			"name": {"type": "string", "description": "The company's name."},
			"domain": {"type": "string", "description": "The company's website domain, such as example.com."},
			"description": {"type": "string", "description": "A short description of the company."},
			"industry": {"type": "string", "description": "The company's industry."},
			"phone": {"type": "string", "description": "The company's phone number."}
		},
		"required": ["name"],
		"additionalProperties": false
	}`)

	hubspotCreateNoteSchema = json.RawMessage(`{
		"type": "object",
		"properties": {
			"body": {"type": "string", "description": "The note's text."},
			"contact_id": {"type": "string", "description": "The contact the note is about; the note is associated with it."},
			"timestamp": {"type": "string", "description": "When the note is about, as an RFC 3339 time. Defaults to now."}
		},
		"required": ["body", "contact_id"],
		"additionalProperties": false
	}`)
)

// hubspotActions are what a caller may do with a HubSpot connection: read and
// write contacts, and open a company or a note about a contact.
func hubspotActions() []Action {
	return []Action{
		{
			Name:    "create_contact",
			Summary: "Creates a HubSpot contact and answers its ID.",
			Input:   hubspotCreateContactSchema,
			Writes:  true,
			Run: func(ctx context.Context, client *http.Client, request ActionRequest) (ActionResult, error) {
				return hubspotCreateContact(ctx, client, hubspotAPI, request)
			},
		},
		{
			Name:    "get_contact",
			Summary: "Answers one HubSpot contact's properties.",
			Input:   hubspotGetContactSchema,
			Writes:  false,
			Run: func(ctx context.Context, client *http.Client, request ActionRequest) (ActionResult, error) {
				return hubspotGetContact(ctx, client, hubspotAPI, request)
			},
		},
		{
			Name:    "update_contact",
			Summary: "Updates a HubSpot contact's properties, by ID or by email address.",
			Input:   hubspotUpdateContactSchema,
			Writes:  true,
			Run: func(ctx context.Context, client *http.Client, request ActionRequest) (ActionResult, error) {
				return hubspotUpdateContact(ctx, client, hubspotAPI, request)
			},
		},
		{
			Name:    "search_contacts",
			Summary: "Searches HubSpot contacts by free text or an exact email address.",
			Input:   hubspotSearchContactsSchema,
			Writes:  false,
			Run: func(ctx context.Context, client *http.Client, request ActionRequest) (ActionResult, error) {
				return hubspotSearchContacts(ctx, client, hubspotAPI, request)
			},
		},
		{
			Name:    "create_company",
			Summary: "Creates a HubSpot company and answers its ID.",
			Input:   hubspotCreateCompanySchema,
			Writes:  true,
			Run: func(ctx context.Context, client *http.Client, request ActionRequest) (ActionResult, error) {
				return hubspotCreateCompany(ctx, client, hubspotAPI, request)
			},
		},
		{
			Name:    "create_note",
			Summary: "Adds a note to a HubSpot contact.",
			Input:   hubspotCreateNoteSchema,
			Writes:  true,
			Run: func(ctx context.Context, client *http.Client, request ActionRequest) (ActionResult, error) {
				return hubspotCreateNote(ctx, client, hubspotAPI, request)
			},
		},
	}
}

// hubspotEndpoint is one CRM objects call of the current generation.
func hubspotEndpoint(apiURL, object, tail string) string {
	endpoint := apiURL + "/crm/objects/" + hubspotGeneration + "/" + object
	if tail == "" {
		return endpoint
	}
	return endpoint + "/" + tail
}

// hubspotDefaultProperties is what a read returns when the caller names none.
var hubspotDefaultProperties = []string{"email", "firstname", "lastname", "company", "jobtitle", "phone"}

// hubspotObject is a CRM record as HubSpot answers it.
type hubspotObject struct {
	ID         string            `json:"id"`
	Properties map[string]string `json:"properties"`
}

func hubspotObjectResult(object hubspotObject) ActionResult {
	return ActionResult{
		Resource: integrationsv1.Resource{Type: "record", ID: object.ID, Name: hubspotRecordName(object)},
		Result:   Answered(map[string]any{"id": object.ID, "properties": object.Properties}),
	}
}

// hubspotRecordName is the most useful thing to call a record by.
func hubspotRecordName(object hubspotObject) string {
	for _, property := range []string{"email", "name", "firstname", "lastname"} {
		if value := strings.TrimSpace(object.Properties[property]); value != "" {
			return value
		}
	}
	return object.ID
}

func hubspotCreateContact(ctx context.Context, client *http.Client, apiURL string, request ActionRequest) (ActionResult, error) {
	var input struct {
		Email     string `json:"email"`
		Firstname string `json:"firstname"`
		Lastname  string `json:"lastname"`
		Phone     string `json:"phone"`
		Company   string `json:"company"`
		JobTitle  string `json:"jobtitle"`
	}
	if err := DecodeInput(request.Input, &input); err != nil {
		return ActionResult{}, err
	}
	if err := Required("email", input.Email); err != nil {
		return ActionResult{}, err
	}
	properties := map[string]string{"email": input.Email}
	for name, value := range map[string]string{
		"firstname": input.Firstname, "lastname": input.Lastname, "phone": input.Phone,
		"company": input.Company, "jobtitle": input.JobTitle,
	} {
		if value != "" {
			properties[name] = value
		}
	}
	var created hubspotObject
	if err := sendJSON(ctx, client, http.MethodPost, hubspotEndpoint(apiURL, "contacts", ""), request.Credential,
		map[string]any{"properties": properties}, &created); err != nil {
		return ActionResult{}, err
	}
	return hubspotObjectResult(created), nil
}

func hubspotGetContact(ctx context.Context, client *http.Client, apiURL string, request ActionRequest) (ActionResult, error) {
	var input struct {
		ContactID  string   `json:"contact_id"`
		Properties []string `json:"properties"`
	}
	if err := DecodeInput(request.Input, &input); err != nil {
		return ActionResult{}, err
	}
	if err := Required("contact_id", input.ContactID); err != nil {
		return ActionResult{}, err
	}
	properties := input.Properties
	if len(properties) == 0 {
		properties = hubspotDefaultProperties
	}
	endpoint := hubspotEndpoint(apiURL, "contacts", url.PathEscape(input.ContactID)) + "?properties=" + url.QueryEscape(strings.Join(properties, ","))
	var found hubspotObject
	if err := sendJSON(ctx, client, http.MethodGet, endpoint, request.Credential, nil, &found); err != nil {
		return ActionResult{}, err
	}
	return hubspotObjectResult(found), nil
}

func hubspotUpdateContact(ctx context.Context, client *http.Client, apiURL string, request ActionRequest) (ActionResult, error) {
	var input struct {
		ContactID  string `json:"contact_id"`
		IDProperty string `json:"id_property"`
		Email      string `json:"email"`
		Firstname  string `json:"firstname"`
		Lastname   string `json:"lastname"`
		Phone      string `json:"phone"`
		Company    string `json:"company"`
		JobTitle   string `json:"jobtitle"`
	}
	if err := DecodeInput(request.Input, &input); err != nil {
		return ActionResult{}, err
	}
	if err := Required("contact_id", input.ContactID); err != nil {
		return ActionResult{}, err
	}
	properties := map[string]string{}
	for name, value := range map[string]string{
		"email": input.Email, "firstname": input.Firstname, "lastname": input.Lastname,
		"phone": input.Phone, "company": input.Company, "jobtitle": input.JobTitle,
	} {
		if value != "" {
			properties[name] = value
		}
	}
	if len(properties) == 0 {
		return ActionResult{}, fmt.Errorf("%w: name at least one property to change", domain.ErrInvalid)
	}
	endpoint := hubspotEndpoint(apiURL, "contacts", url.PathEscape(input.ContactID))
	if strings.EqualFold(strings.TrimSpace(input.IDProperty), "email") {
		endpoint += "?idProperty=email"
	}
	var updated hubspotObject
	if err := sendJSON(ctx, client, http.MethodPatch, endpoint, request.Credential,
		map[string]any{"properties": properties}, &updated); err != nil {
		return ActionResult{}, err
	}
	return hubspotObjectResult(updated), nil
}

func hubspotSearchContacts(ctx context.Context, client *http.Client, apiURL string, request ActionRequest) (ActionResult, error) {
	var input struct {
		Query      string   `json:"query"`
		Email      string   `json:"email"`
		Limit      int      `json:"limit"`
		Properties []string `json:"properties"`
	}
	if err := DecodeInput(request.Input, &input); err != nil {
		return ActionResult{}, err
	}
	if strings.TrimSpace(input.Query) == "" && strings.TrimSpace(input.Email) == "" {
		return ActionResult{}, fmt.Errorf("%w: name a query or an email address", domain.ErrInvalid)
	}
	limit := input.Limit
	if limit <= 0 {
		limit = 10
	}
	if limit > 200 {
		return ActionResult{}, fmt.Errorf("%w: limit is at most 200", domain.ErrInvalid)
	}
	properties := input.Properties
	if len(properties) == 0 {
		properties = hubspotDefaultProperties
	}
	body := map[string]any{"limit": limit, "properties": properties}
	if query := strings.TrimSpace(input.Query); query != "" {
		body["query"] = query
	}
	if email := strings.TrimSpace(input.Email); email != "" {
		body["filterGroups"] = []any{map[string]any{"filters": []any{map[string]any{
			"propertyName": "email", "operator": "EQ", "value": email,
		}}}}
	}
	var answer struct {
		Results []hubspotObject `json:"results"`
		Total   int             `json:"total"`
	}
	if err := sendJSON(ctx, client, http.MethodPost, hubspotEndpoint(apiURL, "contacts", "search"), request.Credential, body, &answer); err != nil {
		return ActionResult{}, err
	}
	contacts := make([]map[string]string, 0, len(answer.Results))
	for _, contact := range answer.Results {
		record := map[string]string{"id": contact.ID}
		for name, value := range contact.Properties {
			record[name] = value
		}
		contacts = append(contacts, record)
	}
	return ActionResult{
		Resource: integrationsv1.Resource{Type: "contact_search", Name: firstNonEmpty(input.Query, input.Email)},
		Result:   Answered(map[string]any{"contacts": contacts, "total": answer.Total}),
	}, nil
}

func hubspotCreateCompany(ctx context.Context, client *http.Client, apiURL string, request ActionRequest) (ActionResult, error) {
	var input struct {
		Name        string `json:"name"`
		Domain      string `json:"domain"`
		Description string `json:"description"`
		Industry    string `json:"industry"`
		Phone       string `json:"phone"`
	}
	if err := DecodeInput(request.Input, &input); err != nil {
		return ActionResult{}, err
	}
	if err := Required("name", input.Name); err != nil {
		return ActionResult{}, err
	}
	properties := map[string]string{"name": input.Name}
	for name, value := range map[string]string{
		"domain": input.Domain, "description": input.Description, "industry": input.Industry, "phone": input.Phone,
	} {
		if value != "" {
			properties[name] = value
		}
	}
	var created hubspotObject
	if err := sendJSON(ctx, client, http.MethodPost, hubspotEndpoint(apiURL, "companies", ""), request.Credential,
		map[string]any{"properties": properties}, &created); err != nil {
		return ActionResult{}, err
	}
	return hubspotObjectResult(created), nil
}

// hubspotCreateNote writes a note and associates it with a contact, which is
// what makes it show on the contact's timeline.
func hubspotCreateNote(ctx context.Context, client *http.Client, apiURL string, request ActionRequest) (ActionResult, error) {
	var input struct {
		Body      string `json:"body"`
		ContactID string `json:"contact_id"`
		Timestamp string `json:"timestamp"`
	}
	if err := DecodeInput(request.Input, &input); err != nil {
		return ActionResult{}, err
	}
	if err := Required("body", input.Body); err != nil {
		return ActionResult{}, err
	}
	if err := Required("contact_id", input.ContactID); err != nil {
		return ActionResult{}, err
	}
	timestamp := strings.TrimSpace(input.Timestamp)
	if timestamp == "" {
		timestamp = request.Now.UTC().Format("2006-01-02T15:04:05.000Z")
	}
	body := map[string]any{
		"properties": map[string]string{"hs_note_body": input.Body, "hs_timestamp": timestamp},
		"associations": []any{map[string]any{
			"to":    map[string]any{"id": input.ContactID},
			"types": []any{map[string]any{"associationCategory": "HUBSPOT_DEFINED", "associationTypeId": hubspotNoteToContact}},
		}},
	}
	var created hubspotObject
	if err := sendJSON(ctx, client, http.MethodPost, hubspotEndpoint(apiURL, "notes", ""), request.Credential, body, &created); err != nil {
		return ActionResult{}, err
	}
	return hubspotObjectResult(created), nil
}
