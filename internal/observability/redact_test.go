package observability

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
)

// adkToolDescriptionKey is set on a tool span as the call ends, from the
// tool's own description.
const adkToolDescriptionKey = attribute.Key("gen_ai.tool.description")

// exportBoth records spans on one provider that exports each twice: as they
// were, and through RedactToolCalls. The two exporters see the same span, so
// what the redaction changed, and what it left alone, can be told by
// comparing them.
func exportBoth(t *testing.T, record func(tracer trace.Tracer)) (plain, exported map[string]tracetest.SpanStub) {
	t.Helper()
	plainExporter, redactedExporter := tracetest.NewInMemoryExporter(), tracetest.NewInMemoryExporter()
	provider := sdktrace.NewTracerProvider(
		sdktrace.WithSyncer(plainExporter),
		sdktrace.WithSyncer(RedactToolCalls(redactedExporter)),
	)
	record(provider.Tracer("test"))
	if err := provider.ForceFlush(context.Background()); err != nil {
		t.Fatal(err)
	}
	byName := func(stubs tracetest.SpanStubs) map[string]tracetest.SpanStub {
		named := map[string]tracetest.SpanStub{}
		for _, stub := range stubs {
			named[stub.Name] = stub
		}
		return named
	}
	plain, exported = byName(plainExporter.GetSpans()), byName(redactedExporter.GetSpans())
	if len(plain) == 0 || len(plain) != len(exported) {
		t.Fatalf("exported %d spans, recorded %d", len(exported), len(plain))
	}
	return plain, exported
}

// toolCall is a call traced the way ADK traces every tool call: the name and
// the arguments as the span starts, and as the call ends its description, its
// ids, its response and, when it failed, the error recorded twice over.
type toolCall struct {
	name, args, response string
	err                  error
	// spanName is the span's name, ADK's "execute_tool <name>" when empty.
	spanName string
	// secrets are what the call carried that no export may.
	secrets []string
}

func (call toolCall) span() string {
	if call.spanName != "" {
		return call.spanName
	}
	return "execute_tool " + call.name
}

func (call toolCall) trace(tracer trace.Tracer) {
	_, span := tracer.Start(context.Background(), call.span(), trace.WithAttributes(
		adkOperationKey.String(toolOperation), adkToolNameKey.String(call.name), adkToolArgsKey.String(call.args)))
	if call.err != nil {
		span.RecordError(call.err)
		span.SetStatus(codes.Error, call.err.Error())
	}
	span.SetAttributes(
		adkOperationKey.String(toolOperation),
		adkToolDescriptionKey.String("Reads what the member asked for."),
		adkEventIDKey.String("event-"+call.name),
		adkToolCallIDKey.String("call-"+call.name),
		adkToolResponseKey.String(call.response),
	)
	span.End()
}

func attributeValues(attributes []attribute.KeyValue) map[attribute.Key]string {
	values := map[attribute.Key]string{}
	for _, value := range attributes {
		values[value.Key] = value.Value.Emit()
	}
	return values
}

// everyText is each string an exported span carries: its name, its
// attributes, its events and their attributes, and its status.
func everyText(stub tracetest.SpanStub) []string {
	texts := []string{stub.Name, stub.Status.Description}
	for _, value := range stub.Attributes {
		texts = append(texts, string(value.Key), value.Value.Emit())
	}
	for _, event := range stub.Events {
		texts = append(texts, event.Name)
		for _, value := range event.Attributes {
			texts = append(texts, string(value.Key), value.Value.Emit())
		}
	}
	return texts
}

