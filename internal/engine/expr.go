package engine

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

// Expr is a value expression: a literal, a column reference, or an operator
// application. Every node knows how to evaluate itself against a row and
// which affinity it carries (used for comparisons, matching SQLite's
// sqlite3ExprAffinity).
type Expr interface {
	eval(row []Value, cols []Column) (Value, error)
	affinity() Affinity
}

// LiteralExpr is a constant value. bigInt is non-empty only for the literal
// 9223372036854775808 (2^63), which overflows int64: on its own it is a
// real, but unary minus turns it into the smallest int64, matching SQLite.
type LiteralExpr struct {
	val    Value
	bigInt string
}

func (e *LiteralExpr) eval(row []Value, cols []Column) (Value, error) {
	return e.val, nil
}

func (e *LiteralExpr) affinity() Affinity { return AffNone }

// ColumnExpr references a column by name; the index is resolved at eval time
// against the row layout. aff is the column's affinity, filled in by
// resolveColumnAffinity once the statement's table is known.
type ColumnExpr struct {
	name string
	aff  Affinity
}

func (e *ColumnExpr) eval(row []Value, cols []Column) (Value, error) {
	for i, c := range cols {
		if strings.EqualFold(c.Name, e.name) {
			return row[i], nil
		}
	}
	return Value{}, &SQLError{Message: fmt.Sprintf("no such column: %s", e.name)}
}

func (e *ColumnExpr) affinity() Affinity { return e.aff }

// CastExpr is a CAST(x AS type) expression.
type CastExpr struct {
	inner Expr
	aff   Affinity
}

func (e *CastExpr) eval(row []Value, cols []Column) (Value, error) {
	v, err := e.inner.eval(row, cols)
	if err != nil {
		return Value{}, err
	}
	return castValue(v, e.aff), nil
}

func (e *CastExpr) affinity() Affinity { return e.aff }

// ArithExpr is a binary arithmetic operator: + - * / %.
type ArithExpr struct {
	op          string
	left, right Expr
}

func (e *ArithExpr) eval(row []Value, cols []Column) (Value, error) {
	lv, err := e.left.eval(row, cols)
	if err != nil {
		return Value{}, err
	}
	rv, err := e.right.eval(row, cols)
	if err != nil {
		return Value{}, err
	}
	return arith(e.op, lv, rv), nil
}

func (e *ArithExpr) affinity() Affinity { return AffNone }

// NegExpr is a unary minus. SQLite implements it as 0 - x.
type NegExpr struct {
	inner Expr
}

func (e *NegExpr) eval(row []Value, cols []Column) (Value, error) {
	v, err := e.inner.eval(row, cols)
	if err != nil {
		return Value{}, err
	}
	return arith("-", IntValue(0), v), nil
}

func (e *NegExpr) affinity() Affinity { return AffNone }

