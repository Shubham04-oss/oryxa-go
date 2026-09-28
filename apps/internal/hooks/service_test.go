package hooks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Shubham04-oss/oryxa-go/apps/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/apps/internal/integration"
	"github.com/Shubham04-oss/oryxa-go/internal/secrets"
)

func TestCreateTakesEachEndpointsSecretFromItsSource(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	envelope := secrets.Envelope{Keys: h.keys, Service: "oryxa-apps"}

	connection := h.create(CreateInput{Toolkit: "github", Scope: integration.HookScopeConnection, OrganizationID: testOrg, ConnectionID: "apc_1"})
	if !idPattern.MatchString(connection.Endpoint.ID) || connection.URL != testBaseURL+PathPrefix+connection.Endpoint.ID {
		t.Fatalf("id %q, URL %q", connection.Endpoint.ID, connection.URL)
	}
	if len(connection.Hook.Secret) != 43 || connection.Hook.URL != connection.URL {
		t.Fatalf("a connection endpoint's Hook = %d-character secret for %q, want a generated 256-bit secret for its URL", len(connection.Hook.Secret), connection.Hook.URL)
	}
	stored := h.store.endpoint(t, connection.Endpoint.ID)
	sealed, _ := json.Marshal(stored.Secret.Current)
	if stored.Secret.Current == nil || stored.Secret.Version != 1 || strings.Contains(string(sealed), connection.Hook.Secret) {
		t.Fatal("a connection endpoint's secret was not kept sealed")
	}
	opened, err := envelope.Open(ctx, binding(connection.Endpoint.ID), *stored.Secret.Current)
	if err != nil || string(opened) != connection.Hook.Secret {
		t.Fatalf("opening the sealed secret = %v", err)
	}
	if _, err := envelope.Open(ctx, binding("aph_bbbbbbbbbbbbbbbbbbbbbbbb"), *stored.Secret.Current); err == nil {
		t.Fatal("the sealed secret opened as another endpoint's")
	}
	if stored.Scheme.Verify.Signature == nil || stored.Scheme.Verify.Signature.Header != "X-Hub-Signature-256" {
		t.Fatalf("the frozen scheme = %+v, want GitHub's webhook section", stored.Scheme)
	}
	second := h.create(CreateInput{Toolkit: "github", Scope: integration.HookScopeConnection, OrganizationID: testOrg, ConnectionID: "apc_1"})
	if second.Endpoint.ID == connection.Endpoint.ID || second.Hook.Secret == connection.Hook.Secret {
		t.Fatal("two endpoints share an id or a secret")
	}

	const entered = "  org-own-slack-signing-secret-0123  \n"
	organization := h.create(CreateInput{Toolkit: "slack", Scope: integration.HookScopeOrganization, OrganizationID: testOrg, Secret: entered})
	if organization.Hook.Secret != "" {
		t.Fatal("an admin's secret was handed back")
	}
	stored = h.store.endpoint(t, organization.Endpoint.ID)
	if opened, err := envelope.Open(ctx, binding(organization.Endpoint.ID), *stored.Secret.Current); err != nil || string(opened) != strings.TrimSpace(entered) {
		t.Fatalf("the organization's secret, opened = %q, %v; want what the admin entered, trimmed", opened, err)
	}

	for _, toolkit := range []string{"slack", "notion", "gmail"} {
		endpoint, _, err := h.service.DeploymentEndpoint(ctx, toolkit)
		if err != nil {
			t.Fatalf("%s's deployment endpoint = %v", toolkit, err)
		}
		if endpoint.Secret.Current != nil {
			t.Fatalf("%s's deployment endpoint stored a secret", toolkit)
		}
	}
	// However it is printed or encoded, a Hook keeps its secret to itself.
	for _, printed := range []string{fmt.Sprint(connection), fmt.Sprintf("%+v", connection), fmt.Sprintf("%#v", connection.Hook)} {
		if strings.Contains(printed, connection.Hook.Secret) {
			t.Fatal("a created endpoint printed its secret")
		}
	}
	encoded, _ := json.Marshal(connection)
	if strings.Contains(string(encoded), connection.Hook.Secret) || strings.Contains(string(encoded), "encrypted_data_key") {
		t.Fatalf("a created endpoint encoded its secret, or its sealed secret: %s", encoded)
	}
}

