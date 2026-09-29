package integrationtest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/Shubham04-oss/oryxa-go/apps/internal/integration"
)

// Conformance says whether a toolkit is fit to be enabled, which is gate G1
// beside cmd/check. It runs over testdata, laid out as:
//
//	testdata/cassettes/<READER>.targets.json      what the reader lists
//	testdata/cassettes/<READER>.page1.json        the reader's first page
//	testdata/cassettes/<READER>.page2.json        and the one its cursor reaches
//	testdata/cassettes/<TOOL>.ok.json            the tool's call, as the
//	                                            provider documents it
//	testdata/cassettes/<TOOL>.app_error.json    the app refusing, in its words
//	testdata/cassettes/<TOOL>.unauthorized.json a 401
//	testdata/cassettes/<TOOL>.rate_limited.json a 429
//	testdata/cassettes/<SLUG>.first.json         a polled event's first poll
//	testdata/hooks/<SLUG>/<name>.json            a verified push delivery
//
// A cassette may name the arguments its tool is run with, which is how a tool
// that needs a real repository id is exercised:
//
//	{"arguments": {"repo": "acme/api"}, "interactions": [...]}
//
// Without one, the tool is run with the example its own input_schema builds.
func Conformance(t *testing.T, i integration.Integration, testdata string) {
	t.Helper()
	for _, problem := range Check(i, testdata) {
		t.Error(problem)
	}
}

// Check is what Conformance reports: every rule a toolkit breaks, in one
// pass, so a worker is told everything that is wrong rather than the first
// thing. A rule that cannot be judged says so instead of failing, which is
// what a reviewer's time is for.
func Check(i integration.Integration, testdata string) []string {
	caught := &collector{}
	manifest := i.Manifest()
	steps := []struct {
		what string
		run  func(reporter)
	}{
		{"manifest", func(t reporter) { checkManifest(t, i, manifest) }},
		{"tools", func(t reporter) { checkTools(t, i, manifest, testdata) }},
		{"readers", func(t reporter) { checkReaders(t, i, manifest, testdata) }},
		{"polls", func(t reporter) { checkPolls(t, i, manifest, testdata) }},
		{"pushes", func(t reporter) { checkPushes(t, i, manifest, testdata) }},
	}
	var problems []string
	for _, step := range steps {
		problems = append(problems, guard(caught, step.what, step.run)...)
	}
	// A stale cassette is only visible once everything that used it is over.
	caught.run()
	return append(problems, caught.found...)
}

// guard runs one step and keeps what it said, even when it could not go on:
// a rule that cannot be judged is one problem, not the loss of the rest.
func guard(c *collector, what string, run func(reporter)) (problems []string) {
	before := len(c.found)
	defer func() {
		if fallen := recover(); fallen != nil {
			if stopped, ok := fallen.(broken); ok {
				problems = append(problems, stopped.message)
				return
			}
			panic(fallen)
		}
		problems = append(problems, c.found[before:]...)
		c.found = c.found[:before]
	}()
	run(c)
	return nil
}

// checkManifest is the manifest itself: it validates, every name it carries is
// prefixed with the toolkit the way Composio spells one, and every sample it
// publishes matches its own payload schema, which is the contract every
// consumer of apps.trigger.received.v1 reads.
func checkManifest(t reporter, i integration.Integration, manifest integration.Manifest) {
	t.Helper()
	if err := manifest.Validate(); err != nil {
		t.Errorf("the manifest does not validate: %v", err)
		return
	}
	functions := i.Tools()
	prefix := strings.ToUpper(manifest.Toolkit) + "_"
	for _, tool := range manifest.Tools {
		if !strings.HasPrefix(tool.Name, prefix) {
			t.Errorf("the tool %s does not carry the toolkit's own %s prefix", tool.Name, prefix)
		}
		if functions[tool.Name] == nil {
			t.Errorf("the tool %s has no function", tool.Name)
		}
	}
	for _, name := range sortedNames(functions) {
		if !slices.ContainsFunc(manifest.Tools, func(tool integration.ToolSpec) bool { return tool.Name == name }) {
			t.Errorf("a function is registered for %s, which the manifest does not declare", name)
		}
	}
	for _, trigger := range manifest.Triggers {
		if len(trigger.Sample) == 0 {
			continue
		}
		if problem := integration.Check(trigger.PayloadSchema, trigger.Sample); problem != nil {
			t.Errorf("the sample for %s does not match its own payload schema: %s",
				trigger.Slug, integration.CheckDescription(problem))
		}
	}
}

