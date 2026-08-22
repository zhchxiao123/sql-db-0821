package engine

import (
	"errors"
	"strings"
	"testing"
)

// exec runs a statement and fails the test on error.
func exec(t *testing.T, e *Engine, sql string) *Result {
	t.Helper()
	res, err := e.Execute(sql)
	if err != nil {
		t.Fatalf("Execute(%q) error: %v", sql, err)
	}
	return res
}

// execErr runs a statement and asserts it fails with an error.
func execErr(t *testing.T, e *Engine, sql string) error {
	t.Helper()
	_, err := e.Execute(sql)
	if err == nil {
		t.Fatalf("Execute(%q) succeeded, want error", sql)
	}
	return err
}

// rowsText flattens a result into "col1|col2" lines for easy comparison.
func rowsText(res *Result) []string {
	var out []string
	for _, row := range res.Rows {
		var parts []string
		for _, v := range row {
			parts = append(parts, v.RenderCLI())
		}
		out = append(out, strings.Join(parts, "|"))
	}
	return out
}

func TestCRUDLifecycle(t *testing.T) {
	e := New()
	exec(t, e, "CREATE TABLE t (a INTEGER, b TEXT)")
	exec(t, e, "INSERT INTO t VALUES (1, 'one')")
	exec(t, e, "INSERT INTO t VALUES (2, 'two')")
	exec(t, e, "INSERT INTO t VALUES (3, 'three')")

	res := exec(t, e, "SELECT * FROM t")
	got := rowsText(res)
	want := []string{"1|one", "2|two", "3|three"}
	if len(got) != len(want) {
		t.Fatalf("got %d rows, want %d: %v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("row %d: got %q want %q", i, got[i], want[i])
		}
	}

	// UPDATE with WHERE.
	exec(t, e, "UPDATE t SET b = 'TWO' WHERE a = 2")
	res = exec(t, e, "SELECT b FROM t WHERE a = 2")
	if got := rowsText(res); len(got) != 1 || got[0] != "TWO" {
		t.Errorf("after update: got %v want [TWO]", got)
	}

	// DELETE with WHERE.
	exec(t, e, "DELETE FROM t WHERE a = 1")
	res = exec(t, e, "SELECT count(*) FROM t")
	if got := rowsText(res); len(got) != 1 || got[0] != "2" {
		t.Errorf("after delete: got %v want [2]", got)
	}

	// DROP TABLE.
	exec(t, e, "DROP TABLE t")
	execErr(t, e, "SELECT * FROM t")
}

func TestInsertColumnList(t *testing.T) {
	e := New()
	exec(t, e, "CREATE TABLE t (a INTEGER, b TEXT, c INTEGER)")
	exec(t, e, "INSERT INTO t (b, a) VALUES ('x', 7)")
	res := exec(t, e, "SELECT * FROM t")
	got := rowsText(res)
	if len(got) != 1 || got[0] != "7|x|NULL" {
		t.Errorf("column-list insert: got %v want [7|x|NULL]", got)
	}
}

