package httpserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/edgedispatch"
	edgev1 "github.com/Shubham04-oss/oryxa-go/contracts/edge/v1"
)

type fakeEdgeTokens struct {
	claims edgev1.TokenClaims
	err    error
	got    string
}

func (tokens *fakeEdgeTokens) Verify(token string) (edgev1.TokenClaims, error) {
	tokens.got = token
	return tokens.claims, tokens.err
}

type fakeDispatcher struct {
	requests []edgedispatch.Request
	err      error
}

func (dispatcher *fakeDispatcher) Dispatch(_ context.Context, request edgedispatch.Request) (edgedispatch.Accepted, error) {
	dispatcher.requests = append(dispatcher.requests, request)
	if dispatcher.err != nil {
		return edgedispatch.Accepted{}, dispatcher.err
	}
	return edgedispatch.Accepted{DispatchID: request.DispatchID, ThreadID: "thr_9", RunID: "run_9"}, nil
}

func dispatchClaims() edgev1.TokenClaims {
	return edgev1.TokenClaims{Issuer: edgev1.IssuerEdge, Audience: edgev1.AudienceAgents, Org: "org_1", Team: "team_1",
		Room: edgev1.Room{Kind: edgev1.RoomTeamspace, ID: "room_1"}, Member: "usr_1", ID: "jti_1",
		Scopes: []edgev1.Scope{edgev1.ScopeCallSpeak}}
}

const dispatchBody = `{"dispatchId":"dsp_1","org":"org_1","team":"team_1","room":{"kind":"teamspace","id":"room_1"},` +
	`"call":"call_1","member":"usr_1","said":"Krude, what's on today?","request":"What's on my calendar?","transcriptContext":["Asha: Krude?"]}`

func postDispatch(t *testing.T, server *Server, authorization, body string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, "/v1/edge/dispatch", strings.NewReader(body))
	if authorization != "" {
		request.Header.Set("Authorization", authorization)
	}
	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, request)
	return recorder
}

func edgeServer(tokens *fakeEdgeTokens, dispatcher *fakeDispatcher) *Server {
	return New(nil, nil, Config{EdgeDispatch: &EdgeDispatch{Tokens: tokens, Dispatcher: dispatcher}}, nil)
}

func TestEdgeDispatchStartsTheWorkTheTokenCovers(t *testing.T) {
	tokens, dispatcher := &fakeEdgeTokens{claims: dispatchClaims()}, &fakeDispatcher{}
	recorder := postDispatch(t, edgeServer(tokens, dispatcher), "Bearer the-token", dispatchBody)
	if recorder.Code != http.StatusAccepted {
		t.Fatalf("status = %d: %s", recorder.Code, recorder.Body)
	}
	var accepted edgedispatch.Accepted
	if err := json.Unmarshal(recorder.Body.Bytes(), &accepted); err != nil || accepted.RunID != "run_9" || accepted.DispatchID != "dsp_1" {
		t.Fatalf("answer = %s (%v)", recorder.Body, err)
	}
	if tokens.got != "the-token" {
		t.Fatalf("verified %q", tokens.got)
	}
	got := dispatcher.requests[0]
	if got.Member != "usr_1" || got.Request != "What's on my calendar?" || len(got.TranscriptContext) != 1 {
		t.Fatalf("dispatched %+v", got)
	}
}

func TestEdgeDispatchRefuses(t *testing.T) {
	other := func(mutate func(*edgev1.TokenClaims)) edgev1.TokenClaims {
		claims := dispatchClaims()
		mutate(&claims)
		return claims
	}
	for name, test := range map[string]struct {
		authorization string
		tokens        *fakeEdgeTokens
		dispatchErr   error
		body          string
		status        int
	}{
		"no token":                    {"", &fakeEdgeTokens{claims: dispatchClaims()}, nil, dispatchBody, http.StatusUnauthorized},
		"not bearer":                  {"Basic abc", &fakeEdgeTokens{claims: dispatchClaims()}, nil, dispatchBody, http.StatusUnauthorized},
		"refused token":               {"Bearer x", &fakeEdgeTokens{err: edgedispatch.ErrUnauthorized}, nil, dispatchBody, http.StatusUnauthorized},
		"other member":                {"Bearer x", &fakeEdgeTokens{claims: other(func(c *edgev1.TokenClaims) { c.Member = "usr_2" })}, nil, dispatchBody, http.StatusForbidden},
		"other room":                  {"Bearer x", &fakeEdgeTokens{claims: other(func(c *edgev1.TokenClaims) { c.Room.ID = "room_2" })}, nil, dispatchBody, http.StatusForbidden},
		"other org":                   {"Bearer x", &fakeEdgeTokens{claims: other(func(c *edgev1.TokenClaims) { c.Org = "org_2" })}, nil, dispatchBody, http.StatusForbidden},
		"other team":                  {"Bearer x", &fakeEdgeTokens{claims: other(func(c *edgev1.TokenClaims) { c.Team = "team_2" })}, nil, dispatchBody, http.StatusForbidden},
		"team the token doesn't name": {"Bearer x", &fakeEdgeTokens{claims: other(func(c *edgev1.TokenClaims) { c.Team = "" })}, nil, dispatchBody, http.StatusForbidden},
		"no call:speak":               {"Bearer x", &fakeEdgeTokens{claims: other(func(c *edgev1.TokenClaims) { c.Scopes = nil })}, nil, dispatchBody, http.StatusForbidden},
		"unknown field":               {"Bearer x", &fakeEdgeTokens{claims: dispatchClaims()}, nil, strings.Replace(dispatchBody, `"call"`, `"sudo":true,"call"`, 1), http.StatusBadRequest},
		"invalid":                     {"Bearer x", &fakeEdgeTokens{claims: dispatchClaims()}, fmt.Errorf("%w: request", edgedispatch.ErrInvalid), dispatchBody, http.StatusBadRequest},
		"not a member":                {"Bearer x", &fakeEdgeTokens{claims: dispatchClaims()}, edgedispatch.ErrNotMember, dispatchBody, http.StatusForbidden},
		"restarting":                  {"Bearer x", &fakeEdgeTokens{claims: dispatchClaims()}, edgedispatch.ErrUnavailable, dispatchBody, http.StatusServiceUnavailable},
		"failure":                     {"Bearer x", &fakeEdgeTokens{claims: dispatchClaims()}, errors.New("database down"), dispatchBody, http.StatusInternalServerError},
	} {
		t.Run(name, func(t *testing.T) {
			dispatcher := &fakeDispatcher{err: test.dispatchErr}
			recorder := postDispatch(t, edgeServer(test.tokens, dispatcher), test.authorization, test.body)
			if recorder.Code != test.status {
				t.Fatalf("status = %d, want %d: %s", recorder.Code, test.status, recorder.Body)
			}
			if test.status == http.StatusServiceUnavailable && recorder.Header().Get("Retry-After") == "" {
				t.Fatal("a 503 says when to send it again")
			}
			if test.dispatchErr == nil && len(dispatcher.requests) != 0 {
				t.Fatal("a refused dispatch reached the dispatcher")
			}
		})
	}
}

func TestEdgeDispatchIsAbsentUnlessConfigured(t *testing.T) {
	recorder := postDispatch(t, New(nil, nil, Config{}, nil), "Bearer x", dispatchBody)
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("status = %d", recorder.Code)
	}
}
