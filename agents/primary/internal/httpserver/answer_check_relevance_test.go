package httpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/answerguard"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/guardpolicy"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/relevance"
)

// routeJudge is a relevance judge that makes the same of every answer, and
// keeps every input it was asked about.
type routeJudge struct {
	mu     sync.Mutex
	inputs []relevance.Input
	result relevance.Result
}

func (judge *routeJudge) Judge(_ context.Context, in relevance.Input) relevance.Result {
	judge.mu.Lock()
	defer judge.mu.Unlock()
	judge.inputs = append(judge.inputs, in)
	return judge.result
}

func (judge *routeJudge) calls() int {
	judge.mu.Lock()
	defer judge.mu.Unlock()
	return len(judge.inputs)
}

// What a judge can make of an answer.
var (
	routeClean    = relevance.Result{Judged: true, Model: "route-judge", Certainty: 0.9}
	routeOffTopic = relevance.Result{
		Judged: true, Hold: true, Extras: []relevance.Category{relevance.Health, relevance.HR}, Certainty: 0.8, Model: "route-judge",
	}
	routeFailed = relevance.Result{Judged: true, Hold: true, Err: relevance.ErrTimeout, Model: "route-judge"}
)

// routeSources are tools that may have read something private, so an answer
// that came from them is judged.
var routeSources = []string{"apps_gmail_search_messages"}

// judgedServer is a server whose check route reads with the real guard, over
// the guardrail bedrockFinding stands in for, and with judge for the relevance
// check; a nil judge builds a guard with none.
func judgedServer(t *testing.T, judge relevance.Judge, settings AnswerGuardPreferences, logger *slog.Logger) *Server {
	t.Helper()
	var options []answerguard.Option
	if judge != nil {
		options = append(options, answerguard.WithRelevance(judge))
	}
	guard, err := answerguard.New(bedrockFinding{}, answerguard.Config{Identifier: "gr_answers", Version: "1", Logger: logger}, options...)
	if err != nil {
		t.Fatal(err)
	}
	server, _ := checkServerWith(guard, settings, logger)
	return server
}

// checkBody is a check's body, from the fields it carries.
func checkBody(t *testing.T, fields map[string]any) string {
	t.Helper()
	encoded, err := json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}

// relevanceBlock is what a check answered under "relevance", read strictly:
// exactly the three booleans, and nothing else.
func relevanceBlock(t *testing.T, result checkResult) (judged, wouldHold, held bool) {
	t.Helper()
	var block map[string]bool
	if err := json.Unmarshal(result.raw["relevance"], &block); err != nil {
		t.Fatalf("relevance = %s: %v", result.raw["relevance"], err)
	}
	if len(block) != 3 {
		t.Fatalf("relevance = %s, want judged, would_hold and held alone", result.raw["relevance"])
	}
	for _, key := range []string{"judged", "would_hold", "held"} {
		if _, said := block[key]; !said {
			t.Fatalf("relevance = %s, want %q in it", result.raw["relevance"], key)
		}
	}
	return block["judged"], block["would_hold"], block["held"]
}

func TestTheCheckRouteNeverSharesTextOmittedByTheRelevanceCap(t *testing.T) {
	for _, test := range []struct {
		name, mode, request string
		texts               []string
		public, incomplete  bool
	}{
		{"exact side answer cap", "hold", "When is launch?", []string{strings.Repeat("é", relevance.MaxAnswerRunes)}, false, false},
		{"maximum side body", "hold", "When is launch?", []string{strings.Repeat("a", 12_000) + "私病" + strings.Repeat("b", 12_000)}, false, true},
		{"aggregate answer cap", "hold", "When is launch?", []string{strings.Repeat("a", 12_001), strings.Repeat("b", 12_000)}, false, true},
		{"missing request", "hold", "", []string{"An answer."}, false, true},
		{"oversized request", "hold", strings.Repeat("é", relevance.MaxRequestRunes+1), []string{"An answer."}, false, true},
		{"shadow records incomplete", "shadow", "When is launch?", []string{strings.Repeat("é", relevance.MaxAnswerRunes+2)}, false, true},
		{"off preserves its cap", "off", "When is launch?", []string{strings.Repeat("é", relevance.MaxAnswerRunes+2)}, false, true},
		{"public sources skip judging", "hold", "", []string{strings.Repeat("é", relevance.MaxAnswerRunes+2)}, true, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			judge := &routeJudge{result: routeClean}
			settings := &fakeAnswerGuardPreferences{ceiling: guardpolicy.Cap{Relevance: guardpolicy.RelevanceMode(test.mode)}}
			server := judgedServer(t, judge, settings, nil)
			sources := routeSources
			if test.public {
				sources = []string{"directory_load"}
			}
			response, got := postCheck(t, server, checkPath, checkBody(t, map[string]any{"texts": test.texts, "request": test.request, "sources": sources, "origin": "owner"}))
			if response.Code != http.StatusOK {
				t.Fatalf("status=%d", response.Code)
			}
			active := test.mode != "off" && !test.public
			wouldHold := active && test.incomplete
			held := wouldHold && test.mode == "hold"
			judged, recorded, blocked := relevanceBlock(t, got)
			if judged != active || recorded != wouldHold || blocked != held || (got.Shareable == nil) != held {
				t.Fatalf("state=%s shareable=%t relevance=%s want active=%t wouldHold=%t held=%t", got.State, got.Shareable != nil, got.raw["relevance"], active, wouldHold, held)
			}
			wantCalls := 0
			if active && !test.incomplete {
				wantCalls = 1
			}
			if judge.calls() != wantCalls {
				t.Fatalf("judge calls=%d want=%d", judge.calls(), wantCalls)
			}
			if !held && !slices.Equal(got.Shareable, test.texts) {
				t.Fatal("a nonholding cap silently clipped the shared text")
			}
		})
	}
}

