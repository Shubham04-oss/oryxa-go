package interactions

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

var (
	ticketKey = []byte("0123456789abcdef0123456789abcdef-slack-tickets")
	ticketNow = time.Date(2026, time.September, 29, 14, 0, 0, 0, time.UTC)
)

func testTickets(t *testing.T) *Tickets {
	t.Helper()
	tickets, err := NewTickets(ticketKey)
	if err != nil {
		t.Fatal(err)
	}
	return tickets
}

func validClaims() TicketClaims {
	return TicketClaims{
		RequestID: "irq_1", OwnerUserID: "user_b", NoticeID: "ntc_1", Choice: ChoiceAccess,
		ExpiresAt: ticketNow.Add(time.Hour),
	}
}

// resign is a ticket signed under key over a body of its own, which is how a
// ticket that is well formed but that this ledger did not make looks.
func resign(t *testing.T, key []byte, body string) string {
	t.Helper()
	signed := ticketVersion + "." + base64.RawURLEncoding.EncodeToString([]byte(body))
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(signed))
	return signed + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// TestATicketReadsBackWhatItWasMadeFor: a ticket is the version, the claims as
// JSON and an HMAC over both, each part base64url, and is short enough to be a
// Slack button's value.
func TestATicketReadsBackWhatItWasMadeFor(t *testing.T) {
	tickets := testTickets(t)
	ticket, err := tickets.Mint(validClaims())
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(ticket, ".")
	if len(parts) != 3 || parts[0] != "t1" || len(ticket) > 300 {
		t.Fatalf("ticket = %q, want t1.<claims>.<mac> of at most 300 characters", ticket)
	}
	body, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatal(err)
	}
	const want = `{"r":"irq_1","o":"user_b","n":"ntc_1","c":"access","exp":1790694000}`
	if string(body) != want {
		t.Fatalf("claims = %s, want %s", body, want)
	}
	claims, err := tickets.Verify(ticket, ticketNow)
	if err != nil || claims != validClaims() {
		t.Fatalf("Verify = %+v, %v; want %+v", claims, err, validClaims())
	}
}

// TestAForgedTicketIsInvalidWhateverItSays: nothing in a ticket is trusted
// before its signature is, so every way of not having been made here reads the
// same, and never as an expired ticket.
func TestAForgedTicketIsInvalidWhateverItSays(t *testing.T) {
	tickets := testTickets(t)
	genuine, err := tickets.Mint(validClaims())
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(genuine, ".")
	otherOwner := strings.Replace(`{"r":"irq_1","o":"user_b","n":"ntc_1","c":"access","exp":1790694000}`, "user_b", "user_x", 1)
	flipped := []byte(parts[2])
	flipped[0] ^= 1
	longer := genuine + "A"
	for name, ticket := range map[string]string{
		"empty":                          "",
		"no dots":                        "t1",
		"two parts":                      parts[0] + "." + parts[1],
		"four parts":                     genuine + ".x",
		"another version":                "t2." + parts[1] + "." + parts[2],
		"no version":                     parts[1] + "." + parts[2],
		"a bit of the signature flipped": parts[0] + "." + parts[1] + "." + string(flipped),
		"the signature left out":         parts[0] + "." + parts[1] + ".",
		"a signature that is not base64": parts[0] + "." + parts[1] + ".***",
		"a longer signature":             longer,
		"another owner, same signature":  parts[0] + "." + base64.RawURLEncoding.EncodeToString([]byte(otherOwner)) + "." + parts[2],
		"signed under another key":       resign(t, []byte("another-key-of-at-least-32-bytes-long!!"), `{"r":"irq_1","o":"user_b","n":"ntc_1","c":"access","exp":1790694000}`),
		"signed with a different choice": strings.Replace(genuine, parts[1], base64.RawURLEncoding.EncodeToString([]byte(`{"r":"irq_1","o":"user_b","n":"ntc_1","c":"deny","exp":1790694000}`)), 1),
		"a ticket far too long":          strings.Repeat("t1.", 500),
	} {
		if _, err := tickets.Verify(ticket, ticketNow); !errors.Is(err, ErrTicketInvalid) {
			t.Errorf("%s: Verify = %v, want ErrTicketInvalid", name, err)
		}
	}
}

