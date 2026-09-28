package signin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/Shubham04-oss/oryxa-go/apps/internal/integration"
	"github.com/Shubham04-oss/oryxa-go/internal/secrets"
)

// Revocation is how disconnecting went at the app.
type Revocation struct {
	// ProviderRevoked says the app confirmed the credential no longer
	// works: it revoked it, or it was already gone. It is false for a key or
	// a password, which only its owner can revoke, for an app that cannot
	// revoke, and for a grant another connection still shares.
	ProviderRevoked bool
	// Pending says the app did not answer. The sealed credential is kept so
	// RetryRevocations can try again once the connection has ended; nothing
	// is sent with it meanwhile.
	Pending bool
}

// revocation is what revoking at an app takes.
type revocation struct {
	manifest     integration.Manifest
	method       integration.Method
	values       map[string]string
	connectionID string
	clientID     string
	clientSecret secrets.Secret
	access       secrets.Secret
	refresh      secrets.Secret
	// label is the account's, or "" while it is not known.
	label string
}

type revokeOutcome int

const (
	// revokeSkipped: nothing here can revoke it, or revoking would harm
	// another connection.
	revokeSkipped revokeOutcome = iota
	// revokeDone: the app revoked it, or nothing that works was left.
	revokeDone
	// revokeFailed: the app did not answer as it should; try again later.
	revokeFailed
)

// slackGoneErrors are auth.revoke's answers for a token already gone.
var slackGoneErrors = []string{"invalid_auth", "token_revoked", "token_expired", "account_inactive", "not_authed"}

// Revoke disconnects a connection's credential: it revokes it at the app, in
// the manifest's revoke_style, and forgets it. For rfc7009, the refresh token
// is revoked when there is one, as RFC 7009 has every server support; an
// expired access token that another style revokes by is renewed first, so
// what is live gets revoked. Where revoking one token revokes the whole
// grant (Google: see grantWide), a grant another live connection shares for
// the same account through the same client is left alone. A revocation the
// app did not answer never blocks the removal: Revoke answers Pending, and
// keeps the sealed credential for RetryRevocations. A connection with no
// credential is not an error.
func (s *Service) Revoke(ctx context.Context, connectionID string) (Revocation, error) {
	credential, err := s.store.Credential(ctx, connectionID)
	if errors.Is(err, ErrNoCredential) {
		return Revocation{}, nil
	}
	if err != nil {
		return Revocation{}, err
	}
	s.cache.forget(connectionID)
	outcome := s.revokeStored(ctx, credential)
	if outcome == revokeFailed {
		failures := credential.CheckFailures + 1
		if err := s.store.RecordCredentialCheck(ctx, connectionID, s.now().UTC().Add(backoff(failures)), failures); err != nil {
			s.logger.WarnContext(ctx, "schedule an app revocation's retry", "connection_id", connectionID, "error", err)
		}
		return Revocation{Pending: true}, nil
	}
	revoked := outcome == revokeDone
	if _, err := s.store.DeleteCredential(ctx, connectionID); err != nil {
		return Revocation{ProviderRevoked: revoked}, err
	}
	return Revocation{ProviderRevoked: revoked}, nil
}

// RetryRevocations tries again the revocations the app did not answer, for
// connections that have ended (removed or failed), and forgets each
// credential once the app has answered, or after maxRevokeAttempts. It
// answers how many it tried.
func (s *Service) RetryRevocations(ctx context.Context, limit int) (int, error) {
	now := s.now().UTC()
	claimed, err := s.store.ClaimRevocations(ctx, now, now.Add(claimFor), limit)
	if err != nil {
		return 0, err
	}
	for _, credential := range claimed {
		s.cache.forget(credential.ConnectionID)
		outcome := s.revokeStored(ctx, credential)
		failures := credential.CheckFailures + 1
		if outcome == revokeFailed && failures < maxRevokeAttempts {
			if err := s.store.RecordCredentialCheck(ctx, credential.ConnectionID, s.now().UTC().Add(backoff(failures)), failures); err != nil {
				s.logger.WarnContext(ctx, "schedule an app revocation's retry", "connection_id", credential.ConnectionID, "error", err)
			}
			continue
		}
		if outcome == revokeFailed {
			s.logger.WarnContext(ctx, "gave up revoking an app credential at the app; forgetting it",
				"connection_id", credential.ConnectionID, "toolkit", credential.Toolkit, "attempts", failures)
		}
		if _, err := s.store.DeleteCredential(ctx, credential.ConnectionID); err != nil {
			s.logger.WarnContext(ctx, "forget a revoked app credential", "connection_id", credential.ConnectionID, "error", err)
		}
	}
	return len(claimed), nil
}

