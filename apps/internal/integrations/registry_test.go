package integrations

import (
	"reflect"
	"slices"
	"testing"

	"github.com/Shubham04-oss/oryxa-go/apps/internal/integration"
	"github.com/Shubham04-oss/oryxa-go/apps/internal/integrations/gmail"
	"github.com/Shubham04-oss/oryxa-go/apps/internal/integrations/googlecalendar"
	"github.com/Shubham04-oss/oryxa-go/apps/internal/integrations/googledrive"
)

func TestEveryShippedManifestValidates(t *testing.T) {
	t.Parallel()
	for _, shipped := range All() {
		manifest := shipped.Manifest()
		t.Run(manifest.Toolkit, func(t *testing.T) {
			t.Parallel()
			if err := manifest.Validate(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestTheRegistryHoldsEveryShippedToolkit(t *testing.T) {
	t.Parallel()
	registry, err := NewRegistry()
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"github", "gmail", "googlecalendar", "googledrive", "notion", "slack"}
	if got := registry.Toolkits(); !slices.Equal(got, want) {
		t.Fatalf("toolkits = %v, want %v", got, want)
	}
}

// Phase 1 ships sign-in and webhooks only. Tools, readers and triggers are
// phase 2's, built on these.
func TestShippedManifestsDeclareOnlySignInAndWebhooks(t *testing.T) {
	t.Parallel()
	for _, shipped := range All() {
		manifest := shipped.Manifest()
		if len(manifest.Tools)+len(manifest.Readers)+len(manifest.Triggers) > 0 || len(shipped.Tools()) > 0 {
			t.Errorf("%s declares tools, readers or triggers already", manifest.Toolkit)
		}
		if manifest.Webhook == nil {
			t.Errorf("%s declares no webhook", manifest.Toolkit)
		}
	}
}

// What each app's documentation says, as its manifest must keep saying it.
// These sections are frozen once phase 2 starts, and this test lives outside
// every toolkit's own package.
func TestShippedManifestsSayWhatTheAppsDocument(t *testing.T) {
	t.Parallel()
	type facts struct {
		client, clientAuth, revokeStyle string
		pkce                            bool
		scope, verify, handshake        string
	}
	want := map[string]facts{
		"github": {
			"github", integration.ClientAuthBody, integration.RevokeGitHubToken, true,
			integration.HookScopeConnection, integration.VerifyHMACSHA256, integration.HandshakeGitHubPing,
		},
		"slack": {
			"slack", integration.ClientAuthHeader, integration.RevokeSlackAuthRevoke, false,
			integration.HookScopeDeployment, integration.VerifyHMACSHA256, integration.HandshakeSlackURLVerification,
		},
		"notion": {
			"notion", integration.ClientAuthHeader, integration.RevokeRFC7009JSON, false,
			integration.HookScopeDeployment, integration.VerifyHMACSHA256, integration.HandshakeNotionVerification,
		},
		"gmail": {
			"google", integration.ClientAuthBody, integration.RevokeRFC7009, true,
			integration.HookScopeDeployment, integration.VerifyJWTPush, "",
		},
		"googledrive": {
			"google", integration.ClientAuthBody, integration.RevokeRFC7009, true,
			integration.HookScopeConnection, integration.VerifyStaticToken, integration.HandshakeGoogleChannelSync,
		},
		"googlecalendar": {
			"google", integration.ClientAuthBody, integration.RevokeRFC7009, true,
			integration.HookScopeConnection, integration.VerifyStaticToken, integration.HandshakeGoogleChannelSync,
		},
	}
	for _, shipped := range All() {
		manifest := shipped.Manifest()
		oauth := manifest.Auth.Methods[0].OAuth2
		if manifest.Auth.Methods[0].Kind != integration.KindOAuth2Code || oauth == nil {
			t.Errorf("%s does not default to an OAuth sign-in", manifest.Toolkit)
			continue
		}
		got := facts{
			oauth.Client, oauth.ClientAuth, oauth.RevokeStyle, oauth.PKCE,
			manifest.Webhook.Scope, manifest.Webhook.Verify.Type, manifest.Webhook.Handshake,
		}
		if got != want[manifest.Toolkit] {
			t.Errorf("%s says %+v, want %+v", manifest.Toolkit, got, want[manifest.Toolkit])
		}
	}
}

// The Google toolkits sign in through one client, so everything but their
// scopes must agree: a refresh or a revocation goes to one place whichever
// toolkit the connection is for.
func TestGoogleToolkitsShareOneClient(t *testing.T) {
	t.Parallel()
	var first integration.OAuth2
	for i, shipped := range []integration.Integration{gmail.New(), googledrive.New(), googlecalendar.New()} {
		oauth := *shipped.Manifest().Auth.Methods[0].OAuth2
		if len(oauth.Scopes) == 0 || oauth.AuthParams["access_type"] != "offline" {
			t.Errorf("%s asks for no scopes, or no refresh token", shipped.Manifest().Toolkit)
		}
		oauth.Scopes = nil
		if i == 0 {
			first = oauth
		} else if !reflect.DeepEqual(oauth, first) {
			t.Errorf("%s signs in as %+v, unlike %+v", shipped.Manifest().Toolkit, oauth, first)
		}
	}
}
