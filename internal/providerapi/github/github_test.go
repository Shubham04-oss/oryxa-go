package github_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"sync"
	"testing"

	"github.com/Shubham04-oss/oryxa-go/internal/providerapi/github"
	"github.com/Shubham04-oss/oryxa-go/internal/providerapi/github/actions"
	"github.com/Shubham04-oss/oryxa-go/internal/providerapi/github/activity"
	"github.com/Shubham04-oss/oryxa-go/internal/providerapi/github/apps"
	"github.com/Shubham04-oss/oryxa-go/internal/providerapi/github/checks"
	"github.com/Shubham04-oss/oryxa-go/internal/providerapi/github/gists"
	"github.com/Shubham04-oss/oryxa-go/internal/providerapi/github/git"
	"github.com/Shubham04-oss/oryxa-go/internal/providerapi/github/issues"
	"github.com/Shubham04-oss/oryxa-go/internal/providerapi/github/orgs"
	"github.com/Shubham04-oss/oryxa-go/internal/providerapi/github/pulls"
	"github.com/Shubham04-oss/oryxa-go/internal/providerapi/github/repos"
	"github.com/Shubham04-oss/oryxa-go/internal/providerapi/github/search"
	"github.com/Shubham04-oss/oryxa-go/internal/providerapi/github/users"
)

const (
	token        = "gho_example"
	owner, repo  = "octocat", "Hello-World"
	repositoryAt = "/repos/octocat/Hello-World"
)

// bearer sends the token a connection issued.
func bearer(_ context.Context, request *http.Request) error {
	request.Header.Set("Authorization", "Bearer "+token)
	return nil
}

// connect builds an area's client the way callers do: over httpClient, asking
// for the API version the clients were generated from, with authorize's
// credentials. Every area's package has the same constructor and options, so
// they are passed in.
func connect[Client, Option any, Doer interface {
	Do(*http.Request) (*http.Response, error)
}, Editor ~func(context.Context, *http.Request) error](
	t *testing.T, server string, httpClient *http.Client, authorize func(context.Context, *http.Request) error,
	build func(string, ...Option) (Client, error), withHTTPClient func(Doer) Option, withEditor func(Editor) Option,
) Client {
	t.Helper()
	doer, ok := any(httpClient).(Doer)
	if !ok {
		t.Fatalf("%T cannot send requests for this client", httpClient)
	}
	client, err := build(server, withHTTPClient(doer), withEditor(github.Versioned), withEditor(authorize))
	if err != nil {
		t.Fatal(err)
	}
	return client
}

// fakeGitHub answers with the examples GitHub publishes in the description each
// client was generated from, and records what it was sent.
type fakeGitHub struct {
	*httptest.Server
	mux *http.ServeMux
	// rewrite, when set, changes an example before it is served.
	rewrite func(example any)

	mu       sync.Mutex
	requests []sent
}

type sent struct {
	method, path string
	query        url.Values
	header       http.Header
	body         []byte
}

func newFakeGitHub(t *testing.T) *fakeGitHub {
	t.Helper()
	fake := &fakeGitHub{mux: http.NewServeMux()}
	fake.Server = httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		body, err := io.ReadAll(request.Body)
		if err != nil {
			t.Error(err)
		}
		fake.mu.Lock()
		fake.requests = append(fake.requests, sent{request.Method, request.URL.Path, request.URL.Query(), request.Header.Clone(), body})
		fake.mu.Unlock()
		fake.mux.ServeHTTP(writer, request)
	}))
	t.Cleanup(fake.Close)
	return fake
}

// answer serves pattern with the example area's description gives for
// operationID's status response, with header added.
func (fake *fakeGitHub) answer(t *testing.T, pattern, area, operationID string, status int, header http.Header) {
	t.Helper()
	example := exampleFor(t, area, operationID, status)
	fake.mux.HandleFunc(pattern, func(writer http.ResponseWriter, _ *http.Request) {
		maps.Copy(writer.Header(), header)
		if example == nil {
			writer.WriteHeader(status)
			return
		}
		body := decodeJSON(t, example)
		if fake.rewrite != nil {
			fake.rewrite(body)
		}
		writer.Header().Set("Content-Type", "application/json; charset=utf-8")
		writer.WriteHeader(status)
		if err := json.NewEncoder(writer).Encode(body); err != nil {
			t.Error(err)
		}
	})
}

