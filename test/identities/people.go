// Package identities is the shared simulated roster the stack k6 journey
// uses. Tokens are the role names. The auth stub resolves them to these
// people for teams and files, and the harness seeds them into auth_memberships.
package identities

import (
	"net/http"
	"strings"
)

const AccessCookie = "oryxa_access"

type Person struct {
	UserID      string
	Email       string
	DisplayName string
	Role        string
	InOrg       bool
}

var People = map[string]Person{
	"owner":    {UserID: "user-owner", Email: "owner@sim.oryxa", DisplayName: "Owner", Role: "owner", InOrg: true},
	"admin":    {UserID: "user-admin", Email: "admin@sim.oryxa", DisplayName: "Org Admin", Role: "admin", InOrg: true},
	"member":   {UserID: "user-member", Email: "member@sim.oryxa", DisplayName: "Member", Role: "member", InOrg: true},
	"outsider": {UserID: "user-outsider", Email: "outsider@sim.oryxa", DisplayName: "Outsider", Role: "member", InOrg: true},
	"stranger": {UserID: "user-stranger", Email: "stranger@sim.oryxa", DisplayName: "Stranger", Role: "member", InOrg: false},
}

func OrgMembers() []Person {
	return []Person{People["owner"], People["admin"], People["member"], People["outsider"]}
}

// PersonByUserID returns the stand-in person whose UserID is userID, and
// whether one was found. Minting an agent token is asked for a user_id —
// the field a Cognito-backed store would use — not the bearer-token role
// key People is keyed by, so this resolves it the other way around.
func PersonByUserID(userID string) (Person, bool) {
	for _, person := range People {
		if person.UserID == userID {
			return person, true
		}
	}
	return Person{}, false
}

func TokenFrom(request *http.Request) (string, bool) {
	parts := strings.Fields(request.Header.Get("Authorization"))
	if len(parts) == 2 && strings.EqualFold(parts[0], "Bearer") && parts[1] != "" {
		return parts[1], true
	}
	if cookie, err := request.Cookie(AccessCookie); err == nil && cookie.Value != "" {
		return cookie.Value, true
	}
	return "", false
}
