package slackbot

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Shubham04-oss/oryxa-go/apps/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/internal/slackfake"
)

func TestRouterRefusalsKeepCompletedAndInFlightLegacyClaimsAcrossUpgrade(t *testing.T) {
	for _, test := range []struct{ name, oldParams, newParams, person string }{
		{"self", `{"reason":"no_owner"}`, `{"reason":"no_owner","hint":"self"}`, ""},
		{"owner count", `{"reason":"too_many_asks"}`, `{"reason":"too_many_asks","hint":"too_many_people"}`, ""},
		{"organization name", `{"reason":"not_member","organization_name":"Acme \u0026 Co"}`, `{"reason":"not_member","organization_name":"Acme \u0026 Co"}`, ""},
		{"omitted owner", `{"reason":"owner_not_member","user_id":"U0RAYA"}`, `{"reason":"owner_not_member","user_id":"U0RAYA"}`, "U0RAYA"},
		{"owner department", `{"reason":"owner_department","user_id":"U0RAYA"}`, `{"reason":"owner_department","user_id":"U0RAYA"}`, "U0RAYA"},
	} {
		for _, completed := range []bool{false, true} {
			t.Run(test.name+map[bool]string{true: "/completed", false: "/inflight"}[completed], func(t *testing.T) {
				h := newHarness(t)
				h.installWithRefusalReactions()
				inbound := h.seenThread(slackfake.Mention{Text: askText, TS: "1780000700.000200", ThreadTS: "1780000000.000100"})
				key := "slackrouter:" + inbound.ID + ":refusal"
				if test.person != "" {
					key += ":" + test.person
				}
				old := refusalRequest(inbound, key, test.oldParams)
				old.RefusalInboundID = ""
				var oldResult PostResult
				if completed {
					var err error
					oldResult, err = h.service.Post(context.Background(), old)
					if err != nil {
						t.Fatal(err)
					}
				} else {
					_, mine, err := h.store.ClaimPost(context.Background(), Post{
						OrganizationID: testOrg, TeamID: inbound.TeamID, Key: key, Kind: PostEphemeral,
						Template: TemplateRefusal, RequestHash: requestHash(old, inbound.ThreadTS), ClaimedAt: h.clock.Now(),
					}, h.clock.Now().Add(-claimLease))
					if err != nil || !mine {
						t.Fatalf("seed legacy claim mine=%v err=%v", mine, err)
					}
				}
				upgraded := refusalRequest(inbound, key, test.newParams)
				if oldHash, newHash := requestHash(old, inbound.ThreadTS), requestHash(upgraded, inbound.ThreadTS); oldHash != newHash {
					t.Fatalf("legacy=%s upgraded=%s", oldHash, newHash)
				}
				result, err := h.service.Post(context.Background(), upgraded)
				if !completed {
					if !errors.Is(err, domain.ErrConflict) || !strings.Contains(err.Error(), "being posted") || len(h.fake.Calls()) != 0 {
						t.Fatalf("inflight upgrade=%+v err=%v calls=%v", result, err, h.fake.Calls())
					}
					// The old worker's completion can now be replayed by the new
					// worker, which only adds the verified reaction.
					if err := h.store.CompletePost(context.Background(), testOrg, key, inbound.ChannelID, "1780000701.000001", h.clock.Now()); err != nil {
						t.Fatal(err)
					}
					result, err = h.service.Post(context.Background(), upgraded)
				} else if result.TS != oldResult.TS {
					t.Fatalf("upgrade moved the old private post: %+v vs %+v", result, oldResult)
				}
				if err != nil || !result.Replayed || len(h.fake.Reactions()) != 1 || len(h.fake.Messages()) != map[bool]int{true: 1, false: 0}[completed] {
					t.Fatalf("legacy upgrade=%+v err=%v messages=%v reactions=%v", result, err, h.fake.Messages(), h.fake.Reactions())
				}
				h.clock.Advance(time.Second)
				other := h.seenThread(slackfake.Mention{Text: askText, TS: "1780000700.000300", ThreadTS: inbound.ThreadTS})
				upgraded.RefusalInboundID = other.ID
				if _, err := h.service.Post(context.Background(), upgraded); !errors.Is(err, domain.ErrConflict) || len(h.fake.Reactions()) != 1 {
					t.Fatalf("legacy reaction retarget err=%v reactions=%v", err, h.fake.Reactions())
				}
			})
		}
	}
}