// TestNoToolCallsContentLeavesInATrace: the calls of the knowledge, apps and
// browser tools, and an agent request's, as ADK traces them, go out with
// their arguments and their response redacted, and so does a span merging
// several of them. What a failed call's error said goes too. The tool's name,
// the span's status and timing, and the kind of an error stay.
func TestNoToolCallsContentLeavesInATrace(t *testing.T) {
	calls := []toolCall{
		{
			name:     "knowledge_search",
			args:     `{"query":"Q3 layoffs plan","corpus":"hr-private"}`,
			response: `{"matches":[{"text":"Priya's salary is 210k, wire to IBAN DE89 3704 0044 0532 0130 00"}]}`,
			secrets:  []string{"layoffs", "hr-private", "210k", "DE89"},
		},
		{
			name:     "apps_run_read_tool",
			args:     `{"toolkit":"gmail","tool":"GMAIL_FETCH_EMAILS","arguments":{"query":"from:priya@example.com"}}`,
			response: `{"messages":[{"subject":"Re: acquisition offer","snippet":"Do not forward. PIN 4921."}]}`,
			secrets:  []string{"priya@example.com", "acquisition", "4921"},
		},
		{
			name:     "apps_search_tools",
			args:     `{"query":"post to the #board-only Slack channel"}`,
			response: `{"tools":[{"name":"SLACK_SEND_MESSAGE"}],"note":"channel #board-only"}`,
			secrets:  []string{"board-only"},
		},
		{
			name:     "browser_navigate",
			args:     `{"url":"https://intranet.acme.example/payroll?token=s3cr3t-token"}`,
			response: `{"page":{"title":"Payroll March","text":"Asha 9,200 Ravi 8,400"}}`,
			secrets:  []string{"intranet.acme.example", "s3cr3t-token", "Payroll", "9,200"},
		},
		{
			name:     "browser_click",
			args:     `{"selector":"#approve-wire"}`,
			response: `{"error":"click failed"}`,
			err:      errors.New(`click #approve-wire: the page said "Wire 48,000 to account 0532013000"`),
			secrets:  []string{"approve-wire", "48,000", "0532013000"},
		},
		{
			name:     "interactions_respond",
			args:     `{"summary":"Priya is at priya@example.com."}`,
			response: `{"status":"review"}`,
			secrets:  []string{"priya@example.com"},
		},
		{
			name:     "interactions_get",
			args:     `{"request_id":"irq_1"}`,
			response: `{"held":{"result":{"summary":"Call +1 415 555 0100."}}}`,
			secrets:  []string{"415 555"},
		},
		{
			// ADK's span for parallel calls holds each call's response, in one.
			name: "(merged tools)", spanName: "execute_tool (merged)", args: "N/A",
			response: `{"Content":{"parts":[{"functionResponse":{"name":"knowledge_search","response":{"text":"Priya's salary is 210k"}}}]}}`,
			secrets:  []string{"salary", "210k"},
		},
	}
	plain, exported := exportBoth(t, func(tracer trace.Tracer) {
		for _, call := range calls {
			call.trace(tracer)
		}
	})
	if len(exported) != len(calls) {
		t.Fatalf("exported %d spans, want %d", len(exported), len(calls))
	}

	for _, call := range calls {
		name := call.span()
		before, after := plain[name], exported[name]
		if after.Name == "" {
			t.Errorf("%s was not exported", name)
			continue
		}
		values := attributeValues(after.Attributes)

		// What stays: the name, the ids, the status and the timing.
		for key, want := range attributeValues(before.Attributes) {
			switch key {
			case adkToolNameKey, adkOperationKey, adkToolCallIDKey, adkEventIDKey:
				if values[key] != want {
					t.Errorf("%s lost %s: %q, want %q", name, key, values[key], want)
				}
			}
		}
		if values[adkToolNameKey] != call.name {
			t.Errorf("%s names its tool %q, want %q", name, values[adkToolNameKey], call.name)
		}
		if after.Status.Code != before.Status.Code {
			t.Errorf("%s has status %v, want %v", name, after.Status.Code, before.Status.Code)
		}
		if !after.StartTime.Equal(before.StartTime) || !after.EndTime.Equal(before.EndTime) ||
			!after.SpanContext.Equal(before.SpanContext) || !after.Parent.Equal(before.Parent) || after.SpanKind != before.SpanKind {
			t.Errorf("%s changed in timing or identity: %+v, was %+v", name, after, before)
		}

		// What goes: the arguments, the response and whatever else the
		// allowlist does not name, each with its size where it has one.
		for _, key := range []attribute.Key{adkToolArgsKey, adkToolResponseKey, adkToolDescriptionKey} {
			if values[key] != redacted {
				t.Errorf("%s carries %s = %q, want it redacted", name, key, values[key])
			}
		}
		if values[toolArgsSizeKey] != fmt.Sprint(len(call.args)) || values[toolResponseSizeKey] != fmt.Sprint(len(call.response)) {
			t.Errorf("%s sizes = %q and %q, want %d and %d", name,
				values[toolArgsSizeKey], values[toolResponseSizeKey], len(call.args), len(call.response))
		}
		for _, text := range everyText(after) {
			for _, secret := range call.secrets {
				if strings.Contains(text, secret) {
					t.Errorf("%s carries %q, which holds %q", name, text, secret)
				}
			}
		}

		// A failed call keeps that it failed, and the kind of error, and
		// loses what the error said.
		if call.err == nil {
			continue
		}
		if after.Status.Code != codes.Error || after.Status.Description != redacted {
			t.Errorf("%s status = %+v, want an error with its message redacted", name, after.Status)
		}
		if len(after.Events) != 1 || after.Events[0].Name != "exception" {
			t.Fatalf("%s events = %+v, want the exception", name, after.Events)
		}
		event := attributeValues(after.Events[0].Attributes)
		if event[exceptionTypeKey] != "*errors.errorString" || event["exception.message"] != redacted {
			t.Errorf("%s exception = %v, want its type kept and its message redacted", name, event)
		}
	}
}

