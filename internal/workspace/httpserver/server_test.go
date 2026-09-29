package httpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/Shubham04-oss/oryxa-go/internal/workspace/runtime"
	"github.com/Shubham04-oss/oryxa-go/internal/workspace/spec"
	"github.com/Shubham04-oss/oryxa-go/internal/workspace/store"
)

type memoryRepository struct {
	mu       sync.Mutex
	versions []store.Version
	mappings []store.ExternalMapping
}

func (repo *memoryRepository) WithApplyLock(_ context.Context, _ string, operation func() error) error {
	repo.mu.Lock()
	defer repo.mu.Unlock()
	return operation()
}

func (repo *memoryRepository) Latest(context.Context, string) (store.Version, error) {
	if len(repo.versions) == 0 {
		return store.Version{}, store.ErrVersionNotFound
	}
	return repo.versions[len(repo.versions)-1], nil
}

func (repo *memoryRepository) Mappings(context.Context, string) ([]store.ExternalMapping, error) {
	return append([]store.ExternalMapping{}, repo.mappings...), nil
}

func (repo *memoryRepository) Versions(context.Context, string) ([]store.Version, error) {
	return append([]store.Version(nil), repo.versions...), nil
}

func (repo *memoryRepository) AtVersion(_ context.Context, _ string, number int64) (store.Version, error) {
	if number <= 0 || number > int64(len(repo.versions)) {
		return store.Version{}, store.ErrVersionNotFound
	}
	return repo.versions[number-1], nil
}

func (repo *memoryRepository) Save(_ context.Context, _, actor string, document []byte) (store.Version, error) {
	parsed, err := spec.Parse(document)
	if err != nil {
		return store.Version{}, err
	}
	repo.mappings = nil
	if parsed.Directory != nil {
		for _, identity := range parsed.Directory.Identities {
			resourceID := ""
			if identity.Member == "owner@acme.com" {
				resourceID = "u1"
			}
			repo.mappings = append(repo.mappings, store.ExternalMapping{
				Source: identity.Source, Kind: "user", ExternalID: identity.ExternalID,
				Reference: identity.Member, ResourceID: resourceID,
			})
		}
	}
	if len(repo.versions) > 0 && bytes.Equal(repo.versions[len(repo.versions)-1].Document, document) {
		return repo.versions[len(repo.versions)-1], nil
	}
	version := store.Version{Number: int64(len(repo.versions) + 1), AppliedBy: actor, AppliedAt: time.Now(), Document: document}
	repo.versions = append(repo.versions, version)
	return version, nil
}

