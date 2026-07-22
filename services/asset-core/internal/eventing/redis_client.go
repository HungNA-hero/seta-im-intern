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
		cfg := RedisConfigFromEnv()
		client = redis.NewClient(&redis.Options{
			Addr:         cfg.Addr,
			Password:     cfg.Password,
			DB:           cfg.DB,
			DialTimeout:  cfg.DialTimeout,
			ReadTimeout:  cfg.ReadTimeout,
			WriteTimeout: cfg.WriteTimeout,
			MaxRetries:   0,
		})
	})
	return client
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
