package engine

import (
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
)

// tokenKind classifies a lexical token.
type tokenKind int

const (
	tokEOF tokenKind = iota
	tokIdent
	tokString
	tokNumber
	tokBlob
	tokOp
	tokPunct
)

type token struct {
	kind tokenKind
	text string
	pos  int
}

// unsupportedKeywords maps keywords that belong to constructs outside the
// supported subset to a human-readable feature name. The parser returns an
// UnsupportedError whenever it meets one of these in a place the grammar
// does not allow. LIKE/GLOB/BETWEEN/IN/IS/NOT/AND/OR are deliberately absent:
// they are part of the expression subset.
var unsupportedKeywords = map[string]string{
	"JOIN":     "JOIN",
	"INNER":    "JOIN",
	"LEFT":     "JOIN",
	"RIGHT":    "JOIN",
	"FULL":     "JOIN",
	"CROSS":    "JOIN",
	"UNION":    "UNION",
	"INTERSECT": "UNION",
	"EXCEPT":   "UNION",
	"CASE":     "CASE expression",
	"WHEN":     "CASE expression",
	"THEN":     "CASE expression",
	"ELSE":     "CASE expression",
	"END":      "CASE expression",
	"EXISTS":   "EXISTS subquery",
	"INDEX":    "CREATE INDEX",
	"ALTER":       "ALTER TABLE",
	"VIEW":        "VIEW",
	"TRANSACTION": "transaction",
	"BEGIN":       "transaction",
	"COMMIT":      "transaction",
	"ROLLBACK":    "transaction",
	"PRAGMA":      "PRAGMA",
	"VACUUM":      "VACUUM",
	"ATTACH":      "ATTACH",
	"DETACH":      "DETACH",
	"REINDEX":     "REINDEX",
	"ANALYZE":     "ANALYZE",
}

// selectStopKeywords are keywords that terminate a SELECT list, so an
// identifier following an expression is an alias only when it is not one of
// these.
var selectStopKeywords = map[string]bool{
	"FROM": true, "WHERE": true, "GROUP": true, "ORDER": true, "LIMIT": true,
	"OFFSET": true, "HAVING": true, "UNION": true, "INTERSECT": true,
	"EXCEPT": true, "JOIN": true, "INNER": true, "LEFT": true, "RIGHT": true,
	"FULL": true, "CROSS": true, "NATURAL": true, "ON": true, "USING": true,
	"AS": true, "AND": true, "OR": true, "NOT": true, "IS": true, "IN": true,
	"BETWEEN": true, "LIKE": true, "GLOB": true, "CASE": true, "WHEN": true,
	"THEN": true, "ELSE": true, "END": true, "EXISTS": true, "ASC": true,
	"DESC": true, "ALL": true, "DISTINCT": true, "ESCAPE": true,
}

