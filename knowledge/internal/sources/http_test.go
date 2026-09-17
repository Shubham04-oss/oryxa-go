package sources

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Shubham04-oss/oryxa-go/knowledge/internal/domain"
)

// fakeProvider answers a scripted sequence, recording what it was asked.
type fakeProvider struct {
	answers  []*http.Response
	failures []error
	calls    []*http.Request
}

func (provider *fakeProvider) Do(request *http.Request) (*http.Response, error) {
	provider.calls = append(provider.calls, request)
	index := len(provider.calls) - 1
	if index < len(provider.failures) && provider.failures[index] != nil {
		return nil, provider.failures[index]
	}
	if index >= len(provider.answers) {
		return nil, fmt.Errorf("the test scripted no answer for call %d", index+1)
	}
	return provider.answers[index], nil
}

func answer(status int, body string, header http.Header) *http.Response {
	if header == nil {
		header = http.Header{}
	}
	return &http.Response{StatusCode: status, Header: header, Body: io.NopCloser(strings.NewReader(body))}
}

// testClient replaces the clock and the sleeping, so a test that exercises
// backoff does not wait for it.
func testClient(t *testing.T, provider Doer) (*Client, *[]time.Duration) {
	t.Helper()
	client := newClient(provider)
	now := time.Date(2026, time.September, 17, 12, 0, 0, 0, time.UTC)
	client.clock = func() time.Time { return now }
	slept := []time.Duration{}
	client.sleep = func(_ context.Context, delay time.Duration) error {
		if delay > 0 {
			slept = append(slept, delay)
		}
		return nil
	}
	return client, &slept
}

func request(t *testing.T) *http.Request {
	t.Helper()
	built, err := http.NewRequest(http.MethodGet, "https://api.example.test/issues", nil)
	if err != nil {
		t.Fatal(err)
	}
	return built
}

func TestDoPlacesTheCredentialWhereConnectionsSaid(t *testing.T) {
	provider := &fakeProvider{answers: []*http.Response{answer(http.StatusOK, "{}", nil)}}
	client, _ := testClient(t, provider)

	if _, err := client.Do(context.Background(), request(t), domain.Credential{
		AccessToken: "secret", In: "header", Name: "Authorization", Prefix: "Bearer ",
	}); err != nil {
		t.Fatalf("Do() error = %v", err)
	}
	if got := provider.calls[0].Header.Get("Authorization"); got != "Bearer secret" {
		t.Fatalf("Authorization = %q, want %q", got, "Bearer secret")
	}
}

func TestDoPlacesACredentialThatGoesInTheQuery(t *testing.T) {
	provider := &fakeProvider{answers: []*http.Response{answer(http.StatusOK, "{}", nil)}}
	client, _ := testClient(t, provider)

	if _, err := client.Do(context.Background(), request(t), domain.Credential{
		AccessToken: "secret", In: "query", Name: "token",
	}); err != nil {
		t.Fatalf("Do() error = %v", err)
	}
	if got := provider.calls[0].URL.Query().Get("token"); got != "secret" {
		t.Fatalf("token = %q, want %q", got, "secret")
	}
	if provider.calls[0].Header.Get("Authorization") != "" {
		t.Fatal("a query credential must not also be sent as a header")
	}
}

func TestDoWaitsAsLongAsTheProviderAsked(t *testing.T) {
	provider := &fakeProvider{answers: []*http.Response{
		answer(http.StatusTooManyRequests, "", http.Header{"Retry-After": {"7"}}),
		answer(http.StatusOK, "{}", nil),
	}}
	client, slept := testClient(t, provider)

	if _, err := client.Do(context.Background(), request(t), domain.Credential{}); err != nil {
		t.Fatalf("Do() error = %v", err)
	}
	if len(provider.calls) != 2 {
		t.Fatalf("made %d calls, want 2", len(provider.calls))
	}
	// The wait carries jitter, so it lands in a band rather than on a value.
	waited := (*slept)[0]
	if waited < 7*time.Second || waited > 7*time.Second+2*time.Second {
		t.Fatalf("waited %s, want about the 7 seconds the provider asked for", waited)
	}
}

func TestDoReadsGitHubsResetHeaderWhenItRefusesWithoutRetryAfter(t *testing.T) {
	provider := &fakeProvider{answers: []*http.Response{
		answer(http.StatusForbidden, "", http.Header{
			"X-Ratelimit-Remaining": {"0"},
			"X-Ratelimit-Reset":     {strconv.FormatInt(time.Date(2026, time.September, 17, 12, 0, 30, 0, time.UTC).Unix(), 10)},
		}),
		answer(http.StatusOK, "{}", nil),
	}}
	client, slept := testClient(t, provider)

	if _, err := client.Do(context.Background(), request(t), domain.Credential{}); err != nil {
		t.Fatalf("Do() error = %v", err)
	}
	if len(*slept) == 0 {
		t.Fatal("a rate-limited call waited for nothing")
	}
	if (*slept)[0] < 30*time.Second {
		t.Fatalf("waited %s, want at least the 30 seconds until the budget refills", (*slept)[0])
	}
}

func TestDoTreatsAnExhaustedForbiddenApartFromARefusedOne(t *testing.T) {
	provider := &fakeProvider{answers: []*http.Response{answer(http.StatusForbidden, "", nil)}}
	client, _ := testClient(t, provider)

	_, err := client.Do(context.Background(), request(t), domain.Credential{})
	if !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("Do() error = %v, want ErrForbidden", err)
	}
	if len(provider.calls) != 1 {
		t.Fatalf("made %d calls, want 1: a refusal is not worth retrying", len(provider.calls))
	}
}

