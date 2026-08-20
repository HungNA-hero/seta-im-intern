package observability

import (
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"seta-im-intern/go-asset-core/internal/eventing/consume"
	"seta-im-intern/go-asset-core/internal/eventing/outbox"
)

var mediaDurationBucketsSeconds = []float64{1, 2, 5, 10, 30, 60, 120, 300}

type mediaHistogram struct {
	Count   uint64
	Sum     float64
	Buckets []uint64
}

type MediaBacklogs struct {
	QueueOldestAge          time.Duration
	OutboxOldestAge         time.Duration
	ReconciliationOldestAge time.Duration
	Cleanup                 int64
	Quarantine              int64
	QuarantineOldestAge     time.Duration
}

type MediaQuotaHeadroom struct {
	OrganizationID string
	ConsumedBytes  int64
	QuotaBytes     int64
}

var mediaOperationalMetrics = struct {
	sync.Mutex
	Sessions              map[string]uint64
	DeclaredBytes         map[string]uint64
	RetryConflicts        uint64
	AbandonedSessions     uint64
	Attempts              map[string]uint64
	Failures              map[string]uint64
	Replays               map[string]uint64
	ProcessorTerminations map[string]uint64
	QueueAge              mediaHistogram
	ProcessingDuration    mediaHistogram
	AcceptanceToTerminal  mediaHistogram
	LeaseRecoveryLatency  mediaHistogram
	LeaseRecoveries       uint64
	Backlogs              MediaBacklogs
	QuotaOrganizations    map[string]uint64
	HighestQuotaRatio     float64
}{
	Sessions:              make(map[string]uint64),
	DeclaredBytes:         make(map[string]uint64),
	Attempts:              make(map[string]uint64),
	Failures:              make(map[string]uint64),
	Replays:               make(map[string]uint64),
	ProcessorTerminations: make(map[string]uint64),
	QueueAge:              newMediaHistogram(),
	ProcessingDuration:    newMediaHistogram(),
	AcceptanceToTerminal:  newMediaHistogram(),
	LeaseRecoveryLatency:  newMediaHistogram(),
	QuotaOrganizations:    make(map[string]uint64),
}

func newMediaHistogram() mediaHistogram {
	return mediaHistogram{Buckets: make([]uint64, len(mediaDurationBucketsSeconds))}
}

func bounded(value string, allowed ...string) string {
	for _, candidate := range allowed {
		if value == candidate {
			return value
		}
	}
	return "other"
}

func RecordMediaSession(outcome string, declaredBytes int64) {
	outcome = bounded(outcome, "created", "replayed")
	mediaOperationalMetrics.Lock()
	defer mediaOperationalMetrics.Unlock()
	mediaOperationalMetrics.Sessions[outcome]++
	if declaredBytes > 0 {
		mediaOperationalMetrics.DeclaredBytes[outcome] += uint64(declaredBytes)
	}
}

func RecordMediaRetryConflict() {
	mediaOperationalMetrics.Lock()
	defer mediaOperationalMetrics.Unlock()
	mediaOperationalMetrics.RetryConflicts++
}

func RecordMediaAbandonedSessions(count int) {
	if count <= 0 {
		return
	}
	mediaOperationalMetrics.Lock()
	defer mediaOperationalMetrics.Unlock()
	mediaOperationalMetrics.AbandonedSessions += uint64(count)
}

func RecordMediaAttempt(stage, outcome string) {
	stage = bounded(stage, "claim", "validating", "transforming", "terminal")
	outcome = bounded(outcome, "success", "failure", "retry", "recovered", "lease_lost")
	mediaOperationalMetrics.Lock()
	defer mediaOperationalMetrics.Unlock()
	mediaOperationalMetrics.Attempts[stage+"\x00"+outcome]++
}

func RecordMediaFailure(category string) {
	category = bounded(category, "deterministic", "transient", "timeout", "storage", "quarantine", "processor")
	mediaOperationalMetrics.Lock()
	defer mediaOperationalMetrics.Unlock()
	mediaOperationalMetrics.Failures[category]++
}

func RecordMediaReplay(outcome string) {
	outcome = bounded(outcome, "success", "failure")
	mediaOperationalMetrics.Lock()
	defer mediaOperationalMetrics.Unlock()
	mediaOperationalMetrics.Replays[outcome]++
}

func RecordMediaProcessorTermination(reason string) {
	reason = bounded(reason, "timeout", "signal", "resource", "failure")
	mediaOperationalMetrics.Lock()
	defer mediaOperationalMetrics.Unlock()
	mediaOperationalMetrics.ProcessorTerminations[reason]++
}

