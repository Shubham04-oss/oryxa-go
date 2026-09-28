package signin

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/Shubham04-oss/oryxa-go/apps/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/apps/internal/integration"
)

const testToken = "pat-0123456789abcdef"

func (h *harness) saveToken(connectionID, token string) (Saved, error) {
	h.t.Helper()
	return h.service.SaveCredential(context.Background(), CredentialRequest{
		OrganizationID: testOrganization, ConnectionID: connectionID, Toolkit: "acme",
		Kind: integration.KindAPIToken, Fields: map[string]string{"token": token},
	})
}

func TestSaveCredentialProvesAKeyBeforeKeepingIt(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.app.set(func(a *stubApp) { a.apiToken = testToken })
	h.store.addConnection("con_1", testOrganization, testMember, "acme", "pending")

	_, err := h.saveToken("con_1", "pat-wrong-0000000000")
	var failure *Failure
	if !errors.As(err, &failure) || failure.Reason != ReasonInvalidCredentials || !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("a refused key = %v, want invalid_credentials", err)
	}
	if strings.Contains(err.Error(), "pat-wrong") {
		t.Fatal("the refusal quotes the key")
	}
	if _, kept := h.store.stored("con_1"); kept {
		t.Fatal("a key the app refused was kept")
	}

	saved, err := h.saveToken("con_1", "  "+testToken+"\t")
	if err != nil {
		t.Fatalf("SaveCredential() = %v", err)
	}
	if saved.Version != 1 || saved.Kind != integration.KindAPIToken || saved.Account != (Account{Label: "octocat", Ref: "42"}) {
		t.Fatalf("saved = %+v", saved)
	}
	credential, _ := h.store.stored("con_1")
	token, err := h.service.open(context.Background(), credentialBinding("con_1", "token"), credential.Sealed["token"])
	if err != nil || token.Reveal() != testToken {
		t.Fatalf("the kept key opens to %v, %v", token, err)
	}
	if len(credential.Sealed) != 1 || credential.Settings[integration.SettingAccountLabel] != "octocat" {
		t.Fatalf("credential = %+v", credential)
	}

	// Replacing the key moves the version on.
	h.store.setStatus("con_1", "active")
	if saved, err := h.saveToken("con_1", testToken); err != nil || saved.Version != 2 {
		t.Fatalf("replacing the key = %+v, %v, want version 2", saved, err)
	}
	// An app that is down keeps nothing, and says so apart from a refusal.
	h.app.set(func(a *stubApp) { a.meStatus = http.StatusServiceUnavailable })
	if _, err := h.saveToken("con_1", testToken); !errors.As(err, &failure) || failure.Reason != ReasonUnavailable {
		t.Fatalf("an app that is down = %v, want unavailable", err)
	}
}

