package usage

import (
	"context"
	"fmt"
	"log"
	"strconv"
	"time"

	"github.com/Sashreek007/mint/keyservice/internal/store"
	"github.com/redis/go-redis/v9"
)

const leaseKey = "lock:usage-flush"

// Flusher periodically mirrors the live Redis usage counters into Postgres.
type Flusher struct {
	rdb       *redis.Client
	store     *store.Store
	replicaID string
	interval  time.Duration
}

func NewFlusher(rdb *redis.Client, st *store.Store, replicaID string, interval time.Duration) *Flusher {
	return &Flusher{rdb: rdb, store: st, replicaID: replicaID, interval: interval}
}

//Run blocks, flushing once per interval until ctx is cancelled, Only the
//replica that wins the lease flushes each round; the others skip.
