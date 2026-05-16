#!/usr/bin/env bash
# v1 SHIP gate companion — docs/17 #1 ≤32 MB binary check.
#
# Walks every binary under bin/dist/ (produced by `make cross-compile`
# or `goreleaser release --snapshot`) and fails if any exceeds 32 MB.
# Run after a cross-compile sweep to enforce the size budget.
#
# History: the ceiling was 30 MB up to v1.7.47. v1.7.48 added the
# admin SPA i18n bundles (9 lazy locale chunks, ~1.84 MB raw across
# en + zh/hi/es/fr/ar/bn/pt/ru/ur) embedded via //go:embed all:dist.
# Bumped to 32 MB to keep "single binary, 10-language admin UI" as
# part of the ship envelope rather than splitting into two artefacts.
#
# Usage:
#   scripts/check-binary-size.sh            # uses bin/dist/
#   scripts/check-binary-size.sh dist/      # for top-level goreleaser output
#
# Exit codes: 0 pass · 1 over budget · 2 no binaries found.

set -euo pipefail

DIR="${1:-bin/dist}"
LIMIT_MB="${RAILBASE_BIN_LIMIT_MB:-32}"

if [[ ! -d "$DIR" ]]; then
    echo "✗ directory not found: $DIR"
    exit 2
fi

# Collect candidate binaries. Include extensionless + .exe; exclude
# *.tar.gz / *.zip / *.txt that goreleaser may also drop here, and the
# `railbase-embed_*` dev binary (embedded postgres tooling — not a
# release artifact, so it's outside the docs/17 #1 size budget). Using
# `find ... -print0` + while-read for portability with macOS bash 3.2
# (no mapfile / readarray on stock macOS).
binaries=()
while IFS= read -r -d '' f; do
    binaries+=("$f")
done < <(find "$DIR" -type f \( -name 'railbase_*' -o -name '*.exe' \) ! -name 'railbase-embed*' ! -name '*.tar.gz' ! -name '*.zip' ! -name '*.txt' ! -name '*.sha256' -print0)

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
