package gate

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"sync"
	"time"
)

// entry is one recorded exchange. A request matches on method, upstream host
// and path with query; each entry answers once, in file order.
type entry struct {
	Method  string            `json:"method"`
	Host    string            `json:"host"`
	Path    string            `json:"path"`
	Status  int               `json:"status"`
	Headers map[string]string `json:"headers,omitempty"`
	Body    json.RawMessage   `json:"body,omitempty"`
}

// egressHostHeader is how a connector names the upstream host (oryxa-platform connectors/broker-proxy.json).
const egressHostHeader = "x-oryxa-egress-host"

// seen is one request the broker received.
type seen struct {
	Method, Host, Path string
	At                 time.Time
	Matched            bool
}

// broker stands in for the egress proxy: it answers from a cassette and
// records what it was asked.
type broker struct {
	srv *httptest.Server

	mu      sync.Mutex
	entries []entry
	used    []bool
	log     []seen
	// replied is when each 429 was answered, by entry index, until its retry arrives.
	replied map[int]time.Time
	// early is set when a retry came sooner than the 429's Retry-After.
	early string
}

func loadCassette(path string) ([]entry, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var es []entry
	if err := json.Unmarshal(raw, &es); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return es, nil
}

func newBroker(entries []entry) *broker {
	b := &broker{entries: entries, used: make([]bool, len(entries)), replied: map[int]time.Time{}}
	b.srv = httptest.NewServer(http.HandlerFunc(b.serve))
	return b
}

func (b *broker) close() { b.srv.Close() }

func (b *broker) serve(w http.ResponseWriter, r *http.Request) {
	host, path := r.Header.Get(egressHostHeader), r.URL.RequestURI()
	now := time.Now()
	b.mu.Lock()
	defer b.mu.Unlock()
	for i, e := range b.entries {
		if b.used[i] || e.Method != r.Method || e.Host != host || e.Path != path {
			continue
		}
		b.used[i] = true
		b.log = append(b.log, seen{r.Method, host, path, now, true})
		// A request after a 429 for the same key is the retry; it must have waited.
		for j, at := range b.replied {
			if o := b.entries[j]; o.Method == e.Method && o.Host == e.Host && o.Path == e.Path {
				secs, _ := strconv.Atoi(o.Headers["Retry-After"])
				if d := now.Sub(at); d < time.Duration(secs)*time.Second && b.early == "" {
					b.early = fmt.Sprintf("retried after %s, Retry-After said %ds", d.Round(time.Millisecond), secs)
				}
				delete(b.replied, j)
			}
		}
		if e.Status == http.StatusTooManyRequests {
			b.replied[i] = now
		}
		for k, v := range e.Headers {
			w.Header().Set(k, v)
		}
		w.WriteHeader(e.Status)
		_, _ = w.Write(e.Body)
		return
	}
	b.log = append(b.log, seen{r.Method, host, path, now, false})
	http.Error(w, "no cassette entry for "+r.Method+" "+host+path, 599)
}

// misses lists requests the cassette had no entry for.
func (b *broker) misses() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []string
	for _, s := range b.log {
		if !s.Matched {
			out = append(out, s.Method+" "+s.Host+s.Path)
		}
	}
	return out
}

// methods lists the HTTP methods used.
func (b *broker) methods() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []string
	for _, s := range b.log {
		out = append(out, s.Method)
	}
	return out
}

// rateLimitProblem reports a 429 that got no retry, or one sooner than its Retry-After.
func (b *broker) rateLimitProblem() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.replied) > 0 {
		return "a 429 was not retried"
	}
	return b.early
}
