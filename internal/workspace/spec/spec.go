// Package spec reads the declarative workspace file used by the setup CLI.
// Only settings with an applier are accepted. A rejected setting is safer than
// reporting a successful apply while the running app still ignores it.
package spec

import (
	"bytes"
	"fmt"
	"io"
	"net/mail"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"go.yaml.in/yaml/v3"
)

const APIVersion = "oryxa.in/v1"

type Document struct {
	APIVersion    string        `yaml:"apiVersion" json:"apiVersion"`
	Kind          string        `yaml:"kind" json:"kind"`
	Metadata      Metadata      `yaml:"metadata" json:"metadata"`
	Preset        string        `yaml:"preset,omitempty" json:"preset,omitempty"`
	Organization  *Organization `yaml:"organization,omitempty" json:"organization,omitempty"`
	People        *People       `yaml:"people,omitempty" json:"people,omitempty"`
	Teams         Teams         `yaml:"teams,omitempty" json:"teams,omitempty"`
	Directory     *yaml.Node    `yaml:"directory,omitempty" json:"-"`
	OrgChart      *OrgChart     `yaml:"orgChart,omitempty" json:"orgChart,omitempty"`
	Chat          *yaml.Node    `yaml:"chat,omitempty" json:"-"`
	Agents        *Agents       `yaml:"agents,omitempty" json:"agents,omitempty"`
	Rules         *yaml.Node    `yaml:"rules,omitempty" json:"-"`
	Safety        *yaml.Node    `yaml:"safety,omitempty" json:"-"`
	Tools         *yaml.Node    `yaml:"tools,omitempty" json:"-"`
	Search        *yaml.Node    `yaml:"search,omitempty" json:"-"`
	Notifications *yaml.Node    `yaml:"notifications,omitempty" json:"-"`
	Data          *yaml.Node    `yaml:"data,omitempty" json:"-"`
	Security      *yaml.Node    `yaml:"security,omitempty" json:"-"`
	Ownership     Ownership     `yaml:"ownership,omitempty" json:"ownership,omitempty"`
	Secrets       *yaml.Node    `yaml:"secrets,omitempty" json:"-"`
}

type Metadata struct {
	Organization string   `yaml:"organization" json:"organization"`
	Domains      []string `yaml:"domains,omitempty" json:"domains,omitempty"`
}

type Organization struct {
	Name     string  `yaml:"name,omitempty" json:"name,omitempty"`
	Timezone *string `yaml:"timezone,omitempty" json:"timezone,omitempty"`
}

type People struct {
	Invite  string   `yaml:"invite,omitempty" json:"invite,omitempty"`
	Guests  *bool    `yaml:"guests,omitempty" json:"guests,omitempty"`
	Members []Member `yaml:"members,omitempty" json:"members,omitempty"`
	Prune   bool     `yaml:"prune,omitempty" json:"prune,omitempty"`
}

type Member struct {
	Email string `yaml:"email" json:"email"`
	Role  string `yaml:"role,omitempty" json:"role,omitempty"`
	Guest bool   `yaml:"guest,omitempty" json:"guest,omitempty"`
}

type Team struct {
	Name    string   `yaml:"name" json:"name"`
	Members []string `yaml:"members,omitempty" json:"members,omitempty"`
	Leads   []string `yaml:"leads,omitempty" json:"leads,omitempty"`
}

// Ownership hints are explicit claims about who owns a topic, system, or
// channel. A key can have one owner; the CLI never guesses one from a title.
type OwnerHint struct {
	Owner   string   `yaml:"owner" json:"owner"`
	What    string   `yaml:"what,omitempty" json:"what,omitempty"`
	Channel string   `yaml:"channel,omitempty" json:"channel,omitempty"`
	System  string   `yaml:"system,omitempty" json:"system,omitempty"`
	Tags    []string `yaml:"tags,omitempty" json:"tags,omitempty"`
}

type Ownership struct {
	Items []OwnerHint `yaml:"items" json:"items"`
	Prune bool        `yaml:"prune,omitempty" json:"prune,omitempty"`
}

func (ownership *Ownership) UnmarshalYAML(node *yaml.Node) error {
	switch node.Kind {
	case yaml.SequenceNode:
		return decodeKnownNode(node, &ownership.Items)
	case yaml.MappingNode:
		var value struct {
			Items []OwnerHint `yaml:"items"`
			Prune bool        `yaml:"prune"`
		}
		if err := decodeKnownNode(node, &value); err != nil {
			return err
		}
		ownership.Items, ownership.Prune = value.Items, value.Prune
		return nil
	default:
		return fmt.Errorf("ownership must be a list or {items, prune}")
	}
}

