package surfaces

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

// migrationsAdvisoryLock is the surfaces service's own, from the 72635107xx
// range the agents' stores share; the router's 7263510719 is the highest taken
// before it.
const migrationsAdvisoryLock int64 = 7263510720

// Store keeps the surfaces service's jobs and links in PostgreSQL.
type Store struct{ database *postgreskit.Database }

func NewStore(database *postgreskit.Database) *Store { return &Store{database: database} }

func (store *Store) Migrate(ctx context.Context) error {
	return store.database.Migrate(ctx, "agents-surfaces", migrations, "migrations/*.sql", migrationsAdvisoryLock)
}

// Job is one request with something to be said about it.
type Job struct {
	RequestID      string
	OrganizationID string
	// AskerUserID and OwnerUserID are the members the last event named.
	AskerUserID string
	OwnerUserID string
	// Wanted is how many events and sweeps had asked for a pass when this one
	// was claimed: what a finished pass covers.
	Wanted int64
	// Attempts is how many times a pass for it has failed in a row.
	Attempts int
}

// Wake is what an event says of the request it is about.
type Wake struct {
	RequestID      string
	OrganizationID string
	EventID        string
	AskerUserID    string
	OwnerUserID    string
}

func (wake Wake) validate() error {
	if strings.TrimSpace(wake.RequestID) == "" || strings.TrimSpace(wake.OrganizationID) == "" {
		return errors.New("waking a request's surfaces needs the request and its organization")
	}
	return nil
}

// Wake makes the request's pass due, and makes the row if there is none. A
// request is one row however many events name it, and an event that names it
// again while its pass runs, or after, makes it due once more: passes are
// idempotent, so the same event delivered twice costs a look at the ledger and
// nothing said twice.
//
// An event does not end a wait. A pass that failed is tried again when its delay
// is over, and reads the ledger then, so it covers what came in meanwhile; to
// run it sooner would only meet the same rate limit or outage again. A row that
// was given up on starts over: it has no retry coming.
func (store *Store) Wake(ctx context.Context, wake Wake) error {
	if err := wake.validate(); err != nil {
		return err
	}
	_, err := store.database.Pool.Exec(ctx, `
		INSERT INTO agents_primary.surface_jobs
			(request_id, organization_id, asker_user_id, owner_user_id, last_event_id)
		VALUES ($1,$2,$3,$4,$5)
		ON CONFLICT (request_id) DO UPDATE SET
			wanted = agents_primary.surface_jobs.wanted + 1,
			last_event_id = EXCLUDED.last_event_id,
			asker_user_id = CASE WHEN EXCLUDED.asker_user_id <> '' THEN EXCLUDED.asker_user_id ELSE agents_primary.surface_jobs.asker_user_id END,
			owner_user_id = CASE WHEN EXCLUDED.owner_user_id <> '' THEN EXCLUDED.owner_user_id ELSE agents_primary.surface_jobs.owner_user_id END,
			available_at = CASE WHEN agents_primary.surface_jobs.gave_up THEN now() ELSE agents_primary.surface_jobs.available_at END,
			attempts = CASE WHEN agents_primary.surface_jobs.gave_up THEN 0 ELSE agents_primary.surface_jobs.attempts END,
			gave_up = false, updated_at = now()`,
		wake.RequestID, wake.OrganizationID, wake.AskerUserID, wake.OwnerUserID, wake.EventID)
	if err != nil {
		return fmt.Errorf("record a request's surfaces work: %w", err)
	}
	return nil
}

