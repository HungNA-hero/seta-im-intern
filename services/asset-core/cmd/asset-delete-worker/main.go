package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
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
	"seta-im-intern/go-asset-core/internal/observability"
	"seta-im-intern/go-asset-core/internal/repository"
	"seta-im-intern/go-asset-core/internal/storage"
	"seta-im-intern/go-asset-core/internal/usecase"
)

const (
	pollInterval     = time.Second
	workerMetricsURL = ":8081"
)

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

	repo := repository.NewLifecycleJobWorkerRepository(db)
	cleanupRepo := repository.NewLifecycleCleanupWorkerRepository(db)
	purgeObjects, err := newLifecycleObjectStore(context.Background())
	if err != nil {
		slog.Error("asset delete worker object storage connection failed", "error", err.Error())
		os.Exit(1)
	}
	purger := usecase.NewLifecyclePurger(repository.NewLifecyclePurgeRepository(db), purgeObjects)
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	metricsEnabled := strings.EqualFold(strings.TrimSpace(os.Getenv("METRICS_ENABLED")), "true")
	observability.SetMetricsEnabled(metricsEnabled)
	if metricsEnabled {
		metricsServer := newWorkerMetricsServer()
		go func() {
			if err := metricsServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				slog.Error("asset delete worker metrics listener failed", "error", err.Error())
			}
		}()
		defer func() {
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := metricsServer.Shutdown(shutdownCtx); err != nil {
				slog.Error("asset delete worker metrics shutdown failed", "error", err.Error())
			}
		}()
		slog.Info("asset delete worker metrics listening", "port", 8081)
	}

	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	slog.Info("asset lifecycle worker started", "workerId", workerID, "pollIntervalMs", pollInterval.Milliseconds())
	for {
		processNextRetentionCleanup(ctx, cleanupRepo)
		processNext(ctx, repo, purger, workerID)
		select {
		case <-ctx.Done():
			slog.Info("asset lifecycle worker stopped", "workerId", workerID)
			return
		case <-ticker.C:
		}
	}
}

func processNextRetentionCleanup(ctx context.Context, repo domain.LifecycleCleanupWorkerRepository) {
	result, err := repo.ProcessNextLifecycleCleanupBatch(ctx)
	if err != nil {
		slog.Error("lifecycle retention cleanup batch failed", "error", err.Error())
		return
	}
	if result == nil {
		return
	}
	slog.Info("lifecycle retention cleanup batch processed", "runId", result.RunID, "queuedJobs", result.QueuedJobs, "completed", result.Completed)
}

func newWorkerMetricsServer() *http.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("/metrics", observability.MetricsHandler)
	return &http.Server{
		Addr:              workerMetricsURL,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}
}

func processNext(ctx context.Context, repo interface {
	ClaimNextLifecycleJob(context.Context, string) (*domain.LifecycleJob, error)
	ProcessLifecycleJob(context.Context, string, string) error
	FailLifecycleJob(context.Context, string, string) error
}, purger interface {
	Process(context.Context, string, string, time.Time) error
}, workerID string) {
	job, err := repo.ClaimNextLifecycleJob(ctx, workerID)
	if err != nil {
		slog.Error("lifecycle job claim failed", "workerId", workerID, "error", err.Error())
		return
	}
	if job == nil {
		return
	}

	slog.Info("lifecycle job claimed", "workerId", workerID, "jobId", job.ID, "orgId", job.OrgID, "operation", job.Operation, "rootResourceId", job.RootResourceID, "attempt", job.Attempts)
	var processErr error
	if job.Operation == domain.LifecycleJobPurge {
		if job.LeaseExpiresAt == nil {
			processErr = fmt.Errorf("purge lifecycle job %s was claimed without a lease expiry", job.ID)
		} else {
			processErr = purger.Process(ctx, job.ID, workerID, *job.LeaseExpiresAt)
		}
	} else {
		processErr = repo.ProcessLifecycleJob(ctx, job.ID, workerID)
	}
	if processErr != nil {
		if errors.Is(processErr, usecase.ErrLifecyclePurgeLeaseLost) {
			slog.Warn("lifecycle purge lease lost; another worker may continue", "workerId", workerID, "jobId", job.ID)
			return
		}
		slog.Error("lifecycle job batch failed", "workerId", workerID, "jobId", job.ID, "operation", job.Operation, "error", processErr.Error())
		if failErr := repo.FailLifecycleJob(context.Background(), job.ID, workerID); failErr != nil {
			slog.Error("lifecycle job failure state update failed", "workerId", workerID, "jobId", job.ID, "error", failErr.Error())
		}
	}
}

// newLifecycleObjectStore uses the same private MinIO endpoint and credentials
// as media-worker. Lifecycle PURGE needs only Delete; it never needs Kafka or
// public presigned URL behavior.
func newLifecycleObjectStore(ctx context.Context) (*storage.MinIOStorage, error) {
	return storage.NewMinIOStorage(ctx, storage.MinIOConfig{
		Bucket:            getenv("ASSET_MEDIA_BUCKET", "seta-media"),
		Region:            getenv("ASSET_MEDIA_S3_REGION", "us-east-1"),
		InternalEndpoint:  getenv("ASSET_MEDIA_S3_INTERNAL_ENDPOINT", "http://minio:9000"),
		PublicEndpoint:    getenv("ASSET_MEDIA_S3_PUBLIC_ENDPOINT", "http://localhost:9000"),
		AccessKeyID:       getenv("ASSET_MEDIA_S3_ACCESS_KEY_ID", ""),
		SecretAccessKey:   getenv("ASSET_MEDIA_S3_SECRET_ACCESS_KEY", ""),
		ChecksumSupported: strings.EqualFold(getenv("ASSET_MEDIA_S3_CHECKSUM_SUPPORTED", "true"), "true"),
	})
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