// revokeStored revokes a stored credential at its app.
func (s *Service) revokeStored(ctx context.Context, credential Credential) revokeOutcome {
	if !renewable(credential.Kind) {
		// A key or a password is its owner's; nothing here revokes it.
		return revokeSkipped
	}
	p, err := s.prepareStored(ctx, credential)
	if errors.Is(err, ErrUnknownToolkit) || errors.Is(err, ErrUnsupportedKind) {
		s.logger.WarnContext(ctx, "no manifest to revoke an app credential by", "connection_id", credential.ConnectionID)
		return revokeSkipped
	}
	if err != nil {
		s.logger.WarnContext(ctx, "prepare an app revocation", "connection_id", credential.ConnectionID, "error", err)
		return revokeFailed
	}
	style := revokeStyle(p.method.OAuth2)
	if style == integration.RevokeNone {
		return revokeSkipped
	}
	r := revocation{
		manifest: p.manifest, method: p.method, values: p.values, connectionID: credential.ConnectionID,
		label: credential.Settings[integration.SettingAccountLabel],
	}
	switch credential.Kind {
	case integration.KindOAuth2Code:
		client, err := s.clientOf(ctx, credential, p)
		if err != nil {
			return revokeFailed
		}
		r.clientID = client.ClientID
		if r.clientSecret, err = s.clientSecret(ctx, client); err != nil {
			return revokeFailed
		}
	default:
		r.clientID = credential.Settings["client_id"]
		if r.clientSecret, err = s.open(ctx, credentialBinding(credential.ConnectionID, "client_secret"), credential.Sealed["client_secret"]); err != nil {
			return revokeFailed
		}
	}
	if s.grantWide(p.method) {
		if r.label == "" {
			return revokeSkipped
		}
		shared, err := s.store.GrantShared(ctx, credential.ConnectionID, r.clientID, r.label)
		if err != nil {
			return revokeFailed
		}
		if shared {
			s.logger.InfoContext(ctx, "left an app grant another connection shares", "connection_id", credential.ConnectionID)
			return revokeSkipped
		}
	}
	if sealed, ok := credential.Sealed[sealedRefreshToken]; ok {
		if r.refresh, err = s.open(ctx, credentialBinding(credential.ConnectionID, sealedRefreshToken), sealed); err != nil {
			return revokeFailed
		}
	}
	if style == integration.RevokeRFC7009 && r.refresh != "" {
		return s.revokeAt(ctx, r)
	}
	switch now := s.now().UTC(); {
	case usable(credential, now):
		if r.access, err = s.open(ctx, credentialBinding(credential.ConnectionID, sealedAccessToken), credential.Sealed[sealedAccessToken]); err != nil {
			return revokeFailed
		}
	case credential.Kind == integration.KindOAuth2Code && r.refresh != "":
		// The access token has expired, but the refresh token behind it
		// has not, and these styles revoke by access token: renew it, to
		// revoke what is live.
		held, err := s.material(ctx, &liveCredential{credential: credential}, p)
		var rejected *Rejected
		switch {
		case errors.As(err, &rejected):
			return revokeDone
		case err != nil:
			return revokeFailed
		}
		r.access = held.token
	default:
		// Nothing that works is left at the app.
		return revokeDone
	}
	return s.revokeAt(ctx, r)
}

