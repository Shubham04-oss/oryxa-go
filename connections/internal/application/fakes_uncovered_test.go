package application

import (
	"context"
	"errors"
	"time"

	"github.com/Shubham04-oss/oryxa-go/connections/internal/domain"
)

// The in-memory fakes cover the connect flow. Features after it are tested
// against the PostgreSQL store; these keep the fakes satisfying the
// interfaces.

var errUncovered = errors.New("the in-memory fake does not cover this")

func (store *fakeStore) AnyApp(ctx context.Context, organizationID, appID string) (domain.App, error) {
	return store.App(ctx, organizationID, appID)
}

func (store *fakeStore) DeleteOrganizationApps(context.Context, string, time.Time, func(domain.App) (domain.Event, error)) (int, error) {
	return 0, errUncovered
}

func (store *fakeStore) CountOpenSessions(context.Context, string, string, time.Time) (int, error) {
	return 0, nil
}

func (store *fakeStore) DeleteExpiredSessions(context.Context, time.Time, int) (int, error) {
	return 0, errUncovered
}

func (store *fakeStore) CreateConnection(context.Context, domain.Connection, domain.Event, time.Time) (domain.Connection, error) {
	return domain.Connection{}, errUncovered
}

func (store *fakeStore) ReplaceCredentials(context.Context, domain.Connection, int64, domain.Event) (domain.Connection, error) {
	return domain.Connection{}, errUncovered
}

func (store *fakeStore) UpdateConnection(context.Context, string, string, domain.ConnectionChange, time.Time) (domain.Connection, error) {
	return domain.Connection{}, errUncovered
}

func (store *fakeStore) DisconnectConnections(context.Context, string, domain.OwnerFilter, domain.DisconnectReason, time.Time, func(domain.Connection) (domain.Event, error)) (int, error) {
	return 0, errUncovered
}

func (store *fakeStore) MarkNeedsReconnect(context.Context, string, string, int64, domain.ReconnectReason, time.Time, func(domain.Connection) (domain.Event, error)) (bool, error) {
	return false, errUncovered
}

func (store *fakeStore) MarkUsed(context.Context, string, string, time.Time) error {
	return errUncovered
}

func (store *fakeStore) AcquireLease(context.Context, string, string, int64, string, time.Time, time.Time) (bool, error) {
	return false, errUncovered
}

func (store *fakeStore) SaveRenewedCredentials(context.Context, string, string, string, int64, *domain.SealedData, []string, time.Time) (int64, error) {
	return 0, errUncovered
}

func (store *fakeStore) ReleaseLease(context.Context, string, string, string) error {
	return errUncovered
}

func (store *fakeStore) AddHold(context.Context, string, string, string, time.Time) error {
	return errUncovered
}

func (store *fakeStore) ReleaseHold(context.Context, string, string, string) error {
	return errUncovered
}

func (store *fakeStore) CountHolds(context.Context, string, string) (int, error) {
	return 0, errUncovered
}

func (store *fakeStore) ReportConnection(context.Context, string, string, time.Time) error {
	return errUncovered
}

func (store *fakeStore) ClaimHealthChecks(context.Context, time.Time, int, string, time.Time) ([]domain.HealthCheck, error) {
	return nil, errUncovered
}

func (store *fakeStore) RecordHealthCheck(context.Context, domain.HealthCheck, string, domain.HealthResult, *domain.Event) error {
	return errUncovered
}

func (store *fakeStore) ClaimCredentialDeletions(context.Context, time.Time, int, string, time.Time) ([]domain.Connection, error) {
	return nil, errUncovered
}

func (store *fakeStore) DeleteCredentials(context.Context, string, string, string, time.Time, domain.Event) error {
	return errUncovered
}

func (store *fakeStore) SharesProviderGrant(context.Context, string, string, string, string) (bool, error) {
	return false, nil
}

func (store *fakeStore) WithProviderGrantLock(ctx context.Context, _, _, _ string, fn func(context.Context) error) error {
	return fn(ctx)
}
