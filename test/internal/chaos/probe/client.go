package probe

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"
)

// Req is one request, plus what the report needs to say about it.
type Req struct {
	Method  string
	Path    string // path and query, exactly as sent
	Header  http.Header
	Body    []byte
	Token   string // sent as a Bearer token when Header carries no Authorization
	Timeout time.Duration
	// Raw, when set, is written to the socket as is and everything else but
	// Timeout is ignored. It is how requests Go's client will not build get sent.
	Raw []byte

	Op       string // operation name: "teams.create"
	Route    string // METHOD /path/{template}
	Mutation string // what was done to the baseline
	Class    string // grouping for reports
	Target   string // which field, parameter or header was mutated
	// Deny marks a request no one may succeed with: a 2xx to it is a hole.
	Deny bool
}

// Res is what came back.
type Res struct {
	Status    int
	Header    http.Header
	Body      []byte
	Latency   time.Duration
	Err       string
	Dropped   bool // the connection closed before a response
	TimedOut  bool
	Truncated bool // the body was longer than the recorded snippet
	BodySize  int64
	RequestID string // the server's X-Request-Id, to find its log lines
}

const bodyCap = 32 << 10

// Client sends requests to one base URL and keeps score.
type Client struct {
	base string
	addr string
	http *http.Client

	total    atomic.Int64
	bytesOut atomic.Int64
	mu       sync.Mutex
	byStatus map[int]int64
	latency  []time.Duration // sampled, for percentiles
	sink     func(Req, Res)
}

