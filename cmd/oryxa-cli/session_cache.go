package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// The cache holds only the short-lived access token. A separate file per API
// prevents a token for one Oryxa deployment from being sent to another.
func sessionCachePath(api string, getenv func(string) string) (string, error) {
	directory := getenv("ORYXA_SESSION_DIR")
	if directory == "" {
		config, err := os.UserConfigDir()
		if err != nil {
			return "", err
		}
		directory = filepath.Join(config, "oryxa", "cli-sessions")
	}
	digest := sha256.Sum256([]byte(strings.TrimRight(api, "/")))
	return filepath.Join(directory, hex.EncodeToString(digest[:])+".json"), nil
}

func loadSession(api string, getenv func(string) string) (cliSession, bool, error) {
	path, err := sessionCachePath(api, getenv)
	if err != nil {
		return cliSession{}, false, err
	}
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return cliSession{}, false, nil
	}
	if err != nil {
		return cliSession{}, false, err
	}
	if !info.Mode().IsRegular() || runtime.GOOS != "windows" && info.Mode().Perm()&0077 != 0 {
		return cliSession{}, false, fmt.Errorf("CLI session file is not a private regular file")
	}
	file, err := os.Open(path)
	if err != nil {
		return cliSession{}, false, err
	}
	defer file.Close()
	decoder := json.NewDecoder(io.LimitReader(file, 16<<10))
	decoder.DisallowUnknownFields()
	var session cliSession
	if err := decoder.Decode(&session); err != nil {
		return cliSession{}, false, fmt.Errorf("read CLI session: %w", err)
	}
	if session.AccessToken == "" || session.ExpiresAt.Before(time.Now().Add(30*time.Second)) {
		_ = os.Remove(path)
		return cliSession{}, false, nil
	}
	return session, true, nil
}

func saveSession(api string, session cliSession, getenv func(string) string) error {
	path, err := sessionCachePath(api, getenv)
	if err != nil {
		return err
	}
	if session.AccessToken == "" || session.ExpiresAt.Before(time.Now().Add(30*time.Second)) {
		return fmt.Errorf("cannot cache an expired CLI session")
	}
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0700); err != nil {
		return err
	}
	info, err := os.Lstat(directory)
	if err != nil {
		return err
	}
	if !info.IsDir() || runtime.GOOS != "windows" && info.Mode().Perm()&0077 != 0 {
		return fmt.Errorf("CLI session directory must be private")
	}
	temporary, err := os.CreateTemp(directory, ".session-*")
	if err != nil {
		return err
	}
	defer os.Remove(temporary.Name())
	if err := json.NewEncoder(temporary).Encode(session); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporary.Name(), path)
}

func clearSession(api string, getenv func(string) string) error {
	path, err := sessionCachePath(api, getenv)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}