func TestWorkspaceDirectoryMappingPlanApplyAndPrune(t *testing.T) {
	api := fakeOrganizationAPI(false)
	defer api.Close()
	repo := &memoryRepository{}
	server, err := New(repo, api.URL)
	if err != nil {
		t.Fatal(err)
	}
	workspace := httptest.NewServer(server)
	defer workspace.Close()
	path := "/v1/organizations/org1/workspace"
	file := "apiVersion: oryxa.in/v1\nkind: Workspace\nmetadata: {organization: acme}\ndirectory:\n  identities: [{source: 'google_workspace:acme.com', externalId: '100', member: owner@acme.com}]\n"
	response := workspaceRequest(t, workspace, http.MethodPost, path+":plan", "owner-token", map[string]string{"document": file})
	var plan runtime.Plan
	if err := json.NewDecoder(response.Body).Decode(&plan); err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusOK || len(plan.Changes) != 1 || plan.Changes[0].Kind != "external_user" {
		t.Fatalf("mapping plan = %d %+v", response.StatusCode, plan)
	}
	response = workspaceRequest(t, workspace, http.MethodPut, path, "owner-token", map[string]any{"document": file, "approved_plan": plan})
	response.Body.Close()
	if response.StatusCode != http.StatusOK || len(repo.mappings) != 1 || repo.mappings[0].ResourceID != "u1" {
		t.Fatalf("mapping apply = %d %+v", response.StatusCode, repo.mappings)
	}
	response = workspaceRequest(t, workspace, http.MethodGet, path+"/mappings", "owner-token", nil)
	var listed struct {
		Mappings []store.ExternalMapping `json:"mappings"`
	}
	if err := json.NewDecoder(response.Body).Decode(&listed); err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusOK || len(listed.Mappings) != 1 || listed.Mappings[0].ExternalID != "100" {
		t.Fatalf("mapping read = %d %+v", response.StatusCode, listed)
	}
	response = workspaceRequest(t, workspace, http.MethodGet, path+"/mappings", "invalid", nil)
	response.Body.Close()
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthorized mapping read = %d", response.StatusCode)
	}
	prune := "apiVersion: oryxa.in/v1\nkind: Workspace\nmetadata: {organization: acme}\ndirectory: {prune: true}\n"
	response = workspaceRequest(t, workspace, http.MethodPost, path+":plan", "owner-token", map[string]string{"document": prune})
	if err := json.NewDecoder(response.Body).Decode(&plan); err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusOK || len(plan.Changes) != 1 || plan.Changes[0].Op != "delete" {
		t.Fatalf("mapping prune plan = %d %+v", response.StatusCode, plan)
	}
	response = workspaceRequest(t, workspace, http.MethodPut, path, "owner-token", map[string]any{"document": prune, "approved_plan": plan})
	response.Body.Close()
	if response.StatusCode != http.StatusOK || len(repo.mappings) != 0 {
		t.Fatalf("mapping prune apply = %d %+v", response.StatusCode, repo.mappings)
	}
}

func fakeOrganizationAPI(extraMember bool) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		if request.Header.Get("Authorization") != "Bearer owner-token" {
			writer.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch request.URL.Path {
		case "/v1/auth/me":
			_, _ = io.WriteString(writer, `{"user":{"id":"u1","email":"owner@acme.com"}}`)
		case "/v1/organizations":
			_, _ = io.WriteString(writer, `{"organizations":[{"id":"org1","name":"Acme","slug":"acme","domains":["acme.com"],"timezone":"UTC"}]}`)
		case "/v1/organizations/org1":
			_, _ = io.WriteString(writer, `{"organization":{"id":"org1","name":"Acme","slug":"acme","domains":["acme.com"],"timezone":"UTC"},"membership":{"organization_id":"org1","user_id":"u1","role":"owner"}}`)
		case "/v1/organizations/org1/members":
			if extraMember {
				_, _ = io.WriteString(writer, `{"members":[{"user_id":"u1","email":"owner@acme.com","role":"owner"},{"user_id":"u2","email":"other@acme.com","role":"member"}]}`)
			} else {
				_, _ = io.WriteString(writer, `{"members":[{"user_id":"u1","email":"owner@acme.com","role":"owner"}]}`)
			}
		case "/v1/organizations/org1/directory":
			if extraMember {
				_, _ = io.WriteString(writer, `{"members":[{"email":"owner@acme.com"},{"email":"other@acme.com"}]}`)
			} else {
				_, _ = io.WriteString(writer, `{"members":[{"email":"owner@acme.com"}]}`)
			}
		case "/v1/organizations/org1/invitations":
			_, _ = io.WriteString(writer, `{"invitations":[]}`)
		case "/v1/organizations/org1/teams":
			_, _ = io.WriteString(writer, `{"teams":[]}`)
		case "/v1/organizations/org1/organization-agent":
			writer.WriteHeader(http.StatusNotFound)
		default:
			writer.WriteHeader(http.StatusNotFound)
		}
	}))
}

func workspaceRequest(t *testing.T, server *httptest.Server, method, path, token string, body any) *http.Response {
	t.Helper()
	var input io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		input = bytes.NewReader(data)
	}
	request, err := http.NewRequest(method, server.URL+path, input)
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	response, err := server.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	return response
}