// checkTools runs each declared tool over the four cassettes every one of
// them must have, and checks that a read tool's cassettes only ever read.
func checkTools(t reporter, i integration.Integration, manifest integration.Manifest, testdata string) {
	t.Helper()
	functions := i.Tools()
	for _, tool := range manifest.Tools {
		run := functions[tool.Name]
		if run == nil {
			// checkManifest has already said the tool has no function.
			continue
		}
		// A read tool's own cassettes are the first line of defence against
		// a tool marked read-only that changes something: the session would
		// refuse the call, but a cassette recording a write says the code
		// tried.
		checkReadPosts(t, tool, testdata)
		checkToolOK(t, tool, run, testdata)
		checkToolRefusals(t, tool, run, testdata)
	}
}

// checkToolOK runs a tool over its ok cassette, which is built from the
// provider's own documented answer.
func checkToolOK(t reporter, tool integration.ToolSpec, run integration.ToolFunc, testdata string) {
	t.Helper()
	record, arguments := cassetteFor(t, tool.InputSchema, testdata, tool.Name+".ok.json")
	if record == nil {
		t.Errorf("the tool %s has no ok cassette", tool.Name)
		return
	}
	result, err := run(context.Background(), Session(t, record.Name, nil), arguments)
	if err != nil {
		t.Errorf("the tool %s failed where its cassette says it succeeds: %v", tool.Name, err)
		return
	}
	if !json.Valid(result) {
		t.Errorf("the tool %s answered %q, which is not JSON", tool.Name, truncate(string(result)))
	}
	leaked(t, "the result of "+tool.Name, string(result))
}

// checkToolRefusals checks the three ways an app says no, which are the three
// the core acts on differently: a refusal in its own words, a refused
// credential, and the app's own pace.
func checkToolRefusals(t reporter, tool integration.ToolSpec, run integration.ToolFunc, testdata string) {
	t.Helper()
	record, arguments := cassetteFor(t, tool.InputSchema, testdata, tool.Name+".app_error.json")
	if record == nil {
		t.Errorf("the tool %s has no app_error cassette", tool.Name)
		return
	}
	_, err := run(context.Background(), Session(t, record.Name, nil), arguments)
	var refused *integration.AppError
	switch {
	case errors.As(err, &refused):
		if strings.TrimSpace(refused.Message) == "" {
			t.Errorf("the app refused %s with no reason, and the reason is what the caller reads to try again", tool.Name)
		}
	default:
		t.Errorf("the app refused %s and the tool answered %v, want an *integration.AppError", tool.Name, err)
	}
	leaked(t, "the error from "+tool.Name, err)

	checkToolStatus(t, tool, run, testdata, tool.Name+".unauthorized.json",
		func(err error) bool { return errors.Is(err, integration.ErrNeedsReconnect) },
		"ErrNeedsReconnect")
	checkToolStatus(t, tool, run, testdata, tool.Name+".rate_limited.json",
		func(err error) bool {
			var limited *integration.RateLimited
			return errors.As(err, &limited)
		}, "RateLimited")
}

