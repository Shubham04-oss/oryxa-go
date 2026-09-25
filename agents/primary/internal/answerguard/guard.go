// Package answerguard checks an answer one member's agent gives another
// member for personal data before that member reads it, with Amazon Bedrock
// Guardrails' standalone ApplyGuardrail API on the output side.
//
// The guardrail is set up (deploy/production/answer_guard.tf) to anonymize
// the kinds that are always masked (secrets, card, bank and government
// numbers) and to detect, without acting, the kinds that are held for the
// answering member's review (email addresses, phone numbers, addresses). So
// what the guard decides is read from each finding in the assessments,
// never from the response's top-level action alone: an answer with only
// held kinds comes back with action NONE and no outputs, its findings listed
// all the same.
//
// A guard that cannot tell, because the call failed or timed out, or the
// guardrail blocked something or answered in a way this code does not
// expect, holds the answer as unchecked: it fails closed.
//
// A finding's match is the value it found, by Bedrock's design. Nothing here
// logs it, or any text: the guard logs kinds and reasons alone.
package answerguard

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"
	"github.com/aws/smithy-go"
)

// The states of a Verdict.
const (
	// StateClear is an answer in which nothing was found.
	StateClear = "clear"
	// StateMasked is an answer in which only kinds that are always masked
	// were found; it is delivered with them masked.
	StateMasked = "masked"
	// StateHeld is an answer with a kind held for review, or one that could
	// not be checked, which waits for the answering member.
	StateHeld = "held"
)

// Unchecked is the one kind a held answer carries when it could not be
// checked at all.
const Unchecked = "UNCHECKED"

// DefaultTimeout bounds checking one answer. The guardrail answers in a
// fraction of a second; one that has not answered by now is taken for one
// that is down, and the answer is held.
const DefaultTimeout = 5 * time.Second

// MaskedKinds are the kinds of personal data the guardrail anonymizes, which
// an answer is delivered with masked: Bedrock's own entity types.
func MaskedKinds() []string {
	return []string{
		"PASSWORD", "AWS_ACCESS_KEY", "AWS_SECRET_KEY",
		"CREDIT_DEBIT_CARD_NUMBER", "CREDIT_DEBIT_CARD_CVV", "CREDIT_DEBIT_CARD_EXPIRY", "PIN",
		"INTERNATIONAL_BANK_ACCOUNT_NUMBER", "SWIFT_CODE",
		"US_BANK_ACCOUNT_NUMBER", "US_BANK_ROUTING_NUMBER", "US_SOCIAL_SECURITY_NUMBER",
		"US_INDIVIDUAL_TAX_IDENTIFICATION_NUMBER", "US_PASSPORT_NUMBER",
		"DRIVER_ID",
		"CA_HEALTH_NUMBER", "CA_SOCIAL_INSURANCE_NUMBER",
		"UK_NATIONAL_HEALTH_SERVICE_NUMBER", "UK_NATIONAL_INSURANCE_NUMBER", "UK_UNIQUE_TAXPAYER_REFERENCE_NUMBER",
	}
}

// HeldKinds are the kinds the guardrail only detects, which hold an answer
// for the answering member's review.
func HeldKinds() []string { return []string{"EMAIL", "PHONE", "ADDRESS"} }

// Verdict is what the guard found in an answer's texts, and the texts as
// they may be delivered.
type Verdict struct {
	// State is StateClear, StateMasked or StateHeld.
	State string
	// Kinds are the entity types found, sorted: the masked ones when
	// masked, the held ones when held, and only Unchecked for an answer that
	// could not be checked. Never a value.
	Kinds []string
	// Texts are the answer's texts, one for each given, as they may be
	// delivered: the guardrail's output, with the kinds that are always
	// masked masked. An answer that could not be checked keeps what could
	// not be read as it was given.
	Texts []string
	// Masked are Texts with every held value also replaced by its kind, as
	// {EMAIL}, for a held answer the guard could check; nil otherwise. A
	// text a held value could not be found in to replace is its kinds
	// alone.
	Masked []string
}

// Client is Bedrock's runtime; *bedrockruntime.Client is one.
type Client interface {
	ApplyGuardrail(context.Context, *bedrockruntime.ApplyGuardrailInput, ...func(*bedrockruntime.Options)) (*bedrockruntime.ApplyGuardrailOutput, error)
}

// Config names the guardrail and bounds a check.
type Config struct {
	// Identifier and Version name the guardrail: its id or ARN, and a
	// numbered version.
	Identifier string
	Version    string
	// Timeout bounds checking one answer, every text of it together;
	// DefaultTimeout when zero.
	Timeout time.Duration
	// Logger records why an answer could not be checked; discarded when nil.
	Logger *slog.Logger
}

// Guard checks answers with one guardrail.
type Guard struct {
	client     Client
	identifier string
	version    string
	timeout    time.Duration
	logger     *slog.Logger
}

