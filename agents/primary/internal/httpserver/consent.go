package httpserver

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	primaryagents "github.com/Shubham04-oss/oryxa-go/agents/primary/internal/agents"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/interactions"
)

// SecretShares keeps the one-time secrets an owner shares with the member who
// asked, sealed, and is where they are opened once and deleted.
// secretshares.Store implements it. Every call comes after the ledger has
// authorized it (AgentInteractions.AuthorizeSecretPut, ...View, ...Revoke), and
// the share it is handed names the request and the two members: the store
// checks nothing more of who may. It is set only when there is one; without it
// the secret routes answer 503.
//
// A value is never logged, put in an event, or returned but to the member who
// asked, once. View deletes the value it returns in the same transaction that
// records the view (interactions.RecordSecretViewed), so a secret is never
// opened without its record, nor recorded without having been opened.
type SecretShares interface {
	// Put seals value for the request the share names, replacing any secret
	// already shared for it, and has it expire ttl from now.
	Put(ctx context.Context, share interactions.SecretShare, value string, ttl time.Duration) (interactions.SecretMeta, error)
	// View returns the value and deletes it, once. A secret that was never
	// shared, or was viewed, revoked or has expired, is interactions.ErrSecretGone.
	View(ctx context.Context, share interactions.SecretShare) (interactions.SecretValue, error)
	// Revoke deletes the secret, whether or not it was opened; there being
	// none is interactions.ErrSecretGone.
	Revoke(ctx context.Context, share interactions.SecretShare) error
}

// consentRequest is an owner's choice of a request that asks for consent.
type consentRequest struct {
	Choice string `json:"choice"`
}

// consentResponse is what an owner's choice came to: the request as it now
// stands, and, when the choice was access, the lease it gave.
type consentResponse struct {
	Request primaryagents.Interaction `json:"request"`
	Lease   *interactions.Lease       `json:"lease,omitempty"`
}

// consentWinner is who won a choice that lost: what the request's decision
// says. A request that is not decided, such as one that expired, has none of
// it.
type consentWinner struct {
	RequestID string                            `json:"request_id"`
	Status    string                            `json:"status"`
	Revision  int64                             `json:"revision"`
	Decision  string                            `json:"decision,omitempty"`
	Consent   string                            `json:"consent,omitempty"`
	DecidedBy *primaryagents.InteractionDecider `json:"decided_by,omitempty"`
	DecidedAt *time.Time                        `json:"decided_at,omitempty"`
}

func winnerOf(request primaryagents.Interaction) consentWinner {
	winner := consentWinner{RequestID: request.ID, Status: request.Status, Revision: request.Revision}
	if decision := request.Decision; decision != nil {
		decidedAt := decision.DecidedAt
		winner.Decision, winner.Consent, winner.DecidedBy, winner.DecidedAt = decision.Decision, decision.Consent, decision.DecidedBy, &decidedAt
	}
	return winner
}

// consentInteraction is the member deciding a request that asks for consent,
// themselves, with one of three choices:
//
//	POST /v1/organizations/{org}/agent-requests/{id}/consent {"choice": "summary" | "access" | "deny"}
//
// It is the member's own call in their own session: a cookie needs an allowed
// origin, and an agent's token is refused. summary approves granting an AI
// summary alone, access approves granting excerpts and gives the asker a lease
// on their follow-ups in that Slack thread, and deny rejects. It answers 200
// with the request as it stands and the lease, if any:
//
//	{"request": {...}, "lease": {...}}
//
// Whoever decides first wins, whether here or on the button of the notice in
// Slack, and a choice that lost is 409 already_decided, carrying the winner:
//
//	{"error": {"code": "already_decided", "message": "..."}, "winner": {"decision": ..., "consent": ..., "decided_by": {"surface": ...}}}
func (server *Server) consentInteraction(writer http.ResponseWriter, request *http.Request) {
	member, ok := server.memberWithAgent(writer, request)
	if !ok {
		return
	}
	if server.interactions == nil {
		writeError(writer, http.StatusServiceUnavailable, "interactions_unavailable", "agent interactions are not configured")
		return
	}
	var body consentRequest
	if !server.decode(writer, request, &body) {
		return
	}
	result, err := server.interactions.ConsentAsOwner(request.Context(), interactionActorOf(member), request.PathValue("requestID"), body.Choice)
	if errors.Is(err, interactions.ErrAlreadyDecided) {
		writeJSON(writer, http.StatusConflict, map[string]any{
			"error":  map[string]string{"code": "already_decided", "message": message(err)},
			"winner": winnerOf(result.Request),
		})
		return
	}
	if err != nil {
		server.handleError(writer, request, err)
		return
	}
	writeJSON(writer, http.StatusOK, consentResponse{Request: result.Request, Lease: result.Lease})
}

