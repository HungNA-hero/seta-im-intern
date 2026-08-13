package outbox

import "sync/atomic"

var (
	publishedTotal           atomic.Int64
	publishFailureTotal      atomic.Int64
	outboxUpdateFailureTotal atomic.Int64
	leaseExhaustedTotal      atomic.Int64
	lastDeliveryLagMillis    atomic.Int64
)

type RelayMetrics struct {
	PublishedTotal           int64
	PublishFailureTotal      int64
	OutboxUpdateFailureTotal int64
	LeaseExhaustedTotal      int64
	LastDeliveryLagMillis    int64
}

func Metrics() RelayMetrics {
	return RelayMetrics{
		PublishedTotal:           publishedTotal.Load(),
		PublishFailureTotal:      publishFailureTotal.Load(),
		OutboxUpdateFailureTotal: outboxUpdateFailureTotal.Load(),
		LeaseExhaustedTotal:      leaseExhaustedTotal.Load(),
		LastDeliveryLagMillis:    lastDeliveryLagMillis.Load(),
	}
}

func ResetMetricsForTests() {
	publishedTotal.Store(0)
	publishFailureTotal.Store(0)
	outboxUpdateFailureTotal.Store(0)
	leaseExhaustedTotal.Store(0)
	lastDeliveryLagMillis.Store(0)
}
