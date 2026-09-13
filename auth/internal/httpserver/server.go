package httpserver

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/Shubham04-oss/oryxa-go/auth/internal/authn"
)

type accessTokenVerifier interface {
	Verify(context.Context, string) (authn.Claims, error)
}

type Config struct {
	CognitoDomain   string
	CognitoClientID string
}

func New(verifier accessTokenVerifier, config Config) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("GET /v1/auth/config", func(writer http.ResponseWriter, _ *http.Request) {
		writeJSON(writer, http.StatusOK, map[string]any{
			"authorization_endpoint": strings.TrimRight(config.CognitoDomain, "/") + "/oauth2/authorize",
			"client_id":              config.CognitoClientID,
			"identity_provider":      "Google",
			"response_type":          "code",
			"scopes":                 []string{"openid", "email", "profile"},
		})
	})
	mux.HandleFunc("GET /v1/auth/me", func(writer http.ResponseWriter, request *http.Request) {
		rawToken, ok := bearerToken(request.Header.Get("Authorization"))
		if !ok {
			writeJSON(writer, http.StatusUnauthorized, map[string]string{"error": "bearer access token required"})
			return
		}
		claims, err := verifier.Verify(request.Context(), rawToken)
		if err != nil {
			writeJSON(writer, http.StatusUnauthorized, map[string]string{"error": "invalid access token"})
			return
		}
		writeJSON(writer, http.StatusOK, map[string]string{
			"subject": claims.Subject,
			"scope":   claims.Scope,
		})
	})
	return mux
}

func bearerToken(value string) (string, bool) {
	parts := strings.Fields(value)
	returnToken := len(parts) == 2 && strings.EqualFold(parts[0], "Bearer") && parts[1] != ""
	if !returnToken {
		return "", false
	}
	return parts[1], true
}

func writeJSON(writer http.ResponseWriter, status int, value any) {
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(value)
}
