package runtime

import (
	"fmt"
	"slices"
	"strings"

	"github.com/Shubham04-oss/oryxa-go/internal/workspace/spec"
)

// MergeContext reconciles the coding agent's ownership and org-chart facts
// with the last applied file. Omission retains old facts. Only an explicit
// prune (or a reviewed rollback) removes one.
func MergeContext(previous, submitted spec.Document, state Snapshot, replace bool) (spec.Document, Plan, error) {
	effective := submitted
	plan := Plan{Changes: []Change{}, NeedsHuman: []HumanStep{}}
	oldHints := map[string]spec.OwnerHint{}
	for _, hint := range previous.Ownership.Items {
		oldHints[hint.Key()] = hint
	}
	newHints := map[string]spec.OwnerHint{}
	if !replace && !submitted.Ownership.Prune {
		for key, hint := range oldHints {
			if contextMemberKnown(hint.Owner, state, submitted) {
				newHints[key] = hint
			} else {
				plan.Notes = append(plan.Notes, "Dropped retained ownership for departed member "+hint.Owner)
			}
		}
	}
	for _, hint := range submitted.Ownership.Items {
		newHints[hint.Key()] = hint
	}
	effective.Ownership = spec.Ownership{Items: make([]spec.OwnerHint, 0, len(newHints))}
	for key, hint := range newHints {
		effective.Ownership.Items = append(effective.Ownership.Items, hint)
		if old, exists := oldHints[key]; !exists {
			plan.Changes = append(plan.Changes, Change{Op: "create", Kind: "ownership", Name: hintLabel(hint), Email: hint.Owner})
		} else if !sameHint(old, hint) {
			plan.Changes = append(plan.Changes, Change{Op: "update", Kind: "ownership", Name: hintLabel(hint), Email: hint.Owner})
		}
		if err := checkContextMember(hint.Owner, state, submitted, &plan); err != nil {
			return spec.Document{}, Plan{}, fmt.Errorf("ownership[].owner: %w", err)
		}
	}
	for key, hint := range oldHints {
		if _, keep := newHints[key]; !keep {
			plan.Changes = append(plan.Changes, Change{Op: "delete", Kind: "ownership", Name: hintLabel(hint), Email: hint.Owner})
		}
	}
	slices.SortFunc(effective.Ownership.Items, func(a, b spec.OwnerHint) int { return strings.Compare(a.Key(), b.Key()) })

	oldChart := spec.OrgChart{}
	if previous.OrgChart != nil {
		oldChart = *previous.OrgChart
	}
	newChart := spec.OrgChart{}
	if !replace && (submitted.OrgChart == nil || !submitted.OrgChart.Prune) {
		newChart.Authority = slices.Clone(oldChart.Authority)
		for _, relationship := range oldChart.Relationships {
			if contextMemberKnown(relationship.From, state, submitted) && contextMemberKnown(relationship.To, state, submitted) {
				newChart.Relationships = append(newChart.Relationships, relationship)
			} else {
				plan.Notes = append(plan.Notes, "Dropped retained relationship involving a departed member: "+relationship.From+" "+relationship.Relation+" "+relationship.To)
			}
		}
		for _, email := range oldChart.Executives {
			if contextMemberKnown(email, state, submitted) {
				newChart.Executives = append(newChart.Executives, email)
			} else {
				plan.Notes = append(plan.Notes, "Dropped retained executive for departed member "+email)
			}
		}
	}
	if submitted.OrgChart != nil {
		newChart.Relationships = mergeRelationships(newChart.Relationships, submitted.OrgChart.Relationships)
		newChart.Authority = mergeStrings(newChart.Authority, submitted.OrgChart.Authority)
		newChart.Executives = mergeStrings(newChart.Executives, submitted.OrgChart.Executives)
	}
	newChart.Prune = false
	slices.SortFunc(newChart.Relationships, func(a, b spec.Relationship) int { return strings.Compare(a.Key(), b.Key()) })
	slices.Sort(newChart.Authority)
	slices.Sort(newChart.Executives)
	if previous.OrgChart != nil || submitted.OrgChart != nil {
		effective.OrgChart = &newChart
	}
	appendRelationshipChanges(&plan, oldChart.Relationships, newChart.Relationships)
	appendStringChanges(&plan, "authority_relation", oldChart.Authority, newChart.Authority)
	appendStringChanges(&plan, "executive", oldChart.Executives, newChart.Executives)
	for _, relationship := range newChart.Relationships {
		for _, email := range []string{relationship.From, relationship.To} {
			if err := checkContextMember(email, state, submitted, &plan); err != nil {
				return spec.Document{}, Plan{}, fmt.Errorf("orgChart.relationships: %w", err)
			}
		}
	}
	for _, email := range newChart.Executives {
		if err := checkContextMember(email, state, submitted, &plan); err != nil {
			return spec.Document{}, Plan{}, fmt.Errorf("orgChart.executives: %w", err)
		}
	}
	if err := validateAuthorityGraph(newChart); err != nil {
		return spec.Document{}, Plan{}, err
	}
	if err := effective.Validate(); err != nil {
		return spec.Document{}, Plan{}, err
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
	slices.SortFunc(plan.NeedsHuman, func(a, b HumanStep) int {
		if a.Step != b.Step {
			return strings.Compare(a.Step, b.Step)
		}
		return strings.Compare(a.Why, b.Why)
	})
	plan.NeedsHuman = slices.CompactFunc(plan.NeedsHuman, func(a, b HumanStep) bool { return a == b })
	return effective, plan, nil
}

func sameHint(a, b spec.OwnerHint) bool {
	return a.Owner == b.Owner && slices.Equal(a.Tags, b.Tags)
}

func hintLabel(hint spec.OwnerHint) string {
	parts := []string{}
	if hint.What != "" {
		parts = append(parts, "what="+hint.What)
	}
	if hint.Channel != "" {
		parts = append(parts, "channel="+hint.Channel)
	}
	if hint.System != "" {
		parts = append(parts, "system="+hint.System)
	}
	return strings.Join(parts, ", ")
}

func checkContextMember(email string, state Snapshot, submitted spec.Document, plan *Plan) error {
	if submitted.People != nil && submitted.People.Prune {
		kept := false
		for _, person := range submitted.People.Members {
			if person.Email == email {
				kept = true
				break
			}
		}
		if !kept {
			return fmt.Errorf("%s is omitted while people.prune removes members", email)
		}
	}
	if _, joined := state.Members[email]; joined {
		return nil
	}
	if _, invited := state.Invitations[email]; invited {
		plan.NeedsHuman = append(plan.NeedsHuman, HumanStep{Step: "context.member", Why: email + " must join before their ownership or relationship can be used"})
		return nil
	}
	if submitted.People != nil {
		for _, person := range submitted.People.Members {
			if person.Email == email {
				plan.NeedsHuman = append(plan.NeedsHuman, HumanStep{Step: "context.member", Why: email + " must join before their ownership or relationship can be used"})
				return nil
			}
		}
	}
	return fmt.Errorf("%s is not a member or planned invitee", email)
}

func contextMemberKnown(email string, state Snapshot, submitted spec.Document) bool {
	if _, ok := state.Members[email]; ok {
		return true
	}
	if _, ok := state.Invitations[email]; ok {
		return true
	}
	if submitted.People != nil {
		for _, member := range submitted.People.Members {
			if member.Email == email {
				return true
			}
		}
	}
	return false
}

func mergeRelationships(old, added []spec.Relationship) []spec.Relationship {
	byKey := map[string]spec.Relationship{}
	for _, item := range old {
		byKey[item.Key()] = item
	}
	for _, item := range added {
		byKey[item.Key()] = item
	}
	result := make([]spec.Relationship, 0, len(byKey))
	for _, item := range byKey {
		result = append(result, item)
	}
	return result
}

func mergeStrings(old, added []string) []string {
	byValue := map[string]bool{}
	for _, item := range old {
		byValue[item] = true
	}
	for _, item := range added {
		byValue[item] = true
	}
	result := make([]string, 0, len(byValue))
	for item := range byValue {
		result = append(result, item)
	}
	return result
}

func appendRelationshipChanges(plan *Plan, old, current []spec.Relationship) {
	before, after := map[string]spec.Relationship{}, map[string]spec.Relationship{}
	for _, item := range old {
		before[item.Key()] = item
	}
	for _, item := range current {
		after[item.Key()] = item
	}
	for key, item := range after {
		if _, exists := before[key]; !exists {
			plan.Changes = append(plan.Changes, Change{Op: "create", Kind: "org_relationship", Name: item.From + " " + item.Relation + " " + item.To})
		}
	}
	for key, item := range before {
		if _, exists := after[key]; !exists {
			plan.Changes = append(plan.Changes, Change{Op: "delete", Kind: "org_relationship", Name: item.From + " " + item.Relation + " " + item.To})
		}
	}
}

func appendStringChanges(plan *Plan, kind string, old, current []string) {
	before, after := map[string]bool{}, map[string]bool{}
	for _, item := range old {
		before[item] = true
	}
	for _, item := range current {
		after[item] = true
	}
	for item := range after {
		if !before[item] {
			plan.Changes = append(plan.Changes, Change{Op: "create", Kind: kind, Name: item})
		}
	}
	for item := range before {
		if !after[item] {
			plan.Changes = append(plan.Changes, Change{Op: "delete", Kind: kind, Name: item})
		}
	}
}

func validateAuthorityGraph(chart spec.OrgChart) error {
	authoritative := map[string]bool{}
	executives := map[string]bool{}
	for _, email := range chart.Executives {
		executives[email] = true
	}
	for _, relation := range chart.Authority {
		authoritative[relation] = true
	}
	edges := map[string][]string{}
	for _, relationship := range chart.Relationships {
		if authoritative[relationship.Relation] {
			if !executives[relationship.From] && executives[relationship.To] {
				return fmt.Errorf("orgChart authority places an executive below a non-executive")
			}
			edges[relationship.From] = append(edges[relationship.From], relationship.To)
		}
	}
	marks := map[string]uint8{}
	var visit func(string) bool
	visit = func(person string) bool {
		if marks[person] == 1 {
			return false
		}
		if marks[person] == 2 {
			return true
		}
		marks[person] = 1
		for _, below := range edges[person] {
			if !visit(below) {
				return false
			}
		}
		marks[person] = 2
		return true
	}
	for person := range edges {
		if !visit(person) {
			return fmt.Errorf("orgChart authority relationships form a cycle")
		}
	}
	return nil
}

// Above answers the chart's one authorization question. The from person is
// above the to person only through explicitly authoritative relationships or
// through the executive list; a person is never above themself.
func Above(chart spec.OrgChart, from, to string) bool {
	known := map[string]bool{}
	for _, email := range chart.Executives {
		known[email] = true
	}
	for _, relationship := range chart.Relationships {
		known[relationship.From] = true
		known[relationship.To] = true
	}
	if from == "" || to == "" || !known[from] || !known[to] || from == to {
		return false
	}
	if slices.Contains(chart.Executives, from) {
		return !slices.Contains(chart.Executives, to)
	}
	if slices.Contains(chart.Executives, to) {
		return false
	}
	authoritative := map[string]bool{}
	for _, relation := range chart.Authority {
		authoritative[relation] = true
	}
	edges := map[string][]string{}
	for _, relationship := range chart.Relationships {
		if authoritative[relationship.Relation] {
			edges[relationship.From] = append(edges[relationship.From], relationship.To)
		}
	}
	seen := map[string]bool{}
	var walk func(string) bool
	walk = func(person string) bool {
		if person == to {
			return true
		}
		if seen[person] {
			return false
		}
		seen[person] = true
		for _, below := range edges[person] {
			if walk(below) {
				return true
			}
		}
		return false
	}
	return walk(from)
}

// RuntimeRemovals retains only deletion approvals for live auth and teams
// resources. The workspace API checks context and mapping deletions against
// the full approved plan.
func RuntimeRemovals(plan Plan) Plan {
	filtered := Plan{Changes: []Change{}}
	for _, change := range plan.Changes {
		if change.Op != "delete" {
			continue
		}
		switch change.Kind {
		case "ownership", "org_relationship", "authority_relation", "executive", "external_user", "external_team":
			continue
		default:
			filtered.Changes = append(filtered.Changes, change)
		}
	}
	return filtered
}

func CombinePlans(plans ...Plan) Plan {
	combined := Plan{Changes: []Change{}, NeedsHuman: []HumanStep{}}
	for _, plan := range plans {
		combined.Changes = append(combined.Changes, plan.Changes...)
		combined.NeedsHuman = append(combined.NeedsHuman, plan.NeedsHuman...)
		combined.Notes = append(combined.Notes, plan.Notes...)
	}
	slices.SortFunc(combined.Changes, func(a, b Change) int {
		if a.Op != b.Op {
			return strings.Compare(a.Op, b.Op)
		}
		if a.Kind != b.Kind {
			return strings.Compare(a.Kind, b.Kind)
		}
		return strings.Compare(a.Name, b.Name)
	})
	slices.SortFunc(combined.NeedsHuman, func(a, b HumanStep) int {
		if a.Step != b.Step {
			return strings.Compare(a.Step, b.Step)
		}
		return strings.Compare(a.Why, b.Why)
	})
	combined.NeedsHuman = slices.CompactFunc(combined.NeedsHuman, func(a, b HumanStep) bool { return a == b })
	slices.Sort(combined.Notes)
	combined.Notes = slices.Compact(combined.Notes)
	return combined
}