// A ticket this ledger signed with a body it does not make is no ticket either:
// the signature says who made it, not that it says something sensible.
func TestASignedTicketWithABodyThisLedgerDoesNotMakeIsInvalid(t *testing.T) {
	tickets := testTickets(t)
	for name, body := range map[string]string{
		"not json":            `not json`,
		"an unknown field":    `{"r":"irq_1","o":"user_b","n":"ntc_1","c":"access","exp":1790694000,"x":1}`,
		"no request":          `{"o":"user_b","n":"ntc_1","c":"access","exp":1790694000}`,
		"no owner":            `{"r":"irq_1","n":"ntc_1","c":"access","exp":1790694000}`,
		"no notice":           `{"r":"irq_1","o":"user_b","c":"access","exp":1790694000}`,
		"an unknown choice":   `{"r":"irq_1","o":"user_b","n":"ntc_1","c":"everything","exp":1790694000}`,
		"two objects":         `{"r":"irq_1","o":"user_b","n":"ntc_1","c":"access","exp":1790694000}{}`,
		"a request too long":  `{"r":"` + strings.Repeat("r", 129) + `","o":"user_b","n":"ntc_1","c":"access","exp":1790694000}`,
		"an id with a space":  `{"r":"irq 1 ","o":"user_b","n":"ntc_1","c":"access","exp":1790694000}`,
		"an expiry as a text": `{"r":"irq_1","o":"user_b","n":"ntc_1","c":"access","exp":"soon"}`,
	} {
		if _, err := tickets.Verify(resign(t, ticketKey, body), ticketNow); !errors.Is(err, ErrTicketInvalid) {
			t.Errorf("%s: Verify = %v, want ErrTicketInvalid", name, err)
		}
	}
}

// TestATicketExpiresAtItsSecond: it works until its second and not from it,
// and only a ticket that was made here is ever an expired one.
func TestATicketExpiresAtItsSecond(t *testing.T) {
	tickets := testTickets(t)
	claims := validClaims()
	ticket, err := tickets.Mint(claims)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tickets.Verify(ticket, claims.ExpiresAt.Add(-time.Nanosecond)); err != nil {
		t.Fatalf("a nanosecond before it runs out: %v", err)
	}
	for _, at := range []time.Time{claims.ExpiresAt, claims.ExpiresAt.Add(time.Second), claims.ExpiresAt.Add(24 * time.Hour)} {
		if _, err := tickets.Verify(ticket, at); !errors.Is(err, ErrTicketExpired) || errors.Is(err, ErrTicketInvalid) {
			t.Errorf("at %s: Verify = %v, want ErrTicketExpired alone", at, err)
		}
	}
}

func TestTicketsRefuseWhatTheyCannotSign(t *testing.T) {
	if _, err := NewTickets(nil); err == nil {
		t.Error("NewTickets accepted no key")
	}
	if _, err := NewTickets([]byte("short")); err == nil {
		t.Error("NewTickets accepted a key shorter than its hash")
	}
	tickets := testTickets(t)
	for name, change := range map[string]func(*TicketClaims){
		"no request":        func(c *TicketClaims) { c.RequestID = "" },
		"no owner":          func(c *TicketClaims) { c.OwnerUserID = "" },
		"no notice":         func(c *TicketClaims) { c.NoticeID = "" },
		"an unknown choice": func(c *TicketClaims) { c.Choice = "everything" },
		"no expiry":         func(c *TicketClaims) { c.ExpiresAt = time.Time{} },
		"a long request":    func(c *TicketClaims) { c.RequestID = strings.Repeat("r", 129) },
		"a NUL in an id":    func(c *TicketClaims) { c.NoticeID = "ntc\x00" },
	} {
		claims := validClaims()
		change(&claims)
		if _, err := tickets.Mint(claims); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: Mint = %v, want ErrInvalid", name, err)
		}
	}
}

// A ticket is made for one choice: the three buttons of a notice differ, and
// none reads as another.
func TestEachChoiceHasATicketOfItsOwn(t *testing.T) {
	tickets := testTickets(t)
	seen := map[string]string{}
	for _, choice := range []string{ChoiceSummary, ChoiceAccess, ChoiceDeny} {
		claims := validClaims()
		claims.Choice = choice
		ticket, err := tickets.Mint(claims)
		if err != nil {
			t.Fatal(err)
		}
		if other, dup := seen[ticket]; dup {
			t.Fatalf("%s and %s made the same ticket", choice, other)
		}
		seen[ticket] = choice
		read, err := tickets.Verify(ticket, ticketNow)
		if err != nil || read.Choice != choice {
			t.Fatalf("%s read back as %+v, %v", choice, read, err)
		}
	}
}

// A ticket is not a secret, and its body is readable, but it reveals nothing
// beyond ids, and its claims carry no words.
func TestATicketCarriesIDsAndNoWords(t *testing.T) {
	ticket, err := testTickets(t).Mint(validClaims())
	if err != nil {
		t.Fatal(err)
	}
	body, _ := base64.RawURLEncoding.DecodeString(strings.Split(ticket, ".")[1])
	var fields map[string]any
	if err := json.Unmarshal(body, &fields); err != nil {
		t.Fatal(err)
	}
	if len(fields) != 5 {
		t.Fatalf("a ticket carries %v, want request, owner, notice, choice and expiry alone", fields)
	}
}
