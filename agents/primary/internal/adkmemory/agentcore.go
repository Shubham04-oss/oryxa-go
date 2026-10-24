package adkmemory

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/bedrockagentcore"
	"github.com/aws/aws-sdk-go-v2/service/bedrockagentcore/types"
	"golang.org/x/time/rate"
	"google.golang.org/genai"

	"google.golang.org/adk/v2/memory"
	"google.golang.org/adk/v2/session"
)

// AgentCore Memory's own limits, as its API reference gives them.
const (
	// maxEventPayloads is how many messages one CreateEvent carries.
	maxEventPayloads = 100
	// maxMessageBytes is what this backend keeps of one message. AgentCore
	// takes 100 KB a message; the margin is for what JSON escaping adds to
	// the text on the wire.
	maxMessageBytes = 90_000
	// maxActorIDLength is the longest actor id AgentCore takes.
	maxActorIDLength = 255
	// maxDeleteBatch is how many records one BatchDeleteMemoryRecords takes.
	maxDeleteBatch = 100
	// eventDeletesPerSecond stays under DeleteEvent's 20 TPS quota, shared
	// by every Forget this backend runs at once.
	eventDeletesPerSecond = 15
)

// primaryAgent is the actor-id segment of a member's primary agent, whose
// scope carries no agent id. A specialist cannot be named it.
const primaryAgent = "primary"

// AgentCoreMemoryOptions is what an AgentCore Memory backend runs against.
type AgentCoreMemoryOptions struct {
	// AWS carries the region and credentials of the account the memory is
	// in.
	AWS aws.Config
	// MemoryID is the AgentCore memory's id. The memory needs one semantic
	// strategy whose namespace template is agentCoreNamespaceTemplate.
	MemoryID string
	// Recalled is how many memories a search returns at most. Zero means
	// defaultRecalled; anything above maxRecalled is maxRecalled.
	Recalled int
	// Logger notes a Forget for an id this layout cannot hold. Nil discards.
	Logger *slog.Logger
}

// agentCoreAPI is the part of AgentCore Memory's data plane this backend
// uses, narrowed so the backend can be tested without one.
// [bedrockagentcore.Client] implements it.
type agentCoreAPI interface {
	CreateEvent(ctx context.Context, params *bedrockagentcore.CreateEventInput, optFns ...func(*bedrockagentcore.Options)) (*bedrockagentcore.CreateEventOutput, error)
	RetrieveMemoryRecords(ctx context.Context, params *bedrockagentcore.RetrieveMemoryRecordsInput, optFns ...func(*bedrockagentcore.Options)) (*bedrockagentcore.RetrieveMemoryRecordsOutput, error)
	ListMemoryRecords(ctx context.Context, params *bedrockagentcore.ListMemoryRecordsInput, optFns ...func(*bedrockagentcore.Options)) (*bedrockagentcore.ListMemoryRecordsOutput, error)
	BatchDeleteMemoryRecords(ctx context.Context, params *bedrockagentcore.BatchDeleteMemoryRecordsInput, optFns ...func(*bedrockagentcore.Options)) (*bedrockagentcore.BatchDeleteMemoryRecordsOutput, error)
	ListActors(ctx context.Context, params *bedrockagentcore.ListActorsInput, optFns ...func(*bedrockagentcore.Options)) (*bedrockagentcore.ListActorsOutput, error)
	ListSessions(ctx context.Context, params *bedrockagentcore.ListSessionsInput, optFns ...func(*bedrockagentcore.Options)) (*bedrockagentcore.ListSessionsOutput, error)
	ListEvents(ctx context.Context, params *bedrockagentcore.ListEventsInput, optFns ...func(*bedrockagentcore.Options)) (*bedrockagentcore.ListEventsOutput, error)
	DeleteEvent(ctx context.Context, params *bedrockagentcore.DeleteEventInput, optFns ...func(*bedrockagentcore.Options)) (*bedrockagentcore.DeleteEventOutput, error)
}

var _ agentCoreAPI = (*bedrockagentcore.Client)(nil)

