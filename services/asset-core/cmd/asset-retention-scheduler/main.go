package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
	_ "time/tzdata"

	"github.com/joho/godotenv"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"seta-im-intern/go-asset-core/internal/domain"
	"seta-im-intern/go-asset-core/internal/repository"
)

const (
	retentionSchedulerName         = "daily-retention-cleanup"
	retentionSchedulerHour         = 2
	retentionSchedulerPollInterval = time.Minute
)

func main() {
	runOnce := flag.Bool("run-once", false, "acquire today's retention cleanup run once, then exit")
	flag.Parse()

	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))
	for _, path := range []string{"../../.env", ".env"} {
		_ = godotenv.Load(path)
	}

	location, err := retentionCleanupLocationFromEnv()
	if err != nil {
		slog.Error("asset retention scheduler timezone configuration failed", "error", err.Error())
		os.Exit(1)
	}
	db, err := openAssetDB(assetDSNFromEnv())
	if err != nil {
		slog.Error("asset retention scheduler database connection failed", "error", err.Error())
		os.Exit(1)
	}
	defer func() {
		if sqlDB, err := db.DB(); err == nil {
			_ = sqlDB.Close()
		}
	}()

	workerID := retentionSchedulerWorkerID()
	repo := repository.NewLifecycleCleanupSchedulerRepository(db)
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if *runOnce {
		if err := processRetentionCleanupRunOnce(ctx, repo, workerID, time.Now(), location); err != nil {
			slog.Error("manual asset retention cleanup run failed", "schedulerName", retentionSchedulerName, "workerId", workerID, "error", err.Error())
			os.Exit(1)
		}
		return
	}

	ticker := time.NewTicker(retentionSchedulerPollInterval)
	defer ticker.Stop()
	slog.Info("asset retention scheduler started", "schedulerName", retentionSchedulerName, "workerId", workerID, "timezone", location.String(), "hour", retentionSchedulerHour)
	for {
		processRetentionCleanupTick(ctx, repo, workerID, time.Now(), location)
		select {
		case <-ctx.Done():
			slog.Info("asset retention scheduler stopped", "schedulerName", retentionSchedulerName, "workerId", workerID)
			return
		case <-ticker.C:
		}
	}
}

// processRetentionCleanupRunOnce is an explicit operator/demo action. It uses
// the same daily run key and PostgreSQL lease as the scheduled 02:00 tick, but
// does not wait for the wall clock. It only creates or reuses the durable run;
// the lifecycle worker remains responsible for selecting eligible roots and
// performing physical purge.
func processRetentionCleanupRunOnce(
	ctx context.Context,
	repo domain.LifecycleCleanupSchedulerRepository,
	workerID string,
	now time.Time,
	location *time.Location,
) error {
	result, err := runRetentionCleanupOnce(ctx, repo, workerID, now, location)
	if err != nil {
		return err
	}
	if !result.LeaseAcquired {
		slog.Info("manual asset retention cleanup run already owned or completed", "schedulerName", retentionSchedulerName, "workerId", workerID)
		return nil
	}
	slog.Info("manual asset retention cleanup run available", "schedulerName", retentionSchedulerName, "workerId", workerID, "runId", result.Run.ID, "created", result.Created, "runDate", result.Run.RunDate.Format("2006-01-02"))
	return nil
}

func processRetentionCleanupTick(
	ctx context.Context,
	repo domain.LifecycleCleanupSchedulerRepository,
	workerID string,
	now time.Time,
	location *time.Location,
) {
	result, due, err := runRetentionCleanupTick(ctx, repo, workerID, now, location)
	if !due {
		return
	}
	if err != nil {
		slog.Error("asset retention scheduler tick failed", "schedulerName", retentionSchedulerName, "workerId", workerID, "error", err.Error())
		return
	}
	if !result.LeaseAcquired {
		slog.Info("asset retention scheduler lease held by another instance", "schedulerName", retentionSchedulerName, "workerId", workerID)
		return
	}
	slog.Info("asset retention cleanup run available", "schedulerName", retentionSchedulerName, "workerId", workerID, "runId", result.Run.ID, "created", result.Created, "runDate", result.Run.RunDate.Format("2006-01-02"))
}

func runRetentionCleanupTick(
	ctx context.Context,
	repo domain.LifecycleCleanupSchedulerRepository,
	workerID string,
	now time.Time,
	location *time.Location,
) (domain.LifecycleCleanupRunAcquireResult, bool, error) {
	localNow := now.In(location)
	if localNow.Hour() != retentionSchedulerHour {
		return domain.LifecycleCleanupRunAcquireResult{}, false, nil
	}
	result, err := acquireRetentionCleanupRun(ctx, repo, workerID, localNow, location)
	return result, true, err
}

// runRetentionCleanupOnce creates or reuses the same calendar-day run that
// the automatic 02:00 scheduler would create. The supplied time selects the
// local calendar date only; it never changes the database's retention test.
func runRetentionCleanupOnce(
	ctx context.Context,
	repo domain.LifecycleCleanupSchedulerRepository,
	workerID string,
	now time.Time,
	location *time.Location,
) (domain.LifecycleCleanupRunAcquireResult, error) {
	localNow := now.In(location)
	runTime := time.Date(localNow.Year(), localNow.Month(), localNow.Day(), retentionSchedulerHour, 0, 0, 0, location)
	return acquireRetentionCleanupRun(ctx, repo, workerID, runTime, location)
}

func acquireRetentionCleanupRun(
	ctx context.Context,
	repo domain.LifecycleCleanupSchedulerRepository,
	workerID string,
	runTime time.Time,
	location *time.Location,
) (domain.LifecycleCleanupRunAcquireResult, error) {
	return repo.AcquireDailyCleanupRun(ctx, retentionSchedulerName, workerID, runTime, location.String())
}

func retentionCleanupLocationFromEnv() (*time.Location, error) {
	timezone := strings.TrimSpace(getenv("ASSET_RETENTION_CLEANUP_TIMEZONE", "Asia/Bangkok"))
	location, err := time.LoadLocation(timezone)
	if err != nil {
		return nil, fmt.Errorf("load asset retention cleanup timezone %q: %w", timezone, err)
	}
	return location, nil
}

func retentionSchedulerWorkerID() string {
	if workerID := strings.TrimSpace(os.Getenv("ASSET_RETENTION_SCHEDULER_ID")); workerID != "" {
		return workerID
	}
	hostname := strings.TrimSpace(os.Getenv("HOSTNAME"))
	if hostname == "" {
		hostname = "unknown-host"
	}
	return fmt.Sprintf("asset-retention-scheduler-%s-%d", hostname, os.Getpid())
}

func openAssetDB(dsn string) (*gorm.DB, error) {
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		return nil, err
	}
	sqlDB, err := db.DB()
	if err != nil {
		return nil, err
	}
	if err := sqlDB.Ping(); err != nil {
		return nil, err
	}
	return db, nil
}

func assetDSNFromEnv() string {
	return fmt.Sprintf(
		"host=%s user=%s password=%s dbname=%s port=%s sslmode=%s",
		getenv("ASSET_DB_HOST", "localhost"),
		getenv("ASSET_DB_USER", "asset_user"),
		getenv("ASSET_DB_PASSWORD", "asset_password"),
		getenv("ASSET_DB_NAME", "asset_db"),
		getenv("ASSET_DB_PORT", "5433"),
		getenv("ASSET_DB_SSLMODE", "disable"),
	)
}

func getenv(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
