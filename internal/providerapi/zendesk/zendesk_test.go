package zendesk_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/Shubham04-oss/oryxa-go/internal/providerapi/zendesk"
	"github.com/Shubham04-oss/oryxa-go/internal/providerapi/zendesk/tickets"
	"github.com/Shubham04-oss/oryxa-go/internal/providerapi/zendesk/users"
)

func TestServerIsTheAccountsSubdomain(t *testing.T) {
	if got := zendesk.Server("acme"); got != "https://acme.zendesk.com" {
		t.Fatalf("Server(acme) = %q", got)
	}
}

func TestClientsCallZendeskAsDescribed(t *testing.T) {
	type sent struct {
		method, path, authorization string
		query                       url.Values
		body                        []byte
	}
	var mu sync.Mutex
	var requests []sent
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		body, _ := io.ReadAll(request.Body)
		mu.Lock()
		requests = append(requests, sent{request.Method, request.URL.Path, request.Header.Get("Authorization"), request.URL.Query(), body})
		mu.Unlock()
		writer.Header().Set("Content-Type", "application/json")
		switch request.Method + " " + request.URL.Path {
		case "GET /api/v2/tickets":
			_, _ = io.WriteString(writer, `{"tickets":[{"id":35436,"subject":"Help, my printer is on fire!","status":"open","priority":"high"}],
				"meta":{"has_more":true,"after_cursor":"xOTQ4Mg==","before_cursor":"xOTE4Nw=="},"links":{"next":"https://acme.zendesk.com/api/v2/tickets.json?page[after]=xOTQ4Mg=="}}`)
		case "POST /api/v2/tickets":
			writer.WriteHeader(http.StatusCreated)
			_, _ = io.WriteString(writer, `{"ticket":{"id":35437,"subject":"Printer","status":"new"}}`)
		case "GET /api/v2/users":
			_, _ = io.WriteString(writer, `{"users":[{"id":1,"name":"Ada","email":"ada@example.test","role":"agent"}]}`)
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()
	bearer := func(_ context.Context, request *http.Request) error {
		request.Header.Set("Authorization", "Bearer token")
		return nil
	}
	ctx := context.Background()

	ticketClient, err := tickets.NewClientWithResponses(server.URL, tickets.WithHTTPClient(server.Client()), tickets.WithRequestEditorFn(bearer))
	if err != nil {
		t.Fatal(err)
	}
	size, after := 2, "xOTE4Nw=="
	listed, err := ticketClient.ListTicketsWithResponse(ctx, &tickets.ListTicketsParams{Page: &tickets.ListTicketsParams_Page{Size: &size, After: &after}})
	if err != nil || listed.JSON200 == nil || listed.JSON200.Tickets == nil || len(*listed.JSON200.Tickets) != 1 {
		t.Fatalf("ListTickets() = %+v %v", listed, err)
	}
	ticket := (*listed.JSON200.Tickets)[0]
	if ticket.ID == nil || *ticket.ID != 35436 || ticket.Status == nil || *ticket.Status != tickets.TicketObjectStatusOpen {
		t.Fatalf("ticket = %+v", ticket)
	}
	// The next cursor is in the body, which the description does not type.
	var page struct {
		Meta struct {
			HasMore     bool   `json:"has_more"`
			AfterCursor string `json:"after_cursor"`
		} `json:"meta"`
	}
	if err := json.Unmarshal(listed.Body, &page); err != nil || !page.Meta.HasMore || page.Meta.AfterCursor != "xOTQ4Mg==" {
		t.Fatalf("page meta = %+v %v", page, err)
	}

	subject, comment := "Printer", "The fire is very colorful."
	created, err := ticketClient.CreateTicketWithResponse(ctx, nil, tickets.CreateTicketJSONRequestBody{
		Ticket: tickets.TicketObject{Subject: &subject, Comment: &tickets.TicketCommentObject{Body: &comment}},
	})
	if err != nil || created.StatusCode() != http.StatusCreated {
		t.Fatalf("CreateTicket() = %+v %v", created, err)
	}

	userClient, err := users.NewClientWithResponses(server.URL, users.WithHTTPClient(server.Client()), users.WithRequestEditorFn(bearer))
	if err != nil {
		t.Fatal(err)
	}
	roles := users.RoleList{"agent", "admin"}
	if listedUsers, err := userClient.ListUsersWithResponse(ctx, &users.ListUsersParams{RoleList: &roles}); err != nil || listedUsers.StatusCode() != http.StatusOK {
		t.Fatalf("ListUsers() = %+v %v", listedUsers, err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(requests) != 3 {
		t.Fatalf("Zendesk was sent %d requests, want 3", len(requests))
	}
	for _, request := range requests {
		if request.authorization != "Bearer token" {
			t.Errorf("%s %s Authorization = %q", request.method, request.path, request.authorization)
		}
	}
	if query := requests[0].query; query.Get("page[size]") != "2" || query.Get("page[after]") != "xOTE4Nw==" || query.Has("page") {
		t.Errorf("ListTickets query = %v, want the cursor as page[size] and page[after]", query)
	}
	if body := strings.TrimSpace(string(requests[1].body)); body != `{"ticket":{"comment":{"body":"The fire is very colorful."},"subject":"Printer"}}` {
		t.Errorf("CreateTicket body = %s", body)
	}
	if query := requests[2].query; !slices.Equal(query["role[]"], []string{"agent", "admin"}) || query.Has("role") {
		t.Errorf("ListUsers query = %v, want roles as role[]", query)
	}
}
