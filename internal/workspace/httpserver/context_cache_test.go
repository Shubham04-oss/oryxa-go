package httpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/Shubham04-oss/oryxa-go/internal/workspace/spec"
	"github.com/Shubham04-oss/oryxa-go/internal/workspace/store"
)

type cachedContextRepository struct {
	version int64
	reads   int
}

type countingContextRepository struct {
	*memoryRepository
	reads atomic.Int32
}

func (repo *countingContextRepository) Latest(ctx context.Context, org string) (store.Version, error) {
	repo.reads.Add(1)
	return repo.memoryRepository.Latest(ctx, org)
}

func TestCachedContextRechecksAccessAndLiveMembership(t *testing.T) {
	var authorized, joined atomic.Bool
	var accessReads, directoryReads atomic.Int32
	authorized.Store(true)
	joined.Store(true)
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/organizations/org1":
			accessReads.Add(1)
			if !authorized.Load() {
				w.WriteHeader(http.StatusForbidden)
				return
			}
			io.WriteString(w, `{"organization":{"id":"org1","slug":"acme"},"membership":{"organization_id":"org1","user_id":"owner1","role":"owner"}}`)
		case "/v1/organizations/org1/directory":
			directoryReads.Add(1)
			if joined.Load() {
				io.WriteString(w, `{"members":[{"user_id":"owner1","email":"owner@acme.com","org_role":"owner"},{"user_id":"joined1","email":"joined@acme.com","org_role":"member"}]}`)
			} else {
				io.WriteString(w, `{"members":[{"user_id":"owner1","email":"owner@acme.com","org_role":"owner"}]}`)
			}
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer api.Close()
	document := []byte("apiVersion: oryxa.in/v1\nkind: Workspace\nmetadata: {organization: acme}\nownership: [{owner: joined@acme.com, system: billing}]\norgChart: {authority: [manages], executives: [owner@acme.com], relationships: [{from: owner@acme.com, to: joined@acme.com, relation: manages}]}\n")
	repo := &countingContextRepository{memoryRepository: &memoryRepository{versions: []store.Version{{Number: 1, Document: document}}}}
	server, err := New(repo, api.URL)
	if err != nil {
		t.Fatal(err)
	}
	workspace := httptest.NewServer(server)
	defer workspace.Close()
	read := func() ([]spec.OwnerHint, spec.OrgChart) {
		t.Helper()
		response := workspaceRequest(t, workspace, http.MethodGet, "/v1/organizations/org1/workspace/context", "owner-token", nil)
		defer response.Body.Close()
		var result struct {
			Ownership []spec.OwnerHint `json:"ownership"`
			OrgChart  spec.OrgChart    `json:"orgChart"`
		}
		if err := json.NewDecoder(response.Body).Decode(&result); err != nil || response.StatusCode != http.StatusOK {
			t.Fatalf("context request status=%d: %v", response.StatusCode, err)
		}
		return result.Ownership, result.OrgChart
	}
	owners, chart := read()
	if len(owners) != 1 || len(chart.Relationships) != 1 {
		t.Fatalf("initial context lost joined member: %+v %+v", owners, chart)
	}
	joined.Store(false)
	owners, chart = read()
	if len(owners) != 0 || len(chart.Relationships) != 0 || repo.reads.Load() != 1 || accessReads.Load() != 2 || directoryReads.Load() != 2 {
		t.Fatalf("cached context bypassed current roster or reparsed document: %+v %+v reads=%d access=%d directory=%d", owners, chart, repo.reads.Load(), accessReads.Load(), directoryReads.Load())
	}
	authorized.Store(false)
	response := workspaceRequest(t, workspace, http.MethodGet, "/v1/organizations/org1/workspace/context", "owner-token", nil)
	response.Body.Close()
	if response.StatusCode != http.StatusForbidden || repo.reads.Load() != 1 || directoryReads.Load() != 2 {
		t.Fatalf("cache bypassed revoked access: status=%d reads=%d directory=%d", response.StatusCode, repo.reads.Load(), directoryReads.Load())
	}
}

func (repo *cachedContextRepository) VersionNumber(context.Context, string) (int64, error) {
	return repo.version, nil
}
func (repo *cachedContextRepository) Latest(context.Context, string) (store.Version, error) {
	repo.reads++
	return store.Version{Number: repo.version, Document: []byte(fmt.Sprintf("apiVersion: oryxa.in/v1\nkind: Workspace\nmetadata: {organization: acme}\nownership: [{owner: owner@acme.com, system: system%d, tags: [billing]}]\n", repo.version))}, nil
}

func TestContextCacheLoadsOnlyOnVersionChangeAndIsolatesCopies(t *testing.T) {
	cache := &contextCache{}
	repo := &cachedContextRepository{version: 1}
	first, err := cache.Get(context.Background(), repo, "org1")
	if err != nil {
		t.Fatal(err)
	}
	first.Ownership.Items[0].Tags[0] = "mutated"
	second, err := cache.Get(context.Background(), repo, "org1")
	if err != nil || repo.reads != 1 || second.Ownership.Items[0].Tags[0] != "billing" {
		t.Fatalf("cache reparsed manifest or shared mutable context: %+v reads=%d %v", second, repo.reads, err)
	}
	repo.version = 2
	third, err := cache.Get(context.Background(), repo, "org1")
	if err != nil || repo.reads != 2 || third.Ownership.Items[0].System != "system2" {
		t.Fatalf("version change was not loaded: %+v reads=%d %v", third, repo.reads, err)
	}
}

func TestContextCacheBoundedAcrossOrganizations(t *testing.T) {
	cache := &contextCache{}
	repo := &cachedContextRepository{version: 1}
	for i := 0; i < contextCacheLimit+20; i++ {
		if _, err := cache.Get(context.Background(), repo, fmt.Sprintf("org%d", i)); err != nil {
			t.Fatal(err)
		}
	}
	if len(cache.entries) != contextCacheLimit {
		t.Fatalf("context cache grew to %d entries", len(cache.entries))
	}
}