// sentTo returns the one request GitHub was sent for method and path.
func (fake *fakeGitHub) sentTo(t *testing.T, method, path string) sent {
	t.Helper()
	fake.mu.Lock()
	defer fake.mu.Unlock()
	var matched []sent
	for _, request := range fake.requests {
		if request.method == method && request.path == path {
			matched = append(matched, request)
		}
	}
	if len(matched) != 1 {
		t.Fatalf("GitHub was sent %d requests for %s %s, want 1", len(matched), method, path)
	}
	return matched[0]
}

// exampleFor returns, as JSON, the first example by name that an area's
// description gives for an operation's response, or nil when the response has
// no body.
func exampleFor(t *testing.T, area, operationID string, status int) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(area, "openapi.json"))
	if err != nil {
		t.Fatal(err)
	}
	var description struct {
		Paths      map[string]map[string]json.RawMessage `json:"paths"`
		Components struct {
			Examples map[string]struct {
				Value json.RawMessage `json:"value"`
			} `json:"examples"`
		} `json:"components"`
	}
	if err := json.Unmarshal(raw, &description); err != nil {
		t.Fatal(err)
	}
	for _, item := range description.Paths {
		for _, rawOperation := range item {
			var operation struct {
				OperationID string `json:"operationId"`
				Responses   map[string]struct {
					Ref     string `json:"$ref"`
					Content map[string]struct {
						Examples map[string]struct {
							Ref   string          `json:"$ref"`
							Value json.RawMessage `json:"value"`
						} `json:"examples"`
					} `json:"content"`
				} `json:"responses"`
			}
			// Path-level fields such as parameters do not decode as an operation.
			if json.Unmarshal(rawOperation, &operation) != nil || operation.OperationID != operationID {
				continue
			}
			response, ok := operation.Responses[strconv.Itoa(status)]
			if !ok || response.Ref != "" {
				t.Fatalf("%s %s has no %d response written in place", area, operationID, status)
			}
			examples := response.Content["application/json"].Examples
			if len(examples) == 0 {
				return nil
			}
			chosen := examples[slices.Sorted(maps.Keys(examples))[0]]
			if chosen.Ref != "" {
				return description.Components.Examples[filepath.Base(chosen.Ref)].Value
			}
			return chosen.Value
		}
	}
	t.Fatalf("%s has no operation %s", area, operationID)
	return nil
}

func decodeJSON(t *testing.T, raw []byte) any {
	t.Helper()
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		t.Error(err)
	}
	return value
}

// described is a generated response as a failure message shows it: the status
// and body GitHub answered with.
func described(response any) string {
	value := reflect.Indirect(reflect.ValueOf(response))
	if !value.IsValid() {
		return "no response"
	}
	answered, _ := value.FieldByName("HTTPResponse").Interface().(*http.Response)
	if answered == nil {
		return "no response"
	}
	return fmt.Sprintf("%d %s", answered.StatusCode, value.FieldByName("Body").Bytes())
}

// sameJSON reports whether two JSON documents hold the same values.
func sameJSON(t *testing.T, got []byte, want string) bool {
	t.Helper()
	gotValue, err := json.Marshal(decodeJSON(t, got))
	if err != nil {
		t.Fatal(err)
	}
	wantValue, err := json.Marshal(decodeJSON(t, []byte(want)))
	if err != nil {
		t.Fatal(err)
	}
	return bytes.Equal(gotValue, wantValue)
}

