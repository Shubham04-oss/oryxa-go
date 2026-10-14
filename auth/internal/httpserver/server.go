package httpserver

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/Shubham04-oss/oryxa-go/auth/internal/application"
	"github.com/Shubham04-oss/oryxa-go/auth/internal/authn"
	"github.com/Shubham04-oss/oryxa-go/auth/internal/clisession"
	"github.com/Shubham04-oss/oryxa-go/auth/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/auth/internal/oauthflow"
	"github.com/Shubham04-oss/oryxa-go/auth/internal/passwordflow"
	"github.com/Shubham04-oss/oryxa-go/internal/cleantext"
	"github.com/Shubham04-oss/oryxa-go/internal/cliscope"
)

const (
	accessCookieName  = "oryxa_access"
	refreshCookieName = "oryxa_refresh"
	stateCookieName   = "oryxa_login_state"
)

type accessTokenVerifier interface {
	Verify(context.Context, string) (authn.Claims, error)
}

type cliIdentityVerifier interface {
	VerifyCLIIdentityToken(context.Context, string, string) (authn.IdentityClaims, error)
}

type authFlow interface {
	Begin(context.Context, string) (string, string, error)
	Complete(context.Context, string, string, string) (oauthflow.Session, error)
	Revoke(context.Context, string) error
}

type passwordAuth interface {
	SignUp(context.Context, string, string, string) error
	ResendEmailVerification(context.Context, string) error
	ConfirmEmail(context.Context, string) error
	Login(context.Context, string, string) (passwordflow.Session, error)
	Refresh(context.Context, string) (passwordflow.Tokens, error)
	RequestPasswordReset(context.Context, string) error
	ResetPassword(context.Context, string, string) error
}

type authService interface {
	SignOut(context.Context, string) error
	CurrentUser(context.Context, string) (domain.User, []domain.Organization, error)
	CreateOrganization(context.Context, string, application.CreateOrganizationInput) (domain.Organization, error)
	UpdateOrganization(context.Context, string, string, application.UpdateOrganizationInput) (domain.Organization, error)
	DeleteOrganization(context.Context, string, string) error
	Organizations(context.Context, string) ([]domain.Organization, error)
	Organization(context.Context, string, string) (domain.Organization, domain.Membership, error)
	Members(context.Context, string, string) ([]domain.Membership, error)
	InviteMember(context.Context, string, string, application.InviteMemberInput) (domain.Invitation, string, error)
	Invitations(context.Context, string, string) ([]domain.Invitation, error)
	RevokeInvitation(context.Context, string, string, string) error
	AcceptInvitation(context.Context, string, string) (domain.Membership, error)
	ReceivedInvitations(context.Context, string) ([]domain.ReceivedInvitation, error)
	AcceptReceivedInvitation(context.Context, string, string) (domain.Membership, error)
	ChangeMemberRole(context.Context, string, string, string, domain.Role) (domain.Membership, error)
	RemoveMember(context.Context, string, string, string) error
}

type Config struct {
	CognitoDomain      string
	CognitoClientID    string
	CognitoCallbackURL string
	AllowedOrigins     []string
	CookieDomain       string
	CookieSecure       bool
	GoogleEnabled      bool
	CLIClientID        string
	CLIIssuer          string
	CLICallbackURL     string
	CLICallbackURLs    []string
	CLISignUpURL       string
	CLIVerifier        cliIdentityVerifier
	CLISessions        clisession.Store
	CLICompleteLogin   func(context.Context, application.FederatedIdentity) (domain.User, bool, error)

	// AgentClientID is COGNITO_AGENT_CLIENT_ID: the Cognito agent app
	// client's ID. Whenever it is set, a verified access token whose
	// client_id matches it is an agent token, restricted in authenticated
	// regardless of whether AgentGrants below is configured.
	AgentClientID string
	// AgentGrants issues and revokes agent grants. Nil means agent grants
	// are disabled and every agent grant route answers 404.
	AgentGrants AgentGrantsFlow
	// Services verifies a signed service token on the internal API. Nil
	// means service tokens are not accepted.
	Services ServiceVerifier
}

type Server struct {
	verifier           accessTokenVerifier
	oauth              authFlow
	passwords          passwordAuth
	service            authService
	config             Config
	allowedOrigins     map[string]struct{}
	mux                *http.ServeMux
	patterns           []string
	cliChallengeLimits cliChallengeLimiter
}

type authContextKey struct{}

