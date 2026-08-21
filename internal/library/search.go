package library

import (
	"context"
	"fmt"
	"strings"
	"unicode"
)

// Search finds media by filename, camera make or model.
//
// The index lives inside the encrypted database, so this is an ordinary FTS5
// query -- no blind indexes, no client-side filtering over a downloaded copy of
// the library. That was the whole reason for encrypting the file rather than
// individual columns.
func Search(ctx context.Context, v Vault, query string, limit int) ([]*Media, error) {
	expr := matchExpression(query)
	if expr == "" {
		return nil, nil
	}
	if limit <= 0 || limit > 500 {
		limit = 100
	}

	rows, err := v.DB.QueryContext(ctx,
		`SELECT `+prefixed("m", mediaColumns)+`
		   FROM media_fts f
		   JOIN media m ON m.id = f.rowid
		  WHERE media_fts MATCH ? AND m.user_id = ? AND m.deleted_at IS NULL
		  ORDER BY coalesce(m.taken_at, m.created_at) DESC, m.id DESC
		  LIMIT ?`, expr, v.UserID, limit)
	if err != nil {
		return nil, fmt.Errorf("library: search: %w", err)
	}
	defer rows.Close()

	var out []*Media
	for rows.Next() {
		m, err := scanMedia(rows)
		if err != nil {
			return nil, fmt.Errorf("library: scan search result: %w", err)
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// matchExpression turns what someone typed into an FTS5 query.
//
// FTS5 has its own syntax -- quotes, NEAR, column filters, boolean operators --
// and a stray quote or asterisk in a search box is a syntax error, not a
// search. So the input is reduced to bare words, each quoted as a literal and
// given a prefix wildcard, which is what someone typing half a filename
// expects. Words are combined with AND: adding a word should narrow the result,
// not widen it.
func matchExpression(query string) string {
	var terms []string
	for _, word := range strings.FieldsFunc(query, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	}) {
		if word == "" {
			continue
		}
		// Quoted so the token cannot be read as syntax; the wildcard has to sit
		// outside the quotes to still count as one.
		terms = append(terms, `"`+word+`"*`)
	}
	return strings.Join(terms, " AND ")
}
