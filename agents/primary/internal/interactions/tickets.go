package interactions

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

// Each button Oryxa puts in front of an owner in Slack carries a ticket: proof
// that Oryxa made that button, for that owner, for that request and that
// choice. A click decides only with a valid ticket, so a leaked Slack signing
// secret, which lets someone forge a click, decides nothing: it cannot make a
// ticket.
//
//	t1.<base64url json {"r":request,"o":owner,"n":notice,"c":choice,"k":kind,"exp":unix}>.<base64url HMAC-SHA256>
//
// The HMAC is over everything before its own dot, under a key only agents
// holds (AGENTS_PRIMARY_SLACK_TICKET_KEY). A ticket is short enough to be a
// Slack button's value, and is no secret: the buttons of a notice carry
// three, and anyone who sees the notice sees them.
//
// A ticket is of one of two kinds, signed with the rest. A consent ticket is on
// a button of the owner's notice and decides the request (Consent); it has no
// "k", as no consent ticket ever had. A review ticket, "k":"review", is on a
// button of the owner's review of an answer the guards held, and sends it or
// keeps it back (ReviewFromSlack). The choices of the two kinds are apart as
// well, so neither kind is ever read as the other: a consent ticket reviews
// nothing, and a review ticket decides nothing.

var (
	// ErrTicketInvalid is a ticket that Oryxa did not make: malformed, from an
	// older or newer format, or signed under another key. It says no more,
	// so a forger learns nothing from it. A ticket Oryxa made for the other
	// kind of button is one too.
	ErrTicketInvalid = errors.New("invalid Slack ticket")
	// ErrTicketExpired is a ticket Oryxa made, whose time has passed.
	ErrTicketExpired = errors.New("expired Slack ticket")
)

// The kinds of ticket (TicketClaims.Kind).
const (
	// TicketConsent is on a button of an owner's notice, and decides a request:
	// ChoiceSummary, ChoiceAccess or ChoiceDeny.
	TicketConsent = "consent"
	// TicketReview is on a button of an owner's review of an answer the guards
	// held, and reviews it: ReviewSend, ReviewSendMasked or ReviewWithhold.
	TicketReview = "review"
)

const (
	ticketVersion = "t1"
	// minTicketKeyBytes is the shortest key a ticket is signed under: an HMAC
	// key shorter than its hash is weaker than the hash.
	minTicketKeyBytes = 32
	// maxTicketBytes bounds a ticket a caller hands over, so a hostile value
	// is refused before it is decoded.
	maxTicketBytes = 1024
	maxTicketField = 128
)

// TicketClaims is what a ticket says.
type TicketClaims struct {
	// RequestID is the request the button decides.
	RequestID string
	// OwnerUserID is the member whose click decides it: the request's target.
	OwnerUserID string
	// NoticeID names the message the button is on: the notice, or the review.
	// It is Oryxa's own id for that message, chosen before the message is
	// posted, since a button is part of it.
	NoticeID string
	// Kind is what the button is for: TicketConsent or TicketReview.
	Kind string
	// Choice is what the button does: for a consent ticket ChoiceSummary,
	// ChoiceAccess or ChoiceDeny, and for a review ticket ReviewSend,
	// ReviewSendMasked or ReviewWithhold.
	Choice string
	// ExpiresAt is when the ticket stops working, to the second.
	ExpiresAt time.Time
}

// ticketBody is the claims as they are signed. The short names keep a
// button's value short. Kind is left out of a consent ticket, which is how
// every consent ticket has always been made, and is "review" for a review's.
type ticketBody struct {
	Request string `json:"r"`
	Owner   string `json:"o"`
	Notice  string `json:"n"`
	Choice  string `json:"c"`
	Kind    string `json:"k,omitempty"`
	Expires int64  `json:"exp"`
}

// wireKind is a kind as a ticket's body spells it, and kindOf the kind a body
// spells: nothing is a consent ticket, "review" a review's, and anything else is
// no ticket this ledger made.
func wireKind(kind string) string {
	if kind == TicketReview {
		return TicketReview
	}
	return ""
}

func kindOf(wire string) (string, bool) {
	switch wire {
	case "":
		return TicketConsent, true
	case TicketReview:
		return TicketReview, true
	}
	return "", false
}

