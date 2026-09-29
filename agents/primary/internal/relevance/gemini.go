package relevance

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net"
	"slices"
	"strings"
	"time"

	"google.golang.org/genai"
)

// DefaultTimeout bounds one judgment when NewGemini is given none. The
// answer is a few categories from a small model; one that has not come by
// now is taken for a model that is down, and the answer is held.
const DefaultTimeout = 6 * time.Second

// maxOutputTokens leaves a thinking model room to think before the few
// tokens of its answer: thinking counts against the limit.
const maxOutputTokens = 2048

// models is the slice of the Gen AI client a judge uses, which a test can
// stand in for; *genai.Models is one.
type models interface {
	GenerateContent(ctx context.Context, model string, contents []*genai.Content, config *genai.GenerateContentConfig) (*genai.GenerateContentResponse, error)
}

// gemini is a Judge that asks a Gemini model, on Vertex AI or wherever its
// client points.
type gemini struct {
	models  models
	model   string
	timeout time.Duration
	// nonce makes the code the data's markers carry; newNonce, but for a
	// test that needs to know it.
	nonce func() string
}

// NewGemini judges with model, through client, one call for each answer that
// needs it. Build the client as the agents do (agents.GeminiClient: the same
// project, location and credentials, and no retries: a judgment that does
// not come in time holds the answer rather than wait for a second try).
// timeout bounds each call; DefaultTimeout when it is zero or less.
//
// A judge built without a client or a model does not panic: every answer it
// is asked to check comes back unchecked, and so held.
func NewGemini(client *genai.Client, model string, timeout time.Duration) Judge {
	var asks models
	if client != nil && client.Models != nil {
		// Only a real client's models: a nil *genai.Models in the interface
		// would not be nil.
		asks = client.Models
	}
	return newGemini(asks, model, timeout)
}

func newGemini(asks models, model string, timeout time.Duration) *gemini {
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	return &gemini{models: asks, model: strings.TrimSpace(model), timeout: timeout, nonce: newNonce}
}

// Judge implements Judge.
func (judge *gemini) Judge(ctx context.Context, in Input) Result {
	result := Result{Model: judge.model}
	if !NeedsJudging(in.Sources) || len(answerTexts(in.Answer)) == 0 {
		// Nothing was read that could be private, or there is no answer to
		// carry it: the model has nothing to add.
		return result
	}
	result.Judged = true
	shown := buildPrompt(in, judge.nonce())
	result.Truncated = shown.truncated
	if judge.models == nil || judge.model == "" {
		return judge.unchecked(result, in, &failure{kind: ErrNotConfigured})
	}

	ctx, cancel := context.WithTimeout(ctx, judge.timeout)
	defer cancel()
	started := time.Now()
	response, err := judge.models.GenerateContent(ctx, judge.model,
		[]*genai.Content{genai.NewContentFromText(shown.user, genai.RoleUser)}, judge.config())
	result.Latency = time.Since(started)
	if err != nil {
		return judge.unchecked(result, in, callFailure(err))
	}
	asked, extras, certainty, err := readAnswer(response)
	if err != nil {
		return judge.unchecked(result, in, err)
	}
	result.Asked, result.Extras, result.Certainty = asked, extras, certainty
	result.Hold = ShouldHold(in.Regulated, asked, extras, nil)
	return result
}

// unchecked is result for an answer that could not be checked: held, and
// saying why. It carries no categories, since none were read.
func (judge *gemini) unchecked(result Result, in Input, err error) Result {
	result.Err = err
	result.Hold = ShouldHold(in.Regulated, nil, nil, err)
	return result
}

// config is the generation config of one call: temperature zero, a JSON
// answer of the shape ShouldHold reads, and the instruction. A call has a
// config of its own so nothing is shared between calls that run at once.
func (judge *gemini) config() *genai.GenerateContentConfig {
	safety := make([]*genai.SafetySetting, 0, 4)
	for _, category := range []genai.HarmCategory{
		genai.HarmCategoryHarassment, genai.HarmCategoryHateSpeech,
		genai.HarmCategorySexuallyExplicit, genai.HarmCategoryDangerousContent,
	} {
		// What is judged is a member's own private material, read, not
		// generated: a model that refused to read it would leave the answer
		// unchecked, which is a hold, and it is most of what is worth holding.
		safety = append(safety, &genai.SafetySetting{Category: category, Threshold: genai.HarmBlockThresholdOff})
	}
	config := &genai.GenerateContentConfig{
		SystemInstruction: &genai.Content{Parts: []*genai.Part{{Text: instruction}}},
		Temperature:       genai.Ptr[float32](0),
		ResponseMIMEType:  "application/json",
		ResponseSchema:    responseSchema(),
		SafetySettings:    safety,
		MaxOutputTokens:   maxOutputTokens,
	}
	// Gemini 2.5 rejects a thinking level; Gemini 3 takes one, and low is
	// enough to weigh breadth. Other models think as they do by default.
	if strings.HasPrefix(judge.model, "gemini-3") {
		config.ThinkingConfig = &genai.ThinkingConfig{ThinkingLevel: genai.ThinkingLevelLow}
	}
	return config
}

