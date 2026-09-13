package httpserver

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/Shubham04-oss/oryxa-go/auth/internal/application"
	"github.com/Shubham04-oss/oryxa-go/auth/internal/authn"
	"github.com/Shubham04-oss/oryxa-go/auth/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/auth/internal/oauthflow"
	"github.com/Shubham04-oss/oryxa-go/auth/internal/passwordflow"
)

const (
	accessCookieName  = "oryxa_access"
	refreshCookieName = "oryxa_refresh"
	stateCookieName   = "oryxa_login_state"
)

type accessTokenVerifier interface {
	Verify(context.Context, string) (authn.Claims, error)
}

type authFlow interface {
	Begin(context.Context, string) (string, string, error)
	Complete(context.Context, string, string, string) (oauthflow.Session, error)
	Refresh(context.Context, string) (oauthflow.Tokens, error)
	Revoke(context.Context, string) error
}

type passwordAuth interface {
	SignUp(context.Context, string, string, string) error
	ResendEmailVerification(context.Context, string) error
	ConfirmEmail(context.Context, string) error
	Login(context.Context, string, string) (passwordflow.Session, error)
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
}

type Server struct {
	verifier       accessTokenVerifier
	oauth          authFlow
	passwords      passwordAuth
	service        authService
	config         Config
	allowedOrigins map[string]struct{}
	mux            *http.ServeMux
}

type authContextKey struct{}

type authenticatedRequest struct {
	Claims     authn.Claims
	FromCookie bool
}

func New(verifier accessTokenVerifier, oauth authFlow, passwords passwordAuth, service authService, config Config) http.Handler {
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

	server.mux.HandleFunc("GET /healthz", server.health)
	server.mux.HandleFunc("GET /v1/auth/config", server.authConfig)
	server.mux.HandleFunc("GET /v1/auth/login", server.login)
	server.mux.HandleFunc("GET /v1/auth/callback", server.callback)
	server.mux.HandleFunc("POST /v1/auth/refresh", server.refresh)
	server.mux.HandleFunc("POST /v1/auth/logout", server.logout)
	server.mux.HandleFunc("POST /v1/auth/password/signup", server.passwordSignUp)
	server.mux.HandleFunc("POST /v1/auth/password/verify", server.passwordVerifyEmail)
	server.mux.HandleFunc("POST /v1/auth/password/resend-verification", server.passwordResendVerification)
	server.mux.HandleFunc("POST /v1/auth/password/login", server.passwordLogin)
	server.mux.HandleFunc("POST /v1/auth/password/forgot", server.passwordForgot)
	server.mux.HandleFunc("POST /v1/auth/password/reset", server.passwordReset)
	server.mux.Handle("GET /v1/auth/me", server.authenticated(http.HandlerFunc(server.me)))
	server.mux.Handle("GET /v1/organizations", server.authenticated(http.HandlerFunc(server.organizations)))
	server.mux.Handle("POST /v1/organizations", server.authenticated(http.HandlerFunc(server.createOrganization)))
	server.mux.Handle("GET /v1/organizations/{organizationID}", server.authenticated(http.HandlerFunc(server.organization)))
	server.mux.Handle("PATCH /v1/organizations/{organizationID}", server.authenticated(http.HandlerFunc(server.updateOrganization)))
	server.mux.Handle("DELETE /v1/organizations/{organizationID}", server.authenticated(http.HandlerFunc(server.deleteOrganization)))
	server.mux.Handle("GET /v1/organizations/{organizationID}/members", server.authenticated(http.HandlerFunc(server.members)))
	server.mux.Handle("POST /v1/organizations/{organizationID}/invitations", server.authenticated(http.HandlerFunc(server.inviteMember)))
	server.mux.Handle("GET /v1/organizations/{organizationID}/invitations", server.authenticated(http.HandlerFunc(server.invitations)))
	server.mux.Handle("DELETE /v1/organizations/{organizationID}/invitations/{invitationID}", server.authenticated(http.HandlerFunc(server.revokeInvitation)))
	server.mux.Handle("POST /v1/invitations/accept", server.authenticated(http.HandlerFunc(server.acceptInvitation)))
	server.mux.Handle("PATCH /v1/organizations/{organizationID}/members/{userID}", server.authenticated(http.HandlerFunc(server.changeMemberRole)))
	server.mux.Handle("DELETE /v1/organizations/{organizationID}/members/{userID}", server.authenticated(http.HandlerFunc(server.removeMember)))

	return server
}

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
	writeJSON(writer, http.StatusOK, map[string]any{
		"authorization_endpoint": strings.TrimRight(server.config.CognitoDomain, "/") + "/oauth2/authorize",
		"callback_url":           server.config.CognitoCallbackURL,
		"client_id":              server.config.CognitoClientID,
		"identity_provider":      "Google",
		"identity_providers":     []string{"Google", "Password"},
		"password_policy": map[string]any{
			"minimum_length":     12,
			"requires_lowercase": true,
			"requires_number":    true,
			"requires_symbol":    true,
			"requires_uppercase": true,
		},
		"response_type": "code",
		"scopes":        []string{"openid", "email", "profile"},
	})
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
	tokens, err := server.oauth.Refresh(request.Context(), cookie.Value)
	if err != nil {
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
		log.Printf("revoke Cognito token: %v", err)
	}
	if rawToken, _, ok := server.requestToken(request); ok {
		if claims, err := server.verifier.Verify(request.Context(), rawToken); err == nil {
			if err := server.service.SignOut(request.Context(), claims.Subject); err != nil {
				log.Printf("enqueue sign-out event: %v", err)
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
	writeJSON(writer, http.StatusOK, map[string]any{"user": user, "organizations": organizations})
}

func (server *Server) organizations(writer http.ResponseWriter, request *http.Request) {
	authenticated := requestAuth(request)
	organizations, err := server.service.Organizations(request.Context(), authenticated.Claims.Subject)
	if err != nil {
		server.handleError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"organizations": organizations})
}

func (server *Server) createOrganization(writer http.ResponseWriter, request *http.Request) {
	var input application.CreateOrganizationInput
	if err := readJSON(request, &input); err != nil {
		writeError(writer, http.StatusBadRequest, "invalid request body")
		return
	}
	authenticated := requestAuth(request)
	organization, err := server.service.CreateOrganization(request.Context(), authenticated.Claims.Subject, input)
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
	writeJSON(writer, http.StatusOK, map[string]any{"organization": organization, "membership": membership})
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
		application.InviteMemberInput{Email: input.Email, Role: input.Role},
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
		value := authenticatedRequest{Claims: claims, FromCookie: fromCookie}
		next.ServeHTTP(writer, request.WithContext(context.WithValue(request.Context(), authContextKey{}, value)))
	})
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
	return nil
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
