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
	Op             string `json:"op"`
	Kind           string `json:"kind"`
	Name           string `json:"name"`
	Role           string `json:"role,omitempty"`
	Team           string `json:"team,omitempty"`
	Email          string `json:"email,omitempty"`
	OrganizationID string `json:"organization_id,omitempty"`
	ResourceID     string `json:"resource_id,omitempty"`
	TeamID         string `json:"team_id,omitempty"`
	Sensitive      bool   `json:"sensitive,omitempty"`
}

type HumanStep struct {
	Step string `json:"step"`
	Why  string `json:"why"`
}

type Plan struct {
	PlanID      string      `json:"plan_id,omitempty"`
	PlanHash    string      `json:"plan_hash,omitempty"`
	BaseVersion int64       `json:"base_version"`
	Changes     []Change    `json:"changes"`
	NeedsHuman  []HumanStep `json:"needs_human"`
}

// SensitiveChanges is the exact set that needs a bound, reviewed server plan.
func SensitiveChanges(plan Plan) []Change {
	invites := 0
	for _, change := range plan.Changes {
		if change.Op == "create" && change.Kind == "invitation" {
			invites++
		}
	}
	changes := []Change{}
	for _, change := range plan.Changes {
		if change.Op == "delete" || change.Sensitive || change.Kind == "policy" || change.Kind == "member_role" || change.Kind == "team_member_role" || change.Role == "admin" || (invites > 25 && change.Kind == "invitation") {
			changes = append(changes, change)
		}
	}
	return changes
}

