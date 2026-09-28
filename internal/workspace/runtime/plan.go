package runtime

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/Shubham04-oss/oryxa-go/internal/workspace/spec"
)

type Change struct {
	Op   string `json:"op"`
	Kind string `json:"kind"`
	Name string `json:"name"`
	Role string `json:"role,omitempty"`
}

type HumanStep struct {
	Step string `json:"step"`
	Why  string `json:"why"`
}

type Plan struct {
	Changes    []Change    `json:"changes"`
	NeedsHuman []HumanStep `json:"needs_human"`
}

func BuildPlan(document spec.Document, state Snapshot) (Plan, error) {
	plan := Plan{Changes: []Change{}, NeedsHuman: []HumanStep{}}
	if state.Organization == nil {
		plan.Changes = append(plan.Changes, Change{Op: "create", Kind: "organization", Name: document.Metadata.Organization})
	} else if document.Organization != nil && document.Organization.Name != "" && state.Organization.Name != document.Organization.Name {
		plan.Changes = append(plan.Changes, Change{Op: "update", Kind: "organization", Name: document.Metadata.Organization})
	}
	wantedMembers := map[string]bool{}
	if document.People != nil {
		for _, member := range document.People.Members {
			wantedMembers[member.Email] = true
			role := member.Role
			if role == "" {
				role = "member"
			}
			if existing, ok := state.Members[member.Email]; ok {
				if existing.Role != role {
					if existing.Role == "owner" || role == "owner" {
						return Plan{}, fmt.Errorf("people.members role changes involving an owner are not supported")
					}
					plan.Changes = append(plan.Changes, Change{Op: "update", Kind: "member_role", Name: member.Email, Role: role})
				}
			} else if invitation, ok := state.Invitations[member.Email]; ok {
				if invitation.Role != role {
					return Plan{}, fmt.Errorf("people.members has a pending invitation with another role")
				}
				plan.NeedsHuman = append(plan.NeedsHuman, HumanStep{Step: "people.join", Why: member.Email + " must accept the invitation"})
			} else {
				if role == "owner" {
					return Plan{}, fmt.Errorf("people.members cannot invite an owner")
				}
				plan.Changes = append(plan.Changes, Change{Op: "create", Kind: "invitation", Name: member.Email, Role: role})
				plan.NeedsHuman = append(plan.NeedsHuman, HumanStep{Step: "people.join", Why: member.Email + " must accept the invitation"})
			}
		}
	}
	for _, team := range document.Teams {
		_, exists := state.Teams[team.Name]
		if !exists {
			plan.Changes = append(plan.Changes, Change{Op: "create", Kind: "team", Name: team.Name})
		}
		for _, email := range team.Members {
			if _, joined := state.Members[email]; !joined {
				if !wantedMembers[email] && state.Invitations[email].Email == "" {
					return Plan{}, fmt.Errorf("teams[].members references a person absent from the organization and people.members")
				}
				plan.NeedsHuman = append(plan.NeedsHuman, HumanStep{Step: "teams.members", Why: email + " must join before being added to " + team.Name})
				continue
			}
			if _, already := state.TeamMembers[team.Name][email]; !already {
				plan.Changes = append(plan.Changes, Change{Op: "create", Kind: "team_member", Name: team.Name + "/" + email})
			}
		}
	}
	return plan, nil
}

