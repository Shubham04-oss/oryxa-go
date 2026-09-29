// Package agentcorebrowser connects native ADK tools to Amazon Bedrock
// AgentCore Browser. The browser and its state stay in AWS, never in the
// primary service's container.
package agentcorebrowser

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/bedrockagentcore"
	"github.com/aws/aws-sdk-go-v2/service/bedrockagentcore/types"

	"github.com/Shubham04-oss/oryxa-go/internal/safehttp"
)

const (
	// SystemBrowser is AWS's built-in browser with its restrictive managed
	// defaults. A custom browser identifier can be supplied for organization
	// specific network, recording, profile, or IAM controls.
	SystemBrowser = "aws.browser.v1"

	DefaultSessionTimeout = 15 * time.Minute
	MinSessionTimeout     = time.Minute
	MaxSessionTimeout     = 8 * time.Hour
	DefaultViewportWidth  = int32(1440)
	DefaultViewportHeight = int32(900)

	maxURLBytes      = 8 * 1024
	maxSelectorBytes = 4 * 1024
	maxFillBytes     = 100 * 1024
)

// Action is one browser operation exposed through ADK.
type Action string

const (
	Navigate   Action = "navigate"
	Snapshot   Action = "snapshot"
	Click      Action = "click"
	Fill       Action = "fill"
	Screenshot Action = "screenshot"
)

// Request is a framework-independent browser operation.
type Request struct {
	Action   Action
	URL      string
	Selector string
	Text     string
}

// Element is one actionable, visible DOM element in a semantic snapshot.
// Selector is generated in the remote page and can be passed to Click or Fill.
type Element struct {
	Selector string `json:"selector"`
	Tag      string `json:"tag"`
	Role     string `json:"role,omitempty"`
	Type     string `json:"type,omitempty"`
	Name     string `json:"name,omitempty"`
	Text     string `json:"text,omitempty"`
	Href     string `json:"href,omitempty"`
	Disabled bool   `json:"disabled,omitempty"`
}

// PageSnapshot is bounded page content returned to the model. Web content is
// untrusted data; the agent instruction explicitly forbids treating it as
// instructions.
type PageSnapshot struct {
	Title             string    `json:"title"`
	URL               string    `json:"url"`
	Text              string    `json:"text,omitempty"`
	Elements          []Element `json:"elements,omitempty"`
	TextTruncated     bool      `json:"text_truncated,omitempty"`
	ElementsTruncated bool      `json:"elements_truncated,omitempty"`
}

// Result is returned by Controller.Invoke. ScreenshotPNG is deliberately not
// JSON encoded; the ADK adapter saves it as an artifact instead of putting a
// potentially large base64 value in model context.
type Result struct {
	Page             PageSnapshot
	ScreenshotPNG    []byte
	ViewportWidth    int32
	ViewportHeight   int32
	SessionRecreated bool
}

// Controller is the boundary consumed by the native ADK toolset.
type Controller interface {
	Invoke(context.Context, string, Request) (Result, error)
	Reset(context.Context, string) error
}

// Options configure AgentCore Browser session management.
type Options struct {
	BrowserIdentifier string
	SessionTimeout    time.Duration
	ViewportWidth     int32
	ViewportHeight    int32
	now               func() time.Time
	connector         automationConnector
	// resolver looks up the host of a URL the browser is sent to, and is the
	// system's when nil.
	resolver safehttp.Resolver
}

// Manager owns the mapping between ADK conversations and AgentCore Browser
// sessions. Operations in one conversation are serialized because they share
// a tab, cookies, and page state. Different conversations can browse in
// parallel without sharing state.
type Manager struct {
	api        runtimeAPI
	connector  automationConnector
	resolver   safehttp.Resolver
	identifier string
	timeout    time.Duration
	width      int32
	height     int32
	now        func() time.Time

	mu       sync.Mutex
	closed   bool
	sessions map[string]*managedSession
}

type managedSession struct {
	mu         sync.Mutex
	key        string
	refs       int
	id         string
	expiresAt  time.Time
	automation automationClient
}

