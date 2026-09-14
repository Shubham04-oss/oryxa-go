package providers

// This file is the GitHub App installation flow: installing the app names an
// installation, not a user consent, so it is minted and checked with a JWT
// the app signs with its own private key rather than a client secret.

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/Shubham04-oss/oryxa-go/connections/internal/domain"
)

var installationSlugPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,99}$`)

// InstallationURL is where a member installs a GitHub App.
func (client Client) InstallationURL(provider Provider, request InstallationRequest) (string, error) {
	if provider.Installation == nil {
		return "", fmt.Errorf("%s does not support installations: %w", provider.Name, domain.ErrInvalid)
	}
	if !installationSlugPattern.MatchString(request.AppSlug) {
		return "", fmt.Errorf("%w: GitHub App slug is invalid", domain.ErrInvalid)
	}
	raw := strings.ReplaceAll(provider.Installation.InstallURL, "{slug}", request.AppSlug)
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return "", fmt.Errorf("%w: install URL is invalid", domain.ErrInvalid)
	}
	query := parsed.Query()
	query.Set("state", request.State)
	parsed.RawQuery = query.Encode()
	return parsed.String(), nil
}

// UserInstallations lists the installation IDs a user token can reach, so
// complete can check that a forged installation_id on the redirect really
// belongs to the user who authorized it.
func (client Client) UserInstallations(ctx context.Context, provider Provider, user Grant) ([]string, error) {
	if provider.Installation == nil {
		return nil, fmt.Errorf("%s does not support installations: %w", provider.Name, domain.ErrInvalid)
	}
	apiURL := strings.TrimRight(provider.Installation.APIURL, "/")
	header := http.Header{"Accept": {"application/vnd.github+json"}, "X-GitHub-Api-Version": {"2022-11-28"}}
	var ids []string
	const maxPages = 10
	for page := 1; page <= maxPages; page++ {
		var result struct {
			Installations []struct {
				ID int64 `json:"id"`
			} `json:"installations"`
		}
		endpoint := fmt.Sprintf("%s/user/installations?per_page=100&page=%d", apiURL, page)
		if err := callJSON(ctx, client.httpClient(), http.MethodGet, endpoint, bearerCredential(user.AccessToken), header, nil, &result); err != nil {
			return nil, err
		}
		for _, installation := range result.Installations {
			ids = append(ids, strconv.FormatInt(installation.ID, 10))
		}
		if len(result.Installations) < 100 {
			break
		}
	}
	return ids, nil
}

// Installation returns the account an installation belongs to.
func (client Client) Installation(ctx context.Context, provider Provider, app GitHubApp, installationID string) (domain.ProviderAccount, error) {
	if provider.Installation == nil {
		return domain.ProviderAccount{}, fmt.Errorf("%s does not support installations: %w", provider.Name, domain.ErrInvalid)
	}
	if !validInstallationID(installationID) {
		return domain.ProviderAccount{}, fmt.Errorf("%w: installation ID must be digits", domain.ErrInvalid)
	}
	jwt, err := client.appJWT(app)
	if err != nil {
		return domain.ProviderAccount{}, err
	}
	var result struct {
		Account struct {
			ID    int64  `json:"id"`
			Login string `json:"login"`
		} `json:"account"`
	}
	endpoint := strings.TrimRight(provider.Installation.APIURL, "/") + "/app/installations/" + installationID
	if _, err := githubAppRequest(ctx, client.httpClient(), http.MethodGet, endpoint, jwt, &result); err != nil {
		return domain.ProviderAccount{}, err
	}
	if result.Account.Login == "" {
		return domain.ProviderAccount{}, fmt.Errorf("%w: installation account is incomplete", domain.ErrProviderUnavailable)
	}
	return domain.ProviderAccount{ID: installationID, Name: result.Account.Login}, nil
}

// InstallationToken mints a token for an installation.
func (client Client) InstallationToken(ctx context.Context, provider Provider, app GitHubApp, installationID string) (Grant, error) {
	if provider.Installation == nil {
		return Grant{}, fmt.Errorf("%s does not support installations: %w", provider.Name, domain.ErrInvalid)
	}
	if !validInstallationID(installationID) {
		return Grant{}, fmt.Errorf("%w: installation ID must be digits", domain.ErrInvalid)
	}
	jwt, err := client.appJWT(app)
	if err != nil {
		return Grant{}, err
	}
	var result struct {
		Token     string `json:"token"`
		ExpiresAt string `json:"expires_at"`
	}
	endpoint := strings.TrimRight(provider.Installation.APIURL, "/") + "/app/installations/" + installationID + "/access_tokens"
	status, err := githubAppRequest(ctx, client.httpClient(), http.MethodPost, endpoint, jwt, &result)
	if err != nil {
		return Grant{}, err
	}
	if status != http.StatusCreated || result.Token == "" {
		return Grant{}, fmt.Errorf("%w: installation token response is incomplete", domain.ErrProviderUnavailable)
	}
	expiry, err := time.Parse(time.RFC3339, result.ExpiresAt)
	if err != nil {
		return Grant{}, fmt.Errorf("%w: installation token expiry is invalid", domain.ErrProviderUnavailable)
	}
	return Grant{AccessToken: result.Token, TokenType: "Bearer", Expiry: expiry}, nil
}

// ParsePrivateKey checks that a GitHub App private key is an RSA PEM key.
func ParsePrivateKey(pem string) error {
	_, err := parseRSAPrivateKey(pem)
	return err
}

// parseRSAPrivateKey decodes a PEM-encoded RSA private key: PKCS#1 ("RSA
// PRIVATE KEY") or PKCS#8 ("PRIVATE KEY") holding an RSA key. The key
// material itself never appears in an error.
func parseRSAPrivateKey(pemText string) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode([]byte(pemText))
	if block == nil {
		return nil, fmt.Errorf("%w: private key must be a PEM block", domain.ErrInvalid)
	}
	switch block.Type {
	case "RSA PRIVATE KEY":
		key, err := x509.ParsePKCS1PrivateKey(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("%w: private key is not a valid RSA key", domain.ErrInvalid)
		}
		return key, nil
	case "PRIVATE KEY":
		parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("%w: private key is not a valid PKCS8 key", domain.ErrInvalid)
		}
		key, ok := parsed.(*rsa.PrivateKey)
		if !ok {
			return nil, fmt.Errorf("%w: private key must be an RSA key", domain.ErrInvalid)
		}
		return key, nil
	default:
		return nil, fmt.Errorf("%w: private key must be an RSA PEM key", domain.ErrInvalid)
	}
}

// appJWT signs a short-lived JSON Web Token identifying a GitHub App, as
// GitHub's app endpoints require in place of a client secret.
func (client Client) appJWT(app GitHubApp) (string, error) {
	if !isDigits(app.AppID) {
		return "", fmt.Errorf("%w: GitHub App ID must be digits", domain.ErrInvalid)
	}
	key, err := parseRSAPrivateKey(app.PrivateKey)
	if err != nil {
		return "", err
	}
	now := client.now()
	claims, err := json.Marshal(struct {
		IssuedAt  int64  `json:"iat"`
		ExpiresAt int64  `json:"exp"`
		Issuer    string `json:"iss"`
	}{now.Add(-60 * time.Second).Unix(), now.Add(9 * time.Minute).Unix(), app.AppID})
	if err != nil {
		return "", fmt.Errorf("%w: could not build app JWT claims", domain.ErrProviderUnavailable)
	}
	payload := jwtHeader + "." + base64.RawURLEncoding.EncodeToString(claims)
	sum := sha256.Sum256([]byte(payload))
	signature, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, sum[:])
	if err != nil {
		return "", fmt.Errorf("%w: could not sign app JWT", domain.ErrProviderUnavailable)
	}
	return payload + "." + base64.RawURLEncoding.EncodeToString(signature), nil
}

// jwtHeader is the fixed RS256 JOSE header {"alg":"RS256","typ":"JWT"},
// base64url-encoded once since it never changes.
var jwtHeader = base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256","typ":"JWT"}`))

