package httpserver

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Shubham04-oss/oryxa-go/internal/workspace/spec"
	"github.com/Shubham04-oss/oryxa-go/internal/workspace/store"
)

func TestContextHandlesTenThousandMemberDirectory(t *testing.T) {
	members := make([]map[string]any, 10000)
	for i := range members {
		members[i] = map[string]any{"email": fmt.Sprintf("member%d@acme.com", i), "display_name": strings.Repeat("a", 220), "teams": []map[string]string{{"id": "team1", "name": "Engineering", "path": "org1.team1"}}}
	}
	payload, err := json.Marshal(map[string]any{"members": members})
	if err != nil {
		t.Fatal(err)
	}
	if len(payload) <= 2<<20 || len(payload) > 8<<20 {
		t.Fatalf("test directory is outside intended size: %d", len(payload))
	}
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/organizations/org1":
			io.WriteString(w, `{"organization":{"id":"org1","slug":"acme"},"membership":{"organization_id":"org1","user_id":"u1","role":"member"}}`)
		case "/v1/organizations/org1/directory":
			w.Write(payload)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer api.Close()
	document := []byte("apiVersion: oryxa.in/v1\nkind: Workspace\nmetadata: {organization: acme}\nownership: [{owner: member9999@acme.com, system: billing}]\n")
	repo := &memoryRepository{versions: []store.Version{{Number: 1, Document: document}}}
	server, err := New(repo, api.URL)
	if err != nil {
		t.Fatal(err)
	}
	workspace := httptest.NewServer(server)
	defer workspace.Close()
	response := workspaceRequest(t, workspace, http.MethodGet, "/v1/organizations/org1/workspace/context", "member-token", nil)
	defer response.Body.Close()
	var result struct {
		Ownership []spec.OwnerHint `json:"ownership"`
	}
	if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK || len(result.Ownership) != 1 || result.Ownership[0].Owner != "member9999@acme.com" {
		t.Fatalf("large directory context lost member: status=%d %+v", response.StatusCode, result)
	}
}
