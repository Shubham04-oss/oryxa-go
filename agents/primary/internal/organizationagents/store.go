package organizationagents

import (
	"context"
	"crypto/rand"
	"embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/events"
	agentsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/agents/v1"
	"github.com/Shubham04-oss/oryxa-go/internal/observability"
	"github.com/Shubham04-oss/oryxa-go/internal/postgreskit"
)

//go:embed migrations/*.sql
var migrations embed.FS

const migrationsAdvisoryLock int64 = 7263510714

type Store struct{ database *postgreskit.Database }

func NewStore(database *postgreskit.Database) *Store { return &Store{database: database} }

func (store *Store) Migrate(ctx context.Context) error {
	return store.database.Migrate(ctx, "organization-agents", migrations, "migrations/*.sql", migrationsAdvisoryLock)
}

func (store *Store) Record(ctx context.Context, delivery Delivery) (bool, error) {
	if err := validateDelivery(delivery); err != nil {
		return false, err
	}
	tag, err := store.database.Pool.Exec(ctx, `INSERT INTO agents_primary.organization_agent_deliveries
		(event_id, entry_id, teamspace_id, organization_id, author_user_id, author_agent_id, entry_kind, published_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8) ON CONFLICT DO NOTHING`,
		delivery.EventID, delivery.EntryID, delivery.TeamspaceID, delivery.OrganizationID,
		delivery.AuthorUserID, delivery.AuthorAgentID, delivery.Kind, delivery.PublishedAt)
	if err != nil {
		return false, fmt.Errorf("record organization-agent delivery: %w", err)
	}
	return tag.RowsAffected() == 1, nil
}

// Claim preserves publication order per organization while allowing separate
// organizations to synthesize concurrently.
func (store *Store) Claim(ctx context.Context, lease time.Duration, limit int) ([]Delivery, error) {
	if lease <= 0 || limit < 1 {
		return nil, errors.New("claiming organization-agent deliveries needs a positive lease and limit")
	}
	rows, err := store.database.Pool.Query(ctx, `WITH picked AS (
		SELECT candidate.event_id
		FROM agents_primary.organization_agent_deliveries candidate
		WHERE candidate.handled_at IS NULL
		  AND candidate.available_at <= now()
		  AND (candidate.claimed_until IS NULL OR candidate.claimed_until < now())
		  AND NOT EXISTS (
			SELECT 1 FROM agents_primary.organization_agent_deliveries earlier
			WHERE earlier.organization_id=candidate.organization_id
			  AND earlier.handled_at IS NULL
			  AND (earlier.published_at, earlier.recorded_at, earlier.event_id) <
			      (candidate.published_at, candidate.recorded_at, candidate.event_id)
		  )
		ORDER BY candidate.available_at, candidate.published_at, candidate.recorded_at, candidate.event_id
		FOR UPDATE SKIP LOCKED LIMIT $1
	)
	UPDATE agents_primary.organization_agent_deliveries claimed
	SET claimed_until=now()+make_interval(secs => $2)
	FROM picked WHERE claimed.event_id=picked.event_id
	RETURNING claimed.event_id, claimed.entry_id, claimed.teamspace_id, claimed.organization_id,
		claimed.author_user_id, claimed.author_agent_id, claimed.entry_kind, claimed.published_at, claimed.attempts`,
		limit, lease.Seconds())
	if err != nil {
		return nil, fmt.Errorf("claim organization-agent deliveries: %w", err)
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (Delivery, error) {
		var delivery Delivery
		err := row.Scan(&delivery.EventID, &delivery.EntryID, &delivery.TeamspaceID, &delivery.OrganizationID,
			&delivery.AuthorUserID, &delivery.AuthorAgentID, &delivery.Kind, &delivery.PublishedAt, &delivery.Attempts)
		return delivery, err
	})
}

