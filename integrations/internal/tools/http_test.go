package tools

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Shubham04-oss/oryxa-go/integrations/internal/httpserver"
	"github.com/Shubham04-oss/oryxa-go/internal/serviceauth"
)

type allowAll struct{}

func (allowAll) Verify(context.Context, string) (serviceauth.Claims, error) {
	return serviceauth.Claims{Service: "apps"}, nil
}

func TestRouteAnswers(t *testing.T) {
	r := newRig(t)
	conn := r.connection(t, "ok")
	server := httpserver.New(allowAll{}, nil)
	Routes(server, r.Service, nil)
	post := func(token, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/v1/tools/call", strings.NewReader(body))
		if token != "" {
			req.Header.Set(serviceauth.Header, token)
		}
		rec := httptest.NewRecorder()
		server.ServeHTTP(rec, req)
		return rec
	}
	base := `"org":"` + r.org + `","member":"m1","agent":"agent_7","role":"member","connection":"` + conn + `"`
	for _, c := range []struct {
		name, token, body string
		status            int
		contains          string
	}{
		{"no token", "", `{}`, http.StatusUnauthorized, "unauthorized"},
		{"output", "t", `{` + base + `,"tool":"fakeapp.echo","input":{"text":"hi"}}`, 200, `{"output":{"text":"hi"}}`},
		{"tool error", "t", `{` + base + `,"tool":"fakeapp.echo","input":{"text":""}}`, 200, `"tool_error":{"code":"invalid_input"`},
		{"unconfirmed write", "t", `{` + base + `,"tool":"fakeapp.add_note","input":{"text":"x"}}`, http.StatusConflict, "confirmation_required"},
		{"unknown tool", "t", `{` + base + `,"tool":"fakeapp.nope","input":{}}`, http.StatusNotFound, "not_found"},
		{"unknown field", "t", `{` + base + `,"tool":"fakeapp.echo","input":{},"x":1}`, http.StatusBadRequest, "invalid_request"},
		{"no input", "t", `{` + base + `,"tool":"fakeapp.echo"}`, http.StatusBadRequest, "invalid_request"},
	} {
		rec := post(c.token, c.body)
		if rec.Code != c.status || !strings.Contains(rec.Body.String(), c.contains) {
			t.Errorf("%s: %d %s, want %d containing %q", c.name, rec.Code, rec.Body, c.status, c.contains)
		}
	}
}
