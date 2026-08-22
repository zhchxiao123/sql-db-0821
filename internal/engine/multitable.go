package engine

import (
	"fmt"
	"strings"
)

// joinedFrom is the result of parsing a FROM clause: a combined column list
// (each column tagged with the source table or alias it belongs to, so
// qualified references resolve) plus the joined rows. For a single table it is
// just that table's columns and rows.
type joinedFrom struct {
	cols []Column
	rows [][]Value
}

// joinKind selects how two FROM sources are combined.
type joinKind int

const (
	joinCross joinKind = iota // comma, CROSS JOIN, bare JOIN (no ON/USING)
	joinInner                 // [INNER] JOIN with ON/USING
	joinLeft                  // LEFT [OUTER] JOIN with ON/USING
)

// parseFrom parses "FROM {table | (SELECT ...)} [, source | join]*" and returns
// the combined column/row space. Comma-separated sources form a cross product;
// JOIN sources combine with ON/USING or as a cross product when no condition
// is given. A derived table in parentheses is evaluated eagerly (it cannot be
// correlated) and contributes its columns.
func (e *Engine) parseFrom(p *parser) (*joinedFrom, error) {
	cur, err := e.parseFromSource(p)
	if err != nil {
		return nil, err
	}
	for {
		t := p.peek()
		if t.kind == tokPunct && t.text == "," {
			p.next()
			src, err := e.parseFromSource(p)
			if err != nil {
				return nil, err
			}
			cur = e.crossJoin(cur, src)
			continue
		}
		natural, mode, ok := p.wantJoinHeader()
		if !ok {
			break
		}
		src, err := e.parseFromSource(p)
		if err != nil {
			return nil, err
		}
		var cond Expr
		var usingNames []string
		if p.isKeyword("ON") {
			p.next()
			cond, err = p.parseExpr()
			if err != nil {
				return nil, err
			}
		} else if p.isKeyword("USING") {
			p.next()
			if err := p.expectPunct("("); err != nil {
				return nil, err
			}
			for {
				u, err := p.expectIdent()
				if err != nil {
					return nil, err
				}
				usingNames = append(usingNames, u)
				if p.peek().text == "," {
					p.next()
					continue
				}
				break
			}
			if err := p.expectPunct(")"); err != nil {
				return nil, err
			}
		}
		if natural {
			// NATURAL uses the common column names as USING columns; with no
			// common columns it behaves as a cross product.
			common := commonColumnNames(cur.cols, src.cols)
			if len(common) == 0 {
				cur = e.crossJoin(cur, src)
			} else {
				cur = e.usingJoin(cur, src, common, mode)
			}
		} else if len(usingNames) > 0 {
			cur = e.usingJoin(cur, src, usingNames, mode)
		} else {
			cur, err = e.onJoin(cur, src, cond, mode)
			if err != nil {
				return nil, err
			}
		}
	}
	return cur, nil
}

