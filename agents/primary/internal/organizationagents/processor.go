package organizationagents

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/teamspaces"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/threads"
)

const (
	processorMaxAttempts = 3
	processorRetryAfter  = time.Minute
	processorRetention   = 7 * 24 * time.Hour
	processorClaims      = 8
	// maxSummaryRunes bounds the shared summary. It is carried into every
	// later synthesis and read by people, so it is kept to a few pages
	// rather than the limit the store would accept.
	maxSummaryRunes  = 8_000
	maxListItems     = 100
	maxListItemRunes = 2_000
	maxFollowUps     = 20
)

type deliveryStore interface {
	Claim(context.Context, time.Duration, int) ([]Delivery, error)
	Synthesis(context.Context, string, string) (Synthesis, error)
	Apply(context.Context, Delivery, string, Result) (Synthesis, error)
	Release(context.Context, string, bool, int, time.Duration, string) (bool, error)
	SetAside(context.Context, string, string) error
	Drop(context.Context, string, string) error
	Purge(context.Context, time.Duration) error
}

type sharedSources interface {
	GetOrganizationAgent(context.Context, string) (teamspaces.OrganizationAgent, error)
	Get(context.Context, string, string) (teamspaces.Space, error)
	GetEntry(context.Context, string, string) (teamspaces.Entry, error)
}

type synthesizer interface {
	Synthesize(context.Context, teamspaces.OrganizationAgent, Synthesis, teamspaces.Space, teamspaces.Entry) (Result, error)
}

type ProcessorConfig struct {
	Interval    time.Duration
	Lease       time.Duration
	Concurrency int
	// Relevance decides which publications are organization context and
	// which are the working group talking among themselves. [ByShape] reads
	// the publication itself and is the default.
	Relevance Relevance
}

type Processor struct {
	store     deliveryStore
	sources   sharedSources
	runtime   synthesizer
	relevance Relevance
	config    ProcessorConfig
	logger    *slog.Logger
}

func NewProcessor(store deliveryStore, sources sharedSources, runtime synthesizer, config ProcessorConfig, logger *slog.Logger) (*Processor, error) {
	if store == nil || sources == nil || runtime == nil {
		return nil, errors.New("organization-agent processor needs a store, shared sources, and runtime")
	}
	if config.Interval <= 0 {
		config.Interval = 5 * time.Second
	}
	if config.Lease <= 0 {
		config.Lease = 10 * time.Minute
	}
	config.Concurrency = max(config.Concurrency, 1)
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	relevance := config.Relevance
	if relevance == nil {
		relevance = ByShape{}
	}
	return &Processor{store: store, sources: sources, runtime: runtime, relevance: relevance, config: config, logger: logger}, nil
}

func (processor *Processor) Run(ctx context.Context) {
	ticker := time.NewTicker(processor.config.Interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		if err := processor.ProcessDue(ctx); err != nil && ctx.Err() == nil {
			processor.logger.ErrorContext(ctx, "process organization-agent synthesis", "error", err)
		}
	}
}

func (processor *Processor) ProcessDue(ctx context.Context) error {
	if err := processor.store.Purge(ctx, processorRetention); err != nil {
		processor.logger.WarnContext(ctx, "purge organization-agent deliveries", "error", err)
	}
	slots := make(chan struct{}, processor.config.Concurrency)
	var running sync.WaitGroup
	defer running.Wait()
	for range processorClaims {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		slots <- struct{}{}
		claimed, err := processor.store.Claim(ctx, processor.config.Lease, 1)
		if err != nil || len(claimed) == 0 {
			<-slots
			return err
		}
		delivery := claimed[0]
		running.Go(func() {
			defer func() { <-slots }()
			processor.process(ctx, delivery)
		})
	}
	return nil
}