func TestDoGivesUpRatherThanSleepingThroughALongPause(t *testing.T) {
	provider := &fakeProvider{answers: []*http.Response{
		answer(http.StatusTooManyRequests, "", http.Header{"Retry-After": {"3600"}}),
	}}
	client, slept := testClient(t, provider)

	_, err := client.Do(context.Background(), request(t), domain.Credential{})
	if !errors.Is(err, domain.ErrRateLimited) {
		t.Fatalf("Do() error = %v, want ErrRateLimited", err)
	}
	if len(provider.calls) != 1 {
		t.Fatalf("made %d calls, want 1: an hour's pause is for the next run", len(provider.calls))
	}
	for _, waited := range *slept {
		if waited > maxBackoff {
			t.Fatalf("waited %s, want no single wait past %s", waited, maxBackoff)
		}
	}
}

func TestDoRetriesAProviderThatWobbled(t *testing.T) {
	provider := &fakeProvider{answers: []*http.Response{
		answer(http.StatusBadGateway, "", nil),
		answer(http.StatusServiceUnavailable, "", nil),
		answer(http.StatusOK, "{}", nil),
	}}
	client, _ := testClient(t, provider)

	response, err := client.Do(context.Background(), request(t), domain.Credential{})
	if err != nil {
		t.Fatalf("Do() error = %v", err)
	}
	if response.Status != http.StatusOK {
		t.Fatalf("Do() status = %d, want 200", response.Status)
	}
	if len(provider.calls) != 3 {
		t.Fatalf("made %d calls, want 3", len(provider.calls))
	}
}

func TestDoStopsAfterTheLastAttempt(t *testing.T) {
	provider := &fakeProvider{answers: []*http.Response{
		answer(http.StatusBadGateway, "", nil),
		answer(http.StatusBadGateway, "", nil),
		answer(http.StatusBadGateway, "", nil),
		answer(http.StatusBadGateway, "", nil),
		answer(http.StatusBadGateway, "", nil),
	}}
	client, _ := testClient(t, provider)

	_, err := client.Do(context.Background(), request(t), domain.Credential{})
	if !errors.Is(err, domain.ErrProviderUnavailable) {
		t.Fatalf("Do() error = %v, want ErrProviderUnavailable", err)
	}
	if len(provider.calls) != maxAttempts {
		t.Fatalf("made %d calls, want %d", len(provider.calls), maxAttempts)
	}
}

func TestDoDoesNotRetryWhatWillNotChange(t *testing.T) {
	for status, want := range map[int]error{
		http.StatusUnauthorized: domain.ErrNeedsReconnect,
		http.StatusNotFound:     domain.ErrNotFound,
		http.StatusBadRequest:   domain.ErrInvalid,
	} {
		provider := &fakeProvider{answers: []*http.Response{answer(status, "", nil)}}
		client, _ := testClient(t, provider)
		_, err := client.Do(context.Background(), request(t), domain.Credential{})
		if !errors.Is(err, want) {
			t.Fatalf("Do(%d) error = %v, want %v", status, err, want)
		}
		if len(provider.calls) != 1 {
			t.Fatalf("Do(%d) made %d calls, want 1", status, len(provider.calls))
		}
	}
}

func TestDoRefusesAnAnswerTooBigToHold(t *testing.T) {
	provider := &fakeProvider{answers: []*http.Response{
		answer(http.StatusOK, strings.Repeat("x", maxResponseBytes+1), nil),
	}}
	client, _ := testClient(t, provider)

	if _, err := client.Do(context.Background(), request(t), domain.Credential{}); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("Do() error = %v, want ErrInvalid", err)
	}
}

func TestDoStopsWhenTheRunWasCancelled(t *testing.T) {
	provider := &fakeProvider{failures: []error{context.Canceled}}
	client, _ := testClient(t, provider)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := client.Do(ctx, request(t), domain.Credential{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("Do() error = %v, want context.Canceled", err)
	}
}

func TestDoPacesCallsToOneHost(t *testing.T) {
	provider := &fakeProvider{answers: []*http.Response{
		answer(http.StatusOK, "{}", nil),
		answer(http.StatusOK, "{}", nil),
	}}
	client, slept := testClient(t, provider)

	for range 2 {
		if _, err := client.Do(context.Background(), request(t), domain.Credential{}); err != nil {
			t.Fatalf("Do() error = %v", err)
		}
	}
	// The clock is frozen, so the second call must be held for the full pace.
	if len(*slept) != 1 || (*slept)[0] != pace {
		t.Fatalf("waits = %v, want one wait of %s between calls to a host", *slept, pace)
	}
}

func TestLimitStaysInsideWhatAProviderAnswers(t *testing.T) {
	if got := Limit(0, 100); got != DefaultLimit {
		t.Fatalf("Limit(unset) = %d, want %d", got, DefaultLimit)
	}
	if got := Limit(500, 100); got != 100 {
		t.Fatalf("Limit(over the maximum) = %d, want 100", got)
	}
	if got := Limit(50, 100); got != 50 {
		t.Fatalf("Limit(under the maximum) = %d, want 50", got)
	}
	if got := Limit(500, 0); got != 500 {
		t.Fatalf("Limit(no maximum) = %d, want 500", got)
	}
}
