package v1

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

var updateGolden = flag.Bool("update", false, "rewrite testdata/composio.golden from the current structs")

// goldenPath holds, one per line, each event exactly as apps published it
// for a Composio connection before Oryxa's own sign-in was added: the detail
// type, a space, and the JSON.
const goldenPath = "testdata/composio.golden"

// composioEvents are the events a Composio connection publishes, filled the
// way apps fills them. Nothing Oryxa's sign-in added is set: a Composio
// connection never sets it.
func composioEvents() map[string]any {
	at := time.Date(2026, time.September, 28, 12, 30, 0, 123000000, time.UTC)
	return map[string]any{
		ConnectionInitiatedDetailType: ConnectionInitiated{
			ConnectionID: "apc_1", OrganizationID: "org_1", MemberID: "user-1", Toolkit: "github", InitiatedAt: at,
		},
		ConnectionConnectedDetailType: ConnectionConnected{
			ConnectionID: "apc_1", OrganizationID: "org_1", MemberID: "user-1", Toolkit: "github", ConnectedAt: at,
		},
		ConnectionFailedDetailType: ConnectionFailed{
			ConnectionID: "apc_1", OrganizationID: "org_1", MemberID: "user-1", Toolkit: "github",
			Reason: FailedDeniedAtProvider, FailedAt: at,
		},
		ConnectionExpiredDetailType: ConnectionExpired{
			ConnectionID: "apc_1", OrganizationID: "org_1", MemberID: "user-1", Toolkit: "github",
			Reason: "the refresh token was revoked", ExpiredAt: at,
		},
		ConnectionRemovedDetailType: ConnectionRemoved{
			ConnectionID: "apc_1", OrganizationID: "org_1", MemberID: "user-1", Toolkit: "github",
			Reason: RemovedByMember, RemovedAt: at,
		},
		TriggerSubscribedDetailType: TriggerSubscribed{
			TriggerID: "apt_1", ConnectionID: "apc_1", OrganizationID: "org_1", MemberID: "user-1", Toolkit: "github",
			Trigger: "GITHUB_COMMIT_EVENT", Kind: "webhook", ManualSetup: true, SubscribedAt: at,
		},
		TriggerUnsubscribedDetailType: TriggerUnsubscribed{
			TriggerID: "apt_1", ConnectionID: "apc_1", OrganizationID: "org_1", MemberID: "user-1", Toolkit: "github",
			Trigger: "GITHUB_COMMIT_EVENT", ByAdmin: true, UnsubscribedAt: at,
		},
		TriggerReceivedDetailType: TriggerReceived{
			TriggerID: "apt_1", ConnectionID: "apc_1", OrganizationID: "org_1", MemberID: "user-1", Toolkit: "github",
			Trigger: "GITHUB_COMMIT_EVENT", Data: json.RawMessage(`{"sha":"abc"}`), ReceivedAt: at,
		},
		TriggerDisabledDetailType: TriggerDisabled{
			TriggerID: "apt_1", ConnectionID: "apc_1", OrganizationID: "org_1", MemberID: "user-1", Toolkit: "github",
			Trigger: "GITHUB_COMMIT_EVENT", Reason: "the connection expired", DisabledAt: at,
		},
		ActionCompletedDetailType: ActionCompleted{
			ActionID: "apa_1", ConnectionID: "apc_1", OrganizationID: "org_1", MemberID: "user-1", Toolkit: "github",
			Tool: "GITHUB_CREATE_AN_ISSUE", Agent: true, FinishedAt: at,
		},
		ActionFailedDetailType: ActionFailed{
			ActionID: "apa_1", ConnectionID: "apc_1", OrganizationID: "org_1", MemberID: "user-1", Toolkit: "github",
			Tool: "GITHUB_CREATE_AN_ISSUE", Error: "app_error", FinishedAt: at,
		},
	}
}

func encodeComposioEvents(t *testing.T) []byte {
	t.Helper()
	events := composioEvents()
	types := make([]string, 0, len(events))
	for detailType := range events {
		types = append(types, detailType)
	}
	slices.Sort(types)
	var out bytes.Buffer
	for _, detailType := range types {
		encoded, err := json.Marshal(events[detailType])
		if err != nil {
			t.Fatal(err)
		}
		out.WriteString(detailType + " " + string(encoded) + "\n")
	}
	return out.Bytes()
}

// Oryxa's own sign-in added fields to the connection events, and every one
// of them is left out when empty, so what a Composio connection publishes is
// byte for byte what it published before: no consumer sees anything change.
func TestComposioEventsAreByteIdenticalToBeforeOryxasSignin(t *testing.T) {
	got := encodeComposioEvents(t)
	if *updateGolden {
		if err := os.MkdirAll(filepath.Dir(goldenPath), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(goldenPath, got, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		gotLines, wantLines := strings.Split(string(got), "\n"), strings.Split(string(want), "\n")
		for index := range max(len(gotLines), len(wantLines)) {
			var gotLine, wantLine string
			if index < len(gotLines) {
				gotLine = gotLines[index]
			}
			if index < len(wantLines) {
				wantLine = wantLines[index]
			}
			if gotLine != wantLine {
				t.Errorf("line %d changed:\n got  %s\n want %s", index+1, gotLine, wantLine)
			}
		}
	}
}