// TestTheCheckRouteRefusesSourcesAndContextOutOfBounds: at most 64 sources of
// at most 128 characters and at most 6 lines of context of at most 1,000, in
// characters and not bytes, each a piece of text with no NUL in it; anything
// else is a 400 invalid_request, before the check counts against the limit or
// anybody's settings are read, and the guard is never asked.
func TestTheCheckRouteRefusesSourcesAndContextOutOfBounds(t *testing.T) {
	names := func(n int) []string {
		list := make([]string, n)
		for index := range list {
			list[index] = "tool_" + fmt.Sprint(index)
		}
		return list
	}
	for name, body := range map[string]string{
		"65 sources":                           checkBody(t, map[string]any{"texts": []string{"fine"}, "sources": names(65)}),
		"a source of 129 characters":           checkBody(t, map[string]any{"texts": []string{"fine"}, "sources": []string{repeated("a", 129)}}),
		"a source of 129 characters, é":        checkBody(t, map[string]any{"texts": []string{"fine"}, "sources": []string{repeated("é", 129)}}),
		"a source of 129 characters, 世":        checkBody(t, map[string]any{"texts": []string{"fine"}, "sources": []string{repeated("世", 129)}}),
		"a source of 129 characters, 😀":        checkBody(t, map[string]any{"texts": []string{"fine"}, "sources": []string{"ok", repeated("😀", 129)}}),
		"7 lines of context":                   checkBody(t, map[string]any{"texts": []string{"fine"}, "context": names(7)}),
		"a line of 1,001 characters":           checkBody(t, map[string]any{"texts": []string{"fine"}, "context": []string{repeated("a", 1001)}}),
		"a line of 1,001 characters, é":        checkBody(t, map[string]any{"texts": []string{"fine"}, "context": []string{repeated("é", 1001)}}),
		"a line of 1,001 characters, 世":        checkBody(t, map[string]any{"texts": []string{"fine"}, "context": []string{repeated("世", 1001)}}),
		"a line of 1,001 characters, 😀":        checkBody(t, map[string]any{"texts": []string{"fine"}, "context": []string{"ok", repeated("😀", 1001)}}),
		"sources that are not a list":          `{"texts":["fine"],"sources":"apps_gmail"}`,
		"sources that are not text":            `{"texts":["fine"],"sources":[1,2]}`,
		"sources that are an object":           `{"texts":["fine"],"sources":{"apps_gmail":true}}`,
		"context that is not a list":           `{"texts":["fine"],"context":"a line"}`,
		"context that is not text":             `{"texts":["fine"],"context":[true]}`,
		"context that is an object":            `{"texts":["fine"],"context":{"line":"a line"}}`,
		"a NUL in a source":                    `{"texts":["fine"],"sources":["apps_` + jsonNUL + `gmail"]}`,
		"a NUL in a line of context":           `{"texts":["fine"],"context":["a li` + jsonNUL + `ne"]}`,
		"an unknown field beside the new ones": `{"texts":["fine"],"sources":["apps_gmail"],"context":["a line"],"__not_a_real_field__":true}`,
		"the field spelled another way":        `{"texts":["fine"],"source":["apps_gmail"]}`,
	} {
		t.Run(name, func(t *testing.T) {
			guard := &fakeAnswerChecker{}
			settings := &fakeAnswerGuardPreferences{}
			server, _ := checkServerWith(guard, settings, nil)
			response := serve(server, http.MethodPost, checkPath, body, bearer)
			if response.Code != http.StatusBadRequest || errorCode(t, response) != "invalid_request" {
				t.Fatalf("check = %d %.200s, want 400 invalid_request", response.Code, response.Body.String())
			}
			if guard.checks() != 0 || len(settings.calls) != 0 || len(server.checks.members()) != 0 {
				t.Fatalf("a check that was refused reached the guard (%d), read settings (%d) or was counted (%v)",
					guard.checks(), len(settings.calls), server.checks.members())
			}
		})
	}
}

