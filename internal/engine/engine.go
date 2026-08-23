// Package engine implements a minimal SQL engine supporting the subset:
// CREATE TABLE, INSERT, SELECT (with WHERE), UPDATE, DELETE and DROP TABLE,
// plus SQLite-compatible expressions, storage classes, affinity and query
// semantics (ORDER BY, LIMIT/OFFSET, DISTINCT, GROUP BY, HAVING, aggregates).
// Everything else is rejected with an UnsupportedError so callers can tell
// "not implemented" apart from "bad SQL".
//
// Transactions (BEGIN/COMMIT/ROLLBACK), statement-level atomicity under
// implicit autocommit, crash-recovery persistence and snapshot-isolated
// concurrent connections are implemented in conn.go and persist.go.
package engine

import "strings"

// Column is a single column of a table. Column-level constraints (NOT NULL,
// UNIQUE, PRIMARY KEY, DEFAULT, CHECK) are captured at CREATE time and
// enforced on INSERT in parseInsert, matching SQLite's error categories.
// PRIMARY KEY is recorded as Unique (SQLite reports a duplicate PRIMARY KEY
// as a UNIQUE constraint failure); it does not imply NOT NULL, because
// INTEGER PRIMARY KEY stays compatible with the pinned select5 suite.
type Column struct {
	Name    string
	Type    string // declared type, upper-cased: INTEGER, TEXT, REAL, VARCHAR, ...
	NotNull bool   // declared NOT NULL; a NULL insert fails
	Unique  bool   // declared UNIQUE or PRIMARY KEY; duplicate non-NULL rows fail
	PrimaryKey bool // declared PRIMARY KEY (distinct from UNIQUE for sqlite_master)
	Default Expr   // DEFAULT value/expression; nil = none (omitted columns become NULL)
	Check   Expr   // CHECK(...) expression; nil = none
}

// Table is an in-memory table: a fixed column list plus rows of values.
// SQL keeps the original CREATE TABLE text so sqlite_master can report it.
// UniqueKeys holds composite UNIQUE / PRIMARY KEY column groups declared at
// table level (e.g. PRIMARY KEY (a, b)); each group is unique as a whole, not
// column by column. A single-column table-level key is equivalent to marking
// that column Unique.
type Table struct {
	Name       string
	Columns    []Column
	Rows       [][]Value
	UniqueKeys [][]string // table-level UNIQUE/PRIMARY KEY column groups
	SQL        string     // original CREATE TABLE statement
}

// Index describes a CREATE [UNIQUE] INDEX: the owning table, the ordered
// indexed columns, and whether the index enforces uniqueness. Indexes do not
// change execution results (the engine always scans), so they are metadata
// for sqlite_master introspection plus UNIQUE enforcement on INSERT.
type Index struct {
	Name    string
	Table   string
	Columns []string
	Unique  bool
	SQL     string // original CREATE INDEX statement
}

// columnIndex returns the index of the named column, or -1.
func (t *Table) columnIndex(name string) int {
	for i, c := range t.Columns {
		if strings.EqualFold(c.Name, name) {
			return i
		}
	}
	return -1
}

// Result is the outcome of executing one statement. SELECT queries fill
// Columns and Rows; DML statements report Affected; DDL returns an empty
// Result.
type Result struct {
	Columns  []string
	Rows     [][]Value
	Affected int64
}

// Engine (store, connections, transactions) is defined in conn.go; this file
// keeps the public record types.
