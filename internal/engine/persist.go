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
	Name string
	Type string
}

type pTable struct {
	Name    string
	Columns []pCol
	Rows    [][]pValue
}

type pDB struct {
	Tables map[string]pTable
}

// marshalTables serializes the committed state. JSON sorts map keys, so the
// output is stable regardless of table iteration order.
func marshalTables(tables map[string]*Table) ([]byte, error) {
	db := pDB{Tables: make(map[string]pTable, len(tables))}
	for name, t := range tables {
		pt := pTable{Name: t.Name, Columns: make([]pCol, len(t.Columns))}
		for i, c := range t.Columns {
			pt.Columns[i] = pCol{Name: c.Name, Type: c.Type}
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
	return json.Marshal(&db)
}

// loadTables reads a persisted database back into committed state. A missing
// file starts an empty database; a present but unparseable file is reported as
// corrupt.
func loadTables(path string) (map[string]*Table, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return make(map[string]*Table), nil
		}
		return nil, err
	}
	var db pDB
	if err := json.Unmarshal(data, &db); err != nil {
		return nil, fmt.Errorf("corrupt database file %s: %v", path, err)
	}
	tables := make(map[string]*Table, len(db.Tables))
	for name, pt := range db.Tables {
		t := &Table{Name: pt.Name, Columns: make([]Column, len(pt.Columns))}
		for i, pc := range pt.Columns {
			t.Columns[i] = Column{Name: pc.Name, Type: pc.Type}
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
	return tables, nil
}

// persist writes the committed state to the database file durably: serialize
// to a temp file, fsync, then atomically rename over the real path and fsync
// the directory. This is what makes a COMMIT survive kill -9.
func (e *Engine) persist() error {
	e.lock.Lock()
	data, err := marshalTables(e.tables)
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
