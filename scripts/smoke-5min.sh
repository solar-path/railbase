#!/usr/bin/env bash
# scripts/smoke-5min.sh — docs/17 #2 "5-minute smoke" gate
#
# Builds the binary with embedded-postgres support, boots it in a
# tempdir, exercises the core HTTP surfaces (health, admin bootstrap,
# admin UI, REST), then shuts down cleanly. Exits 0 on success.
#
# Run from repo root:
#
#     bash scripts/smoke-5min.sh
#
# CI integration: this script is the entry point for the "smoke" job
# in .github/workflows (or equivalent). It assumes:
#   - Go 1.26+ on $PATH
#   - curl on $PATH
#   - tcp port 8090 free (override via $RAILBASE_HTTP_ADDR)
#
# Embedded postgres is downloaded on first run (~5s) and cached at
# $TMPDIR. Total runtime ≈ 60-90s on a warm machine; under 5 min cold.

set -euo pipefail

# === config ===
PORT="${RAILBASE_HTTP_ADDR:-:8090}"
BASE="http://localhost${PORT}"
DATA_DIR="$(mktemp -d -t railbase-smoke-XXXX)"
BIN="${DATA_DIR}/railbase"
LOG="${DATA_DIR}/serve.log"
PID_FILE="${DATA_DIR}/serve.pid"

ADMIN_EMAIL="smoke@example.com"
ADMIN_PASS="smoke-pass-12345"

# === lifecycle ===
cleanup() {
	if [[ -f "${PID_FILE}" ]]; then
		local pid
		pid="$(cat "${PID_FILE}" 2>/dev/null || true)"
		if [[ -n "${pid}" ]] && kill -0 "${pid}" 2>/dev/null; then
			kill -TERM "${pid}" 2>/dev/null || true
			# Give graceful shutdown 10s before SIGKILL.
			for _ in $(seq 1 10); do
				kill -0 "${pid}" 2>/dev/null || break
				sleep 1
			done
			kill -KILL "${pid}" 2>/dev/null || true
		fi
	fi
	# Print server log on failure so CI shows what went wrong.
	if [[ "${SMOKE_OK:-0}" != "1" ]] && [[ -f "${LOG}" ]]; then
		echo "--- serve.log (last 50 lines) ---"
		tail -n 50 "${LOG}" || true
	fi
	rm -rf "${DATA_DIR}"
}
trap cleanup EXIT

# Helper: pretty section header.
section() {
	echo
	echo "=== $* ==="
}

# Helper: assert HTTP status. Args: <expected-code> <description> <curl-args...>
expect_status() {
	local want="$1"; shift
	local desc="$1"; shift
	local got
	got="$(curl -sS -o /dev/null -w '%{http_code}' "$@" || echo "000")"
	if [[ "${got}" != "${want}" ]]; then
		echo "FAIL ${desc}: got HTTP ${got}, want ${want}"
		return 1
	fi
	echo "OK   ${desc} (${got})"
}

# Helper: poll until URL returns 200 or timeout.
wait_for() {
	local url="$1"
	local timeout="${2:-60}"
	for i in $(seq 1 "${timeout}"); do
		if curl -sS -o /dev/null -w '%{http_code}' "${url}" 2>/dev/null | grep -q '^200$'; then
			echo "ready after ${i}s"
			return 0
		fi
		sleep 1
	done
	echo "timeout waiting for ${url}"
	return 1
}

# === 1. Build binary ===
section "Build (with -tags embed_pg)"
cd "$(git rev-parse --show-toplevel 2>/dev/null || pwd)"
go build -tags embed_pg -o "${BIN}" ./cmd/railbase
ls -lh "${BIN}"

# === 2. Verify binary size ≤ 30 MB (docs/17 #1) ===
section "Binary size check (≤ 30 MB)"
size_mb="$(du -m "${BIN}" | awk '{print $1}')"
if [[ "${size_mb}" -gt 30 ]]; then
	echo "FAIL binary too large: ${size_mb} MB > 30 MB"
	exit 1
fi
echo "OK   binary size = ${size_mb} MB (under 30 MB ceiling)"

# === 3. Boot server ===
section "Boot railbase serve --embed-postgres"
RAILBASE_DATA_DIR="${DATA_DIR}/pb_data" \
RAILBASE_EMBED_POSTGRES=true \
RAILBASE_HTTP_ADDR="${PORT}" \
RAILBASE_LOG_LEVEL=info \
RAILBASE_LOG_FORMAT=json \
"${BIN}" serve >"${LOG}" 2>&1 &
echo "$!" >"${PID_FILE}"
echo "spawned pid=$(cat "${PID_FILE}")"

