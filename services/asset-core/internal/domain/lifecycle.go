package domain

import (
	"context"
	"encoding/json"
	"time"
)

// LifecycleResourceType identifies the root resource represented by a Recycle Bin entry.
type LifecycleResourceType string

const (
	LifecycleResourceFolder   LifecycleResourceType = "FOLDER"
	LifecycleResourceMetadata LifecycleResourceType = "METADATA"
)

// LifecycleUnitState records the durable lifecycle state. KAN-82 creates the
// schema and uses DELETED roots; worker transitions are implemented separately.
type LifecycleUnitState string

const (
	LifecycleDeleteQueued  LifecycleUnitState = "DELETE_QUEUED"
	LifecycleDeleting      LifecycleUnitState = "DELETING"
	LifecycleDeleted       LifecycleUnitState = "DELETED"
	LifecycleRestoreQueued LifecycleUnitState = "RESTORE_QUEUED"
	LifecycleRestoring     LifecycleUnitState = "RESTORING"
	LifecycleRestored      LifecycleUnitState = "RESTORED"
	LifecyclePurgeQueued   LifecycleUnitState = "PURGE_QUEUED"
	LifecyclePurging       LifecycleUnitState = "PURGING"
	LifecycleFailed        LifecycleUnitState = "FAILED"
	LifecyclePurged        LifecycleUnitState = "PURGED"
)

// LifecycleUnit is one Recycle Bin root. It owns neither a copied tree nor a
// background job; folders and metadata keep their current source-of-truth rows.
type LifecycleUnit struct {
	ID               string                `gorm:"type:uuid;primaryKey;default:gen_random_uuid()" json:"id"`
	OrgID            string                `gorm:"type:uuid;not null;index" json:"org_id"`
	RootResourceType LifecycleResourceType `gorm:"type:varchar(16);not null" json:"root_resource_type"`
	RootResourceID   string                `gorm:"type:uuid;not null" json:"root_resource_id"`
	// RootFolderPath is the path snapshot captured when the lifecycle unit was
	// created. Recycle Bin authorization reads the current path from source rows
	// so a later ancestor move cannot keep using this historical location.
	RootFolderPath     string             `gorm:"type:ltree;not null" json:"root_folder_path"`
	OriginalParentPath *string            `gorm:"type:ltree" json:"original_parent_path,omitempty"`
	OriginalFolderID   *string            `gorm:"type:uuid" json:"original_folder_id,omitempty"`
	State              LifecycleUnitState `gorm:"type:varchar(24);not null" json:"state"`
	RequestedBy        string             `gorm:"type:uuid;not null" json:"requested_by"`
	DeleteCompletedAt  *time.Time         `json:"delete_completed_at,omitempty"`
	RetentionUntil     *time.Time         `gorm:"index" json:"retention_until,omitempty"`
	CreatedAt          time.Time          `gorm:"not null;default:now()" json:"created_at"`
	UpdatedAt          time.Time          `gorm:"not null;default:now()" json:"updated_at"`
}

func (LifecycleUnit) TableName() string {
	return "asset_lifecycle_units"
}

// LifecycleJobOperation is the work requested for one lifecycle unit.
type LifecycleJobOperation string

const (
	LifecycleJobDelete  LifecycleJobOperation = "DELETE"
	LifecycleJobRestore LifecycleJobOperation = "RESTORE"
	LifecycleJobPurge   LifecycleJobOperation = "PURGE"
)

// LifecycleJobStatus is intentionally separate from LifecycleUnitState. A
// unit tells the product lifecycle state; a job tells a worker whether a
// particular attempt is previewed, claimable, running, or terminal.
type LifecycleJobStatus string

const (
	LifecycleJobPreviewed  LifecycleJobStatus = "PREVIEWED"
	LifecycleJobQueued     LifecycleJobStatus = "QUEUED"
	LifecycleJobRunning    LifecycleJobStatus = "RUNNING"
	LifecycleJobSucceeded  LifecycleJobStatus = "SUCCEEDED"
	LifecycleJobFailed     LifecycleJobStatus = "FAILED"
	LifecycleJobSuppressed LifecycleJobStatus = "SUPPRESSED"
)

