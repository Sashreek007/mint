#!/usr/bin/env python3
"""
Realistic /validate workload — models how real backends actually call us.

NOT "one key hammered N times" (that's meaningless). Instead:
  - Many tenants, each owning several keys (1000 keys total by default).
  - Skewed traffic (Zipf-like): a few "hot" keys take most of the traffic,
    the long tail is called rarely — like real customer distributions.
  - A small fraction of requests use invalid/garbage keys (real traffic
    always has some — expired, revoked, typo'd, probing).
  - High request volume so throughput + hit-rate numbers are stable.

Reports: throughput, status-code mix, and (summed across replicas) the
L1/L2/miss breakdown = the honest cache hit rate under a real workload.

Usage:  python3 benchmarks/realistic.py [N_KEYS] [N_REQUESTS] [CONCURRENCY]
        defaults: 1000 keys, 300000 requests, 50 concurrent
"""
import concurrent.futures, json, random, sys, time, urllib.request

BASE = "http://localhost:8080"
ADMIN = "just-works-for-now"


def post(path, body=None, headers=None):
    data = json.dumps(body).encode() if body is not None else b""
    req = urllib.request.Request(BASE + path, data=data, method="POST")
    req.add_header("Content-Type", "application/json")
    for k, v in (headers or {}).items():
        req.add_header(k, v)
    with urllib.request.urlopen(req) as r:
        return r.status, r.read()


def build_keys(n_keys, n_tenants):
    """Create n_tenants tenants, spread n_keys across them, return the key list."""
    keys = []
    per = max(1, n_keys // n_tenants)
    made = 0
    for t in range(n_tenants):
        _, b = post("/admin/tenants", {"name": f"tenant-{t}"}, {"X-Admin-Token": ADMIN})
        tid = json.loads(b)["id"]
        for k in range(per):
            if made >= n_keys:
                break
            _, b = post(f"/v1/tenants/{tid}/keys", {"name": f"k{k}"}, {"X-Admin-Token": ADMIN})
            keys.append(json.loads(b)["key"])
            made += 1
    return keys


def build_request_plan(keys, n_requests, invalid_frac=0.05):
    """Skewed (Zipf) traffic: hot keys dominate. ~invalid_frac bad keys mixed in."""
    n = len(keys)
    # weights ~ 1/rank → first keys far more popular than the tail
    weights = [1.0 / (i + 1) for i in range(n)]
    plan = random.choices(keys, weights=weights, k=n_requests)
    # sprinkle in invalid keys
    n_bad = int(n_requests * invalid_frac)
    for _ in range(n_bad):
        plan[random.randrange(n_requests)] = "ak_live_" + "x" * 32  # garbage
    random.shuffle(plan)
    return plan


def sum_stats():
    # caller prints the docker command; here we just note it
    pass


def main():
    n_keys = int(sys.argv[1]) if len(sys.argv) > 1 else 1000
    n_requests = int(sys.argv[2]) if len(sys.argv) > 2 else 300_000
    concurrency = int(sys.argv[3]) if len(sys.argv) > 3 else 50
    n_tenants = max(1, n_keys // 10)  # ~10 keys per tenant

    print(f"building {n_keys} keys across {n_tenants} tenants ...")
    keys = build_keys(n_keys, n_tenants)
    print(f"  created {len(keys)} keys")

    print(f"planning {n_requests} requests (Zipf-skewed, ~5% invalid) ...")
    plan = build_request_plan(keys, n_requests)

    codes = {}
    lock = __import__("threading").Lock()

    def fire(key):
        try:
            st, _ = post("/v1/validate", None, {"Authorization": "Bearer " + key})
        except urllib.error.HTTPError as e:
            st = e.code
        except Exception:
            st = 0
        with lock:
            codes[st] = codes.get(st, 0) + 1

    print(f"firing {n_requests} requests at concurrency {concurrency} ...")
    t0 = time.time()
    with concurrent.futures.ThreadPoolExecutor(max_workers=concurrency) as ex:
        list(ex.map(fire, plan))
    dt = time.time() - t0

    print(f"\n=== throughput ===")
    print(f"  {n_requests} requests in {dt:.1f}s  =  {n_requests/dt:,.0f} rps")
    print(f"  (Python driver — slower than `hey`; use for WORKLOAD SHAPE, not peak rps)")
    print(f"=== status codes ===")
    for c in sorted(codes):
        print(f"  {c}: {codes[c]}")
    print(f"\n=== cache stats: sum across replicas ===")
    print("  for c in $(docker compose ps -q keyservice); do \\")
    print("    docker exec $c wget -qO- localhost:8080/v1/cache/stats; echo; done")


if __name__ == "__main__":
    main()