// TestASpanThatIsNoToolCallGoesOutAsItIs: the redaction of a tool call is for
// tool calls. A request's server span, an agent's and a model's go out with
// everything they carry, errors included, and a URL with no query or fragment
// in it as it is.
func TestASpanThatIsNoToolCallGoesOutAsItIs(t *testing.T) {
	failure := errors.New("the model refused: quota")
	plain, exported := exportBoth(t, func(tracer trace.Tracer) {
		_, request := tracer.Start(context.Background(), "GET /v1/organizations/{organizationID}/corpora",
			trace.WithSpanKind(trace.SpanKindServer),
			trace.WithAttributes(attribute.String("http.route", "/v1/organizations/{organizationID}/corpora"),
				attribute.String("url.path", "/v1/organizations/org_1/corpora"),
				attribute.String("url.full", "https://teams.internal:8443/v1/organizations/org_1/members")))
		request.End()
		_, agent := tracer.Start(context.Background(), "invoke_agent krude", trace.WithAttributes(
			attribute.String("gen_ai.operation.name", "invoke_agent"), attribute.String("gen_ai.agent.name", "krude")))
		agent.End()
		_, model := tracer.Start(context.Background(), "generate_content gemini", trace.WithAttributes(
			attribute.String("gen_ai.operation.name", "generate_content"), attribute.String("gen_ai.request.model", "gemini")))
		model.RecordError(failure)
		model.SetStatus(codes.Error, failure.Error())
		model.SetAttributes(attribute.Int("gen_ai.usage.input_tokens", 1200))
		model.End()
	})
	for name, before := range plain {
		after := exported[name]
		if !reflect.DeepEqual(attributeValues(after.Attributes), attributeValues(before.Attributes)) {
			t.Errorf("%s attributes = %v, want %v", name, after.Attributes, before.Attributes)
		}
		if after.Status != before.Status {
			t.Errorf("%s status = %+v, want %+v", name, after.Status, before.Status)
		}
		if len(after.Events) != len(before.Events) {
			t.Fatalf("%s events = %+v, want %+v", name, after.Events, before.Events)
		}
		for index := range before.Events {
			if !reflect.DeepEqual(attributeValues(after.Events[index].Attributes), attributeValues(before.Events[index].Attributes)) {
				t.Errorf("%s event = %+v, want %+v", name, after.Events[index], before.Events[index])
			}
		}
	}
}

