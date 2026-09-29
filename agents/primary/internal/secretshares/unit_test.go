package secretshares

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"maps"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/kms/types"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/httpserver"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/interactions"
	"github.com/Shubham04-oss/oryxa-go/internal/postgreskit"
	"github.com/Shubham04-oss/oryxa-go/internal/secrets"
)

// What the rest of the service holds a Store to: the route's store of secrets,
// a worker that runs until it is stopped, and (the shape of inbound.Forgetter)
// the forgetter that runs when a member leaves or an organization is deleted.
// A change to any of them fails here, not in the wiring.
var (
	_ httpserver.SecretShares                     = (*Store)(nil)
	_ Sealer                                      = secrets.Envelope{}
	_ func(context.Context)                       = (*Store)(nil).Run
	_ func(context.Context, string, string) error = (*Store)(nil).Forget
)

var someShare = interactions.SecretShare{
	RequestID: "irq_1", OrganizationID: "org_1", AskID: "iak_1", OwnerUserID: "user_owner", AskerUserID: "user_asker",
}

// countingSealer fails the test if it is asked to seal or open anything.
type countingSealer struct {
	t  *testing.T
	mu sync.Mutex
	n  int
}

func (sealer *countingSealer) Seal(context.Context, secrets.Binding, []byte) (secrets.SealedData, error) {
	sealer.mu.Lock()
	sealer.n++
	sealer.mu.Unlock()
	return secrets.SealedData{}, errors.New("nothing should have been sealed")
}

func (sealer *countingSealer) Open(context.Context, secrets.Binding, secrets.SealedData) ([]byte, error) {
	sealer.mu.Lock()
	sealer.n++
	sealer.mu.Unlock()
	return nil, errors.New("nothing should have been opened")
}

func (sealer *countingSealer) calls() int {
	sealer.mu.Lock()
	defer sealer.mu.Unlock()
	return sealer.n
}

// detachedStore is a store whose database never answers: a call that reaches it
// fails, so a test that expects a refusal first shows it never got that far.
func detachedStore(t *testing.T, sealer Sealer) *Store {
	t.Helper()
	database := postgreskit.Detached(nil)
	t.Cleanup(database.Close)
	store, err := New(database, sealer, nil)
	if err != nil {
		t.Fatal(err)
	}
	return store
}

// TestPutRefusesWhatTheLedgerWouldNotBeforeSealingAnything: the ledger bounds
// the value and its lifetime before it calls the store, and the store does not
// rely on it. A refusal comes before KMS is asked for a key and before the
// database is used.
func TestPutRefusesWhatTheLedgerWouldNotBeforeSealingAnything(t *testing.T) {
	sealer := &countingSealer{t: t}
	store := detachedStore(t, sealer)
	ctx := context.Background()

	wholeShare := someShare
	noRequest, noOrganization, noOwner, noAsker := someShare, someShare, someShare, someShare
	noRequest.RequestID, noOrganization.OrganizationID, noOwner.OwnerUserID, noAsker.AskerUserID = "", "", "", ""

	for name, test := range map[string]struct {
		share   interactions.SecretShare
		value   string
		ttl     time.Duration
		invalid bool
	}{
		"no value":             {wholeShare, "", time.Minute, true},
		"a value of spaces":    {wholeShare, " \n\t ", time.Minute, true},
		"a value too long":     {wholeShare, strings.Repeat("k", interactions.MaxSecretRunes+1), time.Minute, true},
		"no lifetime":          {wholeShare, "sk-live-value", 0, true},
		"a negative lifetime":  {wholeShare, "sk-live-value", -time.Minute, true},
		"a lifetime over hour": {wholeShare, "sk-live-value", interactions.MaxSecretTTL + time.Nanosecond, true},
		"a day":                {wholeShare, "sk-live-value", 24 * time.Hour, true},
		"no request":           {noRequest, "sk-live-value", time.Minute, false},
		"no organization":      {noOrganization, "sk-live-value", time.Minute, false},
		"no owner":             {noOwner, "sk-live-value", time.Minute, false},
		"no asker":             {noAsker, "sk-live-value", time.Minute, false},
	} {
		_, err := store.Put(ctx, test.share, test.value, test.ttl)
		if err == nil {
			t.Errorf("%s: Put succeeded", name)
			continue
		}
		if got := errors.Is(err, interactions.ErrInvalid); got != test.invalid {
			t.Errorf("%s: ErrInvalid = %v, want %v (%v)", name, got, test.invalid, err)
		}
		if strings.Contains(err.Error(), "sk-live-value") {
			t.Errorf("%s: the error quotes the value: %v", name, err)
		}
	}
	if sealer.calls() != 0 {
		t.Errorf("%d values were sealed or opened for calls that had to be refused", sealer.calls())
	}

	// A view, a revoke and a sweep are refused for what they are given, the
	// same way.
	if _, err := store.View(ctx, noAsker); err == nil {
		t.Error("View of a share that names no asker succeeded")
	}
	if err := store.Revoke(ctx, noOwner); err == nil {
		t.Error("Revoke of a share that names no owner succeeded")
	}
	if _, err := store.Sweep(ctx, 0); err == nil {
		t.Error("a sweep with no limit succeeded")
	}
	if err := store.Forget(ctx, "  ", "user_1"); err == nil {
		t.Error("forgetting with no organization succeeded")
	}
	if sealer.calls() != 0 {
		t.Errorf("%d values were sealed or opened by refused calls", sealer.calls())
	}
}

