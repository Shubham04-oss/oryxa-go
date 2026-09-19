package gcpauth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"cloud.google.com/go/auth"
	"cloud.google.com/go/auth/credentials/externalaccount"
	"github.com/aws/aws-sdk-go-v2/aws"
)

const (
	testProvider       = "//iam.googleapis.com/projects/123456789012/locations/global/workloadIdentityPools/oryxa/providers/aws"
	testServiceAccount = "oryxa-vertex@internalbeta.iam.gserviceaccount.com"
)

func env(values map[string]string) func(string) string {
	return func(name string) string { return values[name] }
}

// fakeAWS is the AWS SDK's credential chain as a Fargate task role presents
// it, counting how often it is asked.
type fakeAWS struct {
	credentials aws.Credentials
	err         error
	calls       atomic.Int32
}

func (fake *fakeAWS) Retrieve(context.Context) (aws.Credentials, error) {
	fake.calls.Add(1)
	return fake.credentials, fake.err
}

func taskRole() *fakeAWS {
	return &fakeAWS{credentials: aws.Credentials{
		AccessKeyID:     "ASIAFAKETASKROLE",
		SecretAccessKey: "fake-secret-access-key",
		SessionToken:    "fake-session-token",
		CanExpire:       true,
		Expires:         time.Now().Add(time.Hour),
	}}
}

func TestConfigFromEnvLeavesApplicationDefaultCredentialsAloneWhenUnset(t *testing.T) {
	t.Parallel()
	for _, values := range []map[string]string{
		{},
		{ProviderVariable: " ", ServiceAccountVariable: "\t"},
	} {
		config, err := ConfigFromEnv(env(values))
		if err != nil || config != (Config{}) {
			t.Fatalf("ConfigFromEnv(%q) = %+v, %v; want the zero Config", values, config, err)
		}
		credentials, err := Credentials(config, aws.Config{})
		if err != nil || credentials != nil {
			t.Fatalf("Credentials(zero) = %v, %v; want nil, so clients find Application Default Credentials", credentials, err)
		}
	}
}

func TestConfigFromEnvRefusesHalfAFederation(t *testing.T) {
	t.Parallel()
	for missing, values := range map[string]map[string]string{
		ServiceAccountVariable: {ProviderVariable: testProvider},
		ProviderVariable:       {ServiceAccountVariable: testServiceAccount},
	} {
		_, err := ConfigFromEnv(env(values))
		if err == nil {
			t.Fatalf("ConfigFromEnv(%q) accepted half a federation", values)
		}
		message := err.Error()
		if !strings.HasPrefix(message, missing+" is not set") ||
			!strings.Contains(message, ProviderVariable) || !strings.Contains(message, ServiceAccountVariable) {
			t.Fatalf("ConfigFromEnv(%q) = %q, want it to say %s is missing and name both variables", values, message, missing)
		}
	}
}

func TestConfigFromEnvRefusesWhatGoogleWouldRefuseLater(t *testing.T) {
	t.Parallel()
	for name, testCase := range map[string]struct {
		provider, serviceAccount, blamed string
	}{
		"a project ID where STS wants the number": {
			provider: "//iam.googleapis.com/projects/internalbeta/locations/global/workloadIdentityPools/oryxa/providers/aws",
			blamed:   ProviderVariable,
		},
		"the provider's resource name without its host": {
			provider: "projects/123456789012/locations/global/workloadIdentityPools/oryxa/providers/aws",
			blamed:   ProviderVariable,
		},
		"an https URL": {
			provider: "https://iam.googleapis.com/projects/123456789012/locations/global/workloadIdentityPools/oryxa/providers/aws",
			blamed:   ProviderVariable,
		},
		"the pool without a provider": {
			provider: "//iam.googleapis.com/projects/123456789012/locations/global/workloadIdentityPools/oryxa",
			blamed:   ProviderVariable,
		},
		"a person rather than a service account": {
			serviceAccount: "someone@example.com",
			blamed:         ServiceAccountVariable,
		},
		"an account name without its project": {
			serviceAccount: "oryxa-vertex",
			blamed:         ServiceAccountVariable,
		},
		"a slash that would move the impersonation URL": {
			serviceAccount: "oryxa/../x@internalbeta.iam.gserviceaccount.com",
			blamed:         ServiceAccountVariable,
		},
	} {
		provider, serviceAccount := testProvider, testServiceAccount
		if testCase.provider != "" {
			provider = testCase.provider
		}
		if testCase.serviceAccount != "" {
			serviceAccount = testCase.serviceAccount
		}
		_, err := ConfigFromEnv(env(map[string]string{ProviderVariable: provider, ServiceAccountVariable: serviceAccount}))
		if err == nil || !strings.HasPrefix(err.Error(), testCase.blamed) {
			t.Errorf("%s: ConfigFromEnv() = %v, want an error about %s", name, err, testCase.blamed)
		}
	}
}