// TestTheCheckRouteTakesTheMostSourcesAndContext: 64 sources of 128 characters
// and 6 lines of context of 1,000 are accepted, in characters and not bytes, in
// the widest, beside eight texts and a request of the most characters too,
// which is the biggest body a check can be; and none, null and blank ones are
// taken as none.
func TestTheCheckRouteTakesTheMostSourcesAndContext(t *testing.T) {
	wide := func(character string, lines, runes int) []string {
		list := make([]string, lines)
		for index := range list {
			list[index] = repeated(character, runes)
		}
		return list
	}
	largest := checkBody(t, map[string]any{
		"texts":   slices.Repeat([]string{repeated("😀", maxCheckTextRunes)}, maxCheckTexts),
		"request": repeated("😀", maxCheckTextRunes), "origin": "owner",
		"sources": wide("😀", maxCheckSources, maxCheckSourceRunes),
		"context": wide("😀", maxCheckContextLines, maxCheckContextLineRunes),
	})
	if len(largest) > maxCheckBodyBytes {
		t.Fatalf("the largest check is %d bytes, over the %d the route reads: the limit has no room for what it accepts", len(largest), maxCheckBodyBytes)
	}
	for name, body := range map[string]string{
		"the most of everything, in the widest": largest,
		"64 sources of 128 characters":          checkBody(t, map[string]any{"texts": []string{"fine"}, "sources": wide("a", 64, 128)}),
		"64 sources of 128 characters, 世":       checkBody(t, map[string]any{"texts": []string{"fine"}, "sources": wide("世", 64, 128)}),
		"6 lines of 1,000 characters":           checkBody(t, map[string]any{"texts": []string{"fine"}, "context": wide("a", 6, 1000)}),
		"6 lines of 1,000 characters, é":        checkBody(t, map[string]any{"texts": []string{"fine"}, "context": wide("é", 6, 1000)}),
		"no sources and no context":             `{"texts":["fine"],"sources":[],"context":[]}`,
		"null sources and null context":         `{"texts":["fine"],"sources":null,"context":null}`,
		"blank sources and blank context":       `{"texts":["fine"],"sources":["", "  "],"context":["", "\n"]}`,
		"a null in each":                        `{"texts":["fine"],"sources":[null],"context":[null]}`,
	} {
		t.Run(name, func(t *testing.T) {
			guard := &fakeAnswerChecker{}
			server, _ := checkServer(guard, nil)
			response := serve(server, http.MethodPost, checkPath, body, bearer)
			if response.Code != http.StatusOK || guard.checks() != 1 {
				t.Fatalf("check = %d %.200s with the guard asked %d times, want 200 and one check", response.Code, response.Body.String(), guard.checks())
			}
		})
	}
	// And a body past the room there is for it is still refused, whatever it is.
	pastIt := checkBody(t, map[string]any{"texts": []string{"fine"}, "sources": []string{repeated("a", maxCheckBodyBytes)}})
	server, _ := checkServer(&fakeAnswerChecker{}, nil)
	if response := serve(server, http.MethodPost, checkPath, pastIt, bearer); response.Code != http.StatusBadRequest {
		t.Errorf("a body past the limit = %d, want 400", response.Code)
	}
}

