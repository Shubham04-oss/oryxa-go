// Package agentcoresandbox connects ADK tools to Amazon Bedrock AgentCore's
// managed Code Interpreter. Nothing runs on the primary service's host.
package agentcoresandbox

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/bedrockagentcore"
	"github.com/aws/aws-sdk-go-v2/service/bedrockagentcore/types"
)

const (
	// SystemCodeInterpreter is AWS's built-in Code Interpreter. A provisioned
	// custom interpreter identifier can be supplied instead for stricter network
	// and IAM controls.
	SystemCodeInterpreter = "aws.codeinterpreter.v1"

	DefaultSessionTimeout = 15 * time.Minute
	MinSessionTimeout     = time.Minute
	MaxSessionTimeout     = 8 * time.Hour

	// A tool result is fed back into the model. Bound returned text and binary
	// data independently of AgentCore's much larger transport limits so one tool
	// call cannot consume the whole model context.
	maxModelOutputBytes = 256 * 1024
	// maxArtifactBytes bounds a file moved straight to the artifact store
	// (sandbox_save_artifact): its bytes never reach the model, so the model
	// budget above does not apply. A deck with images is well past 256 KiB.
	maxArtifactBytes  = 25 * 1024 * 1024
	maxCodeInputBytes = 1024 * 1024
	maxCommandBytes   = 64 * 1024
	maxPathBytes      = 4 * 1024
	maxPathsPerCall   = 100
	maxFilesPerCall   = 20
	maxFileInputBytes = 5 * 1024 * 1024
)

// Action is one synchronous Code Interpreter operation exposed to ADK.
type Action string

const (
	ExecuteCode    Action = "executeCode"
	ExecuteCommand Action = "executeCommand"
	ReadFiles      Action = "readFiles"
	ListFiles      Action = "listFiles"
	RemoveFiles    Action = "removeFiles"
	WriteFiles     Action = "writeFiles"
)

// File is text content written to a path inside an AgentCore session.
type File struct {
	Path string
	Text string
}

// Request is a framework-independent Code Interpreter request.
type Request struct {
	Action        Action
	Code          string
	Command       string
	Language      string
	ClearContext  bool
	DirectoryPath string
	Paths         []string
	Files         []File
	// MaxOutputBytes bounds what the result carries; zero is the model's
	// budget, maxModelOutputBytes.
	MaxOutputBytes int
}

// Content is one output block returned by AgentCore. Binary content is base64
// encoded because ADK function results are JSON.
type Content struct {
	Type               string `json:"type"`
	Text               string `json:"text,omitempty"`
	DataBase64         string `json:"data_base64,omitempty"`
	Name               string `json:"name,omitempty"`
	MIMEType           string `json:"mime_type,omitempty"`
	URI                string `json:"uri,omitempty"`
	Description        string `json:"description,omitempty"`
	Size               *int64 `json:"size,omitempty"`
	ResourceText       string `json:"resource_text,omitempty"`
	ResourceDataBase64 string `json:"resource_data_base64,omitempty"`
	ResourceURI        string `json:"resource_uri,omitempty"`
}

// Result is the JSON-safe aggregate of AgentCore's streamed result events.
type Result struct {
	IsError          bool      `json:"is_error"`
	Content          []Content `json:"content,omitempty"`
	Stdout           string    `json:"stdout,omitempty"`
	Stderr           string    `json:"stderr,omitempty"`
	ExitCode         *int32    `json:"exit_code,omitempty"`
	ExecutionTimeMS  *float64  `json:"execution_time_ms,omitempty"`
	TaskID           string    `json:"task_id,omitempty"`
	TaskStatus       string    `json:"task_status,omitempty"`
	SessionRecreated bool      `json:"session_recreated,omitempty"`
	OutputTruncated  bool      `json:"output_truncated,omitempty"`
}

// Executor is the boundary consumed by the ADK toolset. Manager is its AWS
// implementation; the interface keeps the tools deterministic in tests.
type Executor interface {
	Invoke(context.Context, string, Request) (Result, error)
	Reset(context.Context, string) error
}

// Options configure AgentCore session management.
type Options struct {
	CodeInterpreterIdentifier string
	SessionTimeout            time.Duration
	now                       func() time.Time
}