type authenticatedRequest struct {
	Claims            authn.Claims
	FromCookie        bool
	CLITokenHash      string
	CLIOrganizationID string
}

func New(verifier accessTokenVerifier, oauth authFlow, passwords passwordAuth, service authService, config Config) *Server {
	server := &Server{
		verifier:       verifier,
		oauth:          oauth,
		passwords:      passwords,
		service:        service,
		config:         config,
		allowedOrigins: make(map[string]struct{}),
		mux:            http.NewServeMux(),
	}
	for _, origin := range config.AllowedOrigins {
		if parsed, err := url.Parse(origin); err == nil && parsed.Scheme != "" && parsed.Host != "" {
			server.allowedOrigins[parsed.Scheme+"://"+parsed.Host] = struct{}{}
		}
	}

	server.handleFunc("GET /healthz", server.health)
	server.handleFunc("GET /v1/auth/config", server.authConfig)
	server.handleFunc("POST /v1/auth/cli/session", server.cliSession)
	server.handleFunc("POST /v1/auth/cli/challenge", server.cliChallenge)
	server.handleFunc("GET /v1/auth/login", server.login)
	server.handleFunc("GET /v1/auth/callback", server.callback)
	server.handleFunc("POST /v1/auth/refresh", server.refresh)
	server.handleFunc("POST /v1/auth/logout", server.logout)
	server.handleFunc("POST /v1/auth/password/signup", server.passwordSignUp)
	server.handleFunc("POST /v1/auth/password/verify", server.passwordVerifyEmail)
	server.handleFunc("POST /v1/auth/password/resend-verification", server.passwordResendVerification)
	server.handleFunc("POST /v1/auth/password/login", server.passwordLogin)
	server.handleFunc("POST /v1/auth/password/forgot", server.passwordForgot)
	server.handleFunc("POST /v1/auth/password/reset", server.passwordReset)
	server.handle("GET /v1/auth/me", server.authenticated(http.HandlerFunc(server.me)))
	server.handle("GET /v1/organizations", server.authenticated(http.HandlerFunc(server.organizations)))
	server.handle("POST /v1/organizations", server.authenticated(http.HandlerFunc(server.createOrganization)))
	server.handle("GET /v1/organizations/{organizationID}", server.authenticated(http.HandlerFunc(server.organization)))
	server.handle("PATCH /v1/organizations/{organizationID}", server.authenticated(http.HandlerFunc(server.updateOrganization)))
	server.handle("DELETE /v1/organizations/{organizationID}", server.authenticated(http.HandlerFunc(server.deleteOrganization)))
	server.handle("GET /v1/organizations/{organizationID}/members", server.authenticated(http.HandlerFunc(server.members)))
	server.handle("POST /v1/organizations/{organizationID}/invitations", server.authenticated(http.HandlerFunc(server.inviteMember)))
	server.handle("GET /v1/organizations/{organizationID}/invitations", server.authenticated(http.HandlerFunc(server.invitations)))
	server.handle("DELETE /v1/organizations/{organizationID}/invitations/{invitationID}", server.authenticated(http.HandlerFunc(server.revokeInvitation)))
	server.handle("POST /v1/invitations/accept", server.authenticated(http.HandlerFunc(server.acceptInvitation)))
	server.handle("GET /v1/invitations", server.authenticated(http.HandlerFunc(server.receivedInvitations)))
	server.handle("POST /v1/invitations/{invitationID}/accept", server.authenticated(http.HandlerFunc(server.acceptReceivedInvitation)))
	server.handle("PATCH /v1/organizations/{organizationID}/members/{userID}", server.authenticated(http.HandlerFunc(server.changeMemberRole)))
	server.handle("DELETE /v1/organizations/{organizationID}/members/{userID}", server.authenticated(http.HandlerFunc(server.removeMember)))
	server.handle("GET /v1/agent-grant", server.authenticated(http.HandlerFunc(server.agentGrantStatus)))
	server.handle("POST /v1/agent-grant/authorize", server.authenticated(http.HandlerFunc(server.agentGrantAuthorize)))
	server.handleFunc("GET /v1/agent-grant/callback", server.agentGrantCallback)
	server.handle("DELETE /v1/agent-grant", server.authenticated(http.HandlerFunc(server.agentGrantRevoke)))
	server.handleFunc("POST /v1/internal/agent-tokens", server.mintAgentToken)

	return server
}

