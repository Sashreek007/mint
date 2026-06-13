-- Combined gate: token-bucket rate limite + monthly quota +usage meter
-- One atomix Redis round-trip. Returns: 0=RATE_LIMITED, 1 = ALLOWED, 2= QUOTA_EXCEEDED
-- KEYS[1] = bucket key (per key) KEYS[2] = usage counter key (per tenant, per month)
-- ARGV[1] = rate/sec ARGV[2] = capacity ARGV[3] = now(sec)
-- ARGV[4] = quota(monthly cap; <= 0 = unlimited )  ARGV[5] = usage key TTL(sec)
---@diagnostic disable: undefined-global

local bucketKey = KEYS[1]
local usageKey = KEYS[2]
local rate = tonumber(ARGV[1])
local capacity = tonumber(ARGV[2])
local now = tonumber(ARGV[3])
local quota = tonumber(ARGV[4])
local usage_ttl = tonumber(ARGV[5])

-- read current state + refill the token bucket
local state = redis.call("HMGET", bucketKey, "tokens", "ts")
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

-- decide the verdict

local status
if tokens < 1 then
	status = 0
else
	local used = tonumber(redis.call("GET", usageKey)) or 0
	if quota > 0 and used >= quota then
		status = 2 -- QUOTA_EXCEEDED
	else
		tokens = tokens - 1
		local newused = redis.call("INCR", usageKey)
		if newused == 1 then
			redis.call("EXPIRE", usageKey, usage_ttl)
		end
		status = 1 -- ALLOWED + metered
	end
end

-- write state back, with a TTL so idle buckets self-clean
redis.call("HSET", bucketKey, "tokens", tokens, "ts", ts)
redis.call("EXPIRE", bucketKey, math.ceil(capacity / rate) + 1)

return status
