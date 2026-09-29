package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strings"
	"time"

	"golang.org/x/term"
)

var errVerificationPending = errors.New("verify your email through the link, then rerun this command and sign in")

func interactiveLogin(ctx context.Context, opts options, input io.Reader, stdout, stderr io.Writer) (string, error) {
	config, err := fetchAuthConfig(ctx, opts.api)
	if err != nil {
		return "", err
	}
	reader := bufio.NewReader(input)
	google := slices.Contains(config.IdentityProviders, "Google") && config.ClientID != ""
	fmt.Fprintln(stderr, "Sign in to Oryxa or create an account:")
	if google {
		fmt.Fprintln(stderr, "  1. Continue with Google")
		fmt.Fprintln(stderr, "  2. Sign in with email")
		fmt.Fprintln(stderr, "  3. Create account with email")
	} else {
		fmt.Fprintln(stderr, "  1. Sign in with email")
		fmt.Fprintln(stderr, "  2. Create account with email")
	}
	choice, err := promptLine(reader, stderr, "Choose an option: ")
	if err != nil {
		return "", err
	}
	if google && choice == "1" {
		return nativeOAuthLogin(ctx, opts.api, opts.noBrowser, opts.json, stdout, "Google")
	}
	if choice == "1" && !google || choice == "2" && google {
		return emailSignIn(ctx, opts.api, input, reader, stdout, stderr, opts.json)
	}
	if choice == "2" && !google || choice == "3" && google {
		return emailSignUp(ctx, opts.api, input, reader, stdout, stderr, opts.json)
	}
	return "", fmt.Errorf("choose one of the listed sign-in options")
}

func emailSignIn(ctx context.Context, api string, input io.Reader, reader *bufio.Reader, stdout, stderr io.Writer, asJSON bool) (string, error) {
	email, err := promptLine(reader, stderr, "Email: ")
	if err != nil {
		return "", err
	}
	password, err := promptSecret(input, reader, stderr, "Password: ")
	if err != nil {
		return "", err
	}
	return loginWithPassword(ctx, api, email, password, stdout, stderr, asJSON)
}

func emailSignUp(ctx context.Context, api string, input io.Reader, reader *bufio.Reader, stdout, stderr io.Writer, asJSON bool) (string, error) {
	name, err := promptLine(reader, stderr, "Display name: ")
	if err != nil {
		return "", err
	}
	email, err := promptLine(reader, stderr, "Email: ")
	if err != nil {
		return "", err
	}
	password, err := promptSecret(input, reader, stderr, "New password: ")
	if err != nil {
		return "", err
	}
	confirm, err := promptSecret(input, reader, stderr, "Confirm password: ")
	if err != nil {
		return "", err
	}
	if password != confirm {
		return "", fmt.Errorf("passwords do not match")
	}
	if err := cliPasswordRequest(ctx, api, "/v1/auth/cli/password/signup", map[string]string{
		"display_name": name, "email": email, "password": password,
	}, http.StatusAccepted, nil); err != nil {
		return "", err
	}
	if asJSON {
		_ = json.NewEncoder(stdout).Encode(map[string]any{
			"step": "login", "status": "needs_human",
			"next": map[string]string{"action": "verify_email", "email": email},
		})
	}
	fmt.Fprintln(stderr, "Check your email for a verification link.")
	value, err := promptLine(reader, stderr, "Paste the link or its token (Enter to finish later): ")
	if err != nil {
		return "", err
	}
	if value == "" {
		return "", errVerificationPending
	}
	token, err := verificationToken(value)
	if err != nil {
		return "", err
	}
	if err := cliPasswordRequest(ctx, api, "/v1/auth/cli/password/verify", map[string]string{"token": token}, http.StatusNoContent, nil); err != nil {
		// Someone may already have opened the link in a browser, consuming the
		// one-time token. A successful sign-in confirms verification is done.
		if access, loginErr := loginWithPassword(ctx, api, email, password, stdout, stderr, asJSON); loginErr == nil {
			return access, nil
		}
		return "", err
	}
	return loginWithPassword(ctx, api, email, password, stdout, stderr, asJSON)
}

func loginWithPassword(ctx context.Context, api, email, password string, stdout, stderr io.Writer, asJSON bool) (string, error) {
	var session struct {
		AccessToken string `json:"access_token"`
	}
	if err := cliPasswordRequest(ctx, api, "/v1/auth/cli/password/login", map[string]string{
		"email": email, "password": password,
	}, http.StatusOK, &session); err != nil {
		return "", err
	}
	if session.AccessToken == "" {
		return "", fmt.Errorf("CLI password sign-in returned no access token")
	}
	if asJSON {
		_ = json.NewEncoder(stdout).Encode(map[string]string{"step": "login", "status": "done"})
	} else {
		fmt.Fprintln(stderr, "Signed in.")
	}
	return session.AccessToken, nil
}

func cliPasswordRequest(ctx context.Context, api, path string, input any, expected int, output any) error {
	data, err := json.Marshal(input)
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(api, "/")+path, bytes.NewReader(data))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 20 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("CLI authentication request: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != expected {
		var body struct {
			Error string `json:"error"`
		}
		if response.StatusCode < 500 && strings.HasPrefix(response.Header.Get("Content-Type"), "application/json") && json.NewDecoder(io.LimitReader(response.Body, 1024)).Decode(&body) == nil {
			message := strings.TrimSpace(body.Error)
			if message != "" && len(message) <= 512 && !strings.ContainsAny(message, "\r\n") {
				return fmt.Errorf("CLI authentication: %s", message)
			}
		}
		return fmt.Errorf("CLI authentication: API returned %d", response.StatusCode)
	}
	if output != nil {
		if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(output); err != nil {
			return fmt.Errorf("CLI authentication returned invalid JSON: %w", err)
		}
	}
	return nil
}

func promptLine(reader *bufio.Reader, output io.Writer, label string) (string, error) {
	if _, err := fmt.Fprint(output, label); err != nil {
		return "", err
	}
	line, err := reader.ReadString('\n')
	if err != nil && err != io.EOF {
		return "", err
	}
	if err == io.EOF && line == "" {
		return "", fmt.Errorf("input ended before %s", strings.TrimSuffix(label, ": "))
	}
	return strings.TrimSpace(line), nil
}

func promptSecret(input io.Reader, reader *bufio.Reader, output io.Writer, label string) (string, error) {
	if file, ok := input.(*os.File); ok && term.IsTerminal(int(file.Fd())) {
		if _, err := fmt.Fprint(output, label); err != nil {
			return "", err
		}
		secret, err := term.ReadPassword(int(file.Fd()))
		fmt.Fprintln(output)
		return string(secret), err
	}
	return promptLine(reader, output, label)
}

func verificationToken(value string) (string, error) {
	if !strings.Contains(value, "://") {
		return value, nil
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme != "https" && parsed.Scheme != "http" || parsed.Host == "" || parsed.Query().Get("token") == "" {
		return "", fmt.Errorf("verification link has no token")
	}
	return parsed.Query().Get("token"), nil
}
