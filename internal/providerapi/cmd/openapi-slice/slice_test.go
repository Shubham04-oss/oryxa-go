package main

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
)

// description has two tags. pets/list (tag pets) reaches schemas Pet → Owner →
// Pet (a cycle), a path-level parameter whose schema is a union, a response
// with a header, an example, a pointer into a component, and an escaped name.
// The admin tag holds pets/delete, whose prefix is not its tag, and
// admin/audit; they reach schema Audit, the schemas its discriminator maps to,
// one of them only by name, and the admin security scheme.
const description = `{
  "openapi": "3.0.3",
  "info": {"title": "Pets", "version": "1"},
  "servers": [{"url": "https://pets.example.test"}],
  "tags": [{"name": "pets"}, {"name": "admin"}],
  "paths": {
    "/owners/{owner}/pets": {
      "parameters": [{"$ref": "#/components/parameters/owner"}],
      "get": {
        "operationId": "pets/list",
        "tags": ["pets"],
        "security": [{"token": []}],
        "responses": {
          "200": {
            "description": "Pets",
            "headers": {"Link": {"$ref": "#/components/headers/link"}},
            "content": {"application/json": {
              "schema": {"type": "array", "items": {"$ref": "#/components/schemas/Pet"}},
              "examples": {"default": {"$ref": "#/components/examples/pets"}}
            }}
          },
          "404": {"$ref": "#/components/responses/not_found"}
        }
      },
      "delete": {
        "operationId": "pets/delete",
        "tags": ["admin"],
        "security": [{"admin": []}],
        "responses": {"204": {"description": "Gone"}}
      }
    },
    "/audit": {
      "get": {
        "operationId": "admin/audit",
        "tags": ["admin"],
        "responses": {"200": {"description": "Log", "content": {"application/json": {"schema": {"$ref": "#/components/schemas/Audit"}}}}}
      }
    }
  },
  "components": {
    "schemas": {
      "Pet": {"type": "object", "properties": {"owner": {"$ref": "#/components/schemas/Owner"}, "tag": {"$ref": "#/components/schemas/a~1b"}}},
      "Owner": {"type": "object", "properties": {"pets": {"type": "array", "items": {"$ref": "#/components/schemas/Pet"}}, "size": {"$ref": "#/components/schemas/Sizes/properties/large"}}},
      "Sizes": {"type": "object", "properties": {"large": {"type": "integer", "maximum": 9007199254740993}}},
      "a/b": {"type": "string", "description": "<b>escaped</b> & named"},
      "Audit": {
        "oneOf": [{"$ref": "#/components/schemas/Entry"}],
        "discriminator": {"propertyName": "kind", "mapping": {"entry": "#/components/schemas/Entry", "legacy": "Legacy"}}
      },
      "Entry": {"type": "object", "properties": {"kind": {"type": "string"}}},
      "Legacy": {"type": "object", "properties": {"kind": {"type": "string"}}}
    },
    "parameters": {"owner": {"name": "owner", "in": "path", "required": true, "schema": {"oneOf": [{"type": "integer"}, {"type": "string"}]}}},
    "headers": {"link": {"schema": {"type": "string"}}},
    "examples": {"pets": {"value": [{"owner": null}]}},
    "responses": {"not_found": {"description": "Missing", "content": {"application/json": {"schema": {"$ref": "#/components/schemas/Owner"}}}}},
    "securitySchemes": {"token": {"type": "http", "scheme": "bearer"}, "admin": {"type": "http", "scheme": "basic"}}
  }
}`

func decode(t *testing.T, raw string) map[string]any {
	t.Helper()
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.UseNumber()
	var document map[string]any
	if err := decoder.Decode(&document); err != nil {
		t.Fatal(err)
	}
	return document
}

