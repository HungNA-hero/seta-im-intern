package eventing

import (
	"context"
	"sync"

	"github.com/redis/go-redis/v9"
)

var (
	clientOnce sync.Once
	client     *redis.Client
)

// RedisClient returns the process-wide go-redis client. The client owns its
// connection pool and is safe for concurrent publisher/relay use.
func RedisClient() *redis.Client {
	clientOnce.Do(func() {
		client = newRedisClient(RedisConfigFromEnv())
	})
	return client
}

func newRedisClient(cfg RedisConfig) *redis.Client {
	return redis.NewClient(&redis.Options{
		Addr:         cfg.Addr,
		Password:     cfg.Password,
		DB:           cfg.DB,
		DialTimeout:  cfg.DialTimeout,
		ReadTimeout:  cfg.ReadTimeout,
		WriteTimeout: cfg.WriteTimeout,
		// go-redis treats zero as its three-retry default; -1 is the
		// explicit no-command-retry setting for best-effort publication.
		MaxRetries: -1,
		// One dial attempt prevents the connection pool's five-attempt default.
		DialerRetries: 1,
	})
}

// PingRedis verifies the configured endpoint without creating another client.
func PingRedis(ctx context.Context) error {
	return RedisClient().Ping(ctx).Err()
}

func CloseRedisClient() error {
	if client == nil {
		return nil
	}
	return client.Close()
}
