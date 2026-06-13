package cache

import (
	"sync"
	"time"
)

// Result is a validation outcome, cached in L1 (as a value) and L2 (as JSON).
type Result struct {
	Valid        bool   `json:"valid"`
	TenantID     string `json:"tenant_id"`
	KeyID        string `json:"key_id"`
	MonthlyQuota int64  `json:"monthly_quota,omitempty"`
}

// entry is one caches value plus when it expires
type entry struct {
	value     any
	expiresAt time.Time
}

// Cache is a map guareded by a read-write mutex
type Cache struct {
	mu   sync.RWMutex
	data map[string]entry
}

func New() *Cache {
	return &Cache{data: make(map[string]entry)}
}

func (c *Cache) Get(key string) (any, bool) {
	c.mu.RLock()
	e, ok := c.data[key]
	c.mu.RUnlock()

	if !ok || time.Now().After(e.expiresAt) {
		return nil, false
	}
	return e.value, true
}

// Set stores a value with a TTL (how long until it expires).
func (c *Cache) Set(key string, value any, ttl time.Duration) {
	c.mu.Lock()
	c.data[key] = entry{value: value, expiresAt: time.Now().Add(ttl)}
	c.mu.Unlock()
}

// Delete removes a key now (used by invalidation in E.5).
func (c *Cache) Delete(key string) {
	c.mu.Lock()
	delete(c.data, key)
	c.mu.Unlock()
}
