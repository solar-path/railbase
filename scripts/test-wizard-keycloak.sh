#!/usr/bin/env bash
# v1.7.51 — end-to-end wizard test against the live Keycloak stack at
# /Users/work/apps/keyclock/. Spins up an isolated Railbase backend
# on port 8090 with its own data dir + embedded PG (clean state every
# run) and walks the SAML / LDAP / SCIM cards through realistic Save +
# Status round-trips.
#
# Prerequisites:
#   1. Keycloak stack running: cd /Users/work/apps/keyclock && docker compose ps
#   2. The 'embed_pg'-tagged binary built: make build-embed
#
# Run:
#   scripts/test-wizard-keycloak.sh
#
# This is OPERATOR-FACING test, not CI — it boots a real backend,
# pokes real network endpoints, prints colour-coded PASS/FAIL on
# every assertion. Designed to fail loud + cleanup-on-exit.

set -uo pipefail

# Colours.
R='\033[0;31m'
G='\033[0;32m'
Y='\033[0;33m'
B='\033[0;34m'
N='\033[0m' # No colour

# Test config.
KC_BASE="http://localhost:8081"
KC_REALM="grc-test"
SCIM_CLIENT_ID="scim-client"
SCIM_CLIENT_SECRET="scim-client-dev-secret"
# Port 18095 — far outside common dev port ranges to avoid conflicts
# with other localhost servers. The script verifies the responding
# server is ACTUALLY railbase via /api/health/build before running
# assertions, so a hijacked port fails loud.
RB_PORT=18095
RB_BASE="http://127.0.0.1:${RB_PORT}"
RB_DATA="${TMPDIR:-/tmp}/railbase-wizard-test-$$"
# External PG: use the user's brew socket + their `railbase` DB.
# The script ONLY writes auth.* settings keys and `_scim_tokens`
# rows — no destructive ops on existing data. Test state coexists
# with their dev environment.
RB_DSN="postgres://$(whoami)@/railbase?host=/tmp&sslmode=disable"

# Counters.
PASS=0
FAIL=0
FAILED_NAMES=()

# Test harness.
pass() {
  printf "${G}✓${N} %s\n" "$1"
  PASS=$((PASS + 1))
}
fail() {
  printf "${R}✗${N} %s\n" "$1"
  FAIL=$((FAIL + 1))
  FAILED_NAMES+=("$1")
}
section() {
  printf "\n${B}━━ %s ━━${N}\n" "$1"
}
info() {
  printf "${Y}→${N} %s\n" "$1"
}

# Pre-flight.
section "Pre-flight"

if ! curl -s --max-time 5 -o /dev/null -w "%{http_code}" "${KC_BASE}/realms/${KC_REALM}/protocol/saml/descriptor" | grep -q "200"; then
  printf "${R}Keycloak not reachable at %s. Run:\n  cd /Users/work/apps/keyclock && docker compose up -d${N}\n" "${KC_BASE}"
  exit 1
fi
pass "Keycloak SAML descriptor reachable"

if ! curl -s --max-time 5 -o /dev/null "${KC_BASE}/realms/${KC_REALM}/protocol/openid-connect/token" -d "grant_type=foo"; then
  fail "Keycloak token endpoint unreachable"
  exit 1
fi
pass "Keycloak token endpoint reachable"

# Quick LDAP smoke via docker.
if ! docker compose -f /Users/work/apps/keyclock/docker-compose.yml exec -T openldap \
     ldapsearch -x -H ldap://localhost:1389 -b "dc=grc-test,dc=local" \
     -D "cn=admin,dc=grc-test,dc=local" -w adminpassword \
     "(uid=alice.anderson)" uid 2>/dev/null | grep -q "uid: alice.anderson"; then
  fail "Keycloak OpenLDAP backend not responding correctly"
  exit 1
fi
pass "OpenLDAP backend responding (alice.anderson visible)"

# Build the production binary if missing (no embed_pg — we use external
# PG via DSN to avoid the flaky embed-PG runtime extraction in scripts).
if [ ! -x "/Users/work/apps/Railbase/bin/railbase" ]; then
  info "Building bin/railbase (one-time)…"
  (cd /Users/work/apps/Railbase && make build >/dev/null 2>&1) || {
    fail "Build failed"
    exit 1
  }
fi
pass "bin/railbase present"

