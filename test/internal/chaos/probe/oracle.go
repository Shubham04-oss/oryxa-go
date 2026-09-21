package probe

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// Severity orders findings.
const (
	Critical = "critical"
	High     = "high"
	Medium   = "medium"
	Low      = "low"
	Info     = "info"
)

var severityRank = map[string]int{Critical: 0, High: 1, Medium: 2, Low: 3, Info: 4}

// Finding is something the system did that it should not have.
type Finding struct {
	Kind      string
	Severity  string
	Op        string
	Route     string
	Class     string
	Status    int
	Detail    string
	Snippet   string
	Curl      string
	Count     int
	Examples  []string // mutation names beyond the first
	Latency   time.Duration
	RequestID string // of the first occurrence, to find its log lines
}

// FindingSet collects findings, folding repeats into one with a count.
type FindingSet struct {
	mu    sync.Mutex
	byKey map[string]*Finding
}

// NewFindingSet returns an empty set.
func NewFindingSet() *FindingSet { return &FindingSet{byKey: map[string]*Finding{}} }

// Add records a finding, or counts it again when its kind, route, status and
// class are already known.
func (set *FindingSet) Add(finding Finding, mutation string) {
	key := fmt.Sprintf("%s|%s|%d|%s", finding.Kind, finding.Route, finding.Status, finding.Class)
	set.mu.Lock()
	defer set.mu.Unlock()
	if existing, ok := set.byKey[key]; ok {
		existing.Count++
		if len(existing.Examples) < 6 && mutation != "" {
			existing.Examples = append(existing.Examples, mutation)
		}
		return
	}
	finding.Count = 1
	if mutation != "" {
		finding.Examples = []string{mutation}
	}
	set.byKey[key] = &finding
}

// All returns the findings, worst first.
func (set *FindingSet) All() []*Finding {
	set.mu.Lock()
	defer set.mu.Unlock()
	out := make([]*Finding, 0, len(set.byKey))
	for _, finding := range set.byKey {
		out = append(out, finding)
	}
	sort.Slice(out, func(i, j int) bool {
		if severityRank[out[i].Severity] != severityRank[out[j].Severity] {
			return severityRank[out[i].Severity] < severityRank[out[j].Severity]
		}
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].Route < out[j].Route
	})
	return out
}

var leakPatterns = []*regexp.Regexp{
	regexp.MustCompile(`goroutine \d+ \[`),
	regexp.MustCompile(`panic:|runtime error`),
	regexp.MustCompile(`SQLSTATE|pgconn|pgx|pq: `),
	regexp.MustCompile(`syntax error at or near|invalid input syntax|violates (check|foreign|unique|not-null)`),
	regexp.MustCompile(`relation "[a-z_]+"|column "[a-z_]+"`),
	regexp.MustCompile(`\.go:\d+`),
	regexp.MustCompile(`/Users/|/private/var|/home/[a-z]+/|/usr/local/go`),
	regexp.MustCompile(`AccessDenied|arn:aws:|NoSuchBucket|ResourceNotFoundException|RequestID`),
	regexp.MustCompile(`dial tcp|connection refused|no such host|proxyconnect`),
}

// hostileClasses are the fragment classes whose acceptance is itself worth a note.
var hostileAccepted = map[string]bool{
	"nul-middle": true, "nul-only": true, "bad-utf8": true, "truncated-utf8": true, "overlong-utf8": true,
	"lone-surrogate": true, "len-70k": true, "len-300k-multibyte": true, "len-5k": true,
	"rtl-override": true, "esc-chars": true, "crlf": true,
}

// Judge says what is wrong with an answer, if anything.
func Judge(req Req, res Res, slowAfter time.Duration) []Finding {
	base := Finding{Op: req.Op, Route: req.Route, Class: req.Class, Status: res.Status, Latency: res.Latency, RequestID: res.RequestID}
	var out []Finding
	emit := func(kind, severity, detail string) {
		finding := base
		finding.Kind, finding.Severity, finding.Detail = kind, severity, detail
		finding.Snippet = snippet(res)
		out = append(out, finding)
	}
	switch {
	case res.TimedOut:
		emit("hang", High, fmt.Sprintf("no complete answer within %s", req.Timeout))
	case res.Dropped:
		emit("dropped-connection", High, "the server closed the connection without an answer (a recovered panic does this): "+res.Err)
	case res.Status == 0:
		emit("transport-error", Medium, res.Err)
	case res.Status == 500:
		emit("server-error-500", High, "an unhandled failure on hostile input; the server log has the cause")
	case res.Status >= 500:
		emit(fmt.Sprintf("server-error-%d", res.Status), Medium, "dependency or gateway failure surfaced to the caller")
	}
	if res.Status != 0 && res.Latency > slowAfter {
		emit("slow", Medium, fmt.Sprintf("answered in %s", res.Latency.Round(time.Millisecond)))
	}
	body := string(res.Body)
	for _, pattern := range leakPatterns {
		if match := pattern.FindString(body); match != "" {
			emit("info-leak", Medium, fmt.Sprintf("response body exposes internals (matched %q)", clip(match, 40)))
			break
		}
	}
	if res.Status >= 200 && res.Status < 300 && hostileAccepted[req.Mutation] && req.Target != "content" && req.Target != "embedding" {
		emit("accepted-hostile", Low, fmt.Sprintf("a 2xx for %q, a value nothing legitimate sends", req.Mutation))
	}
	return out
}

