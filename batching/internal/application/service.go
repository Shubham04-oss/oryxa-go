// Package application classifies app triggers and releases durable batches.
package application

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	appsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/apps/v1"
	authv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/auth/v1"
	batchingv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/batching/v1"

	"github.com/Shubham04-oss/oryxa-go/batching/internal/domain"
)

const (
	retention = 7 * 24 * time.Hour
	// forgottenRetention is how long a removal is remembered, so the bus
	// cannot bring back what it removed. SQS keeps a message at most 14 days
	// (the dead-letter queue keeps that long), EventBridge retries for a day,
	// and apps' outbox retries before either; a month is comfortably past all
	// of them, and a tombstone is only a few ids.
	forgottenRetention = 30 * 24 * time.Hour
	sweepEvery         = time.Hour
	sweepBatch         = 500
)

// Classifier is the narrow integration point for Jev. It receives the
// original app trigger and returns only a typed scheduling decision.
type Classifier interface {
	Classify(context.Context, appsv1.TriggerReceived) (domain.Classification, error)
}

type ClassifierFunc func(context.Context, appsv1.TriggerReceived) (domain.Classification, error)

func (classify ClassifierFunc) Classify(ctx context.Context, trigger appsv1.TriggerReceived) (domain.Classification, error) {
	return classify(ctx, trigger)
}

// DigestClassifier is the deterministic policy used when Jev is disabled.
// Every trigger waits for the size/time rule and nothing is silently dropped.
type DigestClassifier struct{}

func (DigestClassifier) Classify(context.Context, appsv1.TriggerReceived) (domain.Classification, error) {
	return domain.Classification{Lane: domain.LaneDigest, Priority: 0, Classifier: "default-digest.v1"}, nil
}

const (
	// breakerFailures is how many classifications in a row may fail before
	// ResilientClassifier stops asking its provider, for breakerCooldown.
	breakerFailures = 5
	breakerCooldown = time.Minute
)

// ResilientClassifier preserves ingestion when the external classifier is
// unavailable. Its fallback, domain.FallbackPriority, accelerates the event
// without turning a provider outage into one agent turn per webhook.
//
// It is also a circuit breaker. After breakerFailures failures in a row it
// stops asking the provider for breakerCooldown and answers with the
// fallback at once, so an outage costs each trigger its timeout only until
// then. After the cool-down one trigger asks again: its success closes the
// circuit, and its failure keeps it open for another cool-down.
type ResilientClassifier struct {
	primary Classifier
	logger  *slog.Logger
	now     func() time.Time

	mutex     sync.Mutex
	failures  int       // in a row, while the circuit is closed
	openUntil time.Time // zero while the circuit is closed
	probing   bool      // one trigger is asking whether the provider is back
}

func NewResilientClassifier(primary Classifier, logger *slog.Logger) *ResilientClassifier {
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	return &ResilientClassifier{primary: primary, logger: logger, now: time.Now}
}

func (classifier *ResilientClassifier) Classify(ctx context.Context, trigger appsv1.TriggerReceived) (domain.Classification, error) {
	ask, probe := classifier.admit()
	if !ask {
		return fallback("Jev not asked while its circuit is open; accelerated digest fallback"), nil
	}
	classification, err := classifier.primary.Classify(ctx, trigger)
	opened, closed := classifier.settle(ctx, err, probe)
	if closed {
		classifier.logger.InfoContext(ctx, "Jev answered again; classifying with it")
	}
	if err == nil {
		return classification, nil
	}
	classifier.logger.ErrorContext(ctx, "Jev classification failed; using an accelerated digest", "error", err,
		"toolkit", trigger.Toolkit, "trigger", trigger.Trigger)
	if opened {
		classifier.logger.WarnContext(ctx, "Jev failed repeatedly; using the accelerated digest without asking it for a while",
			"failures", breakerFailures, "cooldown", breakerCooldown.String())
	}
	return fallback("Jev unavailable; accelerated digest fallback"), nil
}

func fallback(reason string) domain.Classification {
	return domain.Classification{
		Lane: domain.LaneDigest, Priority: domain.FallbackPriority, Classifier: "jev-fallback.v1", Reason: reason,
	}
}

// admit says whether to ask the provider, and whether this is the one call
// asking whether an open circuit's provider is back.
func (classifier *ResilientClassifier) admit() (ask, probe bool) {
	classifier.mutex.Lock()
	defer classifier.mutex.Unlock()
	switch {
	case classifier.openUntil.IsZero():
		return true, false
	case classifier.probing || classifier.now().Before(classifier.openUntil):
		return false, false
	default:
		classifier.probing = true
		return true, true
	}
}

