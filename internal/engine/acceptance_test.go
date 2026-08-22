package engine

import (
	"sort"
	"strings"
	"testing"
)

// TestAcceptanceQueries pins the multi-table acceptance cases (a3–a7): joins
// with ON/USING, LEFT-join NULL padding, derived-table subqueries, and
// UNION/UNION ALL duplicate semantics plus whole-result ORDER BY ordering.
func TestAcceptanceQueries(t *testing.T) {
	e := New()
	setUpTwoTables(t, e)
	cases := []struct {
		sql    string
		sorted bool
		want   []string
	}{
		{"SELECT t.a, t2.b FROM t JOIN t2 ON t.a = t2.a", false, []string{"2|x", "2|x", "3|y"}},
		{"SELECT t.a, t2.b FROM t LEFT JOIN t2 ON t.a = t2.a", false, []string{"1|NULL", "2|x", "2|x", "3|y"}},
		{"SELECT * FROM (SELECT a FROM t) WHERE a > 1", false, []string{"2", "2", "3"}},
		{"SELECT a FROM t UNION SELECT a FROM t", true, []string{"1", "2", "3"}},
		{"SELECT a FROM t UNION ALL SELECT a FROM t", false, []string{"1", "2", "2", "3", "1", "2", "2", "3"}},
		{"SELECT a FROM t UNION SELECT a FROM t2 ORDER BY a DESC", false, []string{"4", "3", "2", "1"}},
		{"SELECT t.a, t2.b FROM t JOIN t2 ON t.a=t2.a ORDER BY t.a", false, []string{"2|x", "2|x", "3|y"}},
	}
	for _, c := range cases {
		res := exec(t, e, c.sql)
		got := rowsText(res)
		if c.sorted {
			sort.Strings(got)
		}
		if strings.Join(got, "\n") != strings.Join(c.want, "\n") {
			t.Errorf("%s\n  got  %v\n  want %v", c.sql, got, c.want)
		}
	}
}
