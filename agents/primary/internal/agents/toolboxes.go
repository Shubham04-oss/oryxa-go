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
		Description:  "A throwaway machine for executing code, shell commands and scratch files — computation and data work only, never the member's own repository, files or accounts. Use apps for those.",
		Instructions: sandboxInstructions,
		Toolset:      toolset,
	}
}

// BrowserToolbox names the native toolset that drives an isolated AgentCore
// browser.
func BrowserToolbox(toolset tool.Toolset) Toolbox {
	return Toolbox{
		Name:         "browser",
		Description:  "An anonymous browser for executing interactive web tasks — navigation, page inspection, screenshots — never signed in as the member anywhere. Use apps to reach an account the member connected.",
		Instructions: browserInstructions,
		Toolset:      toolset,
	}
}