func (processor *Processor) process(ctx context.Context, delivery Delivery) {
	logger := processor.logger.With("event_id", delivery.EventID, "entry_id", delivery.EntryID, "organization_id", delivery.OrganizationID)
	entry, err := processor.sources.GetEntry(ctx, delivery.OrganizationID, delivery.EntryID)
	if errors.Is(err, teamspaces.ErrNotFound) {
		processor.drop(ctx, delivery, "shared entry no longer exists", logger)
		return
	}
	if err != nil {
		processor.retry(ctx, delivery, err, logger)
		return
	}
	if entry.TeamspaceID != delivery.TeamspaceID || entry.AuthorUserID != delivery.AuthorUserID ||
		entry.AuthorAgentID != delivery.AuthorAgentID || string(entry.Kind) != delivery.Kind ||
		!entry.CreatedAt.Equal(delivery.PublishedAt) {
		processor.drop(ctx, delivery, "shared entry does not match its routing event", logger)
		return
	}
	space, err := processor.sources.Get(ctx, delivery.OrganizationID, delivery.TeamspaceID)
	if errors.Is(err, teamspaces.ErrNotFound) {
		processor.drop(ctx, delivery, "teamspace no longer exists", logger)
		return
	}
	if err != nil {
		processor.retry(ctx, delivery, err, logger)
		return
	}
	definition, err := processor.sources.GetOrganizationAgent(ctx, delivery.OrganizationID)
	if errors.Is(err, teamspaces.ErrNotFound) {
		processor.drop(ctx, delivery, "organization agent no longer exists", logger)
		return
	}
	if err != nil {
		processor.retry(ctx, delivery, err, logger)
		return
	}
	if definition.Status != teamspaces.StatusActive || space.Status != teamspaces.StatusActive {
		processor.drop(ctx, delivery, ErrDisabled.Error(), logger)
		return
	}
	// Chatter never reaches the organization agent: it would cost a run and
	// a revision of what everyone reads, to record that somebody said thanks.
	relevant, why, err := processor.relevance.OrganizationRelevant(ctx, entry)
	if err != nil {
		processor.retry(ctx, delivery, err, logger)
		return
	}
	if !relevant {
		if err := processor.store.SetAside(context.WithoutCancel(ctx), delivery.EventID, why); err != nil {
			logger.ErrorContext(ctx, "set aside an organization-agent delivery", "error", err)
			return
		}
		logger.InfoContext(ctx, "shared publication left out of organization context", "source_entry_id", entry.ID, "reason", why)
		return
	}

	// A publication is read only by its own team, so it advances only that
	// teamspace's synthesis, and that synthesis is all the run is shown.
	// ponytail: nothing feeds the organization-wide synthesis (teamspace "")
	// yet, because every teamspace is team-only; org-visible inputs fill it in
	// Layer C (C2).
	previous, err := processor.store.Synthesis(ctx, delivery.OrganizationID, delivery.TeamspaceID)
	if err != nil {
		processor.retry(ctx, delivery, err, logger)
		return
	}
	result, err := processor.runtime.Synthesize(ctx, definition, previous, space, entry)
	if err != nil {
		processor.retry(ctx, delivery, err, logger)
		return
	}
	result = cleanResult(result, participantIDs(space, entry))
	if err := validateResult(result); err != nil {
		processor.retry(ctx, delivery, err, logger)
		return
	}
	if _, err := processor.store.Apply(context.WithoutCancel(ctx), delivery, definition.ID, result); err != nil {
		// Applying is not cut short with the processor, so what goes wrong
		// there is its own doing, whether or not the processor is stopping.
		processor.retry(context.WithoutCancel(ctx), delivery, err, logger)
		return
	}
	logger.InfoContext(ctx, "organization synthesis advanced", "source_entry_id", entry.ID)
}

// interrupted reports whether ctx has ended because the processor is
// stopping, a deploy say. Whatever was under way when it did was cut short,
// which is not the same as having failed.
func interrupted(ctx context.Context) bool { return ctx.Err() != nil }

