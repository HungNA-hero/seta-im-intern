package main

import (
	"context"
	"testing"
	"time"

	"seta-im-intern/go-asset-core/internal/domain"
)

func TestRunRetentionCleanupTick_SkipsOutsideConfiguredHour(t *testing.T) {
	repo := &fakeCleanupSchedulerRepository{}
	location, err := time.LoadLocation("Asia/Bangkok")
	if err != nil {
		t.Fatalf("load test timezone: %v", err)
	}

	_, due, err := runRetentionCleanupTick(context.Background(), repo, "scheduler-a", time.Date(2026, time.August, 18, 1, 59, 0, 0, location), location)
	if err != nil {
		t.Fatalf("run non-due scheduler tick: %v", err)
	}
	if due {
		t.Fatal("expected tick outside 02:00 hour not to be due")
	}
	if repo.calls != 0 {
		t.Fatalf("expected repository not to be called outside 02:00 hour, got %d calls", repo.calls)
	}
}

func TestRunRetentionCleanupTick_UsesConfiguredLocalDateAndTimezone(t *testing.T) {
	repo := &fakeCleanupSchedulerRepository{
		result: domain.LifecycleCleanupRunAcquireResult{
			LeaseAcquired: true,
			Created:       true,
			Run: &domain.LifecycleCleanupRun{
				ID: "run-1",
			},
		},
	}
	location, err := time.LoadLocation("Asia/Bangkok")
	if err != nil {
		t.Fatalf("load test timezone: %v", err)
	}

	result, due, err := runRetentionCleanupTick(context.Background(), repo, "scheduler-a", time.Date(2026, time.August, 17, 19, 30, 0, 0, time.UTC), location)
	if err != nil {
		t.Fatalf("run due scheduler tick: %v", err)
	}
	if !due || !result.LeaseAcquired || !result.Created {
		t.Fatalf("expected due tick to return acquired created run, got due=%t result=%+v", due, result)
	}
	if repo.calls != 1 {
		t.Fatalf("expected one repository call, got %d", repo.calls)
	}
	if repo.schedulerName != retentionSchedulerName || repo.workerID != "scheduler-a" {
		t.Fatalf("unexpected scheduler call identity: name=%q worker=%q", repo.schedulerName, repo.workerID)
	}
	if repo.runDate.Format("2006-01-02") != "2026-08-18" {
		t.Fatalf("expected Bangkok calendar date 2026-08-18, got %s", repo.runDate.Format("2006-01-02"))
	}
	if repo.timezone != "Asia/Bangkok" {
		t.Fatalf("expected Asia/Bangkok timezone, got %q", repo.timezone)
	}
}

func TestRetentionCleanupLocationFromEnv_UsesDefaultAndRejectsInvalidTimezone(t *testing.T) {
	t.Setenv("ASSET_RETENTION_CLEANUP_TIMEZONE", "")
	location, err := retentionCleanupLocationFromEnv()
	if err != nil {
		t.Fatalf("load default cleanup timezone: %v", err)
	}
	if location.String() != "Asia/Bangkok" {
		t.Fatalf("expected Asia/Bangkok default, got %q", location.String())
	}

	t.Setenv("ASSET_RETENTION_CLEANUP_TIMEZONE", "not/a-real-timezone")
	if _, err := retentionCleanupLocationFromEnv(); err == nil {
		t.Fatal("expected invalid cleanup timezone to fail")
	}
}

type fakeCleanupSchedulerRepository struct {
	result        domain.LifecycleCleanupRunAcquireResult
	err           error
	calls         int
	schedulerName string
	workerID      string
	runDate       time.Time
	timezone      string
}

func (f *fakeCleanupSchedulerRepository) AcquireDailyCleanupRun(_ context.Context, schedulerName, workerID string, runDate time.Time, timezone string) (domain.LifecycleCleanupRunAcquireResult, error) {
	f.calls++
	f.schedulerName = schedulerName
	f.workerID = workerID
	f.runDate = runDate
	f.timezone = timezone
	return f.result, f.err
}