// settle counts how a call to the provider went, and says whether it opened
// or closed the circuit. A caller that gave up says nothing about the
// provider, so its failure counts for nothing.
func (classifier *ResilientClassifier) settle(ctx context.Context, err error, probe bool) (opened, closed bool) {
	classifier.mutex.Lock()
	defer classifier.mutex.Unlock()
	if probe {
		classifier.probing = false
	}
	switch {
	case err == nil:
		classifier.failures = 0
		if !classifier.openUntil.IsZero() {
			classifier.openUntil = time.Time{}
			return false, true
		}
	case ctx.Err() != nil:
	case !classifier.openUntil.IsZero():
		// Still down: a failed probe starts another cool-down. A call
		// already under way when the circuit opened changes nothing.
		if probe {
			classifier.openUntil = classifier.now().Add(breakerCooldown)
		}
	default:
		classifier.failures++
		if classifier.failures >= breakerFailures {
			classifier.failures = 0
			classifier.openUntil = classifier.now().Add(breakerCooldown)
			return true, false
		}
	}
	return false, false
}

type store interface {
	Screen(context.Context, domain.Trigger) (domain.Admission, error)
	Record(context.Context, domain.Trigger) (domain.Admission, error)
	ClaimDue(context.Context, domain.Rule, time.Duration, int) ([]domain.Batch, error)
	Unclaim(context.Context, string, []string) error
	Complete(context.Context, domain.Batch, domain.Event) error
	Forget(context.Context, string, string) error
	Discard(context.Context, string, string, string) error
	DeleteBatched(context.Context, time.Time, int) (int, error)
	DeleteForgotten(context.Context, time.Time) (int, error)
}

type Config struct {
	Rule           domain.Rule
	Interval       time.Duration
	ClaimLease     time.Duration
	GroupsPerSweep int
}

type Service struct {
	store      store
	classifier Classifier
	config     Config
	logger     *slog.Logger
	now        func() time.Time
}

func New(store store, classifier Classifier, config Config, logger *slog.Logger) *Service {
	if classifier == nil {
		classifier = DigestClassifier{}
	}
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	return &Service{store: store, classifier: classifier, config: config, logger: logger, now: time.Now}
}

func (service *Service) TriggerReceived(ctx context.Context, eventID string, trigger appsv1.TriggerReceived) error {
	if eventID == "" || trigger.OrganizationID == "" || trigger.MemberID == "" || trigger.ReceivedAt.IsZero() {
		return errors.New("a trigger event needs its event id, organization, member, and received time")
	}
	candidate := domain.Trigger{
		ID: eventID, TriggerID: trigger.TriggerID, ConnectionID: trigger.ConnectionID,
		OrganizationID: trigger.OrganizationID, MemberID: trigger.MemberID,
		Toolkit: trigger.Toolkit, Trigger: trigger.Trigger, Data: trigger.Data,
		DataOmitted: trigger.DataOmitted, ReceivedAt: trigger.ReceivedAt,
	}
	// Screened before it is classified: a redelivery, or a trigger from
	// something since removed, must not wait on the classifier, nor send its
	// payload to the classifier's provider again.
	admission, err := service.store.Screen(ctx, candidate)
	if err != nil {
		return err
	}
	if admission != domain.Admitted {
		service.report(ctx, eventID, admission)
		return nil
	}
	classification, err := service.classifier.Classify(ctx, trigger)
	if err != nil {
		return fmt.Errorf("classify trigger: %w", err)
	}
	if err := classification.Validate(); err != nil {
		return fmt.Errorf("classify trigger: %w", err)
	}
	candidate.Classification = classification
	if admission, err = service.store.Record(ctx, candidate); err != nil {
		return err
	}
	service.report(ctx, eventID, admission)
	return nil
}

// report says why a trigger event was not kept. Neither is a failure: the
// event is acknowledged, since delivering it again would change nothing.
func (service *Service) report(ctx context.Context, eventID string, admission domain.Admission) {
	switch admission {
	case domain.AlreadyRecorded:
		service.logger.InfoContext(ctx, "trigger already recorded", "event_id", eventID)
	case domain.Forgotten:
		service.logger.InfoContext(ctx, "trigger refused: its member, organization, app connection or trigger was removed after apps received it",
			"event_id", eventID)
	}
}

