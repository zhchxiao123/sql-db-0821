package sqllogictest

import (
	"os"
	"path/filepath"
	"testing"
)

// writeTest writes a .test file into a temp dir and returns its path.
func writeTest(t *testing.T, name, content string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestRunFilePass(t *testing.T) {
	path := writeTest(t, "basic.test", `statement ok
CREATE TABLE t (a INTEGER, b TEXT)

statement ok
INSERT INTO t VALUES (1, 'one')

statement ok
INSERT INTO t VALUES (2, 'two')

query IT rowsort
SELECT * FROM t
----
1
one
2
two

query I
SELECT count(*) FROM t
----
2
`)
	stats, err := RunFile(path, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if stats.Total != 5 || stats.Passed != 5 || stats.Failed != 0 || stats.Waived != 0 {
		t.Errorf("stats = %+v, want 5 passed", stats)
	}
}

func TestRunFileHashMode(t *testing.T) {
	path := writeTest(t, "hash.test", `statement ok
CREATE TABLE t (a INTEGER)

statement ok
INSERT INTO t VALUES (1)

statement ok
INSERT INTO t VALUES (2)

query I rowsort
SELECT a FROM t
----
2 values hashing to 6ddb4095eb719e2a9f0a3f95677d24e0
`)
	stats, err := RunFile(path, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if stats.Failed != 0 {
		t.Errorf("hash mode: %+v", stats)
	}
}

func TestRunFileWrongExpected(t *testing.T) {
	path := writeTest(t, "wrong.test", `statement ok
CREATE TABLE t (a INTEGER)

statement ok
INSERT INTO t VALUES (1)

query I
SELECT a FROM t
----
99
`)
	stats, err := RunFile(path, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if stats.Failed != 1 || len(stats.Failures) != 1 {
		t.Errorf("stats = %+v, want 1 failure", stats)
	}
	if stats.Failures[0].Line != 7 {
		t.Errorf("failure line = %d, want 7", stats.Failures[0].Line)
	}
}

func TestRunFileStatementError(t *testing.T) {
	path := writeTest(t, "err.test", `statement error
SELECT * FROM nosuch

statement ok
CREATE TABLE t (a INTEGER)

statement error
INSERT INTO t VALUES (1, 2)
`)
	stats, err := RunFile(path, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if stats.Failed != 0 || stats.Passed != 3 {
		t.Errorf("stats = %+v, want 3 passed", stats)
	}
}

func TestRunFileQueryError(t *testing.T) {
	path := writeTest(t, "qerr.test", `statement ok
CREATE TABLE t (a INTEGER)

query error
SELECT * FROM nosuch
`)
	stats, err := RunFile(path, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if stats.Failed != 0 || stats.Passed != 2 {
		t.Errorf("stats = %+v, want 2 passed", stats)
	}
}

func TestRunFileWaiveUnsupported(t *testing.T) {
	path := writeTest(t, "unsup.test", `statement ok
CREATE TABLE t (a INTEGER)

statement ok
INSERT INTO t VALUES (1)

query I rowsort
SELECT a FROM t ORDER BY a
----
1
`)
	// Non-strict: the ORDER BY record is waived, not failed.
	stats, err := RunFile(path, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if stats.Failed != 0 || stats.Waived != 1 || stats.Passed != 2 {
		t.Errorf("non-strict stats = %+v, want 2 passed 1 waived", stats)
	}
	// Strict: the waived record counts as a failure.
	stats, err = RunFile(path, Options{Strict: true})
	if err != nil {
		t.Fatal(err)
	}
	if stats.Failed != 1 {
		t.Errorf("strict stats = %+v, want 1 failure", stats)
	}
}

func TestSortRows(t *testing.T) {
	// Regression test: rows must be copied out before sorting so the sort
	// cannot corrupt the source slice (see the earlier aliasing bug).
	vals := []string{"2", "1", "1", "2"}
	sortRows(vals, 2)
	want := []string{"1", "2", "2", "1"}
	for i := range want {
		if vals[i] != want[i] {
			t.Fatalf("sortRows = %v, want %v", vals, want)
		}
	}
}

func TestSortRowsThreeRows(t *testing.T) {
	// Three rows of two columns each.
	vals := []string{"b", "2", "a", "1", "c", "3"}
	sortRows(vals, 2)
	want := []string{"a", "1", "b", "2", "c", "3"}
	for i := range want {
		if vals[i] != want[i] {
			t.Fatalf("sortRows = %v, want %v", vals, want)
		}
	}
}

func TestComputeHash(t *testing.T) {
	// md5("1\n2\n") — verified against the reference runner.
	got := computeHash([]string{"1", "2"})
	want := "6ddb4095eb719e2a9f0a3f95677d24e0"
	if got != want {
		t.Errorf("computeHash = %s, want %s", got, want)
	}
}

func TestExtractHash(t *testing.T) {
	if got := extractHash("2 values hashing to abc123"); got != "abc123" {
		t.Errorf("extractHash = %q, want abc123", got)
	}
	if got := extractHash("not a hash line"); got != "" {
		t.Errorf("extractHash = %q, want empty", got)
	}
}

func TestParseFileSkipConditional(t *testing.T) {
	path := writeTest(t, "cond.test", `skipif sqlite
statement ok
CREATE TABLE t (a INTEGER)

onlyif sqlite
statement ok
INSERT INTO t VALUES (1)

statement ok
INSERT INTO t VALUES (2)
`)
	tf, err := ParseFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// Both conditional records (skipif/onlyif) are skipped entirely, so only
	// the final unconditional statement survives.
	if len(tf.Records) != 1 {
		t.Errorf("records = %d, want 1 (conditional records skipped)", len(tf.Records))
	}
}
