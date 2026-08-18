package domain

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"
)

// MediaContentType is the admitted declaration. Only these two are accepted at
// session creation, and only these two may be recorded as a trusted detection.
type MediaContentType string

const (
	MediaContentTypeJPEG MediaContentType = "image/jpeg"
	MediaContentTypePNG  MediaContentType = "image/png"
)

// FileExtension returns the canonical extension for an admitted content type.
func (contentType MediaContentType) FileExtension() (string, bool) {
	switch contentType {
	case MediaContentTypeJPEG:
		return "jpg", true
	case MediaContentTypePNG:
		return "png", true
	default:
		return "", false
	}
}

type UploadSessionState string

const (
	UploadSessionCreated   UploadSessionState = "created"
	UploadSessionCommitted UploadSessionState = "committed"
	UploadSessionExpired   UploadSessionState = "expired"
	UploadSessionCancelled UploadSessionState = "cancelled"
	UploadSessionFailed    UploadSessionState = "failed"
)

// IsTerminal reports whether the session can no longer be committed. Cancelling
// a terminal session is an idempotent no-op.
func (state UploadSessionState) IsTerminal() bool {
	return state != UploadSessionCreated
}

// IsPublished reports whether the media contract exposes this state. Cancelled,
// expired, and failed sessions are gone from a client's perspective, so they are
// never replayed, presigned, or written into a session envelope.
func (state UploadSessionState) IsPublished() bool {
	return state == UploadSessionCreated || state == UploadSessionCommitted
}

type MediaVersionStatus string

const (
	MediaVersionPending    MediaVersionStatus = "pending"
	MediaVersionProcessing MediaVersionStatus = "processing"
	MediaVersionCompleted  MediaVersionStatus = "completed"
	MediaVersionFailed     MediaVersionStatus = "failed"
)

type ProcessingJobStatus string

const (
	ProcessingJobQueued     ProcessingJobStatus = "queued"
	ProcessingJobProcessing ProcessingJobStatus = "processing"
	ProcessingJobCompleted  ProcessingJobStatus = "completed"
	ProcessingJobFailed     ProcessingJobStatus = "failed"
)

type ProcessingStage string

const (
	ProcessingStageValidating   ProcessingStage = "validating"
	ProcessingStageTransforming ProcessingStage = "transforming"
)

// MediaOutputKind names the two derivatives every completed version owns.
type MediaOutputKind string

const (
	MediaOutputThumbnail MediaOutputKind = "thumbnail"
	MediaOutputWeb       MediaOutputKind = "web"
)

// MediaProcessingPolicy is the fixed output manifest. Exactly two derivatives
// are produced, both directly from the raw original, and neither is upscaled.
type MediaProcessingPolicy struct {
	Kind          MediaOutputKind
	BoundingBoxPx int
}

var MediaOutputManifest = []MediaProcessingPolicy{
	{Kind: MediaOutputThumbnail, BoundingBoxPx: 256},
	{Kind: MediaOutputWeb, BoundingBoxPx: 1080},
}

const (
	MinUploadSizeBytes int64 = 1
	MaxUploadSizeBytes int64 = 50_000_000
	ChecksumByteLength       = 32
)

// ObjectKey is a storage key without the bucket. Only the derivation functions
// below produce one, so no call site can invent a key that cleanup or the
// documented teardown order would then fail to find.
type ObjectKey string

func (key ObjectKey) String() string { return string(key) }

// IsRaw reports whether the key addresses a private original.
func (key ObjectKey) IsRaw() bool {
	return len(key) >= len(rawKeyPrefix) && string(key)[:len(rawKeyPrefix)] == rawKeyPrefix
}

const (
	rawKeyPrefix       = "raw/"
	processedKeyPrefix = "processed/"
)

// RawObjectKey derives the immutable key for an original upload.
func RawObjectKey(orgID, assetID, uploadID string, contentType MediaContentType) (ObjectKey, error) {
	extension, ok := contentType.FileExtension()
	if !ok {
		return "", fmt.Errorf("%w: %q", ErrUnsupportedMediaType, contentType)
	}
	return ObjectKey(fmt.Sprintf("%s%s/%s/%s/original.%s", rawKeyPrefix, orgID, assetID, uploadID, extension)), nil
}

// ProcessedObjectKey derives the immutable key for one derivative.
func ProcessedObjectKey(orgID, assetID, versionID string, kind MediaOutputKind, contentType MediaContentType) (ObjectKey, error) {
	extension, ok := contentType.FileExtension()
	if !ok {
		return "", fmt.Errorf("%w: %q", ErrUnsupportedMediaType, contentType)
	}
	name, ok := processedObjectName(kind)
	if !ok {
		return "", fmt.Errorf("%w: %q", ErrUnknownOutputKind, kind)
	}
	return ObjectKey(fmt.Sprintf("%s%s/%s/%s/%s.%s", processedKeyPrefix, orgID, assetID, versionID, name, extension)), nil
}