func TestConfigFromEnvReadsTheFederation(t *testing.T) {
	t.Parallel()
	config, err := ConfigFromEnv(env(map[string]string{
		ProviderVariable:       " " + testProvider + " ",
		ServiceAccountVariable: testServiceAccount,
	}))
	if err != nil {
		t.Fatal(err)
	}
	if want := (Config{Provider: testProvider, ServiceAccount: testServiceAccount}); config != want {
		t.Fatalf("ConfigFromEnv() = %+v, want %+v", config, want)
	}
	credentials, err := Credentials(config, aws.Config{Region: "us-west-2", Credentials: taskRole()})
	if err != nil || credentials == nil {
		t.Fatalf("Credentials() = %v, %v; want credentials", credentials, err)
	}
}

func TestCredentialsNeedAnAWSIdentityToSignInWith(t *testing.T) {
	t.Parallel()
	config := Config{Provider: testProvider, ServiceAccount: testServiceAccount}
	if _, err := Credentials(config, aws.Config{Region: "us-west-2"}); err == nil {
		t.Error("Credentials() without AWS credentials succeeded")
	}
	if _, err := Credentials(config, aws.Config{Credentials: taskRole()}); err == nil {
		t.Error("Credentials() without an AWS region succeeded")
	}
	if _, err := Credentials(Config{Provider: testProvider}, aws.Config{Region: "us-west-2", Credentials: taskRole()}); err == nil {
		t.Error("Credentials() for half a federation built by hand succeeded")
	}
}

// googleStub answers Google's STS and IAM Credentials in memory, recording
// what each was sent. Anything else fails the test: the credentials reach
// Google through those two endpoints alone, and here never the network.
type googleStub struct {
	t        *testing.T
	mutex    sync.Mutex
	requests int
	// exchanged is the form STS was sent.
	exchanged url.Values
	// impersonatedWith is the Authorization IAM Credentials was sent, and
	// impersonation its body.
	impersonatedWith string
	impersonation    struct {
		Scope    []string `json:"scope"`
		Lifetime string   `json:"lifetime"`
	}
}

func (stub *googleStub) RoundTrip(request *http.Request) (*http.Response, error) {
	stub.mutex.Lock()
	defer stub.mutex.Unlock()
	stub.requests++
	body, err := io.ReadAll(request.Body)
	if err != nil {
		return nil, err
	}
	var answer string
	switch request.Method + " " + request.URL.Host + request.URL.Path {
	case "POST sts.googleapis.com/v1/token":
		if stub.exchanged, err = url.ParseQuery(string(body)); err != nil {
			return nil, err
		}
		answer = `{"access_token":"federated-token","issued_token_type":"urn:ietf:params:oauth:token-type:access_token","token_type":"Bearer","expires_in":3600}`
	case "POST iamcredentials.googleapis.com/v1/projects/-/serviceAccounts/" + testServiceAccount + ":generateAccessToken":
		stub.impersonatedWith = request.Header.Get("Authorization")
		if err := json.Unmarshal(body, &stub.impersonation); err != nil {
			return nil, err
		}
		answer = fmt.Sprintf(`{"accessToken":"service-account-token","expireTime":%q}`, time.Now().Add(time.Hour).UTC().Format(time.RFC3339))
	default:
		stub.t.Errorf("the credentials called %s %s", request.Method, request.URL)
		return nil, errors.New("not Google's STS or IAM Credentials")
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(answer)),
		Request:    request,
	}, nil
}

