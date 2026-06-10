-- Token bucket rate limiter, Returns 1 if allowed, 0 if rate limited
-- KEYS[1] = bucket key    ARGV[1] = rate/sec      ARGV[2] = capacity   ARGV[3] = now(sec)
---@diagnostic disable: undefined-global

local key = KEYS[1]
local rate = tonumber(ARGV[1])
local capacity = tonumber(ARGV[2])
local now = tonumber(ARGV[3])

-- read current state
local state = redis.call("HMGET", key, "tokens", "ts")
local tokens = tonumber(state[1])
local ts = tonumber(state[2])

if tokens == nil then
	tokens = capacity
	ts = now
end

-- refill: add tokens for the time elapsed, capped at cpacity

local elapsed = math.max(0, now - ts)
tokens = math.min(capacity, tokens + elapsed * rate)
ts = now

-- try to spend one token

local allowed = 0
if tokens >= 1 then
	tokens = tokens - 1
	allowed = 1
end

-- write state back, with a TTL so idle buckets self-clean
redis.call("HSET", key, "tokens", tokens, "ts", ts)
redis.call("EXPIRE", key, math.ceil(capacity / rate) + 1)

return allowed
