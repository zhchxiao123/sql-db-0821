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
		{"a = 2 AND b = 'b'", nil}, // AND is unsupported -> error, handled below
	}
	for _, c := range cases {
		if strings.Contains(c.where, "AND") {
			// AND is outside the minimal subset; must be an UnsupportedError.
			err := execErr(t, e, "SELECT * FROM t WHERE "+c.where)
			var ue *UnsupportedError
			if !errors.As(err, &ue) {
				t.Errorf("WHERE %s: want UnsupportedError, got %v", c.where, err)
			}
			continue
		}
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
		"SELECT * FROM t ORDER BY a",
		"SELECT * FROM t LIMIT 1",
		"SELECT DISTINCT a FROM t",
		"SELECT a FROM t GROUP BY a",
		"SELECT a FROM t HAVING a > 0",
		"SELECT * FROM t JOIN t2 ON t.a = t2.a",
		"SELECT a FROM t UNION SELECT a FROM t",
		"SELECT CASE WHEN a > 0 THEN 1 ELSE 0 END FROM t",
		"SELECT * FROM t WHERE a LIKE '1%'",
		"SELECT * FROM t WHERE a BETWEEN 1 AND 2",
		"SELECT * FROM t WHERE a IN (1, 2)",
		"SELECT * FROM t WHERE a IS NULL",
		"SELECT * FROM t WHERE a = 1 AND b = 'x'",
		"SELECT * FROM t WHERE a = 1 OR a = 2",
		"SELECT * FROM t WHERE NOT a = 1",
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
		"SELECT * FROM t WHERE a = 1 AND b = 'x' AND a > 0",
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
		"SELECT * FROM t WHERE a",
		"SELECT * FROM t WHERE a =",
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
	}
	for _, c := range cases {
		if got := formatFloatSQLite(c.f); got != c.want {
			t.Errorf("formatFloatSQLite(%v) = %q, want %q", c.f, got, c.want)
		}
	}
}
