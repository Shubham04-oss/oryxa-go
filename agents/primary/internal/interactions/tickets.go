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

// Each button on the notice an owner gets in Slack carries a ticket: proof
// that Oryxa made that button, for that owner, for that request and that
// choice. A click decides only with a valid ticket, so a leaked Slack signing
// secret, which lets someone forge a click, decides nothing: it cannot make a
// ticket.
//
//	t1.<base64url json {"r":request,"o":owner,"n":notice,"c":choice,"exp":unix}>.<base64url HMAC-SHA256>
//
// The HMAC is over everything before its own dot, under a key only agents
// holds (AGENTS_PRIMARY_SLACK_TICKET_KEY). A ticket is short enough to be a
// Slack button's value, and is no secret: the buttons of a notice carry
// three, and anyone who sees the notice sees them.

var (
	// ErrTicketInvalid is a ticket that Oryxa did not make: malformed, from an
	// older or newer format, or signed under another key. It says no more,
	// so a forger learns nothing from it.
	ErrTicketInvalid = errors.New("invalid consent ticket")
	// ErrTicketExpired is a ticket Oryxa made, whose time has passed.
	ErrTicketExpired = errors.New("consent ticket has expired")
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
	// NoticeID names the notice the button is on. It is Oryxa's own id for
	// that message, chosen before the message is posted, since a button is
	// part of it.
	NoticeID string
	// Choice is what the button decides: ChoiceSummary, ChoiceAccess or
	// ChoiceDeny.
	Choice string
	// ExpiresAt is when the ticket stops working, to the second.
	ExpiresAt time.Time
}

// ticketBody is the claims as they are signed. The short names keep a
// button's value short.
type ticketBody struct {
	Request string `json:"r"`
	Owner   string `json:"o"`
	Notice  string `json:"n"`
	Choice  string `json:"c"`
	Expires int64  `json:"exp"`
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
	case !validChoice(claims.Choice):
		return "", fmt.Errorf("%w: a ticket decides summary, access or deny", ErrInvalid)
	case claims.ExpiresAt.IsZero():
		return "", fmt.Errorf("%w: a ticket needs an expiry", ErrInvalid)
	}
	body, err := json.Marshal(ticketBody{
		Request: claims.RequestID, Owner: claims.OwnerUserID, Notice: claims.NoticeID,
		Choice: claims.Choice, Expires: claims.ExpiresAt.Unix(),
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
	claims := TicketClaims{
		RequestID: body.Request, OwnerUserID: body.Owner, NoticeID: body.Notice, Choice: body.Choice,
		ExpiresAt: time.Unix(body.Expires, 0).UTC(),
	}
	if !validTicketField(claims.RequestID) || !validTicketField(claims.OwnerUserID) || !validTicketField(claims.NoticeID) || !validChoice(claims.Choice) {
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
