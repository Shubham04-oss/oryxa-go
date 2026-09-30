package slackbot

import (
	"bytes"
	"os"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"unicode/utf8"

	"go.yaml.in/yaml/v3"

	"github.com/Shubham04-oss/oryxa-go/internal/secrets"
	"github.com/Shubham04-oss/oryxa-go/internal/slackfake"
)

// productionBase is where the shipped manifest points Slack: what
// APPS_PUBLIC_BASE_URL is in production.
const productionBase = "https://api.oryxa.in"

// slackManifest is every key manifest.yaml may hold. Decoding refuses a key
// that is not here, so a key added to the file is added here on purpose.
type slackManifest struct {
	Metadata struct {
		MajorVersion int `yaml:"major_version"`
		MinorVersion int `yaml:"minor_version"`
	} `yaml:"_metadata"`
	DisplayInformation struct {
		Name            string `yaml:"name"`
		Description     string `yaml:"description"`
		LongDescription string `yaml:"long_description"`
		BackgroundColor string `yaml:"background_color"`
	} `yaml:"display_information"`
	Features struct {
		AppHome struct {
			HomeTabEnabled             bool `yaml:"home_tab_enabled"`
			MessagesTabEnabled         bool `yaml:"messages_tab_enabled"`
			MessagesTabReadOnlyEnabled bool `yaml:"messages_tab_read_only_enabled"`
		} `yaml:"app_home"`
		BotUser struct {
			DisplayName  string `yaml:"display_name"`
			AlwaysOnline bool   `yaml:"always_online"`
		} `yaml:"bot_user"`
	} `yaml:"features"`
	OAuthConfig struct {
		RedirectURLs []string `yaml:"redirect_urls"`
		Scopes       struct {
			Bot  []string `yaml:"bot"`
			User []string `yaml:"user"`
		} `yaml:"scopes"`
	} `yaml:"oauth_config"`
	Settings struct {
		EventSubscriptions struct {
			RequestURL string   `yaml:"request_url"`
			BotEvents  []string `yaml:"bot_events"`
			UserEvents []string `yaml:"user_events"`
		} `yaml:"event_subscriptions"`
		Interactivity struct {
			IsEnabled  bool   `yaml:"is_enabled"`
			RequestURL string `yaml:"request_url"`
		} `yaml:"interactivity"`
		OrgDeployEnabled     bool `yaml:"org_deploy_enabled"`
		SocketModeEnabled    bool `yaml:"socket_mode_enabled"`
		TokenRotationEnabled bool `yaml:"token_rotation_enabled"`
	} `yaml:"settings"`
}

func loadManifest(t *testing.T) slackManifest {
	t.Helper()
	decoder := yaml.NewDecoder(bytes.NewReader(Manifest()))
	decoder.KnownFields(true)
	var manifest slackManifest
	if err := decoder.Decode(&manifest); err != nil {
		t.Fatalf("manifest.yaml: %v", err)
	}
	return manifest
}

// The manifest is the only way the app's scopes, events and URLs change, so
// every one of them is pinned here: changing one is changing this test.
func TestTheManifestIsPinned(t *testing.T) {
	manifest := loadManifest(t)

	// The scopes, in the order the file lists them.
	wantScopes := []string{
		"app_mentions:read", "chat:write", "im:write", "users:read", "users:read.email", "channels:read", "groups:read",
	}
	if !reflect.DeepEqual(manifest.OAuthConfig.Scopes.Bot, wantScopes) {
		t.Errorf("bot scopes = %v, want %v", manifest.OAuthConfig.Scopes.Bot, wantScopes)
	}
	if len(manifest.OAuthConfig.Scopes.User) != 0 {
		t.Errorf("the app asks for user scopes: %v", manifest.OAuthConfig.Scopes.User)
	}
	// Nothing reads a channel's history, and nothing writes outside what Oryxa says.
	for _, scope := range manifest.OAuthConfig.Scopes.Bot {
		if strings.Contains(scope, "history") || strings.HasPrefix(scope, "files:") || strings.HasSuffix(scope, ":manage") {
			t.Errorf("scope %s is not one the surface has a use for", scope)
		}
	}

	// The events: the bot's own three, and no user events.
	if want := []string{"app_mention", "app_uninstalled", "tokens_revoked"}; !reflect.DeepEqual(manifest.Settings.EventSubscriptions.BotEvents, want) {
		t.Errorf("bot events = %v, want %v", manifest.Settings.EventSubscriptions.BotEvents, want)
	}
	if len(manifest.Settings.EventSubscriptions.UserEvents) != 0 {
		t.Errorf("user events = %v", manifest.Settings.EventSubscriptions.UserEvents)
	}

	// The URLs.
	if got, want := manifest.Settings.EventSubscriptions.RequestURL, productionBase+"/v1/apps/slack/events"; got != want {
		t.Errorf("events URL = %s, want %s", got, want)
	}
	if got, want := manifest.Settings.Interactivity.RequestURL, productionBase+"/v1/apps/slack/interactivity"; got != want || !manifest.Settings.Interactivity.IsEnabled {
		t.Errorf("interactivity = %s (enabled %v), want %s", got, manifest.Settings.Interactivity.IsEnabled, want)
	}
	if want := []string{productionBase + "/v1/apps/slack/install/callback"}; !reflect.DeepEqual(manifest.OAuthConfig.RedirectURLs, want) {
		t.Errorf("redirect URLs = %v, want %v", manifest.OAuthConfig.RedirectURLs, want)
	}

	// The switches.
	settings := manifest.Settings
	if settings.OrgDeployEnabled || settings.SocketModeEnabled || settings.TokenRotationEnabled {
		t.Errorf("org deploy %v, socket mode %v, token rotation %v: all three are off",
			settings.OrgDeployEnabled, settings.SocketModeEnabled, settings.TokenRotationEnabled)
	}
	if home := manifest.Features.AppHome; home.HomeTabEnabled || !home.MessagesTabEnabled || !home.MessagesTabReadOnlyEnabled {
		t.Errorf("app home = %+v, want a read-only Messages tab and no Home tab", home)
	}
	if manifest.Features.BotUser.DisplayName != "Oryxa" || manifest.DisplayInformation.Name != "Oryxa" {
		t.Errorf("names = %q, %q", manifest.Features.BotUser.DisplayName, manifest.DisplayInformation.Name)
	}
	if manifest.Metadata.MajorVersion != 2 {
		t.Errorf("manifest schema version = %d", manifest.Metadata.MajorVersion)
	}

	// Slack's own limits on the words, so the manifest is not refused when
	// it is pasted.
	information := manifest.DisplayInformation
	if utf8.RuneCountInString(information.Name) > 35 || utf8.RuneCountInString(information.Description) > 140 ||
		utf8.RuneCountInString(information.LongDescription) < 175 || utf8.RuneCountInString(information.LongDescription) > 4000 {
		t.Errorf("description lengths: name %d, description %d, long description %d",
			utf8.RuneCountInString(information.Name), utf8.RuneCountInString(information.Description), utf8.RuneCountInString(information.LongDescription))
	}
}

