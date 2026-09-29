package application

import (
	"context"
	"errors"
	"testing"

	"github.com/Shubham04-oss/oryxa-go/apps/internal/authclient"
	"github.com/Shubham04-oss/oryxa-go/apps/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/apps/internal/integration"
	"github.com/Shubham04-oss/oryxa-go/internal/composio"
	"github.com/Shubham04-oss/oryxa-go/internal/workspace/policy"
)

type fakePolicySource struct {
	settings policy.Settings
	err      error
}

func (source *fakePolicySource) Get(context.Context, string) (policy.Settings, error) {
	return source.settings, source.err
}

func TestWorkspaceToolOfferChecksConnectionsAndExecution(t *testing.T) {
	h := newHarness(t)
	h.readTool()
	h.activeConnection("apc_1", "user-1", "github", "ca_1")
	h.composio.toolkits["github"] = composio.Toolkit{Slug: "github", ComposioManagedAuthSchemes: []string{"OAUTH2"}}
	offer := []string{"slack"}
	source := &fakePolicySource{settings: policy.Settings{Offer: &offer}}
	h.service.policy = source
	ctx := context.Background()
	if _, _, err := h.service.Connect(ctx, memberToken, "org_1", ConnectInput{Toolkit: "github"}); !errors.Is(err, domain.ErrForbidden) || len(h.composio.links) != 0 {
		t.Fatalf("restricted connect = %v, links = %d", err, len(h.composio.links))
	}
	if _, err := h.service.SearchTools(ctx, memberToken, "org_1", "github", "", 0); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("restricted search = %v", err)
	}
	if _, err := h.service.RunTool(ctx, memberToken, "org_1", RunToolInput{Tool: "GITHUB_LIST_ISSUES"}); !errors.Is(err, domain.ErrForbidden) || len(h.composio.executed) != 0 {
		t.Fatalf("restricted run = %v, executions = %d", err, len(h.composio.executed))
	}
	if _, err := h.service.Proxy(ctx, memberToken, "org_1", ProxyInput{Toolkit: "github", Endpoint: "/user"}); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("restricted read = %v", err)
	}
	if _, err := h.service.CreateTrigger(ctx, memberToken, "org_1", TriggerInput{ConnectionID: "apc_1", Trigger: "GITHUB_COMMIT_EVENT"}); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("restricted subscription = %v", err)
	}
	if _, err := h.service.ConnectionTargets(ctx, memberToken, "org_1", "apc_1"); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("restricted targets = %v", err)
	}
	if len(h.store.actions) != 0 || len(h.store.triggers) != 0 {
		t.Fatalf("restricted app caused side effects: actions=%d triggers=%d", len(h.store.actions), len(h.store.triggers))
	}
}

func TestWorkspaceToolOfferDropsExistingTriggerDeliveries(t *testing.T) {
	h := newHarness(t)
	connection := h.activeConnection("apc_1", "user-1", "github", "ca_1")
	h.activeTrigger("apt_1", connection.ID, "ti_1")
	offer := []string{}
	h.service.policy = &fakePolicySource{settings: policy.Settings{Offer: &offer}}
	header, body := delivery(t, "whsec_test", "wh_1", testClock, triggerMessage("wh_1", "ti_1", "ca_1", `{}`))
	if err := h.service.ReceiveWebhook(context.Background(), header, body); err != nil {
		t.Fatal(err)
	}
	if len(h.store.events) != 0 || len(h.store.handled) != 1 {
		t.Fatalf("restricted event was not acknowledged and dropped: events=%d handled=%d", len(h.store.events), len(h.store.handled))
	}
}

func TestWorkspaceAutoSubscribeDefaultAndPolicyFailure(t *testing.T) {
	h := newHarness(t)
	h.composio.toolkits["github"] = composio.Toolkit{Slug: "github", ComposioManagedAuthSchemes: []string{"OAUTH2"}}
	off := false
	source := &fakePolicySource{settings: policy.Settings{AutoSubscribe: &off}}
	h.service.policy = source
	connection, _, err := h.service.Connect(context.Background(), memberToken, "org_1", ConnectInput{Toolkit: "github"})
	if err != nil || connection.AutoSubscribe {
		t.Fatalf("org default = %+v, %v", connection, err)
	}
	source.err = errors.New("policy store down")
	if _, err := h.service.SearchTools(context.Background(), memberToken, "org_1", "github", "", 0); !errors.Is(err, domain.ErrUnavailable) {
		t.Fatalf("policy read failure = %v", err)
	}
	if _, _, err := h.service.Connect(context.Background(), otherToken, "org_1", ConnectInput{Toolkit: "github"}); !errors.Is(err, domain.ErrUnavailable) {
		t.Fatalf("policy read failure on connect = %v", err)
	}
}

func TestOfferedToolsRequiresMembershipAndKeepsEmptyDistinct(t *testing.T) {
	h := newHarness(t)
	offer := []string{}
	off := false
	h.service.policy = &fakePolicySource{settings: policy.Settings{Offer: &offer, AutoSubscribe: &off}}
	if _, err := h.service.OfferedTools(context.Background(), memberToken, "org_2"); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("other organization = %v", err)
	}
	result, err := h.service.OfferedTools(context.Background(), memberToken, "org_1")
	if err != nil || result.Offer == nil || len(*result.Offer) != 0 || result.AutoSubscribeDefault {
		t.Fatalf("empty offer = %+v, %v", result, err)
	}
}

