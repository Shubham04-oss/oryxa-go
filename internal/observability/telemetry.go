package observability

import (
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
)

// instrumentationName scopes the tracer and meter this package hands out, so
// they show up in a backend's instrumentation-scope view as one thing:
// Oryxa's own event-pipeline and HTTP glue, as opposed to a library's.
const instrumentationName = "github.com/Shubham04-oss/oryxa-go/internal/observability"

// Tracer is the tracer this package's own helpers start spans on: the
// producer and consumer sides of the event pipeline, and the HTTP server and
// client instrumentation. A service's own application code should still
// prefer otel.Tracer(its own name) for spans it starts itself, so those spans
// are attributed to the code that opened them.
func Tracer() trace.Tracer { return otel.Tracer(instrumentationName) }

// Meter is the meter this package's own metrics are recorded on.
func Meter() metric.Meter { return otel.Meter(instrumentationName) }
