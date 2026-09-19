package application

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	knowledgev1 "github.com/Shubham04-oss/oryxa-go/contracts/events/knowledge/v1"

	"github.com/Shubham04-oss/oryxa-go/knowledge/internal/authclient"
	"github.com/Shubham04-oss/oryxa-go/knowledge/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/knowledge/internal/sources"
)

var testClock = time.Date(2026, time.September, 17, 12, 0, 0, 0, time.UTC)

// fakeStore keeps what a real store would, in memory, and records the calls a
// test asserts on.
type fakeStore struct {
	mu sync.Mutex

	sources   map[string]domain.Source
	documents map[string]domain.Document
	cursors   map[string]domain.Cursor
	runs      []domain.SyncRun
	events    []domain.Event
	handled   map[string]bool

	// lease is what the store believes currently holds each source, so a test
	// can take one away mid-run.
	lease map[string]string

	retractUnseen int
	paused        []string
	finishedNext  time.Time
	failedReason  string
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		sources:   map[string]domain.Source{},
		documents: map[string]domain.Document{},
		cursors:   map[string]domain.Cursor{},
		handled:   map[string]bool{},
		lease:     map[string]string{},
	}
}

func (store *fakeStore) CreateSource(_ context.Context, source domain.Source, event *domain.Event) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	// The same thing is indexed once, which the schema enforces with a
	// unique index over the live sources. Modelled here so a test cannot
	// pass against a fake that permits what PostgreSQL refuses.
	for _, existing := range store.sources {
		if existing.Status == domain.StatusRemoved {
			continue
		}
		if existing.OrganizationID == source.OrganizationID && existing.Provider == source.Provider &&
			existing.ConnectionID == source.ConnectionID && sameTarget(existing.Target, source.Target) {
			return domain.ErrConflict
		}
	}
	store.sources[source.ID] = source
	if event != nil {
		store.events = append(store.events, *event)
	}
	return nil
}

// HastenSource moves one source, whatever its last sweep: a person asking
// has a reason the schedule does not know.
func (store *fakeStore) HastenSource(_ context.Context, organizationID, sourceID string, at time.Time) (bool, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	source, ok := store.sources[sourceID]
	if !ok || source.OrganizationID != organizationID || source.Status != domain.StatusActive {
		return false, nil
	}
	if source.SyncAt == nil || !source.SyncAt.After(at) {
		return false, nil
	}
	hastened := at
	source.SyncAt = &hastened
	store.sources[sourceID] = source
	return true, nil
}

// HastenSources models the real one: only live sources whose next sweep is
// later than now, and only outside the floor since their last.
func (store *fakeStore) HastenSources(_ context.Context, connectionID string, at time.Time, floor time.Duration) (int64, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	var moved int64
	for id, source := range store.sources {
		if source.ConnectionID != connectionID || source.Status != domain.StatusActive || source.Attempts > 0 {
			continue
		}
		hastened := at
		if source.LastSyncedAt != nil && source.LastSyncedAt.Add(floor).After(hastened) {
			hastened = source.LastSyncedAt.Add(floor)
		}
		if source.SyncAt == nil || !source.SyncAt.After(hastened) {
			continue
		}
		source.SyncAt = &hastened
		store.sources[id] = source
		moved++
	}
	return moved, nil
}

func sameTarget(left, right map[string]string) bool {
	if len(left) != len(right) {
		return false
	}
	for key, value := range left {
		if right[key] != value {
			return false
		}
	}
	return true
}

func (store *fakeStore) Source(_ context.Context, _, sourceID string) (domain.Source, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	source, ok := store.sources[sourceID]
	if !ok {
		return domain.Source{}, domain.ErrNotFound
	}
	return source, nil
}

func (store *fakeStore) Sources(_ context.Context, _ string, query domain.SourceQuery) ([]domain.Source, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	// It answers what the real query does, filters and all: a fake that
	// answered more once let a test pass that the database would have failed.
	listed := []domain.Source{}
	for _, source := range store.sources {
		switch {
		case source.Status == domain.StatusRemoved && !query.IncludeRemoved,
			!query.Admin && source.CreatedBy != query.CreatedBy,
			query.ConnectionID != "" && source.ConnectionID != query.ConnectionID,
			query.Provider != "" && source.Provider != query.Provider:
			continue
		}
		listed = append(listed, source)
	}
	return listed, nil
}

