package domain

import (
	"fmt"
	"slices"
	"strings"
)

// NormalizeScopes trims provider scopes, drops empty ones and duplicates,
// and sorts the rest.
func NormalizeScopes(values []string) []string {
	set := scopeSet(values)
	result := make([]string, 0, len(set))
	for scope := range set {
		result = append(result, scope)
	}
	slices.Sort(result)
	return result
}

// Implications lists, for a scope, the broader scopes that also satisfy it:
// Google's drive satisfies drive.readonly. Providers supply them.
type Implications map[string][]string

// MissingScopes returns the required scopes a connection cannot use. Each one
// must be both granted and still allowed by its app, directly or through a
// broader scope, so narrowing an app takes effect before anyone reconnects.
// The result is sorted, without duplicates.
func MissingScopes(required, granted, allowed []string, implications Implications) []string {
	grantedSet, allowedSet := scopeSet(granted), scopeSet(allowed)
	var missing []string
	for _, scope := range NormalizeScopes(required) {
		if !satisfies(grantedSet, scope, implications) || !satisfies(allowedSet, scope, implications) {
			missing = append(missing, scope)
		}
	}
	return missing
}

// AppScopes validates the scopes an admin allows on an app: everything the
// provider requires and nothing beyond its ceiling. No scopes means just the
// required ones.
func AppScopes(requested, ceiling, required []string) ([]string, error) {
	scopes := NormalizeScopes(requested)
	if len(scopes) == 0 {
		scopes = NormalizeScopes(required)
	}
	if missing := MissingScopes(required, scopes, scopes, nil); len(missing) > 0 {
		return nil, fmt.Errorf("required scope %s is missing: %w", strings.Join(missing, " "), ErrInvalid)
	}
	ceilingSet := scopeSet(ceiling)
	var outside []string
	for _, scope := range scopes {
		if _, ok := ceilingSet[scope]; !ok {
			outside = append(outside, scope)
		}
	}
	if len(outside) > 0 {
		return nil, fmt.Errorf("scope %s is not offered by the provider: %w", strings.Join(outside, " "), ErrInvalid)
	}
	return scopes, nil
}

func satisfies(set map[string]struct{}, scope string, implications Implications) bool {
	if _, ok := set[scope]; ok {
		return true
	}
	for _, broader := range implications[scope] {
		if _, ok := set[broader]; ok {
			return true
		}
	}
	return false
}

func scopeSet(values []string) map[string]struct{} {
	set := make(map[string]struct{}, len(values))
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			set[value] = struct{}{}
		}
	}
	return set
}
