package httpserver

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/Shubham04-oss/oryxa-go/apps/internal/application"
	"github.com/Shubham04-oss/oryxa-go/apps/internal/authclient"
	"github.com/Shubham04-oss/oryxa-go/apps/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/apps/internal/hooks"
	"github.com/Shubham04-oss/oryxa-go/apps/internal/signin"
	"github.com/Shubham04-oss/oryxa-go/internal/secrets"
	"github.com/Shubham04-oss/oryxa-go/internal/serviceauth"
)

// OryxaService is what Oryxa's own sign-in's routes ask of apps.
// *application.Service is one.
type OryxaService interface {
	ConnectOryxa(ctx context.Context, credentials authclient.Credentials, organizationID string, input application.ConnectInput) (application.OryxaConnect, error)
	CompleteSignin(ctx context.Context, callback application.SigninCallback) (application.Completion, error)
	ReplaceCredential(ctx context.Context, credentials authclient.Credentials, organizationID, connectionID string, input application.ReplaceCredentialInput) (domain.Connection, error)
	Integrations() []application.IntegrationView
	Manifest(toolkit string) (application.ManifestView, error)
	OAuthClient(ctx context.Context, credentials authclient.Credentials, organizationID, client string) (application.OAuthClientView, error)
	PutOAuthClient(ctx context.Context, credentials authclient.Credentials, organizationID, client string, input application.OAuthClientInput) (application.OAuthClientView, error)
	RemoveOAuthClient(ctx context.Context, credentials authclient.Credentials, organizationID, client string) error
	RevealVerificationToken(ctx context.Context, credentials authclient.Credentials, organizationID, toolkit string) (secrets.Secret, error)
	DeploymentHook(ctx context.Context, toolkit string) (application.DeploymentHookView, error)
	RevealDeploymentVerificationToken(ctx context.Context, toolkit string) (secrets.Secret, error)
}

// ServiceVerifier checks a signed service token naming apps as its audience.
// *serviceauth.Verifier is one.
type ServiceVerifier interface {
	Verify(ctx context.Context, token string) (serviceauth.Claims, error)
}

// InternalPurpose is the one purpose apps' internal routes take, whatever
// else a calling service may be configured for.
const InternalPurpose = "hooks"

const (
	// SigninCallbackPath is Oryxa's OAuth callback: the path of the one
	// redirect URI registered with every app.
	SigninCallbackPath = "/v1/apps/oauth/callback"
	// signinCookiePrefix names a sign-in's state cookie. The rest of the name
	// comes from the state's hash, so two sign-ins started in one browser
	// each keep their own.
	signinCookiePrefix = "oryxa_apps_signin_"
)

// signinCookieAge is how long a state cookie lives: as long as its flow.
var signinCookieAge = int(signin.FlowLifetime / time.Second)

// routeOryxa registers Oryxa's own sign-in's routes. They are there whether
// or not it is switched on, and say so when it is not; the internal routes
// are there only for a deployment that names the services allowed to call
// them.
func (server *Server) routeOryxa(ingress http.Handler) {
	server.handle("GET "+SigninCallbackPath, server.completeSignin)
	server.handle("GET /v1/apps/integrations", server.integrations)
	server.handle("GET /v1/apps/integrations/{toolkit}/manifest", server.manifest)
	server.handle("PUT /v1/organizations/{organizationID}/apps/connections/{connectionID}/credentials", server.replaceCredential)
	server.handle("GET /v1/organizations/{organizationID}/apps/oauth-clients/{client}", server.oauthClient)
	server.handle("PUT /v1/organizations/{organizationID}/apps/oauth-clients/{client}", server.putOAuthClient)
	server.handle("DELETE /v1/organizations/{organizationID}/apps/oauth-clients/{client}", server.removeOAuthClient)
	server.handle("POST /v1/organizations/{organizationID}/apps/webhooks/{toolkit}/verification-token", server.revealVerificationToken)
	if ingress == nil {
		// No endpoint exists where no ingress runs, so every one is unknown,
		// answered as the ingress answers one.
		ingress = http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
			writeError(writer, http.StatusNotFound, "not_found", "resource not found")
		})
	}
	server.handleHandler(hooks.Pattern, ingress)
	if server.services != nil {
		server.handle("GET /v1/internal/apps/hooks/{toolkit}", server.deploymentHook)
		server.handle("POST /v1/internal/apps/hooks/{toolkit}/verification-token", server.revealDeploymentVerificationToken)
	}
}