func (store *fakeStore) RemoveSource(_ context.Context, _, sourceID string, _ time.Time, events func(domain.Source, []domain.Document) ([]domain.Event, error)) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	source, ok := store.sources[sourceID]
	if !ok {
		return domain.ErrNotFound
	}
	source.Status = domain.StatusRemoved
	store.sources[sourceID] = source
	return store.retractAll(source, events)
}

func (store *fakeStore) RemoveSources(_ context.Context, organizationID, createdBy string, _ time.Time, events func(domain.Source, []domain.Document) ([]domain.Event, error)) ([]domain.Source, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	removed := []domain.Source{}
	for id, source := range store.sources {
		if source.OrganizationID != organizationID || source.Status == domain.StatusRemoved {
			continue
		}
		if createdBy != "" && source.CreatedBy != createdBy {
			continue
		}
		source.Status = domain.StatusRemoved
		store.sources[id] = source
		removed = append(removed, source)
		if err := store.retractAll(source, events); err != nil {
			return nil, err
		}
	}
	return removed, nil
}

// retractAll is called with the lock already held.
func (store *fakeStore) retractAll(source domain.Source, events func(domain.Source, []domain.Document) ([]domain.Event, error)) error {
	if events == nil {
		return nil
	}
	held := []domain.Document{}
	for id, document := range store.documents {
		if document.SourceID != source.ID || document.RemovedAt != nil {
			continue
		}
		removed := testClock
		document.RemovedAt = &removed
		store.documents[id] = document
		held = append(held, document)
	}
	recorded, err := events(source, held)
	if err != nil {
		return err
	}
	store.events = append(store.events, recorded...)
	return nil
}

func (store *fakeStore) RemoveSourcesByConnection(_ context.Context, connectionID string, _ time.Time, events func(domain.Source, []domain.Document) ([]domain.Event, error)) ([]domain.Source, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	removed := []domain.Source{}
	for id, source := range store.sources {
		if source.ConnectionID != connectionID || source.Status == domain.StatusRemoved {
			continue
		}
		source.Status = domain.StatusRemoved
		store.sources[id] = source
		removed = append(removed, source)
		if err := store.retractAll(source, events); err != nil {
			return nil, err
		}
	}
	return removed, nil
}

func (store *fakeStore) RemoveSourcesByTeam(_ context.Context, organizationID, teamID string, _ time.Time, events func(domain.Source, []domain.Document) ([]domain.Event, error)) ([]domain.Source, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	removed := []domain.Source{}
	for id, source := range store.sources {
		if source.OrganizationID != organizationID || source.Scope != domain.ScopeTeam || source.TeamID != teamID || source.Status == domain.StatusRemoved {
			continue
		}
		source.Status = domain.StatusRemoved
		store.sources[id] = source
		removed = append(removed, source)
		if err := store.retractAll(source, events); err != nil {
			return nil, err
		}
	}
	return removed, nil
}

func (store *fakeStore) PauseSources(_ context.Context, connectionID, reason string, _ time.Time, event func(domain.Source) (domain.Event, error)) ([]domain.Source, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	paused := []domain.Source{}
	for id, source := range store.sources {
		if source.Status != domain.StatusActive || source.ConnectionID != connectionID {
			continue
		}
		source.Status = domain.StatusPaused
		source.Reason = reason
		source.SyncAt = nil
		store.sources[id] = source
		paused = append(paused, source)
		if event != nil {
			recorded, err := event(source)
			if err != nil {
				return nil, err
			}
			store.events = append(store.events, recorded)
		}
	}
	store.paused = append(store.paused, reason)
	return paused, nil
}

func (store *fakeStore) ClaimDueSources(_ context.Context, now time.Time, limit int, leaseToken string, _ time.Time) ([]domain.Source, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	claimed := []domain.Source{}
	for id, source := range store.sources {
		if len(claimed) >= limit || source.Status != domain.StatusActive {
			continue
		}
		if source.SyncAt == nil || source.SyncAt.After(now) {
			continue
		}
		store.lease[id] = leaseToken
		source.LeaseToken = leaseToken
		claimed = append(claimed, source)
	}
	return claimed, nil
}

func (store *fakeStore) RenewLease(_ context.Context, sourceID, leaseToken string, _ time.Time) (bool, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	return store.lease[sourceID] == leaseToken, nil
}

