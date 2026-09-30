package httpserver

import (
	"context"
	"net/http"
	"slices"
	"strconv"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/answerguard"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/guardpolicy"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/threads"
)

// AnswerChecker reads texts for personal data before somebody reads them, and,
// when it is given a *answerguard.RelevanceInput, for whether they carry
// private material their request did not ask for. *answerguard.Guard
// implements it; it never fails, holding what it cannot check.
type AnswerChecker interface {
	CheckWith(ctx context.Context, texts []string, relevance *answerguard.RelevanceInput) answerguard.Verdict
}

// What a check accepts. The web app's side conversation asks it about an
// answer before showing it, so a check is a few texts, each an answer's own
// length at most; and, since every check is a call to the guardrail that costs
// money, a member may make only so many.
const (
	// maxCheckTexts is how many texts one check reads.
	maxCheckTexts = 8
	// maxCheckTextRunes is the longest one of them, and the longest request
	// it may carry, in characters.
	maxCheckTextRunes = 25_000
	// maxCheckSources is how many names of tools the run called a check may
	// carry, and maxCheckSourceRunes the longest of them, in characters.
	maxCheckSources     = 64
	maxCheckSourceRunes = 128
	// maxCheckContextLines is how many lines of the conversation around the
	// request a check may carry, and maxCheckContextLineRunes the longest of
	// them, in characters: what the relevance judge reads of the context.
	maxCheckContextLines     = 6
	maxCheckContextLineRunes = 1_000
	// checksPerWindow is how many checks a member may make in checkWindow.
	checksPerWindow = 30
	checkWindow     = time.Minute
	// maxCheckBodyBytes is the largest request body: room for the most texts of
	// the most characters, at up to four bytes each, a request as long, the most
	// sources and context lines of the most characters, and 64 KiB for the rest,
	// in place of the 256 KiB every other route reads. A body past it is cut
	// off, and answered as the malformed JSON it has become.
	maxCheckBodyBytes = (maxCheckTexts+1)*maxCheckTextRunes*4 +
		maxCheckSources*maxCheckSourceRunes*4 + maxCheckContextLines*maxCheckContextLineRunes*4 + 64<<10
)

// The origins a check names: whose answer it is that is to be read.
const (
	// originPeer is an answer another member will read, and what is taken
	// out of it is, by default, as strict as the guard is. It is what a check
	// that names no origin is.
	originPeer = "peer"
	// originOwner is an answer the member started themselves and reads
	// themselves. By default their own personal data is not masked in it, so
	// only the kinds that are always masked still are; a member may choose to
	// have it read as strictly as another's.
	originOwner = "owner"
)

// defaultCheckLevel is how much of what the guard finds is acted on before an
// answer of this origin is shared when no member's settings are kept: what a
// member who chose nothing gets, secrets for an answer they started themselves
// and strict for one another member reads. An origin nobody knows is read
// strictly, so it can never be read looser.
func defaultCheckLevel(origin string) answerguard.Intensity {
	return intensityOf(guardpolicy.DefaultLevel(guardpolicy.Origin(origin)))
}

// intensityOf is the intensity that reads at a member's level. It is only ever
// secrets or strict: a level nobody knows, or none, is strict, and there is no
// level that reads nothing, so an answer is never shared with the kinds that
// are always masked left in it.
func intensityOf(level guardpolicy.Level) answerguard.Intensity {
	if level == guardpolicy.LevelSecrets {
		return answerguard.IntensitySecrets
	}
	return answerguard.IntensityStrict
}

// relevanceModeOf is the mode the guard is asked to check relevance in when a
// member's settings say mode. It is only ever hold, shadow or off: a mode
// nobody knows, or none, is hold, so a check never does less than the settings
// could have asked of it, as intensityOf never reads looser.
func relevanceModeOf(mode guardpolicy.RelevanceMode) string {
	switch mode {
	case guardpolicy.RelevanceOff:
		return answerguard.RelevanceOff
	case guardpolicy.RelevanceShadow:
		return answerguard.RelevanceShadow
	default:
		return answerguard.RelevanceHold
	}
}