type startedSession struct {
	id                 string
	automationEndpoint string
}

// New builds a Browser manager using the standard AWS credential chain. AWS
// credentials are retrieved when the first browser operation starts a session.
func New(config aws.Config, options Options) (*Manager, error) {
	if options.connector == nil {
		connector, err := newSigV4Connector(config)
		if err != nil {
			return nil, err
		}
		options.connector = connector
	}
	return newManager(&runtimeClient{client: bedrockagentcore.NewFromConfig(config)}, options)
}

func newManager(api runtimeAPI, options Options) (*Manager, error) {
	if api == nil {
		return nil, errors.New("AgentCore runtime client is required")
	}
	if options.connector == nil {
		return nil, errors.New("AgentCore browser automation connector is required")
	}
	identifier := strings.TrimSpace(options.BrowserIdentifier)
	if identifier == "" {
		identifier = SystemBrowser
	}
	timeout := options.SessionTimeout
	if timeout == 0 {
		timeout = DefaultSessionTimeout
	}
	if timeout < MinSessionTimeout || timeout > MaxSessionTimeout || timeout%time.Second != 0 {
		return nil, fmt.Errorf("AgentCore browser session timeout must be a whole number of seconds between %s and %s", MinSessionTimeout, MaxSessionTimeout)
	}
	width := options.ViewportWidth
	if width == 0 {
		width = DefaultViewportWidth
	}
	height := options.ViewportHeight
	if height == 0 {
		height = DefaultViewportHeight
	}
	if width < 800 || width > 1920 {
		return nil, errors.New("AgentCore browser viewport width must be between 800 and 1920 pixels")
	}
	if height < 600 || height > 1080 {
		return nil, errors.New("AgentCore browser viewport height must be between 600 and 1080 pixels")
	}
	now := options.now
	if now == nil {
		now = time.Now
	}
	resolver := options.resolver
	if resolver == nil {
		resolver = net.DefaultResolver
	}
	return &Manager{
		api:        api,
		connector:  options.connector,
		resolver:   resolver,
		identifier: identifier,
		timeout:    timeout,
		width:      width,
		height:     height,
		now:        now,
		sessions:   map[string]*managedSession{},
	}, nil
}

// Invoke runs one operation in the remote browser assigned to sessionKey.
func (m *Manager) Invoke(ctx context.Context, sessionKey string, request Request) (Result, error) {
	if err := request.validate(ctx, m.resolver); err != nil {
		return Result{}, err
	}
	entry, err := m.acquireSession(sessionKey, true)
	if err != nil {
		return Result{}, err
	}
	defer m.releaseSession(entry)

	entry.mu.Lock()
	defer entry.mu.Unlock()

	recreated := false
	if entry.id == "" || entry.automation == nil || !m.now().Before(entry.expiresAt.Add(-sessionExpiryCushion(m.timeout))) {
		if err := m.recreateSession(ctx, entry); err != nil {
			return Result{}, err
		}
		recreated = true
	}

	result, err := entry.automation.invoke(ctx, request)
	if err != nil {
		return Result{}, fmt.Errorf("run AgentCore browser %s: %w", request.Action, err)
	}
	result.SessionRecreated = recreated
	result.ViewportWidth = m.width
	result.ViewportHeight = m.height
	return result, nil
}

func (m *Manager) recreateSession(ctx context.Context, entry *managedSession) error {
	if entry.automation != nil {
		_ = entry.automation.close()
		entry.automation = nil
	}
	if entry.id != "" {
		oldID := entry.id
		entry.id = ""
		entry.expiresAt = time.Time{}
		if err := m.api.stop(ctx, m.identifier, oldID); err != nil {
			return fmt.Errorf("stop expiring AgentCore browser %q: %w", oldID, err)
		}
	}

	startedAt := m.now()
	started, err := m.api.start(ctx, m.identifier, sessionName(entry.key), int32(m.timeout/time.Second), m.width, m.height)
	if err != nil {
		return fmt.Errorf("start AgentCore browser: %w", err)
	}
	entry.id = started.id
	entry.expiresAt = startedAt.Add(m.timeout)
	automation, err := m.connector.connect(ctx, started.automationEndpoint)
	if err != nil {
		stopErr := m.api.stop(ctx, m.identifier, started.id)
		if stopErr == nil {
			entry.id = ""
			entry.expiresAt = time.Time{}
		}
		return errors.Join(
			fmt.Errorf("connect AgentCore browser automation stream: %w", err),
			wrapError(stopErr, "stop AgentCore browser after automation connection failure"),
		)
	}
	entry.automation = automation
	return nil
}

