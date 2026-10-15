// Package cfsandbox gives Krude its sandbox_* tools on Cloudflare (live-layer
// A4): the same tools, arguments and result shape as agentcoresandbox,
// served by the room's SandboxDO on the oryxa-edge Worker through
// edgeclient. Each conversation is a thread room with one shared sandbox, as
// each had one AgentCore session.
package cfsandbox

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/edgeclient"
	edgev1 "github.com/Shubham04-oss/oryxa-go/contracts/edge/v1"
)

const (
	// The limits agentcoresandbox checks before a call, so the model gets
	// the same refusals in the same words.
	maxCodeInputBytes = 1024 * 1024
	maxCommandBytes   = 64 * 1024
	maxPathBytes      = 4 * 1024
	maxPathsPerCall   = 100
	maxFilesPerCall   = 20
	maxFileInputBytes = 5 * 1024 * 1024
	// maxArtifactBytes is what sandbox_save_artifact may move in one read,
	// matching agentcoresandbox; the edge caps artifacts at 64 MiB.
	maxArtifactBytes = 25 * 1024 * 1024

	// execTimeout is how long a run may take on the edge, and callSlack
	// what the call adds for the container to start and answer.
	execTimeout = 120 * time.Second
	callSlack   = 90 * time.Second
	fileTimeout = 60 * time.Second
)

// Action is one sandbox operation.
type Action string

const (
	ExecuteCode    Action = "executeCode"
	ExecuteCommand Action = "executeCommand"
	ReadFiles      Action = "readFiles"
	ListFiles      Action = "listFiles"
	RemoveFiles    Action = "removeFiles"
	WriteFiles     Action = "writeFiles"
)

// File is text written to a path in the sandbox.
type File struct {
	Path string
	Text string
}

// Request is one sandbox operation's input.
type Request struct {
	Action        Action
	Code          string
	Command       string
	Language      string
	ClearContext  bool
	DirectoryPath string
	Paths         []string
	Files         []File
	// MaxOutputBytes raises a file read's budget for a caller whose bytes never
	// reach the model; zero keeps the edge's model budget.
	MaxOutputBytes int
}

// Content is one output block, in agentcoresandbox's shape: text, an image
// as base64, a file listing's resource_link, or a file read's resource.
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

// Result is what a sandbox tool returns to the model, in agentcoresandbox's
// JSON shape so prompts and agents read it the same.
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

// Caller is the edge API this package uses; *edgeclient.Client satisfies it.
type Caller interface {
	Do(context.Context, edgeclient.Call, any) error
}

// Executor runs sandbox operations in a target room's shared sandbox.
type Executor struct {
	edge Caller
}

// New returns an Executor that calls edge.
func New(edge Caller) (*Executor, error) {
	if edge == nil {
		return nil, errors.New("cfsandbox: an edge client is required")
	}
	return &Executor{edge: edge}, nil
}

func (executor *Executor) call(ctx context.Context, target edgeclient.Target, method, suffix string, query url.Values, body, out any, timeout time.Duration, idempotent bool) error {
	path, err := edgev1.SandboxPath(target.Room, edgev1.SharedSandbox)
	if err != nil {
		return err
	}
	return executor.edge.Do(ctx, edgeclient.Call{
		Claims:     target.Claims(edgev1.ScopeSandboxExec, edgev1.ScopeSandboxFiles),
		Method:     method,
		Path:       path + suffix,
		Query:      query,
		Body:       body,
		Timeout:    timeout,
		Idempotent: idempotent,
	}, out)
}

// Invoke runs request in target's sandbox.
func (executor *Executor) Invoke(ctx context.Context, target edgeclient.Target, request Request) (Result, error) {
	if err := request.validate(); err != nil {
		return Result{}, err
	}
	switch request.Action {
	case ExecuteCode:
		var executed edgev1.ExecResult
		err := executor.call(ctx, target, http.MethodPost, "/exec/code", nil, edgev1.ExecCodeRequest{
			Language: request.Language, Code: request.Code, TimeoutMs: execTimeout.Milliseconds(), ClearContext: request.ClearContext,
		}, &executed, execTimeout+callSlack, false)
		if err != nil {
			return Result{}, sandboxError(err)
		}
		return execResult(executed), nil
	case ExecuteCommand:
		var executed edgev1.ExecResult
		err := executor.call(ctx, target, http.MethodPost, "/exec/command", nil, edgev1.ExecCommandRequest{
			Command: request.Command, TimeoutMs: execTimeout.Milliseconds(),
		}, &executed, execTimeout+callSlack, false)
		if err != nil {
			return Result{}, sandboxError(err)
		}
		return execResult(executed), nil
	case ListFiles:
		var listed edgev1.ListFilesResponse
		query := url.Values{}
		if request.DirectoryPath != "" {
			query.Set("path", request.DirectoryPath)
		}
		if err := executor.call(ctx, target, http.MethodGet, "/files", query, nil, &listed, fileTimeout+callSlack, true); err != nil {
			return Result{}, sandboxError(err)
		}
		return listResult(listed), nil
	case ReadFiles:
		var read edgev1.ReadFilesResponse
		if err := executor.call(ctx, target, http.MethodPost, "/files/read", nil, edgev1.ReadFilesRequest{Paths: request.Paths, MaxBytes: request.MaxOutputBytes}, &read, fileTimeout+callSlack, true); err != nil {
			return Result{}, sandboxError(err)
		}
		return readResult(read), nil
	case WriteFiles:
		files := make([]edgev1.WriteFile, 0, len(request.Files))
		for _, file := range request.Files {
			files = append(files, edgev1.WriteFile{Path: file.Path, Content: file.Text})
		}
		var written edgev1.WriteFilesResponse
		if err := executor.call(ctx, target, http.MethodPost, "/files/write", nil, edgev1.WriteFilesRequest{Files: files}, &written, fileTimeout+callSlack, true); err != nil {
			return Result{}, sandboxError(err)
		}
		paths := make([]string, 0, len(written.Written))
		for _, file := range written.Written {
			paths = append(paths, file.Path)
		}
		return Result{Content: []Content{{Type: "text", Text: "Successfully wrote " + strings.Join(paths, ", ")}}}, nil
	case RemoveFiles:
		var removed edgev1.RemoveFilesResponse
		if err := executor.call(ctx, target, http.MethodPost, "/files/remove", nil, edgev1.RemoveFilesRequest{Paths: request.Paths}, &removed, fileTimeout+callSlack, false); err != nil {
			return Result{}, sandboxError(err)
		}
		return Result{Content: []Content{{Type: "text", Text: "Successfully removed " + strings.Join(removed.Removed, ", ")}}}, nil
	}
	return Result{}, fmt.Errorf("unsupported sandbox action %q", request.Action)
}

