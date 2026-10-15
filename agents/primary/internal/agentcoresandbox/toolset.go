package agentcoresandbox

import (
	"encoding/base64"
	"errors"
	"fmt"
	"path"
	"strings"

	"google.golang.org/genai"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/tool"
	"google.golang.org/adk/v2/tool/functiontool"
)

// NewToolset exposes the managed sandbox through native ADK function tools.
// The caller can put this toolset in Krude's dynamically loaded sandbox
// toolbox, so none of these declarations consume model context until needed.
func NewToolset(executor Executor) (tool.Toolset, error) {
	if executor == nil {
		return nil, errors.New("sandbox executor is required")
	}
	tools := make([]tool.Tool, 0, 8)
	for _, build := range []func() (tool.Tool, error){
		func() (tool.Tool, error) { return executeCodeTool(executor) },
		func() (tool.Tool, error) { return executeCommandTool(executor) },
		func() (tool.Tool, error) { return listFilesTool(executor) },
		func() (tool.Tool, error) { return readFilesTool(executor) },
		func() (tool.Tool, error) { return writeFilesTool(executor) },
		func() (tool.Tool, error) { return removeFilesTool(executor) },
		func() (tool.Tool, error) { return resetTool(executor) },
		func() (tool.Tool, error) { return saveArtifactTool(executor) },
	} {
		built, err := build()
		if err != nil {
			return nil, err
		}
		tools = append(tools, built)
	}
	return fixedToolset{name: "agentcore_sandbox", tools: tools}, nil
}

type fixedToolset struct {
	name  string
	tools []tool.Tool
}

func (s fixedToolset) Name() string { return s.name }

func (s fixedToolset) Tools(agent.ReadonlyContext) ([]tool.Tool, error) {
	return append([]tool.Tool(nil), s.tools...), nil
}

type executeCodeArgs struct {
	Language     string `json:"language,omitempty" jsonschema:"Programming language: python (default), javascript, or typescript."`
	Code         string `json:"code" jsonschema:"Complete source code to execute inside the isolated AgentCore sandbox."`
	ClearContext bool   `json:"clear_context,omitempty" jsonschema:"Clear the language execution context before running this code. Files remain available."`
}

func executeCodeTool(executor Executor) (tool.Tool, error) {
	return functiontool.New(functiontool.Config{
		Name:        "sandbox_execute_code",
		Description: "Executes Python, JavaScript or TypeScript in the AWS AgentCore sandbox assigned to this conversation. State and files persist for the sandbox session, never on the service host.",
	}, func(ctx agent.Context, args executeCodeArgs) (Result, error) {
		language := args.Language
		if language == "" {
			language = "python"
		}
		return executor.Invoke(ctx, sandboxKey(ctx), Request{
			Action:       ExecuteCode,
			Language:     language,
			Code:         args.Code,
			ClearContext: args.ClearContext,
		})
	})
}

type executeCommandArgs struct {
	Command string `json:"command" jsonschema:"Shell command to execute inside the isolated AgentCore sandbox, never on the primary service host."`
}

func executeCommandTool(executor Executor) (tool.Tool, error) {
	return functiontool.New(functiontool.Config{
		Name:        "sandbox_execute_command",
		Description: "Executes a shell command inside the AWS AgentCore sandbox assigned to this conversation. Use it for packages and file transformations; it cannot access the primary service host.",
	}, func(ctx agent.Context, args executeCommandArgs) (Result, error) {
		return executor.Invoke(ctx, sandboxKey(ctx), Request{Action: ExecuteCommand, Command: args.Command})
	})
}

type listFilesArgs struct {
	DirectoryPath string `json:"directory_path,omitempty" jsonschema:"Directory inside the sandbox to list. Empty means its working directory."`
}

func listFilesTool(executor Executor) (tool.Tool, error) {
	return functiontool.New(functiontool.Config{
		Name:        "sandbox_list_files",
		Description: "Lists files in a directory inside this conversation's AWS AgentCore sandbox.",
	}, func(ctx agent.Context, args listFilesArgs) (Result, error) {
		return executor.Invoke(ctx, sandboxKey(ctx), Request{Action: ListFiles, DirectoryPath: args.DirectoryPath})
	})
}

type readFilesArgs struct {
	Paths []string `json:"paths" jsonschema:"One or more file paths inside this conversation's sandbox."`
}

