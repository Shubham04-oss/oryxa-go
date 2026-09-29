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
)

// AnswerChecker reads texts for personal data before somebody reads them.
// *answerguard.Guard implements it; it never fails, holding what it cannot
// check.
type AnswerChecker interface {
	Check(ctx context.Context, texts []string) answerguard.Verdict
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
	// checksPerWindow is how many checks a member may make in checkWindow.
	checksPerWindow = 30
	checkWindow     = time.Minute
	// maxCheckBodyBytes is the largest request body: room for the most texts of
	// the most characters, at up to four bytes each, and a request as long, in
	// place of the 256 KiB every other route reads. A body past it is cut off,
	// and answered as the malformed JSON it has become.
	maxCheckBodyBytes = (maxCheckTexts+1)*maxCheckTextRunes*4 + 64<<10
)

// The origins a check names: whose answer it is that is to be read.
const (
	// originPeer is an answer another member will read, and what is taken
	// out of it is as strict as the guard is. It is what a check that names no
	// origin is.
	originPeer = "peer"
	// originOwner is an answer the member started themselves and reads
	// themselves. They decided that their own personal data is not masked in
	// it, so only the kinds that are always masked still are.
	originOwner = "owner"
)

// checkLevel is how much of what the guard finds is acted on before an answer
// of this origin is shared. It is a choice of intensity, made here and nowhere
// else, so that when the owner gets a setting for it the level changes and the
// route does not.
func checkLevel(origin string) answerguard.Intensity {
	if origin == originOwner {
		return answerguard.IntensitySecrets
	}
	return answerguard.IntensityStrict
}

// answerCheckRequest is what a check is asked. Request is what was asked, to
// which the texts are an answer; it is only measured. It is reserved for a
// relevance check that is still to come, and is neither kept, read by the
// guard nor logged.
type answerCheckRequest struct {
	Texts   []string `json:"texts"`
	Origin  *string  `json:"origin"`
	Request string   `json:"request"`
}

// answerCheckResult is what a check answers. Shareable is a text for each
// one asked about, or null when none may be shared.
type answerCheckResult struct {
	Checked   bool     `json:"checked"`
	State     string   `json:"state"`
	Kinds     []string `json:"kinds"`
	Shareable []string `json:"shareable"`
}

// checkAnswer reads texts an agent's answer is made of, for the side
// conversation about to show them to a member, and says what may be shared of
// them:
//
//	POST /v1/organizations/{org}/answer-guard/check
//	{"texts": ["..."], "origin": "owner" | "peer", "request": "..."}
//
// It is the member's own call, as every route that changes or spends
// something is: a cookie needs an allowed origin, an agent's token is refused,
// and the caller must be a member of the organization. A check is 1 to 8
// texts, each at most 25,000 characters (else 400), and a member may make 30 a
// minute (else 429 rate_limited, with Retry-After). origin, when sent, is
// exactly "owner" or "peer" (else 400); peer, the default, is the guard's full
// rule, and owner, which is an answer the member started themselves, masks
// only what is always masked (checkLevel).
//
// With no guardrail configured nothing is checked, and it says so
// (checked: false) and shares the texts as given. Otherwise the state is what
// was acted on at the origin's level, kinds are its kinds, and shareable is a
// text for each one asked about, or null when none may be shared (an answer
// the guard could not read is kinds ["UNCHECKED"]).
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
	}
	for _, text := range body.Texts {
		if utf8.RuneCountInString(text) > maxCheckTextRunes {
			writeError(writer, http.StatusBadRequest, "invalid_request", "each text holds at most "+strconv.Itoa(maxCheckTextRunes)+" characters")
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
	outcome := server.answerGuard.Check(request.Context(), body.Texts).At(len(body.Texts), checkLevel(origin))
	if outcome.State != answerguard.StateClear || outcome.Shareable == nil {
		// What was found is logged by kind, never by value; neither a text nor
		// what was asked is ever logged.
		server.logger.InfoContext(request.Context(), "the answer guard read texts a member asked it to check",
			"organization_id", member.OrganizationID, "user_id", member.UserID, "origin", origin,
			"texts", len(body.Texts), "state", outcome.State, "kinds", outcome.Kinds, "shareable", outcome.Shareable != nil)
	}
	writeJSON(writer, http.StatusOK, answerCheckResult{Checked: true, State: outcome.State, Kinds: outcome.Kinds, Shareable: outcome.Shareable})
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