func processedObjectName(kind MediaOutputKind) (string, bool) {
	switch kind {
	case MediaOutputThumbnail:
		return "thumbnail-256", true
	case MediaOutputWeb:
		return "web-1080", true
	default:
		return "", false
	}
}

var (
	ErrUnsupportedMediaType = errors.New("unsupported media content type")
	ErrUnknownOutputKind    = errors.New("unknown media output kind")
	ErrObjectNotFound       = errors.New("object not found in storage")
	ErrObjectAlreadyExists  = errors.New("object already exists at this key")
	ErrRawKeyNotPresignable = errors.New("raw objects are never signed for read")
)

// UploadDescriptor is the complete signed grant handed to a client. Its URL and
// headers are signed together, so a caller must pass it through unchanged; it
// mirrors PresignedUploadDescriptor in the public OpenAPI contract.
type UploadDescriptor struct {
	Protocol            string
	Method              string
	URL                 string
	Headers             map[string]string
	CredentialExpiresAt time.Time
}

// DownloadDescriptor is a short-lived read grant for a processed output.
type DownloadDescriptor struct {
	URL       string
	ExpiresAt time.Time
}

// PutAuthority requests one exact-key whole-object upload grant.
type PutAuthority struct {
	Key               ObjectKey
	ContentType       MediaContentType
	ExpectedSizeBytes int64
	ChecksumSHA256    []byte
	ExpiresIn         time.Duration
}

// PutAttributes carries the trusted metadata for a server-side write.
type PutAttributes struct {
	ContentType    MediaContentType
	ChecksumSHA256 []byte
}

// ObjectAttributes is what storage reports about a stored object. ChecksumSHA256
// is nil when the provider does not expose checksum metadata; the worker's own
// recomputation is the unconditional check.
type ObjectAttributes struct {
	SizeBytes      int64
	ContentType    string
	ChecksumSHA256 []byte
	ETag           string
}

// ObjectStorage is the private-bucket port. Every write targets a generated
// immutable key, so no-overwrite is applied unconditionally rather than offered
// as an option, and PresignGet refuses raw keys so originals cannot leak.
type ObjectStorage interface {
	PresignPut(ctx context.Context, authority PutAuthority) (UploadDescriptor, error)
	PresignGet(ctx context.Context, key ObjectKey, ttl time.Duration) (DownloadDescriptor, error)
	Head(ctx context.Context, key ObjectKey) (ObjectAttributes, error)
	Put(ctx context.Context, key ObjectKey, body io.Reader, attributes PutAttributes) error
	Get(ctx context.Context, key ObjectKey) (io.ReadCloser, error)
	Delete(ctx context.Context, key ObjectKey) error
}

// Clock is injected so session, credential, and lease expiry are testable
// without sleeping.
type Clock interface {
	Now() time.Time
}

// IDGenerator produces the server-side identifiers that become upload, version,
// and job identities.
type IDGenerator interface {
	NewID() string
}

// OrganizationMediaUsage is the per-organization raw-byte quota ledger.
type OrganizationMediaUsage struct {
	OrgID            string    `gorm:"type:uuid;primaryKey" json:"org_id"`
	RawQuotaBytes    int64     `gorm:"not null" json:"raw_quota_bytes"`
	ReservedRawBytes int64     `gorm:"not null" json:"reserved_raw_bytes"`
	StoredRawBytes   int64     `gorm:"not null" json:"stored_raw_bytes"`
	CreatedAt        time.Time `gorm:"not null;default:now()" json:"created_at"`
	UpdatedAt        time.Time `gorm:"not null;default:now()" json:"updated_at"`
}

func (OrganizationMediaUsage) TableName() string { return "organization_media_usage" }

// AvailableBytes reports headroom left under the configured quota.
func (usage OrganizationMediaUsage) AvailableBytes() int64 {
	return usage.RawQuotaBytes - usage.ReservedRawBytes - usage.StoredRawBytes
}

