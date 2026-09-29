package httpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/answerguard"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/authclient"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/guardpolicy"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/managedagents"
)

const checkPath = "/v1/organizations/org_1/answer-guard/check"

// fakeAnswerChecker answers every check with what verdict makes of its texts,
// clear as given when it makes nothing, and keeps every check it was asked,
// and the relevance input each came with (nil for a check that had none).
type fakeAnswerChecker struct {
	mu        sync.Mutex
	asked     [][]string
	relevance []*answerguard.RelevanceInput
	verdict   func(texts []string) answerguard.Verdict
}

func (fake *fakeAnswerChecker) CheckWith(_ context.Context, texts []string, relevance *answerguard.RelevanceInput) answerguard.Verdict {
	fake.mu.Lock()
	fake.asked = append(fake.asked, slices.Clone(texts))
	if relevance != nil {
		kept := *relevance
		kept.Context, kept.Sources = slices.Clone(relevance.Context), slices.Clone(relevance.Sources)
		relevance = &kept
	}
	fake.relevance = append(fake.relevance, relevance)
	fake.mu.Unlock()
	if fake.verdict != nil {
		return fake.verdict(texts)
	}
	return answerguard.Verdict{State: answerguard.StateClear, Texts: slices.Clone(texts), Given: slices.Clone(texts)}
}

func (fake *fakeAnswerChecker) checks() int {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	return len(fake.asked)
}

// members are the members the limiter holds a check for, which the tests read.
func (limiter *checkLimiter) members() []checkKey {
	limiter.mu.Lock()
	defer limiter.mu.Unlock()
	var keys []checkKey
	for key, hits := range limiter.hits {
		if len(hits) > 0 {
			keys = append(keys, key)
		}
	}
	return keys
}

// checkServer is a server whose check route reads with guard, or reads
// nothing when guard is nil, and keeps no member's answer guard settings.
func checkServer(guard AnswerChecker, logger *slog.Logger) (*Server, *fakeDirectory) {
	return checkServerWith(guard, nil, logger)
}

// checkServerWith is checkServer, with settings to tell what level applies to a
// member's answers. A nil settings keeps none; a pointer that was never built,
// in it, is what New is to take for none.
func checkServerWith(guard AnswerChecker, settings AnswerGuardPreferences, logger *slog.Logger) (*Server, *fakeDirectory) {
	directory := member()
	config := Config{
		AllowedOrigins: []string{"https://app.oryxa.test"}, RunTimeout: time.Minute,
		Agents: &fakeManagedAgents{definitions: []managedagents.Definition{{ID: "agt_primary", Kind: managedagents.KindPrimary, Name: "Carl", Status: managedagents.StatusActive}}},
	}
	if guard != nil {
		config.AnswerGuard = guard
	}
	if settings != nil {
		config.AnswerGuardPreferences = settings
	}
	return New(directory, &fakeThreads{}, config, logger), directory
}

// checkResult is what a check answers, and the JSON as it was written.
type checkResult struct {
	Checked   bool     `json:"checked"`
	State     string   `json:"state"`
	Kinds     []string `json:"kinds"`
	Shareable []string `json:"shareable"`
	raw       map[string]json.RawMessage
}

func postCheck(t *testing.T, server *Server, path, body string) (*httptest.ResponseRecorder, checkResult) {
	t.Helper()
	response := serve(server, http.MethodPost, path, body, bearer)
	var result checkResult
	if response.Code == http.StatusOK {
		if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
			t.Fatalf("answer %q: %v", response.Body.String(), err)
		}
		if err := json.Unmarshal(response.Body.Bytes(), &result.raw); err != nil {
			t.Fatal(err)
		}
	}
	return response, result
}

// TestTheCheckRouteSaysWhatTheGuardAllowsToBeShared: the answer is the guard's
// verdict as a room applies it, for an answer another member will read:
// nothing found shares the texts, masked kinds share them masked, a held kind
// shares the fully masked version and says it was held, and what could not be
// read, or a verdict that does not add up, shares nothing, as null.
func TestTheCheckRouteSaysWhatTheGuardAllowsToBeShared(t *testing.T) {
	texts := `{"texts":["first","second"]}`
	for _, testCase := range []struct {
		name      string
		verdict   func(texts []string) answerguard.Verdict
		state     string
		kinds     []string
		shareable []string
		rawKinds  string
		rawShared string
	}{
		{
			name: "nothing found",
			verdict: func(texts []string) answerguard.Verdict {
				return answerguard.Verdict{State: answerguard.StateClear, Texts: texts}
			},
			state: "clear", kinds: []string{}, shareable: []string{"first", "second"}, rawKinds: `[]`, rawShared: `["first","second"]`,
		},
		{
			name: "only kinds that are always masked",
			verdict: func([]string) answerguard.Verdict {
				return answerguard.Verdict{State: answerguard.StateMasked, Kinds: []string{"PASSWORD"}, Texts: []string{"first", "the password is {PASSWORD}"}}
			},
			state: "masked", kinds: []string{"PASSWORD"}, shareable: []string{"first", "the password is {PASSWORD}"}, rawKinds: `["PASSWORD"]`, rawShared: `["first","the password is {PASSWORD}"]`,
		},
		{
			name: "a held kind shares the masked version",
			verdict: func([]string) answerguard.Verdict {
				return answerguard.Verdict{State: answerguard.StateHeld, Kinds: []string{"EMAIL"}, Texts: []string{"first", "write to priya@example.com"}, Masked: []string{"first", "write to {EMAIL}"}}
			},
			state: "held", kinds: []string{"EMAIL"}, shareable: []string{"first", "write to {EMAIL}"}, rawKinds: `["EMAIL"]`, rawShared: `["first","write to {EMAIL}"]`,
		},
		{
			name: "an answer that could not be checked",
			verdict: func(texts []string) answerguard.Verdict {
				return answerguard.Verdict{State: answerguard.StateHeld, Kinds: []string{answerguard.Unchecked}, Texts: texts}
			},
			state: "held", kinds: []string{"UNCHECKED"}, shareable: nil, rawKinds: `["UNCHECKED"]`, rawShared: `null`,
		},
		{
			name: "a verdict for a text too few",
			verdict: func([]string) answerguard.Verdict {
				return answerguard.Verdict{State: answerguard.StateClear, Texts: []string{"first"}}
			},
			state: "held", kinds: []string{"UNCHECKED"}, shareable: nil, rawKinds: `["UNCHECKED"]`, rawShared: `null`,
		},
		{
			name: "a verdict in a state nobody knows",
			verdict: func(texts []string) answerguard.Verdict {
				return answerguard.Verdict{State: "quarantined", Texts: texts}
			},
			state: "held", kinds: []string{"UNCHECKED"}, shareable: nil, rawKinds: `["UNCHECKED"]`, rawShared: `null`,
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			guard := &fakeAnswerChecker{verdict: testCase.verdict}
			server, _ := checkServer(guard, nil)
			response, got := postCheck(t, server, checkPath, texts)
			if response.Code != http.StatusOK || response.Header().Get("Content-Type") != "application/json" {
				t.Fatalf("check = %d %s %v", response.Code, response.Body.String(), response.Header())
			}
			if !got.Checked || got.State != testCase.state || !slices.Equal(got.Kinds, testCase.kinds) ||
				(got.Shareable == nil) != (testCase.shareable == nil) || !slices.Equal(got.Shareable, testCase.shareable) {
				t.Fatalf("check = %+v, want state %q kinds %v shareable %v", got, testCase.state, testCase.kinds, testCase.shareable)
			}
			if string(got.raw["kinds"]) != testCase.rawKinds || string(got.raw["shareable"]) != testCase.rawShared || string(got.raw["checked"]) != "true" {
				t.Fatalf("the JSON was %s, want kinds %s and shareable %s", response.Body.String(), testCase.rawKinds, testCase.rawShared)
			}
			if guard.checks() != 1 || !slices.Equal(guard.asked[0], []string{"first", "second"}) {
				t.Fatalf("the guard was asked %v, want the two texts once", guard.asked)
			}
		})
	}
}

