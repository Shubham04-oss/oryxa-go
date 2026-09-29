package sources

// One extractor reads every toolkit apps has a reader for, rather than one
// per app: a member's own Oryxa sign-in for GitHub and their own for Drive
// are read the same way, by apps' own reader, through the pages route. It is
// mounted beside whatever each toolkit is already read by, so a
// Composio-backed source is read exactly as it always has been.

import (
	"context"
	"fmt"
	"maps"
	"strings"

	"github.com/Shubham04-oss/oryxa-go/knowledge/internal/appsclient"
	"github.com/Shubham04-oss/oryxa-go/knowledge/internal/domain"
)

// appsMaxPage is the most one page may ask for. A page is a unit of work a
// lease has to cover, and the caller already asks for a hundred; what bounds
// it in practice is the 6 MiB apps caps a page at, which appsclient refuses to
// read past.
const appsMaxPage = 100

// pageReader is what Apps needs of apps, so a test answers without one.
type pageReader interface {
	Pages(ctx context.Context, organizationID, connectionID, accessToken string, request appsclient.PageRequest) (appsclient.Page, error)
}

// Apps reads a toolkit through the connection's own reader in apps.
//
// The credential is a member's own token, minted from their standing grant,
// as the files source is read: apps verifies it with auth and answers only
// for the connection's own member, so a pass reads what that member could
// read at a browser and nothing more.
type Apps struct{ client pageReader }

func NewApps(client pageReader) *Apps { return &Apps{client: client} }

// Provider is "apps", since this extractor reads no provider of its own: it
// is mounted under each toolkit whose integration declares a reader, and
// which one it is comes from the source in front of it.
func (source *Apps) Provider() string { return "apps" }

// ReadsThroughApps marks this as the extractor that reads through apps rather
// than Composio's proxy, which is what lets a toolkit's source be registered
// on an Oryxa connection at all (see application.usableConnection). No other
// extractor implements it: a Composio-backed one reads through Composio, which
// cannot see a connection Oryxa's own sign-in holds instead.
type AppsReader interface{ ReadsThroughApps() bool }

func (source *Apps) ReadsThroughApps() bool { return true }

// Target accepts what one of the connection's own targets looks like: the
// reader it is for, and the fields that reader needs to find it — a
// repository, a channel, a folder. What those fields may be is the reader's
// own to say, and apps refuses a target it cannot read when the source is
// registered on it.
func (source *Apps) Target(target map[string]string) error {
	if strings.TrimSpace(target["reader"]) == "" {
		return MissingTarget("a reader, as the connection's own targets name it")
	}
	return nil
}

// Fetch asks apps for the next page of the target, as the member the source
// reads as.
//
// There is no Held here, and that is the plan's decision rather than an
// omission: apps' readers read by the provider's own change feed, so what a
// pass is asked for is already only what changed. What did not change comes
// back anyway, and knowledge's content hash is what keeps it from being
// published twice.
func (source *Apps) Fetch(ctx context.Context, request Request) (Page, error) {
	if request.Source.ConnectionID == "" {
		return Page{}, fmt.Errorf("an apps source reads through a connection: %w", domain.ErrInvalid)
	}
	answer, err := source.client.Pages(ctx, request.Source.OrganizationID, request.Source.ConnectionID,
		request.Credential.AccessToken, askedFor(request))
	if err != nil {
		// Apps says what went wrong in the terms a pass acts on — retry,
		// give up, or pause every source on the connection — and each of them
		// already means something to the caller. Nothing is reinterpreted
		// here: unlike files, apps is not a service deciding what a member may
		// read, so "not there" is not the same as "no longer readable" and
		// must not retire a source and everything it holds.
		return Page{}, err
	}
	return asPage(answer), nil
}

// Check asks apps for one item of the target, as the member the source would
// read as. A limit of one is the whole check: the cheapest call that can fail
// proves the token is the member's own, that the connection is the reader's,
// and that the target is one it can read. Everything else about a pass is
// then only a matter of time.
func (source *Apps) Check(ctx context.Context, request Request) error {
	probe := request
	// A check is not a pass: it asks for the beginning of the target, one
	// item of it, and nothing about where an earlier pass stopped.
	probe.Cursor, probe.Limit, probe.Full = domain.Cursor{}, 1, false
	_, err := source.Fetch(ctx, probe)
	return err
}

