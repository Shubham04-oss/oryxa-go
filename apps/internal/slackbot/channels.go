package slackbot

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/Shubham04-oss/oryxa-go/apps/internal/authclient"
	"github.com/Shubham04-oss/oryxa-go/apps/internal/domain"
)

const (
	// maxMarked bounds the channels an organization may mark.
	maxMarked = 100
	// channelPages and channelPageSize bound listing the channels the bot can
	// see: a thousand of them.
	channelPages    = 5
	channelPageSize = 200
)

// AdminChannel is a private channel the bot can see, or one that was marked
// and the bot no longer can.
type AdminChannel struct {
	ChannelID string `json:"channel_id"`
	Name      string `json:"name"`
	IsPrivate bool   `json:"is_private"`
	IsShared  bool   `json:"is_shared"`
	// Markable says the channel can be marked high priority: private, and
	// shared with nobody.
	Markable     bool       `json:"markable"`
	HighPriority bool       `json:"high_priority"`
	MarkedBy     string     `json:"marked_by,omitempty"`
	MarkedAt     *time.Time `json:"marked_at,omitempty"`
}

// ChannelsView is the channels an admin marks from.
type ChannelsView struct {
	Channels []AdminChannel `json:"channels"`
}

// Channels lists the private channels the bot is in, with the ones marked high
// priority. The bot sees only the channels it was invited to, so an admin
// invites it to a channel before marking it. A channel that stays marked
// after the bot leaves it is listed too, so it can be unmarked. Only an admin
// asks.
func (s *Service) Channels(ctx context.Context, credentials authclient.Credentials, organizationID string) (ChannelsView, error) {
	if _, err := s.orgAdmin(ctx, credentials, organizationID); err != nil {
		return ChannelsView{}, err
	}
	install, err := s.activeInstall(ctx, organizationID)
	if err != nil {
		return ChannelsView{}, err
	}
	return s.channelsView(ctx, install)
}

func (s *Service) channelsView(ctx context.Context, install Install) (ChannelsView, error) {
	token, err := s.botToken(ctx, install)
	if err != nil {
		return ChannelsView{}, err
	}
	marks, err := s.store.HighPriorityChannels(ctx, install.OrganizationID, install.TeamID)
	if err != nil {
		return ChannelsView{}, err
	}
	marked := map[string]MarkedChannel{}
	for _, mark := range marks {
		marked[mark.ChannelID] = mark
	}
	view := ChannelsView{Channels: []AdminChannel{}}
	seen := map[string]bool{}
	cursor := ""
	for range channelPages {
		listed, next, err := s.api.Conversations(ctx, token, "private_channel", cursor, channelPageSize)
		if err != nil {
			return ChannelsView{}, s.slackFailed(ctx, install, err)
		}
		for _, channel := range listed {
			seen[channel.ID] = true
			admin := AdminChannel{
				ChannelID: channel.ID, Name: channel.Name, IsPrivate: channel.Private, IsShared: channel.Shared,
				Markable: channel.Private && !channel.Shared && !channel.MPIM && !channel.IM && !channel.Archived,
			}
			if mark, ok := marked[channel.ID]; ok {
				at := mark.MarkedAt
				admin.HighPriority, admin.MarkedBy, admin.MarkedAt = true, mark.MarkedBy, &at
			}
			view.Channels = append(view.Channels, admin)
		}
		if next == "" {
			break
		}
		cursor = next
	}
	for _, mark := range marks {
		if !seen[mark.ChannelID] {
			at := mark.MarkedAt
			view.Channels = append(view.Channels, AdminChannel{
				ChannelID: mark.ChannelID, Name: mark.Name, HighPriority: true, MarkedBy: mark.MarkedBy, MarkedAt: &at,
			})
		}
	}
	slices.SortFunc(view.Channels, func(a, b AdminChannel) int {
		if c := strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name)); c != 0 {
			return c
		}
		return strings.Compare(a.ChannelID, b.ChannelID)
	})
	return view, nil
}

// SetHighPriority makes exactly the channels named the organization's high
// priority ones. Each must be a private channel that is shared with nobody,
// checked with Slack now; one that is not, or that the bot cannot see, refuses
// the whole change. A channel marked before and named again keeps who marked
// it and when. Only an admin does it.
//
// A mark is only half of what a bypass needs: whoever uses one looks at the
// channel again each time, so a channel that is shared afterwards stops
// counting the moment it is.
func (s *Service) SetHighPriority(ctx context.Context, credentials authclient.Credentials, organizationID string, channelIDs []string) (ChannelsView, error) {
	caller, err := s.orgAdmin(ctx, credentials, organizationID)
	if err != nil {
		return ChannelsView{}, err
	}
	install, err := s.activeInstall(ctx, organizationID)
	if err != nil {
		return ChannelsView{}, err
	}
	var ids []string
	for _, id := range channelIDs {
		switch {
		case !channelIDPattern.MatchString(id):
			return ChannelsView{}, fmt.Errorf("%q is not a Slack channel id: %w", truncate(id, 32), domain.ErrInvalid)
		case !slices.Contains(ids, id):
			ids = append(ids, id)
		}
	}
	if len(ids) > maxMarked {
		return ChannelsView{}, fmt.Errorf("at most %d channels can be marked: %w", maxMarked, domain.ErrInvalid)
	}
	token, err := s.botToken(ctx, install)
	if err != nil {
		return ChannelsView{}, err
	}
	now := s.now().UTC()
	channels := make([]MarkedChannel, 0, len(ids))
	for _, id := range ids {
		conversation, err := s.api.Conversation(ctx, token, id)
		var refused *Error
		switch {
		case errors.As(err, &refused) && (refused.Code == "channel_not_found" || refused.Code == "missing_scope"):
			return ChannelsView{}, fmt.Errorf("the bot cannot see %s: invite it to the channel first: %w", id, domain.ErrInvalid)
		case err != nil:
			return ChannelsView{}, s.slackFailed(ctx, install, err)
		case !conversation.Private || conversation.Shared || conversation.MPIM || conversation.IM || conversation.Archived:
			return ChannelsView{}, fmt.Errorf("%s is not a private channel shared with nobody, and only those can be marked high priority: %w", id, domain.ErrInvalid)
		}
		channels = append(channels, MarkedChannel{ChannelID: id, Name: conversation.Name, MarkedBy: caller.UserID, MarkedAt: now})
	}
	if err := s.store.ReplaceHighPriority(ctx, install.OrganizationID, install.TeamID, caller.UserID, channels, now); err != nil {
		return ChannelsView{}, err
	}
	s.logger.InfoContext(ctx, "slack high priority channels set", "organization_id", install.OrganizationID,
		"member_id", caller.UserID, "channels", len(channels))
	return s.channelsView(ctx, install)
}