func (store *fakeStore) FinishSource(_ context.Context, sourceID, leaseToken string, _ time.Time, nextSync time.Time, fullSweptAt *time.Time, event *domain.Event) (bool, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.lease[sourceID] != leaseToken {
		return false, nil
	}
	store.finishedNext = nextSync
	if fullSweptAt != nil {
		source := store.sources[sourceID]
		source.FullSweepAt = fullSweptAt
		store.sources[sourceID] = source
	}
	if event != nil {
		store.events = append(store.events, *event)
	}
	return true, nil
}

func (store *fakeStore) FailSource(_ context.Context, sourceID, leaseToken, reason string, _ time.Time, _ time.Time, event *domain.Event) (bool, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.lease[sourceID] != leaseToken {
		return false, nil
	}
	store.failedReason = reason
	if event != nil {
		store.events = append(store.events, *event)
	}
	return true, nil
}

func (store *fakeStore) UpsertDocument(_ context.Context, document domain.Document, event func(domain.Document) (domain.Event, error)) (bool, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	key := document.SourceID + "\x00" + document.ExternalID
	existing, found := store.documents[key]
	changed := !found || existing.ContentHash != document.ContentHash || existing.RemovedAt != nil
	if found {
		document.ID = existing.ID
	}
	document.LastSeenAt = document.ExtractedAt
	document.RemovedAt = nil
	store.documents[key] = document
	if !changed || event == nil {
		return false, nil
	}
	recorded, err := event(document)
	if err != nil {
		return false, err
	}
	store.events = append(store.events, recorded)
	return true, nil
}

func (store *fakeStore) RetractUnseen(_ context.Context, sourceID string, before time.Time, _ time.Time, event func(domain.Document) (domain.Event, error)) (int, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.retractUnseen++
	retracted := 0
	for key, document := range store.documents {
		if document.SourceID != sourceID || document.RemovedAt != nil || !document.LastSeenAt.Before(before) {
			continue
		}
		removed := before
		document.RemovedAt = &removed
		store.documents[key] = document
		retracted++
		if event == nil {
			continue
		}
		recorded, err := event(document)
		if err != nil {
			return 0, err
		}
		store.events = append(store.events, recorded)
	}
	return retracted, nil
}

func (store *fakeStore) Document(_ context.Context, _, documentID string) (domain.Document, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	for _, document := range store.documents {
		if document.ID == documentID {
			return document, nil
		}
	}
	return domain.Document{}, domain.ErrNotFound
}

// DocumentByExternalID mirrors the real store: it answers a document
// whatever its removed_at, the same lookup UpsertDocument's own internal
// query makes.
func (store *fakeStore) TouchDocument(_ context.Context, sourceID, externalID string, seenAt time.Time) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	key := sourceID + "\x00" + externalID
	if document, ok := store.documents[key]; ok {
		document.LastSeenAt = seenAt
		store.documents[key] = document
	}
	return nil
}

func (store *fakeStore) ExcludeDocument(_ context.Context, document domain.Document, now time.Time, event func(domain.Document) (domain.Event, error)) (bool, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	key := document.SourceID + "\x00" + document.ExternalID
	existing, found := store.documents[key]
	live := found && existing.RemovedAt == nil
	removedAt := now
	if found {
		document.ID = existing.ID
		if existing.RemovedAt != nil {
			removedAt = *existing.RemovedAt
		}
	}
	document.Body = ""
	document.Original = nil
	document.LastSeenAt = now
	document.RemovedAt = &removedAt
	store.documents[key] = document
	if !live || event == nil {
		return false, nil
	}
	recorded, err := event(document)
	if err != nil {
		return false, err
	}
	store.events = append(store.events, recorded)
	return true, nil
}

// fakeJudge answers a verdict per title, keeping anything it was not told
// about, and records every candidate it was shown.
type fakeJudge struct {
	mu       sync.Mutex
	verdicts map[string]domain.Verdict
	err      error
	calls    []domain.Candidate
}

func (judge *fakeJudge) Judge(_ context.Context, candidate domain.Candidate) (domain.Verdict, error) {
	judge.mu.Lock()
	defer judge.mu.Unlock()
	judge.calls = append(judge.calls, candidate)
	if judge.err != nil {
		return domain.Verdict{}, judge.err
	}
	if verdict, ok := judge.verdicts[candidate.Title]; ok {
		return verdict, nil
	}
	return domain.Verdict{Keep: true, Reason: "worth keeping"}, nil
}

func (judge *fakeJudge) called() int {
	judge.mu.Lock()
	defer judge.mu.Unlock()
	return len(judge.calls)
}

