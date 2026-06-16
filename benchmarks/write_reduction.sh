#!/usr/bin/env bash
#
# Target #3 — usage-metering write reduction.
#
# Load is driven by the Go loadgen (benchmarks/loadgen). The hot path meters every
# request with a Redis INCR; the background flusher mirrors the live counters to
# Postgres once per FLUSH_INTERVAL (one batched UPSERT per active tenant). This
# compares requests served against ACTUAL Postgres writes to usage_counters.
#
#     reduction = requests_served / postgres_writes
#               = (rps * flush_interval) / active_tenants
#
# DEFAULT = REALISTIC: ~300 tenants (KEYS/10), all-valid traffic, 30s flush — the
# operating point the ~500x target assumes. Run the stack to match:
#
#   RATE_LIMIT=100000000 RATE_BURST=100000000 FLUSH_INTERVAL=30s \
#     docker compose up -d --build --force-recreate keyservice
#
# Then: ./benchmarks/write_reduction.sh
# (Set KEYS=10 for the single-tenant "mechanism upper-bound" check.)
set -euo pipefail

KEYS="${KEYS:-3000}"            # ~10 keys/tenant -> ~300 tenants (realistic)
REQS="${REQS:-600000}"         # total requests the Go loadgen sends
C="${C:-50}"                   # concurrency
INVALID="${INVALID:-0}"        # invalid fraction (0 -> every request is metered)
FLUSH_WAIT="${FLUSH_WAIT:-35}" # > FLUSH_INTERVAL, so the final flush lands

say()   { printf '\n\033[1;36m== %s ==\033[0m\n' "$*"; }
pg()    { docker compose exec -T postgres psql -U mint -d mint -tA -c "$1"; }
redis() { docker compose exec -T redis redis-cli "$@"; }

say "preflight"
curl -sf http://localhost:8080/healthz >/dev/null && echo "stack up: OK" || { echo "stack not reachable"; exit 1; }
command -v go >/dev/null || { echo "go not installed"; exit 1; }

say "clean slate (FLUSHALL -> only this run's tenants accrue usage counters)"
redis FLUSHALL >/dev/null

say "baseline Postgres writes to usage_counters"
W0=$(pg "SELECT COALESCE(n_tup_ins,0)+COALESCE(n_tup_upd,0) FROM pg_stat_user_tables WHERE relname='usage_counters';")
W0=${W0:-0}; echo "writes before: $W0"

say "Go loadgen: $KEYS keys (~$((KEYS/10)) tenants), invalid=$INVALID, $REQS requests @ c=$C"
LOG=$(mktemp)
( cd benchmarks/loadgen && go run . -keys "$KEYS" -invalid "$INVALID" -requests "$REQS" -concurrency "$C" ) | tee "$LOG"
REQ=$(awk '/200:/{print $2}' "$LOG" | head -1); REQ=${REQ:-0}

say "wait for the final flush (> FLUSH_INTERVAL)"
sleep "$FLUSH_WAIT"

W1=$(pg "SELECT COALESCE(n_tup_ins,0)+COALESCE(n_tup_upd,0) FROM pg_stat_user_tables WHERE relname='usage_counters';")
W1=${W1:-0}; WRITES=$(( W1 - W0 ))

# active tenants this run = number of usage:* counter keys in Redis (post-FLUSHALL)
KEYLIST=$(redis --scan --pattern 'usage:*' | tr -d '\r')
TENANTS=$(printf '%s\n' "$KEYLIST" | grep -c . || true)
# integrity: sum of live Redis counters should equal metered requests.
# One MGET (keys have no spaces, so unquoted expansion is safe) — a per-key
# `while read | docker exec` loop would let docker-exec steal the loop's stdin.
if [ "${TENANTS:-0}" -gt 0 ]; then
  LIVE=$(redis MGET $KEYLIST | tr -d '\r' | awk '{s+=$1} END{print s+0}')
else
  LIVE=0
fi

say "RESULT — Target #3 (usage-metering write reduction)"
printf 'requests metered (200, = naive PG writes): %s\n' "$REQ"
printf 'active tenants (usage counters in Redis):  %s\n' "$TENANTS"
printf 'actual PG writes to usage_counters:        %s\n' "$WRITES"
printf 'redis live counter sum (~= metered):       %s\n' "$LIVE"
if [ "${WRITES:-0}" -gt 0 ] && [ "${REQ:-0}" -gt 0 ]; then
  printf '\nWRITE REDUCTION: %sx   (%s requests : %s Postgres writes, %s tenants)\n' \
    "$(( REQ / WRITES ))" "$REQ" "$WRITES" "$TENANTS"
fi
rm -f "$LOG"