// LifecycleJob is the durable, worker-owned record for one bounded lifecycle
// operation. Checkpoint is an object owned by the operation implementation;
// the DELETE worker records its completed bounded batches here.
// PREVIEWED delete jobs have no lifecycle unit yet. Every executable job must
// reference a unit in the same organization.
type LifecycleJob struct {
	ID                        string                `gorm:"type:uuid;primaryKey;default:gen_random_uuid()" json:"id"`
	OrgID                     string                `gorm:"type:uuid;not null;index" json:"org_id"`
	UnitID                    *string               `gorm:"type:uuid;index" json:"unit_id,omitempty"`
	LegacyFolderDeletionJobID *string               `gorm:"type:uuid;uniqueIndex" json:"legacy_folder_deletion_job_id,omitempty"`
	RootResourceType          LifecycleResourceType `gorm:"type:varchar(16);not null" json:"root_resource_type"`
	RootResourceID            string                `gorm:"type:uuid;not null" json:"root_resource_id"`
	RootFolderID              string                `gorm:"type:uuid;not null" json:"root_folder_id"`
	RootFolderPath            string                `gorm:"type:ltree;not null" json:"root_folder_path"`
	RequestedBy               string                `gorm:"type:uuid;not null" json:"requested_by"`
	Operation                 LifecycleJobOperation `gorm:"type:varchar(16);not null" json:"operation"`
	Status                    LifecycleJobStatus    `gorm:"type:varchar(16);not null" json:"status"`
	Checkpoint                json.RawMessage       `gorm:"type:jsonb;not null;default:'{}'" json:"checkpoint"`
	Attempts                  int                   `gorm:"not null;default:0" json:"attempts"`
	NextRunAt                 *time.Time            `json:"next_run_at,omitempty"`
	LeaseOwner                *string               `json:"lease_owner,omitempty"`
	LeaseExpiresAt            *time.Time            `json:"lease_expires_at,omitempty"`
	TraceID                   *string               `json:"trace_id,omitempty"`
	FailureCode               *string               `json:"failure_code,omitempty"`
	PreviewTokenHash          []byte                `json:"-"`
	PreviewExpiresAt          *time.Time            `json:"preview_expires_at,omitempty"`
	QueuedAt                  *time.Time            `json:"queued_at,omitempty"`
	StartedAt                 *time.Time            `json:"started_at,omitempty"`
	CompletedAt               *time.Time            `json:"completed_at,omitempty"`
	CreatedAt                 time.Time             `gorm:"not null;default:now()" json:"created_at"`
	UpdatedAt                 time.Time             `gorm:"not null;default:now()" json:"updated_at"`
}

func (LifecycleJob) TableName() string {
	return "asset_lifecycle_jobs"
}

// LifecycleJobWorkerRepository is the worker boundary for the common V8
// engine. FolderDeletionRepository remains the public compatibility contract
// for the V5 preview/confirm/status endpoints while this interface owns
// durable DELETE and RESTORE execution.
type LifecycleJobWorkerRepository interface {
	ClaimNextLifecycleJob(ctx context.Context, workerID string) (*LifecycleJob, error)
	ProcessLifecycleJob(ctx context.Context, jobID, workerID string) error
	FailLifecycleJob(ctx context.Context, jobID, workerID string) error
}

// RecycleBinEntry is an internal candidate root. Access Core applies its own
// object-level read policy before any entry becomes a public GraphQL node. Its
// RootFolderPath is resolved from the source row's current hierarchy, not from
// the lifecycle unit's historical path snapshot.
type RecycleBinEntry struct {
	LifecycleUnitID string                `json:"lifecycle_unit_id"`
	ResourceType    LifecycleResourceType `json:"resource_type"`
	ResourceID      string                `json:"resource_id"`
	DisplayName     string                `json:"display_name"`
	RootFolderPath  string                `json:"root_folder_path"`
	DeletedAt       time.Time             `json:"deleted_at"`
}

// RecycleBinFilter is the trusted keyset tuple passed from Access Core to
// Asset Core. It is not the public GraphQL cursor representation.
type RecycleBinFilter struct {
	Limit            int
	AfterDeletedAt   *time.Time
	AfterLifecycleID *string
}
