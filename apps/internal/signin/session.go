package signin

import (
	"context"
	"fmt"
	"maps"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/Shubham04-oss/oryxa-go/apps/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/apps/internal/integration"
	"github.com/Shubham04-oss/oryxa-go/internal/observability"
	"github.com/Shubham04-oss/oryxa-go/internal/secrets"
)

// prepared is what using a credential needs besides what is sealed: its
// manifest and method, the OAuth client it came through when the method has
// client-level fields, every value that is not secret, and api_base filled
// from them.
type prepared struct {
	manifest integration.Manifest
	method   integration.Method
	client   *OAuthClient
	// values are the settings, and the client's client-level fields that
	// are not secret.
	values map[string]string
	base   *url.URL
	// host is the credential's own host, as canonicalHost spells it: the
	// only one it is ever sent to.
	host string
}

func (s *Service) prepare(manifest integration.Manifest, method integration.Method, client *OAuthClient, settings map[string]string) (*prepared, error) {
	values := merged(clientValues(method, client), settings)
	base, err := fillURL(manifest.APIBase, values)
	if err != nil {
		return nil, err
	}
	return &prepared{manifest: manifest, method: method, client: client, values: values, base: base, host: canonicalHost(base)}, nil
}

// prepareStored prepares a stored credential. Its OAuth client is read only
// when the method has client-level fields; a refresh or a revocation reads
// it when it needs it.
func (s *Service) prepareStored(ctx context.Context, credential Credential) (*prepared, error) {
	manifest, method, err := s.method(credential.Toolkit, credential.Kind)
	if err != nil {
		return nil, err
	}
	var client *OAuthClient
	if credential.OAuthClientID != "" && hasClientFields(method) {
		row, err := s.store.OAuthClient(ctx, credential.OAuthClientID)
		if err != nil {
			return nil, err
		}
		client = &row
	}
	return s.prepare(manifest, method, client, credential.Settings)
}

// clientOf answers the OAuth client a stored credential was issued through.
func (s *Service) clientOf(ctx context.Context, credential Credential, p *prepared) (OAuthClient, error) {
	if p.client != nil {
		return *p.client, nil
	}
	if credential.OAuthClientID == "" {
		return OAuthClient{}, fmt.Errorf("%w: the credential names no OAuth client", ErrNoClient)
	}
	return s.store.OAuthClient(ctx, credential.OAuthClientID)
}

// canonicalHost spells a URL's host as the credential's host is compared:
// lower case, with the port its scheme implies.
func canonicalHost(target *url.URL) string {
	port := target.Port()
	if port == "" {
		port = "443"
		if target.Scheme == "http" {
			port = "80"
		}
	}
	return strings.ToLower(target.Hostname()) + ":" + port
}

// SessionOption adjusts a Session.
type SessionOption func(*sessionOptions)

type sessionOptions struct {
	readOnly  bool
	readPOSTs []string
}

// ReadOnly makes a session for a read tool: it sends GET and HEAD, and POST
// only to readPOSTs, the tool's read_posts under api_base, and refuses
// everything else with ErrReadOnly before anything is sent.
func ReadOnly(readPOSTs ...string) SessionOption {
	return func(options *sessionOptions) {
		options.readOnly = true
		options.readPOSTs = append(options.readPOSTs, readPOSTs...)
	}
}

// Session answers what an integration calls its app with, as one active
// connection. Its HTTP places the credential in the innermost transport,
// below observability.Transport and above safehttp's, and only on requests
// to the credential's own host over HTTPS, so a redirect to another host
// never carries it. An access token due to expire is renewed first, under
// the lease. A refresh the app refuses answers a *Rejected, which is
// integration.ErrNeedsReconnect. Each call that succeeds puts off the
// connection's next health check.
func (s *Service) Session(ctx context.Context, connectionID string, options ...SessionOption) (integration.Session, error) {
	var chosen sessionOptions
	for _, option := range options {
		option(&chosen)
	}
	credential, err := s.store.Credential(ctx, connectionID)
	if err != nil {
		return integration.Session{}, err
	}
	switch domain.ConnectionStatus(credential.Status) {
	case domain.ConnectionActive:
	case domain.ConnectionExpired:
		return integration.Session{}, fmt.Errorf("signin: the connection has expired: %w", integration.ErrNeedsReconnect)
	default:
		return integration.Session{}, ErrNotActive
	}
	p, err := s.prepareStored(ctx, credential)
	if err != nil {
		return integration.Session{}, err
	}
	live := &liveCredential{credential: credential}
	materialFor := func(ctx context.Context) (material, error) { return s.material(ctx, live, p) }
	// A credential that cannot be had fails here, rather than deep inside an
	// SDK's first call.
	if _, err := materialFor(ctx); err != nil {
		return integration.Session{}, err
	}
	transport := &credentialTransport{
		next: s.base, prepared: p, material: materialFor,
		used: func(ctx context.Context) { s.markUsed(ctx, connectionID) },
	}
	if chosen.readOnly {
		transport.policy = newReadPolicy(p, chosen.readPOSTs)
	}
	return integration.Session{
		HTTP:     s.apiClient(transport),
		BaseURL:  strings.TrimRight(p.base.String(), "/"),
		Settings: maps.Clone(p.values),
		Token: func(ctx context.Context) (string, error) {
			held, err := materialFor(ctx)
			if err != nil {
				return "", err
			}
			if held.token == "" {
				return "", fmt.Errorf("%w: a %s credential has no token", ErrUnsupportedKind, credential.Kind)
			}
			// Nothing sees how the token is used, so handing it out counts
			// as its use.
			s.markUsed(ctx, connectionID)
			return held.token.Reveal(), nil
		},
	}, nil
}

