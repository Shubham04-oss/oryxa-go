package application

import (
	"context"
	"errors"
	"testing"

	"github.com/Shubham04-oss/oryxa-go/knowledge/internal/authclient"
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

// A document's access label is decided by Files, not by who registered the
// source that read it. The creator of the organization's files source was an
// admin when they registered it; once they are an ordinary member, an
// admin-only body — a billing attachment, or a file shared with admins
// alone — is not theirs to read, whoever registered its source.
func TestAnAdminOnlyBodyIsReadOnlyByAnAdminNow(t *testing.T) {
	store := newFakeStore()
	store.sources["src_files"] = domain.Source{
		ID: "src_files", OrganizationID: "org_1", Provider: "files",
		Scope: domain.ScopeOrganization, CreatedBy: "user-1", Status: domain.StatusActive,
	}
	documents := map[string]map[string]string{
		"doc_invoice":    {"purpose": "billing.attachment", "access": "admins"},
		"doc_restricted": {"purpose": "upload", "access": "admins"},
		"doc_legacy":     {"purpose": "billing.attachment"},
		"doc_handbook":   {"purpose": "upload", "access": "members"},
	}
	for id, metadata := range documents {
		store.documents["src_files\x00"+id] = domain.Document{
			ID: id, OrganizationID: "org_1", SourceID: "src_files", ExternalID: id,
			Body: "the body of " + id, Metadata: metadata,
		}
	}
	credentials := authclient.Credentials{Authorization: "Bearer user-1"}

	demoted := registrar(t, store, member())
	for _, id := range []string{"doc_invoice", "doc_restricted", "doc_legacy"} {
		if document, err := demoted.Document(context.Background(), credentials, "org_1", id); !errors.Is(err, domain.ErrNotFound) {
			t.Fatalf("a member who created the source read %s: body %q, error %v", id, document.Body, err)
		}
	}
	if document, err := demoted.Document(context.Background(), credentials, "org_1", "doc_handbook"); err != nil || document.Body == "" {
		t.Fatalf("the source's creator could not read a members' document: %q, %v", document.Body, err)
	}

	// An agent acting for an admin is not the admin reading: an unattended
	// agent answers other people.
	admin := member()
	admin.Role = domain.OrgRoleAdmin
	agent := admin
	agent.Agent = true
	if _, err := registrar(t, store, agent).Document(context.Background(), credentials, "org_1", "doc_invoice"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("an admin's agent read an admin-only body: %v", err)
	}
	if document, err := registrar(t, store, admin).Document(context.Background(), credentials, "org_1", "doc_invoice"); err != nil || document.Body == "" {
		t.Fatalf("an admin could not read an admin-only body: %q, %v", document.Body, err)
	}
}
