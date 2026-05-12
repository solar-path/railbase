#!/usr/bin/env bash
# v1 SHIP gate companion — docs/17 #1 ≤30 MB binary check.
#
# Walks every binary under bin/release/ (produced by `make cross-compile`
# or `goreleaser release --snapshot`) and fails if any exceeds 30 MB.
# Run after a cross-compile sweep to enforce the size budget.
#
# Usage:
#   scripts/check-binary-size.sh            # uses bin/release/
#   scripts/check-binary-size.sh dist/      # for goreleaser output
#
# Exit codes: 0 pass · 1 over budget · 2 no binaries found.

set -euo pipefail

DIR="${1:-bin/release}"
LIMIT_MB="${RAILBASE_BIN_LIMIT_MB:-30}"

if [[ ! -d "$DIR" ]]; then
    echo "✗ directory not found: $DIR"
    exit 2
fi

# Collect candidate binaries. Include extensionless + .exe; exclude
# *.tar.gz / *.zip / *.txt that goreleaser may also drop here. Using
# `find ... -print0` + while-read for portability with macOS bash 3.2
# (no mapfile / readarray on stock macOS).
binaries=()
while IFS= read -r -d '' f; do
    binaries+=("$f")
done < <(find "$DIR" -type f \( -name 'railbase*' -o -name '*.exe' \) ! -name '*.tar.gz' ! -name '*.zip' ! -name '*.txt' ! -name '*.sha256' -print0)

if [[ ${#binaries[@]} -eq 0 ]]; then
    echo "✗ no binaries found under $DIR"
    exit 2
fi

fail=0
printf "%-50s %10s %10s\n" "BINARY" "SIZE_MB" "STATUS"
printf "%-50s %10s %10s\n" "--------------------------------------------------" "----------" "----------"
for b in "${binaries[@]}"; do
    # Stat byte size — portable across linux + darwin (no GNU stat
    # required). du -k gives KB, divide by 1024.
    bytes=$(wc -c < "$b" | tr -d ' ')
    mb=$(awk -v b="$bytes" 'BEGIN { printf "%.2f", b/1024/1024 }')
    over=$(awk -v m="$mb" -v l="$LIMIT_MB" 'BEGIN { print (m+0 > l+0) ? "1" : "0" }')
    if [[ "$over" == "1" ]]; then
        printf "%-50s %10s %10s\n" "$b" "$mb" "OVER"
        fail=1
    else
        printf "%-50s %10s %10s\n" "$b" "$mb" "ok"
    fi
done

echo
if [[ $fail -eq 0 ]]; then
    echo "✓ all binaries within $LIMIT_MB MB ceiling (docs/17 #1)"
    exit 0
else
    echo "✗ one or more binaries exceed $LIMIT_MB MB — failing the docs/17 #1 size gate"
    exit 1
fi
