#!/usr/bin/env bash
#
# mint /validate THROUGHPUT benchmark (hey).
#
# This measures raw request-handling speed with a warm cache (one key), which
# isolates rps/latency. It is NOT a hit-rate test — for that use realistic.py
# (many keys, skewed traffic), which models a real workload.
#
# Prereqs: docker compose stack running, `hey` + `python3` installed.
# Usage:   ./benchmarks/run.sh
set -euo pipefail

BASE_URL="${BASE_URL:-http://localhost:8080}"
ADMIN_TOKEN="${ADMIN_TOKEN:-just-works-for-now}"
N="${N:-100000}"      # total requests
C="${C:-50}"          # concurrency (healthy operating point on a laptop)

say() { printf '\n\033[1;36m== %s ==\033[0m\n' "$*"; }

say "preflight"
curl -sf "$BASE_URL/healthz" >/dev/null && echo "stack up: OK" || { echo "stack not reachable"; exit 1; }
command -v hey >/dev/null || { echo "hey not installed (brew install hey)"; exit 1; }

# mint a key
TID=$(curl -s -X POST "$BASE_URL/admin/tenants" \
      -H "X-Admin-Token: $ADMIN_TOKEN" -H "Content-Type: application/json" \
      -d '{"name":"bench"}' | python3 -c "import sys,json;print(json.load(sys.stdin)['id'])")
KEY=$(curl -s -X POST "$BASE_URL/v1/tenants/$TID/keys" \
      -H "X-Admin-Token: $ADMIN_TOKEN" -H "Content-Type: application/json" \
      -d '{"name":"benchkey"}' | python3 -c "import sys,json;print(json.load(sys.stdin)['key'])")

say "warming cache"
hey -n 2000 -c "$C" -m POST -H "Authorization: Bearer $KEY" "$BASE_URL/v1/validate" >/dev/null 2>&1

say "CACHED throughput/latency  (hey -n $N -c $C)"
hey -n "$N" -c "$C" -m POST -H "Authorization: Bearer $KEY" "$BASE_URL/v1/validate" \
  | grep -E "Requests/sec|Average|Slowest|Fastest|50%|95%|99%|\[200\]"

cat <<'NOTE'

------------------------------------------------------------------------
This is THROUGHPUT only (one warm key). For the cache HIT RATE under a
realistic workload (many keys, skewed traffic, 5% invalid), run:

    docker compose exec redis redis-cli FLUSHALL
    PREWARM_LIMIT=0 docker compose up -d --force-recreate keyservice
    python3 benchmarks/realistic.py 1000 300000 50
    for c in $(docker compose ps -q keyservice); do \
      docker exec $c wget -qO- localhost:8080/v1/cache/stats; echo; done

See RESULTS.md for recorded numbers (1k and 10k unique keys).
------------------------------------------------------------------------
NOTE