// handle registers a route backed by an http.Handler and remembers its
// pattern, so a host serving several services knows which paths are this
// one's.
func (server *Server) handle(pattern string, handler http.Handler) {
	server.mux.Handle(pattern, handler)
	server.patterns = append(server.patterns, pattern)
}

// handleFunc is handle for a plain handler function.
func (server *Server) handleFunc(pattern string, handler http.HandlerFunc) {
	server.handle(pattern, handler)
}

// Patterns lists every route the server registered.
func (server *Server) Patterns() []string { return slices.Clone(server.patterns) }

func (server *Server) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	writer.Header().Set("X-Content-Type-Options", "nosniff")
	if strings.HasPrefix(request.URL.Path, "/v1/") {
		noStore(writer)
	}
	origin := request.Header.Get("Origin")
	if origin != "" {
		if _, ok := server.allowedOrigins[origin]; ok {
			writer.Header().Set("Access-Control-Allow-Origin", origin)
			writer.Header().Set("Access-Control-Allow-Credentials", "true")
			writer.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type")
			writer.Header().Set("Access-Control-Allow-Methods", "GET, POST, PATCH, DELETE, OPTIONS")
			writer.Header().Add("Vary", "Origin")
		} else {
			writeError(writer, http.StatusForbidden, "origin is not allowed")
			return
		}
	}
	if request.Method == http.MethodOptions {
		writer.WriteHeader(http.StatusNoContent)
		return
	}
	server.mux.ServeHTTP(writer, request)
}

func (server *Server) health(writer http.ResponseWriter, _ *http.Request) {
	writer.WriteHeader(http.StatusNoContent)
}

func (server *Server) authConfig(writer http.ResponseWriter, _ *http.Request) {
	noStore(writer)
	identityProviders := []string{"Password"}
	config := map[string]any{
		"authorization_endpoint": strings.TrimRight(server.config.CognitoDomain, "/") + "/oauth2/authorize",
		"callback_url":           server.config.CognitoCallbackURL,
		"client_id":              server.config.CognitoClientID,
		"password_policy": map[string]any{
			"minimum_length":     12,
			"requires_lowercase": true,
			"requires_number":    true,
			"requires_symbol":    true,
			"requires_uppercase": true,
		},
		"response_type": "code",
		"scopes":        []string{"openid", "email", "profile"},
	}
	if server.config.GoogleEnabled {
		identityProviders = append([]string{"Google"}, identityProviders...)
		config["identity_provider"] = "Google"
	}
	config["identity_providers"] = identityProviders
	if server.config.CLIClientID != "" {
		config["cli_client_id"] = server.config.CLIClientID
		config["cli_issuer"] = server.config.CLIIssuer
		config["cli_callback_url"] = server.config.CLICallbackURL
		config["cli_callback_urls"] = server.config.CLICallbackURLs
		config["cognito_domain"] = server.config.CognitoDomain
		config["cli_signup_url"] = server.config.CLISignUpURL
	}
	writeJSON(writer, http.StatusOK, config)
}

func (server *Server) passwordSignUp(writer http.ResponseWriter, request *http.Request) {
	if !server.requirePublicMutationOrigin(writer, request) {
		return
	}
	var input struct {
		Email       string `json:"email"`
		Password    string `json:"password"`
		DisplayName string `json:"display_name"`
	}
	if err := readJSON(request, &input); err != nil {
		writeError(writer, http.StatusBadRequest, "invalid request body")
		return
	}
	if err := server.passwords.SignUp(request.Context(), input.Email, input.Password, input.DisplayName); err != nil {
		server.handleError(writer, err)
		return
	}
	writeJSON(writer, http.StatusAccepted, map[string]string{"status": "verification_requested"})
}

func (server *Server) passwordVerifyEmail(writer http.ResponseWriter, request *http.Request) {
	if !server.requirePublicMutationOrigin(writer, request) {
		return
	}
	var input struct {
		Token string `json:"token"`
	}
	if err := readJSON(request, &input); err != nil {
		writeError(writer, http.StatusBadRequest, "invalid request body")
		return
	}
	if err := server.passwords.ConfirmEmail(request.Context(), input.Token); err != nil {
		server.handleError(writer, err)
		return
	}
	writer.WriteHeader(http.StatusNoContent)
}

