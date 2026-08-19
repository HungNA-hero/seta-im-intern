package repository_test

import (
	"context"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"seta-im-intern/go-asset-core/internal/domain"
	"seta-im-intern/go-asset-core/internal/repository"
)

func TestLifecycleCleanupScheduler_OneLeaseOwnerCreatesOneDailyRun_PostgresIntegration(t *testing.T) {
	database := openLifecycleCleanupSchedulerTestDatabase(t)
	tx := rollbackOnlyTransaction(t, database)
	repo := repository.NewLifecycleCleanupSchedulerRepository(tx)

	runDate := time.Date(2026, time.August, 18, 2, 0, 0, 0, time.FixedZone("Asia/Bangkok", 7*60*60))
	schedulerName := "daily-retention-cleanup-lease-test"
	first, err := repo.AcquireDailyCleanupRun(context.Background(), schedulerName, "scheduler-a", runDate, "Asia/Bangkok")
	if err != nil {
		t.Fatalf("acquire first scheduler lease: %v", err)
	}
	if !first.LeaseAcquired || !first.Created || first.Run == nil {
		t.Fatalf("expected first scheduler to acquire and create one run, got %+v", first)
	}

	second, err := repo.AcquireDailyCleanupRun(context.Background(), schedulerName, "scheduler-b", runDate, "Asia/Bangkok")
	if err != nil {
		t.Fatalf("acquire competing scheduler lease: %v", err)
	}
	if second.LeaseAcquired || second.Created || second.Run != nil {
		t.Fatalf("expected competing scheduler to create nothing while the lease is live, got %+v", second)
	}

	var runs int64
	if err := tx.Model(&domain.LifecycleCleanupRun{}).
		Where("scheduler_name = ? AND run_date = ?::date", schedulerName, "2026-08-18").
		Count(&runs).Error; err != nil {
		t.Fatalf("count daily cleanup runs: %v", err)
	}
	if runs != 1 {
		t.Fatalf("expected one daily cleanup run, got %d", runs)
	}
}

func TestLifecycleCleanupScheduler_DailyRunKeyPreventsDuplicateAfterLeaseExpiry_PostgresIntegration(t *testing.T) {
	database := openLifecycleCleanupSchedulerTestDatabase(t)
	tx := rollbackOnlyTransaction(t, database)
	repo := repository.NewLifecycleCleanupSchedulerRepository(tx)

	runDate := time.Date(2026, time.August, 18, 2, 0, 0, 0, time.FixedZone("Asia/Bangkok", 7*60*60))
	schedulerName := "daily-retention-cleanup-run-key-test"
	first, err := repo.AcquireDailyCleanupRun(context.Background(), schedulerName, "scheduler-a", runDate, "Asia/Bangkok")
	if err != nil {
		t.Fatalf("acquire initial scheduler lease: %v", err)
	}
	if !first.LeaseAcquired || !first.Created || first.Run == nil {
		t.Fatalf("expected initial run creation, got %+v", first)
	}

	if err := tx.Exec(`
		UPDATE asset_lifecycle_scheduler_leases
		SET lease_expires_at = statement_timestamp() - interval '1 second'
		WHERE scheduler_name = ?
	`, schedulerName).Error; err != nil {
		t.Fatalf("expire scheduler lease: %v", err)
	}

	second, err := repo.AcquireDailyCleanupRun(context.Background(), schedulerName, "scheduler-b", runDate, "Asia/Bangkok")
	if err != nil {
		t.Fatalf("acquire expired scheduler lease: %v", err)
	}
	if !second.LeaseAcquired || second.Created || second.Run == nil {
		t.Fatalf("expected replacement scheduler to observe, not duplicate, the existing run, got %+v", second)
	}
	if second.Run.ID != first.Run.ID {
		t.Fatalf("expected existing run %s after lease expiry, got %s", first.Run.ID, second.Run.ID)
	}
}

func TestLifecycleCleanupScheduler_LeaseRefreshUpdatesAuditTimestamp_PostgresIntegration(t *testing.T) {
	database := openLifecycleCleanupSchedulerTestDatabase(t)
	tx := rollbackOnlyTransaction(t, database)
	repo := repository.NewLifecycleCleanupSchedulerRepository(tx)

	runDate := time.Date(2026, time.August, 18, 2, 0, 0, 0, time.FixedZone("Asia/Bangkok", 7*60*60))
	schedulerName := "daily-retention-cleanup-audit-test"
	if _, err := repo.AcquireDailyCleanupRun(context.Background(), schedulerName, "scheduler-a", runDate, "Asia/Bangkok"); err != nil {
		t.Fatalf("acquire initial scheduler lease: %v", err)
	}

	var before time.Time
	if err := tx.Table("asset_lifecycle_scheduler_leases").
		Select("updated_at").
		Where("scheduler_name = ?", schedulerName).
		Scan(&before).Error; err != nil {
		t.Fatalf("read lease audit timestamp before refresh: %v", err)
	}
	time.Sleep(5 * time.Millisecond)
	if _, err := repo.AcquireDailyCleanupRun(context.Background(), schedulerName, "scheduler-a", runDate, "Asia/Bangkok"); err != nil {
		t.Fatalf("refresh scheduler lease: %v", err)
	}
	var after time.Time
	if err := tx.Table("asset_lifecycle_scheduler_leases").
		Select("updated_at").
		Where("scheduler_name = ?", schedulerName).
		Scan(&after).Error; err != nil {
		t.Fatalf("read lease audit timestamp after refresh: %v", err)
	}
	if !after.After(before) {
		t.Fatalf("expected lease refresh to advance updated_at, before=%s after=%s", before, after)
	}
}

