package tools

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"time"
)

type auditRow struct {
	id     string
	req    Request
	effect string
	at     time.Time
}

func newID() (string, error) {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return "tc_" + hex.EncodeToString(b[:]), nil
}

func (s *Service) record(ctx context.Context, c auditRow, decision, status string) error {
	_, err := s.pool.Exec(ctx, `INSERT INTO integrations.tool_calls
		(id, org_id, member_id, agent_id, agent_role, connection_id, tool, effect, decision, status, at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)`,
		c.id, c.req.Org, c.req.Member, c.req.Agent, c.req.Role, c.req.Connection, c.req.Tool, c.effect, decision, status, c.at)
	return err
}

// refuse audits a call that was not run and returns why. A refusal that can't
// be recorded is still a refusal.
func (s *Service) refuse(ctx context.Context, c auditRow, reason error) error {
	decision := "allowed"
	if errors.Is(reason, ErrConfirmation) {
		decision = "needs_confirmation"
	}
	if err := s.record(ctx, c, decision, "refused"); err != nil {
		return errors.Join(reason, err)
	}
	return reason
}
