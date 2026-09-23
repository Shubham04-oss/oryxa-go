package application

import (
	"testing"

	"github.com/Shubham04-oss/oryxa-go/knowledge/internal/domain"
)

func TestFilesBillingPurposeSetsAdminAccess(t *testing.T) {
	files := domain.Source{Provider: "files"}
	billing := domain.Document{Metadata: map[string]string{"purpose": "billing.attachment"}}
	if got := documentAccess(files, billing); got != "admins" {
		t.Fatalf("billing access = %q, want admins", got)
	}
	if got := documentAccess(files, domain.Document{Metadata: map[string]string{"purpose": "upload"}}); got != "members" {
		t.Fatalf("general access = %q, want members", got)
	}
	if got := documentAccess(files, domain.Document{Metadata: map[string]string{"purpose": "upload", "access": "admins"}}); got != "admins" {
		t.Fatalf("restricted shared file access = %q, want admins", got)
	}
	if got := documentAccess(domain.Source{Provider: "drive"}, billing); got != "members" {
		t.Fatalf("untrusted provider metadata set access %q", got)
	}
}
