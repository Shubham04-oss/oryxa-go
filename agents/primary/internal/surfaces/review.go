package surfaces

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/agents"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/answerguard"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/appsclient"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/consentguard"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/interactions"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/relevance"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/slackrouter"
)

// An answer the guards hold waits for its owner's review, and the thread is
// told only that it waits (tellHeld). The owner is told in their own direct
// message: who asked what, why the answer was held, the answer as it would be
// sent, and a button for each review they may make. The message is replaced
// with how the review ended, whichever surface it was made on, so its buttons
// go once they would do nothing.
//
// It is the one message that shows a held answer, and it shows it only to the
// member whose agent wrote it, who can read it in Oryxa already:
//
//   - where the guards made a masked version, the preview is that version, and
//     the answer as held is never shown, though Send sends it;
//   - for an ask of a secret there is no preview and no buttons: a secret is
//     never in Slack, so its owner is told to review it in Oryxa;
//   - apps scrubs what it shows of anything that looks like a credential, as it
//     does every text, and the message loses the preview when it is settled.
//
// A review that lapses is withheld by the ledger's sweep, which records the
// event that wakes this pass, so unlike a notice's, the review's buttons need no
// sweep of their own here: they are settled as expired by the pass that event
// wakes, or by any pass that reads the request after it lapsed.

// The templates apps renders for the owner's review.
const (
	templateHeldReview        = "held_review"
	templateHeldReviewSettled = "held_review_settled"
)

// heldReviewParams are the parameters of apps' held_review template. apps
// decodes them strictly, so these are exactly the fields it takes.
type heldReviewParams struct {
	AskerUserID string `json:"asker_user_id,omitempty"`
	AskerName   string `json:"asker_name,omitempty"`
	Question    string `json:"question"`
	// Reasons are why the answer was held, as the codes apps has words for.
	Reasons []string `json:"reasons"`
	// Preview is what the owner is shown of the answer: its masked version when
	// it has one, which Masked says, and otherwise the answer as it was held.
	// Both are left out for an ask of a secret, which Secret says, and which has
	// no buttons either.
	Preview string          `json:"preview,omitempty"`
	Masked  bool            `json:"masked,omitempty"`
	Secret  bool            `json:"secret,omitempty"`
	Tickets *reviewTicketed `json:"tickets,omitempty"`
}

// reviewTicketed are the values of the review's buttons. SendMasked is there
// only for an answer that has a masked version; Reply, for Reply yourself,
// always.
type reviewTicketed struct {
	Send       string `json:"send"`
	SendMasked string `json:"send_masked,omitempty"`
	Withhold   string `json:"withhold"`
	Reply      string `json:"reply,omitempty"`
}

// heldReviewSettledParams are the parameters of apps' held_review_settled
// template.
type heldReviewSettledParams struct {
	AskerUserID string     `json:"asker_user_id,omitempty"`
	AskerName   string     `json:"asker_name,omitempty"`
	Question    string     `json:"question"`
	Outcome     string     `json:"outcome"`
	DecidedAt   *time.Time `json:"decided_at,omitempty"`
	// Reply is what the owner replied instead of sending the held answer, as
	// it was delivered; left out for an ask of a secret.
	Reply string `json:"reply,omitempty"`
}

// The reasons a review says an answer was held for, as apps' held_review
// template names them, in the order it says them. A reason apps does not know it
// says as reasonOther.
const (
	reasonIrrelevant    = "irrelevant"
	reasonUngrounded    = "ungrounded"
	reasonNoSources     = "no_sources"
	reasonCopied        = "copied"
	reasonLink          = "link"
	reasonDeniedTopic   = "denied_topic"
	reasonEmail         = "email"
	reasonPhone         = "phone"
	reasonAddress       = "address"
	reasonOffTopic      = "off_topic"
	reasonPersonalLife  = "personal_life"
	reasonHealth        = "health"
	reasonHR            = "hr"
	reasonCompensation  = "compensation"
	reasonOthersPrivate = "others_private"
	reasonCredentials   = "credentials"
	reasonUnchecked     = "unchecked"
	// reasonPolicy is an answer no guard held: the organization holds every
	// answer another member reads for its owner's review, or its settings could
	// not be read, which holds it all the same.
	reasonPolicy = "policy"
	// reasonOther is a kind this service has no reason for.
	reasonOther = "other"
)

// reasonOrder is the order a review says its reasons in, whatever order the
// guards found them in.
var reasonOrder = []string{
	reasonIrrelevant, reasonUngrounded, reasonNoSources, reasonCopied, reasonLink, reasonDeniedTopic,
	reasonEmail, reasonPhone, reasonAddress,
	reasonOffTopic, reasonPersonalLife, reasonHealth, reasonHR, reasonCompensation, reasonOthersPrivate, reasonCredentials,
	reasonUnchecked, reasonPolicy, reasonOther,
}