// New builds a guard. It contacts nothing until an answer is checked.
func New(client Client, config Config) (*Guard, error) {
	identifier, version := strings.TrimSpace(config.Identifier), strings.TrimSpace(config.Version)
	if client == nil || identifier == "" || version == "" {
		return nil, errors.New("answerguard: a Bedrock client, a guardrail identifier and a version are required")
	}
	timeout := config.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	logger := config.Logger
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	return &Guard{client: client, identifier: identifier, version: version, timeout: timeout, logger: logger}, nil
}

// Check reads an answer's texts, each on its own and all within the guard's
// timeout, and says what it found. A text that is empty is not sent.
func (guard *Guard) Check(ctx context.Context, texts []string) Verdict {
	checking, cancel := context.WithTimeout(ctx, guard.timeout)
	defer cancel()
	readings := make([]reading, len(texts))
	failures := make([]error, len(texts))
	var wait sync.WaitGroup
	for index, text := range texts {
		if strings.TrimSpace(text) == "" {
			readings[index] = reading{output: text}
			continue
		}
		wait.Go(func() { readings[index], failures[index] = guard.read(checking, text) })
	}
	wait.Wait()

	if reason := uncheckable(readings, failures); reason != "" {
		guard.logger.WarnContext(ctx, "the answer guard could not check an answer, so it is held for its owner as unchecked", "reason", reason)
		delivered := make([]string, len(texts))
		for index, text := range texts {
			delivered[index] = text
			if failures[index] == nil && readings[index].unusable == "" {
				delivered[index] = readings[index].output
			}
		}
		return Verdict{State: StateHeld, Kinds: []string{Unchecked}, Texts: delivered}
	}

	var masked []string
	var held []heldMatch
	delivered := make([]string, len(texts))
	for index, read := range readings {
		delivered[index] = read.output
		masked = append(masked, read.masked...)
		held = append(held, read.held...)
	}
	switch {
	case len(held) > 0:
		fully := make([]string, len(delivered))
		for index, text := range delivered {
			fully[index] = maskHeld(text, held, readings[index].held)
		}
		return Verdict{State: StateHeld, Kinds: kindsOf(held), Texts: delivered, Masked: fully}
	case len(masked) > 0:
		return Verdict{State: StateMasked, Kinds: sortedUnique(masked), Texts: delivered}
	default:
		return Verdict{State: StateClear, Texts: slices.Clone(texts)}
	}
}

// reading is what the guardrail made of one text.
type reading struct {
	// output is the text as the guardrail leaves it: masked where it
	// anonymized, and as given where it did not.
	output string
	// masked are the kinds it anonymized.
	masked []string
	// held are the held kinds it found, and any kind it was to mask but did
	// not, with what matched.
	held []heldMatch
	// unusable says why the reading cannot be used, when it cannot: in kinds
	// and policy names, never values.
	unusable string
}

// heldMatch is one held finding: its kind, and the value it matched.
type heldMatch struct{ kind, match string }

func (guard *Guard) read(ctx context.Context, text string) (reading, error) {
	output, err := guard.client.ApplyGuardrail(ctx, &bedrockruntime.ApplyGuardrailInput{
		GuardrailIdentifier: aws.String(guard.identifier),
		GuardrailVersion:    aws.String(guard.version),
		Source:              types.GuardrailContentSourceOutput,
		Content: []types.GuardrailContentBlock{
			&types.GuardrailContentBlockMemberText{Value: types.GuardrailTextBlock{Text: aws.String(text)}},
		},
	})
	if err != nil {
		return reading{}, err
	}
	if output == nil {
		return reading{}, errors.New("the guardrail returned nothing")
	}
	return assess(text, output), nil
}

// assess reads the guardrail's findings on text: every PII entity it
// detected, by its own action, whatever the response's top-level action.
// Held kinds hold the answer however the guardrail treated them; anonymized
// kinds are masked in the guardrail's output; a kind that is always masked
// and was found but not anonymized is held, since its value is still there.
// Anything blocked, a finding of another policy or a custom pattern, and a
// response that does not add up, make the reading unusable.
func assess(text string, output *bedrockruntime.ApplyGuardrailOutput) reading {
	read := reading{output: text}
	anonymized := 0
	// unusable keeps the first reason a reading cannot be used.
	unusable := func(reason string) {
		if read.unusable == "" {
			read.unusable = reason
		}
	}
	for _, assessment := range output.Assessments {
		if policy := otherPolicyBlocked(assessment); policy != "" {
			unusable("the " + policy + " policy blocked")
		}
		policy := assessment.SensitiveInformationPolicy
		if policy == nil {
			continue
		}
		for _, entity := range policy.PiiEntities {
			if entity.Detected != nil && !*entity.Detected {
				continue
			}
			kind := string(entity.Type)
			if entity.Action == types.GuardrailSensitiveInformationPolicyActionAnonymized {
				anonymized++
			}
			switch {
			case entity.Action == types.GuardrailSensitiveInformationPolicyActionBlocked:
				unusable("the guardrail blocked " + kind)
			case slices.Contains(HeldKinds(), kind):
				read.held = append(read.held, heldMatch{kind: kind, match: aws.ToString(entity.Match)})
			case entity.Action == types.GuardrailSensitiveInformationPolicyActionAnonymized:
				read.masked = append(read.masked, kind)
			case slices.Contains(MaskedKinds(), kind):
				read.held = append(read.held, heldMatch{kind: kind, match: aws.ToString(entity.Match)})
			}
		}
		for _, pattern := range policy.Regexes {
			if (pattern.Detected == nil || *pattern.Detected) && pattern.Action != types.GuardrailSensitiveInformationPolicyActionNone {
				unusable("a custom pattern the guard was not set up for acted")
			}
		}
	}
	switch output.Action {
	case types.GuardrailActionNone:
		if anonymized > 0 {
			unusable("the guardrail masked without intervening")
		}
	case types.GuardrailActionGuardrailIntervened:
		masked := outputsText(output.Outputs)
		if anonymized == 0 || masked == "" {
			unusable("the guardrail intervened without masking")
		} else if read.unusable == "" {
			read.output = masked
		}
	default:
		unusable(fmt.Sprintf("the guardrail answered with the action %q", output.Action))
	}
	return read
}

