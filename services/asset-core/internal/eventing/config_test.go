package eventing

import (
	"context"
	"os"
	"testing"
	"time"
)

func TestRedisConfigFromEnv(t *testing.T) {
	t.Setenv("ASSET_REDIS_HOST", "redis.internal")
	t.Setenv("ASSET_REDIS_PORT", "6380")
	t.Setenv("ASSET_REDIS_PASSWORD", "secret")
	t.Setenv("ASSET_REDIS_DB", "2")
	t.Setenv("ASSET_REDIS_CONNECT_TIMEOUT_MS", "400")
	t.Setenv("ASSET_REDIS_COMMAND_TIMEOUT_MS", "90")

	cfg := RedisConfigFromEnv()
	if cfg.Addr != "redis.internal:6380" || cfg.Password != "secret" || cfg.DB != 2 {
		t.Fatalf("unexpected redis config: %+v", cfg)
	}
	if cfg.DialTimeout != 400*time.Millisecond || cfg.ReadTimeout != 90*time.Millisecond || cfg.WriteTimeout != 90*time.Millisecond {
		t.Fatalf("unexpected redis timeouts: %+v", cfg)
	}
}

func TestRedisConfigFromEnvUsesSafeDefaults(t *testing.T) {
	t.Setenv("ASSET_REDIS_HOST", "")
	t.Setenv("ASSET_REDIS_PORT", "")
	t.Setenv("ASSET_REDIS_DB", "invalid")

	cfg := RedisConfigFromEnv()
	if cfg.Addr != "localhost:6379" || cfg.DB != 0 {
		t.Fatalf("unexpected redis defaults: %+v", cfg)
	}
}

func TestRedisClientLive(t *testing.T) {
	if os.Getenv("ASSET_REDIS_LIVE_TEST") != "1" {
		t.Skip("set ASSET_REDIS_LIVE_TEST=1 to verify a running Redis endpoint")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	defer CloseRedisClient()
	if err := PingRedis(ctx); err != nil {
		t.Fatalf("redis ping failed: %v", err)
	}
}
