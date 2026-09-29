package main

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/oauth2"
)

func TestNativeLoginReturnsExpiringSession(t *testing.T) {
	reserved, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	callback := "http://" + reserved.Addr().String() + "/callback"
	reserved.Close()
	key := loginTestKey(t)
	var loginNonce atomic.Value
	var cognito *httptest.Server
	cognito = httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/.well-known/jwks.json" {
			writeTestJWKS(writer, key)
			return
		}
		if request.URL.Path != "/oauth2/token" || request.Method != http.MethodPost {
			writer.WriteHeader(http.StatusNotFound)
			return
		}
		if err := request.ParseForm(); err != nil {
			t.Error(err)
		}
		if request.Form.Get("code") != "code" || request.Form.Get("code_verifier") == "" || request.Form.Get("redirect_uri") != callback {
			t.Errorf("invalid code exchange: %v", request.Form)
		}
		writer.Header().Set("Content-Type", "application/json")
		json.NewEncoder(writer).Encode(map[string]any{"access_token": "access", "id_token": signedLoginToken(t, key, cognito.URL, loginNonce.Load().(string)), "token_type": "Bearer", "expires_in": 900})
	}))
	defer cognito.Close()
	registered := make(chan bool, 1)
	challenge := loginChallenge{State: base64.RawURLEncoding.EncodeToString([]byte(strings.Repeat("s", 32))), Nonce: base64.RawURLEncoding.EncodeToString([]byte(strings.Repeat("n", 32))), ExpiresAt: time.Now().Add(20 * time.Minute)}
	api := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/v1/auth/config":
			_ = json.NewEncoder(writer).Encode(loginConfig{ClientID: "cli", CallbackURL: callback, Domain: cognito.URL, Issuer: cognito.URL, SignUpURL: "https://app.example.com/auth/sign-up"})
		case "/v1/auth/cli/challenge":
			if request.Method != http.MethodPost || request.Header.Get("Authorization") != "" {
				t.Error("challenge must be an unauthenticated POST")
			}
			_ = json.NewEncoder(writer).Encode(challenge)
		case "/v1/auth/cli/session":
			var body map[string]string
			_ = json.NewDecoder(request.Body).Decode(&body)
			registered <- request.Header.Get("Authorization") == "Bearer access" && body["id_token"] != "" && body["state"] == challenge.State && body["nonce"] == ""
			_ = json.NewEncoder(writer).Encode(map[string]any{"user": map[string]string{"id": "u1"}, "expires_at": time.Now().Add(8 * time.Minute)})
		default:
			writer.WriteHeader(http.StatusNotFound)
		}
	}))
	defer api.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	ctx = context.WithValue(ctx, oauth2.HTTPClient, cognito.Client())
	reader, writer := net.Pipe()
	defer reader.Close()
	defer writer.Close()
	result := make(chan struct {
		session cliSession
		err     error
	}, 1)
	go func() {
		session, err := nativeLogin(ctx, api.URL, true, true, writer)
		result <- struct {
			session cliSession
			err     error
		}{session, err}
	}()
	lines := bufio.NewReader(reader)
	line, err := lines.ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	var step struct {
		Next struct {
			URL string `json:"url"`
		} `json:"next"`
	}
	if err := json.Unmarshal([]byte(line), &step); err != nil {
		t.Fatal(err)
	}
	startURL, err := url.Parse(step.Next.URL)
	if err != nil || startURL.Host != reserved.Addr().String() || startURL.Path != "/start" || startURL.Query().Get("session") == "" {
		t.Fatalf("start URL = %q", step.Next.URL)
	}
	session := url.QueryEscape(startURL.Query().Get("session"))
	start, err := http.Get(step.Next.URL)
	if err != nil {
		t.Fatal(err)
	}
	startBody, err := io.ReadAll(start.Body)
	start.Body.Close()
	if err != nil || start.StatusCode != http.StatusOK || start.Header.Get("Cache-Control") != "no-store" || start.Header.Get("Referrer-Policy") != "no-referrer" || start.Header.Get("Content-Security-Policy") == "" || !strings.Contains(string(startBody), `href="/authorize?session=`+session+`"`) || !strings.Contains(string(startBody), `href="/signup?session=`+session+`"`) {
		t.Fatalf("start response = %d %v", start.StatusCode, start.Header)
	}
	noRedirect := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	unguarded, err := noRedirect.Get("http://" + reserved.Addr().String() + "/authorize")
	if err != nil {
		t.Fatal(err)
	}
	unguarded.Body.Close()
	if unguarded.StatusCode != http.StatusBadRequest {
		t.Fatalf("unguarded authorization status = %d", unguarded.StatusCode)
	}
	signUp, err := noRedirect.Get("http://" + reserved.Addr().String() + "/signup?session=" + session)
	if err != nil {
		t.Fatal(err)
	}
	signUp.Body.Close()
	if signUp.StatusCode != http.StatusFound || signUp.Header.Get("Location") != "https://app.example.com/auth/sign-up" {
		t.Fatalf("sign-up redirect = %d %q", signUp.StatusCode, signUp.Header.Get("Location"))
	}
	authorize, err := noRedirect.Get("http://" + reserved.Addr().String() + "/authorize?session=" + session)
	if err != nil {
		t.Fatal(err)
	}
	authorize.Body.Close()
	authURL, err := url.Parse(authorize.Header.Get("Location"))
	if err != nil || authorize.StatusCode != http.StatusFound || authURL.Query().Get("code_challenge") == "" || authURL.Query().Get("nonce") == "" || authURL.Query().Get("identity_provider") != "" {
		t.Fatalf("invalid authorization URL: %s, %v", authorize.Header.Get("Location"), err)
	}
	if authURL.Query().Get("state") != challenge.State || authURL.Query().Get("nonce") != challenge.Nonce {
		t.Fatal("authorization URL did not use the server-issued challenge")
	}
	loginNonce.Store(authURL.Query().Get("nonce"))
	wrongState, err := http.Get(callback + "?code=code&state=wrong")
	if err != nil {
		t.Fatal(err)
	}
	wrongState.Body.Close()
	if wrongState.StatusCode != http.StatusBadRequest {
		t.Fatalf("wrong-state callback status = %d", wrongState.StatusCode)
	}
	// The browser would follow Cognito's redirect to this loopback URL.
	response, err := http.Get(callback + "?code=code&state=" + url.QueryEscape(authURL.Query().Get("state")))
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("callback status = %d", response.StatusCode)
	}
	completion, err := lines.ReadString('\n')
	if err != nil {
		t.Fatalf("login completion output: %v", err)
	}
	var completed struct {
		ExpiresAt time.Time `json:"expires_at"`
	}
	if err := json.Unmarshal([]byte(completion), &completed); err != nil || completed.ExpiresAt.Before(time.Now().Add(7*time.Minute)) || completed.ExpiresAt.After(time.Now().Add(9*time.Minute)) {
		t.Fatalf("login expiry = %q, %v", completion, err)
	}
	select {
	case done := <-result:
		if done.err != nil || done.session.AccessToken != "access" || done.session.ExpiresAt.Before(time.Now().Add(7*time.Minute)) || done.session.ExpiresAt.After(time.Now().Add(9*time.Minute)) {
			t.Fatalf("login = %+v, %v", done.session, done.err)
		}
	case <-ctx.Done():
		t.Fatal("login did not finish")
	}
	select {
	case ok := <-registered:
		if !ok {
			t.Fatal("CLI session was not registered with the tokens")
		}
	default:
		t.Fatal("CLI session was not registered")
	}
}

