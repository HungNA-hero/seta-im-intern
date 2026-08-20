package observability_test

import (
	"log/slog"
	"strings"
	"testing"
	"time"

	"seta-im-intern/go-asset-core/internal/observability"
)

func TestMediaMetricsRenderTheBoundedOperationalContract(t *testing.T) {
	observability.ResetMetricsForTests()
	t.Cleanup(observability.ResetMetricsForTests)

	observability.RecordMediaSession("created", 2048)
	observability.RecordMediaSession("replayed", 4096)
	observability.RecordMediaRetryConflict()
	observability.RecordMediaAbandonedSessions(2)
	observability.ObserveMediaQueueAge(3 * time.Second)
	observability.RecordMediaAttempt("transforming", "success")
	observability.ObserveMediaProcessingDuration(7 * time.Second)
	observability.ObserveMediaAcceptanceToTerminal(11 * time.Second)
	observability.RecordMediaFailure("storage")
	observability.RecordMediaFailure("timeout")
	observability.RecordMediaLeaseRecovery(4 * time.Second)
	observability.RecordMediaReplay("failure")
	observability.SetMediaBacklogs(observability.MediaBacklogs{
		OutboxOldestAge:         2 * time.Second,
		ReconciliationOldestAge: 3 * time.Second,
		Cleanup:                 4,
		Quarantine:              5,
		QuarantineOldestAge:     6 * time.Second,
	})
	secretOrgID := "33333333-3333-4333-8333-333333333333"
	observability.SetMediaQuotaHeadroom([]observability.MediaQuotaHeadroom{{
		OrganizationID: secretOrgID,
		ConsumedBytes:  95,
		QuotaBytes:     100,
	}}, slog.New(slog.NewTextHandler(&strings.Builder{}, nil)))

	rendered := observability.RenderPrometheusMetrics()
	for _, line := range []string{
		`seta_asset_media_sessions_total{outcome="created"} 1`,
		`seta_asset_media_direct_upload_bytes_total{outcome="created"} 2048`,
		`seta_asset_media_retry_conflicts_total 1`,
		`seta_asset_media_abandoned_sessions_total 2`,
		`seta_asset_media_queue_age_seconds_bucket{le="5"} 1`,
		`seta_asset_media_processing_attempts_total{stage="transforming",outcome="success"} 1`,
		`seta_asset_media_failures_total{category="storage"} 1`,
		`seta_asset_media_timeouts_total 1`,
		`seta_asset_media_storage_failures_total 1`,
		`seta_asset_media_cleanup_backlog 4`,
		`seta_asset_media_quarantine_backlog 5`,
		`seta_asset_media_lease_recoveries_total 1`,
		`seta_asset_media_replays_total{outcome="failure"} 1`,
		`seta_asset_media_quota_organizations{consumption="90_to_100_percent"} 1`,
		`seta_asset_media_quota_highest_consumption_ratio 0.95`,
	} {
		if !strings.Contains(rendered, line) {
			t.Fatalf("missing metric %q:\n%s", line, rendered)
		}
	}
	if strings.Contains(rendered, secretOrgID) {
		t.Fatalf("organization identity leaked into metric labels:\n%s", rendered)
	}
}

func TestMediaMetricsFoldUntrustedCategories(t *testing.T) {
	observability.ResetMetricsForTests()
	t.Cleanup(observability.ResetMetricsForTests)

	secret := "photo.jpg?X-Amz-Signature=secret"
	observability.RecordMediaFailure(secret)
	observability.RecordMediaAttempt(secret, secret)
	observability.RecordMediaReplay(secret)

	rendered := observability.RenderPrometheusMetrics()
	if strings.Contains(rendered, secret) {
		t.Fatalf("untrusted value leaked into metric labels:\n%s", rendered)
	}
	for _, line := range []string{
		`seta_asset_media_failures_total{category="other"} 1`,
		`seta_asset_media_processing_attempts_total{stage="other",outcome="other"} 1`,
		`seta_asset_media_replays_total{outcome="other"} 1`,
	} {
		if !strings.Contains(rendered, line) {
			t.Fatalf("missing folded metric %q:\n%s", line, rendered)
		}
	}
}