// oryxaOff answers a route of Oryxa's own sign-in where there is none.
func oryxaOff(writer http.ResponseWriter) {
	writeError(writer, http.StatusUnprocessableEntity, "not_connectable",
		"connecting through Oryxa's own sign-in is not switched on here")
}

// connectOryxa starts a connection through Oryxa's own sign-in. For OAuth it
// answers the app's authorization URL, as the Composio connect answers its
// link, and sets the state cookie the callback needs; a key is proved and
// kept at once, and the connection answered is active.
func (server *Server) connectOryxa(writer http.ResponseWriter, request *http.Request, credentials authclient.Credentials, input application.ConnectInput) {
	if server.oryxa == nil {
		oryxaOff(writer)
		return
	}
	connected, err := server.oryxa.ConnectOryxa(request.Context(), credentials, request.PathValue("organizationID"), input)
	if err != nil {
		server.handleError(writer, request, err)
		return
	}
	answer := map[string]any{"connection": viewConnection(connected.Connection)}
	if connected.RedirectURL != "" {
		answer["redirect_url"] = connected.RedirectURL
		// The callback reads it back only on the host that set it, and only
		// on its own path; JavaScript never sees it.
		http.SetCookie(writer, &http.Cookie{
			Name: signinCookieName(connected.State), Value: connected.State, Path: SigninCallbackPath,
			MaxAge: signinCookieAge, HttpOnly: true, Secure: true, SameSite: http.SameSiteLaxMode,
		})
	}
	writeJSON(writer, http.StatusCreated, answer)
}

// completeSignin is where an app sends the member's browser back. It answers
// the browser as the Composio callback does: with a return page it redirects
// there with app_connection and status, and without one it answers JSON.
func (server *Server) completeSignin(writer http.ResponseWriter, request *http.Request) {
	// The code and state in this URL must not follow the browser anywhere.
	writer.Header().Set("Referrer-Policy", "no-referrer")
	if !server.callbacks.Allow(clientIP(request)) {
		server.logger.WarnContext(request.Context(), "apps request rate limited", "path", request.URL.Path)
		writeError(writer, http.StatusTooManyRequests, "rate_limited", "too many requests; retry")
		return
	}
	query := request.URL.Query()
	state := query.Get("state")
	cookieState := ""
	if state != "" {
		name := signinCookieName(state)
		if cookie, err := request.Cookie(name); err == nil {
			cookieState = cookie.Value
		}
		// Spent or not, the flow is over for this browser.
		http.SetCookie(writer, &http.Cookie{
			Name: name, Path: SigninCallbackPath, MaxAge: -1, Expires: time.Unix(1, 0),
			HttpOnly: true, Secure: true, SameSite: http.SameSiteLaxMode,
		})
	}
	if server.oryxa == nil {
		oryxaOff(writer)
		return
	}
	accessCookie := ""
	if cookie, err := request.Cookie(authclient.AccessCookieName); err == nil {
		accessCookie = cookie.Value
	}
	completion, err := server.oryxa.CompleteSignin(request.Context(), application.SigninCallback{
		State: state, CookieState: cookieState, Code: query.Get("code"), Error: query.Get("error"),
		AccessCookie: accessCookie,
	})
	if err != nil {
		server.handleError(writer, request, err)
		return
	}
	server.sendBack(writer, request, completion)
}

// sendBack answers the browser at the end of connecting: to its return page
// with exactly app_connection and status, or with the connection as JSON when
// there is no return page.
func (server *Server) sendBack(writer http.ResponseWriter, request *http.Request, completion application.Completion) {
	if completion.ReturnTo == "" {
		writeJSON(writer, http.StatusOK, map[string]any{"connection": viewConnection(completion.Connection)})
		return
	}
	target, err := url.Parse(completion.ReturnTo)
	if err != nil {
		server.handleError(writer, request, err)
		return
	}
	values := target.Query()
	values.Set("app_connection", completion.Connection.ID)
	values.Set("status", string(completion.Connection.Status))
	target.RawQuery = values.Encode()
	http.Redirect(writer, request, target.String(), http.StatusSeeOther)
}

