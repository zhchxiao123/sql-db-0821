// Command sqllogictest runs .test files against the minimal SQL engine and
// reports pass/fail statistics. With no file arguments it runs every
// suite/select*.test file. Each file runs against a fresh engine, matching
// the reference sqllogictest runner.
//
// Records that use constructs outside the minimal SQL subset are reported
// as waived (see README); with --strict they count as failures instead.
// Exit code is 0 when there are no failures, 1 otherwise.
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/zhchxiao123/sql-db-0821/internal/sqllogictest"
)

func main() {
	strict := flag.Bool("strict", false, "count unsupported (waived) records as failures")
	flag.Parse()
	files := flag.Args()
	if len(files) == 0 {
		matches, err := filepath.Glob("suite/select*.test")
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(2)
		}
		files = matches
	}
	if len(files) == 0 {
		fmt.Fprintln(os.Stderr, "no test files found (run `make suite` first, or pass .test files as arguments)")
		os.Exit(2)
	}
	sort.Strings(files)

	opts := sqllogictest.Options{Strict: *strict}
	var total, passed, failed, waived int
	for _, f := range files {
		stats, err := sqllogictest.RunFile(f, opts)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error running %s: %v\n", f, err)
			os.Exit(2)
		}
		fmt.Printf("%s: %d records, %d passed, %d failed, %d waived\n",
			filepath.Base(f), stats.Total, stats.Passed, stats.Failed, stats.Waived)
		for _, fail := range stats.Failures {
			fmt.Printf("  FAIL %s:%d: %s\n", filepath.Base(fail.File), fail.Line, fail.Message)
		}
		total += stats.Total
		passed += stats.Passed
		failed += stats.Failed
		waived += stats.Waived
	}
	fmt.Printf("TOTAL: %d records, %d passed, %d failed, %d waived\n", total, passed, failed, waived)
	if failed > 0 {
		os.Exit(1)
	}
}