// checkToolStatus runs a tool over a cassette that answers a status, and
// checks it became the sentinel the core acts on rather than a refusal.
func checkToolStatus(t reporter, tool integration.ToolSpec, run integration.ToolFunc, testdata, name string, wants func(error) bool, said string) {
	t.Helper()
	record, arguments := cassetteFor(t, tool.InputSchema, testdata, name)
	if record == nil {
		t.Errorf("the tool %s has no %s cassette", tool.Name, name)
		return
	}
	_, err := run(context.Background(), Session(t, record.Name, nil), arguments)
	switch {
	case err == nil:
		t.Errorf("%s says the app refused, and the tool %s answered nothing", name, tool.Name)
	case !wants(err):
		t.Errorf("%s made the tool %s answer %v, want %s", name, tool.Name, err, said)
	}
	leaked(t, "the error from "+tool.Name, err)
}

// checkReadPosts is the read boundary, checked against the transcripts rather
// than against the code: a read tool whose cassettes record a POST to
// anything but its own read_posts is a tool that changes something and is
// marked as one that does not.
func checkReadPosts(t reporter, tool integration.ToolSpec, testdata string) {
	t.Helper()
	if !tool.ReadOnly {
		return
	}
	for _, name := range []string{
		tool.Name + ".ok.json", tool.Name + ".app_error.json",
		tool.Name + ".unauthorized.json", tool.Name + ".rate_limited.json",
	} {
		path := cassettePath(testdata, name)
		if _, err := os.Stat(path); err != nil {
			continue
		}
		for _, interaction := range load(t, path).Interactions {
			request := interaction.Request
			if request.Method == "" {
				continue
			}
			switch strings.ToUpper(request.Method) {
			case http.MethodGet, http.MethodHead:
				continue
			case http.MethodPost:
				if !slices.Contains(tool.ReadPOSTs, readPostPath(request.URL)) {
					t.Errorf("%s records POST %s, which is not one of %s's read_posts", name,
						readPostPath(request.URL), tool.Name)
				}
				continue
			default:
				t.Errorf("%s records %s %s, and a read tool may only GET, HEAD or its own read_posts",
					name, strings.ToUpper(request.Method), request.URL)
			}
		}
	}
}

// readPostPath is a recorded URL as a read_post is written: a path under
// api_base, with no query.
func readPostPath(raw string) string {
	parsed := mustParse(raw)
	path := parsed.EscapedPath()
	if index := strings.IndexByte(path, '?'); index >= 0 {
		path = path[:index]
	}
	return path
}

// checkReaders lists a target and drains two pages of it, then resumes from
// the middle, which are the two things a reader gets wrong: a cursor that
// does not carry the pass on, and a page that repeats the last one.
func checkReaders(t reporter, i integration.Integration, manifest integration.Manifest, testdata string) {
	t.Helper()
	reader, isReader := i.(integration.Reader)
	if !isReader {
		if len(manifest.Readers) > 0 {
			t.Errorf("the manifest declares %d readers and the integration implements none", len(manifest.Readers))
		}
		return
	}
	if len(manifest.Readers) == 0 {
		t.Errorf("the integration is a Reader and declares no readers")
		return
	}
	listed := read(t, cassettePath(testdata, declaredReader(manifest)+".targets.json"))
	if listed == nil {
		t.Errorf("a reader needs its targets cassette, %s.targets.json", declaredReader(manifest))
		return
	}
	targets, err := reader.Targets(context.Background(), Session(t, listed.Name, nil))
	if err != nil {
		t.Errorf("the reader %s cannot list its targets: %v", declaredReader(manifest), err)
		return
	}
	if len(targets) == 0 {
		t.Errorf("the reader %s lists nothing, so a picker would have nothing to choose from", declaredReader(manifest))
		return
	}
	for _, target := range targets {
		if target.Kind == "" || target.ID == "" {
			t.Errorf("the target %+v names no kind or no id, and a source is registered by both", target)
			continue
		}
		if problem := integration.Check(specSchema(manifest, target.Kind), target.Target); problem != nil {
			t.Errorf("the target %s does not match the target schema its reader declares: %s",
				target.ID, integration.CheckDescription(problem))
		}
	}
	for _, spec := range manifest.Readers {
		checkReaderPages(t, reader, manifest, spec, testdata, targets[0])
	}
}

