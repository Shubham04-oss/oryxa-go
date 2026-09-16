package main

import (
	"slices"
	"strings"
	"testing"
)

func TestNormalizeDropsDiscriminatorsOverInlineSchemas(t *testing.T) {
	slice := decode(t, `{"openapi": "3.0.3", "paths": {"/runs": {"post": {
		"operationId": "create", "tags": ["checks"],
		"requestBody": {"content": {"application/json": {"schema": {
			"discriminator": {"propertyName": "status"},
			"oneOf": [{"properties": {"status": {"enum": ["completed"]}}}, {"properties": {"status": {"enum": ["queued"]}}}]
		}}}},
		"responses": {"201": {"description": "Created", "content": {"application/json": {"schema": {"$ref": "#/components/schemas/Run"}}}}}
	}}}, "components": {"schemas": {
		"Run": {"discriminator": {"propertyName": "kind"}, "oneOf": [{"$ref": "#/components/schemas/Fast"}, {"$ref": "#/components/schemas/Slow"}],
			"properties": {"discriminator": {"type": "string"}}},
		"Fast": {"type": "object"},
		"Slow": {"type": "object"}
	}}}`)
	if err := normalize(slice); err != nil {
		t.Fatal(err)
	}
	schema := at(t, slice, "paths", "/runs", "post", "requestBody", "content", "application/json", "schema").(map[string]any)
	if _, ok := schema["discriminator"]; ok {
		t.Fatal("a discriminator over inline schemas was kept")
	}
	run := at(t, slice, "components", "schemas", "Run").(map[string]any)
	if _, ok := run["discriminator"]; !ok {
		t.Fatal("a discriminator over references was dropped")
	}
	if at(t, run, "properties", "discriminator") == nil {
		t.Fatal("a property named discriminator was dropped")
	}
}

func TestNormalizeGivesUnionParametersOneType(t *testing.T) {
	slice := decode(t, `{"openapi": "3.0.3", "paths": {"/workflows/{workflow_id}": {
		"parameters": [{"name": "workflow_id", "in": "path", "required": true, "schema": {"oneOf": [{"type": "integer"}, {"type": "string"}]}}],
		"get": {"operationId": "get", "responses": {}, "parameters": [
			{"$ref": "#/components/parameters/has"},
			{"name": "fields", "in": "query", "schema": {"anyOf": [{"type": "string"}, {"type": "array", "maxItems": 50, "items": {"type": "string"}}]}},
			{"name": "page", "in": "query", "schema": {"type": "integer", "default": 1}}
		]}
	}}, "components": {"parameters": {
		"has": {"name": "has", "in": "query", "schema": {"description": "Filters.", "oneOf": [{"type": "string"}, {"type": "array", "items": {"type": "string", "enum": ["patch"]}}]}}
	}}}`)
	if err := normalize(slice); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		parameter any
		want      string
	}{
		{at(t, slice, "paths", "/workflows/{workflow_id}", "parameters", 0), `{"type": "string"}`},
		{at(t, slice, "components", "parameters", "has"), `{"description": "Filters.", "type": "array", "items": {"type": "string", "enum": ["patch"]}}`},
		{at(t, slice, "paths", "/workflows/{workflow_id}", "get", "parameters", 1), `{"type": "array", "maxItems": 50, "items": {"type": "string"}}`},
		{at(t, slice, "paths", "/workflows/{workflow_id}", "get", "parameters", 2), `{"type": "integer", "default": 1}`},
	} {
		parameter := test.parameter.(map[string]any)
		if got, want := encoded(t, parameter["schema"]), encoded(t, decode(t, test.want)); got != want {
			t.Errorf("parameter %v schema = %s, want %s", parameter["name"], got, want)
		}
	}
	if ref := at(t, slice, "paths", "/workflows/{workflow_id}", "get", "parameters", 0, "$ref"); ref != "#/components/parameters/has" {
		t.Errorf("a parameter reference became %v", ref)
	}

	for name, parameter := range map[string]string{
		"an object variant":                     `"schema": {"oneOf": [{"type": "object"}, {"type": "string"}]}`,
		"a referenced variant":                  `"schema": {"anyOf": [{"$ref": "#/components/schemas/id"}, {"type": "string"}]}`,
		"two array variants":                    `"schema": {"oneOf": [{"type": "array", "items": {"type": "string"}}, {"type": "array", "items": {"type": "integer"}}]}`,
		"a deepObject without an object":        `"style": "deepObject", "schema": {"oneOf": [{"type": "integer"}, {"type": "string"}]}`,
		"a deepObject with two objects":         `"style": "deepObject", "schema": {"oneOf": [{"type": "object"}, {"type": "object"}]}`,
		"a deepObject with an array and object": `"style": "deepObject", "schema": {"oneOf": [{"type": "object"}, {"type": "array", "items": {"type": "string"}}]}`,
	} {
		slice := decode(t, `{"components": {"parameters": {"id": {"name": "id", "in": "query", `+parameter+`}}}}`)
		if err := normalize(slice); err == nil || !strings.Contains(err.Error(), `parameter "id"`) {
			t.Errorf("%s: normalize() error = %v, want parameter \"id\" refused", name, err)
		}
	}
}

