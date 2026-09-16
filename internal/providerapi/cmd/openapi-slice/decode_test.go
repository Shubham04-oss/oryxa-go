package main

import (
	"reflect"
	"strings"
	"testing"
)

func TestDescriptionsDecodeTheSameFromJSONAndYAML(t *testing.T) {
	const asJSON = `{
	  "openapi": "3.0.1",
	  "info": {"title": "Pets", "version": "2.16", "x-released": "2026-09-15"},
	  "paths": {
	    "/pets": {
	      "get": {
	        "operationId": "listPets",
	        "tags": ["pets"],
	        "parameters": [{"name": "limit", "in": "query", "schema": {"type": "integer", "maximum": 9007199254740993, "default": 20}}],
	        "responses": {
	          "200": {"description": "Pets", "content": {"application/json": {"schema": {"$ref": "#/components/schemas/Pets"}}}},
	          "404": {"description": "Missing", "content": {"application/json": {"schema": {"$ref": "#/components/schemas/Error"}}}}
	        }
	      }
	    }
	  },
	  "components": {
	    "schemas": {
	      "Pets": {"type": "array", "nullable": true, "items": {"type": "object", "properties": {"weight": {"type": "number", "example": 1.50}, "hex": {"type": "integer", "example": 31}}}},
	      "Error": {"type": "object", "description": "Not found", "x-empty": null}
	    }
	  }
	}`
	// The YAML writes the same description with what YAML allows and JSON
	// does not: unquoted numeric keys, an anchor used twice, a merge key, an
	// unquoted date, and a hexadecimal number.
	const asYAML = `
openapi: 3.0.1
info:
  title: Pets
  version: "2.16"
  x-released: 2026-09-15
paths:
  /pets:
    get:
      operationId: listPets
      tags: [pets]
      parameters:
        - name: limit
          in: query
          schema: {type: integer, maximum: 9007199254740993, default: 20}
      responses:
        200:
          description: Pets
          content:
            application/json:
              schema:
                $ref: '#/components/schemas/Pets'
        404:
          <<: &missing
            description: Missing
          content:
            application/json:
              schema:
                $ref: '#/components/schemas/Error'
components:
  schemas:
    Pets:
      type: array
      nullable: true
      items:
        type: object
        properties:
          weight: {type: number, example: 1.50}
          hex: {type: integer, example: 0x1F}
    Error:
      <<: *missing
      type: object
      description: Not found
      x-empty: ~
`
	fromJSON, err := decodeDescription([]byte(asJSON))
	if err != nil {
		t.Fatal(err)
	}
	fromYAML, err := decodeDescription([]byte(asYAML))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(fromJSON, fromYAML) {
		t.Fatalf("YAML decoded differently:\n JSON %v\n YAML %v", fromJSON, fromYAML)
	}
	if encoded(t, fromYAML) != encoded(t, fromJSON) || !strings.Contains(encoded(t, fromYAML), `"maximum":9007199254740993`) {
		t.Fatalf("YAML encodes as %s", encoded(t, fromYAML))
	}
	// The slice of each is the same too.
	slicedJSON, errJSON := Slice(fromJSON, "pets")
	slicedYAML, errYAML := Slice(fromYAML, "pets")
	if errJSON != nil || errYAML != nil || encoded(t, slicedJSON) != encoded(t, slicedYAML) {
		t.Fatalf("slices differ: %v %v", errJSON, errYAML)
	}

	if _, err := decodeDescription([]byte("openapi: 3.0.1\n---\nopenapi: 3.0.1\n")); err == nil || !strings.Contains(err.Error(), "one document") {
		t.Errorf("decodeDescription(two documents) error = %v, want one document", err)
	}
	for name, raw := range map[string]string{
		"a list":               "- openapi\n",
		"an infinite number":   "openapi: 3.0.1\nx-limit: .inf\n",
		"a merge of a scalar":  "openapi: 3.0.1\ninfo:\n  <<: plain\n",
		"a key that is a list": "? [a, b]\n: value\n",
		"broken JSON":          `{"openapi": `,
		"broken YAML":          "openapi: [3.0.1\n",
	} {
		if _, err := decodeDescription([]byte(raw)); err == nil {
			t.Errorf("decodeDescription(%s) succeeded", name)
		}
	}
}