func BuildPlan(document spec.Document, state Snapshot) (Plan, error) {
	document = document.Normalize()
	if err := document.Validate(); err != nil {
		return Plan{}, err
	}
	plan := Plan{Changes: []Change{}, NeedsHuman: []HumanStep{}}
	if state.Organization == nil {
		plan.Changes = append(plan.Changes, Change{Op: "create", Kind: "organization", Name: document.Metadata.Organization})
	} else if organizationChanged(document, *state.Organization) {
		if state.ActorRole != "owner" || state.ActorIsAgent {
			return Plan{}, fmt.Errorf("organization %s: only a human organization owner may change organization settings", document.Metadata.Organization)
		}
		plan.Changes = append(plan.Changes, Change{Op: "update", Kind: "organization", Name: document.Metadata.Organization})
	}
	wantedMembers := map[string]bool{}
	if document.People != nil {
		for _, member := range document.People.Members {
			wantedMembers[member.Email] = true
			role := desiredMemberRole(member, state)
			if existing, ok := state.Members[member.Email]; ok {
				if existing.Role != role {
					if state.Organization != nil && (state.ActorRole != "owner" || state.ActorIsAgent) {
						return Plan{}, fmt.Errorf("people.members[%s]: only a human organization owner may change member roles", member.Email)
					}
					if existing.Role == "owner" || role == "owner" {
						return Plan{}, fmt.Errorf("people.members[%s]: role changes involving an owner are not supported", member.Email)
					}
					plan.Changes = append(plan.Changes, Change{Op: "update", Kind: "member_role", Name: member.Email, Role: role})
				}
			} else if invitation, ok := state.Invitations[member.Email]; ok {
				if invitation.Role != role || invitation.Guest != member.Guest {
					return Plan{}, fmt.Errorf("people.members has a pending invitation with another role or guest setting")
				}
				plan.NeedsHuman = append(plan.NeedsHuman, HumanStep{Step: "people.join", Why: member.Email + " must accept the invitation"})
			} else {
				if role == "admin" && state.Organization != nil && (state.ActorRole != "owner" || state.ActorIsAgent) {
					return Plan{}, fmt.Errorf("people.members[%s]: only a human organization owner may invite an admin", member.Email)
				}
				if role == "owner" {
					return Plan{}, fmt.Errorf("people.members cannot invite an owner")
				}
				if member.Guest && !guestsEnabled(document, state) {
					return Plan{}, fmt.Errorf("people.members guest invitations require people.guests: true")
				}
				plan.Changes = append(plan.Changes, Change{Op: "create", Kind: "invitation", Name: member.Email, Role: role})
				plan.NeedsHuman = append(plan.NeedsHuman, HumanStep{Step: "people.join", Why: member.Email + " must accept the invitation"})
			}
		}
		if document.People.Prune {
			if state.Organization == nil {
				return Plan{}, fmt.Errorf("people.prune requires an existing organization")
			}
			if state.ActorID == "" {
				return Plan{}, fmt.Errorf("people.prune requires a signed-in member")
			}
			if state.ActorRole != "owner" || state.ActorIsAgent {
				return Plan{}, fmt.Errorf("people.prune requires a human organization owner")
			}
			remainingOwners := 0
			for email, member := range state.Members {
				if wantedMembers[email] && member.Role == "owner" {
					remainingOwners++
				}
				if wantedMembers[email] {
					continue
				}
				if member.UserID == state.ActorID {
					return Plan{}, fmt.Errorf("people.prune cannot remove the signed-in member")
				}
				plan.Changes = append(plan.Changes, Change{Op: "delete", Kind: "member", Name: email})
			}
			if remainingOwners == 0 {
				return Plan{}, fmt.Errorf("people.prune must keep an existing owner")
			}
			for email := range state.Invitations {
				if !wantedMembers[email] {
					plan.Changes = append(plan.Changes, Change{Op: "delete", Kind: "invitation", Name: email})
				}
			}
		}
	}
	wantedTeams := map[string]spec.Team{}
	for _, team := range document.Teams.Items {
		wantedTeams[team.Name] = team
		_, exists := state.Teams[team.Name]
		if !exists {
			plan.Changes = append(plan.Changes, Change{Op: "create", Kind: "team", Name: team.Name})
		}
		joinedLead := false
		for _, email := range team.Leads {
			if _, joined := state.Members[email]; joined {
				joinedLead = true
				break
			}
		}
		emails, roles := teamMemberRoles(team)
		for _, email := range emails {
			if _, joined := state.Members[email]; !joined {
				if !wantedMembers[email] && state.Invitations[email].Email == "" {
					return Plan{}, fmt.Errorf("teams[].members references a person absent from the organization and people.members")
				}
				plan.NeedsHuman = append(plan.NeedsHuman, HumanStep{Step: "teams.members", Why: email + " must join before being added to " + team.Name})
				continue
			}
			if existing, already := state.TeamMembers[team.Name][email]; !already {
				plan.Changes = append(plan.Changes, Change{Op: "create", Kind: "team_member", Name: team.Name + "/" + email, Role: roles[email]})
			} else if roles[email] == "admin" && existing.Role != "admin" {
				plan.Changes = append(plan.Changes, Change{Op: "update", Kind: "team_member_role", Name: team.Name + "/" + email, Role: "admin"})
			} else if document.Teams.Prune && joinedLead && roles[email] == "member" && existing.Role == "admin" {
				plan.Changes = append(plan.Changes, Change{Op: "delete", Kind: "team_admin", Name: team.Name + "/" + email, Team: team.Name, Email: email})
			}
		}
		if document.Teams.Prune && exists {
			wantedTeamMembers := map[string]bool{}
			for _, email := range emails {
				wantedTeamMembers[email] = true
			}
			for email := range state.TeamMembers[team.Name] {
				if !wantedTeamMembers[email] {
					if state.TeamMembers[team.Name][email].UserID == state.ActorID {
						return Plan{}, fmt.Errorf("teams.prune cannot remove the signed-in member from a kept team")
					}
					plan.Changes = append(plan.Changes, Change{Op: "delete", Kind: "team_member", Name: team.Name + "/" + email, Team: team.Name, Email: email})
				}
			}
		}
	}
	if document.Teams.Prune {
		if state.Organization == nil || state.ActorID == "" {
			return Plan{}, fmt.Errorf("teams.prune requires a signed-in member of an existing organization")
		}
		if state.ActorRole != "owner" || state.ActorIsAgent {
			return Plan{}, fmt.Errorf("teams.prune requires a human organization owner")
		}
		for _, team := range state.Teams {
			if team.Path == "" {
				return Plan{}, fmt.Errorf("teams.prune requires team hierarchy paths from the teams service")
			}
		}
		for name, team := range state.Teams {
			if _, keep := wantedTeams[name]; keep {
				continue
			}
			for keptName := range wantedTeams {
				if kept, exists := state.Teams[keptName]; exists && team.Path != "" && strings.HasPrefix(kept.Path, team.Path+".") {
					return Plan{}, fmt.Errorf("teams.prune cannot remove a parent of a kept team")
				}
			}
			plan.Changes = append(plan.Changes, Change{Op: "delete", Kind: "team", Name: name})
			for email := range state.TeamMembers[name] {
				plan.Changes = append(plan.Changes, Change{Op: "delete", Kind: "team_member", Name: name + "/" + email, Team: name, Email: email})
			}
		}
	}
	input, err := organizationAgentInput(document, state)
	if err != nil {
		return Plan{}, err
	}
	for _, field := range []string{"name", "description", "instructions", "status"} {
		if _, changed := input[field]; changed {
			plan.Changes = append(plan.Changes, Change{Op: "update", Kind: "organization_agent", Name: field})
		}
	}
	// Approval must identify the resource reviewed, not just a reusable name or
	// email. Replacements and role changes require a fresh removal review.
	for i := range plan.Changes {
		change := &plan.Changes[i]
		if change.Op != "delete" {
			continue
		}
		change.OrganizationID = state.Organization.ID
		switch change.Kind {
		case "member":
			member := state.Members[change.Name]
			change.ResourceID, change.Role = member.UserID, member.Role
		case "invitation":
			invitation := state.Invitations[change.Name]
			change.ResourceID, change.Role = invitation.ID, invitation.Role
		case "team":
			change.ResourceID = state.Teams[change.Name].ID
		case "team_member", "team_admin":
			member := state.TeamMembers[change.Team][change.Email]
			change.ResourceID, change.Role = member.UserID, member.Role
			change.TeamID = state.Teams[change.Team].ID
			if change.TeamID == "" {
				return Plan{}, fmt.Errorf("removing a team member requires a stable team ID")
			}
		}
		if change.OrganizationID == "" || change.ResourceID == "" {
			return Plan{}, fmt.Errorf("removing %s requires stable organization and resource IDs", change.Kind)
		}
	}
	slices.SortFunc(plan.Changes, func(a, b Change) int {
		if a.Op != b.Op {
			return strings.Compare(a.Op, b.Op)
		}
		if a.Kind != b.Kind {
			return strings.Compare(a.Kind, b.Kind)
		}
		return strings.Compare(a.Name, b.Name)
	})
	return plan, nil
}

