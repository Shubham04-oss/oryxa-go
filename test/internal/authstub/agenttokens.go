// Agent tokens.
//
// In production, agents proves itself to auth with a signed
// X-Oryxa-Service-Token (internal/serviceauth: audience "auth", purpose
// "agent") and calls auth's POST /v1/internal/agent-tokens to exchange a
// member's standing "act for me while I'm away" grant for a short-lived
// access token — see auth/README.md's "Agent grants" section and
// auth/internal/httpserver/internal.go's mintAgentToken. This file mirrors
// that route's request and response JSON exactly, verifying the service
// token the way auth does: reusing internal/serviceauth's own Verifier,
// against the dev KMS key in LocalStack, rather than reinventing the
// crypto.
package authstub

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"

	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/kms"

	"github.com/Shubham04-oss/oryxa-go/internal/serviceauth"
	"github.com/Shubham04-oss/oryxa-go/test/identities"
)

// serviceAudience is the audience agents names when it signs its service
// token for this route (agents/primary/internal/memberauth/client.go),
// matching real auth's own private constant of the same name
// (auth/service.go).
const serviceAudience = "auth"

// agentServiceCaller is the only service this stand-in accepts on the
// route: agents is presently the sole caller of
// POST /v1/internal/agent-tokens.
const agentServiceCaller = "agents"

// agentTokenPurpose is the only serviceauth purpose this route accepts,
// mirroring auth's own agentServicePurpose
// (auth/internal/httpserver/internal.go).
const agentTokenPurpose = "agent"

// agentTokenExpiresIn is how long a minted agent token is said to live.
// Real auth's own answer comes from Cognito's agent app client, provisioned
// with a one-hour access token (auth/README.md's provisioning section);
// the stand-in just repeats that number.
const agentTokenExpiresIn = 3600

// ServiceVerifier checks a signed X-Oryxa-Service-Token, exactly the
// interface real auth's own httpserver.ServiceVerifier declares. A
// *serviceauth.Verifier implements it.
type ServiceVerifier interface {
	Verify(context.Context, string) (serviceauth.Claims, error)
}

// Config configures NewHandler. The zero Config is what Handler() itself
// uses: Services is nil, so POST /v1/internal/agent-tokens refuses every
// caller with 401, exactly as real auth does when AUTH_SERVICE_CALLERS is
// unset.
type Config struct {
	// Services verifies the caller's signed X-Oryxa-Service-Token.
	Services ServiceVerifier
	// Ungranted names stand-in UserIDs (identities.Person.UserID) to treat
	// as though they revoked, or never gave, their standing "act for me
	// while I'm away" grant: minting a token for them answers 404, the way
	// real auth does for a member with no stored grant. A stand-in member
	// absent from Ungranted is treated as granted, which is every member
	// by default (Ungranted is nil).
	Ungranted map[string]bool
}

// ConfigFromEnv builds Config the way test/cmd/authstub runs it against the
// local dev stack:
//
//   - AGENTS_PRIMARY_SERVICE_KEY_ID names the KMS key agents signs its
//     service token with — the same variable agents itself reads
//     (agents/primary/internal/serviceconfig, agents/primary/internal/
//     assembly.MemberTokens). Unset, Config.Services is nil and the route
//     refuses every caller, just as real auth does without
//     AUTH_SERVICE_CALLERS.
//   - AWS_REGION, AWS_ENDPOINT_URL_KMS, and the usual AWS credential
//     environment variables address LocalStack's KMS, the same way every
//     other AWS client in this stack does (internal/host.Platform.AWS).
//   - AUTHSTUB_UNGRANTED_MEMBERS is a comma-separated list of stand-in
//     UserIDs to answer as ungranted; see Config.Ungranted.
func ConfigFromEnv(ctx context.Context) (Config, error) {
	config := Config{Ungranted: parseUngranted(os.Getenv("AUTHSTUB_UNGRANTED_MEMBERS"))}

	keyID := strings.TrimSpace(os.Getenv("AGENTS_PRIMARY_SERVICE_KEY_ID"))
	if keyID == "" {
		return config, nil
	}
	region := strings.TrimSpace(os.Getenv("AWS_REGION"))
	if region == "" {
		region = "ap-south-1"
	}
	awsConfig, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(region))
	if err != nil {
		return config, fmt.Errorf("authstub: load AWS config: %w", err)
	}
	verifier, err := serviceauth.NewVerifier(serviceauth.VerifierConfig{
		Keys:     kms.NewFromConfig(awsConfig),
		Audience: serviceAudience,
		Callers:  []serviceauth.Caller{{Service: agentServiceCaller, KeyID: keyID, Purposes: []string{agentTokenPurpose}}},
	})
	if err != nil {
		return config, fmt.Errorf("authstub: build service verifier: %w", err)
	}
	config.Services = verifier
	return config, nil
}

func parseUngranted(raw string) map[string]bool {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	ungranted := make(map[string]bool)
	for _, id := range strings.Split(raw, ",") {
		if id = strings.TrimSpace(id); id != "" {
			ungranted[id] = true
		}
	}
	return ungranted
}

// mintAgentToken mirrors auth's own POST /v1/internal/agent-tokens
// (auth/internal/httpserver/internal.go): a service proves its identity
// with a signed token in serviceauth.Header and gets back a token it can
// present as the named member while they are away. Every stand-in member
// is treated as having granted that, per Config.Ungranted.
func (config Config) mintAgentToken(writer http.ResponseWriter, request *http.Request) {
	writer.Header().Set("Cache-Control", "no-store")

	token := request.Header.Get(serviceauth.Header)
	if token == "" {
		writeJSON(writer, http.StatusUnauthorized, map[string]string{"error": "a service token is required"})
		return
	}
	if config.Services == nil {
		writeJSON(writer, http.StatusUnauthorized, map[string]string{"error": "service identity is not accepted"})
		return
	}
	claims, err := config.Services.Verify(request.Context(), token)
	switch {
	case errors.Is(err, serviceauth.ErrPurposeNotAllowed):
		writeJSON(writer, http.StatusForbidden, map[string]string{"error": "the service may not use this purpose"})
		return
	case errors.Is(err, serviceauth.ErrInvalidToken):
		writeJSON(writer, http.StatusUnauthorized, map[string]string{"error": "service token is invalid"})
		return
	case err != nil:
		writeJSON(writer, http.StatusServiceUnavailable, map[string]string{"error": "service identity could not be checked; retry"})
		return
	}
	if claims.Purpose != agentTokenPurpose {
		writeJSON(writer, http.StatusForbidden, map[string]string{"error": "the service may not use this purpose"})
		return
	}

	var input struct {
		UserID string `json:"user_id"`
	}
	if err := json.NewDecoder(io.LimitReader(request.Body, 1<<20)).Decode(&input); err != nil || strings.TrimSpace(input.UserID) == "" {
		writeJSON(writer, http.StatusBadRequest, map[string]string{"error": "user_id is required"})
		return
	}
	if _, ok := identities.PersonByUserID(input.UserID); !ok || config.Ungranted[input.UserID] {
		writeJSON(writer, http.StatusNotFound, map[string]string{"error": "resource not found"})
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{
		"access_token": agentTokenPrefix + input.UserID,
		"expires_in":   agentTokenExpiresIn,
	})
}
