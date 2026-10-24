package spicedb

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"io"
	"log/slog"
	"regexp"
	"strings"
	"testing"

	v1 "github.com/authzed/authzed-go/proto/authzed/api/v1"
)

// TestCheckSchema runs against `spicedb serve-testing` at SPICEDB_ENDPOINT;
// a random key gives it a datastore of its own.
func TestCheckSchema(t *testing.T) {
	config := FromEnv()
	if !config.Enabled() {
		t.Skip("SPICEDB_ENDPOINT is not set")
	}
	random := make([]byte, 8)
	_, _ = rand.Read(random)
	config.PresharedKey = "schemacheck-" + hex.EncodeToString(random)
	client, err := Dial(config)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	ctx := context.Background()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	shadowOnly := func(string) string { return "" }
	appsSwitched := func(name string) string {
		if name == "ACCESS_ENGINE_APPS" {
			return "spicedb"
		}
		return ""
	}
	write := func(schema string) {
		t.Helper()
		if _, err := client.WriteSchema(ctx, &v1.WriteSchemaRequest{Schema: schema}); err != nil {
			t.Fatal(err)
		}
	}
	expect := func(name string, wantDiffs bool) {
		t.Helper()
		diffs, err := SchemaDiff(ctx, client)
		if err != nil {
			t.Fatal(err)
		}
		if (len(diffs) > 0) != wantDiffs {
			t.Fatalf("%s: differences %q, want any: %v", name, diffs, wantDiffs)
		}
		if err := CheckSchema(ctx, config, shadowOnly, logger); err != nil {
			t.Fatalf("%s: shadow only must start: %v", name, err)
		}
		if err := CheckSchema(ctx, config, appsSwitched, logger); (err != nil) != wantDiffs {
			t.Fatalf("%s: with apps switched, error %v, want one: %v", name, err, wantDiffs)
		}
	}

	expect("no schema", true)
	write("definition member {}\ndefinition org {\n relation member: member\n}")
	expect("an older schema", true)
	// Comments and layout aren't part of the schema SpiceDB compares.
	write(strings.ReplaceAll(regexp.MustCompile(`(?m)//.*$`).ReplaceAllString(Schema, ""), "\n", "\n\n  "))
	expect("the same schema without comments", false)
	write("definition member {}\ndefinition org {\n relation member: member\n}")
	if err := WriteSchema(ctx, client); err != nil {
		t.Fatal(err)
	}
	expect("after WriteSchema", false)
}