// Claim leases due jobs. Two replicas never hold the same one, and one whose
// holder died is due again once its lease runs out.
func (store *Store) Claim(ctx context.Context, lease time.Duration, limit int) ([]Job, error) {
	if lease <= 0 || limit < 1 {
		return nil, errors.New("claiming surfaces jobs needs a positive lease and limit")
	}
	rows, err := store.database.Pool.Query(ctx, `
		WITH picked AS (
			SELECT candidate.request_id
			FROM agents_primary.surface_jobs candidate
			WHERE candidate.handled < candidate.wanted
			  AND candidate.available_at <= now()
			  AND (candidate.claimed_until IS NULL OR candidate.claimed_until < now())
			ORDER BY candidate.available_at, candidate.recorded_at, candidate.request_id
			FOR UPDATE SKIP LOCKED
			LIMIT $1
		)
		UPDATE agents_primary.surface_jobs claimed
		SET claimed_until = now() + make_interval(secs => $2)
		FROM picked
		WHERE claimed.request_id = picked.request_id
		RETURNING claimed.request_id, claimed.organization_id, claimed.asker_user_id, claimed.owner_user_id,
			claimed.wanted, claimed.attempts`,
		limit, lease.Seconds())
	if err != nil {
		return nil, fmt.Errorf("claim surfaces jobs: %w", err)
	}
	claimed, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (Job, error) {
		var job Job
		err := row.Scan(&job.RequestID, &job.OrganizationID, &job.AskerUserID, &job.OwnerUserID, &job.Wanted, &job.Attempts)
		return job, err
	})
	if err != nil {
		return nil, fmt.Errorf("read claimed surfaces jobs: %w", err)
	}
	return claimed, nil
}

// Done records that a pass covered everything asked for up to job.Wanted. An
// event that came in since leaves the job due again.
func (store *Store) Done(ctx context.Context, job Job) error {
	_, err := store.database.Pool.Exec(ctx, `
		UPDATE agents_primary.surface_jobs
		SET handled = GREATEST(handled, $2), claimed_until = NULL, attempts = 0, last_error = '', updated_at = now()
		WHERE request_id = $1`, job.RequestID, job.Wanted)
	if err != nil {
		return fmt.Errorf("finish a surfaces job: %w", err)
	}
	return nil
}

// GiveUp records that what a job asked for cannot be done, and never will: its
// request is gone or is not one Slack has to hear of. The next event for it
// starts over.
func (store *Store) GiveUp(ctx context.Context, job Job, reason string) error {
	_, err := store.database.Pool.Exec(ctx, `
		UPDATE agents_primary.surface_jobs
		SET handled = GREATEST(handled, $2), claimed_until = NULL, attempts = 0, gave_up = true,
			last_error = $3, updated_at = now()
		WHERE request_id = $1`, job.RequestID, job.Wanted, boundedError(reason))
	if err != nil {
		return fmt.Errorf("give up a surfaces job: %w", err)
	}
	return nil
}