func (server *Server) passwordResendVerification(writer http.ResponseWriter, request *http.Request) {
	if !server.requirePublicMutationOrigin(writer, request) {
		return
	}
	var input struct {
		Email string `json:"email"`
	}
	if err := readJSON(request, &input); err != nil {
		writeError(writer, http.StatusBadRequest, "invalid request body")
		return
	}
	if err := server.passwords.ResendEmailVerification(request.Context(), input.Email); err != nil {
		server.handleError(writer, err)
		return
	}
	writeJSON(writer, http.StatusAccepted, map[string]string{"status": "verification_requested"})
}

func (server *Server) passwordLogin(writer http.ResponseWriter, request *http.Request) {
	if !server.requirePublicMutationOrigin(writer, request) {
		return
	}
	var input struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if err := readJSON(request, &input); err != nil {
		writeError(writer, http.StatusBadRequest, "invalid request body")
		return
	}
	session, err := server.passwords.Login(request.Context(), input.Email, input.Password)
	if err != nil {
		server.handleError(writer, err)
		return
	}
	server.setSessionCookies(writer, session.AccessToken, session.RefreshToken, session.ExpiresIn)
	writeJSON(writer, http.StatusOK, map[string]any{"user": session.User})
}

func (server *Server) passwordForgot(writer http.ResponseWriter, request *http.Request) {
	if !server.requirePublicMutationOrigin(writer, request) {
		return
	}
	var input struct {
		Email string `json:"email"`
	}
	if err := readJSON(request, &input); err != nil {
		writeError(writer, http.StatusBadRequest, "invalid request body")
		return
	}
	if err := server.passwords.RequestPasswordReset(request.Context(), input.Email); err != nil {
		server.handleError(writer, err)
		return
	}
	writeJSON(writer, http.StatusAccepted, map[string]string{"status": "password_reset_requested"})
}

func (server *Server) passwordReset(writer http.ResponseWriter, request *http.Request) {
	if !server.requirePublicMutationOrigin(writer, request) {
		return
	}
	var input struct {
		Token    string `json:"token"`
		Password string `json:"password"`
	}
	if err := readJSON(request, &input); err != nil {
		writeError(writer, http.StatusBadRequest, "invalid request body")
		return
	}
	if err := server.passwords.ResetPassword(request.Context(), input.Token, input.Password); err != nil {
		server.handleError(writer, err)
		return
	}
	writer.WriteHeader(http.StatusNoContent)
}

func (server *Server) login(writer http.ResponseWriter, request *http.Request) {
	noStore(writer)
	if !server.config.GoogleEnabled {
		writeError(writer, http.StatusNotFound, "Google sign-in is not enabled")
		return
	}
	state, target, err := server.oauth.Begin(request.Context(), request.URL.Query().Get("return_to"))
	if err != nil {
		server.handleError(writer, err)
		return
	}
	http.SetCookie(writer, &http.Cookie{
		Name:     stateCookieName,
		Value:    state,
		Path:     "/v1/auth/callback",
		Domain:   server.config.CookieDomain,
		MaxAge:   600,
		HttpOnly: true,
		Secure:   server.config.CookieSecure,
		SameSite: http.SameSiteLaxMode,
	})
	http.Redirect(writer, request, target, http.StatusSeeOther)
}

func (server *Server) callback(writer http.ResponseWriter, request *http.Request) {
	noStore(writer)
	if providerError := request.URL.Query().Get("error"); providerError != "" {
		writeError(writer, http.StatusUnauthorized, "identity provider rejected login")
		return
	}
	stateCookie, err := request.Cookie(stateCookieName)
	if err != nil {
		writeError(writer, http.StatusBadRequest, "login state cookie is missing")
		return
	}
	session, err := server.oauth.Complete(
		request.Context(),
		request.URL.Query().Get("state"),
		stateCookie.Value,
		request.URL.Query().Get("code"),
	)
	if err != nil {
		// The member sees only that sign-in failed; why is for the operator.
		slog.WarnContext(request.Context(), "sign-in refused", "component", "auth", "error", err)
		server.handleError(writer, err)
		return
	}
	server.setSessionCookies(writer, session.AccessToken, session.RefreshToken, session.ExpiresIn)
	server.clearCookie(writer, stateCookieName, "/v1/auth/callback")
	http.Redirect(writer, request, session.ReturnTo, http.StatusSeeOther)
}

