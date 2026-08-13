package consume

import "sync/atomic"

var (
	appliedTotal          atomic.Int64
	duplicateTotal        atomic.Int64
	quarantinedTotal      atomic.Int64
	transientFailureTotal atomic.Int64
)

type ConsumeMetrics struct {
	AppliedTotal          int64
	DuplicateTotal        int64
	QuarantinedTotal      int64
	TransientFailureTotal int64
}

func Metrics() ConsumeMetrics {
	return ConsumeMetrics{
		AppliedTotal:          appliedTotal.Load(),
		DuplicateTotal:        duplicateTotal.Load(),
		QuarantinedTotal:      quarantinedTotal.Load(),
		TransientFailureTotal: transientFailureTotal.Load(),
	}
}

func ResetMetricsForTests() {
	appliedTotal.Store(0)
	duplicateTotal.Store(0)
	quarantinedTotal.Store(0)
	transientFailureTotal.Store(0)
}
