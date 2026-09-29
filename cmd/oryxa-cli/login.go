package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
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
	ClientID          string   `json:"cli_client_id"`
	CallbackURL       string   `json:"cli_callback_url"`
	Domain            string   `json:"cognito_domain"`
	IdentityProviders []string `json:"identity_providers"`
}

type callbackResult struct {
	code string
	err  error
}

func nativeOAuthLogin(ctx context.Context, api string, noBrowser, asJSON bool, stdout io.Writer, provider string) (string, error) {
	config, err := fetchLoginConfig(ctx, api)
	if err != nil {
		return "", err
	}
	callback, err := url.Parse(config.CallbackURL)
	if err != nil || callback.Scheme != "http" || callback.Hostname() != "127.0.0.1" || callback.Port() == "" || callback.Path != "/callback" || callback.User != nil || callback.RawQuery != "" || callback.Fragment != "" {
		return "", fmt.Errorf("the server's CLI callback URL is invalid")
	}
	listener, err := net.Listen("tcp", callback.Host)
	if err != nil {
		return "", fmt.Errorf("listen for browser sign-in at %s: %w", callback.Host, err)
	}
	defer listener.Close()
	flowCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	state, err := randomURLValue()
	if err != nil {
		return "", err
	}
	nonce, err := randomURLValue()
	if err != nil {
		return "", err
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
	mux.HandleFunc("GET /callback", func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "text/plain; charset=utf-8")
		writer.Header().Set("Cache-Control", "no-store")
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
	options := []oauth2.AuthCodeOption{oauth2.S256ChallengeOption(verifier), oauth2.SetAuthURLParam("nonce", nonce)}
	if provider != "" {
		options = append(options, oauth2.SetAuthURLParam("identity_provider", provider))
	}
	authURL := oauth.AuthCodeURL(state, options...)
	if asJSON {
		_ = json.NewEncoder(stdout).Encode(map[string]any{"step": "login", "status": "needs_human", "next": map[string]string{"action": "open_url", "url": authURL}})
	} else {
		fmt.Fprintln(stdout, "Open this URL to sign in:", authURL)
	}
	if !noBrowser {
		_ = openBrowser(authURL)
	}
	var result callbackResult
	select {
	case <-flowCtx.Done():
		return "", fmt.Errorf("browser sign-in timed out or was cancelled")
	case result = <-results:
	}
	if result.err != nil {
		return "", result.err
	}
	token, err := oauth.Exchange(flowCtx, result.code, oauth2.VerifierOption(verifier))
	if err != nil {
		return "", fmt.Errorf("exchange browser sign-in code: %w", err)
	}
	idToken, ok := token.Extra("id_token").(string)
	if !ok || idToken == "" || token.AccessToken == "" {
		return "", fmt.Errorf("identity provider did not issue both required tokens")
	}
	if err := registerCLISession(flowCtx, api, token.AccessToken, idToken, nonce); err != nil {
		return "", err
	}
	if asJSON {
		_ = json.NewEncoder(stdout).Encode(map[string]string{"step": "login", "status": "done"})
	} else {
		fmt.Fprintln(stdout, "Signed in.")
	}
	return token.AccessToken, nil
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
	return config, nil
}

func fetchAuthConfig(ctx context.Context, api string) (loginConfig, error) {
	parsed, err := url.Parse(strings.TrimRight(api, "/"))
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || (parsed.Path != "" && parsed.Path != "/") || (parsed.Scheme != "https" && !(parsed.Scheme == "http" && (parsed.Hostname() == "localhost" || parsed.Hostname() == "127.0.0.1"))) {
		return loginConfig{}, fmt.Errorf("--api must be HTTPS or loopback HTTP")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(parsed.String(), "/")+"/v1/auth/config", nil)
	if err != nil {
		return loginConfig{}, err
	}
	response, err := (&http.Client{Timeout: 20 * time.Second}).Do(request)
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
	response, err := (&http.Client{Timeout: 20 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}).Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("register CLI sign-in: API returned %d", response.StatusCode)
	}
	return nil
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