// TestTheCheckRouteHandsTheGuardWhatTheRelevanceCheckNeeds: the request, the
// context and the sources as the body gave them, whether the caller's
// organization is regulated, the caller's own organization and user, and the
// mode their settings come to: off where none are kept, hold where the
// organization says nothing and the deployment has no ceiling, and otherwise
// what the organization's policy and the deployment's ceiling leave. A mode
// nobody knows is hold. The settings are read once for a check, whatever it
// asks, and the texts alone are what the guard reads for personal data.
func TestTheCheckRouteHandsTheGuardWhatTheRelevanceCheckNeeds(t *testing.T) {
	type input struct {
		mode      string
		regulated bool
	}
	orgs := func(policy guardpolicy.OrgPolicy) guardpolicy.OrgPolicies { return fakeOrgPolicies{"org_1": policy} }
	for _, testCase := range []struct {
		name     string
		settings AnswerGuardPreferences
		want     input
	}{
		{"no settings are kept", nil, input{"off", false}},
		{"the organization says nothing", &fakeAnswerGuardPreferences{}, input{"hold", false}},
		{"a ceiling of shadow", &fakeAnswerGuardPreferences{ceiling: guardpolicy.Cap{Relevance: guardpolicy.RelevanceShadow}}, input{"shadow", false}},
		{"a ceiling of off", &fakeAnswerGuardPreferences{ceiling: guardpolicy.Cap{Relevance: guardpolicy.RelevanceOff}}, input{"off", false}},
		{
			"a ceiling of shadow that exempts the organization",
			&fakeAnswerGuardPreferences{ceiling: guardpolicy.Cap{Relevance: guardpolicy.RelevanceShadow, HoldOrgs: map[string]bool{"org_1": true}}},
			input{"hold", false},
		},
		{"an organization that asks for shadow", &fakeAnswerGuardPreferences{orgs: orgs(guardpolicy.OrgPolicy{Relevance: guardpolicy.RelevanceShadow})}, input{"shadow", false}},
		{"an organization that asks for off", &fakeAnswerGuardPreferences{orgs: orgs(guardpolicy.OrgPolicy{Relevance: guardpolicy.RelevanceOff})}, input{"off", false}},
		{"an organization that asks for a mode nobody knows", &fakeAnswerGuardPreferences{orgs: orgs(guardpolicy.OrgPolicy{Relevance: "strict"})}, input{"hold", false}},
		{"an organization that asks for hold, under a ceiling of shadow", &fakeAnswerGuardPreferences{
			orgs: orgs(guardpolicy.OrgPolicy{Relevance: guardpolicy.RelevanceHold}), ceiling: guardpolicy.Cap{Relevance: guardpolicy.RelevanceShadow}}, input{"shadow", false}},
		{"a regulated organization", &fakeAnswerGuardPreferences{orgs: orgs(guardpolicy.OrgPolicy{Regulated: true})}, input{"hold", true}},
		{"a regulated organization under a ceiling of shadow", &fakeAnswerGuardPreferences{
			orgs: orgs(guardpolicy.OrgPolicy{Regulated: true}), ceiling: guardpolicy.Cap{Relevance: guardpolicy.RelevanceShadow}}, input{"shadow", true}},
		{"an organization other than the caller's has its own", &fakeAnswerGuardPreferences{
			orgs: fakeOrgPolicies{"org_2": {Relevance: guardpolicy.RelevanceOff, Regulated: true}}}, input{"hold", false}},
		{"settings that say shadow and regulated", unanswering{effective: guardpolicy.Effective{Relevance: guardpolicy.RelevanceShadow, Regulated: true}}, input{"shadow", true}},
		{"settings that say off", unanswering{effective: guardpolicy.Effective{Relevance: guardpolicy.RelevanceOff}}, input{"off", false}},
		{"settings that say hold", unanswering{effective: guardpolicy.Effective{Relevance: guardpolicy.RelevanceHold}}, input{"hold", false}},
		{"settings that say no mode", unanswering{}, input{"hold", false}},
		{"settings that say a mode nobody knows", unanswering{effective: guardpolicy.Effective{Relevance: "lenient"}}, input{"hold", false}},
		{"a mode in capitals", unanswering{effective: guardpolicy.Effective{Relevance: "SHADOW"}}, input{"hold", false}},
		{"settings that fail", unanswering{err: errors.New("database down")}, input{"hold", false}},
		{
			"settings that fail beside a mode they still say",
			unanswering{effective: guardpolicy.Effective{Relevance: guardpolicy.RelevanceShadow}, err: errors.New("database down")}, input{"shadow", false},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			for _, origin := range []string{"peer", "owner"} {
				guard := &fakeAnswerChecker{}
				server, _ := checkServerWith(guard, testCase.settings, nil)
				readsBefore := 0
				if settings, ok := testCase.settings.(*fakeAnswerGuardPreferences); ok {
					readsBefore = len(settings.calls)
				}
				body := checkBody(t, map[string]any{
					"texts": []string{"first", "second"}, "origin": origin, "request": "What is Maya's bonus?",
					"sources": []string{"apps_gmail_search_messages", "load_memory"}, "context": []string{"an earlier line", "the line before it"},
				})
				if response := serve(server, http.MethodPost, checkPath, body, bearer); response.Code != http.StatusOK {
					t.Fatalf("check = %d %s", response.Code, response.Body.String())
				}
				if guard.checks() != 1 || len(guard.relevance) != 1 || guard.relevance[0] == nil {
					t.Fatalf("the guard was asked %d times with %v, want once, with a relevance input", guard.checks(), guard.relevance)
				}
				want := answerguard.RelevanceInput{
					Request: "What is Maya's bonus?", Context: []string{"an earlier line", "the line before it"},
					Sources: []string{"apps_gmail_search_messages", "load_memory"}, Regulated: testCase.want.regulated, Mode: testCase.want.mode,
					OrganizationID: "org_1", UserID: "user_1",
				}
				got := *guard.relevance[0]
				if got.Request != want.Request || !slices.Equal(got.Context, want.Context) || !slices.Equal(got.Sources, want.Sources) ||
					got.Regulated != want.Regulated || got.Mode != want.Mode || got.OrganizationID != want.OrganizationID || got.UserID != want.UserID {
					t.Errorf("%s: the guard was given %+v, want %+v", origin, got, want)
				}
				if !slices.Equal(guard.asked[0], []string{"first", "second"}) {
					t.Errorf("%s: the guard read %q for personal data, want the texts alone", origin, guard.asked[0])
				}
				if settings, ok := testCase.settings.(*fakeAnswerGuardPreferences); ok {
					if reads, want := settings.calls[readsBefore:], [][2]string{{"org_1", "user_1"}}; fmt.Sprint(reads) != fmt.Sprint(want) {
						t.Errorf("%s: settings were read for %v, want one read for the caller", origin, reads)
					}
				}
			}
		})
	}
}

