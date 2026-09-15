// Package guardrail screens agent input and output with Amazon Bedrock
// Guardrails through the standalone ApplyGuardrail API.
//
// The models run on Vertex AI, not through Bedrock's Converse API, so the
// Converse-time guardrailConfig and guardContent modes cannot apply; Bedrock's
// standalone API evaluates arbitrary text, which is what this wrapper uses.
//
// While guardrails are on, the model is called without streaming: a partial
// answer that the completed one would have blocked cannot be unsent. The
// wrapper screens the newest content of every request (a member message or a
// tool result) and every model answer. A screen that fails follows
// Config.FailClosed: closed answers with the blocked message, open passes the
// content through and logs.
package guardrail

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"iter"
	"log/slog"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"
	"google.golang.org/genai"

	"google.golang.org/adk/v2/model"
)

// Config configures the screening wrapper.
type Config struct {
	// Identifier and Version name the guardrail. Version must be a numbered
	// snapshot in production; DRAFT is mutable and must not be pinned.
	Identifier string
	Version    string
	// FailClosed answers with the blocked message when the guardrail itself
	// fails; otherwise a failed screen passes the content through.
	FailClosed bool
	// BlockedMessage is the answer a blocked or failed screen produces.
	BlockedMessage string
	// Logger receives screen decisions; slog.Default() when nil.
	Logger *slog.Logger
}

func (c Config) withDefaults() Config {
	if c.BlockedMessage == "" {
		c.BlockedMessage = "I can't help with that."
	}
	if c.Logger == nil {
		c.Logger = slog.Default()
	}
	return c
}

type applyGuardrailAPI interface {
	ApplyGuardrail(context.Context, *bedrockruntime.ApplyGuardrailInput, ...func(*bedrockruntime.Options)) (*bedrockruntime.ApplyGuardrailOutput, error)
}

// Model wraps a model so every request and answer is screened. Without an
// identifier the inner model is returned unchanged.
func Model(inner model.LLM, client applyGuardrailAPI, config Config) model.LLM {
	if config.Identifier == "" {
		return inner
	}
	if client == nil {
		return inner
	}
	return &guarded{LLM: inner, client: client, config: config.withDefaults()}
}

type guarded struct {
	model.LLM
	client applyGuardrailAPI
	config Config
}

func (g *guarded) GenerateContent(ctx context.Context, req *model.LLMRequest, _ bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		allowed, err := g.screenInput(ctx, req)
		switch {
		case err != nil && g.config.FailClosed:
			g.config.Logger.ErrorContext(ctx, "guardrail input screen failed; blocking the request", "error", err)
			yield(g.blockedResponse(), nil)
			return
		case err != nil:
			g.config.Logger.WarnContext(ctx, "guardrail input screen failed; sending the request unscreened", "error", err)
		case !allowed:
			g.config.Logger.InfoContext(ctx, "guardrail blocked a request")
			yield(g.blockedResponse(), nil)
			return
		}

		// Streaming stays off: the completed answer must be known before any
		// of it reaches the member.
		for response, err := range g.LLM.GenerateContent(ctx, req, false) {
			if err != nil {
				if !yield(nil, err) {
					return
				}
				continue
			}
			if response == nil {
				if !yield(nil, nil) {
					return
				}
				continue
			}
			screened, err := g.screenOutput(ctx, response)
			switch {
			case err != nil && g.config.FailClosed:
				g.config.Logger.ErrorContext(ctx, "guardrail output screen failed; blocking the answer", "error", err)
				yield(g.blockedResponse(), nil)
				return
			case err != nil:
				g.config.Logger.WarnContext(ctx, "guardrail output screen failed; passing the answer", "error", err)
				screened = response
			}
			if !yield(screened, nil) {
				return
			}
		}
	}
}

// screenInput evaluates the newest content of the request: the member's
// message, or the tool results the model is about to see.
func (g *guarded) screenInput(ctx context.Context, req *model.LLMRequest) (bool, error) {
	text := newestContentText(req.Contents)
	if text == "" {
		return true, nil
	}
	output, err := g.apply(ctx, types.GuardrailContentSourceInput, text)
	if err != nil {
		return false, err
	}
	return output.Action != types.GuardrailActionGuardrailIntervened, nil
}

// screenOutput evaluates the model's answer. An intervention replaces the
// answer with the guardrail's masked text when it produced one, and with the
// blocked message otherwise.
func (g *guarded) screenOutput(ctx context.Context, response *model.LLMResponse) (*model.LLMResponse, error) {
	if response.Content == nil {
		return response, nil
	}
	text := contentText(response.Content)
	if text == "" {
		return response, nil
	}
	output, err := g.apply(ctx, types.GuardrailContentSourceOutput, text)
	if err != nil {
		return response, err
	}
	if output.Action != types.GuardrailActionGuardrailIntervened {
		return response, nil
	}
	if masked := outputsText(output.Outputs); masked != "" {
		g.config.Logger.InfoContext(ctx, "guardrail masked an answer")
		response.Content = genai.NewContentFromText(masked, genai.Role(response.Content.Role))
		return response, nil
	}
	g.config.Logger.InfoContext(ctx, "guardrail blocked an answer")
	return g.blockedResponse(), nil
}

func (g *guarded) apply(ctx context.Context, source types.GuardrailContentSource, text string) (*bedrockruntime.ApplyGuardrailOutput, error) {
	output, err := g.client.ApplyGuardrail(ctx, &bedrockruntime.ApplyGuardrailInput{
		GuardrailIdentifier: aws.String(g.config.Identifier),
		GuardrailVersion:    aws.String(g.config.Version),
		Source:              source,
		Content: []types.GuardrailContentBlock{
			&types.GuardrailContentBlockMemberText{Value: types.GuardrailTextBlock{Text: aws.String(text)}},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("apply guardrail: %w", err)
	}
	if output == nil {
		return nil, errors.New("apply guardrail returned no result")
	}
	return output, nil
}

func (g *guarded) blockedResponse() *model.LLMResponse {
	return &model.LLMResponse{
		Content:      genai.NewContentFromText(g.config.BlockedMessage, genai.RoleModel),
		TurnComplete: true,
	}
}

// newestContentText reads the last content: the message or tool results that
// just entered the conversation.
func newestContentText(contents []*genai.Content) string {
	if len(contents) == 0 {
		return ""
	}
	return contentText(contents[len(contents)-1])
}

// contentText joins every text part, and every function response as JSON, so
// tool results are screened too.
func contentText(content *genai.Content) string {
	if content == nil {
		return ""
	}
	var text string
	for _, part := range content.Parts {
		if part == nil || part.Thought {
			continue
		}
		if part.Text != "" {
			text += part.Text
		}
		if part.FunctionResponse != nil {
			if encoded, err := json.Marshal(part.FunctionResponse.Response); err == nil {
				text += string(encoded)
			}
		}
	}
	return text
}

func outputsText(outputs []types.GuardrailOutputContent) string {
	var text string
	for _, output := range outputs {
		text += aws.ToString(output.Text)
	}
	return text
}
