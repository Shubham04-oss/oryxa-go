package observability

import (
	"net/http"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
)

// Transport wraps base (http.DefaultTransport when base is nil) so an
// outbound call carries a client span and propagates traceparent to whatever
// it calls: another Oryxa service, or a third party such as Composio, Dodo or
// Cognito. Every http.Client a service builds for such a call should use it,
// so a trace does not go dark the moment it leaves the process.
//
// Client span names are "HTTP <METHOD> <host>" rather than the default,
// which would include the full request path: a path can carry an id (an
// organization, a run, a member), and an unbounded span-name cardinality is
// exactly what the per-route naming in http.go exists to avoid on the server
// side.
func Transport(base http.RoundTripper) http.RoundTripper {
	if base == nil {
		base = http.DefaultTransport
	}
	return otelhttp.NewTransport(base,
		otelhttp.WithSpanNameFormatter(func(_ string, request *http.Request) string {
			return "HTTP " + request.Method + " " + request.URL.Hostname()
		}),
	)
}