func (store *Store) Synthesis(ctx context.Context, organizationID string) (Synthesis, error) {
	var synthesis Synthesis
	var decisions, risks, questions []byte
	err := store.database.Pool.QueryRow(ctx, `SELECT organization_id, agent_id, revision, summary,
		decisions, risks, open_questions, source_entry_id, updated_at
		FROM agents_primary.organization_agent_syntheses WHERE organization_id=$1`, organizationID).
		Scan(&synthesis.OrganizationID, &synthesis.AgentID, &synthesis.Revision, &synthesis.Summary,
			&decisions, &risks, &questions, &synthesis.SourceEntryID, &synthesis.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return emptySynthesis(organizationID), nil
	}
	if err != nil {
		return Synthesis{}, fmt.Errorf("read organization synthesis: %w", err)
	}
	if err := decodeLists(&synthesis, decisions, risks, questions); err != nil {
		return Synthesis{}, err
	}
	return synthesis, nil
}

func (store *Store) ListFollowUps(ctx context.Context, organizationID string, limit int) ([]FollowUp, error) {
	if limit <= 0 {
		limit = 100
	}
	if limit > 500 {
		limit = 500
	}
	rows, err := store.database.Pool.Query(ctx, `SELECT id, organization_id, synthesis_revision,
		source_entry_id, teamspace_id, target_agent_id, task, rationale, status, created_at
		FROM agents_primary.organization_agent_followups WHERE organization_id=$1
		ORDER BY created_at DESC, id DESC LIMIT $2`, organizationID, limit)
	if err != nil {
		return nil, fmt.Errorf("list organization-agent follow-ups: %w", err)
	}
	defer rows.Close()
	result := []FollowUp{}
	for rows.Next() {
		var followUp FollowUp
		if err := rows.Scan(&followUp.ID, &followUp.OrganizationID, &followUp.SynthesisRevision,
			&followUp.SourceEntryID, &followUp.TeamspaceID, &followUp.TargetAgentID,
			&followUp.Task, &followUp.Rationale, &followUp.Status, &followUp.CreatedAt); err != nil {
			return nil, err
		}
		result = append(result, followUp)
	}
	return result, rows.Err()
}

// Apply atomically advances the synthesis, stores proposed follow-ups, marks
// the source delivery handled, and emits one content-minimal lifecycle event.
func (store *Store) Apply(ctx context.Context, delivery Delivery, agentID string, result Result) (Synthesis, error) {
	var synthesis Synthesis
	err := pgx.BeginFunc(ctx, store.database.Pool, func(tx pgx.Tx) error {
		var handledAt *time.Time
		if err := tx.QueryRow(ctx, `SELECT handled_at FROM agents_primary.organization_agent_deliveries
			WHERE event_id=$1 FOR UPDATE`, delivery.EventID).Scan(&handledAt); errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		} else if err != nil {
			return err
		}
		if handledAt != nil {
			current, err := synthesisFrom(ctx, tx, delivery.OrganizationID)
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrNotFound
			}
			synthesis = current
			return err
		}
		current, err := synthesisFrom(ctx, tx, delivery.OrganizationID)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		revision := current.Revision + 1
		now := time.Now().UTC()
		decisions, _ := json.Marshal(result.Decisions)
		risks, _ := json.Marshal(result.Risks)
		questions, _ := json.Marshal(result.OpenQuestions)
		var encodedDecisions, encodedRisks, encodedQuestions []byte
		if err := tx.QueryRow(ctx, `INSERT INTO agents_primary.organization_agent_syntheses
			(organization_id, agent_id, revision, summary, decisions, risks, open_questions, source_entry_id, updated_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)
			ON CONFLICT (organization_id) DO UPDATE SET
				agent_id=EXCLUDED.agent_id, revision=EXCLUDED.revision, summary=EXCLUDED.summary,
				decisions=EXCLUDED.decisions, risks=EXCLUDED.risks, open_questions=EXCLUDED.open_questions,
				source_entry_id=EXCLUDED.source_entry_id, updated_at=EXCLUDED.updated_at
			RETURNING organization_id, agent_id, revision, summary, decisions, risks, open_questions, source_entry_id, updated_at`,
			delivery.OrganizationID, agentID, revision, result.Summary, decisions, risks, questions, delivery.EntryID, now).
			Scan(&synthesis.OrganizationID, &synthesis.AgentID, &synthesis.Revision, &synthesis.Summary,
				&encodedDecisions, &encodedRisks, &encodedQuestions, &synthesis.SourceEntryID, &synthesis.UpdatedAt); err != nil {
			return err
		}
		if err := decodeLists(&synthesis, encodedDecisions, encodedRisks, encodedQuestions); err != nil {
			return err
		}
		for _, proposed := range result.FollowUps {
			id, err := newID("ofu_")
			if err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `INSERT INTO agents_primary.organization_agent_followups
				(id, organization_id, synthesis_revision, source_entry_id, teamspace_id,
				 target_agent_id, task, rationale, status, created_at)
				VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`,
				id, delivery.OrganizationID, revision, delivery.EntryID, delivery.TeamspaceID,
				proposed.TargetAgentID, proposed.Task, proposed.Rationale, FollowUpProposed, now); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, `UPDATE agents_primary.organization_agent_deliveries
			SET handled_at=$2, claimed_until=NULL, last_error='' WHERE event_id=$1`, delivery.EventID, now); err != nil {
			return err
		}
		return recordSynthesisLifecycle(ctx, tx, synthesis, delivery.EntryID, len(result.FollowUps))
	})
	if err != nil {
		return Synthesis{}, fmt.Errorf("apply organization synthesis: %w", err)
	}
	return synthesis, nil
}

