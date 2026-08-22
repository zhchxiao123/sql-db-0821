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

// Column is a single column of a table.
type Column struct {
	Name string
	Type string // declared type, upper-cased: INTEGER, TEXT, REAL, VARCHAR, ...
}

// Table is an in-memory table: a fixed column list plus rows of values.
type Table struct {
	Name    string
	Columns []Column
	Rows    [][]Value
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
