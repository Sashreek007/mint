--per-tenant montly request quota. NULL = unlimited 
ALTER TABLE tenants 
  ADD COLUMN monthly_quota BIGINT NULL;

--Durable mirror of the per period usage counters that live hot in Redis.
--The flusher UPSERTs the latest Redis value here very flush interval. 
CREATE TABLE usage_counters (
  tenant_id UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  period TEXT NOT NULL,
  count BIGINT NOT NULL DEFAULT 0, 
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, period)
);


