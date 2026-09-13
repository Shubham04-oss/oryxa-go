package postgres

import (
	"context"
	"fmt"

	"github.com/pgvector/pgvector-go"

	"github.com/Shubham04-oss/oryxa-go/teams/internal/domain"
)

const defaultSearchLimit = 20

// SearchTeams finds teams in the organization whose names are close to query,
// using pg_trgm similarity.
func (store *Store) SearchTeams(ctx context.Context, organizationID, query string, limit int) ([]domain.Team, error) {
	query, limit, err := searchArgs(query, limit)
	if err != nil {
		return nil, err
	}
	return listTeams(ctx, store.db, `
		SELECT `+teamColumns+`
		FROM teams
		WHERE organization_id = $1
		  AND (name % $2 OR name ILIKE '%' || $2 || '%')
		ORDER BY similarity(name, $2) DESC, lower(name), id
		LIMIT $3`, organizationID, query, limit)
}

// TeamDescendants returns the team at path and every team under it in the
// organization, using ltree.
func (store *Store) TeamDescendants(ctx context.Context, organizationID, path string) ([]domain.Team, error) {
	if path == "" {
		return nil, fmt.Errorf("path is required: %w", domain.ErrInvalid)
	}
	return listTeams(ctx, store.db, `
		SELECT `+teamColumns+`
		FROM teams
		WHERE organization_id = $1 AND path <@ $2::ltree
		ORDER BY path, id`, organizationID, path)
}

// SetTeamEmbedding stores a pgvector embedding for later nearest-neighbour
// lookup. values must be EmbeddingDimensions long.
func (store *Store) SetTeamEmbedding(ctx context.Context, organizationID, teamID string, values []float32) error {
	if len(values) != domain.EmbeddingDimensions {
		return fmt.Errorf("embedding must have %d dimensions: %w", domain.EmbeddingDimensions, domain.ErrInvalid)
	}
	result, err := store.db.ExecContext(ctx, `
		UPDATE teams SET embedding = $3, updated_at = now()
		WHERE id = $1 AND organization_id = $2`, teamID, organizationID, pgvector.NewVector(values))
	if err != nil {
		return fmt.Errorf("set team embedding: %w", err)
	}
	if rows, _ := result.RowsAffected(); rows == 0 {
		return domain.ErrNotFound
	}
	return nil
}

// TeamsNearest returns the organization's teams whose embeddings are closest
// to values, nearest first. Teams without an embedding are skipped.
func (store *Store) TeamsNearest(ctx context.Context, organizationID string, values []float32, limit int) ([]domain.Team, error) {
	if len(values) != domain.EmbeddingDimensions {
		return nil, fmt.Errorf("embedding must have %d dimensions: %w", domain.EmbeddingDimensions, domain.ErrInvalid)
	}
	if limit <= 0 {
		limit = defaultSearchLimit
	}
	return listTeams(ctx, store.db, `
		SELECT `+teamColumns+`
		FROM teams
		WHERE organization_id = $1 AND embedding IS NOT NULL
		ORDER BY embedding <=> $2
		LIMIT $3`, organizationID, pgvector.NewVector(values), limit)
}

func searchArgs(query string, limit int) (string, int, error) {
	if query == "" {
		return "", 0, fmt.Errorf("search query is required: %w", domain.ErrInvalid)
	}
	if limit <= 0 {
		limit = defaultSearchLimit
	}
	return query, limit, nil
}
