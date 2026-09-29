package slackrouter

import (
	"fmt"
	"strings"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/slackrouter/consentpolicy"
)

// refusalHint is the one line that says what to do about a refusal. It is
// written here so a refusal is never a bare no, and sent as the refusal
// template's hint; apps decides how it is shown. What the line says about
// people is their names, cleaned, and nothing else about them.
//
// tooManyPeople tells the two things too_many_asks means apart.
func refusalHint(reason consentpolicy.Reason, names []string, tooManyPeople bool) string {
	switch reason {
	case consentpolicy.NotMember:
		return "Only members of this workspace's Oryxa organization can ask me. Ask an admin to invite you."
	case consentpolicy.OwnerNotMember:
		switch len(names) {
		case 0:
			return "The people you named aren't in Oryxa, so I didn't ask them."
		case 1:
			return names[0] + " isn't in Oryxa, so I didn't ask them."
		}
		return joinNames(names) + " aren't in Oryxa, so I didn't ask them."
	case consentpolicy.GroupDM:
		return "I don't take requests in group messages. Mention me in a channel instead."
	case consentpolicy.ExternalChannel:
		return "I don't take requests in channels shared with other organizations."
	case consentpolicy.NoOwner:
		return "Say who to ask, for example: ask @maya for the Q3 numbers."
	case consentpolicy.TooManyAsks:
		if tooManyPeople {
			return fmt.Sprintf("You can ask at most %d people at once.", consentpolicy.MaxOwners)
		}
		return fmt.Sprintf("You already have %d requests waiting. Wait for an answer, then ask again.", consentpolicy.MaxOpenAsks)
	}
	return "I can't take this request."
}

// joinNames writes names as a list a person would say: "A, B and C".
func joinNames(names []string) string {
	if len(names) < 2 {
		return strings.Join(names, "")
	}
	return strings.Join(names[:len(names)-1], ", ") + " and " + names[len(names)-1]
}
