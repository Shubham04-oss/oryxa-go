package agents

import (
	_ "embed"

	"google.golang.org/adk/v2/tool"
)

// Each toolbox ships with its skill: how its tools are meant to be used,
// carried in Krude's system prompt while the toolbox is loaded.
//
//go:embed toolboxes/sandbox.md
var sandboxInstructions string

//go:embed toolboxes/browser.md
var browserInstructions string

// SandboxToolbox names the native toolset that runs code, shell commands and
// temporary files in Bedrock AgentCore.
func SandboxToolbox(toolset tool.Toolset) Toolbox {
	return Toolbox{
		Name:         "sandbox",
		Description:  "Isolated code, shell and file operations in AWS Bedrock AgentCore for computation and data work.",
		Instructions: sandboxInstructions,
		Toolset:      toolset,
	}
}

// BrowserToolbox names the native toolset that drives an isolated AgentCore
// browser.
func BrowserToolbox(toolset tool.Toolset) Toolbox {
	return Toolbox{
		Name:         "browser",
		Description:  "Isolated web navigation, semantic page inspection, screenshots and confirmed interactions in AWS Bedrock AgentCore Browser.",
		Instructions: browserInstructions,
		Toolset:      toolset,
	}
}
