package integration

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
)

type fakeIntegration struct {
	manifest Manifest
	tools    map[string]ToolFunc
}

func (f fakeIntegration) Manifest() Manifest         { return f.manifest }
func (f fakeIntegration) Tools() map[string]ToolFunc { return f.tools }

func noop(context.Context, Session, json.RawMessage) (json.RawMessage, error) { return nil, nil }

// runnable is an integration with a function for each of its manifest's
// tools.
func runnable(manifest Manifest) fakeIntegration {
	tools := map[string]ToolFunc{}
	for _, tool := range manifest.Tools {
		tools[tool.Name] = noop
	}
	return fakeIntegration{manifest: manifest, tools: tools}
}

// minimalManifest is the least a manifest can be: one way to sign in, and a
// test call.
func minimalManifest(toolkit string) Manifest {
	return Manifest{
		Toolkit: toolkit, Version: 1, Name: toolkit, APIBase: "https://api.example",
		Auth: Auth{
			Methods: []Method{{Kind: KindAPIToken, Place: &Place{In: InHeader, Name: "Authorization", Template: "Bearer {token}"}}},
			Test:    Test{Path: "/me", Label: "/name"},
		},
	}
}

func TestRegistryFilesIntegrationsByToolkit(t *testing.T) {
	t.Parallel()
	registry := NewRegistry()
	acme := runnable(goodManifest())
	for _, integration := range []Integration{runnable(minimalManifest("zeta")), acme} {
		if err := registry.Register(integration); err != nil {
			t.Fatal(err)
		}
	}
	if got := registry.Toolkits(); !slices.Equal(got, []string{"acme", "zeta"}) {
		t.Fatalf("toolkits = %v", got)
	}
	found, ok := registry.Lookup("acme")
	if !ok || found.Manifest().Name != "Acme" {
		t.Fatalf("lookup = %v, %v", found, ok)
	}
	if _, ok := registry.Lookup("github"); ok {
		t.Fatal("found a toolkit nobody registered")
	}
}

func TestRegistryRefusesWhatItCannotServe(t *testing.T) {
	t.Parallel()
	invalid := goodManifest()
	invalid.Version = 0
	undeclared := runnable(minimalManifest("zeta"))
	undeclared.tools = map[string]ToolFunc{"ZETA_SECRET_TOOL": noop}
	missing := runnable(goodManifest())
	delete(missing.tools, "ACME_DELETE_WIDGET")
	unset := runnable(goodManifest())
	unset.tools["ACME_DELETE_WIDGET"] = nil

	for name, testCase := range map[string]struct {
		integration Integration
		says        string
	}{
		"nothing at all":                      {nil, "nil integration"},
		"a manifest that does not validate":   {runnable(invalid), "invalid manifest"},
		"a function for an undeclared tool":   {undeclared, "does not declare"},
		"a declared tool with no function":    {missing, "has no function"},
		"a declared tool with a nil function": {unset, "has no function"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			err := NewRegistry().Register(testCase.integration)
			if err == nil || !strings.Contains(err.Error(), testCase.says) {
				t.Fatalf("error = %v, want one saying %q", err, testCase.says)
			}
		})
	}

	registry := NewRegistry()
	if err := registry.Register(runnable(invalid)); !errors.Is(err, ErrInvalidManifest) {
		t.Fatalf("error = %v, want an invalid manifest", err)
	}
	if len(registry.Toolkits()) != 0 {
		t.Fatal("an invalid manifest was registered")
	}
}

func TestRegistryRefusesAToolkitTwice(t *testing.T) {
	t.Parallel()
	registry := NewRegistry()
	if err := registry.Register(runnable(minimalManifest("zeta"))); err != nil {
		t.Fatal(err)
	}
	second := minimalManifest("zeta")
	second.Name = "Another Zeta"
	if err := registry.Register(runnable(second)); err == nil || !strings.Contains(err.Error(), "already registered") {
		t.Fatalf("error = %v, want already registered", err)
	}
	if found, _ := registry.Lookup("zeta"); found.Manifest().Name != "zeta" {
		t.Fatal("the second registration replaced the first")
	}
}
