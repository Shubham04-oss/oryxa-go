package sources

import (
	"bytes"
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

// TestComposioDoerFollowsABinaryLinkWhenAllowed covers Drive's export and
// alt=media downloads: Composio never returns a file's bytes inline, only a
// link to where it stored them, and this is the one case where following
// that link is what the caller actually wants rather than ErrUnsupportedType.
func TestComposioDoerFollowsABinaryLinkWhenAllowed(t *testing.T) {
	proxy := &fakeProxier{responses: []composio.ProxyResponse{
		{Status: http.StatusOK, Binary: &composio.BinaryData{URL: "https://storage.example.test/f1", ContentType: "application/pdf"}},
	}}
	fetched := []string{}
	doer := &composioDoer{proxy: proxy, binaries: func(_ context.Context, url string) ([]byte, error) {
		fetched = append(fetched, url)
		return []byte("%PDF-1.4 fake"), nil
	}}
	client, _ := testClient(t, doer)

	response, err := client.Do(AllowBinary(context.Background()), request(t), composioCredential("ca_1"))
	if err != nil {
		t.Fatalf("Do() error = %v", err)
	}
	if string(response.Body) != "%PDF-1.4 fake" {
		t.Fatalf("Do() body = %s, want the bytes fetched from Composio's own link", response.Body)
	}
	if response.Header.Get("Content-Type") != "application/pdf" {
		t.Fatalf("Content-Type = %q, want the binary's own content type", response.Header.Get("Content-Type"))
	}
	if len(fetched) != 1 || fetched[0] != "https://storage.example.test/f1" {
		t.Fatalf("fetched = %v, want exactly the link Composio answered with", fetched)
	}
}

// TestComposioDoerStillRefusesBinaryWithoutAFetcher covers a doer that was
// told a binary is allowed but has no way to actually get one — every doer
// built through a bare composioDoer{proxy: ...} literal, as every other test
// in this file uses, which must keep refusing exactly as before. As with
// TestComposioDoerMapsABinaryAnswerToUnsupportedType, the refusal is not a
// status the retrying transport recognises as permanent, so every attempt is
// scripted the same.
func TestComposioDoerStillRefusesBinaryWithoutAFetcher(t *testing.T) {
	binary := composio.ProxyResponse{Status: http.StatusOK, Binary: &composio.BinaryData{URL: "https://storage.example.test/f1"}}
	responses := make([]composio.ProxyResponse, maxAttempts)
	for i := range responses {
		responses[i] = binary
	}
	proxy := &fakeProxier{responses: responses}
	client, _ := testClient(t, &composioDoer{proxy: proxy})

	_, err := client.Do(AllowBinary(context.Background()), request(t), composioCredential("ca_1"))
	if !errors.Is(err, domain.ErrUnsupportedType) {
		t.Fatalf("Do() error = %v, want ErrUnsupportedType: AllowBinary alone is not enough without a way to fetch one", err)
	}
}

// TestDoWithLimitBoundsABinaryDownloadSeparatelyFromDo covers Drive's own
// files being allowed up to sources.MaxOriginalBytes even though it is far
// larger than the maxResponseBytes a page of JSON is bounded to.
func TestDoWithLimitBoundsABinaryDownloadSeparatelyFromDo(t *testing.T) {
	big := bytes.Repeat([]byte("a"), maxResponseBytes+1)
	binary := composio.ProxyResponse{Status: http.StatusOK, Binary: &composio.BinaryData{URL: "https://storage.example.test/big"}}
	proxy := &fakeProxier{responses: []composio.ProxyResponse{binary, binary}}
	doer := &composioDoer{proxy: proxy, binaries: func(context.Context, string) ([]byte, error) { return big, nil }}
	client, _ := testClient(t, doer)

	if _, err := client.Do(AllowBinary(context.Background()), request(t), composioCredential("ca_1")); err == nil {
		t.Fatal("Do() accepted a body larger than maxResponseBytes")
	}
	response, err := client.DoWithLimit(AllowBinary(context.Background()), request(t), composioCredential("ca_1"), MaxOriginalBytes)
	if err != nil {
		t.Fatalf("DoWithLimit() error = %v, want the larger ceiling to admit it", err)
	}
	if len(response.Body) != len(big) {
		t.Fatalf("DoWithLimit() body = %d bytes, want %d", len(response.Body), len(big))
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
