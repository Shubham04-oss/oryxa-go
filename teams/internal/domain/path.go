package domain

import "strings"

// EmbeddingDimensions is the width of teams.embedding (pgvector). It matches
// the common 1536-wide embedding models; writes of any other length are
// rejected.
const EmbeddingDimensions = 1536

// LtreeLabel turns an id into an ltree label: letters, digits and
// underscore only.
func LtreeLabel(id string) string {
	if id == "" {
		return "_"
	}
	var label strings.Builder
	label.Grow(len(id))
	for _, r := range id {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_':
			label.WriteRune(r)
		default:
			label.WriteByte('_')
		}
	}
	return label.String()
}

// TeamPath is the team's place in the organization tree: organization
// label, then team label. Nested teams append further labels.
func TeamPath(organizationID, teamID string) string {
	return LtreeLabel(organizationID) + "." + LtreeLabel(teamID)
}
