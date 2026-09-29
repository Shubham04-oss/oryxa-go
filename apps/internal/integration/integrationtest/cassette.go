// Package integrationtest is the recorded-response harness every
// integration's own tests are written against: a Session that answers from
// cassettes rather than from a network, and Conformance, which says whether a
// toolkit is fit to be enabled.
//
// Nothing here reaches an app. A cassette is a transcript of what a provider's
// own documentation says it answers, so a worker needs no account and no key,
// and a test cannot be broken by a provider changing what it returns. The fake
// token the session carries is never recorded, and a check fails if it reaches
// a result, an error or a captured log line.
package integrationtest

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/Shubham04-oss/oryxa-go/apps/internal/integration"
)

// FakeToken is what the session's token answers. It is deliberately not a
// credential of any provider: a cassette recorded with it in place can never
// be one, and a check that finds it in a result or a log line has found a leak
// of the shape a real credential would take.
const FakeToken = "oryxa-fake-token-never-real"

// fallbackBase is what the session's client resolves a path against when the
// cassette records none: a host under .invalid, which by RFC 2606 can never
// resolve, so a request that escaped the cassette fails rather than reaching
// anything.
const fallbackBase = "https://api.example.invalid"

// reporter is what the harness says problems to. *testing.T is one; so is the
// collector Check uses, so that a test can read what the harness would have
// said about a toolkit that breaks it.
type reporter interface {
	Helper()
	Errorf(format string, args ...any)
	Fatalf(format string, args ...any)
	Cleanup(func())
}

// collector is a reporter that keeps what it is told, which is how a test
// checks that a broken toolkit is caught rather than only that a sound one
// passes.
type collector struct {
	helpers  int
	cleanups []func()
	found    []string
}

func (c *collector) Helper() { c.helpers++ }
func (c *collector) Errorf(format string, args ...any) {
	c.found = append(c.found, fmt.Sprintf(format, args...))
}

// Fatalf records and stops: the harness only calls it when it cannot go on,
// and a rule that cannot be judged is not a failure of the toolkit.
func (c *collector) Fatalf(format string, args ...any) {
	panic(broken{fmt.Sprintf(format, args...)})
}

func (c *collector) Cleanup(f func()) { c.cleanups = append(c.cleanups, f) }

// run runs everything collector was told to clean up, which is where a stale
// cassette is reported.
func (c *collector) run() {
	for i := len(c.cleanups) - 1; i >= 0; i-- {
		c.cleanups[i]()
	}
	c.cleanups = nil
}

// broken is what a Fatalf that cannot be judged carries, so the harness
// reports the rule rather than losing the toolkit's other problems.
type broken struct{ message string }

// Session is a session that answers from one cassette rather than from the
// app: a Session whose HTTP client replays the interactions the cassette
// records, matching on method, URL with its query sorted, and a JSON-equal
// body.
//
// cassette is a file, or a path under testdata/cassettes. An unmatched
// request fails, and so does an interaction nothing ever used, so a stale
// cassette is as loud as a missing one.
func Session(t reporter, cassette string, settings map[string]string) integration.Session {
	t.Helper()
	record := load(t, cassette)
	values := map[string]string{}
	for name, value := range settings {
		values[name] = value
	}
	// The account a connection is labelled with, which an integration reads
	// rather than has told to it.
	if _, ok := values[integration.SettingAccountLabel]; !ok {
		values[integration.SettingAccountLabel] = "oryxa"
	}
	if _, ok := values[integration.SettingAccountRef]; !ok {
		values[integration.SettingAccountRef] = "0"
	}
	router := &recorder{t: t, record: record, used: make([]int, len(record.Interactions))}
	// The app's own base is whatever the cassette says the app is at: a
	// transcript of an app's API records that app's URLs, and an integration
	// builds its calls from Session.BaseURL as it does in production.
	base := baseOf(record)
	// An interaction nothing asked for is a stale cassette rather than a
	// passing test: a transcript holding an exchange nothing reaches is a
	// record of a call the code no longer makes.
	t.Cleanup(func() {
		if unused := router.unused(); len(unused) > 0 {
			t.Errorf("cassette %s records %s, which nothing asked for", record.Name, strings.Join(unused, "; "))
		}
	})
	token := FakeToken
	return integration.Session{
		HTTP:     &http.Client{Transport: router},
		BaseURL:  base,
		Settings: values,
		Token:    func(context.Context) (string, error) { return token, nil },
	}
}