func TestWorkspaceApplyIsVersionedAndIdempotent(t *testing.T) {
	api := fakeOrganizationAPI(false)
	defer api.Close()
	repo := &memoryRepository{}
	server, err := New(repo, api.URL)
	if err != nil {
		t.Fatal(err)
	}
	workspace := httptest.NewServer(server)
	defer workspace.Close()
	document := "apiVersion: oryxa.in/v1\nkind: Workspace\nmetadata: {organization: acme, domains: [acme.com]}\npreset: team\npeople:\n  invite: quiet\n  members: [{email: owner@acme.com, role: owner}]\n"
	path := "/v1/organizations/org1/workspace"
	for _, token := range []string{"", "invalid"} {
		response := workspaceRequest(t, workspace, http.MethodPost, path+":plan", token, map[string]string{"document": document})
		if response.StatusCode != http.StatusUnauthorized {
			t.Errorf("token %q: got %d", token, response.StatusCode)
		}
		response.Body.Close()
	}
	response := workspaceRequest(t, workspace, http.MethodPost, path+":plan", "owner-token", map[string]string{"document": document})
	if response.StatusCode != http.StatusOK {
		t.Fatalf("plan status %d", response.StatusCode)
	}
	var plan runtime.Plan
	if err := json.NewDecoder(response.Body).Decode(&plan); err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if len(plan.Changes) != 0 {
		t.Fatalf("plan had changes: %+v", plan)
	}
	for i := 0; i < 2; i++ {
		response = workspaceRequest(t, workspace, http.MethodPut, path, "owner-token", map[string]any{"document": document, "approved_plan": plan})
		if response.StatusCode != http.StatusOK {
			data, _ := io.ReadAll(response.Body)
			t.Fatalf("apply %d status %d: %s", i, response.StatusCode, data)
		}
		var result runtime.WorkspaceApplyResult
		if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if result.Version != 1 || result.Status != "done" {
			t.Fatalf("apply %d result %+v", i, result)
		}
	}
	if len(repo.versions) != 1 {
		t.Fatalf("saved %d versions", len(repo.versions))
	}
	response = workspaceRequest(t, workspace, http.MethodGet, path, "owner-token", nil)
	data, _ := io.ReadAll(response.Body)
	response.Body.Close()
	pulled, err := spec.Parse(data)
	if err != nil || len(pulled.Metadata.Domains) != 1 || pulled.People.Invite != "quiet" {
		t.Fatalf("pull: %+v, %v", pulled, err)
	}
	response = workspaceRequest(t, workspace, http.MethodGet, path+"/status", "owner-token", nil)
	var status struct {
		Status  string `json:"status"`
		Version int64  `json:"version"`
	}
	if err := json.NewDecoder(response.Body).Decode(&status); err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if status.Status != "done" || status.Version != 1 {
		t.Fatalf("status %+v", status)
	}
}

func TestWorkspaceApplyRejectsUnapprovedRemoval(t *testing.T) {
	api := fakeOrganizationAPI(true)
	defer api.Close()
	repo := &memoryRepository{}
	server, err := New(repo, api.URL)
	if err != nil {
		t.Fatal(err)
	}
	workspace := httptest.NewServer(server)
	defer workspace.Close()
	document := "apiVersion: oryxa.in/v1\nkind: Workspace\nmetadata: {organization: acme}\npeople:\n  prune: true\n  members: [{email: owner@acme.com, role: owner}]\n"
	response := workspaceRequest(t, workspace, http.MethodPut, "/v1/organizations/org1/workspace", "owner-token", map[string]any{"document": document, "approved_plan": runtime.Plan{}})
	defer response.Body.Close()
	if response.StatusCode != http.StatusConflict || len(repo.versions) != 0 {
		t.Fatalf("unapproved removal returned %d and saved %d versions", response.StatusCode, len(repo.versions))
	}
}

