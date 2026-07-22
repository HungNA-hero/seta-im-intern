package eventing

import "sync/atomic"

// Counters track lifecycle-event publishing outcomes. They are process-local
// and cheap; a metrics backend can scrape them via a future exporter without
// this package depending on one.
var (
	publishedTotal   atomic.Int64
	lostPublishTotal atomic.Int64
)

func recordPublishSuccess() {
	publishedTotal.Add(1)
}

func recordLostPublish() {
	lostPublishTotal.Add(1)
}

// MetricsSnapshot is a point-in-time read of the publisher counters.
type MetricsSnapshot struct {
	PublishedTotal   int64
	LostPublishTotal int64
}

func Metrics() MetricsSnapshot {
	return MetricsSnapshot{
		PublishedTotal:   publishedTotal.Load(),
		LostPublishTotal: lostPublishTotal.Load(),
	}
}

// ResetMetricsForTests zeroes the counters between test cases.
func ResetMetricsForTests() {
	publishedTotal.Store(0)
	lostPublishTotal.Store(0)
}