func (server *Server) refresh(writer http.ResponseWriter, request *http.Request) {
	noStore(writer)
	if !server.validMutationOrigin(request) {
		writeError(writer, http.StatusForbidden, "origin is required for cookie authentication")
		return
	}
	cookie, err := request.Cookie(refreshCookieName)
	if err != nil {
		writeError(writer, http.StatusUnauthorized, "refresh session is missing")
		return
	}
	tokens, err := server.passwords.Refresh(request.Context(), cookie.Value)
	if err != nil {
		// Every member refreshes here, however they signed in, so a kind of
		// session Cognito stops renewing shows up in this log first.
		slog.WarnContext(request.Context(), "refresh session", "error", err)
		server.clearSessionCookies(writer)
		writeError(writer, http.StatusUnauthorized, "refresh session is invalid")
		return
	}
	server.setSessionCookies(writer, tokens.AccessToken, tokens.RefreshToken, tokens.ExpiresIn)
	writer.WriteHeader(http.StatusNoContent)
}

func (server *Server) logout(writer http.ResponseWriter, request *http.Request) {
	noStore(writer)
	_, hasBearer := bearerToken(request.Header.Get("Authorization"))
	if !hasBearer && !server.validMutationOrigin(request) {
		writeError(writer, http.StatusForbidden, "origin is required for cookie authentication")
		return
	}
	refreshToken := ""
	if cookie, err := request.Cookie(refreshCookieName); err == nil {
		refreshToken = cookie.Value
	}
	if err := server.oauth.Revoke(request.Context(), refreshToken); err != nil {
		slog.ErrorContext(request.Context(), "revoke Cognito token", "error", err)
	}
	if rawToken, _, ok := server.requestToken(request); ok {
		// An agent's token is the member's identity, but signing the member
		// out is theirs alone to do.
		if claims, err := server.verifier.Verify(request.Context(), rawToken); err == nil && !server.isAgentToken(claims) && !server.isCLIToken(claims) {
			if err := server.service.SignOut(request.Context(), claims.Subject); err != nil {
				slog.ErrorContext(request.Context(), "enqueue sign-out event", "error", err)
			}
		}
	}
	server.clearSessionCookies(writer)
	writer.WriteHeader(http.StatusNoContent)
}

func (server *Server) me(writer http.ResponseWriter, request *http.Request) {
	authenticated := requestAuth(request)
	user, organizations, err := server.service.CurrentUser(request.Context(), authenticated.Claims.Subject)
	if err != nil {
		server.handleError(writer, err)
		return
	}
	organizations = filterCLIOrganizations(authenticated, organizations)
	writeJSON(writer, http.StatusOK, map[string]any{"user": user, "organizations": organizations, "cli": server.isCLIToken(authenticated.Claims)})
}

func (server *Server) organizations(writer http.ResponseWriter, request *http.Request) {
	authenticated := requestAuth(request)
	organizations, err := server.service.Organizations(request.Context(), authenticated.Claims.Subject)
	if err != nil {
		server.handleError(writer, err)
		return
	}
	organizations = filterCLIOrganizations(authenticated, organizations)
	writeJSON(writer, http.StatusOK, map[string]any{"organizations": organizations})
}

func (server *Server) createOrganization(writer http.ResponseWriter, request *http.Request) {
	var input application.CreateOrganizationInput
	if err := readJSON(request, &input); err != nil {
		writeError(writer, http.StatusBadRequest, "invalid request body")
		return
	}
	authenticated := requestAuth(request)
	if authenticated.CLITokenHash != "" {
		if err := server.config.CLISessions.ReserveCLIOrganization(request.Context(), authenticated.CLITokenHash); err != nil {
			server.handleError(writer, err)
			return
		}
	}
	organization, err := server.service.CreateOrganization(request.Context(), authenticated.Claims.Subject, input)
	if authenticated.CLITokenHash != "" {
		finishCtx, cancel := context.WithTimeout(context.WithoutCancel(request.Context()), 5*time.Second)
		defer cancel()
		if err == nil {
			if bindErr := server.config.CLISessions.FinishCLIOrganization(finishCtx, authenticated.CLITokenHash, organization.ID); bindErr != nil {
				server.handleError(writer, bindErr)
				return
			}
		} else if errors.Is(err, domain.ErrInvalid) || errors.Is(err, domain.ErrConflict) || errors.Is(err, domain.ErrForbidden) {
			// Definite pre-creation failures may be corrected and retried.
			_ = server.config.CLISessions.FinishCLIOrganization(finishCtx, authenticated.CLITokenHash, "")
		}
	}
	if err != nil {
		server.handleError(writer, err)
		return
	}
	writeJSON(writer, http.StatusCreated, organization)
}

