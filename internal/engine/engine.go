// Package engine implements a minimal in-memory SQL engine supporting the
// subset: CREATE TABLE, INSERT, SELECT (with WHERE), UPDATE, DELETE and
// DROP TABLE, plus SELECT COUNT(*) as the only aggregate. Everything else is
// rejected with an UnsupportedError so callers can tell "not implemented"
// apart from "bad SQL".
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

// Engine holds the database state (the set of tables).
type Engine struct {
	tables map[string]*Table
}

// New returns an empty engine.
func New() *Engine {
	return &Engine{tables: make(map[string]*Table)}
}

// Execute parses and runs a single SQL statement.
func (e *Engine) Execute(sql string) (*Result, error) {
	toks, err := tokenize(sql)
	if err != nil {
		return nil, err
	}
	p := &parser{toks: toks}
	kw := strings.ToUpper(p.peek().text)
	switch kw {
	case "CREATE":
		return e.parseCreate(p)
	case "INSERT":
		return e.parseInsert(p)
	case "SELECT":
		return e.parseSelect(p)
	case "UPDATE":
		return e.parseUpdate(p)
	case "DELETE":
		return e.parseDelete(p)
	case "DROP":
		return e.parseDrop(p)
	case "":
		return nil, &SQLError{Message: "empty statement"}
	default:
		if f, ok := unsupportedKeywords[kw]; ok {
			return nil, &UnsupportedError{Feature: f}
		}
		return nil, &SQLError{Message: "syntax error near " + kw}
	}
}
