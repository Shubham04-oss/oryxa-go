// Package authz is Oryxa's system-wide authorization engine: given who is
// asking, what they want to do, and to which resource, it decides allow or
// deny. Every Oryxa service that guards a resource asks this package rather
// than growing its own rules, so "who may read this" means the same thing
// everywhere.
//
// Decisions are made in two layers. A fixed, code-owned baseline (embedded
// from internal/authz/policies) reproduces the access rules each domain
// already had before Cedar; it is the only source of permits, so it is also
// the ceiling on what anyone can ever be granted. Organizations then narrow
// that ceiling with their own stored policies, which may only forbid, never
// permit — see PolicyStore, Store and ValidatePolicy. A request is allowed
// when the baseline permits it and no applicable stored forbid matches,
// which is exactly Cedar's own deny-overrides evaluation over the baseline
// and the applicable stored policies combined into one policy set.
//
// Knowledge bases and their documents are the first resources this package
// governs (see KnowledgeBase and Document); the Resource interface exists so
// files, teams, agents and apps can be added later without changing the
// engine or the call sites that already use it.
//
// # A forbid that cannot evaluate is not forbidden
//
// Cedar treats a policy whose condition errors at evaluation time — most
// often because it read an attribute that is not there — as if it were not
// present: it neither permits nor forbids. For a stored policy, which is
// always a forbid, that means an error makes the request MORE permissive,
// not less, which is backwards for something meant to narrow access. The
// baseline is spared this: this package treats any evaluation error in a
// baseline policy as a denial (see Engine.Authorize), because the baseline
// is the only thing standing between a request and the resource. Stored
// policies get no such rescue, which is exactly why ValidatePolicy smoke
// tests a policy against fixture data before it is ever stored, quoting
// Cedar's own error back to whoever is writing it.
//
// The practical consequence for anyone writing a stored policy is: guard an
// attribute that might be absent with has before comparing it, inside the
// same when or unless clause. Cedar's has reports false for a missing
// attribute rather than erroring, so combining it with && short-circuits the
// comparison instead of ever reaching it. This matters most for metadata,
// whose keys are caller-defined and therefore never guaranteed to be there:
//
//	forbid (
//	  principal,
//	  action == Oryxa::Action::"read",
//	  resource is Oryxa::Document
//	) when {
//	  resource.metadata has confidential &&
//	  resource.metadata.confidential == "true"
//	} unless {
//	  context.acts_as_admin
//	};
//
// Without the "has confidential" clause, a document whose metadata never set
// that key would make the when condition error on every evaluation, and the
// forbid would silently stop applying to any document — including the ones
// it was written for.
//
// A second example narrows by principal kind rather than by data shape, and
// needs no has because is and == never error on a value that is simply the
// wrong type or entity — they just compare unequal:
//
//	forbid (
//	  principal is Oryxa::Agent,
//	  action,
//	  resource is Oryxa::Document
//	) when {
//	  resource.access == "admins"
//	};
//
// This keeps an agent acting for an admin off that admin's own admins-only
// documents, even though the admin's own session can still read them.
package authz
