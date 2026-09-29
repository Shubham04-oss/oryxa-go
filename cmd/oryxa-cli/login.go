package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"golang.org/x/oauth2"
)

type loginConfig struct {
	ClientID    string `json:"cli_client_id"`
	CallbackURL string `json:"cli_callback_url"`
	Domain      string `json:"cognito_domain"`
	SignUpURL   string `json:"cli_signup_url"`
	Issuer      string `json:"cli_issuer"`
}

type callbackResult struct {
	code string
	err  error
}

type cliSession struct {
	AccessToken string    `json:"access_token"`
	ExpiresAt   time.Time `json:"expires_at"`
}

func nativeLogin(ctx context.Context, api string, noBrowser, asJSON bool, stdout io.Writer) (cliSession, error) {
	config, err := fetchLoginConfig(ctx, api)
	if err != nil {
		return cliSession{}, err
	}
	callback, err := url.Parse(config.CallbackURL)
	if err != nil || callback.Scheme != "http" || callback.Hostname() != "127.0.0.1" || callback.Port() == "" || callback.Path != "/callback" || callback.User != nil || callback.RawQuery != "" || callback.Fragment != "" {
		return cliSession{}, fmt.Errorf("the server's CLI callback URL is invalid")
	}
	listener, err := net.Listen("tcp", callback.Host)
	if err != nil {
		return cliSession{}, fmt.Errorf("listen for browser sign-in at %s: %w", callback.Host, err)
	}
	defer listener.Close()
	flowCtx, cancel := context.WithTimeout(ctx, 20*time.Minute)
	defer cancel()
	state, err := randomURLValue()
	if err != nil {
		return cliSession{}, err
	}
	launchKey, err := randomURLValue()
	if err != nil {
		return cliSession{}, err
	}
	nonce, err := randomURLValue()
	if err != nil {
		return cliSession{}, err
	}
	verifier := oauth2.GenerateVerifier()
	oauth := oauth2.Config{
		ClientID: config.ClientID, RedirectURL: config.CallbackURL,
		Endpoint: oauth2.Endpoint{
			AuthURL: config.Domain + "/oauth2/authorize", TokenURL: config.Domain + "/oauth2/token",
		},
		Scopes: []string{"openid", "email", "profile"},
	}
	results := make(chan callbackResult, 1)
	mux := http.NewServeMux()
	authURL := oauth.AuthCodeURL(state, oauth2.S256ChallengeOption(verifier), oauth2.SetAuthURLParam("nonce", nonce))
	validLaunch := func(writer http.ResponseWriter, request *http.Request) bool {
		writer.Header().Set("Cache-Control", "no-store")
		writer.Header().Set("Referrer-Policy", "no-referrer")
		if request.Host != callback.Host || subtle.ConstantTimeCompare([]byte(request.URL.Query().Get("session")), []byte(launchKey)) != 1 {
			http.Error(writer, "Invalid sign-in session", http.StatusBadRequest)
			return false
		}
		return true
	}
	mux.HandleFunc("GET /start", func(writer http.ResponseWriter, request *http.Request) {
		if !validLaunch(writer, request) {
			return
		}
		writer.Header().Set("Content-Type", "text/html; charset=utf-8")
		writer.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; base-uri 'none'; form-action 'none'")
		_, _ = io.WriteString(writer, strings.ReplaceAll(browserStartPage, "{session}", url.QueryEscape(launchKey)))
	})
	mux.HandleFunc("GET /authorize", func(writer http.ResponseWriter, request *http.Request) {
		if !validLaunch(writer, request) {
			return
		}
		http.Redirect(writer, request, authURL, http.StatusFound)
	})
	mux.HandleFunc("GET /signup", func(writer http.ResponseWriter, request *http.Request) {
		if !validLaunch(writer, request) {
			return
		}
		http.Redirect(writer, request, config.SignUpURL, http.StatusFound)
	})
	mux.HandleFunc("GET /callback", func(writer http.ResponseWriter, request *http.Request) {
		if request.Host != callback.Host {
			http.Error(writer, "Invalid host", http.StatusBadRequest)
			return
		}
		writer.Header().Set("Content-Type", "text/plain; charset=utf-8")
		writer.Header().Set("Cache-Control", "no-store")
		writer.Header().Set("Referrer-Policy", "no-referrer")
		if subtle.ConstantTimeCompare([]byte(request.URL.Query().Get("state")), []byte(state)) != 1 {
			http.Error(writer, "Sign-in state did not match. Return to the CLI and retry.", http.StatusBadRequest)
			return
		}
		result := callbackResult{code: request.URL.Query().Get("code")}
		if request.URL.Query().Get("error") != "" || result.code == "" {
			result.err = fmt.Errorf("identity provider did not complete sign-in")
		}
		select {
		case results <- result:
			fmt.Fprintln(writer, "Sign-in received. Return to the Oryxa CLI.")
		default:
			http.Error(writer, "Sign-in was already received.", http.StatusConflict)
		}
	})
	server := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = server.Serve(listener) }()
	defer func() {
		shutdownCtx, stop := context.WithTimeout(context.Background(), 2*time.Second)
		defer stop()
		_ = server.Shutdown(shutdownCtx)
	}()
	startURL := "http://" + callback.Host + "/start?session=" + url.QueryEscape(launchKey)
	if asJSON {
		_ = json.NewEncoder(stdout).Encode(map[string]any{"step": "login", "status": "needs_human", "next": map[string]string{"action": "open_url", "url": startURL}})
	} else {
		fmt.Fprintln(stdout, "Open this URL to sign in or create an account:", startURL)
	}
	if !noBrowser {
		_ = openBrowser(startURL)
	}
	var result callbackResult
	select {
	case <-flowCtx.Done():
		return cliSession{}, fmt.Errorf("browser sign-in timed out or was cancelled")
	case result = <-results:
	}
	if result.err != nil {
		return cliSession{}, result.err
	}
	token, err := exchangeLoginCode(flowCtx, &oauth, result.code, verifier)
	if err != nil {
		return cliSession{}, err
	}
	idToken, ok := token.Extra("id_token").(string)
	if !ok || idToken == "" || token.AccessToken == "" {
		return cliSession{}, fmt.Errorf("identity provider did not issue both required tokens")
	}
	if token.Expiry.IsZero() || !token.Valid() {
		return cliSession{}, fmt.Errorf("identity provider did not issue an unexpired access token with a TTL")
	}
	if err := verifyLoginIDToken(flowCtx, config, idToken, nonce); err != nil {
		return cliSession{}, err
	}
	if err := registerCLISession(flowCtx, api, token.AccessToken, idToken, nonce); err != nil {
		return cliSession{}, err
	}
	if asJSON {
		_ = json.NewEncoder(stdout).Encode(map[string]string{"step": "login", "status": "done", "expires_at": token.Expiry.UTC().Format(time.RFC3339)})
	} else {
		fmt.Fprintf(stdout, "Signed in. Access expires at %s.\n", token.Expiry.Local().Format(time.RFC3339))
	}
	return cliSession{AccessToken: token.AccessToken, ExpiresAt: token.Expiry}, nil
}

