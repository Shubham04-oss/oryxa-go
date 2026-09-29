package spec

import (
	"strings"
	"testing"
)

func TestStoredVersionsTolerateFutureFieldsButSubmittedFilesStayStrict(t *testing.T) {
	input := strings.Replace(example, "name: Engineering", "name: Engineering, futureTeam: true", 1) + "futureTop: {enabled: true}\ntools: {offer: [], futurePolicy: true}\nsafety: {guard: {relevance: hold, futureGuard: yes}}\n"
	if _, err := Parse([]byte(input)); err == nil {
		t.Fatal("submitted unknown fields accepted")
	}
	document, err := ParseStored([]byte(input))
	if err != nil {
		t.Fatal(err)
	}
	if document.Tools.Offer == nil || len(*document.Tools.Offer) != 0 || *document.Safety.Guard.Relevance != "hold" || document.Teams.Items[0].Name != "Engineering" {
		t.Fatalf("known stored settings lost: %+v", document)
	}
	legacy, err := ParseStored([]byte(strings.Replace(input, "relevance: hold", "relevance: invalid", 1)))
	if err != nil || *legacy.Safety.Guard.Relevance != "invalid" {
		t.Fatalf("legacy value unavailable for repair: %+v, %v", legacy, err)
	}
}

func TestStoredDocumentsKeepLegacyValuesButDropUnsupportedSections(t *testing.T) {
	input := "apiVersion: oryxa.in/v1\nkind: Workspace\nmetadata: {organization: old_value}\norganization: {name: ''}\nrules: {consent: {expireAfter: 99999h}}\nchat: {future: true}\n"
	document, err := ParseStored([]byte(input))
	if err != nil || document.Metadata.Organization != "old_value" || document.Organization.Name != "" || *document.Rules.Consent.ExpireAfter != "99999h" || document.Chat != nil {
		t.Fatalf("legacy document = %+v, %v", document, err)
	}
	if _, err := Parse([]byte(input)); err == nil {
		t.Fatal("legacy input bypassed strict submission validation")
	}
	for _, invalid := range []string{"[]", input + "---\n{}", "metadata: &m {organization: acme}\ncopy: *m", "metadata: {organization: \"acme\\0\"}"} {
		if _, err := ParseStored([]byte(invalid)); err == nil {
			t.Fatalf("invalid stored structure accepted: %q", invalid)
		}
	}
}
