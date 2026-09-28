package signin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/Shubham04-oss/oryxa-go/apps/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/apps/internal/integration"
	"github.com/Shubham04-oss/oryxa-go/internal/secrets"
)

func TestPutClientSealsTheSecretAndShowsOnlyAHint(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	const secret = "org-client-secret-wxyz"
	view, changed, err := h.service.PutClient(context.Background(), ClientInput{
		OrganizationID: testOrganization, Client: "acme", ClientID: "org-acme", ClientSecret: secret, ActorID: "admin_1",
	})
	if err != nil || !changed {
		t.Fatalf("PutClient() = %v, %v", changed, err)
	}
	if !view.SecretSet || view.SecretHint != "wxyz" || view.ClientID != "org-acme" || view.CreatedBy != "admin_1" {
		t.Fatalf("view = %+v", view)
	}
	for _, shown := range []any{view, fmt.Sprintf("%+v", view)} {
		encoded, _ := json.Marshal(shown)
		if strings.Contains(string(encoded), secret) {
			t.Fatalf("the view shows the secret: %s", encoded)
		}
	}
	row, err := h.store.LiveOAuthClient(context.Background(), testOrganization, "acme")
	if err != nil {
		t.Fatal(err)
	}
	opened, err := h.service.clientSecret(context.Background(), row)
	if err != nil || opened.Reveal() != secret {
		t.Fatalf("the sealed secret opens to %v, %v", opened, err)
	}
	if _, err := h.service.open(context.Background(), clientBinding("aoc_other", secretClientField), *row.SealedSecret); err == nil {
		t.Fatal("the secret opened as another client's")
	}

	// A short secret shows nothing at all.
	view, _, err = h.service.PutClient(context.Background(), ClientInput{
		OrganizationID: "org_2", Client: "acme", ClientID: "org-acme-2", ClientSecret: "short-secret",
	})
	if err != nil || view.SecretHint != "" || !view.SecretSet {
		t.Fatalf("a short secret's view = %+v, %v, want no hint", view, err)
	}
	if got, err := h.service.GetClient(context.Background(), "org_2", "acme"); err != nil || got.ID != view.ID {
		t.Fatalf("GetClient() = %+v, %v", got, err)
	}
	if _, err := h.service.GetClient(context.Background(), "org_3", "acme"); !errors.Is(err, ErrClientNotFound) || !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("GetClient() with none = %v, want ErrClientNotFound", err)
	}
}

func TestPutClientUpdatesTheSameClientAndReplacesAnother(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	put := func(clientID, secret string) (ClientView, bool) {
		t.Helper()
		view, changed, err := h.service.PutClient(context.Background(), ClientInput{
			OrganizationID: testOrganization, Client: "acme", ClientID: clientID, ClientSecret: secrets.Secret(secret),
		})
		if err != nil {
			t.Fatalf("PutClient(%s) = %v", clientID, err)
		}
		return view, changed
	}
	first, _ := put("org-acme", "org-client-secret-0001")
	// The same client, the same secret: nothing changes.
	if again, changed := put("org-acme", "org-client-secret-0001"); changed || again.ID != first.ID {
		t.Fatalf("saving it unchanged = %+v, changed %v", again, changed)
	}
	// The same client with no secret keeps its secret.
	if again, changed := put("org-acme", ""); changed || again.SecretHint != "0001" {
		t.Fatalf("saving it without a secret = %+v, changed %v", again, changed)
	}
	// The same client, a rotated secret: updated in place, so its
	// connections refresh with the new one.
	rotated, changed := put("org-acme", "org-client-secret-0002")
	if !changed || rotated.ID != first.ID || rotated.SecretHint != "0002" {
		t.Fatalf("a rotated secret = %+v, changed %v, want the same row", rotated, changed)
	}
	// Another client: a new live row, and the old one kept for the
	// connections made through it.
	replaced, changed := put("org-acme-v2", "org-client-secret-0003")
	if !changed || replaced.ID == first.ID {
		t.Fatalf("another client = %+v, want a new row", replaced)
	}
	old, err := h.store.OAuthClient(context.Background(), first.ID)
	if err != nil || old.Status != ClientRemoved || old.ClientID != "org-acme" {
		t.Fatalf("the replaced client = %+v, %v, want it kept and removed", old, err)
	}
	if secret, err := h.service.clientSecret(context.Background(), old); err != nil || secret.Reveal() != "org-client-secret-0002" {
		t.Fatalf("the replaced client's secret = %v, want it still usable", err)
	}

	removed, err := h.service.RemoveClient(context.Background(), testOrganization, "acme")
	if err != nil || !removed {
		t.Fatalf("RemoveClient() = %v, %v", removed, err)
	}
	if removed, _ := h.service.RemoveClient(context.Background(), testOrganization, "acme"); removed {
		t.Fatal("RemoveClient() removed something twice")
	}
	// With its own gone, the organization signs in through Oryxa's again.
	started := h.start("con_1", "acme")
	if !strings.Contains(started.AuthorizationURL, "client_id="+defaultClientID) {
		t.Fatalf("authorization URL = %s, want Oryxa's default client", started.AuthorizationURL)
	}
}