// replaceCredential gives an Oryxa connection a new key, password or client
// credentials.
func (server *Server) replaceCredential(writer http.ResponseWriter, request *http.Request) {
	credentials, ok := server.credentials(writer, request)
	if !ok {
		return
	}
	var body struct {
		Kind   string            `json:"kind"`
		Fields map[string]string `json:"fields"`
	}
	if !server.decode(writer, request, &body) {
		return
	}
	if server.oryxa == nil {
		oryxaOff(writer)
		return
	}
	connection, err := server.oryxa.ReplaceCredential(request.Context(), credentials, request.PathValue("organizationID"),
		request.PathValue("connectionID"), application.ReplaceCredentialInput{Kind: body.Kind, Fields: body.Fields})
	if err != nil {
		server.handleError(writer, request, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"connection": viewConnection(connection)})
}

// integrations lists the apps Oryxa's own sign-in is switched on for. Like
// the catalog, it holds nothing of any organization, so it needs no caller.
func (server *Server) integrations(writer http.ResponseWriter, _ *http.Request) {
	integrations := []application.IntegrationView{}
	if server.oryxa != nil {
		integrations = server.oryxa.Integrations()
	}
	writeJSON(writer, http.StatusOK, map[string]any{"integrations": integrations})
}

// manifest answers the form a toolkit's sign-in is rendered from. It holds
// nothing secret and nothing of any organization, so it needs no caller.
func (server *Server) manifest(writer http.ResponseWriter, request *http.Request) {
	if server.oryxa == nil {
		oryxaOff(writer)
		return
	}
	view, err := server.oryxa.Manifest(request.PathValue("toolkit"))
	if err != nil {
		server.handleError(writer, request, err)
		return
	}
	writeJSON(writer, http.StatusOK, view)
}

func (server *Server) oauthClient(writer http.ResponseWriter, request *http.Request) {
	credentials, ok := server.credentials(writer, request)
	if !ok {
		return
	}
	if server.oryxa == nil {
		oryxaOff(writer)
		return
	}
	view, err := server.oryxa.OAuthClient(request.Context(), credentials, request.PathValue("organizationID"), request.PathValue("client"))
	if err != nil {
		server.handleError(writer, request, err)
		return
	}
	writeJSON(writer, http.StatusOK, view)
}

func (server *Server) putOAuthClient(writer http.ResponseWriter, request *http.Request) {
	credentials, ok := server.credentials(writer, request)
	if !ok {
		return
	}
	var body struct {
		ClientID string `json:"client_id"`
		// ClientSecret and WebhookSecrets are write-only: never answered,
		// never logged.
		ClientSecret   string            `json:"client_secret"`
		Fields         map[string]string `json:"fields"`
		RefuseDefault  bool              `json:"refuse_default"`
		WebhookSecrets map[string]string `json:"webhook_secrets"`
	}
	if !server.decode(writer, request, &body) {
		return
	}
	if server.oryxa == nil {
		oryxaOff(writer)
		return
	}
	input := application.OAuthClientInput{
		ClientID: body.ClientID, ClientSecret: secrets.Secret(body.ClientSecret), Fields: body.Fields,
		RefuseDefault: body.RefuseDefault,
	}
	if len(body.WebhookSecrets) > 0 {
		input.WebhookSecrets = make(map[string]secrets.Secret, len(body.WebhookSecrets))
		for toolkit, secret := range body.WebhookSecrets {
			input.WebhookSecrets[toolkit] = secrets.Secret(secret)
		}
	}
	view, err := server.oryxa.PutOAuthClient(request.Context(), credentials, request.PathValue("organizationID"),
		request.PathValue("client"), input)
	if err != nil {
		server.handleError(writer, request, err)
		return
	}
	writeJSON(writer, http.StatusOK, view)
}

func (server *Server) removeOAuthClient(writer http.ResponseWriter, request *http.Request) {
	credentials, ok := server.credentials(writer, request)
	if !ok {
		return
	}
	if server.oryxa == nil {
		oryxaOff(writer)
		return
	}
	if err := server.oryxa.RemoveOAuthClient(request.Context(), credentials, request.PathValue("organizationID"),
		request.PathValue("client")); err != nil {
		server.handleError(writer, request, err)
		return
	}
	writer.WriteHeader(http.StatusNoContent)
}