// responseSchema is the answer asked for: the two lists of categories and the
// judge's certainty, in that order, so the decision comes before the
// confidence in it. A category outside the six cannot be said.
func responseSchema() *genai.Schema {
	names := categoryNames(Categories())
	list := func(description string) *genai.Schema {
		return &genai.Schema{
			Type: genai.TypeArray, Description: description,
			Items: &genai.Schema{Type: genai.TypeString, Enum: names},
		}
	}
	return &genai.Schema{
		Type: genai.TypeObject,
		Properties: map[string]*genai.Schema{
			"asked":  list("The private categories the request itself asks about; empty when it asks about none."),
			"extras": list("The private categories the answer carries that the request did not ask for, at the breadth it asked; empty when there are none."),
			"certainty": {
				Type:        genai.TypeNumber,
				Description: "How sure you are, from 0 to 1, that both lists are exactly right.",
			},
		},
		PropertyOrdering: []string{"asked", "extras", "certainty"},
		Required:         []string{"asked", "extras", "certainty"},
	}
}

// callFailure is why a call to the model failed, without repeating the
// model's own message, which may quote what it was sent.
func callFailure(err error) error {
	var api genai.APIError
	var network net.Error
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return &failure{kind: ErrTimeout, cause: err}
	case errors.Is(err, context.Canceled):
		return &failure{kind: ErrCanceled, cause: err}
	case errors.As(err, &api):
		return &failure{kind: ErrUnavailable, cause: err, status: api.Code}
	case errors.As(err, &network) && network.Timeout():
		return &failure{kind: ErrTimeout, cause: err}
	default:
		return &failure{kind: ErrUnavailable, cause: err}
	}
}

// answer is the JSON the model gives. A list that is missing is not the same
// as one that is empty: a missing one says nothing.
type answer struct {
	Asked     *[]string `json:"asked"`
	Extras    *[]string `json:"extras"`
	Certainty *float64  `json:"certainty"`
}

// readAnswer reads the model's answer into the categories it names, in the
// order of Categories, and its certainty clamped to 0 through 1. It fails,
// so that the answer is held unchecked, when the model would not read the
// text, gave nothing, gave something that is not the JSON asked for, or
// named a category there is not.
func readAnswer(response *genai.GenerateContentResponse) (asked, extras []Category, certainty float64, err error) {
	if response == nil {
		return nil, nil, 0, &failure{kind: ErrEmpty}
	}
	if feedback := response.PromptFeedback; feedback != nil && feedback.BlockReason != "" {
		return nil, nil, 0, &failure{kind: ErrBlocked}
	}
	text := ""
	if len(response.Candidates) > 0 && response.Candidates[0] != nil {
		candidate := response.Candidates[0]
		if candidate.Content != nil {
			text = response.Text()
		}
		switch candidate.FinishReason {
		case genai.FinishReasonSafety, genai.FinishReasonProhibitedContent, genai.FinishReasonBlocklist, genai.FinishReasonSPII:
			if strings.TrimSpace(text) == "" {
				return nil, nil, 0, &failure{kind: ErrBlocked}
			}
		}
	}
	if strings.TrimSpace(text) == "" {
		return nil, nil, 0, &failure{kind: ErrEmpty}
	}

	var given answer
	if err := json.Unmarshal([]byte(trimFence(text)), &given); err != nil || given.Asked == nil || given.Extras == nil {
		return nil, nil, 0, &failure{kind: ErrUnparseable}
	}
	if asked, err = categoriesOf(*given.Asked); err != nil {
		return nil, nil, 0, err
	}
	if extras, err = categoriesOf(*given.Extras); err != nil {
		return nil, nil, 0, err
	}
	if given.Certainty != nil && !math.IsNaN(*given.Certainty) {
		certainty = min(max(*given.Certainty, 0), 1)
	}
	return asked, extras, certainty, nil
}

// categoriesOf turns the model's labels into Categories, once each, in the
// order of Categories. A label is read in any case; one that is not a
// category fails the whole answer.
func categoriesOf(labels []string) ([]Category, error) {
	var found []Category
	for _, label := range labels {
		category := Category(strings.ToLower(strings.TrimSpace(label)))
		if !category.Valid() {
			return nil, &failure{kind: ErrUnknownCategory}
		}
		if !slices.Contains(found, category) {
			found = append(found, category)
		}
	}
	order := Categories()
	slices.SortFunc(found, func(a, b Category) int { return slices.Index(order, a) - slices.Index(order, b) })
	return found, nil
}

// trimFence takes the Markdown fence off JSON a model wrapped in one, which
// some do whatever they are asked.
func trimFence(text string) string {
	text = strings.TrimSpace(text)
	if !strings.HasPrefix(text, "```") {
		return text
	}
	text = strings.TrimPrefix(text, "```")
	text = strings.TrimPrefix(text, "json")
	text = strings.TrimSuffix(strings.TrimSpace(text), "```")
	return strings.TrimSpace(text)
}
