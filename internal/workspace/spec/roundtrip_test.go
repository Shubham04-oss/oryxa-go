package spec

import (
	"math/rand"
	"strings"
	"testing"
)

func TestYAMLRoundTripsTwentyThousandTextValues(t *testing.T) {
	random := rand.New(rand.NewSource(39))
	alphabet := []rune("abc ABC ':\"#|>\\\t\n界")
	for index := 0; index < 20000; index++ {
		value := make([]rune, 1+random.Intn(120))
		for offset := range value {
			value[offset] = alphabet[random.Intn(len(alphabet))]
		}
		text := string(value)
		document := Document{APIVersion: APIVersion, Kind: "Workspace", Metadata: Metadata{Organization: "acme"}, Agents: &Agents{Organization: &OrganizationAgent{Description: &text}}}
		encoded, err := document.YAML()
		if err != nil {
			t.Fatalf("case%d could not serialize: %v", index, err)
		}
		parsed, err := Parse(encoded)
		if err != nil {
			t.Fatalf("case%d invalid emitted YAML: %v", index, err)
		}
		if *parsed.Agents.Organization.Description != strings.TrimSpace(text) {
			t.Fatalf("case%d changed text", index)
		}
	}
}