func TestNormalizeGivesDeepObjectUnionsTheirObject(t *testing.T) {
	// Zendesk's page is a page number or a cursor, sent as page[size]=50.
	slice := decode(t, `{"components": {"parameters": {"page": {"name": "page", "in": "query", "style": "deepObject", "explode": true,
		"schema": {"description": "A page.", "oneOf": [
			{"type": "integer", "example": 1},
			{"type": "object", "properties": {"after": {"type": "string"}, "size": {"type": "integer"}}}
		]}}}}}`)
	if err := normalize(slice); err != nil {
		t.Fatal(err)
	}
	got := encoded(t, at(t, slice, "components", "parameters", "page"))
	want := encoded(t, decode(t, `{"name": "page", "in": "query", "style": "deepObject", "explode": true,
		"schema": {"description": "A page.", "type": "object", "properties": {"after": {"type": "string"}, "size": {"type": "integer"}}}}`))
	if got != want {
		t.Errorf("page = %s, want %s", got, want)
	}
}

func TestNormalizeRenamesComponentsNamedLikeInlineEnums(t *testing.T) {
	slice := decode(t, `{"openapi": "3.0.3",
	  "paths": {"/teams": {"get": {"operationId": "list", "responses": {},
	    "parameters": [{"$ref": "#/components/parameters/team-type"}, {"$ref": "#/components/parameters/team-type-filter"}]}}},
	  "components": {
	    "schemas": {
	      "team": {"type": "object", "properties": {
	        "type": {"type": "string", "enum": ["enterprise", "organization"]},
	        "privacy": {"$ref": "#/components/schemas/team-privacy"},
	        "parent": {"type": "object", "properties": {"states": {"type": "array", "items": {"type": "string", "enum": ["open"]}}}},
	        "kind": {"$ref": "#/components/parameters/team-type/schema"}
	      }},
	      "team-privacy": {"type": "string", "enum": ["secret", "closed"]},
	      "Team_Parent_States": {"type": "string"},
	      "analysis": {"allOf": [{"type": "object", "properties": {"status": {"type": "string", "enum": ["done"]}}}]},
	      "analysis-status": {"type": "string", "enum": ["queued"]},
	      "result": {"oneOf": [{"$ref": "#/components/schemas/analysis-status"}],
	        "discriminator": {"propertyName": "kind", "mapping": {"ref": "#/components/schemas/analysis-status", "name": "analysis-status", "other": "team"}}}
	    },
	    "parameters": {
	      "team-type": {"name": "team_type", "in": "query", "schema": {"type": "string", "enum": ["all", "enterprise"]}},
	      "team-type-filter": {"name": "filter", "in": "query", "schema": {"type": "string"}}
	    }
	  }}`)
	if err := normalize(slice); err != nil {
		t.Fatal(err)
	}
	for kind, want := range map[string][]string{
		// team.type shadows parameter team-type, team.parent.states shadows
		// Team_Parent_States, and analysis's allOf status shadows
		// analysis-status. team.privacy is a reference, not an inline enum.
		"parameters": {"team-type-filter", "team-type-parameter"},
		"schemas":    {"Team_Parent_States-schema", "analysis", "analysis-status-schema", "result", "team", "team-privacy"},
	} {
		if got := names(slice, kind); !slices.Equal(got, want) {
			t.Errorf("components/%s = %v, want %v", kind, got, want)
		}
	}
	for _, test := range []struct {
		got  any
		want string
	}{
		{at(t, slice, "paths", "/teams", "get", "parameters", 0, "$ref"), "#/components/parameters/team-type-parameter"},
		{at(t, slice, "paths", "/teams", "get", "parameters", 1, "$ref"), "#/components/parameters/team-type-filter"},
		{at(t, slice, "components", "schemas", "team", "properties", "kind", "$ref"), "#/components/parameters/team-type-parameter/schema"},
		{at(t, slice, "components", "schemas", "result", "oneOf", 0, "$ref"), "#/components/schemas/analysis-status-schema"},
		{at(t, slice, "components", "schemas", "result", "discriminator", "mapping", "ref"), "#/components/schemas/analysis-status-schema"},
		{at(t, slice, "components", "schemas", "result", "discriminator", "mapping", "name"), "#/components/schemas/analysis-status-schema"},
		{at(t, slice, "components", "schemas", "result", "discriminator", "mapping", "other"), "team"},
	} {
		if test.got != test.want {
			t.Errorf("reference = %v, want %s", test.got, test.want)
		}
	}

	taken := decode(t, `{"components": {
	  "schemas": {"team": {"type": "object", "properties": {"type": {"type": "string", "enum": ["enterprise"]}}}},
	  "parameters": {"team-type": {"name": "a", "in": "query"}, "team-type-parameter": {"name": "b", "in": "query"}}
	}}`)
	if err := normalize(taken); err == nil || !strings.Contains(err.Error(), "team-type-parameter is taken") {
		t.Fatalf("normalize() error = %v, want the taken name refused", err)
	}
}

