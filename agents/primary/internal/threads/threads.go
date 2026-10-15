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
	"bytes"
	"cmp"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/artifact"
	"google.golang.org/adk/v2/session"
	"google.golang.org/genai"

	agentsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/agents/v1"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/adkmemory"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/adksession"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/agents"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/events"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/pricing"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/runprofile"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/runstore"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/s3artifact"
)

// The ways a request can fail that the member can act on.
var (
	// ErrNotFound reports a thread the member does not have in this
	// organization, whether it does not exist or is someone else's.
	ErrNotFound = errors.New("thread not found")
	// ErrInvalid reports a request that cannot be run as sent.
	ErrInvalid = errors.New("invalid request")
	// ErrConflict reports a file changed after an editor opened it.
	ErrConflict = errors.New("artifact version conflict")
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
	// stateOrigin says why a thread exists: OriginChat,
	// OriginSideConversation or OriginWakeup. A thread created before this
	// key existed, or by other code that does not set it, has none in state
	// and reads as OriginChat.
	stateOrigin = "thread_origin"
	// stateWakeup is set, by the message itself, on a message that a batch
	// of app events sent rather than the member.
	stateWakeup = "wakeup_batch"
)

// The origins a thread can have. OriginChat is a member's own conversation
// with Krude; OriginSideConversation is a thread the web app opened to run
// the member's own agent on an internal prompt, kept out of their chat list.
// OriginWakeup is the thread their apps' events are reported in, which the
// member did not start and which is kept out of their chat list as well: only
// Oryxa's own waker makes one (CreateWakeup), and CreateFrom refuses it.
// OriginAll is not an origin a thread can have — it only selects every
// origin, in ListFrom.
const (
	OriginChat             = "chat"
	OriginSideConversation = "side_conversation"
	OriginWakeup           = "wakeup"
	OriginAll              = "all"
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
	// AgentID, AgentName and AgentKind are the persistent identity answering
	// this request. Runs refresh them into state, so renames affect existing
	// conversations without changing ADK's immutable machine name.
	AgentID   string
	AgentName string
	AgentKind string
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
	// Origin is OriginChat, OriginSideConversation or OriginWakeup. A thread
	// with none in its state — every thread created before origins existed,
	// and any other code's threads — reads as OriginChat.
	Origin string `json:"origin"`
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
	Cancel(ctx context.Context, runID string) error
}

// Config is what the service runs with.
type Config struct {
	// AppName is the ADK app threads belong to: Krude's name.
	AppName string
	// Agent answers every run a member starts, and Unattended every run a
	// trigger starts, with nobody watching; it defaults to Agent. Both must
	// answer to the same name, since they take turns in one thread.
	Agent      agent.Agent
	Unattended agent.Agent
	// UnattendedTools names the tool set Unattended was built with, which
	// with what started a run is its mode in runprofile's table; empty is
	// runprofile.ToolsReads, Krude's own wake-up agent.
	UnattendedTools runprofile.Toolset
	// Sessions stores threads. It should seed a member's state when a thread
	// is created, as adksession's does.
	Sessions session.Service
	// Artifacts keeps what runs produce. Optional.
	Artifacts artifact.Service
	// Memory is what Krude remembers across a member's threads, scoped to
	// the organization each run belongs to. Optional: without it a run has
	// no memory at all, which is what ADK does with no memory service.
	Memory *adkmemory.Service
	Runs   RunStore
	// Prices turns a run's tokens into what it cost.
	Prices *pricing.Table
	Logger *slog.Logger
	// RunTimeout bounds one run. Lease is how long a run holds its thread
	// without renewing; a run renews it every third of that.
	RunTimeout time.Duration
	Lease      time.Duration
	// InitialState is copied into each new thread. It lets a persistent agent
	// carry its stable id and renameable display identity without changing
	// ADK's machine name.
	InitialState map[string]any
	// Drain is the process's: every thread service in it shares one, so that
	// stopping the process lets every run in it finish, or interrupts it. Nil
	// tracks nothing, and a stopping process cuts its runs short.
	Drain *Drain
}

