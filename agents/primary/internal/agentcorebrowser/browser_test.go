package agentcorebrowser

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

type browserStart struct {
	identifier string
	name       string
	timeout    int32
	width      int32
	height     int32
}

type fakeRuntime struct {
	mu       sync.Mutex
	starts   []browserStart
	stops    []string
	startErr error
	stopErr  error
}

func (f *fakeRuntime) start(_ context.Context, identifier, name string, timeout, width, height int32) (startedSession, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.startErr != nil {
		return startedSession{}, f.startErr
	}
	f.starts = append(f.starts, browserStart{identifier: identifier, name: name, timeout: timeout, width: width, height: height})
	number := len(f.starts)
	return startedSession{id: "browser-" + string(rune('0'+number)), automationEndpoint: "wss://example.test/session"}, nil
}

func (f *fakeRuntime) stop(_ context.Context, _ string, sessionID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stops = append(f.stops, sessionID)
	return f.stopErr
}

type fakeConnector struct {
	mu        sync.Mutex
	clients   []*fakeAutomation
	endpoints []string
	err       error
}

func (f *fakeConnector) connect(_ context.Context, endpoint string) (automationClient, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.endpoints = append(f.endpoints, endpoint)
	if f.err != nil {
		return nil, f.err
	}
	client := &fakeAutomation{}
	f.clients = append(f.clients, client)
	return client, nil
}

type fakeAutomation struct {
	requests []Request
	closed   bool
	err      error
}

func (f *fakeAutomation) invoke(_ context.Context, request Request) (Result, error) {
	f.requests = append(f.requests, request)
	return Result{Page: PageSnapshot{Title: "Example", URL: "https://example.test/"}, ScreenshotPNG: []byte("png")}, f.err
}

func (f *fakeAutomation) close() error {
	f.closed = true
	return nil
}