func (store *fakeStore) DocumentByExternalID(_ context.Context, sourceID, externalID string) (domain.Document, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	document, ok := store.documents[sourceID+"\x00"+externalID]
	if !ok {
		return domain.Document{}, domain.ErrNotFound
	}
	return document, nil
}

// RetractDocument mirrors the real store: it retracts one live document named
// by its external id, and answers false rather than retracting anything a
// second time.
func (store *fakeStore) RetractDocument(_ context.Context, sourceID, externalID string, now time.Time, event func(domain.Document) (domain.Event, error)) (domain.Document, bool, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	key := sourceID + "\x00" + externalID
	document, ok := store.documents[key]
	if !ok || document.RemovedAt != nil {
		return domain.Document{}, false, nil
	}
	removed := now
	document.RemovedAt = &removed
	document.Body = ""
	store.documents[key] = document
	if event != nil {
		recorded, err := event(document)
		if err != nil {
			return domain.Document{}, false, err
		}
		store.events = append(store.events, recorded)
	}
	return document, true, nil
}

func (store *fakeStore) Cursor(_ context.Context, sourceID string) (domain.Cursor, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	cursor, ok := store.cursors[sourceID]
	if !ok {
		return domain.Cursor{SourceID: sourceID}, nil
	}
	return cursor, nil
}

func (store *fakeStore) SaveCursor(_ context.Context, cursor domain.Cursor) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.cursors[cursor.SourceID] = cursor
	return nil
}

func (store *fakeStore) ClearCursor(_ context.Context, sourceID string) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	delete(store.cursors, sourceID)
	return nil
}

func (store *fakeStore) StartRun(_ context.Context, run domain.SyncRun) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.runs = append(store.runs, run)
	return nil
}

func (store *fakeStore) FinishRun(_ context.Context, run domain.SyncRun, event *domain.Event) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	for index, existing := range store.runs {
		if existing.ID == run.ID {
			store.runs[index] = run
		}
	}
	if event != nil {
		store.events = append(store.events, *event)
	}
	return nil
}

func (store *fakeStore) Runs(_ context.Context, _, sourceID string, _ int) ([]domain.SyncRun, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	listed := []domain.SyncRun{}
	for _, run := range store.runs {
		if run.SourceID == sourceID {
			listed = append(listed, run)
		}
	}
	return listed, nil
}

func (store *fakeStore) EventHandled(_ context.Context, eventID string) (bool, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	return store.handled[eventID], nil
}

func (store *fakeStore) MarkEventHandled(_ context.Context, eventID, _ string, _ time.Time) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.handled[eventID] = true
	return nil
}

func (store *fakeStore) DeleteHandledEventsBefore(context.Context, time.Time) (int64, error) {
	return 0, nil
}

func (store *fakeStore) DeleteRunsBefore(context.Context, time.Time) (int64, error) { return 0, nil }

// lastRun is how the most recent pass ended.
func (store *fakeStore) lastRun() domain.SyncRun {
	store.mu.Lock()
	defer store.mu.Unlock()
	if len(store.runs) == 0 {
		return domain.SyncRun{}
	}
	return store.runs[len(store.runs)-1]
}

// published counts the events of one detail type the store was given.
// publishedReasons are the reasons the events of a type carried, so a test
// can pin which one a caller announced rather than only how many.
func (store *fakeStore) publishedReasons(detailType string) []string {
	store.mu.Lock()
	defer store.mu.Unlock()
	var reasons []string
	for _, event := range store.events {
		if event.DetailType != detailType {
			continue
		}
		var carried struct {
			Reason string `json:"reason"`
		}
		_ = json.Unmarshal(event.Data, &carried)
		reasons = append(reasons, carried.Reason)
	}
	return reasons
}

// publishedDocuments decodes every DocumentPublished the store was given, so a
// test can check what a consumer is actually told.
func (store *fakeStore) publishedDocuments(t *testing.T) []knowledgev1.DocumentPublished {
	t.Helper()
	store.mu.Lock()
	defer store.mu.Unlock()
	var documents []knowledgev1.DocumentPublished
	for _, event := range store.events {
		if event.DetailType != knowledgev1.DocumentPublishedDetailType {
			continue
		}
		var document knowledgev1.DocumentPublished
		if err := json.Unmarshal(event.Data, &document); err != nil {
			t.Fatalf("decode a published document: %v", err)
		}
		documents = append(documents, document)
	}
	return documents
}

