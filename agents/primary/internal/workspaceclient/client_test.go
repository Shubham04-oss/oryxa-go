package workspaceclient

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestLoadCarriesMemberBearerAndParsesContext(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/v1/organizations/org_1/workspace/context" || request.Header.Get("Authorization") != "Bearer member-token" {
			writer.WriteHeader(http.StatusForbidden)
			return
		}
		_, _ = writer.Write([]byte(`{"ownership":[{"owner":"sam@example.com","system":"billing"}],"orgChart":{"authority":["manages"]}}`))
	}))
	defer server.Close()
	client, err := New(server.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	context, err := client.Load(context.Background(), "org_1", "member-token")
	if err != nil || len(context.Ownership) != 1 || context.Ownership[0].System != "billing" || len(context.OrgChart.Authority) != 1 {
		t.Fatalf("context=%+v err=%v", context, err)
	}
}
