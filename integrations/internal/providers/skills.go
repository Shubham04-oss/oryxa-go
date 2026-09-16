package providers

import (
	"embed"
	"fmt"
)

// skills holds each provider's skill: how its events and tools are meant to be
// used. A skill ships with its provider, so changing one is reviewed with the
// code it describes.
//
//go:embed skills/*.md
var skills embed.FS

// skill is a provider's skill, and panics when it is missing, since a provider
// without one is a programming mistake Builtin would carry into production.
func skill(provider string) string {
	content, err := skills.ReadFile("skills/" + provider + ".md")
	if err != nil {
		panic(fmt.Sprintf("provider %s has no skill: %v", provider, err))
	}
	return string(content)
}
