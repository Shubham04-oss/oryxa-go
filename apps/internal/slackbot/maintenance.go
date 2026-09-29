package slackbot

import (
	"context"
	"time"
)

// How long each kind of record is kept.
const (
	// flowRetention is how long a finished or lapsed install in progress is
	// kept, after it expired.
	flowRetention = time.Hour
	// postRetention is how long a post is remembered, for its key and for an
	// update to find it.
	postRetention = 30 * 24 * time.Hour
	// profileRetention is how long what Slack said about a person is kept at
	// all. It is used for a day, and refreshed after that.
	profileRetention = 7 * 24 * time.Hour
	// maintenanceEvery is how often the sweep runs.
	maintenanceEvery = 15 * time.Minute
)

// RunMaintenance forgets what has outlived its retention, until ctx is
// cancelled: what people typed and clicked after 7 days, and the rest of what
// the surface keeps on its own, shorter or longer, schedules.
func (s *Service) RunMaintenance(ctx context.Context) {
	for {
		s.maintain(ctx)
		select {
		case <-ctx.Done():
			return
		case <-time.After(maintenanceEvery):
		}
	}
}

func (s *Service) maintain(ctx context.Context) {
	now := s.now().UTC()
	purged, err := s.store.Purge(ctx, Cutoffs{
		Inbound: now.Add(-InboundRetention), Flows: now.Add(-flowRetention),
		Posts: now.Add(-postRetention), Profiles: now.Add(-profileRetention),
	})
	switch {
	case err != nil:
		if ctx.Err() == nil {
			s.logger.ErrorContext(ctx, "forget Slack records past their retention", "error", err)
		}
	case purged != Purged{}:
		s.logger.InfoContext(ctx, "slack records past their retention forgotten", "inbound", purged.Inbound,
			"install_flows", purged.Flows, "posts", purged.Posts, "profiles", purged.Profiles)
	}
}
