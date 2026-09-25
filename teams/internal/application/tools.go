package application

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"time"

	teamseventsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/teams/v1"

	"github.com/Shubham04-oss/oryxa-go/teams/internal/authclient"
	"github.com/Shubham04-oss/oryxa-go/teams/internal/domain"
)

type teamQueryInput struct {
	Query string `json:"query"`
}

// Tools is the three options: list_teams, call_team, call_team_members.
// The caller must belong to the organization; which teams those tools can
// open is still visibility — organization admins see every team, others
// only teams they belong to.
func (s *Service) Tools(ctx context.Context, credentials authclient.Credentials, organizationID string) ([]domain.Tool, error) {
	if _, _, err := s.visibleTeams(ctx, credentials, organizationID); err != nil {
		return nil, err
	}
	return domain.ToolCatalog(), nil
}

// CallTool runs list_teams, call_team, or call_team_members.
//
// Every one of the three writes a MemberPinged event for each member the
// call touched, and each of those can reach a member as email — so each is
// budgeted per caller before anything is written rather than merely recorded
// after the fact.
func (s *Service) CallTool(ctx context.Context, credentials authclient.Credentials, organizationID, name string, arguments json.RawMessage) (any, error) {
	teams, org, err := s.visibleTeams(ctx, credentials, organizationID)
	if err != nil {
		return nil, err
	}
	if err := s.allowToolCall(organizationID, org.UserID); err != nil {
		return nil, err
	}
	switch name {
	case domain.ToolListTeams:
		if err := rejectArguments(arguments); err != nil {
			return nil, err
		}
		if err := s.recordListed(ctx, org, teams); err != nil {
			return nil, err
		}
		return domain.TeamHandles(teams), nil
	case domain.ToolCallTeam:
		team, err := s.resolveVisible(teams, arguments)
		if err != nil {
			return nil, err
		}
		members, err := s.store.Members(ctx, team.ID)
		if err != nil {
			return nil, err
		}
		if err := s.recordTeamCall(ctx, org, team, members, teamseventsv1.TeamCalledDetailType, teamseventsv1.ToolCallTeam); err != nil {
			return nil, err
		}
		return domain.TeamAsHandle(team), nil
	case domain.ToolCallTeamMembers:
		team, err := s.resolveVisible(teams, arguments)
		if err != nil {
			return nil, err
		}
		members, err := s.store.Members(ctx, team.ID)
		if err != nil {
			return nil, err
		}
		if err := s.recordTeamCall(ctx, org, team, members, teamseventsv1.TeamMembersCalledDetailType, teamseventsv1.ToolCallTeamMembers); err != nil {
			return nil, err
		}
		return domain.MembersOf(team, members), nil
	default:
		return nil, domain.ErrNotFound
	}
}

func (s *Service) recordTeamCall(ctx context.Context, org domain.OrgMembership, team domain.Team, members []domain.Membership, detailType, tool string) error {
	now := s.now().UTC()
	events := make([]domain.Event, 0, 1+len(members))
	called, err := s.event(detailType, teamseventsv1.TeamCalled{
		TeamID:           team.ID,
		TeamName:         team.Name,
		OrganizationID:   team.OrganizationID,
		OrganizationName: org.OrganizationName,
		Tool:             tool,
		CalledBy:         org.UserID,
		CalledAt:         now,
		MemberCount:      len(members),
	}, now)
	if err != nil {
		return err
	}
	events = append(events, called)
	pings, err := s.memberPings(org, team, members, tool, now)
	if err != nil {
		return err
	}
	events = append(events, pings...)
	return s.store.RecordEvents(ctx, events)
}

func (s *Service) recordListed(ctx context.Context, org domain.OrgMembership, teams []domain.Team) error {
	now := s.now().UTC()
	listedTeams := make([]teamseventsv1.ListedTeam, 0, len(teams))
	handles := domain.TeamHandles(teams)
	for _, handle := range handles {
		listedTeams = append(listedTeams, teamseventsv1.ListedTeam{TeamID: handle.ID, Name: handle.Name})
	}
	listed, err := s.event(teamseventsv1.TeamsListedDetailType, teamseventsv1.TeamsListed{
		OrganizationID:   org.OrganizationID,
		OrganizationName: org.OrganizationName,
		Teams:            listedTeams,
		CalledBy:         org.UserID,
		CalledAt:         now,
	}, now)
	if err != nil {
		return err
	}
	events := []domain.Event{listed}
	for _, team := range teams {
		members, err := s.store.Members(ctx, team.ID)
		if err != nil {
			return err
		}
		pings, err := s.memberPings(org, team, members, teamseventsv1.ToolListTeams, now)
		if err != nil {
			return err
		}
		events = append(events, pings...)
	}
	return s.store.RecordEvents(ctx, events)
}

func (s *Service) memberPings(org domain.OrgMembership, team domain.Team, members []domain.Membership, tool string, at time.Time) ([]domain.Event, error) {
	events := make([]domain.Event, 0, len(members))
	for _, member := range members {
		event, err := s.event(teamseventsv1.MemberPingedDetailType, teamseventsv1.MemberPinged{
			TeamID:           team.ID,
			TeamName:         team.Name,
			OrganizationID:   team.OrganizationID,
			OrganizationName: org.OrganizationName,
			UserID:           member.UserID,
			Email:            member.Email,
			Role:             string(member.Role),
			Tool:             tool,
			NotifyEmail:      member.NotifyEmail,
			CalledBy:         org.UserID,
			CalledAt:         at,
		}, at)
		if err != nil {
			return nil, err
		}
		events = append(events, event)
	}
	return events, nil
}

func (s *Service) resolveVisible(teams []domain.Team, arguments json.RawMessage) (domain.Team, error) {
	var input teamQueryInput
	if err := decodeToolArguments(arguments, &input); err != nil {
		return domain.Team{}, err
	}
	return domain.ResolveTeam(teams, input.Query)
}

func (s *Service) visibleTeams(ctx context.Context, credentials authclient.Credentials, organizationID string) ([]domain.Team, domain.OrgMembership, error) {
	org, err := s.caller(ctx, credentials, organizationID)
	if err != nil {
		return nil, domain.OrgMembership{}, err
	}
	var teams []domain.Team
	if org.ActsAsAdmin() {
		teams, err = s.store.Teams(ctx, organizationID)
	} else {
		teams, err = s.store.TeamsForUser(ctx, organizationID, org.UserID)
	}
	if err != nil {
		return nil, domain.OrgMembership{}, err
	}
	return teams, org, nil
}

func decodeToolArguments(arguments json.RawMessage, target any) error {
	if len(arguments) == 0 {
		return fmt.Errorf("tool arguments are required: %w", domain.ErrInvalid)
	}
	decoder := json.NewDecoder(bytes.NewReader(arguments))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("invalid tool arguments: %w", domain.ErrInvalid)
	}
	return nil
}

func rejectArguments(arguments json.RawMessage) error {
	if len(arguments) == 0 {
		return nil
	}
	var empty struct{}
	decoder := json.NewDecoder(bytes.NewReader(arguments))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&empty); err != nil {
		return fmt.Errorf("this tool takes no arguments: %w", domain.ErrInvalid)
	}
	return nil
}
