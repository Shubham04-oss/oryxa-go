package signin

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"golang.org/x/oauth2"

	"github.com/Shubham04-oss/oryxa-go/apps/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/apps/internal/integration"
	"github.com/Shubham04-oss/oryxa-go/internal/secrets"
)

var (
	// errLeaseHeld means another refresh holds the credential's lease.
	errLeaseHeld = errors.New("signin: another refresh holds the credential's lease")
	// errMoved means the credential moved on while it was being refreshed:
	// someone else refreshed or replaced it.
	errMoved = errors.New("signin: the credential moved on while it was being refreshed")
)

// renewable says whether a kind's token is renewed: refreshed, or minted
// again.
func renewable(kind string) bool {
	return kind == integration.KindOAuth2Code || kind == integration.KindOAuth2ClientCredentials
}

// dueForRenewal says whether a credential's access token must be renewed
// before it is used at now.
func dueForRenewal(credential Credential, now time.Time) bool {
	if _, ok := credential.Sealed[sealedAccessToken]; !ok {
		return true
	}
	return credential.ExpiresAt != nil && !now.Add(refreshBefore).Before(*credential.ExpiresAt)
}

// usable says whether a credential's access token has not expired at now.
func usable(credential Credential, now time.Time) bool {
	_, ok := credential.Sealed[sealedAccessToken]
	return ok && (credential.ExpiresAt == nil || now.Before(*credential.ExpiresAt))
}

func rejectionReason(kind string) string {
	if kind == integration.KindOAuth2Code {
		return ExpiredRefreshRejected
	}
	return ExpiredCredentialRejected
}

// material answers what a request carries for a credential: from the cache,
// by opening what is stored, or by renewing it first. One renewal runs at a
// time per connection: in this process, callers of the same version share
// it, and across processes the lease decides. A caller that finds the lease
// held waits for the holder, up to leaseWait, and uses what it saved.
func (s *Service) material(ctx context.Context, live *liveCredential, p *prepared) (material, error) {
	credential := live.get()
	var deadline <-chan time.Time
	for round := 0; ; round++ {
		if round == maxRenewRounds {
			return material{}, fmt.Errorf("%w: the credential kept changing while it was renewed", ErrUnavailable)
		}
		now := s.now().UTC()
		if held, ok := s.cache.get(credential.ConnectionID, credential.Version, now); ok {
			return held, nil
		}
		if !renewable(credential.Kind) || !dueForRenewal(credential, now) {
			held, err := s.openMaterial(ctx, credential, p)
			if err != nil {
				return material{}, err
			}
			s.cache.put(credential.ConnectionID, credential.Version, held, cacheUntil(held, now), now)
			return held, nil
		}
		if _, ok := credential.Sealed[sealedRefreshToken]; credential.Kind == integration.KindOAuth2Code && !ok {
			// Nothing can renew it: use it while it lasts.
			if usable(credential, now) {
				return s.openMaterial(ctx, credential, p)
			}
			return material{}, &Rejected{Reason: ExpiredRefreshRejected, cause: errors.New("the token expired, and there is no refresh token")}
		}

		renewed, held, err := s.renew(ctx, credential, p)
		switch {
		case err == nil:
			live.set(renewed)
			return held, nil
		case errors.Is(err, errMoved):
			// Someone else's refresh or replacement landed: read it now.
		case errors.Is(err, errLeaseHeld):
			if deadline == nil {
				timer := time.NewTimer(s.timings.leaseWait)
				defer timer.Stop()
				deadline = timer.C
			}
			select {
			case <-ctx.Done():
				return material{}, ctx.Err()
			case <-deadline:
				if usable(credential, s.now().UTC()) {
					return s.openMaterial(ctx, credential, p)
				}
				return material{}, fmt.Errorf("%w: another refresh of the credential did not finish in time", ErrUnavailable)
			case <-time.After(s.timings.leasePoll):
			}
		default:
			var rejected *Rejected
			if !errors.As(err, &rejected) && usable(credential, s.now().UTC()) {
				// The app did not answer; a token that has not expired yet
				// still works.
				s.logger.WarnContext(ctx, "renew an app credential", "connection_id", credential.ConnectionID, "error", err)
				return s.openMaterial(ctx, credential, p)
			}
			return material{}, err
		}
		current, err := s.store.Credential(ctx, credential.ConnectionID)
		if err != nil {
			return material{}, err
		}
		credential = current
		live.set(current)
	}
}

