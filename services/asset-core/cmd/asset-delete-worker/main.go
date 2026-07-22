package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/joho/godotenv"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"seta-im-intern/go-asset-core/internal/domain"
	"seta-im-intern/go-asset-core/internal/repository"
)

const pollInterval = time.Second

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))
	for _, path := range []string{"../../.env", ".env"} {
		_ = godotenv.Load(path)
	}

	db, err := openAssetDB(assetDSNFromEnv())
	if err != nil {
		slog.Error("asset delete worker database connection failed", "error", err.Error())
		os.Exit(1)
	}
	defer func() {
		if sqlDB, err := db.DB(); err == nil {
			_ = sqlDB.Close()
		}
	}()

	workerID := strings.TrimSpace(os.Getenv("ASSET_DELETE_WORKER_ID"))
	if workerID == "" {
		workerID = strings.TrimSpace(os.Getenv("HOSTNAME"))
	}
	if workerID == "" {
		workerID = fmt.Sprintf("asset-delete-worker-%d", os.Getpid())
	}

	repo := repository.NewFolderDeletionRepository(db)
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	slog.Info("asset delete worker started", "workerId", workerID, "pollIntervalMs", pollInterval.Milliseconds())
	for {
		processNext(ctx, repo, workerID)
		select {
		case <-ctx.Done():
			slog.Info("asset delete worker stopped", "workerId", workerID)
			return
		case <-ticker.C:
		}
	}
}

func processNext(ctx context.Context, repo interface {
	ClaimNextFolderDeletionJob(context.Context, string) (*domain.FolderDeletionJob, error)
	ProcessFolderDeletionJob(context.Context, string, string) error
	FailFolderDeletionJob(context.Context, string, string) error
}, workerID string) {
	job, err := repo.ClaimNextFolderDeletionJob(ctx, workerID)
	if err != nil {
		slog.Error("folder deletion job claim failed", "workerId", workerID, "error", err.Error())
		return
	}
	if job == nil {
		return
	}

	slog.Info("folder deletion job claimed", "workerId", workerID, "jobId", job.ID, "orgId", job.OrgID, "rootFolderId", job.RootFolderID, "attempt", job.Attempts)
	if err := repo.ProcessFolderDeletionJob(ctx, job.ID, workerID); err != nil {
		slog.Error("folder deletion job batch failed", "workerId", workerID, "jobId", job.ID, "error", err.Error())
		if failErr := repo.FailFolderDeletionJob(context.Background(), job.ID, workerID); failErr != nil {
			slog.Error("folder deletion job failure state update failed", "workerId", workerID, "jobId", job.ID, "error", failErr.Error())
		}
	}
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
