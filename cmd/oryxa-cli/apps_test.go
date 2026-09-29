package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestConnectAppReturnsReviewableBrowserStep(t *testing.T) {
	var started int
	api := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "Bearer short-lived" {
			t.Errorf("authorization = %q", request.Header.Get("Authorization"))
		}
		switch request.Method + " " + request.URL.Path {
		case "GET /v1/organizations":
			fmt.Fprint(writer, `{"organizations":[{"id":"org_1","slug":"acme"}]}`)
		case "GET /v1/organizations/org_1/apps/offer":
			fmt.Fprint(writer, `{"offer":["github"]}`)
		case "GET /v1/organizations/org_1/apps/connections":
			if request.URL.Query().Get("toolkit") != "github" {
				t.Errorf("toolkit query = %q", request.URL.Query().Get("toolkit"))
			}
			fmt.Fprint(writer, `{"connections":[]}`)
		case "POST /v1/organizations/org_1/apps/connections":
			started++
			var body map[string]string
			if err := json.NewDecoder(request.Body).Decode(&body); err != nil || body["toolkit"] != "github" {
				t.Errorf("connection body = %v, %v", body, err)
			}
			fmt.Fprint(writer, `{"connection":{"id":"apc_1","toolkit":"github","status":"pending"},"redirect_url":"https://provider.example/authorize?state=one"}`)
		default:
			t.Errorf("unexpected request %s %s", request.Method, request.URL)
			writer.WriteHeader(http.StatusNotFound)
		}
	}))
	defer api.Close()
	getenv := func(key string) string {
		if key == "ORYXA_ACCESS_TOKEN" {
			return "short-lived"
		}
		return ""
	}
	for _, args := range [][]string{
		{"connect-app", "github", "--org", "acme", "--api", api.URL, "--no-browser", "--json"},
		{"connect-app", "--org", "acme", "--api", api.URL, "--no-browser", "--json", "github"},
	} {
		var stdout, stderr bytes.Buffer
		if code := run(context.Background(), args, &stdout, &stderr, getenv); code != 2 {
			t.Fatalf("args %v exit %d, stderr %s", args, code, stderr.String())
		}
		var result struct {
			Step         string `json:"step"`
			Status       string `json:"status"`
			ConnectionID string `json:"connection_id"`
			Next         struct {
				Action string `json:"action"`
				URL    string `json:"url"`
			} `json:"next"`
		}
		if err := json.Unmarshal(stdout.Bytes(), &result); err != nil || result.Step != "apps.connect" || result.Status != "needs_human" || result.ConnectionID != "apc_1" || result.Next.Action != "open_url" || result.Next.URL != "https://provider.example/authorize?state=one" {
			t.Fatalf("result = %+v, error = %v", result, err)
		}
	}
	if started != 2 {
		t.Fatalf("started %d connections", started)
	}
}

func TestConnectAppReusesActiveConnection(t *testing.T) {
	var started bool
	api := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.Method + " " + request.URL.Path {
		case "GET /v1/organizations":
			fmt.Fprint(writer, `{"organizations":[{"id":"org_1","slug":"acme"}]}`)
		case "GET /v1/organizations/org_1/apps/offer":
			fmt.Fprint(writer, `{"offer":["github"]}`)
		case "GET /v1/organizations/org_1/apps/connections":
			fmt.Fprint(writer, `{"connections":[{"id":"apc_existing","toolkit":"github","status":"active"}]}`)
		default:
			started = true
			writer.WriteHeader(http.StatusNotFound)
		}
	}))
	defer api.Close()
	var stdout, stderr bytes.Buffer
	getenv := func(key string) string {
		if key == "ORYXA_ACCESS_TOKEN" {
			return "short-lived"
		}
		return ""
	}
	code := run(context.Background(), []string{"connect-app", "github", "--org", "acme", "--api", api.URL, "--no-input"}, &stdout, &stderr, getenv)
	if code != 0 || started || !strings.Contains(stdout.String(), "apc_existing") {
		t.Fatalf("exit %d, started %v, stdout %q, stderr %q", code, started, stdout.String(), stderr.String())
	}
}

func TestConnectAppRejectsUnsafeRedirectAndBadToolkit(t *testing.T) {
	for _, target := range []string{"http://untrusted.example/start", "https://user:password@provider.example/authorize", "javascript:alert(1)"} {
		if validAppRedirect(target) {
			t.Fatalf("allowed redirect %q", target)
		}
	}
	var stdout, stderr bytes.Buffer
	code := run(context.Background(), []string{"connect-app", "GitHub", "--org", "acme", "--no-input"}, &stdout, &stderr, func(string) string { return "" })
	if code != 3 || !strings.Contains(stderr.String(), "lowercase toolkit slug") {
		t.Fatalf("invalid slug exit %d, stderr %q", code, stderr.String())
	}
}

func TestConnectAppRefusesRestrictedExistingConnection(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/v1/organizations":
			fmt.Fprint(writer, `{"organizations":[{"id":"org_1","slug":"acme"}]}`)
		case "/v1/organizations/org_1/apps/offer":
			fmt.Fprint(writer, `{"offer":[]}`)
		default:
			t.Errorf("restricted app reached %s", request.URL.Path)
			writer.WriteHeader(http.StatusNotFound)
		}
	}))
	defer api.Close()
	var stdout, stderr bytes.Buffer
	getenv := func(key string) string {
		if key == "ORYXA_ACCESS_TOKEN" {
			return "short-lived"
		}
		return ""
	}
	code := run(context.Background(), []string{"connect-app", "github", "--org", "acme", "--api", api.URL, "--no-input"}, &stdout, &stderr, getenv)
	if code != 1 || !strings.Contains(stderr.String(), "not offered") {
		t.Fatalf("exit %d, stderr %q", code, stderr.String())
	}
}

func TestPendingAppConnectionIsResumableNeedsHuman(t *testing.T) {
	testSessionStore(t)
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/organizations":
			fmt.Fprint(w, `{"organizations":[{"id":"org_1","slug":"acme"}]}`)
		case "/v1/organizations/org_1/apps/offer":
			fmt.Fprint(w, `{"offer":["github"]}`)
		case "/v1/organizations/org_1/apps/connections":
			fmt.Fprint(w, `{"connections":[{"id":"pending","toolkit":"github","status":"pending"}]}`)
		default:
			t.Errorf("unexpected request %s", r.URL)
			w.WriteHeader(404)
		}
	}))
	defer api.Close()
	target := "https://provider.example/authorize?state=pending"
	savePendingAppURL(api.URL, "org_1", "pending", target)
	var stdout, stderr bytes.Buffer
	getenv := func(key string) string {
		if key == "ORYXA_ACCESS_TOKEN" {
			return "access"
		}
		return ""
	}
	code := run(context.Background(), []string{"connect-app", "github", "--api", api.URL, "--org", "acme", "--json", "--no-input"}, &stdout, &stderr, getenv)
	if code != 2 || !strings.Contains(stdout.String(), `"status":"needs_human"`) || !strings.Contains(stdout.String(), target) {
		t.Fatalf("pending connection: exit%d %s %s", code, stdout.String(), stderr.String())
	}
}
