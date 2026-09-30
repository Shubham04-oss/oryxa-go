package slackbot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/Shubham04-oss/oryxa-go/apps/internal/domain"
)

const refusalReaction = "speech_balloon"

// legacyRouterRefusalParams preserves the router's original claim hash across
// deployment. Its key already names the exact inbound and optional omitted
// owner; refusalMessage validates that inbound before the claim is consulted.
// Only refusal fields and new bounded hints may use this path. Other
// keys still hash RefusalInboundID, so a claim cannot acquire a new reaction
// target. Completed legacy ephemeral text stays as sent rather than repeating.
func legacyRouterRefusalParams(request PostRequest) ([]byte, bool) {
	if request.Template != TemplateRefusal || request.RefusalInboundID == "" {
		return nil, false
	}
	var p struct {
		Reason           string `json:"reason"`
		OrganizationName string `json:"organization_name,omitempty"`
		UserID           string `json:"user_id,omitempty"`
		Hint             string `json:"hint,omitempty"`
	}
	if decodeParams(request.Params, &p) != nil ||
		(p.Hint != "" && !(p.Reason == RefusalNoOwner && p.Hint == "self") && !(p.Reason == RefusalTooManyAsks && p.Hint == "too_many_people")) {
		return nil, false
	}
	key := "slackrouter:" + request.RefusalInboundID + ":refusal"
	if p.UserID != "" {
		key += ":" + p.UserID
	}
	if request.IdempotencyKey != key {
		return nil, false
	}
	// The old router serialized these three typed fields in this order;
	// renderer-only fields such as name use the ordinary new hash instead.
	legacy, _ := json.Marshal(struct {
		Reason           string `json:"reason"`
		OrganizationName string `json:"organization_name,omitempty"`
		UserID           string `json:"user_id,omitempty"`
	}{Reason: p.Reason, OrganizationName: p.OrganizationName, UserID: p.UserID})
	return legacy, true
}

// refusalMessage binds the private refusal and public neutral signal to the
// same verified message, organization, installation and human sender. A
// thread's earlier root is never substituted for the refused reply.
func (s *Service) refusalMessage(ctx context.Context, install Install, request PostRequest, root string) (*Inbound, error) {
	if request.RefusalInboundID == "" {
		return nil, nil // Older callers still get their private explanation.
	}
	if request.Template != TemplateRefusal || request.Target.UserID == "" || request.Target.ChannelID == "" ||
		!strings.HasPrefix(request.RefusalInboundID, "asi_") || len(request.RefusalInboundID) > 64 {
		return nil, fmt.Errorf("a refusal reaction must name a verified asker message: %w", domain.ErrInvalid)
	}
	inbound, err := s.store.Inbound(ctx, request.RefusalInboundID)
	if err != nil {
		return nil, err
	}
	if inbound.Kind != InboundMessage || inbound.OrganizationID != install.OrganizationID || inbound.TeamID != install.TeamID ||
		inbound.ChannelID != request.Target.ChannelID || inbound.ThreadTS != root || inbound.UserID != request.Target.UserID ||
		inbound.UserID == install.BotUserID || !tsPattern.MatchString(inbound.MessageTS) ||
		inbound.ReceivedAt.Before(s.now().UTC().Add(-InboundRetention)) {
		return nil, fmt.Errorf("the refusal does not address that verified asker message: %w", domain.ErrForbidden)
	}
	return &inbound, nil
}

// reactToRefusal runs only after the private explanation's completion write
// succeeds. An uncertain completion skips the reaction. A
// transient failure retries the reaction through a replay of that post;
// Slack's already_reacted makes races and replays harmless. Missing scope or
// a message that can no longer take reactions must not hide the explanation
// or turn a settled refusal into an endless retry.
func (s *Service) reactToRefusal(ctx context.Context, install Install, inbound *Inbound) error {
	if inbound == nil {
		return nil
	}
	if !slices.Contains(install.Scopes, "reactions:write") {
		s.logger.WarnContext(ctx, "Slack refusal reaction needs the updated installation scope", "organization_id", install.OrganizationID)
		return nil
	}
	if err := s.spend(install.OrganizationID); err != nil {
		return err
	}
	token, err := s.botToken(ctx, install)
	if err != nil {
		return err
	}
	err = s.api.AddReaction(ctx, token, inbound.ChannelID, inbound.MessageTS, refusalReaction)
	var refused *Error
	if errors.As(err, &refused) && !refused.tokenDead() {
		// These are permanent limits on visibility, not on delivering the
		// reason privately. Other Slack failures remain retryable.
		switch refused.Code {
		case "missing_scope", "message_not_found", "channel_not_found", "is_archived", "not_in_channel", "no_permission",
			"not_reactable", "thread_locked", "too_many_emoji", "too_many_reactions":
			s.logger.WarnContext(ctx, "Slack could not show a refusal reaction", "organization_id", install.OrganizationID, "code", refused.Code)
			return nil
		}
	}
	return s.slackFailed(ctx, install, err)
}