// signedRequest is the subject token: the GetCallerIdentity request the AWS
// role signed, for Google's STS to send on to AWS.
type signedRequest struct {
	URL     string `json:"url"`
	Method  string `json:"method"`
	Headers []struct {
		Key   string `json:"key"`
		Value string `json:"value"`
	} `json:"headers"`
}

func TestCredentialsSignInAsTheAWSRoleAndBecomeTheServiceAccount(t *testing.T) {
	t.Parallel()
	role := taskRole()
	stub := &googleStub{t: t}
	credentials, err := newCredentials(
		Config{Provider: testProvider, ServiceAccount: testServiceAccount},
		aws.Config{Region: "us-west-2", Credentials: role},
		&http.Client{Transport: stub},
	)
	if err != nil {
		t.Fatal(err)
	}
	if stub.requests != 0 || role.calls.Load() != 0 {
		t.Fatalf("building the credentials made %d calls to Google and asked AWS %d times, want neither", stub.requests, role.calls.Load())
	}

	token, err := credentials.Token(context.Background())
	if err != nil {
		t.Fatalf("Token() = %v", err)
	}
	if token.Value != "service-account-token" {
		t.Fatalf("token = %q, want the service account's, not the federated one", token.Value)
	}

	for field, want := range map[string]string{
		"audience":             testProvider,
		"grant_type":           "urn:ietf:params:oauth:grant-type:token-exchange",
		"requested_token_type": "urn:ietf:params:oauth:token-type:access_token",
		"subject_token_type":   "urn:ietf:params:aws:token-type:aws4_request",
		"scope":                Scope,
	} {
		if got := stub.exchanged.Get(field); got != want {
			t.Errorf("STS %s = %q, want %q", field, got, want)
		}
	}
	subject, err := url.QueryUnescape(stub.exchanged.Get("subject_token"))
	if err != nil {
		t.Fatal(err)
	}
	var signed signedRequest
	if err := json.Unmarshal([]byte(subject), &signed); err != nil {
		t.Fatalf("subject token %q: %v", subject, err)
	}
	if signed.Method != http.MethodPost || signed.URL != "https://sts.us-west-2.amazonaws.com?Action=GetCallerIdentity&Version=2011-06-15" {
		t.Errorf("signed %s %s, want POST to GetCallerIdentity on the region's own STS", signed.Method, signed.URL)
	}
	headers := map[string]string{}
	for _, header := range signed.Headers {
		headers[strings.ToLower(header.Key)] = header.Value
	}
	if authorization := headers["authorization"]; !strings.HasPrefix(authorization, "AWS4-HMAC-SHA256 Credential=ASIAFAKETASKROLE/") ||
		!strings.Contains(authorization, "/us-west-2/sts/aws4_request") {
		t.Errorf("Authorization = %q, want a SigV4 signature by the role's key for us-west-2 STS", authorization)
	}
	if headers["x-amz-security-token"] != "fake-session-token" {
		t.Errorf("x-amz-security-token = %q, want the role's session token", headers["x-amz-security-token"])
	}
	if headers["x-goog-cloud-target-resource"] != testProvider {
		t.Errorf("x-goog-cloud-target-resource = %q, want the provider, inside the signature", headers["x-goog-cloud-target-resource"])
	}
	if strings.Contains(subject, "fake-secret-access-key") {
		t.Error("the secret access key left the process; only its signature should")
	}

	if stub.impersonatedWith != "Bearer federated-token" {
		t.Errorf("impersonation Authorization = %q, want the federated token", stub.impersonatedWith)
	}
	if !slices.Equal(stub.impersonation.Scope, []string{Scope}) {
		t.Errorf("impersonation scope = %v, want %s", stub.impersonation.Scope, Scope)
	}

	// Every client in the process shares these credentials, so the token is
	// held rather than exchanged again for each call.
	if _, err := credentials.Token(context.Background()); err != nil {
		t.Fatal(err)
	}
	if stub.requests != 2 || role.calls.Load() != 1 {
		t.Fatalf("two tokens made %d calls to Google and asked AWS %d times, want one exchange", stub.requests, role.calls.Load())
	}
}

