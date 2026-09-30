package slackrouter

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Shubham04-oss/oryxa-go/internal/postgreskit"
)

//go:embed migrations/*.sql
var migrations embed.FS

// migrationsAdvisoryLock is the router's own, from the 72635107xx range the
// agents' stores share; the highest taken before it was guardpolicy's
// 7263510718.
const migrationsAdvisoryLock int64 = 7263510719

// Store keeps the router's jobs and its identities in PostgreSQL.
type Store struct{ database *postgreskit.Database }

func NewStore(database *postgreskit.Database) *Store { return &Store{database: database} }

func (store *Store) Migrate(ctx context.Context) error {
	return store.database.Migrate(ctx, "agents-slack-router", migrations, "migrations/*.sql", migrationsAdvisoryLock)
}

// Enqueue records a job once. The bus delivers at least once, and apps may
// nudge for the same inbound row twice, so the inbound id is the key: it
// reports whether the job is new, and a job seen before, handled or not, is
// left exactly as it is.
func (store *Store) Enqueue(ctx context.Context, job Job) (bool, error) {
	if err := validateJob(job); err != nil {
		return false, err
	}
	tag, err := store.database.Pool.Exec(ctx, `
		INSERT INTO agents_primary.slack_jobs
			(inbound_id, kind, organization_id, team_id, event_id, received_at)
		VALUES ($1,$2,$3,$4,$5,$6)
		ON CONFLICT (inbound_id) DO NOTHING`,
		job.InboundID, job.Kind, job.OrganizationID, job.TeamID, job.EventID, job.ReceivedAt)
	if err != nil {
		return false, fmt.Errorf("record Slack job: %w", err)
	}
	return tag.RowsAffected() == 1, nil
}

// Claim leases due jobs. Two replicas never hold the same one, and one whose
// holder died is due again once its lease runs out.
func (store *Store) Claim(ctx context.Context, lease time.Duration, limit int) ([]Job, error) {
	if lease <= 0 || limit < 1 {
		return nil, errors.New("claiming Slack jobs needs a positive lease and limit")
	}
	rows, err := store.database.Pool.Query(ctx, `
		WITH picked AS (
			SELECT candidate.inbound_id
			FROM agents_primary.slack_jobs candidate
			WHERE candidate.handled_at IS NULL
			  AND candidate.available_at <= now()
			  AND (candidate.claimed_until IS NULL OR candidate.claimed_until < now())
			ORDER BY candidate.available_at, candidate.recorded_at, candidate.inbound_id
			FOR UPDATE SKIP LOCKED
			LIMIT $1
		)
		UPDATE agents_primary.slack_jobs claimed
		SET claimed_until = now() + make_interval(secs => $2)
		FROM picked
		WHERE claimed.inbound_id = picked.inbound_id
		RETURNING claimed.inbound_id, claimed.kind, claimed.organization_id, claimed.team_id,
			claimed.event_id, claimed.received_at, claimed.attempts, claimed.ask_id`,
		limit, lease.Seconds())
	if err != nil {
		return nil, fmt.Errorf("claim Slack jobs: %w", err)
	}
	claimed, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (Job, error) {
		var job Job
		err := row.Scan(&job.InboundID, &job.Kind, &job.OrganizationID, &job.TeamID,
			&job.EventID, &job.ReceivedAt, &job.Attempts, &job.AskID)
		return job, err
	})
	if err != nil {
		return nil, fmt.Errorf("read claimed Slack jobs: %w", err)
	}
	return claimed, nil
}

// Done marks a job handled and keeps its row, so the same inbound row cannot
// be recorded as new again while apps still holds it.
func (store *Store) Done(ctx context.Context, inboundID string) error {
	_, err := store.database.Pool.Exec(ctx, `
		UPDATE agents_primary.slack_jobs
		SET handled_at=now(), claimed_until=NULL, last_error=''
		WHERE inbound_id=$1 AND handled_at IS NULL`, inboundID)
	if err != nil {
		return fmt.Errorf("finish Slack job: %w", err)
	}
	return nil
}

// Drop marks a job handled without its having done anything: what it was
// about is gone, or could never be routed.
func (store *Store) Drop(ctx context.Context, inboundID, reason string) error {
	_, err := store.database.Pool.Exec(ctx, `
		UPDATE agents_primary.slack_jobs
		SET handled_at=now(), claimed_until=NULL, dropped=true, last_error=$2
		WHERE inbound_id=$1 AND handled_at IS NULL`, inboundID, boundedError(reason))
	if err != nil {
		return fmt.Errorf("drop Slack job: %w", err)
	}
	return nil
}

