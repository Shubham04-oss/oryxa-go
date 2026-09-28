package signin

import (
	"context"
	"errors"
	"math/rand/v2"
	"time"
)

// Check outcomes.
const (
	// CheckPassed: the credential works, and the account still answers.
	CheckPassed = "passed"
	// CheckFailed: the app did not answer. The check runs again after a
	// backoff, 5 minutes doubling up to 6 hours; nothing changes meanwhile.
	CheckFailed = "failed"
	// CheckRejected: the app refused the credential for good. The
	// connection needs reconnecting: expire it, with Reason.
	CheckRejected = "rejected"
)

// Check is how one connection's health check went.
type Check struct {
	ConnectionID   string
	OrganizationID string
	MemberID       string
	Toolkit        string
	Outcome        string
	// Reason is ConnectionExpired's reason for a rejected check:
	// refresh_rejected or credential_rejected.
	Reason string
}

// CheckDue checks up to limit active connections whose daily check is due,
// and answers how each went; a rejected one is for the caller to expire. A
// check renews an OAuth token that is due, which keeps an idle refresh token
// alive, then runs the manifest's test call. A connection that was used
// successfully in the last 12 hours is not due: its use pushed its check
// back. Every replica may run it: each check is claimed first.
func (s *Service) CheckDue(ctx context.Context, limit int) ([]Check, error) {
	now := s.now().UTC()
	claimed, err := s.store.ClaimCredentialChecks(ctx, now, now.Add(claimFor), limit)
	if err != nil {
		return nil, err
	}
	checks := make([]Check, 0, len(claimed))
	for _, credential := range claimed {
		check := s.check(ctx, credential)
		checks = append(checks, check)
		checkedAt := s.now().UTC()
		next, failures := nextCheck(checkedAt), 0
		if check.Outcome == CheckFailed {
			failures = credential.CheckFailures + 1
			next = checkedAt.Add(backoff(failures))
		}
		if err := s.store.RecordCredentialCheck(ctx, credential.ConnectionID, next, failures); err != nil {
			s.logger.WarnContext(ctx, "record an app connection's check", "connection_id", credential.ConnectionID, "error", err)
		}
	}
	return checks, nil
}

func (s *Service) check(ctx context.Context, credential Credential) Check {
	check := Check{
		ConnectionID: credential.ConnectionID, OrganizationID: credential.OrganizationID,
		MemberID: credential.MemberID, Toolkit: credential.Toolkit, Outcome: CheckFailed,
	}
	p, err := s.prepareStored(ctx, credential)
	if err != nil {
		s.logger.WarnContext(ctx, "prepare an app connection's check", "connection_id", credential.ConnectionID, "error", err)
		return check
	}
	held, err := s.material(ctx, &liveCredential{credential: credential}, p)
	var rejected *Rejected
	switch {
	case errors.As(err, &rejected):
		check.Outcome, check.Reason = CheckRejected, rejected.Reason
		return check
	case err != nil:
		s.logger.WarnContext(ctx, "an app connection's check could not renew it", "connection_id", credential.ConnectionID, "error", err)
		return check
	}
	if _, err := s.runTest(ctx, p, held); err != nil {
		if errors.Is(err, errTestRefused) {
			check.Outcome, check.Reason = CheckRejected, ExpiredCredentialRejected
			return check
		}
		s.logger.WarnContext(ctx, "an app connection's check went unanswered", "connection_id", credential.ConnectionID, "error", err)
		return check
	}
	check.Outcome = CheckPassed
	return check
}

// jitter is a random span shorter than span.
func jitter(span time.Duration) time.Duration {
	if span <= 0 {
		return 0
	}
	return rand.N(span)
}
