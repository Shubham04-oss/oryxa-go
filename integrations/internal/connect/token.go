package connect

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Shubham04-oss/oryxa-go/integrations/internal/orgapps"
	"github.com/Shubham04-oss/oryxa-go/integrations/manifest"
	"github.com/Shubham04-oss/oryxa-go/internal/secrets"
)

// maxAnswer caps what an app's token endpoint may answer.
const maxAnswer = 1 << 20

var (
	// errGrantRefused is the app refusing the code or refresh token itself.
	errGrantRefused = errors.New("the app refused the grant")
	// errUnavailable is the app not answering as an app does: no answer, a 5xx or a 429.
	errUnavailable = errors.New("the app did not answer")
)

// grant is what a token endpoint answered.
type grant struct {
	access    secrets.Secret
	refresh   secrets.Secret
	expiresAt *time.Time
	scopes    []string
}

// token posts form to the app's token endpoint with the client's credentials
// in the body (ponytail: every app takes them there; one that wants HTTP Basic
// needs a manifest field for it).
func (s *Service) token(ctx context.Context, m *manifest.Manifest, client orgapps.ClientCredentials, form url.Values) (grant, error) {
	form.Set("client_id", client.ClientID)
	form.Set("client_secret", client.Secret.Reveal())
	status, body, err := s.post(ctx, m.Auth.OAuth2.TokenURL, form)
	if err != nil {
		return grant{}, err
	}
	var answer struct {
		AccessToken  secrets.Secret `json:"access_token"`
		RefreshToken secrets.Secret `json:"refresh_token"`
		ExpiresIn    json.Number    `json:"expires_in"`
		Scope        string         `json:"scope"`
		Error        string         `json:"error"`
	}
	decodeErr := json.Unmarshal(body, &answer)
	switch {
	case status == http.StatusTooManyRequests || status >= 500:
		return grant{}, fmt.Errorf("%w: status %d", errUnavailable, status)
	case answer.Error != "":
		// GitHub answers 200 with an error; the others answer 400 or 401.
		// The code is a fixed word of RFC 6749, never the app's free text.
		return grant{}, &tokenError{code: answer.Error}
	case status != http.StatusOK || decodeErr != nil || answer.AccessToken == "":
		return grant{}, fmt.Errorf("%w: status %d with no usable token", errGrantRefused, status)
	}
	out := grant{access: answer.AccessToken, refresh: answer.RefreshToken}
	if seconds, err := answer.ExpiresIn.Int64(); err == nil && seconds > 0 {
		at := s.now().Add(time.Duration(seconds) * time.Second)
		out.expiresAt = &at
	}
	if answer.Scope != "" {
		separator := " "
		if m.Auth.OAuth2.ScopeSeparator != "" {
			separator = m.Auth.OAuth2.ScopeSeparator
		}
		out.scopes = strings.Split(answer.Scope, separator)
	}
	return out, nil
}

// tokenError is an app's OAuth error code. invalid_grant, and the codes that
// stand for it, mean the grant is gone; anything else is the client's or the
// request's fault, which a retry will not fix but a member signing in again
// will not either, so it counts as a failure of the refresh.
type tokenError struct{ code string }

func (e *tokenError) Error() string { return "the app answered " + sanitize(e.code) }

func (e *tokenError) Is(target error) bool {
	return target == errGrantRefused && e.code == "invalid_grant"
}

// sanitize keeps an error code short and plain before it is logged.
func sanitize(code string) string {
	if len(code) > 40 {
		code = code[:40]
	}
	return strings.Map(func(r rune) rune {
		if r == '_' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' {
			return r
		}
		return '?'
	}, code)
}

// post sends a form to an app and answers its status and capped body. The
// request and its form are never in the error.
func (s *Service) post(ctx context.Context, target string, form url.Values) (int, []byte, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, target, strings.NewReader(form.Encode()))
	if err != nil {
		return 0, nil, err
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("Accept", "application/json")
	response, err := s.config.Client.Do(request)
	if err != nil {
		var urlErr *url.Error
		if errors.As(err, &urlErr) {
			err = urlErr.Err
		}
		return 0, nil, fmt.Errorf("%w: %w", errUnavailable, err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, maxAnswer))
	if err != nil {
		return 0, nil, fmt.Errorf("%w: %w", errUnavailable, err)
	}
	return response.StatusCode, body, nil
}

// verify is the manifest's cheap test call, so a sign-in that yields a token
// that does not work never becomes a connection.
func (s *Service) verify(ctx context.Context, m *manifest.Manifest, access secrets.Secret) error {
	if m.Verify == nil {
		return nil
	}
	target, err := url.Parse(strings.TrimRight(m.BaseURL, "/") + m.Verify.Path)
	if err != nil || target.Scheme != "https" || !m.AllowsHost(target.Hostname()) {
		return fmt.Errorf("%w: the manifest's verify call is not on its allowlist", errUnavailable)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	if err != nil {
		return err
	}
	request.Header.Set("Authorization", "Bearer "+access.Reveal())
	response, err := s.config.Client.Do(request)
	if err != nil {
		return fmt.Errorf("%w: verify call", errUnavailable)
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, maxAnswer))
	switch {
	case response.StatusCode == http.StatusTooManyRequests || response.StatusCode >= 500:
		return fmt.Errorf("%w: verify call status %d", errUnavailable, response.StatusCode)
	case response.StatusCode >= 300:
		return fmt.Errorf("the app refused the token: verify call status %d", response.StatusCode)
	}
	return nil
}

// saveToken seals a token for its row and upserts it, moving its version on.
func (s *Service) saveToken(ctx context.Context, tx pgx.Tx, org, connection, kind string, token secrets.Secret, expiresAt *time.Time) error {
	sealed, err := s.sealer.Seal(ctx, credentialWhere(org, connection, kind), []byte(token.Reveal()))
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO integrations.credentials (connection_id, kind, sealed, expires_at)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (connection_id, kind) DO UPDATE
		SET sealed = EXCLUDED.sealed, expires_at = EXCLUDED.expires_at, version = integrations.credentials.version + 1`,
		connection, kind, sealed, expiresAt)
	return err
}