// Service is the run API.
type Service struct {
	artifactSaveMu  sync.Mutex
	appName         string
	agent           agent.Agent
	unattended      agent.Agent
	unattendedTools runprofile.Toolset
	sessions        session.Service
	artifacts       artifact.Service
	memory          *adkmemory.Service
	runs            RunStore
	prices          *pricing.Table
	logger          *slog.Logger
	runTimeout      time.Duration
	lease           time.Duration
	initialState    map[string]any
	drain           *Drain
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
	case config.Unattended != nil && config.Unattended.Name() != config.Agent.Name():
		return nil, errors.New("threads: the unattended agent must answer to the agent's own name")
	}
	logger := config.Logger
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	unattended := config.Unattended
	if unattended == nil {
		unattended = config.Agent
	}
	return &Service{
		appName:         config.AppName,
		agent:           config.Agent,
		unattended:      unattended,
		unattendedTools: config.UnattendedTools,
		sessions:        config.Sessions,
		artifacts:       config.Artifacts,
		memory:          config.Memory,
		runs:            config.Runs,
		prices:          config.Prices,
		logger:          logger,
		runTimeout:      cmp.Or(config.RunTimeout, defaultRunTimeout),
		lease:           cmp.Or(config.Lease, defaultLease),
		initialState:    cloneState(config.InitialState),
		drain:           config.Drain,
	}, nil
}

func cloneState(source map[string]any) map[string]any {
	if len(source) == 0 {
		return nil
	}
	cloned := make(map[string]any, len(source))
	for key, value := range source {
		cloned[key] = value
	}
	return cloned
}

func memberAgentState(member Member) map[string]any {
	state := map[string]any{}
	if member.AgentID != "" {
		state[agents.AgentIDKey] = member.AgentID
	}
	if member.AgentName != "" {
		state[agents.AgentNameKey] = member.AgentName
	}
	if member.AgentKind != "" {
		state[agents.AgentKindKey] = member.AgentKind
	}
	if len(state) == 0 {
		return nil
	}
	return state
}

// Create starts a chat thread for the member in their organization.
func (service *Service) Create(ctx context.Context, member Member) (Thread, error) {
	return service.CreateTitled(ctx, member, "")
}

// CreateTitled starts a chat thread with a title of its own, rather than one
// taken from its first message.
func (service *Service) CreateTitled(ctx context.Context, member Member, title string) (Thread, error) {
	return service.create(ctx, member, title, OriginChat)
}

// CreateFrom starts a thread with an explicit origin: OriginChat or
// OriginSideConversation. origin is trimmed first; "" behaves like
// OriginChat. Anything else is ErrInvalid.
func (service *Service) CreateFrom(ctx context.Context, member Member, origin string) (Thread, error) {
	origin = strings.TrimSpace(origin)
	if origin == "" {
		origin = OriginChat
	}
	if origin != OriginChat && origin != OriginSideConversation {
		return Thread{}, fmt.Errorf("%w: origin must be chat or side_conversation", ErrInvalid)
	}
	return service.create(ctx, member, "", origin)
}

// CreateWakeup starts the thread the member's apps' events are reported in,
// with a title of its own. Only the waker calls it: CreateFrom, which the
// HTTP route uses, refuses OriginWakeup, so a client cannot make one.
func (service *Service) CreateWakeup(ctx context.Context, member Member, title string) (Thread, error) {
	return service.create(ctx, member, title, OriginWakeup)
}

