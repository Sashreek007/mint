# Mint — API Key & Quota Service

A Go service that issues, validates, and meters API keys for other backend services. Other backends call `POST /v1/validate` on every incoming request to check key + rate limit + quota.

## Stack
- **Go 1.24** (stdlib-first, minimal third-party deps)
- **Postgres 16** — source of truth (tenants, api_keys, usage rollups)
- **Redis 7** — hot-path accelerator (cache, rate-limit token buckets, usage counters)
- **nginx** — reverse proxy, load balancer, connection pooler
- **Docker + docker-compose** — local dev and demo

## Architecture, one line
Stateless Go service behind nginx, Postgres as source of truth, Redis as the hot-path accelerator. `/validate` is one Redis round-trip on cache hit; Postgres is off the hot path entirely.

## Design targets
- 5,000 rps sustained on `/validate`
- p99 latency < 20 ms
- Stateless, horizontally scalable
- **Failure mode:** auth fails closed (security), rate limiting fails open (availability)

## Repo layout
```
keyservice/             # main Go service + Dockerfile + .dockerignore
infra/                  # nginx.conf and other infra configs
scripts/                # operational scripts (load tests, smoke checks)
docs/design.html        # interactive HTML design doc — KEEP IN SYNC
docker-compose.yml      # full stack: nginx + 2 keyservice + Postgres + Redis
```

## Verified so far (after Chunk A3)
- 24k rps sustained at realistic concurrency (`hey -c 100`)
- p99 = 17 ms (within the 20 ms budget — substrate-only, no real work yet)
- 50/50 load balancing across 2 replicas (perfect round-robin)

## Optimizations applied
- **Multi-stage Docker build** — ~22 MB final image (vs ~800 MB single-stage)
- **nginx upstream HTTP keep-alive** — `proxy_http_version 1.1` + clear Connection header + `keepalive 32`
- **nginx `worker_processes auto`** — one worker per CPU core (was: 1)
- **nginx `worker_connections 4096`** — 4× per-worker capacity (was: 1024)

## Design decisions locked in
| Question | Decision |
|---|---|
| Key hashing | HMAC-SHA256 + server-side pepper (not Argon2id — too slow at 5k rps) |
| Key format | `ak_live_<32B base62>` (prod) / `ak_test_<...>` (test) |
| Rate-limit algorithm | Token bucket via Redis Lua script |
| Usage pipeline | Redis `INCR` on hot path + background flusher to Postgres (~30–60s) |
| Rate limit vs quota | Two mechanisms (token bucket + simple counter), same Lua call |
| Rotation grace | 48h overlap by default, `force=true` for immediate revoke |
| Hot-path Redis ops | Bundled in one Lua script (single round-trip) |

## Chunks (progress)
- ✓ A1 — bare Go HTTP server with `/healthz`
- ✓ A2 — multi-stage Dockerfile (~22 MB)
- ✓ A3 — docker-compose stack + load testing + nginx tuning
- → **B — Postgres schema + tenants table + admin endpoint (NEXT)**
- ☐ C — API key issuance (POST /v1/tenants/{id}/keys, HMAC hashing)
- ☐ D — `/validate` slow path (Postgres-only, no cache yet)
- ☐ E — Redis cache layer with invalidation
- ☐ F — Rate limiting (token bucket in Lua)
- ☐ G — Usage metering (Redis INCR + batch flusher)
- ☐ H — Observability (slog, /metrics, /readyz)
- ☐ I — `keyservice-go` middleware package
- ☐ J — Demo product API + integration tests

## Conventions
- Module path: `github.com/Sashreek007/mint/keyservice`
- Replica ID: `HOSTNAME` env var (Docker injects), included in `/healthz` JSON
- Admin endpoints: require `X-Admin-Token` header
- Env vars: `DATABASE_URL`, `REDIS_URL`, `ADMIN_TOKEN`, `KEY_PEPPER`
- Docker port mapping: host `8080` → nginx, host `5432` → Postgres, host `6379` → Redis

## How the user wants to be taught
1. **Style: cadence of Arslan Ahmad** — structured, "let us discuss / now the question arises / so what is the solution / pick X." Explain tradeoffs before recommending.
2. **High-level systems design first**, then dive into code.
3. **Tell the user what to do and give the code, but the user writes it themselves** in the files. Do not scaffold non-trivial files without asking.
4. **Define jargon inline.** The user is learning Go and systems design.
5. **Small chunks with checkpoints**, not one big dump. Pause for verification.
6. **After every chunk**, update `docs/design.html`:
   - Add new concepts to that chunk's section in the glossary (chunk-wise structure already in place)
   - Add any optimizations applied (green-tinted cards, separate from concepts)
   - Flip the chunk status in section 05 (done / next / todo)

7. **Push back when the user is wrong.** They want portfolio-quality work.

## When starting a new session
1. Read this file.
2. Skim `docs/design.html` for the up-to-date architecture and glossary.
3. Check `docker compose ps` to see if the stack is running.
4. Pick up from the chunk marked `→ NEXT` above.
