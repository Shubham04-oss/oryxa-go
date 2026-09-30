package slackbot

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Shubham04-oss/oryxa-go/apps/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/internal/slackfake"
)

// installWithRefusalReactions grants the optional reaction scope explicitly;
// the production manifest and ordinary fake installs do not request it yet.
func (h *harness) installWithRefusalReactions() Install {
	h.t.Helper()
	install := h.installFor(testOrg)
	install.Scopes = append(slices.Clone(install.Scopes), "reactions:write")
	h.store.mu.Lock()
	h.store.installs[install.TeamID] = install
	h.store.mu.Unlock()
	workspace := h.fake.Workspace(install.TeamID)
	workspace.Scopes = append(slices.Clone(workspace.Scopes), "reactions:write")
	h.fake.AddWorkspace(workspace)
	return install
}

func refusalRequest(inbound Inbound, key, params string) PostRequest {
	return PostRequest{
		OrganizationID: testOrg, IdempotencyKey: key, RefusalInboundID: inbound.ID,
		Target:   Target{ChannelID: inbound.ChannelID, ThreadTS: inbound.ThreadTS, UserID: inbound.UserID},
		Template: TemplateRefusal, Params: json.RawMessage(params),
	}
}

func TestEveryRefusalIsPrivateWithANeutralBotReactionOnTheExactAsk(t *testing.T) {
	for _, test := range []struct{ reason, hint, line string }{
		{RefusalNotMember, "", "Only members of this organization in Oryxa can ask me."},
		{RefusalOwnerNotMember, "", "Raya isn't in Oryxa, so I didn't ask them."},
		{RefusalGroupDM, "", "I don't work in group messages. Ask me in a channel."},
		{RefusalExternal, "", "I don't answer in channels shared with other organizations."},
		{RefusalNoOwner, "", "Mention the person by picking them from the list."},
		{RefusalNoOwner, "self", "You named yourself. Pick someone else from the mention list so I can ask them."},
		{RefusalTooManyAsks, "", "You have too many open questions. Wait for some to be answered."},
		{RefusalTooManyAsks, "too_many_people", "Ask up to five people at a time. Pick fewer people from the mention list."},
		{RefusalScreenFailed, "", "I couldn't check that just now, so I didn't ask anyone. Try again in a moment."},
		{RefusalNotWork, "", "I only pass on work questions."},
		{RefusalInjection, "", "I couldn't pass that request on safely, so I didn't ask anyone."},
		{RefusalImplausible, "", "This request doesn't look appropriate for your role or team, so I didn't ask anyone."},
		{RefusalNoGrant, "", "Give Oryxa's agent access to act for you in Settings, then ask again."},
		{RefusalOwnerDepartment, "", "I didn't ask Raya: this looks like a question for another team."},
	} {
		t.Run(test.reason+test.hint, func(t *testing.T) {
			h := newHarness(t)
			h.installWithRefusalReactions()
			inbound := h.seenThread(slackfake.Mention{Text: "<@" + slackfake.DefaultBotUserID + "> ask @Raya about private-topic", TS: "1780000700.000200", ThreadTS: "1780000000.000100"})
			params, _ := json.Marshal(refusalParams{Reason: test.reason, Hint: test.hint, Name: "Raya"})
			request := refusalRequest(inbound, "key-refusal", string(params))
			for attempt := range 2 {
				result, err := h.service.Post(context.Background(), request)
				if err != nil || !result.Ephemeral || result.Replayed != (attempt > 0) {
					t.Fatalf("attempt %d=%+v err=%v", attempt, result, err)
				}
			}
			messages, reactions := h.fake.Messages(), h.fake.Reactions()
			if len(messages) != 1 || !messages[0].Ephemeral || messages[0].User != inbound.UserID || messages[0].ThreadTS != inbound.ThreadTS || messages[0].Text != test.line {
				t.Fatalf("private messages=%+v", messages)
			}
			if want := (slackfake.Reaction{TeamID: inbound.TeamID, Channel: inbound.ChannelID, TS: inbound.MessageTS, UserID: slackfake.DefaultBotUserID, Name: refusalReaction}); len(reactions) != 1 || reactions[0] != want {
				t.Fatalf("reactions=%+v, want bot on actual reply %+v", reactions, want)
			}
			for _, call := range h.fake.Calls("reactions.add") {
				if len(call.Args) != 3 || call.Args["name"] != refusalReaction || call.Token != slackfake.DefaultBotToken {
					t.Fatalf("reaction exposed more than neutral signal: %+v", call)
				}
			}
			if len(h.fake.Calls("chat.postMessage")) != 0 || len(h.fake.Calls("chat.postEphemeral")) != 1 {
				t.Fatal("the reason was public or duplicated")
			}
			h.assertLogsLack("private-topic", test.line, slackfake.DefaultBotToken)
		})
	}
}