// TestTheCheckRouteWithNoGuardrailChecksNothingAndSaysSo: without a guardrail
// nothing is read, and the answer is that: checked false, clear, no kinds, and
// every text as it was given, whichever origin it names.
func TestTheCheckRouteWithNoGuardrailChecksNothingAndSaysSo(t *testing.T) {
	server, _ := checkServer(nil, nil)
	for _, body := range []string{
		`{"texts":["The password is hunter2.","write to priya@example.com"]}`,
		`{"texts":["The password is hunter2.","write to priya@example.com"],"origin":"owner"}`,
		`{"texts":["The password is hunter2.","write to priya@example.com"],"origin":"peer","request":"What is it?"}`,
	} {
		response := serve(server, http.MethodPost, checkPath, body, bearer)
		want := `{"checked":false,"state":"clear","kinds":[],"shareable":["The password is hunter2.","write to priya@example.com"],"relevance":{"judged":false,"would_hold":false,"held":false}}` + "\n"
		if response.Code != http.StatusOK || response.Body.String() != want {
			t.Errorf("%s = %d %s, want %s", body, response.Code, response.Body.String(), want)
		}
	}
	// It still has to be the member's own call, and inside the limits.
	if response := serve(server, http.MethodPost, checkPath, `{"texts":[]}`, bearer); response.Code != http.StatusBadRequest {
		t.Errorf("no texts with no guardrail = %d, want 400", response.Code)
	}
	if response := serve(server, http.MethodPost, checkPath, `{"texts":["x"]}`, nil); response.Code != http.StatusUnauthorized {
		t.Errorf("no credentials with no guardrail = %d, want 401", response.Code)
	}
}

// repeated is text of n characters.
func repeated(character string, n int) string { return strings.Repeat(character, n) }

func jsonTexts(texts ...string) string {
	encoded, err := json.Marshal(map[string]any{"texts": texts})
	if err != nil {
		panic(err)
	}
	return string(encoded)
}

// TestTheCheckRouteRefusesWhatIsOutOfBounds: 1 to 8 texts of at most 25,000
// characters each, a request of at most as many, an origin that is exactly
// owner or peer, and a body that is one JSON object with only the fields it
// knows: anything else is a 400 invalid_request, and the guard is never asked.
func TestTheCheckRouteRefusesWhatIsOutOfBounds(t *testing.T) {
	long := repeated("a", maxCheckTextRunes+1)
	for name, body := range map[string]string{
		"no body":                       ``,
		"an empty object":               `{}`,
		"texts left out":                `{"origin":"peer"}`,
		"null texts":                    `{"texts":null}`,
		"no texts":                      `{"texts":[]}`,
		"nine texts":                    jsonTexts("1", "2", "3", "4", "5", "6", "7", "8", "9"),
		"a text 25,001 characters long": jsonTexts("fine", long),
		"a text 25,001 characters, é":   jsonTexts(repeated("é", maxCheckTextRunes+1)),
		"a text 25,001 characters, 世":   jsonTexts(repeated("世", maxCheckTextRunes+1)),
		"a request 25,001 characters":   `{"texts":["fine"],"request":"` + long + `"}`,
		"a request that is not text":    `{"texts":["fine"],"request":42}`,
		"an unknown field":              `{"texts":["fine"],"__not_a_real_field__":true}`,
		"texts that are not text":       `{"texts":[1,2]}`,
		"texts that are not a list":     `{"texts":"fine"}`,
		"a list, not an object":         `["fine"]`,
		"two objects":                   `{"texts":["fine"]}{"texts":["fine"]}`,
		"malformed JSON":                `{"texts":["fine"`,
		"a NUL in a text":               `{"texts":["fi` + jsonNUL + `ne"]}`,
		"a NUL in the request":          `{"texts":["fine"],"request":"wh` + jsonNUL + `at"}`,
		"an origin nobody knows":        `{"texts":["fine"],"origin":"member"}`,
		"an origin in another case":     `{"texts":["fine"],"origin":"Owner"}`,
		"an origin with spaces":         `{"texts":["fine"],"origin":" owner "}`,
		"an empty origin":               `{"texts":["fine"],"origin":""}`,
		"an origin that is not text":    `{"texts":["fine"],"origin":true}`,
		"a body past every limit":       `{"texts":["fine"],"request":"` + repeated("a", maxCheckBodyBytes) + `"}`,
	} {
		t.Run(name, func(t *testing.T) {
			guard := &fakeAnswerChecker{}
			server, _ := checkServer(guard, nil)
			response := serve(server, http.MethodPost, checkPath, body, bearer)
			if response.Code != http.StatusBadRequest || errorCode(t, response) != "invalid_request" {
				t.Fatalf("check = %d %.200s, want 400 invalid_request", response.Code, response.Body.String())
			}
			if guard.checks() != 0 {
				t.Fatalf("the guard was asked %d times over a request that was refused", guard.checks())
			}
		})
	}
}