func TestCreateRefusesWhatAScopeDoesNotTake(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.create(CreateInput{Toolkit: "slack", Scope: integration.HookScopeOrganization, OrganizationID: testOrg, Secret: "org-own-slack-signing-secret-0123"})

	for name, test := range map[string]struct {
		input CreateInput
		want  error
	}{
		"a connection endpoint for the deployment's app":    {CreateInput{Toolkit: "slack", Scope: integration.HookScopeConnection, OrganizationID: testOrg, ConnectionID: "apc_1"}, domain.ErrInvalid},
		"a deployment endpoint for a per-connection app":    {CreateInput{Toolkit: "github", Scope: integration.HookScopeDeployment}, domain.ErrInvalid},
		"an organization endpoint for a per-connection app": {CreateInput{Toolkit: "github", Scope: integration.HookScopeOrganization, OrganizationID: testOrg, Secret: "some-secret-0123456789"}, domain.ErrInvalid},
		"an organization endpoint for signed pushes":        {CreateInput{Toolkit: "gmail", Scope: integration.HookScopeOrganization, OrganizationID: testOrg}, domain.ErrInvalid},
		"a connection endpoint without its connection":      {CreateInput{Toolkit: "github", Scope: integration.HookScopeConnection, OrganizationID: testOrg}, domain.ErrInvalid},
		"a connection endpoint without its organization":    {CreateInput{Toolkit: "github", Scope: integration.HookScopeConnection, ConnectionID: "apc_1"}, domain.ErrInvalid},
		"an organization endpoint without its organization": {CreateInput{Toolkit: "slack", Scope: integration.HookScopeOrganization, Secret: "some-secret-0123456789"}, domain.ErrInvalid},
		"an organization endpoint with a connection":        {CreateInput{Toolkit: "slack", Scope: integration.HookScopeOrganization, OrganizationID: "org_2", ConnectionID: "apc_1", Secret: "some-secret-0123456789"}, domain.ErrInvalid},
		"a deployment endpoint with an organization":        {CreateInput{Toolkit: "slack", Scope: integration.HookScopeDeployment, OrganizationID: testOrg}, domain.ErrInvalid},
		"a secret offered for a connection endpoint":        {CreateInput{Toolkit: "github", Scope: integration.HookScopeConnection, OrganizationID: testOrg, ConnectionID: "apc_1", Secret: "chosen-by-someone-0123"}, domain.ErrInvalid},
		"a secret offered for a deployment endpoint":        {CreateInput{Toolkit: "slack", Scope: integration.HookScopeDeployment, Secret: "chosen-by-someone-0123"}, domain.ErrInvalid},
		"a secret offered for an app that sends its own":    {CreateInput{Toolkit: "notion", Scope: integration.HookScopeOrganization, OrganizationID: "org_2", Secret: "chosen-by-someone-0123"}, domain.ErrInvalid},
		"an organization endpoint without its secret":       {CreateInput{Toolkit: "slack", Scope: integration.HookScopeOrganization, OrganizationID: "org_2"}, domain.ErrInvalid},
		"an admin's secret that is too short":               {CreateInput{Toolkit: "slack", Scope: integration.HookScopeOrganization, OrganizationID: "org_2", Secret: "short"}, domain.ErrInvalid},
		"an admin's secret with a space in it":              {CreateInput{Toolkit: "slack", Scope: integration.HookScopeOrganization, OrganizationID: "org_2", Secret: "two words secret-0123"}, domain.ErrInvalid},
		"a second endpoint for the organization's own app":  {CreateInput{Toolkit: "slack", Scope: integration.HookScopeOrganization, OrganizationID: testOrg, Secret: "another-secret-0123456"}, domain.ErrConflict},
		"a scope nobody knows":                              {CreateInput{Toolkit: "github", Scope: "team", OrganizationID: testOrg}, domain.ErrInvalid},
		"a toolkit that is not Oryxa's own":                 {CreateInput{Toolkit: "jira", Scope: integration.HookScopeDeployment}, domain.ErrNotFound},
		"a toolkit that is not a slug":                      {CreateInput{Toolkit: "../github", Scope: integration.HookScopeDeployment}, domain.ErrInvalid},
	} {
		if _, err := h.service.Create(ctx, test.input); !errors.Is(err, test.want) {
			t.Errorf("%s = %v, want %v", name, err, test.want)
		}
	}
	// An organization with its own Notion integration gets an endpoint whose
	// secret Notion will send.
	own := h.create(CreateInput{Toolkit: "notion", Scope: integration.HookScopeOrganization, OrganizationID: "org_2"})
	if own.Endpoint.Secret.Current != nil || own.Hook.Secret != "" {
		t.Fatal("an endpoint for an app that sends its secret was given one")
	}
}

