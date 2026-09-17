package application

import (
	"context"
	"errors"
	"sync"
	"time"

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
	store.sources[source.ID] = source
	if event != nil {
		store.events = append(store.events, *event)
	}
	return nil
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
	listed := []domain.Source{}
	for _, source := range store.sources {
		if !query.Admin && source.CreatedBy != query.CreatedBy {
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
	err        error
	calls      int
}

func (directory *fakeDirectory) Membership(context.Context, string, authclient.Credentials) (domain.OrgMembership, error) {
	directory.calls++
	if directory.err != nil {
		return domain.OrgMembership{}, directory.err
	}
	return directory.membership, nil
}

// fakeApps answers what apps would about one of a member's connections.
type fakeApps struct {
	connection domain.AppConnection
	err        error
	calls      []string
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