// Release puts a job back to be tried again after retryAfter. A failure
// counts toward maxAttempts and the job is dropped when it reaches it; a
// release that is not a failure, such as a shutdown in the middle of it,
// counts for nothing. It reports whether the job was dropped.
func (store *Store) Release(ctx context.Context, inboundID string, failed bool, maxAttempts int, retryAfter time.Duration, reason string) (bool, error) {
	if maxAttempts < 1 || retryAfter < 0 {
		return false, errors.New("releasing a Slack job needs positive attempts and a non-negative retry delay")
	}
	increment := 0
	if failed {
		increment = 1
	}
	var dropped bool
	err := pgx.BeginFunc(ctx, store.database.Pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
			UPDATE agents_primary.slack_jobs
			SET handled_at=now(), claimed_until=NULL, dropped=true,
				attempts=attempts+$2, last_error=$4
			WHERE inbound_id=$1 AND handled_at IS NULL AND attempts+$2 >= $3`,
			inboundID, increment, maxAttempts, boundedError(reason))
		if err != nil {
			return fmt.Errorf("drop failing Slack job: %w", err)
		}
		dropped = tag.RowsAffected() == 1
		if dropped {
			return nil
		}
		_, err = tx.Exec(ctx, `
			UPDATE agents_primary.slack_jobs
			SET claimed_until=NULL, available_at=now()+make_interval(secs => $3),
				attempts=attempts+$2, last_error=$4
			WHERE inbound_id=$1 AND handled_at IS NULL`,
			inboundID, increment, retryAfter.Seconds(), boundedError(reason))
		if err != nil {
			return fmt.Errorf("release Slack job: %w", err)
		}
		return nil
	})
	return dropped, err
}

// MarkAsked records the ask a mention became.
func (store *Store) MarkAsked(ctx context.Context, inboundID, askID string) error {
	_, err := store.database.Pool.Exec(ctx, `
		UPDATE agents_primary.slack_jobs SET ask_id=$2 WHERE inbound_id=$1`, inboundID, askID)
	if err != nil {
		return fmt.Errorf("record the ask a Slack job made: %w", err)
	}
	return nil
}

// Purge removes jobs handled longer ago than age. apps purges an inbound row
// after seven days, so a job kept that long is past the point where the same
// one could be recorded again.
func (store *Store) Purge(ctx context.Context, age time.Duration) error {
	_, err := store.database.Pool.Exec(ctx, `DELETE FROM agents_primary.slack_jobs
		WHERE handled_at IS NOT NULL AND handled_at < now()-make_interval(secs => $1)`, age.Seconds())
	if err != nil {
		return fmt.Errorf("purge Slack jobs: %w", err)
	}
	return nil
}

// SaveIdentity records that a Slack user is a member, replacing what was
// there. It keeps one Slack user for each member of a workspace: a member who
// is matched to a different Slack user now (their old account was replaced)
// loses the old match.
func (store *Store) SaveIdentity(ctx context.Context, identity Identity) error {
	if strings.TrimSpace(identity.OrganizationID) == "" || strings.TrimSpace(identity.TeamID) == "" ||
		strings.TrimSpace(identity.SlackUserID) == "" || strings.TrimSpace(identity.MemberUserID) == "" {
		return errors.New("a Slack identity needs organization, team, Slack user and member ids")
	}
	return pgx.BeginFunc(ctx, store.database.Pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `DELETE FROM agents_primary.slack_identities
			WHERE team_id=$1 AND member_user_id=$2 AND slack_user_id<>$3`,
			identity.TeamID, identity.MemberUserID, identity.SlackUserID); err != nil {
			return fmt.Errorf("replace Slack identity: %w", err)
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO agents_primary.slack_identities
				(organization_id, team_id, slack_user_id, member_user_id, email, display_name)
			VALUES ($1,$2,$3,$4,$5,$6)
			ON CONFLICT (team_id, slack_user_id) DO UPDATE SET
				organization_id=EXCLUDED.organization_id, member_user_id=EXCLUDED.member_user_id,
				email=EXCLUDED.email, display_name=EXCLUDED.display_name, resolved_at=now()`,
			identity.OrganizationID, identity.TeamID, identity.SlackUserID, identity.MemberUserID,
			strings.ToLower(strings.TrimSpace(identity.Email)), identity.DisplayName); err != nil {
			return fmt.Errorf("save Slack identity: %w", err)
		}
		return nil
	})
}