func isIdentStart(c byte) bool {
	return c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

func isIdentPart(c byte) bool {
	return isIdentStart(c) || (c >= '0' && c <= '9')
}

// tokenize splits SQL text into tokens. String literals use single quotes
// with '' as the escaped quote, blob literals use X'hex', and == is accepted
// as an alias for =, all matching SQLite.
func tokenize(sql string) ([]token, error) {
	var toks []token
	i, n := 0, len(sql)
	for i < n {
		c := sql[i]
		// Blob literal: X'hex' or x'hex'.
		if (c == 'x' || c == 'X') && i+1 < n && sql[i+1] == '\'' {
			j := i + 2
			var sb strings.Builder
			for j < n && sql[j] != '\'' {
				sb.WriteByte(sql[j])
				j++
			}
			if j >= n {
				return nil, &SQLError{Message: "unterminated blob literal"}
			}
			raw := sb.String()
			if len(raw)%2 != 0 {
				return nil, &SQLError{Message: "malformed blob literal"}
			}
			bytes, err := hex.DecodeString(raw)
			if err != nil {
				return nil, &SQLError{Message: "malformed blob literal"}
			}
			toks = append(toks, token{kind: tokBlob, text: string(bytes), pos: i})
			i = j + 1
			continue
		}
		switch {
		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
			i++
		case c == '\'':
			j := i + 1
			var sb strings.Builder
			for j < n {
				if sql[j] == '\'' {
					if j+1 < n && sql[j+1] == '\'' {
						sb.WriteByte('\'')
						j += 2
						continue
					}
					break
				}
				sb.WriteByte(sql[j])
				j++
			}
			if j >= n {
				return nil, &SQLError{Message: "unterminated string literal"}
			}
			toks = append(toks, token{kind: tokString, text: sb.String(), pos: i})
			i = j + 1
		case c >= '0' && c <= '9' || (c == '.' && i+1 < n && sql[i+1] >= '0' && sql[i+1] <= '9'):
			j := i
			for j < n && (sql[j] >= '0' && sql[j] <= '9' || sql[j] == '.') {
				j++
			}
			if j < n && (sql[j] == 'e' || sql[j] == 'E') {
				k := j + 1
				if k < n && (sql[k] == '+' || sql[k] == '-') {
					k++
				}
				if k < n && sql[k] >= '0' && sql[k] <= '9' {
					j = k
					for j < n && sql[j] >= '0' && sql[j] <= '9' {
						j++
					}
				}
			}
			toks = append(toks, token{kind: tokNumber, text: sql[i:j], pos: i})
			i = j
		case isIdentStart(c):
			j := i
			for j < n && isIdentPart(sql[j]) {
				j++
			}
			toks = append(toks, token{kind: tokIdent, text: sql[i:j], pos: i})
			i = j
		case c == '(' || c == ')' || c == ',' || c == ';' || c == '.':
			toks = append(toks, token{kind: tokPunct, text: string(c), pos: i})
			i++
		case c == '=' || c == '<' || c == '>' || c == '!' || c == '+' || c == '-' || c == '/' || c == '%' || c == '*':
			j := i + 1
			if (c == '<' || c == '>' || c == '!') && j < n && sql[j] == '=' {
				j++
			} else if c == '<' && j < n && sql[j] == '>' {
				j++
			} else if c == '=' && j < n && sql[j] == '=' {
				j++ // == is accepted as =
			}
			toks = append(toks, token{kind: tokOp, text: sql[i:j], pos: i})
			i = j
		default:
			return nil, &SQLError{Message: fmt.Sprintf("unexpected character %q at position %d", c, i)}
		}
	}
	toks = append(toks, token{kind: tokEOF, pos: n})
	return toks, nil
}

// parser walks a token stream for one statement.
type parser struct {
	toks []token
	pos  int
}

func (p *parser) peek() token { return p.toks[p.pos] }

func (p *parser) next() token {
	t := p.toks[p.pos]
	if p.pos < len(p.toks)-1 {
		p.pos++
	}
	return t
}

func (p *parser) expectIdent() (string, error) {
	t := p.peek()
	if t.kind != tokIdent {
		return "", &SQLError{Message: "expected identifier, got " + t.text}
	}
	p.next()
	return t.text, nil
}

func (p *parser) expectPunct(s string) error {
	t := p.peek()
	if t.kind != tokPunct || t.text != s {
		return &SQLError{Message: "expected " + s + ", got " + t.text}
	}
	p.next()
	return nil
}

// checkTrailing rejects anything left after a fully parsed statement. If the
// leftover token is a known unsupported keyword we report it as such;
// otherwise it is a plain syntax error.
func (p *parser) checkTrailing() error {
	t := p.peek()
	if t.kind == tokEOF {
		return nil
	}
	if t.kind == tokIdent {
		if f, ok := unsupportedKeywords[strings.ToUpper(t.text)]; ok {
			return &UnsupportedError{Feature: f}
		}
	}
	return &SQLError{Message: "syntax error near " + t.text}
}

// parseValue parses a literal: number, string, blob, or NULL.
func (p *parser) parseValue() (Value, error) {
	t := p.peek()
	switch t.kind {
	case tokNumber:
		p.next()
		if isIntLiteral(t.text) {
			n, err := strconv.ParseInt(t.text, 10, 64)
			if err != nil {
				return Value{}, &SQLError{Message: "invalid integer " + t.text}
			}
			return IntValue(n), nil
		}
		f, err := strconv.ParseFloat(t.text, 64)
		if err != nil {
			return Value{}, &SQLError{Message: "invalid number " + t.text}
		}
		return FloatValue(f), nil
	case tokString:
		p.next()
		return TextValue(t.text), nil
	case tokBlob:
		p.next()
		return BlobValue(t.text), nil
	case tokIdent:
		if strings.EqualFold(t.text, "NULL") {
			p.next()
			return NullValue(), nil
		}
	}
	return Value{}, &SQLError{Message: "expected a value, got " + t.text}
}

func isIntLiteral(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// skipColumnConstraints consumes trailing column-level constraints such as
// PRIMARY KEY, NOT NULL and UNIQUE. Constraint enforcement is out of scope
// (a later sub-requirement), so they are parsed and ignored.
func (p *parser) skipColumnConstraints() error {
	for {
		t := p.peek()
		if t.kind != tokIdent {
			return nil
		}
		switch strings.ToUpper(t.text) {
		case "PRIMARY":
			p.next()
			if !strings.EqualFold(p.peek().text, "KEY") {
				return &SQLError{Message: "expected KEY after PRIMARY"}
			}
			p.next()
		case "NOT":
			p.next()
			if !strings.EqualFold(p.peek().text, "NULL") {
				return &SQLError{Message: "expected NULL after NOT"}
			}
			p.next()
		case "NULL", "UNIQUE", "AUTOINCREMENT":
			p.next()
		default:
			return nil
		}
	}
}

// parseCreate handles CREATE TABLE (and rejects CREATE INDEX / VIEW).
func (e *Engine) parseCreate(p *parser) (*Result, error) {
	p.next() // CREATE
	kw := strings.ToUpper(p.peek().text)
	switch kw {
	case "TABLE":
		p.next()
	case "INDEX":
		return nil, &UnsupportedError{Feature: "CREATE INDEX"}
	case "VIEW":
		return nil, &UnsupportedError{Feature: "CREATE VIEW"}
	default:
		return nil, &SQLError{Message: "expected TABLE after CREATE"}
	}
	name, err := p.expectIdent()
	if err != nil {
		return nil, err
	}
	if err := p.expectPunct("("); err != nil {
		return nil, err
	}
	var cols []Column
	for {
		colName, err := p.expectIdent()
		if err != nil {
			return nil, err
		}
		typeName, err := p.expectIdent()
		if err != nil {
			return nil, err
		}
		// Optional "(n)" size suffix, e.g. VARCHAR(30).
		if p.peek().text == "(" {
			p.next()
			if p.peek().kind != tokNumber {
				return nil, &SQLError{Message: "expected size in type declaration"}
			}
			p.next()
			if err := p.expectPunct(")"); err != nil {
				return nil, err
			}
		}
		if err := p.skipColumnConstraints(); err != nil {
			return nil, err
		}
		cols = append(cols, Column{Name: colName, Type: strings.ToUpper(typeName)})
		if p.peek().text == "," {
			p.next()
			continue
		}
		break
	}
	if err := p.expectPunct(")"); err != nil {
		return nil, err
	}
	if err := p.checkTrailing(); err != nil {
		return nil, err
	}
	if _, exists := e.tables[name]; exists {
		return nil, &SQLError{Message: fmt.Sprintf("table %q already exists", name)}
	}
	e.tables[name] = &Table{Name: name, Columns: cols}
	return &Result{}, nil
}

// parseInsert handles INSERT INTO t [(cols)] VALUES (...). Values are
// converted to the target column's affinity, matching SQLite's storage rules.
func (e *Engine) parseInsert(p *parser) (*Result, error) {
	p.next() // INSERT
	if !strings.EqualFold(p.peek().text, "INTO") {
		return nil, &SQLError{Message: "expected INTO after INSERT"}
	}
	p.next()
	name, err := p.expectIdent()
	if err != nil {
		return nil, err
	}
	var colNames []string
	if p.peek().text == "(" {
		p.next()
		for {
			cn, err := p.expectIdent()
			if err != nil {
				return nil, err
			}
			colNames = append(colNames, cn)
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
	if !strings.EqualFold(p.peek().text, "VALUES") {
		return nil, &UnsupportedError{Feature: "INSERT ... SELECT"}
	}
	p.next()
	if err := p.expectPunct("("); err != nil {
		return nil, err
	}
	var vals []Value
	for {
		v, err := p.parseValue()
		if err != nil {
			return nil, err
		}
		vals = append(vals, v)
		if p.peek().text == "," {
			p.next()
			continue
		}
		break
	}
	if err := p.expectPunct(")"); err != nil {
		return nil, err
	}
	if err := p.checkTrailing(); err != nil {
		return nil, err
	}
	tbl, ok := e.tables[name]
	if !ok {
		return nil, &SQLError{Message: fmt.Sprintf("no such table: %s", name)}
	}
	if len(colNames) == 0 {
		if len(vals) != len(tbl.Columns) {
			return nil, &SQLError{Message: fmt.Sprintf("table %s has %d columns but %d values were supplied", name, len(tbl.Columns), len(vals))}
		}
	} else if len(vals) != len(colNames) {
		return nil, &SQLError{Message: "INSERT has more expressions than target columns"}
	}
	row := make([]Value, len(tbl.Columns))
	if len(colNames) == 0 {
		copy(row, vals)
	} else {
		for i, cn := range colNames {
			idx := tbl.columnIndex(cn)
			if idx < 0 {
				return nil, &SQLError{Message: fmt.Sprintf("no such column: %s", cn)}
			}
			row[idx] = vals[i]
		}
	}
	// Apply each column's affinity to the stored value.
	for i := range row {
		row[i] = applyAffinity(row[i], columnAffinity(tbl.Columns[i].Type))
	}
	tbl.Rows = append(tbl.Rows, row)
	return &Result{Affected: 1}, nil
}

// selectItem is one entry in a SELECT list: an expression plus its result
// column name.
type selectItem struct {
	expr Expr
	name string
}

// exprName derives a result column name for an expression without an alias.
func exprName(e Expr) string {
	switch n := e.(type) {
	case *LiteralExpr:
		return n.val.RenderCLI()
	case *ColumnExpr:
		return n.name
	case *AggExpr:
		if n.star {
			return "COUNT(*)"
		}
		return n.funcName + "(expr)"
	default:
		return "expr"
	}
}

// containsCountStar reports whether an expression tree contains a COUNT(*)
// marker (used to reject aggregates mixed into a FROM select list).
func containsCountStar(e Expr) bool {
	switch n := e.(type) {
	case *CountStarExpr:
		return true
	case *CastExpr:
		return containsCountStar(n.inner)
	case *ArithExpr:
		return containsCountStar(n.left) || containsCountStar(n.right)
	case *NegExpr:
		return containsCountStar(n.inner)
	case *CompareExpr:
		return containsCountStar(n.left) || containsCountStar(n.right)
	case *LogicalExpr:
		return containsCountStar(n.left) || containsCountStar(n.right)
	case *NotExpr:
		return containsCountStar(n.inner)
	case *InExpr:
		if containsCountStar(n.left) {
			return true
		}
		for _, item := range n.list {
			if containsCountStar(item) {
				return true
			}
		}
	case *BetweenExpr:
		return containsCountStar(n.left) || containsCountStar(n.low) || containsCountStar(n.high)
	case *LikeExpr:
		if containsCountStar(n.left) || containsCountStar(n.pattern) {
			return true
		}
		if n.escape != nil {
			return containsCountStar(n.escape)
		}
	}
	return false
}

// parseSelect handles a full single-table SELECT: select list, FROM+WHERE,
// GROUP BY, HAVING, ORDER BY (ordinal or expression keys, ASC/DESC, NULL
// placement), LIMIT/OFFSET and DISTINCT. An aggregate query (one with GROUP BY
// or an aggregate in the select list or HAVING) computes one output row per
// group; a without-FROM SELECT supplies a single constant row so COUNT(*)=1.
func (e *Engine) parseSelect(p *parser) (*Result, error) {
	p.next() // SELECT
	distinct := false
	if p.isKeyword("ALL") {
		p.next()
	} else if p.isKeyword("DISTINCT") {
		p.next()
		distinct = true
	}
	star := false
	if p.peek().text == "*" {
		p.next()
		star = true
	}
	var items []selectItem
	if !star {
		for {
			expr, err := p.parseExpr()
			if err != nil {
				return nil, err
			}
			name := exprName(expr)
			if p.isKeyword("AS") {
				p.next()
				an, err := p.expectIdent()
				if err != nil {
					return nil, err
				}
				name = an
			} else if t := p.peek(); t.kind == tokIdent && !selectStopKeywords[strings.ToUpper(t.text)] {
				p.next()
				name = t.text
			}
			items = append(items, selectItem{expr: expr, name: name})
			if p.peek().text == "," {
				p.next()
				continue
			}
			break
		}
	}
	var tbl *Table
	var cond Expr
	var err error
	if p.isKeyword("FROM") {
		p.next()
		name, err := p.expectIdent()
		if err != nil {
			return nil, err
		}
		switch p.peek().text {
		case ",":
			return nil, &UnsupportedError{Feature: "JOIN"}
		case ".":
			return nil, &UnsupportedError{Feature: "qualified column reference"}
		case "(":
			return nil, &UnsupportedError{Feature: "subquery in FROM"}
		}
		if p.isKeyword("JOIN") {
			return nil, &UnsupportedError{Feature: "JOIN"}
		}
		if p.isKeyword("WHERE") {
			p.next()
			cond, err = p.parseExpr()
			if err != nil {
				return nil, err
			}
		}
		var ok bool
		tbl, ok = e.tables[name]
		if !ok {
			return nil, &SQLError{Message: fmt.Sprintf("no such table: %s", name)}
		}
	}
	cols := []Column(nil)
	if tbl != nil {
		cols = tbl.Columns
	}
	// GROUP BY expr [, ...]
	var groupBy []Expr
	if p.isKeyword("GROUP") {
		p.next()
		if !p.isKeyword("BY") {
			return nil, &SQLError{Message: "expected BY after GROUP"}
		}
		p.next()
		for {
			g, err := p.parseExpr()
			if err != nil {
				return nil, err
			}
			groupBy = append(groupBy, g)
			if p.peek().text == "," {
				p.next()
				continue
			}
			break
		}
	}
	// HAVING expr
	var having Expr
	if p.isKeyword("HAVING") {
		p.next()
		having, err = p.parseExpr()
		if err != nil {
			return nil, err
		}
	}
	// ORDER BY term [ASC|DESC] [, ...]
	var order []orderTerm
	if p.isKeyword("ORDER") {
		p.next()
		if !p.isKeyword("BY") {
			return nil, &SQLError{Message: "expected BY after ORDER"}
		}
		p.next()
		for {
			var term orderTerm
			if t := p.peek(); t.kind == tokNumber && isIntLiteral(t.text) {
				ord, err := strconv.Atoi(t.text)
				if err != nil {
					return nil, &SQLError{Message: "invalid ORDER BY term"}
				}
				term.ordinal = ord
				p.next()
			} else {
				ex, err := p.parseExpr()
				if err != nil {
					return nil, err
				}
				term.expr = ex
			}
			if p.isKeyword("ASC") {
				p.next()
			} else if p.isKeyword("DESC") {
				p.next()
				term.desc = true
			}
			order = append(order, term)
			if p.peek().text == "," {
				p.next()
				continue
			}
			break
		}
	}
	// LIMIT n [OFFSET m] | LIMIT n, m | OFFSET m
	var limit, offset int
	haveLimit := false
	if p.isKeyword("LIMIT") {
		p.next()
		haveLimit = true
		lev, err := p.parseExpr()
		if err != nil {
			return nil, err
		}
		lv, err := lev.eval(nil, nil)
		if err != nil {
			return nil, err
		}
		if p.isKeyword("OFFSET") {
			p.next()
			oev, err := p.parseExpr()
			if err != nil {
				return nil, err
			}
			ov, err := oev.eval(nil, nil)
			if err != nil {
				return nil, err
			}
			limit, offset = int(intValueOf(lv)), int(intValueOf(ov))
		} else if p.peek().text == "," {
			p.next()
			oev, err := p.parseExpr()
			if err != nil {
				return nil, err
			}
			ov, err := oev.eval(nil, nil)
			if err != nil {
				return nil, err
			}
			limit, offset = int(intValueOf(ov)), int(intValueOf(lv))
		} else {
			limit, offset = int(intValueOf(lv)), 0
		}
	} else if p.isKeyword("OFFSET") {
		return nil, &SQLError{Message: "OFFSET requires a LIMIT clause"}
	}
	if err := p.checkTrailing(); err != nil {
		return nil, err
	}
	if star {
		if tbl == nil {
			return nil, &SQLError{Message: "SELECT * requires a FROM clause"}
		}
		for _, c := range tbl.Columns {
			items = append(items, selectItem{expr: &ColumnExpr{name: c.Name}, name: c.Name})
		}
	}
	// Resolve column affinities (also validates column names).
	if err := resolveColumnAffinity(cond, cols); err != nil {
		return nil, err
	}
	for _, it := range items {
		if err := resolveColumnAffinity(it.expr, cols); err != nil {
			return nil, err
		}
	}
	for _, g := range groupBy {
		if err := resolveColumnAffinity(g, cols); err != nil {
			return nil, err
		}
	}
	if having != nil {
		if err := resolveColumnAffinity(having, cols); err != nil {
			return nil, err
		}
	}
	// Aggregate query: GROUP BY present, or an aggregate in the select list or
	// HAVING. HAVING on a non-aggregate query is a SQLite parse error.
	aggQuery := len(groupBy) > 0
	if !aggQuery {
		for _, it := range items {
			if hasAggregate(it.expr) {
				aggQuery = true
				break
			}
		}
	}
	if !aggQuery && having != nil {
		aggQuery = hasAggregate(having)
	}
	if having != nil && !aggQuery {
		return nil, &SQLError{Message: "HAVING clause on a non-aggregate query"}
	}
	// WHERE-filtered source rows (or a single constant row when there is no
	// FROM).
	var condRows [][]Value
	if tbl != nil {
		for _, row := range tbl.Rows {
			if cond != nil && !condTrue(cond, row, tbl.Columns) {
				continue
			}
			condRows = append(condRows, row)
		}
	} else {
		condRows = [][]Value{{}}
	}
	var outRows [][]Value
	if aggQuery {
		if len(groupBy) > 0 {
			if err := checkGroupColumns(items, having, groupBy); err != nil {
				return nil, err
			}
		}
		// Rewrite aggregates across items + HAVING into one shared slot list.
		var aggList []*AggExpr
		for i := range items {
			ne, lst := replaceAggregates(items[i].expr, aggList, false)
			items[i].expr = ne
			aggList = lst
		}
		var havingSlotted Expr
		if having != nil {
			var lst []*AggExpr
			havingSlotted, lst = replaceAggregates(having, aggList, false)
			aggList = lst
		}
		// Evaluate each GROUP BY key per source row.
		var keyRows [][]Value
		for _, row := range condRows {
			var kv []Value
			for _, g := range groupBy {
				v, err := g.eval(row, cols)
				if err != nil {
					return nil, err
				}
				kv = append(kv, v)
			}
			keyRows = append(keyRows, kv)
		}
		var keyIdx []int
		for i := range groupBy {
			keyIdx = append(keyIdx, i)
		}
		groups := partitionGroups(keyRows, keyIdx)
		colLen := 0
		if cols != nil {
			colLen = len(cols)
		}
		for _, g := range groups {
			var groupRows [][]Value
			for _, gi := range g {
				groupRows = append(groupRows, condRows[gi])
			}
			aggVals := make([]Value, len(aggList))
			for i, a := range aggList {
				v, err := evalAgg(a, groupRows, cols)
				if err != nil {
					return nil, err
				}
				aggVals[i] = v
			}
			// The group row is the first source row (or all-NULL for an empty
			// group) with the aggregate values appended.
			base := make([]Value, colLen)
			if len(g) > 0 {
				copy(base, condRows[g[0]])
			}
			grow := append(base, aggVals...)
			if havingSlotted != nil {
				hv, err := havingSlotted.eval(grow, cols)
				if err != nil {
					return nil, err
				}
				if hv.kind != Int || hv.intVal == 0 {
					continue
				}
			}
			out := make([]Value, len(items))
			for i, it := range items {
				v, err := it.expr.eval(grow, cols)
				if err != nil {
					return nil, err
				}
				out[i] = v
			}
			outRows = append(outRows, out)
		}
	} else {
		for _, row := range condRows {
			out := make([]Value, len(items))
			for i, it := range items {
				v, err := it.expr.eval(row, cols)
				if err != nil {
					return nil, err
				}
				out[i] = v
			}
			outRows = append(outRows, out)
		}
	}
	if distinct {
		outRows = dedupRows(outRows)
	}
	if len(order) > 0 {
		outCols := make([]Column, len(items))
		for i, it := range items {
			outCols[i] = Column{Name: it.name}
		}
		if serr := sortRowsByKeys(outRows, outCols, order); serr != nil {
			return nil, serr
		}
	}
	if haveLimit {
		off := offset
		if off < 0 {
			off = 0
		}
		if off > len(outRows) {
			outRows = nil
		} else {
			outRows = outRows[off:]
		}
		if limit >= 0 && limit < len(outRows) {
			outRows = outRows[:limit]
		}
	}
	colNames := make([]string, len(items))
	for i, it := range items {
		colNames[i] = it.name
	}
	return &Result{Columns: colNames, Rows: outRows}, nil
}

// parseUpdate handles UPDATE t SET col = expr [, ...] [WHERE expr].
func (e *Engine) parseUpdate(p *parser) (*Result, error) {
	p.next() // UPDATE
	name, err := p.expectIdent()
	if err != nil {
		return nil, err
	}
	if !strings.EqualFold(p.peek().text, "SET") {
		return nil, &SQLError{Message: "expected SET in UPDATE"}
	}
	p.next()
	type assignment struct {
		col string
		val Expr
	}
	var assigns []assignment
	for {
		cn, err := p.expectIdent()
		if err != nil {
			return nil, err
		}
		if p.peek().text != "=" {
			return nil, &SQLError{Message: "expected = in SET clause"}
		}
		p.next()
		val, err := p.parseExpr()
		if err != nil {
			return nil, err
		}
		assigns = append(assigns, assignment{col: cn, val: val})
		if p.peek().text == "," {
			p.next()
			continue
		}
		break
	}
	var cond Expr
	if strings.EqualFold(p.peek().text, "WHERE") {
		p.next()
		cond, err = p.parseExpr()
		if err != nil {
			return nil, err
		}
	}
	if err := p.checkTrailing(); err != nil {
		return nil, err
	}
	tbl, ok := e.tables[name]
	if !ok {
		return nil, &SQLError{Message: fmt.Sprintf("no such table: %s", name)}
	}
	idxs := make([]int, len(assigns))
	for i, a := range assigns {
		idx := tbl.columnIndex(a.col)
		if idx < 0 {
			return nil, &SQLError{Message: fmt.Sprintf("no such column: %s", a.col)}
		}
		idxs[i] = idx
	}
	if err := resolveColumnAffinity(cond, tbl.Columns); err != nil {
		return nil, err
	}
	for _, a := range assigns {
		if err := resolveColumnAffinity(a.val, tbl.Columns); err != nil {
			return nil, err
		}
	}
	var affected int64
	for _, row := range tbl.Rows {
		if cond != nil && !condTrue(cond, row, tbl.Columns) {
			continue
		}
		for i, a := range assigns {
			v, err := a.val.eval(row, tbl.Columns)
			if err != nil {
				return nil, err
			}
			row[idxs[i]] = v
		}
		affected++
	}
	return &Result{Affected: affected}, nil
}

// parseDelete handles DELETE FROM t [WHERE expr].
func (e *Engine) parseDelete(p *parser) (*Result, error) {
	p.next() // DELETE
	if !strings.EqualFold(p.peek().text, "FROM") {
		return nil, &SQLError{Message: "expected FROM after DELETE"}
	}
	p.next()
	name, err := p.expectIdent()
	if err != nil {
		return nil, err
	}
	var cond Expr
	if strings.EqualFold(p.peek().text, "WHERE") {
		p.next()
		cond, err = p.parseExpr()
		if err != nil {
			return nil, err
		}
	}
	if err := p.checkTrailing(); err != nil {
		return nil, err
	}
	tbl, ok := e.tables[name]
	if !ok {
		return nil, &SQLError{Message: fmt.Sprintf("no such table: %s", name)}
	}
	if err := resolveColumnAffinity(cond, tbl.Columns); err != nil {
		return nil, err
	}
	var kept [][]Value
	var affected int64
	for _, row := range tbl.Rows {
		if cond != nil && !condTrue(cond, row, tbl.Columns) {
			kept = append(kept, row)
			continue
		}
		affected++
	}
	tbl.Rows = kept
	return &Result{Affected: affected}, nil
}

// parseDrop handles DROP TABLE t (and rejects DROP INDEX / VIEW).
func (e *Engine) parseDrop(p *parser) (*Result, error) {
	p.next() // DROP
	kw := strings.ToUpper(p.peek().text)
	switch kw {
	case "TABLE":
		p.next()
	case "INDEX":
		return nil, &UnsupportedError{Feature: "DROP INDEX"}
	case "VIEW":
		return nil, &UnsupportedError{Feature: "DROP VIEW"}
	default:
		return nil, &SQLError{Message: "expected TABLE after DROP"}
	}
	name, err := p.expectIdent()
	if err != nil {
		return nil, err
	}
	if err := p.checkTrailing(); err != nil {
		return nil, err
	}
	if _, ok := e.tables[name]; !ok {
		return nil, &SQLError{Message: fmt.Sprintf("no such table: %s", name)}
	}
	delete(e.tables, name)
	return &Result{}, nil
}