func TestTheAWSAdapterHandsOverTheSDKsCredentialsAndRegion(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	role := taskRole()
	adapter := newAWSCredentials(aws.Config{Region: "us-west-2", Credentials: role})

	region, err := adapter.AwsRegion(ctx, nil)
	if err != nil || region != "us-west-2" {
		t.Fatalf("AwsRegion() = %q, %v; want us-west-2", region, err)
	}
	got, err := adapter.AwsSecurityCredentials(ctx, &externalaccount.RequestOptions{Audience: testProvider})
	if err != nil {
		t.Fatal(err)
	}
	want := externalaccount.AwsSecurityCredentials{
		AccessKeyID:     "ASIAFAKETASKROLE",
		SecretAccessKey: "fake-secret-access-key",
		SessionToken:    "fake-session-token",
	}
	if *got != want {
		t.Fatalf("AwsSecurityCredentials() = %+v, want %+v", *got, want)
	}

	// An uncached provider is cached as the SDK caches its own, so the
	// container endpoint is not asked again for every exchange.
	if _, err := adapter.AwsSecurityCredentials(ctx, nil); err != nil {
		t.Fatal(err)
	}
	if role.calls.Load() != 1 {
		t.Fatalf("the SDK's provider was asked %d times, want once", role.calls.Load())
	}
	cache := aws.NewCredentialsCache(taskRole())
	if cached := newAWSCredentials(aws.Config{Region: "us-west-2", Credentials: cache}); cached.provider != cache {
		t.Fatal("a provider the SDK already caches was wrapped in a second cache")
	}
}

func TestTheAWSAdapterReportsCredentialsItCannotSignWith(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	unreachable := errors.New("the container credentials endpoint did not answer")
	if _, err := newAWSCredentials(aws.Config{Region: "us-west-2", Credentials: &fakeAWS{err: unreachable}}).
		AwsSecurityCredentials(ctx, nil); !errors.Is(err, unreachable) {
		t.Fatalf("AwsSecurityCredentials() = %v, want the SDK's own error", err)
	}
	keyless := &fakeAWS{credentials: aws.Credentials{SessionToken: "fake-session-token"}}
	if _, err := newAWSCredentials(aws.Config{Region: "us-west-2", Credentials: keyless}).
		AwsSecurityCredentials(ctx, nil); err == nil {
		t.Fatal("AwsSecurityCredentials() handed over credentials with no keys")
	}
}

// staticToken is credentials that always hold the same token.
type staticToken string

func (token staticToken) Token(context.Context) (*auth.Token, error) {
	return &auth.Token{Value: string(token), Type: "Bearer"}, nil
}

func TestHTTPClientSendsTheCredentialsToken(t *testing.T) {
	t.Parallel()
	var authorization string
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		authorization = request.Header.Get("Authorization")
		writer.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	credentials := auth.NewCredentials(&auth.CredentialsOptions{TokenProvider: staticToken("service-account-token")})
	client, err := HTTPClient(context.Background(), credentials, Scope)
	if err != nil {
		t.Fatal(err)
	}
	response, err := client.Get(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if authorization != "Bearer service-account-token" {
		t.Fatalf("Authorization = %q, want the credentials' token", authorization)
	}
}

// Without credentials the client is Application Default Credentials', which
// these find in the file GOOGLE_APPLICATION_CREDENTIALS names. The file is
// never exchanged for a token here, so the test stays offline.
func TestHTTPClientFallsBackToApplicationDefaultCredentials(t *testing.T) {
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", filepath.Join(t.TempDir(), "missing.json"))
	if _, err := HTTPClient(context.Background(), nil, Scope); err == nil ||
		!strings.Contains(err.Error(), "application default credentials") {
		t.Fatalf("HTTPClient(nil) with no credentials file = %v, want Application Default Credentials' own failure", err)
	}

	file := filepath.Join(t.TempDir(), "application_default_credentials.json")
	if err := os.WriteFile(file, []byte(`{"type":"authorized_user","client_id":"id","client_secret":"secret","refresh_token":"token"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", file)
	client, err := HTTPClient(context.Background(), nil, Scope)
	if err != nil || client == nil {
		t.Fatalf("HTTPClient(nil) = %v, %v; want a client over Application Default Credentials", client, err)
	}
}