// TestAToolSpanIsKnownByWhatItCarries: ADK's span name is not the only sign of
// a tool call. A span that names the operation, or carries a tool's name, its
// arguments or its response, is a tool span whatever it is called, and a span
// named for one is a tool span whatever it carries.
func TestAToolSpanIsKnownByWhatItCarries(t *testing.T) {
	note := attribute.String("note", "Priya's salary is 210k")
	for name, test := range map[string]struct {
		span       string
		attributes []attribute.KeyValue
		redacted   bool
	}{
		"named for one":            {span: "execute_tool anything", attributes: []attribute.KeyValue{note}, redacted: true},
		"named for the merged one": {span: "execute_tool (merged)", attributes: []attribute.KeyValue{note}, redacted: true},
		"naming the operation":     {span: "call", attributes: []attribute.KeyValue{adkOperationKey.String(toolOperation), note}, redacted: true},
		"carrying a tool's name":   {span: "call", attributes: []attribute.KeyValue{adkToolNameKey.String("x"), note}, redacted: true},
		"carrying arguments":       {span: "call", attributes: []attribute.KeyValue{adkToolArgsKey.String("{}"), note}, redacted: true},
		"carrying a response":      {span: "call", attributes: []attribute.KeyValue{adkToolResponseKey.String("{}"), note}, redacted: true},
		"an agent's":               {span: "invoke_agent krude", attributes: []attribute.KeyValue{adkOperationKey.String("invoke_agent"), note}},
		"a model's":                {span: "generate_content gemini", attributes: []attribute.KeyValue{adkOperationKey.String("generate_content"), note}},
		"a plain one":              {span: "process events", attributes: []attribute.KeyValue{note}},
	} {
		t.Run(name, func(t *testing.T) {
			_, exported := exportBoth(t, func(tracer trace.Tracer) {
				_, span := tracer.Start(context.Background(), test.span, trace.WithAttributes(test.attributes...))
				span.End()
			})
			got := attributeValues(exported[test.span].Attributes)["note"]
			if want := note.Value.Emit(); test.redacted {
				if got != redacted {
					t.Errorf("note = %q, want it redacted", got)
				}
			} else if got != want {
				t.Errorf("note = %q, want %q", got, want)
			}
		})
	}
}

// TestOnlyTheAllowlistIsKeptOnAToolSpan: an attribute nobody has said is safe
// is redacted, whatever its type, and so is one on an event of the span. The
// names and ids, and the kind of an error, are the ones kept.
func TestOnlyTheAllowlistIsKeptOnAToolSpan(t *testing.T) {
	_, exported := exportBoth(t, func(tracer trace.Tracer) {
		_, span := tracer.Start(context.Background(), "execute_tool browser_snapshot", trace.WithAttributes(
			adkOperationKey.String(toolOperation), adkToolNameKey.String("browser_snapshot"),
			attribute.String("a.future.adk.attribute", "Priya's salary is 210k"),
			attribute.Int("a.number", 210000),
			attribute.StringSlice("a.list", []string{"210k"}),
			attribute.Bool("a.flag", true),
			urlFullKey.String("https://x/v1/directory/people?q=raya+secret")))
		span.AddEvent("progress", trace.WithAttributes(
			attribute.String("exception.type", "kind"), attribute.String("detail", "Priya's salary is 210k")))
		span.End()
	})
	stub := exported["execute_tool browser_snapshot"]
	values := attributeValues(stub.Attributes)
	if values[adkToolNameKey] != "browser_snapshot" || values[adkOperationKey] != toolOperation {
		t.Errorf("attributes = %v, want the name and the operation kept", values)
	}
	for _, key := range []attribute.Key{"a.future.adk.attribute", "a.number", "a.list", "a.flag", urlFullKey} {
		if values[key] != redacted {
			t.Errorf("%s = %q, want it redacted", key, values[key])
		}
	}
	if len(stub.Events) != 1 {
		t.Fatalf("events = %+v", stub.Events)
	}
	event := attributeValues(stub.Events[0].Attributes)
	if stub.Events[0].Name != "progress" || event["exception.type"] != "kind" || event["detail"] != redacted {
		t.Errorf("event = %s %v, want its type kept and its detail redacted", stub.Events[0].Name, event)
	}
	for _, text := range everyText(stub) {
		if strings.Contains(text, "210") {
			t.Errorf("the span carries %q", text)
		}
	}
}