// TestTheCheckRouteTakesWhatIsWithinBounds: the most a check may hold is
// accepted, in characters and not bytes: eight texts of 25,000 characters, of
// every width, beside a request as long, though that is far past the 256 KiB
// the other routes read; and the smallest, one text, one character.
func TestTheCheckRouteTakesWhatIsWithinBounds(t *testing.T) {
	for name, body := range map[string]string{
		"one text of one character":             jsonTexts("x"),
		"one blank text":                        jsonTexts(""),
		"eight texts":                           jsonTexts("1", "2", "3", "4", "5", "6", "7", "8"),
		"eight texts of 25,000 characters":      jsonTexts(slices.Repeat([]string{repeated("a", maxCheckTextRunes)}, 8)...),
		"eight texts of 25,000 characters, é":   jsonTexts(slices.Repeat([]string{repeated("é", maxCheckTextRunes)}, 8)...),
		"eight texts of 25,000 characters, 世":   jsonTexts(slices.Repeat([]string{repeated("世", maxCheckTextRunes)}, 8)...),
		"eight texts of 25,000 characters, 😀":   jsonTexts(slices.Repeat([]string{repeated("😀", maxCheckTextRunes)}, 8)...),
		"a request of 25,000 characters":        `{"texts":["fine"],"request":"` + repeated("q", maxCheckTextRunes) + `"}`,
		"the most of everything, in the widest": `{"texts":` + jsonTextsList(slices.Repeat([]string{repeated("😀", maxCheckTextRunes)}, 8)) + `,"request":"` + repeated("😀", maxCheckTextRunes) + `","origin":"owner"}`,
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
}

func jsonTextsList(texts []string) string {
	encoded, err := json.Marshal(texts)
	if err != nil {
		panic(err)
	}
	return string(encoded)
}

// bedrockFinding is a Bedrock that finds a password, hunter2, which the
// guardrail masks, and the email address priya@example.com, which it only
// detects, and that cannot read a text with UNREADABLE in it.
type bedrockFinding struct{}

func (bedrockFinding) ApplyGuardrail(_ context.Context, input *bedrockruntime.ApplyGuardrailInput, _ ...func(*bedrockruntime.Options)) (*bedrockruntime.ApplyGuardrailOutput, error) {
	text := aws.ToString(input.Content[0].(*types.GuardrailContentBlockMemberText).Value.Text)
	if strings.Contains(text, "UNREADABLE") {
		return nil, errors.New("bedrock is down")
	}
	answer := &bedrockruntime.ApplyGuardrailOutput{Action: types.GuardrailActionNone}
	var entities []types.GuardrailPiiEntityFilter
	if strings.Contains(text, "priya@example.com") {
		entities = append(entities, types.GuardrailPiiEntityFilter{
			Type: "EMAIL", Match: aws.String("priya@example.com"), Detected: aws.Bool(true), Action: types.GuardrailSensitiveInformationPolicyActionNone,
		})
	}
	if strings.Contains(text, "hunter2") {
		entities = append(entities, types.GuardrailPiiEntityFilter{
			Type: "PASSWORD", Match: aws.String("hunter2"), Detected: aws.Bool(true), Action: types.GuardrailSensitiveInformationPolicyActionAnonymized,
		})
		answer.Action = types.GuardrailActionGuardrailIntervened
		answer.Outputs = []types.GuardrailOutputContent{{Text: aws.String(strings.ReplaceAll(text, "hunter2", "{PASSWORD}"))}}
	}
	if len(entities) > 0 {
		answer.Assessments = []types.GuardrailAssessment{{SensitiveInformationPolicy: &types.GuardrailSensitiveInformationPolicyAssessment{PiiEntities: entities}}}
	}
	return answer, nil
}

func realGuard(t *testing.T) *answerguard.Guard {
	t.Helper()
	guard, err := answerguard.New(bedrockFinding{}, answerguard.Config{Identifier: "gr_answers", Version: "1"})
	if err != nil {
		t.Fatal(err)
	}
	return guard
}

// TestTheCheckRouteReadsAnAnswerAtTheLevelOfItsOrigin: the same answer is
// read strictly for a peer, which is what a check that names no origin is,
// and, for the member who started it, with only what is always masked taken
// out: an email address beside a password comes back intact with the password
// masked, and the state and kinds say only what was acted on. An answer that
// could not be read shares nothing whoever it is for.
func TestTheCheckRouteReadsAnAnswerAtTheLevelOfItsOrigin(t *testing.T) {
	both := "The password is hunter2; write to priya@example.com."
	contact := "Write to priya@example.com or call the office."
	server, _ := checkServer(realGuard(t), nil)
	type outcome struct {
		state     string
		kinds     []string
		shareable []string
	}
	for _, testCase := range []struct {
		name  string
		texts []string
		peer  outcome
		owner outcome
	}{
		{
			name:  "an email address and a password",
			texts: []string{both},
			peer:  outcome{"held", []string{"EMAIL"}, []string{"The password is {PASSWORD}; write to {EMAIL}."}},
			owner: outcome{"masked", []string{"PASSWORD"}, []string{"The password is {PASSWORD}; write to priya@example.com."}},
		},
		{
			name:  "only an email address",
			texts: []string{"Fine.", contact},
			peer:  outcome{"held", []string{"EMAIL"}, []string{"Fine.", "Write to {EMAIL} or call the office."}},
			owner: outcome{"clear", []string{}, []string{"Fine.", contact}},
		},
		{
			name:  "only a password",
			texts: []string{"The password is hunter2."},
			peer:  outcome{"masked", []string{"PASSWORD"}, []string{"The password is {PASSWORD}."}},
			owner: outcome{"masked", []string{"PASSWORD"}, []string{"The password is {PASSWORD}."}},
		},
		{
			name:  "nothing",
			texts: []string{"The launch is on Thursday."},
			peer:  outcome{"clear", []string{}, []string{"The launch is on Thursday."}},
			owner: outcome{"clear", []string{}, []string{"The launch is on Thursday."}},
		},
		{
			name:  "an answer that could not be read",
			texts: []string{"Fine.", "UNREADABLE " + both},
			peer:  outcome{"held", []string{"UNCHECKED"}, nil},
			owner: outcome{"held", []string{"UNCHECKED"}, nil},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			texts := jsonTextsList(testCase.texts)
			for name, request := range map[string]struct {
				body string
				want outcome
			}{
				"no origin":     {`{"texts":` + texts + `}`, testCase.peer},
				"a null origin": {`{"texts":` + texts + `,"origin":null}`, testCase.peer},
				"a peer":        {`{"texts":` + texts + `,"origin":"peer","request":"What is the password?"}`, testCase.peer},
				"the owner":     {`{"texts":` + texts + `,"origin":"owner","request":"What is the password?"}`, testCase.owner},
			} {
				response, got := postCheck(t, server, checkPath, request.body)
				if response.Code != http.StatusOK || !got.Checked || got.State != request.want.state || !slices.Equal(got.Kinds, request.want.kinds) ||
					(got.Shareable == nil) != (request.want.shareable == nil) || !slices.Equal(got.Shareable, request.want.shareable) {
					t.Errorf("%s: %d %s, want state %q kinds %v shareable %v", name, response.Code, response.Body.String(), request.want.state, request.want.kinds, request.want.shareable)
				}
			}
		})
	}
}