// heldKindReasons is the reason each kind a guard holds an answer for is said
// as: the consent guard's own, the answer guard's personal details, and its
// relevance check's, with the categories of what it found.
var heldKindReasons = map[string]string{
	consentguard.KindIrrelevant:     reasonIrrelevant,
	consentguard.KindUngrounded:     reasonUngrounded,
	consentguard.KindNoSources:      reasonNoSources,
	consentguard.KindCopied:         reasonCopied,
	consentguard.KindLink:           reasonLink,
	consentguard.KindDeniedTopic:    reasonDeniedTopic,
	consentguard.KindUnchecked:      reasonUnchecked,
	"EMAIL":                         reasonEmail,
	"PHONE":                         reasonPhone,
	"ADDRESS":                       reasonAddress,
	answerguard.OffTopic:            reasonOffTopic,
	string(relevance.PersonalLife):  reasonPersonalLife,
	string(relevance.Health):        reasonHealth,
	string(relevance.HR):            reasonHR,
	string(relevance.Compensation):  reasonCompensation,
	string(relevance.OthersPrivate): reasonOthersPrivate,
	string(relevance.Credentials):   reasonCredentials,
}

// categoryReasons are the reasons that name what the relevance check found an
// answer carried, which say more than that it carried something.
var categoryReasons = map[string]bool{
	reasonPersonalLife: true, reasonHealth: true, reasonHR: true, reasonCompensation: true,
	reasonOthersPrivate: true, reasonCredentials: true,
}

// maskedKinds are the kinds the guards mask wherever an answer goes: they are
// in a guard's kinds when they were found, and hold nothing, so they are no
// reason an answer was held.
var maskedKinds = func() map[string]bool {
	kinds := map[string]bool{}
	for _, kind := range answerguard.MaskedKinds() {
		kinds[kind] = true
	}
	for _, kind := range []string{
		consentguard.KindSlackToken, consentguard.KindAWSAccessKey, consentguard.KindAWSSecretKey, consentguard.KindAPIKey,
		consentguard.KindAccessToken, consentguard.KindPrivateKey, consentguard.KindPassword,
	} {
		kinds[kind] = true
	}
	return kinds
}()

// heldReasons are why a request's answer was held, as a review says them: from
// the kinds the guards recorded, never from a value, which a guard never
// records. An answer no guard held was held by its organization's settings.
func heldReasons(request agents.Interaction) []string {
	guard := request.Guard
	if guard == nil || guard.State != answerguard.StateHeld {
		return []string{reasonPolicy}
	}
	found := map[string]bool{}
	for _, kind := range guard.Kinds {
		switch reason, known := heldKindReasons[kind]; {
		case known:
			found[reason] = true
		case !maskedKinds[kind]:
			found[reasonOther] = true
		}
	}
	for reason := range found {
		if categoryReasons[reason] {
			// What it carried is said; that it carried something is then said.
			delete(found, reasonOffTopic)
			break
		}
	}
	reasons := make([]string, 0, len(found))
	for _, reason := range reasonOrder {
		if found[reason] {
			reasons = append(reasons, reason)
		}
	}
	if len(reasons) == 0 {
		// Held, for nothing this service can name: it says so, generally.
		reasons = append(reasons, reasonOther)
	}
	return reasons
}

// heldPreview is what the owner is shown of the answer held for them: its
// masked version when the guards made one, which is then all that is shown of
// it, and otherwise the answer as it was held, as the guards left it. It is the
// words the thread would read (answerText).
func heldPreview(held *agents.InteractionHeld) (preview string, masked bool) {
	switch {
	case held == nil:
		return noAnswerText, false
	case held.MaskedResult != nil:
		return answerText(held.MaskedResult), true
	}
	return answerText(&held.Result), false
}

// reviewState is what the owner's review should say now: statePending while
// the answer waits for them and its request has not lapsed, otherwise how the
// review ended. It is empty for a request whose answer was never held.
func (p *pass) reviewState() string {
	request := p.request
	if request.Status == interactions.StatusReview {
		if !request.ExpiresAt.After(p.now) {
			// The ledger withholds it on its next sweep, and no one may send it.
			return reviewExpired
		}
		return statePending
	}
	review := request.Review
	if review == nil {
		return ""
	}
	switch review.DecidedBy.Kind {
	case interactions.DecidedByMember:
		if review.Action == interactions.ReviewReply {
			// The owner answered in their own words instead, on either surface.
			return reviewReplied
		}
		if review.DecidedBy.Surface != interactions.SurfaceSlack {
			return reviewDecidedElsewhere
		}
		switch review.Action {
		case interactions.ReviewSend:
			return reviewSent
		case interactions.ReviewSendMasked:
			return reviewSentMasked
		}
		return reviewNotSent
	case interactions.DecidedBySystem:
		switch {
		case review.DecidedBy.Reason == interactions.ReasonAnsweredElsewhere:
			return reviewAnsweredByOther
		case review.Reason == interactions.ExpiredBeforeReview:
			return reviewExpired
		}
		return reviewNotSent
	}
	return reviewDecidedElsewhere
}

