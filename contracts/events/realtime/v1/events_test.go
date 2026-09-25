package v1

import (
	"encoding/json"
	"testing"
	"time"
)

func encode(t *testing.T, value any) string {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}

// The payloads are written exactly as the contract names them: snake_case,
// times in UTC, and the optional fields absent rather than empty.
func TestThePayloadsAreWrittenAsTheContractSays(t *testing.T) {
	at := time.Date(2026, time.September, 24, 10, 5, 0, 123000000, time.UTC)
	for name, test := range map[string]struct {
		value any
		want  string
	}{
		"a member's message in a direct conversation": {
			MessagePosted{
				OrganizationID: "org_1", Conversation: Conversation{Kind: ConversationDirect, ID: "org_1~user-a~user-b"},
				ParticipantUserIDs: []string{"user-a", "user-b"}, Message: Message{ID: "msg_1", Seq: 1},
				Author: Author{Kind: AuthorMember, UserID: "user-a"}, PostedAt: at,
			},
			`{"organization_id":"org_1","conversation":{"kind":"direct","id":"org_1~user-a~user-b"},` +
				`"participant_user_ids":["user-a","user-b"],"message":{"id":"msg_1","seq":1},` +
				`"author":{"kind":"member","user_id":"user-a"},"posted_at":"2026-09-24T10:05:00.123Z"}`,
		},
		"an agent's answer in a side conversation": {
			MessagePosted{
				OrganizationID: "org_1", Conversation: Conversation{Kind: ConversationSide, ID: "org_1~tsp_1~user-a~user-b", TeamspaceID: "tsp_1"},
				ParticipantUserIDs: []string{"user-a", "user-b"}, Message: Message{ID: "sdi_1", Seq: 12},
				Author: Author{Kind: AuthorAgent, UserID: "user-b", AgentID: "agt_1"}, PostedAt: at,
			},
			`{"organization_id":"org_1","conversation":{"kind":"side","id":"org_1~tsp_1~user-a~user-b","teamspace_id":"tsp_1"},` +
				`"participant_user_ids":["user-a","user-b"],"message":{"id":"sdi_1","seq":12},` +
				`"author":{"kind":"agent","user_id":"user-b","agent_id":"agt_1"},"posted_at":"2026-09-24T10:05:00.123Z"}`,
		},
		"a read": {
			ConversationRead{
				OrganizationID: "org_1", Conversation: Conversation{Kind: ConversationDirect, ID: "org_1~user-a~user-b"},
				ReaderUserID: "user-b", ReadThroughSeq: 7, ReadAt: at,
			},
			`{"organization_id":"org_1","conversation":{"kind":"direct","id":"org_1~user-a~user-b"},` +
				`"reader_user_id":"user-b","read_through_seq":7,"read_at":"2026-09-24T10:05:00.123Z"}`,
		},
		"a member's purge": {
			PurgeCompleted{
				OrganizationID: "org_1", Scope: PurgeScopeMember, UserID: "user-b",
				RemovedAt: at, SourceEventID: "evt_auth_1", CompletedAt: at.Add(time.Second),
			},
			`{"organization_id":"org_1","scope":"member","user_id":"user-b","removed_at":"2026-09-24T10:05:00.123Z",` +
				`"source_event_id":"evt_auth_1","completed_at":"2026-09-24T10:05:01.123Z"}`,
		},
		"an organization's purge names no member": {
			PurgeCompleted{
				OrganizationID: "org_1", Scope: PurgeScopeOrganization,
				RemovedAt: at, SourceEventID: "evt_auth_2", CompletedAt: at,
			},
			`{"organization_id":"org_1","scope":"organization","removed_at":"2026-09-24T10:05:00.123Z",` +
				`"source_event_id":"evt_auth_2","completed_at":"2026-09-24T10:05:00.123Z"}`,
		},
		"a member's purge in a teamspace they left": {
			PurgeCompleted{
				OrganizationID: "org_1", Scope: PurgeScopeTeamspaceMember, UserID: "user-b", TeamspaceID: "tsp_1",
				RemovedAt: at, SourceEventID: "evt_agents_1", CompletedAt: at.Add(time.Second),
			},
			`{"organization_id":"org_1","scope":"teamspace_member","user_id":"user-b","teamspace_id":"tsp_1",` +
				`"removed_at":"2026-09-24T10:05:00.123Z","source_event_id":"evt_agents_1","completed_at":"2026-09-24T10:05:01.123Z"}`,
		},
		"a deleted teamspace's purge names no member": {
			PurgeCompleted{
				OrganizationID: "org_1", Scope: PurgeScopeTeamspace, TeamspaceID: "tsp_1",
				RemovedAt: at, SourceEventID: "evt_agents_2", CompletedAt: at,
			},
			`{"organization_id":"org_1","scope":"teamspace","teamspace_id":"tsp_1","removed_at":"2026-09-24T10:05:00.123Z",` +
				`"source_event_id":"evt_agents_2","completed_at":"2026-09-24T10:05:00.123Z"}`,
		},
	} {
		if got := encode(t, test.value); got != test.want {
			t.Errorf("%s:\n got %s\nwant %s", name, got, test.want)
		}
	}
}

// The names are the contract the Worker and every consumer match on.
func TestTheNamesAreTheContracts(t *testing.T) {
	for got, want := range map[string]string{
		Source:                     "oryxa.realtime",
		MessagePostedDetailType:    "realtime.message.posted.v1",
		ConversationReadDetailType: "realtime.conversation.read.v1",
		PurgeCompletedDetailType:   "realtime.purge.completed.v1",
		PurgeScopeMember:           "member",
		PurgeScopeOrganization:     "organization",
		PurgeScopeTeamspaceMember:  "teamspace_member",
		PurgeScopeTeamspace:        "teamspace",
	} {
		if got != want {
			t.Errorf("%q, want %q", got, want)
		}
	}
}
