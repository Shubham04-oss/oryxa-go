package workspaceclient

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/agents"
	"github.com/Shubham04-oss/oryxa-go/internal/workspace/spec"
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

func TestLoadAcceptsLargeOrganizationContext(t *testing.T) {
	fixture := agents.DirectoryContext{}
	for i := range 10000 {
		fixture.Ownership = append(fixture.Ownership, spec.OwnerHint{Owner: fmt.Sprintf("member-%d@example.com", i), System: fmt.Sprintf("system-%d-%s", i, strings.Repeat("a", 180))})
	}
	encoded, err := json.Marshal(fixture)
	if err != nil || len(encoded) <= 2<<20 {
		t.Fatalf("fixture must exceed the former limit: %d bytes, %v", len(encoded), err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) { _, _ = writer.Write(encoded) }))
	defer server.Close()
	client, err := New(server.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	got, err := client.Load(context.Background(), "org_1", "member-token")
	if err != nil || len(got.Ownership) != 10000 {
		t.Fatalf("large context ownership count = %d, %v", len(got.Ownership), err)
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