func TestSaveCredentialChecksFieldsWithoutQuotingThem(t *testing.T) {
	t.Parallel()
	h := newHarness(t, func(base string) integration.Manifest {
		manifest := acmeManifest(base)
		manifest.Auth.Methods[1].Fields = []integration.Field{
			{Name: "region", Label: "Region", Level: integration.LevelConnection,
				Validate: integration.Validation{Required: true, Enum: []string{"us", "eu"}}, Use: []integration.Use{{In: integration.InHeader, Name: "X-Region"}}},
			{Name: "tenant", Label: "Tenant", Level: integration.LevelConnection,
				Validate: integration.Validation{Format: integration.FormatUUID}, Use: []integration.Use{{In: integration.InHeader, Name: "X-Tenant"}}},
		}
		return manifest
	})
	h.store.addConnection("con_1", testOrganization, testMember, "acme", "pending")
	cases := []struct {
		fields map[string]string
		field  string
	}{
		{map[string]string{"region": "us"}, "token"},
		{map[string]string{"token": testToken}, "region"},
		{map[string]string{"token": testToken, "region": "mars-secret-value"}, "region"},
		{map[string]string{"token": testToken, "region": "us", "tenant": "not-a-uuid-secret-value"}, "tenant"},
		{map[string]string{"token": testToken, "region": "us", "color": "blue-secret-value"}, "color"},
		{map[string]string{"token": testToken, "region": "us", "Not A Field": "secret-value"}, ""},
		{map[string]string{"token": "pat\nsecret-value", "region": "us"}, "token"},
		{map[string]string{"token": strings.Repeat("x", 4097), "region": "us"}, "token"},
	}
	for _, tc := range cases {
		_, err := h.service.SaveCredential(context.Background(), CredentialRequest{
			OrganizationID: testOrganization, ConnectionID: "con_1", Toolkit: "acme", Kind: integration.KindAPIToken, Fields: tc.fields,
		})
		var failure *Failure
		if !errors.As(err, &failure) || failure.Reason != ReasonInvalidCredentials || failure.Field != tc.field {
			t.Errorf("fields %v = %v, want invalid_credentials naming %q", tc.fields, err, tc.field)
			continue
		}
		if strings.Contains(err.Error(), "secret-value") {
			t.Errorf("the refusal quotes a value: %v", err)
		}
	}
	if len(h.app.seenCalls()) != 0 {
		t.Fatal("the app was called with fields that break their rules")
	}
}

func TestSaveCredentialRefusesTheWrongConnectionOrKind(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.app.set(func(a *stubApp) { a.apiToken = testToken })
	h.connect("con_oauth", "acme")
	// The connection signed in with OAuth: a key is another kind.
	if _, err := h.saveToken("con_oauth", testToken); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("a key over an OAuth credential = %v, want ErrConflict", err)
	}
	for _, status := range []string{"removed", "failed", "expired"} {
		h.store.addConnection("con_"+status, testOrganization, testMember, "acme", status)
		if _, err := h.saveToken("con_"+status, testToken); !errors.Is(err, domain.ErrConflict) {
			t.Errorf("a key for a %s connection = %v, want ErrConflict", status, err)
		}
	}
	h.store.addConnection("con_elsewhere", "org_2", testMember, "acme", "pending")
	if _, err := h.saveToken("con_elsewhere", testToken); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("a key for another organization's connection = %v, want ErrConflict", err)
	}
	if _, err := h.service.SaveCredential(context.Background(), CredentialRequest{
		OrganizationID: testOrganization, ConnectionID: "con_new", Toolkit: "acme", Kind: integration.KindOAuth2Code,
	}); !errors.Is(err, ErrUnsupportedKind) {
		t.Fatalf("SaveCredential() for oauth2_code = %v, want ErrUnsupportedKind", err)
	}
}

func TestSaveCredentialTakesAPassword(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.app.set(func(a *stubApp) { a.username, a.password = "ada@acme.example", "correct horse battery" })
	h.store.addConnection("con_1", testOrganization, testMember, "acme", "pending")
	saved, err := h.service.SaveCredential(context.Background(), CredentialRequest{
		OrganizationID: testOrganization, ConnectionID: "con_1", Toolkit: "acme", Kind: integration.KindBasic,
		Fields: map[string]string{"username": "ada@acme.example", "password": "correct horse battery"},
	})
	if err != nil || saved.Account.Label != "octocat" {
		t.Fatalf("SaveCredential() = %+v, %v", saved, err)
	}
	credential, _ := h.store.stored("con_1")
	if credential.Settings["username"] != "ada@acme.example" || len(credential.Sealed) != 1 {
		t.Fatalf("credential = %+v: the username is a setting, the password sealed", credential)
	}
	h.store.setStatus("con_1", "active")
	session, err := h.service.Session(context.Background(), "con_1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := session.Token(context.Background()); !errors.Is(err, ErrUnsupportedKind) {
		t.Fatalf("Token() for a password = %v, want ErrUnsupportedKind", err)
	}
}