func readFilesTool(executor Executor) (tool.Tool, error) {
	return functiontool.New(functiontool.Config{
		Name:        "sandbox_read_files",
		Description: "Reads one or more files from this conversation's AWS AgentCore sandbox. Binary data is returned as base64 with its MIME type.",
	}, func(ctx agent.Context, args readFilesArgs) (Result, error) {
		return executor.Invoke(ctx, sandboxKey(ctx), Request{Action: ReadFiles, Paths: args.Paths})
	})
}

type writeFileArgs struct {
	Path string `json:"path" jsonschema:"Destination path inside this conversation's sandbox."`
	Text string `json:"text" jsonschema:"UTF-8 text to write to the file."`
}

type writeFilesArgs struct {
	Files []writeFileArgs `json:"files" jsonschema:"Text files to write inside this conversation's sandbox."`
}

func writeFilesTool(executor Executor) (tool.Tool, error) {
	return functiontool.New(functiontool.Config{
		Name:        "sandbox_write_files",
		Description: "Writes text files into this conversation's AWS AgentCore sandbox. It never writes to the primary service host.",
	}, func(ctx agent.Context, args writeFilesArgs) (Result, error) {
		files := make([]File, 0, len(args.Files))
		for _, file := range args.Files {
			files = append(files, File{Path: file.Path, Text: file.Text})
		}
		return executor.Invoke(ctx, sandboxKey(ctx), Request{Action: WriteFiles, Files: files})
	})
}

type removeFilesArgs struct {
	Paths []string `json:"paths" jsonschema:"One or more file paths to remove from this conversation's sandbox."`
}

func removeFilesTool(executor Executor) (tool.Tool, error) {
	return functiontool.New(functiontool.Config{
		Name:                "sandbox_remove_files",
		Description:         "Removes files from this conversation's ephemeral AWS AgentCore sandbox after member confirmation.",
		RequireConfirmation: true,
	}, func(ctx agent.Context, args removeFilesArgs) (Result, error) {
		return executor.Invoke(ctx, sandboxKey(ctx), Request{Action: RemoveFiles, Paths: args.Paths})
	})
}

type resetArgs struct{}

type resetResult struct {
	Reset bool `json:"reset"`
}

func resetTool(executor Executor) (tool.Tool, error) {
	return functiontool.New(functiontool.Config{
		Name:                "sandbox_reset",
		Description:         "Stops this conversation's AWS AgentCore sandbox and permanently clears its execution context and files after member confirmation.",
		RequireConfirmation: true,
	}, func(ctx agent.Context, _ resetArgs) (resetResult, error) {
		if err := executor.Reset(ctx, sandboxKey(ctx)); err != nil {
			return resetResult{}, err
		}
		return resetResult{Reset: true}, nil
	})
}

type saveArtifactArgs struct {
	Path     string `json:"path" jsonschema:"File path inside this conversation's sandbox to keep as an artifact."`
	MIMEType string `json:"mime_type,omitempty" jsonschema:"Overrides the MIME type the sandbox reports for this file. Usually left unset."`
}

type sandboxSavedArtifact struct {
	ArtifactName     string `json:"artifact_name"`
	ArtifactVersion  int64  `json:"artifact_version"`
	MIMEType         string `json:"mime_type"`
	SizeBytes        int    `json:"size_bytes"`
	SessionRecreated bool   `json:"session_recreated,omitempty"`
}