// Reset stops target's sandbox and clears its files.
func (executor *Executor) Reset(ctx context.Context, target edgeclient.Target) error {
	var reset edgev1.ResetResponse
	if err := executor.call(ctx, target, http.MethodPost, "/reset", nil, nil, &reset, fileTimeout+callSlack, true); err != nil {
		return sandboxError(err)
	}
	return nil
}

func execResult(executed edgev1.ExecResult) Result {
	exitCode := int32(executed.ExitCode)
	duration := float64(executed.DurationMs)
	result := Result{
		IsError:          executed.ExitCode != 0 || executed.TimedOut,
		Stdout:           executed.Stdout,
		Stderr:           executed.Stderr,
		ExitCode:         &exitCode,
		ExecutionTimeMS:  &duration,
		SessionRecreated: executed.SessionRecreated,
		OutputTruncated:  executed.OutputTruncated,
	}
	if executed.TimedOut {
		result.TaskStatus = "TIMED_OUT"
		result.Stderr = strings.TrimRight(result.Stderr+"\n", "\n") + fmt.Sprintf("\nThe run was stopped at its %s limit.", execTimeout)
	}
	if executed.Stdout != "" {
		result.Content = append(result.Content, Content{Type: "text", Text: executed.Stdout})
	}
	for _, output := range executed.Results {
		switch output.Type {
		case "image":
			result.Content = append(result.Content, Content{Type: "image", DataBase64: output.Data, MIMEType: output.MIMEType, Name: output.Name})
		default:
			if output.Text != "" {
				result.Content = append(result.Content, Content{Type: "text", Text: output.Text, Name: output.Name})
			}
		}
	}
	return result
}

func listResult(listed edgev1.ListFilesResponse) Result {
	result := Result{OutputTruncated: listed.Truncated}
	for _, entry := range listed.Entries {
		description := entry.Type
		result.Content = append(result.Content, Content{
			Type: "resource_link", Name: entry.Name, URI: "file://" + entry.Path, Description: description, Size: entry.Size,
		})
	}
	return result
}

func readResult(read edgev1.ReadFilesResponse) Result {
	result := Result{}
	for _, file := range read.Files {
		if file.Error != "" {
			result.IsError = true
			result.Content = append(result.Content, Content{Type: "text", Name: file.Path, Text: file.Path + ": " + file.Error})
			continue
		}
		size := file.Size
		block := Content{Type: "resource", Name: file.Path, MIMEType: file.MIMEType, ResourceURI: "file://" + file.Path, Size: &size}
		if file.Encoding == edgev1.EncodingBase64 {
			block.ResourceDataBase64 = file.Content
		} else {
			block.ResourceText = file.Content
		}
		result.OutputTruncated = result.OutputTruncated || file.Truncated
		result.Content = append(result.Content, block)
	}
	return result
}

// sandboxError says in plain words what an edge refusal means for the
// model; anything else passes through.
func sandboxError(err error) error {
	var edgeErr *edgeclient.Error
	if !errors.As(err, &edgeErr) {
		return fmt.Errorf("sandbox: %w", err)
	}
	switch edgeErr.Code {
	case edgev1.CodeLimit:
		return fmt.Errorf("sandbox: this room already runs as many sandboxes as it may; %s", edgeErr.Message)
	case edgev1.CodeUnavailable:
		return fmt.Errorf("sandbox: the sandbox is unavailable right now, try again shortly (%s)", edgeErr.Message)
	default:
		return fmt.Errorf("sandbox: %s", edgeErr.Message)
	}
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
		total := 0
		for _, file := range request.Files {
			if err := validatePath(file.Path, false); err != nil {
				return err
			}
			total += len(file.Text)
			if total > maxFileInputBytes {
				return fmt.Errorf("file content exceeds the %d-byte sandbox input limit", maxFileInputBytes)
			}
		}
	default:
		return fmt.Errorf("unsupported sandbox action %q", request.Action)
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
