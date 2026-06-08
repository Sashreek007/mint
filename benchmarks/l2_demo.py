#!/usr/bin/env python3
"""
L2 demonstration for mint /validate.

The normal benchmark hammers ONE warm key, so every request is an L1 hit and
L2 is never reached. L2 only earns its keep when L1 misses but L2 has the value
— which happens with MANY distinct keys across MULTIPLE replicas: a key cached
in replica-1's L1 is an L1 *miss* on replica-2, but an L2 *hit* (replica-1 put
it in shared Redis).

This script mints a pool of keys and validates them across both replicas, then
sums the per-replica counters so the L2 tier shows real usage.

Usage:  python3 benchmarks/l2_demo.py [NKEYS] [PASSES]
"""
import concurrent.futures
import json
import random
import sys
import time
import urllib.request

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


def main():
    nkeys = int(sys.argv[1]) if len(sys.argv) > 1 else 300
    passes = int(sys.argv[2]) if len(sys.argv) > 2 else 5

    print(f"minting {nkeys} keys ...")
    _, b = post("/admin/tenants", {"name": "l2demo"}, {"X-Admin-Token": ADMIN})
    tid = json.loads(b)["id"]
    keys = []
    for i in range(nkeys):
        _, b = post(f"/v1/tenants/{tid}/keys", {"name": f"k{i}"}, {"X-Admin-Token": ADMIN})
        keys.append(json.loads(b)["key"])
    print("minted.")

    # each key validated `passes` times, all shuffled together
    reqs = keys * passes
    random.shuffle(reqs)

    def validate(key):
        try:
            post("/v1/validate", None, {"Authorization": "Bearer " + key})
        except Exception:
            pass

    print(f"firing {len(reqs)} validates across {nkeys} keys (concurrency 50) ...")
    t0 = time.time()
    with concurrent.futures.ThreadPoolExecutor(max_workers=50) as ex:
        list(ex.map(validate, reqs))
    dt = time.time() - t0
    print(f"done in {dt:.1f}s  (~{len(reqs)/dt:.0f} rps)")
    print("\nNow sum per-replica stats:")
    print("  for c in $(docker compose ps -q keyservice); do \\")
    print("    docker exec $c wget -qO- localhost:8080/v1/cache/stats; echo; done")


if __name__ == "__main__":
    main()
