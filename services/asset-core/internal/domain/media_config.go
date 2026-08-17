package domain

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

type MediaConfig struct {
	Limits         MediaLimits
	Retry          MediaRetryPolicy
	Lease          MediaLeasePolicy
	Reconciliation MediaReconciliationPolicy
	Processor      MediaProcessorConfig
	Storage        MediaStorageConfig
	Kafka          MediaKafkaConfig
	RequireHTTPS   bool
}

type MediaLimits struct {
	MinUploadSizeBytes    int64
	MaxUploadSizeBytes    int64
	MaxImageSidePx        int
	MaxImagePixels        int64
	CredentialTTL         time.Duration
	SessionTTL            time.Duration
	OutputDownloadTTL     time.Duration
	AbandonedQuarantine   time.Duration
	DefaultRawQuotaBytes  int64
	MaxProcessingAttempts int
}

func (limits MediaLimits) AdmitsUploadSize(sizeBytes int64) bool {
	return sizeBytes >= limits.MinUploadSizeBytes && sizeBytes <= limits.MaxUploadSizeBytes
}

type MediaRetryPolicy struct {
	Delays []time.Duration
}

type MediaLeasePolicy struct {
	RenewalInterval time.Duration
	Expiry          time.Duration
}

type MediaReconciliationPolicy struct {
	ScanInterval          time.Duration
	BatchSize             int
	StaleDispatchAfter    time.Duration
	ProcessorHardDeadline time.Duration
}

type MediaProcessorConfig struct {
	ExecutablePath string
	ScratchRoot    string
	UID            int
	GID            int
	Validation     time.Duration
	Transform      time.Duration
	HardDeadline   time.Duration
	SweepInterval  time.Duration
	SweepBatchSize int
}

type MediaStorageConfig struct {
	Bucket            string
	Region            string
	InternalEndpoint  string
	PublicEndpoint    string
	AccessKeyID       string
	SecretAccessKey   string
	UsePathStyle      bool
	ChecksumSupported bool
}

type MediaKafkaConfig struct {
	Brokers          []string
	Topic            string
	DeadLetterTopic  string
	ConsumerGroup    string
	SecurityProtocol string
	SASLMechanism    string
	SASLUsername     string
	SASLPassword     string
}

var ErrInvalidMediaConfig = errors.New("invalid media configuration")

type LookupEnv func(key string) (string, bool)