type uncertainCompletionStore struct {
	Store
	committed bool
	failed    bool
}

func (store *uncertainCompletionStore) CompletePost(ctx context.Context, org, key, channel, ts string, at time.Time) error {
	if store.failed {
		return store.Store.CompletePost(ctx, org, key, channel, ts, at)
	}
	store.failed = true
	if store.committed {
		if err := store.Store.CompletePost(ctx, org, key, channel, ts, at); err != nil {
			return err
		}
	}
	return errors.New("completion write did not answer")
}

func TestUncertainPrivatePostCompletionDoesNotStartAReactionRetry(t *testing.T) {
	for _, committed := range []bool{false, true} {
		t.Run(map[bool]string{true: "write committed before error", false: "write did not commit"}[committed], func(t *testing.T) {
			h := newHarness(t)
			h.installWithRefusalReactions()
			inbound := h.seenThread(slackfake.Mention{Text: askText})
			h.service.store = &uncertainCompletionStore{Store: h.store, committed: committed}
			h.fake.RateLimit("reactions.add", 7*time.Second, 1)
			request := refusalRequest(inbound, "key-uncertain-refusal", `{"reason":"no_owner"}`)
			first, err := h.service.Post(context.Background(), request)
			if err != nil || !first.Ephemeral || len(h.fake.Messages()) != 1 || len(h.fake.Calls("reactions.add")) != 0 || !strings.Contains(h.logs.String(), "record a Slack post that was made") {
				t.Fatalf("uncertain send=%+v err=%v messages=%v calls=%v", first, err, h.fake.Messages(), h.fake.Calls())
			}
			// An independent retry is safe while the original claim is held.
			// Once a completion can be read, reaction failures use that durable
			// replay; otherwise no reaction retry is created.
			h.clock.Advance(claimLease / 2)
			second, err := h.service.Post(context.Background(), request)
			if !committed {
				if !errors.Is(err, domain.ErrConflict) || len(h.fake.Calls("reactions.add")) != 0 || len(h.fake.Messages()) != 1 {
					t.Fatalf("uncertain retry=%+v err=%v calls=%v", second, err, h.fake.Calls())
				}
				return
			}
			var limited *RateLimitedError
			if !errors.As(err, &limited) || !second.Replayed || first.TS != second.TS || len(h.fake.Messages()) != 1 {
				t.Fatalf("durable replay=%+v err=%v messages=%v", second, err, h.fake.Messages())
			}
			if result, err := h.service.Post(context.Background(), request); err != nil || !result.Replayed || len(h.fake.Messages()) != 1 || len(h.fake.Reactions()) != 1 {
				t.Fatalf("reaction-only retry=%+v err=%v messages=%v reactions=%v", result, err, h.fake.Messages(), h.fake.Reactions())
			}
		})
	}
}

func TestRendererOnlyFieldsDoNotUseRouterLegacyHashCompatibility(t *testing.T) {
	request := PostRequest{Template: TemplateRefusal, RefusalInboundID: "asi_1", IdempotencyKey: "slackrouter:asi_1:refusal", Params: json.RawMessage(`{"name":"Raya","reason":"owner_not_member"}`)}
	if _, compatible := legacyRouterRefusalParams(request); compatible {
		t.Fatal("renderer-only name became a legacy router parameter")
	}
	first := requestHash(request, "1780000000.000100")
	request.RefusalInboundID = "asi_2"
	request.IdempotencyKey = "slackrouter:asi_2:refusal"
	if requestHash(request, "1780000000.000100") == first {
		t.Fatal("non-router parameter shapes lost their reaction identity")
	}
}
