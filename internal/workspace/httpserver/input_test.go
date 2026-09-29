package httpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
)

func TestSubmissionRejectsUncleanApprovalFields(t *testing.T) {
	api := fakeOrganizationAPI(false)
	defer api.Close()
	repo := &memoryRepository{}
	server, err := New(repo, api.URL)
	if err != nil {
		t.Fatal(err)
	}
	workspace := httptest.NewServer(server)
	defer workspace.Close()
	for _, path := range []string{"/v1/organizations/org1/workspace", "/v1/organizations/org1/workspace:rollback"} {
		body := map[string]any{"approved_plan": map[string]string{"plan_id": "invalid\x00identifier"}}
		method := http.MethodPut
		if strings.HasSuffix(path, ":rollback") {
			method, body["to"] = http.MethodPost, 1
		} else {
			body["document"] = "apiVersion: oryxa.in/v1\nkind: Workspace\nmetadata: {organization: acme}\n"
		}
		response := workspaceRequest(t, workspace, method, path, "owner-token", body)
		response.Body.Close()
		if response.StatusCode != http.StatusBadRequest {
			t.Fatalf("unclean approval on %s returned %d", path, response.StatusCode)
		}
	}
	if len(repo.versions) != 0 || len(repo.attempts) != 0 {
		t.Fatal("unclean approval reached persistence")
	}
}

func TestAuthorizationPropagatesTrace(t *testing.T) {
	previous := otel.GetTextMapPropagator()
	otel.SetTextMapPropagator(propagation.TraceContext{})
	t.Cleanup(func() { otel.SetTextMapPropagator(previous) })
	parent := trace.NewSpanContext(trace.SpanContextConfig{TraceID: trace.TraceID{1, 2, 3}, SpanID: trace.SpanID{4, 5, 6}, TraceFlags: trace.FlagsSampled})
	api := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if !strings.Contains(request.Header.Get("Traceparent"), parent.TraceID().String()) {
			t.Error("auth call lost parent trace")
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"organization":{"id":"org1","slug":"acme"},"membership":{"organization_id":"org1","user_id":"u1","role":"owner"}}`))
	}))
	defer api.Close()
	server, err := New(&memoryRepository{}, api.URL)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(map[string]string{"document": "apiVersion: oryxa.in/v1\nkind: Workspace\nmetadata: {organization: acme}\n"})
	request := httptest.NewRequest(http.MethodPost, "/v1/organizations/org1/workspace:validate", bytes.NewReader(data))
	request = request.WithContext(trace.ContextWithSpanContext(context.Background(), parent))
	request.Header.Set("Authorization", "Bearer owner-token")
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("validate = %d %s", response.Code, response.Body.String())
	}
}