// parseFromSource parses one FROM element: a table (with optional alias) or a
// derived-subquery "(" ... ")" (with optional alias). Every returned column is
// qualified with the source name so qualified references and qualified star
// expansion resolve correctly.
func (e *Engine) parseFromSource(p *parser) (*joinedFrom, error) {
	if p.peek().kind == tokPunct && p.peek().text == "(" {
		p.next()
		if !p.isKeyword("SELECT") {
			return nil, &SQLError{Message: "expected SELECT in FROM subquery"}
		}
		// Scan the balanced subquery range (SELECT ... ) and evaluate it
		// eagerly; a derived table cannot be correlated.
		a := p.pos
		b, err := p.scanClose(a)
		if err != nil {
			return nil, err
		}
		subToks := p.toks[a:b]
		res, err := e.runSub(subToks, nil, nil)
		if err != nil {
			return nil, err
		}
		// Skip the closing ')', which scanClose returned with depth<0 at ')'.
		p.pos = b + 1
		name := "subquery"
		if p.isKeyword("AS") {
			p.next()
			name, err = p.expectIdent()
			if err != nil {
				return nil, err
			}
		} else if t := p.peek(); t.kind == tokIdent && !selectStopKeywords[strings.ToUpper(t.text)] {
			p.next()
			name = t.text
		}
		cols := make([]Column, len(res.Columns))
		for i, n := range res.Columns {
			cols[i] = Column{Name: n, Qual: name}
		}
		return &joinedFrom{cols: cols, rows: res.Rows}, nil
	}
	name, err := p.expectIdent()
	if err != nil {
		return nil, err
	}
	tbl, ok := e.tables[name]
	if !ok {
		return nil, &SQLError{Message: fmt.Sprintf("no such table: %s", name)}
	}
	alias := name
	if p.isKeyword("AS") {
		p.next()
		alias, err = p.expectIdent()
		if err != nil {
			return nil, err
		}
	} else if t := p.peek(); t.kind == tokIdent && !selectStopKeywords[strings.ToUpper(t.text)] {
		p.next()
		alias = t.text
	}
	cols := make([]Column, len(tbl.Columns))
	for i, c := range tbl.Columns {
		cols[i] = Column{Name: c.Name, Type: c.Type, Qual: alias}
	}
	return &joinedFrom{cols: cols, rows: tbl.Rows}, nil
}

// wantJoinHeader recognizes the start of a JOIN clause (possibly NATURAL and/or
// LEFT/INNER/CROSS with an optional JOIN keyword) and consumes it, returning
// (natural, mode, true). If the next tokens do not form a join header it
// restores the parser position and returns ok=false without consuming.
func (p *parser) wantJoinHeader() (natural bool, mode joinKind, ok bool) {
	save := p.pos
	natural = false
	mode = joinCross
	if p.isKeyword("NATURAL") {
		p.next()
		natural = true
	}
	if p.isKeyword("LEFT") {
		p.next()
		mode = joinLeft
	} else if p.isKeyword("INNER") {
		p.next()
		mode = joinInner
	} else if p.isKeyword("CROSS") {
		p.next()
	}
	if mode == joinLeft && p.isKeyword("OUTER") {
		p.next()
	}
	if !p.isKeyword("JOIN") {
		p.pos = save
		return false, joinCross, false
	}
	p.next()
	return natural, mode, true
}

// scanClose returns the index of the ')' matching the balanced run that starts
// at token index a, counting '(' as +1 and ')' as -1. The first ')' that brings
// the depth below zero is the closer.
func (p *parser) scanClose(a int) (int, error) {
	depth := 0
	for i := a; i < len(p.toks); i++ {
		t := p.toks[i]
		if t.kind == tokEOF {
			return 0, &SQLError{Message: "unterminated subquery"}
		}
		if t.kind == tokPunct {
			switch t.text {
			case "(":
				depth++
			case ")":
				depth--
				if depth < 0 {
					return i, nil
				}
			}
		}
	}
	return 0, &SQLError{Message: "unterminated subquery"}
}

// crossJoin combines two sources into their cartesian product with every
// column of the left followed by every column of the right.
func (e *Engine) crossJoin(l, r *joinedFrom) *joinedFrom {
	cols := make([]Column, 0, len(l.cols)+len(r.cols))
	cols = append(cols, l.cols...)
	cols = append(cols, r.cols...)
	var rows [][]Value
	for _, lr := range l.rows {
		for _, rr := range r.rows {
			row := make([]Value, 0, len(lr)+len(rr))
			row = append(row, lr...)
			row = append(row, rr...)
			rows = append(rows, row)
		}
	}
	return &joinedFrom{cols: cols, rows: rows}
}