// encoded is value as compact JSON with sorted keys, for comparing documents.
func encoded(t *testing.T, value any) string {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// at follows keys and indexes through decoded JSON.
func at(t *testing.T, node any, steps ...any) any {
	t.Helper()
	for _, step := range steps {
		switch key := step.(type) {
		case string:
			object, ok := node.(map[string]any)
			if !ok {
				t.Fatalf("at %v: not an object before %q", steps, key)
			}
			node = object[key]
		case int:
			list, ok := node.([]any)
			if !ok || key >= len(list) {
				t.Fatalf("at %v: no item %d", steps, key)
			}
			node = list[key]
		}
	}
	return node
}

func names(slice map[string]any, kind string) []string {
	components, _ := slice["components"].(map[string]any)
	values, _ := components[kind].(map[string]any)
	var result []string
	for name := range values {
		result = append(result, name)
	}
	slices.Sort(result)
	return result
}

// operations maps each kept operation's id to its original id.
func operations(slice map[string]any) map[string]string {
	result := map[string]string{}
	for _, item := range slice["paths"].(map[string]any) {
		for _, method := range methods {
			if operation, ok := item.(map[string]any)[method].(map[string]any); ok {
				original, _ := operation["x-original-operation-id"].(string)
				result[operation["operationId"].(string)] = original
			}
		}
	}
	return result
}

func TestSliceKeepsWhatATagReaches(t *testing.T) {
	document := decode(t, description)
	pets, err := Slice(document, "pets")
	if err != nil {
		t.Fatalf("Slice(pets) error = %v", err)
	}
	if got := operations(pets); len(got) != 1 || got["list"] != "pets/list" {
		t.Fatalf("pets operations = %v, want list from pets/list", got)
	}
	if at(t, pets, "paths", "/owners/{owner}/pets", "parameters") == nil {
		t.Fatal("the path-level parameters were dropped")
	}
	if _, ok := pets["paths"].(map[string]any)["/audit"]; ok {
		t.Fatal("a path with no pets operation was kept")
	}
	for kind, want := range map[string][]string{
		"schemas":         {"Owner", "Pet", "Sizes", "a/b"},
		"parameters":      {"owner"},
		"headers":         {"link"},
		"examples":        {"pets"},
		"responses":       {"not_found"},
		"securitySchemes": {"token"},
	} {
		if got := names(pets, kind); !slices.Equal(got, want) {
			t.Errorf("pets components/%s = %v, want %v", kind, got, want)
		}
	}
	if tags, _ := pets["tags"].([]any); len(tags) != 1 || tags[0].(map[string]any)["name"] != "pets" {
		t.Errorf("pets tags = %v, want only pets", pets["tags"])
	}
	if pets["servers"] == nil || pets["info"] == nil || pets["openapi"] != "3.0.3" {
		t.Errorf("pets top-level fields = %v", pets)
	}
	if got := encoded(t, at(t, pets, "components", "parameters", "owner", "schema")); got != `{"type":"string"}` {
		t.Errorf("pets owner schema = %s, want the union normalized to a string", got)
	}

	admin, err := Slice(document, "admin")
	if err != nil {
		t.Fatalf("Slice(admin) error = %v", err)
	}
	if got := operations(admin); len(got) != 2 || got["audit"] != "admin/audit" || got["pets-delete"] != "pets/delete" {
		t.Fatalf("admin operations = %v, want audit and pets-delete", got)
	}
	if got := names(admin, "schemas"); !slices.Equal(got, []string{"Audit", "Entry", "Legacy"}) {
		t.Errorf("admin schemas = %v, want Audit and the schemas it maps to", got)
	}
	if got := names(admin, "securitySchemes"); !slices.Equal(got, []string{"admin"}) {
		t.Errorf("admin security schemes = %v, want only admin", got)
	}

	original := at(t, document, "paths", "/owners/{owner}/pets", "get").(map[string]any)
	if original["operationId"] != "pets/list" || original["x-original-operation-id"] != nil {
		t.Fatal("Slice() changed the input's operations")
	}
	if at(t, document, "components", "parameters", "owner", "schema", "oneOf") == nil {
		t.Fatal("Slice() changed the input's components")
	}
}

func TestSliceKeepsWhatSeveralTagsReach(t *testing.T) {
	document := decode(t, description)
	both, err := Slice(document, "admin", "pets")
	if err != nil {
		t.Fatalf("Slice(admin, pets) error = %v", err)
	}
	// pets/delete is tagged admin, so it keeps its pets prefix even though pets
	// is sliced too.
	if got := operations(both); len(got) != 3 || got["list"] != "pets/list" || got["audit"] != "admin/audit" || got["pets-delete"] != "pets/delete" {
		t.Fatalf("operations = %v, want list, audit and pets-delete", got)
	}
	if methods := at(t, both, "paths", "/owners/{owner}/pets").(map[string]any); methods["get"] == nil || methods["delete"] == nil {
		t.Fatalf("/owners/{owner}/pets = %v, want both its operations", methods)
	}
	for kind, want := range map[string][]string{
		"schemas":         {"Audit", "Entry", "Legacy", "Owner", "Pet", "Sizes", "a/b"},
		"securitySchemes": {"admin", "token"},
	} {
		if got := names(both, kind); !slices.Equal(got, want) {
			t.Errorf("components/%s = %v, want %v", kind, got, want)
		}
	}
	if got := encoded(t, both["tags"]); got != `[{"name":"pets"},{"name":"admin"}]` {
		t.Errorf("tags = %s, want both definitions in the description's order", got)
	}
	if _, err := Slice(document, "pets", "owners"); err == nil || !strings.Contains(err.Error(), `tag "owners" has no operations`) {
		t.Errorf("Slice(pets, owners) error = %v, want owners refused", err)
	}
	if _, err := Slice(document); err == nil {
		t.Error("Slice() with no tags succeeded")
	}
}

func TestSliceRefusesWhatItCannotKeep(t *testing.T) {
	for name, test := range map[string]struct {
		raw  string
		tag  string
		want string
	}{
		"a tag with no operations": {description, "owners", `tag "owners" has no operations`},
		"swagger 2":                {`{"swagger": "2.0", "paths": {}}`, "pets", "only OpenAPI 3"},
		"two operations named alike": {`{"openapi": "3.0.0", "paths": {
			"/a": {"get": {"operationId": "x/a-b", "tags": ["x"], "responses": {}}},
			"/b": {"get": {"operationId": "a/b", "tags": ["x"], "responses": {}}}}}`, "x", `would both be named "a-b"`},
		"an external reference": {`{"openapi": "3.1.0", "paths": {"/p": {"get": {"operationId": "p", "tags": ["t"],
			"responses": {"200": {"$ref": "other.json#/components/responses/ok"}}}}}}`, "t", "only local component references"},
		"a dangling reference": {`{"openapi": "3.0.0", "paths": {"/p": {"get": {"operationId": "p", "tags": ["t"],
			"responses": {"200": {"$ref": "#/components/responses/ok"}}}}}}`, "t", "components/responses/ok is referenced but not defined"},
		"a reference to no component": {`{"openapi": "3.0.0", "paths": {"/p": {"get": {"operationId": "p", "tags": ["t"],
			"responses": {"200": {"$ref": "#/components/responses"}}}}}}`, "t", "does not name a component"},
		"a mapping to an undefined schema": {`{"openapi": "3.0.0", "paths": {"/p": {"get": {"operationId": "p", "tags": ["t"],
			"responses": {"200": {"description": "ok", "content": {"application/json": {"schema": {
				"oneOf": [], "discriminator": {"propertyName": "kind", "mapping": {"gone": "Gone"}}}}}}}}}}}`, "t", "components/schemas/Gone is referenced but not defined"},
		"a union it cannot normalize": {`{"openapi": "3.0.0", "paths": {"/p": {"get": {"operationId": "p", "tags": ["t"], "responses": {},
			"parameters": [{"name": "q", "in": "query", "schema": {"oneOf": [{"type": "object"}]}}]}}}}`, "t", `tag "t": parameter "q"`},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := Slice(decode(t, test.raw), test.tag); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Slice() error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestReferenceNames(t *testing.T) {
	for id, want := range map[[2]string]string{
		{"repos/get", "repos"}:                         "get",
		{"repos/list-for-org", "repos"}:                "list-for-org",
		{"api-insights/get-stats", "orgs"}:             "api-insights-get-stats",
		{"meta/root", "meta"}:                          "root",
		{"reposition/get", "repos"}:                    "reposition-get",
		{"already-plain", "repos"}:                     "already-plain",
		{"code-scanning/list-alerts", "code-scanning"}: "list-alerts",
		{"post_files_id#add_shared_link", "Files"}:     "post_files_id#add_shared_link",
		{"users/get", "orgs,users"}:                    "get",
		{"orgs/users/get", "orgs,users"}:               "users-get",
	} {
		if got := operationID(id[0], strings.Split(id[1], ",")); got != want {
			t.Errorf("operationID(%q, %q) = %q, want %q", id[0], id[1], got, want)
		}
	}
	for ref, want := range map[string][2]string{
		"#/components/schemas/a~1b":                   {"schemas", "a/b"},
		"#/components/schemas/a~01":                   {"schemas", "a~1"},
		"#/components/schemas/Pet/properties/owner":   {"schemas", "Pet"},
		"#/components/securitySchemes/oauth~0classic": {"securitySchemes", "oauth~classic"},
	} {
		kind, name, err := componentOf(ref)
		if err != nil || [2]string{kind, name} != want {
			t.Errorf("componentOf(%q) = %s %s %v, want %v", ref, kind, name, err, want)
		}
		if whole := strings.Join(strings.Split(ref, "/")[:4], "/"); componentReference(kind, name) != whole {
			t.Errorf("componentReference(%q, %q) = %q, want %q", kind, name, componentReference(kind, name), whole)
		}
	}
	for value, want := range map[string]string{
		"private-user":                        "#/components/schemas/private-user",
		"v1.Pet_2":                            "#/components/schemas/v1.Pet_2",
		"#/components/schemas/public-user":    "#/components/schemas/public-user",
		"other.json#/components/schemas/user": "other.json#/components/schemas/user",
	} {
		if got := mappingReference(value); got != want {
			t.Errorf("mappingReference(%q) = %q, want %q", value, got, want)
		}
	}
}