// TestAnOriginChoosesHowStrictlyAnAnswerIsReadWhenNobodyChose: with no
// member's settings kept, an answer a peer will read is read strictly, the
// member's own with only the secrets taken out, and an origin nobody knows is
// read strictly too, so it can never be read looser: what a route has always
// done, and what a member who chose nothing gets.
func TestAnOriginChoosesHowStrictlyAnAnswerIsReadWhenNobodyChose(t *testing.T) {
	for origin, want := range map[string]answerguard.Intensity{
		originOwner: answerguard.IntensitySecrets,
		originPeer:  answerguard.IntensityStrict,
		"":          answerguard.IntensityStrict,
		"Owner":     answerguard.IntensityStrict,
		"someone":   answerguard.IntensityStrict,
	} {
		if got := defaultCheckLevel(origin); got != want {
			t.Errorf("defaultCheckLevel(%q) = %q, want %q", origin, got, want)
		}
	}
}

// TestTheCheckRouteKeepsAndLogsNothingItWasAsked: what was asked is never
// among the texts the guard reads for personal data; and the log names what was
// found by kind, never by value, and holds no text and none of what was asked.
func TestTheCheckRouteKeepsAndLogsNothingItWasAsked(t *testing.T) {
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	real := realGuard(t)
	guard := &fakeAnswerChecker{verdict: func(texts []string) answerguard.Verdict {
		return real.Check(context.Background(), texts)
	}}
	server, _ := checkServer(guard, logger)
	const question = "What is the staging password for the vault secret-plan?"
	for _, origin := range []string{"peer", "owner"} {
		body := jsonTextsWithRequest([]string{"The password is hunter2; write to priya@example.com.", "UNREADABLE secret-plan"}, origin, question)
		if response := serve(server, http.MethodPost, checkPath, body, bearer); response.Code != http.StatusOK {
			t.Fatalf("check = %d %s", response.Code, response.Body.String())
		}
	}
	if guard.checks() != 2 {
		t.Fatalf("the guard was asked %d times, want 2", guard.checks())
	}
	for _, texts := range guard.asked {
		for _, text := range texts {
			if strings.Contains(text, "staging password for the vault") {
				t.Errorf("the guard was handed what was asked: %q", text)
			}
		}
	}
	for _, secret := range []string{"hunter2", "priya@example.com", "secret-plan", "staging password", "UNREADABLE", "The password is"} {
		if strings.Contains(logs.String(), secret) {
			t.Errorf("the log holds %q:\n%s", secret, logs.String())
		}
	}
	for _, said := range []string{`"organization_id":"org_1"`, `"user_id":"user_1"`, `"origin":"owner"`, `"origin":"peer"`, `"intensity":"secrets"`, `"intensity":"strict"`, `"UNCHECKED"`} {
		if !strings.Contains(logs.String(), said) {
			t.Errorf("the log does not say %s:\n%s", said, logs.String())
		}
	}
	// A record has one severity: a second "level" key in the line, say for the
	// level a check was read at, would hide it from whatever reads the line.
	for _, line := range strings.Split(strings.TrimSpace(logs.String()), "\n") {
		if strings.Count(line, `"level":`) != 1 {
			t.Errorf("a log line names its level %d times, want once: %s", strings.Count(line, `"level":`), line)
		}
	}
}

func jsonTextsWithRequest(texts []string, origin, request string) string {
	encoded, err := json.Marshal(map[string]any{"texts": texts, "origin": origin, "request": request})
	if err != nil {
		panic(err)
	}
	return string(encoded)
}

// TestAMemberMayMakeThirtyChecksAMinute: the thirty-first check in a minute is
// 429 rate_limited with Retry-After, and never reaches the guard; another
// member, and the same member in another organization, have a limit of their
// own; the limit lets a member in again as their oldest check leaves the
// minute; and what was malformed, or refused before it, counts for nothing.
func TestAMemberMayMakeThirtyChecksAMinute(t *testing.T) {
	guard := &fakeAnswerChecker{}
	server, directory := checkServer(guard, nil)
	now := time.Date(2026, 9, 29, 9, 0, 0, 0, time.UTC)
	server.checks.now = func() time.Time { return now }
	check := func(path string) *httptest.ResponseRecorder {
		return serve(server, http.MethodPost, path, `{"texts":["Hello"]}`, bearer)
	}

	// Malformed checks, and ones nobody may make, do not count.
	for range 50 {
		if response := serve(server, http.MethodPost, checkPath, `{"texts":[]}`, bearer); response.Code != http.StatusBadRequest {
			t.Fatalf("a malformed check = %d", response.Code)
		}
		if response := serve(server, http.MethodPost, checkPath, `{"texts":["Hello"]}`, nil); response.Code != http.StatusUnauthorized {
			t.Fatalf("a check with no credentials = %d", response.Code)
		}
	}

	for count := range checksPerWindow {
		if response := check(checkPath); response.Code != http.StatusOK {
			t.Fatalf("check %d = %d %s, want 200", count+1, response.Code, response.Body.String())
		}
		now = now.Add(time.Second)
	}
	// 30 seconds in: the first check was made 30 seconds ago, so a wait of 30.
	refused := check(checkPath)
	if refused.Code != http.StatusTooManyRequests || errorCode(t, refused) != "rate_limited" || refused.Header().Get("Retry-After") != "30" {
		t.Fatalf("the thirty-first check = %d %s (Retry-After %q), want 429 rate_limited and 30", refused.Code, refused.Body.String(), refused.Header().Get("Retry-After"))
	}
	if guard.checks() != checksPerWindow {
		t.Fatalf("the guard was asked %d times, want %d", guard.checks(), checksPerWindow)
	}

	// Another member, and another organization, have their own.
	directory.membership.UserID = "user_2"
	if response := check(checkPath); response.Code != http.StatusOK {
		t.Errorf("another member's check = %d %s, want 200", response.Code, response.Body.String())
	}
	directory.membership.UserID = "user_1"
	if response := check("/v1/organizations/org_2/answer-guard/check"); response.Code != http.StatusOK {
		t.Errorf("the same member in another organization = %d %s, want 200", response.Code, response.Body.String())
	}
	if response := check(checkPath); response.Code != http.StatusTooManyRequests {
		t.Errorf("the member's next check in the first organization = %d, want it still refused", response.Code)
	}

	// Refused checks do not lengthen the wait: 30 seconds on, the oldest check
	// is a minute old and one more is let in, then it is thirty again.
	now = now.Add(30 * time.Second)
	if response := check(checkPath); response.Code != http.StatusOK {
		t.Fatalf("a check as the oldest one leaves the minute = %d %s, want 200", response.Code, response.Body.String())
	}
	if response := check(checkPath); response.Code != http.StatusTooManyRequests {
		t.Fatalf("a second check in the same second = %d, want 429", response.Code)
	}
	now = now.Add(2 * time.Minute)
	for count := range checksPerWindow {
		if response := check(checkPath); response.Code != http.StatusOK {
			t.Fatalf("check %d after a quiet spell = %d, want 200", count+1, response.Code)
		}
	}
}

