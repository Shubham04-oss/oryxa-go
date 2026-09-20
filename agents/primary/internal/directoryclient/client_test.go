package directoryclient

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/managedagents"
)

type fakeIndex struct{ handles []managedagents.Handle }

func (index fakeIndex) OrganizationAgents(context.Context, string) ([]managedagents.Handle, error) {
	return index.handles, nil
}

func TestLoadJoinsAgentsOnlyOntoVisiblePeople(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "Bearer member-token" || request.URL.Path != "/v1/organizations/org_1/directory" {
			t.Fatalf("request = %s %s", request.Header.Get("Authorization"), request.URL.Path)
		}
		_, _ = writer.Write([]byte(`{"organization":{"id":"org_1","name":"Acme"},"teams":[{"id":"team_1","name":"Engineering","path":"org.team"}],"members":[{"user_id":"user_1","email":"asha@example.com","display_name":"Asha","org_role":"member","teams":[]}]}`))
	}))
	defer server.Close()
	client, err := New(server.URL, server.Client(), fakeIndex{handles: []managedagents.Handle{
		{ID: "agt_1", OwnerUserID: "user_1", Kind: managedagents.KindPrimary, Name: "Carl"},
		{ID: "agt_hidden", OwnerUserID: "user_2", Kind: managedagents.KindSpecialist, Name: "Hidden"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	directory, err := client.Load(context.Background(), "org_1", "member-token")
	if err != nil {
		t.Fatal(err)
	}
	if len(directory.People) != 1 || len(directory.People[0].Agents) != 1 || directory.People[0].Agents[0].Name != "Carl" ||
		directory.People[0].Agents[0].OwnerEmail != "asha@example.com" || directory.People[0].Agents[0].OwnerDisplayName != "Asha" {
		t.Fatalf("directory = %+v", directory)
	}
}

func TestFindAgentsMatchesPurposeAndOwner(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write([]byte(`{"organization":{"id":"org_1","name":"Acme"},"teams":[],"members":[{"user_id":"user_1","email":"asha@example.com","display_name":"Asha","org_role":"member","teams":[]}]}`))
	}))
	defer server.Close()
	client, _ := New(server.URL, server.Client(), fakeIndex{handles: []managedagents.Handle{
		{ID: "agt_sales", OwnerUserID: "user_1", Kind: managedagents.KindSpecialist, Name: "Revenue", Description: "Reviews CRM pipeline"},
	}})
	for _, query := range []string{"pipeline", "Asha", "asha@example.com"} {
		found, err := client.FindAgents(context.Background(), "org_1", "token", query, 10)
		if err != nil || len(found) != 1 || found[0].ID != "agt_sales" || found[0].OwnerEmail != "asha@example.com" {
			t.Fatalf("query %q = %+v, %v", query, found, err)
		}
	}
	if _, err := client.FindAgents(context.Background(), "org_1", "token", " ", 10); err != ErrInvalid {
		t.Fatalf("blank query error = %v", err)
	}
}

func TestFindPeopleEscapesTheQuery(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Query().Get("q") != "Asha & Rao" || !strings.Contains(request.URL.RawQuery, "%26") {
			t.Fatalf("query = %q", request.URL.RawQuery)
		}
		_, _ = writer.Write([]byte(`{"members":[]}`))
	}))
	defer server.Close()
	client, _ := New(server.URL, server.Client(), fakeIndex{})
	people, err := client.FindPeople(context.Background(), "org_1", "token", "Asha & Rao", 3)
	if err != nil || len(people) != 0 {
		t.Fatalf("people=%+v err=%v", people, err)
	}
}
