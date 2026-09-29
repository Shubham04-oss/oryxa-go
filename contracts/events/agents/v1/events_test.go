package v1

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// A request's events say where it was made, who decides it and who did, and
// leave each out when there is nothing to say, so what an older build
// published still reads the same.
func TestInteractionChangedCarriesItsTeamspaceApprovalAndDecider(t *testing.T) {
	at := time.Date(2026, time.September, 25, 9, 30, 0, 0, time.UTC)
	base := InteractionChanged{
		RequestID: "irq_1", OrganizationID: "org_1", RequesterUserID: "user_a", RequesterAgentID: "agt_a",
		TargetUserID: "user_b", TargetAgentID: "agt_b", Status: "approved", Revision: 2, ChangedAt: at,
	}
	const prefix = `{"request_id":"irq_1","organization_id":"org_1","requester_user_id":"user_a","requester_agent_id":"agt_a",` +
		`"target_user_id":"user_b","target_agent_id":"agt_b","status":"approved","revision":2,"changed_at":"2026-09-25T09:30:00Z"`
	withAll := base
	withAll.TeamspaceID, withAll.Approval = "tsp_1", InteractionApprovalOwner
	withAll.DecidedBy = &InteractionDecider{Kind: DecidedByMember, UserID: "user_b"}
	byAgent := base
	byAgent.Approval = InteractionApprovalAgent
	byAgent.DecidedBy = &InteractionDecider{Kind: DecidedByAgent, UserID: "user_b", AgentID: "agt_b"}
	bySystem := base
	bySystem.TeamspaceID, bySystem.Approval = "tsp_1", InteractionApprovalOwner
	bySystem.DecidedBy = &InteractionDecider{Kind: DecidedBySystem}

	for name, test := range map[string]struct {
		value InteractionChanged
		want  string
	}{
		"as an older build published it": {base, prefix + `}`},
		"decided by its member in a teamspace": {withAll,
			prefix + `,"teamspace_id":"tsp_1","approval":"owner","decided_by":{"kind":"member","user_id":"user_b"}}`},
		"decided by an agent outside a teamspace": {byAgent,
			prefix + `,"approval":"agent","decided_by":{"kind":"agent","user_id":"user_b","agent_id":"agt_b"}}`},
		"closed by Oryxa": {bySystem,
			prefix + `,"teamspace_id":"tsp_1","approval":"owner","decided_by":{"kind":"system"}}`},
	} {
		encoded, err := json.Marshal(test.value)
		if err != nil {
			t.Fatal(err)
		}
		if string(encoded) != test.want {
			t.Errorf("%s:\n got %s\nwant %s", name, encoded, test.want)
		}
		// A consumer built before the fields reads the event as it always
		// has, and one built after reads them back.
		var decoded InteractionChanged
		if err := json.Unmarshal(encoded, &decoded); err != nil {
			t.Fatal(err)
		}
		if decoded.TeamspaceID != test.value.TeamspaceID || decoded.Approval != test.value.Approval ||
			(decoded.DecidedBy == nil) != (test.value.DecidedBy == nil) ||
			(decoded.DecidedBy != nil && *decoded.DecidedBy != *test.value.DecidedBy) {
			t.Errorf("%s: read back as %+v", name, decoded)
		}
	}
}

// A room message's event names the room, the message's place and whom it
// named, and leaves out what it does not have.
func TestTeamspaceRoomMessagePostedIsWrittenAsTheContractSays(t *testing.T) {
	at := time.Date(2026, time.September, 25, 9, 30, 0, 123000000, time.UTC)
	for name, test := range map[string]struct {
		value TeamspaceRoomMessagePosted
		want  string
	}{
		"a member naming an agent": {
			TeamspaceRoomMessagePosted{
				OrganizationID: "org_1", TeamspaceID: "tsp_1", Message: TeamspaceRoomMessage{ID: "rmm_1", Seq: 1},
				Author:           TeamspaceRoomAuthor{Kind: TeamspaceRoomAuthorMember, UserID: "user_ada"},
				AddressedHandles: []string{"crew"}, PostedAt: at,
			},
			`{"organization_id":"org_1","teamspace_id":"tsp_1","message":{"id":"rmm_1","seq":1},` +
				`"author":{"kind":"member","user_id":"user_ada"},"addressed_handles":["crew"],"posted_at":"2026-09-25T09:30:00.123Z"}`,
		},
		"an agent's answer": {
			TeamspaceRoomMessagePosted{
				OrganizationID: "org_1", TeamspaceID: "tsp_1", Message: TeamspaceRoomMessage{ID: "rmm_2", Seq: 2, InReplyToSeq: 1},
				Author:   TeamspaceRoomAuthor{Kind: TeamspaceRoomAuthorAgent, UserID: "user_ada", AgentID: "agt_crew"},
				PostedAt: at,
			},
			`{"organization_id":"org_1","teamspace_id":"tsp_1","message":{"id":"rmm_2","seq":2,"in_reply_to_seq":1},` +
				`"author":{"kind":"agent","user_id":"user_ada","agent_id":"agt_crew"},"posted_at":"2026-09-25T09:30:00.123Z"}`,
		},
	} {
		encoded, err := json.Marshal(test.value)
		if err != nil {
			t.Fatal(err)
		}
		if string(encoded) != test.want {
			t.Errorf("%s:\n got %s\nwant %s", name, encoded, test.want)
		}
	}
	if TeamspaceRoomMessagePostedDetailType != "agents.teamspace.room.message.posted.v1" {
		t.Fatalf("detail type = %q", TeamspaceRoomMessagePostedDetailType)
	}
}