// baseOf is the app's own base URL as the first recorded absolute URL gives
// it, without its path: an integration appends to it, as it appends to
// Session.BaseURL in production. A cassette that records only paths has no
// base of its own, and the fallback stands in.
func baseOf(record *recording) string {
	for _, interaction := range record.Interactions {
		parsed := mustParse(interaction.Request.URL)
		if parsed.Scheme == "" || parsed.Host == "" {
			continue
		}
		return parsed.Scheme + "://" + parsed.Host
	}
	return fallbackBase
}

// cassette is one recorded exchange, as it stands in a cassette file.
type cassette struct {
	// Arguments is what the tool under test is run with. It is optional: a
	// tool whose schema needs nothing beyond a string is run with what the
	// schema's own example builds.
	Arguments json.RawMessage  `json:"arguments,omitempty"`
	Request   recordedRequest  `json:"request"`
	Response  recordedResponse `json:"response"`
}

type recordedRequest struct {
	Method string          `json:"method"`
	URL    string          `json:"url"`
	Body   json.RawMessage `json:"body,omitempty"`
}

type recordedResponse struct {
	Status  int                 `json:"status"`
	Headers map[string][]string `json:"headers,omitempty"`
	Body    json.RawMessage     `json:"body,omitempty"`
}

func (response recordedResponse) header() http.Header {
	header := http.Header{}
	for name, values := range response.Headers {
		for _, value := range values {
			header.Add(name, value)
		}
	}
	return header
}

// recording is one cassette file.
type recording struct {
	Name         string
	Arguments    json.RawMessage
	Interactions []cassette
}

// load reads a cassette, and refuses one that holds a credential: those are
// never recorded, so a cassette that has one is a mistake worth failing over
// rather than a fixture to keep.
func load(t reporter, name string) *recording {
	t.Helper()
	record := &recording{Name: name, Arguments: json.RawMessage(`{}`)}
	raw, err := os.ReadFile(resolve(t, name))
	if err != nil {
		t.Fatalf("cassette %s: %v", name, err)
	}
	if err := json.Unmarshal(raw, record); err != nil {
		t.Fatalf("cassette %s is not a cassette: %v", name, err)
	}
	if len(record.Interactions) == 0 {
		t.Fatalf("cassette %s records no interactions", name)
	}
	for i, interaction := range record.Interactions {
		if secret := recordedSecret(interaction); secret != "" {
			t.Errorf("cassette %s interaction %d records a %s, which is never recorded", name, i, secret)
		}
	}
	return record
}

// recordedSecret names what a cassette must never hold, or "" when it holds
// nothing of the kind.
func recordedSecret(interaction cassette) string {
	headers, _ := json.Marshal(interaction.Response.Headers)
	recorded := string(interaction.Request.Body) + string(interaction.Response.Body) + string(headers)
	for _, marker := range []struct{ name, prefix string }{
		{"credential header", "Authorization"}, {"cookie header", "Cookie"},
		{"bearer token", "Bearer "}, {"GitHub token", "ghp_"}, {"GitHub OAuth token", "gho_"},
		{"GitHub app token", "github_pat_"}, {"Slack token", "xox"}, {"Stripe key", "rk_live_"},
		{"Google key", "AIza"},
	} {
		if strings.Contains(recorded, marker.prefix) {
			return marker.name
		}
	}
	if strings.Contains(recorded, FakeToken) {
		return "the fake token"
	}
	return ""
}

// resolve is where a cassette is: as it is named, or under testdata.
func resolve(t reporter, name string) string {
	t.Helper()
	if filepath.IsAbs(name) {
		return name
	}
	for _, candidate := range []string{name, name + ".json", filepath.Join("testdata", "cassettes", name),
		filepath.Join("testdata", "cassettes", name+".json")} {
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
	}
	t.Fatalf("cassette %s is not there, under testdata/cassettes or anywhere else", name)
	return ""
}

// recorder answers a request from a cassette, and says what it could not.
type recorder struct {
	t      reporter
	record *recording
	mu     sync.Mutex
	// used counts each interaction's uses, so an interaction nothing asked
	// for is reported once the run is over.
	used []int
}

