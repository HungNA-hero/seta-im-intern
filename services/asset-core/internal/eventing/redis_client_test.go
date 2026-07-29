package eventing

import (
	"testing"
	"time"
)

func TestPublisherRedisClientDisablesCommandAndDialRetries(t *testing.T) {
	client := newRedisClient(RedisConfig{
		Addr:         "127.0.0.1:1",
		DialTimeout:  time.Millisecond,
		ReadTimeout:  time.Millisecond,
		WriteTimeout: time.Millisecond,
	})
	t.Cleanup(func() { _ = client.Close() })

	options := client.Options()
	// go-redis normalizes the -1 sentinel to zero internally. A configured
	// zero would instead have become the library default of three retries.
	if options.MaxRetries != 0 {
		t.Fatalf("MaxRetries = %d, want disabled value 0", options.MaxRetries)
	}
	if options.DialerRetries != 1 {
		t.Fatalf("DialerRetries = %d, want one total dial attempt", options.DialerRetries)
	}
}
