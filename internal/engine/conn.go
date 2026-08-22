package engine

import (
	"strings"
	"sync"
)

// Engine is the shared database store. Committed state is kept in tables,
// guarded by lock. Committed Table values are never mutated in place — every
// write works on a private copy and commits by replacing the map — so readers
// can snapshot the map and read the tables without holding the lock.
//
// The write lock implements mutual exclusion: at most one connection may hold
// it at a time. A write transaction takes it on its first writing statement and
// keeps it until COMMIT/ROLLBACK; a second concurrent writer immediately gets
// "database is locked" instead of blocking, matching SQLite without
// busy_timeout.
type Engine struct {
	lock    sync.Mutex
	tables  map[string]*Table // committed state
	indexes map[string]*Index // committed index catalog (keyed by index name)
	dbPath  string            // persistent database file; "" = in-memory

	writerL sync.Mutex
	writer  *Conn // connection holding the write lock, nil if none

	defaultConn *Conn
}

// New returns an empty in-memory shared store.
func New() *Engine {
	return &Engine{tables: make(map[string]*Table), indexes: make(map[string]*Index)}
}

// Open loads a database previously persisted at path (crash recovery on
// restart). A path that does not exist yet starts empty.
func Open(path string) (*Engine, error) {
	e := New()
	e.dbPath = path
	loaded, loadedIdx, err := loadTables(path)
	if err != nil {
		return nil, err
	}
	e.lock.Lock()
	e.tables = loaded
	e.indexes = loadedIdx
	e.lock.Unlock()
	return e, nil
}

// Connect returns a new connection sharing this store.
func (e *Engine) Connect() *Conn {
	return &Conn{eng: e}
}

// Execute is the single-connection convenience entry point used by the CLI and
// the sqllogictest runner. The connection persists across statements so a BEGIN
// and a later COMMIT work; without an explicit transaction every statement runs
// under implicit autocommit.
func (e *Engine) Execute(sql string) (*Result, error) {
	if e.defaultConn == nil {
		e.defaultConn = &Conn{eng: e}
	}
	return e.defaultConn.Execute(sql)
}

// snapshot returns a view of the committed tables and indexes. Only the maps
// are copied; the committed values are immutable and safe to read without the
// lock.
func (e *Engine) snapshot() (map[string]*Table, map[string]*Index) {
	e.lock.Lock()
	defer e.lock.Unlock()
	tm := make(map[string]*Table, len(e.tables))
	for name, t := range e.tables {
		tm[name] = t
	}
	im := make(map[string]*Index, len(e.indexes))
	for name, i := range e.indexes {
		im[name] = i
	}
	return tm, im
}

// cloneState deep-copies a table and index set so a transaction can mutate its
// own working copy without disturbing the committed state.
func cloneState(tables map[string]*Table, indexes map[string]*Index) (map[string]*Table, map[string]*Index) {
	td := make(map[string]*Table, len(tables))
	for name, t := range tables {
		td[name] = t.clone()
	}
	id := make(map[string]*Index, len(indexes))
	for name, i := range indexes {
		cp := *i
		cp.Columns = append([]string(nil), i.Columns...)
		id[name] = &cp
	}
	return td, id
}

func (t *Table) clone() *Table {
	cp := &Table{Name: t.Name, Columns: make([]Column, len(t.Columns)), SQL: t.SQL}
	copy(cp.Columns, t.Columns)
	cp.Rows = make([][]Value, len(t.Rows))
	for i := range t.Rows {
		cp.Rows[i] = append([]Value(nil), t.Rows[i]...)
	}
	cp.UniqueKeys = make([][]string, len(t.UniqueKeys))
	for i, g := range t.UniqueKeys {
		cp.UniqueKeys[i] = append([]string(nil), g...)
	}
	return cp
}

// Conn is one connection into a shared store. It carries its own transaction
// state, so concurrent connections see snapshot isolation: the committed state
// is immutable between commits, and an explicit transaction works on a private
// copy nobody else can see until COMMIT.
type Conn struct {
	eng *Engine
	tx  *txState // non-nil while a transaction (explicit or implicit) is open
}

// txState is the working copy a transaction operates on. A commit replaces the
// committed state with this copy; a rollback discards it.
type txState struct {
	tables    map[string]*Table
	indexes   map[string]*Index
	implicit  bool // autocommit transaction: one statement, closed immediately
	writeHeld bool
}

// Execute parses and runs a single SQL statement.
func (c *Conn) Execute(sql string) (*Result, error) {
	toks, err := tokenize(sql)
	if err != nil {
		return nil, err
	}
	p := &parser{toks: toks}
	switch strings.ToUpper(p.peek().text) {
	case "CREATE":
		return c.execWrite(func(tbls map[string]*Table, idx map[string]*Index) (*Result, error) {
			return parseCreate(p, tbls, idx)
		})
	case "INSERT":
		return c.execWrite(func(tbls map[string]*Table, idx map[string]*Index) (*Result, error) {
			return parseInsert(p, tbls, idx)
		})
	case "SELECT":
		return c.execRead(func(tbls map[string]*Table, idx map[string]*Index) (*Result, error) {
			return parseSelect(p, tbls, idx)
		})
	case "UPDATE":
		return c.execWrite(func(tbls map[string]*Table, idx map[string]*Index) (*Result, error) {
			return parseUpdate(p, tbls)
		})
	case "DELETE":
		return c.execWrite(func(tbls map[string]*Table, idx map[string]*Index) (*Result, error) {
			return parseDelete(p, tbls)
		})
	case "ALTER":
		return c.execWrite(func(tbls map[string]*Table, idx map[string]*Index) (*Result, error) {
			return parseAlter(p, tbls, idx)
		})
	case "DROP":
		return c.execWrite(func(tbls map[string]*Table, idx map[string]*Index) (*Result, error) {
			return parseDrop(p, tbls, idx)
		})
	case "BEGIN":
		return c.execBegin(p)
	case "COMMIT":
		return c.execCommit(p)
	case "ROLLBACK":
		return c.execRollback(p)
	case "":
		return nil, &SQLError{Message: "empty statement"}
	default:
		if f, ok := unsupportedKeywords[strings.ToUpper(p.peek().text)]; ok {
			return nil, &UnsupportedError{Feature: f}
		}
		return nil, &SQLError{Message: "syntax error near " + p.peek().text}
	}
}

