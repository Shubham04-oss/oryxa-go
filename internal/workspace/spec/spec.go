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
	Teams         []Team        `yaml:"teams,omitempty" json:"teams,omitempty"`
	Directory     *yaml.Node    `yaml:"directory,omitempty" json:"-"`
	OrgChart      *yaml.Node    `yaml:"orgChart,omitempty" json:"-"`
	Chat          *yaml.Node    `yaml:"chat,omitempty" json:"-"`
	Agents        *Agents       `yaml:"agents,omitempty" json:"agents,omitempty"`
	Rules         *yaml.Node    `yaml:"rules,omitempty" json:"-"`
	Safety        *yaml.Node    `yaml:"safety,omitempty" json:"-"`
	Tools         *yaml.Node    `yaml:"tools,omitempty" json:"-"`
	Search        *yaml.Node    `yaml:"search,omitempty" json:"-"`
	Notifications *yaml.Node    `yaml:"notifications,omitempty" json:"-"`
	Data          *yaml.Node    `yaml:"data,omitempty" json:"-"`
	Security      *yaml.Node    `yaml:"security,omitempty" json:"-"`
	Ownership     *yaml.Node    `yaml:"ownership,omitempty" json:"-"`
	Secrets       *yaml.Node    `yaml:"secrets,omitempty" json:"-"`
}

type Metadata struct {
	Organization string   `yaml:"organization" json:"organization"`
	Domains      []string `yaml:"domains,omitempty" json:"domains,omitempty"`
}

type Organization struct {
	Name string `yaml:"name,omitempty" json:"name,omitempty"`
}

type People struct {
	Invite  string   `yaml:"invite,omitempty" json:"invite,omitempty"`
	Members []Member `yaml:"members,omitempty" json:"members,omitempty"`
	Prune   bool     `yaml:"prune,omitempty" json:"prune,omitempty"`
}

type Member struct {
	Email string `yaml:"email" json:"email"`
	Role  string `yaml:"role,omitempty" json:"role,omitempty"`
}

type Team struct {
	Name    string   `yaml:"name" json:"name"`
	Members []string `yaml:"members,omitempty" json:"members,omitempty"`
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
		case "directory", "orgChart", "chat", "rules", "safety", "tools", "search", "notifications", "data", "security", "ownership", "secrets":
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
		{"directory", document.Directory}, {"orgChart", document.OrgChart},
		{"chat", document.Chat}, {"rules", document.Rules},
		{"safety", document.Safety}, {"tools", document.Tools}, {"search", document.Search},
		{"notifications", document.Notifications}, {"data", document.Data},
		{"security", document.Security}, {"ownership", document.Ownership},
		{"secrets", document.Secrets},
	} {
		if section.node != nil {
			return fmt.Errorf("%s is not yet supported by the workspace applier", section.name)
		}
	}
	if document.People != nil {
		if document.People.Prune {
			return fmt.Errorf("people.prune is not yet supported by the workspace applier")
		}
		if document.People.Invite != "" && document.People.Invite != "email" {
			return fmt.Errorf("people.invite currently supports email only")
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
		if domain == "" || strings.ToLower(domain) != domain || strings.ContainsAny(domain, " @/:\n\r\t") || seenDomains[domain] {
			return fmt.Errorf("metadata.domains must contain unique lowercase domains")
		}
		seenDomains[domain] = true
	}
	seenMembers := map[string]bool{}
	if document.People != nil {
		for _, member := range document.People.Members {
			if err := validateEmail(member.Email, seenDomains); err != nil {
				return fmt.Errorf("people.members: %w", err)
			}
			if seenMembers[member.Email] {
				return fmt.Errorf("people.members has a duplicate email")
			}
			seenMembers[member.Email] = true
			switch member.Role {
			case "", "member", "admin", "owner":
			default:
				return fmt.Errorf("people.members[].role must be owner, admin or member")
			}
		}
	}
	seenTeams := map[string]bool{}
	for _, team := range document.Teams {
		if strings.TrimSpace(team.Name) == "" || len(team.Name) > 120 || seenTeams[team.Name] {
			return fmt.Errorf("teams[].name must be unique and between 1 and 120 characters")
		}
		seenTeams[team.Name] = true
		teamMembers := map[string]bool{}
		for _, email := range team.Members {
			if err := validateEmail(email, seenDomains); err != nil {
				return fmt.Errorf("teams[].members: %w", err)
			}
			if teamMembers[email] {
				return fmt.Errorf("teams[].members has a duplicate email")
			}
			teamMembers[email] = true
		}
	}
	return nil
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