// TestACheckThatNamesNoRelevanceFieldsStillAsksForTheCheck: the sources, the
// context and the request are optional, and a check that carries none still
// tells the guard the mode and the organization; what it does with no source
// is the guard's gate, which judges nothing.
func TestACheckThatNamesNoRelevanceFieldsStillAsksForTheCheck(t *testing.T) {
	guard := &fakeAnswerChecker{}
	server, _ := checkServerWith(guard, &fakeAnswerGuardPreferences{}, nil)
	if response := serve(server, http.MethodPost, checkPath, `{"texts":["Hello"]}`, bearer); response.Code != http.StatusOK {
		t.Fatalf("check = %d %s", response.Code, response.Body.String())
	}
	got := guard.relevance[0]
	if got == nil || got.Request != "" || len(got.Context) != 0 || len(got.Sources) != 0 || got.Mode != "hold" || got.OrganizationID != "org_1" || got.UserID != "user_1" {
		t.Fatalf("the guard was given %+v, want the mode and the caller alone", got)
	}
}

// TestTheCheckRouteAnswersWhatTheRelevanceCheckFound: every answer says
// whether a judge read the answer, whether it would hold it and whether it did,
// in exactly those three fields and nothing of what the judge found; a hold is
// state held, kinds with OFF_TOPIC and no text to share, whatever else the
// verdict says; and what would have been held is only recorded.
func TestTheCheckRouteAnswersWhatTheRelevanceCheckFound(t *testing.T) {
	for _, testCase := range []struct {
		name      string
		verdict   answerguard.Verdict
		state     string
		kinds     []string
		shareable []string
		relevance [3]bool
	}{
		{
			name:    "a verdict that says nothing of relevance",
			verdict: answerguard.Verdict{State: answerguard.StateClear, Texts: []string{"first"}},
			state:   "clear", kinds: []string{}, shareable: []string{"first"}, relevance: [3]bool{false, false, false},
		},
		{
			name:    "judged and clean",
			verdict: answerguard.Verdict{State: answerguard.StateClear, Texts: []string{"first"}, Relevance: &answerguard.RelevanceOutcome{Judged: true, Certainty: 0.9}},
			state:   "clear", kinds: []string{}, shareable: []string{"first"}, relevance: [3]bool{true, false, false},
		},
		{
			name: "would have been held, and was not",
			verdict: answerguard.Verdict{State: answerguard.StateHeld, Kinds: []string{"EMAIL"}, Texts: []string{"write to a@example.com"}, Masked: []string{"write to {EMAIL}"},
				Relevance: &answerguard.RelevanceOutcome{Judged: true, WouldHold: true, Extras: []string{"health"}}},
			state: "held", kinds: []string{"EMAIL"}, shareable: []string{"write to {EMAIL}"}, relevance: [3]bool{true, true, false},
		},
		{
			name: "held for relevance",
			verdict: answerguard.Verdict{State: answerguard.StateHeld, Kinds: []string{answerguard.OffTopic, "health"}, Texts: []string{"first"}, Masked: []string{"first"},
				Relevance: &answerguard.RelevanceOutcome{Judged: true, WouldHold: true, Held: true, Extras: []string{"health"}, Certainty: 0.7, Err: "timeout"}},
			state: "held", kinds: []string{"OFF_TOPIC", "health"}, shareable: nil, relevance: [3]bool{true, true, true},
		},
		{
			name: "marked held for relevance and nothing else says so",
			verdict: answerguard.Verdict{State: answerguard.StateClear, Texts: []string{"first"},
				Relevance: &answerguard.RelevanceOutcome{Judged: true, WouldHold: true, Held: true}},
			state: "held", kinds: []string{"OFF_TOPIC"}, shareable: nil, relevance: [3]bool{true, true, true},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			for _, origin := range []string{"peer", "owner"} {
				guard := &fakeAnswerChecker{verdict: func([]string) answerguard.Verdict { return testCase.verdict }}
				server, _ := checkServer(guard, nil)
				response, got := postCheck(t, server, checkPath, `{"texts":["first"],"origin":"`+origin+`"}`)
				if response.Code != http.StatusOK || !got.Checked {
					t.Fatalf("check = %d %s", response.Code, response.Body.String())
				}
				// An owner reads the held email as written: only a relevance hold or a
				// masked kind is acted on there, so this case is compared as a peer's.
				if origin == "peer" || testCase.relevance[2] {
					if got.State != testCase.state || !slices.Equal(got.Kinds, testCase.kinds) || (got.Shareable == nil) != (testCase.shareable == nil) || !slices.Equal(got.Shareable, testCase.shareable) {
						t.Errorf("%s: %s, want state %q kinds %v shareable %v", origin, response.Body.String(), testCase.state, testCase.kinds, testCase.shareable)
					}
				}
				judged, wouldHold, held := relevanceBlock(t, got)
				if [3]bool{judged, wouldHold, held} != testCase.relevance {
					t.Errorf("%s: relevance = %s, want %v", origin, got.raw["relevance"], testCase.relevance)
				}
				// A hold always comes as held, with nothing to share and OFF_TOPIC said.
				if held && (got.State != "held" || got.Shareable != nil || string(got.raw["shareable"]) != "null" || !slices.Contains(got.Kinds, "OFF_TOPIC")) {
					t.Errorf("%s: held for relevance, but the answer is %s", origin, response.Body.String())
				}
				keys := make([]string, 0, 5)
				for key := range got.raw {
					keys = append(keys, key)
				}
				slices.Sort(keys)
				if !slices.Equal(keys, []string{"checked", "kinds", "relevance", "shareable", "state"}) {
					t.Errorf("%s: the answer carries %v", origin, keys)
				}
				// Nothing of the judge's beyond the decision is sent: not its categories, its
				// certainty or why it failed.
				for _, leaked := range []string{"extras", "certainty", "timeout", "0.7"} {
					if strings.Contains(string(got.raw["relevance"]), leaked) {
						t.Errorf("%s: the relevance block carries %q: %s", origin, leaked, got.raw["relevance"])
					}
				}
			}
		})
	}
}