// execRead runs a read-only statement against the table set this connection
// currently sees: its own transaction copy if inside one, else the committed
// state. Reads never take the write lock.
func (c *Conn) execRead(run func(map[string]*Table, map[string]*Index) (*Result, error)) (*Result, error) {
	if c.tx != nil && !c.tx.implicit {
		return run(c.tx.tables, c.tx.indexes)
	}
	return run(c.eng.snapshot())
}

// execWrite runs a mutating statement. Inside an explicit transaction it works
// on the transaction's copy; otherwise an implicit transaction is opened for
// the single statement and committed on success / discarded on failure, which
// gives statement-level atomicity. Writes take the shared write lock.
func (c *Conn) execWrite(run func(map[string]*Table, map[string]*Index) (*Result, error)) (*Result, error) {
	tbls, idx, err := c.writeTables()
	if err != nil {
		return nil, err
	}
	res, err := run(tbls, idx)
	if err != nil {
		// A failed statement leaves no partial change: in autocommit discard
		// the working copy and release the write lock.
		if c.tx != nil && c.tx.implicit {
			c.releaseWrite()
			c.tx = nil
		}
		return nil, err
	}
	if c.tx != nil && c.tx.implicit {
		if err := c.flush(); err != nil {
			return nil, err
		}
	}
	return res, nil
}

// writeTables returns the table and index set a writing statement operates on,
// taking the write lock. Inside an explicit transaction this is the
// transaction copy; in autocommit a throwaway transaction is opened for this
// one statement.
func (c *Conn) writeTables() (map[string]*Table, map[string]*Index, error) {
	if c.tx != nil && !c.tx.implicit {
		if err := c.acquireWrite(); err != nil {
			return nil, nil, err
		}
		return c.tx.tables, c.tx.indexes, nil
	}
	if err := c.acquireWrite(); err != nil {
		return nil, nil, err
	}
	tt, ii := c.eng.snapshot()
	ct, ci := cloneState(tt, ii)
	c.tx = &txState{tables: ct, indexes: ci, implicit: true, writeHeld: true}
	return c.tx.tables, c.tx.indexes, nil
}

// acquireWrite takes the write lock for this connection. It is idempotent for
// the same connection and fails with "database is locked" if another
// connection holds it.
func (c *Conn) acquireWrite() error {
	c.eng.writerL.Lock()
	defer c.eng.writerL.Unlock()
	if c.eng.writer == nil || c.eng.writer == c {
		c.eng.writer = c
		return nil
	}
	return &SQLError{Message: "database is locked"}
}

// releaseWrite drops the write lock if this connection holds it.
func (c *Conn) releaseWrite() {
	c.eng.writerL.Lock()
	defer c.eng.writerL.Unlock()
	if c.eng.writer == c {
		c.eng.writer = nil
	}
}

// flush commits the connection's current transaction: it replaces the
// committed state with the working copy, persists it (in persistent mode), and
// releases the write lock.
func (c *Conn) flush() error {
	e := c.eng
	e.lock.Lock()
	e.tables = c.tx.tables
	e.indexes = c.tx.indexes
	e.lock.Unlock()
	c.releaseWrite()
	c.tx = nil
	if e.dbPath != "" {
		return e.persist()
	}
	return nil
}

// execBegin starts an explicit transaction by snapshotting the committed state
// and cloning it, so the transaction can mutate its own private copy.
func (c *Conn) execBegin(p *parser) (*Result, error) {
	p.next() // BEGIN
	if err := p.checkTrailing(); err != nil {
		return nil, err
	}
	if c.tx != nil {
		return nil, &SQLError{Message: "cannot start a transaction within a transaction"}
	}
	tt, ii := c.eng.snapshot()
	ct, ci := cloneState(tt, ii)
	c.tx = &txState{tables: ct, indexes: ci}
	return &Result{}, nil
}

// execCommit ends the explicit transaction and makes its changes visible.
func (c *Conn) execCommit(p *parser) (*Result, error) {
	p.next() // COMMIT
	if err := p.checkTrailing(); err != nil {
		return nil, err
	}
	if c.tx == nil {
		return nil, &SQLError{Message: "cannot commit - no transaction is active"}
	}
	if err := c.flush(); err != nil {
		return nil, err
	}
	return &Result{}, nil
}

// execRollback discards the explicit transaction's changes. With no active
// transaction it is a no-op, matching SQLite.
func (c *Conn) execRollback(p *parser) (*Result, error) {
	p.next() // ROLLBACK
	if err := p.checkTrailing(); err != nil {
		return nil, err
	}
	if c.tx == nil {
		return &Result{}, nil
	}
	c.releaseWrite()
	c.tx = nil
	return &Result{}, nil
}