func TestWorkspaceRollbackRestoresEarlierFileAsNewVersion(t *testing.T) {
	api := fakeOrganizationAPI(false)
	defer api.Close()
	repo := &memoryRepository{}
	server, err := New(repo, api.URL)
	if err != nil {
		t.Fatal(err)
	}
	workspace := httptest.NewServer(server)
	defer workspace.Close()
	path := "/v1/organizations/org1/workspace"
	files := []string{
		"apiVersion: oryxa.in/v1\nkind: Workspace\nmetadata: {organization: acme, domains: [acme.com]}\npreset: team\n",
		"apiVersion: oryxa.in/v1\nkind: Workspace\nmetadata: {organization: acme, domains: [acme.com]}\npreset: startup\n",
	}
	for _, document := range files {
		response := workspaceRequest(t, workspace, http.MethodPut, path, "owner-token", map[string]any{"document": document, "approved_plan": runtime.Plan{}})
		if response.StatusCode != http.StatusOK {
			data, _ := io.ReadAll(response.Body)
			t.Fatalf("apply status %d: %s", response.StatusCode, data)
		}
		response.Body.Close()
	}
	response := workspaceRequest(t, workspace, http.MethodGet, path+"/versions/1", "owner-token", nil)
	data, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusOK || !bytes.Contains(data, []byte("acme.com")) {
		t.Fatalf("read version 1 status %d: %s", response.StatusCode, data)
	}
	response = workspaceRequest(t, workspace, http.MethodPost, path+":rollback", "owner-token", map[string]any{"to": 1, "approved_plan": runtime.Plan{}})
	if response.StatusCode != http.StatusOK {
		data, _ := io.ReadAll(response.Body)
		t.Fatalf("rollback status %d: %s", response.StatusCode, data)
	}
	var result runtime.WorkspaceApplyResult
	if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if result.Version != 3 || len(repo.versions) != 3 || !bytes.Equal(repo.versions[0].Document, repo.versions[2].Document) {
		t.Fatalf("rollback result %+v, versions %d", result, len(repo.versions))
	}
}

