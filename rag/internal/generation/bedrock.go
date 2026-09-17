package generation

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	bedrocktypes "github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"

	"github.com/Shubham04-oss/oryxa-go/rag/internal/domain"
)

// ConverseAPI is the part of Bedrock Runtime this package uses.
//
// Converse rather than InvokeModel, because it is the one request shape that
// works across the models Bedrock hosts. Grounded answering is the same job
// whichever model does it, and a per-vendor request body here would mean
// changing this package to change model.
type ConverseAPI interface {
	Converse(context.Context, *bedrockruntime.ConverseInput, ...func(*bedrockruntime.Options)) (*bedrockruntime.ConverseOutput, error)
}

// Bedrock answers with a model on Amazon Bedrock.
type Bedrock struct {
	client ConverseAPI
	model  string
	// guardrail, when set, screens the answer through Amazon Bedrock
	// Guardrails, the same way the agents service does.
	guardrail *bedrocktypes.GuardrailConfiguration
}

// BedrockOptions configure the generator.
type BedrockOptions struct {
	// GuardrailIdentifier and GuardrailVersion screen input and output. An
	// empty identifier leaves screening off. The version must be a numbered
	// snapshot, never DRAFT, so what ran is what was reviewed.
	GuardrailIdentifier string
	GuardrailVersion    string
}

// NewBedrock builds a generator over a Bedrock model id.
//
// There is no default model on purpose. The id that works depends on the
// region and on whether a cross-region inference profile is in use, so a
// wrong default would fail at the first question rather than at startup.
func NewBedrock(client ConverseAPI, model string, options BedrockOptions) (*Bedrock, error) {
	if client == nil {
		return nil, errors.New("a Bedrock client is required")
	}
	model = strings.TrimSpace(model)
	if model == "" {
		return nil, errors.New("a Bedrock model id is required to answer questions")
	}
	generator := &Bedrock{client: client, model: model}
	if identifier := strings.TrimSpace(options.GuardrailIdentifier); identifier != "" {
		version := strings.TrimSpace(options.GuardrailVersion)
		if version == "" {
			return nil, errors.New("a guardrail version is required when a guardrail is configured")
		}
		if strings.EqualFold(version, "DRAFT") {
			return nil, errors.New("a guardrail version must be a numbered snapshot, not DRAFT")
		}
		generator.guardrail = &bedrocktypes.GuardrailConfiguration{
			GuardrailIdentifier: aws.String(identifier),
			GuardrailVersion:    aws.String(version),
		}
	}
	return generator, nil
}

func (generator *Bedrock) Model() string { return generator.model }

func (generator *Bedrock) Generate(ctx context.Context, request Request) (Response, error) {
	system, user := Prompt(request)
	maxTokens := request.MaxTokens
	if maxTokens <= 0 {
		maxTokens = MaxAnswerTokens
	}
	output, err := generator.client.Converse(ctx, &bedrockruntime.ConverseInput{
		ModelId: aws.String(generator.model),
		System:  []bedrocktypes.SystemContentBlock{&bedrocktypes.SystemContentBlockMemberText{Value: system}},
		Messages: []bedrocktypes.Message{{
			Role:    bedrocktypes.ConversationRoleUser,
			Content: []bedrocktypes.ContentBlock{&bedrocktypes.ContentBlockMemberText{Value: user}},
		}},
		InferenceConfig: &bedrocktypes.InferenceConfiguration{
			MaxTokens: aws.Int32(int32(maxTokens)),
			// Grounded answering wants the same answer from the same sources.
			// Sampling here buys variety nobody asked for and makes a wrong
			// answer harder to reproduce.
			Temperature: aws.Float32(0),
		},
		GuardrailConfig: generator.guardrail,
	})
	if err != nil {
		return Response{}, fmt.Errorf("converse with %s: %w", generator.model, err)
	}

	message, ok := output.Output.(*bedrocktypes.ConverseOutputMemberMessage)
	if !ok {
		return Response{}, fmt.Errorf("%s returned no message", generator.model)
	}
	var answer strings.Builder
	for _, block := range message.Value.Content {
		if text, isText := block.(*bedrocktypes.ContentBlockMemberText); isText {
			answer.WriteString(text.Value)
		}
	}
	if answer.Len() == 0 {
		// A guardrail intervention or a refusal returns a message with no
		// text. Saying so beats returning an empty answer that looks like the
		// documents had nothing in them.
		return Response{}, fmt.Errorf("%s returned no answer text (stop reason %q)", generator.model, output.StopReason)
	}
	return Response{Text: answer.String(), Usage: usageFrom(output.Usage)}, nil
}

func usageFrom(usage *bedrocktypes.TokenUsage) domain.Usage {
	if usage == nil {
		return domain.Usage{}
	}
	return domain.Usage{
		InputTokens:  int(aws.ToInt32(usage.InputTokens)),
		OutputTokens: int(aws.ToInt32(usage.OutputTokens)),
	}
}
