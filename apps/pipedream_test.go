package apps

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Shubham04-oss/oryxa-go/apps/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/internal/pipedream"
)

// The port answers apps' own types, and Pipedream's missing account as
// domain.ErrNotFound, which is what lets removing one that is gone succeed.
func TestThePipedreamPortSpeaksAppsTerms(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		switch {
		case request.URL.Path == "/oauth/token":
			_, _ = writer.Write([]byte(`{"access_token":"token","expires_in":3600}`))
		case request.URL.Path == "/connect/proj_1/users/oryxa.org_1.user-1/accounts":
			_, _ = writer.Write([]byte(`[{"id":"apn_1","name":"ada@acme.test","external_id":"oryxa.org_1.user-1",
				"healthy":true,"dead":false,"app":{"name_slug":"linear"},"created_at":"2026-10-13T10:00:00Z"}]`))
		case request.URL.Path == "/connect/apps" && request.URL.Query().Get("after") == "":
			_, _ = writer.Write([]byte(`{"data":[{"name_slug":"linear","name":"Linear","img_src":"https://logo"}],
				"page_info":{"count":1,"total_count":2,"end_cursor":"c1"}}`))
		case request.URL.Path == "/connect/apps":
			_, _ = writer.Write([]byte(`{"data":[{"name_slug":"jira","name":"Jira"}],"page_info":{"count":1,"total_count":2,"end_cursor":null}}`))
		default:
			writer.WriteHeader(http.StatusNotFound)
			_, _ = writer.Write([]byte(`{"error":"Not Found"}`))
		}
	}))
	defer server.Close()
	client, err := pipedream.New(pipedream.Config{
		ProjectID: "proj_1", Environment: pipedream.EnvironmentDevelopment,
		ClientID: "id", ClientSecret: "secret", BaseURL: server.URL,
	})
	if err != nil {
		t.Fatal(err)
	}
	port := pipedreamPort{client: client}
	ctx := context.Background()

	accounts, err := port.ListAccounts(ctx, "oryxa.org_1.user-1", "linear")
	if err != nil || len(accounts) != 1 {
		t.Fatalf("ListAccounts() = %+v, %v", accounts, err)
	}
	account := accounts[0]
	if account.ID != "apn_1" || account.App != "linear" || account.ExternalUserID != "oryxa.org_1.user-1" ||
		!account.Healthy || !account.CreatedAt.Equal(time.Date(2026, 10, 13, 10, 0, 0, 0, time.UTC)) {
		t.Fatalf("account = %+v", account)
	}
	apps, err := port.ListApps(ctx, "")
	if err != nil || len(apps) != 2 || apps[0].Slug != "linear" || apps[0].Logo != "https://logo" || apps[1].Slug != "jira" {
		t.Fatalf("ListApps() = %+v, %v", apps, err)
	}
	err = port.DeleteAccount(ctx, "apn_gone")
	if !errors.Is(err, domain.ErrNotFound) || !errors.Is(err, pipedream.ErrNotFound) {
		t.Fatalf("DeleteAccount(gone) = %v, want not found in both terms", err)
	}
}
