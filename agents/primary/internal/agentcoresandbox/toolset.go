package agentcoresandbox

import (
	"errors"
	"fmt"

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
	tools := make([]tool.Tool, 0, 7)
	for _, build := range []func() (tool.Tool, error){
		func() (tool.Tool, error) { return executeCodeTool(executor) },
		func() (tool.Tool, error) { return executeCommandTool(executor) },
		func() (tool.Tool, error) { return listFilesTool(executor) },
		func() (tool.Tool, error) { return readFilesTool(executor) },
		func() (tool.Tool, error) { return writeFilesTool(executor) },
		func() (tool.Tool, error) { return removeFilesTool(executor) },
		func() (tool.Tool, error) { return resetTool(executor) },
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

func sandboxKey(ctx agent.ReadonlyContext) string {
	return fmt.Sprintf("%s\x00%s\x00%s", ctx.AppName(), ctx.UserID(), ctx.SessionID())
}
