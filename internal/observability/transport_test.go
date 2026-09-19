package observability

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestTransportPropagatesTraceparentAndNamesTheClientSpan(t *testing.T) {
	// Both the parent span and Transport's client span must come from the
	// same provider for this test's recorder to see the client span:
	// otelhttp's RoundTripper deliberately builds it from whatever provider
	// made the context's current span, falling back to the global provider
	// only when there is none (see its transport.go). That is also how
	// production actually behaves, since there is only one provider once
	// Setup has run, so starting the parent span on Tracer() - which resolves
	// the global provider at call time, exactly as production code does -
	// is the representative way to set this test up, not a workaround.
	recorder := withRecordedSpans(t)
	withPropagator(t)

	var gotTraceparent string
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		gotTraceparent = request.Header.Get("traceparent")
		writer.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(server.Close)

	ctx, parent := Tracer().Start(context.Background(), "caller")
	t.Cleanup(func() { parent.End() })
	client := &http.Client{Transport: Transport(nil)}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+"/v1/widgets/abc", nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()

	if gotTraceparent == "" {
		t.Fatal("outbound request did not carry a traceparent header")
	}
	if !strings.HasPrefix(gotTraceparent, "00-") {
		t.Fatalf("traceparent = %q, want a W3C version-00 header", gotTraceparent)
	}

	spans := recorder.Ended()
	if len(spans) != 1 {
		t.Fatalf("ended spans = %d, want 1 client span", len(spans))
	}
	// The span name must be "HTTP <METHOD> <host>", not the default (which
	// otelhttp builds from the full URL, including this request's path -
	// exactly the per-request-id cardinality server-side naming avoids).
	serverURL, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	if want := "HTTP GET " + serverURL.Hostname(); spans[0].Name() != want {
		t.Fatalf("client span name = %q, want %q", spans[0].Name(), want)
	}
}

func TestTransportDefaultsToHTTPDefaultTransport(t *testing.T) {
	if Transport(nil) == nil {
		t.Fatal("Transport(nil) returned nil")
	}
}