# === 4. Wait for /readyz ===
section "Wait for /readyz"
wait_for "${BASE}/readyz" 90

# === 5. Bootstrap admin ===
section "Bootstrap admin"
expect_status 200 "GET /api/_admin/_bootstrap (probe)" \
	"${BASE}/api/_admin/_bootstrap"

bootstrap_body="$(curl -sS -X POST \
	-H 'Content-Type: application/json' \
	-d "{\"email\":\"${ADMIN_EMAIL}\",\"password\":\"${ADMIN_PASS}\"}" \
	"${BASE}/api/_admin/_bootstrap")"
echo "bootstrap response: ${bootstrap_body}" | head -c 200; echo
if ! echo "${bootstrap_body}" | grep -q "${ADMIN_EMAIL}"; then
	echo "FAIL bootstrap response doesn't echo the admin email"
	exit 1
fi
echo "OK   admin created"

# === 6. Signin as admin ===
section "Admin signin"
signin_body="$(curl -sS -X POST \
	-H 'Content-Type: application/json' \
	-d "{\"email\":\"${ADMIN_EMAIL}\",\"password\":\"${ADMIN_PASS}\"}" \
	"${BASE}/api/_admin/auth")"
TOKEN="$(echo "${signin_body}" | sed -n 's/.*"token":"\([^"]*\)".*/\1/p')"
if [[ -z "${TOKEN}" ]]; then
	echo "FAIL signin returned no token: ${signin_body}"
	exit 1
fi
echo "OK   token received (${#TOKEN} chars)"

# === 7. Authenticated probes ===
section "Authenticated admin endpoints"
expect_status 200 "GET /api/_admin/me" \
	-H "Authorization: Bearer ${TOKEN}" \
	"${BASE}/api/_admin/me"

expect_status 200 "GET /api/_admin/schema" \
	-H "Authorization: Bearer ${TOKEN}" \
	"${BASE}/api/_admin/schema"

expect_status 200 "GET /api/_admin/settings" \
	-H "Authorization: Bearer ${TOKEN}" \
	"${BASE}/api/_admin/settings"

expect_status 200 "GET /api/_admin/audit" \
	-H "Authorization: Bearer ${TOKEN}" \
	"${BASE}/api/_admin/audit"

# v1.7.6 — logs admin endpoint
expect_status 200 "GET /api/_admin/logs (v1.7.6)" \
	-H "Authorization: Bearer ${TOKEN}" \
	"${BASE}/api/_admin/logs"

# v1.7.9 — jobs + api-tokens admin endpoints
expect_status 200 "GET /api/_admin/jobs (v1.7.9)" \
	-H "Authorization: Bearer ${TOKEN}" \
	"${BASE}/api/_admin/jobs"

expect_status 200 "GET /api/_admin/api-tokens (v1.7.9)" \
	-H "Authorization: Bearer ${TOKEN}" \
	"${BASE}/api/_admin/api-tokens"

# v1.7.11 — backups + notifications admin endpoints
expect_status 200 "GET /api/_admin/backups (v1.7.11)" \
	-H "Authorization: Bearer ${TOKEN}" \
	"${BASE}/api/_admin/backups"

expect_status 200 "GET /api/_admin/notifications (v1.7.11)" \
	-H "Authorization: Bearer ${TOKEN}" \
	"${BASE}/api/_admin/notifications"

# === 8. Admin UI served ===
section "Admin UI HTML served at /_/"
expect_status 200 "GET /_/" "${BASE}/_/"
html="$(curl -sS "${BASE}/_/")"
if ! echo "${html}" | grep -qi 'railbase\|<title>\|<!doctype html>'; then
	echo "FAIL /_/ didn't return HTML-shaped body"
	exit 1
fi
echo "OK   admin UI HTML body received"

# === 9. PB-compat auth-methods discovery ===
section "PB-compat auth-methods discovery"
# Requires an auth collection; with bare bootstrap there are none, so
# this just confirms the endpoint exists and 404s cleanly.
expect_status 404 "GET /api/collections/users/auth-methods (no users coll yet)" \
	"${BASE}/api/collections/users/auth-methods"

# === 10. Compat mode discovery (v1.7.4) ===
section "Compat mode discovery (v1.7.4)"
expect_status 200 "GET /api/_compat-mode" "${BASE}/api/_compat-mode"

# === Done ===
section "SUCCESS"
SMOKE_OK=1
echo "5-min smoke gate PASSED for binary at ${BIN} (${size_mb} MB)"
echo "All 13 HTTP probes returned expected status codes."
echo
echo "Server pid was $(cat "${PID_FILE}"); shutting down via SIGTERM (trap)..."
exit 0