// validKindChoice is whether a choice is one a ticket of kind makes.
func validKindChoice(kind, choice string) bool {
	switch kind {
	case TicketConsent:
		return validChoice(choice)
	case TicketReview:
		return validReview(choice)
	}
	return false
}

// Tickets makes and checks consent tickets under one key.
type Tickets struct{ key []byte }

// NewTickets returns tickets signed under key, which must be at least 32
// bytes.
func NewTickets(key []byte) (*Tickets, error) {
	if len(key) < minTicketKeyBytes {
		return nil, fmt.Errorf("consent tickets need a key of at least %d bytes", minTicketKeyBytes)
	}
	return &Tickets{key: bytes.Clone(key)}, nil
}

// Mint makes the ticket for claims.
func (tickets *Tickets) Mint(claims TicketClaims) (string, error) {
	switch {
	case !validTicketField(claims.RequestID) || !validTicketField(claims.OwnerUserID) || !validTicketField(claims.NoticeID):
		return "", fmt.Errorf("%w: a ticket names a request, its owner and a notice, each of 1 to %d characters", ErrInvalid, maxTicketField)
	case claims.Kind != TicketConsent && claims.Kind != TicketReview:
		return "", fmt.Errorf("%w: a ticket is a consent's or a review's", ErrInvalid)
	case !validKindChoice(claims.Kind, claims.Choice):
		return "", fmt.Errorf("%w: a consent ticket decides summary, access or deny, and a review ticket sends, sends masked or withholds", ErrInvalid)
	case claims.ExpiresAt.IsZero():
		return "", fmt.Errorf("%w: a ticket needs an expiry", ErrInvalid)
	}
	body, err := json.Marshal(ticketBody{
		Request: claims.RequestID, Owner: claims.OwnerUserID, Notice: claims.NoticeID,
		Choice: claims.Choice, Kind: wireKind(claims.Kind), Expires: claims.ExpiresAt.Unix(),
	})
	if err != nil {
		return "", err
	}
	signed := ticketVersion + "." + base64.RawURLEncoding.EncodeToString(body)
	return signed + "." + base64.RawURLEncoding.EncodeToString(tickets.sign(signed)), nil
}

// Verify reads a ticket handed back by a click. The signature is checked
// first, in constant time, and nothing in a ticket is read before it holds:
// so a ticket that was not made here is ErrTicketInvalid whatever it says,
// and only one that was is ever ErrTicketExpired.
func (tickets *Tickets) Verify(ticket string, now time.Time) (TicketClaims, error) {
	if len(ticket) == 0 || len(ticket) > maxTicketBytes {
		return TicketClaims{}, ErrTicketInvalid
	}
	parts := strings.Split(ticket, ".")
	if len(parts) != 3 || parts[0] != ticketVersion {
		return TicketClaims{}, ErrTicketInvalid
	}
	given, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil || !hmac.Equal(given, tickets.sign(parts[0]+"."+parts[1])) {
		return TicketClaims{}, ErrTicketInvalid
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return TicketClaims{}, ErrTicketInvalid
	}
	var body ticketBody
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&body); err != nil || decoder.More() {
		return TicketClaims{}, ErrTicketInvalid
	}
	kind, known := kindOf(body.Kind)
	claims := TicketClaims{
		RequestID: body.Request, OwnerUserID: body.Owner, NoticeID: body.Notice, Kind: kind, Choice: body.Choice,
		ExpiresAt: time.Unix(body.Expires, 0).UTC(),
	}
	if !known || !validTicketField(claims.RequestID) || !validTicketField(claims.OwnerUserID) || !validTicketField(claims.NoticeID) ||
		!validKindChoice(claims.Kind, claims.Choice) {
		return TicketClaims{}, ErrTicketInvalid
	}
	if !now.Before(claims.ExpiresAt) {
		return TicketClaims{}, ErrTicketExpired
	}
	return claims, nil
}

func (tickets *Tickets) sign(signed string) []byte {
	mac := hmac.New(sha256.New, tickets.key)
	mac.Write([]byte(signed))
	return mac.Sum(nil)
}

func validTicketField(value string) bool {
	return value != "" && utf8.RuneCountInString(value) <= maxTicketField && validID(value) && strings.TrimSpace(value) == value
}
