package slackrouter

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Shubham04-oss/oryxa-go/internal/jev"
)

// fakeJev is TypeSafe's System One as the screen sees it: a function from a
// request to answers. It answers every question of a request with a
// probability of yes worked out from the state the request carried, and
// remembers each request. No network is involved.
type fakeJev struct {
	mu       sync.Mutex
	requests []jev.Request
	// deadlines are how long each call was given, when it was given a deadline.
	deadlines []time.Duration
	// score is the probability of yes for the question id, given the state.
	// Nil answers as a clean ask.
	score func(id string, state screenState) float64
	// err fails the call. hang holds it until its context ends, as a Jev that
	// never answers does. answers, when set, replaces the whole answer.
	err     error
	hang    bool
	answers func(request jev.Request) jev.Result
	// errFor fails the call with an error made from the request, as a service
	// that quotes what it was sent in its failure would.
	errFor func(request jev.Request) error
}

func (fake *fakeJev) Ask(ctx context.Context, request jev.Request) (jev.Result, error) {
	fake.mu.Lock()
	fake.requests = append(fake.requests, request)
	if deadline, ok := ctx.Deadline(); ok {
		fake.deadlines = append(fake.deadlines, time.Until(deadline))
	}
	err, hang, score, answers, errFor := fake.err, fake.hang, fake.score, fake.answers, fake.errFor
	fake.mu.Unlock()
	if hang {
		<-ctx.Done()
		return jev.Result{}, fmt.Errorf("jev: call TypeSafe: %w", ctx.Err())
	}
	if err != nil {
		return jev.Result{}, err
	}
	if errFor != nil {
		return jev.Result{}, errFor(request)
	}
	if answers != nil {
		return answers(request), nil
	}
	if score == nil {
		score = cleanScore
	}
	state := stateOf(request)
	result := jev.Result{Model: "jev-test", InputTokens: 100, Answers: map[string]jev.Answer{}}
	for id, question := range request.Questions {
		result.Answers[id] = jev.Answer{Type: question.Type, Noul: score(id, state)}
	}
	return result, nil
}

func (fake *fakeJev) calls() []jev.Request {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	return append([]jev.Request(nil), fake.requests...)
}

// stateOf reads the state of a request as Jev would read it, from its JSON.
func stateOf(request jev.Request) screenState {
	encoded, err := json.Marshal(request.State)
	if err != nil {
		panic(err)
	}
	var state screenState
	if err := json.Unmarshal(encoded, &state); err != nil {
		panic(err)
	}
	return state
}

// cleanScore is Jev's read of an ask that is work, is not an injection, is
// plausible, asks for no credential, and fits every owner it is put to.
func cleanScore(id string, _ screenState) float64 {
	switch {
	case id == qWork:
		return 0.95
	case id == qPlausible:
		return 0.9
	case id == qInjection, id == qCredential:
		return 0.02
	case strings.HasPrefix(id, qFitPrefix):
		return 0.9
	}
	panic("a question this fake does not know: " + id)
}

// keywordScore reads the state as a crude judge would, by what it says, so that
// a table of asks can be told apart by what they contain. It stands in for
// Jev's judgment; what the tests check is what the router does with it.
func keywordScore(id string, state screenState) float64 {
	message := strings.ToLower(state.Message)
	has := func(words ...string) bool {
		for _, word := range words {
			if strings.Contains(message, word) {
				return true
			}
		}
		return false
	}
	switch {
	case id == qWork:
		if has("who won the game", "lol") {
			return 0.03
		}
	case id == qInjection:
		if has("ignore your previous instructions", "you are now in developer mode") {
			return 0.96
		}
	case id == qCredential:
		if has("password", "api key", "deploy token") {
			return 0.94
		}
	case id == qPlausible:
		if strings.Contains(strings.ToLower(state.Asker.SlackTitle), "intern") && has("acquisition") {
			return 0.04
		}
	case strings.HasPrefix(id, qFitPrefix):
		index, err := strconv.Atoi(strings.TrimPrefix(id, qFitPrefix))
		if err != nil || index >= len(state.Owners) {
			return 0.5
		}
		// A question about a database is not one for someone on the Design team.
		for _, team := range state.Owners[index].Teams {
			if strings.EqualFold(team, "Design") && has("database", "deploy") {
				return 0.05
			}
		}
	default:
		panic("a question this fake does not know: " + id)
	}
	return cleanScore(id, state)
}

// useJev screens with the production screen over a fake Jev.
func (h *harness) useJev(fake *fakeJev, timeout time.Duration) {
	h.t.Helper()
	h.router.deps.Screen = NewJevScreen(JevScreenConfig{Ask: fake.Ask, Timeout: timeout})
}

// screenInput is an ask as the router puts it to a screen: the question and
// an asker with a role, and no teams.
func screenInput(question string, owners ...ScreenOwner) ScreenInput {
	return ScreenInput{Question: question, Asker: ScreenAsker{OrgRole: "member"}, Owners: owners}
}

func mustJudge(t *testing.T, screen Screen, input ScreenInput) ScreenResult {
	t.Helper()
	result, err := screen.Judge(context.Background(), input)
	if err != nil {
		t.Fatalf("Judge: %v", err)
	}
	return result
}
