package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/joho/godotenv"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"seta-im-intern/go-asset-core/internal/domain"
	"seta-im-intern/go-asset-core/internal/eventing/consume"
	"seta-im-intern/go-asset-core/internal/eventing/kafka"
	"seta-im-intern/go-asset-core/internal/eventing/media"
	"seta-im-intern/go-asset-core/internal/eventing/outbox"
	"seta-im-intern/go-asset-core/internal/media/processing"
	"seta-im-intern/go-asset-core/internal/repository"
	"seta-im-intern/go-asset-core/internal/storage"
	"seta-im-intern/go-asset-core/internal/usecase"
)

const (
	relayInterval  = 500 * time.Millisecond
	relayBatchSize = 50
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))
	for _, path := range []string{"../../.env", ".env"} {
		_ = godotenv.Load(path)
	}

	if err := run(); err != nil {
		slog.Error("media worker stopped", "error", err.Error())
		os.Exit(1)
	}
}

func run() error {
	config, err := domain.LoadMediaConfig(os.LookupEnv)
	if err != nil {
		return fmt.Errorf("loading media configuration: %w", err)
	}

	db, err := openAssetDB(assetDSNFromEnv())
	if err != nil {
		return fmt.Errorf("connecting to asset_db: %w", err)
	}
	defer func() {
		if sqlDB, closeErr := db.DB(); closeErr == nil {
			_ = sqlDB.Close()
		}
	}()

	workerID := resolveWorkerID()
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	relay, producer, err := buildRelay(db, config, workerID)
	if err != nil {
		return err
	}
	defer func() { _ = producer.Close() }()

	reader, err := buildReader(ctx, db, config, workerID)
	if err != nil {
		return err
	}
	defer func() { _ = reader.Close() }()

	slog.Info(
		"media worker started",
		"workerId", workerID,
		"topic", config.Kafka.Topic,
		"consumerGroup", config.Kafka.ConsumerGroup,
		"leaseRenewalSeconds", config.Lease.RenewalInterval.Seconds(),
		"leaseExpirySeconds", config.Lease.Expiry.Seconds(),
	)

	var waitGroup sync.WaitGroup
	var readerErr error

	waitGroup.Add(1)
	go func() {
		defer waitGroup.Done()
		drainUntilStopped(ctx, relay)
	}()

	purger, err := buildObjectPurger(ctx, db, config)
	if err != nil {
		return err
	}
	waitGroup.Add(1)
	go func() {
		defer waitGroup.Done()
		purger.PurgeUntilStopped(ctx, config.Processor.SweepInterval)
	}()

	waitGroup.Add(1)
	go func() {
		defer waitGroup.Done()
		// Stopping the shared context is what makes a fatal reader error end the
		// process. Without it the relay loop keeps ticking on a worker that will
		// never consume another notification, and no orchestrator restarts it
		// because nothing has exited.
		defer stop()
		readerErr = reader.Run(ctx)
	}()

	waitGroup.Wait()
	slog.Info("media worker stopped", "workerId", workerID)
	return readerErr
}