func observeMediaHistogram(histogram *mediaHistogram, duration time.Duration) {
	seconds := max(0, duration.Seconds())
	histogram.Count++
	histogram.Sum += seconds
	for index, bucket := range mediaDurationBucketsSeconds {
		if seconds <= bucket {
			histogram.Buckets[index]++
		}
	}
}

func ObserveMediaQueueAge(duration time.Duration) {
	mediaOperationalMetrics.Lock()
	defer mediaOperationalMetrics.Unlock()
	observeMediaHistogram(&mediaOperationalMetrics.QueueAge, duration)
}

func ObserveMediaProcessingDuration(duration time.Duration) {
	mediaOperationalMetrics.Lock()
	defer mediaOperationalMetrics.Unlock()
	observeMediaHistogram(&mediaOperationalMetrics.ProcessingDuration, duration)
}

func ObserveMediaAcceptanceToTerminal(duration time.Duration) {
	mediaOperationalMetrics.Lock()
	defer mediaOperationalMetrics.Unlock()
	observeMediaHistogram(&mediaOperationalMetrics.AcceptanceToTerminal, duration)
}

func RecordMediaLeaseRecovery(duration time.Duration) {
	mediaOperationalMetrics.Lock()
	defer mediaOperationalMetrics.Unlock()
	mediaOperationalMetrics.LeaseRecoveries++
	observeMediaHistogram(&mediaOperationalMetrics.LeaseRecoveryLatency, duration)
}

func SetMediaBacklogs(backlogs MediaBacklogs) {
	mediaOperationalMetrics.Lock()
	defer mediaOperationalMetrics.Unlock()
	mediaOperationalMetrics.Backlogs = backlogs
}

func quotaBucket(ratio float64) string {
	switch {
	case ratio < 0.5:
		return "under_50_percent"
	case ratio < 0.75:
		return "50_to_75_percent"
	case ratio < 0.9:
		return "75_to_90_percent"
	case ratio <= 1:
		return "90_to_100_percent"
	default:
		return "over_100_percent"
	}
}

// SetMediaQuotaHeadroom replaces the quota snapshot. Organization identity is
// deliberately available only to the structured alert log, never to a metric
// label or value.
func SetMediaQuotaHeadroom(observations []MediaQuotaHeadroom, logger *slog.Logger) {
	counts := make(map[string]uint64)
	highest := float64(0)
	highestOrg := ""
	for _, observation := range observations {
		if observation.QuotaBytes <= 0 {
			continue
		}
		ratio := float64(max(int64(0), observation.ConsumedBytes)) / float64(observation.QuotaBytes)
		counts[quotaBucket(ratio)]++
		if ratio > highest {
			highest = ratio
			highestOrg = observation.OrganizationID
		}
	}
	mediaOperationalMetrics.Lock()
	mediaOperationalMetrics.QuotaOrganizations = counts
	mediaOperationalMetrics.HighestQuotaRatio = highest
	mediaOperationalMetrics.Unlock()
	if highest >= 0.9 && logger != nil {
		logger.Warn("media quota headroom is low", "organizationId", highestOrg, "consumptionRatio", highest)
	}
}

func renderHistogram(output *strings.Builder, name, help string, histogram mediaHistogram) {
	fmt.Fprintf(output, "# HELP %s %s\n", name, help)
	fmt.Fprintf(output, "# TYPE %s histogram\n", name)
	for index, bucket := range mediaDurationBucketsSeconds {
		fmt.Fprintf(output, "%s_bucket%s %d\n", name, prometheusLabels(map[string]string{"le": fmt.Sprint(bucket)}), histogram.Buckets[index])
	}
	fmt.Fprintf(output, "%s_bucket%s %d\n", name, prometheusLabels(map[string]string{"le": "+Inf"}), histogram.Count)
	fmt.Fprintf(output, "%s_sum %g\n", name, histogram.Sum)
	fmt.Fprintf(output, "%s_count %d\n", name, histogram.Count)
}

