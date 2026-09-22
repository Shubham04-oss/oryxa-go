package agentteamrooms

import (
	"context"
	"crypto/rand"
	"embed"
	"encoding/base64"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/agentteams"
	"github.com/Shubham04-oss/oryxa-go/internal/postgreskit"
)

//go:embed migrations/*.sql
var migrations embed.FS

const migrationsAdvisoryLock int64 = 7263510717

type Store struct{ database *postgreskit.Database }

func NewStore(database *postgreskit.Database) *Store { return &Store{database: database} }

func (store *Store) Migrate(ctx context.Context) error {
	return store.database.Migrate(ctx, "agent-team-rooms", migrations, "migrations/*.sql", migrationsAdvisoryLock)
}

const messageColumns = `id, team_id, seq, kind, author_name, author_agent_id, body, in_reply_to_seq, created_at`
const qualifiedMessageColumns = `message.id, message.team_id, message.seq, message.kind, message.author_name, message.author_agent_id, message.body, message.in_reply_to_seq, message.created_at`

func (store *Store) Append(ctx context.Context, scope agentteams.Scope, message Message) (Message, error) {
	bytes := make([]byte, 16)
	if _, err := rand.Read(bytes); err != nil {
		return Message{}, err
	}
	message.ID = "atmsg_" + base64.RawURLEncoding.EncodeToString(bytes)
	message.CreatedAt = time.Now().UTC()
	err := pgx.BeginFunc(ctx, store.database.Pool, func(tx pgx.Tx) error {
		// Lock the team row before allocating a sequence, so simultaneous posts
		// cannot observe the same MAX(seq). This also checks live ownership.
		var id string
		err := tx.QueryRow(ctx, `SELECT id FROM agents_primary.member_agent_teams
			WHERE id=$1 AND organization_id=$2 AND owner_user_id=$3 FOR UPDATE`,
			message.TeamID, scope.OrganizationID, scope.UserID).Scan(&id)
		if errors.Is(err, pgx.ErrNoRows) {
			return agentteams.ErrNotFound
		}
		if err != nil {
			return err
		}
		return scanMessage(tx.QueryRow(ctx, `INSERT INTO agents_primary.member_agent_team_messages
			(id, team_id, seq, kind, author_name, author_agent_id, body, in_reply_to_seq, created_at)
			VALUES ($1,$2,COALESCE((SELECT MAX(seq)+1 FROM agents_primary.member_agent_team_messages WHERE team_id=$2),1),$3,$4,$5,$6,$7,$8)
			RETURNING `+messageColumns,
			message.ID, message.TeamID, message.Kind, message.AuthorName,
			message.AuthorAgentID, message.Body, message.InReplyToSeq, message.CreatedAt), &message)
	})
	if err != nil {
		return Message{}, fmt.Errorf("append agent team message: %w", err)
	}
	return message, nil
}

func (store *Store) List(ctx context.Context, scope agentteams.Scope, teamID string, after int64, limit int) ([]Message, error) {
	if after == 0 {
		// A first open needs the latest conversation, not the first page a
		// long-lived team ever wrote. The outer query restores reading order.
		return store.query(ctx, `SELECT `+messageColumns+` FROM (
			SELECT `+qualifiedMessageColumns+` FROM agents_primary.member_agent_team_messages message
			JOIN agents_primary.member_agent_teams team ON team.id=message.team_id
			WHERE team.id=$1 AND team.organization_id=$2 AND team.owner_user_id=$3
			ORDER BY message.seq DESC LIMIT $4
		) recent ORDER BY seq ASC`, teamID, scope.OrganizationID, scope.UserID, limit)
	}
	return store.query(ctx, `SELECT `+qualifiedMessageColumns+` FROM agents_primary.member_agent_team_messages message
		JOIN agents_primary.member_agent_teams team ON team.id=message.team_id
		WHERE team.id=$1 AND team.organization_id=$2 AND team.owner_user_id=$3 AND message.seq>$4
		ORDER BY message.seq ASC LIMIT $5`, teamID, scope.OrganizationID, scope.UserID, after, limit)
}

func (store *Store) Recent(ctx context.Context, scope agentteams.Scope, teamID string, limit int) ([]Message, error) {
	return store.query(ctx, `SELECT `+messageColumns+` FROM (
		SELECT `+qualifiedMessageColumns+` FROM agents_primary.member_agent_team_messages message
		JOIN agents_primary.member_agent_teams team ON team.id=message.team_id
		WHERE team.id=$1 AND team.organization_id=$2 AND team.owner_user_id=$3
		ORDER BY message.seq DESC LIMIT $4
	) recent ORDER BY seq ASC`, teamID, scope.OrganizationID, scope.UserID, limit)
}

func (store *Store) query(ctx context.Context, sql string, args ...any) ([]Message, error) {
	rows, err := store.database.Pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []Message{}
	for rows.Next() {
		var message Message
		if err := scanMessage(rows, &message); err != nil {
			return nil, err
		}
		result = append(result, message)
	}
	return result, rows.Err()
}

type scanner interface{ Scan(...any) error }

func scanMessage(row scanner, message *Message) error {
	return row.Scan(&message.ID, &message.TeamID, &message.Seq, &message.Kind,
		&message.AuthorName, &message.AuthorAgentID, &message.Body,
		&message.InReplyToSeq, &message.CreatedAt)
}

func (store *Store) Thread(ctx context.Context, scope agentteams.Scope, teamID, agentID string) (string, error) {
	var threadID string
	err := store.database.Pool.QueryRow(ctx, `SELECT thread.thread_id FROM agents_primary.member_agent_team_threads thread
		JOIN agents_primary.member_agent_teams team ON team.id=thread.team_id
		WHERE team.id=$1 AND team.organization_id=$2 AND team.owner_user_id=$3 AND thread.agent_id=$4`,
		teamID, scope.OrganizationID, scope.UserID, agentID).Scan(&threadID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	return threadID, err
}

func (store *Store) SetThread(ctx context.Context, scope agentteams.Scope, teamID, agentID, threadID string) error {
	tag, err := store.database.Pool.Exec(ctx, `INSERT INTO agents_primary.member_agent_team_threads (team_id, agent_id, thread_id)
		SELECT id, $4, $5 FROM agents_primary.member_agent_teams
		WHERE id=$1 AND organization_id=$2 AND owner_user_id=$3
		ON CONFLICT (team_id, agent_id) DO UPDATE SET thread_id=EXCLUDED.thread_id, updated_at=now()`,
		teamID, scope.OrganizationID, scope.UserID, agentID, threadID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return agentteams.ErrNotFound
	}
	return nil
}
