package models

import (
	"time"

	"gorm.io/gorm"
)

// OrganizationRef acts as a shadow reference to Access DB organizations.
type OrganizationRef struct {
	OrgID string `gorm:"type:uuid;primaryKey;column:org_id"`
}

func (OrganizationRef) TableName() string {
	return "organization_ref"
}

// UserRef acts as a shadow reference to Access DB users.
type UserRef struct {
	UserID string `gorm:"type:uuid;primaryKey;column:user_id"`
}

func (UserRef) TableName() string {
	return "user_ref"
}

// Folder represents a node in the asset tree, identified by ltree path.
type Folder struct {
	ID          string    `gorm:"type:uuid;primaryKey;default:gen_random_uuid()"`
	OrgID       string    `gorm:"type:uuid;not null;index:uq_folders_active_path,unique"`
	Path        string    `gorm:"type:ltree;not null;index:uq_folders_active_path,unique;index:idx_folders_path_gist,type:gist"`
	Name        string    `gorm:"type:varchar(255);not null"`
	Description string    `gorm:"type:text"`
	CreatedBy   string    `gorm:"type:uuid;not null"`
	UpdatedBy   *string   `gorm:"type:uuid"`
	CreatedAt   time.Time `gorm:"not null;default:now()"`
	UpdatedAt   time.Time `gorm:"not null;default:now()"`
	DeletedAt   gorm.DeletedAt
}

func (Folder) TableName() string {
	return "folders"
}

// MetadataItem holds textual metadata for assets.
type MetadataItem struct {
	ID             string         `gorm:"type:uuid;primaryKey;default:gen_random_uuid()"`
	FolderID       string         `gorm:"type:uuid;not null;index"`
	Title          string         `gorm:"type:varchar(255);not null"`
	Description    string         `gorm:"type:text"`
	Labels         string         `gorm:"type:text[];default:'{}'"` // Note: pq.StringArray could be used with lib/pq
	Category       *string        `gorm:"type:varchar(100)"`
	ExternalSource *string        `gorm:"type:varchar(100);uniqueIndex:uq_metadata_items_external_identity"`
	ExternalID     *string        `gorm:"type:varchar(255);uniqueIndex:uq_metadata_items_external_identity"`
	SourceURL      *string        `gorm:"type:text"`
	ThumbnailURL   *string        `gorm:"type:text"`
	License        *string        `gorm:"type:varchar(255)"`
	Author         *string        `gorm:"type:varchar(255)"`
	MetadataJSON   string         `gorm:"type:jsonb;not null;default:'{}'"`
	Notes          *string        `gorm:"type:text"`
	CreatedBy      string         `gorm:"type:uuid;not null"`
	UpdatedBy      *string        `gorm:"type:uuid"`
	CreatedAt      time.Time      `gorm:"not null;default:now()"`
	UpdatedAt      time.Time      `gorm:"not null;default:now()"`
	DeletedAt      gorm.DeletedAt `gorm:"index:idx_metadata_items_active_folder_id"`
}

func (MetadataItem) TableName() string {
	return "metadata_items"
}
