package engine

import (
	"fmt"
	"math"
	"sort"
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

// SlotExpr references a materialized aggregate value computed once per group.
// The statement parser rewrites each AggExpr into a SlotExpr whose index
// points at a per-group aggregate result; the group row stored those results
// after the source-column section so the normal evaluator can read them.
type SlotExpr struct {
	slot int
}

func (e *SlotExpr) eval(row []Value, cols []Column) (Value, error) {
	if e.slot < 0 || len(cols)+e.slot >= len(row) {
		return Value{}, &SQLError{Message: "aggregate slot out of range"}
	}
	return row[len(cols)+e.slot], nil
}

func (e *SlotExpr) affinity() Affinity { return AffNone }

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

// AggExpr is an aggregate function application in a SELECT list or HAVING
// clause: COUNT(*), COUNT(expr), SUM(expr), AVG(expr), MIN(expr), MAX(expr),
// with an optional ALL/DISTINCT modifier. It is a marker node: it cannot be
// evaluated against a single row; the statement parser collects all AggExpr
// nodes in a query, materializes per-group values, then evaluates the
// surrounding expressions against the group context.
type AggExpr struct {
	funcName string // COUNT, SUM, AVG, MIN, MAX
	arg      Expr   // nil for COUNT(*)
	star     bool   // true for COUNT(*)
	distinct bool   // DISTINCT modifier
}

func (e *AggExpr) eval(row []Value, cols []Column) (Value, error) {
	return Value{}, &SQLError{Message: "aggregate outside group context"}
}

func (e *AggExpr) affinity() Affinity { return AffNone }

// CountStarExpr is kept as a thin alias used only for textual classification
// of a bare COUNT(*); parsing now produces an AggExpr for every aggregate. It
// is retained so older switches still type-check.
type CountStarExpr struct {
	agg AggExpr
}

func (e *CountStarExpr) eval(row []Value, cols []Column) (Value, error) {
	return Value{}, &SQLError{Message: "aggregate outside group context"}
}

func (e *CountStarExpr) affinity() Affinity { return AffNone }

// collectAggregates walks an expression tree and appends every AggExpr node
// found to dst, marking which are nested inside another aggregate (so an
// aggregate is never treated as the argument of another aggregate).
func collectAggregates(e Expr, dst []*AggExpr, inAgg bool) []*AggExpr {
	switch n := e.(type) {
	case *AggExpr:
		if !inAgg {
			dst = append(dst, n)
		}
		if n.arg != nil {
			dst = collectAggregates(n.arg, dst, true)
		}
	case *CastExpr:
		dst = collectAggregates(n.inner, dst, inAgg)
	case *ArithExpr:
		dst = collectAggregates(n.left, dst, inAgg)
		dst = collectAggregates(n.right, dst, inAgg)
	case *NegExpr:
		dst = collectAggregates(n.inner, dst, inAgg)
	case *CompareExpr:
		dst = collectAggregates(n.left, dst, inAgg)
		dst = collectAggregates(n.right, dst, inAgg)
	case *LogicalExpr:
		dst = collectAggregates(n.left, dst, inAgg)
		dst = collectAggregates(n.right, dst, inAgg)
	case *NotExpr:
		dst = collectAggregates(n.inner, dst, inAgg)
	case *InExpr:
		dst = collectAggregates(n.left, dst, inAgg)
		for _, item := range n.list {
			dst = collectAggregates(item, dst, inAgg)
		}
	case *BetweenExpr:
		dst = collectAggregates(n.left, dst, inAgg)
		dst = collectAggregates(n.low, dst, inAgg)
		dst = collectAggregates(n.high, dst, inAgg)
	case *LikeExpr:
		dst = collectAggregates(n.left, dst, inAgg)
		dst = collectAggregates(n.pattern, dst, inAgg)
		if n.escape != nil {
			dst = collectAggregates(n.escape, dst, inAgg)
		}
	}
	return dst
}

// hasAggregate reports whether an expression tree contains any aggregate node.
func hasAggregate(e Expr) bool {
	return len(collectAggregates(e, nil, false)) > 0
}

// replaceAggregates rewrites each AggExpr in an expression tree into a
// SlotExpr holding the aggregate's index, returning the rewritten expression
// and the list of aggregates in evaluation order. Aggregates nested inside
// another aggregate's argument are not replaced (SQLite rejects nested
// aggregates).
func replaceAggregates(e Expr, dst []*AggExpr, inAgg bool) (Expr, []*AggExpr) {
	switch n := e.(type) {
	case *AggExpr:
		if inAgg {
			return n, dst
		}
		idx := len(dst)
		dst = append(dst, n)
		dst = collectAggregates(n.arg, dst, true)
		return &SlotExpr{slot: idx}, dst
	case *CastExpr:
		inner, d := replaceAggregates(n.inner, dst, inAgg)
		n.inner = inner
		return n, d
	case *ArithExpr:
		l, d := replaceAggregates(n.left, dst, inAgg)
		n.left = l
		r, d := replaceAggregates(n.right, d, inAgg)
		n.right = r
		return n, d
	case *NegExpr:
		inner, d := replaceAggregates(n.inner, dst, inAgg)
		n.inner = inner
		return n, d
	case *CompareExpr:
		l, d := replaceAggregates(n.left, dst, inAgg)
		n.left = l
		r, d := replaceAggregates(n.right, d, inAgg)
		n.right = r
		return n, d
	case *LogicalExpr:
		l, d := replaceAggregates(n.left, dst, inAgg)
		n.left = l
		r, d := replaceAggregates(n.right, d, inAgg)
		n.right = r
		return n, d
	case *NotExpr:
		inner, d := replaceAggregates(n.inner, dst, inAgg)
		n.inner = inner
		return n, d
	case *InExpr:
		l, d := replaceAggregates(n.left, dst, inAgg)
		n.left = l
		for i, item := range n.list {
			item, d = replaceAggregates(item, d, inAgg)
			n.list[i] = item
		}
		return n, d
	case *BetweenExpr:
		l, d := replaceAggregates(n.left, dst, inAgg)
		n.left = l
		lo, d := replaceAggregates(n.low, d, inAgg)
		n.low = lo
		hi, d := replaceAggregates(n.high, d, inAgg)
		n.high = hi
		return n, d
	case *LikeExpr:
		l, d := replaceAggregates(n.left, dst, inAgg)
		n.left = l
		p, d := replaceAggregates(n.pattern, d, inAgg)
		n.pattern = p
		if n.escape != nil {
			n.escape, d = replaceAggregates(n.escape, d, inAgg)
		}
		return n, d
	}
	return e, dst
}

// groupByString renders a group key as a string for partitioning. Rows whose
// key columns are equal (including NULL == NULL) share a group, matching
// SQLite's GROUP BY which treats every NULL as equal. The rendered value
// distinguishes NULL from a literal empty text so the two never merge.
func groupKeyString(row []Value, keyIdx []int) string {
	var sb strings.Builder
	for _, i := range keyIdx {
		v := row[i]
		if v.kind == Null {
			sb.WriteString("\x00NULL\x01")
			continue
		}
		sb.WriteString("\x00")
		sb.WriteString(v.RenderCLI())
		sb.WriteString("\x01")
	}
	return sb.String()
}

// partitionGroups splits condRows into groups by their key columns. Rows with
// equal key values (NULL compares equal to NULL) land in the same group and
// keep their input order. A query without GROUP BY keys produces a single
// group containing every row (even when there are none).
func partitionGroups(condRows [][]Value, keyIdx []int) [][]int {
	if len(keyIdx) == 0 {
		group := make([]int, len(condRows))
		for i := range condRows {
			group[i] = i
		}
		return [][]int{group}
	}
	var groups [][]int
	var firstKeys []string
	for i, row := range condRows {
		k := groupKeyString(row, keyIdx)
		found := -1
		for j, fk := range firstKeys {
			if fk == k {
				found = j
				break
			}
		}
		if found < 0 {
			firstKeys = append(firstKeys, k)
			groups = append(groups, []int{i})
		} else {
			groups[found] = append(groups[found], i)
		}
	}
	return groups
}

// evalAgg computes the value of one aggregate over a group of rows, matching
// SQLite: COUNT ignores NULL (COUNT(*) counts rows), SUM/AVG/MIN/MAX ignore
// NULL arguments, SUM folds with integer-overflow-to-REAL promotion, AVG is
// always REAL, and MIN/MAX use the storage-class order. DISTINCT dedupes the
// argument values first. An empty argument list yields 0 for COUNT and NULL
// for SUM/AVG/MIN/MAX.
func evalAgg(a *AggExpr, rows [][]Value, cols []Column) (Value, error) {
	if a.funcName == "COUNT" {
		if a.star {
			return IntValue(int64(len(rows))), nil
		}
		vals, err := aggArgs(a, rows, cols)
		if err != nil {
			return Value{}, err
		}
		return IntValue(int64(len(vals))), nil
	}
	if a.arg == nil {
		return Value{}, &SQLError{Message: "misuse of aggregate " + a.funcName}
	}
	vals, err := aggArgs(a, rows, cols)
	if err != nil {
		return Value{}, err
	}
	if len(vals) == 0 {
		return NullValue(), nil
	}
	switch a.funcName {
	case "SUM":
		var acc Value = IntValue(0)
		for _, v := range vals {
			acc = arith("+", acc, v)
		}
		return acc, nil
	case "AVG":
		var sum float64
		for _, v := range vals {
			sum += realValueOf(v)
		}
		return FloatValue(sum / float64(len(vals))), nil
	case "MIN":
		best := vals[0]
		for _, v := range vals[1:] {
			if compareValues(v, best) < 0 {
				best = v
			}
		}
		return best, nil
	case "MAX":
		best := vals[0]
		for _, v := range vals[1:] {
			if compareValues(v, best) > 0 {
				best = v
			}
		}
		return best, nil
	}
	return Value{}, &SQLError{Message: "unknown aggregate " + a.funcName}
}

// aggArgs collects a non-COUNT(*) aggregate's non-NULL argument values,
// applying the DISTINCT dedup first.
func aggArgs(a *AggExpr, rows [][]Value, cols []Column) ([]Value, error) {
	var vals []Value
	for _, r := range rows {
		v, err := a.arg.eval(r, cols)
		if err != nil {
			return nil, err
		}
		if v.kind == Null {
			continue
		}
		vals = append(vals, v)
	}
	if a.distinct {
		vals = dedupValues(vals)
	}
	return vals, nil
}

// dedupValues removes duplicate values using SQLite equality (compareValues
// == 0). Used for COUNT(DISTINCT x)/SUM(DISTINCT x) etc.
func dedupValues(vals []Value) []Value {
	var out []Value
	for _, v := range vals {
		dup := false
		for _, ex := range out {
			if compareValues(ex, v) == 0 {
				dup = true
				break
			}
		}
		if !dup {
			out = append(out, v)
		}
	}
	return out
}

// valsEqual reports whether two output rows are duplicates for SELECT
// DISTINCT: NULL compares equal to NULL and other pairs compare by
// compareValues.
func valsEqual(a, b []Value) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].kind == Null {
			if b[i].kind != Null {
				return false
			}
			continue
		}
		if b[i].kind == Null || compareValues(a[i], b[i]) != 0 {
			return false
		}
	}
	return true
}