// onJoin combines two sources as a cross product, keeping the rows for which
// the ON condition is true. For joinLeft the unmatched left rows are kept with
// NULL padded across the right-hand columns. A nil condition (bare JOIN,
// CROSS JOIN) leaves the full cross product.
func (e *Engine) onJoin(l, r *joinedFrom, cond Expr, mode joinKind) (*joinedFrom, error) {
	cross := e.crossJoin(l, r)
	if cond == nil {
		return cross, nil
	}
	if err := resolveColumnAffinity(cond, cross.cols); err != nil {
		return nil, err
	}
	if mode != joinLeft {
		var rows [][]Value
		for _, row := range cross.rows {
			if condTrue(cond, row, cross.cols) {
				rows = append(rows, row)
			}
		}
		cross.rows = rows
		return cross, nil
	}
	var out [][]Value
	for _, lr := range l.rows {
		matched := false
		for _, rr := range r.rows {
			row := make([]Value, 0, len(lr)+len(rr))
			row = append(row, lr...)
			row = append(row, rr...)
			if condTrue(cond, row, cross.cols) {
				out = append(out, row)
				matched = true
			}
		}
		if !matched {
			pad := make([]Value, len(r.cols))
			row := make([]Value, 0, len(lr)+len(r.cols))
			row = append(row, lr...)
			row = append(row, pad...)
			out = append(out, row)
		}
	}
	cross.rows = out
	return cross, nil
}

// commonColumnNames returns the column names shared by two sources (case
// insensitive), in the order they appear in the left source.
func commonColumnNames(l, r []Column) []string {
	var out []string
	for _, lc := range l {
		if columnNamed(r, lc.Name) >= 0 && !containsNameFold(out, lc.Name) {
			out = append(out, lc.Name)
		}
	}
	return out
}

func columnNamed(cols []Column, name string) int {
	for i, c := range cols {
		if strings.EqualFold(c.Name, name) {
			return i
		}
	}
	return -1
}

func containsNameFold(list []string, name string) bool {
	for _, s := range list {
		if strings.EqualFold(s, name) {
			return true
		}
	}
	return false
}

// usingJoin combines two sources on shared USING columns. The merged column
// appears once (from the left side, qualified by the left source); the right
// side's copy of the shared column is omitted. Rows match when all USING
// columns are equal and non-NULL. For joinLeft the unmatched left rows are
// kept with NULL padded across the omitted right columns.
func (e *Engine) usingJoin(l, r *joinedFrom, names []string, mode joinKind) *joinedFrom {
	// Right-hand emission columns: all right columns except the USING ones.
	var rEmit []Column
	var rEmitSrc []int
	for i, c := range r.cols {
		if containsNameFold(names, c.Name) {
			continue
		}
		rEmit = append(rEmit, c)
		rEmitSrc = append(rEmitSrc, i)
	}
	// Left indices for each USING name (the merged copy lives on the left).
	var lIdx []int
	for _, u := range names {
		lIdx = append(lIdx, columnNamed(l.cols, u))
	}
	var rIdx []int
	for _, u := range names {
		rIdx = append(rIdx, columnNamed(r.cols, u))
	}
	cols := make([]Column, 0, len(l.cols)+len(rEmit))
	cols = append(cols, l.cols...)
	cols = append(cols, rEmit...)
	var rows [][]Value
	for _, lr := range l.rows {
		matched := false
		for _, rr := range r.rows {
			if !usingMatch(lr, rr, lIdx, rIdx) {
				continue
			}
			row := make([]Value, 0, len(lr)+len(rEmit))
			row = append(row, lr...)
			for _, ri := range rEmitSrc {
				row = append(row, rr[ri])
			}
			rows = append(rows, row)
			matched = true
		}
		if !matched && mode == joinLeft {
			row := make([]Value, 0, len(lr)+len(rEmit))
			row = append(row, lr...)
			for range rEmit {
				row = append(row, NullValue())
			}
			rows = append(rows, row)
		}
	}
	return &joinedFrom{cols: cols, rows: rows}
}

// usingMatch reports whether a left and right row agree on all USING columns
// (no NULL either side, equal otherwise).
func usingMatch(lr, rr []Value, lIdx, rIdx []int) bool {
	for i := range lIdx {
		a := lr[lIdx[i]]
		b := rr[rIdx[i]]
		if a.kind == Null || b.kind == Null {
			return false
		}
		if compareValues(a, b) != 0 {
			return false
		}
	}
	return true
}

