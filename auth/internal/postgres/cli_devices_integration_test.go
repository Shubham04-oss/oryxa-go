package postgres

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/Shubham04-oss/oryxa-go/auth/internal/clisession"
	"github.com/Shubham04-oss/oryxa-go/auth/internal/domain"
)

type testDevice struct {
	clisession.Device
	deviceCode, userCode string
}

func createTestDevice(t *testing.T, store *Store, interval time.Duration) testDevice {
	t.Helper()
	ctx := context.Background()
	device := testDevice{deviceCode: clisession.NewDeviceCode(), userCode: clisession.NewUserCode()}
	device.Device = clisession.Device{
		DeviceCodeHash: clisession.Hash(device.deviceCode), UserCodeHash: clisession.Hash(device.userCode),
		ClientName: "oryxa-cli on test", ClientAddress: "192.0.2.1",
		ExpiresAt: time.Now().Add(clisession.DeviceTTL), Interval: interval,
	}
	t.Cleanup(func() {
		_, _ = store.pool.Exec(ctx, `DELETE FROM auth_cli_devices WHERE device_code_hash=$1`, device.DeviceCodeHash)
	})
	if err := store.CreateCLIDevice(ctx, device.Device); err != nil {
		t.Fatal(err)
	}
	return device
}

func testTokenHash(t *testing.T, store *Store) string {
	t.Helper()
	hash := clisession.Hash(clisession.NewDeviceToken())
	t.Cleanup(func() {
		_, _ = store.pool.Exec(context.Background(), `DELETE FROM auth_cli_sessions WHERE token_hash=$1`, hash)
	})
	return hash
}

func TestCLIDeviceStoresOnlyHashesAndShowsThePendingRequest(t *testing.T) {
	store := cliStore(t)
	ctx := context.Background()
	device := createTestDevice(t, store, 5*time.Second)
	var stored int
	if err := store.pool.QueryRow(ctx, `SELECT count(*) FROM auth_cli_devices WHERE device_code_hash=$1 OR user_code_hash=$1 OR device_code_hash=$2 OR user_code_hash=$2`, device.deviceCode, device.userCode).Scan(&stored); err != nil || stored != 0 {
		t.Fatal("raw code stored", stored, err)
	}
	got, err := store.CLIDevice(ctx, device.UserCodeHash)
	if err != nil || got.ClientName != "oryxa-cli on test" || got.ClientAddress != "192.0.2.1" || got.CreatedAt.IsZero() || got.Interval != 5*time.Second {
		t.Fatal(got, err)
	}
	if _, err := store.CLIDevice(ctx, clisession.Hash("CDFG-HJKM")); !errors.Is(err, domain.ErrNotFound) {
		t.Fatal(err)
	}
}

func TestCLIDevicePollsPendingThenSlowsDownTooFastPollers(t *testing.T) {
	store := cliStore(t)
	ctx := context.Background()
	device := createTestDevice(t, store, 5*time.Second)
	token := testTokenHash(t, store)
	poll, err := store.PollCLIDevice(ctx, device.DeviceCodeHash, token, time.Now().Add(time.Minute))
	if err != nil || poll.Status != clisession.PollPending || poll.Interval != 5*time.Second {
		t.Fatal(poll, err)
	}
	poll, err = store.PollCLIDevice(ctx, device.DeviceCodeHash, token, time.Now().Add(time.Minute))
	if err != nil || poll.Status != clisession.PollSlowDown || poll.Interval != 10*time.Second {
		t.Fatal(poll, err)
	}
	// Once the interval has passed, polling is pending again.
	if _, err := store.pool.Exec(ctx, `UPDATE auth_cli_devices SET last_polled_at=now()-interval '11 seconds' WHERE device_code_hash=$1`, device.DeviceCodeHash); err != nil {
		t.Fatal(err)
	}
	if poll, err = store.PollCLIDevice(ctx, device.DeviceCodeHash, token, time.Now().Add(time.Minute)); err != nil || poll.Status != clisession.PollPending {
		t.Fatal(poll, err)
	}
	if _, err := store.CLISession(ctx, token); !errors.Is(err, domain.ErrUnauthorized) {
		t.Fatal("pending poll admitted a session", err)
	}
}