// NewClient talks to base, such as http://127.0.0.1:18090.
func NewClient(base string) (*Client, error) {
	parsed, err := url.Parse(base)
	if err != nil {
		return nil, err
	}
	transport := &http.Transport{
		MaxIdleConns: 256, MaxIdleConnsPerHost: 128, IdleConnTimeout: 30 * time.Second,
		DisableCompression: true, ResponseHeaderTimeout: 0,
	}
	return &Client{
		base: strings.TrimRight(base, "/"), addr: parsed.Host, byStatus: map[int]int64{},
		http: &http.Client{
			Transport:     transport,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
	}, nil
}

// CloseIdle drops the client's idle keep-alive connections, so what the server
// still holds afterwards is its own.
func (client *Client) CloseIdle() { client.http.CloseIdleConnections() }

// Sink registers a function called with every request and its result.
func (client *Client) Sink(sink func(Req, Res)) { client.sink = sink }

// Do sends a request. It never returns an error: a failure is part of the result.
func (client *Client) Do(ctx context.Context, req Req) Res {
	if req.Timeout == 0 {
		req.Timeout = 20 * time.Second
	}
	started := time.Now()
	var res Res
	if req.Raw != nil {
		res = client.raw(ctx, req)
	} else {
		res = client.send(ctx, req)
	}
	res.Latency = time.Since(started)
	client.record(req, res)
	return res
}

func (client *Client) send(ctx context.Context, req Req) Res {
	ctx, cancel := context.WithTimeout(ctx, req.Timeout)
	defer cancel()
	target := client.base + req.Path
	var body io.Reader
	if req.Body != nil {
		body = bytes.NewReader(req.Body)
	}
	httpReq, err := http.NewRequestWithContext(ctx, req.Method, target, body)
	if err != nil {
		// Go refuses to build it (bad escape, control byte in a header). Say it
		// with the raw sender instead: a server must cope with whatever arrives.
		return client.raw(ctx, Req{Raw: buildRaw(req, client.addr), Timeout: req.Timeout})
	}
	for name, values := range req.Header {
		for _, value := range values {
			httpReq.Header.Add(name, value)
		}
	}
	if req.Token != "" && httpReq.Header.Get("Authorization") == "" {
		httpReq.Header.Set("Authorization", "Bearer "+req.Token)
	}
	response, err := client.http.Do(httpReq)
	if err != nil {
		if strings.Contains(err.Error(), "invalid header") || strings.Contains(err.Error(), "invalid method") {
			return client.raw(ctx, Req{Raw: buildRaw(req, client.addr), Timeout: req.Timeout})
		}
		return failure(ctx, err)
	}
	defer response.Body.Close()
	captured, err := io.ReadAll(io.LimitReader(response.Body, bodyCap))
	size := int64(len(captured))
	if err == nil {
		extra, _ := io.Copy(io.Discard, response.Body)
		size += extra
	}
	res := Res{Status: response.StatusCode, Header: response.Header, Body: captured, BodySize: size, Truncated: size > int64(len(captured)), RequestID: response.Header.Get("X-Request-Id")}
	if err != nil {
		res.Err = err.Error()
		res.Dropped = errors.Is(err, io.ErrUnexpectedEOF) || strings.Contains(err.Error(), "reset")
		res.TimedOut = ctx.Err() != nil
	}
	return res
}

func failure(ctx context.Context, err error) Res {
	res := Res{Err: err.Error()}
	if ctx.Err() != nil || strings.Contains(err.Error(), "deadline") || strings.Contains(err.Error(), "Timeout") {
		res.TimedOut = true
		return res
	}
	text := err.Error()
	res.Dropped = strings.Contains(text, "EOF") || strings.Contains(text, "reset") || strings.Contains(text, "broken pipe")
	return res
}

// raw dials the server and writes payload as is.
func (client *Client) raw(ctx context.Context, req Req) Res {
	dialer := net.Dialer{Timeout: 5 * time.Second}
	conn, err := dialer.DialContext(ctx, "tcp", client.addr)
	if err != nil {
		return Res{Err: err.Error()}
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(req.Timeout))
	if _, err := conn.Write(req.Raw); err != nil {
		return failure(ctx, err)
	}
	reader := bufio.NewReader(conn)
	response, err := http.ReadResponse(reader, nil)
	if err != nil {
		return failure(ctx, err)
	}
	defer response.Body.Close()
	captured, _ := io.ReadAll(io.LimitReader(response.Body, bodyCap))
	return Res{Status: response.StatusCode, Header: response.Header, Body: captured, BodySize: int64(len(captured)), RequestID: response.Header.Get("X-Request-Id")}
}

// buildRaw writes a request in HTTP/1.1 wire form without validating it.
func buildRaw(req Req, host string) []byte {
	var out bytes.Buffer
	fmt.Fprintf(&out, "%s %s HTTP/1.1\r\nHost: %s\r\nConnection: close\r\n", req.Method, req.Path, host)
	for name, values := range req.Header {
		for _, value := range values {
			fmt.Fprintf(&out, "%s: %s\r\n", name, value)
		}
	}
	if req.Token != "" && req.Header.Get("Authorization") == "" {
		fmt.Fprintf(&out, "Authorization: Bearer %s\r\n", req.Token)
	}
	if req.Body != nil {
		fmt.Fprintf(&out, "Content-Length: %d\r\n", len(req.Body))
	}
	out.WriteString("\r\n")
	out.Write(req.Body)
	return out.Bytes()
}

func (client *Client) record(req Req, res Res) {
	client.total.Add(1)
	client.bytesOut.Add(int64(len(req.Body)))
	client.mu.Lock()
	client.byStatus[res.Status]++
	if client.total.Load()%7 == 0 && len(client.latency) < 200_000 {
		client.latency = append(client.latency, res.Latency)
	}
	client.mu.Unlock()
	if client.sink != nil {
		client.sink(req, res)
	}
}

// Stats summarises what has been sent so far.
type Stats struct {
	Total    int64
	ByStatus map[int]int64
	P50, P95 time.Duration
	P99, Max time.Duration
}

// Stats returns the score so far.
func (client *Client) Stats() Stats {
	client.mu.Lock()
	defer client.mu.Unlock()
	stats := Stats{Total: client.total.Load(), ByStatus: map[int]int64{}}
	for status, count := range client.byStatus {
		stats.ByStatus[status] = count
	}
	if n := len(client.latency); n > 0 {
		sorted := append([]time.Duration(nil), client.latency...)
		sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
		at := func(q float64) time.Duration { return sorted[int(float64(n-1)*q)] }
		stats.P50, stats.P95, stats.P99, stats.Max = at(0.50), at(0.95), at(0.99), sorted[n-1]
	}
	return stats
}

// Curl renders a request as a command that reproduces it.
func Curl(base string, req Req) string {
	if req.Raw != nil {
		return fmt.Sprintf("printf %q | nc %s   # raw request, %d bytes", clip(string(req.Raw), 300), strings.TrimPrefix(base, "http://"), len(req.Raw))
	}
	var out strings.Builder
	fmt.Fprintf(&out, "curl -sS -i -X %s '%s%s'", req.Method, base, shellQuote(req.Path))
	if req.Token != "" {
		fmt.Fprintf(&out, " -H 'Authorization: Bearer %s'", req.Token)
	}
	for name, values := range req.Header {
		for _, value := range values {
			fmt.Fprintf(&out, " -H '%s: %s'", name, shellQuote(clip(value, 120)))
		}
	}
	if len(req.Body) > 0 {
		switch {
		case len(req.Body) > 600:
			fmt.Fprintf(&out, " --data-binary @body.bin   # body is %d bytes: %q…", len(req.Body), clip(string(req.Body), 80))
		case utf8.Valid(req.Body) && !bytes.ContainsRune(req.Body, 0):
			fmt.Fprintf(&out, " -H 'Content-Type: application/json' --data-raw '%s'", shellQuote(string(req.Body)))
		default:
			fmt.Fprintf(&out, " --data-binary $%q", string(req.Body))
		}
	}
	return out.String()
}

func shellQuote(text string) string { return strings.ReplaceAll(text, "'", `'\''`) }

func clip(text string, limit int) string {
	if len(text) <= limit {
		return text
	}
	return text[:limit] + fmt.Sprintf("…(+%d)", len(text)-limit)
}
