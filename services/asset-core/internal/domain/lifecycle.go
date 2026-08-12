package domain

import "time"

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