// MediaUploadSession is one admitted transfer operation. It owns retry identity
// before any media version exists.
type MediaUploadSession struct {
	ID                     string             `gorm:"type:uuid;primaryKey;default:gen_random_uuid()" json:"id"`
	OrgID                  string             `gorm:"type:uuid;not null" json:"org_id"`
	AssetID                string             `gorm:"type:uuid;not null" json:"asset_id"`
	RequestedBy            string             `gorm:"type:uuid;not null" json:"requested_by"`
	IdempotencyKey         string             `gorm:"type:uuid;not null" json:"idempotency_key"`
	RequestFingerprint     []byte             `gorm:"type:bytea;not null" json:"-"`
	State                  UploadSessionState `gorm:"type:varchar(20);not null" json:"state"`
	OriginalFilename       string             `gorm:"type:varchar(255);not null" json:"original_filename"`
	DeclaredContentType    MediaContentType   `gorm:"type:varchar(32);not null" json:"declared_content_type"`
	FileExtension          string             `gorm:"type:varchar(5);not null" json:"file_extension"`
	ExpectedSizeBytes      int64              `gorm:"not null" json:"expected_size_bytes"`
	DeclaredChecksumSHA256 []byte             `gorm:"type:bytea;not null" json:"-"`
	RawObjectKey           string             `gorm:"type:text;not null" json:"-"`
	CredentialExpiresAt    time.Time          `gorm:"not null" json:"credential_expires_at"`
	SessionExpiresAt       time.Time          `gorm:"not null" json:"session_expires_at"`
	CommittedAt            *time.Time         `json:"committed_at,omitempty"`
	ExpiredAt              *time.Time         `json:"expired_at,omitempty"`
	CancelledAt            *time.Time         `json:"cancelled_at,omitempty"`
	CreatedAt              time.Time          `gorm:"not null;default:now()" json:"created_at"`
	UpdatedAt              time.Time          `gorm:"not null;default:now()" json:"updated_at"`
}

func (MediaUploadSession) TableName() string { return "media_upload_sessions" }

// AssetMediaVersion is one immutable candidate or replacement operation.
type AssetMediaVersion struct {
	ID                  string             `gorm:"type:uuid;primaryKey;default:gen_random_uuid()" json:"id"`
	OrgID               string             `gorm:"type:uuid;not null" json:"org_id"`
	AssetID             string             `gorm:"type:uuid;not null" json:"asset_id"`
	UploadID            string             `gorm:"type:uuid;not null" json:"upload_id"`
	RequestedBy         string             `gorm:"type:uuid;not null" json:"requested_by"`
	Status              MediaVersionStatus `gorm:"type:varchar(20);not null" json:"status"`
	RawObjectKey        string             `gorm:"type:text;not null" json:"-"`
	RawRetained         bool               `gorm:"not null;default:true" json:"raw_retained"`
	DeclaredContentType MediaContentType   `gorm:"type:varchar(32);not null" json:"declared_content_type"`
	DetectedContentType *MediaContentType  `gorm:"type:varchar(32)" json:"detected_content_type,omitempty"`
	OriginalSizeBytes   int64              `gorm:"not null" json:"original_size_bytes"`
	SHA256              []byte             `gorm:"type:bytea" json:"-"`
	SourceWidth         *int               `json:"source_width,omitempty"`
	SourceHeight        *int               `json:"source_height,omitempty"`
	FailureCode         *string            `gorm:"type:varchar(64)" json:"failure_code,omitempty"`
	ProcessingStartedAt *time.Time         `json:"processing_started_at,omitempty"`
	CompletedAt         *time.Time         `json:"completed_at,omitempty"`
	FailedAt            *time.Time         `json:"failed_at,omitempty"`
	ActivatedAt         *time.Time         `json:"activated_at,omitempty"`
	CreatedAt           time.Time          `gorm:"not null;default:now()" json:"created_at"`
	UpdatedAt           time.Time          `gorm:"not null;default:now()" json:"updated_at"`
}

func (AssetMediaVersion) TableName() string { return "asset_media_versions" }

// MediaOutput is trusted metadata for one processed object.
type MediaOutput struct {
	ID          string           `gorm:"type:uuid;primaryKey;default:gen_random_uuid()" json:"id"`
	VersionID   string           `gorm:"type:uuid;not null" json:"version_id"`
	Kind        MediaOutputKind  `gorm:"type:varchar(16);not null" json:"kind"`
	ObjectKey   string           `gorm:"type:text;not null" json:"-"`
	ContentType MediaContentType `gorm:"type:varchar(32);not null" json:"content_type"`
	Width       int              `gorm:"not null" json:"width"`
	Height      int              `gorm:"not null" json:"height"`
	SizeBytes   int64            `gorm:"not null" json:"size_bytes"`
	SHA256      []byte           `gorm:"type:bytea;not null" json:"-"`
	CreatedAt   time.Time        `gorm:"not null;default:now()" json:"created_at"`
}

func (MediaOutput) TableName() string { return "media_outputs" }

