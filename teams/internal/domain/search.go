package domain

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// MaxSearchQueryLength bounds a search query, in characters. A team's name runs
// to 120 and an email address to 254, so nothing longer can be looked for; and
// what a search costs grows with the length of the query, over every row it is
// scored against, so an unbounded one lets any member make the database work
// for seconds at a time.
const MaxSearchQueryLength = 256

// SearchQuery trims a search query and refuses one that is empty or too long.
func SearchQuery(query string) (string, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return "", fmt.Errorf("search query is required: %w", ErrInvalid)
	}
	if utf8.RuneCountInString(query) > MaxSearchQueryLength {
		return "", fmt.Errorf("search query must be at most %d characters: %w", MaxSearchQueryLength, ErrInvalid)
	}
	return query, nil
}

func ClampSearchLimit(limit int) int {
	if limit <= 0 {
		return DefaultSearchLimit
	}
	if limit > MaxSearchLimit {
		return MaxSearchLimit
	}
	return limit
}

// FuzzyMatch is true when query is a substring of a value, or close enough
// by trigram similarity (the same idea as pg_trgm on team names).
func FuzzyMatch(query string, values ...string) bool {
	query = strings.ToLower(strings.TrimSpace(query))
	if query == "" {
		return false
	}
	for _, value := range values {
		value = strings.ToLower(strings.TrimSpace(value))
		if value == "" {
			continue
		}
		if strings.Contains(value, query) || strings.Contains(query, value) {
			return true
		}
		if Similarity(query, value) >= FuzzyMatchThreshold {
			return true
		}
	}
	return false
}

// Similarity is the Dice coefficient of padded trigrams, like pg_trgm.
func Similarity(left, right string) float64 {
	a := trigrams(left)
	b := trigrams(right)
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	overlap := 0
	for gram := range a {
		if _, ok := b[gram]; ok {
			overlap++
		}
	}
	return 2 * float64(overlap) / float64(len(a)+len(b))
}

func trigrams(value string) map[string]struct{} {
	runes := []rune(strings.ToLower(strings.TrimSpace(value)))
	if len(runes) == 0 {
		return map[string]struct{}{}
	}
	padded := make([]rune, 0, len(runes)+3)
	padded = append(padded, ' ', ' ')
	padded = append(padded, runes...)
	padded = append(padded, ' ')
	grams := make(map[string]struct{}, len(padded))
	for i := 0; i+2 < len(padded); i++ {
		grams[string(padded[i:i+3])] = struct{}{}
	}
	return grams
}
