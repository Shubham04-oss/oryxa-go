package domain

import (
	"fmt"
	"slices"
)

// Candidate is what a judge sees of a document when deciding whether it is
// worth indexing: whether a member would ever want Krude to find it.
type Candidate struct {
	Provider string
	Kind     string
	Title    string
	// MIME is the type the document is kept as: the text's own for one
	// knowledge read, the provider's for one kept as its original bytes.
	MIME     string
	Metadata map[string]string
	// Text is the document's extracted text, for one knowledge reads itself.
	Text string
	// Original is the document's own bytes, for one kept as them (a PDF, an
	// Office file). A judge that reads the format looks inside; one that does
	// not judges it by the rest.
	Original []byte
	// Size is the document's size in bytes, text or original.
	Size int
	// Include is what the source indexes, by kind; nil is DefaultKinds.
	Include []string
}

// Verdict is what a judge decided about a Candidate.
type Verdict struct {
	Keep bool
	// Category is the document's semantic kind, when the judge supplied one.
	// It is classification for search, never an access-control decision.
	Category string
	// Reason is a few words on why, which an excluded document keeps so a
	// member or an operator can see what was left out and on what grounds.
	Reason string
	// Judgment is the judge's own answer, as JSON, kept with the document:
	// what kind of document it is and how sure, from which the verdict can be
	// drawn again, for other kinds, without asking the judge again.
	Judgment []byte
}

// KindInfo is one kind of document a member can choose to index or not.
type KindInfo struct {
	Name  string `json:"name"`
	Label string `json:"label"`
	// Hint says what the kind covers, in a member's terms.
	Hint string `json:"hint"`
	// Default says whether a source indexes it unless its member chooses.
	Default bool `json:"default"`
}

// Kinds are the kinds of document a member chooses among, in the order they
// are shown. Spam and empty documents are never indexed and not offered; a
// document no kind describes is judged on its own.
var Kinds = []KindInfo{
	{Name: "conversation", Label: "Conversations", Hint: "Mail and messages with people", Default: true},
	{Name: "work_material", Label: "Documents and notes", Hint: "Plans, specs, notes, reports, proposals", Default: true},
	{Name: "financial_record", Label: "Payments and invoices", Hint: "Invoices, receipts, bank transactions, failed payments, billing alerts", Default: true},
	{Name: "booking", Label: "Orders and bookings", Hint: "Orders, tickets, reservations, itineraries", Default: true},
	{Name: "application_status", Label: "Applications", Hint: "Applications to programs, funds, employers and tax portals", Default: true},
	{Name: "support_case", Label: "Support cases", Hint: "Tickets you opened, and the replies", Default: true},
	{Name: "account_record", Label: "Account records", Hint: "Accounts opened or closed, plans, credits, domains, filings", Default: true},
	{Name: "security_incident", Label: "Security incidents", Hint: "Unusual activity or exposed credentials a provider found", Default: true},
	{Name: "event_update", Label: "Event updates", Hint: "Registrations accepted or declined, venue changes, messages from hosts"},
	{Name: "newsletter", Label: "Newsletters and announcements", Hint: "Digests, product updates and policy changes sent to many"},
	{Name: "promotion", Label: "Promotions", Hint: "Sales, offers, launches and outreach"},
	{Name: "routine_notification", Label: "Routine notices", Hint: "Codes, sign-in alerts, welcome mail, invitations, trials and reminders"},
}

// KindNames are the names of Kinds, in the order kinds are shown.
func KindNames() []string {
	names := make([]string, 0, len(Kinds))
	for _, kind := range Kinds {
		names = append(names, kind.Name)
	}
	return names
}

// DefaultKinds are what a source indexes unless its member chooses.
func DefaultKinds() []string {
	var names []string
	for _, kind := range Kinds {
		if kind.Default {
			names = append(names, kind.Name)
		}
	}
	return names
}

// IncludedKinds is the set a source indexes: its own choice, or the default.
func IncludedKinds(chosen []string) map[string]bool {
	if chosen == nil {
		chosen = DefaultKinds()
	}
	set := make(map[string]bool, len(chosen))
	for _, name := range chosen {
		set[name] = true
	}
	return set
}

// ValidateKinds refuses a choice naming a kind no member can choose.
func ValidateKinds(chosen []string) error {
	known := KindNames()
	for _, name := range chosen {
		if !slices.Contains(known, name) {
			return fmt.Errorf("%q is not a kind of document a source can index: %w", name, ErrInvalid)
		}
	}
	return nil
}
