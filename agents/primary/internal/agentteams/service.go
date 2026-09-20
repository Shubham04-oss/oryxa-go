package agentteams

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"
)

const (
	maxNameRunes        = 80
	maxDescriptionRunes = 1000
	maxRoleRunes        = 300
	maxMembers          = 16
)

type Repository interface {
	Create(context.Context, Scope, Team) (Team, error)
	List(context.Context, Scope) ([]Team, error)
	Get(context.Context, Scope, string) (Team, error)
	Update(context.Context, Scope, string, UpdateInput) (Team, error)
	Delete(context.Context, Scope, string) error
}

type Service struct{ repository Repository }

func New(repository Repository) (*Service, error) {
	if repository == nil {
		return nil, errors.New("agent teams: a repository is required")
	}
	return &Service{repository: repository}, nil
}

func (service *Service) Create(ctx context.Context, scope Scope, input CreateInput) (Team, error) {
	input.Name = cleanLine(input.Name)
	input.Description = strings.TrimSpace(input.Description)
	input.Members = cleanMembers(input.Members)
	if err := validate(input.Name, input.Description, input.Members); err != nil {
		return Team{}, err
	}
	id, err := newID()
	if err != nil {
		return Team{}, err
	}
	return service.repository.Create(ctx, scope, Team{
		ID: id, Name: input.Name, Description: input.Description,
		Status: StatusActive, Revision: 1, Members: memberStubs(input.Members),
	})
}

func (service *Service) List(ctx context.Context, scope Scope) ([]Team, error) {
	return service.repository.List(ctx, scope)
}

func (service *Service) Get(ctx context.Context, scope Scope, id string) (Team, error) {
	if strings.TrimSpace(id) == "" {
		return Team{}, ErrNotFound
	}
	return service.repository.Get(ctx, scope, id)
}

func (service *Service) Update(ctx context.Context, scope Scope, id string, input UpdateInput) (Team, error) {
	if input.Name == nil && input.Description == nil && input.Status == nil && input.Members == nil {
		return Team{}, fmt.Errorf("%w: send at least one field to change", ErrInvalid)
	}
	current, err := service.Get(ctx, scope, id)
	if err != nil {
		return Team{}, err
	}
	if input.Name != nil {
		value := cleanLine(*input.Name)
		input.Name = &value
		current.Name = value
	}
	if input.Description != nil {
		value := strings.TrimSpace(*input.Description)
		input.Description = &value
		current.Description = value
	}
	if input.Status != nil {
		if *input.Status != StatusActive && *input.Status != StatusDisabled {
			return Team{}, fmt.Errorf("%w: status must be active or disabled", ErrInvalid)
		}
		current.Status = *input.Status
	}
	if input.Members != nil {
		members := cleanMembers(*input.Members)
		input.Members = &members
		current.Members = memberStubs(members)
	}
	members := make([]MemberInput, 0, len(current.Members))
	for _, member := range current.Members {
		members = append(members, MemberInput{AgentID: member.AgentID, Role: member.Role})
	}
	if err := validate(current.Name, current.Description, members); err != nil {
		return Team{}, err
	}
	return service.repository.Update(ctx, scope, id, input)
}

func (service *Service) Delete(ctx context.Context, scope Scope, id string) error {
	return service.repository.Delete(ctx, scope, strings.TrimSpace(id))
}

func cleanMembers(members []MemberInput) []MemberInput {
	result := make([]MemberInput, 0, len(members))
	for _, member := range members {
		result = append(result, MemberInput{AgentID: strings.TrimSpace(member.AgentID), Role: cleanLine(member.Role)})
	}
	return result
}

func memberStubs(inputs []MemberInput) []Member {
	result := make([]Member, 0, len(inputs))
	for position, input := range inputs {
		result = append(result, Member{AgentID: input.AgentID, Role: input.Role, Position: position})
	}
	return result
}

func validate(name, description string, members []MemberInput) error {
	switch {
	case name == "":
		return fmt.Errorf("%w: name is required", ErrInvalid)
	case utf8.RuneCountInString(name) > maxNameRunes:
		return fmt.Errorf("%w: name holds at most %d characters", ErrInvalid, maxNameRunes)
	case utf8.RuneCountInString(description) > maxDescriptionRunes:
		return fmt.Errorf("%w: description holds at most %d characters", ErrInvalid, maxDescriptionRunes)
	case len(members) == 0:
		return fmt.Errorf("%w: a team needs at least one specialist", ErrInvalid)
	case len(members) > maxMembers:
		return fmt.Errorf("%w: a team holds at most %d specialists", ErrInvalid, maxMembers)
	}
	seen := map[string]bool{}
	for _, member := range members {
		switch {
		case member.AgentID == "":
			return fmt.Errorf("%w: every team member needs an agent id", ErrInvalid)
		case seen[member.AgentID]:
			return fmt.Errorf("%w: agent %q is listed twice", ErrInvalid, member.AgentID)
		case member.Role == "":
			return fmt.Errorf("%w: every team member needs a role", ErrInvalid)
		case utf8.RuneCountInString(member.Role) > maxRoleRunes:
			return fmt.Errorf("%w: a team role holds at most %d characters", ErrInvalid, maxRoleRunes)
		}
		seen[member.AgentID] = true
	}
	return nil
}

func cleanLine(value string) string { return strings.Join(strings.Fields(value), " ") }

func newID() (string, error) {
	bytes := make([]byte, 16)
	if _, err := rand.Read(bytes); err != nil {
		return "", fmt.Errorf("create agent team id: %w", err)
	}
	return "atm_" + base64.RawURLEncoding.EncodeToString(bytes), nil
}