func TestLifecycleCleanupScheduler_GORMDefaultsMatchDatabaseContract_PostgresIntegration(t *testing.T) {
	database := openLifecycleCleanupSchedulerTestDatabase(t)
	tx := rollbackOnlyTransaction(t, database)
	schedulerName := "daily-retention-cleanup-gorm-defaults-test"
	if err := tx.Exec("INSERT INTO asset_lifecycle_scheduler_leases (scheduler_name) VALUES (?)", schedulerName).Error; err != nil {
		t.Fatalf("seed scheduler lease: %v", err)
	}

	run := domain.LifecycleCleanupRun{
		Scheduler: schedulerName,
		RunDate:   time.Date(2026, time.August, 18, 0, 0, 0, 0, time.UTC),
		Timezone:  "Asia/Bangkok",
	}
	if err := tx.Create(&run).Error; err != nil {
		t.Fatalf("create cleanup run through GORM defaults: %v", err)
	}
	var persisted domain.LifecycleCleanupRun
	if err := tx.Where("id = ?", run.ID).First(&persisted).Error; err != nil {
		t.Fatalf("read cleanup run created through GORM: %v", err)
	}
	if persisted.Status != domain.LifecycleCleanupRunQueued {
		t.Fatalf("expected GORM-created run status QUEUED, got %q", persisted.Status)
	}
	if string(persisted.Checkpoint) != "{}" {
		t.Fatalf("expected GORM-created run checkpoint {}, got %s", persisted.Checkpoint)
	}
}

func TestLifecycleCleanupScheduler_CompetingInstancesCreateExactlyOneRun_PostgresIntegration(t *testing.T) {
	database := openLifecycleCleanupSchedulerTestDatabase(t)
	schedulerName := "cleanup-race-" + uuid.NewString()
	t.Cleanup(func() {
		if err := database.Exec("DELETE FROM asset_lifecycle_cleanup_runs WHERE scheduler_name = ?", schedulerName).Error; err != nil {
			t.Errorf("delete race-test cleanup runs: %v", err)
		}
		if err := database.Exec("DELETE FROM asset_lifecycle_scheduler_leases WHERE scheduler_name = ?", schedulerName).Error; err != nil {
			t.Errorf("delete race-test scheduler lease: %v", err)
		}
	})

	type attempt struct {
		result domain.LifecycleCleanupRunAcquireResult
		err    error
	}
	results := make(chan attempt, 2)
	start := make(chan struct{})
	var waitGroup sync.WaitGroup
	for _, workerID := range []string{"scheduler-a", "scheduler-b"} {
		waitGroup.Add(1)
		go func(workerID string) {
			defer waitGroup.Done()
			<-start
			result, err := repository.NewLifecycleCleanupSchedulerRepository(database).AcquireDailyCleanupRun(
				context.Background(),
				schedulerName,
				workerID,
				time.Date(2026, time.August, 18, 2, 0, 0, 0, time.FixedZone("Asia/Bangkok", 7*60*60)),
				"Asia/Bangkok",
			)
			results <- attempt{result: result, err: err}
		}(workerID)
	}
	close(start)
	waitGroup.Wait()
	close(results)

	leaseOwners := 0
	createdRuns := 0
	for item := range results {
		if item.err != nil {
			t.Fatalf("acquire competing scheduler lease: %v", item.err)
		}
		if item.result.LeaseAcquired {
			leaseOwners++
		}
		if item.result.Created {
			createdRuns++
		}
	}
	if leaseOwners != 1 {
		t.Fatalf("expected exactly one lease owner, got %d", leaseOwners)
	}
	if createdRuns != 1 {
		t.Fatalf("expected exactly one created run, got %d", createdRuns)
	}

	var runs int64
	if err := database.Model(&domain.LifecycleCleanupRun{}).
		Where("scheduler_name = ?", schedulerName).
		Count(&runs).Error; err != nil {
		t.Fatalf("count race-test cleanup runs: %v", err)
	}
	if runs != 1 {
		t.Fatalf("expected exactly one persisted run after concurrent acquisition, got %d", runs)
	}
}

func openLifecycleCleanupSchedulerTestDatabase(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := os.Getenv("ASSET_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("ASSET_TEST_DATABASE_URL is not set")
	}
	database, err := gorm.Open(postgres.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("open PostgreSQL: %v", err)
	}
	return database
}

func rollbackOnlyTransaction(t *testing.T, database *gorm.DB) *gorm.DB {
	t.Helper()
	tx := database.Begin()
	if tx.Error != nil {
		t.Fatalf("begin rollback-only transaction: %v", tx.Error)
	}
	t.Cleanup(func() {
		if err := tx.Rollback().Error; err != nil && !errors.Is(err, gorm.ErrInvalidTransaction) {
			t.Errorf("rollback integration transaction: %v", err)
		}
	})
	return tx
}