func LoadMediaConfig(lookup LookupEnv) (MediaConfig, error) {
	reader := envReader{lookup: lookup}

	config := MediaConfig{
		Limits: MediaLimits{
			MinUploadSizeBytes:    MinUploadSizeBytes,
			MaxUploadSizeBytes:    reader.int64("ASSET_MEDIA_MAX_UPLOAD_BYTES", MaxUploadSizeBytes),
			MaxImageSidePx:        reader.int("ASSET_MEDIA_MAX_IMAGE_SIDE_PX", 20_000),
			MaxImagePixels:        reader.int64("ASSET_MEDIA_MAX_IMAGE_PIXELS", 50_000_000),
			CredentialTTL:         reader.duration("ASSET_MEDIA_CREDENTIAL_TTL_MINUTES", time.Minute, 60),
			SessionTTL:            reader.duration("ASSET_MEDIA_SESSION_TTL_HOURS", time.Hour, 24),
			OutputDownloadTTL:     reader.duration("ASSET_MEDIA_OUTPUT_LINK_TTL_MINUTES", time.Minute, 15),
			AbandonedQuarantine:   reader.duration("ASSET_MEDIA_CLEANUP_QUARANTINE_HOURS", time.Hour, 24),
			DefaultRawQuotaBytes:  reader.int64("ASSET_MEDIA_DEFAULT_RAW_QUOTA_BYTES", 10_737_418_240),
			MaxProcessingAttempts: reader.int("ASSET_MEDIA_MAX_PROCESSING_ATTEMPTS", 3),
		},
		Retry: MediaRetryPolicy{
			Delays: []time.Duration{
				reader.duration("ASSET_MEDIA_RETRY_DELAY_1_SECONDS", time.Second, 2),
				reader.duration("ASSET_MEDIA_RETRY_DELAY_2_SECONDS", time.Second, 10),
			},
		},
		Lease: MediaLeasePolicy{
			RenewalInterval: reader.duration("ASSET_MEDIA_LEASE_RENEWAL_SECONDS", time.Second, 10),
			Expiry:          reader.duration("ASSET_MEDIA_LEASE_EXPIRY_SECONDS", time.Second, 40),
		},
		Reconciliation: MediaReconciliationPolicy{
			ScanInterval:          reader.duration("ASSET_MEDIA_RECONCILE_INTERVAL_SECONDS", time.Second, 30),
			BatchSize:             reader.int("ASSET_MEDIA_RECONCILE_BATCH_SIZE", 50),
			StaleDispatchAfter:    reader.duration("ASSET_MEDIA_RECONCILE_STALE_SECONDS", time.Second, 60),
			ProcessorHardDeadline: reader.duration("ASSET_MEDIA_PROCESSOR_DEADLINE_SECONDS", time.Second, 90),
		},
		Processor: MediaProcessorConfig{
			ExecutablePath: reader.string("ASSET_MEDIA_PROCESSOR_PATH", "/usr/local/bin/media-processor"),
			ScratchRoot:    reader.string("ASSET_MEDIA_SCRATCH_DIR", os.TempDir()),
			UID:            reader.int("ASSET_MEDIA_PROCESSOR_UID", 0),
			GID:            reader.int("ASSET_MEDIA_PROCESSOR_GID", 0),
			Validation:     reader.duration("ASSET_MEDIA_PROCESSOR_VALIDATION_SECONDS", time.Second, 15),
			Transform:      reader.duration("ASSET_MEDIA_PROCESSOR_TRANSFORM_SECONDS", time.Second, 60),
			HardDeadline:   reader.duration("ASSET_MEDIA_PROCESSOR_DEADLINE_SECONDS", time.Second, 90),
			SweepInterval:  reader.duration("ASSET_MEDIA_SWEEP_INTERVAL_MINUTES", time.Minute, 15),
			SweepBatchSize: reader.int("ASSET_MEDIA_SWEEP_BATCH_SIZE", 50),
		},
		Storage: MediaStorageConfig{
			Bucket:            reader.string("ASSET_MEDIA_BUCKET", "seta-media"),
			Region:            reader.string("ASSET_MEDIA_S3_REGION", "us-east-1"),
			InternalEndpoint:  reader.string("ASSET_MEDIA_S3_INTERNAL_ENDPOINT", "http://minio:9000"),
			PublicEndpoint:    reader.string("ASSET_MEDIA_S3_PUBLIC_ENDPOINT", "http://localhost:9000"),
			AccessKeyID:       reader.string("ASSET_MEDIA_S3_ACCESS_KEY_ID", ""),
			SecretAccessKey:   reader.string("ASSET_MEDIA_S3_SECRET_ACCESS_KEY", ""),
			UsePathStyle:      reader.bool("ASSET_MEDIA_S3_USE_PATH_STYLE", true),
			ChecksumSupported: reader.bool("ASSET_MEDIA_S3_CHECKSUM_SUPPORTED", true),
		},
		Kafka: MediaKafkaConfig{
			Brokers:          splitBrokers(reader.string("ASSET_KAFKA_BROKERS", "localhost:29092")),
			Topic:            reader.string("ASSET_KAFKA_MEDIA_TOPIC", "media-processing.v1"),
			DeadLetterTopic:  reader.string("ASSET_KAFKA_MEDIA_DLQ_TOPIC", "media-processing-dlq.v1"),
			ConsumerGroup:    reader.string("ASSET_KAFKA_MEDIA_GROUP_ID", "media-workers-v1"),
			SecurityProtocol: reader.string("ASSET_KAFKA_SECURITY_PROTOCOL", "PLAINTEXT"),
			SASLMechanism:    reader.string("ASSET_KAFKA_SASL_MECHANISM", ""),
			SASLUsername:     reader.string("ASSET_KAFKA_SASL_USERNAME", ""),
			SASLPassword:     reader.string("ASSET_KAFKA_SASL_PASSWORD", ""),
		},
		RequireHTTPS: reader.bool("ASSET_MEDIA_REQUIRE_HTTPS", true),
	}

	if reader.err != nil {
		return MediaConfig{}, reader.err
	}
	if err := config.validate(); err != nil {
		return MediaConfig{}, err
	}
	return config, nil
}

