package intercom_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/Shubham04-oss/oryxa-go/internal/providerapi/intercom"
)

func TestClientCallsIntercomAsDescribed(t *testing.T) {
	type sent struct {
		uri, authorization, version string
	}
	var mu sync.Mutex
	var requests []sent
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		mu.Lock()
		requests = append(requests, sent{request.RequestURI, request.Header.Get("Authorization"), request.Header.Get("Intercom-Version")})
		mu.Unlock()
		writer.Header().Set("Content-Type", "application/json")
		switch request.Method + " " + request.URL.Path {
		case "GET /me":
			_, _ = io.WriteString(writer, `{"type":"admin","id":"991267459","email":"ada@example.test","name":"Ada","email_verified":true,
				"app":{"type":"app","id_code":"abc123de","name":"Acme Support","region":"US","timezone":"America/Los_Angeles","created_at":1671465577,"identity_verification":false}}`)
		case "GET /conversations/123":
			_, _ = io.WriteString(writer, `{"type":"conversation","id":"123","title":"Refund","open":true,"state":"open","created_at":1734537460,"updated_at":1734537460}`)
		case "GET /conversations/404":
			writer.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(writer, `{"type":"error.list","request_id":"b68959ea-b9d3-4b1e-a0c0-4b8bd5c0d6d5","errors":[{"code":"not_found","message":"Resource Not Found"}]}`)
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()
	bearer := func(_ context.Context, request *http.Request) error {
		request.Header.Set("Authorization", "Bearer dG9rOjE=")
		return nil
	}
	client, err := intercom.NewClientWithResponses(server.URL, intercom.WithHTTPClient(server.Client()),
		intercom.WithRequestEditorFn(intercom.Versioned), intercom.WithRequestEditorFn(bearer))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	me, err := client.IdentifyAdminWithResponse(ctx, nil)
	if err != nil || me.JSON200 == nil || me.JSON200.ID == nil || *me.JSON200.ID != "991267459" || me.JSON200.App == nil ||
		me.JSON200.App.IDCode == nil || *me.JSON200.App.IDCode != "abc123de" {
		t.Fatalf("IdentifyAdmin() = %+v %v", me, err)
	}
	// A call that names its own version keeps it.
	older, plain := intercom.IntercomVersion("2.15"), "plaintext"
	conversation, err := client.RetrieveConversationWithResponse(ctx, 123, &intercom.RetrieveConversationParams{DisplayAs: &plain, IntercomVersion: &older})
	if err != nil || conversation.JSON200 == nil || conversation.JSON200.Title == nil || *conversation.JSON200.Title != "Refund" ||
		conversation.JSON200.State == nil || *conversation.JSON200.State != intercom.ConversationStateOpen {
		t.Fatalf("RetrieveConversation() = %+v %v", conversation, err)
	}
	missing, err := client.RetrieveConversationWithResponse(ctx, 404, nil)
	if err != nil || missing.StatusCode() != http.StatusNotFound || missing.JSON404 == nil || len(missing.JSON404.Errors) != 1 ||
		missing.JSON404.Errors[0].Code != "not_found" {
		t.Fatalf("RetrieveConversation() of a missing conversation = %+v %v", missing, err)
	}

	mu.Lock()
	defer mu.Unlock()
	want := []sent{
		{"/me", "Bearer dG9rOjE=", intercom.APIVersion},
		{"/conversations/123?display_as=plaintext", "Bearer dG9rOjE=", "2.15"},
		{"/conversations/404", "Bearer dG9rOjE=", intercom.APIVersion},
	}
	if len(requests) != len(want) {
		t.Fatalf("Intercom was sent %d requests, want %d", len(requests), len(want))
	}
	for index, request := range requests {
		if request != want[index] {
			t.Errorf("request %d = %+v, want %+v", index, request, want[index])
		}
	}
}