func TestNewNeedsADatabaseAndASealer(t *testing.T) {
	if _, err := New(nil, &countingSealer{t: t}, nil); err == nil {
		t.Error("a store without a database")
	}
	database := postgreskit.Detached(nil)
	t.Cleanup(database.Close)
	if _, err := New(database, nil, nil); err == nil {
		t.Error("a store without a sealer")
	}
}

// TestSecretsAreSealedForTheAgentsServiceAndTheirRequest: the KMS encryption
// context is service=oryxa-agents, which is what the task role's grant on the
// agents-secrets key is conditioned on, with the resource type and the request;
// and the value's encryption is bound to the request as well, so the same
// ciphertext does not open for another.
func TestSecretsAreSealedForTheAgentsServiceAndTheirRequest(t *testing.T) {
	api := &fakeKMSAPI{dataKey: bytes.Repeat([]byte{7}, 32), wrappedKey: []byte("wrapped-data-key")}
	sealer, err := NewSealer(api, "  alias/oryxa-production-agents-secrets ")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	want := map[string]string{"service": "oryxa-agents", "resource_type": "secret_share", "resource_id": "irq_42"}

	sealed, err := sealer.Seal(ctx, bindingOf("irq_42"), []byte("s3cret-value"))
	if err != nil {
		t.Fatal(err)
	}
	if len(api.generate) != 1 {
		t.Fatalf("%d data keys were asked for", len(api.generate))
	}
	input := api.generate[0]
	if input.KeyId == nil || *input.KeyId != "alias/oryxa-production-agents-secrets" || input.KeySpec != types.DataKeySpecAes256 {
		t.Errorf("data key asked of %v as %v", input.KeyId, input.KeySpec)
	}
	if !maps.Equal(input.EncryptionContext, want) {
		t.Errorf("encryption context = %v, want %v", input.EncryptionContext, want)
	}
	if bytes.Contains(sealed.Ciphertext, []byte("s3cret-value")) {
		t.Error("the ciphertext holds the value")
	}

	opened, err := sealer.Open(ctx, bindingOf("irq_42"), sealed)
	if err != nil || string(opened) != "s3cret-value" {
		t.Fatalf("opened %q, %v", opened, err)
	}
	if len(api.decrypt) != 1 || !maps.Equal(api.decrypt[0].EncryptionContext, want) {
		t.Errorf("decrypted under %+v, want the context it was sealed under", api.decrypt)
	}
	// KMS is not the only thing that ties it to its request: the associated
	// data of the encryption is the same context.
	if _, err := sealer.Open(ctx, bindingOf("irq_43"), sealed); !errors.Is(err, secrets.ErrInvalidEnvelope) {
		t.Errorf("a value sealed for one request opened for another: %v", err)
	}
}

