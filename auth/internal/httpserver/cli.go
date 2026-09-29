package httpserver

import (
	"net/http"
	"time"

	"github.com/Shubham04-oss/oryxa-go/auth/internal/application"
	"github.com/Shubham04-oss/oryxa-go/auth/internal/clisession"
	"github.com/Shubham04-oss/oryxa-go/auth/internal/domain"
)

func (server *Server) cliChallenge(writer http.ResponseWriter, request *http.Request) {
	if server.config.CLIClientID == "" || server.config.CLISessions == nil {
		writeError(writer, http.StatusNotFound, "CLI sign-in is not configured")
		return
	}
	// Native clients have no Origin. A browser may only initiate from a trusted origin.
	if request.Header.Get("Origin") != "" && !server.validMutationOrigin(request) {
		writeError(writer, http.StatusForbidden, "origin is not allowed")
		return
	}
	if !server.cliChallengeLimits.allow(request.RemoteAddr, time.Now()) {
		server.handleError(writer, domain.ErrRateLimited)
		return
	}
	challenge := clisession.NewChallenge()
	if err := server.config.CLISessions.CreateCLIChallenge(request.Context(), challenge); err != nil {
		server.handleError(writer, err)
		return
	}
	writer.Header().Set("Cache-Control", "no-store")
	writeJSON(writer, http.StatusOK, challenge)
}

// cliSession verifies a nonce issued by this server, atomically consumes the
// challenge, and admits this access token for setup for at most fifteen minutes.
func (server *Server) cliSession(writer http.ResponseWriter, request *http.Request) {
	if server.config.CLIClientID == "" || server.config.CLIVerifier == nil || server.config.CLICompleteLogin == nil || server.config.CLISessions == nil {
		writeError(writer, http.StatusNotFound, "CLI sign-in is not configured")
		return
	}
	rawAccess, ok := bearerToken(request.Header.Get("Authorization"))
	if !ok {
		writeError(writer, http.StatusUnauthorized, "CLI access token is required")
		return
	}
	access, err := server.verifier.Verify(request.Context(), rawAccess)
	if err != nil || access.ClientID != server.config.CLIClientID || access.ExpiresAt <= time.Now().Unix() {
		writeError(writer, http.StatusUnauthorized, "CLI access token is invalid")
		return
	}
	var input struct {
		IDToken string `json:"id_token"`
		State   string `json:"state"`
	}
	if err := readJSON(request, &input); err != nil {
		writeError(writer, http.StatusBadRequest, "invalid request body")
		return
	}
	challenge, err := server.config.CLISessions.CLIChallenge(request.Context(), input.State)
	if err != nil {
		server.handleError(writer, err)
		return
	}
	identity, err := server.config.CLIVerifier.VerifyCLIIdentityToken(request.Context(), input.IDToken, challenge.Nonce)
	if err != nil || identity.Subject != access.Subject {
		writeError(writer, http.StatusUnauthorized, "CLI identity token is invalid")
		return
	}
	user, _, err := server.config.CLICompleteLogin(request.Context(), application.FederatedIdentity{Subject: identity.Subject, Email: identity.Email, DisplayName: identity.Name, AvatarURL: identity.Picture, Provider: "Cognito CLI"})
	if err != nil {
		server.handleError(writer, err)
		return
	}
	expires := time.Unix(access.ExpiresAt, 0)
	if ceiling := time.Now().Add(clisession.SessionTTL); expires.After(ceiling) {
		expires = ceiling
	}
	if err := server.config.CLISessions.AdmitCLISession(request.Context(), input.State, challenge.Nonce, clisession.Hash(rawAccess), expires); err != nil {
		server.handleError(writer, err)
		return
	}
	writer.Header().Set("Cache-Control", "no-store")
	writeJSON(writer, http.StatusOK, map[string]any{"user": user, "expires_at": expires})
}

func (server *Server) authorizeCLI(writer http.ResponseWriter, request *http.Request, rawToken string, authenticated *authenticatedRequest) bool {
	if server.config.CLISessions == nil {
		writeError(writer, http.StatusUnauthorized, "CLI session is not admitted")
		return false
	}
	hash := clisession.Hash(rawToken)
	session, err := server.config.CLISessions.CLISession(request.Context(), hash)
	if err != nil {
		server.handleError(writer, err)
		return false
	}
	authenticated.CLITokenHash = hash
	authenticated.CLIOrganizationID = session.OrganizationID
	orgID := request.PathValue("organizationID")
	if orgID != "" && session.OrganizationID != "" && session.OrganizationID != orgID {
		writeError(writer, http.StatusForbidden, "CLI session is bound to another organization")
		return false
	}
	checkedOrg := orgID
	if checkedOrg == "" && session.OrganizationID != clisession.CreatingOrganization {
		checkedOrg = session.OrganizationID
	}
	if checkedOrg != "" {
		// Recheck current administrator authority on every use, including
		// discovery. A demotion or departure immediately ends setup access.
		_, membership, err := server.service.Organization(request.Context(), authenticated.Claims.Subject, checkedOrg)
		if err != nil {
			server.handleError(writer, err)
			return false
		}
		if membership.Role != domain.RoleOwner && membership.Role != domain.RoleAdmin {
			writeError(writer, http.StatusForbidden, "CLI setup requires an organization administrator")
			return false
		}
	}
	if orgID != "" {
		if err := server.config.CLISessions.BindCLISession(request.Context(), hash, orgID); err != nil {
			server.handleError(writer, err)
			return false
		}
		authenticated.CLIOrganizationID = orgID
	} else if request.Pattern == "POST /v1/organizations" && session.OrganizationID != "" {
		writeError(writer, http.StatusForbidden, "CLI session already selected an organization")
		return false
	}

	return true
}

func filterCLIOrganizations(authenticated authenticatedRequest, organizations []domain.Organization) []domain.Organization {
	if authenticated.CLITokenHash == "" || authenticated.CLIOrganizationID == "" {
		return organizations
	}
	filtered := []domain.Organization{}
	for _, org := range organizations {
		if org.ID == authenticated.CLIOrganizationID {
			filtered = append(filtered, org)
		}
	}
	return filtered
}