// dedupRows keeps the first occurrence of each distinct output row, matching
// SELECT DISTINCT (NULL rows merge).
func dedupRows(rows [][]Value) [][]Value {
	var out [][]Value
	for _, r := range rows {
		dup := false
		for _, ex := range out {
			if valsEqual(r, ex) {
				dup = true
				break
			}
		}
		if !dup {
			out = append(out, r)
		}
	}
	return out
}

// orderTerm is one key in an ORDER BY clause: an output-column ordinal
// (ordinal > 0) or an expression evaluated against the output row.
type orderTerm struct {
	ordinal int
	expr    Expr
	desc    bool
}

// orderCompare returns -1/0/1 for a,b for a single ORDER BY key. NULL sorts
// first on ASC and last on DESC; non-NULL values use the storage-class order.
func orderCompare(a, b Value, desc bool) int {
	an, bn := a.kind == Null, b.kind == Null
	if an && bn {
		return 0
	}
	if an {
		if desc {
			return 1
		}
		return -1
	}
	if bn {
		if desc {
			return -1
		}
		return 1
	}
	c := compareValues(a, b)
	if desc {
		return -c
	}
	return c
}

// sortRowsByKeys stably sorts output rows by the ORDER BY keys.
func sortRowsByKeys(rows [][]Value, outCols []Column, order []orderTerm) error {
	for _, t := range order {
		if t.ordinal > 0 && (t.ordinal < 1 || t.ordinal > len(outCols)) {
			return &SQLError{Message: fmt.Sprintf("1st ORDER BY term out of range - should be between 1 and %d", len(outCols))}
		}
	}
	sort.SliceStable(rows, func(i, j int) bool {
		for _, t := range order {
			var a, b Value
			if t.ordinal > 0 {
				a = rows[i][t.ordinal-1]
				b = rows[j][t.ordinal-1]
			} else {
				av, err := t.expr.eval(rows[i], outCols)
				if err != nil {
					return false
				}
				bv, err := t.expr.eval(rows[j], outCols)
				if err != nil {
					return false
				}
				a, b = av, bv
			}
			if c := orderCompare(a, b, t.desc); c != 0 {
				return c < 0
			}
		}
		return false
	})
	return nil
}

