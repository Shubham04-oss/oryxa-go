package runtime

import (
	"fmt"
	"slices"
	"strings"

	"github.com/Shubham04-oss/oryxa-go/internal/workspace/spec"
	"github.com/Shubham04-oss/oryxa-go/internal/workspace/store"
)

type directoryRecord struct {
	source, kind, externalID, reference string
}

// ResolveDirectory supplies API-resolved IDs to persistence. Storage never
// reaches across service schemas to infer an identity from a reusable label.
func ResolveDirectory(document spec.Document, state Snapshot, mapped []store.ExternalMapping) ([]store.ExternalMapping, error) {
	effective, _, err := MergeDirectory(document, document, state, mapped, false)
	if err != nil {
		return nil, err
	}
	bindings := map[string]store.ExternalMapping{}
	for _, mapping := range mapped {
		bindings[directoryRecord{mapping.Source, mapping.Kind, mapping.ExternalID, mapping.Reference}.key()] = mapping
	}
	result := []store.ExternalMapping{}
	for key, record := range records(effective.Directory) {
		resourceID := bindings[key].ResourceID
		if record.kind == "user" {
			if member, ok := state.Members[record.reference]; ok {
				resourceID = member.UserID
			}
		} else if team, ok := state.Teams[record.reference]; ok {
			resourceID = team.ID
		}
		mapping := store.ExternalMapping{Source: record.source, Kind: record.kind, ExternalID: record.externalID, Reference: record.reference, ResourceID: resourceID}
		if record.kind == "team" && bindings[key].ResourceID != "" && bindings[key].ResourceID != resourceID {
			mapping.PreviousResourceID = bindings[key].ResourceID
		}
		result = append(result, mapping)
	}
	slices.SortFunc(result, func(a, b store.ExternalMapping) int {
		return strings.Compare(a.Source+"\x00"+a.Kind+"\x00"+a.ExternalID, b.Source+"\x00"+b.Kind+"\x00"+b.ExternalID)
	})
	return result, nil
}

func (record directoryRecord) key() string {
	return record.source + "\x00" + record.kind + "\x00" + record.externalID
}

func records(directory *spec.Directory) map[string]directoryRecord {
	result := map[string]directoryRecord{}
	if directory == nil {
		return result
	}
	for _, identity := range directory.Identities {
		record := directoryRecord{identity.Source, "user", identity.ExternalID, identity.Member}
		result[record.key()] = record
	}
	for _, group := range directory.Groups {
		record := directoryRecord{group.Source, "team", group.ExternalID, group.Team}
		result[record.key()] = record
	}
	return result
}

