package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	workspaceruntime "github.com/Shubham04-oss/oryxa-go/internal/workspace/runtime"
)

// Device sign-in (RFC 8628) needs no browser on this machine: the CLI shows a
// URL and a code, the member approves in any browser where they are signed
// in to Oryxa, and the CLI polls auth until they decide.

type deviceAuthorization struct {
	DeviceCode              string `json:"device_code"`
	UserCode                string `json:"user_code"`
	VerificationURI         string `json:"verification_uri"`
	VerificationURIComplete string `json:"verification_uri_complete"`
	ExpiresIn               int    `json:"expires_in"`
	Interval                int    `json:"interval"`
}

const (
	deviceDefaultInterval = 5 * time.Second
	deviceSlowDownStep    = 5 * time.Second
	deviceMaxInterval     = 60 * time.Second
)

// deviceSleep waits between polls; tests replace it.
var deviceSleep = func(ctx context.Context, wait time.Duration) error {
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// useDeviceLogin decides between the loopback browser flow and device
// sign-in. --no-browser keeps the loopback flow: it prints a URL for a
// browser on this machine, which a coding agent relays to its member.
func useDeviceLogin(opts options, getenv func(string) string) bool {
	if opts.device {
		return true
	}
	if getenv("SSH_CONNECTION") != "" || getenv("SSH_TTY") != "" {
		return true
	}
	return !opts.noBrowser && !canOpenBrowser(getenv)
}

var lookPath = exec.LookPath

func canOpenBrowser(getenv func(string) string) bool {
	switch runtime.GOOS {
	case "darwin", "windows":
		return true
	}
	if getenv("DISPLAY") == "" && getenv("WAYLAND_DISPLAY") == "" {
		return false
	}
	_, err := lookPath("xdg-open")
	return err == nil
}

func deviceClientName() string {
	host, err := os.Hostname()
	if err != nil || host == "" {
		return "oryxa-cli (" + runtime.GOOS + ")"
	}
	return fmt.Sprintf("oryxa-cli on %s (%s)", host, runtime.GOOS)
}

func deviceLogin(ctx context.Context, api string, asJSON bool, stdout io.Writer) (cliSession, error) {
	api, err := workspaceruntime.ValidateAPIURL(api)
	if err != nil {
		return cliSession{}, &inputError{err}
	}
	start, err := startDeviceLogin(ctx, api)
	if err != nil {
		return cliSession{}, err
	}
	if asJSON {
		_ = json.NewEncoder(stdout).Encode(map[string]any{"step": "login", "status": "needs_human", "next": map[string]string{
			"action": "enter_code", "url": start.VerificationURI, "code": start.UserCode,
		}})
	} else {
		fmt.Fprintf(stdout, "To sign in, open %s in any browser where you are signed in to Oryxa,\nand enter the code %s\n", start.VerificationURI, start.UserCode)
		fmt.Fprintln(stdout, "Waiting for approval...")
	}
	flowCtx, cancel := context.WithTimeout(ctx, time.Duration(start.ExpiresIn)*time.Second)
	defer cancel()
	session, err := pollDeviceLogin(flowCtx, api, start)
	if err != nil {
		return cliSession{}, err
	}
	if asJSON {
		_ = json.NewEncoder(stdout).Encode(map[string]string{"step": "login", "status": "done", "expires_at": session.ExpiresAt.UTC().Format(time.RFC3339)})
	} else {
		fmt.Fprintf(stdout, "Signed in. Access expires at %s.\n", session.ExpiresAt.Local().Format(time.RFC3339))
	}
	return session, nil
}

func startDeviceLogin(ctx context.Context, api string) (deviceAuthorization, error) {
	body, err := json.Marshal(map[string]string{"client_name": deviceClientName()})
	if err != nil {
		return deviceAuthorization{}, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, api+"/v1/auth/cli/device", bytes.NewReader(body))
	if err != nil {
		return deviceAuthorization{}, err
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := loginHTTPClient(ctx).Do(request)
	if err != nil {
		return deviceAuthorization{}, fmt.Errorf("start device sign-in: server request failed")
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusNotFound {
		return deviceAuthorization{}, fmt.Errorf("this server does not offer device sign-in; run login without --device on a machine with a browser")
	}
	if response.StatusCode != http.StatusOK {
		return deviceAuthorization{}, fmt.Errorf("start device sign-in: API returned %d", response.StatusCode)
	}
	var start deviceAuthorization
	if err := json.NewDecoder(io.LimitReader(response.Body, 8192)).Decode(&start); err != nil {
		return deviceAuthorization{}, fmt.Errorf("start device sign-in: invalid server response")
	}
	if err := validateDeviceAuthorization(&start); err != nil {
		return deviceAuthorization{}, err
	}
	return start, nil
}

func validateDeviceAuthorization(start *deviceAuthorization) error {
	verification, err := url.Parse(start.VerificationURI)
	if err != nil || verification.User != nil || verification.Host == "" || verification.RawQuery != "" || verification.Fragment != "" ||
		!(verification.Scheme == "https" || verification.Scheme == "http" && (verification.Hostname() == "localhost" || verification.Hostname() == "127.0.0.1")) {
		return fmt.Errorf("start device sign-in: the server's verification URL is invalid")
	}
	complete, err := url.Parse(start.VerificationURIComplete)
	if err != nil || complete.Scheme != verification.Scheme || complete.Host != verification.Host || complete.Path != verification.Path {
		start.VerificationURIComplete = start.VerificationURI
	}
	if start.DeviceCode == "" || start.UserCode == "" || len(start.UserCode) > 16 || start.ExpiresIn <= 0 || start.ExpiresIn > 15*60 {
		return fmt.Errorf("start device sign-in: invalid server response")
	}
	if start.Interval <= 0 {
		start.Interval = int(deviceDefaultInterval / time.Second)
	}
	return nil
}

// pollDeviceLogin polls at the server's interval, adding five seconds on each
// slow_down (and on a rate limit), until approval, denial or expiry.
func pollDeviceLogin(ctx context.Context, api string, start deviceAuthorization) (cliSession, error) {
	interval := min(time.Duration(start.Interval)*time.Second, deviceMaxInterval)
	for {
		if err := deviceSleep(ctx, interval); err != nil {
			return cliSession{}, fmt.Errorf("device sign-in timed out or was cancelled")
		}
		session, status, serverInterval, err := pollDeviceToken(ctx, api, start.DeviceCode)
		if err != nil {
			return cliSession{}, err
		}
		switch status {
		case "":
			return session, nil
		case "authorization_pending":
		case "slow_down", "rate_limited":
			interval = min(max(interval+deviceSlowDownStep, serverInterval), deviceMaxInterval)
		case "access_denied":
			return cliSession{}, fmt.Errorf("device sign-in was denied in the browser")
		case "expired_token":
			return cliSession{}, fmt.Errorf("device sign-in code expired; run login again")
		default:
			return cliSession{}, fmt.Errorf("device sign-in failed: %s", status)
		}
	}
}

func pollDeviceToken(ctx context.Context, api, deviceCode string) (cliSession, string, time.Duration, error) {
	body, err := json.Marshal(map[string]string{"grant_type": "urn:ietf:params:oauth:grant-type:device_code", "device_code": deviceCode})
	if err != nil {
		return cliSession{}, "", 0, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, api+"/v1/auth/cli/device/token", bytes.NewReader(body))
	if err != nil {
		return cliSession{}, "", 0, err
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := loginHTTPClient(ctx).Do(request)
	if err != nil {
		if ctx.Err() != nil {
			return cliSession{}, "", 0, fmt.Errorf("device sign-in timed out or was cancelled")
		}
		// A dropped connection over SSH is worth another try at the next tick.
		return cliSession{}, "authorization_pending", 0, nil
	}
	defer response.Body.Close()
	var result struct {
		AccessToken string    `json:"access_token"`
		TokenType   string    `json:"token_type"`
		ExpiresAt   time.Time `json:"expires_at"`
		Error       string    `json:"error"`
		Interval    int       `json:"interval"`
	}
	decodeErr := json.NewDecoder(io.LimitReader(response.Body, 8192)).Decode(&result)
	switch {
	case response.StatusCode == http.StatusOK:
		if decodeErr != nil || result.AccessToken == "" || !strings.EqualFold(result.TokenType, "Bearer") || !result.ExpiresAt.After(time.Now()) || result.ExpiresAt.After(time.Now().Add(15*time.Minute+30*time.Second)) {
			return cliSession{}, "", 0, fmt.Errorf("device sign-in: invalid session from server")
		}
		return cliSession{AccessToken: result.AccessToken, ExpiresAt: result.ExpiresAt}, "", 0, nil
	case response.StatusCode == http.StatusTooManyRequests:
		return cliSession{}, "rate_limited", 0, nil
	case response.StatusCode >= 500:
		return cliSession{}, "authorization_pending", 0, nil
	case response.StatusCode == http.StatusBadRequest && decodeErr == nil && result.Error != "":
		return cliSession{}, result.Error, time.Duration(result.Interval) * time.Second, nil
	default:
		return cliSession{}, "", 0, fmt.Errorf("device sign-in: API returned %d", response.StatusCode)
	}
}
