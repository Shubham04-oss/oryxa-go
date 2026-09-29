package workspaceclient

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
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

func TestRejectsUnsafeWorkspaceOrigins(t *testing.T) {
	for _, url := range []string{"http://workspace.example", "https://user:password@workspace.example", "https://workspace.example/path", "https://workspace.example?token=value"} {
		if _, err := New(url, nil); err == nil {
			t.Fatalf("accepted unsafe workspace origin %q", url)
		}
	}
}

func TestContextReadPropagatesTrace(t *testing.T) {
	previous := otel.GetTextMapPropagator()
	otel.SetTextMapPropagator(propagation.TraceContext{})
	t.Cleanup(func() { otel.SetTextMapPropagator(previous) })
	parent := trace.NewSpanContext(trace.SpanContextConfig{
		TraceID: trace.TraceID{1, 2, 3}, SpanID: trace.SpanID{4, 5, 6}, TraceFlags: trace.FlagsSampled,
	})
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if !strings.Contains(request.Header.Get("Traceparent"), parent.TraceID().String()) {
			t.Errorf("context read lost parent trace")
		}
		_, _ = writer.Write([]byte(`{"ownership":[],"orgChart":{}}`))
	}))
	defer server.Close()
	client, err := New(server.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Load(trace.ContextWithSpanContext(context.Background(), parent), "org_1", "member-token"); err != nil {
		t.Fatal(err)
	}
}