// DeleteIdentity forgets a match that no longer holds: the Slack user is a
// guest now, or their address is no longer a member's.
func (store *Store) DeleteIdentity(ctx context.Context, teamID, slackUserID string) error {
	_, err := store.database.Pool.Exec(ctx, `DELETE FROM agents_primary.slack_identities
		WHERE team_id=$1 AND slack_user_id=$2`, teamID, slackUserID)
	if err != nil {
		return fmt.Errorf("delete Slack identity: %w", err)
	}
	return nil
}

// SlackUserFor is the way back from a member to the Slack user they were last
// matched to in the organization, for whoever must send them a direct
// message. ErrNoIdentity when they never were.
func (store *Store) SlackUserFor(ctx context.Context, organizationID, memberUserID string) (Identity, error) {
	var identity Identity
	err := store.database.Pool.QueryRow(ctx, `
		SELECT organization_id, team_id, slack_user_id, member_user_id, email, display_name, resolved_at
		FROM agents_primary.slack_identities
		WHERE organization_id=$1 AND member_user_id=$2
		ORDER BY resolved_at DESC
		LIMIT 1`, organizationID, memberUserID).
		Scan(&identity.OrganizationID, &identity.TeamID, &identity.SlackUserID, &identity.MemberUserID,
			&identity.Email, &identity.DisplayName, &identity.ResolvedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Identity{}, ErrNoIdentity
	}
	if err != nil {
		return Identity{}, fmt.Errorf("read Slack identity: %w", err)
	}
	return identity, nil
}

// Forget removes what the router holds for a departed member, or for a
// deleted organization when no member is named: their matches, and for an
// organization its jobs too. It is inbound.Forgetter's shape.
func (store *Store) Forget(ctx context.Context, organizationID, memberUserID string) error {
	if strings.TrimSpace(organizationID) == "" {
		return errors.New("forgetting Slack matches needs an organization")
	}
	return pgx.BeginFunc(ctx, store.database.Pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `DELETE FROM agents_primary.slack_identities
			WHERE organization_id=$1 AND ($2='' OR member_user_id=$2)`, organizationID, memberUserID); err != nil {
			return fmt.Errorf("forget Slack identities: %w", err)
		}
		if memberUserID == "" {
			if _, err := tx.Exec(ctx, `DELETE FROM agents_primary.slack_jobs WHERE organization_id=$1`, organizationID); err != nil {
				return fmt.Errorf("forget Slack jobs: %w", err)
			}
		}
		return nil
	})
}

// ForgetTeam removes what the router holds for a workspace the organization
// uninstalled the bot from: every match, and the jobs that had not run,
// which have nothing left to answer.
func (store *Store) ForgetTeam(ctx context.Context, organizationID, teamID string) error {
	if strings.TrimSpace(organizationID) == "" || strings.TrimSpace(teamID) == "" {
		return errors.New("forgetting a Slack workspace needs an organization and a team")
	}
	return pgx.BeginFunc(ctx, store.database.Pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `DELETE FROM agents_primary.slack_identities
			WHERE organization_id=$1 AND team_id=$2`, organizationID, teamID); err != nil {
			return fmt.Errorf("forget Slack identities of a workspace: %w", err)
		}
		if _, err := tx.Exec(ctx, `
			UPDATE agents_primary.slack_jobs
			SET handled_at=now(), claimed_until=NULL, dropped=true, last_error='the workspace was uninstalled'
			WHERE organization_id=$1 AND team_id=$2 AND handled_at IS NULL`, organizationID, teamID); err != nil {
			return fmt.Errorf("drop Slack jobs of a workspace: %w", err)
		}
		return nil
	})
}

func boundedError(reason string) string {
	runes := []rune(strings.TrimSpace(reason))
	if len(runes) > 1000 {
		runes = runes[:1000]
	}
	return strings.ToValidUTF8(strings.ReplaceAll(string(runes), "\x00", ""), "")
}

var (
	_ Identities = (*Store)(nil)
	_ Asked      = (*Store)(nil)
	_ jobQueue   = (*Store)(nil)
)