func checkReaderPages(t reporter, reader integration.Reader, manifest integration.Manifest, spec integration.ReaderSpec, testdata string, target integration.Target) {
	t.Helper()
	first := read(t, cassettePath(testdata, spec.Name+".page1.json"))
	second := read(t, cassettePath(testdata, spec.Name+".page2.json"))
	if first == nil || second == nil {
		t.Errorf("the reader %s needs two cassettes: %s.page1.json and %s.page2.json", spec.Name, spec.Name, spec.Name)
		return
	}
	request := integration.PageRequest{
		// A page request always names its reader, and the reader's own kinds
		// name the fields it reads by.
		Target: map[string]string{integration.TargetReader: spec.Name},
		Limit:  50,
	}
	for kind, value := range targetFields(spec, target) {
		request.Target[kind] = value
	}
	page, err := reader.Read(context.Background(), Session(t, first.Name, nil), request)
	if err != nil {
		t.Errorf("the reader %s cannot answer its first page: %v", spec.Name, err)
		return
	}
	switch {
	case page.Cursor == "":
		t.Errorf("the first page of %s carries no cursor, so a second page can never be reached", spec.Name)
		return
	case len(page.Items) == 0:
		t.Errorf("the first page of %s carries no items", spec.Name)
		return
	}
	// The second cassette again, from the cursor the first page answered. A
	// reader that cannot resume fails here rather than in a member's
	// indexing pass.
	resumed, err := reader.Read(context.Background(), Session(t, second.Name, nil),
		integration.PageRequest{Target: request.Target, Cursor: page.Cursor, Limit: request.Limit})
	if err != nil {
		t.Errorf("the reader %s cannot resume from %q: %v", spec.Name, truncate(page.Cursor), err)
		return
	}
	if resumed.Cursor != "" {
		t.Errorf("the second page of %s still carries the cursor %q, so a pass would never end",
			spec.Name, truncate(resumed.Cursor))
	}
	if len(resumed.Items) == 0 {
		t.Errorf("the second page of %s carries no items, so the cursor carried the pass to nothing", spec.Name)
	}
	if ids(page) == ids(resumed) {
		t.Errorf("both pages of %s carry the same items, so a pass would index them twice", spec.Name)
	}
	checkItems(t, spec.Name, append(append([]integration.Item{}, page.Items...), resumed.Items...))
}

// checkItems is what knowledge needs of every item it is given, and what a
// page must fit inside.
func checkItems(t reporter, reader string, items []integration.Item) {
	t.Helper()
	total := 0
	for _, item := range items {
		if item.ExternalID == "" || item.Kind == "" {
			t.Errorf("the item %+v the reader %s yielded names no external id or no kind, and a source is registered by both", item, reader)
		}
		leaked(t, "the item the reader "+reader+" yielded", item.ExternalID, string(item.Content))
		total += len(item.Content)
	}
	if total > integration.MaxPageBytes {
		t.Errorf("the reader %s answered %d bytes of content, over the %d a page can carry",
			reader, total, integration.MaxPageBytes)
	}
}

// targetFields is the target a page request carries: the reader's own kinds
// name the fields it reads by, and their values come from the first thing the
// reader listed.
func targetFields(spec integration.ReaderSpec, target integration.Target) map[string]string {
	fields := map[string]string{}
	for _, kind := range spec.Kinds {
		fields[kind] = target.ID
	}
	if len(target.Target) > 0 {
		var carried map[string]any
		if json.Unmarshal(target.Target, &carried) == nil {
			for name, value := range carried {
				if text, ok := value.(string); ok {
					fields[name] = text
				}
			}
		}
	}
	return fields
}

