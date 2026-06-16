#!/usr/bin/env bash
#
# Target #3 — usage-metering write reduction.
#
# Load is driven by the Go loadgen (benchmarks/loadgen) with -keys 10 -invalid 0:
# ONE tenant, all-valid traffic, so every request is metered (one Redis INCR).
# This compares requests served against ACTUAL Postgres writes to usage_counters.
#
# Run the stack with throttling OFF + a short flush first:
#   RATE_LIMIT=100000000 RATE_BURST=100000000 FLUSH_INTERVAL=5s \
#     docker compose up --build -d --force-recreate
#
# Then: ./benchmarks/write_reduction.sh
set -euo pipefail

REQS="${REQS:-500000}"          # total requests the Go loadgen sends
C="${C:-50}"                    # concurrency
FLUSH_WAIT="${FLUSH_WAIT:-8}"   # > FLUSH_INTERVAL, so the final flush lands

say()   { printf '\n\033[1;36m== %s ==\033[0m\n' "$*"; }
pg()    { docker compose exec -T postgres psql -U mint -d mint -tA -c "$1"; }
redis() { docker compose exec -T redis redis-cli "$@"; }

say "preflight"
curl -sf http://localhost:8080/healthz >/dev/null && echo "stack up: OK" || { echo "stack not reachable"; exit 1; }
command -v go >/dev/null || { echo "go not installed"; exit 1; }

say "clean slate (FLUSHALL -> only the bench tenant accrues a usage counter)"
redis FLUSHALL >/dev/null

say "baseline Postgres writes to usage_counters"
W0=$(pg "SELECT COALESCE(n_tup_ins,0)+COALESCE(n_tup_upd,0) FROM pg_stat_user_tables WHERE relname='usage_counters';")
W0=${W0:-0}; echo "writes before: $W0"

say "Go loadgen: 1 tenant, all valid, $REQS requests @ c=$C"
LOG=$(mktemp)
( cd benchmarks/loadgen && go run . -keys 10 -invalid 0 -requests "$REQS" -concurrency "$C" ) | tee "$LOG"
REQ=$(awk '/200:/{print $2}' "$LOG" | head -1); REQ=${REQ:-0}

say "wait for the final flush (> FLUSH_INTERVAL)"
sleep "$FLUSH_WAIT"

W1=$(pg "SELECT COALESCE(n_tup_ins,0)+COALESCE(n_tup_upd,0) FROM pg_stat_user_tables WHERE relname='usage_counters';")
W1=${W1:-0}; WRITES=$(( W1 - W0 ))

# integrity check: sum of live Redis counters should equal requests served
LIVE=$(redis --scan --pattern 'usage:*' | tr -d '\r' \
        | while read -r k; do redis GET "$k"; done | tr -d '\r' \
        | awk '{s+=$1} END{print s+0}')

say "RESULT — Target #3 (usage-metering write reduction)"
printf 'requests served (200, = naive PG writes): %s\n' "$REQ"
printf 'actual PG writes to usage_counters:       %s\n' "$WRITES"
printf 'redis live counter sum (~= served):       %s\n' "$LIVE"
if [ "${WRITES:-0}" -gt 0 ] && [ "${REQ:-0}" -gt 0 ]; then
  printf '\nWRITE REDUCTION: %sx   (%s requests : %s Postgres writes)\n' "$(( REQ / WRITES ))" "$REQ" "$WRITES"
fi
rm -f "$LOG"