// apiClient calls an app through inner, traced above it, with the base
// client's redirect policy and timeout.
func (s *Service) apiClient(inner http.RoundTripper) *http.Client {
	return &http.Client{Transport: observability.Transport(inner), CheckRedirect: s.checkRedirect, Timeout: s.timeout}
}

// liveCredential is a session's view of its credential, which a refresh
// moves on.
type liveCredential struct {
	mu         sync.Mutex
	credential Credential
}

func (l *liveCredential) get() Credential {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.credential
}

func (l *liveCredential) set(credential Credential) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if credential.Version >= l.credential.Version {
		l.credential = credential
	}
}

// credentialTransport places a credential on the requests to its own host,
// and nowhere else. It sees every redirect, since http.Client sends each
// through it, and decides for each one alone.
type credentialTransport struct {
	next     http.RoundTripper
	prepared *prepared
	material func(context.Context) (material, error)
	// policy, when set, refuses writes: the session is a read tool's.
	policy *readPolicy
	// used, when set, is told about a call to the credential's host that
	// succeeded.
	used func(context.Context)
}

func (t *credentialTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if t.policy != nil && !t.policy.allows(request) {
		closeBody(request)
		return nil, fmt.Errorf("%s to %s: %w", methodOf(request), request.URL.Host, ErrReadOnly)
	}
	// A RoundTripper does not change the request it is given.
	outgoing := request.Clone(request.Context())
	// Only the core sets these, and only for the credential's own host.
	outgoing.Header.Del("Authorization")
	outgoing.Header.Del("Cookie")
	own := outgoing.URL.Scheme == "https" && canonicalHost(outgoing.URL) == t.prepared.host
	if own {
		held, err := t.material(request.Context())
		if err != nil {
			closeBody(request)
			return nil, err
		}
		place(outgoing, t.prepared, held)
	}
	response, err := t.next.RoundTrip(outgoing)
	if err == nil && own && t.used != nil && response.StatusCode >= 200 && response.StatusCode < 300 {
		t.used(request.Context())
	}
	return response, err
}

// CloseIdleConnections lets http.Client.CloseIdleConnections reach the
// transport underneath.
func (t *credentialTransport) CloseIdleConnections() {
	if closer, ok := t.next.(interface{ CloseIdleConnections() }); ok {
		closer.CloseIdleConnections()
	}
}

// place puts a credential and the method's placed fields on a request.
func place(request *http.Request, p *prepared, held material) {
	switch p.method.Kind {
	case integration.KindOAuth2Code, integration.KindOAuth2ClientCredentials:
		request.Header.Set("Authorization", "Bearer "+held.token.Reveal())
	case integration.KindAPIToken:
		value := strings.ReplaceAll(p.method.Place.Template, "{token}", held.token.Reveal())
		if p.method.Place.In == integration.InQuery {
			setQuery(request.URL, p.method.Place.Name, value)
		} else {
			request.Header.Set(p.method.Place.Name, value)
		}
	case integration.KindBasic:
		request.SetBasicAuth(held.username, held.password.Reveal())
	}
	for _, field := range p.method.Fields {
		value := p.values[field.Name]
		if field.Secret {
			value = held.fields[field.Name].Reveal()
		}
		if value == "" {
			continue
		}
		for _, use := range field.Use {
			switch use.In {
			case integration.InHeader:
				request.Header.Set(use.Name, render(use.Template, value))
			case integration.InQuery:
				setQuery(request.URL, use.Name, render(use.Template, value))
			}
		}
	}
}

func setQuery(target *url.URL, name, value string) {
	query := target.Query()
	query.Set(name, value)
	target.RawQuery = query.Encode()
}