// checkSettings is what applies to one check, from its caller's settings.
type checkSettings struct {
	// intensity is how much of what the guard finds is acted on before an
	// answer is shared.
	intensity answerguard.Intensity
	// relevance is what the relevance check does with what its judge finds:
	// answerguard.RelevanceHold, RelevanceShadow or RelevanceOff.
	relevance string
	// regulated is whether the caller's organization runs the regulated preset.
	regulated bool
}

// checkSettingsFor is what applies to a check of an answer of this origin, for
// this member. The intensity is how much of what the guard finds is acted on
// before an answer of this origin is shared: the level that applies to them,
// which is what they chose, or the default, and never less than their
// organization's minimum asks (guardpolicy.Effective). It is a choice of
// intensity, made here and nowhere else, so that the route does not change
// when a level does. The relevance mode and whether the organization is
// regulated are read from the same Effective: one read of the settings serves
// the whole check.
//
// Where no settings are kept the intensity is the default, as it always was,
// and the relevance check is off: nothing says how an organization wants an
// off-topic answer treated. Where the member's own levels cannot be read,
// Effective answers the strictest reading with its error: the check goes on
// with that, and the log says so, by who and which origin and why, never with
// a text.
func (server *Server) checkSettingsFor(ctx context.Context, member threads.Member, origin string) checkSettings {
	if server.guardPreferences == nil {
		return checkSettings{intensity: defaultCheckLevel(origin), relevance: answerguard.RelevanceOff}
	}
	effective, err := server.guardPreferences.Effective(ctx, member.OrganizationID, member.UserID)
	if err != nil {
		server.logger.WarnContext(ctx, "an answer check could not read the member's answer guard settings, so it reads as strictly as it can",
			"organization_id", member.OrganizationID, "user_id", member.UserID, "origin", origin, "error", err)
	}
	return checkSettings{
		intensity: intensityOf(effective.For(guardpolicy.Origin(origin))),
		relevance: relevanceModeOf(effective.Relevance),
		regulated: effective.Regulated,
	}
}

// answerCheckRequest is what a check is asked. Request is what was asked, to
// which the texts are an answer; Sources are the names of the tools the run
// that gave it called, and Context up to six lines of the conversation around
// the request. The relevance check reads all three when the member's settings
// say to run it, and none of them is kept, sent to the guardrail or logged.
type answerCheckRequest struct {
	Texts   []string `json:"texts"`
	Origin  *string  `json:"origin"`
	Request string   `json:"request"`
	Sources []string `json:"sources"`
	Context []string `json:"context"`
}

// answerCheckResult is what a check answers. Shareable is a text for each
// one asked about, or null when none may be shared.
type answerCheckResult struct {
	Checked   bool                 `json:"checked"`
	State     string               `json:"state"`
	Kinds     []string             `json:"kinds"`
	Shareable []string             `json:"shareable"`
	Relevance answerCheckRelevance `json:"relevance"`
}

// answerCheckRelevance is what the relevance check made of the answer: whether
// a judge read it, whether it would hold it, and whether it did. It is
// decisions alone: the categories of what an answer carried that was not asked
// for are named in kinds when it is held, and nothing else of the judge's is
// ever sent.
type answerCheckRelevance struct {
	Judged    bool `json:"judged"`
	WouldHold bool `json:"would_hold"`
	Held      bool `json:"held"`
}

// relevanceResult is the outcome as the route answers it: nothing judged, as
// far as the answer says, when the guard reported none.
func relevanceResult(outcome *answerguard.RelevanceOutcome) answerCheckRelevance {
	if outcome == nil {
		return answerCheckRelevance{}
	}
	return answerCheckRelevance{Judged: outcome.Judged, WouldHold: outcome.WouldHold, Held: outcome.Held}
}