func TestTypeKey(t *testing.T) {
	for _, names := range [][2]string{
		{"rule-suite-result", "RuleSuite_Result"},
		{"team-type", "TeamType"},
		{"secret_scanning.alert~validity", "SecretScanningAlertValidity"},
		{"v2-id", "V2ID"},
	} {
		if typeKey(names[0]) != typeKey(names[1]) {
			t.Errorf("typeKey(%q) = %q, typeKey(%q) = %q, want equal", names[0], typeKey(names[0]), names[1], typeKey(names[1]))
		}
	}
	if typeKey("team-type") == typeKey("team-types") {
		t.Error("different names share a type key")
	}
}

func TestNormalizeNamesListParametersApart(t *testing.T) {
	// Zendesk filters users by role, or by several with role[]=agent&role[]=admin.
	slice := decode(t, `{"openapi": "3.0.3", "paths": {
	  "/users": {"get": {"operationId": "list", "responses": {}, "parameters": [
	    {"$ref": "#/components/parameters/roles"}, {"$ref": "#/components/parameters/role"}, {"name": "role", "in": "path", "required": true, "schema": {"type": "string"}}
	  ]}},
	  "/groups/{id}/users": {
	    "parameters": [{"$ref": "#/components/parameters/role"}],
	    "get": {"operationId": "group-users", "responses": {}, "parameters": [{"$ref": "#/components/parameters/roles"}, {"name": "tag[]", "in": "query", "schema": {"type": "array", "items": {"type": "string"}}}]}
	  },
	  "/tags": {"get": {"operationId": "tags", "responses": {}, "parameters": [
	    {"name": "tag", "in": "query", "schema": {"type": "string"}}, {"name": "tag[]", "in": "query", "schema": {"type": "array", "items": {"type": "string"}}},
	    {"name": "custom_field[]", "in": "query", "schema": {"type": "array", "items": {"type": "string"}}}, {"name": "custom_field", "in": "query", "schema": {"type": "string"}}
	  ]}},
	  "/teams/{id}/users": {
	    "parameters": [{"$ref": "#/components/parameters/role"}],
	    "get": {"operationId": "team-users", "responses": {}, "parameters": [{"name": "role", "in": "query", "description": "Replaces the path's role.", "schema": {"type": "string"}}]}
	  }
	}, "components": {"parameters": {
	  "role": {"name": "role", "in": "query", "schema": {"type": "string"}},
	  "roles": {"name": "role[]", "in": "query", "schema": {"type": "array", "items": {"type": "string"}}}
	}}}`)
	if err := normalize(slice); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		parameter any
		want      any
	}{
		// role[] is renamed where the component is defined, for every use.
		{at(t, slice, "components", "parameters", "roles"), "RoleList"},
		{at(t, slice, "components", "parameters", "role"), nil},
		{at(t, slice, "paths", "/tags", "get", "parameters", 1), "TagList"},
		{at(t, slice, "paths", "/tags", "get", "parameters", 0), nil},
		{at(t, slice, "paths", "/tags", "get", "parameters", 2), "CustomFieldList"},
		{at(t, slice, "paths", "/tags", "get", "parameters", 3), nil},
		// An operation's parameter replaces its path's of the same name.
		{at(t, slice, "paths", "/teams/{id}/users", "get", "parameters", 0), nil},
		// A list parameter with nothing of its name beside it is left alone,
		// as is a path parameter, which is an argument rather than a field.
		{at(t, slice, "paths", "/groups/{id}/users", "get", "parameters", 1), nil},
		{at(t, slice, "paths", "/users", "get", "parameters", 2), nil},
	} {
		parameter := test.parameter.(map[string]any)
		if got := parameter["x-go-name"]; got != test.want {
			t.Errorf("parameter %v x-go-name = %v, want %v", parameter["name"], got, test.want)
		}
	}

	for name, parameters := range map[string]string{
		"two names alike without []": `[{"name": "page_size", "in": "query"}, {"name": "page-size", "in": "query"}]`,
		"a list named after another": `[{"name": "role_list", "in": "header"}, {"name": "role", "in": "query"}, {"name": "role[]", "in": "query"}]`,
	} {
		slice := decode(t, `{"paths": {"/p": {"get": {"operationId": "p", "responses": {}, "parameters": `+parameters+`}}}}`)
		if err := normalize(slice); err == nil || !strings.Contains(err.Error(), "GET /p: parameters") {
			t.Errorf("%s: normalize() error = %v, want the pair refused", name, err)
		}
	}
}