func TestNewSealerNeedsAClientAndAKey(t *testing.T) {
	if _, err := NewSealer(nil, "alias/x"); err == nil {
		t.Error("a sealer without a KMS client")
	}
	if _, err := NewSealer(&fakeKMSAPI{}, "   "); err == nil {
		t.Error("a sealer without a key")
	}
}

// TestTheTableAndTheLedgerAgreeOnALifetime: the table refuses a secret that
// lives longer than the ledger allows. If the ledger's longest lifetime moves,
// migration 001's check has to move with it, in a migration of its own.
func TestTheTableAndTheLedgerAgreeOnALifetime(t *testing.T) {
	if interactions.MaxSecretTTL != time.Hour {
		t.Fatalf("interactions.MaxSecretTTL is %s: change the check in migrations/001_secret_shares.sql, and add a migration to change it where it has run", interactions.MaxSecretTTL)
	}
	contents, err := migrations.ReadFile("migrations/001_secret_shares.sql")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(contents), "expires_at <= created_at + interval '1 hour'") {
		t.Error("migration 001 does not hold a secret to an hour")
	}
}

// syncBuffer is a log destination that several goroutines may write.
type syncBuffer struct {
	mu     sync.Mutex
	buffer bytes.Buffer
}

func (buffer *syncBuffer) Write(data []byte) (int, error) {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	return buffer.buffer.Write(data)
}

func (buffer *syncBuffer) String() string {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	return buffer.buffer.String()
}

func TestTheSweeperSweepsAtOnceAndDrainsWholeBatches(t *testing.T) {
	logs := &syncBuffer{}
	logger := slog.New(slog.NewTextHandler(logs, nil))
	results := []int{5, 5, 2}
	var (
		mu      sync.Mutex
		batches []int
	)
	done := make(chan struct{})
	sweep := func(_ context.Context, batch int) (int, error) {
		mu.Lock()
		defer mu.Unlock()
		batches = append(batches, batch)
		if len(batches) == len(results) {
			defer close(done)
		}
		return results[len(batches)-1], nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	stopped := make(chan struct{})
	go func() {
		// The interval is an hour: what runs, runs before the first tick.
		sweepLoop(ctx, time.Hour, 5, sweep, logger)
		close(stopped)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the sweeper did not sweep before its first tick")
	}
	cancel()
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("the sweeper did not stop with its context")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(batches) != 3 || batches[0] != 5 {
		t.Errorf("sweeps = %v, want three of a batch of 5", batches)
	}
	if !strings.Contains(logs.String(), "deleted=12") {
		t.Errorf("the log does not say twelve were deleted: %s", logs.String())
	}
}

func TestTheSweeperTriesAgainOnTheNextTickAfterAFailure(t *testing.T) {
	logs := &syncBuffer{}
	logger := slog.New(slog.NewTextHandler(logs, nil))
	var (
		mu    sync.Mutex
		calls int
	)
	sweep := func(context.Context, int) (int, error) {
		mu.Lock()
		defer mu.Unlock()
		calls++
		return 0, errors.New("the database is away")
	}
	ctx, cancel := context.WithCancel(context.Background())
	stopped := make(chan struct{})
	go func() {
		sweepLoop(ctx, 20*time.Millisecond, 5, sweep, logger)
		close(stopped)
	}()
	time.Sleep(230 * time.Millisecond)
	cancel()
	<-stopped
	mu.Lock()
	defer mu.Unlock()
	// One at once and one a tick, not a tight loop of them.
	if calls < 3 || calls > 14 {
		t.Errorf("%d sweeps in about ten ticks of a failing database", calls)
	}
	if got := strings.Count(logs.String(), "the database is away"); got != calls {
		t.Errorf("%d failures logged for %d sweeps", got, calls)
	}
}