func teamMemberRoles(team spec.Team) ([]string, map[string]string) {
	roles := make(map[string]string, len(team.Members)+len(team.Leads))
	for _, email := range team.Members {
		roles[email] = "member"
	}
	for _, email := range team.Leads {
		roles[email] = "admin"
	}
	emails := make([]string, 0, len(roles))
	for email := range roles {
		emails = append(emails, email)
	}
	slices.Sort(emails)
	return emails, roles
}

func desiredMemberRole(member spec.Member, state Snapshot) string {
	if member.Role != "" {
		return member.Role
	}
	if existing, ok := state.Members[member.Email]; ok {
		return existing.Role
	}
	if existing, ok := state.Invitations[member.Email]; ok {
		return existing.Role
	}
	return "member"
}

func guestsEnabled(document spec.Document, state Snapshot) bool {
	if document.People != nil && document.People.Guests != nil {
		return *document.People.Guests
	}
	return state.Organization != nil && state.Organization.GuestsAllowed
}

func organizationChanged(document spec.Document, current Organization) bool {
	if document.Organization != nil {
		if document.Organization.Name != "" && document.Organization.Name != current.Name {
			return true
		}
		if document.Organization.Timezone != nil && *document.Organization.Timezone != current.Timezone {
			return true
		}
	}
	if document.People != nil && document.People.Guests != nil && *document.People.Guests != current.GuestsAllowed {
		return true
	}
	if document.Metadata.Domains != nil {
		wanted := slices.Clone(document.Metadata.Domains)
		actual := slices.Clone(current.Domains)
		slices.Sort(wanted)
		slices.Sort(actual)
		return !slices.Equal(wanted, actual)
	}
	return false
}