func (ownership Ownership) MarshalYAML() (any, error) {
	if !ownership.Prune {
		return ownership.Items, nil
	}
	return struct {
		Items []OwnerHint `yaml:"items"`
		Prune bool        `yaml:"prune"`
	}{Items: ownership.Items, Prune: true}, nil
}

func (ownership Ownership) IsZero() bool { return len(ownership.Items) == 0 && !ownership.Prune }

type Relationship struct {
	From     string `yaml:"from" json:"from"`
	Relation string `yaml:"relation" json:"relation"`
	To       string `yaml:"to" json:"to"`
}

type OrgChart struct {
	Relationships []Relationship `yaml:"relationships,omitempty" json:"relationships,omitempty"`
	Authority     []string       `yaml:"authority,omitempty" json:"authority,omitempty"`
	Executives    []string       `yaml:"executives,omitempty" json:"executives,omitempty"`
	Prune         bool           `yaml:"prune,omitempty" json:"prune,omitempty"`
}

// Teams accepts the compact list form or {items, prune}. The latter is
// explicit because omission alone must never remove a team or team member.
type Teams struct {
	Items []Team `yaml:"items" json:"items"`
	Prune bool   `yaml:"prune,omitempty" json:"prune,omitempty"`
}

func (teams *Teams) UnmarshalYAML(node *yaml.Node) error {
	switch node.Kind {
	case yaml.SequenceNode:
		return decodeKnownNode(node, &teams.Items)
	case yaml.MappingNode:
		var value struct {
			Items []Team `yaml:"items"`
			Prune bool   `yaml:"prune"`
		}
		if err := decodeKnownNode(node, &value); err != nil {
			return err
		}
		teams.Items, teams.Prune = value.Items, value.Prune
		return nil
	default:
		return fmt.Errorf("teams must be a list or {items, prune}")
	}
}

func (teams Teams) MarshalYAML() (any, error) {
	if !teams.Prune {
		return teams.Items, nil
	}
	return struct {
		Items []Team `yaml:"items"`
		Prune bool   `yaml:"prune"`
	}{Items: teams.Items, Prune: true}, nil
}

func (teams Teams) IsZero() bool { return len(teams.Items) == 0 && !teams.Prune }

func decodeKnownNode(node *yaml.Node, target any) error {
	data, err := yaml.Marshal(node)
	if err != nil {
		return err
	}
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	return decoder.Decode(target)
}

// Agents exposes the organization agent settings backed by the agents API.
// Other agent policy fields remain unsupported until they affect the runtime.
type Agents struct {
	Organization *OrganizationAgent `yaml:"organization,omitempty" json:"organization,omitempty"`
}

type OrganizationAgent struct {
	Enabled      *bool   `yaml:"enabled,omitempty" json:"enabled,omitempty"`
	DisplayName  *string `yaml:"displayName,omitempty" json:"displayName,omitempty"`
	Description  *string `yaml:"description,omitempty" json:"description,omitempty"`
	Instructions *string `yaml:"instructions,omitempty" json:"instructions,omitempty"`
}

