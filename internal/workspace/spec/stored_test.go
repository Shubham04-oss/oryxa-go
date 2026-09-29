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
	if _, err := ParseStored([]byte(strings.Replace(input, "relevance: hold", "relevance: invalid", 1))); err == nil {
		t.Fatal("invalid known stored policy accepted")
	}
}
