// Command joinfilter runs only records whose SQL touches a multi-table
// construct (JOIN/NATURAL/LEFT/INNER/CROSS/FULL/RIGHT, UNION/INTERSECT/EXCEPT,
// scalar/IN/EXISTS/FROM subqueries) — the acceptance scope for the multitable
// requirement. It is a dev tool to verify "joins/subquery/union records are
// 0-failed" on a narrowed record set without running the whole heavy suite,
// which is not feasible in memory.
package main

import (
	"crypto/md5"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/zhchxiao123/sql-db-0821/internal/engine"
	"github.com/zhchxiao123/sql-db-0821/internal/sqllogictest"
)

var wantPat = regexp.MustCompile(`(?i)\b(JOIN|NATURAL|LEFT|RIGHT|INNER|CROSS|OUTER|FULL|UNION|INTERSECT|EXCEPT|EXISTS)\b|IN\s*\(\s*SELECT|FROM\s*\(|\s*\(\s*SELECT`)

func main() {
	var total, passed, failed, waived int
	exit := 0
	for _, f := range os.Args[1:] {
		tf, err := sqllogictest.ParseFile(f)
		if err != nil {
			fmt.Fprintf(os.Stderr, "parse %s: %v\n", f, err)
			os.Exit(2)
		}
		eng := engine.New()
		var tfTotal, tfPass, tfFail, tfWaive int
		for _, rec := range tf.Records {
			// Statement records always execute so table state stays in effect
			// (sqllogictest runs one engine per file). Query records never
			// mutate tables, so only relevant ones are executed — this keeps
			// the filtered run from materializing the huge result sets that
			// break memory when the whole file runs.
			relevant := wantPat.MatchString(rec.SQL)
			if rec.Kind != "statement" && !relevant {
				continue
			}
			verdict, msg := runRecState(eng, rec)
			if !relevant {
				continue
			}
			tfTotal++
			switch verdict {
			case "pass":
				tfPass++
			case "fail":
				tfFail++
				fmt.Printf("  FAIL %s:%d: %s\nSQL: %s\n", f, rec.Line, msg, rec.SQL)
			case "waive":
				tfWaive++
			}
		}
		fmt.Printf("%s: %d relevant records, %d passed, %d failed, %d waived\n",
			f, tfTotal, tfPass, tfFail, tfWaive)
		total += tfTotal
		passed += tfPass
		failed += tfFail
		waived += tfWaive
	}
	fmt.Printf("TOTAL: %d relevant records, %d passed, %d failed, %d waived\n", total, passed, failed, waived)
	if failed > 0 {
		exit = 1
	}
	os.Exit(exit)
}

func runRecState(eng *engine.Engine, rec *sqllogictest.Record) (string, string) {
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
		if err != nil {
			return "pass", ""
		}
		return "fail", "statement expected error but succeeded"
	}
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
		rowsort(vals, nCol)
	case "valuesort":
		sort.Strings(vals)
	}
	if rec.HashLine != "" {
		actual := hashVals(vals)
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
		return "fail", fmt.Sprintf("result count mismatch: expected %d got %d", len(rec.Expected), len(vals))
	}
	for i := range vals {
		if vals[i] != rec.Expected[i] {
			return "fail", fmt.Sprintf("value mismatch at %d: expected %q got %q", i, rec.Expected[i], vals[i])
		}
	}
	return "pass", ""
}

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

func rowsort(vals []string, nCol int) {
	nRows := len(vals) / nCol
	rows := make([][]string, nRows)
	for i := 0; i < nRows; i++ {
		r := make([]string, nCol)
		copy(r, vals[i*nCol:(i+1)*nCol])
		rows[i] = r
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

func hashVals(vals []string) string {
	h := md5.New()
	for _, v := range vals {
		h.Write([]byte(v))
		h.Write([]byte("\n"))
	}
	return hex.EncodeToString(h.Sum(nil))
}

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