type renewal struct {
	credential Credential
	material   material
}

// renew renews a credential at one version once in this process, however
// many callers ask. It runs detached from them: once the app has been asked,
// its answer is saved even if every caller has gone, because a rotated
// refresh token that is never saved is lost.
func (s *Service) renew(ctx context.Context, credential Credential, p *prepared) (Credential, material, error) {
	key := credential.ConnectionID + "@" + strconv.FormatInt(credential.Version, 10)
	detached := context.WithoutCancel(ctx)
	results := s.refreshes.DoChan(key, func() (any, error) {
		ctx, cancel := context.WithTimeout(detached, s.timings.refresh)
		defer cancel()
		renewed, held, err := s.renewUnderLease(ctx, credential, p)
		return renewal{credential: renewed, material: held}, err
	})
	select {
	case <-ctx.Done():
		return Credential{}, material{}, ctx.Err()
	case result := <-results:
		if result.Err != nil {
			return Credential{}, material{}, result.Err
		}
		done := result.Val.(renewal)
		return done.credential, done.material, nil
	}
}

// renewUnderLease takes the credential's lease at its version, asks the app,
// and saves the answer by compare-and-swap on that version, which also
// releases the lease. A refusal counts against the credential only while it
// is still the version asked about.
func (s *Service) renewUnderLease(ctx context.Context, credential Credential, p *prepared) (Credential, material, error) {
	token, err := randomToken(18)
	if err != nil {
		return Credential{}, material{}, err
	}
	now := s.now().UTC()
	acquired, err := s.store.AcquireCredentialLease(ctx, credential.ConnectionID, credential.Version, token, now, now.Add(s.timings.lease))
	if err != nil {
		return Credential{}, material{}, err
	}
	if !acquired {
		return Credential{}, material{}, errLeaseHeld
	}
	saved := false
	defer func() {
		if saved {
			return
		}
		release, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		if err := s.store.ReleaseCredentialLease(release, credential.ConnectionID, token); err != nil {
			s.logger.WarnContext(ctx, "release an app credential's lease", "connection_id", credential.ConnectionID, "error", err)
		}
	}()

	renewed, err := s.renewAtApp(ctx, credential, p)
	if err != nil {
		if errors.Is(err, errClientRefused) {
			// Not the member's grant: the app refused Oryxa's or the
			// organization's client, which only an operator can fix.
			s.logger.ErrorContext(ctx, "an app refused an OAuth client's refresh", "connection_id", credential.ConnectionID,
				"toolkit", credential.Toolkit, "oauth_client", credential.OAuthClientID, "error", err)
		}
		if errors.Is(err, errClientRefused) || errors.Is(err, errTokenUnavailable) {
			return Credential{}, material{}, fmt.Errorf("%w: renewing the credential: %w", ErrUnavailable, err)
		}
		if !errors.Is(err, errGrantRefused) {
			return Credential{}, material{}, err
		}
		if current, readErr := s.store.Credential(ctx, credential.ConnectionID); readErr == nil && current.Version != credential.Version {
			return Credential{}, material{}, errMoved
		}
		return Credential{}, material{}, &Rejected{Reason: rejectionReason(credential.Kind), cause: err}
	}

	fresh := map[string]secrets.Secret{sealedAccessToken: renewed.access}
	if renewed.refresh != "" {
		fresh[sealedRefreshToken] = renewed.refresh
	}
	newly, err := s.sealEach(ctx, func(name string) secrets.Binding {
		return credentialBinding(credential.ConnectionID, name)
	}, fresh)
	if err != nil {
		return Credential{}, material{}, err
	}
	sealed := mergedSealed(credential.Sealed, newly)
	scopes := renewed.scopes
	if scopes == nil {
		scopes = credential.Scopes
	}
	savedAt := s.now().UTC()
	version, err := s.store.SaveRefreshedCredential(ctx, credential.ConnectionID, token, credential.Version, Refreshed{
		Sealed: sealed, Settings: credential.Settings, Scopes: scopes, ExpiresAt: renewed.expiresAt, UpdatedAt: savedAt,
	})
	if err != nil {
		if errors.Is(err, domain.ErrConflict) {
			return Credential{}, material{}, errMoved
		}
		return Credential{}, material{}, fmt.Errorf("signin: save a renewed credential: %w", err)
	}
	saved = true

	next := credential
	next.Sealed, next.Scopes, next.ExpiresAt, next.Version, next.UpdatedAt = sealed, scopes, renewed.expiresAt, version, savedAt
	held := material{token: renewed.access, expires: renewed.expiresAt}
	if held.fields, err = s.placedSecrets(ctx, next, p); err != nil {
		return Credential{}, material{}, err
	}
	s.cache.put(credential.ConnectionID, version, held, cacheUntil(held, savedAt), savedAt)
	return next, held, nil
}

