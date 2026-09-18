// Package threads is the agents service's run API beneath HTTP: a member's
// conversations with Krude, called threads, and the runs that answer them.
//
// A thread is an ADK session owned by one member in one organization: its
// user is the member, and its state names the organization, so a member only
// ever reaches their own threads, and only in the organization they were
// started in. Who the member is has already been decided by auth before
// anything here is called.
package threads

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/artifact"
	"google.golang.org/adk/v2/session"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/adksession"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/events"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/pricing"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/runstore"
)

// The ways a request can fail that the member can act on.
var (
	// ErrNotFound reports a thread the member does not have in this
	// organization, whether it does not exist or is someone else's.
	ErrNotFound = errors.New("thread not found")
	// ErrInvalid reports a request that cannot be run as sent.
	ErrInvalid = errors.New("invalid request")
	// ErrConfirmationPending reports a message sent while a tool waits for
	// the member to approve or decline it: the answer has to come first.
	ErrConfirmationPending = errors.New("a tool is waiting for you to approve or decline it")
	// ErrRunInProgress reports a thread that is already being answered.
	ErrRunInProgress = runstore.ErrRunInProgress
)

// The state keys the run API keeps in a thread beside Krude's own.
const (
	stateTitle     = "thread_title"
	stateCreatedAt = "thread_created_at"
	// stateWakeup is set, by the message itself, on a message that a batch
	// of app events sent rather than the member.
	stateWakeup = "wakeup_batch"
)

const (
	defaultRunTimeout = 10 * time.Minute
	defaultLease      = time.Minute
	// maxTitleRunes bounds a thread's title, taken from its first message.
	maxTitleRunes = 80
	// maxMessageRunes bounds what a member sends in one message.
	maxMessageRunes = 32_000
)

// Member is who a request acts for. Auth decided it.
type Member struct {
	UserID         string
	OrganizationID string
	// AccessToken is the member's own token. A run carries it so the tools
	// that act for the member present it as theirs; it is never stored or
	// logged.
	AccessToken string
}

