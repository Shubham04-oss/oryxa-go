package authz

import (
	"errors"
	"strings"
	"testing"
)

// TestValidatePolicyAcceptsExamples checks every worked example this
// package documents or tests elsewhere: both from doc.go, the "only admins
// may search" and multi-statement examples engine_test.go authorizes
// against, and a bare permit-free forbid with no Oryxa:: reference at all
// (unusual, but not against any rule ValidatePolicy states).
func TestValidatePolicyAcceptsExamples(t *testing.T) {
	examples := []string{
		`forbid (
			principal,
			action == Oryxa::Action::"read",
			resource is Oryxa::Document
		) when {
			resource.metadata has confidential &&
			resource.metadata.confidential == "true"
		} unless {
			context.acts_as_admin
		};`,
		`forbid (principal is Oryxa::Agent, action, resource is Oryxa::Document) when { resource.access == "admins" };`,
		`forbid (principal, action == Oryxa::Action::"search", resource is Oryxa::KnowledgeBase) unless { context.acts_as_admin };`,
		`forbid (principal, action == Oryxa::Action::"search", resource is Oryxa::KnowledgeBase) unless { context.acts_as_admin };
			forbid (principal, action == Oryxa::Action::"read", resource is Oryxa::Document) when { resource.metadata has secret };`,
		`forbid (principal, action, resource) unless { context.acts_as_admin };`,
	}
	for _, text := range examples {
		if err := ValidatePolicy(text); err != nil {
			t.Errorf("ValidatePolicy(%q) error = %v, want it accepted", text, err)
		}
	}
}

// TestValidatePolicyRejects checks every way ValidatePolicy.s own doc
// comment says it can refuse a policy, in the order it checks them.
func TestValidatePolicyRejects(t *testing.T) {
	tests := []struct {
		name string
		text string
	}{
		{
			name: "too large",
			text: `forbid (principal, action, resource) when { "` + strings.Repeat("x", maxPolicyBytes) + `" == "y" };`,
		},
		{
			name: "does not parse",
			text: `forbid (principal, action, resource`,
		},
		{
			name: "no statements",
			text: `// nothing but a comment`,
		},
		{
			name: "a permit",
			text: `permit (principal, action, resource);`,
		},
		{
			name: "a permit alongside a forbid",
			text: `forbid (principal, action, resource) unless { context.acts_as_admin };
				permit (principal, action == Oryxa::Action::"read", resource is Oryxa::Document);`,
		},
		{
			name: "unknown entity type",
			text: `forbid (principal, action, resource is Oryxa::Folder);`,
		},
		{
			name: "unknown action",
			text: `forbid (principal, action == Oryxa::Action::"delete", resource);`,
		},
		{
			name: "evaluation error on a fixture",
			text: `forbid (principal, action == Oryxa::Action::"read", resource is Oryxa::Document) when { resource.metadata.confidential == "true" };`,
		},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			err := ValidatePolicy(testCase.text)
			if err == nil {
				t.Fatalf("ValidatePolicy() error = nil, want it refused")
			}
			if !errors.Is(err, ErrInvalid) {
				t.Fatalf("ValidatePolicy() error = %v, want it to wrap ErrInvalid", err)
			}
		})
	}
}

// TestValidatePolicyAcceptsAtTheSizeLimit checks the boundary itself, since
// TestValidatePolicyRejects only checks one byte over it.
func TestValidatePolicyAcceptsAtTheSizeLimit(t *testing.T) {
	// "forbid (principal, action, resource) when { \"\" == \"\" };" padded
	// with a comment to land exactly on maxPolicyBytes.
	base := `forbid (principal, action, resource) when { "" == "" };`
	padding := maxPolicyBytes - len(base) - len("// \n")
	if padding < 0 {
		t.Fatalf("base statement is already %d bytes, longer than the %d byte limit", len(base), maxPolicyBytes)
	}
	text := "// " + strings.Repeat("x", padding) + "\n" + base
	if len(text) != maxPolicyBytes {
		t.Fatalf("built a %d byte policy, want exactly %d", len(text), maxPolicyBytes)
	}
	if err := ValidatePolicy(text); err != nil {
		t.Fatalf("ValidatePolicy() at the size limit error = %v, want it accepted", err)
	}
}

// TestCheckReferencesIgnoresComments checks that a reference to something
// outside the model is only refused in live text, not inside a comment
// explaining it.
func TestCheckReferencesIgnoresComments(t *testing.T) {
	text := `// mentions Oryxa::Folder, which does not exist, only as an example
		forbid (principal, action, resource) unless { context.acts_as_admin };`
	if err := checkReferences(text); err != nil {
		t.Fatalf("checkReferences() error = %v, want a comment's reference ignored", err)
	}
}
