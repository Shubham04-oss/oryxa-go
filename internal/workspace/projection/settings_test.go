package projection

import (
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
