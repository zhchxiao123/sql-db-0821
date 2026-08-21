package engine

import "fmt"

// SQLError is a normal, expected error: a syntax error, a missing table, a
// type mismatch, etc. The engine reports these to the caller and keeps
// running; they never crash the process.
type SQLError struct {
	Message string
}

func (e *SQLError) Error() string { return e.Message }

// UnsupportedError marks a statement that uses a construct outside the
// minimal SQL subset (ORDER BY, JOIN, subqueries, aggregates other than
// COUNT(*), etc.). The runner treats these as waivable records; the engine
// CLI reports them as errors with a non-zero exit code.
type UnsupportedError struct {
	Feature string
}

func (e *UnsupportedError) Error() string {
	return fmt.Sprintf("unsupported SQL construct: %s", e.Feature)
}
