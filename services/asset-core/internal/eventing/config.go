package eventing

import (
	"os"
	"strconv"
	"strings"
	"time"
)

// RedisConfig contains the asset-core connection settings used by the event
// publisher/relay. Values are sourced only from ASSET_REDIS_* environment
// variables so local and container deployments can use different endpoints.
type RedisConfig struct {
	Addr         string
	Password     string
	DB           int
	DialTimeout  time.Duration
	ReadTimeout  time.Duration
	WriteTimeout time.Duration
}

func RedisConfigFromEnv() RedisConfig {
	host := getenv("ASSET_REDIS_HOST", "localhost")
	port := getenv("ASSET_REDIS_PORT", "6379")
	return RedisConfig{
		Addr:         host + ":" + port,
		Password:     strings.TrimSpace(os.Getenv("ASSET_REDIS_PASSWORD")),
		DB:           getenvInt("ASSET_REDIS_DB", 0),
		DialTimeout:  time.Duration(getenvInt("ASSET_REDIS_CONNECT_TIMEOUT_MS", 250)) * time.Millisecond,
		ReadTimeout:  time.Duration(getenvInt("ASSET_REDIS_COMMAND_TIMEOUT_MS", 75)) * time.Millisecond,
		WriteTimeout: time.Duration(getenvInt("ASSET_REDIS_COMMAND_TIMEOUT_MS", 75)) * time.Millisecond,
	}
}

func getenv(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}

func getenvInt(key string, fallback int) int {
	value, err := strconv.Atoi(strings.TrimSpace(os.Getenv(key)))
	if err != nil || value < 0 {
		return fallback
	}
	return value
}