// TestRedactionLeavesTheSpansItWasGiven: the exporter is handed the process's
// own spans, and redacting must not rewrite what another exporter, or a later
// export, would read from them.
func TestRedactionLeavesTheSpansItWasGiven(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	toolCall{
		name: "knowledge_search", args: `{"query":"layoffs"}`, response: `{"text":"Priya's salary"}`,
		err: errors.New("no corpus called hr-private"),
	}.trace(provider.Tracer("test"))
	ended := recorder.Ended()
	if len(ended) != 1 {
		t.Fatalf("recorded %d spans", len(ended))
	}
	exported := tracetest.NewInMemoryExporter()
	if err := RedactToolCalls(exported).ExportSpans(context.Background(), []sdktrace.ReadOnlySpan{ended[0]}); err != nil {
		t.Fatal(err)
	}
	if got := exported.GetSpans(); len(got) != 1 || attributeValues(got[0].Attributes)[adkToolArgsKey] != redacted {
		t.Fatalf("exported = %+v, want it redacted", got)
	}
	if got := attributeValues(ended[0].Attributes())[adkToolArgsKey]; got != `{"query":"layoffs"}` {
		t.Errorf("the recorded span's arguments = %q, want them untouched", got)
	}
	if got := ended[0].Status().Description; got != "no corpus called hr-private" {
		t.Errorf("the recorded span's status = %q, want it untouched", got)
	}
	if got := attributeValues(ended[0].Events()[0].Attributes)["exception.message"]; got != "no corpus called hr-private" {
		t.Errorf("the recorded span's exception = %q, want it untouched", got)
	}
}

// endedSpan is a span as the SDK hands it to an exporter once it has ended.
func endedSpan(t *testing.T, name string, record func(span trace.Span)) sdktrace.ReadOnlySpan {
	t.Helper()
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	_, span := provider.Tracer("test").Start(context.Background(), name, trace.WithSpanKind(trace.SpanKindClient))
	record(span)
	span.End()
	ended := recorder.Ended()
	if len(ended) != 1 {
		t.Fatalf("recorded %d spans", len(ended))
	}
	return ended[0]
}