func TestManagerReusesAndIsolatesBrowserSessions(t *testing.T) {
	api := &fakeRuntime{}
	connector := &fakeConnector{}
	manager, err := newManager(api, Options{connector: connector, SessionTimeout: time.Hour, resolver: publicResolver()})
	if err != nil {
		t.Fatal(err)
	}

	first, err := manager.Invoke(t.Context(), "one", Request{Action: Navigate, URL: "https://example.test"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := manager.Invoke(t.Context(), "one", Request{Action: Snapshot})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Invoke(t.Context(), "two", Request{Action: Snapshot}); err != nil {
		t.Fatal(err)
	}

	if len(api.starts) != 2 || len(connector.clients) != 2 {
		t.Fatalf("starts = %+v, clients = %d", api.starts, len(connector.clients))
	}
	if len(connector.clients[0].requests) != 2 || connector.clients[0].requests[0].Action != Navigate || connector.clients[0].requests[1].Action != Snapshot {
		t.Fatalf("first browser requests = %+v", connector.clients[0].requests)
	}
	if !first.SessionRecreated || second.SessionRecreated {
		t.Fatalf("session recreation flags = %v, %v", first.SessionRecreated, second.SessionRecreated)
	}
	if first.ViewportWidth != DefaultViewportWidth || first.ViewportHeight != DefaultViewportHeight {
		t.Fatalf("viewport = %dx%d", first.ViewportWidth, first.ViewportHeight)
	}
}

func TestManagerRecreatesBeforeExpiryAndStopsOldSession(t *testing.T) {
	now := time.Date(2026, time.September, 14, 12, 0, 0, 0, time.UTC)
	api := &fakeRuntime{}
	connector := &fakeConnector{}
	manager, err := newManager(api, Options{
		connector:      connector,
		SessionTimeout: time.Minute,
		now:            func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Invoke(t.Context(), "one", Request{Action: Snapshot}); err != nil {
		t.Fatal(err)
	}
	now = now.Add(55 * time.Second)
	result, err := manager.Invoke(t.Context(), "one", Request{Action: Snapshot})
	if err != nil {
		t.Fatal(err)
	}
	if !result.SessionRecreated || len(api.starts) != 2 || !slices.Equal(api.stops, []string{"browser-1"}) {
		t.Fatalf("result = %+v, starts = %+v, stops = %v", result, api.starts, api.stops)
	}
	if !connector.clients[0].closed {
		t.Fatal("expired automation connection was not closed")
	}
}

func TestManagerEvictsLocallyExpiredInactiveSessions(t *testing.T) {
	now := time.Date(2026, time.September, 14, 12, 0, 0, 0, time.UTC)
	api := &fakeRuntime{}
	connector := &fakeConnector{}
	manager, err := newManager(api, Options{
		connector:      connector,
		SessionTimeout: time.Minute,
		now:            func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Invoke(t.Context(), "old", Request{Action: Snapshot}); err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Minute)
	if _, err := manager.Invoke(t.Context(), "new", Request{Action: Snapshot}); err != nil {
		t.Fatal(err)
	}
	if manager.sessions["old"] != nil || manager.sessions["new"] == nil {
		t.Fatalf("sessions = %+v", manager.sessions)
	}
	if !connector.clients[0].closed {
		t.Fatal("expired local automation connection was not closed")
	}
	if len(api.stops) != 0 {
		t.Fatalf("already-expired remote session was stopped explicitly: %v", api.stops)
	}
}

func TestManagerResetAndCloseStopSessions(t *testing.T) {
	api := &fakeRuntime{}
	connector := &fakeConnector{}
	manager, err := newManager(api, Options{connector: connector})
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"one", "two"} {
		if _, err := manager.Invoke(t.Context(), key, Request{Action: Snapshot}); err != nil {
			t.Fatal(err)
		}
	}
	if err := manager.Reset(t.Context(), "one"); err != nil {
		t.Fatal(err)
	}
	if err := manager.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(api.stops, []string{"browser-1", "browser-2"}) {
		t.Fatalf("stops = %v", api.stops)
	}
	if !connector.clients[0].closed || !connector.clients[1].closed {
		t.Fatal("automation connections were not closed")
	}
	if _, err := manager.Invoke(t.Context(), "three", Request{Action: Snapshot}); err == nil || !strings.Contains(err.Error(), "closed") {
		t.Fatalf("Invoke after Close error = %v", err)
	}
}

func TestManagerStopsSessionWhenAutomationConnectionFails(t *testing.T) {
	api := &fakeRuntime{}
	manager, err := newManager(api, Options{connector: &fakeConnector{err: errors.New("upgrade rejected")}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = manager.Invoke(t.Context(), "one", Request{Action: Snapshot})
	if err == nil || !strings.Contains(err.Error(), "connect AgentCore browser automation stream") {
		t.Fatalf("Invoke error = %v", err)
	}
	if !slices.Equal(api.stops, []string{"browser-1"}) {
		t.Fatalf("stops = %v", api.stops)
	}
}

func TestManagerRetainsFailedCleanupForNextAttempt(t *testing.T) {
	api := &fakeRuntime{stopErr: errors.New("stop unavailable")}
	connector := &fakeConnector{err: errors.New("upgrade rejected")}
	manager, err := newManager(api, Options{connector: connector})
	if err != nil {
		t.Fatal(err)
	}
	_, err = manager.Invoke(t.Context(), "one", Request{Action: Snapshot})
	if err == nil || !strings.Contains(err.Error(), "stop AgentCore browser after automation connection failure") {
		t.Fatalf("Invoke error = %v", err)
	}
	entry := manager.sessions["one"]
	if entry == nil || entry.id != "browser-1" || entry.automation != nil {
		t.Fatalf("retained entry = %+v", entry)
	}

	api.stopErr = nil
	connector.err = nil
	result, err := manager.Invoke(t.Context(), "one", Request{Action: Snapshot})
	if err != nil {
		t.Fatal(err)
	}
	if !result.SessionRecreated || len(api.starts) != 2 || !slices.Equal(api.stops, []string{"browser-1", "browser-1"}) {
		t.Fatalf("result = %+v, starts = %+v, stops = %v", result, api.starts, api.stops)
	}
}

func TestBrowserRequestValidation(t *testing.T) {
	tests := []struct {
		name    string
		request Request
		wantErr bool
	}{
		{name: "navigate", request: Request{Action: Navigate, URL: "https://example.test/path?q=1"}},
		{name: "reject relative", request: Request{Action: Navigate, URL: "/private"}, wantErr: true},
		{name: "reject script", request: Request{Action: Navigate, URL: "javascript:alert(1)"}, wantErr: true},
		{name: "reject URL credentials", request: Request{Action: Navigate, URL: "https://user:pass@example.test"}, wantErr: true},
		{name: "reject localhost", request: Request{Action: Navigate, URL: "http://localhost:8080"}, wantErr: true},
		{name: "reject private IP", request: Request{Action: Navigate, URL: "http://10.0.0.1/private"}, wantErr: true},
		{name: "reject metadata IP", request: Request{Action: Navigate, URL: "http://169.254.169.254/latest/meta-data"}, wantErr: true},
		{name: "reject a name for the metadata IP", request: Request{Action: Navigate, URL: "http://metadata.test/latest/meta-data"}, wantErr: true},
		{name: "reject an unknown host", request: Request{Action: Navigate, URL: "https://no-such-host.test/"}, wantErr: true},
		{name: "navigate a public IP", request: Request{Action: Navigate, URL: "http://93.184.216.34/"}},
		{name: "snapshot", request: Request{Action: Snapshot}},
		{name: "click", request: Request{Action: Click, Selector: "#submit"}},
		{name: "blank selector", request: Request{Action: Click, Selector: " "}, wantErr: true},
		{name: "fill", request: Request{Action: Fill, Selector: "input[name=q]", Text: "hello"}},
		{name: "null fill", request: Request{Action: Fill, Selector: "input", Text: "bad\x00text"}, wantErr: true},
		{name: "screenshot", request: Request{Action: Screenshot}},
		{name: "unknown", request: Request{Action: "launch"}, wantErr: true},
	}
	resolver := publicResolver()
	resolver.answers["metadata.test"] = addresses("169.254.169.254")
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := test.request.validate(t.Context(), resolver)
			if (err != nil) != test.wantErr {
				t.Fatalf("validate error = %v", err)
			}
		})
	}
}

func TestNewManagerValidatesOptions(t *testing.T) {
	connector := &fakeConnector{}
	if _, err := newManager(nil, Options{connector: connector}); err == nil {
		t.Fatal("newManager(nil) error = nil")
	}
	if _, err := newManager(&fakeRuntime{}, Options{}); err == nil {
		t.Fatal("newManager without connector error = nil")
	}
	for _, options := range []Options{
		{connector: connector, SessionTimeout: time.Second},
		{connector: connector, SessionTimeout: MaxSessionTimeout + time.Second},
		{connector: connector, ViewportWidth: 799},
		{connector: connector, ViewportHeight: 1081},
	} {
		if _, err := newManager(&fakeRuntime{}, options); err == nil {
			t.Fatalf("newManager accepted options %+v", options)
		}
	}
}