func (store *Store) Release(ctx context.Context, eventID string, failed bool, maxAttempts int, retryAfter time.Duration, reason string) (bool, error) {
	increment := 0
	if failed {
		increment = 1
	}
	var dropped bool
	err := pgx.BeginFunc(ctx, store.database.Pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE agents_primary.organization_agent_deliveries
			SET handled_at=now(), claimed_until=NULL, dropped=true, attempts=attempts+$2, last_error=$4
			WHERE event_id=$1 AND handled_at IS NULL AND attempts+$2 >= $3`,
			eventID, increment, maxAttempts, bounded(reason))
		if err != nil {
			return err
		}
		dropped = tag.RowsAffected() == 1
		if dropped {
			return nil
		}
		_, err = tx.Exec(ctx, `UPDATE agents_primary.organization_agent_deliveries
			SET claimed_until=NULL, available_at=now()+make_interval(secs => $3),
			attempts=attempts+$2, last_error=$4 WHERE event_id=$1 AND handled_at IS NULL`,
			eventID, increment, retryAfter.Seconds(), bounded(reason))
		return err
	})
	return dropped, err
}

func (store *Store) Drop(ctx context.Context, eventID, reason string) error {
	_, err := store.database.Pool.Exec(ctx, `UPDATE agents_primary.organization_agent_deliveries
		SET handled_at=now(), claimed_until=NULL, dropped=true, last_error=$2
		WHERE event_id=$1 AND handled_at IS NULL`, eventID, bounded(reason))
	return err
}

func (store *Store) Purge(ctx context.Context, age time.Duration) error {
	_, err := store.database.Pool.Exec(ctx, `DELETE FROM agents_primary.organization_agent_deliveries
		WHERE handled_at IS NOT NULL AND handled_at < now()-make_interval(secs => $1)`, age.Seconds())
	return err
}

func (store *Store) Forget(ctx context.Context, organizationID, userID string) error {
	if strings.TrimSpace(userID) != "" {
		_, err := store.database.Pool.Exec(ctx, `UPDATE agents_primary.organization_agent_followups
			SET target_agent_id=''
			WHERE organization_id=$1 AND target_agent_id IN (
				SELECT id FROM agents_primary.member_agents
				WHERE organization_id=$1 AND owner_user_id=$2
			)`, organizationID, userID)
		return err
	}
	return pgx.BeginFunc(ctx, store.database.Pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `DELETE FROM agents_primary.organization_agent_followups WHERE organization_id=$1`, organizationID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM agents_primary.organization_agent_deliveries WHERE organization_id=$1`, organizationID); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `DELETE FROM agents_primary.organization_agent_syntheses WHERE organization_id=$1`, organizationID)
		return err
	})
}

