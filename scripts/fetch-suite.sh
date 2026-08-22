#!/usr/bin/env bash
# fetch-suite.sh — download the pinned sqllogictest suite into suite/.
#
# The suite is pinned to the gregrahn/sqllogictest repository at tag
# version-3.11.0 (commit 0b24fd28f7bbe2598fb87dab53cb17b8ddd77520). The
# select*.test files and the random/expr/*.test files are fetched; they are
# the executable subset for this engine (see README for the waiver policy).
#
# Usage: scripts/fetch-suite.sh [output-dir]
#   output-dir defaults to suite/ relative to the repository root.

set -euo pipefail

REPO="https://raw.githubusercontent.com/gregrahn/sqllogictest/version-3.11.0/test"
COMMIT="0b24fd28f7bbe2598fb87dab53cb17b8ddd77520"

# Resolve the repository root (parent of scripts/).
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
OUT="${1:-$ROOT/suite}"
mkdir -p "$OUT/random/expr"

echo "Fetching sqllogictest suite (pinned to version-3.11.0, $COMMIT) into $OUT"
echo "  verify: git ls-remote https://github.com/gregrahn/sqllogictest.git refs/tags/version-3.11.0"
for f in select1.test select2.test select3.test select4.test select5.test; do
  url="$REPO/$f"
  echo "  $url"
  curl -fsSL "$url" -o "$OUT/$f"
done
for i in $(seq 0 119); do
  url="$REPO/random/expr/slt_good_$i.test"
  echo "  $url"
  curl -fsSL "$url" -o "$OUT/random/expr/slt_good_$i.test"
done

echo "Done. Verify the pin with:"
echo "  git -C $OUT log --oneline -1 2>/dev/null || true"
echo "  (the files are plain downloads; the pin is recorded in README.md)"