func TestCLIDeviceApprovalAdmitsOneSessionForTheApprover(t *testing.T) {
	store := cliStore(t)
	ctx := context.Background()
	device := createTestDevice(t, store, 5*time.Second)
	if err := store.DecideCLIDevice(ctx, device.UserCodeHash, "", true); err == nil {
		t.Fatal("approved without a member")
	}
	if err := store.DecideCLIDevice(ctx, device.UserCodeHash, "member-1", true); err != nil {
		t.Fatal(err)
	}
	if err := store.DecideCLIDevice(ctx, device.UserCodeHash, "member-2", true); !errors.Is(err, domain.ErrNotFound) {
		t.Fatal("decided twice", err)
	}
	if _, err := store.CLIDevice(ctx, device.UserCodeHash); !errors.Is(err, domain.ErrNotFound) {
		t.Fatal("decided request still shown", err)
	}
	// Two racing polls: exactly one gets the session.
	tokens := []string{testTokenHash(t, store), testTokenHash(t, store)}
	var wg sync.WaitGroup
	polls := make([]clisession.Poll, 2)
	for i := range 2 {
		wg.Go(func() {
			polls[i], _ = store.PollCLIDevice(ctx, device.DeviceCodeHash, tokens[i], time.Now().Add(clisession.SessionTTL))
		})
	}
	wg.Wait()
	admitted := 0
	for i, poll := range polls {
		if poll.Status == "" {
			admitted++
			session, err := store.CLISession(ctx, tokens[i])
			if err != nil || session.Subject != "member-1" || session.OrganizationID != "" || session.ExpiresAt.After(time.Now().Add(clisession.SessionTTL)) {
				t.Fatal(session, err)
			}
		} else if poll.Status != clisession.PollExpired {
			t.Fatal("racing poll", poll)
		}
	}
	if admitted != 1 {
		t.Fatal("admitted", admitted)
	}
	// The issued session binds to one organization, like a browser one.
	admittedToken := tokens[0]
	if polls[1].Status == "" {
		admittedToken = tokens[1]
	}
	if err := store.BindCLISession(ctx, admittedToken, "org_1"); err != nil {
		t.Fatal(err)
	}
	if err := store.BindCLISession(ctx, admittedToken, "org_2"); !errors.Is(err, domain.ErrForbidden) {
		t.Fatal("rebound to another organization", err)
	}
}

func TestCLIDeviceDenialAndExpiryAreFinal(t *testing.T) {
	store := cliStore(t)
	ctx := context.Background()
	denied := createTestDevice(t, store, 5*time.Second)
	if err := store.DecideCLIDevice(ctx, denied.UserCodeHash, "member-1", false); err != nil {
		t.Fatal(err)
	}
	token := testTokenHash(t, store)
	for _, want := range []string{clisession.PollDenied, clisession.PollExpired} {
		if poll, err := store.PollCLIDevice(ctx, denied.DeviceCodeHash, token, time.Now().Add(time.Minute)); err != nil || poll.Status != want {
			t.Fatal(want, poll, err)
		}
	}
	expired := createTestDevice(t, store, 5*time.Second)
	if _, err := store.pool.Exec(ctx, `UPDATE auth_cli_devices SET expires_at=now()-interval '1 second' WHERE device_code_hash=$1`, expired.DeviceCodeHash); err != nil {
		t.Fatal(err)
	}
	if err := store.DecideCLIDevice(ctx, expired.UserCodeHash, "member-1", true); !errors.Is(err, domain.ErrNotFound) {
		t.Fatal("approved an expired request", err)
	}
	if poll, err := store.PollCLIDevice(ctx, expired.DeviceCodeHash, token, time.Now().Add(time.Minute)); err != nil || poll.Status != clisession.PollExpired {
		t.Fatal(poll, err)
	}
	if _, err := store.CLISession(ctx, token); !errors.Is(err, domain.ErrUnauthorized) {
		t.Fatal("denied or expired request admitted a session", err)
	}
	if err := store.PurgeCLISessions(ctx); err != nil {
		t.Fatal(err)
	}
}