func (server *Server) organization(writer http.ResponseWriter, request *http.Request) {
	authenticated := requestAuth(request)
	organization, membership, err := server.service.Organization(
		request.Context(),
		authenticated.Claims.Subject,
		request.PathValue("organizationID"),
	)
	if err != nil {
		server.handleError(writer, err)
		return
	}
	response := map[string]any{"organization": organization, "membership": membership}
	if server.isCLIToken(authenticated.Claims) {
		response["cli"] = true
	}
	if server.isAgentToken(authenticated.Claims) {
		response["agent"] = true
		identity := domain.AgentIdentity{Role: domain.AgentRolePrimary}
		if server.config.AgentGrants != nil {
			rawToken, _, _ := server.requestToken(request)
			identity, err = server.config.AgentGrants.AgentIdentity(request.Context(), authenticated.Claims.Subject, rawToken)
			if err != nil {
				server.handleError(writer, err)
				return
			}
		}
		response["agent_claims"] = identity
	}
	writeJSON(writer, http.StatusOK, response)
}

func (server *Server) updateOrganization(writer http.ResponseWriter, request *http.Request) {
	var input application.UpdateOrganizationInput
	if err := readJSON(request, &input); err != nil {
		writeError(writer, http.StatusBadRequest, "invalid request body")
		return
	}
	authenticated := requestAuth(request)
	organization, err := server.service.UpdateOrganization(
		request.Context(),
		authenticated.Claims.Subject,
		request.PathValue("organizationID"),
		input,
	)
	if err != nil {
		server.handleError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, organization)
}

func (server *Server) deleteOrganization(writer http.ResponseWriter, request *http.Request) {
	authenticated := requestAuth(request)
	err := server.service.DeleteOrganization(
		request.Context(),
		authenticated.Claims.Subject,
		request.PathValue("organizationID"),
	)
	if err != nil {
		server.handleError(writer, err)
		return
	}
	writer.WriteHeader(http.StatusNoContent)
}

func (server *Server) members(writer http.ResponseWriter, request *http.Request) {
	authenticated := requestAuth(request)
	members, err := server.service.Members(request.Context(), authenticated.Claims.Subject, request.PathValue("organizationID"))
	if err != nil {
		server.handleError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"members": members})
}

func (server *Server) inviteMember(writer http.ResponseWriter, request *http.Request) {
	var input struct {
		Email string      `json:"email"`
		Role  domain.Role `json:"role"`
		Quiet bool        `json:"quiet"`
		Guest bool        `json:"guest"`
	}
	if err := readJSON(request, &input); err != nil {
		writeError(writer, http.StatusBadRequest, "invalid request body")
		return
	}
	authenticated := requestAuth(request)
	invitation, _, err := server.service.InviteMember(
		request.Context(),
		authenticated.Claims.Subject,
		request.PathValue("organizationID"),
		application.InviteMemberInput{Email: input.Email, Role: input.Role, Quiet: input.Quiet, Guest: input.Guest},
	)
	if err != nil {
		server.handleError(writer, err)
		return
	}
	writeJSON(writer, http.StatusCreated, invitation)
}

func (server *Server) invitations(writer http.ResponseWriter, request *http.Request) {
	authenticated := requestAuth(request)
	invitations, err := server.service.Invitations(
		request.Context(),
		authenticated.Claims.Subject,
		request.PathValue("organizationID"),
	)
	if err != nil {
		server.handleError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"invitations": invitations})
}

func (server *Server) revokeInvitation(writer http.ResponseWriter, request *http.Request) {
	authenticated := requestAuth(request)
	err := server.service.RevokeInvitation(
		request.Context(),
		authenticated.Claims.Subject,
		request.PathValue("organizationID"),
		request.PathValue("invitationID"),
	)
	if err != nil {
		server.handleError(writer, err)
		return
	}
	writer.WriteHeader(http.StatusNoContent)
}

func (server *Server) acceptInvitation(writer http.ResponseWriter, request *http.Request) {
	var input struct {
		Token string `json:"token"`
	}
	if err := readJSON(request, &input); err != nil || strings.TrimSpace(input.Token) == "" {
		writeError(writer, http.StatusBadRequest, "invitation token is required")
		return
	}
	authenticated := requestAuth(request)
	membership, err := server.service.AcceptInvitation(request.Context(), authenticated.Claims.Subject, input.Token)
	if err != nil {
		server.handleError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, membership)
}