// Release puts a job back to be tried again after retryAfter. A failure counts
// toward maxAttempts, and the job is given up on when it reaches it; a release
// that is not a failure, such as a shutdown in the middle of a pass, counts for
// nothing. It reports whether the job was given up on.
func (store *Store) Release(ctx context.Context, job Job, failed bool, maxAttempts int, retryAfter time.Duration, reason string) (bool, error) {
	if maxAttempts < 1 || retryAfter < 0 {
		return false, errors.New("releasing a surfaces job needs positive attempts and a non-negative retry delay")
	}
	increment := 0
	if failed {
		increment = 1
	}
	var gaveUp bool
	err := pgx.BeginFunc(ctx, store.database.Pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
			UPDATE agents_primary.surface_jobs
			SET handled = GREATEST(handled, $2), claimed_until = NULL, attempts = 0, gave_up = true,
				last_error = $5, updated_at = now()
			WHERE request_id = $1 AND attempts + $3 >= $4`,
			job.RequestID, job.Wanted, increment, maxAttempts, boundedError(reason))
		if err != nil {
			return fmt.Errorf("give up a failing surfaces job: %w", err)
		}
		gaveUp = tag.RowsAffected() == 1
		if gaveUp {
			return nil
		}
		_, err = tx.Exec(ctx, `
			UPDATE agents_primary.surface_jobs
			SET claimed_until = NULL, available_at = now() + make_interval(secs => $3),
				attempts = attempts + $2, last_error = $4, updated_at = now()
			WHERE request_id = $1`,
			job.RequestID, increment, retryAfter.Seconds(), boundedError(reason))
		if err != nil {
			return fmt.Errorf("release a surfaces job: %w", err)
		}
		return nil
	})
	return gaveUp, err
}

// WakeExpired makes due the passes of notices whose buttons have outlived their
// request: nothing else would take the buttons away, since a request that
// lapses is announced by no event. Each is woken at most once a minute until its
// pass settles the notice. It reports how many it woke.
func (store *Store) WakeExpired(ctx context.Context, now time.Time, limit int) (int, error) {
	if limit < 1 {
		return 0, errors.New("waking expired notices needs a positive limit")
	}
	tag, err := store.database.Pool.Exec(ctx, `
		WITH due AS (
			UPDATE agents_primary.surface_links link SET swept_at = $1
			WHERE (link.request_id, link.role) IN (
				SELECT candidate.request_id, candidate.role
				FROM agents_primary.surface_links candidate
				WHERE candidate.role = 'notice' AND candidate.state = 'pending'
				  AND candidate.expires_at IS NOT NULL AND candidate.expires_at <= $1
				  AND (candidate.swept_at IS NULL OR candidate.swept_at < $1 - make_interval(mins => 1))
				ORDER BY candidate.expires_at
				FOR UPDATE SKIP LOCKED
				LIMIT $2
			)
			RETURNING link.request_id, link.organization_id, link.asker_user_id, link.owner_user_id
		)
		INSERT INTO agents_primary.surface_jobs
			(request_id, organization_id, asker_user_id, owner_user_id, last_event_id)
		SELECT request_id, organization_id, asker_user_id, owner_user_id, 'expiry' FROM due
		ON CONFLICT (request_id) DO UPDATE SET
			wanted = agents_primary.surface_jobs.wanted + 1,
			available_at = CASE WHEN agents_primary.surface_jobs.gave_up THEN now() ELSE agents_primary.surface_jobs.available_at END,
			attempts = CASE WHEN agents_primary.surface_jobs.gave_up THEN 0 ELSE agents_primary.surface_jobs.attempts END,
			gave_up = false, updated_at = now()`, now.UTC(), limit)
	if err != nil {
		return 0, fmt.Errorf("wake the notices of expired requests: %w", err)
	}
	return int(tag.RowsAffected()), nil
}

// Purge forgets what is no longer needed: jobs finished longer ago than jobAge,
// and links not touched for linkAge, which is long past the time any request
// they are about could still be asked, answered or reviewed.
func (store *Store) Purge(ctx context.Context, jobAge, linkAge time.Duration) error {
	if _, err := store.database.Pool.Exec(ctx, `DELETE FROM agents_primary.surface_jobs
		WHERE handled >= wanted AND updated_at < now() - make_interval(secs => $1)`, jobAge.Seconds()); err != nil {
		return fmt.Errorf("purge surfaces jobs: %w", err)
	}
	if _, err := store.database.Pool.Exec(ctx, `DELETE FROM agents_primary.surface_links
		WHERE updated_at < now() - make_interval(secs => $1)`, linkAge.Seconds()); err != nil {
		return fmt.Errorf("purge surfaces links: %w", err)
	}
	return nil
}

// Link is one message of a request that was posted, or given up on.
type Link struct {
	RequestID      string
	Role           string
	OrganizationID string
	TeamID         string
	AskerUserID    string
	OwnerUserID    string
	// ChannelID and TS are where the message is in Slack: what an update names
	// it by. Empty for one that was given up on before it was sent, and for an
	// ephemeral message, which cannot be updated.
	ChannelID string
	TS        string
	// State is what the message says: for a notice, which of its states; for the
	// rest, that it was sent or given up on.
	State string
	// ExpiresAt is, for a notice that still has its buttons, when its request
	// lapses.
	ExpiresAt *time.Time
	UpdatedAt time.Time
}

func (link Link) validate() error {
	if strings.TrimSpace(link.RequestID) == "" || strings.TrimSpace(link.Role) == "" || strings.TrimSpace(link.OrganizationID) == "" {
		return errors.New("a surfaces link needs its request, role and organization")
	}
	return nil
}

// Link reads the link of a request's role, and whether there is one.
func (store *Store) Link(ctx context.Context, requestID, role string) (Link, bool, error) {
	var link Link
	err := store.database.Pool.QueryRow(ctx, `
		SELECT request_id, role, organization_id, team_id, asker_user_id, owner_user_id, channel_id, ts, state, expires_at, updated_at
		FROM agents_primary.surface_links WHERE request_id = $1 AND role = $2`, requestID, role).
		Scan(&link.RequestID, &link.Role, &link.OrganizationID, &link.TeamID, &link.AskerUserID, &link.OwnerUserID,
			&link.ChannelID, &link.TS, &link.State, &link.ExpiresAt, &link.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Link{}, false, nil
	}
	if err != nil {
		return Link{}, false, fmt.Errorf("read a surfaces link: %w", err)
	}
	return link, true, nil
}

// PutLink records a message, replacing what was recorded for the same request
// and role.
func (store *Store) PutLink(ctx context.Context, link Link) error {
	if err := link.validate(); err != nil {
		return err
	}
	_, err := store.database.Pool.Exec(ctx, `
		INSERT INTO agents_primary.surface_links
			(request_id, role, organization_id, team_id, asker_user_id, owner_user_id, channel_id, ts, state, expires_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)
		ON CONFLICT (request_id, role) DO UPDATE SET
			organization_id = EXCLUDED.organization_id, team_id = EXCLUDED.team_id,
			asker_user_id = EXCLUDED.asker_user_id, owner_user_id = EXCLUDED.owner_user_id,
			channel_id = EXCLUDED.channel_id, ts = EXCLUDED.ts, state = EXCLUDED.state,
			expires_at = EXCLUDED.expires_at, updated_at = now()`,
		link.RequestID, link.Role, link.OrganizationID, link.TeamID, link.AskerUserID, link.OwnerUserID,
		link.ChannelID, link.TS, link.State, link.ExpiresAt)
	if err != nil {
		return fmt.Errorf("record a surfaces link: %w", err)
	}
	return nil
}

// SetLinkState records what a message now says, and that it no longer has
// buttons to expire.
func (store *Store) SetLinkState(ctx context.Context, requestID, role, state string) error {
	_, err := store.database.Pool.Exec(ctx, `
		UPDATE agents_primary.surface_links SET state = $3, expires_at = NULL, updated_at = now()
		WHERE request_id = $1 AND role = $2`, requestID, role, state)
	if err != nil {
		return fmt.Errorf("record what a surfaces link says: %w", err)
	}
	return nil
}

// Forget removes what the service holds for a departed member, or for a deleted
// organization when no member is named: the rows of every request they were
// party to, as the one who asked or the one asked. It is inbound.Forgetter's
// shape. What was said in Slack stays there, in the workspace's own hands.
func (store *Store) Forget(ctx context.Context, organizationID, memberUserID string) error {
	if strings.TrimSpace(organizationID) == "" {
		return errors.New("forgetting surfaces rows needs an organization")
	}
	return pgx.BeginFunc(ctx, store.database.Pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `DELETE FROM agents_primary.surface_links
			WHERE organization_id = $1 AND ($2 = '' OR asker_user_id = $2 OR owner_user_id = $2)`,
			organizationID, memberUserID); err != nil {
			return fmt.Errorf("forget surfaces links: %w", err)
		}
		if _, err := tx.Exec(ctx, `DELETE FROM agents_primary.surface_jobs
			WHERE organization_id = $1 AND ($2 = '' OR asker_user_id = $2 OR owner_user_id = $2)`,
			organizationID, memberUserID); err != nil {
			return fmt.Errorf("forget surfaces jobs: %w", err)
		}
		return nil
	})
}

// ForgetTeam removes what the service holds for a Slack workspace the
// organization uninstalled the app from (Slack calls a workspace a team): the
// coordinates of every message in it, which nothing can be said to any more,
// and the passes that had not run. An organization has one workspace, so the
// passes are the organization's. It is the removal counterpart of the router's
// ForgetTeam.
func (store *Store) ForgetTeam(ctx context.Context, organizationID, teamID string) error {
	if strings.TrimSpace(organizationID) == "" || strings.TrimSpace(teamID) == "" {
		return errors.New("forgetting a Slack workspace's surfaces rows needs an organization and a team")
	}
	return pgx.BeginFunc(ctx, store.database.Pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `DELETE FROM agents_primary.surface_links
			WHERE organization_id = $1 AND team_id = $2`, organizationID, teamID); err != nil {
			return fmt.Errorf("forget the surfaces links of a workspace: %w", err)
		}
		if _, err := tx.Exec(ctx, `
			UPDATE agents_primary.surface_jobs
			SET handled = wanted, claimed_until = NULL, gave_up = true, last_error = 'the workspace was uninstalled', updated_at = now()
			WHERE organization_id = $1 AND handled < wanted`, organizationID); err != nil {
			return fmt.Errorf("give up the surfaces jobs of a workspace: %w", err)
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