// discard revokes at once, best effort, tokens an app issued that are not
// being kept.
func (s *Service) discard(ctx context.Context, r revocation) {
	if revokeStyle(r.method.OAuth2) == integration.RevokeNone {
		return
	}
	if s.grantWide(r.method) {
		// Revoking would revoke the whole grant, which another connection
		// to the same account may share. Without the account's label
		// nothing can tell, so the token is left to lapse.
		if r.label == "" {
			return
		}
		if shared, err := s.store.GrantShared(ctx, r.connectionID, r.clientID, r.label); err != nil || shared {
			return
		}
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), callTimeout)
	defer cancel()
	if s.revokeAt(ctx, r) == revokeFailed {
		s.logger.WarnContext(ctx, "revoke tokens an app issued for a sign-in that failed", "connection_id", r.connectionID)
	}
}

// grantWide says whether revoking a method's token revokes the whole grant
// the account gave its client, taking every other token of it too. That is
// Google's case, and what marks it: one client registration that several
// toolkits sign in through (gmail, googledrive and googlecalendar all use
// google), revoked RFC 7009's way. Every other app revokes one token.
func (s *Service) grantWide(method integration.Method) bool {
	if method.Kind != integration.KindOAuth2Code || method.OAuth2 == nil || method.OAuth2.Client == "" ||
		revokeStyle(method.OAuth2) != integration.RevokeRFC7009 {
		return false
	}
	toolkits := 0
	for _, toolkit := range s.manifests.Toolkits() {
		registered, ok := s.manifests.Lookup(toolkit)
		if !ok {
			continue
		}
		if slices.ContainsFunc(registered.Manifest().Auth.Methods, func(other integration.Method) bool {
			return other.OAuth2 != nil && other.OAuth2.Client == method.OAuth2.Client
		}) {
			toolkits++
		}
	}
	return toolkits > 1
}

func revokeStyle(oauth *integration.OAuth2) string {
	if oauth == nil {
		return integration.RevokeNone
	}
	if oauth.RevokeStyle == "" {
		return integration.RevokeRFC7009
	}
	return oauth.RevokeStyle
}

// revokeAt asks the app to revoke, in its style.
func (s *Service) revokeAt(ctx context.Context, r revocation) revokeOutcome {
	style := revokeStyle(r.method.OAuth2)
	request, err := revokeRequest(ctx, style, r)
	if err != nil {
		s.logger.WarnContext(ctx, "build an app revocation", "connection_id", r.connectionID, "error", err)
		return revokeFailed
	}
	if request == nil {
		return revokeDone
	}
	response, err := s.endpoints.Do(request)
	if err != nil {
		s.logger.WarnContext(ctx, "revoke at an app", "connection_id", r.connectionID, "error", err)
		return revokeFailed
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(response.Body, 64<<10))
	outcome := classifyRevoke(style, response.StatusCode, body)
	if outcome == revokeFailed {
		s.logger.WarnContext(ctx, "an app did not revoke", "connection_id", r.connectionID, "status", response.StatusCode)
	}
	return outcome
}