// TestTheCheckRouteWithNoGuardrailSaysNothingWasJudged: with no guardrail there
// is no guard to run the relevance check in, so it says so in the block, whatever
// the settings and the sources.
func TestTheCheckRouteWithNoGuardrailSaysNothingWasJudged(t *testing.T) {
	server, _ := checkServerWith(nil, &fakeAnswerGuardPreferences{}, nil)
	response, got := postCheck(t, server, checkPath, checkBody(t, map[string]any{
		"texts": []string{"Hello"}, "request": "Hi?", "sources": routeSources, "context": []string{"a line"},
	}))
	if response.Code != http.StatusOK || got.Checked {
		t.Fatalf("check = %d %s, want it unchecked", response.Code, response.Body.String())
	}
	if judged, wouldHold, held := relevanceBlock(t, got); judged || wouldHold || held {
		t.Fatalf("relevance = %s, want nothing judged", got.raw["relevance"])
	}
}

// TestTheCheckRouteHoldsOrRecordsAnOffTopicAnswerAsTheMembersSettingsSay drives
// the route through the real guard, a judge and the settings a member has: in
// hold an answer the judge would hold is held, state held with OFF_TOPIC and
// the categories, and nothing to share, however it is read; in shadow it is
// exactly what the personal data read made of it and the block says what would
// have been held; off, and no source that may have read something private, and
// no judge, judge nothing.
func TestTheCheckRouteHoldsOrRecordsAnOffTopicAnswerAsTheMembersSettingsSay(t *testing.T) {
	const doctor = "No, she has a doctor's appointment."
	hold := &fakeAnswerGuardPreferences{}
	shadow := func() *fakeAnswerGuardPreferences {
		return &fakeAnswerGuardPreferences{ceiling: guardpolicy.Cap{Relevance: guardpolicy.RelevanceShadow}}
	}
	off := func() *fakeAnswerGuardPreferences {
		return &fakeAnswerGuardPreferences{ceiling: guardpolicy.Cap{Relevance: guardpolicy.RelevanceOff}}
	}
	regulated := func() *fakeAnswerGuardPreferences {
		return &fakeAnswerGuardPreferences{orgs: fakeOrgPolicies{"org_1": {Regulated: true}}}
	}
	asked := []relevance.Category{relevance.Health}
	held := func(kinds ...string) checkOutcome { return checkOutcome{"held", kinds, nil} }

	for _, testCase := range []struct {
		name     string
		settings AnswerGuardPreferences
		judge    relevance.Result
		noJudge  bool
		origin   string
		texts    []string
		sources  []string
		want     checkOutcome
		relevant [3]bool
		asked    int
	}{
		{"hold, an off-topic answer, from a peer", hold, routeOffTopic, false, "peer", []string{doctor}, routeSources, held("OFF_TOPIC", "health", "hr"), [3]bool{true, true, true}, 1},
		{"hold, an off-topic answer, from the owner", hold, routeOffTopic, false, "owner", []string{doctor}, routeSources, held("OFF_TOPIC", "health", "hr"), [3]bool{true, true, true}, 1},
		{"hold, a clean answer", hold, routeClean, false, "peer", []string{doctor}, routeSources, checkOutcome{"clear", []string{}, []string{doctor}}, [3]bool{true, false, false}, 1},
		{"shadow, an off-topic answer, from a peer", shadow(), routeOffTopic, false, "peer", []string{doctor}, routeSources, checkOutcome{"clear", []string{}, []string{doctor}}, [3]bool{true, true, false}, 1},
		{"shadow, an off-topic answer, from the owner", shadow(), routeOffTopic, false, "owner", []string{doctor}, routeSources, checkOutcome{"clear", []string{}, []string{doctor}}, [3]bool{true, true, false}, 1},
		{"off, an off-topic answer", off(), routeOffTopic, false, "peer", []string{doctor}, routeSources, checkOutcome{"clear", []string{}, []string{doctor}}, [3]bool{false, false, false}, 0},
		{"no settings are kept, an off-topic answer", nil, routeOffTopic, false, "peer", []string{doctor}, routeSources, checkOutcome{"clear", []string{}, []string{doctor}}, [3]bool{false, false, false}, 0},
		{"hold, no sources", hold, routeOffTopic, false, "peer", []string{doctor}, nil, checkOutcome{"clear", []string{}, []string{doctor}}, [3]bool{false, false, false}, 0},
		{"hold, sources that read nothing private", hold, routeOffTopic, false, "peer", []string{doctor}, []string{"directory_find_people", "list_tools"}, checkOutcome{"clear", []string{}, []string{doctor}}, [3]bool{false, false, false}, 0},
		{"hold, a tool nobody has heard of", hold, routeOffTopic, false, "peer", []string{doctor}, []string{"some_new_tool"}, held("OFF_TOPIC", "health", "hr"), [3]bool{true, true, true}, 1},
		{"hold, and no judge at all", hold, routeOffTopic, true, "peer", []string{doctor}, routeSources, checkOutcome{"clear", []string{}, []string{doctor}}, [3]bool{false, false, false}, 0},

		// The personal data read is first, and what it found is kept.
		{
			"hold, an off-topic answer with an email address, from a peer", hold, routeOffTopic, false, "peer", []string{"Fine.", contactAnswer}, routeSources,
			held("EMAIL", "OFF_TOPIC", "health", "hr"), [3]bool{true, true, true}, 1,
		},
		{
			"hold, an off-topic answer with an email address, from the owner", hold, routeOffTopic, false, "owner", []string{"Fine.", contactAnswer}, routeSources,
			held("EMAIL", "OFF_TOPIC", "health", "hr"), [3]bool{true, true, true}, 1,
		},
		{
			"hold, an off-topic answer with a password", hold, routeOffTopic, false, "peer", []string{secretAnswer}, routeSources,
			held("EMAIL", "OFF_TOPIC", "health", "hr"), [3]bool{true, true, true}, 1,
		},
		{
			"shadow, an email address, from a peer", shadow(), routeOffTopic, false, "peer", []string{"Fine.", contactAnswer}, routeSources,
			checkOutcome{"held", []string{"EMAIL"}, []string{"Fine.", "Write to {EMAIL} or call the office."}}, [3]bool{true, true, false}, 1,
		},
		{
			"shadow, an email address and a password, from the owner", shadow(), routeOffTopic, false, "owner", []string{secretAnswer}, routeSources,
			checkOutcome{"masked", []string{"PASSWORD"}, []string{"The password is {PASSWORD}; write to priya@example.com."}}, [3]bool{true, true, false}, 1,
		},
		{
			"hold, an answer that could not be read, and off topic", hold, routeOffTopic, false, "peer", []string{"Fine.", "UNREADABLE " + contactAnswer}, routeSources,
			held("OFF_TOPIC", "UNCHECKED", "health", "hr"), [3]bool{true, true, true}, 1,
		},
		{
			"hold, an answer that could not be read, and clean", hold, routeClean, false, "peer", []string{"Fine.", "UNREADABLE " + contactAnswer}, routeSources,
			held("UNCHECKED"), [3]bool{true, false, false}, 1,
		},

		// A judge that could not tell.
		{"hold, a judge that could not tell", hold, routeFailed, false, "peer", []string{doctor}, routeSources, held("OFF_TOPIC", "UNCHECKED"), [3]bool{true, true, true}, 1},
		{"shadow, a judge that could not tell", shadow(), routeFailed, false, "peer", []string{doctor}, routeSources, checkOutcome{"clear", []string{}, []string{doctor}}, [3]bool{true, true, false}, 1},

		// A regulated organization holds what was asked for, and nothing else is extra.
		{
			"regulated, asked for health", regulated(), relevance.Result{Judged: true, Asked: asked, Model: "route-judge"}, false, "peer", []string{"Yes."}, routeSources,
			held("OFF_TOPIC"), [3]bool{true, true, true}, 1,
		},
		{
			"unregulated, asked for health", hold, relevance.Result{Judged: true, Asked: asked, Model: "route-judge"}, false, "peer", []string{"Yes."}, routeSources,
			checkOutcome{"clear", []string{}, []string{"Yes."}}, [3]bool{true, false, false}, 1,
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			judge := &routeJudge{result: testCase.judge}
			var server *Server
			if testCase.noJudge {
				server = judgedServer(t, nil, testCase.settings, nil)
			} else {
				server = judgedServer(t, judge, testCase.settings, nil)
			}
			body := checkBody(t, map[string]any{
				"texts": testCase.texts, "origin": testCase.origin, "request": "Is she free on Thursday?",
				"sources": testCase.sources, "context": []string{"an earlier line"},
			})
			response, got := postCheck(t, server, checkPath, body)
			if response.Code != http.StatusOK || !got.Checked {
				t.Fatalf("check = %d %s", response.Code, response.Body.String())
			}
			if outcome := (checkOutcome{got.State, got.Kinds, got.Shareable}); !outcome.is(testCase.want.state, testCase.want.kinds, testCase.want.shareable...) ||
				(got.Shareable == nil) != (testCase.want.shareable == nil) {
				t.Errorf("check = %s, want state %q kinds %v shareable %v", response.Body.String(), testCase.want.state, testCase.want.kinds, testCase.want.shareable)
			}
			if judged, wouldHold, held := relevanceBlock(t, got); [3]bool{judged, wouldHold, held} != testCase.relevant {
				t.Errorf("relevance = %s, want %v", got.raw["relevance"], testCase.relevant)
			}
			if judge.calls() != testCase.asked {
				t.Fatalf("the judge was asked %d times, want %d", judge.calls(), testCase.asked)
			}
			if testCase.asked == 1 {
				// The judge reads what the agent said, as it was given, and what was asked.
				in := judge.inputs[0]
				if in.Request != "Is she free on Thursday?" || !slices.Equal(in.Context, []string{"an earlier line"}) || !slices.Equal(in.Sources, testCase.sources) ||
					!slices.Equal(in.Answer, testCase.texts) || in.Regulated != (testCase.name == "regulated, asked for health") {
					t.Errorf("the judge was given %+v", in)
				}
			}
			if testCase.relevant[2] && got.Shareable != nil {
				t.Errorf("an answer held for relevance shares %q", got.Shareable)
			}
		})
	}
}