func TestNativeLoginRejectsMissingClient(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(writer, `{"cognito_domain":"https://example.com"}`)
	}))
	defer api.Close()
	_, err := fetchLoginConfig(context.Background(), api.URL)
	if err == nil || !strings.Contains(err.Error(), "not configured") {
		t.Fatalf("error = %v", err)
	}
}

func TestNativeLoginRejectsUnsafeSignUpURL(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(writer).Encode(loginConfig{
			ClientID: "cli", CallbackURL: "http://127.0.0.1:8765/callback",
			Domain: "https://cognito.example.com", SignUpURL: "http://remote.example.com/auth/sign-up",
		})
	}))
	defer api.Close()
	_, err := fetchLoginConfig(context.Background(), api.URL)
	if err == nil || !strings.Contains(err.Error(), "account creation") {
		t.Fatalf("error = %v", err)
	}
}

func TestLoginConfigRejectsRedirectToAnotherAuthority(t *testing.T) {
	var followed atomic.Int32
	other := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		followed.Add(1)
		fmt.Fprint(writer, `{}`)
	}))
	defer other.Close()
	api := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		http.Redirect(writer, request, other.URL, http.StatusFound)
	}))
	defer api.Close()
	if _, err := fetchAuthConfig(context.Background(), api.URL); err == nil || !strings.Contains(err.Error(), "302") {
		t.Fatalf("redirect error = %v", err)
	}
	if followed.Load() != 0 {
		t.Fatal("login configuration followed a redirect to a different authority")
	}
}