func (config MediaConfig) validate() error {
	limits := config.Limits
	if limits.MaxUploadSizeBytes < limits.MinUploadSizeBytes {
		return fmt.Errorf("%w: max upload bytes %d is below the minimum %d", ErrInvalidMediaConfig, limits.MaxUploadSizeBytes, limits.MinUploadSizeBytes)
	}
	if limits.MaxImageSidePx <= 0 || limits.MaxImagePixels <= 0 {
		return fmt.Errorf("%w: image dimension limits must be positive", ErrInvalidMediaConfig)
	}
	if limits.DefaultRawQuotaBytes <= 0 {
		return fmt.Errorf("%w: default raw quota must be positive", ErrInvalidMediaConfig)
	}
	if limits.MaxProcessingAttempts <= 0 {
		return fmt.Errorf("%w: processing attempts must be positive", ErrInvalidMediaConfig)
	}
	if limits.CredentialTTL >= limits.SessionTTL {
		return fmt.Errorf("%w: credential TTL %s must be shorter than the session TTL %s", ErrInvalidMediaConfig, limits.CredentialTTL, limits.SessionTTL)
	}
	if len(config.Retry.Delays) != limits.MaxProcessingAttempts-1 {
		return fmt.Errorf("%w: %d retry delays cannot schedule %d attempts", ErrInvalidMediaConfig, len(config.Retry.Delays), limits.MaxProcessingAttempts)
	}

	if config.Lease.Expiry <= config.Lease.RenewalInterval {
		return fmt.Errorf("%w: lease expiry %s must exceed the renewal interval %s", ErrInvalidMediaConfig, config.Lease.Expiry, config.Lease.RenewalInterval)
	}
	if config.Reconciliation.BatchSize <= 0 {
		return fmt.Errorf("%w: reconciliation batch size must be positive", ErrInvalidMediaConfig)
	}

	processor := config.Processor
	if processor.Validation <= 0 || processor.Transform <= 0 || processor.HardDeadline <= 0 {
		return fmt.Errorf("%w: processor budgets must be positive", ErrInvalidMediaConfig)
	}
	if processor.Validation > processor.HardDeadline || processor.Transform > processor.HardDeadline {
		return fmt.Errorf(
			"%w: processor stage budgets (%s validation, %s transform) must fit inside the %s hard deadline",
			ErrInvalidMediaConfig, processor.Validation, processor.Transform, processor.HardDeadline,
		)
	}
	if processor.SweepInterval <= 0 || processor.SweepBatchSize <= 0 {
		return fmt.Errorf("%w: sweep interval and batch size must be positive", ErrInvalidMediaConfig)
	}
	if strings.TrimSpace(processor.ExecutablePath) == "" || strings.TrimSpace(processor.ScratchRoot) == "" {
		return fmt.Errorf("%w: processor path and scratch directory are required", ErrInvalidMediaConfig)
	}
	if (processor.UID == 0) != (processor.GID == 0) {
		return fmt.Errorf("%w: processor UID and GID must be configured together", ErrInvalidMediaConfig)
	}
	if config.RequireHTTPS && processor.UID == 0 {
		return fmt.Errorf("%w: isolated processor UID and GID are required outside local/test mode", ErrInvalidMediaConfig)
	}

	if strings.TrimSpace(config.Storage.Bucket) == "" {
		return fmt.Errorf("%w: media bucket is required", ErrInvalidMediaConfig)
	}
	for name, endpoint := range map[string]string{
		"ASSET_MEDIA_S3_INTERNAL_ENDPOINT": config.Storage.InternalEndpoint,
		"ASSET_MEDIA_S3_PUBLIC_ENDPOINT":   config.Storage.PublicEndpoint,
	} {
		if err := config.validateEndpoint(name, endpoint); err != nil {
			return err
		}
	}
	if config.Storage.AccessKeyID == "" || config.Storage.SecretAccessKey == "" {
		return fmt.Errorf("%w: media storage credentials are required", ErrInvalidMediaConfig)
	}

	if len(config.Kafka.Brokers) == 0 {
		return fmt.Errorf("%w: at least one Kafka broker is required", ErrInvalidMediaConfig)
	}
	for name, topic := range map[string]string{
		"ASSET_KAFKA_MEDIA_TOPIC":     config.Kafka.Topic,
		"ASSET_KAFKA_MEDIA_DLQ_TOPIC": config.Kafka.DeadLetterTopic,
		"ASSET_KAFKA_MEDIA_GROUP_ID":  config.Kafka.ConsumerGroup,
	} {
		if strings.TrimSpace(topic) == "" {
			return fmt.Errorf("%w: %s is required", ErrInvalidMediaConfig, name)
		}
	}
	return nil
}