// otherPolicyBlocked names a policy other than sensitive information that
// blocked, in which case the guardrail's output is its blocked message
// rather than the masked text.
func otherPolicyBlocked(assessment types.GuardrailAssessment) string {
	if policy := assessment.TopicPolicy; policy != nil {
		for _, topic := range policy.Topics {
			if topic.Action == types.GuardrailTopicPolicyActionBlocked {
				return "topic"
			}
		}
	}
	if policy := assessment.ContentPolicy; policy != nil {
		for _, filter := range policy.Filters {
			if filter.Action == types.GuardrailContentPolicyActionBlocked {
				return "content"
			}
		}
	}
	if policy := assessment.WordPolicy; policy != nil {
		for _, word := range policy.CustomWords {
			if word.Action == types.GuardrailWordPolicyActionBlocked {
				return "word"
			}
		}
		for _, word := range policy.ManagedWordLists {
			if word.Action == types.GuardrailWordPolicyActionBlocked {
				return "word"
			}
		}
	}
	if policy := assessment.ContextualGroundingPolicy; policy != nil {
		for _, filter := range policy.Filters {
			if filter.Action == types.GuardrailContextualGroundingPolicyActionBlocked {
				return "contextual grounding"
			}
		}
	}
	return ""
}

func outputsText(outputs []types.GuardrailOutputContent) string {
	var text strings.Builder
	for _, output := range outputs {
		text.WriteString(aws.ToString(output.Text))
	}
	return text.String()
}

// uncheckable is why an answer cannot be taken as checked, or nothing: a
// call that failed or ran out of time, or a reading that cannot be used.
func uncheckable(readings []reading, failures []error) string {
	for index, failure := range failures {
		if failure != nil {
			return failureReason(failure)
		}
		if readings[index].unusable != "" {
			return readings[index].unusable
		}
	}
	return ""
}

// failureReason says why a call failed without repeating its message, which
// may quote what it was sent: a timeout, a cancellation, or the error code
// Bedrock answered with.
func failureReason(err error) string {
	var api smithy.APIError
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return "the guardrail did not answer in time"
	case errors.Is(err, context.Canceled):
		return "the check was cancelled"
	case errors.As(err, &api):
		return "the guardrail answered " + api.ErrorCode()
	default:
		return "the guardrail could not be reached"
	}
}

// maskHeld is text with every held value replaced by its kind, as {EMAIL},
// longest first, whatever its case and however its spaces run. A value the
// guardrail found in this text that cannot be found in it to replace makes
// the whole text its kinds, since it could be there still.
func maskHeld(text string, every, own []heldMatch) string {
	for _, found := range own {
		pattern := valuePattern(found.match)
		if pattern == nil || !pattern.MatchString(text) {
			return strings.Join(placeholders(own), " ")
		}
	}
	longestFirst := slices.SortedStableFunc(slices.Values(every), func(a, b heldMatch) int { return len(b.match) - len(a.match) })
	for _, found := range longestFirst {
		if pattern := valuePattern(found.match); pattern != nil {
			text = pattern.ReplaceAllLiteralString(text, "{"+found.kind+"}")
		}
	}
	return text
}

// valuePattern finds value in any case with any run of spaces between its
// words, or is nil for a value with no words.
func valuePattern(value string) *regexp.Regexp {
	words := strings.Fields(value)
	if len(words) == 0 {
		return nil
	}
	for index, word := range words {
		words[index] = regexp.QuoteMeta(word)
	}
	return regexp.MustCompile(`(?i)` + strings.Join(words, `\s+`))
}

func placeholders(matches []heldMatch) []string {
	kinds := kindsOf(matches)
	for index, kind := range kinds {
		kinds[index] = "{" + kind + "}"
	}
	return kinds
}

func kindsOf(matches []heldMatch) []string {
	kinds := make([]string, 0, len(matches))
	for _, found := range matches {
		kinds = append(kinds, found.kind)
	}
	return sortedUnique(kinds)
}

func sortedUnique(values []string) []string {
	sorted := slices.Sorted(slices.Values(values))
	return slices.Compact(sorted)
}