func buildRelay(db *gorm.DB, config domain.MediaConfig, workerID string) (*outbox.Relay, *kafka.Producer, error) {
	store, err := repository.NewMediaOutboxStore(db, repository.MediaOutboxStoreOptions{
		Topic: config.Kafka.Topic,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("building the media outbox store: %w", err)
	}

	producer, err := kafka.NewProducer(kafka.ProducerOptions{
		Brokers:  config.Kafka.Brokers,
		Security: securityFor(config.Kafka),
	})
	if err != nil {
		return nil, nil, fmt.Errorf("building the media Kafka producer: %w", err)
	}

	relay := outbox.NewRelay(store, producer, outbox.RelayOptions{
		Owner:     workerID,
		BatchSize: relayBatchSize,
	})
	return relay, producer, nil
}

func buildReader(ctx context.Context, db *gorm.DB, config domain.MediaConfig, workerID string) (*kafka.Reader, error) {
	jobs := repository.NewMediaJobStore(db, config.Lease, config.Limits.MaxProcessingAttempts)
	leases := usecase.NewLeaseKeeper(jobs, config.Lease, slog.Default())

	executor, err := buildRenditionExecutor(ctx, db, config)
	if err != nil {
		return nil, err
	}
	worker := usecase.NewMediaWorker(
		jobs,
		leases,
		executor,
		workerID,
		config.Processor.HardDeadline,
		slog.Default(),
	)

	consumer := consume.NewConsumer(
		&notificationEffect{runner: worker, logger: slog.Default()},
		&loggingQuarantine{logger: slog.Default()},
		consume.ConsumerOptions{
			MaxValueBytes:  media.MaxRecordBytes,
			KnownVersions:  []int{media.SchemaVersion},
			RoutableTypes:  []string{media.EventType},
			AggregateField: media.AggregateField,
		},
	)

	reader, err := kafka.NewReader(consumer, kafka.ReaderOptions{
		Brokers:  config.Kafka.Brokers,
		Topic:    config.Kafka.Topic,
		GroupID:  config.Kafka.ConsumerGroup,
		Security: securityFor(config.Kafka),
	})
	if err != nil {
		return nil, fmt.Errorf("building the media Kafka reader: %w", err)
	}
	return reader, nil
}

func mediaObjectStorage(ctx context.Context, config domain.MediaConfig) (*storage.MinIOStorage, error) {
	objects, err := storage.NewMinIOStorage(ctx, storage.MinIOConfig{
		Bucket:            config.Storage.Bucket,
		Region:            config.Storage.Region,
		InternalEndpoint:  config.Storage.InternalEndpoint,
		PublicEndpoint:    config.Storage.PublicEndpoint,
		AccessKeyID:       config.Storage.AccessKeyID,
		SecretAccessKey:   config.Storage.SecretAccessKey,
		ChecksumSupported: config.Storage.ChecksumSupported,
	})
	if err != nil {
		return nil, fmt.Errorf("connecting to media object storage: %w", err)
	}
	return objects, nil
}

func buildRenditionExecutor(ctx context.Context, db *gorm.DB, config domain.MediaConfig) (*usecase.RenditionExecutor, error) {
	sources := repository.NewMediaJobStore(db, config.Lease, config.Limits.MaxProcessingAttempts)

	objects, err := mediaObjectStorage(ctx, config)
	if err != nil {
		return nil, err
	}

	if err := os.MkdirAll(config.Processor.ScratchRoot, 0o711); err != nil {
		return nil, fmt.Errorf("preparing the media scratch directory: %w", err)
	}
	if err := os.Chmod(config.Processor.ScratchRoot, 0o711); err != nil {
		return nil, fmt.Errorf("securing the media scratch directory: %w", err)
	}

	var identity *processing.ProcessIdentity
	if config.Processor.UID > 0 {
		identity = &processing.ProcessIdentity{
			UID: uint32(config.Processor.UID),
			GID: uint32(config.Processor.GID),
		}
	}
	supervisor := processing.NewChildSupervisor(processing.SupervisorOptions{
		ExecutablePath: config.Processor.ExecutablePath,
		Identity:       identity,
		Budgets: processing.Budgets{
			Validation: config.Processor.Validation,
			Transform:  config.Processor.Transform,
			Total:      config.Processor.HardDeadline,
		},
		Logger: slog.Default(),
	})

	return usecase.NewRenditionExecutor(sources, objects, supervisor, usecase.RenditionOptions{
		Limits:      config.Limits,
		ScratchRoot: config.Processor.ScratchRoot,
		Logger:      slog.Default(),
	}), nil
}

func buildObjectPurger(ctx context.Context, db *gorm.DB, config domain.MediaConfig) (*usecase.MediaObjectPurger, error) {
	objects, err := mediaObjectStorage(ctx, config)
	if err != nil {
		return nil, err
	}
	store := repository.NewMediaJobStore(db, config.Lease, config.Limits.MaxProcessingAttempts)

	return usecase.NewMediaObjectPurger(store, objects, usecase.MediaPurgeOptions{
		Quarantine: config.Limits.AbandonedQuarantine,
		BatchSize:  config.Processor.SweepBatchSize,
		Logger:     slog.Default(),
	}), nil
}

func drainUntilStopped(ctx context.Context, relay *outbox.Relay) {
	ticker := time.NewTicker(relayInterval)
	defer ticker.Stop()

	for {
		published, err := relay.DrainOnce(ctx)
		if err != nil && ctx.Err() == nil {
			slog.Error("media outbox drain failed", "error", err.Error())
		}
		if published > 0 {
			slog.Debug("media outbox drained", "published", published)
		}

		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func securityFor(config domain.MediaKafkaConfig) kafka.SecurityOptions {
	return kafka.SecurityOptions{
		Protocol:  config.SecurityProtocol,
		Mechanism: config.SASLMechanism,
		Username:  config.SASLUsername,
		Password:  config.SASLPassword,
	}
}

func resolveWorkerID() string {
	for _, candidate := range []string{os.Getenv("ASSET_MEDIA_WORKER_ID"), os.Getenv("HOSTNAME")} {
		if trimmed := strings.TrimSpace(candidate); trimmed != "" {
			return trimmed
		}
	}
	return fmt.Sprintf("media-worker-%d", os.Getpid())
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