// TestAURLKeepsItsSchemeHostAndPathAndLosesTheRest: what somebody typed into a
// URL is its query, and a fragment and userinfo are as little for a trace.
// Where the request went is the rest, and stays. A value that is no URL is
// not trusted to hold no query, and goes whole.
func TestAURLKeepsItsSchemeHostAndPathAndLosesTheRest(t *testing.T) {
	for rawURL, want := range map[string]string{
		"https://x/v1/directory/people?q=raya+secret":      "https://x/v1/directory/people?[redacted]",
		"https://x/v1/directory/people?q=raya&limit=5":     "https://x/v1/directory/people?[redacted]",
		"https://x/v1/directory/people?":                   "https://x/v1/directory/people?[redacted]",
		"https://x/v1/directory/people?q=raya#raya-secret": "https://x/v1/directory/people?[redacted]",
		"https://x/v1/directory/people#raya-secret":        "https://x/v1/directory/people",
		"https://raya:secret@x:8443/a%2Fb/c?q=1":           "https://x:8443/a%2Fb/c?[redacted]",
		"https://raya:secret@x/v1/directory/people":        "https://x/v1/directory/people",
		"http://[2606:4700::1]:8080/a?b=c":                 "http://[2606:4700::1]:8080/a?[redacted]",
		"https://x?q=raya":                                 "https://x?[redacted]",
		"https://x/p?q=%zz":                                "https://x/p?[redacted]",
		"https://x/v1/directory/people?q=[redacted]":       "https://x/v1/directory/people?[redacted]",
		"/v1/directory/people?q=raya+secret":               "/v1/directory/people?[redacted]",
		"//x/v1/directory/people?q=raya":                   "//x/v1/directory/people?[redacted]",
		"https://x/v1/directory/people":                    "https://x/v1/directory/people",
		"https://x":                                        "https://x",
		"https://x:8443/a%2Fb/c":                           "https://x:8443/a%2Fb/c",
		"/v1/directory/people":                             "/v1/directory/people",
		"/":                                                "/",
		"":                                                 "",
		"raya secret":                                      "[redacted]",
		"raya?q=secret":                                    "[redacted]",
		"not a url":                                        "[redacted]",
		"%zz":                                              "[redacted]",
		"https://x/%zz?q=raya+secret":                      "[redacted]",
		"mailto:raya@example.com?subject=secret":           "[redacted]",
		"https://":                                         "[redacted]",
		"https:///v1/directory/people?q=raya":              "[redacted]",
		"x.internal:8443":                                  "[redacted]",
		"https://x/v1/directory/people?q=raya\nsecret":     "[redacted]",
	} {
		if got := redactURL(rawURL); got != want {
			t.Errorf("redactURL(%q) = %q, want %q", rawURL, got, want)
		}
	}
}

// TestNoQueryStringLeavesOnAnySpan: a client span's URL, in either of the
// names it goes by, a request target, and a query on its own, lose what was
// typed and keep the rest of the span. It does not take a tool call for this.
func TestNoQueryStringLeavesOnAnySpan(t *testing.T) {
	const people = "https://x/v1/directory/people"
	for name, test := range map[string]struct {
		attribute attribute.KeyValue
		want      string
	}{
		"a client span's URL":          {urlFullKey.String(people + "?q=raya+secret"), people + "?[redacted]"},
		"the older name for it":        {httpURLKey.String(people + "?q=raya+secret"), people + "?[redacted]"},
		"a request target":             {httpTargetKey.String("/v1/directory/people?q=raya+secret"), "/v1/directory/people?[redacted]"},
		"a query on its own":           {urlQueryKey.String("q=raya+secret"), "[redacted]"},
		"an empty query on its own":    {urlQueryKey.String(""), "[redacted]"},
		"a fragment":                   {urlFullKey.String(people + "#raya-secret"), people},
		"a query and a fragment":       {httpURLKey.String(people + "?q=raya#raya-secret"), people + "?[redacted]"},
		"userinfo":                     {urlFullKey.String("https://raya:secret@x/v1/directory/people"), people},
		"a value that is no URL":       {urlFullKey.String("raya secret"), "[redacted]"},
		"an older value that is none":  {httpURLKey.String("raya?q=secret"), "[redacted]"},
		"a value that is not a string": {urlFullKey.Int(200), "[redacted]"},
		"a URL with no query":          {urlFullKey.String(people), people},
		"a target with no query":       {httpTargetKey.String("/v1/directory/people"), "/v1/directory/people"},
	} {
		t.Run(name, func(t *testing.T) {
			plain, exported := exportBoth(t, func(tracer trace.Tracer) {
				_, span := tracer.Start(context.Background(), "HTTP GET x", trace.WithSpanKind(trace.SpanKindClient),
					trace.WithAttributes(test.attribute, attribute.String("http.request.method", "GET"), attribute.String("server.address", "x")))
				span.SetAttributes(attribute.Int("http.response.status_code", 200))
				span.End()
			})
			before, after := plain["HTTP GET x"], exported["HTTP GET x"]
			values := attributeValues(after.Attributes)
			if got := values[test.attribute.Key]; got != test.want {
				t.Errorf("%s = %q, want %q", test.attribute.Key, got, test.want)
			}
			for key, want := range attributeValues(before.Attributes) {
				if key != test.attribute.Key && values[key] != want {
					t.Errorf("%s = %q, want it untouched: %q", key, values[key], want)
				}
			}
			if len(after.Attributes) != len(before.Attributes) || after.Status != before.Status ||
				!after.StartTime.Equal(before.StartTime) || !after.EndTime.Equal(before.EndTime) ||
				!after.SpanContext.Equal(before.SpanContext) || after.SpanKind != before.SpanKind {
				t.Errorf("the span changed in more than the URL: %+v, was %+v", after, before)
			}
			for _, text := range everyText(after) {
				for _, secret := range []string{"raya", "secret"} {
					if strings.Contains(text, secret) {
						t.Errorf("the span carries %q, which holds %q", text, secret)
					}
				}
			}
		})
	}
}