// Manager owns the mapping between ADK conversations and AgentCore sessions.
// Calls in one conversation are serialized because they share interpreter
// context and a filesystem; different conversations can run concurrently.
type Manager struct {
	api        runtimeAPI
	identifier string
	timeout    time.Duration
	now        func() time.Time

	mu       sync.Mutex
	closed   bool
	sessions map[string]*managedSession
}

type managedSession struct {
	mu        sync.Mutex
	key       string
	refs      int
	id        string
	expiresAt time.Time
}

// New builds a session manager using the standard AWS credential chain.
func New(config aws.Config, options Options) (*Manager, error) {
	return newManager(&runtimeClient{client: bedrockagentcore.NewFromConfig(config)}, options)
}

func newManager(api runtimeAPI, options Options) (*Manager, error) {
	if api == nil {
		return nil, errors.New("AgentCore runtime client is required")
	}
	identifier := strings.TrimSpace(options.CodeInterpreterIdentifier)
	if identifier == "" {
		identifier = SystemCodeInterpreter
	}
	timeout := options.SessionTimeout
	if timeout == 0 {
		timeout = DefaultSessionTimeout
	}
	if timeout < MinSessionTimeout || timeout > MaxSessionTimeout || timeout%time.Second != 0 {
		return nil, fmt.Errorf("AgentCore session timeout must be a whole number of seconds between %s and %s", MinSessionTimeout, MaxSessionTimeout)
	}
	now := options.now
	if now == nil {
		now = time.Now
	}
	return &Manager{
		api:        api,
		identifier: identifier,
		timeout:    timeout,
		now:        now,
		sessions:   map[string]*managedSession{},
	}, nil
}

