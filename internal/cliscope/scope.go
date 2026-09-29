// Package cliscope limits native CLI credentials to workspace setup routes.
// It never grants membership or roles: auth clients still verify both with auth.
package cliscope

import (
	"context"
	"net/http"
)

type setupKey struct{}

// Allowed matches the registered HTTP pattern, never a caller-controlled path
// prefix. A new route is unavailable to CLI credentials until reviewed here.
func Allowed(pattern string) bool {
	switch pattern {
	case "GET /v1/auth/me",
		"GET /v1/organizations", "POST /v1/organizations",
		"GET /v1/organizations/{organizationID}", "PATCH /v1/organizations/{organizationID}",
		"GET /v1/organizations/{organizationID}/members",
		"POST /v1/organizations/{organizationID}/invitations", "GET /v1/organizations/{organizationID}/invitations",
		"DELETE /v1/organizations/{organizationID}/invitations/{invitationID}",
		"PATCH /v1/organizations/{organizationID}/members/{userID}",
		"DELETE /v1/organizations/{organizationID}/members/{userID}",
		"GET /v1/organizations/{organizationID}/directory",
		"GET /v1/organizations/{organizationID}/teams", "POST /v1/organizations/{organizationID}/teams",
		"GET /v1/organizations/{organizationID}/teams/{teamID}", "PATCH /v1/organizations/{organizationID}/teams/{teamID}",
		"DELETE /v1/organizations/{organizationID}/teams/{teamID}",
		"GET /v1/organizations/{organizationID}/teams/{teamID}/members", "POST /v1/organizations/{organizationID}/teams/{teamID}/members",
		"PATCH /v1/organizations/{organizationID}/teams/{teamID}/members/{userID}",
		"DELETE /v1/organizations/{organizationID}/teams/{teamID}/members/{userID}",
		"GET /v1/organizations/{organizationID}/organization-agent", "PATCH /v1/organizations/{organizationID}/organization-agent",
		"GET /v1/organizations/{organizationID}/apps/offer",
		"GET /v1/organizations/{organizationID}/apps/connections", "POST /v1/organizations/{organizationID}/apps/connections",
		"GET /v1/organizations/{organizationID}/apps/connections/{connectionID}":
		return true
	}
	return false
}

// Wrap marks only an explicitly registered setup handler. No header, token
// claim supplied by the caller, or raw URL can enable this marker.
func Wrap(pattern string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), setupKey{}, Allowed(pattern))))
	})
}

// AllowedContext is checked after trusted auth introspection reports cli:true.
func AllowedContext(ctx context.Context) bool {
	allowed, _ := ctx.Value(setupKey{}).(bool)
	return allowed
}
