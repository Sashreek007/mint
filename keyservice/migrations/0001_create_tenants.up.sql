

CREATE EXTENSION IF NOT EXISTS pgcrypto;

CREATE TABLE tenants(
id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
name TEXT NOT NULL,
status TEXT NOT NULL DEFAULT 'active'
                CHECK ( status IN ('active','suspended','deleted') ),
created_at TIMESTAMPTZ   NOT NULL  DEFAULT now(),
updated_at TIMESTAMPTZ NOT NULL DEFAULT now()

);
