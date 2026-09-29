package httpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
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
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/managedagents"
)

const checkPath = "/v1/organizations/org_1/answer-guard/check"

// fakeAnswerChecker answers every check with what verdict makes of its texts,
// clear as given when it makes nothing, and keeps every check it was asked.
type fakeAnswerChecker struct {
	mu      sync.Mutex
	asked   [][]string
	verdict func(texts []string) answerguard.Verdict
}

func (fake *fakeAnswerChecker) Check(_ context.Context, texts []string) answerguard.Verdict {
	fake.mu.Lock()
	fake.asked = append(fake.asked, slices.Clone(texts))
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
// nothing when guard is nil.
func checkServer(guard AnswerChecker, logger *slog.Logger) (*Server, *fakeDirectory) {
	directory := member()
	config := Config{
		AllowedOrigins: []string{"https://app.oryxa.test"}, RunTimeout: time.Minute,
		Agents: &fakeManagedAgents{definitions: []managedagents.Definition{{ID: "agt_primary", Kind: managedagents.KindPrimary, Name: "Carl", Status: managedagents.StatusActive}}},
	}
	if guard != nil {
		config.AnswerGuard = guard
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
		want := `{"checked":false,"state":"clear","kinds":[],"shareable":["The password is hunter2.","write to priya@example.com"]}` + "\n"
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

// TestAnOriginChoosesHowStrictlyAnAnswerIsRead: an answer a peer will read is
// read strictly, the member's own with only the secrets taken out, and a
// origin nobody knows is read strictly too, so it can never be read looser.
func TestAnOriginChoosesHowStrictlyAnAnswerIsRead(t *testing.T) {
	for origin, want := range map[string]answerguard.Intensity{
		originOwner: answerguard.IntensitySecrets,
		originPeer:  answerguard.IntensityStrict,
		"":          answerguard.IntensityStrict,
		"Owner":     answerguard.IntensityStrict,
		"someone":   answerguard.IntensityStrict,
	} {
		if got := checkLevel(origin); got != want {
			t.Errorf("checkLevel(%q) = %q, want %q", origin, got, want)
		}
	}
}

// TestTheCheckRouteKeepsAndLogsNothingItWasAsked: what was asked is measured
// and dropped, never handed to the guard; and the log names what was found by
// kind, never by value, and holds no text and none of what was asked.
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
	for _, said := range []string{`"organization_id":"org_1"`, `"user_id":"user_1"`, `"origin":"owner"`, `"origin":"peer"`, `"UNCHECKED"`} {
		if !strings.Contains(logs.String(), said) {
			t.Errorf("the log does not say %s:\n%s", said, logs.String())
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