// Invoke runs one operation in the AgentCore session assigned to sessionKey.
func (m *Manager) Invoke(ctx context.Context, sessionKey string, request Request) (Result, error) {
	if err := request.validate(); err != nil {
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
	if entry.id == "" || !m.now().Before(entry.expiresAt.Add(-sessionExpiryCushion(m.timeout))) {
		started, err := m.api.start(ctx, m.identifier, sessionName(sessionKey), int32(m.timeout/time.Second))
		if err != nil {
			return Result{}, fmt.Errorf("start AgentCore sandbox: %w", err)
		}
		entry.id = started
		entry.expiresAt = m.now().Add(m.timeout)
		recreated = true
	}

	results, err := m.api.invoke(ctx, m.identifier, entry.id, request)
	if err != nil {
		return Result{}, fmt.Errorf("invoke AgentCore sandbox: %w", err)
	}
	limit := request.MaxOutputBytes
	if limit <= 0 {
		limit = maxModelOutputBytes
	}
	result := aggregate(results, limit)
	result.SessionRecreated = recreated
	return result, nil
}

// Reset stops and forgets one conversation's sandbox. Its next tool call gets
// a fresh filesystem and execution context.
func (m *Manager) Reset(ctx context.Context, sessionKey string) error {
	entry, err := m.acquireSession(sessionKey, false)
	if err != nil || entry == nil {
		return err
	}
	defer m.releaseSession(entry)
	entry.mu.Lock()
	defer entry.mu.Unlock()
	if entry.id == "" {
		return nil
	}
	id := entry.id
	entry.id = ""
	entry.expiresAt = time.Time{}
	if err := m.api.stop(ctx, m.identifier, id); err != nil {
		return fmt.Errorf("stop AgentCore sandbox: %w", err)
	}
	return nil
}

// Close stops every active sandbox owned by this process. AgentCore's TTL is
// the fallback if shutdown is interrupted.
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
		id := entry.id
		entry.id = ""
		entry.expiresAt = time.Time{}
		if id != "" {
			if err := m.api.stop(ctx, m.identifier, id); err != nil {
				failures = append(failures, fmt.Errorf("stop AgentCore sandbox %q: %w", id, err))
			}
		}
		entry.mu.Unlock()
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
		return nil, errors.New("AgentCore sandbox manager is closed")
	}
	now := m.now()
	for candidateKey, candidate := range m.sessions {
		if candidate.refs == 0 && (candidate.id == "" || !now.Before(candidate.expiresAt)) {
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
	return fmt.Sprintf("oryxa-%x", digest[:8])
}

func (request Request) validate() error {
	switch request.Action {
	case ExecuteCode:
		if request.Code == "" {
			return errors.New("code is required")
		}
		if len(request.Code) > maxCodeInputBytes {
			return fmt.Errorf("code exceeds the %d-byte sandbox input limit", maxCodeInputBytes)
		}
		switch request.Language {
		case "python", "javascript", "typescript":
		default:
			return fmt.Errorf("language must be python, javascript or typescript, got %q", request.Language)
		}
	case ExecuteCommand:
		if request.Command == "" {
			return errors.New("command is required")
		}
		if len(request.Command) > maxCommandBytes {
			return fmt.Errorf("command exceeds the %d-byte sandbox input limit", maxCommandBytes)
		}
	case ListFiles:
		// The empty directory names the sandbox's working directory.
		if err := validatePath(request.DirectoryPath, true); err != nil {
			return err
		}
	case ReadFiles, RemoveFiles:
		if len(request.Paths) == 0 {
			return errors.New("at least one path is required")
		}
		if len(request.Paths) > maxPathsPerCall {
			return fmt.Errorf("a sandbox call accepts at most %d paths", maxPathsPerCall)
		}
		for _, path := range request.Paths {
			if err := validatePath(path, false); err != nil {
				return err
			}
		}
	case WriteFiles:
		if len(request.Files) == 0 {
			return errors.New("at least one file is required")
		}
		if len(request.Files) > maxFilesPerCall {
			return fmt.Errorf("a sandbox call accepts at most %d files", maxFilesPerCall)
		}
		totalBytes := 0
		for _, file := range request.Files {
			if err := validatePath(file.Path, false); err != nil {
				return err
			}
			totalBytes += len(file.Text)
			if totalBytes > maxFileInputBytes {
				return fmt.Errorf("file content exceeds the %d-byte sandbox input limit", maxFileInputBytes)
			}
		}
	default:
		return fmt.Errorf("unsupported AgentCore sandbox action %q", request.Action)
	}
	return nil
}

func validatePath(path string, allowEmpty bool) error {
	if !allowEmpty && strings.TrimSpace(path) == "" {
		return errors.New("file paths cannot be blank")
	}
	if len(path) > maxPathBytes {
		return fmt.Errorf("sandbox paths cannot exceed %d bytes", maxPathBytes)
	}
	if strings.ContainsRune(path, '\x00') {
		return errors.New("sandbox paths cannot contain a null byte")
	}
	return nil
}

type runtimeAPI interface {
	start(context.Context, string, string, int32) (string, error)
	invoke(context.Context, string, string, Request) ([]types.CodeInterpreterResult, error)
	stop(context.Context, string, string) error
}

type runtimeClient struct {
	client *bedrockagentcore.Client
}

func (r *runtimeClient) start(ctx context.Context, identifier, name string, timeout int32) (string, error) {
	output, err := r.client.StartCodeInterpreterSession(ctx, &bedrockagentcore.StartCodeInterpreterSessionInput{
		CodeInterpreterIdentifier: aws.String(identifier),
		Name:                      aws.String(name),
		SessionTimeoutSeconds:     aws.Int32(timeout),
	})
	if err != nil {
		return "", err
	}
	if output.SessionId == nil || *output.SessionId == "" {
		return "", errors.New("AgentCore returned an empty session ID")
	}
	return *output.SessionId, nil
}

func (r *runtimeClient) invoke(ctx context.Context, identifier, sessionID string, request Request) ([]types.CodeInterpreterResult, error) {
	input := &bedrockagentcore.InvokeCodeInterpreterInput{
		CodeInterpreterIdentifier: aws.String(identifier),
		SessionId:                 aws.String(sessionID),
		Name:                      types.ToolName(request.Action),
		Arguments:                 request.arguments(),
	}
	output, err := r.client.InvokeCodeInterpreter(ctx, input)
	if err != nil {
		return nil, err
	}
	stream := output.GetStream()
	if stream == nil || stream.Reader == nil {
		return nil, errors.New("AgentCore returned no result stream")
	}
	var results []types.CodeInterpreterResult
	for event := range stream.Events() {
		switch event := event.(type) {
		case *types.CodeInterpreterStreamOutputMemberResult:
			results = append(results, event.Value)
		default:
			_ = stream.Close()
			return nil, fmt.Errorf("AgentCore returned an unknown result event %T", event)
		}
	}
	if err := stream.Close(); err != nil {
		return nil, err
	}
	return results, nil
}

func (r *runtimeClient) stop(ctx context.Context, identifier, sessionID string) error {
	_, err := r.client.StopCodeInterpreterSession(ctx, &bedrockagentcore.StopCodeInterpreterSessionInput{
		CodeInterpreterIdentifier: aws.String(identifier),
		SessionId:                 aws.String(sessionID),
	})
	return err
}

func (request Request) arguments() *types.ToolArguments {
	arguments := &types.ToolArguments{}
	switch request.Action {
	case ExecuteCode:
		arguments.Code = aws.String(request.Code)
		arguments.Language = types.ProgrammingLanguage(request.Language)
		arguments.ClearContext = aws.Bool(request.ClearContext)
	case ExecuteCommand:
		arguments.Command = aws.String(request.Command)
	case ListFiles:
		arguments.DirectoryPath = aws.String(request.DirectoryPath)
	case ReadFiles, RemoveFiles:
		arguments.Paths = append([]string(nil), request.Paths...)
	case WriteFiles:
		arguments.Content = make([]types.InputContentBlock, 0, len(request.Files))
		for _, file := range request.Files {
			arguments.Content = append(arguments.Content, types.InputContentBlock{
				Path: aws.String(file.Path),
				Text: aws.String(file.Text),
			})
		}
	}
	return arguments
}

func aggregate(events []types.CodeInterpreterResult, limit int) Result {
	result := Result{}
	remaining := limit
	for _, event := range events {
		result.IsError = result.IsError || aws.ToBool(event.IsError)
		for _, block := range event.Content {
			result.Content = append(result.Content, content(block, &remaining, &result.OutputTruncated))
		}
		if structured := event.StructuredContent; structured != nil {
			result.Stdout += limitedText(aws.ToString(structured.Stdout), &remaining, &result.OutputTruncated)
			result.Stderr += limitedText(aws.ToString(structured.Stderr), &remaining, &result.OutputTruncated)
			result.ExitCode = structured.ExitCode
			result.ExecutionTimeMS = structured.ExecutionTime
			result.TaskID = aws.ToString(structured.TaskId)
			result.TaskStatus = string(structured.TaskStatus)
		}
	}
	return result
}

func content(block types.ContentBlock, remaining *int, truncated *bool) Content {
	result := Content{
		Type:        string(block.Type),
		Name:        aws.ToString(block.Name),
		MIMEType:    aws.ToString(block.MimeType),
		URI:         aws.ToString(block.Uri),
		Description: aws.ToString(block.Description),
		Size:        block.Size,
	}
	result.Text = limitedText(aws.ToString(block.Text), remaining, truncated)
	result.DataBase64 = limitedData(block.Data, remaining, truncated)
	if block.Resource != nil {
		result.ResourceText = limitedText(aws.ToString(block.Resource.Text), remaining, truncated)
		result.ResourceDataBase64 = limitedData(block.Resource.Blob, remaining, truncated)
		result.ResourceURI = aws.ToString(block.Resource.Uri)
		if result.MIMEType == "" {
			result.MIMEType = aws.ToString(block.Resource.MimeType)
		}
	}
	return result
}

func limitedText(value string, remaining *int, truncated *bool) string {
	if value == "" {
		return ""
	}
	if *remaining <= 0 {
		*truncated = true
		return ""
	}
	if len(value) <= *remaining {
		*remaining -= len(value)
		return value
	}
	value = value[:*remaining]
	for !utf8.ValidString(value) {
		value = value[:len(value)-1]
	}
	*remaining = 0
	*truncated = true
	return value
}

func limitedData(value []byte, remaining *int, truncated *bool) string {
	if len(value) == 0 {
		return ""
	}
	if *remaining <= 0 {
		*truncated = true
		return ""
	}
	available := (*remaining / 4) * 3
	for available > 0 && base64.StdEncoding.EncodedLen(available) > *remaining {
		available--
	}
	if len(value) > available {
		value = value[:available]
		*truncated = true
	}
	encoded := base64.StdEncoding.EncodeToString(value)
	*remaining -= len(encoded)
	return encoded
}