// TestTheLimitOnChecksHoldsWithNoGuardrailToo: a server with no guardrail
// holds a client to the same limit, so what development lets through is what
// production will.
func TestTheLimitOnChecksHoldsWithNoGuardrailToo(t *testing.T) {
	server, _ := checkServer(nil, nil)
	for count := range checksPerWindow {
		if response := serve(server, http.MethodPost, checkPath, `{"texts":["Hello"]}`, bearer); response.Code != http.StatusOK {
			t.Fatalf("check %d = %d", count+1, response.Code)
		}
	}
	if response := serve(server, http.MethodPost, checkPath, `{"texts":["Hello"]}`, bearer); response.Code != http.StatusTooManyRequests {
		t.Fatalf("the thirty-first check = %d, want 429", response.Code)
	}
}

// TestTheCheckLimiterHoldsOnlyMembersWhoCheckedLately: a member idle for a
// window is forgotten, so the limiter's memory follows who is checking now and
// not everybody who ever did; and however many ask at once, no more than the
// limit are let in.
func TestTheCheckLimiterHoldsOnlyMembersWhoCheckedLately(t *testing.T) {
	limiter := newCheckLimiter(3, time.Minute)
	now := time.Date(2026, 9, 29, 9, 0, 0, 0, time.UTC)
	limiter.now = func() time.Time { return now }
	for member := range 20 {
		if allowed, _ := limiter.allow(checkKey{"org_1", "user_" + strconv.Itoa(member)}); !allowed {
			t.Fatal("a member's first check was refused")
		}
	}
	if got := len(limiter.members()); got != 20 {
		t.Fatalf("the limiter holds %d members, want 20", got)
	}
	now = now.Add(2 * time.Minute)
	if allowed, _ := limiter.allow(checkKey{"org_1", "user_0"}); !allowed {
		t.Fatal("a member back after two minutes was refused")
	}
	if got := len(limiter.hits); got != 1 {
		t.Fatalf("the limiter still holds %d members after they were idle a window", got)
	}

	limiter = newCheckLimiter(30, time.Minute)
	var allowed, refused int
	var wait sync.WaitGroup
	var mu sync.Mutex
	for range 200 {
		wait.Go(func() {
			ok, _ := limiter.allow(checkKey{"org_1", "user_1"})
			mu.Lock()
			defer mu.Unlock()
			if ok {
				allowed++
			} else {
				refused++
			}
		})
	}
	wait.Wait()
	if allowed != 30 || refused != 170 {
		t.Fatalf("of 200 checks at once %d were let in and %d refused, want 30 and 170", allowed, refused)
	}
}

// TestTheCheckRouteIsTheMembersOwnCall: an agent's own token is 403 forbidden,
// no credentials 401, and a cookie from another site 403, each before the
// guard is asked and without counting against anybody; a cookie from an
// allowed origin, and a bearer token, are let in.
func TestTheCheckRouteIsTheMembersOwnCall(t *testing.T) {
	guard := &fakeAnswerChecker{}
	server, directory := checkServer(guard, nil)

	directory.membership.Agent = true
	if response := serve(server, http.MethodPost, checkPath, `{"texts":["Hello"]}`, bearer); response.Code != http.StatusForbidden || errorCode(t, response) != "forbidden" {
		t.Errorf("an agent's own token = %d %s, want 403 forbidden", response.Code, response.Body.String())
	}
	directory.membership.Agent = false
	if response := serve(server, http.MethodPost, checkPath, `{"texts":["Hello"]}`, nil); response.Code != http.StatusUnauthorized {
		t.Errorf("no credentials = %d, want 401", response.Code)
	}
	if response := serve(server, http.MethodPost, checkPath, `{"texts":["Hello"]}`, map[string]string{"Cookie": "cookie-token", "Origin": "https://evil.test"}); response.Code != http.StatusForbidden {
		t.Errorf("a cookie from another site = %d, want 403", response.Code)
	}
	if response := serve(server, http.MethodPost, checkPath, `{"texts":["Hello"]}`, map[string]string{"Cookie": "cookie-token"}); response.Code != http.StatusForbidden {
		t.Errorf("a cookie with no origin = %d, want 403", response.Code)
	}
	if guard.checks() != 0 || len(server.checks.members()) != 0 {
		t.Fatalf("a call nobody may make reached the guard (%d) or was counted (%v)", guard.checks(), server.checks.members())
	}
	if response := serve(server, http.MethodPost, checkPath, `{"texts":["Hello"]}`, map[string]string{"Cookie": "cookie-token", "Origin": "https://app.oryxa.test"}); response.Code != http.StatusOK {
		t.Errorf("a cookie from an allowed origin = %d, want 200", response.Code)
	}
	if response := serve(server, http.MethodPost, checkPath, `{"texts":["Hello"]}`, bearer); response.Code != http.StatusOK {
		t.Errorf("a bearer token = %d, want 200", response.Code)
	}
	if guard.checks() != 2 {
		t.Fatalf("the guard was asked %d times, want 2", guard.checks())
	}

	// A caller who is not in the organization is the directory's to refuse.
	directory.err = authclient.ErrForbidden
	if response := serve(server, http.MethodPost, checkPath, `{"texts":["Hello"]}`, bearer); response.Code != http.StatusForbidden || guard.checks() != 2 {
		t.Errorf("a caller who is not in the organization = %d, with the guard asked %d times, want 403 and the same 2", response.Code, guard.checks())
	}
	// And a server that cannot run agents does not read anything either.
	unavailable := New(nil, nil, Config{Unavailable: "not set up", AnswerGuard: guard}, nil)
	if response := serve(unavailable, http.MethodPost, checkPath, `{"texts":["Hello"]}`, bearer); response.Code != http.StatusServiceUnavailable {
		t.Errorf("an unavailable server = %d, want 503", response.Code)
	}
	if guard.checks() != 2 {
		t.Fatalf("the guard was asked %d times, want it still 2", guard.checks())
	}
}

// The answers the tests below are checked on: an email address alone, and an
// email address beside a password, which the guardrail masks.
const (
	contactAnswer = "Write to priya@example.com or call the office."
	secretAnswer  = "The password is hunter2; write to priya@example.com."
)

// checkOutcome is what a check answered, in the terms the tests compare.
type checkOutcome struct {
	state     string
	kinds     []string
	shareable []string
}

// checked posts a check of texts as origin, in the organization at path, and
// says what it answered.
func checked(t *testing.T, server *Server, path string, origin string, texts ...string) checkOutcome {
	t.Helper()
	response, got := postCheck(t, server, path, `{"texts":`+jsonTextsList(texts)+`,"origin":"`+origin+`"}`)
	if response.Code != http.StatusOK || !got.Checked {
		t.Fatalf("check of %v as %s = %d %s, want 200 and a check made", texts, origin, response.Code, response.Body.String())
	}
	return checkOutcome{state: got.State, kinds: got.Kinds, shareable: got.Shareable}
}