func (service *Service) MemberRemoved(ctx context.Context, event authv1.MemberRemoved) error {
	if event.OrganizationID == "" || event.UserID == "" {
		return errors.New("a removed member needs an organization and user")
	}
	return service.store.Forget(ctx, event.OrganizationID, event.UserID)
}

func (service *Service) OrganizationDeleted(ctx context.Context, event authv1.OrganizationDeleted) error {
	if event.OrganizationID == "" {
		return errors.New("a deleted organization needs its id")
	}
	return service.store.Forget(ctx, event.OrganizationID, "")
}

// ConnectionRemoved drops what a member's app reported that is still
// waiting, once they disconnect it, rather than wake their agent for an app
// they have taken away.
func (service *Service) ConnectionRemoved(ctx context.Context, event appsv1.ConnectionRemoved) error {
	if event.OrganizationID == "" || event.ConnectionID == "" {
		return errors.New("a removed connection needs an organization and its id")
	}
	return service.store.Discard(ctx, event.OrganizationID, event.ConnectionID, "")
}

// TriggerUnsubscribed drops what a trigger reported that is still waiting,
// once the member or an admin removes it.
func (service *Service) TriggerUnsubscribed(ctx context.Context, event appsv1.TriggerUnsubscribed) error {
	if event.OrganizationID == "" || event.TriggerID == "" {
		return errors.New("a removed trigger needs an organization and its id")
	}
	return service.store.Discard(ctx, event.OrganizationID, "", event.TriggerID)
}

func (service *Service) Run(ctx context.Context) {
	service.flush(ctx)
	service.sweep(ctx)
	lastSweep := service.now()
	ticker := time.NewTicker(service.config.Interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			service.flush(ctx)
			if service.now().Sub(lastSweep) >= sweepEvery {
				service.sweep(ctx)
				lastSweep = service.now()
			}
		}
	}
}

// claimsPerSweep bounds how many claims one sweep makes while each comes back
// full. A backlog then drains at the database's pace, GroupsPerSweep groups a
// claim, rather than GroupsPerSweep groups a sweep interval; the bound only
// makes a sweep under constant load end, so retention and shutdown get their
// turn.
const claimsPerSweep = 32

func (service *Service) flush(ctx context.Context) {
	for range claimsPerSweep {
		batches, err := service.store.ClaimDue(ctx, service.config.Rule, service.config.ClaimLease, service.config.GroupsPerSweep)
		if err != nil && ctx.Err() == nil {
			service.logger.ErrorContext(ctx, "claim due trigger batches failed", "error", err)
		}
		for _, batch := range batches {
			if err := service.ready(ctx, batch); err != nil && ctx.Err() == nil {
				service.logger.ErrorContext(ctx, "make trigger batch ready", "batch_id", batch.ID, "error", err)
			}
		}
		// A claim that came back short took everything due. One that failed
		// is tried again next sweep.
		if err != nil || ctx.Err() != nil || len(batches) == 0 || len(batches) < service.config.GroupsPerSweep {
			return
		}
	}
}

func (service *Service) ready(ctx context.Context, batch domain.Batch) error {
	if len(batch.Triggers) == 0 {
		return errors.New("claimed batch has no triggers")
	}
	readyAt := service.now().UTC()
	detail, kept, omitted, err := batchDetail(batch, readyAt)
	if err != nil {
		return err
	}
	if len(omitted) > 0 {
		service.logger.WarnContext(ctx, "a trigger too large for any batch goes without its data",
			"batch_id", batch.ID, "triggers", omitted)
	}
	if kept < len(batch.Triggers) {
		overflow := make([]string, 0, len(batch.Triggers)-kept)
		for _, trigger := range batch.Triggers[kept:] {
			overflow = append(overflow, trigger.ID)
		}
		if err := service.store.Unclaim(ctx, batch.ID, overflow); err != nil {
			return err
		}
		batch.Triggers = batch.Triggers[:kept]
	}
	eventID, err := randomID("evt_", 18)
	if err != nil {
		return fmt.Errorf("make batch event id: %w", err)
	}
	return service.store.Complete(ctx, batch, domain.Event{
		ID: eventID, DetailType: batchingv1.TriggerBatchReadyDetailType,
		Data: detail, OccurredAt: readyAt,
	})
}