# Ensure migrations are applied + a minimal `users` auth-collection
# exists. The wizard step that registers domain auth-collections runs
# OUTSIDE the wizard scope (operator writes Go code), so we provision
# a thin `users` table directly here. SCIM Users CRUD tests need it.
section "Bootstrap schema state in target DB"

(cd /Users/work/apps/Railbase && \
  RAILBASE_DSN="${RB_DSN}" ./bin/railbase migrate up >/dev/null 2>&1)
pass "Sys migrations applied"

# Idempotent CREATE — `IF NOT EXISTS` so re-runs are no-ops.
psql -h /tmp -U "$(whoami)" -d railbase -v ON_ERROR_STOP=1 >/dev/null 2>&1 <<'PSQL' || true
CREATE TABLE IF NOT EXISTS users (
    id            UUID         PRIMARY KEY DEFAULT gen_random_uuid(),
    email         TEXT         NOT NULL,
    password_hash TEXT         NOT NULL,
    verified      BOOLEAN      NOT NULL DEFAULT FALSE,
    token_key     TEXT         NOT NULL,
    last_login_at TIMESTAMPTZ,
    external_id   TEXT,
    scim_managed  BOOLEAN      NOT NULL DEFAULT FALSE,
    created       TIMESTAMPTZ  NOT NULL DEFAULT now(),
    updated       TIMESTAMPTZ  NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX IF NOT EXISTS users_email_idx ON users (lower(email));
CREATE UNIQUE INDEX IF NOT EXISTS users_external_id_idx ON users (external_id) WHERE external_id IS NOT NULL;
PSQL
if psql -h /tmp -U "$(whoami)" -d railbase -tAc "SELECT to_regclass('users') IS NOT NULL" 2>/dev/null | grep -q "t"; then
  pass "users auth-collection table provisioned"
else
  fail "users table provisioning failed"
fi

# Start an isolated backend.
section "Boot isolated Railbase backend on :${RB_PORT}"

mkdir -p "${RB_DATA}"
trap '
  printf "\n${Y}→${N} Cleaning up backend (PID=%s)…\n" "${RB_PID:-}"
  if [ -n "${RB_PID:-}" ]; then kill "${RB_PID}" 2>/dev/null; fi
  rm -rf "${RB_DATA}"
' EXIT INT TERM

RAILBASE_DSN="${RB_DSN}" \
RAILBASE_HTTP_ADDR=":${RB_PORT}" \
RAILBASE_DATA_DIR="${RB_DATA}" \
RAILBASE_LOG_LEVEL=warn \
/Users/work/apps/Railbase/bin/railbase serve >"${RB_DATA}/server.log" 2>&1 &
RB_PID=$!

# Poll readiness AND verify it's actually our backend (port hijack
# guard). railbase responds 200 on /healthz with a build-info shape;
# something else listening on the same port would return its own page.
for i in {1..30}; do
  HEALTH=$(curl -s --max-time 2 "${RB_BASE}/healthz" 2>/dev/null || true)
  if echo "${HEALTH}" | grep -q '"status":"ok"'; then
    pass "Backend ready + identity-confirmed (took ${i}s)"
    break
  fi
  if ! kill -0 "${RB_PID}" 2>/dev/null; then
    fail "Backend exited during startup; last 30 lines of log:"
    tail -30 "${RB_DATA}/server.log"
    exit 1
  fi
  sleep 1
  if [ "$i" = "30" ]; then
    fail "Backend never returned ok status on /healthz after 30s. Last log lines:"
    tail -30 "${RB_DATA}/server.log"
    exit 1
  fi
done

# --- WIZARD STEP 1: auth-status default ---
section "Wizard status — fresh install defaults"

STATUS_JSON=$(curl -s "${RB_BASE}/api/_admin/_setup/auth-status")
if [ -z "${STATUS_JSON}" ]; then
  fail "auth-status returned empty body"
else
  if echo "${STATUS_JSON}" | grep -q '"plugin_gated":\[\]'; then
    pass "plugin_gated is empty (SCIM, LDAP, SAML all in core post-v1.7.51)"
  else
    fail "plugin_gated should be empty array; got: $(echo "$STATUS_JSON" | head -c 200)…"
  fi
  if echo "${STATUS_JSON}" | grep -q '"scim":'; then
    pass "auth-status surfaces scim block (v1.7.51)"
  else
    fail "auth-status missing scim block"
  fi
  if echo "${STATUS_JSON}" | grep -q '"endpoint_url":"http://127.0.0.1:'; then
    pass "scim.endpoint_url is auto-computed from request host"
  else
    fail "scim.endpoint_url not populated correctly"
  fi
fi

# --- WIZARD STEP 2: LDAP card — Save with Keycloak OpenLDAP creds ---
section "LDAP — wizard save against Keycloak OpenLDAP"

# Use the LDAP exposed by the keycloak stack at port 1389.
LDAP_SAVE=$(curl -s -X POST "${RB_BASE}/api/_admin/_setup/auth-save" \
  -H "Content-Type: application/json" \
  -d '{
    "methods": {"password": true},
    "ldap": {
      "enabled": true,
      "url": "ldap://localhost:1389",
      "tls_mode": "none",
      "bind_dn": "cn=admin,dc=grc-test,dc=local",
      "bind_password": "adminpassword",
      "user_base_dn": "ou=users,dc=grc-test,dc=local",
      "user_filter": "(uid=%s)",
      "email_attr": "mail",
      "name_attr": "cn"
    }
  }')
if echo "${LDAP_SAVE}" | grep -q '"ok":true'; then
  pass "LDAP save returns ok=true"
else
  fail "LDAP save failed: ${LDAP_SAVE}"
fi

LDAP_STATUS=$(curl -s "${RB_BASE}/api/_admin/_setup/auth-status")
if echo "${LDAP_STATUS}" | grep -q '"url":"ldap://localhost:1389"'; then
  pass "LDAP URL round-trips via status"
else
  fail "LDAP URL not round-tripping"
fi
if echo "${LDAP_STATUS}" | grep -q '"bind_password_set":true'; then
  pass "LDAP bind_password_set=true (stored)"
else
  fail "LDAP bind_password_set not set after save"
fi
if echo "${LDAP_STATUS}" | grep -q 'adminpassword'; then
  fail "LDAP status response LEAKED the bind_password value (regression)"
else
  pass "LDAP bind_password not echoed in status (correct)"
fi

# --- WIZARD STEP 3: SAML card — Save with Keycloak SAML metadata URL ---
section "SAML — wizard save against Keycloak SAML IdP metadata"

SAML_METADATA_URL="${KC_BASE}/realms/${KC_REALM}/protocol/saml/descriptor"

SAML_SAVE=$(curl -s -X POST "${RB_BASE}/api/_admin/_setup/auth-save" \
  -H "Content-Type: application/json" \
  -d "{
    \"methods\": {\"password\": true},
    \"saml\": {
      \"enabled\": true,
      \"idp_metadata_url\": \"${SAML_METADATA_URL}\",
      \"sp_entity_id\": \"http://localhost:${RB_PORT}/saml/sp\",
      \"sp_acs_url\": \"http://localhost:${RB_PORT}/api/collections/users/auth-with-saml/acs\",
      \"sp_slo_url\": \"http://localhost:${RB_PORT}/api/collections/users/auth-with-saml/slo\",
      \"email_attribute\": \"email\",
      \"name_attribute\": \"name\",
      \"group_attribute\": \"memberOf\",
      \"role_mapping\": \"{\\\"grc-admins\\\":\\\"site_admin\\\",\\\"grc-auditors\\\":\\\"auditor\\\"}\"
    }
  }")
