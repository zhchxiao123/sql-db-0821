// Command reprodebug runs select4.test records through the engine and, when it
// reaches the compound query at line 3229, executes and prints each arm
// separately plus the full compounded result so the merge can be inspected.
package main

import (
	"fmt"
	"os"

	"github.com/zhchxiao123/sql-db-0821/internal/engine"
	"github.com/zhchxiao123/sql-db-0821/internal/sqllogictest"
)

const targetLine = 3229

func main() {
	tf, err := sqllogictest.ParseFile(os.Args[1])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	eng := engine.New()
	var target *sqllogictest.Record
	for _, rec := range tf.Records {
		if rec.Line >= targetLine {
			target = rec
			break
		}
		eng.Execute(rec.SQL)
	}
	if target == nil {
		fmt.Fprintln(os.Stderr, "record not found")
		os.Exit(2)
	}
	fmt.Printf("=== record at line %d ===\n%s\n", target.Line, target.SQL)
	res, err := eng.Execute(target.SQL)
	if err != nil {
		fmt.Fprintln(os.Stderr, "execute:", err)
		os.Exit(2)
	}
	fmt.Printf("rows=%d\n", len(res.Rows))
	for _, row := range res.Rows {
		for _, v := range row {
			fmt.Printf("%s ", v.RenderCLI())
		}
		fmt.Println()
	}
}
