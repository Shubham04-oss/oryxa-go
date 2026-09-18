package sources

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	knowledgev1 "github.com/Shubham04-oss/oryxa-go/contracts/events/knowledge/v1"

	"github.com/Shubham04-oss/oryxa-go/knowledge/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/knowledge/internal/filesclient"
	"github.com/Shubham04-oss/oryxa-go/knowledge/internal/transform"
)

// filesMaxPage bounds how many objects one call reads bytes for. Unlike every
// other extractor here, the page size is not what the provider is asked for —
// files answers its whole listing at once — but how much of that answer this
// call fetches content for before handing the worker back.
const filesMaxPage = 50

// objectLister is what Files needs of the files service, so a test answers
// without one.
type objectLister interface {
	Objects(ctx context.Context, organizationID, accessToken string, query filesclient.Query) ([]filesclient.Object, error)
	Content(ctx context.Context, organizationID, fileID, accessToken string) ([]byte, string, error)
}

// Files reads the organization's own object storage.
//
// It is the one internal source, and it does not behave like the others.
// Files has no service identity, so the credential is a member's own token
// rather than a provider credential; its listing takes no cursor and answers
// everything at once; and a file whose retention lapsed simply stops
// answering, with no event to say so.
type Files struct {
	client objectLister
}

func NewFiles(client objectLister) *Files { return &Files{client: client} }

func (source *Files) Provider() string { return "files" }

// billingAttachment is the purpose files gives an invoice or receipt the
// organization keeps. Files shows those to admins alone, and they are a
// record of what the organization paid, not knowledge anyone should search.
const billingAttachment = "billing.attachment"

// Target accepts {"team": "<id>"} to index one team's files, {"purpose":
// "<purpose>"} to narrow to a kind, or nothing to index the organization's
// own shared files.
func (source *Files) Target(target map[string]string) error {
	if team, named := target["team"]; named && strings.TrimSpace(team) == "" {
		return MissingTarget("a team id")
	}
	if purpose, named := target["purpose"]; named {
		switch strings.TrimSpace(purpose) {
		case "":
			return MissingTarget("a purpose")
		case billingAttachment:
			return fmt.Errorf("billing attachments are not indexed: %w", domain.ErrInvalid)
		}
	}
	return nil
}

// Fetch reads the next slice of an organization's files.
//
// Files has no pagination of its own — one call answers the whole listing —
// so the walk is paged here instead, against a stable ordering by id, with
// the cursor naming the last object read. That keeps a resumed pass from
// starting over, and keeps a file uploaded mid-walk from shifting everything
// after it the way an offset would.
func (source *Files) Fetch(ctx context.Context, request Request) (Page, error) {
	query := filesclient.Query{
		TeamID:  strings.TrimSpace(request.Source.Target["team"]),
		Purpose: strings.TrimSpace(request.Source.Target["purpose"]),
	}
	// The scope is always named, and that matters more than it looks.
	//
	// Everything this source publishes carries its own scope: the
	// organization's, or one team's when the target names a team. Files,
	// asked with no scope at all, answers the caller's own private files
	// alongside the shared ones — which would put the registering member's
	// private files into an index others search. So this asks for exactly
	// the scope the documents will carry, and a member's own files are simply
	// not what this source is for.
	query.Scope = string(domain.ScopeOrganization)
	if query.TeamID != "" {
		query.Scope = string(domain.ScopeTeam)
	}

	objects, err := source.client.Objects(ctx, request.Source.OrganizationID, request.Credential.AccessToken, query)
	if err != nil {
		return Page{}, err
	}
	slices.SortFunc(objects, func(left, right filesclient.Object) int {
		return strings.Compare(left.ID, right.ID)
	})

	after := request.Cursor.Value
	size := Limit(request.Limit, filesMaxPage)
	page := Page{Items: make([]Item, 0, size)}
	for _, object := range objects {
		if after != "" && object.ID <= after {
			continue
		}
		if len(page.Items) >= size {
			// More remain, so the walk carries on from the last one read.
			page.Cursor = after
			return page, nil
		}
		after = object.ID
		if !publishable(object, query) {
			continue
		}

		item, ok, err := source.read(ctx, request, object)
		if err != nil {
			return Page{}, err
		}
		if !ok {
			continue
		}
		page.Items = append(page.Items, item)
		page.Watermark = watermarkAfter(page.Watermark, object.CreatedAt)
	}
	return page, nil
}

// publishable is the second guard on what a pass publishes, after the scope
// the listing asked for. The source reads as the member who registered it,
// and that is an admin more often than not — only an admin can share a file
// with the whole organization — so files answers an admin's view: billing
// attachments included. An object is published only if it is what this
// source's scope may carry, and never if it is a billing attachment.
func publishable(object filesclient.Object, query filesclient.Query) bool {
	if object.Purpose == billingAttachment {
		return false
	}
	if object.Scope != query.Scope {
		return false
	}
	return query.Scope != string(domain.ScopeTeam) || object.TeamID == query.TeamID
}

// read turns one stored object into an item, or says to pass over it.
func (source *Files) read(ctx context.Context, request Request, object filesclient.Object) (Item, bool, error) {
	// A file's type is checked before its bytes are asked for: most of what an
	// organization stores is not text, and fetching a PDF to discard it is a
	// round trip for nothing.
	declared := object.ContentType
	if named := transform.MIMEForFilename(object.Filename); named != "" {
		declared = named
	}
	mime, err := transform.CanonicalMIME(declared)
	if err != nil {
		return Item{}, false, nil
	}

	content, served, err := source.client.Content(ctx, request.Source.OrganizationID, object.ID, request.Credential.AccessToken)
	if err != nil {
		// Retention lapsing is silent — files publishes no event for it and
		// simply stops answering — so a missing file is something this pass
		// steps over. The next full pass retracts what it did not see.
		if errors.Is(err, domain.ErrNotFound) {
			return Item{}, false, nil
		}
		return Item{}, false, err
	}
	if canonical, err := transform.CanonicalMIME(served); err == nil {
		mime = canonical
	}

	created := object.CreatedAt
	return Item{
		// Objects are immutable and there is no overwrite, so a file's id
		// names the same bytes for as long as it exists.
		ExternalID: object.ID,
		Kind:       knowledgev1.KindFile,
		Title:      object.Filename,
		MIME:       mime,
		Content:    content,
		Metadata: map[string]string{
			"filename":   object.Filename,
			"purpose":    object.Purpose,
			"sha256":     object.SHA256,
			"created_by": object.CreatedBy,
		},
		UpdatedAt: &created,
	}, true, nil
}
