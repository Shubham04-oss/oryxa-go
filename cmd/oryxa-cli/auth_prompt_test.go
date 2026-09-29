package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestInteractiveSignupVerifiesThenSignsInWithoutExposingSecrets(t *testing.T) {
	steps := []string{}
	var stepsMu sync.Mutex
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		switch request.URL.Path {
		case "/v1/auth/config":
			fmt.Fprint(writer, `{"identity_providers":["Password"]}`)
		case "/v1/auth/cli/password/signup":
			var body map[string]string
			_ = json.NewDecoder(request.Body).Decode(&body)
			if body["email"] != "creator@example.com" || body["display_name"] != "Creator" || body["password"] != "long-secret-password" {
				t.Errorf("signup request omitted creator fields")
			}
			stepsMu.Lock()
			steps = append(steps, "signup")
			stepsMu.Unlock()
			writer.WriteHeader(http.StatusAccepted)
			fmt.Fprint(writer, `{"status":"verification_requested"}`)
		case "/v1/auth/cli/password/verify":
			var body map[string]string
			_ = json.NewDecoder(request.Body).Decode(&body)
			if body["token"] != "email-code" {
				t.Errorf("verification token = %q", body["token"])
			}
			stepsMu.Lock()
			steps = append(steps, "verify")
			stepsMu.Unlock()
			writer.WriteHeader(http.StatusNoContent)
		case "/v1/auth/cli/password/login":
			stepsMu.Lock()
			steps = append(steps, "login")
			stepsMu.Unlock()
			fmt.Fprint(writer, `{"access_token":"short-lived-token"}`)
		default:
			writer.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	var stdout, stderr bytes.Buffer
	input := strings.NewReader("2\nCreator\ncreator@example.com\nlong-secret-password\nlong-secret-password\nhttps://app.example.com/auth/verify-email?token=email-code\n")
	token, err := interactiveLogin(context.Background(), options{api: server.URL, json: true}, input, &stdout, &stderr)
	stepsMu.Lock()
	defer stepsMu.Unlock()
	if err != nil || token != "short-lived-token" || strings.Join(steps, ",") != "signup,verify,login" {
		t.Fatalf("login = %q, %v; steps=%v", token, err, steps)
	}
	if strings.Contains(stdout.String()+stderr.String(), "long-secret-password") || strings.Contains(stdout.String()+stderr.String(), token) || !strings.Contains(stdout.String(), `"status":"done"`) {
		t.Fatalf("unexpected CLI output: stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}

func TestSetupRequiresLoginBeforeCreatingWorkspaceFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "oryxa.yaml")
	var stdout, stderr bytes.Buffer
	code := run(context.Background(), []string{"setup", "--file", path, "--org", "acme", "--no-input"}, &stdout, &stderr, func(string) string { return "" })
	if code != 3 || !strings.Contains(stderr.String(), "sign-in is required") {
		t.Fatalf("exit=%d stderr=%q", code, stderr.String())
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("setup wrote the workspace file before sign-in: %v", err)
	}
}

func TestInteractiveEmailSignInUsesNativeToken(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		switch request.URL.Path {
		case "/v1/auth/config":
			fmt.Fprint(writer, `{"identity_providers":["Password"]}`)
		case "/v1/auth/cli/password/login":
			var body map[string]string
			_ = json.NewDecoder(request.Body).Decode(&body)
			if body["email"] != "creator@example.com" || body["password"] != "password" {
				t.Error("email sign-in sent incorrect credentials")
			}
			fmt.Fprint(writer, `{"access_token":"signed-in-token"}`)
		default:
			writer.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	var stdout, stderr bytes.Buffer
	token, err := interactiveLogin(context.Background(), options{api: server.URL}, strings.NewReader("1\ncreator@example.com\npassword\n"), &stdout, &stderr)
	if err != nil || token != "signed-in-token" || strings.Contains(stdout.String()+stderr.String(), "password") || strings.Contains(stdout.String()+stderr.String(), token) {
		t.Fatalf("sign-in token or output was incorrect: %v", err)
	}
}

func TestSetupSignsUpBeforePlanningNewOrganization(t *testing.T) {
	path := filepath.Join(t.TempDir(), "oryxa.yaml")
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		switch request.URL.Path {
		case "/v1/auth/config":
			if _, err := os.Stat(path); !os.IsNotExist(err) {
				t.Error("setup created the workspace file before authentication")
			}
			fmt.Fprint(writer, `{"identity_providers":["Password"]}`)
		case "/v1/auth/cli/password/signup":
			writer.WriteHeader(http.StatusAccepted)
		case "/v1/auth/cli/password/verify":
			writer.WriteHeader(http.StatusNoContent)
		case "/v1/auth/cli/password/login":
			fmt.Fprint(writer, `{"access_token":"creator-token"}`)
		case "/v1/organizations":
			if request.Header.Get("Authorization") != "Bearer creator-token" {
				t.Error("organization snapshot did not use the signed-in account")
			}
			fmt.Fprint(writer, `{"organizations":[]}`)
		case "/v1/auth/me":
			fmt.Fprint(writer, `{"user":{"id":"creator-1","email":"creator@example.com"}}`)
		default:
			writer.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	stdin, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer stdin.Close()
	_, _ = io.WriteString(writer, "2\nCreator\ncreator@example.com\nlong-secret-password\nlong-secret-password\nemail-code\n")
	writer.Close()
	previous := os.Stdin
	os.Stdin = stdin
	defer func() { os.Stdin = previous }()
	var stdout, stderr bytes.Buffer
	code := run(context.Background(), []string{"setup", "--org", "acme", "--file", path, "--api", server.URL, "--dry-run", "--json"}, &stdout, &stderr, func(string) string { return "" })
	if code != 0 || !strings.Contains(stdout.String(), `"kind":"organization"`) {
		t.Fatalf("setup exit=%d output=%q error=%q", code, stdout.String(), stderr.String())
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("setup did not create a starter file after authentication: %v", err)
	}
}

func TestSignupCanPauseForEmailVerification(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		if request.URL.Path == "/v1/auth/config" {
			fmt.Fprint(writer, `{"identity_providers":["Password"]}`)
			return
		}
		if request.URL.Path == "/v1/auth/cli/password/signup" {
			writer.WriteHeader(http.StatusAccepted)
			return
		}
		writer.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()
	var stdout, stderr bytes.Buffer
	input := strings.NewReader("2\nCreator\ncreator@example.com\nlong-secret-password\nlong-secret-password\n\n")
	_, err := interactiveLogin(context.Background(), options{api: server.URL, json: true}, input, &stdout, &stderr)
	if !errors.Is(err, errVerificationPending) || reportError(err, &stderr) != 2 || !strings.Contains(stdout.String(), `"action":"verify_email"`) {
		t.Fatalf("paused signup: err=%v stdout=%q", err, stdout.String())
	}
}