// SubQueryExpr is a scalar "(SELECT ...)" in an expression (e.g. a SELECT-list
// item or a comparison operand). It defers execution until eval: the enclosing
// row is pushed as the outer context so correlated references resolve. Its
// value is the first cell of the first result row, or NULL for an empty result.
type SubQueryExpr struct {
	toks []token
	a, b int // range [a,b) holds the inner "SELECT ..."
	e    *Engine
}

func (sq *SubQueryExpr) result(row []Value, cols []Column) (*Result, error) {
	return sq.e.runSub(sq.toks[sq.a:sq.b], row, cols)
}

func (sq *SubQueryExpr) eval(row []Value, cols []Column) (Value, error) {
	res, err := sq.result(row, cols)
	if err != nil {
		return Value{}, err
	}
	if len(res.Rows) == 0 {
		return NullValue(), nil
	}
	return res.Rows[0][0], nil
}

func (sq *SubQueryExpr) affinity() Affinity { return AffNone }

// ExistsExpr is "EXISTS (SELECT ...)" (or NOT EXISTS, via NotExpr). It is true
// when the subquery returns at least one row.
type ExistsExpr struct {
	sub *SubQueryExpr
}

func (e *ExistsExpr) eval(row []Value, cols []Column) (Value, error) {
	res, err := e.sub.result(row, cols)
	if err != nil {
		return Value{}, err
	}
	if len(res.Rows) > 0 {
		return IntValue(1), nil
	}
	return IntValue(0), nil
}

func (e *ExistsExpr) affinity() Affinity { return AffNone }

// InSubExpr is "x IN (SELECT ...)" or "x NOT IN (SELECT ...)". Membership uses
// the same affinity rules as a plain IN list; NULL handling follows SQLite.
type InSubExpr struct {
	negate bool
	left   Expr
	sub    *SubQueryExpr
}