func synthesisFrom(ctx context.Context, tx pgx.Tx, organizationID string) (Synthesis, error) {
	var synthesis Synthesis
	var decisions, risks, questions []byte
	err := tx.QueryRow(ctx, `SELECT organization_id, agent_id, revision, summary,
		decisions, risks, open_questions, source_entry_id, updated_at
		FROM agents_primary.organization_agent_syntheses WHERE organization_id=$1 FOR UPDATE`, organizationID).
		Scan(&synthesis.OrganizationID, &synthesis.AgentID, &synthesis.Revision, &synthesis.Summary,
			&decisions, &risks, &questions, &synthesis.SourceEntryID, &synthesis.UpdatedAt)
	if err != nil {
		return Synthesis{}, err
	}
	return synthesis, decodeLists(&synthesis, decisions, risks, questions)
}

func decodeLists(synthesis *Synthesis, decisions, risks, questions []byte) error {
	if err := json.Unmarshal(decisions, &synthesis.Decisions); err != nil {
		return fmt.Errorf("decode synthesis decisions: %w", err)
	}
	if err := json.Unmarshal(risks, &synthesis.Risks); err != nil {
		return fmt.Errorf("decode synthesis risks: %w", err)
	}
	if err := json.Unmarshal(questions, &synthesis.OpenQuestions); err != nil {
		return fmt.Errorf("decode synthesis questions: %w", err)
	}
	return nil
}

func emptySynthesis(organizationID string) Synthesis {
	return Synthesis{OrganizationID: organizationID, Decisions: []string{}, Risks: []string{}, OpenQuestions: []string{}}
}

func recordSynthesisLifecycle(ctx context.Context, tx pgx.Tx, synthesis Synthesis, sourceEntryID string, followUps int) error {
	event, err := events.NewEvent(agentsv1.OrganizationAgentSynthesisUpdatedDetailType, agentsv1.OrganizationAgentSynthesisUpdated{
		OrganizationID: synthesis.OrganizationID, AgentID: synthesis.AgentID, Revision: synthesis.Revision,
		SourceEntryID: sourceEntryID, ProposedFollowUps: followUps, UpdatedAt: synthesis.UpdatedAt,
	}, synthesis.UpdatedAt)
	if err != nil {
		return err
	}
	trace := observability.CaptureTraceCarrier(ctx).Encode()
	_, err = tx.Exec(ctx, `INSERT INTO agents_primary.agents_outbox
		(id, detail_type, detail, occurred_at, trace_context) VALUES ($1,$2,$3,$4,NULLIF($5,''))`,
		event.ID, event.DetailType, event.Data, event.OccurredAt, trace)
	return err
}

func validateDelivery(delivery Delivery) error {
	switch {
	case strings.TrimSpace(delivery.EventID) == "", strings.TrimSpace(delivery.EntryID) == "":
		return fmt.Errorf("%w: delivery needs event and entry ids", ErrInvalid)
	case strings.TrimSpace(delivery.TeamspaceID) == "", strings.TrimSpace(delivery.OrganizationID) == "":
		return fmt.Errorf("%w: delivery needs teamspace and organization ids", ErrInvalid)
	case strings.TrimSpace(delivery.AuthorUserID) == "", strings.TrimSpace(delivery.AuthorAgentID) == "":
		return fmt.Errorf("%w: delivery needs attributed author ids", ErrInvalid)
	case strings.TrimSpace(delivery.Kind) == "":
		return fmt.Errorf("%w: delivery needs an entry kind", ErrInvalid)
	case delivery.PublishedAt.IsZero():
		return fmt.Errorf("%w: delivery needs its publication time", ErrInvalid)
	}
	return nil
}

func newID(prefix string) (string, error) {
	bytes := make([]byte, 16)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	return prefix + base64.RawURLEncoding.EncodeToString(bytes), nil
}

func bounded(value string) string {
	value = strings.TrimSpace(value)
	runes := []rune(value)
	if len(runes) > 2000 {
		return string(runes[:2000])
	}
	return value
}
