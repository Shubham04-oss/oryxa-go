package authz

import (
	_ "embed"
	"testing"

	"github.com/cedar-policy/cedar-go/types"
	"github.com/cedar-policy/cedar-go/x/exp/schema"
)

//go:embed schema.cedarschema
var schemaCedarText string

// TestSchemaParses keeps schema.cedarschema valid Cedar schema syntax. This
// package never feeds the schema to Cedar's (experimental) validator — see
// schema.cedarschema's own doc comment — so this parse is the only thing
// that would catch it going stale.
func TestSchemaParses(t *testing.T) {
	var parsed schema.Schema
	if err := parsed.UnmarshalCedar([]byte(schemaCedarText)); err != nil {
		t.Fatalf("schema.cedarschema does not parse: %v", err)
	}
	namespace, ok := parsed.AST().Namespaces[types.Path("Oryxa")]
	if !ok {
		t.Fatal("schema.cedarschema does not declare the Oryxa namespace")
	}
	for _, entityType := range []string{"Organization", "Member", "Agent", "KnowledgeBase", "Document"} {
		if _, ok := namespace.Entities[types.Ident(entityType)]; !ok {
			t.Errorf("schema.cedarschema does not declare entity %s", entityType)
		}
	}
	for _, action := range []string{"read", "search", "administer"} {
		if _, ok := namespace.Actions[types.String(action)]; !ok {
			t.Errorf("schema.cedarschema does not declare action %q", action)
		}
	}
}
