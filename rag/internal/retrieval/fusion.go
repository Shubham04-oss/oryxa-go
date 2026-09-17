package retrieval

import (
	"sort"
	"strings"
	"unicode"

	"github.com/Shubham04-oss/oryxa-go/rag/internal/domain"
)

// RRFConstant softens the difference between the top ranks.
//
// Sixty is the value from the paper reciprocal rank fusion comes from, and it
// is not arbitrary: with a small constant the first rank dominates so
// completely that fusion is barely different from taking the better retriever,
// and with a large one every rank looks alike and fusion is barely different
// from taking either. Sixty leaves a chunk that both retrievers ranked
// respectably above one that a single retriever loved.
const RRFConstant = 60.0

// ranked is one retriever's output, in its own order.
type ranked struct {
	name string
	hits []domain.ScoredChunk
}

// fused is one chunk after fusion, with each retriever's opinion kept.
type fused struct {
	chunkID     string
	score       float64
	dense       *float64
	lexical     *float64
	denseRank   int
	lexicalRank int
}

// fuse combines ranked lists by reciprocal rank fusion.
//
// It adds reciprocals of ranks rather than the scores themselves, because the
// scores are not comparable: a cosine similarity and a ts_rank_cd are
// different numbers about different things, and normalizing them against each
// other would be inventing a relationship that is not there. Positions are
// comparable, and that is all this needs.
//
// The result is scaled so that a chunk ranked first by every retriever scores
// exactly one. That makes the score mean something fixed across queries, which
// is what a minimum-score threshold needs in order to be a usable dial.
func fuse(lists []ranked) []fused {
	if len(lists) == 0 {
		return nil
	}
	byChunk := map[string]*fused{}
	order := make([]string, 0, 64)
	for _, list := range lists {
		for position, hit := range list.hits {
			entry, seen := byChunk[hit.ChunkID]
			if !seen {
				entry = &fused{chunkID: hit.ChunkID}
				byChunk[hit.ChunkID] = entry
				order = append(order, hit.ChunkID)
			}
			rank := position + 1
			entry.score += 1 / (RRFConstant + float64(rank))
			score := hit.Score
			switch list.name {
			case denseRetriever:
				entry.dense, entry.denseRank = &score, rank
			case lexicalRetriever:
				entry.lexical, entry.lexicalRank = &score, rank
			}
		}
	}
	best := float64(len(lists)) / (RRFConstant + 1)
	results := make([]fused, 0, len(order))
	for _, chunkID := range order {
		entry := byChunk[chunkID]
		if best > 0 {
			entry.score /= best
		}
		results = append(results, *entry)
	}
	sortFused(results)
	return results
}

// sortFused orders by score, then by chunk id so the same query over the same
// corpus always returns the same order. Ties are common when only one
// retriever found a chunk, and an unstable order there makes a result set look
// like it is changing when nothing has.
func sortFused(results []fused) {
	sort.SliceStable(results, func(first, second int) bool {
		if results[first].score != results[second].score {
			return results[first].score > results[second].score
		}
		return results[first].chunkID < results[second].chunkID
	})
}

// diversify re-orders matches to trade some relevance for spread, by maximal
// marginal relevance.
//
// It earns its place on a corpus that holds near-duplicates — the same policy
// paragraph in five documents, or an overlapping chunk of one. Without it the
// top results are the same passage over and over and the answer is built from
// one source that happened to be repeated.
//
// Redundancy is measured on the words the chunks share rather than on their
// vectors. That is deliberate: the thing being suppressed here is a passage
// that says the same thing twice, which word overlap measures directly, and it
// needs no second fetch of vectors the store was never asked to return.
func diversify(matches []domain.Match, lambda float64, want int) []domain.Match {
	if lambda >= 1 || len(matches) <= 1 || want <= 0 {
		return matches
	}
	shingles := make([]map[string]struct{}, len(matches))
	for index, match := range matches {
		shingles[index] = tokenSet(match.Chunk.Text)
	}
	selected := make([]domain.Match, 0, min(want, len(matches)))
	chosen := make([]int, 0, cap(selected))
	taken := make([]bool, len(matches))

	for len(selected) < min(want, len(matches)) {
		bestIndex, bestValue := -1, 0.0
		for index, match := range matches {
			if taken[index] {
				continue
			}
			redundancy := 0.0
			for _, other := range chosen {
				if overlap := jaccard(shingles[index], shingles[other]); overlap > redundancy {
					redundancy = overlap
				}
			}
			value := lambda*match.Score - (1-lambda)*redundancy
			if bestIndex < 0 || value > bestValue {
				bestIndex, bestValue = index, value
			}
		}
		if bestIndex < 0 {
			break
		}
		taken[bestIndex] = true
		chosen = append(chosen, bestIndex)
		selected = append(selected, matches[bestIndex])
	}
	return selected
}

// tokenSet reduces text to the distinct words worth comparing on. Case is
// dropped and punctuation with it, so "Rotation." and "rotation" count as the
// same word, which is what makes near-duplicates look near.
func tokenSet(text string) map[string]struct{} {
	tokens := map[string]struct{}{}
	for _, word := range strings.FieldsFunc(strings.ToLower(text), func(symbol rune) bool {
		return !unicode.IsLetter(symbol) && !unicode.IsDigit(symbol)
	}) {
		if len(word) > 2 {
			tokens[word] = struct{}{}
		}
	}
	return tokens
}

func jaccard(left, right map[string]struct{}) float64 {
	if len(left) == 0 || len(right) == 0 {
		return 0
	}
	shared := 0
	smaller, larger := left, right
	if len(larger) < len(smaller) {
		smaller, larger = larger, smaller
	}
	for token := range smaller {
		if _, found := larger[token]; found {
			shared++
		}
	}
	union := len(left) + len(right) - shared
	if union == 0 {
		return 0
	}
	return float64(shared) / float64(union)
}
