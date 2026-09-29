// Package clisession describes short-lived setup sessions. A Cognito CLI token
// is usable only after completing a challenge issued by this server.
package clisession

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"time"
)

const ChallengeTTL = 20 * time.Minute
const SessionTTL = 15 * time.Minute
const CreatingOrganization = "@creating"

type Challenge struct {
	State     string    `json:"state"`
	Nonce     string    `json:"nonce"`
	ExpiresAt time.Time `json:"expires_at"`
}

type Session struct {
	OrganizationID string
	ExpiresAt      time.Time
}

type Store interface {
	CreateCLIChallenge(context.Context, Challenge) error
	CLIChallenge(context.Context, string) (Challenge, error)
	AdmitCLISession(context.Context, string, string, string, time.Time) error
	CLISession(context.Context, string) (Session, error)
	BindCLISession(context.Context, string, string) error
	ReserveCLIOrganization(context.Context, string) error
	FinishCLIOrganization(context.Context, string, string) error
}

func NewChallenge() Challenge {
	return Challenge{State: random(), Nonce: random(), ExpiresAt: time.Now().UTC().Add(ChallengeTTL)}
}
func random() string {
	var value [32]byte
	if _, err := rand.Read(value[:]); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(value[:])
}
func Hash(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}
