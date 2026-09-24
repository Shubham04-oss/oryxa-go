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
	// originalsEnabled says a PDF or an Office file is worth reading the
	// bytes of at all: without somewhere to keep such a file, as
	// KNOWLEDGE_ORIGINALS_BUCKET does, its bytes are never worth the round
	// trip, so the pre-filter in read continues to skip it before asking for
	// content the same way it always has.
	originalsEnabled bool
}

// FilesOption configures Files beyond the client it reads through.
type FilesOption func(*Files)

// WithOriginals lets Files pass a PDF or an Office file through as a binary
// item — its real MIME, its bytes untouched — instead of skipping it,
// because the caller has somewhere to keep such a file's bytes. Without it,
// Files behaves exactly as it always has: only a type transform.CanonicalMIME
// already reads is published.
func WithOriginals() FilesOption {
	return func(source *Files) { source.originalsEnabled = true }
}

func NewFiles(client objectLister, options ...FilesOption) *Files {
	source := &Files{client: client}
	for _, option := range options {
		option(source)
	}
	return source
}

func (source *Files) Provider() string { return "files" }

// billingAttachment is the purpose files gives an invoice or receipt the
// organization keeps. Files shows those to admins alone, and they are a
// record of what the organization paid, so its index entry is admin-only.
const billingAttachment = "billing.attachment"

// Target accepts {"member": "self"} for the caller's own private files,
// {"team": "<id>"} for one team's files, {"purpose": "<purpose>"} to narrow
// to a kind, or nothing for the organization's shared files.
func (source *Files) Target(target map[string]string) error {
	if member, named := target["member"]; named && member != "self" {
		return fmt.Errorf("files member target must be self: %w", domain.ErrInvalid)
	}
	if target["member"] != "" && target["team"] != "" {
		return fmt.Errorf("files target cannot select a member and team together: %w", domain.ErrInvalid)
	}
	if team, named := target["team"]; named && strings.TrimSpace(team) == "" {
		return MissingTarget("a team id")
	}
	if purpose, named := target["purpose"]; named {
		if strings.TrimSpace(purpose) == "" {
			return MissingTarget("a purpose")
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
	if request.Source.Scope == domain.ScopeMember && request.Source.MemberID == "" {
		return Page{}, fmt.Errorf("a member files source has no owner: %w", domain.ErrInvalid)
	}
	if request.Source.Scope == domain.ScopeTeam &&
		(request.Source.TeamID == "" || request.Source.Target["team"] != request.Source.TeamID) {
		return Page{}, fmt.Errorf("a team files source has no matching team target: %w", domain.ErrInvalid)
	}
	query := filesclient.Query{
		TeamID:  strings.TrimSpace(request.Source.Target["team"]),
		Purpose: strings.TrimSpace(request.Source.Target["purpose"]),
	}
	// The scope is always named, and that matters more than it looks.
	//
	// Everything this source publishes carries its own scope. Files asked
	// with no scope also answers the caller's private files, which could
	// otherwise land in a broader index. Ask for the exact scope and check
	// each returned object's owner before reading its bytes.
	query.Scope = string(domain.ScopeOrganization)
	if request.Source.Scope == domain.ScopeMember && request.Source.MemberID != "" {
		query.Scope = string(domain.ScopeMember)
	} else if query.TeamID != "" {
		query.Scope = string(domain.ScopeTeam)
	}

	objects, err := source.client.Objects(ctx, request.Source.OrganizationID, request.Credential.AccessToken, query)
	if err != nil {
		return Page{}, refused(query, err)
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
		if !publishable(object, query, request.Source.MemberID) {
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

// refused says what a listing Files would not answer means. Not found and
// forbidden are Files refusing the member a source reads as the placement
// itself — they left the team, or are no longer an admin — which no retry
// fixes, unlike Files being down or a token it did not take.
func refused(query filesclient.Query, err error) error {
	if errors.Is(err, domain.ErrNotFound) || errors.Is(err, domain.ErrForbidden) {
		return fmt.Errorf("files will not list these %s files for the member this reads as: %w: %w",
			query.Scope, domain.ErrUnreadable, err)
	}
	return err
}

// publishable is the second guard on what a pass publishes, after the scope
// the listing asked for. The source reads as the member who registered it,
// and that is an admin more often than not — only an admin can share a file
// with the whole organization — so files answers an admin's view. An object
// is published only if it is what this source's scope may carry; its purpose
// travels with it so RAG can enforce the document ACL.
func publishable(object filesclient.Object, query filesclient.Query, memberID string) bool {
	if object.Scope != query.Scope {
		return false
	}
	switch query.Scope {
	case string(domain.ScopeTeam):
		return object.TeamID == query.TeamID
	case string(domain.ScopeMember):
		return memberID != "" && object.MemberID == memberID
	default:
		return true
	}
}

// read turns one stored object into an item, or says to pass over it.
func (source *Files) read(ctx context.Context, request Request, object filesclient.Object) (Item, bool, error) {
	if object.Access != "" && object.Access != "members" && object.Access != "admins" {
		return Item{}, false, fmt.Errorf("files returned invalid access for %s: %w", object.ID, domain.ErrInvalid)
	}
	// A file's type is checked before its bytes are asked for: most of what an
	// organization stores is not text, and fetching a PDF to discard it is a
	// round trip for nothing — unless there is somewhere to keep a PDF's
	// bytes regardless of never reading them as text, which WithOriginals
	// says.
	declared := object.ContentType
	if named := transform.MIMEForFilename(object.Filename); named != "" {
		declared = named
	}
	mime, err := transform.CanonicalMIME(declared)
	passthrough := false
	if err != nil {
		if !source.originalsEnabled || !transform.IsPassthroughMIME(declared) {
			return Item{}, false, nil
		}
		mime, passthrough = declared, true
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
		mime, passthrough = canonical, false
	} else if passthrough && transform.IsPassthroughMIME(served) {
		// What files actually served is more trustworthy than what was
		// declared at listing time, and is still a type worth keeping.
		mime = served
	}
	if passthrough && len(content) > MaxOriginalBytes {
		// Too large to keep; counted as skipped the same as any other file
		// this pass steps over. transform.Normalize applies its own, much
		// smaller ceiling to an ordinary text file instead of this one.
		return Item{}, false, nil
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
			"access":     object.Access,
			"sha256":     object.SHA256,
			"created_by": object.CreatedBy,
		},
		UpdatedAt: &created,
	}, true, nil
}