func (outcome checkOutcome) is(state string, kinds []string, shareable ...string) bool {
	return outcome.state == state && slices.Equal(outcome.kinds, kinds) && slices.Equal(outcome.shareable, shareable)
}

// TestAMemberWhoChoseStrictForTheirOwnAnswersHasEmailsHeldOnAnOwnerCheck: the
// same answer, checked as the member's own, is read at the level that applies
// to that member: strictly for one who chose it, with the email address held
// and shared as {EMAIL}; as it always was for one who chose nothing; and as
// loosely as secrets, for what another reads, for one who chose that.
func TestAMemberWhoChoseStrictForTheirOwnAnswersHasEmailsHeldOnAnOwnerCheck(t *testing.T) {
	settings := &fakeAnswerGuardPreferences{chosen: map[[2]string]guardpolicy.Choices{
		{"org_1", "user_1"}: {Owner: guardpolicy.LevelStrict},
		{"org_1", "user_3"}: {Peer: guardpolicy.LevelSecrets},
	}}
	server, directory := checkServerWith(realGuard(t), settings, nil)
	held := func(masked string) checkOutcome {
		return checkOutcome{"held", []string{"EMAIL"}, []string{"Fine.", masked}}
	}

	// user_1 chose strict for what they start themselves.
	if got := checked(t, server, checkPath, "owner", "Fine.", contactAnswer); !got.is("held", []string{"EMAIL"}, "Fine.", "Write to {EMAIL} or call the office.") {
		t.Errorf("an owner check for a member who chose strict = %+v, want the email held and masked: %+v", got, held("Write to {EMAIL} or call the office."))
	}
	if got := checked(t, server, checkPath, "owner", secretAnswer); !got.is("held", []string{"EMAIL"}, "The password is {PASSWORD}; write to {EMAIL}.") {
		t.Errorf("an owner check of a password and an email for that member = %+v, want both masked, the email held", got)
	}
	// What another reads is still strict for them, by default.
	if got := checked(t, server, checkPath, "peer", "Fine.", contactAnswer); !got.is("held", []string{"EMAIL"}, "Fine.", "Write to {EMAIL} or call the office.") {
		t.Errorf("a peer check for that member = %+v, want it strict, as by default", got)
	}

	// user_2 chose nothing: what they start themselves keeps its email address.
	directory.membership.UserID = "user_2"
	if got := checked(t, server, checkPath, "owner", "Fine.", contactAnswer); !got.is("clear", []string{}, "Fine.", contactAnswer) {
		t.Errorf("an owner check for a member who chose nothing = %+v, want it clear, as it always was", got)
	}
	if got := checked(t, server, checkPath, "owner", secretAnswer); !got.is("masked", []string{"PASSWORD"}, "The password is {PASSWORD}; write to priya@example.com.") {
		t.Errorf("an owner check of a password and an email for that member = %+v, want the password masked and the address intact", got)
	}

	// user_3 chose secrets for what others read: their email addresses go as written.
	directory.membership.UserID = "user_3"
	if got := checked(t, server, checkPath, "peer", "Fine.", contactAnswer); !got.is("clear", []string{}, "Fine.", contactAnswer) {
		t.Errorf("a peer check for a member who chose secrets = %+v, want it clear", got)
	}
	if got := checked(t, server, checkPath, "peer", secretAnswer); !got.is("masked", []string{"PASSWORD"}, "The password is {PASSWORD}; write to priya@example.com.") {
		t.Errorf("a peer check of a password and an email for that member = %+v, want the password masked and the address intact", got)
	}
	// ... and never the password, whatever they chose, nor an answer nobody could read.
	if got := checked(t, server, checkPath, "peer", "Fine.", "UNREADABLE "+secretAnswer); !got.is("held", []string{"UNCHECKED"}) || got.shareable != nil {
		t.Errorf("a peer check of what could not be read = %+v, want nothing shared", got)
	}
}

// TestAnOrganizationsMinimumOverridesAMembersChoiceOnACheck: a member who chose
// secrets, in an organization whose policy asks for at least strict, has
// strict; in an organization that asks nothing their own choice stands, so the
// minimum is the organization's and no other's; and lowering it lets the
// choice apply again.
func TestAnOrganizationsMinimumOverridesAMembersChoiceOnACheck(t *testing.T) {
	orgs := fakeOrgPolicies{"org_1": {MinimumOwner: guardpolicy.LevelStrict, MinimumPeer: guardpolicy.LevelStrict}}
	settings := &fakeAnswerGuardPreferences{
		orgs: orgs,
		chosen: map[[2]string]guardpolicy.Choices{
			{"org_1", "user_1"}: {Owner: guardpolicy.LevelSecrets, Peer: guardpolicy.LevelSecrets},
			{"org_2", "user_1"}: {Owner: guardpolicy.LevelSecrets, Peer: guardpolicy.LevelSecrets},
		},
	}
	server, _ := checkServerWith(realGuard(t), settings, nil)
	const otherPath = "/v1/organizations/org_2/answer-guard/check"
	masked := "Write to {EMAIL} or call the office."

	for _, origin := range []string{"owner", "peer"} {
		if got := checked(t, server, checkPath, origin, contactAnswer); !got.is("held", []string{"EMAIL"}, masked) {
			t.Errorf("a %s check in the organization whose minimum is strict = %+v, want the email held whatever the member chose", origin, got)
		}
		if got := checked(t, server, otherPath, origin, contactAnswer); !got.is("clear", []string{}, contactAnswer) {
			t.Errorf("a %s check in an organization with no minimum = %+v, want the member's own choice of secrets", origin, got)
		}
	}
	// An organization that raises only what others read leaves the member's own answers as they chose.
	orgs["org_1"] = guardpolicy.OrgPolicy{MinimumPeer: guardpolicy.LevelStrict}
	if got := checked(t, server, checkPath, "owner", contactAnswer); !got.is("clear", []string{}, contactAnswer) {
		t.Errorf("an owner check when only the peer minimum is strict = %+v, want the member's choice", got)
	}
	if got := checked(t, server, checkPath, "peer", contactAnswer); !got.is("held", []string{"EMAIL"}, masked) {
		t.Errorf("a peer check when the peer minimum is strict = %+v, want the email held", got)
	}
	// And one that lowers its minimum lets the choice apply again.
	delete(orgs, "org_1")
	if got := checked(t, server, checkPath, "peer", contactAnswer); !got.is("clear", []string{}, contactAnswer) {
		t.Errorf("a peer check once the minimum is gone = %+v, want the member's choice", got)
	}
}