// saveArtifactTool keeps a sandbox file as an ADK artifact directly, sandbox
// to artifact service, without its bytes ever passing through you as text:
// a real document or image is safer moved this way than retyped through
// sandbox_read_files and save_artifact, which asks you to reproduce
// arbitrarily long base64 character for character and has no way to notice
// if you do not. Bound by the same output budget as sandbox_read_files, so a
// file past that has to be split in the sandbox first.
func saveArtifactTool(executor Executor) (tool.Tool, error) {
	return functiontool.New(functiontool.Config{
		Name:        "sandbox_save_artifact",
		Description: "Saves a file already in this conversation's sandbox as an artifact, so it outlives the sandbox — its bytes go straight from the sandbox to the artifact store, never through you. Prefer this over sandbox_read_files followed by save_artifact for anything but a small text file. Read it back with load_artifacts, using the name this returns.",
	}, func(ctx agent.Context, args saveArtifactArgs) (sandboxSavedArtifact, error) {
		if ctx.Artifacts() == nil {
			return sandboxSavedArtifact{}, errors.New("saving a sandbox file as an artifact requires an ADK artifact service")
		}
		path := strings.TrimSpace(args.Path)
		if path == "" {
			return sandboxSavedArtifact{}, errors.New("path is required")
		}
		result, err := executor.Invoke(ctx, sandboxKey(ctx), Request{Action: ReadFiles, Paths: []string{path}, MaxOutputBytes: maxArtifactBytes})
		if err != nil {
			return sandboxSavedArtifact{}, err
		}
		if result.IsError {
			return sandboxSavedArtifact{}, fmt.Errorf("sandbox could not read %s: %s", path, sandboxErrorText(result))
		}
		raw, mimeType, err := sandboxFileBytes(result, path)
		if err != nil {
			return sandboxSavedArtifact{}, err
		}
		if override := strings.TrimSpace(args.MIMEType); override != "" {
			mimeType = override
		}
		if result.OutputTruncated {
			return sandboxSavedArtifact{}, fmt.Errorf("%s is larger than this tool can move in one call; split it in the sandbox first", path)
		}

		saved, err := ctx.Artifacts().Save(ctx, artifactName(path), genai.NewPartFromBytes(raw, mimeType))
		if err != nil {
			return sandboxSavedArtifact{}, fmt.Errorf("save artifact: %w", err)
		}
		return sandboxSavedArtifact{
			ArtifactName:     artifactName(path),
			ArtifactVersion:  saved.Version,
			MIMEType:         mimeType,
			SizeBytes:        len(raw),
			SessionRecreated: result.SessionRecreated,
		}, nil
	})
}

// sandboxErrorText is what a failed sandbox call said, from its streams or,
// when those are empty, its text blocks: an empty error leaves the model
// retrying the same call blind.
func sandboxErrorText(result Result) string {
	if text := strings.TrimSpace(result.Stderr + " " + result.Stdout); text != "" {
		return text
	}
	var said []string
	for _, block := range result.Content {
		if text := strings.TrimSpace(block.Text + " " + block.ResourceText); text != "" {
			said = append(said, text)
		}
	}
	if len(said) == 0 {
		return "it gave no reason; check the path with sandbox_list_files"
	}
	return strings.Join(said, "; ")
}

// sandboxFileBytes picks requestedPath's own content block out of a ReadFiles
// result and decodes it. AgentCore answers a file read as a "resource" block,
// whose payload is ResourceText or ResourceDataBase64 — never the Text or
// DataBase64 fields a text tool result like sandbox_execute_code's stdout
// uses — so both shapes are checked. Base64 content is sandbox.go's own
// limitedData encoding, so it is always well-formed. Falls back to the
// result's only block when AgentCore does not echo the path back on it.
func sandboxFileBytes(result Result, requestedPath string) ([]byte, string, error) {
	var chosen *Content
	for index := range result.Content {
		block := &result.Content[index]
		if block.Name == requestedPath || block.Name == path.Base(requestedPath) {
			chosen = block
			break
		}
	}
	if chosen == nil && len(result.Content) == 1 {
		chosen = &result.Content[0]
	}
	if chosen == nil {
		return nil, "", fmt.Errorf("sandbox returned no content for %s", requestedPath)
	}
	mimeType := chosen.MIMEType
	if mimeType == "" {
		mimeType = "application/octet-stream"
	}
	if text := firstNonEmpty(chosen.Text, chosen.ResourceText); text != "" {
		return []byte(text), "text/plain", nil
	}
	encoded := firstNonEmpty(chosen.DataBase64, chosen.ResourceDataBase64)
	if encoded == "" {
		return nil, "", fmt.Errorf("sandbox returned empty content for %s", requestedPath)
	}
	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, "", fmt.Errorf("decode sandbox content for %s: %w", requestedPath, err)
	}
	return raw, mimeType, nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

// artifactName is what a sandbox file is saved as: its own base name, so
// load_artifacts can be pointed at something recognizable.
func artifactName(sandboxPath string) string {
	return path.Base(sandboxPath)
}

func sandboxKey(ctx agent.ReadonlyContext) string {
	return fmt.Sprintf("%s\x00%s\x00%s", ctx.AppName(), ctx.UserID(), ctx.SessionID())
}