// Reset stops and forgets one conversation's browser, including all ephemeral
// cookies and page state. The next operation starts a fresh session.
func (m *Manager) Reset(ctx context.Context, sessionKey string) error {
	entry, err := m.acquireSession(sessionKey, false)
	if err != nil || entry == nil {
		return err
	}
	defer m.releaseSession(entry)
	entry.mu.Lock()
	defer entry.mu.Unlock()
	return m.stopSession(ctx, entry)
}

// Close stops every active browser session owned by this process. AgentCore's
// TTL remains the fallback if shutdown is interrupted.
func (m *Manager) Close(ctx context.Context) error {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil
	}
	m.closed = true
	entries := make([]*managedSession, 0, len(m.sessions))
	for _, entry := range m.sessions {
		entries = append(entries, entry)
	}
	m.mu.Unlock()

	var failures []error
	for _, entry := range entries {
		entry.mu.Lock()
		if err := m.stopSession(ctx, entry); err != nil {
			failures = append(failures, err)
		}
		entry.mu.Unlock()
	}
	return errors.Join(failures...)
}

func (m *Manager) stopSession(ctx context.Context, entry *managedSession) error {
	var failures []error
	if entry.automation != nil {
		if err := entry.automation.close(); err != nil {
			failures = append(failures, fmt.Errorf("close AgentCore browser automation stream: %w", err))
		}
		entry.automation = nil
	}
	id := entry.id
	entry.id = ""
	entry.expiresAt = time.Time{}
	if id != "" {
		if err := m.api.stop(ctx, m.identifier, id); err != nil {
			failures = append(failures, fmt.Errorf("stop AgentCore browser %q: %w", id, err))
		}
	}
	return errors.Join(failures...)
}

func (m *Manager) acquireSession(key string, create bool) (*managedSession, error) {
	key = strings.TrimSpace(key)
	if key == "" {
		return nil, errors.New("ADK session key is required")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return nil, errors.New("AgentCore browser manager is closed")
	}
	now := m.now()
	for candidateKey, candidate := range m.sessions {
		if candidate.refs != 0 {
			continue
		}
		if candidate.id == "" {
			delete(m.sessions, candidateKey)
			continue
		}
		// Once the configured TTL has elapsed, AgentCore has terminated the
		// remote microVM. Close our stale WebSocket and evict the local entry so
		// old conversations cannot grow this process's session map indefinitely.
		if !candidate.expiresAt.IsZero() && !now.Before(candidate.expiresAt) {
			candidate.mu.Lock()
			if candidate.automation != nil {
				_ = candidate.automation.close()
			}
			candidate.automation = nil
			candidate.id = ""
			candidate.expiresAt = time.Time{}
			candidate.mu.Unlock()
			delete(m.sessions, candidateKey)
		}
	}
	if entry := m.sessions[key]; entry != nil {
		entry.refs++
		return entry, nil
	}
	if !create {
		return nil, nil
	}
	entry := &managedSession{key: key, refs: 1}
	m.sessions[key] = entry
	return entry, nil
}

func wrapError(err error, message string) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s: %w", message, err)
}

func (m *Manager) releaseSession(entry *managedSession) {
	m.mu.Lock()
	defer m.mu.Unlock()
	entry.refs--
	if entry.refs == 0 && entry.id == "" && m.sessions[entry.key] == entry {
		delete(m.sessions, entry.key)
	}
}