// revealVerificationToken shows an organization's admin, once, the token
// its own app sent its endpoint (Notion's), to paste back into the app.
func (server *Server) revealVerificationToken(writer http.ResponseWriter, request *http.Request) {
	credentials, ok := server.credentials(writer, request)
	if !ok {
		return
	}
	if server.oryxa == nil {
		oryxaOff(writer)
		return
	}
	toolkit := strings.ToLower(strings.TrimSpace(request.PathValue("toolkit")))
	token, err := server.oryxa.RevealVerificationToken(request.Context(), credentials, request.PathValue("organizationID"), toolkit)
	if err != nil {
		server.handleError(writer, request, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]string{"toolkit": toolkit, "verification_token": token.Reveal()})
}

// deploymentHook answers the deployment's own endpoint for a toolkit, making
// it the first time, to a service: never to a member.
func (server *Server) deploymentHook(writer http.ResponseWriter, request *http.Request) {
	if !server.internalCaller(writer, request) {
		return
	}
	if server.oryxa == nil {
		oryxaOff(writer)
		return
	}
	view, err := server.oryxa.DeploymentHook(request.Context(), request.PathValue("toolkit"))
	if err != nil {
		server.handleError(writer, request, err)
		return
	}
	writeJSON(writer, http.StatusOK, view)
}

// revealDeploymentVerificationToken shows, once, the token an app sent the
// deployment's own endpoint, to a service: never to a member.
func (server *Server) revealDeploymentVerificationToken(writer http.ResponseWriter, request *http.Request) {
	if !server.internalCaller(writer, request) {
		return
	}
	if server.oryxa == nil {
		oryxaOff(writer)
		return
	}
	toolkit := strings.ToLower(strings.TrimSpace(request.PathValue("toolkit")))
	token, err := server.oryxa.RevealDeploymentVerificationToken(request.Context(), toolkit)
	if err != nil {
		server.handleError(writer, request, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]string{"toolkit": toolkit, "verification_token": token.Reveal()})
}

// internalCaller checks an internal route's service token, answering the
// caller itself when it does not pass. A member's own credentials are never
// looked at.
func (server *Server) internalCaller(writer http.ResponseWriter, request *http.Request) bool {
	token := request.Header.Get(serviceauth.Header)
	if token == "" {
		writeError(writer, http.StatusUnauthorized, "unauthorized", "a service token is required")
		return false
	}
	claims, err := server.services.Verify(request.Context(), token)
	switch {
	case errors.Is(err, serviceauth.ErrPurposeNotAllowed):
		writeError(writer, http.StatusForbidden, "forbidden", "the service may not use this purpose")
		return false
	case errors.Is(err, serviceauth.ErrInvalidToken):
		writeError(writer, http.StatusUnauthorized, "unauthorized", "the service token is invalid")
		return false
	case err != nil:
		server.logger.ErrorContext(request.Context(), "check an internal caller", "error", err)
		writeError(writer, http.StatusServiceUnavailable, "unavailable", "service identity could not be checked; retry")
		return false
	case claims.Purpose != InternalPurpose:
		writeError(writer, http.StatusForbidden, "forbidden", "the service may not use this purpose")
		return false
	}
	server.logger.InfoContext(request.Context(), "internal call", "service", claims.Service, "path", request.URL.Path)
	return true
}

// signinCookieName is the name of the state cookie for one sign-in.
func signinCookieName(state string) string {
	sum := sha256.Sum256([]byte(state))
	return signinCookiePrefix + hex.EncodeToString(sum[:8])
}

// clientIP is the address a request came from. Production is reached only
// through Cloudflare, which sets CF-Connecting-IP to that address and does
// not let a client set it; anywhere else it is the peer's own.
func clientIP(request *http.Request) string {
	if forwarded := strings.TrimSpace(request.Header.Get("CF-Connecting-IP")); forwarded != "" {
		if ip := net.ParseIP(forwarded); ip != nil {
			return ip.String()
		}
	}
	host, _, err := net.SplitHostPort(request.RemoteAddr)
	if err != nil {
		return request.RemoteAddr
	}
	return host
}