// CompareExpr is a binary comparison: =, !=, <>, <, >, <=, >=, IS, IS NOT.
// nullSafe marks IS/IS NOT, which treat NULL as a comparable value.
type CompareExpr struct {
	op       string
	nullSafe bool
	left     Expr
	right    Expr
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
	if e.nullSafe {
		// IS / IS NOT: NULL is a value, not an unknown.
		if lv.kind == Null && rv.kind == Null {
			if e.op == "IS" {
				return IntValue(1), nil
			}
			return IntValue(0), nil
		}
		if lv.kind == Null || rv.kind == Null {
			if e.op == "IS" {
				return IntValue(0), nil
			}
			return IntValue(1), nil
		}
	} else if lv.kind == Null || rv.kind == Null {
		return NullValue(), nil
	}
	// Comparison affinity: convert operands per SQLite rules before
	// comparing.
	lv, rv = applyComparisonAffinity(lv, rv, comparisonAffinity(e))
	c := compareValues(lv, rv)
	var b bool
	switch e.op {
	case "=", "IS":
		b = c == 0
	case "!=", "<>", "IS NOT":
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

func (e *CompareExpr) affinity() Affinity { return AffNone }

// LogicalExpr is AND or OR.
type LogicalExpr struct {
	op          string
	left, right Expr
}

func (e *LogicalExpr) eval(row []Value, cols []Column) (Value, error) {
	lv, err := e.left.eval(row, cols)
	if err != nil {
		return Value{}, err
	}
	rv, err := e.right.eval(row, cols)
	if err != nil {
		return Value{}, err
	}
	if e.op == "AND" {
		return andValues(lv, rv), nil
	}
	return orValues(lv, rv), nil
}

func (e *LogicalExpr) affinity() Affinity { return AffNone }

// NotExpr is a unary NOT.
type NotExpr struct {
	inner Expr
}

func (e *NotExpr) eval(row []Value, cols []Column) (Value, error) {
	v, err := e.inner.eval(row, cols)
	if err != nil {
		return Value{}, err
	}
	return notValue(v), nil
}

func (e *NotExpr) affinity() Affinity { return AffNone }

// InExpr is "x IN (v1, v2, ...)" or "x NOT IN (...)".
type InExpr struct {
	negate bool
	left   Expr
	list   []Expr
}

func (e *InExpr) eval(row []Value, cols []Column) (Value, error) {
	lv, err := e.left.eval(row, cols)
	if err != nil {
		return Value{}, err
	}
	hasNull := false
	for _, item := range e.list {
		iv, err := item.eval(row, cols)
		if err != nil {
			return Value{}, err
		}
		if iv.kind == Null {
			hasNull = true
			continue
		}
		if lv.kind == Null {
			continue
		}
		// IN compares with the same affinity rules as "=".
		a, b := applyComparisonAffinity(lv, iv, comparisonAffinity(&CompareExpr{left: e.left, right: item}))
		if compareValues(a, b) == 0 {
			if e.negate {
				return IntValue(0), nil
			}
			return IntValue(1), nil
		}
	}
	if lv.kind == Null || hasNull {
		return NullValue(), nil
	}
	if e.negate {
		return IntValue(1), nil
	}
	return IntValue(0), nil
}

func (e *InExpr) affinity() Affinity { return AffNone }

// BetweenExpr is "x BETWEEN low AND high" (or NOT BETWEEN), which SQLite
// rewrites as x >= low AND x <= high.
type BetweenExpr struct {
	negate      bool
	left, low, high Expr
}

func (e *BetweenExpr) eval(row []Value, cols []Column) (Value, error) {
	ge := &CompareExpr{op: ">=", left: e.left, right: e.low}
	le := &CompareExpr{op: "<=", left: e.left, right: e.high}
	geV, err := ge.eval(row, cols)
	if err != nil {
		return Value{}, err
	}
	leV, err := le.eval(row, cols)
	if err != nil {
		return Value{}, err
	}
	r := andValues(geV, leV)
	if e.negate {
		return notValue(r), nil
	}
	return r, nil
}

func (e *BetweenExpr) affinity() Affinity { return AffNone }

// LikeExpr is "x LIKE pattern [ESCAPE c]" or "x GLOB pattern".
type LikeExpr struct {
	glob    bool
	negate  bool
	left    Expr
	pattern Expr
	escape  Expr // nil unless ESCAPE given
}

func (e *LikeExpr) eval(row []Value, cols []Column) (Value, error) {
	lv, err := e.left.eval(row, cols)
	if err != nil {
		return Value{}, err
	}
	pv, err := e.pattern.eval(row, cols)
	if err != nil {
		return Value{}, err
	}
	if lv.kind == Null || pv.kind == Null {
		return NullValue(), nil
	}
	s, _ := likeOperand(lv)
	pat, _ := likeOperand(pv)
	var matched bool
	if e.glob {
		matched = globMatch(pat, s)
	} else {
		esc := byte(0)
		if e.escape != nil {
			ev, err := e.escape.eval(row, cols)
			if err != nil {
				return Value{}, err
			}
			if ev.kind == Null {
				return NullValue(), nil
			}
			es, _ := likeOperand(ev)
			if len(es) != 1 {
				return Value{}, &SQLError{Message: "ESCAPE expression must be a single character"}
			}
			esc = es[0]
		}
		matched = likeMatch(pat, s, esc)
	}
	if e.negate {
		matched = !matched
	}
	if matched {
		return IntValue(1), nil
	}
	return IntValue(0), nil
}

func (e *LikeExpr) affinity() Affinity { return AffNone }

// CountStarExpr is the marker for COUNT(*) in a SELECT list. In a constant
// SELECT (no FROM) it evaluates to 1, matching SQLite; with FROM the
// statement parser handles it as a row count.
type CountStarExpr struct{}

func (e *CountStarExpr) eval(row []Value, cols []Column) (Value, error) {
	return IntValue(1), nil
}

func (e *CountStarExpr) affinity() Affinity { return AffNone }

// condTrue evaluates a WHERE condition: true iff the result is a non-zero
// integer. NULL and zero both filter the row out, matching SQLite.
func condTrue(cond Expr, row []Value, cols []Column) bool {
	v, err := cond.eval(row, cols)
	if err != nil {
		return false
	}
	return v.kind == Int && v.intVal != 0
}

// comparisonAffinity computes the affinity to apply to a comparison's
// operands, matching sqlite3CompareAffinity: if either side is a column, the
// column's affinity wins; if both are columns, numeric affinity wins when
// either column is numeric, otherwise BLOB (no conversion).
func comparisonAffinity(e *CompareExpr) Affinity {
	la := e.left.affinity()
	ra := e.right.affinity()
	if la == AffNone && ra == AffNone {
		return AffNone
	}
	if la == AffNone {
		return ra
	}
	if ra == AffNone {
		return la
	}
	if isNumericAffinity(la) || isNumericAffinity(ra) {
		return AffNumeric
	}
	return AffBlob
}

// applyComparisonAffinity converts the two operands of a comparison per the
// affinity rules of OP_Eq/OP_Ne/OP_Lt etc.:
//
//   - numeric affinity: only TEXT operands are converted to numbers; REAL
//     operands are left alone (they already compare numerically).
//   - TEXT affinity: INTEGER/REAL operands are converted to text.
//   - BLOB affinity: no conversion.
func applyComparisonAffinity(a, b Value, aff Affinity) (Value, Value) {
	switch aff {
	case AffNumeric, AffInteger, AffReal:
		if a.kind == Text {
			a = applyNumericAffinity(a, false)
		}
		if b.kind == Text {
			b = applyNumericAffinity(b, false)
		}
	case AffText:
		if a.kind == Int {
			a = TextValue(strconv.FormatInt(a.intVal, 10))
		} else if a.kind == Float {
			a = TextValue(formatFloatSQLite(a.floatVal))
		}
		if b.kind == Int {
			b = TextValue(strconv.FormatInt(b.intVal, 10))
		} else if b.kind == Float {
			b = TextValue(formatFloatSQLite(b.floatVal))
		}
	}
	return a, b
}

// parseExpr parses a full expression with SQLite's operator precedence
// (lowest to highest): OR, AND, NOT, comparison, additive, multiplicative,
// unary, primary.
func (p *parser) parseExpr() (Expr, error) {
	return p.parseOr()
}

func (p *parser) parseOr() (Expr, error) {
	left, err := p.parseAnd()
	if err != nil {
		return nil, err
	}
	for p.isKeyword("OR") {
		p.next()
		right, err := p.parseAnd()
		if err != nil {
			return nil, err
		}
		left = &LogicalExpr{op: "OR", left: left, right: right}
	}
	return left, nil
}

func (p *parser) parseAnd() (Expr, error) {
	left, err := p.parseNot()
	if err != nil {
		return nil, err
	}
	for p.isKeyword("AND") {
		p.next()
		right, err := p.parseNot()
		if err != nil {
			return nil, err
		}
		left = &LogicalExpr{op: "AND", left: left, right: right}
	}
	return left, nil
}

func (p *parser) parseNot() (Expr, error) {
	if p.isKeyword("NOT") {
		p.next()
		inner, err := p.parseNot()
		if err != nil {
			return nil, err
		}
		return &NotExpr{inner: inner}, nil
	}
	return p.parseComparison()
}

func (p *parser) parseComparison() (Expr, error) {
	left, err := p.parseAdditive()
	if err != nil {
		return nil, err
	}
	for {
		t := p.peek()
		if t.kind == tokOp && isCompareOp(t.text) {
			p.next()
			right, err := p.parseAdditive()
			if err != nil {
				return nil, err
			}
			left = &CompareExpr{op: t.text, left: left, right: right}
			continue
		}
		if t.kind == tokIdent {
			kw := strings.ToUpper(t.text)
			switch kw {
			case "IS":
				p.next()
				neg := false
				if p.isKeyword("NOT") {
					p.next()
					neg = true
				}
				right, err := p.parseAdditive()
				if err != nil {
					return nil, err
				}
				op := "IS"
				if neg {
					op = "IS NOT"
				}
				left = &CompareExpr{op: op, nullSafe: true, left: left, right: right}
				continue
			case "NOT":
				// NOT IN / NOT BETWEEN / NOT LIKE / NOT GLOB.
				p.next()
				switch {
				case p.isKeyword("IN"):
					p.next()
					item, err := p.parseInList()
					if err != nil {
						return nil, err
					}
					left = &InExpr{negate: true, left: left, list: item}
				case p.isKeyword("BETWEEN"):
					p.next()
					low, err := p.parseAdditive()
					if err != nil {
						return nil, err
					}
					if !p.isKeyword("AND") {
						return nil, &SQLError{Message: "expected AND in BETWEEN"}
					}
					p.next()
					high, err := p.parseAdditive()
					if err != nil {
						return nil, err
					}
					left = &BetweenExpr{negate: true, left: left, low: low, high: high}
				case p.isKeyword("LIKE"), p.isKeyword("GLOB"):
					glob := p.isKeyword("GLOB")
					p.next()
					pat, err := p.parseAdditive()
					if err != nil {
						return nil, err
					}
					var esc Expr
					if p.isKeyword("ESCAPE") {
						p.next()
						esc, err = p.parseAdditive()
						if err != nil {
							return nil, err
						}
					}
					left = &LikeExpr{glob: glob, negate: true, left: left, pattern: pat, escape: esc}
				default:
					return nil, &SQLError{Message: "syntax error near NOT"}
				}
				continue
			case "IN":
				p.next()
				item, err := p.parseInList()
				if err != nil {
					return nil, err
				}
				left = &InExpr{left: left, list: item}
				continue
			case "BETWEEN":
				p.next()
				low, err := p.parseAdditive()
				if err != nil {
					return nil, err
				}
				if !p.isKeyword("AND") {
					return nil, &SQLError{Message: "expected AND in BETWEEN"}
				}
				p.next()
				high, err := p.parseAdditive()
				if err != nil {
					return nil, err
				}
				left = &BetweenExpr{left: left, low: low, high: high}
				continue
			case "LIKE", "GLOB":
				p.next()
				glob := kw == "GLOB"
				pat, err := p.parseAdditive()
				if err != nil {
					return nil, err
				}
				var esc Expr
				if p.isKeyword("ESCAPE") {
					p.next()
					esc, err = p.parseAdditive()
					if err != nil {
						return nil, err
					}
				}
				left = &LikeExpr{glob: glob, left: left, pattern: pat, escape: esc}
				continue
			}
		}
		break
	}
	return left, nil
}

// parseInList parses "(expr, expr, ...)" after the IN keyword.
func (p *parser) parseInList() ([]Expr, error) {
	if err := p.expectPunct("("); err != nil {
		return nil, err
	}
	var list []Expr
	for {
		item, err := p.parseExpr()
		if err != nil {
			return nil, err
		}
		list = append(list, item)
		if p.peek().text == "," {
			p.next()
			continue
		}
		break
	}
	if err := p.expectPunct(")"); err != nil {
		return nil, err
	}
	return list, nil
}

func (p *parser) parseAdditive() (Expr, error) {
	left, err := p.parseMultiplicative()
	if err != nil {
		return nil, err
	}
	for {
		t := p.peek()
		if t.kind == tokOp && (t.text == "+" || t.text == "-") {
			p.next()
			right, err := p.parseMultiplicative()
			if err != nil {
				return nil, err
			}
			left = &ArithExpr{op: t.text, left: left, right: right}
			continue
		}
		break
	}
	return left, nil
}

func (p *parser) parseMultiplicative() (Expr, error) {
	left, err := p.parseUnary()
	if err != nil {
		return nil, err
	}
	for {
		t := p.peek()
		if t.kind == tokOp && (t.text == "*" || t.text == "/" || t.text == "%") {
			p.next()
			right, err := p.parseUnary()
			if err != nil {
				return nil, err
			}
			left = &ArithExpr{op: t.text, left: left, right: right}
			continue
		}
		break
	}
	return left, nil
}

func (p *parser) parseUnary() (Expr, error) {
	t := p.peek()
	if t.kind == tokOp && (t.text == "-" || t.text == "+") {
		p.next()
		inner, err := p.parseUnary()
		if err != nil {
			return nil, err
		}
		if t.text == "-" {
			// -9223372036854775808 is the smallest int64, not a real.
			if lit, ok := inner.(*LiteralExpr); ok && lit.bigInt != "" {
				return &LiteralExpr{val: IntValue(math.MinInt64)}, nil
			}
			return &NegExpr{inner: inner}, nil
		}
		return inner, nil // unary plus is a no-op
	}
	return p.parsePrimary()
}

func (p *parser) parsePrimary() (Expr, error) {
	t := p.peek()
	switch t.kind {
	case tokNumber:
		p.next()
		if isIntLiteral(t.text) {
			n, err := strconv.ParseInt(t.text, 10, 64)
			if err != nil {
				// Overflow: 2^63 is special (it negates to MinInt64);
				// any other overflow becomes a real.
				if t.text == "9223372036854775808" {
					return &LiteralExpr{val: FloatValue(9.223372036854776e18), bigInt: t.text}, nil
				}
				f, ferr := strconv.ParseFloat(t.text, 64)
				if ferr != nil {
					return nil, &SQLError{Message: "invalid integer " + t.text}
				}
				return &LiteralExpr{val: FloatValue(f)}, nil
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
	case tokBlob:
		p.next()
		return &LiteralExpr{val: BlobValue(t.text)}, nil
	case tokIdent:
		kw := strings.ToUpper(t.text)
		switch kw {
		case "NULL":
			p.next()
			return &LiteralExpr{val: NullValue()}, nil
		case "TRUE":
			p.next()
			return &LiteralExpr{val: IntValue(1)}, nil
		case "FALSE":
			p.next()
			return &LiteralExpr{val: IntValue(0)}, nil
		case "CAST":
			p.next()
			if err := p.expectPunct("("); err != nil {
				return nil, err
			}
			inner, err := p.parseExpr()
			if err != nil {
				return nil, err
			}
			if !p.isKeyword("AS") {
				return nil, &SQLError{Message: "expected AS in CAST"}
			}
			p.next()
			typeName, err := p.expectIdent()
			if err != nil {
				return nil, err
			}
			// Optional "(n)" size suffix, e.g. CAST(x AS VARCHAR(10)).
			if p.peek().text == "(" {
				p.next()
				if p.peek().kind != tokNumber {
					return nil, &SQLError{Message: "expected size in CAST type"}
				}
				p.next()
				if err := p.expectPunct(")"); err != nil {
					return nil, err
				}
			}
			if err := p.expectPunct(")"); err != nil {
				return nil, err
			}
			return &CastExpr{inner: inner, aff: castAffinity(typeName)}, nil
		case "COUNT":
			p.next()
			if p.peek().text == "(" {
				p.next()
				if p.peek().text == "*" {
					p.next()
					if err := p.expectPunct(")"); err != nil {
						return nil, err
					}
					return &CountStarExpr{}, nil
				}
			}
			return nil, &UnsupportedError{Feature: "aggregate functions other than COUNT(*)"}
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
			p.next()
			if p.isKeyword("SELECT") {
				return nil, &UnsupportedError{Feature: "subquery"}
			}
			inner, err := p.parseExpr()
			if err != nil {
				return nil, err
			}
			if err := p.expectPunct(")"); err != nil {
				return nil, err
			}
			return inner, nil
		case ".":
			return nil, &UnsupportedError{Feature: "qualified column reference"}
		}
	}
	return nil, &SQLError{Message: "expected a value, got " + t.text}
}

// castAffinity maps a CAST type name to an affinity, matching
// sqlite3AffinityType for the CAST context.
func castAffinity(typeName string) Affinity {
	aff := columnAffinity(typeName)
	if aff == AffInteger {
		return AffInteger
	}
	if aff == AffReal {
		return AffReal
	}
	if aff == AffText {
		return AffText
	}
	if aff == AffBlob {
		return AffBlob
	}
	return AffNumeric
}

func (p *parser) isKeyword(kw string) bool {
	t := p.peek()
	return t.kind == tokIdent && strings.EqualFold(t.text, kw)
}

func isCompareOp(op string) bool {
	switch op {
	case "=", "!=", "<>", "<", ">", "<=", ">=":
		return true
	}
	return false
}

// resolveColumnAffinity walks an expression tree and fills in the affinity of
// every ColumnExpr from the statement's table schema. It is called after
// parsing, once the target table is known, and reports unknown columns the
// way SQLite does at prepare time (regardless of row count).
func resolveColumnAffinity(e Expr, cols []Column) error {
	switch n := e.(type) {
	case *ColumnExpr:
		for _, c := range cols {
			if strings.EqualFold(c.Name, n.name) {
				n.aff = columnAffinity(c.Type)
				return nil
			}
		}
		return &SQLError{Message: fmt.Sprintf("no such column: %s", n.name)}
	case *CastExpr:
		return resolveColumnAffinity(n.inner, cols)
	case *ArithExpr:
		if err := resolveColumnAffinity(n.left, cols); err != nil {
			return err
		}
		return resolveColumnAffinity(n.right, cols)
	case *NegExpr:
		return resolveColumnAffinity(n.inner, cols)
	case *CompareExpr:
		if err := resolveColumnAffinity(n.left, cols); err != nil {
			return err
		}
		return resolveColumnAffinity(n.right, cols)
	case *LogicalExpr:
		if err := resolveColumnAffinity(n.left, cols); err != nil {
			return err
		}
		return resolveColumnAffinity(n.right, cols)
	case *NotExpr:
		return resolveColumnAffinity(n.inner, cols)
	case *InExpr:
		if err := resolveColumnAffinity(n.left, cols); err != nil {
			return err
		}
		for _, item := range n.list {
			if err := resolveColumnAffinity(item, cols); err != nil {
				return err
			}
		}
	case *BetweenExpr:
		if err := resolveColumnAffinity(n.left, cols); err != nil {
			return err
		}
		if err := resolveColumnAffinity(n.low, cols); err != nil {
			return err
		}
		return resolveColumnAffinity(n.high, cols)
	case *LikeExpr:
		if err := resolveColumnAffinity(n.left, cols); err != nil {
			return err
		}
		if err := resolveColumnAffinity(n.pattern, cols); err != nil {
			return err
		}
		if n.escape != nil {
			return resolveColumnAffinity(n.escape, cols)
		}
	}
	return nil
}