func TestWhereComparisons(t *testing.T) {
	e := New()
	exec(t, e, "CREATE TABLE t (a INTEGER, b TEXT)")
	for _, sql := range []string{
		"INSERT INTO t VALUES (1, 'a')",
		"INSERT INTO t VALUES (2, 'b')",
		"INSERT INTO t VALUES (3, 'c')",
		"INSERT INTO t VALUES (4, 'd')",
	} {
		exec(t, e, sql)
	}
	cases := []struct {
		where string
		want  []string
	}{
		{"a > 2", []string{"3|c", "4|d"}},
		{"a >= 3", []string{"3|c", "4|d"}},
		{"a < 2", []string{"1|a"}},
		{"a <= 1", []string{"1|a"}},
		{"a != 2", []string{"1|a", "3|c", "4|d"}},
		{"a <> 2", []string{"1|a", "3|c", "4|d"}},
		{"b = 'c'", []string{"3|c"}},
		{"a = 2 AND b = 'b'", []string{"2|b"}},
		{"a = 1 OR a = 4", []string{"1|a", "4|d"}},
		{"NOT a = 1", []string{"2|b", "3|c", "4|d"}},
		{"a BETWEEN 2 AND 3", []string{"2|b", "3|c"}},
		{"a IN (1, 3)", []string{"1|a", "3|c"}},
		{"b LIKE 'b%'", []string{"2|b"}},
		{"a IS NOT NULL", []string{"1|a", "2|b", "3|c", "4|d"}},
	}
	for _, c := range cases {
		res := exec(t, e, "SELECT * FROM t WHERE "+c.where)
		got := rowsText(res)
		if len(got) != len(c.want) {
			t.Errorf("WHERE %s: got %v want %v", c.where, got, c.want)
			continue
		}
		for i := range c.want {
			if got[i] != c.want[i] {
				t.Errorf("WHERE %s: row %d got %q want %q", c.where, i, got[i], c.want[i])
			}
		}
	}
}

func TestNullSemantics(t *testing.T) {
	e := New()
	exec(t, e, "CREATE TABLE t (a INTEGER)")
	exec(t, e, "INSERT INTO t VALUES (NULL)")
	exec(t, e, "INSERT INTO t VALUES (1)")
	res := exec(t, e, "SELECT * FROM t WHERE a = 1")
	if got := rowsText(res); len(got) != 1 || got[0] != "1" {
		t.Errorf("a = 1: got %v want [1]", got)
	}
	// NULL comparisons filter the row out (SQLite semantics).
	res = exec(t, e, "SELECT * FROM t WHERE a = NULL")
	if got := rowsText(res); len(got) != 0 {
		t.Errorf("a = NULL: got %v want []", got)
	}
	res = exec(t, e, "SELECT * FROM t")
	if got := rowsText(res); len(got) != 2 || got[0] != "NULL" || got[1] != "1" {
		t.Errorf("select all: got %v want [NULL 1]", got)
	}
}

func TestCountStar(t *testing.T) {
	e := New()
	exec(t, e, "CREATE TABLE t (a INTEGER)")
	exec(t, e, "INSERT INTO t VALUES (1)")
	exec(t, e, "INSERT INTO t VALUES (2)")
	exec(t, e, "INSERT INTO t VALUES (3)")
	res := exec(t, e, "SELECT count(*) FROM t")
	if got := rowsText(res); len(got) != 1 || got[0] != "3" {
		t.Errorf("count(*): got %v want [3]", got)
	}
	res = exec(t, e, "SELECT count(*) FROM t WHERE a > 1")
	if got := rowsText(res); len(got) != 1 || got[0] != "2" {
		t.Errorf("count(*) where: got %v want [2]", got)
	}
}

func TestUnsupportedConstructs(t *testing.T) {
	e := New()
	exec(t, e, "CREATE TABLE t (a INTEGER, b TEXT)")
	exec(t, e, "INSERT INTO t VALUES (1, 'x')")
	cases := []string{
		"SELECT * FROM t JOIN t2 ON t.a = t2.a",
		"SELECT a FROM t UNION SELECT a FROM t",
		"SELECT CASE WHEN a > 0 THEN 1 ELSE 0 END FROM t",
		"SELECT abs(a) FROM t",
		"SELECT coalesce(a, b) FROM t",
		"SELECT (SELECT a FROM t) FROM t",
		"SELECT a FROM t WHERE EXISTS (SELECT 1 FROM t)",
		"CREATE INDEX idx ON t (a)",
		"CREATE VIEW v AS SELECT * FROM t",
		"ALTER TABLE t ADD COLUMN c INTEGER",
		"DROP INDEX idx",
		"DROP VIEW v",
		"BEGIN",
		"COMMIT",
		"PRAGMA table_info(t)",
	}
	for _, sql := range cases {
		err := execErr(t, e, sql)
		var ue *UnsupportedError
		if !errors.As(err, &ue) {
			t.Errorf("%q: want UnsupportedError, got %v", sql, err)
		}
	}
}

