// Package v1 contains the stable version-one event contracts emitted by auth.
package v1

import "time"

const (
	Source                   = "oryxa.auth"
	UserRegisteredDetailType = "auth.user.registered.v1"
)

// UserRegistered is emitted after Oryxa has accepted a newly federated user.
// It deliberately excludes credentials and tokens.
type UserRegistered struct {
	UserID       string    `json:"user_id"`
	Email        string    `json:"email"`
	DisplayName  string    `json:"display_name,omitempty"`
	RegisteredAt time.Time `json:"registered_at"`
}
