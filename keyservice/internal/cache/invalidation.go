package cache

import (
	"context"
	"log"

	"github.com/redis/go-redis/v9"
)

const RevocationChannel = "revocations"

// PublishRevocation broadcasts a key hash so every replica evixts it from L1
func PublishRevocation(ctx context.Context, rdb *redis.Client, keyHash string) error {
	return rdb.Publish(ctx, RevocationChannel, keyHash).Err()
}

// SubscribeRevocations runs forever. it listens on to the revocations channel and deletes each rexeived key hash from the local l1 cache
func SubscribeRevocations(ctx context.Context, rdb *redis.Client, l1 *Cache) {
	sub := rdb.Subscribe(ctx, RevocationChannel)
	defer sub.Close()

	ch := sub.Channel()
	for msg := range ch {
		l1.Delete(msg.Payload)
		log.Printf("evicted revoke key from l1")
	}
}