func (config MediaConfig) validateEndpoint(name, endpoint string) error {
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Host == "" {
		return fmt.Errorf("%w: %s is not a valid URL", ErrInvalidMediaConfig, name)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return fmt.Errorf("%w: %s must use http or https", ErrInvalidMediaConfig, name)
	}
	if config.RequireHTTPS && parsed.Scheme != "https" {
		return fmt.Errorf("%w: %s must use https unless ASSET_MEDIA_REQUIRE_HTTPS is false", ErrInvalidMediaConfig, name)
	}
	return nil
}

func splitBrokers(value string) []string {
	brokers := make([]string, 0, 2)
	for _, broker := range strings.Split(value, ",") {
		if trimmed := strings.TrimSpace(broker); trimmed != "" {
			brokers = append(brokers, trimmed)
		}
	}
	return brokers
}

type envReader struct {
	lookup LookupEnv
	err    error
}

func (reader *envReader) string(key, fallback string) string {
	value, ok := reader.lookup(key)
	if !ok || strings.TrimSpace(value) == "" {
		return fallback
	}
	return strings.TrimSpace(value)
}

func (reader *envReader) int64(key string, fallback int64) int64 {
	value, ok := reader.lookup(key)
	if !ok || strings.TrimSpace(value) == "" {
		return fallback
	}
	parsed, parseErr := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
	if parseErr != nil || parsed <= 0 {
		reader.fail(key, value)
		return fallback
	}
	return parsed
}

func (reader *envReader) int(key string, fallback int) int {
	return int(reader.int64(key, int64(fallback)))
}

func (reader *envReader) duration(key string, unit time.Duration, fallbackUnits int64) time.Duration {
	return time.Duration(reader.int64(key, fallbackUnits)) * unit
}

func (reader *envReader) bool(key string, fallback bool) bool {
	value, ok := reader.lookup(key)
	if !ok || strings.TrimSpace(value) == "" {
		return fallback
	}
	parsed, parseErr := strconv.ParseBool(strings.TrimSpace(value))
	if parseErr != nil {
		reader.fail(key, value)
		return fallback
	}
	return parsed
}

func (reader *envReader) fail(key, value string) {
	if reader.err == nil {
		reader.err = fmt.Errorf("%w: %s has an unusable value %q", ErrInvalidMediaConfig, key, value)
	}
}
