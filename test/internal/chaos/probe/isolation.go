package probe

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

// Isolation calls every operation the way an attacker inside the system
// would: with credentials that are valid for something, aimed at objects
// that belong to someone else. Each attempt that succeeds is a tenant
// boundary or an authentication failure.
//
// The cases, for an operation on organization B's objects:
//
//	member-of-A-uses-B-ids   A's owner, A's URL, B's object ids       -> must fail
//	B-owner-on-A-url         B's owner, A's org in the URL            -> must fail
//	ghost-on-A               a signed-in person in no organization    -> must fail
//	dual-uses-B-ids-in-A     a member of both, A's URL, B's object    -> must fail
//	no-credentials           nobody                                   -> must fail
//	garbage-credentials      a token nobody issued                    -> must fail
func (runner *Runner) Isolation(ctx context.Context, ops []Op) (int, [][]string) {
	world := runner.W
	var matrix [][]string
	sent := 0
	for _, op := range ops {
		if op.Skip != "" || op.Public {
			continue
		}
		targetB, missing := world.Baseline(op, "b")
		if len(missing) > 0 && !op.Shallow {
			continue
		}
		inA := func(path string) string {
			return strings.Replace(path, "/organizations/"+world.B.ID, "/organizations/"+world.A.ID, 1)
		}
		type attempt struct {
			name  string
			actor Identity
			path  string
		}
		// Using B's object ids under A's URL only says something when there are
		// ids to use. On a collection route (GET .../files) it is just A's owner
		// reading A's own collection, which is exactly what should work.
		foreign := len(placeholders(op.Path)) > 1 || referencesVars(op.Body)
		attempts := []attempt{
			{"B-owner-on-A-url", world.B.Owner, inA(targetB.Path)},
			{"ghost-on-A", world.Ghost, inA(targetB.Path)},
			{"ghost-on-B", world.Ghost, targetB.Path},
			{"A-owner-on-B", world.A.Owner, targetB.Path},
			{"no-credentials", Identity{}, targetB.Path},
			{"garbage-credentials", Identity{Token: "not-a-real-token"}, targetB.Path},
		}
		if foreign {
			attempts = append([]attempt{
				{"member-of-A-uses-B-ids", world.A.Owner, inA(targetB.Path)},
				{"dual-uses-B-ids-in-A", world.Dual, inA(targetB.Path)},
			}, attempts...)
		}
		// For B's own path, the actor is B's owner, so a "B-owner-on-A-url" that
		// still names B's ids under A's URL is the cross-tenant probe worth making.
		row := []string{op.Name}
		if !foreign {
			row = append(row, "-", "-") // the two cases that need foreign ids
		}
		for _, one := range attempts {
			req := targetB
			req.Header = targetB.Header.Clone()
			req.Path, req.Token = one.path, one.actor.Token
			req.Mutation, req.Class, req.Target, req.Deny = one.name, "isolation", "credentials", true
			res := world.C.Do(ctx, req)
			sent++
			row = append(row, fmt.Sprint(res.Status))
			if res.Status >= 200 && res.Status < 300 {
				kind, detail := "cross-tenant-access", fmt.Sprintf("%s got %d on %s", one.name, res.Status, op.Route())
				if one.name == "no-credentials" || one.name == "garbage-credentials" {
					kind = "unauthenticated-access"
				}
				runner.Findings.Add(Finding{
					Kind: kind, Severity: Critical, Op: op.Name, Route: op.Route(), Class: "isolation", Status: res.Status,
					Detail: detail, Snippet: snippet(res), Curl: Curl(world.C.base, req),
				}, one.name)
			}
			for _, finding := range Judge(req, res, runner.SlowAfter) {
				finding.Curl = Curl(world.C.base, req)
				runner.Findings.Add(finding, one.name)
			}
		}
		matrix = append(matrix, row)
	}
	return sent, matrix
}

// referencesVars reports whether a body template mentions {variables}, that is,
// ids of objects that exist in one organization or another.
func referencesVars(body any) bool {
	switch value := body.(type) {
	case map[string]any:
		for _, child := range value {
			if referencesVars(child) {
				return true
			}
		}
	case []any:
		for _, child := range value {
			if referencesVars(child) {
				return true
			}
		}
	case string:
		return strings.Contains(value, "{") && !strings.Contains(value, "{org}")
	}
	return false
}

// RoleMatrix sends each operation as each role of organization A, against A's
// own objects, and tabulates the statuses. It asserts nothing about who
// should be allowed; it makes a policy readable at a glance, so a member who
// can do what only an owner should stands out.
func (runner *Runner) RoleMatrix(ctx context.Context, ops []Op) ([]string, [][]string) {
	world := runner.W
	roles := []struct {
		name  string
		actor Identity
	}{
		{"owner", world.A.Owner}, {"admin", world.A.Admin}, {"member", world.A.Member},
		{"member2", world.A.Member2}, {"ghost", world.Ghost}, {"none", Identity{}},
	}
	header := []string{"operation"}
	for _, role := range roles {
		header = append(header, role.name)
	}
	var rows [][]string
	for _, op := range ops {
		if op.Skip != "" || op.Consumes || op.Public {
			continue
		}
		base, missing := world.Baseline(op, "a")
		if len(missing) > 0 {
			continue
		}
		row := []string{op.Name}
		for _, role := range roles {
			req := base
			req.Header = base.Header.Clone()
			req.Token = role.actor.Token
			req.Mutation, req.Class = "role-"+role.name, "roles"
			res := world.C.Do(ctx, req)
			row = append(row, fmt.Sprint(res.Status))
		}
		rows = append(rows, row)
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i][0] < rows[j][0] })
	return header, rows
}