// TestWithNoSettingsKeptACheckReadsAsItAlwaysHas: with no answer guard
// settings, or a settings service that was never built, the origin alone
// decides: the member's own answers keep their email addresses, and what
// another reads holds them. That is today's behaviour, and nothing is asked.
func TestWithNoSettingsKeptACheckReadsAsItAlwaysHas(t *testing.T) {
	var neverBuilt *fakeAnswerGuardPreferences
	var neverOpened *guardpolicy.Service
	for name, settings := range map[string]AnswerGuardPreferences{
		"none":                            nil,
		"a fake that was never built":     neverBuilt,
		"a service that was never opened": neverOpened,
	} {
		t.Run(name, func(t *testing.T) {
			server, _ := checkServerWith(realGuard(t), settings, nil)
			if got := checked(t, server, checkPath, "owner", "Fine.", contactAnswer); !got.is("clear", []string{}, "Fine.", contactAnswer) {
				t.Errorf("an owner check = %+v, want the email left as written", got)
			}
			if got := checked(t, server, checkPath, "peer", "Fine.", contactAnswer); !got.is("held", []string{"EMAIL"}, "Fine.", "Write to {EMAIL} or call the office.") {
				t.Errorf("a peer check = %+v, want the email held and masked", got)
			}
			response, got := postCheck(t, server, checkPath, `{"texts":`+jsonTextsList([]string{"Fine.", contactAnswer})+`}`)
			if outcome := (checkOutcome{got.State, got.Kinds, got.Shareable}); response.Code != http.StatusOK ||
				!outcome.is("held", []string{"EMAIL"}, "Fine.", "Write to {EMAIL} or call the office.") {
				t.Errorf("a check that names no origin = %d %s, want it read as a peer's", response.Code, response.Body.String())
			}
		})
	}
}

// unanswering is a settings service that tells a check what it is made to,
// and fails as it is made to: it is how a service that gives less than the
// strictest reading with its error, or a level nobody knows, is read.
type unanswering struct {
	effective guardpolicy.Effective
	err       error
}

func (unanswering) Preferences(context.Context, string, string) (guardpolicy.View, error) {
	return guardpolicy.View{}, nil
}

func (unanswering) SetPreferences(context.Context, string, string, *guardpolicy.Level, *guardpolicy.Level) (guardpolicy.View, error) {
	return guardpolicy.View{}, nil
}

func (settings unanswering) Effective(context.Context, string, string) (guardpolicy.Effective, error) {
	return settings.effective, settings.err
}

// TestACheckWhoseSettingsCannotBeReadIsReadStrictlyAndSaysSo: when a member's
// own levels cannot be read the check does not fail, and does not read looser:
// it reads strictly, whatever the member chose, and logs a warning that names
// who and which origin and why, and holds no text and no value. A service that
// answers nothing beside its error, or a level nobody knows, is read strictly
// too; and there is no level that reads nothing.
func TestACheckWhoseSettingsCannotBeReadIsReadStrictlyAndSaysSo(t *testing.T) {
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	masked := "Write to {EMAIL} or call the office."
	strictly := func(t *testing.T, server *Server, origin string) {
		t.Helper()
		if got := checked(t, server, checkPath, origin, contactAnswer); !got.is("held", []string{"EMAIL"}, masked) {
			t.Errorf("an %s check = %+v, want the email held and masked", origin, got)
		}
	}

	// A member who chose secrets for both, whose choice cannot be read.
	failing := &fakeAnswerGuardPreferences{
		err:    errors.New("database down"),
		chosen: map[[2]string]guardpolicy.Choices{{"org_1", "user_1"}: {Owner: guardpolicy.LevelSecrets, Peer: guardpolicy.LevelSecrets}},
	}
	server, _ := checkServerWith(realGuard(t), failing, logger)
	strictly(t, server, "owner")
	strictly(t, server, "peer")
	warned := 0
	for _, line := range strings.Split(strings.TrimSpace(logs.String()), "\n") {
		var record map[string]any
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatalf("log line %q: %v", line, err)
		}
		if record["level"] != "WARN" {
			continue
		}
		warned++
		if record["organization_id"] != "org_1" || record["user_id"] != "user_1" || !strings.Contains(record["error"].(string), "database down") ||
			(record["origin"] != "owner" && record["origin"] != "peer") {
			t.Errorf("warning = %v, want the organization, the member, the origin and why", record)
		}
	}
	if warned != 2 {
		t.Errorf("logged %d warnings over two checks, want one each:\n%s", warned, logs.String())
	}
	for _, secret := range []string{"priya@example.com", "hunter2", "Write to", "office", "The password"} {
		if strings.Contains(logs.String(), secret) {
			t.Errorf("the log holds %q:\n%s", secret, logs.String())
		}
	}

	// A service that fails with nothing to go on, or with a level nobody knows,
	// or with the level that would read nothing, is read strictly all the same.
	for name, settings := range map[string]unanswering{
		"nothing beside its error":       {err: errors.New("database down")},
		"a level nobody knows":           {effective: guardpolicy.Effective{Owner: "lenient", Peer: "lenient"}},
		"a level of off":                 {effective: guardpolicy.Effective{Owner: "off", Peer: "off"}},
		"no level and no error":          {},
		"levels in capitals":             {effective: guardpolicy.Effective{Owner: "SECRETS", Peer: "SECRETS"}},
		"a level of off beside an error": {effective: guardpolicy.Effective{Owner: "off", Peer: "off"}, err: errors.New("database down")},
	} {
		t.Run(name, func(t *testing.T) {
			server, _ := checkServerWith(realGuard(t), settings, nil)
			strictly(t, server, "owner")
			strictly(t, server, "peer")
		})
	}
	// A service that answers, without an error, is believed, and not warned
	// about: what is read strictly is only what could not be read.
	quiet := unanswering{effective: guardpolicy.Effective{Owner: guardpolicy.LevelSecrets, Peer: guardpolicy.LevelSecrets}}
	logs.Reset()
	server, _ = checkServerWith(realGuard(t), quiet, logger)
	if got := checked(t, server, checkPath, "peer", contactAnswer); !got.is("clear", []string{}, contactAnswer) {
		t.Errorf("a peer check at secrets = %+v, want it clear", got)
	}
	if strings.Contains(logs.String(), `"level":"WARN"`) {
		t.Errorf("a check whose settings were read warned:\n%s", logs.String())
	}
}