func TestAnEndpointsSchemeIsFrozenWhenItIsMade(t *testing.T) {
	h := newHarness(t)
	created := h.connectionEndpoint("github", "apc_1", "mem_1", githubTrigger)
	// The manifest changes where the signature is read from.
	changed := h.fakes["github"].manifest
	changed.Webhook = &integration.Webhook{
		Scope: integration.HookScopeConnection,
		Verify: integration.Verify{
			Type: integration.VerifyHMACSHA256, Signature: &integration.Locator{Header: "X-Other-Signature"},
			Encoding: integration.EncodingHex, Signed: "{body}",
		},
		DeliveryID: integration.Locator{Header: "X-GitHub-Delivery"},
	}
	h.fakes["github"].manifest = changed

	body := []byte(`{"id":"issue-1"}`)
	if response := h.deliver(created.Endpoint.ID, githubDelivery(created.Hook.Secret, "d-1", "issues", body), body); response.Code != http.StatusOK {
		t.Fatalf("a delivery signed as the endpoint's frozen scheme says = %d, want 200", response.Code)
	}
	later := h.create(CreateInput{Toolkit: "github", Scope: integration.HookScopeConnection, OrganizationID: testOrg, ConnectionID: "apc_1"})
	if later.Endpoint.Scheme.Verify.Signature.Header != "X-Other-Signature" {
		t.Fatal("a new endpoint did not take the manifest's new scheme")
	}
}

func TestRenewalHandsOverAndADeletedReplacementHandsBack(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	created := h.connectionEndpoint("googledrive", "apc_1", "mem_1", driveTrigger)
	lapse := testStart.Add(time.Hour)
	if err := h.service.SetRemote(ctx, created.Endpoint.ID, "channel-1", &lapse); err != nil {
		t.Fatal(err)
	}
	due, err := h.service.Due(ctx, 2*time.Hour, 0)
	if err != nil || len(due) != 1 || due[0].ID != created.Endpoint.ID || due[0].RemoteID != "channel-1" {
		t.Fatalf("due = %v, %v; want the channel that lapses within the hour", due, err)
	}
	if due, _ := h.service.Due(ctx, 30*time.Minute, 0); len(due) != 0 {
		t.Fatalf("due within 30 minutes = %v, want nothing yet", due)
	}

	renewed, err := h.service.Renew(ctx, created.Endpoint.ID)
	if err != nil {
		t.Fatal(err)
	}
	old := h.store.endpoint(t, created.Endpoint.ID)
	if old.Status != StatusReplaced || old.ReplacedBy != renewed.Endpoint.ID {
		t.Fatalf("the renewed endpoint = %s replaced by %q, want replaced by %s", old.Status, old.ReplacedBy, renewed.Endpoint.ID)
	}
	if renewed.Endpoint.ConnectionID != "apc_1" || renewed.Endpoint.OrganizationID != testOrg || renewed.Endpoint.ExpiresAt != nil {
		t.Fatalf("the renewal = %+v, want the same connection, not yet subscribed", renewed.Endpoint)
	}
	if due, _ := h.service.Due(ctx, 2*time.Hour, 0); len(due) != 0 {
		t.Fatalf("due after renewing = %v, want nothing: the old one is handed over", due)
	}
	if _, err := h.service.Renew(ctx, created.Endpoint.ID); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("renewing it twice = %v, want conflict", err)
	}

	// Subscribing the new endpoint failed, so it is deleted, and the old one
	// is in charge again, and due again.
	if err := h.service.Delete(ctx, renewed.Endpoint.ID); err != nil {
		t.Fatal(err)
	}
	if old := h.store.endpoint(t, created.Endpoint.ID); old.Status != StatusActive || old.ReplacedBy != "" {
		t.Fatalf("after its replacement was deleted, the old endpoint = %s replaced by %q, want active", old.Status, old.ReplacedBy)
	}
	if due, _ := h.service.Due(ctx, 2*time.Hour, 0); len(due) != 1 {
		t.Fatalf("due after the failed renewal = %v, want the old endpoint again", due)
	}
	if err := h.service.Delete(ctx, renewed.Endpoint.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("deleting it twice = %v, want not found", err)
	}

	slack, _, err := h.service.DeploymentEndpoint(ctx, "slack")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.service.Renew(ctx, slack.ID); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("renewing the deployment's endpoint = %v, want invalid", err)
	}
}

