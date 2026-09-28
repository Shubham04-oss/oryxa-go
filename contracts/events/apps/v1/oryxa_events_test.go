package v1

import (
	"encoding/json"
	"testing"
	"time"
)

// An Oryxa connection's events say so, and the two new detail types are
// spelled as the contract pins them: snake_case, times in UTC, and never a
// secret.
func TestOryxaEventsAreWrittenAsTheContractSays(t *testing.T) {
	at := time.Date(2026, time.September, 28, 12, 30, 0, 0, time.UTC)
	revoked, kept := true, false
	for name, test := range map[string]struct {
		value any
		want  string
	}{
		"connected through Oryxa": {
			ConnectionConnected{ConnectionID: "apc_1", OrganizationID: "org_1", MemberID: "user-1", Toolkit: "github",
				Broker: BrokerOryxa, ConnectedAt: at},
			`{"connection_id":"apc_1","organization_id":"org_1","member_id":"user-1","toolkit":"github","broker":"oryxa","connected_at":"2026-09-28T12:30:00Z"}`,
		},
		"failed on a key the app refused": {
			ConnectionFailed{ConnectionID: "apc_1", OrganizationID: "org_1", MemberID: "user-1", Toolkit: "github",
				Broker: BrokerOryxa, Reason: FailedInvalidCredentials, FailedAt: at},
			`{"connection_id":"apc_1","organization_id":"org_1","member_id":"user-1","toolkit":"github","broker":"oryxa","reason":"invalid_credentials","failed_at":"2026-09-28T12:30:00Z"}`,
		},
		"expired on a refused refresh": {
			ConnectionExpired{ConnectionID: "apc_1", OrganizationID: "org_1", MemberID: "user-1", Toolkit: "github",
				Broker: BrokerOryxa, Reason: ExpiredRefreshRejected, ExpiredAt: at},
			`{"connection_id":"apc_1","organization_id":"org_1","member_id":"user-1","toolkit":"github","broker":"oryxa","reason":"refresh_rejected","expired_at":"2026-09-28T12:30:00Z"}`,
		},
		"removed and revoked at the app": {
			ConnectionRemoved{ConnectionID: "apc_1", OrganizationID: "org_1", MemberID: "user-1", Toolkit: "github",
				Broker: BrokerOryxa, Reason: RemovedByMember, ProviderRevoked: &revoked, RemovedAt: at},
			`{"connection_id":"apc_1","organization_id":"org_1","member_id":"user-1","toolkit":"github","broker":"oryxa","reason":"removed_by_member","provider_revoked":true,"removed_at":"2026-09-28T12:30:00Z"}`,
		},
		// false is said, not left out: a key only its owner can revoke is
		// news the member needs.
		"removed, the key left to its owner": {
			ConnectionRemoved{ConnectionID: "apc_1", OrganizationID: "org_1", MemberID: "user-1", Toolkit: "github",
				Broker: BrokerOryxa, Reason: RemovedMemberLeft, ProviderRevoked: &kept, RemovedAt: at},
			`{"connection_id":"apc_1","organization_id":"org_1","member_id":"user-1","toolkit":"github","broker":"oryxa","reason":"member_left","provider_revoked":false,"removed_at":"2026-09-28T12:30:00Z"}`,
		},
		"a key replaced": {
			CredentialReplaced{ConnectionID: "apc_1", OrganizationID: "org_1", MemberID: "user-1", Toolkit: "github",
				Kind: "api_token", ReplacedAt: at},
			`{"connection_id":"apc_1","organization_id":"org_1","member_id":"user-1","toolkit":"github","kind":"api_token","replaced_at":"2026-09-28T12:30:00Z"}`,
		},
		"an organization's OAuth client changed": {
			OAuthClientChanged{OrganizationID: "org_1", Client: "google", ClientID: "123.apps.googleusercontent.com",
				ActorID: "user-3", ChangedAt: at},
			`{"organization_id":"org_1","client":"google","client_id":"123.apps.googleusercontent.com","actor_id":"user-3","changed_at":"2026-09-28T12:30:00Z"}`,
		},
	} {
		t.Run(name, func(t *testing.T) {
			encoded, err := json.Marshal(test.value)
			if err != nil {
				t.Fatal(err)
			}
			if string(encoded) != test.want {
				t.Fatalf("encoded\n got  %s\n want %s", encoded, test.want)
			}
		})
	}
}

func TestTheNewDetailTypesAreVersionOne(t *testing.T) {
	for _, detailType := range []string{CredentialReplacedDetailType, OAuthClientSavedDetailType, OAuthClientRemovedDetailType} {
		if len(detailType) < 8 || detailType[:5] != "apps." || detailType[len(detailType)-3:] != ".v1" {
			t.Errorf("%q is not an apps version-one detail type", detailType)
		}
	}
}