// askedFor is what one pass asks apps for. The walk's own place and the
// window the last drained pass left are separate: a pass that stopped
// part-way resumes the walk, and a pass that starts one asks for what changed
// since the window closed. Only a full pass asks for everything, and so
// carries no window at all.
func askedFor(request Request) appsclient.PageRequest {
	asked := appsclient.PageRequest{
		Target: maps.Clone(request.Source.Target),
		Cursor: request.Cursor.Value,
		Full:   request.Full,
		Limit:  Limit(request.Limit, appsMaxPage),
	}
	if !request.Full {
		// A reader that reads by time answers a moment, one that reads by
		// its own feed answers an opaque token, and at most one of the two
		// is ever set.
		asked.Watermark, asked.Token = request.Cursor.Watermark, request.Cursor.Token
	}
	return asked
}

// asPage is apps' page as knowledge's own. The items are one-to-one; the only
// thing read differently is an item with no id, which is not a document: what
// makes a document the same document is the app's own name for it, and one
// without a name is stepped over rather than written under an empty id that
// every such item would share.
func asPage(answer appsclient.Page) Page {
	page := Page{Cursor: answer.Cursor, Watermark: answer.Watermark, Token: answer.Token, Removed: answer.Removed}
	page.Items = make([]Item, 0, len(answer.Items))
	for _, item := range answer.Items {
		if strings.TrimSpace(item.ExternalID) == "" {
			continue
		}
		page.Items = append(page.Items, Item{
			ExternalID: item.ExternalID, Kind: item.Kind, Title: item.Title, URL: item.URL,
			MIME: item.MIME, Content: item.Content, Metadata: item.Metadata, UpdatedAt: item.UpdatedAt,
		})
	}
	return page
}

// RegisterApps mounts one apps reader beside every extractor the registry
// already holds, so each toolkit is read through whichever of the two holds
// the credential behind the source in front of it. It answers the same
// registry, and is a no-op for a provider that is not a toolkit: files is the
// organization's own storage and keeps reading as it always has.
//
// This is what "one extractor, apps, registered under each toolkit whose
// integration has readers" means in a build that cannot see those manifests.
// A toolkit whose integration has no reader yet is mounted too, and says so
// where it matters: the check refuses it, so nothing is ever registered on a
// reader that is not there.
func RegisterApps(registry Registry, client pageReader) Registry {
	if client == nil {
		return registry
	}
	reader := NewApps(client)
	for toolkit, extractor := range registry {
		if toolkit == "files" {
			continue
		}
		registry[toolkit] = &perBroker{toolkit: toolkit, composio: extractor, apps: reader}
	}
	return registry
}

// perBroker is one toolkit's mounted extractor: what this build has always
// read that toolkit by, and the apps reader, side by side.
//
// A registry holds one extractor per provider and a source carries no
// extractor of its own, so which of the two reads a source is decided here,
// by the credential behind it: a Composio account for a Composio connection,
// and a member's own token for one Oryxa signed in for. That is the same
// rule application.credential uses to decide what a pass is read with, and
// the same one the source itself records.
type perBroker struct {
	toolkit  string
	composio Extractor
	apps     *Apps
}

func (extractor *perBroker) Provider() string { return extractor.toolkit }

// ReadsThroughApps says this toolkit can be read through apps, which is what
// lets a source be registered on an Oryxa connection for it.
func (extractor *perBroker) ReadsThroughApps() bool { return true }

// Target decides which of the two a target is for, before the connection
// behind the source is known. An apps target always names the reader it is
// for and a Composio one never does, so the target says which it means; a
// target that is wrong about that is refused by Check, once the connection
// has said which broker holds it.
func (extractor *perBroker) Target(target map[string]string) error {
	if strings.TrimSpace(target["reader"]) != "" {
		return extractor.apps.Target(target)
	}
	return extractor.composio.Target(target)
}

// Fetch reads a source through whoever holds its credential: apps' own reader
// for an Oryxa connection, and the extractor this toolkit has always had for
// a Composio one.
func (extractor *perBroker) Fetch(ctx context.Context, request Request) (Page, error) {
	if viaComposio(request) {
		return extractor.composio.Fetch(ctx, request)
	}
	return extractor.apps.Fetch(ctx, request)
}

// Check proves a source can be read as registered, before it is registered.
// For an Oryxa connection that is one item of the target from apps; for a
// Composio one it is the same rule the target was just checked against, run
// again once the connection has said which broker holds it — so a target
// meant for an apps reader is refused here rather than read as something else
// on the first pass.
func (extractor *perBroker) Check(ctx context.Context, request Request) error {
	if viaComposio(request) {
		return extractor.composio.Target(request.Source.Target)
	}
	return extractor.apps.Check(ctx, request)
}

// viaComposio says which of the two holds the credential behind a source: a
// Composio account means Composio's proxy places the app's own, and anything
// else means the source is read as the member who made it.
func viaComposio(request Request) bool {
	return request.Source.ComposioAccountID != ""
}