// checkAnswer reads texts an agent's answer is made of, for the side
// conversation about to show them to a member, and says what may be shared of
// them:
//
//	POST /v1/organizations/{org}/answer-guard/check
//	{"texts": ["..."], "origin": "owner" | "peer", "request": "...",
//	 "sources": ["tool", ...], "context": ["line", ...]}
//
// It is the member's own call, as every route that changes or spends
// something is: a cookie needs an allowed origin, an agent's token is refused,
// and the caller must be a member of the organization. A check is 1 to 8
// texts, each at most 25,000 characters (else 400), and a member may make 30 a
// minute (else 429 rate_limited, with Retry-After). origin, when sent, is
// exactly "owner" or "peer" (else 400). sources, when sent, are at most 64
// names of at most 128 characters, and context at most 6 lines of at most
// 1,000 (else 400). How much is taken out follows the member's own settings for
// that origin (checkSettingsFor): by default peer, which is what a check that
// names no origin is, is the guard's full rule, and owner, an answer the member
// started themselves, masks only what is always masked.
//
// The relevance check runs as the member's settings say (hold, shadow or off;
// off where none are kept), for an answer whose sources may have read something
// private: none sent means nothing is judged. An answer it holds is state
// "held", kinds with "OFF_TOPIC" and the categories it carried, and shareable
// null, whatever intensity the answer is read at; in shadow the answer is as
// the read left it, and the relevance block says what would have been held.
//
// With no guardrail configured nothing is checked, and it says so
// (checked: false) and shares the texts as given. Otherwise the state is what
// was acted on at the origin's level, kinds are its kinds, and shareable is a
// text for each one asked about, or null when none may be shared (an answer
// the guard could not read is kinds ["UNCHECKED"]). Either way the answer
// carries relevance: {judged, would_hold, held}.
func (server *Server) checkAnswer(writer http.ResponseWriter, request *http.Request) {
	member, ok := server.member(writer, request)
	if !ok {
		return
	}
	var body answerCheckRequest
	if !server.decodeUpTo(writer, request, &body, maxCheckBodyBytes) {
		return
	}
	origin := originPeer
	if body.Origin != nil {
		origin = *body.Origin
	}
	switch {
	case origin != originPeer && origin != originOwner:
		writeError(writer, http.StatusBadRequest, "invalid_request", `origin must be "owner" or "peer"`)
		return
	case len(body.Texts) < 1 || len(body.Texts) > maxCheckTexts:
		writeError(writer, http.StatusBadRequest, "invalid_request", "texts must hold from 1 to "+strconv.Itoa(maxCheckTexts)+" texts")
		return
	case utf8.RuneCountInString(body.Request) > maxCheckTextRunes:
		writeError(writer, http.StatusBadRequest, "invalid_request", "request holds at most "+strconv.Itoa(maxCheckTextRunes)+" characters")
		return
	case len(body.Sources) > maxCheckSources:
		writeError(writer, http.StatusBadRequest, "invalid_request", "sources holds at most "+strconv.Itoa(maxCheckSources)+" names")
		return
	case len(body.Context) > maxCheckContextLines:
		writeError(writer, http.StatusBadRequest, "invalid_request", "context holds at most "+strconv.Itoa(maxCheckContextLines)+" lines")
		return
	}
	for _, text := range body.Texts {
		if utf8.RuneCountInString(text) > maxCheckTextRunes {
			writeError(writer, http.StatusBadRequest, "invalid_request", "each text holds at most "+strconv.Itoa(maxCheckTextRunes)+" characters")
			return
		}
	}
	for _, name := range body.Sources {
		if utf8.RuneCountInString(name) > maxCheckSourceRunes {
			writeError(writer, http.StatusBadRequest, "invalid_request", "each source holds at most "+strconv.Itoa(maxCheckSourceRunes)+" characters")
			return
		}
	}
	for _, line := range body.Context {
		if utf8.RuneCountInString(line) > maxCheckContextLineRunes {
			writeError(writer, http.StatusBadRequest, "invalid_request", "each context line holds at most "+strconv.Itoa(maxCheckContextLineRunes)+" characters")
			return
		}
	}

	// Only a check that will be made counts against the limit: one that was
	// malformed cost nothing. Whether or not a guardrail is configured, so a
	// development server without one holds a client to what production will.
	if allowed, retryAfter := server.checks.allow(checkKey{organizationID: member.OrganizationID, userID: member.UserID}); !allowed {
		seconds := int((retryAfter + time.Second - 1) / time.Second)
		writer.Header().Set("Retry-After", strconv.Itoa(max(seconds, 1)))
		writeError(writer, http.StatusTooManyRequests, "rate_limited", "too many answer checks; try again shortly")
		return
	}

	if server.answerGuard == nil {
		writeJSON(writer, http.StatusOK, answerCheckResult{
			Checked: false, State: answerguard.StateClear, Kinds: []string{}, Shareable: slices.Clone(body.Texts),
		})
		return
	}
	settings := server.checkSettingsFor(request.Context(), member, origin)
	verdict := server.answerGuard.CheckWith(request.Context(), body.Texts, &answerguard.RelevanceInput{
		Request: body.Request, Context: body.Context, Sources: body.Sources,
		Regulated: settings.regulated, Mode: settings.relevance,
		RequireCompleteInput: true,
		OrganizationID:       member.OrganizationID, UserID: member.UserID,
	})
	outcome := verdict.At(len(body.Texts), settings.intensity)
	if outcome.State != answerguard.StateClear || outcome.Shareable == nil {
		// What was found is logged by kind, never by value; neither a text nor
		// what was asked is ever logged. The intensity is not called level: a
		// record has one of those already, and a second key of the name would
		// hide its severity from whatever reads the line.
		server.logger.InfoContext(request.Context(), "the answer guard read texts a member asked it to check",
			"organization_id", member.OrganizationID, "user_id", member.UserID, "origin", origin, "intensity", settings.intensity,
			"texts", len(body.Texts), "state", outcome.State, "kinds", outcome.Kinds, "shareable", outcome.Shareable != nil)
	}
	writeJSON(writer, http.StatusOK, answerCheckResult{
		Checked: true, State: outcome.State, Kinds: outcome.Kinds, Shareable: outcome.Shareable, Relevance: relevanceResult(verdict.Relevance),
	})
}