func fetchLoginConfig(ctx context.Context, api string) (loginConfig, error) {
	config, err := fetchAuthConfig(ctx, api)
	if err != nil {
		return loginConfig{}, err
	}
	domain, err := url.Parse(config.Domain)
	if config.ClientID == "" || err != nil || domain.Scheme != "https" || domain.Host == "" || domain.User != nil || domain.Path != "" || domain.RawQuery != "" || domain.Fragment != "" {
		return loginConfig{}, fmt.Errorf("CLI browser sign-in is not configured on this server")
	}
	signUp, err := url.Parse(config.SignUpURL)
	if err != nil || signUp.User != nil || signUp.Host == "" || signUp.Path != "/auth/sign-up" || signUp.RawQuery != "" || signUp.Fragment != "" || !(signUp.Scheme == "https" || signUp.Scheme == "http" && (signUp.Hostname() == "localhost" || signUp.Hostname() == "127.0.0.1")) {
		return loginConfig{}, fmt.Errorf("CLI browser account creation is not configured on this server")
	}
	return config, nil
}

const browserStartPage = `<!doctype html><html lang="en"><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>Oryxa CLI sign-in</title><style>body{font:16px system-ui,sans-serif;max-width:34rem;margin:12vh auto;padding:0 1.5rem;color:#222}a{display:inline-block;padding:.75rem 1rem;border-radius:.5rem;background:#222;color:#fff;text-decoration:none;margin:.5rem .75rem .5rem 0}a.secondary{background:#eee;color:#222}p{line-height:1.5}</style><h1>Continue to Oryxa CLI</h1><p>Sign in in your browser. Oryxa will return a short-lived session to the CLI.</p><a href="/authorize?session={session}">Sign in</a><a class="secondary" href="/signup?session={session}" target="_blank" rel="noopener noreferrer">Create account</a><p>Creating an account? Verify your email, then return to this tab and choose Sign in.</p></html>`