// AgentCoreMemory keeps what Krude remembers in Amazon Bedrock AgentCore
// Memory. A run's spoken turns go in as one short-term event; AgentCore's
// semantic strategy extracts the facts worth keeping from it, asynchronously,
// into long-term records under the actor's namespace, which is what a search
// reads.
//
// Each scope is one actor, `<organization>/<member>/<agent or primary>`, so
// the organization is part of every address the service matches on. Where the
// actor id and its namespace come from is [agentCoreActor] and
// [agentCoreNamespace] alone, so the layout can change in one place.
type AgentCoreMemory struct {
	api      agentCoreAPI
	memoryID string
	recalled int32
	// forgetTimeout is what one Forget allows itself, listing included; zero
	// means defaultForgetTimeout.
	forgetTimeout time.Duration
	// deletingEvents paces DeleteEvent across every Forget at once.
	deletingEvents *rate.Limiter
	// now is the clock a turn with no time of its own is stamped with.
	now    func() time.Time
	logger *slog.Logger
}

var _ Backend = (*AgentCoreMemory)(nil)

// NewAgentCoreMemory returns the backend for the AgentCore memory options
// name. It contacts nothing until it is used.
func NewAgentCoreMemory(options AgentCoreMemoryOptions) (*AgentCoreMemory, error) {
	return newAgentCoreMemory(bedrockagentcore.NewFromConfig(options.AWS), options)
}

func newAgentCoreMemory(api agentCoreAPI, options AgentCoreMemoryOptions) (*AgentCoreMemory, error) {
	switch {
	case strings.TrimSpace(options.MemoryID) == "":
		return nil, errors.New("adkmemory: an AgentCore memory needs its memory id")
	case options.Recalled < 0:
		return nil, errors.New("adkmemory: an AgentCore memory cannot recall fewer than no memories")
	}
	return &AgentCoreMemory{
		api:            api,
		memoryID:       strings.TrimSpace(options.MemoryID),
		recalled:       int32(min(cmp.Or(options.Recalled, defaultRecalled), maxRecalled)),
		deletingEvents: rate.NewLimiter(eventDeletesPerSecond, 1),
		now:            time.Now,
		logger:         cmp.Or(options.Logger, slog.New(slog.DiscardHandler)),
	}, nil
}

// actorSegment is what one part of an actor id may be. Organization, member
// and agent ids are base64url, which this admits; a "/" would add a segment
// and let one scope's address reach into another's, and is refused.
var actorSegment = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// agentCoreNamespaceTemplate is the namespace template the memory's semantic
// strategy is created with (deploy/production/memory.tf). [agentCoreNamespace]
// renders it the way AgentCore does.
const agentCoreNamespaceTemplate = "/m/{actorId}/"

// errUnheld is an id this layout cannot hold, so nothing was ever kept
// under it.
var errUnheld = errors.New("adkmemory: an id AgentCore Memory cannot hold")

// checkSegment refuses an id that cannot be one segment of an actor id.
func checkSegment(name, value string) error {
	switch {
	case strings.Contains(value, "/"):
		return fmt.Errorf("%w: a %s containing \"/\" (%q), which separates the parts of an actor id", errUnheld, name, value)
	case !actorSegment.MatchString(value):
		return fmt.Errorf("%w: a %s may hold only letters, digits, \"_\" and \"-\", not %q", errUnheld, name, value)
	}
	return nil
}

// agentCoreActor is the actor id scope is kept under:
// `<organization>/<member>/<agent>`, with primaryAgent for the member's
// primary agent. It is the one place the layout is decided.
func agentCoreActor(scope Scope) (string, error) {
	if err := scope.Validate(); err != nil {
		return "", err
	}
	agent := scope.AgentID
	switch agent {
	case "":
		agent = primaryAgent
	case primaryAgent:
		return "", fmt.Errorf("adkmemory: an agent cannot be named %q: it is the primary agent's place in AgentCore Memory", primaryAgent)
	}
	for _, part := range []struct{ name, value string }{
		{"organization", scope.OrganizationID},
		{"member", scope.UserID},
		{"agent", agent},
	} {
		if err := checkSegment(part.name, part.value); err != nil {
			return "", err
		}
	}
	actor := scope.OrganizationID + "/" + scope.UserID + "/" + agent
	if len(actor) > maxActorIDLength {
		return "", fmt.Errorf("adkmemory: an AgentCore actor id is at most %d characters, and this scope's is %d", maxActorIDLength, len(actor))
	}
	if first := actor[0]; first == '_' || first == '-' {
		return "", fmt.Errorf("adkmemory: an AgentCore actor id starts with a letter or digit, and organization %q does not", scope.OrganizationID)
	}
	return actor, nil
}