func TestPutClientRefusesWhatItCannotSave(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	cases := []struct {
		input ClientInput
		field string
		is    error
	}{
		{ClientInput{OrganizationID: testOrganization, Client: "zeta", ClientID: "x", ClientSecret: "y"}, "", domain.ErrNotFound},
		{ClientInput{OrganizationID: testOrganization, Client: "acme", ClientSecret: "org-client-secret-0001"}, "client_id", nil},
		{ClientInput{OrganizationID: testOrganization, Client: "acme", ClientID: "org-acme"}, "client_secret", nil},
		{ClientInput{OrganizationID: testOrganization, Client: "acme", ClientID: "org acme", ClientSecret: "x"}, "client_id", nil},
		{ClientInput{OrganizationID: testOrganization, Client: "acme", ClientID: "org-acme", ClientSecret: "x", Fields: map[string]string{"color": "blue"}}, "color", nil},
		{ClientInput{Client: "acme", RefuseDefault: true}, "", domain.ErrInvalid},
	}
	for _, tc := range cases {
		_, _, err := h.service.PutClient(context.Background(), tc.input)
		var failure *Failure
		switch {
		case tc.is != nil && !errors.Is(err, tc.is):
			t.Errorf("PutClient(%+v) = %v, want %v", tc.input, err, tc.is)
		case tc.is == nil && (!errors.As(err, &failure) || failure.Field != tc.field):
			t.Errorf("PutClient(%+v) = %v, want a failure naming %q", tc.input, err, tc.field)
		}
	}
}

func TestClientLevelFieldsReachTheTokenRequestAndTheAPI(t *testing.T) {
	t.Parallel()
	h := newHarness(t, func(base string) integration.Manifest {
		manifest := acmeManifest(base)
		manifest.Auth.Methods = manifest.Auth.Methods[:1]
		manifest.Auth.Methods[0].Fields = []integration.Field{
			{Name: "audience", Label: "API audience", Level: integration.LevelClient,
				Validate: integration.Validation{Required: true, Format: integration.FormatHTTPSURL},
				Use:      []integration.Use{{In: integration.InTokenRequest, Name: "audience"}}},
			{Name: "developer_key", Label: "Developer key", Secret: true, Level: integration.LevelClient,
				Use: []integration.Use{{In: integration.InHeader, Name: "X-Developer-Key"}}},
		}
		return manifest
	})
	h.app.set(func(a *stubApp) { a.clients["org-acme"] = "org-client-secret-0001" })
	_, _, err := h.service.PutClient(context.Background(), ClientInput{
		OrganizationID: testOrganization, Client: "acme", ClientID: "org-acme", ClientSecret: "org-client-secret-0001",
	})
	var failure *Failure
	if !errors.As(err, &failure) || failure.Field != "audience" {
		t.Fatalf("a client without its required field = %v, want a failure naming audience", err)
	}
	view, _, err := h.service.PutClient(context.Background(), ClientInput{
		OrganizationID: testOrganization, Client: "acme", ClientID: "org-acme", ClientSecret: "org-client-secret-0001",
		Fields: map[string]string{"audience": "https://api.acme.example", "developer_key": "dev-key-0123456789abcd"},
	})
	if err != nil || view.Fields["audience"] != "https://api.acme.example" || view.SecretFields["developer_key"] != "abcd" {
		t.Fatalf("PutClient() = %+v, %v", view, err)
	}
	if _, present := view.Fields["developer_key"]; present {
		t.Fatal("a secret client field is shown as a setting")
	}

	h.connect("con_1", "acme")
	if form := h.app.tokenForm(0); form.Get("audience") != "https://api.acme.example" {
		t.Fatalf("the token request = %v, want the client's audience", form)
	}
	session, err := h.service.Session(context.Background(), "con_1")
	if err != nil {
		t.Fatal(err)
	}
	if session.Settings["audience"] != "https://api.acme.example" {
		t.Fatalf("settings = %v", session.Settings)
	}
	if _, err := get(t, session, h.app.base+"/api/data"); err != nil {
		t.Fatal(err)
	}
	calls := h.app.seenCalls()
	if last := calls[len(calls)-1]; last.path != "/api/data" || last.header.Get("X-Developer-Key") != "dev-key-0123456789abcd" {
		t.Fatalf("the API saw %+v, want the client's developer key", last)
	}
}