func memberIsGuest(email string, domains []string) bool {
	if len(domains) == 0 {
		return false
	}
	_, emailDomain, _ := strings.Cut(email, "@")
	return !slices.Contains(domains, emailDomain)
}

func organizationAgentInput(document spec.Document, state Snapshot) (map[string]any, error) {
	input := map[string]any{}
	if document.Agents == nil || document.Agents.Organization == nil {
		return input, nil
	}
	wanted := document.Agents.Organization
	if state.Organization != nil && state.Agent == nil {
		return nil, fmt.Errorf("agents.organization requires the agents service to be available")
	}
	if wanted.DisplayName != nil && (state.Agent == nil || state.Agent.Name != *wanted.DisplayName) {
		input["name"] = *wanted.DisplayName
	}
	if wanted.Description != nil && (state.Agent == nil || state.Agent.Description != *wanted.Description) {
		input["description"] = *wanted.Description
	}
	if wanted.Instructions != nil && (state.Agent == nil || state.Agent.Instructions != *wanted.Instructions) {
		input["instructions"] = *wanted.Instructions
	}
	if wanted.Enabled != nil {
		status := "disabled"
		if *wanted.Enabled {
			status = "active"
		}
		if state.Agent == nil || state.Agent.Status != status {
			input["status"] = status
		}
	}
	if len(input) > 0 && state.Agent != nil && !state.Agent.CanManage {
		return nil, fmt.Errorf("agents.organization requires an organization owner or admin")
	}
	return input, nil
}

// Apply reconciles only settings backed by the existing services. Repeated
// applies skip previously created resources; members who have not accepted an
// invitation are reported as human steps for the next run.
func Apply(ctx context.Context, client *Client, document spec.Document) (Plan, error) {
	return apply(ctx, client, document, nil)
}

// ApplyApproved applies removals only when the caller approved the exact
// deletion set. A new join between plan and apply forces another review.
func ApplyApproved(ctx context.Context, client *Client, document spec.Document, approved Plan) (Plan, error) {
	return apply(ctx, client, document, &approved)
}