// agentCoreNamespace is the namespace an actor's long-term records are
// extracted into: agentCoreNamespaceTemplate with the actor filled in.
func agentCoreNamespace(actor string) string {
	return strings.Replace(agentCoreNamespaceTemplate, "{actorId}", actor, 1)
}

// agentCoreForgetting is what Forget removes: the actor-id prefix of the
// short-term events and the namespace prefix of the long-term records, for
// an organization or, when a member is named, only that member's agents.
// Both end in "/", so organization "acme" is never a prefix of "acme2".
func agentCoreForgetting(organizationID, userID string) (actorPrefix, namespacePath string, err error) {
	if strings.TrimSpace(organizationID) == "" {
		return "", "", errors.New("adkmemory: forgetting needs an organization")
	}
	if err := checkSegment("organization", organizationID); err != nil {
		return "", "", err
	}
	actorPrefix = organizationID + "/"
	if userID != "" {
		if err := checkSegment("member", userID); err != nil {
			return "", "", err
		}
		actorPrefix += userID + "/"
	}
	return actorPrefix, agentCoreNamespace(strings.TrimSuffix(actorPrefix, "/")), nil
}

// sessionIDPattern is the session ids AgentCore takes.
var sessionIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,99}$`)

// agentCoreSession is the session id a thread's events are kept under: the
// thread id itself when AgentCore takes it, and otherwise a stable digest of
// it, so the same thread is always the same session.
func agentCoreSession(threadID string) string {
	if sessionIDPattern.MatchString(threadID) {
		return threadID
	}
	sum := sha256.Sum256([]byte(threadID))
	return "t-" + hex.EncodeToString(sum[:16])
}

// clientToken names one event of a write so that writing it again — a retry
// of the same run — is ignored rather than recorded twice. It is the same for
// the same actor, session, first turn and part, and 64 characters, inside
// AgentCore's 33 to 256.
func clientToken(actor, sessionID string, first time.Time, part int) string {
	sum := sha256.Sum256([]byte(actor + "\x00" + sessionID + "\x00" + first.UTC().Format(time.RFC3339Nano) + "\x00" + strconv.Itoa(part)))
	return hex.EncodeToString(sum[:])
}

// truncateMessage keeps at most maxMessageBytes of text, cut at a character.
func truncateMessage(text string) string {
	if len(text) <= maxMessageBytes {
		return text
	}
	cut := maxMessageBytes
	for cut > 0 && !utf8.RuneStart(text[cut]) {
		cut--
	}
	return text[:cut]
}

// agentCoreRole is AgentCore's role for a turn.
func agentCoreRole(role string) types.Role {
	if role == genai.RoleUser {
		return types.RoleUser
	}
	return types.RoleAssistant
}

// Write implements [Backend]. It records the run's spoken turns as one event
// in the thread's session — several, when there are more turns than one event
// carries — and AgentCore extracts the lasting facts from them later.
func (store *AgentCoreMemory) Write(ctx context.Context, scope Scope, conversation session.Session) error {
	actor, err := agentCoreActor(scope)
	if err != nil {
		return err
	}
	said := spokenTurns(conversation)
	if !memberSpoke(said) {
		// Nothing the member said is nothing of theirs to remember.
		return nil
	}
	sessionID := agentCoreSession(conversation.ID())
	first := said[0].at
	if first.IsZero() {
		first = store.now()
	}
	for part := 0; part*maxEventPayloads < len(said); part++ {
		chunk := said[part*maxEventPayloads : min((part+1)*maxEventPayloads, len(said))]
		payload := make([]types.PayloadType, 0, len(chunk))
		for _, spoke := range chunk {
			payload = append(payload, &types.PayloadTypeMemberConversational{Value: types.Conversational{
				Role:    agentCoreRole(spoke.role),
				Content: &types.ContentMemberText{Value: truncateMessage(strings.Join(spoke.texts, "\n"))},
			}})
		}
		// Each part is stamped with its first turn's time, so the parts of a
		// long run stay in order; a turn with none takes the run's.
		stamp := chunk[0].at
		if stamp.IsZero() {
			stamp = first
		}
		if _, err := store.api.CreateEvent(ctx, &bedrockagentcore.CreateEventInput{
			MemoryId:       aws.String(store.memoryID),
			ActorId:        aws.String(actor),
			SessionId:      aws.String(sessionID),
			EventTimestamp: aws.Time(stamp),
			Payload:        payload,
			ClientToken:    aws.String(clientToken(actor, sessionID, first, part)),
		}); err != nil {
			return fmt.Errorf("create memory event: %w", err)
		}
	}
	return nil
}

// Search implements [Backend]. It reads the actor's own namespace exactly,
// never a prefix, so it cannot answer from another scope.
func (store *AgentCoreMemory) Search(ctx context.Context, scope Scope, query string) ([]memory.Entry, error) {
	actor, err := agentCoreActor(scope)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(query) == "" {
		return nil, nil
	}
	found, err := store.api.RetrieveMemoryRecords(ctx, &bedrockagentcore.RetrieveMemoryRecordsInput{
		MemoryId:  aws.String(store.memoryID),
		Namespace: aws.String(agentCoreNamespace(actor)),
		SearchCriteria: &types.SearchCriteria{
			SearchQuery: aws.String(query),
			TopK:        aws.Int32(store.recalled),
		},
		MaxResults: aws.Int32(store.recalled),
	})
	if err != nil {
		return nil, fmt.Errorf("retrieve memory records: %w", err)
	}
	entries := make([]memory.Entry, 0, len(found.MemoryRecordSummaries))
	for _, record := range found.MemoryRecordSummaries {
		text, ok := record.Content.(*types.MemoryContentMemberText)
		if !ok || text.Value == "" {
			continue
		}
		entry := memory.Entry{
			ID:      aws.ToString(record.MemoryRecordId),
			Content: genai.NewContentFromText(text.Value, genai.RoleUser),
		}
		switch {
		case record.Timestamp != nil:
			entry.Timestamp = *record.Timestamp
		case record.CreatedAt != nil:
			entry.Timestamp = *record.CreatedAt
		}
		entries = append(entries, entry)
	}
	return entries, nil
}

// Forget implements [Backend]. It removes the long-term records under the
// organization's (or member's) namespace prefix, then the short-term events
// of the matching actors, which hold the conversations themselves until they
// expire.
//
// Both are collected before anything is deleted, so deleting cannot move a
// page under a listing. Everything runs under one deadline; what is left when
// it passes is said in the error, and the next Forget finds only that.
//
// An id the layout refuses is forgotten at once: Write refuses it too, so
// nothing was ever kept under it, and an error would only have the cleanup
// that asked redeliver it forever.
func (store *AgentCoreMemory) Forget(ctx context.Context, organizationID, userID string) error {
	actorPrefix, namespacePath, err := agentCoreForgetting(organizationID, userID)
	if errors.Is(err, errUnheld) {
		store.logger.InfoContext(ctx, "nothing to forget in AgentCore Memory: no memory can be kept under this id",
			"organization_id", organizationID, "reason", err)
		return nil
	}
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, cmp.Or(store.forgetTimeout, defaultForgetTimeout))
	defer cancel()
	records := store.forgetRecords(ctx, namespacePath)
	if ctx.Err() != nil {
		return records
	}
	return errors.Join(records, store.forgetEvents(ctx, actorPrefix))
}

// forgetRecords deletes every long-term record under namespacePath.
func (store *AgentCoreMemory) forgetRecords(ctx context.Context, namespacePath string) error {
	var forgetting []types.MemoryRecordDeleteInput
	var listing error
	var token *string
	for {
		page, err := store.api.ListMemoryRecords(ctx, &bedrockagentcore.ListMemoryRecordsInput{
			MemoryId:      aws.String(store.memoryID),
			NamespacePath: aws.String(namespacePath),
			NextToken:     token,
		})
		if err != nil {
			listing = fmt.Errorf("list memory records to forget: %w", err)
			break
		}
		for _, record := range page.MemoryRecordSummaries {
			// AgentCore already matched the prefix; matching it again here
			// means a listing that answered more widely than asked still
			// deletes nothing outside it.
			namespace, ok := underNamespace(record.Namespaces, namespacePath)
			if !ok || record.MemoryRecordId == nil {
				continue
			}
			forgetting = append(forgetting, types.MemoryRecordDeleteInput{
				MemoryRecordId: record.MemoryRecordId,
				Namespace:      aws.String(namespace),
			})
		}
		token = page.NextToken
		if aws.ToString(token) == "" {
			break
		}
	}
	// What a broken-off listing found is still deleted.
	return errors.Join(listing, store.deleteRecords(ctx, forgetting))
}

// underNamespace returns the first of namespaces under path.
func underNamespace(namespaces []string, path string) (string, bool) {
	for _, namespace := range namespaces {
		if strings.HasPrefix(namespace, path) {
			return namespace, true
		}
	}
	return "", false
}

// deleteRecords deletes records maxDeleteBatch at a time, and those a batch
// reports failed once more. A record already gone is forgotten.
func (store *AgentCoreMemory) deleteRecords(ctx context.Context, records []types.MemoryRecordDeleteInput) error {
	if len(records) == 0 {
		return nil
	}
	failed, err := store.deleteBatches(ctx, records)
	if err == nil && len(failed) > 0 {
		failed, err = store.deleteBatches(ctx, failed)
	}
	if err == nil && len(failed) > 0 {
		err = fmt.Errorf("%d could not be deleted", len(failed))
	}
	if err != nil {
		return fmt.Errorf("forgot %d of %d memory records: %w", len(records)-len(failed), len(records), err)
	}
	return nil
}

// deleteBatches sends records in batches and returns the ones not deleted:
// those a batch reported failed and, when a batch could not be sent or the
// time ran out, every one from there on, with why.
func (store *AgentCoreMemory) deleteBatches(ctx context.Context, records []types.MemoryRecordDeleteInput) ([]types.MemoryRecordDeleteInput, error) {
	var failed []types.MemoryRecordDeleteInput
	for start := 0; start < len(records); start += maxDeleteBatch {
		if ctx.Err() != nil {
			return append(failed, records[start:]...), context.Cause(ctx)
		}
		batch := records[start:min(start+maxDeleteBatch, len(records))]
		answer, err := store.api.BatchDeleteMemoryRecords(ctx, &bedrockagentcore.BatchDeleteMemoryRecordsInput{
			MemoryId: aws.String(store.memoryID),
			Records:  batch,
		})
		if err != nil {
			return append(failed, records[start:]...), err
		}
		byID := make(map[string]types.MemoryRecordDeleteInput, len(batch))
		for _, record := range batch {
			byID[aws.ToString(record.MemoryRecordId)] = record
		}
		for _, outcome := range answer.FailedRecords {
			if aws.ToInt32(outcome.ErrorCode) == 404 {
				// Gone already, because another Forget overlapped this one.
				continue
			}
			if record, ok := byID[aws.ToString(outcome.MemoryRecordId)]; ok {
				failed = append(failed, record)
			}
		}
	}
	return failed, nil
}

// agentCoreEvent names one short-term event.
type agentCoreEvent struct {
	actor, session, id string
}

// forgetEvents deletes every short-term event of the actors under
// actorPrefix, paced under DeleteEvent's quota.
func (store *AgentCoreMemory) forgetEvents(ctx context.Context, actorPrefix string) error {
	events, listing := store.listEvents(ctx, actorPrefix)
	forgot := 0
	var failure error
	for _, event := range events {
		if err := store.deletingEvents.Wait(ctx); err != nil {
			// Wait refuses at once when the deadline would pass before the
			// next delete may go.
			failure = cmp.Or(context.Cause(ctx), err)
			break
		}
		_, err := store.api.DeleteEvent(ctx, &bedrockagentcore.DeleteEventInput{
			MemoryId:  aws.String(store.memoryID),
			ActorId:   aws.String(event.actor),
			SessionId: aws.String(event.session),
			EventId:   aws.String(event.id),
		})
		var gone *types.ResourceNotFoundException
		switch {
		case err == nil, errors.As(err, &gone):
			// Gone already is forgotten all the same.
			forgot++
		case ctx.Err() != nil:
		case failure == nil:
			failure = fmt.Errorf("forget memory event %s: %w", event.id, err)
		}
		if ctx.Err() != nil {
			failure = context.Cause(ctx)
			break
		}
	}
	if forgot == len(events) {
		return listing
	}
	return errors.Join(listing, fmt.Errorf("forgot %d of %d memory events: %w", forgot, len(events), failure))
}

// listEvents collects the events of every actor under actorPrefix. A listing
// that breaks off returns what it found with the error.
func (store *AgentCoreMemory) listEvents(ctx context.Context, actorPrefix string) ([]agentCoreEvent, error) {
	actors, err := store.listActors(ctx, actorPrefix)
	var events []agentCoreEvent
	if err != nil {
		return nil, err
	}
	for _, actor := range actors {
		sessions, err := store.listSessions(ctx, actor)
		if err != nil {
			return events, err
		}
		for _, sessionID := range sessions {
			var token *string
			for {
				page, err := store.api.ListEvents(ctx, &bedrockagentcore.ListEventsInput{
					MemoryId:        aws.String(store.memoryID),
					ActorId:         aws.String(actor),
					SessionId:       aws.String(sessionID),
					IncludePayloads: aws.Bool(false),
					NextToken:       token,
				})
				if err != nil {
					return events, fmt.Errorf("list memory events to forget: %w", err)
				}
				for _, event := range page.Events {
					if event.EventId != nil {
						events = append(events, agentCoreEvent{actor: actor, session: sessionID, id: *event.EventId})
					}
				}
				token = page.NextToken
				if aws.ToString(token) == "" {
					break
				}
			}
		}
	}
	return events, nil
}

// listActors returns the memory's actors whose id starts with prefix.
// ListActors takes no filter, so every actor is read and matched here.
func (store *AgentCoreMemory) listActors(ctx context.Context, prefix string) ([]string, error) {
	var actors []string
	var token *string
	for {
		page, err := store.api.ListActors(ctx, &bedrockagentcore.ListActorsInput{
			MemoryId:  aws.String(store.memoryID),
			NextToken: token,
		})
		if err != nil {
			return nil, fmt.Errorf("list memory actors to forget: %w", err)
		}
		for _, actor := range page.ActorSummaries {
			if id := aws.ToString(actor.ActorId); strings.HasPrefix(id, prefix) {
				actors = append(actors, id)
			}
		}
		token = page.NextToken
		if aws.ToString(token) == "" {
			return actors, nil
		}
	}
}

// listSessions returns actor's sessions.
func (store *AgentCoreMemory) listSessions(ctx context.Context, actor string) ([]string, error) {
	var sessions []string
	var token *string
	for {
		page, err := store.api.ListSessions(ctx, &bedrockagentcore.ListSessionsInput{
			MemoryId:  aws.String(store.memoryID),
			ActorId:   aws.String(actor),
			NextToken: token,
		})
		if err != nil {
			return nil, fmt.Errorf("list memory sessions to forget: %w", err)
		}
		for _, summary := range page.SessionSummaries {
			if id := aws.ToString(summary.SessionId); id != "" {
				sessions = append(sessions, id)
			}
		}
		token = page.NextToken
		if aws.ToString(token) == "" {
			return sessions, nil
		}
	}
}
