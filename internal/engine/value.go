package engine

import (
	"fmt"
	"strconv"
	"strings"
)

// ValueKind identifies the runtime type of a stored or computed value.
type ValueKind int

const (
	Null ValueKind = iota
	Int
	Float
	Text
)

// Value is a single cell in a table row or query result. The engine is
// strictly typed: INTEGER columns hold Int values, REAL columns hold Float
// values, TEXT/VARCHAR columns hold Text values, and NULL is its own kind.
type Value struct {
	kind     ValueKind
	intVal   int64
	floatVal float64
	textVal  string
}

func NullValue() Value           { return Value{kind: Null} }
func IntValue(v int64) Value     { return Value{kind: Int, intVal: v} }
func FloatValue(v float64) Value { return Value{kind: Float, floatVal: v} }
func TextValue(v string) Value   { return Value{kind: Text, textVal: v} }

func (v Value) Kind() ValueKind { return v.kind }
func (v Value) IsNull() bool    { return v.kind == Null }
func (v Value) Int() int64      { return v.intVal }
func (v Value) Float() float64  { return v.floatVal }
func (v Value) Text() string    { return v.textVal }

// RenderCLI renders a value for the engine command-line output. The format
// follows the sqlite3 CLI: rows are joined with "|" and NULL is rendered as
// the literal "NULL" (sqllogictest convention, not sqlite3's empty string).
func (v Value) RenderCLI() string {
	switch v.kind {
	case Null:
		return "NULL"
	case Int:
		return strconv.FormatInt(v.intVal, 10)
	case Float:
		return formatFloatSQLite(v.floatVal)
	case Text:
		return v.textVal
	}
	return ""
}

// RenderSLT renders a value in the sqllogictest result format for the given
// column type character ('I', 'R' or 'T'). This mirrors the reference
// sqllogictest runner (slt_sqlite.c): NULL is "NULL", integers are decimal,
// floats are "%.3f", empty text is "(empty)" and control characters are "@".
func (v Value) RenderSLT(typeChar byte) string {
	if v.kind == Null {
		return "NULL"
	}
	switch typeChar {
	case 'I':
		switch v.kind {
		case Int:
			return strconv.FormatInt(v.intVal, 10)
		case Float:
			return strconv.FormatInt(int64(v.floatVal), 10)
		}
		return v.textVal
	case 'R':
		switch v.kind {
		case Float:
			return fmt.Sprintf("%.3f", v.floatVal)
		case Int:
			return fmt.Sprintf("%.3f", float64(v.intVal))
		}
		return v.textVal
	case 'T':
		s := v.textVal
		if s == "" {
			return "(empty)"
		}
		var b strings.Builder
		for i := 0; i < len(s); i++ {
			c := s[i]
			if c < ' ' || c > '~' {
				b.WriteByte('@')
			} else {
				b.WriteByte(c)
			}
		}
		return b.String()
	}
	return v.textVal
}

// formatFloatSQLite renders a float the way the sqlite3 CLI does: 15
// significant digits, always showing a decimal point, and a two-digit signed
// exponent. Verified against sqlite3 3.51.0 for the common cases.
func formatFloatSQLite(f float64) string {
	if f == 0 {
		return "0.0"
	}
	s := strconv.FormatFloat(f, 'g', 15, 64)
	if !strings.ContainsAny(s, ".eE") {
		s += ".0"
	} else if i := strings.IndexAny(s, "eE"); i >= 0 && !strings.Contains(s[:i], ".") {
		s = s[:i] + ".0" + s[i:]
	}
	return s
}