func TestWorkspaceContextPlanApplyReadAndRollback(t *testing.T) {
	api := fakeOrganizationAPI(true)
	defer api.Close()
	repo := &memoryRepository{}
	server, err := New(repo, api.URL)
	if err != nil {
		t.Fatal(err)
	}
	workspace := httptest.NewServer(server)
	defer workspace.Close()
	path := "/v1/organizations/org1/workspace"
	first := "apiVersion: oryxa.in/v1\nkind: Workspace\nmetadata: {organization: acme}\nownership: [{owner: owner@acme.com, system: billing}]\norgChart:\n  authority: [manages]\n  executives: [owner@acme.com]\n  relationships: [{from: owner@acme.com, relation: manages, to: other@acme.com}]\n"
	response := workspaceRequest(t, workspace, http.MethodPost, path+":plan", "owner-token", map[string]string{"document": first})
	var plan runtime.Plan
	if err := json.NewDecoder(response.Body).Decode(&plan); err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusOK || len(plan.Changes) != 4 {
		t.Fatalf("context plan status=%d plan=%+v", response.StatusCode, plan)
	}
	response = workspaceRequest(t, workspace, http.MethodPut, path, "owner-token", map[string]any{"document": first, "approved_plan": plan})
	if response.StatusCode != http.StatusOK {
		data, _ := io.ReadAll(response.Body)
		t.Fatalf("apply status=%d body=%s", response.StatusCode, data)
	}
	response.Body.Close()
	response = workspaceRequest(t, workspace, http.MethodGet, path+"/context", "owner-token", nil)
	var context struct {
		Ownership []spec.OwnerHint `json:"ownership"`
		OrgChart  spec.OrgChart    `json:"orgChart"`
	}
	if err := json.NewDecoder(response.Body).Decode(&context); err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusOK || len(context.Ownership) != 1 || len(context.OrgChart.Relationships) != 1 {
		t.Fatalf("context status=%d context=%+v", response.StatusCode, context)
	}
	second := "apiVersion: oryxa.in/v1\nkind: Workspace\nmetadata: {organization: acme}\n"
	response = workspaceRequest(t, workspace, http.MethodPut, path, "owner-token", map[string]any{"document": second, "approved_plan": runtime.Plan{}})
	response.Body.Close()
	if response.StatusCode != http.StatusOK || len(repo.versions) != 1 {
		t.Fatalf("omission changed context: status=%d versions=%d", response.StatusCode, len(repo.versions))
	}
	pruned := second + "ownership: {items: [], prune: true}\norgChart: {prune: true}\n"
	response = workspaceRequest(t, workspace, http.MethodPost, path+":plan", "owner-token", map[string]string{"document": pruned})
	if err := json.NewDecoder(response.Body).Decode(&plan); err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if !runtime.RemovalsDiffer(plan, runtime.Plan{}) {
		t.Fatalf("prune did not plan removals: %+v", plan)
	}
	response = workspaceRequest(t, workspace, http.MethodPut, path, "owner-token", map[string]any{"document": pruned, "approved_plan": runtime.Plan{}})
	response.Body.Close()
	if response.StatusCode != http.StatusConflict {
		t.Fatalf("unapproved context removal returned %d", response.StatusCode)
	}
	response = workspaceRequest(t, workspace, http.MethodPut, path, "owner-token", map[string]any{"document": pruned, "approved_plan": plan})
	response.Body.Close()
	if response.StatusCode != http.StatusOK || len(repo.versions) != 2 {
		t.Fatalf("approved context removal returned %d, versions=%d", response.StatusCode, len(repo.versions))
	}
	response = workspaceRequest(t, workspace, http.MethodPost, path+":rollback-plan", "owner-token", map[string]any{"to": 1})
	if err := json.NewDecoder(response.Body).Decode(&plan); err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusOK || len(plan.Changes) != 4 {
		t.Fatalf("rollback plan status=%d plan=%+v", response.StatusCode, plan)
	}
	response = workspaceRequest(t, workspace, http.MethodPost, path+":rollback", "owner-token", map[string]any{"to": 1, "approved_plan": plan})
	response.Body.Close()
	if response.StatusCode != http.StatusOK || len(repo.versions) != 3 {
		t.Fatalf("rollback status=%d versions=%d", response.StatusCode, len(repo.versions))
	}
}

func TestWorkspaceContextReadsAsMemberAndFiltersPendingPeople(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "Bearer member-token" {
			writer.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch request.URL.Path {
		case "/v1/organizations/org1":
			_, _ = io.WriteString(writer, `{"organization":{"id":"org1","slug":"acme"},"membership":{"organization_id":"org1","user_id":"u1","role":"member"}}`)
		case "/v1/organizations/org1/directory":
			_, _ = io.WriteString(writer, `{"members":[{"email":"joined@acme.com"}]}`)
		default:
			writer.WriteHeader(http.StatusNotFound)
		}
	}))
	defer api.Close()
	file := []byte("apiVersion: oryxa.in/v1\nkind: Workspace\nmetadata: {organization: acme}\nownership:\n  - {owner: joined@acme.com, system: billing}\n  - {owner: pending@acme.com, system: payroll}\norgChart:\n  authority: [manages]\n  executives: [pending@acme.com]\n  relationships: [{from: pending@acme.com, relation: manages, to: joined@acme.com}]\n")
	repo := &memoryRepository{versions: []store.Version{{Number: 1, Document: file}}}
	server, err := New(repo, api.URL)
	if err != nil {
		t.Fatal(err)
	}
	workspace := httptest.NewServer(server)
	defer workspace.Close()
	response := workspaceRequest(t, workspace, http.MethodGet, "/v1/organizations/org1/workspace/context", "member-token", nil)
	var context struct {
		Ownership []spec.OwnerHint `json:"ownership"`
		OrgChart  spec.OrgChart    `json:"orgChart"`
	}
	if err := json.NewDecoder(response.Body).Decode(&context); err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusOK || len(context.Ownership) != 1 || context.Ownership[0].System != "billing" || len(context.OrgChart.Executives) != 0 || len(context.OrgChart.Relationships) != 0 {
		t.Fatalf("status=%d context=%+v", response.StatusCode, context)
	}
}

