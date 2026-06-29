package domain

import (
	"context"
	"time"

	"gorm.io/gorm"
)

// OrganizationRef acts as a shadow reference to Access DB organizations.
type OrganizationRef struct {
	OrgID string `gorm:"type:uuid;primaryKey;column:org_id" json:"org_id"`
}

func (OrganizationRef) TableName() string {
	return "organization_ref"
}

// UserRef acts as a shadow reference to Access DB users.
type UserRef struct {
	UserID string `gorm:"type:uuid;primaryKey;column:user_id" json:"user_id"`
}

func (UserRef) TableName() string {
	return "user_ref"
}

// Folder represents a node in the asset tree, identified by ltree path.
type Folder struct {
	ID          string         `gorm:"type:uuid;primaryKey;default:gen_random_uuid()" json:"id"`
	OrgID       string         `gorm:"type:uuid;not null;index:uq_folders_active_path,unique" json:"org_id"`
	Path        string         `gorm:"type:ltree;not null;index:uq_folders_active_path,unique;index:idx_folders_path_gist,type:gist" json:"path"`
	Name        string         `gorm:"type:varchar(255);not null" json:"name"`
	Description string         `gorm:"type:text" json:"description"`
	CreatedBy   string         `gorm:"type:uuid;not null" json:"created_by"`
	UpdatedBy   *string        `gorm:"type:uuid" json:"updated_by"`
	CreatedAt   time.Time      `gorm:"not null;default:now()" json:"created_at"`
	UpdatedAt   time.Time      `gorm:"not null;default:now()" json:"updated_at"`
	DeletedAt   gorm.DeletedAt `json:"-"`
}

func (Folder) TableName() string {
	return "folders"
}

// MetadataItem holds textual metadata for assets.
type MetadataItem struct {
	ID             string         `gorm:"type:uuid;primaryKey;default:gen_random_uuid()" json:"id"`
	FolderID       string         `gorm:"type:uuid;not null;index" json:"folder_id"`
	Title          string         `gorm:"type:varchar(255);not null" json:"title"`
	Description    string         `gorm:"type:text" json:"description"`
	Labels         string         `gorm:"type:text[];default:'{}'" json:"labels"`
	Category       *string        `gorm:"type:varchar(100)" json:"category"`
	ExternalSource *string        `gorm:"type:varchar(100);uniqueIndex:uq_metadata_items_external_identity" json:"external_source"`
	ExternalID     *string        `gorm:"type:varchar(255);uniqueIndex:uq_metadata_items_external_identity" json:"external_id"`
	SourceURL      *string        `gorm:"type:text" json:"source_url"`
	ThumbnailURL   *string        `gorm:"type:text" json:"thumbnail_url"`
	License        *string        `gorm:"type:varchar(255)" json:"license"`
	Author         *string        `gorm:"type:varchar(255)" json:"author"`
	MetadataJSON   string         `gorm:"type:jsonb;not null;default:'{}'" json:"metadata_json"`
	Notes          *string        `gorm:"type:text" json:"notes"`
	CreatedBy      string         `gorm:"type:uuid;not null" json:"created_by"`
	UpdatedBy      *string        `gorm:"type:uuid" json:"updated_by"`
	CreatedAt      time.Time      `gorm:"not null;default:now()" json:"created_at"`
	UpdatedAt      time.Time      `gorm:"not null;default:now()" json:"updated_at"`
	DeletedAt      gorm.DeletedAt `gorm:"index:idx_metadata_items_active_folder_id" json:"-"`
}

func (MetadataItem) TableName() string {
	return "metadata_items"
}

// AssetRepository defines the contract for database operations related to assets.
type AssetRepository interface {
	GetFolderTree(ctx context.Context, orgID string, rootPath string) ([]Folder, error)
	EnsureRefs(ctx context.Context, userID, orgID string) error
}

// AssetUsecase defines the contract for business logic operations related to assets.
type AssetUsecase interface {
	GetFolderTree(ctx context.Context, orgID string, rootPath string) ([]Folder, error)
	EnsureRefs(ctx context.Context, userID, orgID string) error
}
