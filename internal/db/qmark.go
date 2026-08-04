// Package db opens SAK's PostgreSQL database and applies pending migrations.
package db

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Rebind converts SQL using "?" placeholders into Postgres "$1, $2, …" form.
//
// Claude 2026-08-04: driver-level ?→$n shim for the SQLite→Postgres migration.
// Reason: ~337 call sites and 5 dynamic IN (?) builders; rewriting every store
// would be riskier than one tested rewriter.
// Troubleshooting: if a query silently misbinds, check for ? inside quotes or
// a jsonb ? operator — use ?? for a literal question mark.
// Review if: stores migrate to native $n or a different driver.
// Related files: internal/db/qmark_driver.go, .omc/plans/autopilot-impl-sakms-postgres-migration.md
func Rebind(query string) string {
	var b strings.Builder
	b.Grow(len(query) + 8)
	n := 0
	i := 0
	for i < len(query) {
		c := query[i]

		// Single-quoted string literal (SQL standard '' escape).
		if c == '\'' {
			b.WriteByte(c)
			i++
			for i < len(query) {
				if query[i] == '\'' {
					b.WriteByte('\'')
					i++
					if i < len(query) && query[i] == '\'' {
						b.WriteByte('\'')
						i++
						continue
					}
					break
				}
				b.WriteByte(query[i])
				i++
			}
			continue
		}

		// Double-quoted identifier.
		if c == '"' {
			b.WriteByte(c)
			i++
			for i < len(query) {
				b.WriteByte(query[i])
				if query[i] == '"' {
					i++
					break
				}
				i++
			}
			continue
		}

		// Dollar-quoted string: $tag$ … $tag$
		if c == '$' {
			tag, next, ok := readDollarTag(query, i)
			if ok {
				b.WriteString(query[i:next])
				i = next
				end := "$" + tag + "$"
				if j := strings.Index(query[i:], end); j >= 0 {
					b.WriteString(query[i : i+j+len(end)])
					i = i + j + len(end)
				} else {
					b.WriteString(query[i:])
					i = len(query)
				}
				continue
			}
		}

		// Line comment.
		if c == '-' && i+1 < len(query) && query[i+1] == '-' {
			for i < len(query) && query[i] != '\n' {
				b.WriteByte(query[i])
				i++
			}
			continue
		}

		// Block comment.
		if c == '/' && i+1 < len(query) && query[i+1] == '*' {
			b.WriteByte(c)
			i++
			b.WriteByte(query[i])
			i++
			for i+1 < len(query) {
				if query[i] == '*' && query[i+1] == '/' {
					b.WriteByte('*')
					b.WriteByte('/')
					i += 2
					break
				}
				b.WriteByte(query[i])
				i++
			}
			continue
		}

		// Escaped literal question mark.
		if c == '?' && i+1 < len(query) && query[i+1] == '?' {
			b.WriteByte('?')
			i += 2
			continue
		}

		if c == '?' {
			n++
			fmt.Fprintf(&b, "$%d", n)
			i++
			continue
		}

		b.WriteByte(c)
		i++
	}
	return b.String()
}

// readDollarTag parses a leading $tag$ at index i. tag may be empty ($$).
func readDollarTag(s string, i int) (tag string, next int, ok bool) {
	if i >= len(s) || s[i] != '$' {
		return "", i, false
	}
	j := i + 1
	for j < len(s) {
		r, size := utf8.DecodeRuneInString(s[j:])
		if r == '$' {
			return s[i+1 : j], j + 1, true
		}
		// Postgres dollar-tag: letters, digits, underscore.
		if !(unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_') {
			return "", i, false
		}
		j += size
	}
	return "", i, false
}
