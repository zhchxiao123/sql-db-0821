# Makefile for sql-db-0821 — a minimal in-memory SQL engine with a
# sqllogictest runner. See README.md for the full story.

GO      ?= go
SUITE   ?= suite/select*.test

.PHONY: all build test suite sqllogictest check clean

all: build

build:
	$(GO) build ./...

test:
	$(GO) test ./...

# Fetch the pinned sqllogictest suite (version-3.11.0) into suite/.
suite:
	scripts/fetch-suite.sh

# Run the sqllogictest suite. Records using constructs outside the minimal
# subset are waived; pass --strict to count them as failures.
sqllogictest:
	$(GO) run ./cmd/sqllogictest $(SUITE)

# One-command repro: build, unit-test, and run the full suite.
check: build test sqllogictest

clean:
	$(GO) clean