// create starts a thread carrying origin in its state, next to
// adksession.StateOrganizationID — the same state map CreateTitled has
// always seeded.
func (service *Service) create(ctx context.Context, member Member, title, origin string) (Thread, error) {
	state := map[string]any{
		adksession.StateOrganizationID: member.OrganizationID,
		stateCreatedAt:                 time.Now().UTC().Format(time.RFC3339Nano),
		stateOrigin:                    origin,
	}
	for key, value := range service.initialState {
		state[key] = value
	}
	for key, value := range memberAgentState(member) {
		state[key] = value
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

// List returns the member's chat threads in their organization, the most
// recently active first. Their side conversations' and wake-ups' threads are
// not among them: ListFrom reaches those.
func (service *Service) List(ctx context.Context, member Member) ([]Thread, error) {
	return service.ListFrom(ctx, member, OriginChat)
}

// ListFrom returns the member's threads of one origin — chat,
// side_conversation or wakeup — or, with OriginAll, every origin together,
// the most recently active first. Anything else, including blank, is
// ErrInvalid.
func (service *Service) ListFrom(ctx context.Context, member Member, origin string) ([]Thread, error) {
	switch origin {
	case OriginChat, OriginSideConversation, OriginWakeup, OriginAll:
	default:
		return nil, fmt.Errorf("%w: origin must be chat, side_conversation, wakeup or all", ErrInvalid)
	}
	listed, err := service.sessions.List(ctx, &session.ListRequest{AppName: service.appName, UserID: member.UserID})
	if err != nil {
		return nil, fmt.Errorf("list threads: %w", err)
	}
	threads := make([]Thread, 0, len(listed.Sessions))
	for _, stored := range listed.Sessions {
		if stored.UserID() != member.UserID || stateString(stored.State(), adksession.StateOrganizationID) != member.OrganizationID {
			continue
		}
		thread := threadOf(stored)
		if origin != OriginAll && thread.Origin != origin {
			continue
		}
		threads = append(threads, thread)
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
		Items:   displayAgent(historyItems(history), service.agent.Name(), member.AgentName),
		Pending: nonNil(pendingConfirmations(history, service.agent.Name())),
	}, nil
}

// Origin says why one of the member's threads exists: OriginChat,
// OriginSideConversation or OriginWakeup. It reads the thread's state and
// next to none of its history, so it is cheap to ask of a long thread.
func (service *Service) Origin(ctx context.Context, member Member, threadID string) (string, error) {
	stored, err := service.readThread(ctx, member, threadID, 1)
	if err != nil {
		return "", err
	}
	return threadOf(stored).Origin, nil
}

// renameInvocation names the invocation a rename's event belongs to. It is no
// run's: a rename writes the title and nothing an agent reads.
const renameInvocation = "oryxa-rename"

// Rename gives one of the member's threads a title of their own, in place of
// the one taken from its first message. The title is trimmed, its spaces
// collapsed as a created thread's are, and must then hold 1 to maxTitleRunes
// characters: a longer one is refused rather than cut. The Requests thread
// keeps RequestsTitle: it is Oryxa's, and notes are written to it without the
// hold below, so a rename of it is refused (ErrInvalid).
//
// The title is the thread's state, so it is written as ADK writes state: an
// event of a state change alone, by the member, which shows nothing in the
// thread's history and nothing to the model. ADK refuses a write to a session
// changed since it was read, so a run writing the thread at the same moment
// would fail; the rename holds the thread the way a run does while it writes,
// and answers ErrRunInProgress while a run is answering it. The hold is
// released without a trace: no run is recorded, and no event published.
//
// The rename is a write to the thread like any other, so it moves the
// thread's updated_at and with it the thread to the top of List: the member
// has just touched it.
func (service *Service) Rename(ctx context.Context, member Member, threadID, title string) (Thread, error) {
	title = strings.Join(strings.Fields(title), " ")
	switch count := utf8.RuneCountInString(title); {
	case count == 0:
		return Thread{}, fmt.Errorf("%w: a title needs at least one character", ErrInvalid)
	case count > maxTitleRunes:
		return Thread{}, fmt.Errorf("%w: a title holds at most %d characters", ErrInvalid, maxTitleRunes)
	}
	if threadID == RequestsThreadID(member.OrganizationID) {
		return Thread{}, fmt.Errorf("%w: the Requests thread keeps its title", ErrInvalid)
	}
	// The thread must be the member's before the rename holds it, as before
	// a run does.
	if _, err := service.readThread(ctx, member, threadID, 1); err != nil {
		return Thread{}, err
	}
	holdID, err := newRunID()
	if err != nil {
		return Thread{}, err
	}
	if err := service.runs.Start(ctx, runstore.Run{
		ID:             holdID,
		SessionID:      threadID,
		UserID:         member.UserID,
		OrganizationID: member.OrganizationID,
		StartedBy:      agentsv1.StartedByMember,
	}, service.lease); err != nil {
		if errors.Is(err, runstore.ErrRunInProgress) {
			return Thread{}, ErrRunInProgress
		}
		return Thread{}, fmt.Errorf("hold thread to rename it: %w", err)
	}
	defer func() {
		if err := service.runs.Cancel(context.WithoutCancel(ctx), holdID); err != nil {
			service.logger.WarnContext(ctx, "release a renamed thread", "thread_id", threadID, "error", err)
		}
	}()
	// Read again now that the rename holds the thread: the write must be
	// against the thread as it is, or ADK refuses it as stale.
	stored, err := service.readThread(ctx, member, threadID, 1)
	if err != nil {
		return Thread{}, err
	}
	event := session.NewEvent(ctx, renameInvocation)
	event.Author = adkUserAuthor
	event.Actions.StateDelta = map[string]any{stateTitle: title}
	if err := service.sessions.AppendEvent(ctx, stored, event); err != nil {
		return Thread{}, fmt.Errorf("rename thread: %w", err)
	}
	renamed, err := service.readThread(ctx, member, threadID, 1)
	if err != nil {
		return Thread{}, err
	}
	return threadOf(renamed), nil
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
	// What a conversation taught Krude goes with the conversation: memory
	// distilled from threads in this organization would otherwise outlive
	// the organization itself. It is forgotten even when no thread is left
	// to remove, and a failure here does not hide one below — the queue
	// delivers at least once and both halves are safe to repeat.
	var failures []error
	if service.memory != nil {
		if err := service.memory.Forget(ctx, organizationID, userID); err != nil {
			failures = append(failures, err)
		}
	}
	listed, err := service.sessions.List(ctx, &session.ListRequest{AppName: service.appName, UserID: userID})
	if err != nil {
		return 0, errors.Join(append(failures, fmt.Errorf("list threads to forget: %w", err))...)
	}
	removed := 0
	for _, stored := range listed.Sessions {
		if stateString(stored.State(), adksession.StateOrganizationID) != organizationID {
			continue
		}
		if err := service.sessions.Delete(ctx, &session.DeleteRequest{
			AppName: service.appName, UserID: stored.UserID(), SessionID: stored.ID(),
		}); err != nil && !errors.Is(err, session.ErrNotFound) {
			return removed, errors.Join(append(failures, fmt.Errorf("forget thread: %w", err))...)
		}
		service.deleteArtifacts(ctx, Member{UserID: stored.UserID(), OrganizationID: organizationID}, stored.ID())
		removed++
	}
	return removed, errors.Join(failures...)
}

// File is an artifact's content.
type File struct {
	Name     string
	MIMEType string
	Data     []byte
	Version  int64
}

// resolveArtifact checks that the thread is the member's and that the file has
// the version asked for, the latest when version is 0, reading only what the
// store keeps about the file and not its bytes. It returns the request that
// loads exactly that version, so bytes and version come from one revision.
func (service *Service) resolveArtifact(ctx context.Context, member Member, threadID, name string, version int64) (*artifact.LoadRequest, int64, error) {
	if service.artifacts == nil || strings.TrimSpace(name) == "" || version < 0 {
		return nil, 0, ErrNotFound
	}
	if _, err := service.thread(ctx, member, threadID); err != nil {
		return nil, 0, err
	}
	ctx = s3artifact.WithOrganization(ctx, member.OrganizationID)
	request := &artifact.LoadRequest{
		AppName: service.appName, UserID: member.UserID, SessionID: threadID, FileName: name, Version: version,
	}
	// A name ADK would refuse, such as one with a path separator, names no
	// file a run could have kept.
	if request.Validate() != nil {
		return nil, 0, ErrNotFound
	}
	metadata, err := service.artifacts.GetArtifactVersion(ctx, &artifact.GetArtifactVersionRequest{
		AppName: service.appName, UserID: member.UserID, SessionID: threadID, FileName: name, Version: version,
	})
	if errors.Is(err, fs.ErrNotExist) {
		return nil, 0, ErrNotFound
	}
	if err != nil {
		return nil, 0, fmt.Errorf("read artifact version: %w", err)
	}
	if metadata == nil || metadata.ArtifactVersion == nil {
		return nil, 0, ErrNotFound
	}
	request.Version = metadata.ArtifactVersion.Version
	return request, request.Version, nil
}

// Artifact reads a file one of the member's threads kept: the latest
// version, or the one named. A "user:" file is the member's own, shared by
// all their threads, and is read through any of them.
func (service *Service) Artifact(ctx context.Context, member Member, threadID, name string, version int64) (File, error) {
	request, version, err := service.resolveArtifact(ctx, member, threadID, name, version)
	if err != nil {
		return File{}, err
	}
	loaded, err := service.artifacts.Load(s3artifact.WithOrganization(ctx, member.OrganizationID), request)
	if errors.Is(err, fs.ErrNotExist) {
		return File{}, ErrNotFound
	}
	if err != nil {
		return File{}, fmt.Errorf("read artifact: %w", err)
	}
	part := loaded.Part
	switch {
	case part == nil:
		return File{}, ErrNotFound
	case part.InlineData != nil:
		return File{Name: name, MIMEType: part.InlineData.MIMEType, Data: part.InlineData.Data, Version: version}, nil
	case part.Text != "":
		return File{Name: name, MIMEType: "text/plain; charset=utf-8", Data: []byte(part.Text), Version: version}, nil
	default:
		return File{}, ErrNotFound
	}
}

// ArtifactVersion is the version a file is at now, from what the store keeps
// about it: the file itself, which can be tens of megabytes, is not read. The
// document editor asks it to name the version it opens.
func (service *Service) ArtifactVersion(ctx context.Context, member Member, threadID, name string) (int64, error) {
	_, version, err := service.resolveArtifact(ctx, member, threadID, name, 0)
	return version, err
}

// SaveArtifact stores an edited version only if the version the editor opened
// is still current. Explicit version allocation also prevents lost updates in
// the S3 artifact store when two saves race. The new version keeps the media
// type of the one it replaces: what an editor made of a file is not a reason
// to change what the file is.
func (service *Service) SaveArtifact(ctx context.Context, member Member, threadID, name string, expectedVersion int64, data []byte) (int64, error) {
	if expectedVersion < 1 || len(data) == 0 {
		return 0, ErrInvalid
	}
	service.artifactSaveMu.Lock()
	defer service.artifactSaveMu.Unlock()
	current, err := service.Artifact(ctx, member, threadID, name, 0)
	if err != nil {
		return 0, err
	}
	if current.Version == expectedVersion+1 && bytes.Equal(current.Data, data) {
		return current.Version, nil // a document server retry of a completed save
	}
	if current.Version != expectedVersion {
		return 0, ErrConflict
	}
	mimeType := cmp.Or(strings.TrimSpace(current.MIMEType), "application/octet-stream")
	saved, err := service.artifacts.Save(s3artifact.WithOrganization(ctx, member.OrganizationID), &artifact.SaveRequest{
		AppName: service.appName, UserID: member.UserID, SessionID: threadID, FileName: name,
		Version: expectedVersion + 1, Part: genai.NewPartFromBytes(data, mimeType),
	})
	if err != nil {
		if errors.Is(err, s3artifact.ErrVersionConflict) {
			return 0, ErrConflict
		}
		return 0, fmt.Errorf("save edited artifact: %w", err)
	}
	return saved.Version, nil
}

// deleteArtifacts removes the files a thread's runs kept. The thread is
// already gone, so a failure here is logged rather than reported: the store
// expires what is left.
func (service *Service) deleteArtifacts(ctx context.Context, member Member, threadID string) {
	if service.artifacts == nil {
		return
	}
	ctx = s3artifact.WithOrganization(ctx, member.OrganizationID)
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
	return service.readThread(ctx, member, threadID, 0)
}

// readThread is thread, reading no more than the latest recent events of the
// thread's history, or all of it when recent is 0. What the thread's state
// says does not depend on how much of the history comes with it.
func (service *Service) readThread(ctx context.Context, member Member, threadID string, recent int) (session.Session, error) {
	if strings.TrimSpace(threadID) == "" {
		return nil, ErrNotFound
	}
	got, err := service.sessions.Get(ctx, &session.GetRequest{
		AppName: service.appName, UserID: member.UserID, SessionID: threadID, NumRecentEvents: recent,
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
		Origin:    cmp.Or(stateString(stored.State(), stateOrigin), OriginChat),
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