func TestSweepDeletesLapsedEndpointsAndForgetsRotatedOutSecrets(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	lapsing := h.connectionEndpoint("googledrive", "apc_1", "mem_1", driveTrigger)
	lapse := testStart.Add(time.Hour)
	if err := h.service.SetRemote(ctx, lapsing.Endpoint.ID, "channel-1", &lapse); err != nil {
		t.Fatal(err)
	}
	rotating := h.connectionEndpoint("github", "apc_2", "mem_2", githubTrigger)
	if _, err := h.service.Rotate(ctx, rotating.Endpoint.ID, ""); err != nil {
		t.Fatal(err)
	}

	h.clock.Set(lapse.Add(-time.Second))
	if swept, err := h.service.SweepExpired(ctx); err != nil || swept != 0 {
		t.Fatalf("sweeping before anything lapsed = %d, %v", swept, err)
	}
	h.clock.Set(lapse)
	if swept, err := h.service.SweepExpired(ctx); err != nil || swept != 1 {
		t.Fatalf("sweeping once the channel lapsed = %d, %v; want 1", swept, err)
	}
	if _, err := h.service.Endpoint(ctx, lapsing.Endpoint.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("the lapsed endpoint = %v, want deleted", err)
	}
	if h.store.endpoint(t, rotating.Endpoint.ID).Secret.Previous == nil {
		t.Fatal("a previous secret was forgotten inside its overlap")
	}
	h.clock.Set(testStart.Add(RotationOverlap))
	if _, err := h.service.SweepExpired(ctx); err != nil {
		t.Fatal(err)
	}
	if rotated := h.store.endpoint(t, rotating.Endpoint.ID); rotated.Secret.Previous != nil || rotated.Secret.PreviousUntil != nil {
		t.Fatal("a previous secret outlived its overlap")
	}
}

func TestDeleteForConnectionAnswersWhatToUnsubscribe(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	first := h.connectionEndpoint("github", "apc_1", "mem_1", githubTrigger)
	h.clock.Advance(time.Second)
	second := h.connectionEndpoint("github", "apc_1", "mem_1", githubTrigger)
	other := h.connectionEndpoint("github", "apc_2", "mem_2", githubTrigger)
	for i, created := range []Created{first, second, other} {
		if err := h.service.SetRemote(ctx, created.Endpoint.ID, fmt.Sprint("hook-", i), nil); err != nil {
			t.Fatal(err)
		}
	}
	listed, err := h.service.EndpointsForConnection(ctx, "apc_1")
	if err != nil || len(listed) != 2 {
		t.Fatalf("apc_1's endpoints = %v, %v", listed, err)
	}
	deleted, err := h.service.DeleteForConnection(ctx, "apc_1")
	if err != nil || len(deleted) != 2 || deleted[0].RemoteID != "hook-0" || deleted[1].RemoteID != "hook-1" {
		t.Fatalf("deleted %v, %v; want both of apc_1's, with their subscriptions", deleted, err)
	}
	body := []byte(`{"id":"x"}`)
	if response := h.deliver(first.Endpoint.ID, githubDelivery(first.Hook.Secret, "d-1", "issues", body), body); response.Code != http.StatusNotFound {
		t.Fatalf("a delivery to a removed connection's endpoint = %d, want 404", response.Code)
	}
	if response := h.deliver(other.Endpoint.ID, githubDelivery(other.Hook.Secret, "d-1", "issues", body), body); response.Code != http.StatusOK {
		t.Fatalf("another connection's endpoint = %d, want 200", response.Code)
	}
}

func TestAnOrganizationRotatesToTheSecretItsAdminEntered(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	const first, second = "org-own-slack-signing-secret-first", "org-own-slack-signing-secret-second"
	created := h.create(CreateInput{Toolkit: "slack", Scope: integration.HookScopeOrganization, OrganizationID: testOrg, Secret: first})
	if _, err := h.service.Rotate(ctx, created.Endpoint.ID, ""); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("rotating an organization's endpoint with no secret = %v, want invalid", err)
	}
	rotated, err := h.service.Rotate(ctx, created.Endpoint.ID, second)
	if err != nil || rotated.Hook.Secret != "" {
		t.Fatalf("rotating = %v, %v; want the admin's secret kept and not handed back", rotated.Hook, err)
	}
	found, err := h.service.OrganizationEndpoint(ctx, testOrg, "slack")
	if err != nil || found.ID != created.Endpoint.ID || found.Secret.Version != 2 {
		t.Fatalf("the organization's endpoint = %+v, %v", found, err)
	}
	h.subscribeAccount(Subscription{TriggerID: "apt_a", ConnectionID: "apc_a", OrganizationID: testOrg, MemberID: "mem_a", Toolkit: "slack", Trigger: slackTrigger, AccountRef: "U_A"})
	for _, secret := range []string{first, second} {
		body := []byte(`{"id":"Ev-` + secret + `","account":"U_A"}`)
		if response := h.deliver(created.Endpoint.ID, slackDelivery(secret, testStart, body), body); response.Code != http.StatusOK {
			t.Fatalf("a delivery during the overlap = %d", response.Code)
		}
	}
	if _, err := h.service.OrganizationEndpoint(ctx, "", "slack"); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("an organization endpoint for no organization = %v", err)
	}
}

