package httpserver

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	primaryagents "github.com/Shubham04-oss/oryxa-go/agents/primary/internal/agents"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/interactions"
)

const replyPath = "/v1/organizations/org_1/agent-requests/irq_7/reply"

// TestTheOwnerRepliesInOneCall: the text reaches the ledger as the member's own
// session, with their own token and whatever else the body says, and the answer
// is the request as they now read it, their words the result.
func TestTheOwnerRepliesInOneCall(t *testing.T) {
	ledger := &fakeInteractions{}
	server := slackServer(ledger, nil)
	response := serve(server, http.MethodPost, replyPath, `{"text":"  We ship on the 14th.  "}`, bearer)
	if response.Code != http.StatusOK {
		t.Fatalf("reply = %d %s", response.Code, response.Body.String())
	}
	var body struct {
		Request primaryagents.Interaction `json:"request"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Request.ID != "irq_7" || body.Request.Status != interactions.StatusCompleted || body.Request.Result == nil ||
		!body.Request.Result.WrittenByOwner || body.Request.Decision == nil || body.Request.Decision.Consent != interactions.ConsentReply {
		t.Fatalf("reply answered %s", response.Body.String())
	}
	if !strings.Contains(response.Body.String(), `"written_by_owner":true`) || !strings.Contains(response.Body.String(), `"consent":"reply"`) {
		t.Fatalf("the answer does not say it is the owner's own words: %s", response.Body.String())
	}
	// The ledger trims and checks the words; the route hands them over as sent.
	if len(ledger.replies) != 1 || ledger.replies[0] != "  We ship on the 14th.  " || ledger.token != "member-token" {
		t.Fatalf("the ledger was given %q with token %q", ledger.replies, ledger.token)
	}
	for _, actor := range ledger.actors {
		if actor.Session != primaryagents.SessionMember || actor.UserID != "user_1" || actor.AgentKind != "primary" {
			t.Fatalf("the ledger was called as %+v, want the member in their own session", actor)
		}
	}
}

// TestAReplyThatLostIs409WithTheWinner: whoever decides first wins, whether a
// choice, a review or another reply, and the reply that came second is told who,
// in the consent route's own shape.
func TestAReplyThatLostIs409WithTheWinner(t *testing.T) {
	decidedAt := time.Date(2026, time.October, 1, 9, 30, 0, 0, time.UTC)
	ledger := &fakeInteractions{
		request: primaryagents.Interaction{
			ID: "irq_7", Status: "approved", Revision: 2, Task: "When do we ship?",
			Decision: &primaryagents.InteractionDecision{
				Decision: "approved", Consent: "summary", DecidedAt: decidedAt,
				DecidedBy: &primaryagents.InteractionDecider{Kind: "member", UserID: "user_1", Surface: "slack"},
			},
		},
		replyErr: interactions.ErrAlreadyDecided,
	}
	response := serve(slackServer(ledger, nil), http.MethodPost, replyPath, `{"text":"Too late."}`, bearer)
	if response.Code != http.StatusConflict || errorCode(t, response) != "already_decided" {
		t.Fatalf("a reply that lost = %d %s, want 409 already_decided", response.Code, response.Body.String())
	}
	var body struct {
		Winner struct {
			RequestID string `json:"request_id"`
			Status    string `json:"status"`
			Revision  int64  `json:"revision"`
			Decision  string `json:"decision"`
			Consent   string `json:"consent"`
			DecidedBy struct {
				Kind, UserID, Surface string
			} `json:"decided_by"`
			DecidedAt time.Time `json:"decided_at"`
		} `json:"winner"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	winner := body.Winner
	if winner.RequestID != "irq_7" || winner.Status != "approved" || winner.Revision != 2 || winner.Decision != "approved" || winner.Consent != "summary" ||
		winner.DecidedBy.Kind != "member" || winner.DecidedBy.Surface != "slack" || !winner.DecidedAt.Equal(decidedAt) {
		t.Fatalf("winner = %+v in %s", winner, response.Body.String())
	}
	// The winner is the decision alone, never the request's words or the reply's.
	for _, words := range []string{"When do we ship", "Too late", "task", "result"} {
		if strings.Contains(response.Body.String(), words) {
			t.Fatalf("the loser was told %q: %s", words, response.Body.String())
		}
	}
}

// TestAReplyThatIsRefusedIsAnsweredWithItsCode: the ledger's refusals, as the
// other request routes answer them, and never an internal failure's words.
func TestAReplyThatIsRefusedIsAnsweredWithItsCode(t *testing.T) {
	for _, testCase := range []struct {
		err  error
		want int
		code string
	}{
		{fmt.Errorf("%w: text is required and holds at most 4000 characters", interactions.ErrInvalid), http.StatusBadRequest, "invalid_request"},
		{interactions.ErrForbidden, http.StatusForbidden, "forbidden"},
		{interactions.ErrNotFound, http.StatusNotFound, "not_found"},
		{interactions.ErrExpired, http.StatusConflict, "request_expired"},
		{interactions.ErrClosed, http.StatusConflict, "request_closed"},
		{errors.New("the database is down"), http.StatusInternalServerError, "internal"},
	} {
		ledger := &fakeInteractions{replyErr: testCase.err}
		response := serve(slackServer(ledger, nil), http.MethodPost, replyPath, `{"text":"Mine."}`, bearer)
		if response.Code != testCase.want || errorCode(t, response) != testCase.code {
			t.Errorf("%v = %d %s, want %d %s", testCase.err, response.Code, response.Body.String(), testCase.want, testCase.code)
		}
		if strings.Contains(response.Body.String(), "database is down") {
			t.Errorf("an internal failure was told to the caller: %s", response.Body.String())
		}
	}
	// The ledger's own words say what to fix.
	ledger := &fakeInteractions{replyErr: fmt.Errorf("%w: text is required and holds at most 4000 characters", interactions.ErrInvalid)}
	response := serve(slackServer(ledger, nil), http.MethodPost, replyPath, `{"text":"   "}`, bearer)
	if !strings.Contains(response.Body.String(), "text is required and holds at most 4000 characters") {
		t.Fatalf("an empty reply was answered %s", response.Body.String())
	}
	// A body that is not one object of the text alone is refused before the ledger.
	refused := &fakeInteractions{}
	server := slackServer(refused, nil)
	for _, body := range []string{`{"text":"a","surface":"slack"}`, `{"text":1}`, `[]`, ``, `{"text":"a"} {}`, "{\"text\":\"a\\u0000b\"}", `{"choice":"summary"}`} {
		if response := serve(server, http.MethodPost, replyPath, body, bearer); response.Code != http.StatusBadRequest {
			t.Errorf("body %q = %d, want 400", body, response.Code)
		}
	}
	if len(refused.replies) != 0 {
		t.Fatalf("the ledger was asked to reply with a body it should have refused: %q", refused.replies)
	}
}

// TestAReplyIsTheMembersOwnCall: a cookie needs the app's own origin, and an
// agent's own token replies nothing, whatever it names.
func TestAReplyIsTheMembersOwnCall(t *testing.T) {
	for name, test := range map[string]struct {
		headers map[string]string
		want    int
	}{
		"a cookie from the app's own origin": {map[string]string{"Cookie": "cookie-token", "Origin": "https://app.oryxa.test"}, http.StatusOK},
		"a cookie from another site":         {map[string]string{"Cookie": "cookie-token", "Origin": "https://evil.test"}, http.StatusForbidden},
		"a cookie with no origin":            {map[string]string{"Cookie": "cookie-token"}, http.StatusForbidden},
		"nothing at all":                     {nil, http.StatusUnauthorized},
	} {
		ledger := &fakeInteractions{}
		response := serve(slackServer(ledger, nil), http.MethodPost, replyPath, `{"text":"Mine."}`, test.headers)
		if response.Code != test.want {
			t.Errorf("%s = %d %s, want %d", name, response.Code, response.Body.String(), test.want)
		}
		if test.want != http.StatusOK && len(ledger.replies) != 0 {
			t.Errorf("%s reached the ledger", name)
		}
	}
	directory := member()
	directory.membership.Agent = true
	ledger := &fakeInteractions{}
	server := slackServer(ledger, nil)
	server.directory = directory
	if response := serve(server, http.MethodPost, replyPath, `{"text":"Mine."}`, bearer); response.Code != http.StatusForbidden || len(ledger.replies) != 0 {
		t.Fatalf("an agent's own token = %d %s, want 403 and nothing replied", response.Code, response.Body.String())
	}
}