// TestACheckReadsTheCallersOwnSettingsAndNoOnesElse: the level a check is read
// at is the one for the member who asked, in the organization they asked in;
// nothing in the query, the headers or the body names whose settings they are.
func TestACheckReadsTheCallersOwnSettingsAndNoOnesElse(t *testing.T) {
	settings := &fakeAnswerGuardPreferences{}
	guard := &fakeAnswerChecker{}
	server, _ := checkServerWith(guard, settings, nil)
	smuggled := map[string]string{"Authorization": "Bearer member-token", "X-User-Id": "user_2", "X-Organization-Id": "org_2", "X-Forwarded-User": "user_2"}

	response := serve(server, http.MethodPost, checkPath+"?user_id=user_2&organization_id=org_2", `{"texts":["Hello"],"origin":"owner"}`, smuggled)
	if response.Code != http.StatusOK {
		t.Fatalf("check = %d %s", response.Code, response.Body.String())
	}
	for _, body := range []string{
		`{"texts":["Hello"],"user_id":"user_2"}`,
		`{"texts":["Hello"],"organization_id":"org_2"}`,
		`{"texts":["Hello"],"level":"secrets"}`,
		`{"texts":["Hello"],"effective":{"owner":"secrets"}}`,
	} {
		if response := serve(server, http.MethodPost, checkPath, body, bearer); response.Code != http.StatusBadRequest {
			t.Errorf("%s = %d, want 400", body, response.Code)
		}
	}
	if want := [][2]string{{"org_1", "user_1"}}; fmt.Sprint(settings.calls) != fmt.Sprint(want) || guard.checks() != 1 {
		t.Fatalf("settings were read for %v with the guard asked %d times, want %v and once", settings.calls, guard.checks(), want)
	}
	// The organization is the path's, which the caller was proved a member of.
	if response := serve(server, http.MethodPost, "/v1/organizations/org_9/answer-guard/check", `{"texts":["Hello"]}`, bearer); response.Code != http.StatusOK {
		t.Fatalf("check in org_9 = %d", response.Code)
	}
	if want := [][2]string{{"org_1", "user_1"}, {"org_9", "user_1"}}; fmt.Sprint(settings.calls) != fmt.Sprint(want) {
		t.Fatalf("settings were read for %v, want %v", settings.calls, want)
	}
}

// TestSettingsAreOnlyReadForACheckThatReadsAnAnswer: a check that is refused,
// that nobody may make, that is over the limit, or that has no guardrail to
// read with, costs no read of anybody's settings.
func TestSettingsAreOnlyReadForACheckThatReadsAnAnswer(t *testing.T) {
	settings := &fakeAnswerGuardPreferences{}

	// No guardrail: nothing is read, and so nothing is asked either.
	server, _ := checkServerWith(nil, settings, nil)
	for _, origin := range []string{"owner", "peer"} {
		if response, got := postCheck(t, server, checkPath, `{"texts":["Hello"],"origin":"`+origin+`"}`); response.Code != http.StatusOK || got.Checked {
			t.Fatalf("a check with no guardrail = %d %s, want it unchecked", response.Code, response.Body.String())
		}
	}
	if len(settings.calls) != 0 {
		t.Fatalf("settings were read %d times for checks that read nothing", len(settings.calls))
	}

	guard := &fakeAnswerChecker{}
	server, directory := checkServerWith(guard, settings, nil)
	now := time.Date(2026, 9, 29, 9, 0, 0, 0, time.UTC)
	server.checks.now = func() time.Time { return now }
	for _, refused := range []struct {
		name    string
		body    string
		headers map[string]string
		want    int
	}{
		{"a malformed check", `{"texts":[]}`, bearer, http.StatusBadRequest},
		{"an origin nobody knows", `{"texts":["Hello"],"origin":"member"}`, bearer, http.StatusBadRequest},
		{"no credentials", `{"texts":["Hello"]}`, nil, http.StatusUnauthorized},
		{"another site's cookie", `{"texts":["Hello"]}`, map[string]string{"Cookie": "cookie-token", "Origin": "https://evil.test"}, http.StatusForbidden},
	} {
		if response := serve(server, http.MethodPost, checkPath, refused.body, refused.headers); response.Code != refused.want {
			t.Errorf("%s = %d, want %d", refused.name, response.Code, refused.want)
		}
	}
	directory.membership.Agent = true
	if response := serve(server, http.MethodPost, checkPath, `{"texts":["Hello"]}`, bearer); response.Code != http.StatusForbidden {
		t.Errorf("an agent's token = %d, want 403", response.Code)
	}
	directory.membership.Agent = false
	if len(settings.calls) != 0 || guard.checks() != 0 {
		t.Fatalf("refused checks read settings %d times and asked the guard %d times, want neither", len(settings.calls), guard.checks())
	}

	// A member's thirty checks each read their settings once; the thirty-first
	// is refused before it does.
	for count := range checksPerWindow {
		if response := serve(server, http.MethodPost, checkPath, `{"texts":["Hello"]}`, bearer); response.Code != http.StatusOK {
			t.Fatalf("check %d = %d", count+1, response.Code)
		}
	}
	if response := serve(server, http.MethodPost, checkPath, `{"texts":["Hello"]}`, bearer); response.Code != http.StatusTooManyRequests {
		t.Fatalf("the thirty-first check = %d, want 429", response.Code)
	}
	if len(settings.calls) != checksPerWindow || guard.checks() != checksPerWindow {
		t.Fatalf("settings were read %d times and the guard asked %d, want %d of each", len(settings.calls), guard.checks(), checksPerWindow)
	}
}

// TestTheLevelsACheckIsReadAtAreTheGuardsOwn: guardpolicy and answerguard spell
// the two levels alike, and a check is only ever read at one of them: any other
// level, none at all, and the one that would read nothing, are strict.
func TestTheLevelsACheckIsReadAtAreTheGuardsOwn(t *testing.T) {
	if answerguard.Intensity(guardpolicy.LevelSecrets) != answerguard.IntensitySecrets || answerguard.Intensity(guardpolicy.LevelStrict) != answerguard.IntensityStrict {
		t.Fatalf("guardpolicy spells the levels %q and %q, answerguard %q and %q", guardpolicy.LevelSecrets, guardpolicy.LevelStrict, answerguard.IntensitySecrets, answerguard.IntensityStrict)
	}
	if guardpolicy.Origin(originOwner) != guardpolicy.OriginOwner || guardpolicy.Origin(originPeer) != guardpolicy.OriginPeer {
		t.Fatalf("the route spells the origins %q and %q, guardpolicy %q and %q", originOwner, originPeer, guardpolicy.OriginOwner, guardpolicy.OriginPeer)
	}
	for level, want := range map[guardpolicy.Level]answerguard.Intensity{
		guardpolicy.LevelSecrets: answerguard.IntensitySecrets,
		guardpolicy.LevelStrict:  answerguard.IntensityStrict,
		"":                       answerguard.IntensityStrict,
		"off":                    answerguard.IntensityStrict,
		"SECRETS":                answerguard.IntensityStrict,
		"everything":             answerguard.IntensityStrict,
	} {
		if got := intensityOf(level); got != want {
			t.Errorf("intensityOf(%q) = %q, want %q", level, got, want)
		}
	}
	// What a member who chose nothing gets is what the route always did.
	for origin, want := range map[string]answerguard.Intensity{originOwner: answerguard.IntensitySecrets, originPeer: answerguard.IntensityStrict} {
		if got := defaultCheckLevel(origin); got != want {
			t.Errorf("defaultCheckLevel(%q) = %q, want %q", origin, got, want)
		}
	}
}