// revokeRequest builds a style's revocation, or nil when there is no token
// to revoke.
func revokeRequest(ctx context.Context, style string, r revocation) (*http.Request, error) {
	oauth := r.method.OAuth2
	fillRevokeURL := func() (string, error) {
		target, err := fillURL(oauth.RevokeURL, r.values)
		if err != nil {
			return "", err
		}
		return target.String(), nil
	}
	switch style {
	case integration.RevokeRFC7009:
		token, hint := r.refresh, "refresh_token"
		if token == "" {
			token, hint = r.access, "access_token"
		}
		if token == "" {
			return nil, nil
		}
		target, err := fillRevokeURL()
		if err != nil {
			return nil, err
		}
		form := url.Values{"token": {token.Reveal()}, "token_type_hint": {hint}}
		if oauth.ClientAuth != integration.ClientAuthHeader {
			form.Set("client_id", r.clientID)
			if r.clientSecret != "" {
				form.Set("client_secret", r.clientSecret.Reveal())
			}
		}
		request, err := http.NewRequestWithContext(ctx, http.MethodPost, target, strings.NewReader(form.Encode()))
		if err != nil {
			return nil, err
		}
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		if oauth.ClientAuth == integration.ClientAuthHeader {
			// RFC 6749 section 2.3.1: each is form-encoded first.
			request.SetBasicAuth(url.QueryEscape(r.clientID), url.QueryEscape(r.clientSecret.Reveal()))
		}
		return request, nil
	case integration.RevokeRFC7009JSON:
		if r.access == "" {
			return nil, nil
		}
		target, err := fillRevokeURL()
		if err != nil {
			return nil, err
		}
		body := map[string]string{"token": r.access.Reveal()}
		if oauth.ClientAuth != integration.ClientAuthHeader {
			body["client_id"] = r.clientID
			if r.clientSecret != "" {
				body["client_secret"] = r.clientSecret.Reveal()
			}
		}
		request, err := jsonRequest(ctx, http.MethodPost, target, body)
		if err != nil {
			return nil, err
		}
		if oauth.ClientAuth == integration.ClientAuthHeader {
			request.SetBasicAuth(r.clientID, r.clientSecret.Reveal())
		}
		return request, nil
	case integration.RevokeGitHubToken:
		if r.access == "" {
			return nil, nil
		}
		base, err := fillURL(r.manifest.APIBase, r.values)
		if err != nil {
			return nil, err
		}
		target := strings.TrimRight(base.String(), "/") + "/applications/" + url.PathEscape(r.clientID) + "/token"
		request, err := jsonRequest(ctx, http.MethodDelete, target, map[string]string{"access_token": r.access.Reveal()})
		if err != nil {
			return nil, err
		}
		request.Header.Set("Accept", "application/vnd.github+json")
		request.SetBasicAuth(r.clientID, r.clientSecret.Reveal())
		return request, nil
	case integration.RevokeSlackAuthRevoke:
		if r.access == "" {
			return nil, nil
		}
		target, err := fillRevokeURL()
		if err != nil {
			return nil, err
		}
		request, err := http.NewRequestWithContext(ctx, http.MethodPost, target, nil)
		if err != nil {
			return nil, err
		}
		request.Header.Set("Authorization", "Bearer "+r.access.Reveal())
		return request, nil
	}
	return nil, errors.New("signin: an unknown revoke style")
}

func jsonRequest(ctx context.Context, method, target string, body map[string]string) (*http.Request, error) {
	encoded, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(ctx, method, target, bytes.NewReader(encoded))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", "application/json")
	return request, nil
}

// classifyRevoke reads a style's answer.
func classifyRevoke(style string, status int, body []byte) revokeOutcome {
	switch style {
	case integration.RevokeGitHubToken:
		switch status {
		case http.StatusNoContent, http.StatusOK:
			return revokeDone
		case http.StatusNotFound, http.StatusUnprocessableEntity:
			// Already gone.
			return revokeDone
		}
		return revokeFailed
	case integration.RevokeSlackAuthRevoke:
		var answer struct {
			OK    bool   `json:"ok"`
			Error string `json:"error"`
		}
		if status != http.StatusOK || json.Unmarshal(body, &answer) != nil {
			return revokeFailed
		}
		if answer.OK || slices.Contains(slackGoneErrors, answer.Error) {
			return revokeDone
		}
		return revokeFailed
	default:
		if status >= 200 && status < 300 {
			return revokeDone
		}
		var answer struct {
			Error string `json:"error"`
		}
		if status == http.StatusBadRequest && json.Unmarshal(body, &answer) == nil &&
			(answer.Error == "invalid_token" || answer.Error == "invalid_grant") {
			// Already gone.
			return revokeDone
		}
		return revokeFailed
	}
}

// backoff is the wait after failures in a row: minBackoff, doubling up to
// maxBackoff.
func backoff(failures int) time.Duration {
	wait := minBackoff
	for range max(failures-1, 0) {
		wait *= 2
		if wait >= maxBackoff {
			return maxBackoff
		}
	}
	return wait
}