// renewAtApp asks the app for a new access token: with the refresh token for
// oauth2_code, or with the client's own credentials for
// oauth2_client_credentials. A refresh token the app did not replace comes
// back empty, so it is not sealed again.
func (s *Service) renewAtApp(ctx context.Context, credential Credential, p *prepared) (grant, error) {
	oauth := p.method.OAuth2
	tokenURL, err := fillURL(oauth.TokenURL, p.values)
	if err != nil {
		return grant{}, err
	}
	switch credential.Kind {
	case integration.KindOAuth2Code:
		client, err := s.clientOf(ctx, credential, p)
		if err != nil {
			return grant{}, err
		}
		clientSecret, err := s.clientSecret(ctx, client)
		if err != nil {
			return grant{}, err
		}
		refresh, err := s.open(ctx, credentialBinding(credential.ConnectionID, sealedRefreshToken), credential.Sealed[sealedRefreshToken])
		if err != nil {
			return grant{}, err
		}
		config := &oauth2.Config{
			ClientID: client.ClientID, ClientSecret: clientSecret.Reveal(),
			Endpoint: oauth2.Endpoint{TokenURL: tokenURL.String(), AuthStyle: authStyle(oauth.ClientAuth)},
		}
		token, err := s.refreshGrant(ctx, config, refresh)
		if err != nil {
			return grant{}, err
		}
		renewed := s.grantFrom(token)
		if renewed.refresh == refresh {
			renewed.refresh = ""
		}
		return renewed, nil
	case integration.KindOAuth2ClientCredentials:
		clientSecret, err := s.open(ctx, credentialBinding(credential.ConnectionID, "client_secret"), credential.Sealed["client_secret"])
		if err != nil {
			return grant{}, err
		}
		secretValues, err := s.clientFieldSecrets(ctx, p.method, p.client)
		if err != nil {
			return grant{}, err
		}
		for _, field := range p.method.Fields {
			if sealed, ok := credential.Sealed[field.Name]; ok && field.Secret && field.Level != integration.LevelClient {
				value, err := s.open(ctx, credentialBinding(credential.ConnectionID, field.Name), sealed)
				if err != nil {
					return grant{}, err
				}
				secretValues[field.Name] = value
			}
		}
		token, err := s.mintToken(ctx, oauth, tokenURL.String(), credential.Settings["client_id"], clientSecret,
			tokenParams(p.method, p.values, secretValues))
		if err != nil {
			return grant{}, err
		}
		renewed := s.grantFrom(token)
		renewed.refresh = ""
		return renewed, nil
	}
	return grant{}, ErrUnsupportedKind
}