// Apply reconciles only settings backed by the existing services. Repeated
// applies skip previously created resources; members who have not accepted an
// invitation are reported as human steps for the next run.
func Apply(ctx context.Context, client *Client, document spec.Document) (Plan, error) {
	state, err := client.Snapshot(ctx, document.Metadata.Organization)
	if err != nil {
		return Plan{}, err
	}
	if _, err := BuildPlan(document, state); err != nil {
		return Plan{}, err
	}
	if state.Organization == nil {
		name := document.Metadata.Organization
		if document.Organization != nil && document.Organization.Name != "" {
			name = document.Organization.Name
		}
		if err := client.CreateOrganization(ctx, name, document.Metadata.Organization); err != nil {
			return Plan{}, err
		}
		state, err = settledSnapshot(ctx, client, document.Metadata.Organization)
		if err != nil {
			return Plan{}, err
		}
		if state.Organization == nil {
			return Plan{}, fmt.Errorf("organization creation has not appeared in the API yet; retry apply")
		}
	}
	if document.Organization != nil && document.Organization.Name != "" && state.Organization.Name != document.Organization.Name {
		if err := client.UpdateOrganization(ctx, state.Organization.ID, document.Organization.Name, state.Organization.Slug); err != nil {
			return Plan{}, err
		}
	}
	if document.People != nil {
		for _, member := range document.People.Members {
			role := member.Role
			if role == "" {
				role = "member"
			}
			if existing, ok := state.Members[member.Email]; ok {
				if existing.Role != role {
					if err := client.ChangeMemberRole(ctx, state.Organization.ID, existing.UserID, role); err != nil {
						return Plan{}, err
					}
				}
			} else if _, invited := state.Invitations[member.Email]; !invited {
				if err := client.Invite(ctx, state.Organization.ID, member.Email, role); err != nil {
					return Plan{}, err
				}
			}
		}
	}
	for _, team := range document.Teams {
		if _, exists := state.Teams[team.Name]; !exists {
			if err := retryNotFound(ctx, func() error { return client.CreateTeam(ctx, state.Organization.ID, team.Name) }); err != nil {
				return Plan{}, err
			}
		}
	}
	// Refresh after creating teams: their IDs and the creator's automatic team
	// memberships are server-assigned, and must be observed before adding more.
	state, err = settledSnapshot(ctx, client, document.Metadata.Organization)
	if err != nil {
		return Plan{}, err
	}
	for _, team := range document.Teams {
		current, exists := state.Teams[team.Name]
		if !exists {
			return Plan{}, fmt.Errorf("team creation has not appeared in the API yet; retry apply")
		}
		for _, email := range team.Members {
			member, joined := state.Members[email]
			if !joined {
				continue
			}
			if _, already := state.TeamMembers[team.Name][email]; already {
				continue
			}
			if err := retryNotFound(ctx, func() error { return client.AddTeamMember(ctx, state.Organization.ID, current.ID, member.UserID) }); err != nil {
				return Plan{}, err
			}
		}
	}
	state, err = settledSnapshot(ctx, client, document.Metadata.Organization)
	if err != nil {
		return Plan{}, err
	}
	return BuildPlan(document, state)
}

func settledSnapshot(ctx context.Context, client *Client, slug string) (Snapshot, error) {
	var state Snapshot
	err := retryNotFound(ctx, func() error {
		var readErr error
		state, readErr = client.Snapshot(ctx, slug)
		return readErr
	})
	return state, err
}

func retryNotFound(ctx context.Context, operation func() error) error {
	deadline := time.NewTimer(15 * time.Second)
	defer deadline.Stop()
	for {
		err := operation()
		if err == nil || !IsStatus(err, http.StatusNotFound) {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return fmt.Errorf("the service has not observed the new organization or member yet; retry apply: %w", err)
		case <-time.After(300 * time.Millisecond):
		}
	}
}

func Pull(state Snapshot) (spec.Document, error) {
	if state.Organization == nil {
		return spec.Document{}, fmt.Errorf("organization does not exist")
	}
	document := spec.Document{
		APIVersion: spec.APIVersion, Kind: "Workspace",
		Metadata: spec.Metadata{Organization: state.Organization.Slug},
		Preset:   "team", Organization: &spec.Organization{Name: state.Organization.Name},
		People: &spec.People{Invite: "email"},
	}
	for _, member := range state.Members {
		document.People.Members = append(document.People.Members, spec.Member{Email: member.Email, Role: member.Role})
	}
	for email, invitation := range state.Invitations {
		if _, joined := state.Members[email]; !joined {
			document.People.Members = append(document.People.Members, spec.Member{Email: email, Role: invitation.Role})
		}
	}
	slices.SortFunc(document.People.Members, func(a, b spec.Member) int { return strings.Compare(a.Email, b.Email) })
	for _, team := range state.Teams {
		entry := spec.Team{Name: team.Name}
		for email := range state.TeamMembers[team.Name] {
			entry.Members = append(entry.Members, email)
		}
		slices.Sort(entry.Members)
		document.Teams = append(document.Teams, entry)
	}
	slices.SortFunc(document.Teams, func(a, b spec.Team) int { return strings.Compare(a.Name, b.Name) })
	return document, document.Validate()
}
