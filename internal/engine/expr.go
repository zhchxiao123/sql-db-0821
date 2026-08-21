package engine

import (
	"fmt"
	"strconv"
	"strings"
)

// Expr is a WHERE condition or a SET value expression. The minimal subset
// only needs literals, column references and binary comparisons.
type Expr interface {
	eval(row []Value, cols []Column) (Value, error)
}

// LiteralExpr is a constant value.
type LiteralExpr struct {
	val Value
}

func (e *LiteralExpr) eval(row []Value, cols []Column) (Value, error) {
	return e.val, nil
}

// ColumnExpr references a column by name; the index is resolved at eval time
// against the row layout.
type ColumnExpr struct {
	name string
}

func (e *ColumnExpr) eval(row []Value, cols []Column) (Value, error) {
	for i, c := range cols {
		if strings.EqualFold(c.Name, e.name) {
			return row[i], nil
		}
	}
	return Value{}, &SQLError{Message: fmt.Sprintf("no such column: %s", e.name)}
}

// CompareExpr is a binary comparison: =, !=, <>, <, >, <=, >=.
type CompareExpr struct {
	op          string
	left, right Expr
}

func (e *CompareExpr) eval(row []Value, cols []Column) (Value, error) {
	lv, err := e.left.eval(row, cols)
	if err != nil {
		return Value{}, err
	}
	rv, err := e.right.eval(row, cols)
	if err != nil {
		return Value{}, err
	}
	if lv.kind == Null || rv.kind == Null {
		return NullValue(), nil
	}
	c, err := compareValues(lv, rv)
	if err != nil {
		return Value{}, err
	}
	var b bool
	switch e.op {
	case "=":
		b = c == 0
	case "!=", "<>":
		b = c != 0
	case "<":
		b = c < 0
	case ">":
		b = c > 0
	case "<=":
		b = c <= 0
	case ">=":
		b = c >= 0
	default:
		return Value{}, &SQLError{Message: "unknown operator " + e.op}
	}
	if b {
		return IntValue(1), nil
	}
	return IntValue(0), nil
}

// condTrue evaluates a WHERE condition: true iff the result is a non-zero
// integer. NULL and zero both filter the row out, matching SQLite.
func condTrue(cond Expr, row []Value, cols []Column) bool {
	v, err := cond.eval(row, cols)
	if err != nil {
		return false
	}
	return v.kind == Int && v.intVal != 0
}

// compareValues orders two non-NULL values the way SQLite does: numbers
// compare numerically, text compares byte-wise, and a number is always less
// than text that cannot be parsed as a number.
func compareValues(a, b Value) (int, error) {
	an := a.kind == Int || a.kind == Float
	bn := b.kind == Int || b.kind == Float
	if an && bn {
		if a.kind == Int && b.kind == Int {
			if a.intVal < b.intVal {
				return -1, nil
			}
			if a.intVal > b.intVal {
				return 1, nil
			}
			return 0, nil
		}
		af := a.floatVal
		if a.kind == Int {
			af = float64(a.intVal)
		}
		bf := b.floatVal
		if b.kind == Int {
			bf = float64(b.intVal)
		}
		if af < bf {
			return -1, nil
		}
		if af > bf {
			return 1, nil
		}
		return 0, nil
	}
	if an && !bn {
		if f, err := strconv.ParseFloat(b.textVal, 64); err == nil {
			af := a.floatVal
			if a.kind == Int {
				af = float64(a.intVal)
			}
			if af < f {
				return -1, nil
			}
			if af > f {
				return 1, nil
			}
			return 0, nil
		}
		return -1, nil // numbers sort before unparseable text
	}
	if !an && bn {
		if f, err := strconv.ParseFloat(a.textVal, 64); err == nil {
			bf := b.floatVal
			if b.kind == Int {
				bf = float64(b.intVal)
			}
			if f < bf {
				return -1, nil
			}
			if f > bf {
				return 1, nil
			}
			return 0, nil
		}
		return 1, nil
	}
	if a.textVal < b.textVal {
		return -1, nil
	}
	if a.textVal > b.textVal {
		return 1, nil
	}
	return 0, nil
}

// parseOperand parses a literal or a column reference.
func (p *parser) parseOperand() (Expr, error) {
	t := p.peek()
	switch t.kind {
	case tokNumber:
		p.next()
		if isIntLiteral(t.text) {
			n, err := strconv.ParseInt(t.text, 10, 64)
			if err != nil {
				return nil, &SQLError{Message: "invalid integer " + t.text}
			}
			return &LiteralExpr{val: IntValue(n)}, nil
		}
		f, err := strconv.ParseFloat(t.text, 64)
		if err != nil {
			return nil, &SQLError{Message: "invalid number " + t.text}
		}
		return &LiteralExpr{val: FloatValue(f)}, nil
	case tokString:
		p.next()
		return &LiteralExpr{val: TextValue(t.text)}, nil
	case tokIdent:
		kw := strings.ToUpper(t.text)
		if kw == "NULL" {
			p.next()
			return &LiteralExpr{val: NullValue()}, nil
		}
		if kw == "TRUE" || kw == "FALSE" {
			return nil, &UnsupportedError{Feature: "boolean literals"}
		}
		if f, ok := unsupportedKeywords[kw]; ok {
			return nil, &UnsupportedError{Feature: f}
		}
		p.next()
		if p.peek().text == "(" {
			return nil, &UnsupportedError{Feature: "function call"}
		}
		return &ColumnExpr{name: t.text}, nil
	case tokPunct:
		switch t.text {
		case "(":
			return nil, &UnsupportedError{Feature: "parenthesized expression"}
		case ".":
			return nil, &UnsupportedError{Feature: "qualified column reference"}
		}
	}
	return nil, &SQLError{Message: "expected a value, got " + t.text}
}

// parseCondition parses a single comparison: operand op operand. Anything
// richer (AND/OR/NOT/LIKE/IN/BETWEEN/IS, arithmetic) is unsupported.
func (p *parser) parseCondition() (Expr, error) {
	left, err := p.parseOperand()
	if err != nil {
		return nil, err
	}
	t := p.peek()
	if t.kind == tokIdent {
		kw := strings.ToUpper(t.text)
		if f, ok := unsupportedKeywords[kw]; ok {
			return nil, &UnsupportedError{Feature: f}
		}
		return nil, &SQLError{Message: "syntax error near " + t.text}
	}
	if t.kind == tokOp {
		op := t.text
		if !isCompareOp(op) {
			return nil, &UnsupportedError{Feature: "arithmetic expression"}
		}
		p.next()
		right, err := p.parseOperand()
		if err != nil {
			return nil, err
		}
		return &CompareExpr{op: op, left: left, right: right}, nil
	}
	return nil, &SQLError{Message: "expected comparison operator, got " + t.text}
}

func isCompareOp(op string) bool {
	switch op {
	case "=", "!=", "<>", "<", ">", "<=", ">=":
		return true
	}
	return false
}
