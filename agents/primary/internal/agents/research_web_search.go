package agents

import (
	"fmt"
	"strings"

	"google.golang.org/genai"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/agent/llmagent"
	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/tool"
	"google.golang.org/adk/v2/tool/agenttool"
	"google.golang.org/adk/v2/tool/geminitool"
)

// webSearchName is the tool single-agent Krude searches the web with.
const webSearchName = "web_search"

// webSearchInstruction is all the search agent is told: no persona, nothing
// about the member. It sees the question and nothing else.
const webSearchInstruction = `You answer one question from the live web. Use Google Search, and read any link the question names. Lead with the answer and keep it short. Cite the sources you relied on. Say what is still uncertain when the results do not settle it, instead of guessing.`

// maxWebSources bounds the sources listed under a web_search answer.
const maxWebSources = 8

// webSearchInput is web_search's one argument. The agent tool hands it to
// the search agent as JSON, as its only message.
var webSearchInput = &genai.Schema{
	Type: genai.TypeObject,
	Properties: map[string]*genai.Schema{
		"question": {
			Type:        genai.TypeString,
			Description: "The question to answer from the live web, complete on its own: the search sees nothing else of the conversation. Never put the member's private data in it.",
		},
	},
	Required: []string{"question"},
}

// webSearchTool is Krude's web search when it is one agent (SingleAgentRoot):
// a search-only agent, with Google Search and URL context and nothing else,
// wrapped as a tool. It is an agent of its own, rather than Google Search
// among Krude's tools, only because Vertex AI refuses a request that
// combines Google Search with function declarations, and Krude's other tools
// are function declarations. Delete the wrapper, and give Krude the search
// tools directly, once Vertex documents mixing them.
//
// It runs in a runner of its own (agenttool), so it sees only the question.
// It returns {"result": the answer}, with the pages Google Search grounded
// it on listed under it.
func webSearchTool(llm model.LLM) (tool.Tool, error) {
	searcher, err := llmagent.New(llmagent.Config{
		Name:        webSearchName,
		Description: "Answers a question from the live web with Google Search, reading any link the question names, and returns the answer with its sources. For current public facts only, never for the member's private data.",
		Model:       llm,
		Mode:        llmagent.ModeSingleTurn,
		// A provider returns the instruction as written, so nothing in it is
		// read as a session state placeholder.
		InstructionProvider:      func(agent.ReadonlyContext) (string, error) { return webSearchInstruction, nil },
		InputSchema:              webSearchInput,
		Tools:                    []tool.Tool{geminitool.GoogleSearch{}, urlContext()},
		AfterModelCallbacks:      []llmagent.AfterModelCallback{citeGroundingSources},
		DisallowTransferToParent: true,
		DisallowTransferToPeers:  true,
	})
	if err != nil {
		return nil, fmt.Errorf("build %s: %w", webSearchName, err)
	}
	return agenttool.New(searcher, nil), nil
}

// citeGroundingSources lists the pages Google Search grounded an answer on
// under its text. The agent tool returns the answer's text alone and drops
// its grounding metadata, so without this Krude would see only the sources
// the model chose to name. A partial streamed response is left alone: the
// final one carries the metadata.
func citeGroundingSources(_ agent.Context, response *model.LLMResponse, responseErr error) (*model.LLMResponse, error) {
	if responseErr != nil || response == nil || response.Partial || response.Content == nil || response.GroundingMetadata == nil {
		return nil, nil
	}
	sources := groundingSources(response.GroundingMetadata)
	if len(sources) == 0 {
		return nil, nil
	}
	cited := *response
	content := *response.Content
	content.Parts = append(append([]*genai.Part{}, response.Content.Parts...), genai.NewPartFromText("\n\nSources:\n"+strings.Join(sources, "\n")))
	cited.Content = &content
	return &cited, nil
}

// groundingSources are the web pages in metadata, one line each, without
// repeats and at most maxWebSources of them.
func groundingSources(metadata *genai.GroundingMetadata) []string {
	var lines []string
	seen := map[string]bool{}
	for _, chunk := range metadata.GroundingChunks {
		if chunk == nil || chunk.Web == nil {
			continue
		}
		uri := strings.TrimSpace(chunk.Web.URI)
		if uri == "" || seen[uri] {
			continue
		}
		seen[uri] = true
		line := "- " + uri
		if title := boundedText(chunk.Web.Title, 120); title != "" {
			line = "- " + title + ": " + uri
		}
		lines = append(lines, line)
		if len(lines) == maxWebSources {
			break
		}
	}
	return lines
}