// TestTheCheckRouteNeverLogsWhatTheRelevanceCheckWasGiven: the route and the
// guard log what was decided, by whose answer it was, and never a text, the
// request, the context, a source or anything the judge said, whatever the
// answer was and however the check went.
func TestTheCheckRouteNeverLogsWhatTheRelevanceCheckWasGiven(t *testing.T) {
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	texts := []string{"Priya starts chemotherapy on Monday; the vault password is hunter2 (priya@example.com)."}
	body := func(origin string) string {
		return checkBody(t, map[string]any{
			"texts": texts, "origin": origin, "request": "What is the salary of Priya Raman?",
			"context": []string{"an earlier line about the acquisition of Northwind"}, "sources": []string{"apps_gmail_search_messages", "secret_tool_name"},
		})
	}
	for name, result := range map[string]relevance.Result{
		"an off-topic answer": routeOffTopic,
		"a clean answer":      routeClean,
		"a judge that failed": {Judged: true, Hold: true, Err: fmt.Errorf("the model said: %s %w", texts[0], relevance.ErrUnavailable)},
	} {
		logs.Reset()
		server := judgedServer(t, &routeJudge{result: result}, &fakeAnswerGuardPreferences{}, logger)
		for _, origin := range []string{"peer", "owner"} {
			if response := serve(server, http.MethodPost, checkPath, body(origin), bearer); response.Code != http.StatusOK {
				t.Fatalf("%s: check = %d %s", name, response.Code, response.Body.String())
			}
		}
		for _, secret := range []string{
			"chemotherapy", "hunter2", "priya@example.com", "Priya", "Raman", "salary", "Northwind", "acquisition", "secret_tool_name", "apps_gmail", "the model said",
		} {
			if strings.Contains(logs.String(), secret) {
				t.Errorf("%s: the log holds %q:\n%s", name, secret, logs.String())
			}
		}
		relevanceLines := 0
		for _, line := range strings.Split(strings.TrimSpace(logs.String()), "\n") {
			if strings.Count(line, `"level":`) != 1 {
				t.Errorf("%s: a log line names its level %d times, want once: %s", name, strings.Count(line, `"level":`), line)
			}
			if strings.Contains(line, "relevance check") {
				relevanceLines++
				for _, said := range []string{`"organization_id":"org_1"`, `"user_id":"user_1"`, `"mode":"hold"`} {
					if !strings.Contains(line, said) {
						t.Errorf("%s: the relevance line does not say %s: %s", name, said, line)
					}
				}
			}
		}
		if relevanceLines != 2 {
			t.Errorf("%s: %d relevance lines for two judged checks, want 2:\n%s", name, relevanceLines, logs.String())
		}
	}
}