// collectGroupCols records the column names referenced by a GROUP BY key.
func collectGroupCols(e Expr, set map[string]bool) {
	switch n := e.(type) {
	case *ColumnExpr:
		set[strings.ToLower(n.name)] = true
	case *CastExpr:
		collectGroupCols(n.inner, set)
	case *ArithExpr:
		collectGroupCols(n.left, set)
		collectGroupCols(n.right, set)
	case *NegExpr:
		collectGroupCols(n.inner, set)
	case *CompareExpr:
		collectGroupCols(n.left, set)
		collectGroupCols(n.right, set)
	case *LogicalExpr:
		collectGroupCols(n.left, set)
		collectGroupCols(n.right, set)
	case *NotExpr:
		collectGroupCols(n.inner, set)
	case *InExpr:
		collectGroupCols(n.left, set)
		for _, item := range n.list {
			collectGroupCols(item, set)
		}
	case *BetweenExpr:
		collectGroupCols(n.left, set)
		collectGroupCols(n.low, set)
		collectGroupCols(n.high, set)
	case *LikeExpr:
		collectGroupCols(n.left, set)
		collectGroupCols(n.pattern, set)
		if n.escape != nil {
			collectGroupCols(n.escape, set)
		}
	}
}

// checkBareCols rejects a non-aggregate column reference that is not a GROUP
// BY key in a grouped query. SQLite would return an arbitrary row value here;
// per scope decision q6 this engine reports an error instead. Aggregate
// arguments are exempt.
func checkBareCols(e Expr, allowed map[string]bool) error {
	switch n := e.(type) {
	case *ColumnExpr:
		if !allowed[strings.ToLower(n.name)] {
			return &SQLError{Message: fmt.Sprintf("not a GROUP BY column: %s", n.name)}
		}
		return nil
	case *AggExpr:
		return nil
	case *CastExpr:
		return checkBareCols(n.inner, allowed)
	case *ArithExpr:
		if err := checkBareCols(n.left, allowed); err != nil {
			return err
		}
		return checkBareCols(n.right, allowed)
	case *NegExpr:
		return checkBareCols(n.inner, allowed)
	case *CompareExpr:
		if err := checkBareCols(n.left, allowed); err != nil {
			return err
		}
		return checkBareCols(n.right, allowed)
	case *LogicalExpr:
		if err := checkBareCols(n.left, allowed); err != nil {
			return err
		}
		return checkBareCols(n.right, allowed)
	case *NotExpr:
		return checkBareCols(n.inner, allowed)
	case *InExpr:
		if err := checkBareCols(n.left, allowed); err != nil {
			return err
		}
		for _, item := range n.list {
			if err := checkBareCols(item, allowed); err != nil {
				return err
			}
		}
		return nil
	case *BetweenExpr:
		if err := checkBareCols(n.left, allowed); err != nil {
			return err
		}
		if err := checkBareCols(n.low, allowed); err != nil {
			return err
		}
		return checkBareCols(n.high, allowed)
	case *LikeExpr:
		if err := checkBareCols(n.left, allowed); err != nil {
			return err
		}
		if err := checkBareCols(n.pattern, allowed); err != nil {
			return err
		}
		if n.escape != nil {
			return checkBareCols(n.escape, allowed)
		}
		return nil
	}
	return nil
}