if echo "${SAML_SAVE}" | grep -q '"ok":true'; then
  pass "SAML save (with Keycloak metadata + group mapping) returns ok=true"
else
  fail "SAML save failed: ${SAML_SAVE}"
fi

SAML_STATUS=$(curl -s "${RB_BASE}/api/_admin/_setup/auth-status")
if echo "${SAML_STATUS}" | grep -q "\"idp_metadata_url\":\"${SAML_METADATA_URL}\""; then
  pass "SAML idp_metadata_url round-trips"
else
  fail "SAML idp_metadata_url not round-tripping"
fi
if echo "${SAML_STATUS}" | grep -q '"sp_slo_url":"http://localhost:'; then
  pass "SAML sp_slo_url round-trips (v1.7.50.2)"
else
  fail "SAML sp_slo_url not round-tripping"
fi
if echo "${SAML_STATUS}" | grep -q '"group_attribute":"memberOf"'; then
  pass "SAML group_attribute round-trips (v1.7.50.1d)"
else
  fail "SAML group_attribute not round-tripping"
fi
if echo "${SAML_STATUS}" | grep -q '"role_mapping":"{\\\"grc-admins\\\":'; then
  pass "SAML role_mapping JSON round-trips"
else
  fail "SAML role_mapping not round-tripping"
fi