// A person leaving a teamspace is written with its identities in this order,
// and removed_at to the nanosecond, in UTC, as Teams' clock gave it.
func TestTeamspaceMemberRemovedIsWrittenAsTheContractSays(t *testing.T) {
	removed := TeamspaceMemberRemoved{
		OrganizationID: "org_1", TeamspaceID: "tsp_1", TeamID: "team_1", UserID: "user_ada",
		RemovedAt: time.Date(2026, time.September, 24, 10, 0, 0, 123456789, time.UTC),
	}
	encoded, err := json.Marshal(removed)
	if err != nil {
		t.Fatal(err)
	}
	const want = `{"organization_id":"org_1","teamspace_id":"tsp_1","team_id":"team_1","user_id":"user_ada",` +
		`"removed_at":"2026-09-24T10:00:00.123456789Z"}`
	if string(encoded) != want {
		t.Fatalf("\n got %s\nwant %s", encoded, want)
	}
	if TeamspaceMemberRemovedDetailType != "agents.teamspace.member.removed.v1" {
		t.Fatalf("detail type = %q", TeamspaceMemberRemovedDetailType)
	}
}

// A Slack ask's events say which ask a request belongs to, where it was made,
// how far its owner let the answer go, and where and why it was decided, in
// this order after everything an older build published. An event without any
// of them is still written as it always was.
func TestInteractionChangedCarriesItsAskOriginConsentAndDecisionSurface(t *testing.T) {
	at := time.Date(2026, time.September, 29, 14, 2, 0, 0, time.UTC)
	base := InteractionChanged{
		RequestID: "irq_1", OrganizationID: "org_1", RequesterUserID: "user_a", RequesterAgentID: "agt_a",
		TargetUserID: "user_b", TargetAgentID: "agt_b", Status: "approved", Revision: 2, ChangedAt: at,
		Approval: InteractionApprovalOwner, AskID: "iak_1", Origin: InteractionOriginSlack, Consent: InteractionConsentAccess,
	}
	const prefix = `{"request_id":"irq_1","organization_id":"org_1","requester_user_id":"user_a","requester_agent_id":"agt_a",` +
		`"target_user_id":"user_b","target_agent_id":"agt_b","status":"approved","revision":2,"changed_at":"2026-09-29T14:02:00Z","approval":"owner"`
	const suffix = `,"ask_id":"iak_1","origin":"slack","consent":"access"}`

	byClick := base
	byClick.DecidedBy = &InteractionDecider{Kind: DecidedByMember, UserID: "user_b", Surface: DecidedViaSlack}
	byLease := base
	byLease.DecidedBy = &InteractionDecider{Kind: DecidedByLease, UserID: "user_b", Surface: DecidedViaLease, Reason: "ils_1"}
	byBypass := base
	byBypass.DecidedBy = &InteractionDecider{Kind: DecidedByBypass, Surface: DecidedViaBypass, Reason: "private_channel_marked"}
	answeredElsewhere := base
	answeredElsewhere.Status, answeredElsewhere.Consent = "rejected", ""
	answeredElsewhere.DecidedBy = &InteractionDecider{Kind: DecidedBySystem, Reason: "answered_elsewhere"}
	summary := base
	summary.Consent = InteractionConsentSummary
	summary.DecidedBy = &InteractionDecider{Kind: DecidedByMember, UserID: "user_b", Surface: DecidedViaWeb}

	for name, test := range map[string]struct {
		value InteractionChanged
		want  string
	}{
		"approved by the owner's click in Slack": {byClick,
			prefix + `,"decided_by":{"kind":"member","user_id":"user_b","surface":"slack"}` + suffix},
		"approved under a lease": {byLease,
			prefix + `,"decided_by":{"kind":"lease","user_id":"user_b","surface":"lease","reason":"ils_1"}` + suffix},
		"approved by a bypass": {byBypass,
			prefix + `,"decided_by":{"kind":"bypass","surface":"bypass","reason":"private_channel_marked"}` + suffix},
		"closed because another owner answered": {answeredElsewhere,
			`{"request_id":"irq_1","organization_id":"org_1","requester_user_id":"user_a","requester_agent_id":"agt_a",` +
				`"target_user_id":"user_b","target_agent_id":"agt_b","status":"rejected","revision":2,"changed_at":"2026-09-29T14:02:00Z","approval":"owner",` +
				`"decided_by":{"kind":"system","reason":"answered_elsewhere"},"ask_id":"iak_1","origin":"slack"}`},
		"approved as a summary alone on the web": {summary,
			prefix + `,"decided_by":{"kind":"member","user_id":"user_b","surface":"web"},"ask_id":"iak_1","origin":"slack","consent":"summary"}`},
	} {
		encoded, err := json.Marshal(test.value)
		if err != nil {
			t.Fatal(err)
		}
		if string(encoded) != test.want {
			t.Errorf("%s:\n got %s\nwant %s", name, encoded, test.want)
		}
		var decoded InteractionChanged
		if err := json.Unmarshal(encoded, &decoded); err != nil {
			t.Fatal(err)
		}
		if decoded.AskID != test.value.AskID || decoded.Origin != test.value.Origin || decoded.Consent != test.value.Consent ||
			decoded.DecidedBy == nil || *decoded.DecidedBy != *test.value.DecidedBy {
			t.Errorf("%s: read back as %+v", name, decoded)
		}
	}

	// A consumer built before these fields ignores them, and an event an older
	// build published, which has none, reads as it did.
	var older InteractionChanged
	if err := json.Unmarshal([]byte(prefix+`}`), &older); err != nil || older.AskID != "" || older.Origin != "" || older.Consent != "" {
		t.Fatalf("an older event read as %+v, %v", older, err)
	}
	var newer struct {
		RequestID string `json:"request_id"`
		Status    string `json:"status"`
	}
	if err := json.Unmarshal([]byte(prefix+suffix), &newer); err != nil || newer.RequestID != "irq_1" || newer.Status != "approved" {
		t.Fatalf("an older consumer read a newer event as %+v, %v", newer, err)
	}
}

