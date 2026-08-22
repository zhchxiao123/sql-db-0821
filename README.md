# sql-db-0821

A minimal in-memory SQL database engine written in **Go 1.24**, with a
command-line interface and a [sqllogictest](https://www.sqlite.org/sqllogictest/doc/trunk/about.wiki)
runner. This is the runnable skeleton for a database project: it implements a
small SQL subset end to end (parser, executor, result rendering) and is
verified against the pinned sqllogictest suite.

## Language

Go 1.24.0. All future sub-requirements will use Go.

## Layout

```
cmd/sqldb          engine CLI: read SQL from stdin, print results
cmd/sqllogictest   sqllogictest runner CLI
internal/engine    tokenizer, parser, executor, value rendering
internal/sqllogictest  .test parser and result comparison (full + hash modes)
suite/             pinned sqllogictest suite (select*.test, random/expr/*.test)
testdata/          self-test .test files (basic, error, expr, wrong, unsupported)
scripts/           fetch-suite.sh (suite acquisition)
Makefile           build / test / suite / check targets
```

## Supported SQL subset

- `CREATE TABLE t (col TYPE [PRIMARY KEY|NOT NULL|UNIQUE], ...)` — column
  constraints are parsed and ignored (enforcement is a later sub-requirement).
- `INSERT INTO t [(cols)] VALUES (...)` — one row per statement. Values are
  converted to the target column's affinity (INTEGER/REAL/TEXT/NUMERIC/BLOB),
  matching SQLite's storage rules.
- `SELECT [ALL|DISTINCT] expr [AS alias] [, ...] [FROM t [WHERE expr]]` —
  constant SELECTs (no `FROM`) evaluate once; `DISTINCT` is a no-op for a
  constant SELECT and unsupported with `FROM`; `COUNT(*)` counts rows with
  `FROM` and evaluates to 1 without it.
- `UPDATE t SET col = expr [, ...] [WHERE expr]`
- `DELETE FROM t [WHERE expr]`
- `DROP TABLE t`

Expressions follow SQLite semantics, verified against sqlite3 3.51.0:

- **Arithmetic** `+ - * / %` with integer overflow promoting to REAL (never
  wrapping), integer division truncation, and division by zero → NULL.
- **Comparison** `= != <> < > <= >=` with SQLite's storage-class ordering
  (NULL < numbers < TEXT < BLOB) and comparison affinity (a text literal
  compared to a numeric column is converted to a number).
- **Logical** `AND OR NOT` with SQLite's three-valued logic (0 wins over
  NULL in AND, 1 wins over NULL in OR).
- **`IS` / `IS NOT`** (NULL-safe equality), **`IN`** / `NOT IN`,
  **`BETWEEN`** / `NOT BETWEEN`, **`LIKE`** / `NOT LIKE` (case-insensitive,
  `%`/`_`, `ESCAPE`), **`GLOB`** (case-sensitive, `*`/`?`/`[...]`).
- **`CAST(x AS type)`** for INTEGER/REAL/NUMERIC/TEXT/BLOB.
- **NULL propagation** matching SQLite: any arithmetic or comparison with
  NULL yields NULL (except `IS`/`IS NOT` and the logical short-circuits).
- **Blob literals** `X'hex'` and the `==` operator (alias for `=`).

Everything else — `ORDER BY`, `LIMIT`, `DISTINCT` (with `FROM`), `GROUP BY`,
`JOIN`, `UNION`, `CASE`, subqueries, function calls, aggregates other than
bare `COUNT(*)`, transactions, `CREATE INDEX`/`VIEW`, `ALTER`, `PRAGMA` — is
rejected with an `UnsupportedError` (never a crash). The runner reports such
records as **waived** rather than failed; see the waiver policy below.

## Value rendering

- Engine CLI (`cmd/sqldb`): rows joined with `|`, `NULL` rendered as the
  literal `NULL`, floats formatted like the sqlite3 CLI (15 significant
  digits, always a decimal point, two-digit signed exponent), blobs as their
  raw bytes.
