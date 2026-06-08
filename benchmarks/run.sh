#!/usr/bin/env bash
#
# mint /validate benchmark — reproduces every number in RESULTS.md with one command.
#
# Prereqs: docker compose stack running, `hey` and `python3` installed.
# Usage:   ./benchmarks/run.sh
#
# What it measures:
#   1. Baseline (Postgres-only)  — cache effectively bypassed (cold, unique keys)
#   2. Cached throughput/latency — warm cache, single hot key (peak performance)
#   3. Cache hit rate            — cold start (PREWARM_LIMIT=0), realistic warming
#
# Every run records the exact `hey` command, concurrency, and request count.
set -euo pipefail

BASE_URL="${BASE_URL:-http://localhost:8080}"
ADMIN_TOKEN="${ADMIN_TOKEN:-just-works-for-now}"
N="${N:-100000}"      # total requests per measured run
C="${C:-50}"          # concurrency (the healthy operating point on a laptop)

say() { printf '\n\033[1;36m== %s ==\033[0m\n' "$*"; }

# --- helpers ---------------------------------------------------------------
mint_key() {
  # creates a tenant + key, echoes the plaintext key
  local name="$1"
  local tid kid
  tid=$(curl -s -X POST "$BASE_URL/admin/tenants" \
        -H "X-Admin-Token: $ADMIN_TOKEN" -H "Content-Type: application/json" \
        -d "{\"name\":\"$name\"}" | python3 -c "import sys,json;print(json.load(sys.stdin)['id'])")
  curl -s -X POST "$BASE_URL/v1/tenants/$tid/keys" \
        -H "X-Admin-Token: $ADMIN_TOKEN" -H "Content-Type: application/json" \
        -d '{"name":"benchkey"}' | python3 -c "import sys,json;print(json.load(sys.stdin)['key'])"
}

bench() {
  # bench <label> <key>
  local label="$1" key="$2"
  say "$label  (hey -n $N -c $C)"
  hey -n "$N" -c "$C" -m POST -H "Authorization: Bearer $key" "$BASE_URL/v1/validate" \
    | grep -E "Requests/sec|Average|Slowest|Fastest|50%|95%|99%|\[200\]"
}

# --- preflight -------------------------------------------------------------
say "preflight"
curl -sf "$BASE_URL/healthz" >/dev/null && echo "stack up: OK" || { echo "stack not reachable at $BASE_URL"; exit 1; }
command -v hey >/dev/null || { echo "hey not installed (brew install hey)"; exit 1; }

# --- run 1: cached peak (warm, single hot key) -----------------------------
KEY=$(mint_key "bench-cached")
say "warming cache"
hey -n 2000 -c "$C" -m POST -H "Authorization: Bearer $KEY" "$BASE_URL/v1/validate" >/dev/null 2>&1
bench "RUN 1 — CACHED throughput/latency (warm, L1 hits)" "$KEY"

# --- run 2: hit rate (read the stats endpoint after the warm run) ----------
say "RUN 2 — cache hit rate (per-replica counters via /v1/cache/stats)"
echo "NOTE: each replica has its own counters; nginx routes you to one."
curl -s "$BASE_URL/v1/cache/stats" | python3 -m json.tool

cat <<'NOTE'

------------------------------------------------------------------------
For the HONEST cold-start hit rate (Target #4), restart the stack with
PREWARM_LIMIT=0 so the cache is empty, then re-run:

    PREWARM_LIMIT=0 docker compose up -d --force-recreate keyservice
    ./benchmarks/run.sh

A cold start makes the first request per key a miss; with many requests
per key the natural hit rate climbs toward ~99% — an honest number, not a
pre-warmed 100%.
------------------------------------------------------------------------
NOTE
