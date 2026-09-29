package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"golang.org/x/oauth2"
)

func TestNativeLoginKeepsTokenInMemory(t *testing.T) {
	reserved, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	callback := "http://" + reserved.Addr().String() + "/callback"
	reserved.Close()
	cognito := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
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
		fmt.Fprint(writer, `{"access_token":"access","id_token":"identity","token_type":"Bearer","expires_in":900}`)
	}))
	defer cognito.Close()
	registered := make(chan bool, 1)
	api := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/v1/auth/config":
			_ = json.NewEncoder(writer).Encode(loginConfig{ClientID: "cli", CallbackURL: callback, Domain: cognito.URL})
		case "/v1/auth/cli/session":
			var body map[string]string
			_ = json.NewDecoder(request.Body).Decode(&body)
			registered <- request.Header.Get("Authorization") == "Bearer access" && body["id_token"] == "identity" && body["nonce"] != ""
			fmt.Fprint(writer, `{"user":{"id":"u1"}}`)
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
		token string
		err   error
	}, 1)
	go func() {
		token, err := nativeOAuthLogin(ctx, api.URL, true, true, writer, "Google")
		result <- struct {
			token string
			err   error
		}{token, err}
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
	authURL, err := url.Parse(step.Next.URL)
	if err != nil || authURL.Query().Get("code_challenge") == "" || authURL.Query().Get("nonce") == "" || authURL.Query().Get("identity_provider") != "Google" {
		t.Fatalf("invalid authorization URL: %s, %v", step.Next.URL, err)
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
	if _, err := lines.ReadString('\n'); err != nil {
		t.Fatalf("login completion output: %v", err)
	}
	select {
	case done := <-result:
		if done.err != nil || done.token != "access" {
			t.Fatalf("login = %q, %v", done.token, done.err)
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