func TestSyntaxErrors(t *testing.T) {
	e := New()
	exec(t, e, "CREATE TABLE t (a INTEGER)")
	cases := []string{
		"",
		"SELECT",
		"SELECT FROM t",
		"SELECT * FROM",
		"INSERT INTO t VALUES",
		"INSERT INTO t VALUES (1",
		"UPDATE t SET",
		"DELETE FROM",
		"DROP TABLE",
		"CREATE TABLE",
		"SELECT * FROM nosuch",
		"SELECT nosuch FROM t",
		"INSERT INTO t VALUES (1, 2)",
		"SELECT * FROM t WHERE",
		"SELECT * FROM t WHERE a =",
		"SELECT * FROM t WHERE a = 1 AND",
		"SELECT 'unterminated",
	}
	for _, sql := range cases {
		err := execErr(t, e, sql)
		var ue *UnsupportedError
		if errors.As(err, &ue) {
			t.Errorf("%q: got UnsupportedError %v, want plain SQLError", sql, err)
		}
	}
}

func TestDuplicateTable(t *testing.T) {
	e := New()
	exec(t, e, "CREATE TABLE t (a INTEGER)")
	execErr(t, e, "CREATE TABLE t (a INTEGER)")
}

func TestColumnConstraintsIgnored(t *testing.T) {
	e := New()
	exec(t, e, "CREATE TABLE t (a INTEGER PRIMARY KEY, b TEXT NOT NULL, c INTEGER UNIQUE)")
	exec(t, e, "INSERT INTO t VALUES (1, 'x', 2)")
	res := exec(t, e, "SELECT * FROM t")
	if got := rowsText(res); len(got) != 1 || got[0] != "1|x|2" {
		t.Errorf("constraints: got %v want [1|x|2]", got)
	}
}

func TestFloatRendering(t *testing.T) {
	cases := []struct {
		f    float64
		want string
	}{
		{0, "0.0"},
		{1.5, "1.5"},
		{100, "100.0"},
		{1e-5, "1.0e-05"},
		{1.23456789012345, "1.23456789012345"},
		{-0.0, "0.0"},
		{1e10, "10000000000.0"},
		{1e15, "1.0e+15"},
		{1e100, "1.0e+100"},
		{1.0 / 3.0, "0.333333333333333"},
	}
	for _, c := range cases {
		if got := formatFloatSQLite(c.f); got != c.want {
			t.Errorf("formatFloatSQLite(%v) = %q, want %q", c.f, got, c.want)
		}
	}
}

// constRows runs a constant SELECT and returns the single result row.
func constRows(t *testing.T, sql string) []string {
	t.Helper()
	e := New()
	res := exec(t, e, sql)
	if len(res.Rows) != 1 {
		t.Fatalf("%s: got %d rows, want 1", sql, len(res.Rows))
	}
	return rowsText(res)
}

