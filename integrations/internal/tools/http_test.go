package tools

import (
	"context"
	"encoding/json"
	"io"
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

// GET /v1/tools answers the tools of the apps the member's own live
// connections use, from the manifest, and nothing for another member.
func TestListRouteAnswersAMembersTools(t *testing.T) {
	r := newRig(t)
	r.connection(t, "ok")
	server := httptest.NewServer(func() http.Handler {
		s := httpserver.New(allowAll{}, nil)
		Routes(s, r.Service, nil)
		return s
	}())
	t.Cleanup(server.Close)
	get := func(query string) (int, string) {
		req, _ := http.NewRequest(http.MethodGet, server.URL+"/v1/tools?"+query, nil)
		req.Header.Set(serviceauth.Header, "t")
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		var body strings.Builder
		_, _ = io.Copy(&body, res.Body)
		return res.StatusCode, body.String()
	}

	status, body := get("org=" + r.org + "&member=m1")
	var answer struct {
		Tools []Offered `json:"tools"`
	}
	if err := json.Unmarshal([]byte(body), &answer); status != http.StatusOK || err != nil {
		t.Fatalf("%d %s", status, body)
	}
	m, _ := r.host.Manifest("fakeapp")
	if len(answer.Tools) != len(m.Tools) {
		t.Fatalf("tools = %+v, want the manifest's %d", answer.Tools, len(m.Tools))
	}
	effects := map[string]string{}
	for _, tool := range answer.Tools {
		if tool.App != "fakeapp" || tool.Description == "" || len(tool.InputSchema) == 0 {
			t.Errorf("tool = %+v", tool)
		}
		effects[tool.Name] = tool.Effect
	}
	if effects["fakeapp.echo"] != "read" || effects["fakeapp.add_note"] == "read" || effects["fakeapp.add_note"] == "" {
		t.Errorf("effects = %v", effects)
	}

	for _, c := range []struct{ query, want string }{
		{"org=" + r.org + "&member=m2", `{"tools":[]}`},
		{"org=" + r.org + "&member=m1&app=otherapp", `{"tools":[]}`},
		{"org=other_org&member=m1", `{"tools":[]}`},
	} {
		if status, body := get(c.query); status != http.StatusOK || strings.TrimSpace(body) != c.want {
			t.Errorf("%s: %d %s, want %s", c.query, status, body, c.want)
		}
	}
	if status, body := get("org=" + r.org + "&member=m1&app=fakeapp"); status != http.StatusOK || !strings.Contains(body, "fakeapp.echo") {
		t.Errorf("by app: %d %s", status, body)
	}
	if status, _ := get("org=" + r.org); status != http.StatusBadRequest {
		t.Errorf("without a member: %d, want 400", status)
	}

	// A revoked connection offers nothing.
	if _, err := r.pool.Exec(context.Background(), `UPDATE integrations.connections SET status = 'revoked' WHERE org_id = $1`, r.org); err != nil {
		t.Fatal(err)
	}
	if status, body := get("org=" + r.org + "&member=m1"); status != http.StatusOK || strings.TrimSpace(body) != `{"tools":[]}` {
		t.Errorf("revoked: %d %s", status, body)
	}
}
