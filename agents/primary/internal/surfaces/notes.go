package surfaces

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/threads"
)

// primaryAgentKind is the kind of the agent an ask is filed as: the asker's
// primary agent.
const primaryAgentKind = "primary"

// noteStates are the states of the request that have a line in the asker's agent
// chat, in the order they come: it is asked, and then it ends one way. A request
// another owner's answer settled has no ending of its own; that owner's request
// has it.
func (p *pass) noteStates() []string {
	states := []string{threads.NoteAsked}
	switch p.outcomeKind() {
	case outcomeHeld:
		states = append(states, threads.NoteHeld)
	case outcomeAnswered:
		states = append(states, threads.NoteAnswered)
	case outcomeDenied:
		states = append(states, threads.NoteDeclined)
	case outcomeClosed:
		states = append(states, threads.NoteClosed)
	}
	return states
}

// note writes the asker's lines in their own agent chat: "Asked Raya's agent in
// #launch", and how it went. It calls no model, and is what tells the asker in
// Oryxa what they were told in Slack, since their agent is not woken to say it.
func (p *pass) note(ctx context.Context) error {
	notes := p.service.deps.Notes
	if notes == nil {
		return nil
	}
	member := threads.Member{
		UserID: p.request.Requester.UserID, OrganizationID: p.request.OrganizationID,
		AgentID: p.request.Requester.AgentID, AgentName: p.request.Requester.AgentName, AgentKind: primaryAgentKind,
	}
	for _, state := range p.noteStates() {
		role := roleNotePrefix + state
		link, said, err := p.said(ctx, role)
		if err != nil {
			return err
		}
		if said && link.State != "" {
			continue
		}
		text, err := p.noteText(ctx, state)
		if err != nil {
			return err
		}
		err = notes.Note(ctx, member, threads.Note{RequestID: p.request.ID, State: state, Text: text})
		switch {
		case err == nil:
		case errors.Is(err, threads.ErrInvalid):
			// What the service wrote does not fit a note, which trying again will
			// not change.
			if failure := p.giveUp(ctx, role, err); failure != nil {
				return failure
			}
			continue
		default:
			return fmt.Errorf("write the asker's note: %w", err)
		}
		if err := p.putLink(ctx, role, "", "", stateSent, nil); err != nil {
			return err
		}
	}
	return nil
}

// noteText is the line a note says, as one sentence with the state at its end:
// "Asked Raya's agent in #launch · answered". It holds nothing the asker did not
// write or read in Slack: who they asked, and where.
func (p *pass) noteText(ctx context.Context, state string) (string, error) {
	name, err := p.nameOf(ctx, p.request.Target)
	if err != nil {
		return "", err
	}
	whose := "their agent"
	if name != "" {
		whose = name + "'s agent"
	}
	line := "Asked " + whose + p.channelPhrase(ctx)
	switch state {
	case threads.NoteAnswered:
		line += " · answered"
	case threads.NoteHeld:
		line += " · held"
	case threads.NoteDeclined:
		line += " · declined"
	case threads.NoteClosed:
		line += " · no answer"
	}
	return line, nil
}

// channelPhrase says where the request was asked: " in #launch" when the
// channel's name is known, and " in Slack" when it is not, for a conversation
// that has none or one that could not be read. The name is decoration, and
// never a reason to fail.
func (p *pass) channelPhrase(ctx context.Context) string {
	channel, err := p.channelDetails(ctx)
	if err != nil {
		return " in Slack"
	}
	if name := strings.TrimSpace(strings.TrimPrefix(channel.Name, "#")); name != "" && !channel.IM && !channel.MPIM {
		return " in #" + name
	}
	return " in Slack"
}
