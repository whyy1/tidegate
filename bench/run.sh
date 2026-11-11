#!/usr/bin/env bash
# Runs the Tidegate benchmarks.
#
#   bash bench/run.sh                       # full run: 100/1000/10000 VUs, 30s each, plus the memory test
#   bash bench/run.sh -duration 5s -vus 100 -think-vus 0 -targets tidegate-fast
#
# k6 is used when found in K6_BIN, on PATH or at its default install
# location; otherwise its rows are marked "未做 k6 对比". Raw output goes to
# bench/out/ (ignored by git).
set -euo pipefail
cd "$(dirname "$0")/.."
exec go run ./bench/harness "$@"
