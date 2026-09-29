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