func TestWorkspacePoliciesApplyAndRollbackAsVersions(t *testing.T) {
	api := fakeOrganizationAPI(false)
	defer api.Close()
	repo := &memoryRepository{}
	server, err := New(repo, api.URL)
	if err != nil {
		t.Fatal(err)
	}
	workspace := httptest.NewServer(server)
	defer workspace.Close()
	path := "/v1/organizations/org1/workspace"
	first := "apiVersion: oryxa.in/v1\nkind: Workspace\nmetadata: {organization: acme}\nrules: {consent: {expireAfter: 6h}}\nsafety: {guard: {holdForReview: true}}\ntools: {offer: [github], autoSubscribe: false}\n"
	response := workspaceRequest(t, workspace, http.MethodPost, path+":plan", "owner-token", map[string]string{"document": first})
	var plan runtime.Plan
	if err := json.NewDecoder(response.Body).Decode(&plan); err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusOK || len(plan.Changes) != 4 {
		t.Fatalf("policy plan status=%d plan=%+v", response.StatusCode, plan)
	}
	response = workspaceRequest(t, workspace, http.MethodPut, path, "owner-token", map[string]any{"document": first, "approved_plan": plan})
	response.Body.Close()
	if response.StatusCode != http.StatusOK || len(repo.versions) != 1 {
		t.Fatalf("policy apply status=%d versions=%d", response.StatusCode, len(repo.versions))
	}
	response = workspaceRequest(t, workspace, http.MethodGet, path, "owner-token", nil)
	pulled, err := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusOK || !bytes.Contains(pulled, []byte("offer:")) || !bytes.Contains(pulled, []byte("autoSubscribe: false")) {
		t.Fatalf("pull omitted tools policy: status=%d document=%s", response.StatusCode, pulled)
	}
	omitted := "apiVersion: oryxa.in/v1\nkind: Workspace\nmetadata: {organization: acme}\n"
	response = workspaceRequest(t, workspace, http.MethodPut, path, "owner-token", map[string]any{"document": omitted, "approved_plan": runtime.Plan{}})
	response.Body.Close()
	if response.StatusCode != http.StatusOK || len(repo.versions) != 1 {
		t.Fatalf("policy omission status=%d versions=%d", response.StatusCode, len(repo.versions))
	}
	second := "apiVersion: oryxa.in/v1\nkind: Workspace\nmetadata: {organization: acme}\nrules: {consent: {expireAfter: 2h}}\nsafety: {guard: {holdForReview: false}}\ntools: {offer: [], autoSubscribe: true}\n"
	response = workspaceRequest(t, workspace, http.MethodPut, path, "owner-token", map[string]any{"document": second, "approved_plan": runtime.Plan{}})
	response.Body.Close()
	if response.StatusCode != http.StatusOK || len(repo.versions) != 2 {
		t.Fatalf("policy update status=%d versions=%d", response.StatusCode, len(repo.versions))
	}
	response = workspaceRequest(t, workspace, http.MethodPost, path+":rollback-plan", "owner-token", map[string]any{"to": 1})
	if err := json.NewDecoder(response.Body).Decode(&plan); err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusOK || len(plan.Changes) != 4 {
		t.Fatalf("policy rollback plan status=%d plan=%+v", response.StatusCode, plan)
	}
	response = workspaceRequest(t, workspace, http.MethodPost, path+":rollback", "owner-token", map[string]any{"to": 1, "approved_plan": plan})
	response.Body.Close()
	if response.StatusCode != http.StatusOK || len(repo.versions) != 3 || !bytes.Equal(repo.versions[0].Document, repo.versions[2].Document) {
		t.Fatalf("policy rollback status=%d versions=%d", response.StatusCode, len(repo.versions))
	}
}
