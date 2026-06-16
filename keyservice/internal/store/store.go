// Package store is the Postgres data-access layer. It owns all SQL and the
// connection pool; nothing above it writes a query.
package store

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Sentinel errors — the api layer maps these to HTTP status codes, so the
// store stays ignorant of HTTP and the api layer stays ignorant of SQL codes.
var (
	ErrInvalidTenantID = errors.New("invalid tenant id")
	ErrTenantNotFound  = errors.New("tenant not found")
	ErrKeyNotValid     = errors.New("key not valid")
)

// Store wraps the pgx pool. The pool field is lowercase => private; callers
// go through the methods, never the raw pool.
type Store struct {
	pool *pgxpool.Pool
}

// New is the constructor convention in Go: a package-level func returning the type.
func New(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool}
}

// Tenant is the row shape returned to callers. JSON tags travel with it so the
// api layer can encode it directly.
type Tenant struct {
	ID           string    `json:"id"`
	Name         string    `json:"name"`
	Status       string    `json:"status"`
	MonthlyQuota *int64    `json:"monthly_quota,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
}

// Usage is one tenant's request count for a period, mirrored from Redis.
type Usage struct {
	TenantID string
	Period   string
	Count    int64
}

// CreateTenant inserts a tenant and returns the created row.
// (s *Store) makes this a METHOD on Store — s is the receiver, like self/this.
func (s *Store) CreateTenant(ctx context.Context, name string, monthlyQuota *int64) (Tenant, error) {
	var t Tenant
	err := s.pool.QueryRow(ctx,
		`INSERT INTO tenants (name,monthly_quota)
		 VALUES ($1,$2)
		 RETURNING id, name, status,monthly_quota, created_at`,
		name, monthlyQuota,
	).Scan(&t.ID, &t.Name, &t.Status, &t.MonthlyQuota, &t.CreatedAt)
	return t, err
}

// APIKey is the stored metadata for a key. Note: no plaintext key field — the
// store never holds it. The handler attaches the plaintext to its response separately.
type APIKey struct {
	ID        string    `json:"id"`
	TenantID  string    `json:"tenant_id"`
	Name      string    `json:"name"`
	KeyPrefix string    `json:"key_prefix"`
	CreatedAt time.Time `json:"created_at"`
}

// CreateAPIKey inserts a key row. It translates Postgres error codes into the
// sentinel errors above so callers never see raw pg codes.
func (s *Store) CreateAPIKey(ctx context.Context, tenantID, name, keyPrefix string, keyHash []byte) (APIKey, error) {
	k := APIKey{TenantID: tenantID, Name: name, KeyPrefix: keyPrefix}
	err := s.pool.QueryRow(ctx,
		`INSERT INTO api_keys (tenant_id, name, key_prefix, key_hash)
		 VALUES ($1, $2, $3, $4)
		 RETURNING id, created_at`,
		tenantID, name, keyPrefix, keyHash,
	).Scan(&k.ID, &k.CreatedAt)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) {
			switch pgErr.Code {
			case "22P02": // invalid_text_representation — id wasn't a UUID
				return APIKey{}, ErrInvalidTenantID
			case "23503": // foreign_key_violation — no such tenant
				return APIKey{}, ErrTenantNotFound
			}
		}
		return APIKey{}, err
	}
	return k, nil
}

// ValidatesKey is the indentity returned when a key cheks out
type ValidatedKey struct {
	KeyID        string
	TenantID     string
	MonthlyQuota int64
}

// ValidateKey looks up a key by its hash and confirms both the key and its tenant are active.
// Any miss collapses to ErrKeyNotValid
func (s *Store) ValidateKey(ctx context.Context, keyHash []byte) (ValidatedKey, error) {
	var vk ValidatedKey
	err := s.pool.QueryRow(ctx,
		`SELECT k.id, k.tenant_id, COALESCE(t.monthly_quota,0)
		FROM api_keys k 
		JOIN tenants t ON t.id = k.tenant_id
		WHERE k.key_hash = $1
		AND k.status = 'active'
		AND t.status = 'active'
		`, keyHash).Scan(&vk.KeyID, &vk.TenantID, &vk.MonthlyQuota)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ValidatedKey{}, ErrKeyNotValid
		}
		return ValidatedKey{}, err
	}
	return vk, nil
}

// RevokeKey marks a key revoked and returns its keu_hash. Caller should evict it from the caches
func (s *Store) RevokeKey(ctx context.Context, keyID string) ([]byte, error) {
	var keyHash []byte
	err := s.pool.QueryRow(ctx,
		`UPDATE api_keys
				SET status = 'revoked', revoked_at = now()
		WHERE id = $1 AND status = 'active'
		RETURNING key_hash`, keyID).Scan(&keyHash)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrKeyNotValid
		}
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "22P02" {
			return nil, ErrInvalidTenantID
		}
		return nil, err
	}
	return keyHash, nil
}

// ActiveKey is one row for cache pre-warming: the hash plus who it belongs to.
type ActiveKey struct {
	KeyHash      []byte
	KeyID        string
	TenantID     string
	MonthlyQuota int64
}

// RecentActiveKeys returns up to limit active keys (tenant also active),
// most-recently-used first — the hot set worth pre-warming into L1.
func (s *Store) RecentActiveKeys(ctx context.Context, limit int) ([]ActiveKey, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT k.key_hash, k.id, k.tenant_id, COALESCE(t.monthly_quota,0)
		   FROM api_keys k
		   JOIN tenants  t ON t.id = k.tenant_id
		  WHERE k.status = 'active' AND t.status = 'active'
		  ORDER BY k.last_used_at DESC NULLS LAST
		  LIMIT $1`,
		limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []ActiveKey
	for rows.Next() {
		var a ActiveKey
		if err := rows.Scan(&a.KeyHash, &a.KeyID, &a.TenantID, &a.MonthlyQuota); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// UpsertUsage mirrors the live Redis counters into Postgres. It writes the
// ABSOLUTE value (count = EXCLUDED.count), so re-running with the same numbers
// is a no-op — that idempotency is what makes a double-flush harmless.
func (s *Store) UpsertUsage(ctx context.Context, rows []Usage) error {
	if len(rows) == 0 {
		return nil
	}
	batch := &pgx.Batch{}
	for _, u := range rows {
		batch.Queue(
			`INSERT INTO usage_counters (tenant_id, period, count, updated_at)
			 VALUES ($1, $2, $3, now())
			 ON CONFLICT (tenant_id, period)
			 DO UPDATE SET count = EXCLUDED.count, updated_at = now()`,
			u.TenantID, u.Period, u.Count,
		)
	}
	br := s.pool.SendBatch(ctx, batch)
	defer br.Close()
	for range rows {
		if _, err := br.Exec(); err != nil {
			return err
		}
	}
	return nil
}

// GetTenantQuota returns a tenant's monthly cap (nil = unlimited)
func (s *Store) GetTenantQuota(ctx context.Context, tenantID string) (*int64, error) {
	var quota *int64
	err := s.pool.QueryRow(ctx,
		`SELECT monthly_quota FROM tenants WHERE id = $1`, tenantID,
	).Scan(&quota)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrTenantNotFound
		}
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "22P02" {
			return nil, ErrInvalidTenantID
		}
		return nil, err
	}
	return quota, nil
}

func (s *Store) Ping(ctx context.Context) error {
	return s.pool.Ping(ctx)
}