// review keeps the owner's review as the ledger says it is: sent to them while
// their answer waits in review, and replaced with how the review ended once it
// has.
func (p *pass) review(ctx context.Context) error {
	link, linked, err := p.said(ctx, roleReview)
	if err != nil {
		return err
	}
	if linked && link.State == stateFailed {
		return nil
	}
	switch state := p.reviewState(); {
	case state == "":
		return nil
	case state == statePending:
		if linked {
			return nil
		}
		return p.sendReview(ctx)
	default:
		// Nothing was sent to replace when the review ended before it was sent.
		if !linked || link.State == state {
			return nil
		}
		return p.settleReview(ctx, link, state)
	}
}

// sendReview sends the owner their review: who asked what, why the answer was
// held, the answer as it would be sent, and a button for each review, whose
// tickets the ledger makes for it.
func (p *pass) sendReview(ctx context.Context) error {
	request := p.request
	owner, err := p.member(ctx, request.Target.UserID)
	if err != nil {
		return err
	}
	if owner.slackUserID == "" {
		// They can still review it in Oryxa.
		return p.giveUp(ctx, roleReview, errors.New("the owner is not matched to a Slack user"))
	}
	askerUser, askerWord, err := p.askerParams(ctx)
	if err != nil {
		return err
	}
	params := heldReviewParams{AskerUserID: askerUser, AskerName: askerWord, Question: request.Task, Reasons: heldReasons(request)}
	if p.ask.Kind == interactions.AskSecret {
		// A secret is never in Slack, whatever the answer holds: the owner is told
		// it waits for them, and why, and reviews it in Oryxa.
		params.Secret = true
	} else {
		tickets, err := p.service.deps.Tickets.ReviewTickets(ctx, request.ID, reviewID(request.ID))
		switch {
		case errors.Is(err, interactions.ErrConflict), errors.Is(err, interactions.ErrExpired):
			// Reviewed, or lapsed, since this pass read the request. There are no
			// buttons to send, and the pass that follows says how it ended.
			return nil
		case errors.Is(err, interactions.ErrNotFound), errors.Is(err, interactions.ErrInvalid), errors.Is(err, interactions.ErrConsentUnavailable):
			return p.giveUp(ctx, roleReview, fmt.Errorf("no tickets can be made for this review: %w", err))
		case err != nil:
			return fmt.Errorf("make the review's tickets: %w", err)
		}
		params.Preview, params.Masked = heldPreview(request.Held)
		params.Tickets = &reviewTicketed{Send: tickets.Send, SendMasked: tickets.SendMasked, Withhold: tickets.Withhold, Reply: tickets.Reply}
		if params.Masked != (tickets.SendMasked != "") {
			// Both read the one held answer, which does not change while it is in
			// review. A message that offered what the ledger refuses, or showed the
			// answer unmasked beside a masked button, is worse than none: the owner
			// can still review it in Oryxa.
			return p.giveUp(ctx, roleReview, errors.New("the review's tickets and the held answer disagree on its masked version"))
		}
	}
	posted, sent, err := p.send(ctx, roleReview, slackrouter.Post{
		Target: directTarget(owner.slackUserID), Template: templateHeldReview, Params: params,
	})
	if err != nil || !sent {
		return err
	}
	return p.putLink(ctx, roleReview, posted.ChannelID, posted.TS, statePending, nil)
}

// settleReview replaces the owner's review with how it ended, which takes its
// buttons, and the answer it showed, away, whichever surface the owner reviewed
// on.
func (p *pass) settleReview(ctx context.Context, link Link, state string) error {
	request := p.request
	askerUser, askerWord, err := p.askerParams(ctx)
	if err != nil {
		return err
	}
	params := heldReviewSettledParams{AskerUserID: askerUser, AskerName: askerWord, Question: request.Task, Outcome: state}
	if review := request.Review; review != nil && review.DecidedBy.Kind == interactions.DecidedByMember && !review.DecidedAt.IsZero() {
		at := review.DecidedAt.UTC()
		params.DecidedAt = &at
	}
	if state == reviewReplied {
		// What the owner replied instead, as it was delivered. The held answer
		// it replaced is gone, and the settled review never shows it.
		params.Reply = p.replyShown()
	}
	err = p.service.deps.Slack.Update(ctx, appsclient.Update{
		OrganizationID: request.OrganizationID, ChannelID: link.ChannelID, TS: link.TS,
		Template: templateHeldReviewSettled, Params: params,
	})
	switch {
	case err == nil:
	case slackrouter.IsPermanent(err) || errors.Is(err, slackrouter.ErrGone):
		// As for a notice: the message cannot be replaced, and nothing more can
		// be done for it.
		p.service.logger.WarnContext(ctx, "the owner's review could not be replaced and will not be tried again",
			"request_id", request.ID, "organization_id", request.OrganizationID, "state", state, "reason", err)
	default:
		return fmt.Errorf("replace the owner's review: %w", err)
	}
	return p.service.links.SetLinkState(ctx, request.ID, roleReview, state)
}
