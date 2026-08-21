package sqllogictest

import (
	"crypto/md5"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/zhchxiao123/sql-db-0821/internal/engine"
)

// Options controls runner behaviour.
type Options struct {
	// Strict counts unsupported (waived) records as failures. Off by
	// default: records that use constructs outside the minimal SQL subset
	// are reported as waived, not failed, and the README documents the
	// waiver policy.
	Strict bool
}

// Failure is one mismatched record.
type Failure struct {
	File    string
	Line    int
	Message string
}

// Stats aggregates the outcome of running one test file.
type Stats struct {
	Total    int
	Passed   int
	Failed   int
	Waived   int
	Failures []Failure
}

// RunFile parses path and runs every record against a fresh engine.
func RunFile(path string, opts Options) (*Stats, error) {
	tf, err := ParseFile(path)
	if err != nil {
		return nil, err
	}
	eng := engine.New()
	stats := &Stats{}
	for _, rec := range tf.Records {
		stats.Total++
		verdict, msg := runRecord(eng, rec)
		switch verdict {
		case "pass":
			stats.Passed++
		case "fail":
			stats.Failed++
			stats.Failures = append(stats.Failures, Failure{File: path, Line: rec.Line, Message: msg})
		case "waive":
			if opts.Strict {
				stats.Failed++
				stats.Failures = append(stats.Failures, Failure{File: path, Line: rec.Line, Message: "unsupported: " + msg})
			} else {
				stats.Waived++
			}
		}
	}
	return stats, nil
}

// runRecord executes one record and returns a verdict: "pass", "fail" or
// "waive" (unsupported construct outside the minimal subset).
func runRecord(eng *engine.Engine, rec *Record) (string, string) {
	res, err := eng.Execute(rec.SQL)
	if rec.Kind == "statement" {
		if rec.ExpectOK {
			if err != nil {
				if isUnsupported(err) {
					return "waive", err.Error()
				}
				return "fail", fmt.Sprintf("statement expected ok but failed: %v", err)
			}
			return "pass", ""
		}
		// statement error: any error is the expected outcome.
		if err != nil {
			return "pass", ""
		}
		return "fail", "statement expected error but succeeded"
	}
	// query record
	if err != nil {
		if isUnsupported(err) {
			return "waive", err.Error()
		}
		if rec.IsError {
			return "pass", ""
		}
		return "fail", fmt.Sprintf("query failed: %v", err)
	}
	if rec.IsError {
		return "fail", "query expected error but succeeded"
	}
	vals := renderResult(res, rec.TypeStr)
	switch rec.SortMode {
	case "rowsort":
		nCol := len(rec.TypeStr)
		if nCol == 0 {
			nCol = 1
		}
		sortRows(vals, nCol)
	case "valuesort":
		sort.Strings(vals)
	}
	if rec.HashLine != "" {
		actual := computeHash(vals)
		expected := extractHash(rec.HashLine)
		if expected == "" {
			return "fail", fmt.Sprintf("malformed hash line %q", rec.HashLine)
		}
		if actual != expected {
			return "fail", fmt.Sprintf("hash mismatch: expected %s got %s", expected, actual)
		}
		return "pass", ""
	}
	if len(vals) != len(rec.Expected) {
		return "fail", fmt.Sprintf("result count mismatch: expected %d values got %d", len(rec.Expected), len(vals))
	}
	for i := range vals {
		if vals[i] != rec.Expected[i] {
			return "fail", fmt.Sprintf("value mismatch at position %d: expected %q got %q", i, rec.Expected[i], vals[i])
		}
	}
	return "pass", ""
}

// renderResult flattens the result rows into sqllogictest values, one per
// line, rendered according to the query's column type string.
func renderResult(res *engine.Result, typeStr string) []string {
	var vals []string
	for _, row := range res.Rows {
		for i, v := range row {
			tc := byte('T')
			if i < len(typeStr) {
				tc = typeStr[i]
			}
			vals = append(vals, v.RenderSLT(tc))
		}
	}
	return vals
}

// sortRows sorts the flattened value list in row-major order, comparing rows
// lexicographically (strcmp on the rendered text), matching the reference
// runner's rowsort. Rows are copied out first so the sort cannot corrupt the
// source slice while it is being rewritten.
func sortRows(vals []string, nCol int) {
	nRows := len(vals) / nCol
	rows := make([][]string, nRows)
	for i := 0; i < nRows; i++ {
		row := make([]string, nCol)
		copy(row, vals[i*nCol:(i+1)*nCol])
		rows[i] = row
	}
	sort.Slice(rows, func(a, b int) bool {
		for i := 0; i < nCol; i++ {
			if rows[a][i] != rows[b][i] {
				return rows[a][i] < rows[b][i]
			}
		}
		return false
	})
	for i := 0; i < nRows; i++ {
		copy(vals[i*nCol:], rows[i])
	}
}

// computeHash computes the sqllogictest result hash: md5 over each value
// followed by a newline, in order.
func computeHash(vals []string) string {
	h := md5.New()
	for _, v := range vals {
		h.Write([]byte(v))
		h.Write([]byte("\n"))
	}
	return hex.EncodeToString(h.Sum(nil))
}

// extractHash parses "N values hashing to <md5>" and returns the md5.
func extractHash(line string) string {
	fields := strings.Fields(line)
	if len(fields) == 5 && fields[2] == "hashing" && fields[3] == "to" {
		if _, err := strconv.Atoi(fields[0]); err == nil {
			return fields[4]
		}
	}
	return ""
}

func isUnsupported(err error) bool {
	var ue *engine.UnsupportedError
	return errors.As(err, &ue)
}
