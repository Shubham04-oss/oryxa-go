package slackbot

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/Shubham04-oss/oryxa-go/apps/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/internal/slackfake"
)

func (h *harness) channelsOf(view ChannelsView) map[string]AdminChannel {
	byID := map[string]AdminChannel{}
	for _, channel := range view.Channels {
		byID[channel.ChannelID] = channel
	}
	return byID
}

func TestAdminsSeeThePrivateChannelsTheBotIsInAndWhichCanBeMarked(t *testing.T) {
	h := newHarness(t)
	h.installFor(testOrg)
	h.fake.AddChannel(slackfake.Channel{ID: "G0ALPHA", Name: "alpha", Private: true})
	h.fake.AddChannel(slackfake.Channel{ID: "G0BRAVO", Name: "bravo", Private: true, Shared: true})
	h.fake.AddChannel(slackfake.Channel{ID: "C0PUBLIC", Name: "general"})
	// A mark on a channel the bot is no longer in is still listed, to be unmarked.
	h.store.marks[testOrg+"/"+slackfake.DefaultTeamID+"/G0GONE"] = MarkedChannel{ChannelID: "G0GONE", Name: "gone", MarkedBy: "user-owner", MarkedAt: testStart}

	view, err := h.service.Channels(context.Background(), bearer("admin"), testOrg)
	if err != nil {
		t.Fatal(err)
	}
	channels := h.channelsOf(view)
	if len(channels) != 3 || channels["C0PUBLIC"].ChannelID != "" {
		t.Fatalf("channels = %+v, want the private ones and the stale mark", view.Channels)
	}
	if alpha := channels["G0ALPHA"]; !alpha.Markable || alpha.HighPriority || !alpha.IsPrivate || alpha.IsShared || alpha.Name != "alpha" {
		t.Fatalf("alpha = %+v", alpha)
	}
	if bravo := channels["G0BRAVO"]; bravo.Markable || !bravo.IsShared {
		t.Fatalf("bravo = %+v", bravo)
	}
	if gone := channels["G0GONE"]; !gone.HighPriority || gone.Markable || gone.MarkedBy != "user-owner" || gone.MarkedAt == nil {
		t.Fatalf("gone = %+v", gone)
	}
	if list := h.fake.Calls("conversations.list"); len(list) != 1 || list[0].Args["types"] != "private_channel" {
		t.Fatalf("conversations.list = %+v", list)
	}
	// The listing is in name order.
	if names := []string{view.Channels[0].Name, view.Channels[1].Name, view.Channels[2].Name}; strings.Join(names, ",") != "alpha,bravo,gone" {
		t.Fatalf("order = %v", names)
	}
}

func TestOnlyAPrivateChannelSharedWithNobodyCanBeMarkedHighPriority(t *testing.T) {
	h := newHarness(t)
	h.installFor(testOrg)
	h.fake.AddChannel(slackfake.Channel{ID: "G0ALPHA", Name: "alpha", Private: true})
	h.fake.AddChannel(slackfake.Channel{ID: "G0DELTA", Name: "delta", Private: true})
	h.fake.AddChannel(slackfake.Channel{ID: "C0PUBLIC", Name: "general"})
	h.fake.AddChannel(slackfake.Channel{ID: "G0BRAVO", Name: "bravo", Private: true, Shared: true})
	h.fake.AddChannel(slackfake.Channel{ID: "G0GROUP", MPIM: true, Private: true})
	h.fake.AddChannel(slackfake.Channel{ID: "G0OLD", Name: "old", Private: true, Archived: true})

	for name, id := range map[string]string{
		"a public channel":             "C0PUBLIC",
		"a shared channel":             "G0BRAVO",
		"a group message":              "G0GROUP",
		"an archived channel":          "G0OLD",
		"a channel the bot cannot see": "G0HIDDEN",
	} {
		_, err := h.service.SetHighPriority(context.Background(), bearer("admin"), testOrg, []string{"G0ALPHA", id})
		if !errors.Is(err, domain.ErrInvalid) {
			t.Errorf("%s = %v, want ErrInvalid", name, err)
		}
	}
	if marked, _ := h.store.HighPriorityChannels(context.Background(), testOrg, slackfake.DefaultTeamID); len(marked) != 0 {
		t.Fatalf("a refused change marked %+v: it is all or nothing", marked)
	}

	view, err := h.service.SetHighPriority(context.Background(), bearer("admin"), testOrg, []string{"G0ALPHA", "G0DELTA", "G0ALPHA"})
	if err != nil {
		t.Fatal(err)
	}
	channels := h.channelsOf(view)
	if !channels["G0ALPHA"].HighPriority || !channels["G0DELTA"].HighPriority || channels["G0ALPHA"].MarkedBy != "user-admin" || channels["G0ALPHA"].MarkedAt == nil {
		t.Fatalf("channels = %+v", view.Channels)
	}
	// What is marked is what Channel says is marked.
	if channel, err := h.service.Channel(context.Background(), testOrg, "G0ALPHA"); err != nil || !channel.HighPriority {
		t.Fatalf("channel = %+v, %v", channel, err)
	}

	// Setting the set again by another admin keeps who marked what, and
	// unmarking is leaving a channel out.
	h.clock.Advance(time.Hour)
	view, err = h.service.SetHighPriority(context.Background(), bearer("owner"), testOrg, []string{"G0ALPHA"})
	if err != nil {
		t.Fatal(err)
	}
	channels = h.channelsOf(view)
	if !channels["G0ALPHA"].HighPriority || channels["G0ALPHA"].MarkedBy != "user-admin" || channels["G0DELTA"].HighPriority {
		t.Fatalf("channels = %+v", view.Channels)
	}
	view, err = h.service.SetHighPriority(context.Background(), bearer("admin"), testOrg, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, channel := range view.Channels {
		if channel.HighPriority {
			t.Fatalf("%s is still marked", channel.ChannelID)
		}
	}
}

func TestMarkingChannelsIsForAdminsWithAnInstallAndSaneIds(t *testing.T) {
	h := newHarness(t)
	if _, err := h.service.SetHighPriority(context.Background(), bearer("admin"), testOrg, []string{"G0ALPHA"}); !errors.Is(err, domain.ErrNotConnected) {
		t.Fatalf("with no install = %v", err)
	}
	h.installFor(testOrg)
	h.fake.AddChannel(slackfake.Channel{ID: "G0ALPHA", Name: "alpha", Private: true})
	for name, who := range map[string]string{"a member": "member", "an agent": "agent"} {
		if _, err := h.service.SetHighPriority(context.Background(), bearer(who), testOrg, []string{"G0ALPHA"}); !errors.Is(err, domain.ErrForbidden) {
			t.Errorf("%s = %v, want forbidden", name, err)
		}
	}
	if marked, _ := h.store.HighPriorityChannels(context.Background(), testOrg, slackfake.DefaultTeamID); len(marked) != 0 {
		t.Fatal("someone who may not, marked a channel")
	}
	if _, err := h.service.SetHighPriority(context.Background(), bearer("admin"), testOrg, []string{"general"}); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("a channel that is no id = %v", err)
	}
	many := make([]string, 0, maxMarked+1)
	for i := range maxMarked + 1 {
		many = append(many, fmt.Sprintf("G0CHAN%03d", i))
	}
	if _, err := h.service.SetHighPriority(context.Background(), bearer("admin"), testOrg, many); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("too many channels = %v", err)
	}
	h.assertLogsLack(slackfake.DefaultBotToken)
}