// retry gives a delivery back to be claimed again, and counts the attempt
// against it unless the processor is stopping. A synthesis cut short did not
// fail, whatever error the run it was cut short in reports: it is tried again,
// and the attempt does not count toward dropping the delivery.
func (processor *Processor) retry(ctx context.Context, delivery Delivery, cause error, logger *slog.Logger) {
	failed, reason := true, cause.Error()
	// A run the process's drain refused or stopped (threads.Interrupted) was
	// cut short the same way, while ctx still lives.
	if interrupted(ctx) || threads.Interrupted(cause) {
		logger.InfoContext(ctx, "organization synthesis interrupted; it is tried again", "reason", reason)
		failed, reason = false, "interrupted: "+reason
	}
	dropped, err := processor.store.Release(context.WithoutCancel(ctx), delivery.EventID, failed, processorMaxAttempts, processorRetryAfter, reason)
	if err != nil {
		logger.ErrorContext(ctx, "release organization-agent delivery", "error", err)
		return
	}
	if dropped {
		logger.WarnContext(ctx, "drop organization-agent delivery after repeated failures", "attempts", processorMaxAttempts, "error", cause)
	}
}

func (processor *Processor) drop(ctx context.Context, delivery Delivery, reason string, logger *slog.Logger) {
	if err := processor.store.Drop(context.WithoutCancel(ctx), delivery.EventID, reason); err != nil {
		logger.ErrorContext(ctx, "drop organization-agent delivery", "error", err)
	}
}

func participantIDs(space teamspaces.Space, entry teamspaces.Entry) []string {
	result := []string{entry.AuthorAgentID}
	for _, contribution := range space.Contributions {
		if contribution.Status == "active" && !slices.Contains(result, contribution.AgentID) {
			result = append(result, contribution.AgentID)
		}
	}
	return result
}

func cleanResult(result Result, participants []string) Result {
	result.Summary = strings.TrimSpace(result.Summary)
	// The summary is read back into every later synthesis, so a run that
	// rambles would make every run after it more expensive. Cut it here
	// rather than refusing the whole result.
	if summary := []rune(result.Summary); len(summary) > maxSummaryRunes {
		result.Summary = strings.TrimSpace(string(summary[:maxSummaryRunes]))
	}
	result.Decisions = cleanList(result.Decisions)
	result.Risks = cleanList(result.Risks)
	result.OpenQuestions = cleanList(result.OpenQuestions)
	if len(result.FollowUps) > maxFollowUps {
		result.FollowUps = result.FollowUps[:maxFollowUps]
	}
	cleaned := make([]FollowUpResult, 0, len(result.FollowUps))
	for _, followUp := range result.FollowUps {
		followUp.TargetAgentID = strings.TrimSpace(followUp.TargetAgentID)
		followUp.Task = strings.TrimSpace(followUp.Task)
		followUp.Rationale = strings.TrimSpace(followUp.Rationale)
		if followUp.TargetAgentID != "" && !slices.Contains(participants, followUp.TargetAgentID) {
			followUp.TargetAgentID = ""
		}
		cleaned = append(cleaned, followUp)
	}
	result.FollowUps = cleaned
	return result
}

func cleanList(values []string) []string {
	result := make([]string, 0, min(len(values), maxListItems))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" && !slices.Contains(result, value) {
			result = append(result, value)
			if len(result) == maxListItems {
				break
			}
		}
	}
	return result
}

func validateResult(result Result) error {
	switch {
	case result.Summary == "":
		return fmt.Errorf("%w: summary is required", ErrInvalid)
	case utf8.RuneCountInString(result.Summary) > maxSummaryRunes:
		return fmt.Errorf("%w: summary is too long", ErrInvalid)
	}
	for _, values := range [][]string{result.Decisions, result.Risks, result.OpenQuestions} {
		for _, value := range values {
			if utf8.RuneCountInString(value) > maxListItemRunes {
				return fmt.Errorf("%w: synthesis list item is too long", ErrInvalid)
			}
		}
	}
	for _, followUp := range result.FollowUps {
		if followUp.Task == "" || followUp.Rationale == "" {
			return fmt.Errorf("%w: every follow-up needs a task and rationale", ErrInvalid)
		}
		if utf8.RuneCountInString(followUp.Task) > maxListItemRunes || utf8.RuneCountInString(followUp.Rationale) > maxListItemRunes {
			return fmt.Errorf("%w: follow-up is too long", ErrInvalid)
		}
	}
	return nil
}