// TestClientsCallGitHubAsDescribed calls at least one operation in every area
// against GitHub's published examples, and checks what each client sent and
// what it decoded.
func TestClientsCallGitHubAsDescribed(t *testing.T) {
	fake := newFakeGitHub(t)
	ctx := context.Background()

	t.Run("users", func(t *testing.T) {
		fake.answer(t, "GET /user", "users", "get-authenticated", http.StatusOK, nil)
		client := connect(t, fake.URL, fake.Client(), bearer, users.NewClientWithResponses, users.WithHTTPClient, users.WithRequestEditorFn)
		response, err := client.GetAuthenticatedWithResponse(ctx)
		if err != nil || response.JSON200 == nil {
			t.Fatalf("GetAuthenticated = %s, %v", described(response), err)
		}
		if user, err := response.JSON200.AsPrivateUser(); err != nil || user.Login != "octocat" {
			t.Errorf("authenticated user = %+v, %v", user, err)
		}
	})

	t.Run("repos", func(t *testing.T) {
		fake.answer(t, "GET "+repositoryAt, "repos", "get", http.StatusOK, nil)
		fake.mux.HandleFunc("GET /repos/octocat/missing", func(writer http.ResponseWriter, _ *http.Request) {
			writer.Header().Set("Content-Type", "application/json; charset=utf-8")
			writer.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(writer, `{"message":"Not Found","documentation_url":"https://docs.github.com/rest/repos/repos#get-a-repository","status":"404"}`)
		})
		fake.answer(t, "GET "+repositoryAt+"/rulesets/rule-suites", "repos", "get-repo-rule-suites", http.StatusOK, nil)
		client := connect(t, fake.URL, fake.Client(), bearer, repos.NewClientWithResponses, repos.WithHTTPClient, repos.WithRequestEditorFn)

		repository, err := client.GetWithResponse(ctx, owner, repo)
		if err != nil || repository.JSON200 == nil || repository.JSON200.FullName != "octocat/Hello-World" || repository.JSON200.Owner.Login != "octocat" {
			t.Errorf("Get = %s, %v", described(repository), err)
		}
		missing, err := client.GetWithResponse(ctx, owner, "missing")
		if err != nil || missing.StatusCode() != http.StatusNotFound || missing.JSON404 == nil || missing.JSON404.Message == nil || *missing.JSON404.Message != "Not Found" {
			t.Errorf("Get of a missing repository = %s, %v", described(missing), err)
		}

		result := repos.GetRepoRuleSuitesParamsRuleSuiteResultBypass
		suites, err := client.GetRepoRuleSuitesWithResponse(ctx, owner, repo, &repos.GetRepoRuleSuitesParams{RuleSuiteResult: &result})
		if err != nil || suites.JSON200 == nil || len(*suites.JSON200) != 2 {
			t.Errorf("GetRepoRuleSuites = %s, %v", described(suites), err)
		}
		if query := fake.sentTo(t, http.MethodGet, repositoryAt+"/rulesets/rule-suites").query; query.Get("rule_suite_result") != "bypass" {
			t.Errorf("GetRepoRuleSuites query = %v, want rule_suite_result=bypass", query)
		}
	})

	t.Run("issues", func(t *testing.T) {
		next := `<https://api.github.com/repositories/1296269/issues?page=2>; rel="next"`
		location := "https://api.github.com/repos/octocat/Hello-World/issues/1347"
		fake.answer(t, "GET "+repositoryAt+"/issues", "issues", "list-for-repo", http.StatusOK, http.Header{"Link": {next}})
		fake.answer(t, "POST "+repositoryAt+"/issues", "issues", "create", http.StatusCreated, http.Header{"Location": {location}})
		client := connect(t, fake.URL, fake.Client(), bearer, issues.NewClientWithResponses, issues.WithHTTPClient, issues.WithRequestEditorFn)

		state, perPage := issues.ListForRepoParamsStateAll, 5
		list, err := client.ListForRepoWithResponse(ctx, owner, repo, &issues.ListForRepoParams{State: &state, PerPage: &perPage})
		if err != nil || list.JSON200 == nil || len(*list.JSON200) != 1 || (*list.JSON200)[0].Number != 1347 {
			t.Fatalf("ListForRepo = %s, %v", described(list), err)
		}
		if list.Headers200 == nil || list.Headers200.Link == nil || *list.Headers200.Link != next {
			t.Errorf("ListForRepo Link = %+v, want %s", list.Headers200, next)
		}
		if query := fake.sentTo(t, http.MethodGet, repositoryAt+"/issues").query; query.Get("state") != "all" || query.Get("per_page") != "5" || len(query) != 2 {
			t.Errorf("ListForRepo query = %v, want state=all and per_page=5", query)
		}

		var title issues.CreateJSONBody_Title
		if err := title.FromCreateJSONBodyTitle0("Found a bug"); err != nil {
			t.Fatal(err)
		}
		body := "I'm having a problem with this."
		created, err := client.CreateWithResponse(ctx, owner, repo, issues.CreateJSONRequestBody{Title: title, Body: &body})
		if err != nil || created.JSON201 == nil || created.JSON201.Number != 1347 {
			t.Fatalf("Create = %s, %v", described(created), err)
		}
		if created.Headers201 == nil || created.Headers201.Location == nil || *created.Headers201.Location != location {
			t.Errorf("Create Location = %+v, want %s", created.Headers201, location)
		}
		request := fake.sentTo(t, http.MethodPost, repositoryAt+"/issues")
		if !sameJSON(t, request.body, `{"title": "Found a bug", "body": "I'm having a problem with this."}`) || request.header.Get("Content-Type") != "application/json" {
			t.Errorf("Create sent %s %s", request.header.Get("Content-Type"), request.body)
		}
	})

	t.Run("pulls", func(t *testing.T) {
		fake.answer(t, "GET "+repositoryAt+"/pulls", "pulls", "list", http.StatusOK, nil)
		client := connect(t, fake.URL, fake.Client(), bearer, pulls.NewClientWithResponses, pulls.WithHTTPClient, pulls.WithRequestEditorFn)
		state := pulls.ListParamsStateOpen
		list, err := client.ListWithResponse(ctx, owner, repo, &pulls.ListParams{State: &state})
		if err != nil || list.JSON200 == nil || len(*list.JSON200) != 1 || (*list.JSON200)[0].Number != 1347 || (*list.JSON200)[0].Head.Ref != "new-topic" {
			t.Errorf("List = %s, %v", described(list), err)
		}
	})

	t.Run("actions", func(t *testing.T) {
		fake.answer(t, "GET "+repositoryAt+"/actions/workflows/161335", "actions", "get-workflow", http.StatusOK, nil)
		fake.answer(t, "GET "+repositoryAt+"/actions/workflows/blank.yaml", "actions", "get-workflow", http.StatusOK, nil)
		client := connect(t, fake.URL, fake.Client(), bearer, actions.NewClientWithResponses, actions.WithHTTPClient, actions.WithRequestEditorFn)
		// A workflow is named by its ID or by its file name. The description's
		// union of the two is a string in the client.
		for _, workflowID := range []actions.WorkflowID{"161335", "blank.yaml"} {
			workflow, err := client.GetWorkflowWithResponse(ctx, owner, repo, workflowID)
			if err != nil || workflow.JSON200 == nil || workflow.JSON200.ID != 161335 || workflow.JSON200.State != actions.WorkflowStateActive {
				t.Errorf("GetWorkflow(%s) = %s, %v", workflowID, described(workflow), err)
			}
		}
	})

	t.Run("checks", func(t *testing.T) {
		fake.answer(t, "POST "+repositoryAt+"/check-runs", "checks", "create", http.StatusCreated, nil)
		client := connect(t, fake.URL, fake.Client(), bearer, checks.NewClientWithResponses, checks.WithHTTPClient, checks.WithRequestEditorFn)
		status := checks.CreateJSONBodyStatusInProgress
		run, err := client.CreateWithResponse(ctx, owner, repo, checks.CreateJSONRequestBody{
			Name: "mighty_readme", HeadSha: "ce587453ced02b1526dfb4cb910479d431683101", Status: &status,
		})
		if err != nil || run.JSON201 == nil || run.JSON201.Name != "mighty_readme" {
			t.Fatalf("Create = %s, %v", described(run), err)
		}
		if body := fake.sentTo(t, http.MethodPost, repositoryAt+"/check-runs").body; !sameJSON(t, body,
			`{"name": "mighty_readme", "head_sha": "ce587453ced02b1526dfb4cb910479d431683101", "status": "in_progress"}`) {
			t.Errorf("Create sent %s", body)
		}
	})

	t.Run("orgs", func(t *testing.T) {
		fake.answer(t, "GET /orgs/github", "orgs", "get", http.StatusOK, nil)
		client := connect(t, fake.URL, fake.Client(), bearer, orgs.NewClientWithResponses, orgs.WithHTTPClient, orgs.WithRequestEditorFn)
		organization, err := client.GetWithResponse(ctx, "github")
		if err != nil || organization.JSON200 == nil || organization.JSON200.Login != "github" || organization.JSON200.PublicRepos != 2 {
			t.Errorf("Get = %s, %v", described(organization), err)
		}
	})

	t.Run("activity", func(t *testing.T) {
		fake.answer(t, "PUT /user/starred/octocat/Hello-World", "activity", "star-repo-for-authenticated-user", http.StatusNoContent, nil)
		client := connect(t, fake.URL, fake.Client(), bearer, activity.NewClientWithResponses, activity.WithHTTPClient, activity.WithRequestEditorFn)
		starred, err := client.StarRepoForAuthenticatedUserWithResponse(ctx, owner, repo)
		if err != nil || starred.StatusCode() != http.StatusNoContent {
			t.Errorf("StarRepoForAuthenticatedUser = %s, %v", described(starred), err)
		}
	})

	t.Run("apps", func(t *testing.T) {
		fake.answer(t, "GET /installation/repositories", "apps", "list-repos-accessible-to-installation", http.StatusOK, nil)
		client := connect(t, fake.URL, fake.Client(), bearer, apps.NewClientWithResponses, apps.WithHTTPClient, apps.WithRequestEditorFn)
		perPage := 100
		list, err := client.ListReposAccessibleToInstallationWithResponse(ctx, &apps.ListReposAccessibleToInstallationParams{PerPage: &perPage})
		if err != nil || list.JSON200 == nil || list.JSON200.TotalCount != 1 || len(list.JSON200.Repositories) != 1 {
			t.Errorf("ListReposAccessibleToInstallation = %s, %v", described(list), err)
		}
	})

	t.Run("gists", func(t *testing.T) {
		fake.answer(t, "POST /gists", "gists", "create", http.StatusCreated, nil)
		client := connect(t, fake.URL, fake.Client(), bearer, gists.NewClientWithResponses, gists.WithHTTPClient, gists.WithRequestEditorFn)
		var public gists.CreateJSONBody_Public
		if err := public.FromCreateJSONBodyPublic0(false); err != nil {
			t.Fatal(err)
		}
		description := "Example of a gist"
		gist, err := client.CreateWithResponse(ctx, gists.CreateJSONRequestBody{
			Description: &description, Public: &public, Files: map[string]gists.CreateJSONBody_Files{"README.md": {Content: "Hello World"}},
		})
		if err != nil || gist.JSON201 == nil || gist.JSON201.ID == nil || *gist.JSON201.ID != "2decf6c462d9b4418f2" {
			t.Fatalf("Create = %s, %v", described(gist), err)
		}
		if body := fake.sentTo(t, http.MethodPost, "/gists").body; !sameJSON(t, body,
			`{"description": "Example of a gist", "public": false, "files": {"README.md": {"content": "Hello World"}}}`) {
			t.Errorf("Create sent %s", body)
		}
	})

	t.Run("git", func(t *testing.T) {
		fake.answer(t, "POST "+repositoryAt+"/git/refs", "git", "create-ref", http.StatusCreated, nil)
		client := connect(t, fake.URL, fake.Client(), bearer, git.NewClientWithResponses, git.WithHTTPClient, git.WithRequestEditorFn)
		ref, err := client.CreateRefWithResponse(ctx, owner, repo, git.CreateRefJSONRequestBody{Ref: "refs/heads/featureA", Sha: "aa218f56b14c9653891f9e74264a383fa43fefbd"})
		if err != nil || ref.JSON201 == nil || ref.JSON201.Ref != "refs/heads/featureA" {
			t.Fatalf("CreateRef = %s, %v", described(ref), err)
		}
		if body := fake.sentTo(t, http.MethodPost, repositoryAt+"/git/refs").body; !sameJSON(t, body,
			`{"ref": "refs/heads/featureA", "sha": "aa218f56b14c9653891f9e74264a383fa43fefbd"}`) {
			t.Errorf("CreateRef sent %s", body)
		}
	})

	t.Run("search", func(t *testing.T) {
		fake.answer(t, "GET /search/issues", "search", "issues-and-pull-requests", http.StatusOK, nil)
		client := connect(t, fake.URL, fake.Client(), bearer, search.NewClientWithResponses, search.WithHTTPClient, search.WithRequestEditorFn)
		q := "windows label:bug language:python state:open"
		found, err := client.IssuesAndPullRequestsWithResponse(ctx, &search.IssuesAndPullRequestsParams{Q: q})
		if err != nil || found.JSON200 == nil || found.JSON200.TotalCount != 280 {
			t.Errorf("IssuesAndPullRequests = %s, %v", described(found), err)
		}
		if query := fake.sentTo(t, http.MethodGet, "/search/issues").query; query.Get("q") != q {
			t.Errorf("IssuesAndPullRequests query = %v, want q=%s", query, q)
		}
	})

	fake.mu.Lock()
	defer fake.mu.Unlock()
	for _, request := range fake.requests {
		if request.header.Get("Authorization") != "Bearer "+token || request.header.Get("X-GitHub-Api-Version") != github.APIVersion ||
			request.header.Get("Accept") != "application/vnd.github+json" {
			t.Errorf("%s %s was sent with headers %v", request.method, request.path, request.header)
		}
	}
}

