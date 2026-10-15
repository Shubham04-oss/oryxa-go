package tools

import (
	"errors"
	"strings"
	"testing"
)

func TestProjectKeepsOnlyWhatTheSchemaNames(t *testing.T) {
	schema := []byte(`{"type":"object","properties":{"id":{"type":"string"},"items":{"type":"array","items":{"type":"object","properties":{"n":{"type":"integer"}}}}}}`)
	got, err := project(schema, `{"id":"a","secret":"s","items":[{"n":1,"token":"t"},{"n":2}]}`)
	if err != nil || string(got) != `{"id":"a","items":[{"n":1},{"n":2}]}` {
		t.Fatalf("project = %s, %v", got, err)
	}
}

func TestProjectKeepsWholeWhatTheSchemaLeavesOpen(t *testing.T) {
	got, err := project([]byte(`{"type":"object"}`), `{"a":1}`)
	if err != nil || string(got) != `{"a":1}` {
		t.Fatalf("project = %s, %v", got, err)
	}
}

func TestProjectRefusesOverTheCap(t *testing.T) {
	schema := []byte(`{"type":"object","properties":{"text":{"type":"string"}}}`)
	if _, err := project(schema, `{"text":"`+strings.Repeat("a", MaxOutput)+`"}`); !errors.Is(err, ErrOutputTooLarge) {
		t.Fatalf("err = %v, want ErrOutputTooLarge", err)
	}
	if _, err := project(schema, `{"text":"`+strings.Repeat("a", MaxOutput-20)+`"}`); err != nil {
		t.Fatalf("an output under the cap: %v", err)
	}
}
