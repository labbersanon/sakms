package proposals

// AC3 (.omc/plans/autopilot-impl.md §7) — proves that Repick and RepickEpisode's
// UPDATE ... SET column lists are byte-for-byte what they were before this
// feature added MoveMode to the same file, using the property-based-assertion
// style internal/api/airdatemonitor_static_test.go established: assert a
// specific, load-bearing property of the source (here, the SQL column list),
// not whole-function or whole-file text identity.
//
// Deliberately NOT whole-function text equality. A whole-function diff breaks
// on an unrelated gofmt run or a comment edit inside Repick/RepickEpisode —
// both of which AC3 itself explicitly permits (see the plan's "R-6 vs AC3"
// ruling) — and a future session would "fix" a spurious failure by
// regenerating the golden, silently retiring the guard the next time it
// mattered. The column list is the actual invariant: it is what
// internal/rename and internal/dedup depend on Repick/RepickEpisode writing,
// and it is exactly what a stray future edit to these two methods would
// change.
//
// WHAT THIS TEST PROVES: the SQL string literal inside each of
// Store.Repick/Store.RepickEpisode still writes exactly the same ordered
// column list it always has.
//
// WHAT IT DOES NOT PROVE: it says nothing about comments, whitespace,
// argument order in the Go call, or any other method in this file — MoveMode
// included. It is a narrow backstop against a regression to these two
// specific, frozen methods, not a general "proposals.go is unchanged" proof.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// repickFrozenFile is the one file this backstop covers — the same file
// MoveMode was added to. Its absence (a rename or move) is a hard failure,
// not a skip, mirroring airdatemonitor_static_test.go's file-presence
// assertion: a silent vacuous pass would defeat the whole point of the test.
const repickFrozenFile = "proposals.go"

// repickFrozenSetClausePattern extracts the SET clause body between "SET" and
// the WHERE that follows it. (?is) makes it case-insensitive and lets '.'
// match newlines, since both queries wrap the SET clause across lines.
var repickFrozenSetClausePattern = regexp.MustCompile(`(?is)UPDATE\s+proposals\s+SET\s+(.*?)\s+WHERE\s+id\s*=\s*\?`)

// repickGoldenColumns is the golden, ordered column list for each frozen
// method — captured from the SQL as it exists today. Any change to either
// method's UPDATE, including a reorder, must update this golden deliberately
// (and, per AC3, must not happen as a side effect of this feature).
var repickGoldenColumns = map[string][]string{
	"Repick":        {"title", "tmdb_id", "year", "status", "reason"},
	"RepickEpisode": {"title", "tmdb_id", "year", "season_number", "episode_number", "extra_episode_numbers", "status", "reason"},
}

// TestRepickStoreMethodsUnchanged is AC3(a): AST-parse proposals.go, locate
// the Repick and RepickEpisode FuncDecls (methods on *Store specifically, not
// merely any function with a matching name), and assert only their SQL
// UPDATE ... SET column lists against the goldens above.
func TestRepickStoreMethodsUnchanged(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, repickFrozenFile, nil, parser.ParseComments)
	if err != nil {
		t.Fatalf("parsing %s: %v", repickFrozenFile, err)
	}

	found := map[string]bool{}
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Recv == nil || len(fn.Recv.List) != 1 {
			continue
		}
		if !repickFrozenIsStoreReceiver(fn.Recv.List[0].Type) {
			continue
		}
		golden, want := repickGoldenColumns[fn.Name.Name]
		if !want {
			continue
		}
		found[fn.Name.Name] = true

		lit := repickFrozenFindSQLLiteral(t, fn)
		got := repickFrozenColumnList(t, fn.Name.Name, lit)

		if len(got) != len(golden) {
			t.Errorf("%s: UPDATE column list changed shape — got %d columns %v, want %d columns %v",
				fn.Name.Name, len(got), got, len(golden), golden)
			continue
		}
		for i := range golden {
			if got[i] != golden[i] {
				t.Errorf("%s: UPDATE column list diverged at position %d — got %q, want %q (full: got %v, want %v)",
					fn.Name.Name, i, got[i], golden[i], got, golden)
			}
		}
	}

	// Vacuous-pass guard: if either method were renamed, moved off *Store, or
	// removed, the loop above would silently find nothing to check and this
	// test would pass for the wrong reason. Fail hard instead, same
	// discipline as airdatemonitor_static_test.go's file/symbol lookups.
	for name := range repickGoldenColumns {
		if !found[name] {
			t.Fatalf("%s no longer declares a *Store method named %q — this backstop's target moved or was renamed; update the test (and confirm §4.2's frozen-line-range check still makes sense) rather than letting this pass vacuously", repickFrozenFile, name)
		}
	}
}

// repickFrozenIsStoreReceiver reports whether a receiver type expression is
// *Store — the pointer-receiver shape every method in this file uses.
func repickFrozenIsStoreReceiver(expr ast.Expr) bool {
	star, ok := expr.(*ast.StarExpr)
	if !ok {
		return false
	}
	ident, ok := star.X.(*ast.Ident)
	return ok && ident.Name == "Store"
}

// repickFrozenFindSQLLiteral returns the one raw (backtick) string literal in
// fn's body — the SQL query. Repick/RepickEpisode each also contain one or
// two ordinary double-quoted fmt.Errorf format strings, so this is scoped to
// backtick literals specifically rather than "the first string literal
// found", which would be fragile against wrapping error text differently.
// Zero or more-than-one hits is a hard failure: either would mean this test
// is checking the wrong thing, or nothing at all.
func repickFrozenFindSQLLiteral(t *testing.T, fn *ast.FuncDecl) *ast.BasicLit {
	t.Helper()
	var hits []*ast.BasicLit
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		lit, ok := n.(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return true
		}
		if strings.HasPrefix(lit.Value, "`") {
			hits = append(hits, lit)
		}
		return true
	})
	if len(hits) != 1 {
		t.Fatalf("%s: expected exactly 1 raw SQL string literal, found %d — this test's SQL-literal-detection assumption (backtick-quoted, singular) no longer holds for this method; update the test rather than trusting a partial result", fn.Name.Name, len(hits))
	}
	return hits[0]
}

// repickFrozenColumnList unquotes lit and extracts the ordered column-name
// list from its "UPDATE proposals SET <list> WHERE id = ?" shape.
func repickFrozenColumnList(t *testing.T, methodName string, lit *ast.BasicLit) []string {
	t.Helper()
	sql, err := strconv.Unquote(lit.Value)
	if err != nil {
		t.Fatalf("%s: unquoting SQL literal: %v", methodName, err)
	}

	m := repickFrozenSetClausePattern.FindStringSubmatch(sql)
	if m == nil {
		t.Fatalf("%s: SQL literal does not match the expected \"UPDATE proposals SET ... WHERE id = ?\" shape — got: %s", methodName, sql)
	}

	var cols []string
	for _, assignment := range strings.Split(m[1], ",") {
		assignment = strings.TrimSpace(assignment)
		if assignment == "" {
			continue
		}
		eq := strings.Index(assignment, "=")
		if eq < 0 {
			t.Fatalf("%s: malformed SET assignment %q (no '=')", methodName, assignment)
		}
		col := strings.TrimSpace(assignment[:eq])
		col = strings.Trim(col, `"`)
		cols = append(cols, col)
	}
	return cols
}