// checkPolls checks the contract a poll has before it is given a member: the
// first poll with no cursor answers no events and a cursor, so nobody is
// handed an app's whole history the moment they subscribe.
func checkPolls(t reporter, i integration.Integration, manifest integration.Manifest, testdata string) {
	t.Helper()
	poller, isPoller := i.(integration.Poller)
	polled := triggersOf(manifest, integration.TriggerPoll)
	if !isPoller {
		if len(polled) > 0 {
			t.Errorf("the manifest declares %d polled events and the integration implements no Poller", len(polled))
		}
		return
	}
	for _, trigger := range polled {
		record := read(t, cassettePath(testdata, trigger.Slug+".first.json"))
		if record == nil {
			t.Errorf("the polled event %s needs its first-poll cassette, %s.first.json", trigger.Slug, trigger.Slug)
			continue
		}
		batch, err := poller.Poll(context.Background(), Session(t, record.Name, nil),
			trigger.Slug, json.RawMessage(`{}`), "")
		if err != nil {
			t.Errorf("the first poll of %s: %v", trigger.Slug, err)
			continue
		}
		if len(batch.Events) != 0 {
			t.Errorf("the first poll of %s answered %d events; it must answer none, or a new subscriber is handed the app's whole history",
				trigger.Slug, len(batch.Events))
		}
		if strings.TrimSpace(batch.Cursor) == "" {
			t.Errorf("the first poll of %s answers no cursor, so the next one starts from nowhere", trigger.Slug)
		}
	}
}

// checkPushes reads a verified delivery per pushed event and checks that what
// the integration parses validates against the event's own payload schema,
// which is the contract every consumer reads.
func checkPushes(t reporter, i integration.Integration, manifest integration.Manifest, testdata string) {
	t.Helper()
	pusher, isPusher := i.(integration.Pusher)
	pushed := triggersOf(manifest, integration.TriggerPush)
	if !isPusher {
		if len(pushed) > 0 {
			t.Errorf("the manifest declares %d pushed events and the integration implements no Pusher", len(pushed))
		}
		return
	}
	for _, trigger := range pushed {
		deliveries := deliveryFiles(testdata, trigger.Slug)
		if len(deliveries) == 0 {
			t.Errorf("the pushed event %s needs at least one delivery under testdata/hooks/%s/", trigger.Slug, trigger.Slug)
			continue
		}
		for _, delivery := range deliveries {
			checkDelivery(t, pusher, trigger, delivery)
		}
	}
}

func checkDelivery(t reporter, pusher integration.Pusher, trigger integration.TriggerSpec, path string) {
	t.Helper()
	name := filepath.Base(path)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Errorf("the delivery %s could not be read: %v", name, err)
		return
	}
	events, err := pusher.Parse(context.Background(), deliveryHeaders(path), raw)
	if err != nil {
		t.Errorf("the delivery %s could not be parsed: %v", name, err)
		return
	}
	if len(events) == 0 {
		t.Errorf("the delivery %s yields no events, so nothing would ever be delivered", name)
	}
	for _, event := range events {
		if event.Trigger != trigger.Slug {
			t.Errorf("the delivery %s yields an event for %s, which the manifest does not push", name, event.Trigger)
		}
		if event.ID == "" {
			t.Errorf("the delivery %s yields an event with no id, so a redelivery would be delivered twice", name)
		}
		if event.OccurredAt.IsZero() {
			t.Errorf("the delivery %s yields an event with no time", name)
		}
		if problem := integration.Check(trigger.PayloadSchema, event.Data); problem != nil {
			t.Errorf("the delivery %s yields an event whose data does not match %s: %s", name, trigger.Slug,
				integration.CheckDescription(problem))
		}
		if len(event.Data) > maxPayload {
			t.Errorf("the delivery %s yields %d bytes of payload, over the %d an event carries", name, len(event.Data), maxPayload)
		}
		leaked(t, "the event from "+name, string(event.Data))
	}
}

