package engine

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// pValue, pCol, pTable and pDB are JSON-friendly shadows of the engine's
// internal types, since Value's fields are unexported. Each commit serializes
// the full committed state and writes it durably (temp file + fsync + atomic
// rename), which gives crash recovery: a committed transaction's bytes are on
// disk before the process can be killed, while an uncommitted transaction never
// reaches the file.

type pValue struct {
	Kind  string // "null", "int", "float", "text", "blob"
	Int   int64
	Float float64
	Text  string // base64 payload for text and blob (arbitrary bytes)
}

func toPValue(v Value) pValue {
	switch v.kind {
	case Int:
		return pValue{Kind: "int", Int: v.intVal}
	case Float:
		return pValue{Kind: "float", Float: v.floatVal}
	case Text:
		return pValue{Kind: "text", Text: base64.StdEncoding.EncodeToString([]byte(v.textVal))}
	case Blob:
		return pValue{Kind: "blob", Text: base64.StdEncoding.EncodeToString([]byte(v.textVal))}
	default:
		return pValue{Kind: "null"}
	}
}

func fromPValue(p pValue) Value {
	switch p.Kind {
	case "int":
		return IntValue(p.Int)
	case "float":
		return FloatValue(p.Float)
	case "text":
		if b, err := base64.StdEncoding.DecodeString(p.Text); err == nil {
			return TextValue(string(b))
		}
		return TextValue(p.Text)
	case "blob":
		if b, err := base64.StdEncoding.DecodeString(p.Text); err == nil {
			return BlobValue(string(b))
		}
		return BlobValue(p.Text)
	default:
		return NullValue()
	}
}

type pCol struct {
	Name    string
	Type    string
	NotNull bool
	Unique  bool
	SQL     string // synthesized column constraint text for sqlite_master
}

type pTable struct {
	Name    string
	Columns []pCol
	Rows    [][]pValue
	SQL     string // original CREATE TABLE statement
}

type pIndex struct {
	Name    string
	Table   string
	Columns []string
	Unique  bool
	SQL     string // original CREATE INDEX statement
}

type pDB struct {
	Tables  map[string]pTable
	Indexes map[string]pIndex
}

// marshalTables serializes the committed state. JSON sorts map keys, so the
// output is stable regardless of table/index iteration order.
func marshalTables(tables map[string]*Table, indexes map[string]*Index) ([]byte, error) {
	db := pDB{Tables: make(map[string]pTable, len(tables)), Indexes: make(map[string]pIndex, len(indexes))}
	for name, t := range tables {
		pt := pTable{Name: t.Name, SQL: t.SQL, Columns: make([]pCol, len(t.Columns))}
		for i, c := range t.Columns {
			pt.Columns[i] = pCol{Name: c.Name, Type: c.Type, NotNull: c.NotNull, Unique: c.Unique}
		}
		pt.Rows = make([][]pValue, len(t.Rows))
		for i, row := range t.Rows {
			vals := make([]pValue, len(row))
			for j, v := range row {
				vals[j] = toPValue(v)
			}
			pt.Rows[i] = vals
		}
		db.Tables[name] = pt
	}
	for name, ix := range indexes {
		db.Indexes[name] = pIndex{Name: ix.Name, Table: ix.Table, Columns: append([]string(nil), ix.Columns...), Unique: ix.Unique, SQL: ix.SQL}
	}
	return json.Marshal(&db)
}

// loadTables reads a persisted database back into committed state. A missing
// file starts an empty database; a present but unparseable file is reported as
// corrupt.
func loadTables(path string) (map[string]*Table, map[string]*Index, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return make(map[string]*Table), make(map[string]*Index), nil
		}
		return nil, nil, err
	}
	var db pDB
	if err := json.Unmarshal(data, &db); err != nil {
		return nil, nil, fmt.Errorf("corrupt database file %s: %v", path, err)
	}
	tables := make(map[string]*Table, len(db.Tables))
	for name, pt := range db.Tables {
		// Re-parse the stored CREATE TABLE text so every constraint (DEFAULT,
		// CHECK, PRIMARY KEY, UNIQUE, NOT NULL) is reconstructed exactly, not
		// only the booleans persisted in pCol. Falls back to the persisted
		// column metadata if the SQL text is missing.
		t := &Table{Name: pt.Name, SQL: pt.SQL, Columns: make([]Column, len(pt.Columns))}
		if pt.SQL != "" {
			if rt, err := parseStoredTableSQL(pt.SQL); err == nil {
				t.Columns = rt.Columns
			} else {
				for i, pc := range pt.Columns {
					t.Columns[i] = Column{Name: pc.Name, Type: pc.Type, NotNull: pc.NotNull, Unique: pc.Unique}
				}
			}
		} else {
			for i, pc := range pt.Columns {
				t.Columns[i] = Column{Name: pc.Name, Type: pc.Type, NotNull: pc.NotNull, Unique: pc.Unique}
			}
		}
		t.Rows = make([][]Value, len(pt.Rows))
		for i, prow := range pt.Rows {
			row := make([]Value, len(prow))
			for j, pv := range prow {
				row[j] = fromPValue(pv)
			}
			t.Rows[i] = row
		}
		tables[name] = t
	}
	indexes := make(map[string]*Index, len(db.Indexes))
	for name, pi := range db.Indexes {
		indexes[name] = &Index{Name: pi.Name, Table: pi.Table, Columns: append([]string(nil), pi.Columns...), Unique: pi.Unique, SQL: pi.SQL}
	}
	return tables, indexes, nil
}

// persist writes the committed state to the database file durably: serialize
// to a temp file, fsync, then atomically rename over the real path and fsync
// the directory. This is what makes a COMMIT survive kill -9.
func (e *Engine) persist() error {
	e.lock.Lock()
	data, err := marshalTables(e.tables, e.indexes)
	e.lock.Unlock()
	if err != nil {
		return err
	}
	dir := filepath.Dir(e.dbPath)
	tmp := filepath.Join(dir, "."+filepath.Base(e.dbPath)+".tmp")
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, e.dbPath); err != nil {
		os.Remove(tmp)
		return err
	}
	if d, err := os.Open(dir); err == nil {
		d.Sync()
		d.Close()
	}
	return nil
}
