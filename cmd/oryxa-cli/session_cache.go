package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/zalando/go-keyring"
)

const sessionService = "in.oryxa.cli.sessions.v1"

type credentialStore interface {
	Get(service, account string) (string, error)
	Set(service, account, value string) error
	Delete(service, account string) error
}
type systemCredentialStore struct{}

func (systemCredentialStore) Get(service, account string) (string, error) {
	return keyring.Get(service, account)
}
func (systemCredentialStore) Set(service, account, value string) error {
	return keyring.Set(service, account, value)
}
func (systemCredentialStore) Delete(service, account string) error {
	return keyring.Delete(service, account)
}

var sessionStore credentialStore = systemCredentialStore{}
var memorySessions = struct {
	sync.Mutex
	values map[string]cliSession
}{values: map[string]cliSession{}}

func sessionAccount(api string) string {
	digest := sha256.Sum256([]byte(strings.TrimRight(api, "/")))
	return hex.EncodeToString(digest[:])
}

// Only used to purge the former plaintext cache. Tokens are never read from it.
func sessionCachePath(api string, getenv func(string) string) (string, error) {
	directory := getenv("ORYXA_SESSION_DIR")
	if directory == "" {
		config, err := os.UserConfigDir()
		if err != nil {
			return "", err
		}
		directory = filepath.Join(config, "oryxa", "cli-sessions")
	}
	return filepath.Join(directory, sessionAccount(api)+".json"), nil
}

func purgeLegacySession(api string, getenv func(string) string) error {
	path, err := sessionCachePath(api, getenv)
	if err != nil {
		return err
	}
	// Anchor removal to the opened directory so renames cannot redirect it.
	root, err := os.OpenRoot(filepath.Dir(path))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	defer root.Close()
	if err := root.Remove(filepath.Base(path)); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func loadSession(api string, getenv func(string) string) (cliSession, bool, error) {
	if err := purgeLegacySession(api, getenv); err != nil {
		return cliSession{}, false, fmt.Errorf("could not remove legacy CLI session")
	}
	account := sessionAccount(api)
	memorySessions.Lock()
	session, found := memorySessions.values[account]
	memorySessions.Unlock()
	if !found {
		encoded, err := sessionStore.Get(sessionService, account)
		if errors.Is(err, keyring.ErrNotFound) {
			return cliSession{}, false, nil
		}
		if err != nil {
			return cliSession{}, false, fmt.Errorf("OS credential store is unavailable")
		}
		if len(encoded) > 16<<10 || json.Unmarshal([]byte(encoded), &session) != nil {
			return cliSession{}, false, fmt.Errorf("invalid saved CLI session")
		}
	}
	if session.AccessToken == "" || session.ExpiresAt.Before(time.Now().Add(30*time.Second)) {
		_ = clearSession(api, getenv)
		return cliSession{}, false, nil
	}
	return session, true, nil
}

func saveSession(api string, session cliSession, getenv func(string) string) error {
	if err := purgeLegacySession(api, getenv); err != nil {
		return fmt.Errorf("could not remove legacy CLI session")
	}
	if session.AccessToken == "" || session.ExpiresAt.Before(time.Now().Add(30*time.Second)) {
		return fmt.Errorf("cannot cache an expired CLI session")
	}
	account := sessionAccount(api)
	memorySessions.Lock()
	memorySessions.values[account] = session
	memorySessions.Unlock()
	encoded, err := json.Marshal(session)
	if err != nil {
		return err
	}
	// This also stays below the smallest supported credential-store item limit.
	if len(encoded) > 2400 {
		return fmt.Errorf("CLI session exceeds secure storage limit; retained in memory only")
	}
	if err := sessionStore.Set(sessionService, account, string(encoded)); err != nil {
		return fmt.Errorf("OS credential store is unavailable; CLI session retained in memory only")
	}
	return nil
}

func clearSession(api string, getenv func(string) string) error {
	account := sessionAccount(api)
	memorySessions.Lock()
	delete(memorySessions.values, account)
	memorySessions.Unlock()
	if err := purgeLegacySession(api, getenv); err != nil {
		return err
	}
	if err := sessionStore.Delete(sessionService, account); err != nil && !errors.Is(err, keyring.ErrNotFound) {
		return fmt.Errorf("could not clear OS credential store session")
	}
	return nil
}
