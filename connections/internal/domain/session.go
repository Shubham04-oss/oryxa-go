package domain

import "time"

// SessionTTL is how long a member has to consent and come back.
const SessionTTL = 10 * time.Minute

type SessionIntent string

const (
	IntentConnect   SessionIntent = "connect"
	IntentReconnect SessionIntent = "reconnect"
)

// Session is one authorization attempt. The OAuth state and the handle the
// app completes with are stored only as SHA-256 hashes; the PKCE verifier and
// the code the provider returned are encrypted.
type Session struct {
	StateHash      string
	HandleHash     string
	OrganizationID string
	MemberID       string
	AppID          string
	Intent         SessionIntent
	ConnectionID   string
	Scope          Scope
	TeamID         string
	// Usage is what a new organization connection will allow.
	Usage             Usage
	ReturnURI         string
	RedirectURI       string
	RequestedScopes   []string
	EncryptedVerifier *SealedData
	EncryptedResult   *SealedData
	ProviderError     string
	CreatedAt         time.Time
	ExpiresAt         time.Time
	CallbackAt        *time.Time
	// ClaimedAt is when a completion took the session, before exchanging its
	// code.
	ClaimedAt   *time.Time
	CompletedAt *time.Time
}

// Placement is where a connect session's connection will hang. A member
// connection belongs to the member who started the session.
func (session Session) Placement() Placement {
	placement := Placement{Scope: session.Scope, TeamID: session.TeamID}
	if session.Scope == ScopeMember {
		placement.MemberID = session.MemberID
	}
	return placement
}

// Completable decides whether memberID may complete the session now. Another
// member's session, or one the provider has not returned to, is concealed as
// not found.
func (session Session) Completable(memberID string, now time.Time) error {
	if session.MemberID == "" || session.MemberID != memberID || session.HandleHash == "" || session.CallbackAt == nil {
		return ErrNotFound
	}
	if session.ClaimedAt != nil || session.CompletedAt != nil || session.ProviderError != "" || !now.Before(session.ExpiresAt) {
		return ErrSessionExpired
	}
	return nil
}

// ConnectionQuery narrows a connection listing. With no Scope, a member sees
// their own member connections and the organization's; an admin also sees
// every member's. Team connections are listed only by TeamID.
type ConnectionQuery struct {
	AppID    string
	Scope    Scope
	TeamID   string
	MemberID string
	Admin    bool
}

type ConnectionHealth struct {
	State     Health     `json:"state"`
	CheckedAt *time.Time `json:"checked_at,omitempty"`
}