func closeBody(request *http.Request) {
	if request.Body != nil {
		_ = request.Body.Close()
	}
}

func methodOf(request *http.Request) string {
	if request.Method == "" {
		return http.MethodGet
	}
	return request.Method
}

// readPolicy is what a read tool's session may send: GET and HEAD anywhere,
// and POST only to its read_posts on the credential's own host.
type readPolicy struct {
	host string
	// posts are each read_post's segments beneath api_base's path; "{}"
	// stands for any one segment.
	posts [][]string
}

func newReadPolicy(p *prepared, readPOSTs []string) *readPolicy {
	policy := &readPolicy{host: p.host}
	base := strings.TrimRight(p.base.EscapedPath(), "/")
	for _, post := range readPOSTs {
		policy.posts = append(policy.posts, strings.Split(base+post, "/"))
	}
	return policy
}

func (p *readPolicy) allows(request *http.Request) bool {
	switch methodOf(request) {
	case http.MethodGet, http.MethodHead:
		return true
	case http.MethodPost:
	default:
		return false
	}
	if request.URL.Scheme != "https" || canonicalHost(request.URL) != p.host {
		return false
	}
	segments := strings.Split(request.URL.EscapedPath(), "/")
	for _, post := range p.posts {
		if matchSegments(post, segments) {
			return true
		}
	}
	return false
}

// matchSegments matches a request's escaped path segments against a
// read_post's, where "{}" stands for one segment that is really one: not
// empty, not a dot segment, and not hiding a slash.
func matchSegments(pattern, segments []string) bool {
	if len(pattern) != len(segments) {
		return false
	}
	for index, want := range pattern {
		got := segments[index]
		if want != "{}" {
			if got != want {
				return false
			}
			continue
		}
		decoded, err := url.PathUnescape(got)
		if err != nil || decoded == "" || decoded == "." || decoded == ".." || strings.ContainsAny(decoded, `/\`) {
			return false
		}
	}
	return true
}

// markUsed writes down, at most every markUseEvery per process, that a
// connection's credential just worked: its next check waits recentUse, and
// its failed checks are forgotten. Real use is the health check.
func (s *Service) markUsed(ctx context.Context, connectionID string) {
	now := s.now().UTC()
	if last, ok := s.used.Load(connectionID); ok && now.Sub(last.(time.Time)) < markUseEvery {
		return
	}
	s.used.Store(connectionID, now)
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err := s.store.MarkCredentialUsed(ctx, connectionID, now.Add(recentUse)); err != nil {
		s.logger.WarnContext(ctx, "record an app connection's use", "connection_id", connectionID, "error", err)
	}
}

// openMaterial opens what a stored credential places on requests.
func (s *Service) openMaterial(ctx context.Context, credential Credential, p *prepared) (material, error) {
	held := material{expires: credential.ExpiresAt}
	opened := func(name string) (secrets.Secret, error) {
		sealed, ok := credential.Sealed[name]
		if !ok {
			return "", fmt.Errorf("signin: the credential holds no %s", name)
		}
		return s.open(ctx, credentialBinding(credential.ConnectionID, name), sealed)
	}
	var err error
	switch credential.Kind {
	case integration.KindOAuth2Code, integration.KindOAuth2ClientCredentials:
		held.token, err = opened(sealedAccessToken)
	case integration.KindAPIToken:
		held.token, err = opened("token")
	case integration.KindBasic:
		held.username = credential.Settings["username"]
		held.password, err = opened("password")
	default:
		err = ErrUnsupportedKind
	}
	if err != nil {
		return material{}, err
	}
	held.fields, err = s.placedSecrets(ctx, credential, p)
	if err != nil {
		return material{}, err
	}
	return held, nil
}

// placedSecrets opens the secret fields a method places on requests: the
// connection's own, and its client's.
func (s *Service) placedSecrets(ctx context.Context, credential Credential, p *prepared) (map[string]secrets.Secret, error) {
	opened := map[string]secrets.Secret{}
	for _, field := range p.method.Fields {
		if !field.Secret || !placesOnRequests(field) {
			continue
		}
		var binding secrets.Binding
		var sealed secrets.SealedData
		var ok bool
		if field.Level == integration.LevelClient {
			if p.client == nil {
				continue
			}
			sealed, ok = p.client.SealedFields[field.Name]
			binding = clientBinding(p.client.ID, field.Name)
		} else {
			sealed, ok = credential.Sealed[field.Name]
			binding = credentialBinding(credential.ConnectionID, field.Name)
		}
		if !ok {
			continue
		}
		value, err := s.open(ctx, binding, sealed)
		if err != nil {
			return nil, err
		}
		opened[field.Name] = value
	}
	return opened, nil
}
