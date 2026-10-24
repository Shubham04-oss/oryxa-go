package subagents

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"time"
)

// MemoryStore is Store in memory, behaving as PostgresStore does, for
// development and tests: what it holds goes with the process.
type MemoryStore struct {
	mu      sync.Mutex
	records map[string]Record
}

// NewMemoryStore returns an empty store.
func NewMemoryStore() *MemoryStore { return &MemoryStore{records: map[string]Record{}} }

func (s *MemoryStore) Create(_ context.Context, record Record) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, taken := s.records[record.ID]; taken {
		return errors.New("duplicate id")
	}
	s.records[record.ID] = clone(record)
	return nil
}

func clone(record Record) Record {
	record.Tools, record.Files = nonNil(record.Tools), nonNil(record.Files)
	return record
}

func (s *MemoryStore) Get(_ context.Context, thread Thread, id string) (Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	record, ok := s.records[id]
	if !ok || record.Thread != thread {
		return Record{}, ErrNotFound
	}
	return clone(record), nil
}

func (s *MemoryStore) List(_ context.Context, thread Thread) ([]Record, error) {
	return s.filter(func(record Record) bool { return record.Thread == thread }), nil
}

func (s *MemoryStore) ListMember(_ context.Context, organizationID, userID string) ([]Record, error) {
	return s.filter(func(record Record) bool {
		return record.Thread.OrganizationID == organizationID && (userID == "" || record.Thread.UserID == userID)
	}), nil
}

func (s *MemoryStore) filter(keep func(Record) bool) []Record {
	s.mu.Lock()
	defer s.mu.Unlock()
	var records []Record
	for _, record := range s.records {
		if keep(record) {
			records = append(records, clone(record))
		}
	}
	slices.SortFunc(records, func(a, b Record) int {
		if c := a.CreatedAt.Compare(b.CreatedAt); c != 0 {
			return c
		}
		return strings.Compare(a.ID, b.ID)
	})
	return records
}

func (s *MemoryStore) Update(_ context.Context, record Record, from ...string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	stored, ok := s.records[record.ID]
	if !ok || !slices.Contains(from, stored.State) {
		return ErrStateChanged
	}
	updated := stored
	updated.ParentRunID, updated.State, updated.Question, updated.Progress = record.ParentRunID, record.State, record.Question, record.Progress
	updated.Result, updated.FullResult, updated.ResultArtifact, updated.Error = record.Result, record.FullResult, record.ResultArtifact, record.Error
	updated.ModelCalls, updated.InputTokens, updated.OutputTokens = record.ModelCalls, record.InputTokens, record.OutputTokens
	updated.CachedTokens, updated.TotalTokens = record.CachedTokens, record.TotalTokens
	updated.UpdatedAt, updated.ExpiresAt = record.UpdatedAt, record.ExpiresAt
	s.records[record.ID] = updated
	return nil
}

func (s *MemoryStore) SetProgress(_ context.Context, id, progress string, modelCalls int, expiresAt time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	stored, ok := s.records[id]
	if !ok || stored.State != StateRunning {
		return nil
	}
	stored.Progress, stored.ModelCalls, stored.ExpiresAt = progress, modelCalls, expiresAt
	s.records[id] = stored
	return nil
}

func (s *MemoryStore) Delete(_ context.Context, ids ...string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, id := range ids {
		delete(s.records, id)
	}
	return nil
}

var (
	_ Store = (*MemoryStore)(nil)
	_ Store = (*PostgresStore)(nil)
)