// githubAppRequest sends a request authenticated as a GitHub App and decodes
// a bounded JSON response into target, when target is not nil. A 404 means
// the installation is gone; a 401 or 403 means GitHub refused the app's JWT;
// anything else outside 2xx means GitHub is unavailable. It returns the
// response status so a 201-only caller (minting a token) can tell an
// unexpected-but-successful status from the one the API contract promises.
func githubAppRequest(ctx context.Context, client *http.Client, method, endpoint, jwt string, target any) (int, error) {
	request, err := http.NewRequestWithContext(ctx, method, endpoint, nil)
	if err != nil {
		return 0, fmt.Errorf("%w: provider request could not be built", domain.ErrProviderUnavailable)
	}
	request.Header.Set("Authorization", "Bearer "+jwt)
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	response, err := client.Do(request)
	if err != nil {
		return 0, fmt.Errorf("%w: provider request failed", domain.ErrProviderUnavailable)
	}
	defer response.Body.Close()
	payload, readErr := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	switch {
	case response.StatusCode == http.StatusNotFound:
		return response.StatusCode, fmt.Errorf("%w: %w", domain.ErrCredentialRejected, ErrInstallationNotFound)
	case response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden:
		return response.StatusCode, fmt.Errorf("%w: GitHub App credentials were refused", domain.ErrCredentialRejected)
	case response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices:
		return response.StatusCode, fmt.Errorf("%w: GitHub returned %d", domain.ErrProviderUnavailable, response.StatusCode)
	case readErr != nil || len(payload) > maxResponseBytes:
		return response.StatusCode, fmt.Errorf("%w: provider response could not be read", domain.ErrProviderUnavailable)
	}
	if target != nil && json.Unmarshal(payload, target) != nil {
		return response.StatusCode, fmt.Errorf("%w: provider response is not valid", domain.ErrProviderUnavailable)
	}
	return response.StatusCode, nil
}

// validInstallationID reports whether id is a valid GitHub installation ID:
// digits only, at most 20 characters.
func validInstallationID(id string) bool {
	return id != "" && len(id) <= 20 && isDigits(id)
}

func isDigits(value string) bool {
	if value == "" {
		return false
	}
	for _, character := range value {
		if character < '0' || character > '9' {
			return false
		}
	}
	return true
}