// interactionActivity lists what happened to the member's Slack asks, newest
// first: asks made and received, how each was decided and where, bypasses,
// leases and secrets opened.
//
//	GET /v1/organizations/{org}/agent-requests/activity?limit=
//
// It answers {"activity": [...]}: names, states and times, never a question
// or an answer.
func (server *Server) interactionActivity(writer http.ResponseWriter, request *http.Request) {
	member, ok := server.memberWithAgent(writer, request)
	if !ok {
		return
	}
	if server.interactions == nil {
		writeError(writer, http.StatusServiceUnavailable, "interactions_unavailable", "agent interactions are not configured")
		return
	}
	limit, ok := server.limit(writer, request)
	if !ok {
		return
	}
	entries, err := server.interactions.Activity(request.Context(), interactionActorOf(member), limit)
	if err != nil {
		server.handleError(writer, request, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"activity": entries})
}

// revokeLease is the owner ending, before it runs out, the access they gave an
// asker in one Slack thread:
//
//	DELETE /v1/organizations/{org}/agent-leases/{id}
//
// From then on the asker's follow-ups there ask again, and no answer to a
// request approved under the lease is delivered. It answers {"lease": {...}}
// with the lease as it now stands. Revoking one that already ended changes
// nothing. A lease that is not the member's to revoke is 404.
func (server *Server) revokeLease(writer http.ResponseWriter, request *http.Request) {
	member, ok := server.memberWithAgent(writer, request)
	if !ok {
		return
	}
	if server.interactions == nil {
		writeError(writer, http.StatusServiceUnavailable, "interactions_unavailable", "agent interactions are not configured")
		return
	}
	lease, err := server.interactions.RevokeLease(request.Context(), interactionActorOf(member), request.PathValue("leaseID"))
	if err != nil {
		server.handleError(writer, request, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"lease": lease})
}

// secretRequest is a secret an owner shares. ttl_seconds is how long it waits to
// be opened, from a minute to an hour, and an hour when it is left out.
type secretRequest struct {
	Value      string `json:"value"`
	TTLSeconds int    `json:"ttl_seconds"`
}

// putSecret is the owner sharing a secret for a request they approved with
// access, which the member who asked can then open once:
//
//	PUT /v1/organizations/{org}/agent-requests/{id}/secret {"value": "...", "ttl_seconds": 3600}
//
// The owner types the value in themselves, so it never passes through a model.
// It answers {"request_id": ..., "expires_at": ...}, and never the value.
func (server *Server) putSecret(writer http.ResponseWriter, request *http.Request) {
	member, ok := server.memberWithAgent(writer, request)
	if !ok {
		return
	}
	if !server.secretsAvailable(writer) {
		return
	}
	var body secretRequest
	if !server.decode(writer, request, &body) {
		return
	}
	switch {
	case strings.TrimSpace(body.Value) == "" || utf8.RuneCountInString(body.Value) > interactions.MaxSecretRunes:
		writeError(writer, http.StatusBadRequest, "invalid_request", "value is required and holds at most 16000 characters")
		return
	case body.TTLSeconds < 0 || body.TTLSeconds > int(interactions.MaxSecretTTL/time.Second):
		writeError(writer, http.StatusBadRequest, "invalid_request", "ttl_seconds is from 60 to 3600")
		return
	}
	requestID := request.PathValue("requestID")
	share, ttl, err := server.interactions.AuthorizeSecretPut(request.Context(), interactionActorOf(member), requestID,
		time.Duration(body.TTLSeconds)*time.Second)
	if err != nil {
		server.handleError(writer, request, err)
		return
	}
	meta, err := server.secretShares.Put(request.Context(), share, body.Value, ttl)
	if err != nil {
		server.handleError(writer, request, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"request_id": share.RequestID, "expires_at": meta.ExpiresAt})
}