// A lease an owner ended is written with its identities, its thread and both
// times in UTC, and never with a question.
func TestInteractionLeaseRevokedIsWrittenAsTheContractSays(t *testing.T) {
	revoked := InteractionLeaseRevoked{
		LeaseID: "ils_1", OrganizationID: "org_1", OwnerUserID: "user_b", AskerUserID: "user_a",
		ChannelID: "C0123", ThreadTS: "1790000000.000100", RequestID: "irq_1",
		ExpiresAt: time.Date(2026, time.September, 29, 15, 2, 0, 0, time.UTC),
		RevokedAt: time.Date(2026, time.September, 29, 14, 30, 0, 0, time.UTC),
	}
	encoded, err := json.Marshal(revoked)
	if err != nil {
		t.Fatal(err)
	}
	const want = `{"lease_id":"ils_1","organization_id":"org_1","owner_user_id":"user_b","asker_user_id":"user_a",` +
		`"channel_id":"C0123","thread_ts":"1790000000.000100","request_id":"irq_1",` +
		`"expires_at":"2026-09-29T15:02:00Z","revoked_at":"2026-09-29T14:30:00Z"}`
	if string(encoded) != want {
		t.Fatalf("\n got %s\nwant %s", encoded, want)
	}
	revoked.RequestID = ""
	encoded, err = json.Marshal(revoked)
	if err != nil || strings.Contains(string(encoded), "request_id") {
		t.Fatalf("a lease with no known request wrote %s, %v", encoded, err)
	}
	if InteractionLeaseRevokedDetailType != "agents.interaction.lease.revoked.v1" {
		t.Fatalf("detail type = %q", InteractionLeaseRevokedDetailType)
	}
}

// A secret that was opened is written with who shared it and who opened it,
// and has no field the value could go in.
func TestInteractionSecretViewedIsWrittenAsTheContractSays(t *testing.T) {
	viewed := InteractionSecretViewed{
		RequestID: "irq_1", OrganizationID: "org_1", AskID: "iak_1", OwnerUserID: "user_b", AskerUserID: "user_a",
		ViewedAt: time.Date(2026, time.September, 29, 14, 5, 0, 0, time.UTC),
	}
	encoded, err := json.Marshal(viewed)
	if err != nil {
		t.Fatal(err)
	}
	const want = `{"request_id":"irq_1","organization_id":"org_1","ask_id":"iak_1","owner_user_id":"user_b","asker_user_id":"user_a",` +
		`"viewed_at":"2026-09-29T14:05:00Z"}`
	if string(encoded) != want {
		t.Fatalf("\n got %s\nwant %s", encoded, want)
	}
	viewed.AskID = ""
	encoded, err = json.Marshal(viewed)
	if err != nil || strings.Contains(string(encoded), "ask_id") {
		t.Fatalf("a secret on a request with no ask wrote %s, %v", encoded, err)
	}
	if InteractionSecretViewedDetailType != "agents.interaction.secret.viewed.v1" {
		t.Fatalf("detail type = %q", InteractionSecretViewedDetailType)
	}
	// The value is never in an event: the struct has no field for one.
	for _, field := range []string{"value", "secret", "text", "content"} {
		if strings.Contains(string(encoded), `"`+field+`"`) {
			t.Fatalf("the event has a %q field: %s", field, encoded)
		}
	}
}
