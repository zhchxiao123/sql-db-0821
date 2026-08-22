// Command sqldb is the command-line entry point of the minimal SQL engine.
// It reads a sequence of SQL statements from stdin (separated by ';'),
// executes them, and prints SELECT results in the sqlite3 CLI format: one row
// per line, values joined with '|', NULL rendered as the literal "NULL".
// Non-SELECT statements print nothing.
//
// With -db <file> the database persists durably on every COMMIT and is loaded
// again on the next run (crash recovery); without it the database is in-memory
// and lost when the process exits.
//
// Exit code is 0 on success and 1 on the first error, which is reported on
// stderr.
package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/zhchxiao123/sql-db-0821/internal/engine"
)

func main() {
	dbPath := flag.String("db", "", "persistent database file ('' = in-memory)")
	flag.Parse()
	data, err := io.ReadAll(os.Stdin)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error reading stdin: %v\n", err)
		os.Exit(1)
	}
	var eng *engine.Engine
	if *dbPath != "" {
		eng, err = engine.Open(*dbPath)
	} else {
		eng = engine.New()
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %s\n", err)
		os.Exit(1)
	}
	for _, stmt := range splitStatements(string(data)) {
		if strings.TrimSpace(stmt) == "" {
			continue
		}
		res, err := eng.Execute(stmt)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %s\n", err)
			os.Exit(1)
		}
		for _, row := range res.Rows {
			parts := make([]string, len(row))
			for i, v := range row {
				parts[i] = v.RenderCLI()
			}
			fmt.Println(strings.Join(parts, "|"))
		}
	}
}

// splitStatements splits SQL text on ';' while respecting single-quoted
// string literals (with ” as the escaped quote).
func splitStatements(sql string) []string {
	var stmts []string
	var cur strings.Builder
	inStr := false
	for i := 0; i < len(sql); i++ {
		c := sql[i]
		if inStr {
			cur.WriteByte(c)
			if c == '\'' {
				if i+1 < len(sql) && sql[i+1] == '\'' {
					cur.WriteByte(sql[i+1])
					i++
				} else {
					inStr = false
				}
			}
			continue
		}
		switch c {
		case '\'':
			inStr = true
			cur.WriteByte(c)
		case ';':
			stmts = append(stmts, cur.String())
			cur.Reset()
		default:
			cur.WriteByte(c)
		}
	}
	if cur.Len() > 0 {
		stmts = append(stmts, cur.String())
	}
	return stmts
}