// The code that asks for the install, the URLs it registers and what the fake
// grants are all held to the manifest, so none of them can drift from it.
func TestTheManifestAndTheCodeAgree(t *testing.T) {
	manifest := loadManifest(t)
	if !reflect.DeepEqual(BotScopes, manifest.OAuthConfig.Scopes.Bot) {
		t.Errorf("BotScopes = %v, the manifest's = %v", BotScopes, manifest.OAuthConfig.Scopes.Bot)
	}
	if !reflect.DeepEqual(slackfake.BotScopes, manifest.OAuthConfig.Scopes.Bot) {
		t.Errorf("slackfake.BotScopes = %v, the manifest's = %v", slackfake.BotScopes, manifest.OAuthConfig.Scopes.Bot)
	}
	if manifest.Settings.EventSubscriptions.RequestURL != productionBase+EventsPath ||
		manifest.Settings.Interactivity.RequestURL != productionBase+InteractivityPath ||
		manifest.OAuthConfig.RedirectURLs[0] != productionBase+InstallCallbackPath {
		t.Errorf("the manifest's URLs and the paths served differ: %+v", manifest.Settings)
	}
	if slackfake.EventsPath != EventsPath || slackfake.InteractivityPath != InteractivityPath {
		t.Errorf("slackfake serves the ingress at other paths than %s and %s", EventsPath, InteractivityPath)
	}
	// The redirect an install names is the redirect the manifest registers.
	service, err := New(Dependencies{Store: newMemoryStore(), Sealer: secrets.Envelope{Keys: &fakeKeys{}, KeyID: "alias/test", Service: "oryxa-apps"}, Directory: &fakeDirectory{}}, Config{
		PublicBaseURL: productionBase, App: App{ClientID: "1.2", ClientSecret: "secret-secret", SigningSecret: "signing-signing"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if service.RedirectURI() != manifest.OAuthConfig.RedirectURLs[0] {
		t.Errorf("an install redirects to %s, the manifest registers %s", service.RedirectURI(), manifest.OAuthConfig.RedirectURLs[0])
	}
}

// Every scope in the file says why it is there, on the line above it: a
// reviewer at Slack reads these.
func TestEveryScopeInTheManifestSaysWhyItIsThere(t *testing.T) {
	lines := strings.Split(string(Manifest()), "\n")
	scope := regexp.MustCompile(`^\s+- ([a-z_]+(?:\.[a-z]+)?:[a-z.]+)\s*$`)
	found := 0
	for i, line := range lines {
		match := scope.FindStringSubmatch(line)
		if match == nil {
			continue
		}
		found++
		previous := strings.TrimSpace(lines[i-1])
		if !strings.HasPrefix(previous, "# ") || len(previous) < 20 {
			t.Errorf("scope %s has no comment above it saying why it is asked for: %q", match[1], previous)
		}
	}
	if found != len(BotScopes) {
		t.Errorf("found %d scope lines, want %d", found, len(BotScopes))
	}
}

func TestTheManifestFileIsTheOneEmbedded(t *testing.T) {
	onDisk, err := os.ReadFile("manifest.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(onDisk, Manifest()) {
		t.Fatal("the embedded manifest is not manifest.yaml")
	}
	// A caller cannot change what is embedded.
	copyOf := Manifest()
	copyOf[0] = '!'
	if Manifest()[0] == '!' {
		t.Fatal("Manifest hands out its own bytes")
	}
}
