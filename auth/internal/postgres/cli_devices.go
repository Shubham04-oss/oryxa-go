package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/Shubham04-oss/oryxa-go/auth/internal/clisession"
	"github.com/Shubham04-oss/oryxa-go/auth/internal/domain"
	"github.com/jackc/pgx/v5"
)

const cliDevicesAdvisoryLock int64 = 1123581355

func (store *Store) CreateCLIDevice(ctx context.Context, device clisession.Device) error {
	return pgx.BeginFunc(ctx, store.pool, func(tx pgx.Tx) error {
		// As for challenges, a global bound complements per-process limits.
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, cliDevicesAdvisoryLock); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM auth_cli_devices WHERE expires_at<=now()`); err != nil {
			return err
		}
		var pending int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM auth_cli_devices`).Scan(&pending); err != nil {
			return err
		}
		if pending >= 4096 {
			return domain.ErrRateLimited
		}
		_, err := tx.Exec(ctx, `INSERT INTO auth_cli_devices(device_code_hash,user_code_hash,client_name,client_address,interval_seconds,expires_at) VALUES($1,$2,$3,$4,$5,$6)`,
			device.DeviceCodeHash, device.UserCodeHash, device.ClientName, device.ClientAddress, int(device.Interval/time.Second), device.ExpiresAt)
		return err
	})
}

func (store *Store) CLIDevice(ctx context.Context, userCodeHash string) (clisession.Device, error) {
	result := clisession.Device{UserCodeHash: userCodeHash}
	var interval int
	err := store.pool.QueryRow(ctx, `SELECT device_code_hash,client_name,client_address,interval_seconds,created_at,expires_at FROM auth_cli_devices WHERE user_code_hash=$1 AND status='pending' AND expires_at>now()`, userCodeHash).
		Scan(&result.DeviceCodeHash, &result.ClientName, &result.ClientAddress, &interval, &result.CreatedAt, &result.ExpiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return clisession.Device{}, domain.ErrNotFound
	}
	result.Interval = time.Duration(interval) * time.Second
	return result, err
}

func (store *Store) DecideCLIDevice(ctx context.Context, userCodeHash, subject string, approve bool) error {
	if subject == "" {
		return domain.ErrUnauthorized
	}
	status := clisession.DeviceDenied
	if approve {
		status = clisession.DeviceApproved
	}
	updated, err := store.pool.Exec(ctx, `UPDATE auth_cli_devices SET status=$3, subject=$2 WHERE user_code_hash=$1 AND status='pending' AND expires_at>now()`, userCodeHash, subject, string(status))
	if err != nil {
		return err
	}
	if updated.RowsAffected() != 1 {
		return domain.ErrNotFound
	}
	return nil
}

func (store *Store) PollCLIDevice(ctx context.Context, deviceCodeHash, tokenHash string, sessionExpires time.Time) (clisession.Poll, error) {
	var result clisession.Poll
	err := pgx.BeginFunc(ctx, store.pool, func(tx pgx.Tx) error {
		var status, subject string
		var interval int
		var live, tooFast bool
		err := tx.QueryRow(ctx, `SELECT status, subject, interval_seconds, expires_at>now(),
			last_polled_at IS NOT NULL AND last_polled_at > now() - interval_seconds * interval '1 second'
			FROM auth_cli_devices WHERE device_code_hash=$1 FOR UPDATE`, deviceCodeHash).Scan(&status, &subject, &interval, &live, &tooFast)
		if errors.Is(err, pgx.ErrNoRows) {
			result.Status = clisession.PollExpired
			return nil
		}
		if err != nil {
			return err
		}
		result.Interval = time.Duration(interval) * time.Second
		finished := !live || status != string(clisession.DevicePending)
		if finished {
			// Every outcome but pending is answered once; a later poll with
			// the same code finds nothing.
			if _, err := tx.Exec(ctx, `DELETE FROM auth_cli_devices WHERE device_code_hash=$1`, deviceCodeHash); err != nil {
				return err
			}
		}
		switch {
		case !live:
			result.Status = clisession.PollExpired
		case status == string(clisession.DeviceDenied):
			result.Status = clisession.PollDenied
		case status == string(clisession.DeviceApproved):
			if subject == "" || !sessionExpires.After(time.Now()) {
				return domain.ErrUnauthorized
			}
			inserted, err := tx.Exec(ctx, `INSERT INTO auth_cli_sessions(token_hash,subject,expires_at) VALUES($1,$2,$3) ON CONFLICT DO NOTHING`, tokenHash, subject, sessionExpires)
			if err != nil {
				return err
			}
			if inserted.RowsAffected() != 1 {
				return domain.ErrUnauthorized
			}
			result.Subject = subject
		case tooFast:
			interval += int(clisession.SlowDownStep / time.Second)
			if _, err := tx.Exec(ctx, `UPDATE auth_cli_devices SET last_polled_at=now(), interval_seconds=$2 WHERE device_code_hash=$1`, deviceCodeHash, interval); err != nil {
				return err
			}
			result.Status = clisession.PollSlowDown
			result.Interval = time.Duration(interval) * time.Second
		default:
			if _, err := tx.Exec(ctx, `UPDATE auth_cli_devices SET last_polled_at=now() WHERE device_code_hash=$1`, deviceCodeHash); err != nil {
				return err
			}
			result.Status = clisession.PollPending
		}
		return nil
	})
	if err != nil {
		return clisession.Poll{}, err
	}
	return result, nil
}