func TestLoginExchangeDoesNotForwardCodeAndVerifierOnRedirect(t *testing.T) {
	var received atomic.Int32
	other := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		received.Add(1)
		fmt.Fprint(writer, `{}`)
	}))
	defer other.Close()
	provider := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		http.Redirect(writer, request, other.URL, http.StatusTemporaryRedirect)
	}))
	defer provider.Close()
	ctx := context.WithValue(context.Background(), oauth2.HTTPClient, provider.Client())
	config := oauth2.Config{ClientID: "cli", Endpoint: oauth2.Endpoint{TokenURL: provider.URL}}
	if _, err := exchangeLoginCode(ctx, &config, "private-code", "private-verifier"); err == nil || !strings.Contains(err.Error(), "307") {
		t.Fatalf("exchange error = %v", err)
	}
	if received.Load() != 0 {
		t.Fatal("authorization code and verifier were sent to the redirect destination")
	}
}

func TestLoginExchangeDoesNotExposeProviderErrorBodies(t *testing.T) {
	for _, body := range []string{
		`{"error":"invalid_grant","error_description":"private-access-token private-refresh-token"}`,
		`private-access-token private-refresh-token`,
	} {
		t.Run(body, func(t *testing.T) {
			provider := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				writer.Header().Set("Content-Type", "application/json")
				writer.WriteHeader(http.StatusBadRequest)
				fmt.Fprint(writer, body)
			}))
			defer provider.Close()
			ctx := context.WithValue(context.Background(), oauth2.HTTPClient, provider.Client())
			config := oauth2.Config{ClientID: "cli", Endpoint: oauth2.Endpoint{TokenURL: provider.URL}}
			_, err := exchangeLoginCode(ctx, &config, "private-code", "private-verifier")
			if err == nil || !strings.Contains(err.Error(), "400") || strings.Contains(err.Error(), "private-") {
				t.Fatalf("unsafe exchange error = %v", err)
			}
		})
	}
}

func TestConcurrentSignInsUseRegisteredFallbackPorts(t *testing.T) {
	callbacks := []string{}
	for range 2 {
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		callbacks = append(callbacks, "http://"+listener.Addr().String()+"/callback")
		listener.Close()
	}
	config := loginConfig{CallbackURL: callbacks[0], CallbackURLs: callbacks}
	first, firstURL, err := listenLoginCallback(config)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, secondURL, err := listenLoginCallback(config)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	if firstURL.String() == secondURL.String() {
		t.Fatal("concurrent signins shared callback")
	}
	if listener, _, err := listenLoginCallback(config); err == nil {
		listener.Close()
		t.Fatal("occupied callbacks reused")
	}
	first.Close()
	reopened, _, err := listenLoginCallback(config)
	if err != nil {
		t.Fatal("callback not released promptly:", err)
	}
	reopened.Close()
}
