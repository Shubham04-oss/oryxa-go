package catalog

import "testing"

func TestSlackCatalogPinsPieceScopesAndCallback(t *testing.T) {
	t.Parallel()
	catalog := Static{PublicBaseURL: "https://connect.oryxa.test"}
	definition, ok := catalog.OAuthDefinition("slack")
	if !ok {
		t.Fatal("Slack definition is missing")
	}
	if definition.PieceName != SlackPieceName || definition.PieceVersion != SlackPieceVersion {
		t.Fatalf("piece = %s@%s", definition.PieceName, definition.PieceVersion)
	}
	if definition.RedirectURI != "https://connect.oryxa.test/v1/oauth/callback/slack" {
		t.Fatalf("redirect URI = %q", definition.RedirectURI)
	}
	if len(definition.RequiredScopes) != 2 || definition.SupportsPKCE {
		t.Fatalf("unexpected Slack policy: %+v", definition)
	}
	if _, ok := catalog.OAuthDefinition("unapproved"); ok {
		t.Fatal("unknown provider was accepted")
	}
}