// MergeDirectory reconciles the source snapshot with the last applied file.
// The normalized table holds the durable binding; a changed email or team
// name may update its label only when it still resolves to the same resource.
func MergeDirectory(previous, submitted spec.Document, state Snapshot, mapped []store.ExternalMapping, replace bool) (spec.Document, Plan, error) {
	previous = resolveTeamNames(previous, state)
	submitted = resolveTeamNames(submitted, state)
	effective := submitted
	plan := Plan{Changes: []Change{}, NeedsHuman: []HumanStep{}}
	old := records(previous.Directory)
	next := map[string]directoryRecord{}
	if !replace && (submitted.Directory == nil || !submitted.Directory.Prune) {
		for key, record := range old {
			known := contextMemberKnown(record.reference, state, submitted)
			if record.kind == "team" {
				_, known = state.Teams[record.reference]
				for _, team := range submitted.Teams.Items {
					if team.Name == record.reference {
						known = true
						break
					}
				}
			}
			if known {
				next[key] = record
			} else {
				plan.Notes = append(plan.Notes, "Dropped retained external "+record.kind+" mapping for departed resource "+record.reference)
			}
		}
	}
	for key, record := range records(submitted.Directory) {
		next[key] = record
	}
	bindings := map[string]store.ExternalMapping{}
	for _, mapping := range mapped {
		key := directoryRecord{mapping.Source, mapping.Kind, mapping.ExternalID, mapping.Reference}.key()
		bindings[key] = mapping
	}
	boundResources := map[string]string{}
	for key, record := range next {
		oldBinding := bindings[key]
		rebind := false
		previousResourceID := oldBinding.ResourceID
		if record.kind == "team" && oldBinding.ResourceID != "" {
			live := false
			for _, team := range state.Teams {
				if team.ID == oldBinding.ResourceID {
					live = true
					break
				}
			}
			if !live {
				rebind = true
				oldBinding.ResourceID = ""
			}
		}
		resourceID := ""
		if record.kind == "user" {
			if submitted.People != nil && submitted.People.Prune {
				kept := false
				for _, person := range submitted.People.Members {
					if person.Email == record.reference {
						kept = true
						break
					}
				}
				if !kept {
					return spec.Document{}, Plan{}, fmt.Errorf("directory identity for %s must be pruned before removing the member", record.reference)
				}
			}
			if member, ok := state.Members[record.reference]; ok {
				resourceID = member.UserID
			} else if _, ok := state.Invitations[record.reference]; ok {
				plan.NeedsHuman = append(plan.NeedsHuman, HumanStep{Step: "directory.identity", Why: record.reference + " must join before the external ID is bound"})
			} else if submitted.People != nil {
				found := false
				for _, person := range submitted.People.Members {
					if person.Email == record.reference {
						found = true
						break
					}
				}
				if found {
					plan.NeedsHuman = append(plan.NeedsHuman, HumanStep{Step: "directory.identity", Why: record.reference + " must join before the external ID is bound"})
				} else {
					return spec.Document{}, Plan{}, fmt.Errorf("directory.identities references %s, which is not a member or planned invitee", record.reference)
				}
			} else {
				return spec.Document{}, Plan{}, fmt.Errorf("directory.identities references %s, which is not a member or planned invitee", record.reference)
			}
		} else {
			if submitted.Teams.Prune {
				kept := false
				for _, team := range submitted.Teams.Items {
					if team.Name == record.reference {
						kept = true
						break
					}
				}
				if !kept {
					return spec.Document{}, Plan{}, fmt.Errorf("directory group for %s must be pruned before removing the team", record.reference)
				}
			}
			if team, ok := state.Teams[record.reference]; ok {
				resourceID = team.ID
			} else {
				found := false
				for _, team := range submitted.Teams.Items {
					if team.Name == record.reference {
						found = true
						break
					}
				}
				if !found {
					return spec.Document{}, Plan{}, fmt.Errorf("directory.groups references %s, which is not a team or planned team", record.reference)
				}
			}
		}
		if oldBinding.ResourceID != "" && resourceID != "" && resourceID != oldBinding.ResourceID {
			return spec.Document{}, Plan{}, fmt.Errorf("directory external ID %q is already bound to another Oryxa %s", record.externalID, record.kind)
		}
		if oldBinding.ResourceID != "" && resourceID == "" && oldBinding.Reference != record.reference {
			return spec.Document{}, Plan{}, fmt.Errorf("directory external ID %q cannot change its reference before the same Oryxa %s is found", record.externalID, record.kind)
		}
		if resourceID == "" {
			resourceID = oldBinding.ResourceID
		}
		if resourceID != "" {
			resourceKey := record.source + "\x00" + record.kind + "\x00" + resourceID
			if other, ok := boundResources[resourceKey]; ok && other != key {
				return spec.Document{}, Plan{}, fmt.Errorf("directory maps one Oryxa %s to two external IDs in the same source", record.kind)
			}
			boundResources[resourceKey] = key
		}
		label := record.source + "/" + record.externalID
		if rebind {
			change := Change{Op: "update", Kind: "external_team_binding", Name: label, Team: record.reference, ResourceID: previousResourceID, TeamID: resourceID, Sensitive: true}
			if state.Organization != nil {
				change.OrganizationID = state.Organization.ID
			}
			plan.Changes = append(plan.Changes, change)
		}
		if prior, ok := old[key]; !ok {
			plan.Changes = append(plan.Changes, Change{Op: "create", Kind: "external_" + record.kind, Name: label})
		} else if prior.reference != record.reference || oldBinding.Source == "" || (oldBinding.ResourceID == "" && resourceID != "") {
			plan.Changes = append(plan.Changes, Change{Op: "update", Kind: "external_" + record.kind, Name: label})
		}
	}
	for key, record := range old {
		if _, keep := next[key]; !keep {
			change := Change{Op: "delete", Kind: "external_" + record.kind, Name: record.source + "/" + record.externalID, ResourceID: bindings[key].ResourceID}
			if state.Organization != nil {
				change.OrganizationID = state.Organization.ID
			}
			if record.kind == "user" {
				change.Email = record.reference
			} else {
				change.Team = record.reference
			}
			plan.Changes = append(plan.Changes, change)
		}
	}
	if len(next) > 0 || submitted.Directory != nil || (previous.Directory != nil && !replace) {
		effective.Directory = &spec.Directory{}
		for _, record := range next {
			if record.kind == "user" {
				effective.Directory.Identities = append(effective.Directory.Identities, spec.ExternalIdentity{Source: record.source, ExternalID: record.externalID, Member: record.reference})
			} else {
				effective.Directory.Groups = append(effective.Directory.Groups, spec.ExternalGroup{Source: record.source, ExternalID: record.externalID, Team: record.reference})
			}
		}
		slices.SortFunc(effective.Directory.Identities, func(a, b spec.ExternalIdentity) int {
			return strings.Compare(a.Source+"\x00"+a.ExternalID, b.Source+"\x00"+b.ExternalID)
		})
		slices.SortFunc(effective.Directory.Groups, func(a, b spec.ExternalGroup) int {
			return strings.Compare(a.Source+"\x00"+a.ExternalID, b.Source+"\x00"+b.ExternalID)
		})
	} else {
		effective.Directory = nil
	}
	slices.SortFunc(plan.Changes, func(a, b Change) int { return strings.Compare(a.Name, b.Name) })
	slices.SortFunc(plan.NeedsHuman, func(a, b HumanStep) int { return strings.Compare(a.Why, b.Why) })
	return effective, plan, effective.Validate()
}