func (server *Server) receivedInvitations(writer http.ResponseWriter, request *http.Request) {
	authenticated := requestAuth(request)
	invitations, err := server.service.ReceivedInvitations(request.Context(), authenticated.Claims.Subject)
	if err != nil {
		server.handleError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"invitations": invitations})
}

func (server *Server) acceptReceivedInvitation(writer http.ResponseWriter, request *http.Request) {
	authenticated := requestAuth(request)
	membership, err := server.service.AcceptReceivedInvitation(
		request.Context(),
		authenticated.Claims.Subject,
		request.PathValue("invitationID"),
	)
	if err != nil {
		server.handleError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, membership)
}

func (server *Server) changeMemberRole(writer http.ResponseWriter, request *http.Request) {
	var input struct {
		Role domain.Role `json:"role"`
	}
	if err := readJSON(request, &input); err != nil {
		writeError(writer, http.StatusBadRequest, "invalid request body")
		return
	}
	authenticated := requestAuth(request)
	membership, err := server.service.ChangeMemberRole(
		request.Context(),
		authenticated.Claims.Subject,
		request.PathValue("organizationID"),
		request.PathValue("userID"),
		input.Role,
	)
	if err != nil {
		server.handleError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, membership)
}

func (server *Server) removeMember(writer http.ResponseWriter, request *http.Request) {
	authenticated := requestAuth(request)
	err := server.service.RemoveMember(
		request.Context(),
		authenticated.Claims.Subject,
		request.PathValue("organizationID"),
		request.PathValue("userID"),
	)
	if err != nil {
		server.handleError(writer, err)
		return
	}
	writer.WriteHeader(http.StatusNoContent)
}

// agentAllowedPatterns are the only authenticated-route patterns an agent
// token may use; every other authenticated route answers 403. Matched
// against request.Pattern, the exact pattern net/http's ServeMux resolved
// the request to, never a prefix of the raw path, so no path trick can make
// a different route look like one of these.
var agentAllowedPatterns = map[string]bool{
	"GET /v1/auth/me":                                true,
	"GET /v1/organizations":                          true,
	"GET /v1/organizations/{organizationID}":         true,
	"GET /v1/organizations/{organizationID}/members": true,
}

func (server *Server) authenticated(next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		rawToken, fromCookie, ok := server.requestToken(request)
		if !ok {
			writeError(writer, http.StatusUnauthorized, "access token is required")
			return
		}
		if fromCookie && isMutation(request.Method) && !server.validMutationOrigin(request) {
			writeError(writer, http.StatusForbidden, "origin is required for cookie authentication")
			return
		}
		claims, err := server.verifier.Verify(request.Context(), rawToken)
		if err != nil {
			writeError(writer, http.StatusUnauthorized, "access token is invalid")
			return
		}
		if server.isCLIToken(claims) && !cliscope.Allowed(request.Pattern) {
			writeError(writer, http.StatusForbidden, "CLI sessions can only configure workspace setup")
			return
		}
		if server.isAgentToken(claims) && !agentAllowedPatterns[request.Pattern] {
			writeError(writer, http.StatusForbidden, "agents cannot do this")
			return
		}
		value := authenticatedRequest{Claims: claims, FromCookie: fromCookie}
		if server.isCLIToken(claims) {
			if !server.authorizeCLI(writer, request, rawToken, &value) {
				return
			}
		}
		next.ServeHTTP(writer, request.WithContext(context.WithValue(request.Context(), authContextKey{}, value)))
	})
}

// isAgentToken reports whether claims belongs to an agent, not the member's
// own session: an access token whose client_id is the Cognito agent app
// client's. It only ever recognizes agent tokens when AgentClientID is
// configured, independent of whether AgentGrants itself is enabled, since
// Cognito can issue such a token as soon as the agent app client exists.
func (server *Server) isAgentToken(claims authn.Claims) bool {
	return server.config.AgentClientID != "" && claims.ClientID == server.config.AgentClientID
}

func (server *Server) isCLIToken(claims authn.Claims) bool {
	return server.config.CLIClientID != "" && claims.ClientID == server.config.CLIClientID
}

func (server *Server) requestToken(request *http.Request) (string, bool, bool) {
	if token, ok := bearerToken(request.Header.Get("Authorization")); ok {
		return token, false, true
	}
	if cookie, err := request.Cookie(accessCookieName); err == nil && cookie.Value != "" {
		return cookie.Value, true, true
	}
	return "", false, false
}