func (e *InSubExpr) eval(row []Value, cols []Column) (Value, error) {
	lv, err := e.left.eval(row, cols)
	if err != nil {
		return Value{}, err
	}
	res, err := e.sub.result(row, cols)
	if err != nil {
		return Value{}, err
	}
	hasNull := false
	for _, r := range res.Rows {
		for _, iv := range r {
			if iv.kind == Null {
				hasNull = true
				continue
			}
			if lv.kind == Null {
				continue
			}
			a, b := applyComparisonAffinity(lv, iv, comparisonAffinity(&CompareExpr{left: e.left, right: &LiteralExpr{val: iv}}))
			if compareValues(a, b) == 0 {
				if e.negate {
					return IntValue(0), nil
				}
				return IntValue(1), nil
			}
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

func (e *InSubExpr) affinity() Affinity { return AffNone }

// runSub evaluates a nested SELECT over the given token range with an optional
// enclosing row/column context for correlated references, pushing and popping
// the outer context so nested subqueries work.
func (e *Engine) runSub(toks []token, outerRow []Value, outerCols []Column) (*Result, error) {
	outerRowStack = append(outerRowStack, outerRow)
	outerColsStack = append(outerColsStack, outerCols)
	defer func() {
		outerRowStack = outerRowStack[:len(outerRowStack)-1]
		outerColsStack = outerColsStack[:len(outerColsStack)-1]
	}()
	// The parser relies on a trailing tokEOF sentinel (peek()/checkTrailing
	// assume one is present). Subquery slices coming from scanClose exclude the
	// surrounding ')'; append an EOF sentinel so the inner SELECT terminates
	// cleanly instead of mis-rejecting the last identifier as trailing garbage.
	runToks := toks
	if len(toks) == 0 || toks[len(toks)-1].kind != tokEOF {
		runToks = append(append([]token{}, toks...), token{kind: tokEOF})
	}
	p := &parser{toks: runToks, e: e}
	return e.parseSelect(p)
}

// mergeCompound combines two compound SELECT arms under one set operator,
// matching SQLite's duplicate semantics. UNION removes duplicates; UNION ALL
// keeps them; INTERSECT keeps rows present in both, deduplicated; EXCEPT keeps
// rows of the left arm absent from the right, deduplicated. The result column
// names come from the first arm. Type merging uses the width of the first arm's
// columns as the target; other arms are checked for column-count equality.
func mergeCompound(a, b *Result, kind compoundKind) (*Result, error) {
	if len(b.Columns) != len(a.Columns) {
		return nil, &SQLError{Message: fmt.Sprintf("SELECTs to the left and right of %s do not have the same number of result columns", compoundName(kind))}
	}
	var rows [][]Value
	switch kind {
	case compUnionAll:
		rows = append(rows, a.Rows...)
		rows = append(rows, b.Rows...)
	case compUnion:
		// UNION is dedup(a ∪ b): every distinct row of either arm.
		rows = append(rows, a.Rows...)
		rows = append(rows, b.Rows...)
		rows = dedupRows(rows)
	case compIntersect:
		// INTERSECT keeps left rows that also appear in the right arm,
		// deduplicated (NULL matches NULL, as with DISTINCT).
		rightDedup := dedupRows(b.Rows)
		for _, r := range dedupRows(a.Rows) {
			for _, rr := range rightDedup {
				if valsEqual(r, rr) {
					rows = append(rows, r)
					break
				}
			}
		}
	case compExcept:
		// EXCEPT keeps left rows absent from the right arm, deduplicated.
		rightDedup := dedupRows(b.Rows)
		for _, r := range dedupRows(a.Rows) {
			dupe := false
			for _, rr := range rightDedup {
				if valsEqual(r, rr) {
					dupe = true
					break
				}
			}
			if !dupe {
				rows = append(rows, r)
			}
		}
	}
	return &Result{Columns: a.Columns, Rows: rows}, nil
}

func compoundName(k compoundKind) string {
	switch k {
	case compUnion:
		return "UNION"
	case compUnionAll:
		return "UNION ALL"
	case compIntersect:
		return "INTERSECT"
	case compExcept:
		return "EXCEPT"
	}
	return "COMPOUND"
}

// parseInSub recognizes "IN (SELECT ...)" after the IN keyword: the '(' must be
// followed by SELECT for it to be a subquery, otherwise it is a plain IN list.
// It returns an *InSubExpr when a subquery is present, or nil (leaving the '('
// unread) for the list form.
func (e *Engine) parseInSub(p *parser, left Expr, negate bool) (Expr, error) {
	if len(p.toks) == 0 || p.toks[p.pos].kind != tokPunct || p.toks[p.pos].text != "(" {
		return nil, nil
	}
	nxt := p.toks[p.pos+1]
	if nxt.kind != tokIdent || !strings.EqualFold(nxt.text, "SELECT") {
		return nil, nil
	}
	p.next() // '('
	a := p.pos
	b, err := p.scanClose(a)
	if err != nil {
		return nil, err
	}
	sub := &SubQueryExpr{toks: p.toks, a: a, b: b, e: e}
	p.pos = b + 1
	return &InSubExpr{negate: negate, left: left, sub: sub}, nil
}

// joinSetOpKind identifies a compound operator.
type compoundKind int

const (
	compUnion compoundKind = iota
	compUnionAll
	compIntersect
	compExcept
)

// compoundKeyword maps the keyword after a SELECT core to a compound kind.
func peekCompoundKind(p *parser) (compoundKind, bool) {
	t := p.peek()
	if t.kind != tokIdent {
		return compUnion, false
	}
	switch strings.ToUpper(t.text) {
	case "UNION":
		p.next()
		all := p.isKeyword("ALL")
		if all {
			p.next()
		}
		if all {
			return compUnionAll, true
		}
		return compUnion, true
	case "INTERSECT":
		p.next()
		return compIntersect, true
	case "EXCEPT":
		p.next()
		return compExcept, true
	}
	return compUnion, false
}
