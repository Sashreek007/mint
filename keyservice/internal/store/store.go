// Package store is the Postgres data-access layer. It owns all SQL and the
// connection pool; nothing above it writes a query.
package store

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Sentinel errors — the api layer maps these to HTTP status codes, so the
// store stays ignorant of HTTP and the api layer stays ignorant of SQL codes.
var (
	ErrInvalidTenantID = errors.New("invalid tenant id")
	ErrTenantNotFound  = errors.New("tenant not found")
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
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"created_at"`
}

// CreateTenant inserts a tenant and returns the created row.
// (s *Store) makes this a METHOD on Store — s is the receiver, like self/this.
func (s *Store) CreateTenant(ctx context.Context, name string) (Tenant, error) {
	var t Tenant
	err := s.pool.QueryRow(ctx,
		`INSERT INTO tenants (name)
		 VALUES ($1)
		 RETURNING id, name, status, created_at`,
		name,
	).Scan(&t.ID, &t.Name, &t.Status, &t.CreatedAt)
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