// TestDecodedResponsesAreNotValidated pins what the package documentation
// promises callers: drift in which fields GitHub sends, or in the values of an
// enum, decodes without error, and drift in a value's type does not.
func TestDecodedResponsesAreNotValidated(t *testing.T) {
	for name, test := range map[string]struct {
		drift func(repository map[string]any)
		check func(repository *repos.FullRepository) bool
	}{
		"a field the description does not know": {
			func(repository map[string]any) { repository["copilot_settings"] = map[string]any{"enabled": true} },
			func(repository *repos.FullRepository) bool { return repository.FullName == "octocat/Hello-World" },
		},
		"a required field left out": {
			func(repository map[string]any) { delete(repository, "node_id") },
			func(repository *repos.FullRepository) bool { return repository.NodeID == "" },
		},
		"null where the description promises a value": {
			func(repository map[string]any) { repository["full_name"] = nil },
			func(repository *repos.FullRepository) bool { return repository.FullName == "" },
		},
		"an enum value the description does not list": {
			func(repository map[string]any) { repository["squash_merge_commit_title"] = "PR_BODY" },
			func(repository *repos.FullRepository) bool {
				return repository.SquashMergeCommitTitle != nil && *repository.SquashMergeCommitTitle == "PR_BODY"
			},
		},
	} {
		t.Run(name, func(t *testing.T) {
			fake := newFakeGitHub(t)
			fake.rewrite = func(example any) { test.drift(example.(map[string]any)) }
			fake.answer(t, "GET "+repositoryAt, "repos", "get", http.StatusOK, nil)
			client := connect(t, fake.URL, fake.Client(), bearer, repos.NewClientWithResponses, repos.WithHTTPClient, repos.WithRequestEditorFn)
			response, err := client.GetWithResponse(context.Background(), owner, repo)
			if err != nil || response.JSON200 == nil {
				t.Fatalf("Get = %s, %v, want it decoded", described(response), err)
			}
			if !test.check(response.JSON200) {
				t.Errorf("decoded %+v", response.JSON200)
			}
		})
	}

	t.Run("a value of another type", func(t *testing.T) {
		fake := newFakeGitHub(t)
		fake.rewrite = func(example any) { example.(map[string]any)["id"] = "1296269" }
		fake.answer(t, "GET "+repositoryAt, "repos", "get", http.StatusOK, nil)
		client := connect(t, fake.URL, fake.Client(), bearer, repos.NewClientWithResponses, repos.WithHTTPClient, repos.WithRequestEditorFn)
		if response, err := client.GetWithResponse(context.Background(), owner, repo); err == nil {
			t.Fatalf("Get = %+v, want a decoding error", response.JSON200)
		}
	})
}
