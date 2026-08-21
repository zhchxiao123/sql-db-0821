package engine

import (
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
	tokOp
	tokPunct
)

type token struct {
	kind tokenKind
	text string
	pos  int
}

// unsupportedKeywords maps keywords that belong to constructs outside the
// minimal subset to a human-readable feature name. The parser returns an
// UnsupportedError whenever it meets one of these in a place the grammar
// does not allow.
var unsupportedKeywords = map[string]string{
	"ORDER":       "ORDER BY",
	"LIMIT":       "LIMIT",
	"OFFSET":      "OFFSET",
	"DISTINCT":    "DISTINCT",
	"GROUP":       "GROUP BY",
	"HAVING":      "HAVING",
	"JOIN":        "JOIN",
	"INNER":       "JOIN",
	"LEFT":        "JOIN",
	"RIGHT":       "JOIN",
	"FULL":        "JOIN",
	"CROSS":       "JOIN",
	"UNION":       "UNION",
	"INTERSECT":   "UNION",
	"EXCEPT":      "UNION",
	"CASE":        "CASE expression",
	"WHEN":        "CASE expression",
	"THEN":        "CASE expression",
	"ELSE":        "CASE expression",
	"END":         "CASE expression",
	"EXISTS":      "EXISTS subquery",
	"LIKE":        "LIKE",
	"GLOB":        "GLOB",
	"BETWEEN":     "BETWEEN",
	"IN":          "IN",
	"IS":          "IS NULL",
	"NOT":         "NOT",
	"AND":         "AND",
	"OR":          "OR",
	"ASC":         "ORDER BY",
	"DESC":        "ORDER BY",
	"INDEX":       "CREATE INDEX",
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

func isIdentStart(c byte) bool {
	return c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

func isIdentPart(c byte) bool {
	return isIdentStart(c) || (c >= '0' && c <= '9')
}

// tokenize splits SQL text into tokens. String literals use single quotes
// with ” as the escaped quote, matching SQLite.
func tokenize(sql string) ([]token, error) {
	var toks []token
	i, n := 0, len(sql)
	for i < n {
		c := sql[i]
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
		case c == '(' || c == ')' || c == ',' || c == ';' || c == '*' || c == '.':
			toks = append(toks, token{kind: tokPunct, text: string(c), pos: i})
			i++
		case c == '=' || c == '<' || c == '>' || c == '!' || c == '+' || c == '-' || c == '/' || c == '%':
			j := i + 1
			if (c == '<' || c == '>' || c == '!') && j < n && sql[j] == '=' {
				j++
			} else if c == '<' && j < n && sql[j] == '>' {
				j++
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

// parseValue parses a literal: number, string, or NULL.
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

// parseInsert handles INSERT INTO t [(cols)] VALUES (...).
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
	tbl.Rows = append(tbl.Rows, row)
	return &Result{Affected: 1}, nil
}

// parseSelect handles SELECT cols|*|COUNT(*) FROM t [WHERE cond].
func (e *Engine) parseSelect(p *parser) (*Result, error) {
	p.next() // SELECT
	if strings.EqualFold(p.peek().text, "DISTINCT") {
		return nil, &UnsupportedError{Feature: "DISTINCT"}
	}
	var selCols []string // nil means "*"
	isCount := false
	switch {
	case p.peek().text == "*":
		p.next()
	case strings.EqualFold(p.peek().text, "COUNT"):
		p.next()
		if err := p.expectPunct("("); err != nil {
			return nil, err
		}
		if p.peek().text != "*" {
			return nil, &UnsupportedError{Feature: "aggregate functions other than COUNT(*)"}
		}
		p.next()
		if err := p.expectPunct(")"); err != nil {
			return nil, err
		}
		isCount = true
	default:
		for {
			t := p.peek()
			if t.kind == tokPunct && t.text == "(" {
				return nil, &UnsupportedError{Feature: "subquery"}
			}
			if t.kind == tokIdent {
				if f, ok := unsupportedKeywords[strings.ToUpper(t.text)]; ok {
					return nil, &UnsupportedError{Feature: f}
				}
			}
			cn, err := p.expectIdent()
			if err != nil {
				return nil, err
			}
			selCols = append(selCols, cn)
			switch p.peek().text {
			case ",":
				p.next()
				continue
			case ".":
				return nil, &UnsupportedError{Feature: "qualified column reference"}
			case "(", "*":
				return nil, &UnsupportedError{Feature: "arithmetic expression"}
			}
			if p.peek().kind == tokOp {
				return nil, &UnsupportedError{Feature: "arithmetic expression"}
			}
			break
		}
	}
	if !strings.EqualFold(p.peek().text, "FROM") {
		return nil, &SQLError{Message: "expected FROM in SELECT"}
	}
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
	if strings.EqualFold(p.peek().text, "JOIN") {
		return nil, &UnsupportedError{Feature: "JOIN"}
	}
	var cond Expr
	if strings.EqualFold(p.peek().text, "WHERE") {
		p.next()
		cond, err = p.parseCondition()
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
	if isCount {
		var n int64
		for _, row := range tbl.Rows {
			if cond == nil || condTrue(cond, row, tbl.Columns) {
				n++
			}
		}
		return &Result{Columns: []string{"COUNT(*)"}, Rows: [][]Value{{IntValue(n)}}}, nil
	}
	var outCols []Column
	if selCols == nil {
		outCols = tbl.Columns
	} else {
		for _, cn := range selCols {
			idx := tbl.columnIndex(cn)
			if idx < 0 {
				return nil, &SQLError{Message: fmt.Sprintf("no such column: %s", cn)}
			}
			outCols = append(outCols, tbl.Columns[idx])
		}
	}
	var rows [][]Value
	for _, row := range tbl.Rows {
		if cond != nil && !condTrue(cond, row, tbl.Columns) {
			continue
		}
		out := make([]Value, len(outCols))
		for i, c := range outCols {
			out[i] = row[tbl.columnIndex(c.Name)]
		}
		rows = append(rows, out)
	}
	colNames := make([]string, len(outCols))
	for i, c := range outCols {
		colNames[i] = c.Name
	}
	return &Result{Columns: colNames, Rows: rows}, nil
}

// parseUpdate handles UPDATE t SET col = val [, ...] [WHERE cond].
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
		val, err := p.parseOperand()
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
		cond, err = p.parseCondition()
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

// parseDelete handles DELETE FROM t [WHERE cond].
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
		cond, err = p.parseCondition()
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