// TestNoQueryStringLeavesOnAnEvent: an event on a span, a redirect say, carries
// a URL as a span does, and loses the same of it.
func TestNoQueryStringLeavesOnAnEvent(t *testing.T) {
	plain, exported := exportBoth(t, func(tracer trace.Tracer) {
		_, span := tracer.Start(context.Background(), "HTTP GET x", trace.WithSpanKind(trace.SpanKindClient))
		span.AddEvent("redirect", trace.WithAttributes(
			urlFullKey.String("https://x/v1/directory/people?q=raya+secret#raya-secret"),
			httpURLKey.String("https://x/v1/directory/teams?q=raya+secret"),
			urlQueryKey.String("q=raya+secret"),
			attribute.String("http.response.status_code", "302"),
			attribute.String("note", "kept")))
		span.AddEvent("retry", trace.WithAttributes(urlFullKey.String("https://x/v1/directory/agents")))
		span.End()
	})
	before, after := plain["HTTP GET x"], exported["HTTP GET x"]
	if len(after.Events) != 2 || after.Events[0].Name != "redirect" || after.Events[1].Name != "retry" {
		t.Fatalf("events = %+v, want the two the span had", after.Events)
	}
	redirect, retry := attributeValues(after.Events[0].Attributes), attributeValues(after.Events[1].Attributes)
	for key, want := range map[attribute.Key]string{
		urlFullKey: "https://x/v1/directory/people?[redacted]", httpURLKey: "https://x/v1/directory/teams?[redacted]",
		urlQueryKey: "[redacted]", "http.response.status_code": "302", "note": "kept",
	} {
		if redirect[key] != want {
			t.Errorf("the redirect's %s = %q, want %q", key, redirect[key], want)
		}
	}
	if retry[urlFullKey] != "https://x/v1/directory/agents" {
		t.Errorf("the retry's URL = %q, want it untouched", retry[urlFullKey])
	}
	if !after.Events[0].Time.Equal(before.Events[0].Time) || len(after.Events[0].Attributes) != len(before.Events[0].Attributes) {
		t.Errorf("the redirect changed in more than its URLs: %+v, was %+v", after.Events[0], before.Events[0])
	}
	for _, text := range everyText(after) {
		for _, secret := range []string{"raya", "secret"} {
			if strings.Contains(text, secret) {
				t.Errorf("the span carries %q, which holds %q", text, secret)
			}
		}
	}
}