// MediaProcessingJob is the durable source of truth for asynchronous work.
// Kafka is notification transport only; this row decides authority.
type MediaProcessingJob struct {
	ID                     string              `gorm:"type:uuid;primaryKey;default:gen_random_uuid()" json:"id"`
	OrgID                  string              `gorm:"type:uuid;not null" json:"org_id"`
	AssetID                string              `gorm:"type:uuid;not null" json:"asset_id"`
	VersionID              string              `gorm:"type:uuid;not null" json:"version_id"`
	Status                 ProcessingJobStatus `gorm:"type:varchar(20);not null" json:"status"`
	Stage                  *ProcessingStage    `gorm:"type:varchar(20)" json:"stage,omitempty"`
	AttemptCount           int                 `gorm:"not null;default:0" json:"attempt_count"`
	NextAttemptAt          time.Time           `gorm:"not null;default:now()" json:"next_attempt_at"`
	LeaseOwner             *string             `gorm:"type:text" json:"-"`
	LeaseExpiresAt         *time.Time          `json:"-"`
	LastErrorCode          *string             `gorm:"type:varchar(64)" json:"last_error_code,omitempty"`
	NotificationIsolatedAt *time.Time          `json:"notification_isolated_at,omitempty"`
	QueuedAt               time.Time           `gorm:"not null;default:now()" json:"queued_at"`
	StartedAt              *time.Time          `json:"started_at,omitempty"`
	CompletedAt            *time.Time          `json:"completed_at,omitempty"`
	FailedAt               *time.Time          `json:"failed_at,omitempty"`
	CreatedAt              time.Time           `gorm:"not null;default:now()" json:"created_at"`
	UpdatedAt              time.Time           `gorm:"not null;default:now()" json:"updated_at"`
}

func (MediaProcessingJob) TableName() string { return "media_processing_jobs" }

// MediaJobOutboxRecord is the media-owned outbox row. The shared KAN-81 relay
// publishes it; nothing here publishes directly to Kafka.
type MediaJobOutboxRecord struct {
	ID             string     `gorm:"type:uuid;primaryKey;default:gen_random_uuid()" json:"id"`
	JobID          string     `gorm:"type:uuid;not null" json:"job_id"`
	EventType      string     `gorm:"type:varchar(64);not null" json:"event_type"`
	SchemaVersion  int        `gorm:"not null;default:1" json:"schema_version"`
	Payload        []byte     `gorm:"type:jsonb;not null" json:"-"`
	Status         string     `gorm:"type:varchar(16);not null;default:pending" json:"status"`
	AttemptCount   int        `gorm:"not null;default:0" json:"attempt_count"`
	NextAttemptAt  time.Time  `gorm:"not null;default:now()" json:"next_attempt_at"`
	LeaseOwner     *string    `gorm:"type:text" json:"-"`
	LeaseExpiresAt *time.Time `json:"-"`
	LastErrorCode  *string    `gorm:"type:varchar(64)" json:"last_error_code,omitempty"`
	PublishedAt    *time.Time `json:"published_at,omitempty"`
	CreatedAt      time.Time  `gorm:"not null;default:now()" json:"created_at"`
	UpdatedAt      time.Time  `gorm:"not null;default:now()" json:"updated_at"`
}

func (MediaJobOutboxRecord) TableName() string { return "media_job_outbox" }

const MediaProcessingRequestedEventType = "media.processing.requested"

// CreateUploadSessionRequest is the admitted, already-authorized session request.
type CreateUploadSessionRequest struct {
	OrgID                  string
	AssetID                string
	RequestedBy            string
	IdempotencyKey         string
	OriginalFilename       string
	DeclaredContentType    MediaContentType
	ExpectedSizeBytes      int64
	DeclaredChecksumSHA256 []byte
}

// CommitUploadRequest carries only the upload identity. A lost PUT response is
// recovered by verifying the exact stored object, not by re-uploading.
type CommitUploadRequest struct {
	OrgID       string
	AssetID     string
	RequestedBy string
	UploadID    string
}

// UploadSessionResult is what a create, replay, or refresh returns.
type UploadSessionResult struct {
	Session    MediaUploadSession
	Descriptor UploadDescriptor
	Replayed   bool
}

// CommitAcceptance is the durable 202 result: version, job, and outbox row all
// committed in one transaction.
type CommitAcceptance struct {
	VersionID  string
	JobID      string
	Status     ProcessingJobStatus
	AcceptedAt time.Time
}

// CommitUploadResult is the complete synchronous response. It deliberately
// contains declared metadata only; the checksum is not trusted content
// identity until the worker recomputes it from downloaded bytes.
type CommitUploadResult struct {
	AssetID             string
	UploadID            string
	VersionID           string
	JobID               string
	Status              ProcessingJobStatus
	OriginalFilename    string
	OriginalContentType MediaContentType
	OriginalSizeBytes   int64
	AcceptedAt          time.Time
	Replayed            bool
}