// viewSecret is the member who asked opening, once, the secret the owner
// shared for their request:
//
//	GET /v1/organizations/{org}/agent-requests/{id}/secret
//
// It answers {"value": "...", "expires_at": ...} and deletes the secret: a
// second call is 410 secret_gone, as it is for one revoked or expired. Only the
// member who asked may open it. Opening consumes it, so it is not a read that
// any page may trigger: a request made with the browser's cookie must come from
// an allowed origin, or be a same-origin fetch, and HEAD, which a GET route
// also answers, is refused.
func (server *Server) viewSecret(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		writer.Header().Set("Allow", http.MethodGet)
		writeError(writer, http.StatusMethodNotAllowed, "method_not_allowed", "a secret is opened with GET")
		return
	}
	if !server.mayConsume(request) {
		writeError(writer, http.StatusForbidden, "forbidden", "origin is required for cookie authentication")
		return
	}
	member, ok := server.memberWithAgent(writer, request)
	if !ok {
		return
	}
	if !server.secretsAvailable(writer) {
		return
	}
	share, err := server.interactions.AuthorizeSecretView(request.Context(), interactionActorOf(member), request.PathValue("requestID"))
	if err != nil {
		server.handleError(writer, request, err)
		return
	}
	secret, err := server.secretShares.View(request.Context(), share)
	if err != nil {
		server.handleError(writer, request, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"value": secret.Value, "expires_at": secret.ExpiresAt})
}

// revokeSecret is the owner taking back a secret they shared, before it is
// opened or expires:
//
//	DELETE /v1/organizations/{org}/agent-requests/{id}/secret
//
// It answers 204, whether the secret was still there or had already gone.
func (server *Server) revokeSecret(writer http.ResponseWriter, request *http.Request) {
	member, ok := server.memberWithAgent(writer, request)
	if !ok {
		return
	}
	if !server.secretsAvailable(writer) {
		return
	}
	share, err := server.interactions.AuthorizeSecretRevoke(request.Context(), interactionActorOf(member), request.PathValue("requestID"))
	if err != nil {
		server.handleError(writer, request, err)
		return
	}
	if err := server.secretShares.Revoke(request.Context(), share); err != nil && !errors.Is(err, interactions.ErrSecretGone) {
		server.handleError(writer, request, err)
		return
	}
	writer.WriteHeader(http.StatusNoContent)
}

// secretsAvailable answers 503 itself, and says so, when the ledger or the
// store of secrets is not configured here.
func (server *Server) secretsAvailable(writer http.ResponseWriter) bool {
	if server.interactions == nil {
		writeError(writer, http.StatusServiceUnavailable, "interactions_unavailable", "agent interactions are not configured")
		return false
	}
	if server.secretShares == nil {
		writeError(writer, http.StatusServiceUnavailable, "secrets_unavailable", "one-time secrets are not kept on this server")
		return false
	}
	return true
}

// mayConsume says whether a GET that consumes what it reads may go ahead. A
// bearer token is not something another site can attach to a request, so it may.
// A cookie is, so a request that carries one must come from an origin the
// server allows, or from no other site at all: the browser marks its
// same-origin fetches, and the other sites' with Sec-Fetch-Site.
func (server *Server) mayConsume(request *http.Request) bool {
	if request.Header.Get("Authorization") != "" {
		return true
	}
	if origin := request.Header.Get("Origin"); origin != "" {
		_, allowed := server.allowedOrigins[origin]
		return allowed
	}
	switch request.Header.Get("Sec-Fetch-Site") {
	case "", "same-origin":
		return true
	}
	return false
}
