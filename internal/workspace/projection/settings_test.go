package projection

import (
	"github.com/Shubham04-oss/oryxa-go/internal/workspace/spec"
	"strings"
	"testing"
	"time"
)

func TestLegacyProjectionIgnoresNewManifestFields(t *testing.T) {
	value, err := FromYAML([]byte(`preset: regulated
futureSection: {enabled: true}
rules: {consent: {expireAfter: 6h, future: true}}
safety: {guard: {holdForReview: true, pii: {minimum: {owner: secrets, peer: strict}}, relevance: shadow, futureGuard: true}}
tools: {offer: [], futureToolSetting: true}
teams: {futureShape: ignored}
`))
	if err != nil || value.ExpireAfter != 6*time.Hour || !value.HoldForReview || value.Offer == nil || len(*value.Offer) != 0 || !value.Regulated || value.PIIMinimumOwner != "secrets" || value.PIIMinimumPeer != "strict" || value.GuardRelevance != "shadow" {
		t.Fatalf("newer manifest disrupted known policy: %+v %v", value, err)
	}
}

func TestLegacyProjectionDoesNotValidateUnrelatedManifestText(t *testing.T) {
	value, err := FromYAML([]byte("agents:\n  organization:\n    future: " + strings.Repeat("x", 600000) + "\n  personal: {proactive: off}\n"))
	if err != nil || value.Proactive != "off" {
		t.Fatalf("projection = %+v %v", value, err)
	}
}

func TestEveryCatalogProjectionFieldExistsAndUsesKnownTypes(t *testing.T) {
	document := spec.Document{}
	for _, setting := range spec.SettingsCatalog() {
		var value any
		switch setting.Kind {
		case "duration":
			value = "6h"
			if setting.Path == "tools.wakeUpWithin" {
				value = "30m"
			}
		case "enum":
			value = setting.Values[0]
		case "bool":
			value = true
		case "toolkits":
			value = []string{}
		}
		setting.Set(&document, value)
	}
	settings, err := FromDocument(document)
	if err != nil || settings.Offer == nil || len(*settings.Offer) != 0 || settings.PIIMinimumOwner != "secrets" || settings.PIIMinimumPeer != "secrets" || settings.GuardRelevance != "hold" || settings.WakeUpWithin != 30*time.Minute {
		t.Fatalf("catalog projection incomplete: %+v %v", settings, err)
	}
}
