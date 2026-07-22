package domain

import (
	"context"
	"time"
)

type FolderDeletionJobStatus string

const (
	FolderDeletionPreviewed FolderDeletionJobStatus = "previewed"
	FolderDeletionQueued    FolderDeletionJobStatus = "queued"
	FolderDeletionRunning   FolderDeletionJobStatus = "running"
	FolderDeletionSucceeded FolderDeletionJobStatus = "succeeded"
	FolderDeletionFailed    FolderDeletionJobStatus = "failed"
	FolderDeletionCancelled FolderDeletionJobStatus = "cancelled"
)

const (
	FolderDeletionMetadataBatchSize = 1000
	FolderDeletionFolderBatchSize   = 100
	FolderDeletionLeaseDuration     = 30 * time.Second
	FolderDeletionPreviewTTL        = 15 * time.Minute
	FolderDeletionMaxAttempts       = 4 // Initial attempt plus three automatic retries.
)

// FolderDeletionPreview is the one-time confirmation payload returned before
// an asynchronous subtree delete can be queued.
type FolderDeletionPreview struct {
	ID                     string    `json:"id"`
	RootFolderID           string    `json:"root_folder_id"`
	ActiveFolderCount      int64     `json:"active_folder_count"`
	ActiveMetadataCount    int64     `json:"active_metadata_count"`
	TombstoneFolderCount   int64     `json:"tombstone_folder_count"`
	TombstoneMetadataCount int64     `json:"tombstone_metadata_count"`
	TotalRows              int64     `json:"total_rows"`
	ConfirmationToken      string    `json:"confirmation_token"`
	ExpiresAt              time.Time `json:"expires_at"`
}

// FolderDeletionJob tracks an irreversible, bounded-batch physical delete.
// RootFolderID intentionally has no foreign key because the root is deleted on success.
type FolderDeletionJob struct {
	ID                     string                  `gorm:"type:uuid;primaryKey;column:id" json:"id"`
	OrgID                  string                  `gorm:"type:uuid;not null;column:org_id" json:"org_id"`
	RootFolderID           string                  `gorm:"type:uuid;not null;column:root_folder_id" json:"root_folder_id"`
	RootPath               string                  `gorm:"type:ltree;not null;column:root_path" json:"root_path"`
	RequestedBy            string                  `gorm:"type:uuid;not null;column:requested_by" json:"requested_by"`
	Status                 FolderDeletionJobStatus `gorm:"type:varchar(16);not null;column:status" json:"status"`
	ConfirmationTokenHash  []byte                  `gorm:"column:confirmation_token_hash" json:"-"`
	PreviewExpiresAt       *time.Time              `gorm:"column:preview_expires_at" json:"preview_expires_at,omitempty"`
	ActiveFolderCount      int64                   `gorm:"column:active_folder_count" json:"active_folder_count"`
	ActiveMetadataCount    int64                   `gorm:"column:active_metadata_count" json:"active_metadata_count"`
	TombstoneFolderCount   int64                   `gorm:"column:tombstone_folder_count" json:"tombstone_folder_count"`
	TombstoneMetadataCount int64                   `gorm:"column:tombstone_metadata_count" json:"tombstone_metadata_count"`
	DeletedFolderCount     int64                   `gorm:"column:deleted_folder_count" json:"deleted_folder_count"`
	DeletedMetadataCount   int64                   `gorm:"column:deleted_metadata_count" json:"deleted_metadata_count"`
	Attempts               int                     `gorm:"column:attempts" json:"attempts"`
	ManualRetries          int                     `gorm:"column:manual_retries" json:"manual_retries"`
	NextRunAt              *time.Time              `gorm:"column:next_run_at" json:"next_run_at,omitempty"`
	LeaseOwner             *string                 `gorm:"column:lease_owner" json:"-"`
	LeaseExpiresAt         *time.Time              `gorm:"column:lease_expires_at" json:"-"`
	LastErrorCode          *string                 `gorm:"column:last_error_code" json:"last_error_code,omitempty"`
	QueuedAt               *time.Time              `gorm:"column:queued_at" json:"queued_at,omitempty"`
	StartedAt              *time.Time              `gorm:"column:started_at" json:"started_at,omitempty"`
	CompletedAt            *time.Time              `gorm:"column:completed_at" json:"completed_at,omitempty"`
	CancelledAt            *time.Time              `gorm:"column:cancelled_at" json:"cancelled_at,omitempty"`
	CreatedAt              time.Time               `gorm:"column:created_at" json:"created_at"`
	UpdatedAt              time.Time               `gorm:"column:updated_at" json:"updated_at"`
}

func (FolderDeletionJob) TableName() string { return "folder_deletion_jobs" }

func (job FolderDeletionJob) TotalRows() int64 {
	return job.ActiveFolderCount + job.ActiveMetadataCount + job.TombstoneFolderCount + job.TombstoneMetadataCount
}

type FolderDeletionRepository interface {
	PreviewFolderDeletion(ctx context.Context, orgID, userID, folderID string) (FolderDeletionPreview, error)
	ConfirmFolderDeletion(ctx context.Context, orgID, userID, folderID, previewID, token string) (FolderDeletionJob, error)
	GetFolderDeletionJob(ctx context.Context, orgID, actorID, jobID string, actorIsOrgAdmin bool) (FolderDeletionJob, error)
	CancelFolderDeletionJob(ctx context.Context, orgID, actorID, jobID string, actorIsOrgAdmin bool) (FolderDeletionJob, error)
	RetryFolderDeletionJob(ctx context.Context, orgID, actorID, jobID string, actorIsOrgAdmin bool) (FolderDeletionJob, error)
	ClaimNextFolderDeletionJob(ctx context.Context, workerID string) (*FolderDeletionJob, error)
	ProcessFolderDeletionJob(ctx context.Context, jobID, workerID string) error
	FailFolderDeletionJob(ctx context.Context, jobID, workerID string) error
}

type FolderDeletionUsecase interface {
	PreviewFolderDeletion(ctx context.Context, orgID, userID, folderID string) (FolderDeletionPreview, error)
	ConfirmFolderDeletion(ctx context.Context, orgID, userID, folderID, previewID, token string) (FolderDeletionJob, error)
	GetFolderDeletionJob(ctx context.Context, orgID, actorID, jobID string, actorIsOrgAdmin bool) (FolderDeletionJob, error)
	CancelFolderDeletionJob(ctx context.Context, orgID, actorID, jobID string, actorIsOrgAdmin bool) (FolderDeletionJob, error)
	RetryFolderDeletionJob(ctx context.Context, orgID, actorID, jobID string, actorIsOrgAdmin bool) (FolderDeletionJob, error)
}
