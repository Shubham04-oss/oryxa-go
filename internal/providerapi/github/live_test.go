package github_test

import (
	"context"
	"net/http"
	"os"
	"path"
	"testing"
	"time"

	"github.com/Shubham04-oss/oryxa-go/internal/providerapi/github"
	"github.com/Shubham04-oss/oryxa-go/internal/providerapi/github/actions"
	"github.com/Shubham04-oss/oryxa-go/internal/providerapi/github/issues"
	"github.com/Shubham04-oss/oryxa-go/internal/providerapi/github/orgs"
	"github.com/Shubham04-oss/oryxa-go/internal/providerapi/github/pulls"
	"github.com/Shubham04-oss/oryxa-go/internal/providerapi/github/repos"
	"github.com/Shubham04-oss/oryxa-go/internal/providerapi/github/users"
)

// TestLiveGitHub reads public data from api.github.com through several areas'
// clients, to check that GitHub's real responses decode where its examples
// did. It runs only with PROVIDERAPI_LIVE=1 and never writes. With GITHUB_TOKEN
// set it sends the token and also reads the authenticated user.
func TestLiveGitHub(t *testing.T) {
	if os.Getenv("PROVIDERAPI_LIVE") != "1" {
		t.Skip("set PROVIDERAPI_LIVE=1 to call api.github.com")
	}
	const owner, repo = "cli", "cli"
	token := os.Getenv("GITHUB_TOKEN")
	authorize := func(_ context.Context, request *http.Request) error {
		if token != "" {
			request.Header.Set("Authorization", "Bearer "+token)
		}
		return nil
	}
	httpClient := &http.Client{Timeout: 30 * time.Second}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	limited := func(t *testing.T, status int) {
		t.Helper()
		if status == http.StatusForbidden || status == http.StatusTooManyRequests {
			t.Skipf("GitHub answered %d; the unauthenticated rate limit is probably spent", status)
		}
	}

	t.Run("repos", func(t *testing.T) {
		client := connect(t, github.Server, httpClient, authorize, repos.NewClientWithResponses, repos.WithHTTPClient, repos.WithRequestEditorFn)
		response, err := client.GetWithResponse(ctx, owner, repo)
		if err != nil {
			t.Fatal(err)
		}
		limited(t, response.StatusCode())
		if response.JSON200 == nil || response.JSON200.FullName != "cli/cli" {
			t.Errorf("Get: status %d, repository %+v", response.StatusCode(), response.JSON200)
		}
	})

	t.Run("issues", func(t *testing.T) {
		client := connect(t, github.Server, httpClient, authorize, issues.NewClientWithResponses, issues.WithHTTPClient, issues.WithRequestEditorFn)
		state, perPage := issues.ListForRepoParamsStateAll, 30
		response, err := client.ListForRepoWithResponse(ctx, owner, repo, &issues.ListForRepoParams{State: &state, PerPage: &perPage})
		if err != nil {
			t.Fatal(err)
		}
		limited(t, response.StatusCode())
		if response.JSON200 == nil || len(*response.JSON200) == 0 {
			t.Errorf("ListForRepo: status %d, issues %v", response.StatusCode(), response.JSON200)
		}
	})

	t.Run("pulls", func(t *testing.T) {
		client := connect(t, github.Server, httpClient, authorize, pulls.NewClientWithResponses, pulls.WithHTTPClient, pulls.WithRequestEditorFn)
		state, perPage := pulls.ListParamsStateAll, 30
		response, err := client.ListWithResponse(ctx, owner, repo, &pulls.ListParams{State: &state, PerPage: &perPage})
		if err != nil {
			t.Fatal(err)
		}
		limited(t, response.StatusCode())
		if response.JSON200 == nil || len(*response.JSON200) == 0 {
			t.Fatalf("List: status %d, pull requests %v", response.StatusCode(), response.JSON200)
		}
		deletedForks := 0
		for _, pull := range *response.JSON200 {
			// GitHub sends head.repo as null for a pull request from a fork that
			// was deleted, though its description says it is never null.
			if pull.Head.Repo.ID == 0 {
				deletedForks++
			}
		}
		t.Logf("decoded %d pull requests; %d have an empty head repository where GitHub sent null", len(*response.JSON200), deletedForks)
	})

	t.Run("actions", func(t *testing.T) {
		client := connect(t, github.Server, httpClient, authorize, actions.NewClientWithResponses, actions.WithHTTPClient, actions.WithRequestEditorFn)
		list, err := client.ListRepoWorkflowsWithResponse(ctx, owner, repo, nil)
		if err != nil {
			t.Fatal(err)
		}
		limited(t, list.StatusCode())
		if list.JSON200 == nil || len(list.JSON200.Workflows) == 0 {
			t.Fatalf("ListRepoWorkflows: status %d, workflows %+v", list.StatusCode(), list.JSON200)
		}
		first := list.JSON200.Workflows[0]
		byName, err := client.GetWorkflowWithResponse(ctx, owner, repo, path.Base(first.Path))
		if err != nil {
			t.Fatal(err)
		}
		limited(t, byName.StatusCode())
		if byName.JSON200 == nil || byName.JSON200.ID != first.ID {
			t.Errorf("GetWorkflow(%s): status %d, workflow %+v, want ID %d", path.Base(first.Path), byName.StatusCode(), byName.JSON200, first.ID)
		}
	})

	t.Run("orgs", func(t *testing.T) {
		client := connect(t, github.Server, httpClient, authorize, orgs.NewClientWithResponses, orgs.WithHTTPClient, orgs.WithRequestEditorFn)
		response, err := client.GetWithResponse(ctx, owner)
		if err != nil {
			t.Fatal(err)
		}
		limited(t, response.StatusCode())
		if response.JSON200 == nil || response.JSON200.Login != "cli" {
			t.Errorf("Get: status %d, organization %+v", response.StatusCode(), response.JSON200)
		}
	})

	t.Run("users", func(t *testing.T) {
		if token == "" {
			t.Skip("set GITHUB_TOKEN to read the authenticated user")
		}
		client := connect(t, github.Server, httpClient, authorize, users.NewClientWithResponses, users.WithHTTPClient, users.WithRequestEditorFn)
		response, err := client.GetAuthenticatedWithResponse(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if response.JSON200 == nil {
			t.Fatalf("GetAuthenticated: status %d", response.StatusCode())
		}
		if _, err := response.JSON200.AsPrivateUser(); err != nil {
			t.Errorf("GetAuthenticated: %v", err)
		}
	})
}
