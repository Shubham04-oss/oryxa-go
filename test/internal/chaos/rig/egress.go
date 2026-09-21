package rig

import (
	"net"
	"net/http"
	"sort"
	"sync"
)

// Egress is a proxy that refuses everything and writes down what was asked.
//
// The process under test is started with HTTPS_PROXY and HTTP_PROXY pointing
// here and NO_PROXY naming only the local fakes and emulators, so a call to
// anything on the internet (Composio, Dodo, Google, real AWS endpoints) fails
// at once instead of leaving the machine. What it refused is the answer to
// "what would this system have called out to?", which is worth reading.
type Egress struct {
	mu       sync.Mutex
	attempts map[string]int
	server   *http.Server
	listener net.Listener
}

// NewEgress listens on address.
func NewEgress(address string) (*Egress, error) {
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return nil, err
	}
	egress := &Egress{attempts: map[string]int{}, listener: listener}
	egress.server = &http.Server{Handler: http.HandlerFunc(egress.refuse)}
	go func() { _ = egress.server.Serve(listener) }()
	return egress, nil
}

func (egress *Egress) refuse(writer http.ResponseWriter, request *http.Request) {
	host := request.Host
	if request.Method == http.MethodConnect {
		host = request.RequestURI
	}
	egress.mu.Lock()
	egress.attempts[request.Method+" "+host]++
	egress.mu.Unlock()
	http.Error(writer, "refused by the chaos rig: no traffic may leave the machine", http.StatusForbidden)
}

// Attempts is every refused destination with how often it was tried.
func (egress *Egress) Attempts() map[string]int {
	egress.mu.Lock()
	defer egress.mu.Unlock()
	out := make(map[string]int, len(egress.attempts))
	for key, count := range egress.attempts {
		out[key] = count
	}
	return out
}

// Summary lists attempts, most frequent first.
func (egress *Egress) Summary() []string {
	attempts := egress.Attempts()
	keys := make([]string, 0, len(attempts))
	for key := range attempts {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool { return attempts[keys[i]] > attempts[keys[j]] })
	lines := make([]string, 0, len(keys))
	for _, key := range keys {
		lines = append(lines, key)
	}
	return lines
}

// Close stops the proxy.
func (egress *Egress) Close() { _ = egress.server.Close() }