func TestConstantExpressions(t *testing.T) {
	// Acceptance criterion a2: these must match sqlite3 exactly.
	cases := []struct {
		sql  string
		want string
	}{
		{"SELECT 1+2*3", "7"},
		{"SELECT 1/0", "NULL"},
		{"SELECT 10/3", "3"},
		{"SELECT NULL AND 1", "NULL"},
		{"SELECT 'a'<'b'", "1"},
		{"SELECT CAST('12' AS INTEGER)", "12"},
		{"SELECT 'abc' LIKE 'a%'", "1"},
		{"SELECT 7%3", "1"},
		{"SELECT -5+3", "-2"},
		{"SELECT -2*3", "-6"},
		{"SELECT 1-2-3", "-4"},
		{"SELECT 2+3*4", "14"},
		{"SELECT (1+2)*3", "9"},
		{"SELECT 1 = '1'", "0"},
		{"SELECT 2 < '10'", "1"},
		{"SELECT 1 OR NULL", "1"},
		{"SELECT NOT NULL", "NULL"},
		{"SELECT NOT 1", "0"},
		{"SELECT 0 AND NULL", "0"},
		{"SELECT 1 AND 2", "1"},
		{"SELECT NULL IS NULL", "1"},
		{"SELECT 1 IS NOT NULL", "1"},
		{"SELECT NULL IS NOT NULL", "0"},
		{"SELECT 1 IN (1, 2)", "1"},
		{"SELECT 3 IN (1, 2)", "0"},
		{"SELECT NULL IN (1, NULL)", "NULL"},
		{"SELECT 1 IN (1, NULL)", "1"},
		{"SELECT 2 NOT IN (1, 3)", "1"},
		{"SELECT 5 BETWEEN 1 AND 10", "1"},
		{"SELECT 0 BETWEEN 1 AND 10", "0"},
		{"SELECT NULL BETWEEN 1 AND 10", "NULL"},
		{"SELECT 'abc' LIKE '_b_'", "1"},
		{"SELECT 'ABC' LIKE 'abc'", "1"},
		{"SELECT 'abc' LIKE 'd%'", "0"},
		{"SELECT CAST(1.9 AS INTEGER)", "1"},
		{"SELECT CAST('abc' AS INTEGER)", "0"},
		{"SELECT CAST('12x' AS INTEGER)", "12"},
		{"SELECT CAST(3 AS TEXT)", "3"},
		{"SELECT -9223372036854775808", "-9223372036854775808"},
		{"SELECT 9223372036854775808", "9.22337203685478e+18"},
		{"SELECT -9223372036854775808 + 0", "-9223372036854775808"},
		{"SELECT -9223372036854775808 % -1", "0"},
	}
	for _, c := range cases {
		got := constRows(t, c.sql)
		if len(got) != 1 || got[0] != c.want {
			t.Errorf("%s: got %v want [%s]", c.sql, got, c.want)
		}
	}
}

func TestOverflowPromotesToReal(t *testing.T) {
	// Integer overflow promotes to REAL instead of wrapping (SQLite).
	cases := []struct {
		sql  string
		want string
	}{
		{"SELECT 9223372036854775807 + 1", "9.22337203685478e+18"},
		{"SELECT -9223372036854775808 - 1", "-9.22337203685478e+18"},
		{"SELECT 9223372036854775807 * 2", "1.84467440737096e+19"},
		{"SELECT -9223372036854775808 / -1", "9.22337203685478e+18"},
	}
	for _, c := range cases {
		got := constRows(t, c.sql)
		if len(got) != 1 || got[0] != c.want {
			t.Errorf("%s: got %v want [%s]", c.sql, got, c.want)
		}
	}
}

func TestAffinityConversion(t *testing.T) {
	// Acceptance criterion a4: INSERT applies column affinity, and the
	// rendered output matches sqlite3 verbatim.
	e := New()
	exec(t, e, "CREATE TABLE aff (i INTEGER, r REAL, tx TEXT, n NUMERIC, b BLOB)")
	exec(t, e, "INSERT INTO aff VALUES (1.9, '1.0', 42, '48.00', 'abc')")
	exec(t, e, "INSERT INTO aff VALUES ('12', 2.5, 3.14, '1e5', X'4142')")
	res := exec(t, e, "SELECT * FROM aff")
	got := rowsText(res)
	want := []string{"1.9|1.0|42|48|abc", "12|2.5|3.14|100000|AB"}
	if len(got) != len(want) {
		t.Fatalf("got %d rows, want %d: %v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("row %d: got %q want %q", i, got[i], want[i])
		}
	}
}

