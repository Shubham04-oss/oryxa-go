package sources

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/Shubham04-oss/oryxa-go/internal/composio"

	"github.com/Shubham04-oss/oryxa-go/knowledge/internal/domain"
)

// fakeProxier answers a scripted sequence of Proxy calls, recording what it
// was asked.
type fakeProxier struct {
	responses []composio.ProxyResponse
	errs      []error
	calls     []composio.ProxyRequest
}

func (proxy *fakeProxier) Proxy(_ context.Context, request composio.ProxyRequest) (composio.ProxyResponse, error) {
	index := len(proxy.calls)
	proxy.calls = append(proxy.calls, request)
	if index < len(proxy.errs) && proxy.errs[index] != nil {
		return composio.ProxyResponse{}, proxy.errs[index]
	}
	if index >= len(proxy.responses) {
		return composio.ProxyResponse{}, errors.New("the test scripted no response for this call")
	}
	return proxy.responses[index], nil
}

func composioCredential(accountID string) domain.Credential {
	return domain.Credential{Metadata: map[string]string{"composio_account_id": accountID}}
}

func TestComposioDoerCallsThroughTheConnectedAccountOnTheRequest(t *testing.T) {
	proxy := &fakeProxier{responses: []composio.ProxyResponse{
		{Status: http.StatusOK, Data: json.RawMessage(`{"ok":true}`)},
	}}
	client, _ := testClient(t, &composioDoer{proxy: proxy})

	httpRequest, err := http.NewRequest(http.MethodGet, "https://api.github.com/repos/acme/api/issues?state=all", nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := client.Do(context.Background(), httpRequest, composioCredential("ca_1"))
	if err != nil {
		t.Fatalf("Do() error = %v", err)
	}
	if string(response.Body) != `{"ok":true}` {
		t.Fatalf("Do() body = %s", response.Body)
	}
	if len(proxy.calls) != 1 {
		t.Fatalf("made %d proxy calls, want 1", len(proxy.calls))
	}
	call := proxy.calls[0]
	if call.ConnectedAccountID != "ca_1" {
		t.Fatalf("ConnectedAccountID = %q, want ca_1", call.ConnectedAccountID)
	}
	if call.Endpoint != "https://api.github.com/repos/acme/api/issues?state=all" {
		t.Fatalf("Endpoint = %q, want the absolute URL with its query", call.Endpoint)
	}
	if call.Method != http.MethodGet {
		t.Fatalf("Method = %q, want GET", call.Method)
	}
}

func TestComposioDoerCarriesProviderHeadersAsParametersNeverAuthorization(t *testing.T) {
	proxy := &fakeProxier{responses: []composio.ProxyResponse{{Status: http.StatusOK}}}
	client, _ := testClient(t, &composioDoer{proxy: proxy})

	httpRequest, err := http.NewRequest(http.MethodGet, "https://api.notion.com/v1/search", nil)
	if err != nil {
		t.Fatal(err)
	}
	httpRequest.Header.Set("Notion-Version", "2025-09-03")
	httpRequest.Header.Set("Accept", "application/json")
	// Never set in practice, but guarded against regardless: Composio places
	// the credential itself, from the connected account named on the call.
	httpRequest.Header.Set("Authorization", "Bearer should-never-travel")

	if _, err := client.Do(context.Background(), httpRequest, composioCredential("ca_1")); err != nil {
		t.Fatalf("Do() error = %v", err)
	}
	parameters := proxy.calls[0].Parameters
	seen := map[string]string{}
	for _, parameter := range parameters {
		if parameter.Type != "header" {
			t.Fatalf("parameter %+v is not a header parameter", parameter)
		}
		seen[parameter.Name] = parameter.Value
	}
	if seen["Notion-Version"] != "2025-09-03" {
		t.Fatalf("Notion-Version = %q, want 2025-09-03", seen["Notion-Version"])
	}
	if seen["Accept"] != "application/json" {
		t.Fatalf("Accept = %q, want application/json", seen["Accept"])
	}
	if _, carried := seen["Authorization"]; carried {
		t.Fatal("an Authorization header must never be carried to Composio's proxy")
	}
}

func TestComposioDoerCarriesTheRequestBody(t *testing.T) {
	proxy := &fakeProxier{responses: []composio.ProxyResponse{{Status: http.StatusOK}}}
	client, _ := testClient(t, &composioDoer{proxy: proxy})

	httpRequest, err := JSONRequest(context.Background(), http.MethodPost, "https://api.notion.com/v1/search", []byte(`{"page_size":100}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Do(context.Background(), httpRequest, composioCredential("ca_1")); err != nil {
		t.Fatalf("Do() error = %v", err)
	}
	if string(proxy.calls[0].Body) != `{"page_size":100}` {
		t.Fatalf("Body = %s, want the request's own JSON body", proxy.calls[0].Body)
	}
}

func TestComposioDoerRefusesASourceWithNoAccount(t *testing.T) {
	proxy := &fakeProxier{}
	client, _ := testClient(t, &composioDoer{proxy: proxy})

	_, err := client.Do(context.Background(), request(t), domain.Credential{})
	if !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("Do() error = %v, want ErrForbidden", err)
	}
	if len(proxy.calls) != 0 {
		t.Fatal("a missing account must never reach Composio")
	}
}

func TestComposioDoerMapsABinaryAnswerToUnsupportedType(t *testing.T) {
	// A file answer is not a status code, so the retrying transport has
	// nothing to recognise as permanent and retries it like any other
	// error — every attempt is scripted the same rather than just the
	// first, and the mapping must still survive to the caller once they
	// are exhausted.
	binary := composio.ProxyResponse{Status: http.StatusOK, Binary: &composio.BinaryData{URL: "https://files.example.test/1", ContentType: "application/pdf"}}
	responses := make([]composio.ProxyResponse, maxAttempts)
	for i := range responses {
		responses[i] = binary
	}
	proxy := &fakeProxier{responses: responses}
	client, _ := testClient(t, &composioDoer{proxy: proxy})

	_, err := client.Do(context.Background(), request(t), composioCredential("ca_1"))
	if !errors.Is(err, domain.ErrUnsupportedType) {
		t.Fatalf("Do() error = %v, want ErrUnsupportedType", err)
	}
	if len(proxy.calls) != maxAttempts {
		t.Fatalf("made %d calls, want %d", len(proxy.calls), maxAttempts)
	}
}

func TestComposioDoerMapsRateLimitedToASyntheticRetryAfter(t *testing.T) {
	proxy := &fakeProxier{errs: []error{
		&composio.Error{Status: http.StatusTooManyRequests, RetryAfter: 5 * time.Second},
		nil,
	}, responses: []composio.ProxyResponse{{}, {Status: http.StatusOK}}}
	client, slept := testClient(t, &composioDoer{proxy: proxy})

	if _, err := client.Do(context.Background(), request(t), composioCredential("ca_1")); err != nil {
		t.Fatalf("Do() error = %v", err)
	}
	if len(proxy.calls) != 2 {
		t.Fatalf("made %d calls, want 2: a rate limit Composio itself reported is worth retrying", len(proxy.calls))
	}
	if len(*slept) == 0 || (*slept)[0] < 5*time.Second {
		t.Fatalf("waited %v, want at least the 5 seconds Composio asked for", *slept)
	}
}

func TestComposioDoerMapsUnavailableToRetryable(t *testing.T) {
	proxy := &fakeProxier{
		errs:      []error{composio.ErrUnavailable, nil},
		responses: []composio.ProxyResponse{{}, {Status: http.StatusOK}},
	}
	client, _ := testClient(t, &composioDoer{proxy: proxy})

	if _, err := client.Do(context.Background(), request(t), composioCredential("ca_1")); err != nil {
		t.Fatalf("Do() error = %v", err)
	}
	if len(proxy.calls) != 2 {
		t.Fatalf("made %d calls, want 2: Composio being unavailable is worth retrying", len(proxy.calls))
	}
}

func TestComposioDoerMapsAccountTroubleToNeedsReconnect(t *testing.T) {
	for _, accountErr := range []error{composio.ErrNotFound, composio.ErrForbidden, composio.ErrUnauthorized} {
		proxy := &fakeProxier{errs: []error{accountErr}}
		client, _ := testClient(t, &composioDoer{proxy: proxy})

		_, err := client.Do(context.Background(), request(t), composioCredential("ca_gone"))
		if !errors.Is(err, domain.ErrNeedsReconnect) {
			t.Fatalf("Do() with %v error = %v, want ErrNeedsReconnect", accountErr, err)
		}
		if len(proxy.calls) != 1 {
			t.Fatalf("made %d calls, want 1: the account will not exist on retry", len(proxy.calls))
		}
	}
}

func TestComposioDoerStopsOnCancellation(t *testing.T) {
	proxy := &fakeProxier{errs: []error{context.Canceled}}
	client, _ := testClient(t, &composioDoer{proxy: proxy})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := client.Do(ctx, request(t), composioCredential("ca_1")); !errors.Is(err, context.Canceled) {
		t.Fatalf("Do() error = %v, want context.Canceled", err)
	}
}