// checkKey names a member: a person in one organization.
type checkKey struct{ organizationID, userID string }

// checkLimiter lets each member make a number of checks in any window of a
// given length, in this process. It keeps, for each member, when their checks
// in the last window were made, so no window holds more than the limit, and a
// member held to it is let in again as their oldest check leaves it.
type checkLimiter struct {
	mu     sync.Mutex
	limit  int
	window time.Duration
	// now is the clock, which tests set.
	now  func() time.Time
	hits map[checkKey][]time.Time
	// swept is when members with no check left in the window were last
	// forgotten, which is once a window.
	swept time.Time
}

func newCheckLimiter(limit int, window time.Duration) *checkLimiter {
	return &checkLimiter{limit: limit, window: window, now: time.Now, hits: map[checkKey][]time.Time{}}
}

// allow records a check by key and says whether it is within the limit. When
// it is not, nothing is recorded, so a client held to the limit does not make
// its own wait longer by trying, and retryAfter is how long until it may.
func (limiter *checkLimiter) allow(key checkKey) (allowed bool, retryAfter time.Duration) {
	limiter.mu.Lock()
	defer limiter.mu.Unlock()
	now := limiter.now()
	limiter.forgetIdleMembers(now)
	recent := limiter.hits[key]
	cutoff := now.Add(-limiter.window)
	first := 0
	for first < len(recent) && !recent[first].After(cutoff) {
		first++
	}
	recent = recent[first:]
	if len(recent) >= limiter.limit {
		limiter.hits[key] = recent
		return false, recent[0].Add(limiter.window).Sub(now)
	}
	limiter.hits[key] = append(recent, now)
	return true, 0
}

// forgetIdleMembers drops the members whose latest check has left the window,
// at most once a window, so the limiter holds only members who checked lately.
func (limiter *checkLimiter) forgetIdleMembers(now time.Time) {
	if now.Sub(limiter.swept) < limiter.window {
		return
	}
	limiter.swept = now
	cutoff := now.Add(-limiter.window)
	for key, hits := range limiter.hits {
		if len(hits) == 0 || !hits[len(hits)-1].After(cutoff) {
			delete(limiter.hits, key)
		}
	}
}
