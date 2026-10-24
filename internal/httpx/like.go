package httpx

import "strings"

// MaxSearchLen is the longest search text used, in characters. Longer input
// is cut: nobody types a 5,000 character search, and a long pattern only
// makes the database work harder.
const MaxSearchLen = 100

var likeEscaper = strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)

// EscapeLike makes s match literally inside a LIKE/ILIKE pattern, whose
// default escape character is the backslash. Without it a search for "%"
// or "_" matches every row, and a pattern full of them is slow on purpose.
func EscapeLike(s string) string { return likeEscaper.Replace(s) }

// LikeContains turns search text into a "contains" pattern for a bound
// parameter: NULs dropped, trimmed, cut to MaxSearchLen characters, wildcards
// escaped.
func LikeContains(q string) string {
	// Postgres text cannot hold a NUL byte; sent as-is it fails the query.
	q = strings.TrimSpace(strings.ReplaceAll(q, "\x00", ""))
	if r := []rune(q); len(r) > MaxSearchLen {
		q = string(r[:MaxSearchLen])
	}
	return "%" + EscapeLike(q) + "%"
}
