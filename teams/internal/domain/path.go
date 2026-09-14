package domain

import "strings"

// EmbeddingDimensions is the width of teams.embedding (pgvector). It matches
// the common 1536-wide embedding models; writes of any other length are
// rejected.
const EmbeddingDimensions = 1536

const (
	DefaultSearchLimit = 20
	MaxSearchLimit     = 100
	// FuzzyMatchThreshold is pg_trgm's default for `%`.
	FuzzyMatchThreshold = 0.3
)

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

// UserPath is the person's place in the organization: organization
// label, then user label. Users are a flat layer under the org; they
// do not nest.
func UserPath(organizationID, userID string) string {
	return LtreeLabel(organizationID) + "." + LtreeLabel(userID)
}

// MemberPath hangs a person under a team: the team's path, then the
// user label. Nested teams keep their members under the parent too
// (`path <@ team.path`).
func MemberPath(teamPath, userID string) string {
	return ChildPath(teamPath, userID)
}

// ChildPath hangs a team under an existing ltree path.
func ChildPath(parentPath, teamID string) string {
	parentPath = strings.TrimSpace(parentPath)
	if parentPath == "" {
		return LtreeLabel(teamID)
	}
	return parentPath + "." + LtreeLabel(teamID)
}

// PathContains reports whether path is ancestor or the same node as
// descendant, using ltree's dotted labels.
func PathContains(path, descendant string) bool {
	return path != "" && (descendant == path || strings.HasPrefix(descendant, path+"."))
}
