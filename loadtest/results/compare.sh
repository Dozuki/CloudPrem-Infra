#!/usr/bin/env bash
# Usage: compare.sh results/A.log results/B.log
set -euo pipefail
extract() { grep -oE 'p95=[0-9]+ms p99=[0-9]+ms reqs=[0-9]+ err_rate=[0-9.]+%' "$1" | tail -1; }
echo "A ($1): $(extract "$1")"
echo "B ($2): $(extract "$2")"
echo "(Compare guide/search p95 + err_rate; read DB-load/CPU deltas from Grafana for the run windows.)"