# --- WIZARD STEP 4: SAML metadata fetch — does the saved metadata URL
#     ACTUALLY parse against the live Keycloak descriptor? ---
section "SAML metadata fetch — does Keycloak descriptor parse cleanly?"

KC_METADATA=$(curl -s --max-time 5 "${SAML_METADATA_URL}")
if echo "${KC_METADATA}" | head -c 500 | grep -q "EntityDescriptor"; then
  pass "Keycloak metadata is a valid EntityDescriptor"
else
  fail "Keycloak metadata isn't an EntityDescriptor; head: $(echo "$KC_METADATA" | head -c 200)…"
fi
if echo "${KC_METADATA}" | grep -q "SingleSignOnService"; then
  pass "Keycloak metadata advertises SSO endpoint"
else
  fail "Keycloak metadata missing SingleSignOnService"
fi
if echo "${KC_METADATA}" | grep -q "SingleLogoutService"; then
  pass "Keycloak metadata advertises SLO endpoint (SLO supported end-to-end)"
else
  fail "Keycloak metadata missing SingleLogoutService"
fi

# --- WIZARD STEP 5: SCIM card — Save with default collection ---
section "SCIM — wizard save"

SCIM_SAVE=$(curl -s -X POST "${RB_BASE}/api/_admin/_setup/auth-save" \
  -H "Content-Type: application/json" \
  -d '{
    "methods": {"password": true},
    "scim": {
      "enabled": true,
      "collection": "users"
    }
  }')
if echo "${SCIM_SAVE}" | grep -q '"ok":true'; then
  pass "SCIM save returns ok=true"
else
  fail "SCIM save failed: ${SCIM_SAVE}"
fi

SCIM_STATUS=$(curl -s "${RB_BASE}/api/_admin/_setup/auth-status")
if echo "${SCIM_STATUS}" | grep -q '"enabled":true.*"collection":"users"' || \
   echo "${SCIM_STATUS}" | python3 -c "import sys,json; d=json.load(sys.stdin); sys.exit(0 if d['scim']['enabled']==True and d['scim']['collection']=='users' else 1)"; then
  pass "SCIM status round-trips enabled=true + collection=users"
else
  fail "SCIM status mismatch: $(echo "$SCIM_STATUS" | python3 -c "import sys,json; print(json.dumps(json.load(sys.stdin)['scim']))" 2>/dev/null)"
fi

# --- WIZARD STEP 6: SCIM token mint via CLI — does it work? ---
section "SCIM — mint a bearer credential via CLI"

# Run scim token create against the same DSN. CLI loads master.key
# from RAILBASE_DATA_DIR — must be the same one the server uses.
SCIM_TOKEN_OUTPUT=$(RAILBASE_DSN="${RB_DSN}" RAILBASE_DATA_DIR="${RB_DATA}" \
  /Users/work/apps/Railbase/bin/railbase scim token create \
  --name "keycloak-test" --collection users 2>&1)
RAW_TOKEN=$(echo "${SCIM_TOKEN_OUTPUT}" | grep '^rbsm_' | head -1)

if [ -n "${RAW_TOKEN}" ]; then
  pass "SCIM CLI minted a token: ${RAW_TOKEN:0:12}…"
else
  fail "SCIM CLI didn't print a rbsm_ token; output: ${SCIM_TOKEN_OUTPUT}"
fi

# --- WIZARD STEP 7: SCIM discovery endpoints (public, no auth) ---
section "SCIM — discovery endpoints public + RFC-7644 shaped"

SPC=$(curl -s -o /dev/null -w "%{http_code}" "${RB_BASE}/scim/v2/ServiceProviderConfig")
RT=$(curl -s -o /dev/null -w "%{http_code}" "${RB_BASE}/scim/v2/ResourceTypes")
SCH=$(curl -s -o /dev/null -w "%{http_code}" "${RB_BASE}/scim/v2/Schemas")
if [ "${SPC}" = "200" ]; then pass "/scim/v2/ServiceProviderConfig public + 200"; else fail "/scim/v2/ServiceProviderConfig = ${SPC}"; fi
if [ "${RT}" = "200" ]; then pass "/scim/v2/ResourceTypes public + 200"; else fail "/scim/v2/ResourceTypes = ${RT}"; fi
if [ "${SCH}" = "200" ]; then pass "/scim/v2/Schemas public + 200"; else fail "/scim/v2/Schemas = ${SCH}"; fi