func (store *fakeStore) published(detailType string) int {
	store.mu.Lock()
	defer store.mu.Unlock()
	count := 0
	for _, event := range store.events {
		if event.DetailType == detailType {
			count++
		}
	}
	return count
}

// fakeDirectory answers what auth would about the caller, and counts how
// often it was asked.
type fakeDirectory struct {
	membership domain.OrgMembership
	// agentToken is a token auth would answer as an agent's, the way it
	// answers every token a standing grant redeems to.
	agentToken string
	err        error
	calls      int
}

func (directory *fakeDirectory) Membership(_ context.Context, _ string, credentials authclient.Credentials) (domain.OrgMembership, error) {
	directory.calls++
	if directory.err != nil {
		return domain.OrgMembership{}, directory.err
	}
	membership := directory.membership
	if directory.agentToken != "" && credentials.Authorization == "Bearer "+directory.agentToken {
		membership.Agent = true
	}
	return membership, nil
}

// fakeApps answers what apps would about one of a member's connections.
type fakeApps struct {
	connection domain.AppConnection
	// targets is what a connection holds, and targetsErr what asking for it
	// answers instead.
	targets    []domain.AppTarget
	targetsErr error
	err        error
	calls      []string
}

func (apps *fakeApps) Targets(_ context.Context, _, connectionID string, _ authclient.Credentials) ([]domain.AppTarget, error) {
	apps.calls = append(apps.calls, "targets:"+connectionID)
	if apps.targetsErr != nil {
		return nil, apps.targetsErr
	}
	return apps.targets, nil
}

func (apps *fakeApps) Connection(_ context.Context, _, connectionID string, _ authclient.Credentials) (domain.AppConnection, error) {
	apps.calls = append(apps.calls, connectionID)
	if apps.err != nil {
		return domain.AppConnection{}, apps.err
	}
	if apps.connection.ID == "" || apps.connection.ID != connectionID {
		return domain.AppConnection{}, domain.ErrNotFound
	}
	return apps.connection, nil
}

// fakeGrants mints a member token, recording whom it was minted for.
type fakeGrants struct {
	token   string
	err     error
	members []string
}

func (grants *fakeGrants) MemberToken(_ context.Context, organizationID, memberID string) (string, error) {
	grants.members = append(grants.members, organizationID+"/"+memberID)
	if grants.err != nil {
		return "", grants.err
	}
	return grants.token, nil
}

// fakeOriginals is the bucket a document's own bytes would be kept in,
// recording what was stored and removed so a test can assert on it without
// touching S3.
type fakeOriginals struct {
	mu      sync.Mutex
	objects map[string][]byte
	types   map[string]string
	putErr  error

	puts    []string
	deletes []string
}

func newFakeOriginals() *fakeOriginals {
	return &fakeOriginals{objects: map[string][]byte{}, types: map[string]string{}}
}

func (originals *fakeOriginals) Put(_ context.Context, key string, content []byte, mime string) error {
	originals.mu.Lock()
	defer originals.mu.Unlock()
	originals.puts = append(originals.puts, key)
	if originals.putErr != nil {
		return originals.putErr
	}
	stored := make([]byte, len(content))
	copy(stored, content)
	originals.objects[key] = stored
	originals.types[key] = mime
	return nil
}

func (originals *fakeOriginals) Delete(_ context.Context, key string) error {
	originals.mu.Lock()
	defer originals.mu.Unlock()
	originals.deletes = append(originals.deletes, key)
	delete(originals.objects, key)
	return nil
}

func (originals *fakeOriginals) has(key string) bool {
	originals.mu.Lock()
	defer originals.mu.Unlock()
	_, ok := originals.objects[key]
	return ok
}

// fakeExtractor serves scripted pages, so a test can shape a walk exactly.
type fakeExtractor struct {
	provider  string
	pages     []sources.Page
	errs      []error
	calls     []sources.Request
	targetErr error
}

func (extractor *fakeExtractor) Provider() string { return extractor.provider }

func (extractor *fakeExtractor) Target(map[string]string) error { return extractor.targetErr }

func (extractor *fakeExtractor) Fetch(_ context.Context, request sources.Request) (sources.Page, error) {
	index := len(extractor.calls)
	extractor.calls = append(extractor.calls, request)
	if index < len(extractor.errs) && extractor.errs[index] != nil {
		return sources.Page{}, extractor.errs[index]
	}
	if index >= len(extractor.pages) {
		return sources.Page{}, errors.New("the test scripted no page for this call")
	}
	return extractor.pages[index], nil
}