func snippet(res Res) string {
	text := strings.Map(func(r rune) rune {
		if r < 32 && r != '\n' && r != '\t' {
			return '·'
		}
		return r
	}, string(res.Body))
	if res.Err != "" && text == "" {
		text = res.Err
	}
	return clip(strings.TrimSpace(text), 240)
}

// LogEntry is one distinct problem the server logged about itself.
type LogEntry struct {
	Level     string
	Component string
	Message   string
	Error     string
	Path      string
	Count     int
	First     string
}

// RouteStats is what the server's own access log says about one route.
type RouteStats struct {
	Route    string
	Requests int
	Statuses map[int]int
	MaxMS    float64
	TotalMS  float64
	Bytes    int64
}

// LogDigest is what the server said about itself while it was being abused.
type LogDigest struct {
	Lines    int
	Errors   int
	Warnings int
	Panics   int
	Entries  []LogEntry
	PanicTxt []string
	// Access is the server's view of each route: requests, statuses, latency.
	Access []RouteStats
	// Slowest are the slowest requests the server logged.
	Slowest []string
}

var (
	idNoise  = regexp.MustCompile(`(org|team|user|file|agent|corpus|doc|src|thr|run)_[A-Za-z0-9_-]{8,}`)
	numNoise = regexp.MustCompile(`\b\d{2,}\b`)
)

// ScanLog reads the server log from an offset and digests its ERROR and WARN
// lines, and anything that looks like a panic. It returns the new offset.
func ScanLog(path string, from int64) (LogDigest, int64, error) {
	var digest LogDigest
	file, err := os.Open(path)
	if err != nil {
		return digest, from, err
	}
	defer file.Close()
	if _, err := file.Seek(from, 0); err != nil {
		return digest, from, err
	}
	seen := map[string]*LogEntry{}
	access := map[string]*RouteStats{}
	type slow struct {
		ms   float64
		text string
	}
	var slowest []slow
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 1<<20), 16<<20)
	offset := from
	for scanner.Scan() {
		line := scanner.Text()
		offset += int64(len(line)) + 1
		digest.Lines++
		if strings.Contains(line, "panic serving") || strings.Contains(line, "panic:") || strings.Contains(line, "goroutine ") && strings.Contains(line, "[running]") {
			digest.Panics++
			if len(digest.PanicTxt) < 5 {
				digest.PanicTxt = append(digest.PanicTxt, clip(line, 400))
			}
		}
		var record map[string]any
		if json.Unmarshal([]byte(line), &record) != nil {
			continue
		}
		level, _ := record["level"].(string)
		if message, _ := record["msg"].(string); message == "http request completed" {
			route, _ := record["http.route"].(string)
			status, _ := record["http.status_code"].(float64)
			millis, _ := record["duration_ms"].(float64)
			size, _ := record["http.response_bytes"].(float64)
			stats := access[route]
			if stats == nil {
				stats = &RouteStats{Route: route, Statuses: map[int]int{}}
				access[route] = stats
			}
			stats.Requests++
			stats.Statuses[int(status)]++
			stats.TotalMS += millis
			stats.Bytes += int64(size)
			if millis > stats.MaxMS {
				stats.MaxMS = millis
			}
			if millis > 500 {
				slowest = append(slowest, slow{millis, fmt.Sprintf("%.0fms %v %v -> %d (request %v)", millis, record["http.method"], route, int(status), record["request_id"])})
			}
			continue
		}
		if level != "ERROR" && level != "WARN" {
			continue
		}
		if level == "ERROR" {
			digest.Errors++
		} else {
			digest.Warnings++
		}
		message, _ := record["msg"].(string)
		component, _ := record["component"].(string)
		errText := fmt.Sprint(record["error"])
		if record["error"] == nil {
			errText = ""
		}
		path, _ := record["path"].(string)
		key := level + "|" + component + "|" + message + "|" + normalise(errText)
		if entry, ok := seen[key]; ok {
			entry.Count++
			continue
		}
		seen[key] = &LogEntry{Level: level, Component: component, Message: message, Error: clip(errText, 300), Path: normalise(path), Count: 1, First: clip(line, 500)}
	}
	for _, entry := range seen {
		digest.Entries = append(digest.Entries, *entry)
	}
	for _, stats := range access {
		digest.Access = append(digest.Access, *stats)
	}
	sort.Slice(digest.Access, func(i, j int) bool { return digest.Access[i].Requests > digest.Access[j].Requests })
	sort.Slice(slowest, func(i, j int) bool { return slowest[i].ms > slowest[j].ms })
	for index, one := range slowest {
		if index == 8 {
			break
		}
		digest.Slowest = append(digest.Slowest, one.text)
	}
	sort.Slice(digest.Entries, func(i, j int) bool {
		if digest.Entries[i].Level != digest.Entries[j].Level {
			return digest.Entries[i].Level == "ERROR"
		}
		return digest.Entries[i].Count > digest.Entries[j].Count
	})
	return digest, offset, scanner.Err()
}

func normalise(text string) string {
	return numNoise.ReplaceAllString(idNoise.ReplaceAllString(text, "$1_ID"), "N")
}