// batchDetail takes the largest whole prefix that fits. It never truncates an
// app payload: overflow is returned to the waiting set for the next batch. A
// trigger too large to fit even alone is sent without its data instead, and
// named in the ids answered.
func batchDetail(batch domain.Batch, readyAt time.Time) (json.RawMessage, int, []string, error) {
	ready := batchingv1.TriggerBatchReady{
		BatchID: batch.ID, OrganizationID: batch.OrganizationID, MemberID: batch.MemberID,
		Lane: string(batch.Lane), FlushReason: batch.FlushReason, ReadyAt: readyAt,
	}
	var encoded []byte
	var omitted []string
	for index, trigger := range batch.Triggers {
		ready.Triggers = append(ready.Triggers, batchingv1.Trigger{
			EventID: trigger.ID, TriggerID: trigger.TriggerID, ConnectionID: trigger.ConnectionID,
			Toolkit: trigger.Toolkit, Trigger: trigger.Trigger, Data: trigger.Data,
			DataOmitted: trigger.DataOmitted, ReceivedAt: trigger.ReceivedAt,
			Priority: trigger.Classification.Priority, Classifier: trigger.Classification.Classifier,
			DecisionReason: trigger.Classification.Reason,
			TargetSize:     trigger.Schedule.TargetSize, Pressure: trigger.Schedule.Pressure,
			SoftDueAt: trigger.Schedule.SoftDueAt, HardDueAt: trigger.Schedule.HardDueAt,
			PolicyVersion: trigger.Schedule.PolicyVersion,
		})
		ready.EventCount = len(ready.Triggers)
		ready.FirstReceivedAt = ready.Triggers[0].ReceivedAt
		ready.LastReceivedAt = ready.Triggers[len(ready.Triggers)-1].ReceivedAt
		if index == 0 {
			ready.TargetSize = trigger.Schedule.TargetSize
			ready.SoftDueAt = trigger.Schedule.SoftDueAt
			ready.HardDueAt = trigger.Schedule.HardDueAt
			ready.PolicyVersion = trigger.Schedule.PolicyVersion
		} else {
			ready.TargetSize = min(ready.TargetSize, trigger.Schedule.TargetSize)
			ready.SoftDueAt = earlierTime(ready.SoftDueAt, trigger.Schedule.SoftDueAt)
			ready.HardDueAt = earlierTime(ready.HardDueAt, trigger.Schedule.HardDueAt)
			if ready.PolicyVersion != trigger.Schedule.PolicyVersion {
				ready.PolicyVersion = "mixed"
			}
		}
		ready.Pressure += trigger.Schedule.Pressure
		candidate, err := json.Marshal(ready)
		if err != nil {
			return nil, 0, nil, fmt.Errorf("encode trigger batch: %w", err)
		}
		if len(candidate) > batchingv1.MaxBatchDataBytes {
			if index > 0 {
				return encoded, index, omitted, nil
			}
			// Too large to travel even alone: apps checks a payload's raw
			// bytes, and encoding can grow them past this budget (every <, >
			// and & becomes six bytes). Refusing it would re-claim the same
			// group for ever, holding every trigger queued behind it, so it
			// goes the way apps sends a payload it cannot carry: without its
			// data, and saying so.
			alone := &ready.Triggers[len(ready.Triggers)-1]
			alone.Data, alone.DataOmitted = nil, true
			if candidate, err = json.Marshal(ready); err != nil {
				return nil, 0, nil, fmt.Errorf("encode trigger batch: %w", err)
			}
			if len(candidate) > batchingv1.MaxBatchDataBytes {
				return nil, 0, nil, fmt.Errorf("trigger %s makes a %d byte batch even without its data, over the %d byte limit",
					trigger.ID, len(candidate), batchingv1.MaxBatchDataBytes)
			}
			omitted = append(omitted, trigger.ID)
		}
		encoded = candidate
	}
	return encoded, len(batch.Triggers), omitted, nil
}

func earlierTime(first, second time.Time) time.Time {
	if first.IsZero() || (!second.IsZero() && second.Before(first)) {
		return second
	}
	return first
}

func (service *Service) sweep(ctx context.Context) {
	now := service.now()
	if _, err := service.store.DeleteForgotten(ctx, now.Add(-forgottenRetention)); err != nil && ctx.Err() == nil {
		service.logger.WarnContext(ctx, "delete expired removals failed", "error", err)
	}
	before := now.Add(-retention)
	for ctx.Err() == nil {
		deleted, err := service.store.DeleteBatched(ctx, before, sweepBatch)
		if err != nil {
			service.logger.WarnContext(ctx, "delete retained trigger events failed", "error", err)
			return
		}
		if deleted < sweepBatch {
			return
		}
	}
}

func randomID(prefix string, size int) (string, error) {
	value := make([]byte, size)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return prefix + base64.RawURLEncoding.EncodeToString(value), nil
}