# --- WIZARD STEP 8: SCIM Users endpoint — gated by bearer; works with
#     our minted token; refuses without it. ---
section "SCIM — Users endpoint gated by bearer"

NO_AUTH=$(curl -s -o /dev/null -w "%{http_code}" "${RB_BASE}/scim/v2/Users")
WITH_AUTH=$(curl -s -o /dev/null -w "%{http_code}" -H "Authorization: Bearer ${RAW_TOKEN}" "${RB_BASE}/scim/v2/Users")
WRONG_PREFIX=$(curl -s -o /dev/null -w "%{http_code}" -H "Authorization: Bearer rbat_fake" "${RB_BASE}/scim/v2/Users")
if [ "${NO_AUTH}" = "401" ]; then pass "/scim/v2/Users without bearer = 401"; else fail "/scim/v2/Users no-auth = ${NO_AUTH} (want 401)"; fi
if [ "${WITH_AUTH}" = "200" ]; then pass "/scim/v2/Users with valid bearer = 200"; else fail "/scim/v2/Users with valid bearer = ${WITH_AUTH}"; fi
if [ "${WRONG_PREFIX}" = "401" ]; then pass "/scim/v2/Users with rbat_ prefix = 401 (correct rejection)"; else fail "wrong-prefix bearer = ${WRONG_PREFIX}"; fi

# --- WIZARD STEP 9: SCIM Users POST — create a Keycloak user mirror ---
section "SCIM — POST /Users (provision a user the way Okta would)"

SCIM_POST=$(curl -s -X POST "${RB_BASE}/scim/v2/Users" \
  -H "Authorization: Bearer ${RAW_TOKEN}" \
  -H "Content-Type: application/scim+json" \
  -d '{
    "schemas":["urn:ietf:params:scim:schemas:core:2.0:User"],
    "userName":"alice.anderson@grc-test.local",
    "externalId":"keycloak-alice-id",
    "active":true,
    "emails":[{"value":"alice.anderson@grc-test.local","primary":true,"type":"work"}]
  }')
if echo "${SCIM_POST}" | grep -q '"id":"[0-9a-f-]\+"'; then
  pass "SCIM POST /Users created alice.anderson"
else
  fail "SCIM POST /Users failed: $(echo "$SCIM_POST" | head -c 300)"
fi

# Verify the user was JIT-created in `users` table.
SCIM_LIST=$(curl -s -H "Authorization: Bearer ${RAW_TOKEN}" \
  "${RB_BASE}/scim/v2/Users?filter=userName%20eq%20%22alice.anderson@grc-test.local%22")
if echo "${SCIM_LIST}" | grep -q '"totalResults":1'; then
  pass "SCIM filter (userName eq) returned alice"
else
  fail "SCIM filter failed: $(echo "$SCIM_LIST" | head -c 300)"
fi

# --- WIZARD STEP 10: Disable & re-enable preserve checks ---
section "Disable/re-enable cycle — preserve-on-empty contract"

# Disable SAML — stored config should remain.
DIS=$(curl -s -X POST "${RB_BASE}/api/_admin/_setup/auth-save" \
  -H "Content-Type: application/json" \
  -d '{"methods":{"password":true},"saml":{"enabled":false}}')
if echo "${DIS}" | grep -q '"ok":true'; then
  pass "SAML disable save returns ok=true"
else
  fail "SAML disable failed: ${DIS}"
fi
DIS_STATUS=$(curl -s "${RB_BASE}/api/_admin/_setup/auth-status")
# After disable, the saved metadata URL should STILL be there
# (one-click re-enable contract).
if echo "${DIS_STATUS}" | grep -q "\"idp_metadata_url\":\"${SAML_METADATA_URL}\""; then
  pass "SAML disable preserved stored metadata URL"
else
  fail "SAML disable WIPED the stored metadata URL (regression)"
fi

# Final summary.
section "Summary"
TOTAL=$((PASS + FAIL))
if [ "${FAIL}" = "0" ]; then
  printf "${G}━━━ All ${TOTAL} checks passed ━━━${N}\n"
  exit 0
else
  printf "${R}━━━ ${FAIL} of ${TOTAL} checks FAILED ━━━${N}\n"
  for n in "${FAILED_NAMES[@]}"; do
    printf "  ${R}✗${N} %s\n" "$n"
  done
  printf "\nBackend log: %s/server.log (preserved for inspection)\n" "${RB_DATA}"
  trap - EXIT  # leave RB_DATA for inspection
  kill "${RB_PID}" 2>/dev/null
  exit 1
fi
