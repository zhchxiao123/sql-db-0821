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
	"JOIN":      "JOIN",
	"INNER":     "JOIN",
	"LEFT":      "JOIN",
	"RIGHT":     "JOIN",
	"FULL":      "JOIN",
	"CROSS":     "JOIN",
	"UNION":     "UNION",
	"INTERSECT": "UNION",
	"EXCEPT":    "UNION",
	"CASE":      "CASE expression",
	"WHEN":      "CASE expression",
	"THEN":      "CASE expression",
	"ELSE":      "CASE expression",
	"END":       "CASE expression",
	"EXISTS":    "EXISTS subquery",
	"VIEW":      "VIEW",
	"PRAGMA":    "PRAGMA",
	"VACUUM":    "VACUUM",
	"ATTACH":    "ATTACH",
	"DETACH":    "DETACH",
	"REINDEX":   "REINDEX",
	"ANALYZE":   "ANALYZE",
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
// with ” as the escaped quote, blob literals use X'hex', and == is accepted
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

// parseColumnConstraints consumes trailing column-level constraints (PRIMARY
// KEY, NOT NULL, UNIQUE, AUTOINCREMENT, DEFAULT and CHECK) and records them on
// col so INSERT can enforce them. PRIMARY KEY is recorded as Unique only: it
// does not imply NOT NULL, keeping INTEGER PRIMARY KEY compatible with the
// pin set (SQLite reports a duplicate PRIMARY KEY as a UNIQUE constraint
// failure, and a PRIMARY KEY column — like a UNIQUE one — accepts multiple
// NULLs). AUTOINCREMENT is parsed and ignored (it only matters for rowid
// generation, which this engine does not model).
func (p *parser) parseColumnConstraints(col *Column) error {
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
			col.Unique = true
			col.PrimaryKey = true
		case "NOT":
			p.next()
			if !strings.EqualFold(p.peek().text, "NULL") {
				return &SQLError{Message: "expected NULL after NOT"}
			}
			p.next()
			col.NotNull = true
		case "NULL", "AUTOINCREMENT":
			p.next()
		case "UNIQUE":
			p.next()
			col.Unique = true
		case "DEFAULT":
			p.next()
			e, err := p.parseDefaultValue()
			if err != nil {
				return err
			}
			if e != nil {
				col.Default = e
			}
		case "CHECK":
			p.next()
			if err := p.expectPunct("("); err != nil {
				return err
			}
			e, err := p.parseExpr()
			if err != nil {
				return err
			}
			if err := p.expectPunct(")"); err != nil {
				return err
			}
			col.Check = e
		default:
			return nil
		}
	}
}

// parseDefaultValue parses a DEFAULT clause value: a signed literal (number,
// string, blob or NULL) or a parenthesised expression, matching SQLite. The
// expression is evaluated at INSERT time against an empty row, so it must be
// a constant (column references are not allowed, as in SQLite).
func (p *parser) parseDefaultValue() (Expr, error) {
	if p.peek().text == "(" {
		p.next()
		e, err := p.parseExpr()
		if err != nil {
			return nil, err
		}
		if err := p.expectPunct(")"); err != nil {
			return nil, err
		}
		return e, nil
	}
	// A bare literal is wrapped in a LiteralExpr so evaluation yields its
	// value directly. A leading sign is folded into the literal value.
	v, err := p.parseValue()
	if err != nil {
		return nil, err
	}
	return &LiteralExpr{val: v}, nil
}

