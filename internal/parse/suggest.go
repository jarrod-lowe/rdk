package parse

import (
	"fmt"

	"github.com/jarrod-lowe/rdk/internal/kind"
	"github.com/jarrod-lowe/rdk/internal/schema"
)

// Typos are the common case for an unrecognised kind or field, so the error
// text names the likely intended spelling. The comparison lives here rather
// than in schema because it serves error messages only — nothing about a kind
// depends on how close two names look.

// didYouMean renders the suggestion clause for an error message, or "" when no
// candidate is close enough. Returning the whole clause keeps the callers'
// format strings readable, since the clause is optional in every one of them.
func didYouMean(s string, candidates []string) string {
	if best, ok := nearest(s, candidates); ok {
		return fmt.Sprintf("; did you mean %q?", best)
	}
	return ""
}

// knownKinds is every registered kind name, in the registry's sorted order (DD-1).
func knownKinds() []string {
	all := kind.All()
	names := make([]string, 0, len(all))
	for _, k := range all {
		names = append(names, k.Name())
	}
	return names
}

// fieldNames lists a kind's fields in the order the kind declares them, which
// leads with the ones an author writes first.
func fieldNames(k schema.Kind) []string {
	names := make([]string, 0, len(k.Fields))
	for _, f := range k.Fields {
		names = append(names, f.Name)
	}
	return names
}

// nearest returns the candidate closest to s, when one is close enough that a
// typo is the likely explanation. Short names get a tighter budget: at two
// edits, a four-letter word is no longer a misspelling of another.
func nearest(s string, candidates []string) (string, bool) {
	budget := 2
	if len(s) < 4 {
		budget = 1
	}
	best, bestDist := "", budget+1
	for _, c := range candidates {
		if d := editDistance(s, c); d < bestDist {
			best, bestDist = c, d
		}
	}
	return best, bestDist <= budget
}

// editDistance is Levenshtein distance, computed over two rolling rows rather
// than a full matrix. Inputs are single definition keys, so this is small by
// construction.
func editDistance(a, b string) int {
	ar, br := []rune(a), []rune(b)
	prev := make([]int, len(br)+1)
	curr := make([]int, len(br)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(ar); i++ {
		curr[0] = i
		for j := 1; j <= len(br); j++ {
			cost := 1
			if ar[i-1] == br[j-1] {
				cost = 0
			}
			curr[j] = min(prev[j]+1, curr[j-1]+1, prev[j-1]+cost)
		}
		prev, curr = curr, prev
	}
	return prev[len(br)]
}

// yamlType names the YAML type of a decoded value. Error messages address
// someone editing YAML, so "number" is the useful word for what they wrote;
// "uint64" is an artifact of how rdk happens to decode it.
func yamlType(v any) string {
	switch v.(type) {
	case string:
		return "string"
	case bool:
		return "boolean"
	case int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64, float32, float64:
		return "number"
	case []any:
		return "list"
	case map[string]any, map[any]any:
		return "mapping"
	case nil:
		return "null"
	}
	return "value"
}
