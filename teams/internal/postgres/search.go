package postgres

import (
	"context"
	"fmt"

	"github.com/jmoiron/sqlx"
	"github.com/pgvector/pgvector-go"

	"github.com/Shubham04-oss/oryxa-go/teams/internal/domain"
)

const defaultSearchLimit = domain.DefaultSearchLimit

// SearchTeams finds teams in the organization whose names are close to query,
// using pg_trgm similarity. A non-nil teamIDs list restricts the match set;
// an empty list matches nothing.
func (store *Store) SearchTeams(ctx context.Context, organizationID, query string, limit int, teamIDs []string) ([]domain.Team, error) {
	query, limit, err := searchArgs(query, limit)
	if err != nil {
		return nil, err
	}
	if teamIDs != nil && len(teamIDs) == 0 {
		return []domain.Team{}, nil
	}
	if teamIDs == nil {
		return listTeams(ctx, store.db, `
			SELECT `+teamColumns+`
			FROM teams
			WHERE organization_id = $1
			  AND (name % $2 OR name ILIKE '%' || $2 || '%')
			ORDER BY similarity(name, $2) DESC, lower(name), id
			LIMIT $3`, organizationID, query, limit)
	}
	return listTeams(ctx, store.db, `
		SELECT `+teamColumns+`
		FROM teams
		WHERE organization_id = $1
		  AND (name % $2 OR name ILIKE '%' || $2 || '%')
		  AND id = ANY($4)
		ORDER BY similarity(name, $2) DESC, lower(name), id
		LIMIT $3`, organizationID, query, limit, teamIDs)
}

// MembersUnder returns every membership hanging under path, including
// people on nested teams (`membership.path <@ path`).
func (store *Store) MembersUnder(ctx context.Context, organizationID, path string) ([]domain.Membership, error) {
	if path == "" {
		return nil, fmt.Errorf("path is required: %w", domain.ErrInvalid)
	}
	members := make([]domain.Membership, 0)
	if err := store.db.SelectContext(ctx, &members, `
		SELECT `+membershipColumns+` FROM teams_memberships
		WHERE organization_id = $1 AND path <@ $2::ltree
		ORDER BY path, user_id`, organizationID, path); err != nil {
		return nil, fmt.Errorf("list members under path: %w", err)
	}
	for i := range members {
		members[i].JoinedAt = members[i].JoinedAt.UTC()
	}
	return members, nil
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
// to values, nearest first. Teams without an embedding are skipped. The
// query runs in a transaction so iterative HNSW can keep scanning until the
// organization (and optional visibility) filters have enough hits.
func (store *Store) TeamsNearest(ctx context.Context, organizationID string, values []float32, limit int, teamIDs []string) ([]domain.Team, error) {
	if len(values) != domain.EmbeddingDimensions {
		return nil, fmt.Errorf("embedding must have %d dimensions: %w", domain.EmbeddingDimensions, domain.ErrInvalid)
	}
	limit = domain.ClampSearchLimit(limit)
	if teamIDs != nil && len(teamIDs) == 0 {
		return []domain.Team{}, nil
	}
	query := `
		SELECT ` + teamColumns + `
		FROM teams
		WHERE organization_id = $1 AND embedding IS NOT NULL
		ORDER BY embedding <=> $2, id
		LIMIT $3`
	args := []any{organizationID, pgvector.NewVector(values), limit}
	if teamIDs != nil {
		query = `
			SELECT ` + teamColumns + `
			FROM teams
			WHERE organization_id = $1 AND embedding IS NOT NULL AND id = ANY($4)
			ORDER BY embedding <=> $2, id
			LIMIT $3`
		args = append(args, teamIDs)
	}
	var teams []domain.Team
	err := store.inTx(ctx, func(tx *sqlx.Tx) error {
		if _, err := tx.ExecContext(ctx, `SELECT set_config('hnsw.iterative_scan', 'relaxed_order', true)`); err != nil {
			return fmt.Errorf("enable iterative hnsw scan: %w", err)
		}
		var err error
		teams, err = listTeams(ctx, tx, query, args...)
		return err
	})
	if err != nil {
		return nil, err
	}
	return teams, nil
}

func searchArgs(query string, limit int) (string, int, error) {
	if query == "" {
		return "", 0, fmt.Errorf("search query is required: %w", domain.ErrInvalid)
	}
	return query, domain.ClampSearchLimit(limit), nil
}