// parseCreate handles CREATE TABLE (with column-level PRIMARY KEY / NOT NULL /
// UNIQUE / DEFAULT / CHECK constraints and table-level PRIMARY KEY / UNIQUE
// constraints) and CREATE [UNIQUE] INDEX. SQL text is captured on the Table /
// Index so sqlite_master can report definitions. CREATE VIEW stays unsupported.
func parseCreate(p *parser, tbls map[string]*Table, idxs map[string]*Index) (*Result, error) {
	p.next() // CREATE
	kw := strings.ToUpper(p.peek().text)
	switch kw {
	case "TABLE":
		p.next()
	case "INDEX":
		// peek is at INDEX; parseCreateIndex consumes it.
		return parseCreateIndex(p, idxs, false)
	case "UNIQUE":
		p.next() // consume UNIQUE
		return parseCreateIndex(p, idxs, true)
	case "VIEW":
		return nil, &UnsupportedError{Feature: "CREATE VIEW"}
	default:
		return nil, &SQLError{Message: "expected TABLE after CREATE"}
	}
	name, err := p.expectIdent()
	if err != nil {
		return nil, err
	}
	sqlStart := p.pos
	if err := p.expectPunct("("); err != nil {
		return nil, err
	}
	var cols []Column
	var pkCols []string // table-level PRIMARY KEY column list
	var uniqueCols []string
	for {
		// A table-level constraint appears as a keyword instead of a column name.
		kw := strings.ToUpper(p.peek().text)
		if p.peek().kind == tokIdent && (kw == "PRIMARY" || kw == "UNIQUE") {
			if kw == "PRIMARY" {
				p.next()
				if !strings.EqualFold(p.peek().text, "KEY") {
					return nil, &SQLError{Message: "expected KEY after PRIMARY"}
				}
				p.next()
				if err := p.expectPunct("("); err != nil {
					return nil, err
				}
				for {
					cn, err := p.expectIdent()
					if err != nil {
						return nil, err
					}
					pkCols = append(pkCols, cn)
					if p.peek().text == "," {
						p.next()
						continue
					}
					break
				}
				if err := p.expectPunct(")"); err != nil {
					return nil, err
				}
			} else { // UNIQUE
				p.next()
				if err := p.expectPunct("("); err != nil {
					return nil, err
				}
				for {
					cn, err := p.expectIdent()
					if err != nil {
						return nil, err
					}
					uniqueCols = append(uniqueCols, cn)
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
		} else {
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
			col := Column{Name: colName, Type: strings.ToUpper(typeName)}
			if err := p.parseColumnConstraints(&col); err != nil {
				return nil, err
			}
			cols = append(cols, col)
		}
		if p.peek().text == "," {
			p.next()
			continue
		}
		break
	}
	if err := p.expectPunct(")"); err != nil {
		return nil, err
	}
	// Resolve column affinities for CHECK expressions (they are evaluated at
	// INSERT time; resolution here matches how SELECT does it).
	for i := range cols {
		if cols[i].Check != nil {
			if err := resolveColumnAffinity(cols[i].Check, cols); err != nil {
				return nil, err
			}
		}
	}
	if err := p.checkTrailing(); err != nil {
		return nil, err
	}
	if _, exists := tbls[name]; exists {
		return nil, &SQLError{Message: fmt.Sprintf("table %q already exists", name)}
	}
	// Collect table-level PRIMARY KEY/UNIQUE into composite key groups. A
	// single-column group is equivalent to marking that column Unique (SQLite
	// reports the duplicate as UNIQUE constraint failed); a multi-column group
	// is unique as a whole and enforced in parseInsert against the group.
	var uniqueKeys [][]string
	for _, g := range [][]string{pkCols, uniqueCols} {
		if len(g) == 0 {
			continue
		}
		for _, cn := range g {
			if tableColumnSeq(cols).index(cn) < 0 {
				return nil, &SQLError{Message: fmt.Sprintf("no such column: %s", cn)}
			}
		}
		if len(g) == 1 {
			if i := tableColumnSeq(cols).index(g[0]); i >= 0 {
				cols[i].Unique = true
				continue
			}
		}
		uniqueKeys = append(uniqueKeys, g)
	}
	sqlText := renderCreateTableSQL(p, sqlStart, name, cols, uniqueKeys)
	tbls[name] = &Table{Name: name, Columns: cols, UniqueKeys: uniqueKeys, SQL: sqlText}
	return &Result{}, nil
}

// tableColumnSeq is a small helper view over []Column for name lookup.
type tableColumnSeq []Column

func (s tableColumnSeq) index(name string) int {
	for i, c := range s {
		if strings.EqualFold(c.Name, name) {
			return i
		}
	}
	return -1
}

// parseCreateIndex handles the remainder of CREATE [UNIQUE] INDEX idx ON
// tbl (col [, ...]). The index is validated against the owning table and
// registered in idxs; it does not alter query results (the engine always
// scans), so it matters for sqlite_master introspection and UNIQUE enforcement.
func parseCreateIndex(p *parser, idxs map[string]*Index, unique bool) (*Result, error) {
	if !strings.EqualFold(p.peek().text, "INDEX") {
		return nil, &SQLError{Message: "expected INDEX"}
	}
	p.next()
	idxName, err := p.expectIdent()
	if err != nil {
		return nil, err
	}
	if !strings.EqualFold(p.peek().text, "ON") {
		return nil, &SQLError{Message: "expected ON after index name"}
	}
	p.next()
	tableName, err := p.expectIdent()
	if err != nil {
		return nil, err
	}
	if err := p.expectPunct("("); err != nil {
		return nil, err
	}
	var columns []string
	for {
		cn, err := p.expectIdent()
		if err != nil {
			return nil, err
		}
		columns = append(columns, cn)
		// Optional ASC/DESC sort order — parsed and ignored for enforcement
		// (the engine always scans, so ordering is metadata only).
		if p.isKeyword("ASC") || p.isKeyword("DESC") {
			p.next()
		}
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
	if _, exists := idxs[idxName]; exists {
		return nil, &SQLError{Message: fmt.Sprintf("index %q already exists", idxName)}
	}
	// Unique-ness must be enforced, so the owning table has to be known; the
	// catalog is global but the table may not have been committed yet inside
	// the same statement. We register the index regardless; enforcement looks
	// up the table by name at INSERT time.
	idxs[idxName] = &Index{Name: idxName, Table: tableName, Columns: columns, Unique: unique, SQL: renderCreateIndexSQL(p, idxName, tableName, columns, unique)}
	return &Result{}, nil
}

// renderCreateTableSQL synthesizes the original CREATE TABLE text from the
// parsed definition, so sqlite_master can report a stable definition even when
// the engine reconstructed it (e.g. after reload).
func renderCreateTableSQL(p *parser, sqlStart int, name string, cols []Column, uniqueKeys [][]string) string {
	var b strings.Builder
	b.WriteString("CREATE TABLE ")
	b.WriteString(name)
	b.WriteString(" (\n")
	for i, c := range cols {
		b.WriteString("  ")
		b.WriteString(c.Name)
		b.WriteString(" ")
		b.WriteString(c.Type)
		if c.NotNull {
			b.WriteString(" NOT NULL")
		}
		if c.PrimaryKey {
			b.WriteString(" PRIMARY KEY")
		} else if c.Unique {
			b.WriteString(" UNIQUE")
		}
		if c.Default != nil {
			b.WriteString(" DEFAULT ")
			b.WriteString(exprRender(c.Default))
		}
		if c.Check != nil {
			b.WriteString(" CHECK (")
			b.WriteString(exprRender(c.Check))
			b.WriteString(")")
		}
		if i < len(cols)-1 || len(uniqueKeys) > 0 {
			b.WriteString(",")
		}
		b.WriteString("\n")
	}
	for i, g := range uniqueKeys {
		b.WriteString("  UNIQUE (")
		for j, cn := range g {
			b.WriteString(cn)
			if j < len(g)-1 {
				b.WriteString(", ")
			}
		}
		b.WriteString(")")
		if i < len(uniqueKeys)-1 {
			b.WriteString(",")
		}
		b.WriteString("\n")
	}
	b.WriteString(")")
	return b.String()
}

// renderCreateIndexSQL synthesizes the original CREATE INDEX statement.
func renderCreateIndexSQL(p *parser, idxName, tableName string, columns []string, unique bool) string {
	var b strings.Builder
	b.WriteString("CREATE ")
	if unique {
		b.WriteString("UNIQUE ")
	}
	b.WriteString("INDEX ")
	b.WriteString(idxName)
	b.WriteString(" ON ")
	b.WriteString(tableName)
	b.WriteString(" (")
	for i, cn := range columns {
		b.WriteString(cn)
		if i < len(columns)-1 {
			b.WriteString(", ")
		}
	}
	b.WriteString(")")
	return b.String()
}

// parseAlter handles ALTER TABLE t RENAME TO new and ALTER TABLE t ADD COLUMN
// col def. RENAME rewrites the stored SQL (and re-points any indexes on t);
// ADD COLUMN appends the column and back-fills existing rows with the column's
// DEFAULT (or NULL when there is none), mirroring SQLite. Adding a NOT NULL
// column without a non-NULL default is rejected, as is adding a PRIMARY KEY.
func parseAlter(p *parser, tbls map[string]*Table, idxs map[string]*Index) (*Result, error) {
	p.next() // ALTER
	if !strings.EqualFold(p.peek().text, "TABLE") {
		return nil, &SQLError{Message: "expected TABLE after ALTER"}
	}
	p.next()
	name, err := p.expectIdent()
	if err != nil {
		return nil, err
	}
	tbl, ok := tbls[name]
	if !ok {
		return nil, &SQLError{Message: fmt.Sprintf("no such table: %s", name)}
	}
	kw := strings.ToUpper(p.peek().text)
	switch kw {
	case "RENAME":
		p.next()
		if !strings.EqualFold(p.peek().text, "TO") {
			return nil, &SQLError{Message: "expected TO after RENAME"}
		}
		p.next()
		newName, err := p.expectIdent()
		if err != nil {
			return nil, err
		}
		if err := p.checkTrailing(); err != nil {
			return nil, err
		}
		if newName == name {
			return nil, &SQLError{Message: fmt.Sprintf("table %q already exists", newName)}
		}
		if _, exists := tbls[newName]; exists {
			return nil, &SQLError{Message: fmt.Sprintf("table %q already exists", newName)}
		}
		tbl.Name = newName
		tbl.SQL = rewriteSQLTableName(tbl.SQL, name, newName)
		delete(tbls, name)
		tbls[newName] = tbl
		for _, ix := range idxs {
			if strings.EqualFold(ix.Table, name) {
				ix.Table = newName
				ix.SQL = rewriteSQLTableName(ix.SQL, name, newName)
			}
		}
		return &Result{}, nil
	case "ADD":
		p.next()
		if !strings.EqualFold(p.peek().text, "COLUMN") {
			return nil, &SQLError{Message: "expected COLUMN after ADD"}
		}
		p.next()
		colName, err := p.expectIdent()
		if err != nil {
			return nil, err
		}
		typeName, err := p.expectIdent()
		if err != nil {
			return nil, err
		}
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
		col := Column{Name: colName, Type: strings.ToUpper(typeName)}
		if err := p.parseColumnConstraints(&col); err != nil {
			return nil, err
		}
		if err := p.checkTrailing(); err != nil {
			return nil, err
		}
		// SQLite forbids adding a NOT NULL column that defaults to NULL, and
		// forbids ADD COLUMN with PRIMARY KEY entirely.
		if col.NotNull {
			dv := defaultForColumn(col)
			if dv.IsNull() {
				return nil, &SQLError{Message: fmt.Sprintf("Cannot add a NOT NULL column with default value NULL")}
			}
		}
		if isPrimaryKeyColumn(col) {
			return nil, &SQLError{Message: "Cannot add a PRIMARY KEY column"}
		}
		if col.Unique {
			return nil, &SQLError{Message: "Cannot add a UNIQUE column"}
		}
		tbl.Columns = append(tbl.Columns, col)
		// Back-fill existing rows with the new column's default.
		for i := range tbl.Rows {
			tbl.Rows[i] = append(tbl.Rows[i], defaultForColumn(col))
		}
		// Rewrite the stored definition so the added column survives reload:
		// persisted CREATE TABLE text is the single source of truth for the
		// constraint set, and without this the new column would be dropped
		// when loadTables re-parses the old SQL after a restart.
		tbl.SQL = renderCreateTableSQL(nil, 0, tbl.Name, tbl.Columns, tbl.UniqueKeys)
		return &Result{}, nil
	default:
		return nil, &SQLError{Message: "expected RENAME or ADD"}
	}
}

// parseStoredTableSQL re-parses a stored CREATE TABLE statement into its Table
// definition (columns and constraints only; rows are restored from the
// persisted rows). Used on reload so DEFAULT/CHECK/UNIQUE/PRIMARY KEY survive
// persistence — only the SQL text is stored, so re-parsing is the single
// source of truth for the full constraint set.
func parseStoredTableSQL(sql string) (*Table, error) {
	toks, err := tokenize(sql)
	if err != nil {
		return nil, err
	}
	p := &parser{toks: toks}
	p.next() // CREATE
	if !strings.EqualFold(p.peek().text, "TABLE") {
		return nil, &SQLError{Message: "not a CREATE TABLE statement"}
	}
	p.next()
	name, err := p.expectIdent()
	if err != nil {
		return nil, err
	}
	if err := p.expectPunct("("); err != nil {
		return nil, err
	}
	var cols []Column
	var uniqueKeys [][]string
	for {
		kw := strings.ToUpper(p.peek().text)
		if p.peek().kind == tokIdent && (kw == "PRIMARY" || kw == "UNIQUE") {
			p.next()
			if kw == "PRIMARY" {
				p.next() // KEY
			}
			if err := p.expectPunct("("); err != nil {
				return nil, err
			}
			var g []string
			for {
				cn, err := p.expectIdent()
				if err != nil {
					return nil, err
				}
				g = append(g, cn)
				if p.peek().text == "," {
					p.next()
					continue
				}
				break
			}
			if err := p.expectPunct(")"); err != nil {
				return nil, err
			}
			// Mirror parseCreate's folding: a single-column group marks that
			// column Unique; a multi-column group is unique as a whole.
			if len(g) == 1 {
				if i := tableColumnSeq(cols).index(g[0]); i >= 0 {
					cols[i].Unique = true
				}
			} else {
				uniqueKeys = append(uniqueKeys, g)
			}
		} else {
			colName, err := p.expectIdent()
			if err != nil {
				return nil, err
			}
			typeName, err := p.expectIdent()
			if err != nil {
				return nil, err
			}
			if p.peek().text == "(" {
				p.next()
				p.next() // size
				if err := p.expectPunct(")"); err != nil {
					return nil, err
				}
			}
			col := Column{Name: colName, Type: strings.ToUpper(typeName)}
			if err := p.parseColumnConstraints(&col); err != nil {
				return nil, err
			}
			cols = append(cols, col)
		}
		if p.peek().text == "," {
			p.next()
			continue
		}
		break
	}
	if err := p.expectPunct(")"); err != nil {
		return nil, err
	}
	return &Table{Name: name, Columns: cols, UniqueKeys: uniqueKeys, SQL: sql}, nil
}

// isPrimaryKeyColumn reports whether a column was declared PRIMARY KEY.
func isPrimaryKeyColumn(col Column) bool {
	return col.PrimaryKey
}

// rewriteSQLTableName replaces the first table-name occurrence in a stored SQL
// statement. For ALTER TABLE RENAME the statement text still carries the old
// name; this rewrites it so sqlite_master stays consistent.
func rewriteSQLTableName(sql, oldName, newName string) string {
	if sql == "" {
		return sql
	}
	idx := strings.Index(sql, oldName)
	if idx < 0 {
		return sql
	}
	return sql[:idx] + newName + sql[idx+len(oldName):]
}

// exprRender renders an expression back to SQL text for sqlite_master and for
// recording a DEFAULT value in the persisted definition. It must be able to
// reproduce every node the parser produces, because the persisted CREATE TABLE
// text is re-parsed on reload as the single source of truth for CHECK and
// expression DEFAULT constraints: a CHECK (c > 0) collapsed here to "(expr)"
// would reload as a bogus column reference and reject every row. Operator
// precedence is reproduced faithfully by emitting parenthesised operands so the
// re-parsed expression tree is identical.
func exprRender(e Expr) string {
	switch n := e.(type) {
	case *LiteralExpr:
		if n.val.IsNull() {
			return "NULL"
		}
		switch n.val.kind {
		case Text:
			return "'" + strings.ReplaceAll(n.val.textVal, "'", "''") + "'"
		case Blob:
			return "X'" + formatBlobHex(n.val.textVal) + "'"
		default:
			return n.val.RenderCLI()
		}
	case *ColumnExpr:
		return n.name
	case *CastExpr:
		return "CAST(" + exprRender(n.inner) + " AS " + affinityName(n.aff) + ")"
	case *ArithExpr:
		return "(" + exprRender(n.left) + " " + n.op + " " + exprRender(n.right) + ")"
	case *NegExpr:
		return "-(" + exprRender(n.inner) + ")"
	case *CompareExpr:
		if n.nullSafe {
			if n.op == "IS NOT" {
				return "(" + exprRender(n.left) + " IS NOT " + exprRender(n.right) + ")"
			}
			return "(" + exprRender(n.left) + " IS " + exprRender(n.right) + ")"
		}
		return "(" + exprRender(n.left) + " " + n.op + " " + exprRender(n.right) + ")"
	case *LogicalExpr:
		return "(" + exprRender(n.left) + " " + n.op + " " + exprRender(n.right) + ")"
	case *NotExpr:
		return "NOT (" + exprRender(n.inner) + ")"
	case *InExpr:
		var b strings.Builder
		b.WriteString("(")
		b.WriteString(exprRender(n.left))
		if n.negate {
			b.WriteString(" NOT IN (")
		} else {
			b.WriteString(" IN (")
		}
		for i, item := range n.list {
			if i > 0 {
				b.WriteString(", ")
			}
			b.WriteString(exprRender(item))
		}
		b.WriteString("))")
		return b.String()
	case *BetweenExpr:
		var b strings.Builder
		b.WriteString("(")
		b.WriteString(exprRender(n.left))
		if n.negate {
			b.WriteString(" NOT BETWEEN ")
		} else {
			b.WriteString(" BETWEEN ")
		}
		b.WriteString(exprRender(n.low))
		b.WriteString(" AND ")
		b.WriteString(exprRender(n.high))
		b.WriteString(")")
		return b.String()
	case *LikeExpr:
		var b strings.Builder
		b.WriteString("(")
		b.WriteString(exprRender(n.left))
		if n.negate {
			b.WriteString(" NOT ")
		} else {
			b.WriteString(" ")
		}
		if n.glob {
			b.WriteString("GLOB ")
		} else {
			b.WriteString("LIKE ")
		}
		b.WriteString(exprRender(n.pattern))
		if n.escape != nil {
			b.WriteString(" ESCAPE ")
			b.WriteString(exprRender(n.escape))
		}
		b.WriteString(")")
		return b.String()
	case *CountStarExpr:
		return "COUNT(*)"
	case *AggExpr:
		if n.star {
			return n.funcName + "(*)"
		}
		if n.distinct {
			return n.funcName + "(DISTINCT " + exprRender(n.arg) + ")"
		}
		return n.funcName + "(" + exprRender(n.arg) + ")"
	default:
		// Aggregate slots and any unknown node carry no renderable SQL text.
		// They never appear in a CHECK/DEFAULT expression, whose grammar is the
		// constant-expression subset.
		return "(expr)"
	}
}

// formatBlobHex renders blob bytes as the uppercase hex used in SQLite blob
// literals.
func formatBlobHex(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		b.WriteString(fmt.Sprintf("%02X", s[i]))
	}
	return b.String()
}

// affinityName renders an Affinity back to the SQLite type keyword it
// corresponds to, for CAST expression round-tripping.
func affinityName(a Affinity) string {
	switch a {
	case AffInteger:
		return "INTEGER"
	case AffReal:
		return "REAL"
	case AffText:
		return "TEXT"
	case AffBlob:
		return "BLOB"
	default:
		return ""
	}
}

// parseInsert handles INSERT INTO t [(cols)] VALUES (...). Values are
// converted to the target column's affinity, matching SQLite's storage rules.
// Column-level constraints (NOT NULL, CHECK, UNIQUE/PRIMARY KEY) and UNIQUE
// INDEXes are enforced here, so a failing INSERT leaves the table unchanged
// (statement-level atomicity in conn.go discards the whole working copy).
func parseInsert(p *parser, tbls map[string]*Table, idxs map[string]*Index) (*Result, error) {
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
	tbl, ok := tbls[name]
	if !ok {
		return nil, &SQLError{Message: fmt.Sprintf("no such table: %s", name)}
	}
	if !strings.EqualFold(p.peek().text, "VALUES") {
		return nil, &UnsupportedError{Feature: "INSERT ... SELECT"}
	}
	p.next()
	if err := p.expectPunct("("); err != nil {
		return nil, err
	}
	// VALUES may contain the literal DEFAULT to request a column's default.
	var vals []Value
	colIdx := 0
	for {
		if strings.EqualFold(p.peek().text, "DEFAULT") {
			// DEFAULT fills the value from the column default (or NULL if the
			// column has none). Target column is resolved by position.
			p.next()
			if len(colNames) == 0 {
				if colIdx >= len(tbl.Columns) {
					return nil, &SQLError{Message: "INSERT has more expressions than target columns"}
				}
				vals = append(vals, defaultForColumn(tbl.Columns[colIdx]))
			} else {
				di := tbl.columnIndex(colNames[colIdx])
				if di < 0 {
					return nil, &SQLError{Message: fmt.Sprintf("no such column: %s", colNames[colIdx])}
				}
				vals = append(vals, defaultForColumn(tbl.Columns[di]))
			}
			colIdx++
		} else {
			v, err := p.parseValue()
			if err != nil {
				return nil, err
			}
			vals = append(vals, v)
			colIdx++
		}
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
	if len(colNames) == 0 {
		if len(vals) != len(tbl.Columns) {
			return nil, &SQLError{Message: fmt.Sprintf("table %s has %d columns but %d values were supplied", name, len(tbl.Columns), len(vals))}
		}
	} else if len(vals) != len(colNames) {
		return nil, &SQLError{Message: "INSERT has more expressions than target columns"}
	}
	row := make([]Value, len(tbl.Columns))
	// Start every row from its column defaults, then overlay supplied values.
	// Columns with no DEFAULT and no supplied value stay NULL, matching SQLite.
	for i := range tbl.Columns {
		row[i] = defaultForColumn(tbl.Columns[i])
	}
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
	// Enforce NOT NULL, CHECK, and UNIQUE/PRIMARY KEY constraints.
	for i := range tbl.Columns {
		if tbl.Columns[i].NotNull && row[i].IsNull() {
			return nil, &SQLError{Message: fmt.Sprintf("NOT NULL constraint failed: %s.%s", name, tbl.Columns[i].Name)}
		}
		if tbl.Columns[i].Check != nil {
			if !checkHolds(tbl.Columns[i].Check, row, tbl.Columns) {
				return nil, &SQLError{Message: fmt.Sprintf("CHECK constraint failed: %s", name)}
			}
		}
	}
	// UNIQUE (column) and PRIMARY KEY reject a duplicate value in a non-NULL
	// column; NULLs never conflict. Report as a UNIQUE constraint failure,
	// matching SQLite's wording.
	for i := range tbl.Columns {
		if tbl.Columns[i].Unique && !row[i].IsNull() {
			if columnHasEqualValue(tbl, tbl.Columns[i].Name, row) {
				return nil, &SQLError{Message: fmt.Sprintf("UNIQUE constraint failed: %s.%s", name, tbl.Columns[i].Name)}
			}
		}
	}
	// A table-level PRIMARY KEY / UNIQUE group (composite key) is unique as a
	// whole: the row conflicts only when every member of the group matches an
	// existing row. NULL in any member never conflicts, as in SQLite.
	for _, g := range tbl.UniqueKeys {
		pos := make([]int, len(g))
		valid := true
		for j, cn := range g {
			pi := tbl.columnIndex(cn)
			if pi < 0 {
				valid = false
				break
			}
			pos[j] = pi
		}
		if !valid || indexKeyHasNull(pos, row) {
			continue
		}
		if indexHasEqualRow(tbl, pos, row) {
			return nil, &SQLError{Message: fmt.Sprintf("UNIQUE constraint failed: %s.%s", name, strings.Join(g, ", "))}
		}
	}
	// A UNIQUE INDEX enforces the same rule across its whole column list. As in
	// SQLite, a NULL in any indexed column means the row never conflicts
	// (NULLs are distinct). The row is not yet in tbl.Rows, so the scan below
	// only sees existing rows — exactly what duplicates must be rejected against.
	for _, ix := range idxs {
		if !ix.Unique || !strings.EqualFold(ix.Table, name) {
			continue
		}
		idxCols, ok := indexColumnPositions(tbl, ix.Columns)
		if !ok {
			continue
		}
		if indexKeyHasNull(idxCols, row) {
			continue
		}
		if indexHasEqualRow(tbl, idxCols, row) {
			return nil, &SQLError{Message: fmt.Sprintf("UNIQUE constraint failed: %s.%s", name, ix.Name)}
		}
	}
	tbl.Rows = append(tbl.Rows, row)
	return &Result{Affected: 1}, nil
}

// checkHolds evaluates a CHECK constraint for a row. As in SQLite, a NULL
// evaluation result means the constraint is satisfied (NOT NULL rejects NULL,
// but CHECK does not), so NULL returns true.
func checkHolds(check Expr, row []Value, cols []Column) bool {
	v, err := check.eval(row, cols)
	if err != nil {
		return false
	}
	if v.kind == Null {
		return true
	}
	return v.kind == Int && v.intVal != 0
}

// defaultForColumn returns the column's DEFAULT value, evaluated, or NULL when
// the column has no DEFAULT.
func defaultForColumn(col Column) Value {
	if col.Default == nil {
		return NullValue()
	}
	v, err := col.Default.eval(nil, nil)
	if err == nil {
		return v
	}
	return NullValue()
}

// columnHasEqualValue reports whether any existing row already has the same
// value as row in the named column (used to enforce UNIQUE/PRIMARY KEY on
// INSERT).
func columnHasEqualValue(tbl *Table, colName string, row []Value) bool {
	idx := tbl.columnIndex(colName)
	if idx < 0 {
		return false
	}
	for _, r := range tbl.Rows {
		if compareValues(r[idx], row[idx]) == 0 {
			return true
		}
	}
	return false
}

// indexColumnPositions maps an index's column names to positions in the table,
// reporting false if any column is unknown (shouldn't happen: CREATE INDEX
// references a committed table).
func indexColumnPositions(tbl *Table, cols []string) ([]int, bool) {
	pos := make([]int, len(cols))
	for i, cn := range cols {
		p := tbl.columnIndex(cn)
		if p < 0 {
			return nil, false
		}
		pos[i] = p
	}
	return pos, true
}

// indexKeyHasNull reports whether any column of the index key is NULL in row
// (a NULL in a UNIQUE INDEX column means the row never conflicts, as in
// SQLite).
func indexKeyHasNull(pos []int, row []Value) bool {
	for _, p := range pos {
		if row[p].IsNull() {
			return true
		}
	}
	return false
}

// indexHasEqualRow reports whether any existing row has a key equal to row's
// key over the given index column positions.
func indexHasEqualRow(tbl *Table, pos []int, row []Value) bool {
	for _, r := range tbl.Rows {
		equal := true
		for _, p := range pos {
			if compareValues(r[p], row[p]) != 0 {
				equal = false
				break
			}
		}
		if equal {
			return true
		}
	}
	return false
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

// buildSQLiteMaster synthesizes the sqlite_master virtual table: one row per
// table and index with type/name/tbl_name/sql columns, matching SQLite's schema
// table. It is built on demand and never stored in tbls, keeping DROP TABLE /
// DROP INDEX consistent with what introspection reports.
func buildSQLiteMaster(tbls map[string]*Table, idxs map[string]*Index) *Table {
	rows := make([][]Value, 0, len(tbls)+len(idxs))
	for _, t := range tbls {
		sql := t.SQL
		if sql == "" {
			sql = renderCreateTableSQL(nil, 0, t.Name, t.Columns, t.UniqueKeys)
		}
		rows = append(rows, []Value{TextValue("table"), TextValue(t.Name), TextValue(t.Name), IntValue(0), TextValue(sql)})
	}
	for _, ix := range idxs {
		rows = append(rows, []Value{TextValue("index"), TextValue(ix.Name), TextValue(ix.Table), IntValue(0), TextValue(ix.SQL)})
	}
	return &Table{
		Name: "sqlite_master",
		Columns: []Column{
			{Name: "type", Type: "TEXT"},
			{Name: "name", Type: "TEXT"},
			{Name: "tbl_name", Type: "TEXT"},
			{Name: "rootpage", Type: "INT"},
			{Name: "sql", Type: "TEXT"},
		},
		Rows: rows,
	}
}

// parseSelect handles a full single-table SELECT: select list, FROM+WHERE,
// GROUP BY, HAVING, ORDER BY (ordinal or expression keys, ASC/DESC, NULL
// placement), LIMIT/OFFSET and DISTINCT. An aggregate query (one with GROUP BY
// or an aggregate in the select list or HAVING) computes one output row per
// group; a without-FROM SELECT supplies a single constant row so COUNT(*)=1.
func parseSelect(p *parser, tbls map[string]*Table, idxs map[string]*Index) (*Result, error) {
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
		tbl, ok = tbls[name]
		if !ok && strings.EqualFold(name, "sqlite_master") {
			tbl = buildSQLiteMaster(tbls, idxs)
		}
		if tbl == nil {
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
func parseUpdate(p *parser, tbls map[string]*Table) (*Result, error) {
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
	tbl, ok := tbls[name]
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
	// Copy-on-write: every updated row is built on a private copy and swapped
	// in only when the whole statement succeeds, so a failed evaluation can't
	// leave a partially-mutated row behind (statement-level atomicity).
	var updated [][]Value
	var affected int64
	for _, row := range tbl.Rows {
		if cond != nil && !condTrue(cond, row, tbl.Columns) {
			updated = append(updated, row)
			continue
		}
		cp := append([]Value(nil), row...)
		for i, a := range assigns {
			v, err := a.val.eval(cp, tbl.Columns)
			if err != nil {
				return nil, err
			}
			cp[idxs[i]] = v
		}
		updated = append(updated, cp)
		affected++
	}
	tbl.Rows = updated
	return &Result{Affected: affected}, nil
}

// parseDelete handles DELETE FROM t [WHERE expr].
func parseDelete(p *parser, tbls map[string]*Table) (*Result, error) {
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
	tbl, ok := tbls[name]
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

// parseDrop handles DROP TABLE t and DROP INDEX i (CREATE VIEW is unsupported,
// so DROP VIEW stays too). Dropping a table also drops every index on it.
func parseDrop(p *parser, tbls map[string]*Table, idxs map[string]*Index) (*Result, error) {
	p.next() // DROP
	kw := strings.ToUpper(p.peek().text)
	switch kw {
	case "TABLE":
		p.next()
	case "INDEX":
		p.next()
		name, err := p.expectIdent()
		if err != nil {
			return nil, err
		}
		if err := p.checkTrailing(); err != nil {
			return nil, err
		}
		if _, ok := idxs[name]; !ok {
			return nil, &SQLError{Message: fmt.Sprintf("no such index: %s", name)}
		}
		delete(idxs, name)
		return &Result{}, nil
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
	if _, ok := tbls[name]; !ok {
		return nil, &SQLError{Message: fmt.Sprintf("no such table: %s", name)}
	}
	delete(tbls, name)
	for ixName, ix := range idxs {
		if strings.EqualFold(ix.Table, name) {
			delete(idxs, ixName)
		}
	}
	return &Result{}, nil
}
