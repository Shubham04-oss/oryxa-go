// Package guardpolicy decides how strictly the answer guard reads an answer
// for one member: what that member chose, combined with the least their
// organization allows.
//
// The guard reads an answer at one of two levels. Secrets masks what is
// always masked: passwords, keys, and card, bank and government numbers
// (answerguard.MaskedKinds). Strict also holds what the guardrail only
// detects: email addresses, phone numbers and addresses
// (answerguard.HeldKinds). There is no level below secrets, so those kinds
// are masked whatever anybody chooses.
//
// A level applies to an origin: an answer the member started themselves
// (owner), or an answer another member reads (peer). A member who chose
// nothing gets secrets for their own answers and strict for the ones others
// read. What applies is the stricter of the organization's minimum for the
// origin and the member's choice or default. A choice below the minimum is
// kept, and the minimum wins; the choice applies again if the minimum is
// lowered.
//
// The organization's minimums come from its workspace policy, which this
// package reads through OrgPolicies and never keeps. The same policy says how
// the relevance check treats an answer (hold, shadow or off); a deployment's
// Cap holds that to a ceiling, except for the organizations it names. A
// workspace service that is missing, or fails, is no reason to fail a check:
// the defaults apply and the failure is logged, once for as long as it lasts.
//
// A member's choices, and every change to them, are kept in PostgreSQL (see
// migrations) and forgotten when the member or their organization is.
package guardpolicy
