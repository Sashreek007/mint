#!/usr/bin/env python3

"""
    this file is to verify if ngix is distributing the requests across keyservice replica
"""

import argparse
import json
import sys
import time
import urllib.request
from collections import Counter


def hit(url: str) -> tuple[str, float]:
    start = time.perf_counter()
    with urllib.request.urlopen(url, timeout=5) as resp:
        body = resp.read().decode("utf-8")
    elapsed_ms = (time.perf_counter() - start) * 1000
    return json.loads(body)["replica"], elapsed_ms


def main():

    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--url", default="http://localhost:8080/healthz")
    parser.add_argument("-n", "--count", type=int, default=100,
                        help="number of requests to send (default:100)")

    parser.add_argument("--min-share", type=float, default=0.10,
                        help="minimum traffic share each replica must get (default 0.10)")
    args = parser.parse_args()

    print(f"hitting {args.url} x {args.count} ...")

    replicas: Counter[str] = Counter()
    latencies: list[float] = []
    failures = 0

    for i in range(args.count):
        try:
            replica, lat = hit(args.url)
            replicas[replica] += 1
            latencies.append(lat)
        except Exception as e:
            failures += 1
            print(f"request {i+1} failed :{e}", file=sys.stderr)

    print()

    print(f"-----results: {args.count -
          failures} ok, {failures} failed --------------")

    # Distribution as a tiny bar chart.
    print(f"\ndistribution across {len(replicas)} replica(s):")
    total = sum(replicas.values()) or 1
    for replica, count in replicas.most_common():
        pct = count / total * 100
        bar = "█" * int(pct / 2)
        print(f"  {replica[:12]:<14} {count:>4}  ({pct:5.1f}%)  {bar}")

    # Latency percentiles.
    if latencies:
        latencies.sort()
        p50 = latencies[len(latencies) // 2]
        p99 = latencies[min(len(latencies) - 1, int(len(latencies) * 0.99))]
        avg = sum(latencies) / len(latencies)
        print(f"\nlatency:")
        print(f"  avg = {avg:6.2f} ms")
        print(f"  p50 = {p50:6.2f} ms")
        print(f"  p99 = {p99:6.2f} ms")

    # Pass/fail.
    print()
    if failures > 0:
        print(f"FAIL: {failures} request(s) failed")
        sys.exit(1)
    if len(replicas) < 2:
        print(f"FAIL: expected ≥ 2 replicas, saw {len(replicas)}")
        sys.exit(1)
    for replica, count in replicas.items():
        share = count / total
        if share < args.min_share:
            print(f"FAIL: replica {replica[:12]} got {share:.1%}, "
                  f"below minimum {args.min_share:.0%}")
            sys.exit(1)
    print("PASS")


if __name__ == "__main__":
    main()