- sqllogictest output: `NULL` → `NULL`, integers decimal, floats `%.3f`,
  empty text `(empty)`, control characters `@` — matching the reference
  runner (`slt_sqlite.c`).

## sqllogictest suite

The suite is pinned to the upstream
[gregrahn/sqllogictest](https://github.com/gregrahn/sqllogictest) repository:

- **Tag:** `version-3.11.0`
- **Commit:** `0b24fd28f7bbe2598fb87dab53cb17b8ddd77520`
- **Source:** `https://github.com/gregrahn/sqllogictest/tree/version-3.11.0/test`
- **Acquisition:** `scripts/fetch-suite.sh` downloads the `select*.test`
  files and the `random/expr/*.test` expression suite from the pinned tag into
  `suite/` (or `make suite`).

The `select*.test` files exercise the CRUD/WHERE subset; `random/expr/*.test`
is the expression and type-system suite (120 files, ~515k records) that this
sub-requirement targets. The other suite files (`random` beyond `expr`,
`subquery`, `index`, `evidence`, ...) exercise constructs outside the minimal
subset and are out of scope for the skeleton.

### Waiver policy

The minimal subset covers only a small fraction of the suite. A record that
uses an unsupported construct is reported as **waived**, not failed, so the
suite run reports the executable subset's true status. `--strict` flips
waived records into failures. The README documents this policy; the runner
never crashes on unsupported input (it returns a typed `UnsupportedError`).

## Usage

```sh
# Build and unit-test
make build
make test

# Fetch the pinned suite (one time)
make suite

# Run the sqllogictest suite (0 failures expected)
make sqllogictest

# One-command repro: build + unit tests + suite
make check
```

### Engine CLI

```sh
go run ./cmd/sqldb <<'EOF'
CREATE TABLE t (a INTEGER, b TEXT);
INSERT INTO t VALUES (1, 'one');
INSERT INTO t VALUES (2, 'two');
SELECT * FROM t WHERE a > 1;
EOF
```

### Runner CLI

```sh
# Run the whole suite
go run ./cmd/sqllogictest

# Run specific files
go run ./cmd/sqllogictest testdata/basic.test testdata/error.test

# Count unsupported records as failures
go run ./cmd/sqllogictest --strict
```

## Self-test

`testdata/` holds small .test files used to verify the acceptance criteria:

- `basic.test` — CRUD lifecycle, WHERE, NULL, `COUNT(*)`; every expected
  value verified against sqlite3 3.51.0. Passes.
- `error.test` — records that expect an error. Passes.
- `expr.test` — the expression and type-system subset (acceptance criteria
  a2/a3/a4): constant expressions, arithmetic, logical, IS/IN/BETWEEN/LIKE,
  CAST, overflow promotion, full-expression UPDATE/DELETE WHERE, column
  affinity. Every expected value verified against sqlite3 3.51.0. Passes.
- `unsupported.test` — unsupported constructs return errors (waived). Passes.
- `wrong.test` — deliberately wrong expected values; running it must report
  failures (exit code 1). Used to verify failure detection.

```sh
go run ./cmd/sqllogictest testdata/basic.test testdata/error.test testdata/expr.test testdata/unsupported.test
go run ./cmd/sqllogictest testdata/wrong.test; echo "exit: $?"   # expect exit 1
```

## Current suite status

```
select1.test: 1031 records, 31 passed, 0 failed, 1000 waived
select2.test: 1031 records, 217 passed, 0 failed, 814 waived
select3.test: 3351 records, 377 passed, 0 failed, 2974 waived
select4.test: 3857 records, 1027 passed, 0 failed, 2830 waived
select5.test: 1436 records, 704 passed, 0 failed, 732 waived
random/expr: 517295 records, 458762 passed, 0 failed, 58533 waived
TOTAL: 528001 records, 461118 passed, 0 failed, 66883 waived
```
