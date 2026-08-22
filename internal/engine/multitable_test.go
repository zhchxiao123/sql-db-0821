package engine

import (
	"strings"
	"testing"
)

// setUpTwoTables creates t (a INTEGER) and t2 (a INTEGER, b TEXT) with fixed
// rows for join/subquery/union tests: t holds (1),(2),(2),(3); t2 holds
// (2,'x'),(3,'y'),(4,'z').
func setUpTwoTables(t *testing.T, e *Engine) {
	t.Helper()
	exec(t, e, "CREATE TABLE t (a INTEGER)")
	for _, sql := range []string{
		"INSERT INTO t VALUES (1)",
		"INSERT INTO t VALUES (2)",
		"INSERT INTO t VALUES (2)",
		"INSERT INTO t VALUES (3)",
	} {
		exec(t, e, sql)
	}
	exec(t, e, "CREATE TABLE t2 (a INTEGER, b TEXT)")
	for _, sql := range []string{
		"INSERT INTO t2 VALUES (2, 'x')",
		"INSERT INTO t2 VALUES (3, 'y')",
		"INSERT INTO t2 VALUES (4, 'z')",
	} {
		exec(t, e, sql)
	}
}

func TestInnerJoin(t *testing.T) {
	e := New()
	setUpTwoTables(t, e)
	res := exec(t, e, "SELECT t.a, t2.b FROM t JOIN t2 ON t.a = t2.a")
	want := []string{"2|x", "2|x", "3|y"}
	got := rowsText(res)
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("INNER JOIN got %v want %v", got, want)
	}
	// Explicit INNER keyword and CROSS (no condition) behave the same for a
	// matching subset; verify CROSS JOIN returns the full product.
	res = exec(t, e, "SELECT t.a, t2.a FROM t CROSS JOIN t2")
	if len(res.Rows) != 4*3 {
		t.Errorf("CROSS JOIN rows=%d want %d", len(res.Rows), 12)
	}
	// USING merges the shared column into one output column.
	res = exec(t, e, "SELECT a, b FROM t JOIN t2 USING (a)")
	want = []string{"2|x", "2|x", "3|y"}
	got = rowsText(res)
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("USING JOIN got %v want %v", got, want)
	}
}

func TestLeftJoinNullPads(t *testing.T) {
	e := New()
	setUpTwoTables(t, e)
	res := exec(t, e, "SELECT t.a, t2.b FROM t LEFT JOIN t2 ON t.a = t2.a")
	// Row for a=1 has no t2 match, so b is NULL.
	want := []string{"1|NULL", "2|x", "2|x", "3|y"}
	got := rowsText(res)
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("LEFT JOIN got %v want %v", got, want)
	}
}

func TestUnionSemantics(t *testing.T) {
	e := New()
	setUpTwoTables(t, e)
	cases := []struct {
		sql  string
		want []string
	}{
		{"SELECT a FROM t UNION SELECT a FROM t", []string{"1", "2", "3"}},
		{"SELECT a FROM t UNION ALL SELECT a FROM t", []string{"1", "2", "2", "3", "1", "2", "2", "3"}},
		{"SELECT a FROM t INTERSECT SELECT a FROM t2", []string{"2", "3"}},
		{"SELECT a FROM t EXCEPT SELECT a FROM t2", []string{"1"}},
		// Compound with a whole-result ORDER BY sorts the merged rows.
		{"SELECT a FROM t UNION SELECT a FROM t ORDER BY a DESC", []string{"3", "2", "1"}},
	}
	for _, c := range cases {
		res := exec(t, e, c.sql)
		got := rowsText(res)
		if strings.Join(got, "\n") != strings.Join(c.want, "\n") {
			t.Errorf("%s got %v want %v", c.sql, got, c.want)
		}
	}
}

func TestSubqueries(t *testing.T) {
	e := New()
	setUpTwoTables(t, e)
	// Scalar subquery in the select list: first row's value repeated.
	res := exec(t, e, "SELECT (SELECT a FROM t) FROM t")
	if len(res.Rows) != 4 {
		t.Errorf("scalar subquery rows=%d want 4", len(res.Rows))
	}
	for _, row := range res.Rows {
		if got := row[0].RenderCLI(); got != "1" {
			t.Errorf("scalar subquery cell got %q want 1", got)
		}
	}
	// Derived table in FROM, filtered by WHERE.
	res = exec(t, e, "SELECT * FROM (SELECT a FROM t) WHERE a > 1")
	want := []string{"2", "2", "3"}
	got := rowsText(res)
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("derived table got %v want %v", got, want)
	}
	// EXISTS correlated subquery.
	res = exec(t, e, "SELECT a FROM t WHERE EXISTS (SELECT 1 FROM t2 WHERE t.a = t2.a)")
	want = []string{"2", "2", "3"}
	got = rowsText(res)
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("EXISTS got %v want %v", got, want)
	}
	// IN subquery has the same affinity semantics as a plain IN list.
	res = exec(t, e, "SELECT a FROM t WHERE a IN (SELECT a FROM t2)")
	want = []string{"2", "2", "3"}
	got = rowsText(res)
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("IN subquery got %v want %v", got, want)
	}
}

func TestNaturalJoin(t *testing.T) {
	e := New()
	setUpTwoTables(t, e)
	res := exec(t, e, "SELECT a, b FROM t NATURAL JOIN t2")
	want := []string{"2|x", "2|x", "3|y"}
	got := rowsText(res)
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("NATURAL JOIN got %v want %v", got, want)
	}
}

func TestQualifiedColumns(t *testing.T) {
	e := New()
	setUpTwoTables(t, e)
	res := exec(t, e, "SELECT t.a, t2.a FROM t, t2 WHERE t.a = t2.a")
	want := []string{"2|2", "2|2", "3|3"}
	got := rowsText(res)
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("comma cross join got %v want %v", got, want)
	}
}