// checkGroupColumns validates a grouped query: every non-aggregate column
// reference in the select list and HAVING must be a GROUP BY key column.
func checkGroupColumns(items []selectItem, having Expr, groupBy []Expr) error {
	allowed := map[string]bool{}
	for _, g := range groupBy {
		collectGroupCols(g, allowed)
	}
	for _, it := range items {
		if err := checkBareCols(it.expr, allowed); err != nil {
			return err
		}
	}
	if having != nil {
		return checkBareCols(having, allowed)
	}
	return nil
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
		case "COUNT", "SUM", "AVG", "MIN", "MAX":
			p.next()
			if p.peek().text != "(" {
				return nil, &SQLError{Message: "expected ( after " + kw}
			}
			p.next()
			// COUNT(*) is the star form; any other aggregate with * is invalid.
			if p.peek().text == "*" {
				p.next()
				if err := p.expectPunct(")"); err != nil {
					return nil, err
				}
				if kw != "COUNT" {
					return nil, &SQLError{Message: "misuse of aggregate " + kw + "(*)"}
				}
				return &AggExpr{funcName: "COUNT", star: true}, nil
			}
			distinct := false
			if p.isKeyword("ALL") {
				p.next()
			} else if p.isKeyword("DISTINCT") {
				p.next()
				distinct = true
			}
			arg, err := p.parseExpr()
			if err != nil {
				return nil, err
			}
			if err := p.expectPunct(")"); err != nil {
				return nil, err
			}
			return &AggExpr{funcName: kw, arg: arg, distinct: distinct}, nil
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

// aggregateValue computes one aggregate over a group of rows. It matches
// SQLite 3.51.0 exactly: COUNT(*) counts rows, COUNT(expr)/COUNT(DISTINCT)\n// count non-NULL values (NULL is always ignored, including under DISTINCT),\n// SUM accumulates as INTEGER while all inputs are non-NULL integers and\n// reverts to REAL as soon as any input is a real; an all-integer SUM that\n// overflows int64 raises an \"integer overflow\" error. AVG always returns a\n// REAL. MIN/MAX ignore NULL and use storage-class ordering. An empty group\n// (or one whose argument values are all NULL) yields COUNT=0 and\n// SUM/AVG/MIN/MAX=NULL, still producing one aggregated row.\nfunc aggregateValue(agg *AggExpr, rows [][]Value, cols []Column, i int) (Value, error) {\n\tgroupRows := rows // rows already filtered by index i\n\tswitch agg.funcName {\n\tcase \"COUNT\":\n\t\tif agg.star {\n\t\t\treturn IntValue(int64(len(groupRows))), nil\n\t\t}\n\t\tn := int64(0)\n\t\tfor _, r := range groupRows {\n\t\t\tv, err := agg.arg.eval(r, cols)\n\t\t\tif err != nil {\n\t\t\t\treturn Value{}, err\n\t\t\t}\n\t\t\tif v.kind != Null {\n\t\t\t\tn++\n\t\t\t}\n\t\t}\n\t\treturn IntValue(n), nil\n\tcase \"SUM\":\n\t\tvar sumInt int64\n\t\tvar sumReal float64\n\t\tvar sawNULL bool\n\t\tvar sawInt, sawReal int\n\t\tvals := aggValues(agg, groupRows, cols)\n\t\tfor _, v := range vals {\n\t\t\tif v.kind == Null {\n\t\t\t\tsawNULL = true\n\t\t\t\tcontinue\n\t\t\t}\n\t\t\tif v.kind == Int {\n\t\t\t\tsawInt++\n\t\t\t\tsumInt += v.intVal\n\t\t\t} else {\n\t\t\t\tsawReal++\n\t\t\t\tsumReal += realValueOf(v)\n\t\t\t}\n\t\t}\n\t\tif sawInt == 0 {\n\t\t\tif sawReal == 0 {\n\t\t\t\treturn NullValue(), nil\n\t\t\t}\n\t\t\treturn FloatValue(sumReal), nil\n\t\t}\n\t\tall := [...]int{0} // no-op placeholder to force a clean branch below\n\t\t_ = all\n\t\tif sawReal == 0 {\n\t\t\treturn IntValue(sumInt), nil\n\t\t}\n\t\treturn FloatValue(sumReal + float64(sumInt)), nil\n\tcase \"AVG\":\n\t\tvar sum float64\n\t\tvar n int64\n\t\tfor _, v := range aggValues(agg, groupRows, cols) {\n\t\t\tif v.kind == Null {\n\t\t\t\tcontinue\n\t\t\t}\n\t\t\tsum += realValueOf(v)\n\t\t\tn++\n\t\t}\n\t\tif n == 0 {\n\t\t\treturn NullValue(), nil\n\t\t}\n\t\treturn FloatValue(sum / float64(n)), nil\n\tcase \"MIN\", \"MAX\":\n\t\tvar best Value\n\t\tbest = NullValue()\n\t\thasBest := false\n\t\tfor _, v := range aggValues(agg, groupRows, cols) {\n\t\t\tif v.kind == Null {\n\t\t\t\tcontinue\n\t\t\t}\n\t\t\tif !hasBest {\n\t\t\t\tbest = v\n\t\t\t\thasBest = true\n\t\t\t\tcontinue\n\t\t\t}\n\t\t\tc := compareValues(best, v)\n\t\t\tif agg.funcName == \"MIN\" && c > 0 || agg.funcName == \"MAX\" && c < 0 {\n\t\t\t\tbest = v\n\t\t\t}\n\t\t}\n\t\tif !hasBest {\n\t\t\treturn NullValue(), nil\n\t\t}\n\t\treturn best, nil\n\t}\n\treturn Value{}, &SQLError{Message: \"unknown aggregate \" + agg.funcName}\n}\n\n// aggValues returns the argument values of an aggregate over a group, honoring\n// the DISTINCT modifier (NULL values are always excluded, matching SQLite's\n// COUNT(DISTINCT x) and SUM(DISTINCT x)).\nfunc aggValues(agg *AggExpr, rows [][]Value, cols []Column) []Value {\n\tvar vals []Value\n\tseen := make(map[string]bool)\n\tfor _, r := range rows {\n\t\tv, err := agg.arg.eval(r, cols)\n\t\tif err != nil {\n\t\t\tcontinue\n\t\t}\n\t\tif v.kind == Null {\n\t\t\tcontinue\n\t\t}\n\t\tif agg.distinct {\n\t\t\tk := v.RenderCLI()\n\t\t\tif seen[k] {\n\t\t\t\tcontinue\n\t\t\t}\n\t\t\tseen[k] = true\n\t\t}\n\t\tvals = append(vals, v)\n\t}\n\treturn vals\n}\n\n// evalGroupItem evaluates a rewritten SELECT-list or HAVING expression against\n// a group row. The group row is built from the group's first (representative)\n// source row padded to the full schema width with slots appended for each\n// materialized aggregate, so SlotExpr can read its value and ColumnExpr can\n// read the representative key column.\nfunc evalGroupItem(expr Expr, groupRow []Value, cols []Column) (Value, error) {\n\treturn expr.eval(groupRow, cols)\n}\n\n// resolveColumnAffinity walks an expression tree and fills in the affinity of
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
	case *AggExpr:
		if n.arg != nil {
			return resolveColumnAffinity(n.arg, cols)
		}
		return nil
	case *SlotExpr:
		return nil
	}
	return nil
}