// TestASpanWithNothingToLeaveOutIsTheSameSpan: the exporter hands on the span
// it was given, not a copy of it, unless a URL on it or on an event has a
// query or a fragment to lose.
func TestASpanWithNothingToLeaveOutIsTheSameSpan(t *testing.T) {
	sameSpan := func(got, want sdktrace.ReadOnlySpan) bool {
		_, copied := got.(redactedSpan)
		return !copied && got == want
	}
	for name, record := range map[string]func(span trace.Span){
		"no attributes": func(trace.Span) {},
		"attributes but no URL": func(span trace.Span) {
			span.SetAttributes(attribute.String("http.route", "/v1/x"), attribute.Int("http.response.status_code", 200))
		},
		"a URL with no query": func(span trace.Span) {
			span.SetAttributes(urlFullKey.String("https://x/v1/directory/people"), httpTargetKey.String("/v1/directory/people"))
		},
		"a URL the redaction already left": func(span trace.Span) {
			span.SetAttributes(urlFullKey.String("https://x/v1/directory/people?[redacted]"), urlQueryKey.String("[redacted]"))
		},
		"an event with a URL with no query": func(span trace.Span) {
			span.AddEvent("redirect", trace.WithAttributes(urlFullKey.String("https://x/v1/directory/people")))
		},
		"an error and its event": func(span trace.Span) {
			span.RecordError(errors.New("the model refused: quota"))
			span.SetStatus(codes.Error, "the model refused: quota")
		},
	} {
		span := endedSpan(t, "HTTP GET x", record)
		if got := redactSpan(span); !sameSpan(got, span) {
			t.Errorf("%s: got a copy of the span, want the span itself", name)
		}
	}
	for name, record := range map[string]func(span trace.Span){
		"a query on the span": func(span trace.Span) { span.SetAttributes(urlFullKey.String("https://x/p?q=1")) },
		"a fragment":          func(span trace.Span) { span.SetAttributes(httpURLKey.String("https://x/p#top")) },
		"a query on its own":  func(span trace.Span) { span.SetAttributes(urlQueryKey.String("q=1")) },
		"a query on an event": func(span trace.Span) {
			span.AddEvent("redirect", trace.WithAttributes(urlFullKey.String("https://x/p?q=1")))
		},
		"a URL that is no URL": func(span trace.Span) { span.SetAttributes(urlFullKey.String("raya secret")) },
	} {
		span := endedSpan(t, "HTTP GET x", record)
		if got := redactSpan(span); sameSpan(got, span) {
			t.Errorf("%s: got the span itself, want it redacted", name)
		}
	}
}

// TestNoQueryStringOfAnHTTPCallReachesATrace: the case the redaction is for, as
// the process makes it. A caller asks a service's directory for a member by
// name through Transport, the service answers through HTTPHandler, and
// instrumentation writes both spans. The name is in neither, or in an event,
// while where the call went, and what came of it, still are.
func TestNoQueryStringOfAnHTTPCallReachesATrace(t *testing.T) {
	exported := tracetest.NewInMemoryExporter()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSampler(sdktrace.AlwaysSample()), sdktrace.WithSyncer(RedactToolCalls(exported)))
	previous := otel.GetTracerProvider()
	otel.SetTracerProvider(provider)
	t.Cleanup(func() {
		_ = provider.Shutdown(context.Background())
		otel.SetTracerProvider(previous)
	})

	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/directory/people", func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusNoContent)
	})
	directory := httptest.NewServer(HTTPHandler("oryxa-teams", mux))
	t.Cleanup(directory.Close)

	client := &http.Client{Transport: Transport(nil)}
	response, err := client.Get(directory.URL + "/v1/directory/people?q=raya+secret#raya-secret")
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if err := provider.ForceFlush(context.Background()); err != nil {
		t.Fatal(err)
	}

	var clientSpan *tracetest.SpanStub
	spans := exported.GetSpans()
	for index := range spans {
		if spans[index].SpanKind == trace.SpanKindClient {
			clientSpan = &spans[index]
		}
		for _, text := range everyText(spans[index]) {
			for _, secret := range []string{"raya", "secret"} {
				if strings.Contains(text, secret) {
					t.Errorf("%s carries %q, which holds %q", spans[index].Name, text, secret)
				}
			}
		}
	}
	if len(spans) < 2 || clientSpan == nil {
		t.Fatalf("exported %d spans, want the client's and the server's", len(spans))
	}
	values := attributeValues(clientSpan.Attributes)
	if want := directory.URL + "/v1/directory/people?[redacted]"; values[urlFullKey] != want {
		t.Errorf("the client span's URL = %q, want %q", values[urlFullKey], want)
	}
	if values["http.response.status_code"] != "204" || values["http.request.method"] != "GET" {
		t.Errorf("the client span = %v, want the call's method and status kept", values)
	}
}