func TestSetRemoteRecordsTheAppsSubscription(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	created := h.connectionEndpoint("outlook", "apc_1", "mem_1", graphTrigger)
	lapse := testStart.Add(72 * time.Hour).In(time.FixedZone("elsewhere", 5*3600))
	if err := h.service.SetRemote(ctx, created.Endpoint.ID, "7f105c7d-2dc5-4530-97cd-4e7ae6534c07", &lapse); err != nil {
		t.Fatal(err)
	}
	stored := h.store.endpoint(t, created.Endpoint.ID)
	if stored.RemoteID != "7f105c7d-2dc5-4530-97cd-4e7ae6534c07" || !stored.ExpiresAt.Equal(lapse) || stored.ExpiresAt.Location() != time.UTC {
		t.Fatalf("stored %q until %v", stored.RemoteID, stored.ExpiresAt)
	}
	if err := h.service.SetRemote(ctx, "aph_aaaaaaaaaaaaaaaaaaaaaaaa", "x", nil); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("an unknown endpoint = %v", err)
	}
	if err := h.service.SetRemote(ctx, created.Endpoint.ID, strings.Repeat("x", maxRemoteID+1), nil); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("an overlong subscription id = %v", err)
	}
}

func TestDeploymentEndpointSurvivesARaceToMakeIt(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	first, _, err := h.service.DeploymentEndpoint(ctx, "slack")
	if err != nil {
		t.Fatal(err)
	}
	// Another replica's endpoint exists, but this one looked before it did.
	raced := &racingStore{memoryStore: h.store, misses: 1}
	h.service.store = raced
	second, url, err := h.service.DeploymentEndpoint(ctx, "slack")
	if err != nil || second.ID != first.ID || url != h.service.URL(first.ID) {
		t.Fatalf("losing the race = %s, %v; want the endpoint the other replica made, %s", second.ID, err, first.ID)
	}
}

// racingStore answers that no active endpoint exists, a given number of
// times, as a replica that looked just before another made one would see.
type racingStore struct {
	*memoryStore
	misses int
}

func (r *racingStore) ActiveHookEndpoint(ctx context.Context, scope, organizationID, toolkit string) (Endpoint, error) {
	if r.misses > 0 {
		r.misses--
		return Endpoint{}, domain.ErrNotFound
	}
	return r.memoryStore.ActiveHookEndpoint(ctx, scope, organizationID, toolkit)
}

func TestNewChecksWhatHooksNeeds(t *testing.T) {
	h := newHarness(t)
	full := Dependencies{Store: h.store, Sealer: secrets.Envelope{Keys: h.keys, Service: "oryxa-apps"}, Integrations: fakeRegistry{}, Subscriptions: h.subscriptions}
	for name, test := range map[string]struct {
		change  func(*Dependencies)
		baseURL string
	}{
		"no store":               {func(d *Dependencies) { d.Store = nil }, testBaseURL},
		"no sealer":              {func(d *Dependencies) { d.Sealer = nil }, testBaseURL},
		"no integrations":        {func(d *Dependencies) { d.Integrations = nil }, testBaseURL},
		"no subscriptions":       {func(d *Dependencies) { d.Subscriptions = nil }, testBaseURL},
		"no base URL":            {func(*Dependencies) {}, ""},
		"a relative URL":         {func(*Dependencies) {}, "/v1"},
		"a URL with a query":     {func(*Dependencies) {}, "https://api.oryxa.test?x=1"},
		"an ftp URL":             {func(*Dependencies) {}, "ftp://api.oryxa.test"},
		"a URL with credentials": {func(*Dependencies) {}, "https://user:pass@api.oryxa.test"},
	} {
		dependencies := full
		test.change(&dependencies)
		if _, err := New(dependencies, Config{PublicBaseURL: test.baseURL}); err == nil {
			t.Errorf("%s: New succeeded", name)
		}
	}
	service, err := New(full, Config{PublicBaseURL: "https://api.oryxa.test/"})
	if err != nil {
		t.Fatal(err)
	}
	if got := service.URL("aph_x"); got != "https://api.oryxa.test/v1/apps/hooks/aph_x" {
		t.Fatalf("URL = %s", got)
	}
}

type fakeRegistry struct{}

func (fakeRegistry) Lookup(string) (integration.Integration, bool) { return nil, false }
