// Package sqllogictest implements a runner for the sqllogictest format:
// it parses .test files, executes each statement/query against the engine,
// and compares the output with the expected results in full and hash modes.
package sqllogictest

import (
	"fmt"
	"os"
	"strings"
)

// Record is one statement or query record from a .test file.
type Record struct {
	Line     int
	Kind     string // "statement" or "query"
	ExpectOK bool   // statement: ok (true) or error (false)
	IsError  bool   // query: "query error" record
	TypeStr  string // query: column type string, e.g. "I", "IT"
	SortMode string // query: nosort, rowsort, valuesort
	Label    string // query: optional hash label
	SQL      string
	Expected []string // full-mode expected values, one per line
	HashLine string   // hash-mode expected line, e.g. "30 values hashing to <md5>"
}

// TestFile is a parsed .test file.
type TestFile struct {
	Path    string
	Records []*Record
}

// ParseFile reads and parses a .test file.
func ParseFile(path string) (*TestFile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	lines := strings.Split(string(data), "\n")
	tf := &TestFile{Path: path}
	i, n := 0, len(lines)
	for i < n {
		line := strings.TrimRight(lines[i], "\r")
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			i++
			continue
		}
		fields := strings.Fields(trimmed)
		switch fields[0] {
		case "statement":
			rec := &Record{Line: i + 1, Kind: "statement", ExpectOK: true}
			if len(fields) >= 2 {
				switch fields[1] {
				case "ok":
					rec.ExpectOK = true
				case "error":
					rec.ExpectOK = false
				default:
					return nil, fmt.Errorf("%s:%d: unknown statement expectation %q", path, i+1, fields[1])
				}
			}
			j := i + 1
			var sql []string
			for j < n && strings.TrimSpace(lines[j]) != "" {
				sql = append(sql, lines[j])
				j++
			}
			rec.SQL = strings.Join(sql, "\n")
			tf.Records = append(tf.Records, rec)
			i = j
		case "query":
			rec := &Record{Line: i + 1, Kind: "query", SortMode: "nosort"}
			if len(fields) >= 2 {
				if fields[1] == "error" {
					rec.IsError = true
				} else {
					rec.TypeStr = fields[1]
				}
			}
			if len(fields) >= 3 {
				rec.SortMode = fields[2]
			}
			if len(fields) >= 4 {
				rec.Label = fields[3]
			}
			j := i + 1
			var sql []string
			for j < n && !strings.HasPrefix(lines[j], "----") && strings.TrimSpace(lines[j]) != "" {
				sql = append(sql, lines[j])
				j++
			}
			rec.SQL = strings.Join(sql, "\n")
			if j < n && strings.HasPrefix(lines[j], "----") {
				j++
			}
			var exp []string
			for j < n && strings.TrimSpace(lines[j]) != "" {
				exp = append(exp, strings.TrimRight(lines[j], "\r"))
				j++
			}
			if len(exp) == 1 && strings.Contains(exp[0], "values hashing to") {
				rec.HashLine = exp[0]
			} else {
				rec.Expected = exp
			}
			tf.Records = append(tf.Records, rec)
			i = j
		case "hash-threshold":
			i++
		case "halt":
			i = n
		case "skipif", "onlyif":
			// Conditional records are not supported; skip the whole record.
			j := i + 1
			for j < n && strings.TrimSpace(lines[j]) != "" {
				j++
			}
			i = j
		case "forget":
			i++
		default:
			return nil, fmt.Errorf("%s:%d: unknown record type %q", path, i+1, fields[0])
		}
	}
	return tf, nil
}