func fetchAuthConfig(ctx context.Context, api string) (loginConfig, error) {
	parsed, err := url.Parse(strings.TrimRight(api, "/"))
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || (parsed.Path != "" && parsed.Path != "/") || (parsed.Scheme != "https" && !(parsed.Scheme == "http" && (parsed.Hostname() == "localhost" || parsed.Hostname() == "127.0.0.1"))) {
		return loginConfig{}, fmt.Errorf("--api must be HTTPS or loopback HTTP")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(parsed.String(), "/")+"/v1/auth/config", nil)
	if err != nil {
		return loginConfig{}, err
	}
	response, err := loginHTTPClient(ctx).Do(request)
	if err != nil {
		return loginConfig{}, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return loginConfig{}, fmt.Errorf("read CLI sign-in configuration: API returned %d", response.StatusCode)
	}
	var config loginConfig
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&config); err != nil {
		return loginConfig{}, err
	}
	return config, nil
}

func registerCLISession(ctx context.Context, api, accessToken, idToken, nonce string) error {
	body, err := json.Marshal(map[string]string{"id_token": idToken, "nonce": nonce})
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(api, "/")+"/v1/auth/cli/session", bytes.NewReader(body))
	if err != nil {
		return err
	}
	request.Header.Set("Authorization", "Bearer "+accessToken)
	request.Header.Set("Content-Type", "application/json")
	response, err := loginHTTPClient(ctx).Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("register CLI sign-in: API returned %d", response.StatusCode)
	}
	return nil
}

// OAuth exchanges carry the authorization code and PKCE verifier in their
// request body. Even a cross-origin 307 preserves that body, so redirects must
// be rejected rather than relying on net/http's Authorization header rules.
func loginHTTPClient(ctx context.Context) *http.Client {
	base := http.DefaultClient
	if configured, ok := ctx.Value(oauth2.HTTPClient).(*http.Client); ok && configured != nil {
		base = configured
	}
	client := *base
	client.Timeout = 20 * time.Second
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &client
}

func exchangeLoginCode(ctx context.Context, config *oauth2.Config, code, verifier string) (*oauth2.Token, error) {
	ctx = context.WithValue(ctx, oauth2.HTTPClient, loginHTTPClient(ctx))
	token, err := config.Exchange(ctx, code, oauth2.VerifierOption(verifier))
	if err == nil {
		return token, nil
	}
	// oauth2.RetrieveError includes the provider response body, which may echo
	// credentials. Keep provider bodies and descriptions out of terminal logs.
	var rejected *oauth2.RetrieveError
	if errors.As(err, &rejected) && rejected.Response != nil {
		return nil, fmt.Errorf("exchange browser sign-in code: identity provider returned %d", rejected.Response.StatusCode)
	}
	if ctx.Err() != nil {
		return nil, fmt.Errorf("exchange browser sign-in code: %w", ctx.Err())
	}
	return nil, fmt.Errorf("exchange browser sign-in code: identity provider request failed")
}

func randomURLValue() (string, error) {
	value := make([]byte, 32)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(value), nil
}

func openBrowser(target string) error {
	switch runtime.GOOS {
	case "darwin":
		return exec.Command("open", target).Start()
	case "windows":
		return exec.Command("rundll32", "url.dll,FileProtocolHandler", target).Start()
	default:
		return exec.Command("xdg-open", target).Start()
	}
}