func apply(ctx context.Context, client *Client, document spec.Document, approved *Plan) (Plan, error) {
	document = document.Normalize()
	state, err := client.Snapshot(ctx, document.Metadata.Organization)
	if err != nil {
		return Plan{}, err
	}
	initial, err := BuildPlan(document, state)
	if err != nil {
		return Plan{}, err
	}
	if err := verifyApprovedRemovals(initial, approved); err != nil {
		return Plan{}, err
	}
	if state.Organization == nil {
		name := document.Metadata.Organization
		if document.Organization != nil && document.Organization.Name != "" {
			name = document.Organization.Name
		}
		if err := client.CreateOrganization(ctx, name, document.Metadata.Organization, document); err != nil {
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
	if organizationChanged(document, *state.Organization) {
		if err := client.UpdateOrganization(ctx, state.Organization.ID, *state.Organization, document); err != nil {
			return Plan{}, err
		}
	}
	if document.People != nil {
		for _, member := range document.People.Members {
			role := desiredMemberRole(member, state)
			if existing, ok := state.Members[member.Email]; ok {
				if existing.Role != role {
					if err := client.ChangeMemberRole(ctx, state.Organization.ID, existing.UserID, role); err != nil {
						return Plan{}, err
					}
				}
			} else if _, invited := state.Invitations[member.Email]; !invited {
				if err := client.Invite(ctx, state.Organization.ID, member.Email, role, document.People.Invite == "quiet", member.Guest); err != nil {
					return Plan{}, err
				}
			}
		}
	}
	for _, team := range document.Teams.Items {
		if _, exists := state.Teams[team.Name]; !exists {
			if err := retryNotFound(ctx, func() error { return client.CreateTeam(ctx, state.Organization.ID, team.Name, document.Teams.Prune) }); err != nil {
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
	for _, team := range document.Teams.Items {
		current, exists := state.Teams[team.Name]
		if !exists {
			return Plan{}, fmt.Errorf("team creation has not appeared in the API yet; retry apply")
		}
		emails, roles := teamMemberRoles(team)
		for _, email := range emails {
			member, joined := state.Members[email]
			if !joined {
				continue
			}
			if existing, already := state.TeamMembers[team.Name][email]; already {
				if roles[email] == "admin" && existing.Role != "admin" {
					if err := client.ChangeTeamMemberRole(ctx, state.Organization.ID, current.ID, member.UserID, "admin"); err != nil {
						return Plan{}, err
					}
				}
				continue
			}
			if err := retryNotFound(ctx, func() error {
				return client.AddTeamMember(ctx, state.Organization.ID, current.ID, member.UserID, roles[email])
			}); err != nil {
				return Plan{}, err
			}
		}
	}
	input, err := organizationAgentInput(document, state)
	if err != nil {
		return Plan{}, err
	}
	if len(input) > 0 {
		if err := client.UpdateOrganizationAgent(ctx, state.Organization.ID, input); err != nil {
			return Plan{}, err
		}
	}
	if (document.People != nil && document.People.Prune) || document.Teams.Prune {
		state, err = settledSnapshot(ctx, client, document.Metadata.Organization)
		if err != nil {
			return Plan{}, err
		}
		currentPlan, err := BuildPlan(document, state)
		if err != nil {
			return Plan{}, err
		}
		if err := verifyApprovedRemovals(currentPlan, approved); err != nil {
			return Plan{}, err
		}
		if err := applyRemovals(ctx, client, state, currentPlan); err != nil {
			return Plan{}, err
		}
	}
	state, err = settledSnapshot(ctx, client, document.Metadata.Organization)
	if err != nil {
		return Plan{}, err
	}
	return BuildPlan(document, state)
}

func applyRemovals(ctx context.Context, client *Client, state Snapshot, plan Plan) error {
	// Remove team members before teams, and children before parents. Removing a
	// parent can cascade in the teams service, so deepest paths go first.
	removeTeam := map[string]bool{}
	for _, change := range plan.Changes {
		if change.Op == "delete" && change.Kind == "team" {
			removeTeam[change.Name] = true
		}
	}
	for _, change := range plan.Changes {
		if change.Op != "delete" || change.Kind != "team_member" || removeTeam[change.Team] {
			continue
		}
		if err := client.RemoveTeamMember(ctx, state.Organization.ID, state.Teams[change.Team].ID, state.TeamMembers[change.Team][change.Email].UserID); err != nil {
			return err
		}
	}
	for _, change := range plan.Changes {
		if change.Op != "delete" || change.Kind != "team_admin" || removeTeam[change.Team] {
			continue
		}
		if err := client.ChangeTeamMemberRole(ctx, state.Organization.ID, state.Teams[change.Team].ID, state.TeamMembers[change.Team][change.Email].UserID, "member"); err != nil {
			return err
		}
	}
	var removeTeams []Team
	for _, change := range plan.Changes {
		if change.Op == "delete" && change.Kind == "team" {
			removeTeams = append(removeTeams, state.Teams[change.Name])
		}
	}
	slices.SortFunc(removeTeams, func(a, b Team) int { return strings.Compare(b.Path, a.Path) })
	for _, team := range removeTeams {
		if err := client.DeleteTeam(ctx, state.Organization.ID, team.ID); err != nil {
			return err
		}
	}
	for _, change := range plan.Changes {
		if change.Op != "delete" {
			continue
		}
		switch change.Kind {
		case "invitation":
			if err := client.RevokeInvitation(ctx, state.Organization.ID, state.Invitations[change.Name].ID); err != nil {
				return err
			}
		case "member":
			if err := client.RemoveMember(ctx, state.Organization.ID, state.Members[change.Name].UserID); err != nil {
				return err
			}
		}
	}
	return nil
}

func verifyApprovedRemovals(plan Plan, approved *Plan) error {
	wanted := deletions(plan)
	if len(wanted) == 0 {
		return nil
	}
	if approved == nil || !slices.Equal(wanted, deletions(*approved)) {
		return fmt.Errorf("the removal plan changed or was not confirmed; run plan and confirm again")
	}
	return nil
}

// RemovalsDiffer reports whether an approved plan still matches the current
// deletion set, including when a previously planned deletion disappeared.
func RemovalsDiffer(current, approved Plan) bool {
	return !slices.Equal(deletions(current), deletions(approved))
}

func deletions(plan Plan) []Change {
	var result []Change
	for _, change := range plan.Changes {
		if change.Op == "delete" {
			result = append(result, change)
		}
	}
	slices.SortFunc(result, func(a, b Change) int {
		if a.Kind != b.Kind {
			return strings.Compare(a.Kind, b.Kind)
		}
		return strings.Compare(a.Name, b.Name)
	})
	return result
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
		Metadata: spec.Metadata{Organization: state.Organization.Slug, Domains: state.Organization.Domains},
		Preset:   "team", Organization: &spec.Organization{Name: state.Organization.Name},
		People: &spec.People{Invite: "email", Guests: &state.Organization.GuestsAllowed},
	}
	if state.Organization.Timezone != "" {
		document.Organization.Timezone = &state.Organization.Timezone
	}
	for _, member := range state.Members {
		document.People.Members = append(document.People.Members, spec.Member{Email: member.Email, Role: member.Role, Guest: memberIsGuest(member.Email, state.Organization.Domains)})
	}
	for email, invitation := range state.Invitations {
		if _, joined := state.Members[email]; !joined {
			document.People.Members = append(document.People.Members, spec.Member{Email: email, Role: invitation.Role, Guest: invitation.Guest})
		}
	}
	slices.SortFunc(document.People.Members, func(a, b spec.Member) int { return strings.Compare(a.Email, b.Email) })
	for _, team := range state.Teams {
		entry := spec.Team{Name: team.Name}
		for email := range state.TeamMembers[team.Name] {
			entry.Members = append(entry.Members, email)
			if state.TeamMembers[team.Name][email].Role == "admin" {
				entry.Leads = append(entry.Leads, email)
			}
		}
		slices.Sort(entry.Members)
		slices.Sort(entry.Leads)
		document.Teams.Items = append(document.Teams.Items, entry)
	}
	slices.SortFunc(document.Teams.Items, func(a, b spec.Team) int { return strings.Compare(a.Name, b.Name) })
	if state.Agent != nil {
		enabled := state.Agent.Status == "active"
		document.Agents = &spec.Agents{Organization: &spec.OrganizationAgent{
			Enabled: &enabled, DisplayName: &state.Agent.Name,
			Description: &state.Agent.Description, Instructions: &state.Agent.Instructions,
		}}
	}
	return document, document.Validate()
}