func renderMediaPrometheusMetrics() string {
	mediaOperationalMetrics.Lock()
	defer mediaOperationalMetrics.Unlock()

	var output strings.Builder
	fmt.Fprintln(&output, "# HELP seta_asset_media_sessions_total Media upload session outcomes.")
	fmt.Fprintln(&output, "# TYPE seta_asset_media_sessions_total counter")
	fmt.Fprintln(&output, "# HELP seta_asset_media_direct_upload_bytes_total Bytes declared for direct upload.")
	fmt.Fprintln(&output, "# TYPE seta_asset_media_direct_upload_bytes_total counter")
	for outcome, count := range mediaOperationalMetrics.Sessions {
		metricLabels := prometheusLabels(map[string]string{"outcome": outcome})
		fmt.Fprintf(&output, "seta_asset_media_sessions_total%s %d\n", metricLabels, count)
		fmt.Fprintf(&output, "seta_asset_media_direct_upload_bytes_total%s %d\n", metricLabels, mediaOperationalMetrics.DeclaredBytes[outcome])
	}
	fmt.Fprintln(&output, "# TYPE seta_asset_media_retry_conflicts_total counter")
	fmt.Fprintf(&output, "seta_asset_media_retry_conflicts_total %d\n", mediaOperationalMetrics.RetryConflicts)
	fmt.Fprintln(&output, "# TYPE seta_asset_media_abandoned_sessions_total counter")
	fmt.Fprintf(&output, "seta_asset_media_abandoned_sessions_total %d\n", mediaOperationalMetrics.AbandonedSessions)

	renderHistogram(&output, "seta_asset_media_queue_age_seconds", "Age of a job when claimed.", mediaOperationalMetrics.QueueAge)
	renderHistogram(&output, "seta_asset_media_processing_duration_seconds", "Claim-to-settlement processing duration.", mediaOperationalMetrics.ProcessingDuration)
	renderHistogram(&output, "seta_asset_media_acceptance_to_terminal_seconds", "Durable acceptance-to-terminal duration.", mediaOperationalMetrics.AcceptanceToTerminal)
	renderHistogram(&output, "seta_asset_media_lease_recovery_latency_seconds", "Claim-to-recovery latency after lease expiry.", mediaOperationalMetrics.LeaseRecoveryLatency)

	fmt.Fprintln(&output, "# TYPE seta_asset_media_processing_attempts_total counter")
	for key, count := range mediaOperationalMetrics.Attempts {
		parts := strings.SplitN(key, "\x00", 2)
		fmt.Fprintf(&output, "seta_asset_media_processing_attempts_total%s %d\n", prometheusLabels(map[string]string{"stage": parts[0], "outcome": parts[1]}), count)
	}
	fmt.Fprintln(&output, "# TYPE seta_asset_media_failures_total counter")
	for category, count := range mediaOperationalMetrics.Failures {
		fmt.Fprintf(&output, "seta_asset_media_failures_total%s %d\n", prometheusLabels(map[string]string{"category": category}), count)
	}
	fmt.Fprintln(&output, "# TYPE seta_asset_media_timeouts_total counter")
	fmt.Fprintf(&output, "seta_asset_media_timeouts_total %d\n", mediaOperationalMetrics.Failures["timeout"])
	fmt.Fprintln(&output, "# TYPE seta_asset_media_storage_failures_total counter")
	fmt.Fprintf(&output, "seta_asset_media_storage_failures_total %d\n", mediaOperationalMetrics.Failures["storage"])

	fmt.Fprintln(&output, "# TYPE seta_asset_media_cleanup_backlog gauge")
	fmt.Fprintf(&output, "seta_asset_media_cleanup_backlog %d\n", max(int64(0), mediaOperationalMetrics.Backlogs.Cleanup))
	fmt.Fprintln(&output, "# TYPE seta_asset_media_oldest_queued_age_seconds gauge")
	fmt.Fprintf(&output, "seta_asset_media_oldest_queued_age_seconds %g\n", max(0, mediaOperationalMetrics.Backlogs.QueueOldestAge.Seconds()))
	fmt.Fprintln(&output, "# TYPE seta_asset_media_quarantine_backlog gauge")
	fmt.Fprintf(&output, "seta_asset_media_quarantine_backlog %d\n", max(int64(0), mediaOperationalMetrics.Backlogs.Quarantine))
	fmt.Fprintln(&output, "# TYPE seta_asset_media_quarantine_oldest_age_seconds gauge")
	fmt.Fprintf(&output, "seta_asset_media_quarantine_oldest_age_seconds %g\n", max(0, mediaOperationalMetrics.Backlogs.QuarantineOldestAge.Seconds()))
	fmt.Fprintln(&output, "# TYPE seta_asset_media_outbox_oldest_age_seconds gauge")
	fmt.Fprintf(&output, "seta_asset_media_outbox_oldest_age_seconds %g\n", max(0, mediaOperationalMetrics.Backlogs.OutboxOldestAge.Seconds()))
	fmt.Fprintln(&output, "# TYPE seta_asset_media_reconciliation_oldest_age_seconds gauge")
	fmt.Fprintf(&output, "seta_asset_media_reconciliation_oldest_age_seconds %g\n", max(0, mediaOperationalMetrics.Backlogs.ReconciliationOldestAge.Seconds()))

	fmt.Fprintln(&output, "# TYPE seta_asset_media_lease_recoveries_total counter")
	fmt.Fprintf(&output, "seta_asset_media_lease_recoveries_total %d\n", mediaOperationalMetrics.LeaseRecoveries)
	fmt.Fprintln(&output, "# TYPE seta_asset_media_replays_total counter")
	for outcome, count := range mediaOperationalMetrics.Replays {
		fmt.Fprintf(&output, "seta_asset_media_replays_total%s %d\n", prometheusLabels(map[string]string{"outcome": outcome}), count)
	}
	fmt.Fprintln(&output, "# TYPE seta_asset_media_processor_terminations_total counter")
	for reason, count := range mediaOperationalMetrics.ProcessorTerminations {
		fmt.Fprintf(&output, "seta_asset_media_processor_terminations_total%s %d\n", prometheusLabels(map[string]string{"outcome": reason}), count)
	}

	fmt.Fprintln(&output, "# TYPE seta_asset_media_quota_organizations gauge")
	for bucket, count := range mediaOperationalMetrics.QuotaOrganizations {
		fmt.Fprintf(&output, "seta_asset_media_quota_organizations%s %d\n", prometheusLabels(map[string]string{"consumption": bucket}), count)
	}
	fmt.Fprintln(&output, "# TYPE seta_asset_media_quota_highest_consumption_ratio gauge")
	fmt.Fprintf(&output, "seta_asset_media_quota_highest_consumption_ratio %g\n", mediaOperationalMetrics.HighestQuotaRatio)

	relay := outbox.Metrics()
	consumer := consume.Metrics()
	fmt.Fprintln(&output, "# TYPE seta_asset_media_outbox_publish_total counter")
	fmt.Fprintf(&output, "seta_asset_media_outbox_publish_total%s %d\n", prometheusLabels(map[string]string{"outcome": "success"}), relay.PublishedTotal)
	fmt.Fprintf(&output, "seta_asset_media_outbox_publish_total%s %d\n", prometheusLabels(map[string]string{"outcome": "failure"}), relay.PublishFailureTotal)
	fmt.Fprintln(&output, "# TYPE seta_asset_media_outbox_delivery_lag_seconds gauge")
	fmt.Fprintf(&output, "seta_asset_media_outbox_delivery_lag_seconds %g\n", float64(relay.LastDeliveryLagMillis)/1000)
	fmt.Fprintln(&output, "# TYPE seta_asset_media_notifications_total counter")
	fmt.Fprintf(&output, "seta_asset_media_notifications_total%s %d\n", prometheusLabels(map[string]string{"outcome": "applied"}), consumer.AppliedTotal)
	fmt.Fprintf(&output, "seta_asset_media_notifications_total%s %d\n", prometheusLabels(map[string]string{"outcome": "duplicate"}), consumer.DuplicateTotal)
	fmt.Fprintf(&output, "seta_asset_media_notifications_total%s %d\n", prometheusLabels(map[string]string{"outcome": "quarantined"}), consumer.QuarantinedTotal)
	fmt.Fprintf(&output, "seta_asset_media_notifications_total%s %d\n", prometheusLabels(map[string]string{"outcome": "transient_failure"}), consumer.TransientFailureTotal)
	return output.String()
}