func (r *recorder) RoundTrip(request *http.Request) (*http.Response, error) {
	want := wanted{Method: request.Method, URL: canonicalURL(request.URL), Body: bodyOf(request)}
	r.mu.Lock()
	chosen := -1
	for i := range r.record.Interactions {
		if r.used[i] == 0 && r.record.Interactions[i].matches(want) {
			r.used[i]++
			chosen = i
			break
		}
	}
	r.mu.Unlock()
	if chosen < 0 {
		// Naming what the cassette does have, rather than only what was
		// asked for, is what lets a worker find the transcript to add.
		missing := fmt.Errorf("%s is not in it; it has %s", want, r.record.describe())
		r.t.Errorf("cassette %s: %v", r.record.Name, missing)
		return nil, missing
	}
	interaction := r.record.Interactions[chosen]
	return &http.Response{
		Status:        strconv.Itoa(interaction.Response.Status) + " " + http.StatusText(interaction.Response.Status),
		StatusCode:    interaction.Response.Status,
		Proto:         "HTTP/1.1",
		ProtoMajor:    1,
		ProtoMinor:    1,
		Header:        interaction.Response.header(),
		Body:          io.NopCloser(bytes.NewReader(interaction.Response.Body)),
		ContentLength: int64(len(interaction.Response.Body)),
		Request:       request,
	}, nil
}

// CloseIdleConnections lets http.Client reach a transport that has one.
func (r *recorder) CloseIdleConnections() {}

// unused are the interactions nothing asked for.
func (r *recorder) unused() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var names []string
	for i, interaction := range r.record.Interactions {
		if r.used[i] == 0 {
			names = append(names, interaction.Request.describe())
		}
	}
	return names
}

func (r *recording) describe() string {
	names := make([]string, 0, len(r.Interactions))
	for _, interaction := range r.Interactions {
		names = append(names, interaction.Request.describe())
	}
	return strings.Join(names, ", ")
}

func (request recordedRequest) describe() string {
	if request.URL == "" {
		return strings.ToUpper(request.Method) + " with no URL"
	}
	return strings.ToUpper(request.Method) + " " + canonicalURL(mustParse(request.URL))
}

func (interaction cassette) matches(want wanted) bool {
	return strings.EqualFold(interaction.Request.Method, want.Method) &&
		interaction.Request.URL != "" && canonicalURL(mustParse(interaction.Request.URL)) == want.URL &&
		sameBody(interaction.Request.Body, want.Body)
}

func mustParse(raw string) *url.URL {
	parsed, err := url.Parse(raw)
	if err != nil {
		return &url.URL{Path: raw}
	}
	return parsed
}

// wanted is one request as a cassette matches it: the method, the URL with
// its query sorted, and the body compared by what it means.
type wanted struct {
	Method string
	URL    string
	Body   []byte
}

func (want wanted) String() string {
	if len(want.Body) == 0 {
		return strings.ToUpper(want.Method) + " " + want.URL
	}
	return strings.ToUpper(want.Method) + " " + want.URL + " with the body " + truncate(string(want.Body))
}

// canonicalURL is a URL as a cassette matches it: the scheme and host, the
// path, and the query sorted, so a request that spells its parameters in
// another order is the same request.
func canonicalURL(target *url.URL) string {
	if target.Scheme == "" || target.Host == "" {
		// A path with no host, which is how a cassette records one: the
		// session is given the app's own base separately.
		return canonicalPath(target)
	}
	return target.Scheme + "://" + target.Host + canonicalPath(target)
}

func canonicalPath(target *url.URL) string {
	query := target.Query()
	path := target.EscapedPath()
	if path == "" {
		path = "/"
	}
	if len(query) == 0 {
		return path
	}
	// url.Values.Encode sorts by key, and by value within a key.
	return path + "?" + query.Encode()
}

// sameBody compares two bodies by what they mean, so 1 and 1.0 are one body
// and so are two orderings of an object. A body that is not JSON is compared
// as it stands.
func sameBody(recorded json.RawMessage, sent []byte) bool {
	if len(recorded) == 0 {
		return len(sent) == 0
	}
	var left, right any
	if json.Unmarshal(recorded, &left) != nil {
		return bytes.Equal(bytes.TrimSpace(recorded), bytes.TrimSpace(sent))
	}
	if json.Unmarshal(sent, &right) != nil {
		return false
	}
	first, errFirst := json.Marshal(left)
	second, errSecond := json.Marshal(right)
	if errFirst != nil || errSecond != nil {
		return false
	}
	return bytes.Equal(first, second)
}

// maxBodyBytes bounds a recorded body. A provider's own examples are far
// smaller, and a cassette is a transcript rather than a store.
const maxBodyBytes = 8 << 20

// bodyOf is the body a request carries, read into memory because a cassette
// matches on what it means.
func bodyOf(request *http.Request) []byte {
	if request.Body == nil {
		return nil
	}
	defer request.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(request.Body, maxBodyBytes+1))
	return raw
}