func TestRefusalReactionRetryKeepsThePrivateExplanationOnce(t *testing.T) {
	h := newHarness(t)
	h.installWithRefusalReactions()
	inbound := h.seenThread(slackfake.Mention{Text: askText})
	request := refusalRequest(inbound, "key-retry-refusal", `{"reason":"no_owner"}`)
	h.fake.RateLimit("reactions.add", 3*time.Second, 1)
	first, err := h.service.Post(context.Background(), request)
	var limited *RateLimitedError
	if !errors.As(err, &limited) || limited.RetryAfter != 3*time.Second || !first.Ephemeral || len(h.fake.Messages()) != 1 || len(h.fake.Reactions()) != 0 {
		t.Fatalf("first=%+v err=%v messages=%v reactions=%v", first, err, h.fake.Messages(), h.fake.Reactions())
	}
	second, err := h.service.Post(context.Background(), request)
	if err != nil || !second.Replayed || first.TS != second.TS || len(h.fake.Messages()) != 1 || len(h.fake.Reactions()) != 1 {
		t.Fatalf("retry=%+v err=%v messages=%v reactions=%v", second, err, h.fake.Messages(), h.fake.Reactions())
	}
}

func TestARecordedRefusalCannotMoveItsReactionToAnotherAsk(t *testing.T) {
	h := newHarness(t)
	h.installWithRefusalReactions()
	first := h.seenThread(slackfake.Mention{Text: askText, TS: "1780000000.000100"})
	request := refusalRequest(first, "key-fixed-refusal", `{"reason":"no_owner"}`)
	if _, err := h.service.Post(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	h.clock.Advance(time.Second)
	second := h.seenThread(slackfake.Mention{Text: askText, TS: "1780000000.000200", ThreadTS: first.ThreadTS})
	request.RefusalInboundID = second.ID
	if _, err := h.service.Post(context.Background(), request); !errors.Is(err, domain.ErrConflict) || len(h.fake.Reactions()) != 1 || len(h.fake.Messages()) != 1 {
		t.Fatalf("redirecting recorded reaction err=%v reactions=%v messages=%v", err, h.fake.Reactions(), h.fake.Messages())
	}
}

func TestAReactionWhoseBotTokenEndedKeepsThePrivateRefusalAndEndsTheInstall(t *testing.T) {
	h := newHarness(t)
	h.installWithRefusalReactions()
	inbound := h.seenThread(slackfake.Mention{Text: askText})
	h.fake.Fail("reactions.add", "token_revoked", 1)
	if _, err := h.service.Post(context.Background(), refusalRequest(inbound, "key-token-refusal", `{"reason":"no_owner"}`)); !errors.Is(err, domain.ErrNotConnected) || len(h.fake.Messages()) != 1 || h.store.install(slackfake.DefaultTeamID).Active() {
		t.Fatalf("ended token err=%v messages=%v install=%+v", err, h.fake.Messages(), h.store.install(slackfake.DefaultTeamID))
	}
}

func TestRefusalVisibilityFailureStillExplainsPrivately(t *testing.T) {
	for _, code := range []string{"missing_scope", "message_not_found", "no_permission", "thread_locked"} {
		t.Run(code, func(t *testing.T) {
			h := newHarness(t)
			h.installWithRefusalReactions()
			inbound := h.seenThread(slackfake.Mention{Text: askText})
			h.fake.Fail("reactions.add", code, 1)
			if result, err := h.service.Post(context.Background(), refusalRequest(inbound, "key-visibility", `{"reason":"no_owner"}`)); err != nil || !result.Ephemeral || len(h.fake.Messages()) != 1 {
				t.Fatalf("visibility=%+v err=%v messages=%v", result, err, h.fake.Messages())
			}
			if !strings.Contains(h.logs.String(), "could not show a refusal reaction") || !h.store.install(slackfake.DefaultTeamID).Active() {
				t.Fatal("the permanent visibility failure was hidden or ended the install")
			}
		})
	}
	t.Run("old installation", func(t *testing.T) {
		h := newHarness(t)
		install := h.installWithRefusalReactions()
		install.Scopes = slices.DeleteFunc(slices.Clone(install.Scopes), func(scope string) bool { return scope == "reactions:write" })
		h.store.installs[install.TeamID] = install
		inbound := h.seenThread(slackfake.Mention{Text: askText})
		if _, err := h.service.Post(context.Background(), refusalRequest(inbound, "key-old-install", `{"reason":"no_owner"}`)); err != nil || len(h.fake.Messages()) != 1 || len(h.fake.Calls("reactions.add")) != 0 || !strings.Contains(h.logs.String(), "updated installation scope") {
			t.Fatalf("old installation err=%v messages=%v logs=%s", err, h.fake.Messages(), h.logs)
		}
	})
}

func TestRefusalReactionCannotBeRedirectedToAnotherVerifiedMessage(t *testing.T) {
	for name, edit := range map[string]func(*Inbound){
		"organization": func(in *Inbound) { in.OrganizationID = "org_elsewhere" },
		"workspace":    func(in *Inbound) { in.TeamID = "T0OTHER" },
		"sender":       func(in *Inbound) { in.UserID = "U0OTHER" },
		"channel":      func(in *Inbound) { in.ChannelID = "C0OTHER" },
		"root":         func(in *Inbound) { in.ThreadTS = "1780000000.000001" },
		"bot":          func(in *Inbound) { in.UserID = slackfake.DefaultBotUserID },
		"click":        func(in *Inbound) { in.Kind = InboundAction },
		"expired":      func(in *Inbound) { in.ReceivedAt = testStart.Add(-InboundRetention - time.Second) },
	} {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t)
			h.installWithRefusalReactions()
			inbound := h.seenThread(slackfake.Mention{Text: askText, TS: "1780000700.000200", ThreadTS: "1780000000.000100"})
			request := refusalRequest(inbound, "key-bound-refusal", `{"reason":"no_owner"}`)
			copied := inbound
			copied.ID = "asi_redirected"
			edit(&copied)
			h.store.inbound[copied.ID] = copied
			request.RefusalInboundID = copied.ID
			if _, err := h.service.Post(context.Background(), request); !errors.Is(err, domain.ErrForbidden) || len(h.fake.Calls()) != 0 {
				t.Fatalf("redirect err=%v calls=%v", err, h.fake.Calls())
			}
		})
	}
}

func TestRefusalHintsAreBoundedAndLegacyPostHashesStayStable(t *testing.T) {
	h := newHarness(t)
	for _, params := range []string{`{"reason":"no_owner","hint":"arbitrary text"}`, `{"reason":"injection","hint":"self"}`} {
		if _, _, err := h.service.render(TemplateRefusal, json.RawMessage(params)); !errors.Is(err, domain.ErrInvalid) {
			t.Fatalf("hint=%s err=%v", params, err)
		}
	}
	request := PostRequest{Template: TemplateRefusal, Target: Target{ChannelID: "C0GENERAL", UserID: "U0ASKER"}, Params: json.RawMessage(`{"reason":"no_owner"}`)}
	want := "42170d95b3b0e451da21e92d4104eae1151fe9e40314396ceabed7b70066a9ce"
	if hash := requestHash(request, "1780000000.000100"); hash != want {
		t.Fatalf("legacy hash=%s, want %s", hash, want)
	}
	request.RefusalInboundID = "asi_1"
	first := requestHash(request, "1780000000.000100")
	request.RefusalInboundID = "asi_2"
	if requestHash(request, "1780000000.000100") == first {
		t.Fatal("reaction identity was omitted from the post claim")
	}
}