func TestUpdateDeleteFullExpressions(t *testing.T) {
	// Acceptance criterion a3: UPDATE/DELETE with full-expression WHERE
	// matches sqlite3 row-by-row.
	e := New()
	exec(t, e, "CREATE TABLE t (a INTEGER, b TEXT)")
	exec(t, e, "INSERT INTO t VALUES (1, 'x1')")
	exec(t, e, "INSERT INTO t VALUES (2, 'x2')")
	exec(t, e, "INSERT INTO t VALUES (3, 'y3')")
	exec(t, e, "UPDATE t SET a = a + 1 WHERE b LIKE 'x%'")
	res := exec(t, e, "SELECT * FROM t")
	got := rowsText(res)
	want := []string{"2|x1", "3|x2", "3|y3"}
	if len(got) != len(want) {
		t.Fatalf("after update: got %v want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("after update row %d: got %q want %q", i, got[i], want[i])
		}
	}
	exec(t, e, "DELETE FROM t WHERE a > 2 AND b LIKE 'y%'")
	res = exec(t, e, "SELECT * FROM t")
	got = rowsText(res)
	want = []string{"2|x1", "3|x2"}
	if len(got) != len(want) {
		t.Fatalf("after delete: got %v want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("after delete row %d: got %q want %q", i, got[i], want[i])
		}
	}
}

func TestComparisonAffinity(t *testing.T) {
	// A text literal compared to an INTEGER column is converted to a number
	// (SQLite comparison affinity).
	e := New()
	exec(t, e, "CREATE TABLE w (a INTEGER)")
	exec(t, e, "INSERT INTO w VALUES (1)")
	exec(t, e, "INSERT INTO w VALUES (2)")
	res := exec(t, e, "SELECT a FROM w WHERE a = '2'")
	if got := rowsText(res); len(got) != 1 || got[0] != "2" {
		t.Errorf("a = '2': got %v want [2]", got)
	}
	res = exec(t, e, "SELECT a FROM w WHERE a > '1'")
	if got := rowsText(res); len(got) != 1 || got[0] != "2" {
		t.Errorf("a > '1': got %v want [2]", got)
	}
}

func TestSelectExpressions(t *testing.T) {
	e := New()
	exec(t, e, "CREATE TABLE t (a INTEGER, b TEXT)")
	exec(t, e, "INSERT INTO t VALUES (1, 'x')")
	exec(t, e, "INSERT INTO t VALUES (2, 'y')")
	exec(t, e, "INSERT INTO t VALUES (3, 'z')")
	res := exec(t, e, "SELECT a + 1, b FROM t WHERE a >= 2")
	got := rowsText(res)
	want := []string{"3|y", "4|z"}
	if len(got) != len(want) {
		t.Fatalf("got %v want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("row %d: got %q want %q", i, got[i], want[i])
		}
	}
	// Constant SELECT with alias and multiple columns.
	res = exec(t, e, "SELECT 1 AS x, 2 + 3 AS y")
	if got := rowsText(res); len(got) != 1 || got[0] != "1|5" {
		t.Errorf("constant select: got %v want [1|5]", got)
	}
	// COUNT(*) in a constant SELECT is 1 (SQLite).
	res = exec(t, e, "SELECT count(*) + 1")
	if got := rowsText(res); len(got) != 1 || got[0] != "2" {
		t.Errorf("count(*) constant: got %v want [2]", got)
	}
}

func TestBlobLiterals(t *testing.T) {
	e := New()
	exec(t, e, "CREATE TABLE t (b BLOB)")
	exec(t, e, "INSERT INTO t VALUES (X'4142')")
	res := exec(t, e, "SELECT * FROM t")
	if got := rowsText(res); len(got) != 1 || got[0] != "AB" {
		t.Errorf("blob: got %v want [AB]", got)
	}
	execErr(t, e, "INSERT INTO t VALUES (X'4')")  // odd hex length
	execErr(t, e, "INSERT INTO t VALUES (X'zz')") // non-hex
}