func resetMediaMetricsForTests() {
	mediaOperationalMetrics.Lock()
	defer mediaOperationalMetrics.Unlock()
	mediaOperationalMetrics.Sessions = make(map[string]uint64)
	mediaOperationalMetrics.DeclaredBytes = make(map[string]uint64)
	mediaOperationalMetrics.RetryConflicts = 0
	mediaOperationalMetrics.AbandonedSessions = 0
	mediaOperationalMetrics.Attempts = make(map[string]uint64)
	mediaOperationalMetrics.Failures = make(map[string]uint64)
	mediaOperationalMetrics.Replays = make(map[string]uint64)
	mediaOperationalMetrics.ProcessorTerminations = make(map[string]uint64)
	mediaOperationalMetrics.QueueAge = newMediaHistogram()
	mediaOperationalMetrics.ProcessingDuration = newMediaHistogram()
	mediaOperationalMetrics.AcceptanceToTerminal = newMediaHistogram()
	mediaOperationalMetrics.LeaseRecoveryLatency = newMediaHistogram()
	mediaOperationalMetrics.LeaseRecoveries = 0
	mediaOperationalMetrics.Backlogs = MediaBacklogs{}
	mediaOperationalMetrics.QuotaOrganizations = make(map[string]uint64)
	mediaOperationalMetrics.HighestQuotaRatio = 0
}
