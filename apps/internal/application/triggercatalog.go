package application

import (
	"context"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/Shubham04-oss/oryxa-go/apps/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/internal/composio"
)

const (
	// triggerCatalogTTL is how long one app's trigger definitions are reused.
	// The catalog changes when Composio adds a trigger, which is rare, and a
	// stale entry can only mean a subscription is checked against the
	// definition of an hour ago.
	triggerCatalogTTL = time.Hour
	// triggerCatalogToolkits bounds how many apps' definitions are held, so
	// a deployment that has seen every app in the catalog does not hold it
	// all. The least recently read is dropped.
	triggerCatalogToolkits = 64
	// triggerCatalogPage is how many definitions one listing call reads.
	triggerCatalogPage = 50
	// triggerCatalogPages bounds the walk of one app's triggers. No app in
	// the catalog comes close: GitHub, among the largest, has 46.
	triggerCatalogPages = 40
)

// triggerCatalog holds each app's trigger definitions for a while, so
// checking a subscription against the trigger it names does not walk
// Composio's catalog every time. It is safe for concurrent use.
type triggerCatalog struct {
	composio Composio
	now      func() time.Time

	mu      sync.Mutex
	toolkit map[string]*toolkitTriggers
}

type toolkitTriggers struct {
	definitions map[string]domain.TriggerDefinition
	readAt      time.Time
	// loading is held while one caller reads this app's definitions, so a
	// burst of subscriptions to the same app makes one call rather than many.
	loading sync.Mutex
}

func newTriggerCatalog(client Composio, now func() time.Time) *triggerCatalog {
	return &triggerCatalog{composio: client, now: now, toolkit: map[string]*toolkitTriggers{}}
}

// Definition answers what Composio publishes about one trigger of one app.
// ErrNotFound means the app delivers no such event, which is the honest
// answer for a slug that does not exist and for one belonging to another app.
func (catalog *triggerCatalog) Definition(ctx context.Context, toolkit, slug string) (domain.TriggerDefinition, error) {
	definitions, err := catalog.definitions(ctx, toolkit)
	if err != nil {
		return domain.TriggerDefinition{}, err
	}
	definition, known := definitions[slug]
	if !known {
		return domain.TriggerDefinition{}, fmt.Errorf("%s delivers no event called %s: %w", toolkit, slug, domain.ErrNotFound)
	}
	return definition, nil
}

// definitions answers one app's trigger definitions, reading them from
// Composio when none are held or the ones held have aged out.
func (catalog *triggerCatalog) definitions(ctx context.Context, toolkit string) (map[string]domain.TriggerDefinition, error) {
	entry := catalog.entry(toolkit)

	entry.loading.Lock()
	defer entry.loading.Unlock()
	// Another caller may have filled it while this one waited.
	if held, fresh := catalog.held(toolkit); fresh {
		return held, nil
	}

	definitions, err := catalog.read(ctx, toolkit)
	if err != nil {
		return nil, err
	}
	catalog.mu.Lock()
	entry.definitions = definitions
	entry.readAt = catalog.now()
	catalog.evict()
	catalog.mu.Unlock()
	return definitions, nil
}

// entry answers the holder for one app, making it if there is none.
func (catalog *triggerCatalog) entry(toolkit string) *toolkitTriggers {
	catalog.mu.Lock()
	defer catalog.mu.Unlock()
	entry, held := catalog.toolkit[toolkit]
	if !held {
		entry = &toolkitTriggers{}
		catalog.toolkit[toolkit] = entry
	}
	return entry
}

// held answers what is kept for an app and whether it is still fresh.
func (catalog *triggerCatalog) held(toolkit string) (map[string]domain.TriggerDefinition, bool) {
	catalog.mu.Lock()
	defer catalog.mu.Unlock()
	entry, ok := catalog.toolkit[toolkit]
	if !ok || entry.definitions == nil {
		return nil, false
	}
	return entry.definitions, catalog.now().Sub(entry.readAt) < triggerCatalogTTL
}

// evict drops the least recently read apps once too many are held. The
// caller holds the lock.
func (catalog *triggerCatalog) evict() {
	if len(catalog.toolkit) <= triggerCatalogToolkits {
		return
	}
	// An app being read right now holds nothing yet, so dropping it frees
	// nothing and only costs that caller its read. Only apps that actually
	// hold definitions are candidates.
	var names []string
	for name, entry := range catalog.toolkit {
		if entry.definitions != nil {
			names = append(names, name)
		}
	}
	slices.SortFunc(names, func(a, b string) int {
		return catalog.toolkit[a].readAt.Compare(catalog.toolkit[b].readAt)
	})
	// Never more than are there: in-flight entries are not candidates, so
	// over-capacity can exceed what can be dropped.
	drop := min(max(len(catalog.toolkit)-triggerCatalogToolkits, 0), len(names))
	for _, name := range names[:drop] {
		delete(catalog.toolkit, name)
	}
}

// read walks every trigger one app delivers.
func (catalog *triggerCatalog) read(ctx context.Context, toolkit string) (map[string]domain.TriggerDefinition, error) {
	definitions := map[string]domain.TriggerDefinition{}
	cursor := ""
	for range triggerCatalogPages {
		page, err := catalog.composio.TriggerTypes(ctx, composio.TriggerTypeQuery{
			Toolkits: []string{toolkit}, Cursor: cursor, Limit: triggerCatalogPage,
		})
		if err != nil {
			return nil, composioError("list the app's triggers", err)
		}
		for _, trigger := range page.Items {
			slug, err := domain.CheckTool(trigger.Slug)
			if err != nil {
				// Composio's own slug does not look like one. Nothing can be
				// subscribed under it, so leaving it out is the whole effect.
				continue
			}
			definitions[slug] = domain.TriggerDefinition{
				Slug: slug, Toolkit: trigger.Toolkit, Name: trigger.Name, Instructions: trigger.Instructions,
				Kind: trigger.Kind, ConfigSchema: trigger.Config, ManualSetup: trigger.RequiresWebhookEndpointSetup,
			}
		}
		if cursor = page.NextCursor; cursor == "" {
			return definitions, nil
		}
	}
	return definitions, nil
}