// Thread is one conversation.
type Thread struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	// CreatedAt is unset for a thread the run API did not start, such as
	// one from the development UI.
	CreatedAt time.Time `json:"created_at,omitzero"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Detail is a thread with everything that happened in it.
type Detail struct {
	Thread
	Items []Item `json:"items"`
	// Pending are the confirmations waiting for the member's answer, by the
	// call ID each answer names. A run on this thread has to answer every one
	// of them before anything else.
	Pending []string `json:"pending_confirmations"`
}

// RunStore keeps the record of every run and holds a thread for the run
// answering it. runstore.Store implements it.
type RunStore interface {
	Start(ctx context.Context, run runstore.Run, lease time.Duration) error
	Renew(ctx context.Context, runID string, lease time.Duration) error
	Finish(ctx context.Context, runID string, outcome runstore.Outcome, event events.Event) error
}

// Config is what the service runs with.
type Config struct {
	// AppName is the ADK app threads belong to: Krude's name.
	AppName string
	// Agent answers every run.
	Agent agent.Agent
	// Sessions stores threads. It should seed a member's state when a thread
	// is created, as adksession's does.
	Sessions session.Service
	// Artifacts keeps what runs produce. Optional.
	Artifacts artifact.Service
	Runs      RunStore
	// Prices turns a run's tokens into what it cost.
	Prices *pricing.Table
	Logger *slog.Logger
	// RunTimeout bounds one run. Lease is how long a run holds its thread
	// without renewing; a run renews it every third of that.
	RunTimeout time.Duration
	Lease      time.Duration
}

// Service is the run API.
type Service struct {
	appName    string
	agent      agent.Agent
	sessions   session.Service
	artifacts  artifact.Service
	runs       RunStore
	prices     *pricing.Table
	logger     *slog.Logger
	runTimeout time.Duration
	lease      time.Duration
}

// New returns the service.
func New(config Config) (*Service, error) {
	switch {
	case config.AppName == "":
		return nil, errors.New("threads: an app name is required")
	case config.Agent == nil:
		return nil, errors.New("threads: an agent is required")
	case config.Sessions == nil:
		return nil, errors.New("threads: a session service is required")
	case config.Runs == nil:
		return nil, errors.New("threads: a run store is required")
	case config.Prices == nil:
		return nil, errors.New("threads: a price table is required")
	}
	logger := config.Logger
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	return &Service{
		appName:    config.AppName,
		agent:      config.Agent,
		sessions:   config.Sessions,
		artifacts:  config.Artifacts,
		runs:       config.Runs,
		prices:     config.Prices,
		logger:     logger,
		runTimeout: cmp.Or(config.RunTimeout, defaultRunTimeout),
		lease:      cmp.Or(config.Lease, defaultLease),
	}, nil
}

// Create starts a thread for the member in their organization.
func (service *Service) Create(ctx context.Context, member Member) (Thread, error) {
	return service.CreateTitled(ctx, member, "")
}

// CreateTitled starts a thread with a title of its own, rather than one
// taken from its first message.
func (service *Service) CreateTitled(ctx context.Context, member Member, title string) (Thread, error) {
	state := map[string]any{
		adksession.StateOrganizationID: member.OrganizationID,
		stateCreatedAt:                 time.Now().UTC().Format(time.RFC3339Nano),
	}
	if title = bounded(strings.Join(strings.Fields(title), " "), maxTitleRunes); title != "" {
		state[stateTitle] = title
	}
	created, err := service.sessions.Create(ctx, &session.CreateRequest{
		AppName: service.appName,
		UserID:  member.UserID,
		State:   state,
	})
	if err != nil {
		return Thread{}, fmt.Errorf("create thread: %w", err)
	}
	return threadOf(created.Session), nil
}

// List returns the member's threads in their organization, the most
// recently active first.
func (service *Service) List(ctx context.Context, member Member) ([]Thread, error) {
	listed, err := service.sessions.List(ctx, &session.ListRequest{AppName: service.appName, UserID: member.UserID})
	if err != nil {
		return nil, fmt.Errorf("list threads: %w", err)
	}
	threads := make([]Thread, 0, len(listed.Sessions))
	for _, stored := range listed.Sessions {
		if stored.UserID() != member.UserID || stateString(stored.State(), adksession.StateOrganizationID) != member.OrganizationID {
			continue
		}
		threads = append(threads, threadOf(stored))
	}
	slices.SortStableFunc(threads, func(a, b Thread) int {
		return cmp.Or(b.UpdatedAt.Compare(a.UpdatedAt), strings.Compare(a.ID, b.ID))
	})
	return threads, nil
}

// Get returns one of the member's threads with its history.
func (service *Service) Get(ctx context.Context, member Member, threadID string) (Detail, error) {
	stored, err := service.thread(ctx, member, threadID)
	if err != nil {
		return Detail{}, err
	}
	history := sessionEvents(stored)
	return Detail{
		Thread:  threadOf(stored),
		Items:   historyItems(history),
		Pending: nonNil(pendingConfirmations(history, service.agent.Name())),
	}, nil
}

// Delete removes one of the member's threads, and what its runs kept.
func (service *Service) Delete(ctx context.Context, member Member, threadID string) error {
	if _, err := service.thread(ctx, member, threadID); err != nil {
		return err
	}
	if err := service.sessions.Delete(ctx, &session.DeleteRequest{
		AppName: service.appName, UserID: member.UserID, SessionID: threadID,
	}); err != nil {
		return fmt.Errorf("delete thread: %w", err)
	}
	service.deleteArtifacts(ctx, member, threadID)
	return nil
}

// Forget removes every thread in organizationID — only userID's, when one is
// named — with what their runs kept, for a member who left the organization
// or an organization that is gone. Nothing a conversation held should
// outlive the place it was held in. It reports how many it removed; running
// it again removes nothing more.
func (service *Service) Forget(ctx context.Context, organizationID, userID string) (int, error) {
	if organizationID == "" {
		return 0, errors.New("threads: forgetting needs an organization")
	}
	listed, err := service.sessions.List(ctx, &session.ListRequest{AppName: service.appName, UserID: userID})
	if err != nil {
		return 0, fmt.Errorf("list threads to forget: %w", err)
	}
	removed := 0
	for _, stored := range listed.Sessions {
		if stateString(stored.State(), adksession.StateOrganizationID) != organizationID {
			continue
		}
		if err := service.sessions.Delete(ctx, &session.DeleteRequest{
			AppName: service.appName, UserID: stored.UserID(), SessionID: stored.ID(),
		}); err != nil && !errors.Is(err, session.ErrNotFound) {
			return removed, fmt.Errorf("forget thread: %w", err)
		}
		service.deleteArtifacts(ctx, Member{UserID: stored.UserID(), OrganizationID: organizationID}, stored.ID())
		removed++
	}
	return removed, nil
}

// deleteArtifacts removes the files a thread's runs kept. The thread is
// already gone, so a failure here is logged rather than reported: the store
// expires what is left.
func (service *Service) deleteArtifacts(ctx context.Context, member Member, threadID string) {
	if service.artifacts == nil {
		return
	}
	listed, err := service.artifacts.List(ctx, &artifact.ListRequest{
		AppName: service.appName, UserID: member.UserID, SessionID: threadID,
	})
	if err != nil {
		service.logger.WarnContext(ctx, "list a deleted thread's artifacts", "thread_id", threadID, "error", err)
		return
	}
	for _, name := range listed.FileNames {
		// A "user:" artifact is the member's, shared by all their threads,
		// and outlives any one of them.
		if strings.HasPrefix(name, "user:") {
			continue
		}
		if err := service.artifacts.Delete(ctx, &artifact.DeleteRequest{
			AppName: service.appName, UserID: member.UserID, SessionID: threadID, FileName: name,
		}); err != nil {
			service.logger.WarnContext(ctx, "delete a deleted thread's artifact", "thread_id", threadID, "error", err)
		}
	}
}

// thread reads one of the member's threads, answering ErrNotFound for one
// that is not theirs in this organization.
func (service *Service) thread(ctx context.Context, member Member, threadID string) (session.Session, error) {
	if strings.TrimSpace(threadID) == "" {
		return nil, ErrNotFound
	}
	got, err := service.sessions.Get(ctx, &session.GetRequest{
		AppName: service.appName, UserID: member.UserID, SessionID: threadID,
	})
	if errors.Is(err, session.ErrNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("read thread: %w", err)
	}
	stored := got.Session
	if stored == nil || stored.UserID() != member.UserID ||
		stateString(stored.State(), adksession.StateOrganizationID) != member.OrganizationID {
		return nil, ErrNotFound
	}
	return stored, nil
}

func threadOf(stored session.Session) Thread {
	thread := Thread{
		ID:        stored.ID(),
		Title:     stateString(stored.State(), stateTitle),
		UpdatedAt: stored.LastUpdateTime().UTC(),
	}
	if created, err := time.Parse(time.RFC3339Nano, stateString(stored.State(), stateCreatedAt)); err == nil {
		thread.CreatedAt = created.UTC()
	}
	return thread
}

func sessionEvents(stored session.Session) []*session.Event {
	events := stored.Events()
	if events == nil {
		return nil
	}
	collected := make([]*session.Event, 0, events.Len())
	for event := range events.All() {
		collected = append(collected, event)
	}
	return collected
}

func stateString(state session.ReadonlyState, key string) string {
	if state == nil {
		return ""
	}
	value, err := state.Get(key)
	if err != nil {
		return ""
	}
	text, _ := value.(string)
	return text
}

func nonNil(values []string) []string {
	if values == nil {
		return []string{}
	}
	return values
}