var slugPattern = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)
var domainPattern = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]*[a-z0-9])?(?:\.[a-z0-9](?:[a-z0-9-]*[a-z0-9])?)+$`)
var relationPattern = regexp.MustCompile(`^[a-z][a-z0-9_-]*$`)

// Parse refuses unknown keys, aliases that hide settings, and multiple YAML
// documents. Error messages name paths, never submitted values.
func Parse(data []byte) (Document, error) {
	if len(data) > 1<<20 {
		return Document{}, fmt.Errorf("workspace file exceeds 1 MiB")
	}
	var root yaml.Node
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	if err := decoder.Decode(&root); err != nil {
		return Document{}, fmt.Errorf("parse workspace file: %w", err)
	}
	var extra yaml.Node
	if err := decoder.Decode(&extra); err != io.EOF {
		return Document{}, fmt.Errorf("workspace file must contain one document")
	}
	if err := rejectAliases(&root); err != nil {
		return Document{}, err
	}
	if err := rejectUnsupportedSections(&root); err != nil {
		return Document{}, err
	}
	var document Document
	strict := yaml.NewDecoder(bytes.NewReader(data))
	strict.KnownFields(true)
	if err := strict.Decode(&document); err != nil {
		return Document{}, fmt.Errorf("decode workspace file: %w", err)
	}
	if err := document.Validate(); err != nil {
		return Document{}, err
	}
	return document, nil
}

func rejectUnsupportedSections(root *yaml.Node) error {
	if len(root.Content) != 1 || root.Content[0].Kind != yaml.MappingNode {
		return fmt.Errorf("workspace file must be a mapping")
	}
	for i := 0; i < len(root.Content[0].Content); i += 2 {
		name := root.Content[0].Content[i].Value
		switch name {
		case "directory", "chat", "rules", "safety", "tools", "search", "notifications", "data", "security", "secrets":
			return fmt.Errorf("%s is not yet supported by the workspace applier", name)
		}
	}
	return nil
}

func rejectAliases(node *yaml.Node) error {
	if node.Kind == yaml.AliasNode || node.Anchor != "" {
		return fmt.Errorf("YAML aliases and anchors are not allowed")
	}
	for _, child := range node.Content {
		if err := rejectAliases(child); err != nil {
			return err
		}
	}
	return nil
}

func (document Document) Validate() error {
	if document.APIVersion != APIVersion {
		return fmt.Errorf("apiVersion must be %s", APIVersion)
	}
	if document.Kind != "Workspace" {
		return fmt.Errorf("kind must be Workspace")
	}
	if !slugPattern.MatchString(document.Metadata.Organization) {
		return fmt.Errorf("metadata.organization must be a lowercase slug")
	}
	switch document.Preset {
	case "", "startup", "team", "regulated":
	default:
		return fmt.Errorf("preset must be startup, team or regulated")
	}
	for _, section := range []struct {
		name string
		node *yaml.Node
	}{
		{"directory", document.Directory},
		{"chat", document.Chat}, {"rules", document.Rules},
		{"safety", document.Safety}, {"tools", document.Tools}, {"search", document.Search},
		{"notifications", document.Notifications}, {"data", document.Data},
		{"security", document.Security},
		{"secrets", document.Secrets},
	} {
		if section.node != nil {
			return fmt.Errorf("%s is not yet supported by the workspace applier", section.name)
		}
	}
	if document.People != nil {
		if document.People.Invite != "" && document.People.Invite != "email" && document.People.Invite != "quiet" {
			return fmt.Errorf("people.invite must be email or quiet")
		}
	}
	if document.Organization != nil && document.Organization.Timezone != nil {
		if *document.Organization.Timezone == "" {
			return fmt.Errorf("organization.timezone must be an IANA time zone")
		}
		if _, err := time.LoadLocation(*document.Organization.Timezone); err != nil {
			return fmt.Errorf("organization.timezone must be an IANA time zone")
		}
	}
	if document.Agents != nil && document.Agents.Organization != nil {
		agent := document.Agents.Organization
		if agent.DisplayName != nil && (strings.TrimSpace(*agent.DisplayName) == "" || utf8.RuneCountInString(*agent.DisplayName) > 100) {
			return fmt.Errorf("agents.organization.displayName must be between 1 and 100 characters")
		}
		if agent.Description != nil && utf8.RuneCountInString(*agent.Description) > 2000 {
			return fmt.Errorf("agents.organization.description holds at most 2000 characters")
		}
		if agent.Instructions != nil && (strings.TrimSpace(*agent.Instructions) == "" || utf8.RuneCountInString(*agent.Instructions) > 20000) {
			return fmt.Errorf("agents.organization.instructions must be between 1 and 20000 characters")
		}
	}
	seenDomains := map[string]bool{}
	for _, domain := range document.Metadata.Domains {
		if len(domain) > 253 || !domainPattern.MatchString(domain) || seenDomains[domain] {
			return fmt.Errorf("metadata.domains must contain unique lowercase domains")
		}
		seenDomains[domain] = true
	}
	seenMembers := map[string]bool{}
	guestMembers := map[string]bool{}
	if document.People != nil {
		for _, member := range document.People.Members {
			if err := validateEmail(member.Email, nil); err != nil {
				return fmt.Errorf("people.members: %w", err)
			}
			_, emailDomain, _ := strings.Cut(member.Email, "@")
			if len(seenDomains) > 0 && !seenDomains[emailDomain] {
				if !member.Guest {
					return fmt.Errorf("people.members outside metadata.domains require guest: true")
				}
			}
			if seenMembers[member.Email] {
				return fmt.Errorf("people.members has a duplicate email")
			}
			seenMembers[member.Email] = true
			guestMembers[member.Email] = member.Guest
			switch member.Role {
			case "", "member", "admin", "owner":
			default:
				return fmt.Errorf("people.members[].role must be owner, admin or member")
			}
		}
	}
	seenTeams := map[string]bool{}
	for _, team := range document.Teams.Items {
		if strings.TrimSpace(team.Name) == "" || len(team.Name) > 120 || seenTeams[team.Name] {
			return fmt.Errorf("teams[].name must be unique and between 1 and 120 characters")
		}
		seenTeams[team.Name] = true
		teamMembers := map[string]bool{}
		for _, email := range team.Members {
			if err := validateEmail(email, nil); err != nil {
				return fmt.Errorf("teams[].members: %w", err)
			}
			_, emailDomain, _ := strings.Cut(email, "@")
			if len(seenDomains) > 0 && !seenDomains[emailDomain] && !guestMembers[email] {
				return fmt.Errorf("teams[].members outside metadata.domains must be declared as guest members")
			}
			if teamMembers[email] {
				return fmt.Errorf("teams[].members has a duplicate email")
			}
			teamMembers[email] = true
		}
		seenLeads := map[string]bool{}
		for _, email := range team.Leads {
			if err := validateEmail(email, nil); err != nil {
				return fmt.Errorf("teams[].leads: %w", err)
			}
			_, emailDomain, _ := strings.Cut(email, "@")
			if len(seenDomains) > 0 && !seenDomains[emailDomain] && !guestMembers[email] {
				return fmt.Errorf("teams[].leads outside metadata.domains must be declared as guest members")
			}
			if seenLeads[email] {
				return fmt.Errorf("teams[].leads has a duplicate email")
			}
			seenLeads[email] = true
		}
	}
	return document.validateContext()
}

func (document Document) validateContext() error {
	seenHints := map[string]bool{}
	for _, hint := range document.Ownership.Items {
		if err := validateEmail(hint.Owner, nil); err != nil {
			return fmt.Errorf("ownership[].owner: %w", err)
		}
		if hint.What == "" && hint.Channel == "" && hint.System == "" {
			return fmt.Errorf("ownership[] needs what, channel, or system")
		}
		for _, field := range []string{hint.What, hint.Channel, hint.System} {
			if len(field) > 200 || strings.ContainsAny(field, "\x00\r\n") || (field != "" && strings.TrimSpace(field) != field) {
				return fmt.Errorf("ownership[] selectors must be trimmed single-line text up to 200 bytes")
			}
		}
		key := hint.Key()
		if seenHints[key] {
			return fmt.Errorf("ownership[] has duplicate selectors")
		}
		seenHints[key] = true
		seenTags := map[string]bool{}
		for _, tag := range hint.Tags {
			if tag == "" || len(tag) > 64 || strings.ContainsAny(tag, "\x00\r\n") || seenTags[tag] {
				return fmt.Errorf("ownership[].tags must be unique short single-line values")
			}
			seenTags[tag] = true
		}
	}
	if document.OrgChart == nil {
		return nil
	}
	seenRelations := map[string]bool{}
	for _, relationship := range document.OrgChart.Relationships {
		if err := validateEmail(relationship.From, nil); err != nil {
			return fmt.Errorf("orgChart.relationships[].from: %w", err)
		}
		if err := validateEmail(relationship.To, nil); err != nil {
			return fmt.Errorf("orgChart.relationships[].to: %w", err)
		}
		if relationship.From == relationship.To || !relationPattern.MatchString(relationship.Relation) {
			return fmt.Errorf("orgChart.relationships[] needs distinct people and a lowercase relation name")
		}
		key := relationship.Key()
		if seenRelations[key] {
			return fmt.Errorf("orgChart.relationships[] has a duplicate relation")
		}
		seenRelations[key] = true
	}
	seenAuthority := map[string]bool{}
	for _, relation := range document.OrgChart.Authority {
		if !relationPattern.MatchString(relation) || seenAuthority[relation] {
			return fmt.Errorf("orgChart.authority must contain unique lowercase relation names")
		}
		seenAuthority[relation] = true
	}
	seenExecutives := map[string]bool{}
	for _, email := range document.OrgChart.Executives {
		if err := validateEmail(email, nil); err != nil {
			return fmt.Errorf("orgChart.executives: %w", err)
		}
		if seenExecutives[email] {
			return fmt.Errorf("orgChart.executives has a duplicate email")
		}
		seenExecutives[email] = true
	}
	return nil
}

func (hint OwnerHint) Key() string {
	return hint.What + "\x00" + hint.Channel + "\x00" + hint.System
}

func (relationship Relationship) Key() string {
	return relationship.From + "\x00" + relationship.Relation + "\x00" + relationship.To
}

func validateEmail(email string, domains map[string]bool) error {
	address, err := mail.ParseAddress(email)
	if err != nil || address.Address != email || strings.ToLower(email) != email {
		return fmt.Errorf("email must be a lowercase address")
	}
	_, domain, ok := strings.Cut(email, "@")
	if len(domains) > 0 && (!ok || !domains[domain]) {
		return fmt.Errorf("email is outside metadata.domains")
	}
	return nil
}

func (document Document) YAML() ([]byte, error) {
	if err := document.Validate(); err != nil {
		return nil, err
	}
	return yaml.Marshal(document)
}