func (server *Server) validMutationOrigin(request *http.Request) bool {
	origin := request.Header.Get("Origin")
	if origin == "" {
		return false
	}
	_, ok := server.allowedOrigins[origin]
	return ok
}

func (server *Server) requirePublicMutationOrigin(writer http.ResponseWriter, request *http.Request) bool {
	if server.validMutationOrigin(request) {
		return true
	}
	writeError(writer, http.StatusForbidden, "origin is required for authentication requests")
	return false
}

func (server *Server) setSessionCookies(writer http.ResponseWriter, accessToken, refreshToken string, expiresIn int) {
	if expiresIn <= 0 {
		expiresIn = 3600
	}
	http.SetCookie(writer, &http.Cookie{
		Name:     accessCookieName,
		Value:    accessToken,
		Path:     "/",
		Domain:   server.config.CookieDomain,
		MaxAge:   expiresIn,
		HttpOnly: true,
		Secure:   server.config.CookieSecure,
		SameSite: http.SameSiteLaxMode,
	})
	if refreshToken != "" {
		http.SetCookie(writer, &http.Cookie{
			Name:     refreshCookieName,
			Value:    refreshToken,
			Path:     "/v1/auth",
			Domain:   server.config.CookieDomain,
			MaxAge:   30 * 24 * 60 * 60,
			HttpOnly: true,
			Secure:   server.config.CookieSecure,
			SameSite: http.SameSiteStrictMode,
		})
	}
}

func (server *Server) clearSessionCookies(writer http.ResponseWriter) {
	server.clearCookie(writer, accessCookieName, "/")
	server.clearCookie(writer, refreshCookieName, "/v1/auth")
}

func (server *Server) clearCookie(writer http.ResponseWriter, name, path string) {
	http.SetCookie(writer, &http.Cookie{
		Name:     name,
		Path:     path,
		Domain:   server.config.CookieDomain,
		MaxAge:   -1,
		Expires:  time.Unix(1, 0),
		HttpOnly: true,
		Secure:   server.config.CookieSecure,
		SameSite: http.SameSiteLaxMode,
	})
}

func (server *Server) handleError(writer http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, domain.ErrInvalid):
		writeError(writer, http.StatusBadRequest, strings.TrimSuffix(err.Error(), ": "+domain.ErrInvalid.Error()))
	case errors.Is(err, domain.ErrNotFound):
		writeError(writer, http.StatusNotFound, "resource not found")
	case errors.Is(err, domain.ErrForbidden):
		writeError(writer, http.StatusForbidden, "operation is not allowed")
	case errors.Is(err, domain.ErrUnauthorized):
		writeError(writer, http.StatusUnauthorized, "authentication failed")
	case errors.Is(err, domain.ErrConflict):
		writeError(writer, http.StatusConflict, "resource state conflicts with the request")
	case errors.Is(err, domain.ErrExpired):
		writeError(writer, http.StatusGone, "action has expired")
	case errors.Is(err, domain.ErrLimitReached):
		writeError(writer, http.StatusPaymentRequired, "organization has reached its member limit")
	case errors.Is(err, domain.ErrRateLimited):
		writer.Header().Set("Retry-After", "60")
		writeError(writer, http.StatusTooManyRequests, "too many authentication attempts")
	case errors.Is(err, domain.ErrUnavailable):
		writeError(writer, http.StatusServiceUnavailable, "identity provider did not respond; retry")
	default:
		log.Printf("auth request failed: %v", err)
		writeError(writer, http.StatusInternalServerError, "internal server error")
	}
}

func requestAuth(request *http.Request) authenticatedRequest {
	value, _ := request.Context().Value(authContextKey{}).(authenticatedRequest)
	return value
}

func bearerToken(value string) (string, bool) {
	parts := strings.Fields(value)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") || parts[1] == "" {
		return "", false
	}
	return parts[1], true
}

func isMutation(method string) bool {
	return method != http.MethodGet && method != http.MethodHead && method != http.MethodOptions
}

func readJSON(request *http.Request, target any) error {
	decoder := json.NewDecoder(io.LimitReader(request.Body, 1<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("request body must contain one JSON object")
	}
	return cleantext.Check(target)
}

func noStore(writer http.ResponseWriter) {
	writer.Header().Set("Cache-Control", "no-store")
}

func writeError(writer http.ResponseWriter, status int, message string) {
	writeJSON(writer, status, map[string]string{"error": message})
}

func writeJSON(writer http.ResponseWriter, status int, value any) {
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(value)
}