func TestEmployeeConnectionPreferenceAllowsAdminsAndExistingMemberUse(t *testing.T) {
	h := newHarness(t)
	h.readTool()
	h.activeConnection("apc_1", "user-1", "github", "ca_1")
	h.composio.toolkits["github"] = composio.Toolkit{Slug: "github", ComposioManagedAuthSchemes: []string{"OAUTH2"}}
	off := false
	h.service.policy = &fakePolicySource{settings: policy.Settings{EmployeeConnections: &off}}
	ctx := context.Background()
	if _, _, err := h.service.Connect(ctx, memberToken, "org_1", ConnectInput{Toolkit: "github"}); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("member connect = %v", err)
	}
	if _, _, err := h.service.Connect(ctx, adminToken, "org_1", ConnectInput{Toolkit: "github"}); err != nil {
		t.Fatalf("admin connect = %v", err)
	}
	if _, err := h.service.SearchTools(ctx, memberToken, "org_1", "github", "", 0); err != nil {
		t.Fatalf("existing connection tool access = %v", err)
	}
	result, err := h.service.OfferedTools(ctx, memberToken, "org_1")
	if err != nil || result.EmployeeConnections || result.CanConnect {
		t.Fatalf("connection preference = %+v, %v", result, err)
	}
	adminOffer, err := h.service.OfferedTools(ctx, adminToken, "org_1")
	if err != nil || !adminOffer.CanConnect {
		t.Fatalf("admin connection preference = %+v, %v", adminOffer, err)
	}
}

func TestEmployeeConnectionPreferenceAlsoAppliesToOryxaSignin(t *testing.T) {
	h := newOryxaHarness(t, "github")
	off := false
	h.service.policy = &fakePolicySource{settings: policy.Settings{EmployeeConnections: &off}}
	input := ConnectInput{Toolkit: "github", Host: "app.oryxa.test"}
	if _, err := h.service.ConnectOryxa(context.Background(), memberToken, "org_1", input); !errors.Is(err, domain.ErrForbidden) || len(h.signin.starts) != 0 {
		t.Fatalf("member Oryxa sign-in = %v, starts = %d", err, len(h.signin.starts))
	}
	if _, err := h.service.ConnectOryxa(context.Background(), adminToken, "org_1", input); err != nil || len(h.signin.starts) != 1 {
		t.Fatalf("admin Oryxa sign-in = %v, starts = %d", err, len(h.signin.starts))
	}
}

func TestWorkspacePolicyChecksCredentialReplacementBeforeProviderAccess(t *testing.T) {
	noApps := []string{}
	off := false
	for name, test := range map[string]struct {
		settings  policy.Settings
		policyErr error
		caller    authclient.Credentials
		memberID  string
		want      error
	}{
		"unoffered app for member":            {settings: policy.Settings{Offer: &noApps}, caller: memberToken, memberID: "user-1", want: domain.ErrForbidden},
		"unoffered app for admin":             {settings: policy.Settings{Offer: &noApps}, caller: adminToken, memberID: "user-3", want: domain.ErrForbidden},
		"employee connections disabled":       {settings: policy.Settings{EmployeeConnections: &off}, caller: memberToken, memberID: "user-1", want: domain.ErrForbidden},
		"policy unavailable":                  {policyErr: errors.New("policy store down"), caller: memberToken, memberID: "user-1", want: domain.ErrUnavailable},
		"admin retains own connection access": {settings: policy.Settings{EmployeeConnections: &off}, caller: adminToken, memberID: "user-3"},
	} {
		t.Run(name, func(t *testing.T) {
			h := newOryxaHarness(t, "github")
			h.oryxaConnection("apc_key", test.memberID, "github", domain.ConnectionActive, integration.KindAPIToken)
			h.service.policy = &fakePolicySource{settings: test.settings, err: test.policyErr}
			_, err := h.service.ReplaceCredential(context.Background(), test.caller, "org_1", "apc_key", ReplaceCredentialInput{
				Fields: map[string]string{"token": "replacement-key"},
			})
			if !errors.Is(err, test.want) {
				t.Fatalf("replacement error = %v, want %v", err, test.want)
			}
			if test.want != nil {
				if len(h.signin.saves) != 0 || len(h.store.events) != 0 {
					t.Fatalf("blocked replacement contacted provider or published events: saves=%d events=%d", len(h.signin.saves), len(h.store.events))
				}
			} else if len(h.signin.saves) != 1 {
				t.Fatalf("allowed replacement saves = %d", len(h.signin.saves))
			}
		})
	}
}

func TestWorkspacePolicyFailuresKeepTheirCause(t *testing.T) {
	h := newHarness(t)
	cause := errors.New("stored policy decode failed at tools.offer")
	h.service.policy = &fakePolicySource{err: cause}
	ctx := context.Background()
	for name, check := range map[string]func() error{
		"offer":               func() error { _, err := h.service.OfferedTools(ctx, memberToken, "org_1"); return err },
		"toolkit":             func() error { return h.service.allowToolkit(ctx, "org_1", "github") },
		"employee preference": func() error { return h.service.allowEmployeeConnect(ctx, "org_1", false) },
		"auto subscribe":      func() error { _, err := h.service.defaultAutoSubscribe(ctx, "org_1"); return err },
	} {
		if err := check(); !errors.Is(err, domain.ErrUnavailable) || !errors.Is(err, cause) {
			t.Errorf("%s lost classification or cause: %v", name, err)
		}
	}
}
