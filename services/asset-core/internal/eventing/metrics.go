package eventing

import "sync/atomic"

// Counters track lifecycle-event publishing outcomes. They are process-local
// so Asset Core and the deletion worker expose their own producer outcomes.
var (
	publishSuccessTotal atomic.Int64
	publishFailureTotal atomic.Int64
)

func recordPublishSuccess() {
	publishSuccessTotal.Add(1)
}

func recordPublishFailure() {
	publishFailureTotal.Add(1)
}

// MetricsSnapshot is a point-in-time read of the publisher counters.
type MetricsSnapshot struct {
	PublishSuccessTotal int64
	PublishFailureTotal int64
}

func Metrics() MetricsSnapshot {
	return MetricsSnapshot{
		PublishSuccessTotal: publishSuccessTotal.Load(),
		PublishFailureTotal: publishFailureTotal.Load(),
	}
}

// ResetMetricsForTests zeroes the counters between test cases.
func ResetMetricsForTests() {
	publishSuccessTotal.Store(0)
	publishFailureTotal.Store(0)
}
