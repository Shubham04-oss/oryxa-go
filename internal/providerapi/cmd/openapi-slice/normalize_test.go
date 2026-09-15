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

	for name, schema := range map[string]string{
		"an object variant":    `{"oneOf": [{"type": "object"}, {"type": "string"}]}`,
		"a referenced variant": `{"anyOf": [{"$ref": "#/components/schemas/id"}, {"type": "string"}]}`,
		"two array variants":   `{"oneOf": [{"type": "array", "items": {"type": "string"}}, {"type": "array", "items": {"type": "integer"}}]}`,
	} {
		slice := decode(t, `{"components": {"parameters": {"id": {"name": "id", "in": "query", "schema": `+schema+`}}}}`)
		if err := normalize(slice); err == nil || !strings.Contains(err.Error(), `parameter "id"`) {
			t.Errorf("%s: normalize() error = %v, want parameter \"id\" refused", name, err)
		}
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