func sessionExpiryCushion(timeout time.Duration) time.Duration {
	cushion := timeout / 10
	if cushion > 30*time.Second {
		return 30 * time.Second
	}
	return cushion
}

func sessionName(key string) string {
	digest := sha256.Sum256([]byte(key))
	return fmt.Sprintf("oryxa-browser-%x", digest[:8])
}

// validate refuses a request the browser must not be sent. A navigation's host
// is looked up with resolver, and every address it answers must be public.
func (request Request) validate(ctx context.Context, resolver safehttp.Resolver) error {
	switch request.Action {
	case Navigate:
		if len(request.URL) > maxURLBytes {
			return fmt.Errorf("URL exceeds the %d-byte browser input limit", maxURLBytes)
		}
		parsed, err := url.ParseRequestURI(strings.TrimSpace(request.URL))
		if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
			return errors.New("browser URL must be an absolute http or https URL")
		}
		if parsed.User != nil {
			return errors.New("browser URLs cannot contain credentials")
		}
		if err := checkHost(ctx, resolver, parsed.Hostname()); err != nil {
			return err
		}
	case Click:
		if err := validateSelector(request.Selector); err != nil {
			return err
		}
	case Fill:
		if err := validateSelector(request.Selector); err != nil {
			return err
		}
		if len(request.Text) > maxFillBytes {
			return fmt.Errorf("fill text exceeds the %d-byte browser input limit", maxFillBytes)
		}
		if strings.ContainsRune(request.Text, '\x00') || !utf8.ValidString(request.Text) {
			return errors.New("fill text must be valid UTF-8 without null bytes")
		}
	case Snapshot, Screenshot:
	default:
		return fmt.Errorf("unsupported AgentCore browser action %q", request.Action)
	}
	return nil
}

func validateSelector(selector string) error {
	if strings.TrimSpace(selector) == "" {
		return errors.New("CSS selector is required")
	}
	if len(selector) > maxSelectorBytes {
		return fmt.Errorf("CSS selector exceeds the %d-byte browser input limit", maxSelectorBytes)
	}
	if strings.ContainsRune(selector, '\x00') || !utf8.ValidString(selector) {
		return errors.New("CSS selector must be valid UTF-8 without null bytes")
	}
	return nil
}

type runtimeAPI interface {
	start(context.Context, string, string, int32, int32, int32) (startedSession, error)
	stop(context.Context, string, string) error
}

type runtimeClient struct {
	client *bedrockagentcore.Client
}

func (r *runtimeClient) start(ctx context.Context, identifier, name string, timeout, width, height int32) (startedSession, error) {
	output, err := r.client.StartBrowserSession(ctx, &bedrockagentcore.StartBrowserSessionInput{
		BrowserIdentifier:     aws.String(identifier),
		Name:                  aws.String(name),
		SessionTimeoutSeconds: aws.Int32(timeout),
		ViewPort: &types.ViewPort{
			Width:  aws.Int32(width),
			Height: aws.Int32(height),
		},
	})
	if err != nil {
		return startedSession{}, err
	}
	if output.SessionId == nil || *output.SessionId == "" {
		return startedSession{}, errors.New("AgentCore returned an empty browser session ID")
	}
	if output.Streams == nil || output.Streams.AutomationStream == nil || output.Streams.AutomationStream.StreamEndpoint == nil || *output.Streams.AutomationStream.StreamEndpoint == "" {
		return startedSession{}, errors.New("AgentCore returned no browser automation endpoint")
	}
	return startedSession{id: *output.SessionId, automationEndpoint: *output.Streams.AutomationStream.StreamEndpoint}, nil
}

func (r *runtimeClient) stop(ctx context.Context, identifier, sessionID string) error {
	_, err := r.client.StopBrowserSession(ctx, &bedrockagentcore.StopBrowserSessionInput{
		BrowserIdentifier: aws.String(identifier),
		SessionId:         aws.String(sessionID),
	})
	return err
}

type automationConnector interface {
	connect(context.Context, string) (automationClient, error)
}

type automationClient interface {
	invoke(context.Context, Request) (Result, error)
	close() error
}