// maxPayload is what an event carries: over it the payload is left out and
// the event says so, so an integration that always fills it is answering
// something no consumer receives.
const maxPayload = 128 << 10

// deliveryHeaders reads the headers a recorded delivery would have arrived
// with, from a file beside it, so a delivery that cannot be read without them
// is recorded whole. The core has already verified whatever it names: Parse
// never sees a signature, and the Authorization and Cookie headers are gone
// before it is called.
func deliveryHeaders(path string) http.Header {
	raw, err := os.ReadFile(path + ".headers.json")
	if err != nil {
		return http.Header{}
	}
	var headers map[string][]string
	if json.Unmarshal(raw, &headers) != nil {
		return http.Header{}
	}
	return http.Header(headers)
}

func deliveryFiles(testdata, slug string) []string {
	entries, err := os.ReadDir(filepath.Join(testdata, "hooks", slug))
	if err != nil {
		return nil
	}
	var files []string
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") ||
			strings.HasSuffix(entry.Name(), ".headers.json") {
			continue
		}
		files = append(files, filepath.Join(testdata, "hooks", slug, entry.Name()))
	}
	slices.Sort(files)
	return files
}

func triggersOf(manifest integration.Manifest, kind string) []integration.TriggerSpec {
	var triggers []integration.TriggerSpec
	for _, trigger := range manifest.Triggers {
		if trigger.Kind == kind {
			triggers = append(triggers, trigger)
		}
	}
	return triggers
}

// cassetteFor is a cassette and the arguments its tool is run with: the ones
// it names, or the example its own schema builds.
func cassetteFor(t reporter, schema json.RawMessage, testdata, name string) (*recording, json.RawMessage) {
	t.Helper()
	record := read(t, cassettePath(testdata, name))
	if record == nil {
		return nil, nil
	}
	if len(record.Arguments) > 0 {
		return record, record.Arguments
	}
	return record, integration.Example(schema)
}

// cassettePath is where one cassette is.
func cassettePath(testdata, name string) string {
	return filepath.Join(testdata, "cassettes", name)
}

// read is a cassette that may not be there, which is a fact the caller
// reports rather than a failure of its own.
func read(t reporter, path string) *recording {
	t.Helper()
	if _, err := os.Stat(path); err != nil {
		return nil
	}
	return load(t, path)
}

// leaked fails a check when the fake token reaches anything a caller would log
// or show: the token is the only credential-shaped thing a cassette run has,
// and a leak of it is a leak of the shape a real one would take.
func leaked(t reporter, where string, values ...any) {
	t.Helper()
	for _, value := range values {
		if strings.Contains(fmt.Sprint(value), FakeToken) {
			t.Errorf("the fake token reached %s", where)
			return
		}
	}
}

// declaredReader is the name of the first declared reader, which is the
// cassette a listing is recorded against when a toolkit has one reader.
func declaredReader(manifest integration.Manifest) string {
	if len(manifest.Readers) == 0 {
		return "reader"
	}
	return manifest.Readers[0].Name
}

// specSchema is the target schema a reader declares for a kind, and nothing
// where there is none, so a check never has to decide.
func specSchema(manifest integration.Manifest, kind string) json.RawMessage {
	for _, reader := range manifest.Readers {
		if slices.Contains(reader.Kinds, kind) {
			return reader.TargetSchema
		}
	}
	return nil
}

func ids(page integration.Page) string {
	parts := make([]string, 0, len(page.Items))
	for _, item := range page.Items {
		parts = append(parts, item.ExternalID)
	}
	return strings.Join(parts, ",")
}

func sortedNames[V any](values map[string]V) []string {
	names := make([]string, 0, len(values))
	for name := range values {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}

func truncate(value string) string {
	const limit = 120
	if len(value) <= limit {
		return value
	}
	return value[:limit] + "…"
}
